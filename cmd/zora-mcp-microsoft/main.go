package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhiruo/zora/internal/mcpmicrosoft"
)

func main() {
	// MCP stdio 的 stdout 专用于 JSON-RPC；包括鉴权错误在内的日志只能写 stderr。
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	server, err := mcpmicrosoft.New(mcpmicrosoft.Config{
		AccessToken: os.Getenv("ZORA_MCP_MICROSOFT_ACCESS_TOKEN"),
		BaseURL:     os.Getenv("ZORA_MCP_MICROSOFT_BASE_URL"),
		UserID:      os.Getenv("ZORA_MCP_MICROSOFT_USER_ID"),
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
