package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhiruo/zora/internal/mcpfiles"
)

func main() {
	// MCP stdio 的 stdout 专用于 JSON-RPC；日志必须写到 stderr，不能污染协议数据。
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	server, err := mcpfiles.New(os.Getenv("ZORA_MCP_FILES_ROOT"))
	if err != nil {
		logger.Error("启动文件 MCP Server 失败", "error", err)
		os.Exit(1)
	}
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		logger.Error("文件 MCP Server 已停止", "error", err)
		os.Exit(1)
	}
}
