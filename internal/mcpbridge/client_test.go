package mcpbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhiruo/zora/internal/mcpfiles"
)

func TestConnectServerAdaptsAllowlistedReadOnlyTools(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "会议.txt"), []byte("会议时间：周五 14:00"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := mcpfiles.New(root)
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverCtx, cancelServer := context.WithCancel(context.Background())
	defer cancelServer()
	go func() { _ = server.Run(serverCtx, serverTransport) }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, session, err := connectServer(ctx, ServerConfig{
		Name: "files", AllowedTools: []string{"list_files", "read_text_file"},
	}, Options{ConnectTimeout: time.Second, CallTimeout: time.Second, MaxOutputRunes: 12_000}, clientTransport)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if len(tools) != 2 {
		t.Fatalf("tool count = %d, want 2", len(tools))
	}

	var readTool tool.InvokableTool
	for _, item := range tools {
		info, infoErr := item.Info(ctx)
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info.Name == "mcp_files_read_text_file" {
			readTool = item.(tool.InvokableTool)
		}
	}
	if readTool == nil {
		t.Fatal("adapted read_text_file tool not found")
	}
	result, err := readTool.InvokableRun(ctx, `{"path":"会议.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "会议时间") || !strings.Contains(result, "会议.txt") {
		t.Fatalf("unexpected MCP result: %s", result)
	}
}

func TestConnectServerRejectsMissingAllowlistedTool(t *testing.T) {
	server, err := mcpfiles.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Run(ctx, serverTransport) }()
	_, _, err = connectServer(ctx, ServerConfig{
		Name: "files", AllowedTools: []string{"delete_file"},
	}, Options{ConnectTimeout: time.Second, CallTimeout: time.Second, MaxOutputRunes: 12_000}, clientTransport)
	if err == nil || !strings.Contains(err.Error(), "缺少白名单工具") {
		t.Fatalf("expected missing allowlisted tool error, got %v", err)
	}
}

func TestSelectedEnvironmentDoesNotInheritUnlistedValues(t *testing.T) {
	t.Setenv("ZORA_API_KEY", "secret")
	t.Setenv("ZORA_MCP_FILES_ROOT", "/safe-root")
	values := selectedEnvironment([]string{"ZORA_MCP_FILES_ROOT"})
	if len(values) != 1 || values[0] != "ZORA_MCP_FILES_ROOT=/safe-root" {
		t.Fatalf("unexpected selected environment: %v", values)
	}
}
