// Package identity 定义经过认证的调用主体，并通过 context 在 HTTP、业务和存储层之间传递。
package identity

import (
	"context"
	"errors"
	"strings"
)

var ErrUnauthenticated = errors.New("请先登录后再继续")

// Principal 是服务端从可信登录态解析出的身份，禁止从业务请求 JSON 中直接构造。
type Principal struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenant_id"`
	Provider  string `json:"provider"`
	Subject   string `json:"subject"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

func (p Principal) Valid() bool {
	return strings.TrimSpace(p.ID) != "" && strings.TrimSpace(p.TenantID) != ""
}

type contextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, principal)
}

func FromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(contextKey{}).(Principal)
	return principal, ok && principal.Valid()
}

func Require(ctx context.Context) (Principal, error) {
	principal, ok := FromContext(ctx)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	return principal, nil
}

// ScopeOrLocal 供存储实现兼容无认证的本地测试；生产配置会在启动时强制开启认证，
// 因此线上请求不会走到 local 回退分支。
func ScopeOrLocal(ctx context.Context) Principal {
	if principal, ok := FromContext(ctx); ok {
		return principal
	}
	return LocalPrincipal("local-user")
}

// LocalPrincipal 只用于未启用外部认证的本地开发模式，保持原有零依赖启动体验。
func LocalPrincipal(principalID string) Principal {
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		principalID = "local-user"
	}
	return Principal{
		ID: principalID, TenantID: "local", Provider: "local",
		Subject: principalID, Username: principalID,
	}
}
