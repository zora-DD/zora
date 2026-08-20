package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	"github.com/cloudwego/eino/components/tool"

	"github.com/zhiruo/zora/internal/agentruntime"
	"github.com/zhiruo/zora/internal/agenttools"
	"github.com/zhiruo/zora/internal/approval"
	"github.com/zhiruo/zora/internal/chat"
	"github.com/zhiruo/zora/internal/config"
	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/feedback"
	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/office"
	"github.com/zhiruo/zora/internal/security"
	"github.com/zhiruo/zora/internal/store/sqlite"
	"github.com/zhiruo/zora/internal/summary"
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
		Provider: "mock", Model: "zora-mock-primary", Instruction: "Be helpful.",
		RequestTimeout: time.Second, MaxIterations: 5,
	}, registeredTools)
	if err != nil {
		t.Fatal(err)
	}
	alternateRuntime, err := agentruntime.New(context.Background(), config.Config{
		Provider: "mock", Model: "zora-mock-alternate", Instruction: "Be helpful.",
		RequestTimeout: time.Second, MaxIterations: 5,
	}, registeredTools)
	if err != nil {
		t.Fatal(err)
	}
	memoryService := newTestMemoryService(t, database)
	feedbackService, err := feedback.NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(chat.NewService(database, runtime,
		chat.WithRuntimeProfiles("primary", []chat.RuntimeProfile{
			{ModelProfile: chat.ModelProfile{ID: "primary", Name: "主模型", Provider: "mock", Model: "zora-mock-primary"}, Runtime: runtime},
			{ModelProfile: chat.ModelProfile{ID: "alternate", Name: "备用模型", Provider: "mock", Model: "zora-mock-alternate"}, Runtime: alternateRuntime},
		}),
		chat.WithMemoryCapturer(memoryService), chat.WithMemoryRecaller(memoryService),
		chat.WithConversationSummarizer(failingSummaryService{}),
		chat.WithFeedback(feedbackService, true),
	), knowledgeService, memoryService, slog.New(slog.NewTextHandler(io.Discard, nil)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	info := httptest.NewRecorder()
	handler.ServeHTTP(info, httptest.NewRequest(http.MethodGet, "/api/info", nil))
	if info.Code != http.StatusOK || !strings.Contains(info.Body.String(), `"default_model_id":"primary"`) || !strings.Contains(info.Body.String(), `"id":"alternate"`) {
		t.Fatalf("unexpected multi-model info: %s", info.Body.String())
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

	send := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conversation.ID+"/messages", bytes.NewBufferString(`{"content":"计算 6 * 7","model_id":"alternate"}`))
	send.Header.Set("Content-Type", "application/json")
	stream := httptest.NewRecorder()
	handler.ServeHTTP(stream, send)
	if stream.Code != http.StatusOK {
		t.Fatalf("send status = %d, body = %s", stream.Code, stream.Body.String())
	}
	events := stream.Body.String()
	if !strings.Contains(events, "event: tool_call") || !strings.Contains(events, "event: tool_result") || !strings.Contains(events, "event: done") || strings.Contains(events, "event: error") || !strings.Contains(events, "42") {
		t.Fatalf("unexpected SSE stream:\n%s", events)
	}
	if !strings.Contains(events, `"metrics":`) || !strings.Contains(events, `"model_calls":2`) {
		t.Fatalf("done event does not include run metrics:\n%s", events)
	}
	messages, err := database.ListMessages(context.Background(), conversation.ID, 10)
	if err != nil || len(messages) != 2 {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	assistantMessage := messages[len(messages)-1]
	feedbackRequest := httptest.NewRequest(http.MethodPut, "/api/messages/"+assistantMessage.ID+"/feedback", strings.NewReader(`{"rating":-1,"reason":"缺少依据"}`))
	feedbackRequest.Header.Set("Content-Type", "application/json")
	feedbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(feedbackResponse, feedbackRequest)
	if feedbackResponse.Code != http.StatusOK || !strings.Contains(feedbackResponse.Body.String(), `"rating":-1`) {
		t.Fatalf("feedback status=%d body=%s", feedbackResponse.Code, feedbackResponse.Body.String())
	}
	messageListResponse := httptest.NewRecorder()
	handler.ServeHTTP(messageListResponse, httptest.NewRequest(http.MethodGet, "/api/conversations/"+conversation.ID+"/messages", nil))
	if messageListResponse.Code != http.StatusOK || !strings.Contains(messageListResponse.Body.String(), `"feedback":`) {
		t.Fatalf("message list status=%d body=%s", messageListResponse.Code, messageListResponse.Body.String())
	}
	if !strings.Contains(events, `"model_id":"alternate"`) {
		t.Fatalf("start event does not include selected model:\n%s", events)
	}

	// Ensure every SSE frame contains valid JSON data.
	scanner := bufio.NewScanner(strings.NewReader(events))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") && !json.Valid([]byte(strings.TrimPrefix(line, "data: "))) {
			t.Fatalf("invalid SSE JSON: %s", line)
		}
	}

	runsResponse := httptest.NewRecorder()
	handler.ServeHTTP(runsResponse, httptest.NewRequest(http.MethodGet, "/api/runs?limit=5", nil))
	if runsResponse.Code != http.StatusOK {
		t.Fatalf("runs status = %d, body = %s", runsResponse.Code, runsResponse.Body.String())
	}
	var runList struct {
		Runs []struct {
			Run     domain.AgentRun `json:"run"`
			Metrics struct {
				ModelCalls    int  `json:"model_calls"`
				ToolCalls     int  `json:"tool_calls"`
				UsageComplete bool `json:"usage_complete"`
			} `json:"metrics"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(runsResponse.Body.Bytes(), &runList); err != nil {
		t.Fatal(err)
	}
	if len(runList.Runs) != 1 || runList.Runs[0].Run.Model != "zora-mock-alternate" || runList.Runs[0].Metrics.ModelCalls != 2 || runList.Runs[0].Metrics.ToolCalls != 1 || runList.Runs[0].Metrics.UsageComplete {
		t.Fatalf("unexpected run summaries: %+v", runList.Runs)
	}
	metricsResponse := httptest.NewRecorder()
	handler.ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/api/runs/"+runList.Runs[0].Run.ID+"/metrics", nil))
	if metricsResponse.Code != http.StatusOK || !strings.Contains(metricsResponse.Body.String(), `"model_calls":2`) {
		t.Fatalf("metrics status = %d, body = %s", metricsResponse.Code, metricsResponse.Body.String())
	}
}

func TestAgentCreatesPersistedEmailDraftPreview(t *testing.T) {
	t.Parallel()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "office-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	registeredTools, err := agenttools.Build()
	if err != nil {
		t.Fatal(err)
	}
	officeService, err := office.NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	draftTools, err := office.NewDraftTools(officeService)
	if err != nil {
		t.Fatal(err)
	}
	registeredTools = append(registeredTools, draftTools...)
	runtime, err := agentruntime.New(context.Background(), config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "请使用中文回答。",
		RequestTimeout: time.Second, MaxIterations: 6,
	}, registeredTools)
	if err != nil {
		t.Fatal(err)
	}
	knowledgeService := newTestKnowledgeService(t, database)
	memoryService := newTestMemoryService(t, database)
	chatService := chat.NewService(database, runtime)
	handler, err := New(chatService, knowledgeService, memoryService,
		slog.New(slog.NewTextHandler(io.Discard, nil)), 3*time.Second,
		WithOfficeService(officeService))
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := chatService.CreateConversation(context.Background(), "草稿预览")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conversation.ID+"/messages",
		strings.NewReader(`{"content":"起草邮件，收件人 dev@example.com，主题：发布通知；正文：项目将在周五发布。"}`))
	request.Header.Set("Content-Type", "application/json")
	stream := httptest.NewRecorder()
	handler.ServeHTTP(stream, request)
	if stream.Code != http.StatusOK || !strings.Contains(stream.Body.String(), "邮件草稿已保存") || !strings.Contains(stream.Body.String(), "尚未发送") {
		t.Fatalf("draft SSE status = %d, body = %s", stream.Code, stream.Body.String())
	}
	listRequest := httptest.NewRequest(http.MethodGet, "/api/office/drafts?kind=email&status=draft", nil)
	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, listRequest)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listed.Code, listed.Body.String())
	}
	var response struct {
		Drafts []office.Draft `json:"drafts"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Drafts) != 1 || response.Drafts[0].ConversationID != conversation.ID || response.Drafts[0].Status != office.StatusDraft {
		t.Fatalf("unexpected drafts: %+v", response.Drafts)
	}
	draftID := response.Drafts[0].ID
	submitRequest := httptest.NewRequest(http.MethodPost, "/api/office/drafts/"+draftID+"/confirmation", nil)
	submitted := httptest.NewRecorder()
	handler.ServeHTTP(submitted, submitRequest)
	if submitted.Code != http.StatusOK || !strings.Contains(submitted.Body.String(), `"status":"pending_confirmation"`) ||
		!strings.Contains(submitted.Body.String(), `"external_effect":false`) {
		t.Fatalf("submit status = %d, body = %s", submitted.Code, submitted.Body.String())
	}
	duplicateSubmit := httptest.NewRecorder()
	handler.ServeHTTP(duplicateSubmit, httptest.NewRequest(http.MethodPost, "/api/office/drafts/"+draftID+"/confirmation", nil))
	if duplicateSubmit.Code != http.StatusConflict {
		t.Fatalf("duplicate submit status = %d, body = %s", duplicateSubmit.Code, duplicateSubmit.Body.String())
	}
	decisionRequest := httptest.NewRequest(http.MethodPost, "/api/office/drafts/"+draftID+"/decision",
		strings.NewReader(`{"decision":"approved","reason":"收件人和正文已核对"}`))
	decisionRequest.Header.Set("Content-Type", "application/json")
	decided := httptest.NewRecorder()
	handler.ServeHTTP(decided, decisionRequest)
	if decided.Code != http.StatusOK || !strings.Contains(decided.Body.String(), `"status":"approved"`) ||
		!strings.Contains(decided.Body.String(), "尚未执行") {
		t.Fatalf("decision status = %d, body = %s", decided.Code, decided.Body.String())
	}
	eventsRequest := httptest.NewRequest(http.MethodGet, "/api/office/drafts/"+draftID+"/events", nil)
	eventsResponse := httptest.NewRecorder()
	handler.ServeHTTP(eventsResponse, eventsRequest)
	if eventsResponse.Code != http.StatusOK || !strings.Contains(eventsResponse.Body.String(), `"to_status":"approved"`) {
		t.Fatalf("events status = %d, body = %s", eventsResponse.Code, eventsResponse.Body.String())
	}
	prepareRequest := httptest.NewRequest(http.MethodPost, "/api/office/drafts/"+draftID+"/operation", nil)
	prepared := httptest.NewRecorder()
	handler.ServeHTTP(prepared, prepareRequest)
	if prepared.Code != http.StatusCreated || !strings.Contains(prepared.Body.String(), `"status":"pending"`) ||
		!strings.Contains(prepared.Body.String(), `"execution_enabled":false`) {
		t.Fatalf("prepare operation status = %d, body = %s", prepared.Code, prepared.Body.String())
	}
	var preparedBody struct {
		Operation office.Operation `json:"operation"`
	}
	if err := json.Unmarshal(prepared.Body.Bytes(), &preparedBody); err != nil {
		t.Fatal(err)
	}
	duplicatePrepare := httptest.NewRecorder()
	handler.ServeHTTP(duplicatePrepare, httptest.NewRequest(http.MethodPost, "/api/office/drafts/"+draftID+"/operation", nil))
	if duplicatePrepare.Code != http.StatusOK || !strings.Contains(duplicatePrepare.Body.String(), preparedBody.Operation.ID) {
		t.Fatalf("duplicate prepare status = %d, body = %s", duplicatePrepare.Code, duplicatePrepare.Body.String())
	}
	executeResponse := httptest.NewRecorder()
	handler.ServeHTTP(executeResponse, httptest.NewRequest(http.MethodPost, "/api/office/operations/"+preparedBody.Operation.ID+"/execute", nil))
	if executeResponse.Code != http.StatusServiceUnavailable || !strings.Contains(executeResponse.Body.String(), "执行器尚未配置") {
		t.Fatalf("disabled execute status = %d, body = %s", executeResponse.Code, executeResponse.Body.String())
	}
	operationEvents := httptest.NewRecorder()
	handler.ServeHTTP(operationEvents, httptest.NewRequest(http.MethodGet, "/api/office/operations/"+preparedBody.Operation.ID+"/events", nil))
	if operationEvents.Code != http.StatusOK || !strings.Contains(operationEvents.Body.String(), `"to_status":"pending"`) {
		t.Fatalf("operation events status = %d, body = %s", operationEvents.Code, operationEvents.Body.String())
	}
}

func TestMultiAgentWriterPreservesTrustedDraftIdentity(t *testing.T) {
	t.Parallel()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "office-multi-agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	officeService, err := office.NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	draftTools, err := office.NewDraftTools(officeService)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "请使用中文回答。",
		RequestTimeout: 2 * time.Second, MaxIterations: 8,
		MultiAgentMaxHandoffs: 4, MultiAgentMaxParallel: 2,
		MultiAgentSpecialistTimeout: time.Second,
	}
	chatModel, err := agentruntime.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agentruntime.NewMultiAgentWithModel(context.Background(), cfg,
		agentruntime.SpecialistToolset{Writer: draftTools}, chatModel)
	if err != nil {
		t.Fatal(err)
	}
	chatService := chat.NewService(database, runtime)
	conversation, err := chatService.CreateConversation(context.Background(), "多 Agent 草稿预览")
	if err != nil {
		t.Fatal(err)
	}
	if err := chatService.Send(context.Background(), conversation.ID,
		"起草邮件，收件人 dev@example.com，主题：发布通知；正文：项目将在周五发布。",
		func(chat.StreamEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	drafts, err := officeService.List(context.Background(), office.ListFilter{Kind: office.KindEmail})
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].ConversationID != conversation.ID || drafts[0].SourceRunID == "" {
		t.Fatalf("unexpected multi-agent drafts: %+v", drafts)
	}
}

func TestMultiAgentApprovalResumesSSEAndPersistsChildRun(t *testing.T) {
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "approval-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	researchTools, err := agenttools.Build()
	if err != nil {
		t.Fatal(err)
	}
	knowledgeService := newTestKnowledgeService(t, database)
	knowledgeTool, err := knowledge.NewSearchTool(knowledgeService)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "请使用中文回答。",
		RequestTimeout: 3 * time.Second, MaxIterations: 8,
		MultiAgentMaxHandoffs: 4, MultiAgentMaxParallel: 2,
		MultiAgentSpecialistTimeout: time.Second,
	}
	chatModel, err := agentruntime.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agentruntime.NewMultiAgentWithModel(context.Background(), cfg, agentruntime.SpecialistToolset{
		Research: researchTools, Document: []tool.BaseTool{knowledgeTool},
	}, chatModel)
	if err != nil {
		t.Fatal(err)
	}
	approvalService, err := approval.NewService(database, approval.Options{Mode: approval.ModeRisky, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	chatService := chat.NewService(database, runtime, chat.WithApprovalGate(approvalService))
	memoryService := newTestMemoryService(t, database)
	handler, err := New(chatService, knowledgeService, memoryService,
		slog.New(slog.NewTextHandler(io.Discard, nil)), 3*time.Second,
		WithApprovalService(approvalService))
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := chatService.CreateConversation(context.Background(), "审批测试")
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conversation.ID+"/messages",
		strings.NewReader(`{"content":"请写一份发布通知并发送给团队"}`))
	request.Header.Set("Content-Type", "application/json")
	stream := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(stream, request)
		close(done)
	}()

	var pending approval.Approval
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		items, listErr := approvalService.List(context.Background(), approval.StatusPending, 10)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(items) > 0 {
			pending = items[0]
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if pending.ID == "" {
		t.Fatal("未观察到待审批记录")
	}
	decision := httptest.NewRequest(http.MethodPost, "/api/approvals/"+pending.ID+"/decision",
		strings.NewReader(`{"decision":"approved","reason":"测试批准"}`))
	decision.Header.Set("Content-Type", "application/json")
	decided := httptest.NewRecorder()
	handler.ServeHTTP(decided, decision)
	if decided.Code != http.StatusOK || !strings.Contains(decided.Body.String(), `"status":"approved"`) {
		t.Fatalf("decision status = %d, body = %s", decided.Code, decided.Body.String())
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("审批通过后原 SSE 未恢复")
	}
	if body := stream.Body.String(); !strings.Contains(body, "event: approval_required") ||
		!strings.Contains(body, "event: approval_approved") || !strings.Contains(body, "event: agent_handoff_started") ||
		!strings.Contains(body, `"child_run_id":"task_`) || !strings.Contains(body, "event: done") {
		t.Fatalf("unexpected approval SSE:\n%s", body)
	}
	runs, err := chatService.ListAgentTaskRuns(context.Background(), pending.RunID)
	if err != nil || len(runs) != 1 || runs[0].Status != domain.RunCompleted {
		t.Fatalf("child runs = %+v, %v", runs, err)
	}
}

type failingSummaryService struct{}

func (failingSummaryService) Get(context.Context, string) (summary.Summary, error) {
	return summary.Summary{}, summary.ErrNotFound
}

func (failingSummaryService) Update(context.Context, string, int64) (summary.UpdateResult, error) {
	return summary.UpdateResult{}, errors.New("摘要模型暂时不可用")
}

func (failingSummaryService) HistoryLimit() int { return 4 }

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
	if err := writer.WriteField("visibility", knowledge.VisibilityPublic); err != nil {
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
	if result.Document.ChunkCount != 1 || result.Document.Version != 1 || result.Document.Visibility != knowledge.VisibilityPublic {
		t.Fatalf("unexpected document metadata: %+v", result.Document)
	}
	versionsRequest := httptest.NewRequest(http.MethodGet, "/api/knowledge/documents/"+result.Document.ID+"/versions", nil)
	versionsResponse := httptest.NewRecorder()
	handler.ServeHTTP(versionsResponse, versionsRequest)
	if versionsResponse.Code != http.StatusOK || !strings.Contains(versionsResponse.Body.String(), `"version":1`) {
		t.Fatalf("versions status = %d, body = %s", versionsResponse.Code, versionsResponse.Body.String())
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
		if path == "/" && !strings.Contains(response.Body.String(), "运行监控") {
			t.Fatalf("GET / did not include the run metrics entry")
		}
		if path == "/" && !strings.Contains(response.Body.String(), `id="modelSelect"`) {
			t.Fatalf("GET / did not include the model selector")
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
	if !strings.Contains(response.Body.String(), `"version":"0.12.0-dev"`) ||
		!strings.Contains(response.Body.String(), `"tool_count":4`) ||
		!strings.Contains(response.Body.String(), `"run-metrics"`) ||
		!strings.Contains(response.Body.String(), `"memory-auto-capture"`) ||
		!strings.Contains(response.Body.String(), `"memory_auto_capture":true`) ||
		!strings.Contains(response.Body.String(), `"memory_recall":true`) ||
		!strings.Contains(response.Body.String(), `"memory-context-injection"`) ||
		!strings.Contains(response.Body.String(), `"conversation_summary":true`) ||
		!strings.Contains(response.Body.String(), `"context-compression"`) {
		t.Fatalf("info does not report the current runtime capabilities: %s", response.Body.String())
	}
}

func TestSecurityCSRFEndpointAndWriteProtection(t *testing.T) {
	handler := newTestHandlerWithOptions(t, func(database *sqlite.SQLite) []Option {
		manager, err := security.New(security.Config{CSRFEnabled: true, PrincipalID: "test"}, database)
		if err != nil {
			t.Fatal(err)
		}
		return []Option{WithSecurity(manager)}
	})

	tokenResponse := httptest.NewRecorder()
	handler.ServeHTTP(tokenResponse, httptest.NewRequest(http.MethodGet, "/api/security/csrf", nil))
	if tokenResponse.Code != http.StatusOK || len(tokenResponse.Result().Cookies()) != 1 {
		t.Fatalf("csrf token status=%d cookies=%d body=%s", tokenResponse.Code, len(tokenResponse.Result().Cookies()), tokenResponse.Body.String())
	}
	var tokenBody struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(tokenResponse.Body.Bytes(), &tokenBody); err != nil || tokenBody.Token == "" {
		t.Fatalf("csrf body=%s err=%v", tokenResponse.Body.String(), err)
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(`{"title":"blocked"}`)))
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d body=%s", missing.Code, missing.Body.String())
	}

	validRequest := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(`{"title":"allowed"}`))
	validRequest.Header.Set("Content-Type", "application/json")
	validRequest.Header.Set("X-CSRF-Token", tokenBody.Token)
	validRequest.AddCookie(tokenResponse.Result().Cookies()[0])
	valid := httptest.NewRecorder()
	handler.ServeHTTP(valid, validRequest)
	if valid.Code != http.StatusCreated {
		t.Fatalf("valid csrf status=%d body=%s", valid.Code, valid.Body.String())
	}
}

func TestHTTPSResponsesIncludeBrowserSecurityHeaders(t *testing.T) {
	handler := newTestHandlerWithOptions(t, func(database *sqlite.SQLite) []Option {
		manager, err := security.New(security.Config{TrustedProxyCIDRs: []string{"10.20.0.0/24"}, PrincipalID: "test"}, database)
		if err != nil {
			t.Fatal(err)
		}
		return []Option{WithSecurity(manager)}
	})
	request := httptest.NewRequest(http.MethodGet, "http://zora.example.com/api/info", nil)
	request.Host = "zora.example.com"
	request.RemoteAddr = "10.20.0.8:12345"
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("info status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Strict-Transport-Security") == "" || response.Header().Get("X-Frame-Options") != "DENY" ||
		!strings.Contains(response.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("HTTPS 安全响应头不完整：%v", response.Header())
	}
}

func TestConversationSummaryEndpointAndContextInjection(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)

	create := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(`{"title":"Summary test"}`))
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	var conversation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &conversation); err != nil {
		t.Fatal(err)
	}

	send := func(content string) string {
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
	send("第一项任务是实现会话摘要。")
	second := send("第二项任务是保留最近消息。")
	if !strings.Contains(second, `"summary":{"updated":true`) {
		t.Fatalf("second response did not report summary update: %s", second)
	}

	getSummary := httptest.NewRequest(http.MethodGet, "/api/conversations/"+conversation.ID+"/summary", nil)
	gotSummary := httptest.NewRecorder()
	handler.ServeHTTP(gotSummary, getSummary)
	if gotSummary.Code != http.StatusOK || !strings.Contains(gotSummary.Body.String(), "第一项任务") || !strings.Contains(gotSummary.Body.String(), `"through_sequence":2`) {
		t.Fatalf("summary status = %d, body = %s", gotSummary.Code, gotSummary.Body.String())
	}

	answer := send("总结一下我们之前聊过什么？")
	if !strings.Contains(answer, "根据这段对话的历史摘要") || !strings.Contains(answer, "第一项任务") {
		t.Fatalf("summary was not injected into mock context: %s", answer)
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
	third := sendMessage("我的主要编程语言是什么？")
	if !strings.Contains(third, "根据长期记忆") ||
		!strings.Contains(third, "用户的主要编程语言是Java。") ||
		!strings.Contains(third, `"memory_recalled":1`) {
		t.Fatalf("third SSE does not use recalled memory: %s", third)
	}
	recall := httptest.NewRequest(http.MethodPost, "/api/memories/recall", strings.NewReader(`{"query":"主要编程语言"}`))
	recall.Header.Set("Content-Type", "application/json")
	recalled := httptest.NewRecorder()
	handler.ServeHTTP(recalled, recall)
	if recalled.Code != http.StatusOK || !strings.Contains(recalled.Body.String(), `"relevance":`) || !strings.Contains(recalled.Body.String(), "Java") {
		t.Fatalf("recall status = %d, body = %s", recalled.Code, recalled.Body.String())
	}

	list := httptest.NewRequest(http.MethodGet, "/api/memories?kind=semantic", nil)
	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "用户的主要编程语言是Java。") || strings.Contains(listed.Body.String(), "用户的主要编程语言是Go。") {
		t.Fatalf("consolidated memories status = %d, body = %s", listed.Code, listed.Body.String())
	}
}

func TestAsyncMemoryCaptureReturnsJobAndExposesStatusAPI(t *testing.T) {
	t.Parallel()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "async-memory-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	registeredTools, err := agenttools.Build()
	if err != nil {
		t.Fatal(err)
	}
	knowledgeService := newTestKnowledgeService(t, database)
	runtime, err := agentruntime.New(context.Background(), config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "Be helpful.",
		RequestTimeout: time.Second, MaxIterations: 5,
	}, registeredTools)
	if err != nil {
		t.Fatal(err)
	}
	memoryService := newTestMemoryService(t, database)
	queue, err := memory.NewCaptureQueue(database)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(chat.NewService(database, runtime,
		chat.WithMemoryCaptureQueue(queue, 3),
	), knowledgeService, memoryService, slog.New(slog.NewTextHandler(io.Discard, nil)), 3*time.Second,
		WithMemoryCaptureQueue(queue))
	if err != nil {
		t.Fatal(err)
	}

	create := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(`{"title":"Async memory"}`))
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	var conversation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &conversation); err != nil {
		t.Fatal(err)
	}
	send := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conversation.ID+"/messages",
		strings.NewReader(`{"content":"我的主要编程语言是 Go。"}`))
	send.Header.Set("Content-Type", "application/json")
	stream := httptest.NewRecorder()
	handler.ServeHTTP(stream, send)
	if stream.Code != http.StatusOK || !strings.Contains(stream.Body.String(), `"memory_job":{"id":"memory_job_`) ||
		!strings.Contains(stream.Body.String(), `"status":"pending"`) {
		t.Fatalf("async memory SSE = %d, %s", stream.Code, stream.Body.String())
	}

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/memory-capture/jobs?status=pending", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"status":"pending"`) {
		t.Fatalf("capture jobs = %d, %s", list.Code, list.Body.String())
	}
	var jobs struct {
		Jobs []memory.CaptureJob `json:"jobs"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &jobs); err != nil || len(jobs.Jobs) != 1 {
		t.Fatalf("decoded jobs = %+v, %v", jobs, err)
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/memory-capture/jobs/"+jobs.Jobs[0].ID, nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), jobs.Jobs[0].RunID) {
		t.Fatalf("capture job = %d, %s", get.Code, get.Body.String())
	}
}

func newTestHandler(t *testing.T) http.Handler {
	return newTestHandlerWithOptions(t, nil)
}

func TestReadinessReflectsSharedDependencyFailure(t *testing.T) {
	t.Parallel()
	handler := newTestHandlerWithOptions(t, func(*sqlite.SQLite) []Option {
		return []Option{WithReadinessCheck(func(context.Context) error {
			return errors.New("redis unavailable")
		})}
	})
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/api/ready", nil))
	if ready.Code != http.StatusServiceUnavailable || !strings.Contains(ready.Body.String(), "共享依赖暂时不可用") {
		t.Fatalf("readiness = %d, %s", ready.Code, ready.Body.String())
	}
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("liveness 不应被依赖故障影响：%d", health.Code)
	}
}

func newTestHandlerWithOptions(t *testing.T, buildOptions func(*sqlite.SQLite) []Option) http.Handler {
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
	ruleSummarizer, err := summary.NewRuleSummarizer(500)
	if err != nil {
		t.Fatal(err)
	}
	summaryService, err := summary.NewService(database, ruleSummarizer, summary.Options{
		TriggerMessages: 4, KeepRecent: 2, MaxRunes: 500, Model: "zora-mock",
	})
	if err != nil {
		t.Fatal(err)
	}
	options := []Option(nil)
	if buildOptions != nil {
		options = buildOptions(database)
	}
	handler, err := New(chat.NewService(database, runtime,
		chat.WithMemoryCapturer(memoryService), chat.WithMemoryRecaller(memoryService),
		chat.WithConversationSummarizer(summaryService),
	), knowledgeService, memoryService, slog.New(slog.NewTextHandler(io.Discard, nil)), 3*time.Second, options...)
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
