package mcpmicrosoft

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOAuthClientCredentialsCachesAndRefreshesToken(t *testing.T) {
	t.Parallel()
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("test-client-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	var requests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/tenant-1/oauth2/v2.0/token" {
			t.Fatalf("unexpected token request: %s %s", request.Method, request.URL.Path)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if request.Form.Get("grant_type") != "client_credentials" || request.Form.Get("scope") != defaultGraphScope || request.Form.Get("client_secret") != "test-client-secret" {
			t.Fatalf("unexpected OAuth form: %v", request.Form)
		}
		body := fmt.Sprintf(`{"token_type":"Bearer","expires_in":3600,"access_token":"token-%d"}`, requests.Load())
		return testHTTPResponse(request, http.StatusOK, body), nil
	})}
	source, appOnly, err := configuredTokenSource(Config{
		TenantID: "tenant-1", ClientID: "client-1", ClientSecretFile: secretFile,
		OAuthBaseURL: "http://127.0.0.1", HTTPClient: httpClient,
	}, httpClient, func() time.Time { return now })
	if err != nil || !appOnly {
		t.Fatalf("source = %T, appOnly=%v, err=%v", source, appOnly, err)
	}
	first, err := source.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.Token(context.Background())
	if err != nil || second != first || requests.Load() != 1 {
		t.Fatalf("cached token = %q, requests=%d, err=%v", second, requests.Load(), err)
	}
	now = now.Add(59 * time.Minute)
	refreshed, err := source.Token(context.Background())
	if err != nil || refreshed == first || requests.Load() != 2 {
		t.Fatalf("refreshed token = %q, requests=%d, err=%v", refreshed, requests.Load(), err)
	}
}

func TestOAuthClientCredentialsConcurrentRequestsShareToken(t *testing.T) {
	t.Parallel()
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("concurrent-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		return testHTTPResponse(request, http.StatusOK,
			`{"token_type":"Bearer","expires_in":3600,"access_token":"shared-token"}`), nil
	})}
	source, _, err := configuredTokenSource(Config{
		TenantID: "tenant-1", ClientID: "client-1", ClientSecretFile: secretFile,
		OAuthBaseURL: "http://127.0.0.1",
	}, httpClient, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 20
	var waitGroup sync.WaitGroup
	errors := make(chan error, workers)
	for range workers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			token, err := source.Token(context.Background())
			if err != nil {
				errors <- err
				return
			}
			if token != "shared-token" {
				errors <- fmt.Errorf("unexpected token: %q", token)
			}
		}()
	}
	waitGroup.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("concurrent OAuth requests = %d, want 1", requests.Load())
	}
}

func TestMicrosoftAuthenticationModesAreMutuallyExclusive(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	tokenFile := filepath.Join(directory, "access-token")
	secretFile := filepath.Join(directory, "client-secret")
	if err := os.WriteFile(tokenFile, []byte("file-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretFile, []byte("client-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		config Config
	}{
		{name: "直接令牌与令牌文件", config: Config{AccessToken: "direct-token", AccessTokenFile: tokenFile}},
		{name: "令牌文件与 OAuth", config: Config{AccessTokenFile: tokenFile, TenantID: "tenant-1", ClientID: "client-1", ClientSecretFile: secretFile}},
		{name: "直接令牌与 OAuth", config: Config{AccessToken: "direct-token", TenantID: "tenant-1", ClientID: "client-1", ClientSecretFile: secretFile}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := configuredTokenSource(test.config, http.DefaultClient, time.Now)
			if err == nil || !strings.Contains(err.Error(), "只能选择一种") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestOAuthScopeMustUseGraphDefault(t *testing.T) {
	t.Parallel()
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("client-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := configuredTokenSource(Config{
		TenantID: "tenant-1", ClientID: "client-1", ClientSecretFile: secretFile,
		OAuthBaseURL: "http://127.0.0.1", OAuthScope: "https://graph.microsoft.com/Mail.Read",
	}, http.DefaultClient, time.Now)
	if err == nil || !strings.Contains(err.Error(), defaultGraphScope) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGraphUnauthorizedInvalidatesOAuthTokenAndRetriesOnce(t *testing.T) {
	t.Parallel()
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("retry-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	var tokenRequests atomic.Int32
	var graphRequests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Path, "/oauth2/v2.0/token") {
			index := tokenRequests.Add(1)
			return testHTTPResponse(request, http.StatusOK,
				fmt.Sprintf(`{"token_type":"Bearer","expires_in":3600,"access_token":"oauth-token-%d"}`, index)), nil
		}
		index := graphRequests.Add(1)
		if index == 1 {
			if request.Header.Get("Authorization") != "Bearer oauth-token-1" {
				t.Fatalf("first graph authorization = %q", request.Header.Get("Authorization"))
			}
			return testHTTPResponse(request, http.StatusUnauthorized, `{"error":{"code":"InvalidAuthenticationToken","message":"expired"}}`), nil
		}
		if request.Header.Get("Authorization") != "Bearer oauth-token-2" {
			t.Fatalf("retry graph authorization = %q", request.Header.Get("Authorization"))
		}
		return testHTTPResponse(request, http.StatusOK, `{"value":[]}`), nil
	})}
	source, _, err := configuredTokenSource(Config{
		TenantID: "tenant-1", ClientID: "client-1", ClientSecretFile: secretFile,
		OAuthBaseURL: "http://127.0.0.1",
	}, httpClient, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	client := &connector{tokenSource: source, baseURL: "http://127.0.0.1/v1.0", userPath: "/users/test@example.com", httpClient: httpClient, now: time.Now}
	_, _, err = client.searchEmails(context.Background(), nil, searchEmailsInput{})
	if err != nil || tokenRequests.Load() != 2 || graphRequests.Load() != 2 {
		t.Fatalf("token requests=%d graph requests=%d err=%v", tokenRequests.Load(), graphRequests.Load(), err)
	}
}

func TestOAuthErrorDoesNotLeakClientSecret(t *testing.T) {
	t.Parallel()
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("never-leak-client-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return testHTTPResponse(request, http.StatusUnauthorized,
			`{"error":"invalid_client","error_description":"secret never-leak-client-secret is invalid"}`), nil
	})}
	source, _, err := configuredTokenSource(Config{
		TenantID: "tenant-1", ClientID: "client-1", ClientSecretFile: secretFile,
		OAuthBaseURL: "http://127.0.0.1",
	}, httpClient, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid_client") || strings.Contains(err.Error(), "never-leak-client-secret") {
		t.Fatalf("unexpected OAuth error: %v", err)
	}
}

func TestAccessTokenFileSupportsRotation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "access-token")
	if err := os.WriteFile(path, []byte("token-one"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, appOnly, err := configuredTokenSource(Config{AccessTokenFile: path}, http.DefaultClient, time.Now)
	if err != nil || appOnly {
		t.Fatalf("source = %T, appOnly=%v, err=%v", source, appOnly, err)
	}
	first, err := source.Token(context.Background())
	if err != nil || first != "token-one" {
		t.Fatalf("first token = %q, err=%v", first, err)
	}
	if err := os.WriteFile(path, []byte("token-two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := source.Token(context.Background())
	if err != nil || second != "token-two" {
		t.Fatalf("rotated token = %q, err=%v", second, err)
	}
}

func TestOAuthAppIdentityRequiresExplicitUser(t *testing.T) {
	t.Parallel()
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(Config{
		TenantID: "tenant-1", ClientID: "client-1", ClientSecretFile: secretFile,
		OAuthBaseURL: "http://127.0.0.1", BaseURL: "http://127.0.0.1/v1.0", UserID: "me",
	})
	if err == nil || !strings.Contains(err.Error(), "不能访问 /me") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testHTTPResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}
