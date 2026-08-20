package authn

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/identity"
)

func TestGitHubOAuthCreatesServerSessionWithStableIdentity(t *testing.T) {
	store := &memorySessionStore{values: map[string][]byte{}}
	manager, err := New(Config{
		Enabled: true, ClientID: "client", ClientSecret: "secret",
		RedirectURL:  "https://zora.example.com/api/auth/callback",
		AuthorizeURL: "https://github.test/authorize", TokenURL: "https://github.test/token", UserAPIURL: "https://github.test/user",
		SessionTTL: time.Hour, CookieSecure: true,
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil)), identity.LocalPrincipal("local-user"))
	if err != nil {
		t.Fatal(err)
	}
	// 自定义 Transport 不监听本地端口，沙箱和 CI 都能稳定执行。
	manager.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.Path {
		case "/token":
			if r.Method != http.MethodPost {
				t.Fatalf("token method = %s", r.Method)
			}
			_ = r.ParseForm()
			if r.Form.Get("code_verifier") == "" {
				t.Fatal("缺少 PKCE code_verifier")
			}
			body = `{"access_token":"github-token"}`
		case "/user":
			if r.Header.Get("Authorization") != "Bearer github-token" {
				t.Fatal("未使用 GitHub Bearer Token 获取用户")
			}
			body = `{"id":123456,"login":"zora-user","avatar_url":"https://example.com/avatar.png"}`
		default:
			return response(http.StatusNotFound, ""), nil
		}
		return response(http.StatusOK, body), nil
	})}
	mux := http.NewServeMux()
	manager.Register(mux)
	handler := manager.Middleware(mux)
	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "https://zora.example.com/api/auth/status", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"enabled":true`) {
		t.Fatalf("未登录用户应能读取认证状态：status=%d body=%s", status.Code, status.Body.String())
	}

	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "https://zora.example.com/api/auth/login", nil))
	if login.Code != http.StatusFound {
		t.Fatalf("login status = %d, body=%s", login.Code, login.Body.String())
	}
	authorize, err := url.Parse(login.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := authorize.Query().Get("state")
	if state == "" || authorize.Query().Get("code_challenge_method") != "S256" || authorize.Query().Get("code_challenge") == "" {
		t.Fatalf("OAuth authorize 参数不完整：%s", authorize.RawQuery)
	}
	stateCookie := findCookie(t, login.Result().Cookies(), stateCookieName)

	callbackRequest := httptest.NewRequest(http.MethodGet, "https://zora.example.com/api/auth/callback?code=once&state="+url.QueryEscape(state), nil)
	callbackRequest.AddCookie(stateCookie)
	callback := httptest.NewRecorder()
	handler.ServeHTTP(callback, callbackRequest)
	if callback.Code != http.StatusSeeOther {
		t.Fatalf("callback status = %d, body=%s", callback.Code, callback.Body.String())
	}
	sessionCookie := findCookie(t, callback.Result().Cookies(), sessionCookieName)
	if !sessionCookie.HttpOnly || !sessionCookie.Secure || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("会话 Cookie 安全属性无效：%+v", sessionCookie)
	}

	meRequest := httptest.NewRequest(http.MethodGet, "https://zora.example.com/api/auth/me", nil)
	meRequest.AddCookie(sessionCookie)
	me := httptest.NewRecorder()
	handler.ServeHTTP(me, meRequest)
	if me.Code != http.StatusOK {
		t.Fatalf("me status = %d, body=%s", me.Code, me.Body.String())
	}
	if !strings.Contains(me.Body.String(), `"id":"github:123456"`) || !strings.Contains(me.Body.String(), `"tenant_id":"github-user:123456"`) {
		t.Fatalf("服务端会话身份错误：%s", me.Body.String())
	}

	// OAuth state 采用一次性 Take，重复回调必须失败。
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, callbackRequest.Clone(callbackRequest.Context()))
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("重放 callback status = %d", replay.Code)
	}
}

func TestGitHubOAuthLoginCanonicalizesCallbackHost(t *testing.T) {
	store := &memorySessionStore{values: map[string][]byte{}}
	manager, err := New(Config{
		Enabled: true, ClientID: "client", ClientSecret: "secret",
		RedirectURL: "http://localhost:8088/api/auth/callback",
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil)), identity.LocalPrincipal("local-user"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	manager.Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"http://127.0.0.1:8088/api/auth/login?return_to=%2Fconversations%2F123", nil))

	if response.Code != http.StatusFound {
		t.Fatalf("login status = %d, body=%s", response.Code, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "http://localhost:8088/api/auth/login?return_to=%2Fconversations%2F123" {
		t.Fatalf("canonical login location = %q", location)
	}
	if len(response.Result().Cookies()) != 0 || len(store.values) != 0 {
		t.Fatal("切换到标准主机前不应创建 OAuth state 或 Cookie")
	}
}

func TestSessionStoreOutageReturnsServiceUnavailable(t *testing.T) {
	manager, err := New(Config{
		Enabled: true, ClientID: "client", ClientSecret: "secret",
		RedirectURL: "https://zora.example.com/api/auth/callback", SessionTTL: time.Hour,
	}, failingSessionStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)), identity.LocalPrincipal("local-user"))
	if err != nil {
		t.Fatal(err)
	}
	handler := manager.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("Redis 故障时不应进入业务 Handler")
	}))
	request := httptest.NewRequest(http.MethodGet, "https://zora.example.com/api/info", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "登录会话服务暂时不可用") {
		t.Fatalf("store outage = %d, %s", response.Code, response.Body.String())
	}
}

func TestSafeReturnToOnlyAllowsLocalPath(t *testing.T) {
	tests := map[string]string{
		"/conversations/123?tab=run": "/conversations/123?tab=run",
		"https://evil.example":       "/",
		"//evil.example":             "/",
		`/\evil.example`:             "/",
		"":                           "/",
	}
	for input, expected := range tests {
		if actual := safeReturnTo(input); actual != expected {
			t.Errorf("safeReturnTo(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type memorySessionStore struct {
	mu     sync.Mutex
	values map[string][]byte
}

type failingSessionStore struct{}

func (failingSessionStore) Put(context.Context, string, []byte, time.Duration) error {
	return errors.New("redis unavailable")
}
func (failingSessionStore) Take(context.Context, string) ([]byte, error) {
	return nil, errors.New("redis unavailable")
}
func (failingSessionStore) Get(context.Context, string) ([]byte, error) {
	return nil, errors.New("redis unavailable")
}
func (failingSessionStore) Refresh(context.Context, string, time.Duration) error {
	return errors.New("redis unavailable")
}
func (failingSessionStore) Delete(context.Context, string) error { return nil }

func (s *memorySessionStore) Put(_ context.Context, key string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = append([]byte(nil), value...)
	return nil
}
func (s *memorySessionStore) Take(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	delete(s.values, key)
	if !ok {
		return nil, errSessionNotFound
	}
	return append([]byte(nil), value...), nil
}
func (s *memorySessionStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	if !ok {
		return nil, errSessionNotFound
	}
	return append([]byte(nil), value...), nil
}
func (s *memorySessionStore) Refresh(context.Context, string, time.Duration) error { return nil }
func (s *memorySessionStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

func findCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("未找到 Cookie %s", name)
	return nil
}
