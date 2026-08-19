// Package authn 实现 GitHub OAuth 登录与 Redis 服务端会话。
package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zhiruo/zora/internal/identity"
)

const (
	sessionCookieName = "zora_session"
	stateCookieName   = "zora_oauth_state"
)

var errSessionNotFound = errors.New("登录会话不存在")

type Config struct {
	Enabled        bool
	ClientID       string
	ClientSecret   string
	RedirectURL    string
	AuthorizeURL   string
	TokenURL       string
	UserAPIURL     string
	SessionTTL     time.Duration
	CookieSecure   bool
	CookieDomain   string
	RedisKeyPrefix string
}

type Manager struct {
	config Config
	store  SessionStore
	client *http.Client
	logger *slog.Logger
	local  identity.Principal
}

// SessionStore 保留 OAuth 临时状态和登录会话所需的最小原子能力。
// Take 必须采用 GETDEL 语义，防止授权码回调被重放。
type SessionStore interface {
	Put(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Take(ctx context.Context, key string) ([]byte, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Refresh(ctx context.Context, key string, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

type oauthState struct {
	Verifier string `json:"verifier"`
	ReturnTo string `json:"return_to"`
}

func New(config Config, store SessionStore, logger *slog.Logger, local identity.Principal) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if config.SessionTTL <= 0 {
		config.SessionTTL = 24 * time.Hour
	}
	if config.AuthorizeURL == "" {
		config.AuthorizeURL = "https://github.com/login/oauth/authorize"
	}
	if config.TokenURL == "" {
		config.TokenURL = "https://github.com/login/oauth/access_token"
	}
	if config.UserAPIURL == "" {
		config.UserAPIURL = "https://api.github.com/user"
	}
	if config.RedisKeyPrefix == "" {
		config.RedisKeyPrefix = "zora:auth:"
	}
	if config.Enabled {
		if strings.TrimSpace(config.ClientID) == "" || strings.TrimSpace(config.ClientSecret) == "" || strings.TrimSpace(config.RedirectURL) == "" {
			return nil, fmt.Errorf("启用 GitHub 登录时必须配置 Client ID、Client Secret 和回调地址")
		}
		if store == nil {
			return nil, fmt.Errorf("启用 GitHub 登录时必须配置 Redis，以共享多副本会话")
		}
		redirect, err := url.ParseRequestURI(config.RedirectURL)
		if err != nil || redirect.Scheme == "" || redirect.Host == "" {
			return nil, fmt.Errorf("GitHub OAuth 回调地址必须是包含协议和主机的绝对地址")
		}
	}
	return &Manager{config: config, store: store, client: &http.Client{Timeout: 15 * time.Second}, logger: logger, local: local}, nil
}

func (m *Manager) Enabled() bool { return m != nil && m.config.Enabled }

func (m *Manager) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/status", m.status)
	mux.HandleFunc("GET /api/auth/login", m.login)
	mux.HandleFunc("GET /api/auth/callback", m.callback)
	mux.HandleFunc("GET /api/auth/me", m.me)
	mux.HandleFunc("POST /api/auth/logout", m.logout)
}

// Middleware 只信任 Redis 中的随机会话 ID；客户端不能通过 Header 伪造 principal 或 tenant。
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.Enabled() {
			next.ServeHTTP(w, r.WithContext(identity.WithPrincipal(r.Context(), m.local)))
			return
		}
		if isPublic(r) {
			next.ServeHTTP(w, r)
			return
		}
		principal, sessionID, err := m.authenticate(r.Context(), r)
		if err != nil {
			if !errors.Is(err, errSessionNotFound) && !errors.Is(err, identity.ErrUnauthenticated) {
				m.logger.Error("读取登录会话失败", "错误", err)
				writeProblem(w, http.StatusServiceUnavailable, "登录会话服务暂时不可用，请稍后重试")
				return
			}
			writeProblem(w, http.StatusUnauthorized, "登录状态已失效，请使用 GitHub 重新登录")
			return
		}
		// 滑动续期让活跃用户无需频繁登录；会话仍受固定配置 TTL 约束。
		if err := m.store.Refresh(r.Context(), m.sessionKey(sessionID), m.config.SessionTTL); err != nil {
			m.logger.Warn("续期登录会话失败", "错误", err)
		}
		next.ServeHTTP(w, r.WithContext(identity.WithPrincipal(r.Context(), principal)))
	})
}

func isPublic(r *http.Request) bool {
	if r.Method == http.MethodOptions || r.URL.Path == "/api/health" || r.URL.Path == "/api/ready" || r.URL.Path == "/api/auth/status" || r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/callback" {
		return true
	}
	// 前端静态资源必须公开，未登录时由 Web 页面展示登录入口。
	return !strings.HasPrefix(r.URL.Path, "/api/")
}

// status 是前端启动时唯一需要公开读取的认证信息，不返回 Client ID、回调地址或用户数据。
func (m *Manager) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": m.Enabled()})
}

func (m *Manager) login(w http.ResponseWriter, r *http.Request) {
	if !m.Enabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	state, err := randomToken(32)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "无法创建登录请求")
		return
	}
	verifier, err := randomToken(48)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "无法创建登录请求")
		return
	}
	payload, _ := json.Marshal(oauthState{Verifier: verifier, ReturnTo: safeReturnTo(r.URL.Query().Get("return_to"))})
	if err := m.store.Put(r.Context(), m.stateKey(state), payload, 10*time.Minute); err != nil {
		m.logger.Error("保存 OAuth 临时状态失败", "错误", err)
		writeProblem(w, http.StatusServiceUnavailable, "登录服务暂时不可用，请稍后重试")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookieName, Value: state, Path: "/api/auth/callback", HttpOnly: true,
		Secure: m.config.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id":             {m.config.ClientID},
		"redirect_uri":          {m.config.RedirectURL},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, m.config.AuthorizeURL+"?"+query.Encode(), http.StatusFound)
}

func (m *Manager) callback(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	cookie, err := r.Cookie(stateCookieName)
	if err != nil || state == "" || code == "" || len(cookie.Value) != len(state) || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		writeProblem(w, http.StatusBadRequest, "GitHub 登录回调校验失败，请重新登录")
		return
	}
	payload, err := m.store.Take(r.Context(), m.stateKey(state))
	if err != nil {
		if !errors.Is(err, errSessionNotFound) {
			m.logger.Error("读取 OAuth 临时状态失败", "错误", err)
			writeProblem(w, http.StatusServiceUnavailable, "登录服务暂时不可用，请稍后重试")
			return
		}
		writeProblem(w, http.StatusBadRequest, "登录请求已过期或已被使用，请重新登录")
		return
	}
	var pending oauthState
	if json.Unmarshal(payload, &pending) != nil || pending.Verifier == "" {
		writeProblem(w, http.StatusBadRequest, "登录请求状态无效，请重新登录")
		return
	}
	token, err := m.exchange(r.Context(), code, pending.Verifier)
	if err != nil {
		m.logger.Warn("交换 GitHub OAuth Token 失败", "错误", err)
		writeProblem(w, http.StatusBadGateway, "GitHub 登录失败，请稍后重试")
		return
	}
	principal, err := m.fetchPrincipal(r.Context(), token)
	if err != nil {
		m.logger.Warn("读取 GitHub 用户身份失败", "错误", err)
		writeProblem(w, http.StatusBadGateway, "无法读取 GitHub 用户信息，请稍后重试")
		return
	}
	sessionID, err := randomToken(32)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "无法创建登录会话")
		return
	}
	encoded, _ := json.Marshal(principal)
	if err := m.store.Put(r.Context(), m.sessionKey(sessionID), encoded, m.config.SessionTTL); err != nil {
		m.logger.Error("保存登录会话失败", "错误", err)
		writeProblem(w, http.StatusServiceUnavailable, "登录会话服务暂时不可用")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: sessionID, Path: "/", Domain: m.config.CookieDomain,
		HttpOnly: true, Secure: m.config.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: int(m.config.SessionTTL.Seconds())})
	http.SetCookie(w, &http.Cookie{Name: stateCookieName, Value: "", Path: "/api/auth/callback", HttpOnly: true,
		Secure: m.config.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	http.Redirect(w, r, pending.ReturnTo, http.StatusSeeOther)
}

func (m *Manager) me(w http.ResponseWriter, r *http.Request) {
	principal, ok := identity.FromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "尚未登录")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "principal": principal})
}

func (m *Manager) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && m.store != nil {
		_ = m.store.Delete(r.Context(), m.sessionKey(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", Domain: m.config.CookieDomain,
		HttpOnly: true, Secure: m.config.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"logged_out": true})
}

func (m *Manager) authenticate(ctx context.Context, r *http.Request) (identity.Principal, string, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return identity.Principal{}, "", identity.ErrUnauthenticated
	}
	encoded, err := m.store.Get(ctx, m.sessionKey(cookie.Value))
	if err != nil {
		return identity.Principal{}, "", err
	}
	var principal identity.Principal
	if json.Unmarshal(encoded, &principal) != nil || !principal.Valid() {
		return identity.Principal{}, "", identity.ErrUnauthenticated
	}
	return principal, cookie.Value, nil
}

func (m *Manager) exchange(ctx context.Context, code, verifier string) (string, error) {
	form := url.Values{"client_id": {m.config.ClientID}, "client_secret": {m.config.ClientSecret}, "code": {code},
		"redirect_uri": {m.config.RedirectURL}, "code_verifier": {verifier}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, m.config.TokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("GitHub Token 接口返回 HTTP %d", resp.StatusCode)
	}
	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.AccessToken == "" {
		return "", fmt.Errorf("GitHub Token 响应无效：%s", result.Error)
	}
	return result.AccessToken, nil
}

func (m *Manager) fetchPrincipal(ctx context.Context, token string) (identity.Principal, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, m.config.UserAPIURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := m.client.Do(req)
	if err != nil {
		return identity.Principal{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return identity.Principal{}, fmt.Errorf("GitHub 用户接口返回 HTTP %d", resp.StatusCode)
	}
	var user struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&user); err != nil || user.ID <= 0 || user.Login == "" {
		return identity.Principal{}, errors.New("GitHub 用户响应缺少稳定 ID 或登录名")
	}
	subject := strconv.FormatInt(user.ID, 10)
	return identity.Principal{ID: "github:" + subject, TenantID: "github-user:" + subject,
		Provider: "github", Subject: subject, Username: user.Login, AvatarURL: user.AvatarURL}, nil
}

func (m *Manager) sessionKey(id string) string { return m.config.RedisKeyPrefix + "session:" + id }
func (m *Manager) stateKey(id string) string   { return m.config.RedisKeyPrefix + "state:" + id }

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func safeReturnTo(value string) string {
	value = strings.TrimSpace(value)
	// 浏览器可能把反斜线规范化为斜线；同时拒绝它，避免 /\\evil.example 被解释为站外跳转。
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
		return "/"
	}
	return value
}

func writeProblem(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
