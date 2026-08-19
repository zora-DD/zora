// Package security 提供部署边界上的限流、持久化配额、CORS 与 CSRF 防护。
package security

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ResourceRequests   = "requests"
	ResourceChatRuns   = "chat_runs"
	ResourceUploadByte = "knowledge_upload_bytes"
	csrfCookieName     = "zora_csrf"
)

type QuotaUsage struct {
	Resource string    `json:"resource"`
	Used     int64     `json:"used"`
	Limit    int64     `json:"limit"`
	ResetAt  time.Time `json:"reset_at"`
}

type QuotaStore interface {
	// ConsumeAPIQuota 必须原子完成“检查并扣减”，避免并发请求突破上限。
	ConsumeAPIQuota(ctx context.Context, date, principal, resource string, amount, limit int64, now time.Time) (QuotaUsage, bool, error)
}

type Config struct {
	RateLimitEnabled     bool
	RequestsPerSecond    float64
	Burst                int
	DailyRequestQuota    int64
	DailyChatQuota       int64
	DailyUploadByteQuota int64
	CSRFEnabled          bool
	CookieSecure         bool
	AllowedOrigins       []string
	TrustedProxyCIDRs    []string
	PrincipalID          string
}

type Manager struct {
	config         Config
	quota          QuotaStore
	limiter        *ipLimiter
	allowedOrigins map[string]struct{}
	trustedProxies []*net.IPNet
	now            func() time.Time
}

func New(config Config, quota QuotaStore) (*Manager, error) {
	if quota == nil {
		return nil, fmt.Errorf("API 配额存储不能为空")
	}
	if config.RateLimitEnabled && (config.RequestsPerSecond <= 0 || math.IsNaN(config.RequestsPerSecond) || math.IsInf(config.RequestsPerSecond, 0) || config.Burst < 1) {
		return nil, fmt.Errorf("API 限流速率和突发容量必须大于 0")
	}
	if config.DailyRequestQuota < 0 || config.DailyChatQuota < 0 || config.DailyUploadByteQuota < 0 {
		return nil, fmt.Errorf("API 每日配额不能小于 0")
	}
	config.PrincipalID = strings.TrimSpace(config.PrincipalID)
	if config.PrincipalID == "" {
		config.PrincipalID = "local-user"
	}
	manager := &Manager{config: config, quota: quota, allowedOrigins: map[string]struct{}{}, now: time.Now}
	for _, raw := range config.AllowedOrigins {
		origin, err := normalizeOrigin(raw)
		if err != nil {
			return nil, err
		}
		manager.allowedOrigins[origin] = struct{}{}
	}
	for _, raw := range config.TrustedProxyCIDRs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("可信代理 CIDR %q 无效：%w", raw, err)
		}
		manager.trustedProxies = append(manager.trustedProxies, network)
	}
	if config.RateLimitEnabled {
		manager.limiter = newIPLimiter(config.RequestsPerSecond, config.Burst)
	}
	return manager, nil
}

func (m *Manager) CSRFEnabled() bool      { return m != nil && m.config.CSRFEnabled }
func (m *Manager) RateLimitEnabled() bool { return m != nil && m.config.RateLimitEnabled }
func (m *Manager) QuotaEnabled() bool {
	return m != nil && (m.config.DailyRequestQuota > 0 || m.config.DailyChatQuota > 0 || m.config.DailyUploadByteQuota > 0)
}

// IssueCSRFToken 使用双提交 Cookie：令牌只在当前响应正文与 HttpOnly Cookie 中出现，
// Web 前端保存在内存并通过 X-CSRF-Token 回传，不写 localStorage。
func (m *Manager) IssueCSRFToken(w http.ResponseWriter) (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("生成 CSRF Token 失败：%w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: token, Path: "/", HttpOnly: true,
		Secure: m.config.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 60 * 60})
	return token, nil
}

func (m *Manager) Middleware(next http.Handler) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.applyCORS(w, r) {
			return
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/health" || r.URL.Path == "/api/security/csrf" {
			next.ServeHTTP(w, r)
			return
		}
		clientIP := m.clientIP(r)
		if m.limiter != nil {
			allowed, retryAfter := m.limiter.Allow(clientIP, m.now())
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(retryAfter.Seconds())))))
				writeProblem(w, http.StatusTooManyRequests, "请求过于频繁，请稍后重试")
				return
			}
		}
		if m.config.CSRFEnabled && isUnsafe(r.Method) {
			cookie, err := r.Cookie(csrfCookieName)
			header := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
			if err != nil || header == "" || len(cookie.Value) != len(header) || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
				writeProblem(w, http.StatusForbidden, "CSRF 校验失败，请刷新页面后重试")
				return
			}
		}
		principal := m.config.PrincipalID + ":" + clientIP
		if !m.consume(w, r, principal, ResourceRequests, 1, m.config.DailyRequestQuota) {
			return
		}
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/conversations/") && strings.HasSuffix(r.URL.Path, "/messages") {
			if !m.consume(w, r, principal, ResourceChatRuns, 1, m.config.DailyChatQuota) {
				return
			}
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/knowledge/documents" {
			amount := r.ContentLength
			if amount < 0 {
				// 分块上传无法提前获知大小，按接口允许的最大请求体预占配额。
				amount = 6 << 20
			}
			if !m.consume(w, r, principal, ResourceUploadByte, amount, m.config.DailyUploadByteQuota) {
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Manager) consume(w http.ResponseWriter, r *http.Request, principal, resource string, amount, limit int64) bool {
	if limit == 0 || amount <= 0 {
		return true
	}
	now := m.now().UTC()
	usage, allowed, err := m.quota.ConsumeAPIQuota(r.Context(), now.Format("2006-01-02"), principal, resource, amount, limit, now)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "配额服务暂时不可用，请稍后重试")
		return false
	}
	w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(usage.Limit, 10))
	w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(max(0, usage.Limit-usage.Used), 10))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(usage.ResetAt.Unix(), 10))
	if !allowed {
		seconds := max(1, int(usage.ResetAt.Sub(now).Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeProblem(w, http.StatusTooManyRequests, "今日 API 配额已用完，请在配额重置后重试")
		return false
	}
	return true
}

func (m *Manager) applyCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	normalized, err := normalizeOrigin(origin)
	if err != nil {
		writeProblem(w, http.StatusForbidden, "请求来源不受信任")
		return false
	}
	if normalized != m.requestOrigin(r) {
		if _, allowed := m.allowedOrigins[normalized]; !allowed {
			writeProblem(w, http.StatusForbidden, "请求来源不在 CORS 白名单中")
			return false
		}
	}
	w.Header().Set("Access-Control-Allow-Origin", normalized)
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Add("Vary", "Origin")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-CSRF-Token")
		w.Header().Set("Access-Control-Max-Age", "600")
	}
	return true
}

func (m *Manager) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	direct := net.ParseIP(host)
	trusted := m.isTrustedProxy(direct)
	if trusted {
		first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
		if forwarded := net.ParseIP(strings.TrimSpace(first)); forwarded != nil {
			return forwarded.String()
		}
	}
	if direct == nil {
		return "unknown"
	}
	return direct.String()
}

func (m *Manager) isTrustedProxy(ip net.IP) bool {
	for _, network := range m.trustedProxies {
		if ip != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func normalizeOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("CORS Origin %q 无效，必须是完整的 http(s) 源且不能包含路径", raw)
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), nil
}

func (m *Manager) requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host, _, splitErr := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if splitErr != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	// 只接受可信反向代理提供的协议，防止客户端伪造 X-Forwarded-Proto 绕过同源判断。
	if m.isTrustedProxy(net.ParseIP(host)) {
		forwardedProto := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")))
		if forwardedProto == "http" || forwardedProto == "https" {
			scheme = forwardedProto
		}
	}
	return scheme + "://" + strings.ToLower(r.Host)
}

func isUnsafe(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func writeProblem(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

type bucket struct {
	tokens        float64
	updated, seen time.Time
}
type ipLimiter struct {
	mu         sync.Mutex
	rate       float64
	burst      float64
	buckets    map[string]bucket
	operations uint64
}

func newIPLimiter(rate float64, burst int) *ipLimiter {
	return &ipLimiter{rate: rate, burst: float64(burst), buckets: make(map[string]bucket)}
}

func (l *ipLimiter) Allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, exists := l.buckets[key]
	if !exists {
		entry = bucket{tokens: l.burst, updated: now}
	}
	elapsed := now.Sub(entry.updated).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	entry.tokens = min(l.burst, entry.tokens+elapsed*l.rate)
	entry.updated, entry.seen = now, now
	allowed := entry.tokens >= 1
	retry := time.Duration(0)
	if allowed {
		entry.tokens--
	} else {
		retry = time.Duration((1 - entry.tokens) / l.rate * float64(time.Second))
	}
	l.buckets[key] = entry
	l.operations++
	if l.operations%1024 == 0 {
		for item, value := range l.buckets {
			if now.Sub(value.seen) > 10*time.Minute {
				delete(l.buckets, item)
			}
		}
	}
	return allowed, retry
}
