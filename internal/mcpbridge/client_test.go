package mcpbridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhiruo/zora/internal/mcpfiles"
	"github.com/zhiruo/zora/internal/mcpmicrosoft"
	"github.com/zhiruo/zora/internal/office"
)

type officeRoundTripFunc func(*http.Request) (*http.Response, error)

func (function officeRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

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

func TestOfficeExecutorReusesEmailCheckpointAcrossRetries(t *testing.T) {
	createCalls, stateCalls, sendCalls := 0, 0, 0
	checkpointSaved := false
	httpClient := &http.Client{Transport: officeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, `{}`
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1.0/me/messages":
			createCalls++
			status, body = http.StatusCreated, `{"id":"immutable-mail-1"}`
		case request.Method == http.MethodGet && request.URL.Path == "/v1.0/me/messages/immutable-mail-1":
			stateCalls++
			isDraft := stateCalls == 1
			encoded, _ := json.Marshal(map[string]any{"id": "immutable-mail-1", "isDraft": isDraft})
			body = string(encoded)
		case request.Method == http.MethodPost && request.URL.Path == "/v1.0/me/messages/immutable-mail-1/send":
			if !checkpointSaved {
				t.Fatal("send was called before the remote draft checkpoint was saved")
			}
			sendCalls++
			if sendCalls == 1 {
				status, body = http.StatusServiceUnavailable, `{"error":{"code":"ServiceUnavailable","message":"请稍后重试"}}`
			} else {
				status, body = http.StatusAccepted, ""
			}
		default:
			t.Fatalf("unexpected Graph request: %s %s", request.Method, request.URL.Path)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	server, err := mcpmicrosoft.New(mcpmicrosoft.Config{
		AccessToken: "test-token", BaseURL: "http://127.0.0.1/v1.0", WriteEnabled: true, HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "office-executor-test", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOfficeTools(ctx, session); err != nil {
		t.Fatal(err)
	}
	executor := &OfficeExecutor{session: session, callTimeout: 2 * time.Second}
	t.Cleanup(func() { _ = executor.Close() })
	payload, _ := json.Marshal(office.EmailDraft{
		To: []string{"dev@example.com"}, Subject: "发布通知", Body: "今晚发布。",
	})
	reference := ""
	request := office.ExecutionRequest{
		Draft: office.Draft{Kind: office.KindEmail, Payload: payload}, IdempotencyKey: "stable-key",
		Checkpoint: func(_ context.Context, value string) error {
			reference, checkpointSaved = value, true
			return nil
		},
	}
	if _, err := executor.Execute(ctx, request); err == nil || reference == "" {
		t.Fatalf("first execution should fail after checkpoint: reference=%q err=%v", reference, err)
	}
	request.ExternalReference = reference
	result, err := executor.Execute(ctx, request)
	if err != nil || !result.ExternalEffect || result.ExternalReference != reference {
		t.Fatalf("retry result = %+v, err=%v", result, err)
	}
	// 模拟“远端已发送、本地完成状态尚未落库”的恢复：状态核对后直接成功，不能再次发送。
	result, err = executor.Execute(ctx, request)
	if err != nil || !result.ExternalEffect || createCalls != 1 || stateCalls != 2 || sendCalls != 2 {
		t.Fatalf("recovery result=%+v calls=create:%d state:%d send:%d err=%v", result, createCalls, stateCalls, sendCalls, err)
	}
}

func TestCalendarTransactionIDIsStableUUID(t *testing.T) {
	first, second := transactionID("same-key"), transactionID("same-key")
	if first != second || len(first) != 36 || first == transactionID("another-key") {
		t.Fatalf("unexpected transaction IDs: %q %q", first, second)
	}
}
