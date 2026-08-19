package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryQuotaStore struct {
	mu   sync.Mutex
	used map[string]int64
}

func (s *memoryQuotaStore) ConsumeAPIQuota(_ context.Context, date, principal, resource string, amount, limit int64, now time.Time) (QuotaUsage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used == nil {
		s.used = map[string]int64{}
	}
	key := date + ":" + principal + ":" + resource
	allowed := s.used[key]+amount <= limit
	if allowed {
		s.used[key] += amount
	}
	reset := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day()+1, 0, 0, 0, 0, time.UTC)
	return QuotaUsage{Resource: resource, Used: s.used[key], Limit: limit, ResetAt: reset}, allowed, nil
}

func TestCSRFMiddlewareRequiresCookieAndHeader(t *testing.T) {
	manager, err := New(Config{CSRFEnabled: true, PrincipalID: "test"}, &memoryQuotaStore{})
	if err != nil {
		t.Fatal(err)
	}
	tokenRecorder := httptest.NewRecorder()
	token, err := manager.IssueCSRFToken(tokenRecorder)
	if err != nil {
		t.Fatal(err)
	}
	cookie := tokenRecorder.Result().Cookies()[0]
	handler := manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	missing := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader("{}"))
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missing)
	if missingResponse.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", missingResponse.Code)
	}

	valid := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader("{}"))
	valid.AddCookie(cookie)
	valid.Header.Set("X-CSRF-Token", token)
	validResponse := httptest.NewRecorder()
	handler.ServeHTTP(validResponse, valid)
	if validResponse.Code != http.StatusNoContent {
		t.Fatalf("valid CSRF status = %d, body=%s", validResponse.Code, validResponse.Body.String())
	}
}

func TestCORSAcceptsExactAllowlistAndRejectsOthers(t *testing.T) {
	manager, err := New(Config{AllowedOrigins: []string{"https://app.example.com"}, PrincipalID: "test"}, &memoryQuotaStore{})
	if err != nil {
		t.Fatal(err)
	}
	handler := manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	allowed := httptest.NewRequest(http.MethodOptions, "/api/conversations", nil)
	allowed.Header.Set("Origin", "https://app.example.com")
	allowedResponse := httptest.NewRecorder()
	handler.ServeHTTP(allowedResponse, allowed)
	if allowedResponse.Code != http.StatusNoContent || allowedResponse.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("allowed CORS status=%d header=%q", allowedResponse.Code, allowedResponse.Header().Get("Access-Control-Allow-Origin"))
	}

	denied := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
	denied.Header.Set("Origin", "https://evil.example")
	deniedResponse := httptest.NewRecorder()
	handler.ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden {
		t.Fatalf("denied CORS status=%d", deniedResponse.Code)
	}
}

func TestCORSTrustsForwardedProtocolOnlyFromConfiguredProxy(t *testing.T) {
	manager, err := New(Config{TrustedProxyCIDRs: []string{"10.20.0.0/24"}, PrincipalID: "test"}, &memoryQuotaStore{})
	if err != nil {
		t.Fatal(err)
	}
	handler := manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	trusted := httptest.NewRequest(http.MethodGet, "http://zora.example.com/api/info", nil)
	trusted.Host = "zora.example.com"
	trusted.RemoteAddr = "10.20.0.8:12345"
	trusted.Header.Set("Origin", "https://zora.example.com")
	trusted.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, trusted)
	if response.Code != http.StatusOK {
		t.Fatalf("可信代理的 HTTPS 同源请求应通过：status=%d body=%s", response.Code, response.Body.String())
	}
	if !manager.SecureRequest(trusted) {
		t.Fatal("可信代理提供的 HTTPS 协议应被识别为安全请求")
	}

	untrusted := trusted.Clone(trusted.Context())
	untrusted.RemoteAddr = "203.0.113.10:12345"
	untrustedResponse := httptest.NewRecorder()
	handler.ServeHTTP(untrustedResponse, untrusted)
	if untrustedResponse.Code != http.StatusForbidden {
		t.Fatalf("非可信来源不能用 X-Forwarded-Proto 伪造 HTTPS 同源：status=%d", untrustedResponse.Code)
	}
	if manager.SecureRequest(untrusted) {
		t.Fatal("非可信来源不能用 X-Forwarded-Proto 伪造安全请求")
	}
}

func TestRateLimitAndDailyQuota(t *testing.T) {
	manager, err := New(Config{RateLimitEnabled: true, RequestsPerSecond: 1, Burst: 1, DailyRequestQuota: 1, PrincipalID: "test"}, &memoryQuotaStore{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	handler := manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/info", nil))
	if first.Code != http.StatusOK || first.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("first status=%d remaining=%q", first.Code, first.Header().Get("X-RateLimit-Remaining"))
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/info", nil))
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("second status=%d retry-after=%q", second.Code, second.Header().Get("Retry-After"))
	}

	// 补充令牌后仍会命中持久化的日配额，而不是进程内限流。
	now = now.Add(2 * time.Second)
	third := httptest.NewRecorder()
	handler.ServeHTTP(third, httptest.NewRequest(http.MethodGet, "/api/info", nil))
	if third.Code != http.StatusTooManyRequests || !strings.Contains(third.Body.String(), "今日 API 配额") {
		t.Fatalf("third status=%d body=%s", third.Code, third.Body.String())
	}
}

func TestRateLimitCoversOAuthEndpoints(t *testing.T) {
	manager, err := New(Config{RateLimitEnabled: true, RequestsPerSecond: 1, Burst: 1, PrincipalID: "anonymous"}, &memoryQuotaStore{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	handler := manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusFound) }))
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))
	if first.Code != http.StatusFound || second.Code != http.StatusTooManyRequests {
		t.Fatalf("OAuth 登录端点应受限流保护：first=%d second=%d", first.Code, second.Code)
	}
}

func TestUntrustedClientCannotSpoofForwardedIP(t *testing.T) {
	manager, err := New(Config{RateLimitEnabled: true, RequestsPerSecond: 1, Burst: 1, PrincipalID: "test"}, &memoryQuotaStore{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	handler := manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	firstRequest := httptest.NewRequest(http.MethodGet, "/api/info", nil)
	firstRequest.RemoteAddr = "203.0.113.10:1000"
	firstRequest.Header.Set("X-Forwarded-For", "198.51.100.1")
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, firstRequest)

	secondRequest := httptest.NewRequest(http.MethodGet, "/api/info", nil)
	secondRequest.RemoteAddr = "203.0.113.10:1001"
	secondRequest.Header.Set("X-Forwarded-For", "198.51.100.2")
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, secondRequest)
	if first.Code != http.StatusOK || second.Code != http.StatusTooManyRequests {
		t.Fatalf("forwarded spoof should not create a new bucket: first=%d second=%d", first.Code, second.Code)
	}
}
