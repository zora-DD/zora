package mcpmicrosoft

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultOAuthBaseURL = "https://login.microsoftonline.com"
	defaultGraphScope   = "https://graph.microsoft.com/.default"
	maxSecretBytes      = 64 << 10
	maxOAuthResponse    = 1 << 20
)

// TokenSource 把访问令牌的来源隔离在 Microsoft MCP 子进程内。
// 主 Zora 进程只传递 Secret 文件路径和非敏感 OAuth 配置，不读取凭据内容。
type TokenSource interface {
	Token(context.Context) (string, error)
}

type invalidatingTokenSource interface {
	TokenSource
	Invalidate(string)
}

type staticTokenSource struct{ token string }

func (source staticTokenSource) Token(context.Context) (string, error) { return source.token, nil }

type fileTokenSource struct{ path string }

func (source fileTokenSource) Token(context.Context) (string, error) {
	return readSecretFile(source.path, "Microsoft Graph 访问令牌")
}

// 文件内容可能由 Secret Volume 原子轮换，因此 401 后允许立即重读一次。
func (fileTokenSource) Invalidate(string) {}

type oauthClientCredentialsSource struct {
	tenantID         string
	clientID         string
	clientSecretFile string
	scope            string
	tokenEndpoint    string
	httpClient       *http.Client
	now              func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func (source *oauthClientCredentialsSource) Token(ctx context.Context) (string, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	now := source.now()
	if source.token != "" && now.Before(source.expiresAt) {
		return source.token, nil
	}
	secret, err := readSecretFile(source.clientSecretFile, "Microsoft OAuth 客户端 Secret")
	if err != nil {
		return "", err
	}
	form := url.Values{
		"client_id":     {source.clientID},
		"client_secret": {secret},
		"scope":         {source.scope},
		"grant_type":    {"client_credentials"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, source.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("创建 Microsoft OAuth 令牌请求失败：%w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := source.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("请求 Microsoft OAuth 令牌失败：%w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxOAuthResponse+1))
	if err != nil {
		return "", fmt.Errorf("读取 Microsoft OAuth 响应失败：%w", err)
	}
	if len(body) > maxOAuthResponse {
		return "", fmt.Errorf("Microsoft OAuth 响应超过 1 MiB 上限")
	}
	if response.StatusCode != http.StatusOK {
		return "", oauthStatusError(response.StatusCode, body, secret)
	}
	var payload struct {
		TokenType   string `json:"token_type"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("解析 Microsoft OAuth 响应失败：%w", err)
	}
	payload.AccessToken = strings.TrimSpace(payload.AccessToken)
	if !strings.EqualFold(strings.TrimSpace(payload.TokenType), "Bearer") || payload.AccessToken == "" || payload.ExpiresIn < 60 {
		return "", fmt.Errorf("Microsoft OAuth 返回了无效的访问令牌或过期时间")
	}
	// 提前 10%（最多 2 分钟）刷新，避免令牌在一次 Graph 请求途中到期。
	lifetime := time.Duration(payload.ExpiresIn) * time.Second
	skew := lifetime / 10
	if skew > 2*time.Minute {
		skew = 2 * time.Minute
	}
	source.token = payload.AccessToken
	source.expiresAt = now.Add(lifetime - skew)
	return source.token, nil
}

func (source *oauthClientCredentialsSource) Invalidate(token string) {
	source.mu.Lock()
	defer source.mu.Unlock()
	if token == "" || source.token == token {
		source.token = ""
		source.expiresAt = time.Time{}
	}
}

func configuredTokenSource(config Config, httpClient *http.Client, now func() time.Time) (TokenSource, bool, error) {
	if config.TokenSource != nil {
		return config.TokenSource, false, nil
	}
	staticToken := strings.TrimSpace(config.AccessToken)
	tokenFile := strings.TrimSpace(config.AccessTokenFile)
	oauthConfigured := strings.TrimSpace(config.TenantID) != "" || strings.TrimSpace(config.ClientID) != "" || strings.TrimSpace(config.ClientSecretFile) != ""
	modes := 0
	if staticToken != "" {
		modes++
	}
	if tokenFile != "" {
		modes++
	}
	if oauthConfigured {
		modes++
	}
	if modes == 0 {
		return nil, false, fmt.Errorf("Microsoft Graph MCP Server 必须配置访问令牌、访问令牌文件或 OAuth 客户端凭据")
	}
	if modes > 1 {
		return nil, false, fmt.Errorf("Microsoft Graph 鉴权方式只能选择一种，不能混用令牌、令牌文件和 OAuth 客户端凭据")
	}
	if staticToken != "" {
		return staticTokenSource{token: staticToken}, false, nil
	}
	if tokenFile != "" {
		if _, err := readSecretFile(tokenFile, "Microsoft Graph 访问令牌"); err != nil {
			return nil, false, err
		}
		return fileTokenSource{path: tokenFile}, false, nil
	}
	tenantID := strings.TrimSpace(config.TenantID)
	clientID := strings.TrimSpace(config.ClientID)
	secretFile := strings.TrimSpace(config.ClientSecretFile)
	if tenantID == "" || clientID == "" || secretFile == "" {
		return nil, false, fmt.Errorf("OAuth client_credentials 必须同时配置 tenant_id、client_id 和 client_secret_file")
	}
	if strings.ContainsAny(tenantID, "/?#\\\r\n\t ") || len(tenantID) > 255 {
		return nil, false, fmt.Errorf("Microsoft OAuth tenant_id 非法")
	}
	if _, err := readSecretFile(secretFile, "Microsoft OAuth 客户端 Secret"); err != nil {
		return nil, false, err
	}
	oauthBaseURL := strings.TrimRight(strings.TrimSpace(config.OAuthBaseURL), "/")
	if oauthBaseURL == "" {
		oauthBaseURL = defaultOAuthBaseURL
	}
	if err := validateSecureBaseURL(oauthBaseURL, "Microsoft OAuth BaseURL"); err != nil {
		return nil, false, err
	}
	scope := strings.TrimSpace(config.OAuthScope)
	if scope == "" {
		scope = defaultGraphScope
	}
	if scope != defaultGraphScope {
		return nil, false, fmt.Errorf("Microsoft Graph 应用身份 scope 必须固定为 %s", defaultGraphScope)
	}
	return &oauthClientCredentialsSource{
		tenantID: tenantID, clientID: clientID, clientSecretFile: secretFile, scope: scope,
		tokenEndpoint: oauthBaseURL + "/" + url.PathEscape(tenantID) + "/oauth2/v2.0/token",
		httpClient:    httpClient, now: now,
	}, true, nil
}

func readSecretFile(path, label string) (string, error) {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return "", fmt.Errorf("读取%s文件失败：%w", label, err)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxSecretBytes+1))
	if err != nil {
		return "", fmt.Errorf("读取%s文件失败：%w", label, err)
	}
	if len(contents) > maxSecretBytes {
		return "", fmt.Errorf("%s文件超过 64 KiB 上限", label)
	}
	value := strings.TrimSpace(string(contents))
	if value == "" {
		return "", fmt.Errorf("%s文件不能为空", label)
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("%s文件只能包含一行", label)
	}
	return value, nil
}

func oauthStatusError(statusCode int, body []byte, secret string) error {
	var payload struct {
		Code        string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &payload)
	message := strings.TrimSpace(payload.Description)
	if message == "" {
		message = http.StatusText(statusCode)
	}
	if len([]rune(message)) > 300 {
		message = string([]rune(message)[:300])
	}
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[凭据已隐藏]")
	}
	if code := strings.TrimSpace(payload.Code); code != "" {
		return fmt.Errorf("Microsoft OAuth 返回 HTTP %d（%s）：%s", statusCode, code, message)
	}
	return fmt.Errorf("Microsoft OAuth 返回 HTTP %d：%s", statusCode, message)
}

func validateSecureBaseURL(value, label string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()))) {
		return fmt.Errorf("%s 必须是 HTTPS 地址；测试时仅允许本机 HTTP", label)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s 不能包含凭据、查询参数或片段", label)
	}
	return nil
}
