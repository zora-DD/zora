package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/agentruntime"
	"github.com/zhiruo/zora/internal/agenttools"
	"github.com/zhiruo/zora/internal/chat"
	"github.com/zhiruo/zora/internal/config"
	"github.com/zhiruo/zora/internal/store/sqlite"
)

func TestConversationAndAgentSSE(t *testing.T) {
	t.Parallel()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	registeredTools, err := agenttools.Build()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agentruntime.New(context.Background(), config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "Be helpful.",
		RequestTimeout: time.Second, MaxIterations: 5,
	}, registeredTools)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(chat.NewService(database, runtime), slog.New(slog.NewTextHandler(io.Discard, nil)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	create := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(`{"title":"API test"}`))
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var conversation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &conversation); err != nil {
		t.Fatal(err)
	}

	send := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conversation.ID+"/messages", bytes.NewBufferString(`{"content":"计算 6 * 7"}`))
	send.Header.Set("Content-Type", "application/json")
	stream := httptest.NewRecorder()
	handler.ServeHTTP(stream, send)
	if stream.Code != http.StatusOK {
		t.Fatalf("send status = %d, body = %s", stream.Code, stream.Body.String())
	}
	events := stream.Body.String()
	if !strings.Contains(events, "event: tool_call") || !strings.Contains(events, "event: tool_result") || !strings.Contains(events, "event: done") || !strings.Contains(events, "42") {
		t.Fatalf("unexpected SSE stream:\n%s", events)
	}

	// Ensure every SSE frame contains valid JSON data.
	scanner := bufio.NewScanner(strings.NewReader(events))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") && !json.Valid([]byte(strings.TrimPrefix(line, "data: "))) {
			t.Fatalf("invalid SSE JSON: %s", line)
		}
	}
}

func TestEmbeddedSPA(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)
	for _, path := range []string{"/", "/conversation/example", "/styles.css"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, response.Code)
		}
		if path != "/styles.css" && !strings.Contains(response.Body.String(), "Zora Agent") {
			t.Fatalf("GET %s did not serve the SPA entry", path)
		}
	}
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "spa.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	registeredTools, err := agenttools.Build()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agentruntime.New(context.Background(), config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "Be helpful.",
		RequestTimeout: time.Second, MaxIterations: 5,
	}, registeredTools)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(chat.NewService(database, runtime), slog.New(slog.NewTextHandler(io.Discard, nil)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
