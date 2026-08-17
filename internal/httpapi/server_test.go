package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/agentruntime"
	"github.com/zhiruo/zora/internal/agenttools"
	"github.com/zhiruo/zora/internal/chat"
	"github.com/zhiruo/zora/internal/config"
	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/memory"
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
	knowledgeService := newTestKnowledgeService(t, database)
	knowledgeTool, err := knowledge.NewSearchTool(knowledgeService)
	if err != nil {
		t.Fatal(err)
	}
	registeredTools = append(registeredTools, knowledgeTool)
	runtime, err := agentruntime.New(context.Background(), config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "Be helpful.",
		RequestTimeout: time.Second, MaxIterations: 5,
	}, registeredTools)
	if err != nil {
		t.Fatal(err)
	}
	memoryService := newTestMemoryService(t, database)
	handler, err := New(chat.NewService(database, runtime, chat.WithMemoryCapturer(memoryService)), knowledgeService, memoryService, slog.New(slog.NewTextHandler(io.Discard, nil)), 3*time.Second)
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

func TestKnowledgeUploadSearchAndDelete(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "release.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("# 蓝鲸项目\n\n蓝鲸项目的发布日是 2026 年 9 月 18 日，上线前必须完成灰度验证。")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	upload := httptest.NewRequest(http.MethodPost, "/api/knowledge/documents", &body)
	upload.Header.Set("Content-Type", writer.FormDataContentType())
	uploaded := httptest.NewRecorder()
	handler.ServeHTTP(uploaded, upload)
	if uploaded.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body = %s", uploaded.Code, uploaded.Body.String())
	}
	var result knowledge.IngestResult
	if err := json.Unmarshal(uploaded.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Document.ChunkCount != 1 {
		t.Fatalf("chunk count = %d, want 1", result.Document.ChunkCount)
	}

	search := httptest.NewRequest(http.MethodPost, "/api/knowledge/search", strings.NewReader(`{"query":"蓝鲸什么时候发布","top_k":3}`))
	search.Header.Set("Content-Type", "application/json")
	searched := httptest.NewRecorder()
	handler.ServeHTTP(searched, search)
	if searched.Code != http.StatusOK || !strings.Contains(searched.Body.String(), "2026") || !strings.Contains(searched.Body.String(), "release.md") {
		t.Fatalf("search status = %d, body = %s", searched.Code, searched.Body.String())
	}

	createConversation := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(`{"title":"RAG test"}`))
	createConversation.Header.Set("Content-Type", "application/json")
	createdConversation := httptest.NewRecorder()
	handler.ServeHTTP(createdConversation, createConversation)
	var conversation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createdConversation.Body.Bytes(), &conversation); err != nil {
		t.Fatal(err)
	}
	question := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conversation.ID+"/messages",
		strings.NewReader(`{"content":"根据上传的文档，蓝鲸项目发布日期和上线要求是什么？"}`))
	question.Header.Set("Content-Type", "application/json")
	answer := httptest.NewRecorder()
	handler.ServeHTTP(answer, question)
	if answer.Code != http.StatusOK || !strings.Contains(answer.Body.String(), "knowledge_search") || strings.Contains(answer.Body.String(), "current_time") || !strings.Contains(answer.Body.String(), "2026") {
		t.Fatalf("RAG SSE status = %d, body = %s", answer.Code, answer.Body.String())
	}

	request := httptest.NewRequest(http.MethodDelete, "/api/knowledge/documents/"+result.Document.ID, nil)
	deleted := httptest.NewRecorder()
	handler.ServeHTTP(deleted, request)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", deleted.Code, deleted.Body.String())
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
		if path == "/" && !strings.Contains(response.Body.String(), "长期记忆") {
			t.Fatalf("GET / did not include the memory management entry")
		}
	}
}

func TestInfoReportsSQLiteRetrievalBackend(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/api/info", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("info status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"retrieval_backend":"sqlite-exact-scan"`) {
		t.Fatalf("info body = %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"version":"0.3.0-dev"`) ||
		!strings.Contains(response.Body.String(), `"memory-auto-capture"`) ||
		!strings.Contains(response.Body.String(), `"memory_auto_capture":true`) {
		t.Fatalf("info does not report V0.3 memory capability: %s", response.Body.String())
	}
}

func TestMemoryCRUD(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)

	create := httptest.NewRequest(http.MethodPost, "/api/memories", strings.NewReader(`{
		"kind":"semantic",
		"content":"用户偏好使用 Go 编写后端服务。",
		"importance":0.8
	}`))
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create memory status = %d, body = %s", created.Code, created.Body.String())
	}
	var item memory.Memory
	if err := json.Unmarshal(created.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.ID == "" || item.Kind != memory.KindSemantic || item.SourceType != memory.SourceManual {
		t.Fatalf("unexpected created memory: %+v", item)
	}

	list := httptest.NewRequest(http.MethodGet, "/api/memories?kind=semantic&include_expired=true", nil)
	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), item.ID) {
		t.Fatalf("list memories status = %d, body = %s", listed.Code, listed.Body.String())
	}

	replace := httptest.NewRequest(http.MethodPut, "/api/memories/"+item.ID, strings.NewReader(`{
		"kind":"episodic",
		"content":"用户在 2026 年开始开发 Zora 长期记忆。",
		"importance":0.9
	}`))
	replace.Header.Set("Content-Type", "application/json")
	replaced := httptest.NewRecorder()
	handler.ServeHTTP(replaced, replace)
	if replaced.Code != http.StatusOK || !strings.Contains(replaced.Body.String(), `"kind":"episodic"`) {
		t.Fatalf("replace memory status = %d, body = %s", replaced.Code, replaced.Body.String())
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/memories/"+item.ID, nil)
	deleted := httptest.NewRecorder()
	handler.ServeHTTP(deleted, deleteRequest)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete memory status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
	missing := httptest.NewRequest(http.MethodGet, "/api/memories/"+item.ID, nil)
	notFound := httptest.NewRecorder()
	handler.ServeHTTP(notFound, missing)
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("get deleted memory status = %d, body = %s", notFound.Code, notFound.Body.String())
	}
}

func TestConversationAutomaticallyCapturesAndConsolidatesMemory(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)

	create := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(`{"title":"Memory test"}`))
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	var conversation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &conversation); err != nil {
		t.Fatal(err)
	}

	sendMessage := func(content string) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conversation.ID+"/messages",
			strings.NewReader(`{"content":`+strconv.Quote(content)+`}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("send status = %d, body = %s", response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	first := sendMessage("我的主要编程语言是 Go。")
	if !strings.Contains(first, `"memory":{"enabled":true,"candidates":1,"created":1`) {
		t.Fatalf("first SSE does not include capture result: %s", first)
	}
	second := sendMessage("我的主要编程语言是 Java。")
	if !strings.Contains(second, `"updated":1`) {
		t.Fatalf("second SSE does not include consolidation result: %s", second)
	}

	list := httptest.NewRequest(http.MethodGet, "/api/memories?kind=semantic", nil)
	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "用户的主要编程语言是Java。") || strings.Contains(listed.Body.String(), "用户的主要编程语言是Go。") {
		t.Fatalf("consolidated memories status = %d, body = %s", listed.Code, listed.Body.String())
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
	knowledgeService := newTestKnowledgeService(t, database)
	knowledgeTool, err := knowledge.NewSearchTool(knowledgeService)
	if err != nil {
		t.Fatal(err)
	}
	registeredTools = append(registeredTools, knowledgeTool)
	runtime, err := agentruntime.New(context.Background(), config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "Be helpful.",
		RequestTimeout: time.Second, MaxIterations: 5,
	}, registeredTools)
	if err != nil {
		t.Fatal(err)
	}
	memoryService := newTestMemoryService(t, database)
	handler, err := New(chat.NewService(database, runtime, chat.WithMemoryCapturer(memoryService)), knowledgeService, memoryService, slog.New(slog.NewTextHandler(io.Discard, nil)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func newTestMemoryService(t *testing.T, database *sqlite.SQLite) *memory.Service {
	t.Helper()
	extractor, err := memory.NewRuleExtractor(3)
	if err != nil {
		t.Fatal(err)
	}
	service, err := memory.NewService(database, memory.WithExtractor(extractor))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newTestKnowledgeService(t *testing.T, database *sqlite.SQLite) *knowledge.Service {
	t.Helper()
	embedder, err := knowledge.NewHashEmbedder(128)
	if err != nil {
		t.Fatal(err)
	}
	service, err := knowledge.NewService(database, embedder, knowledge.ChunkOptions{MaxRunes: 300, OverlapRunes: 40})
	if err != nil {
		t.Fatal(err)
	}
	return service
}
