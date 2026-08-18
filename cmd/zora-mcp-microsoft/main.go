package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhiruo/zora/internal/mcpmicrosoft"
)

func main() {
	// MCP stdio 的 stdout 专用于 JSON-RPC；包括鉴权错误在内的日志只能写 stderr。
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	prefix := "ZORA_MCP_MICROSOFT_"
	// 专用执行器使用独立前缀和独立 Entra 应用，避免只读 Agent 连接器继承写权限。
	if _, configured := os.LookupEnv("ZORA_OFFICE_MICROSOFT_WRITE_ENABLED"); configured {
		prefix = "ZORA_OFFICE_MICROSOFT_"
	}
	value := func(name string) string { return os.Getenv(prefix + name) }
	writeEnabled := false
	if raw := strings.TrimSpace(value("WRITE_ENABLED")); raw != "" {
		var err error
		writeEnabled, err = strconv.ParseBool(raw)
		if err != nil {
			logger.Error(prefix + "WRITE_ENABLED 必须是 true 或 false")
			os.Exit(1)
		}
	}
	server, err := mcpmicrosoft.New(mcpmicrosoft.Config{
		AccessToken:      value("ACCESS_TOKEN"),
		AccessTokenFile:  value("ACCESS_TOKEN_FILE"),
		TenantID:         value("TENANT_ID"),
		ClientID:         value("CLIENT_ID"),
		ClientSecretFile: value("CLIENT_SECRET_FILE"),
		OAuthBaseURL:     value("OAUTH_BASE_URL"),
		OAuthScope:       value("OAUTH_SCOPE"),
		BaseURL:          value("BASE_URL"),
		UserID:           value("USER_ID"),
		WriteEnabled:     writeEnabled,
	})
	if err != nil {
		logger.Error("启动 Microsoft Graph MCP Server 失败", "error", err)
		os.Exit(1)
	}
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		logger.Error("Microsoft Graph MCP Server 已停止", "error", err)
		os.Exit(1)
	}
}
