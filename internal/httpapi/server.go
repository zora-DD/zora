// Package httpapi 通过 JSON API 和 SSE 暴露聊天服务。
package httpapi

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/zhiruo/zora/internal/approval"
	"github.com/zhiruo/zora/internal/chat"
	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/store"
	"github.com/zhiruo/zora/internal/summary"
)

//go:embed web/*
var webFiles embed.FS

type Server struct {
	chat           *chat.Service
	knowledge      *knowledge.Service
	memory         *memory.Service
	approval       *approval.Service
	logger         *slog.Logger
	requestTimeout time.Duration
}

type Option func(*Server)

func WithApprovalService(service *approval.Service) Option {
	return func(server *Server) { server.approval = service }
}

func New(chatService *chat.Service, knowledgeService *knowledge.Service, memoryService *memory.Service, logger *slog.Logger, requestTimeout time.Duration, options ...Option) (http.Handler, error) {
	if chatService == nil || knowledgeService == nil || memoryService == nil {
		return nil, fmt.Errorf("对话、知识库和长期记忆服务不能为空")
	}
	server := &Server{chat: chatService, knowledge: knowledgeService, memory: memoryService, logger: logger, requestTimeout: requestTimeout}
	for _, option := range options {
		option(server)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", server.health)
	mux.HandleFunc("GET /api/info", server.info)
	mux.HandleFunc("GET /api/conversations", server.listConversations)
	mux.HandleFunc("POST /api/conversations", server.createConversation)
	mux.HandleFunc("GET /api/conversations/{conversationID}/messages", server.listMessages)
	mux.HandleFunc("GET /api/conversations/{conversationID}/summary", server.getConversationSummary)
	mux.HandleFunc("PATCH /api/conversations/{conversationID}", server.renameConversation)
	mux.HandleFunc("DELETE /api/conversations/{conversationID}", server.deleteConversation)
	mux.HandleFunc("POST /api/conversations/{conversationID}/messages", server.sendMessage)
	mux.HandleFunc("GET /api/runs/{runID}/events", server.listRunEvents)
	mux.HandleFunc("GET /api/runs/{runID}/children", server.listAgentTaskRuns)
	if server.approval != nil && server.approval.Enabled() {
		mux.HandleFunc("GET /api/approvals", server.listApprovals)
		mux.HandleFunc("POST /api/approvals/{approvalID}/decision", server.decideApproval)
	}
	mux.HandleFunc("GET /api/knowledge/documents", server.listKnowledgeDocuments)
	mux.HandleFunc("POST /api/knowledge/documents", server.uploadKnowledgeDocument)
	mux.HandleFunc("DELETE /api/knowledge/documents/{documentID}", server.deleteKnowledgeDocument)
	mux.HandleFunc("POST /api/knowledge/search", server.searchKnowledge)
	mux.HandleFunc("GET /api/memories", server.listMemories)
	mux.HandleFunc("POST /api/memories", server.createMemory)
	mux.HandleFunc("POST /api/memories/recall", server.recallMemories)
	mux.HandleFunc("GET /api/memories/{memoryID}", server.getMemory)
	mux.HandleFunc("PUT /api/memories/{memoryID}", server.replaceMemory)
	mux.HandleFunc("DELETE /api/memories/{memoryID}", server.deleteMemory)

	// 前端资源编译进 Go 二进制，部署时不需要额外静态文件服务器。
	assets, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, fmt.Errorf("加载内嵌 Web 资源失败：%w", err)
	}
	mux.Handle("/", spaHandler{assets: assets})
	return server.middleware(mux), nil
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})
}

func (s *Server) info(w http.ResponseWriter, _ *http.Request) {
	capabilities := []string{
		"chat", "streaming", "tools", "persistence", "run-audit",
		"knowledge-ingestion", "hybrid-retrieval", "knowledge-citations",
		"semantic-memory", "episodic-memory", "memory-crud",
	}
	if s.memory.AutoCaptureEnabled() {
		capabilities = append(capabilities, "memory-auto-capture", "memory-consolidation")
	}
	if s.chat.MemoryRecallEnabled() {
		capabilities = append(capabilities, "memory-recall", "memory-context-injection")
	}
	if s.chat.SummaryEnabled() {
		capabilities = append(capabilities, "conversation-summary", "context-compression")
	}
	if s.chat.MultiAgentEnabled() {
		capabilities = append(capabilities, "supervisor", "specialist-agents", "agent-handoff-audit")
	}
	if s.chat.ApprovalEnabled() {
		capabilities = append(capabilities, "human-approval")
	}
	if s.knowledge.RetrievalBackend() == "postgres-pgvector-fts" {
		capabilities = append(capabilities, "pgvector-hnsw", "postgresql-fts")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": "Zora", "version": "0.4.0-dev",
		"provider": s.chat.Provider(), "model": s.chat.Model(),
		"agent_name": s.chat.AgentName(), "multi_agent": s.chat.MultiAgentEnabled(),
		"human_approval":       s.chat.ApprovalEnabled(),
		"embedding_model":      s.knowledge.EmbeddingModel(),
		"retrieval_backend":    s.knowledge.RetrievalBackend(),
		"memory_auto_capture":  s.memory.AutoCaptureEnabled(),
		"memory_recall":        s.chat.MemoryRecallEnabled(),
		"conversation_summary": s.chat.SummaryEnabled(),
		"capabilities":         capabilities,
	})
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	conversations, err := s.chat.ListConversations(r.Context())
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": conversations})
}

func (s *Server) createConversation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.problem(w, err)
		return
	}
	conversation, err := s.chat.CreateConversation(r.Context(), input.Title)
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, conversation)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	messages, err := s.chat.ListMessages(r.Context(), r.PathValue("conversationID"))
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
}

func (s *Server) getConversationSummary(w http.ResponseWriter, r *http.Request) {
	item, err := s.chat.GetConversationSummary(r.Context(), r.PathValue("conversationID"))
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) renameConversation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.problem(w, err)
		return
	}
	if err := s.chat.RenameConversation(r.Context(), r.PathValue("conversationID"), input.Title); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	if err := s.chat.DeleteConversation(r.Context(), r.PathValue("conversationID")); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.problem(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.problem(w, errors.New("当前服务器不支持流式输出"))
		return
	}

	// 关闭代理缓冲，保证模型增量和工具事件能够立即到达浏览器。
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// 请求 Context 会同时感知浏览器断开和服务端超时，并一路传递到 Eino/模型。
	ctx, cancel := contextWithTimeout(r, s.requestTimeout)
	defer cancel()
	err := s.chat.Send(ctx, r.PathValue("conversationID"), input.Content, func(event chat.StreamEvent) error {
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})
	if err != nil {
		payload, _ := json.Marshal(chat.StreamEvent{Type: "error", Content: userError(err)})
		_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
		flusher.Flush()
		s.logger.Error("agent run failed", "error", err, "conversation_id", r.PathValue("conversationID"))
	}
}

func (s *Server) listRunEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.chat.ListRunEvents(r.Context(), r.PathValue("runID"))
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) listAgentTaskRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.chat.ListAgentTaskRuns(r.Context(), r.PathValue("runID"))
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.approval.List(r.Context(), strings.TrimSpace(r.URL.Query().Get("status")), limit)
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": items})
}

func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.problem(w, err)
		return
	}
	item, err := s.approval.Decide(r.Context(), r.PathValue("approvalID"), strings.ToLower(strings.TrimSpace(input.Decision)), input.Reason)
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) listKnowledgeDocuments(w http.ResponseWriter, r *http.Request) {
	documents, err := s.knowledge.ListDocuments(r.Context())
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"documents": documents})
}

func (s *Server) uploadKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	// V0.2 先限制为 5 MiB 纯文本；多出的 1 MiB 留给 multipart 边界和表单字段。
	r.Body = http.MaxBytesReader(w, r.Body, 6<<20)
	if err := r.ParseMultipartForm(6 << 20); err != nil {
		s.problem(w, fmt.Errorf("multipart 上传请求无效：%w", err))
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		s.problem(w, fmt.Errorf("请通过 file 字段上传文件：%w", err))
		return
	}
	defer file.Close()

	extension := strings.ToLower(filepath.Ext(header.Filename))
	if extension != ".txt" && extension != ".md" && extension != ".markdown" {
		s.problem(w, fmt.Errorf("仅支持 .txt、.md 和 .markdown 文件"))
		return
	}
	contents, err := io.ReadAll(io.LimitReader(file, (5<<20)+1))
	if err != nil {
		s.problem(w, fmt.Errorf("读取上传文档失败：%w", err))
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = filepath.Base(header.Filename)
	}
	mimeType := header.Header.Get("Content-Type")
	if mimeType == "" || mimeType == "application/octet-stream" {
		if extension == ".md" || extension == ".markdown" {
			mimeType = "text/markdown"
		} else {
			mimeType = "text/plain"
		}
	}
	result, err := s.knowledge.Ingest(r.Context(), knowledge.IngestInput{
		Name: name, SourceType: "upload", MIMEType: mimeType, Content: contents,
	})
	if err != nil {
		s.problem(w, err)
		return
	}
	status := http.StatusCreated
	if result.Deduplicated {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

func (s *Server) deleteKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	if err := s.knowledge.DeleteDocument(r.Context(), r.PathValue("documentID")); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) searchKnowledge(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.problem(w, err)
		return
	}
	results, err := s.knowledge.Search(r.Context(), input.Query, input.TopK)
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"embedding_model": s.knowledge.EmbeddingModel(), "results": results,
	})
}

func (s *Server) listMemories(w http.ResponseWriter, r *http.Request) {
	includeExpired := false
	if raw := strings.TrimSpace(r.URL.Query().Get("include_expired")); raw != "" {
		var err error
		includeExpired, err = strconv.ParseBool(raw)
		if err != nil {
			s.problem(w, fmt.Errorf("include_expired 必须是 true 或 false"))
			return
		}
	}
	items, err := s.memory.List(r.Context(), r.URL.Query().Get("kind"), includeExpired)
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memories": items})
}

func (s *Server) recallMemories(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Query string `json:"query"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.problem(w, err)
		return
	}
	results, err := s.memory.Recall(r.Context(), input.Query)
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) createMemory(w http.ResponseWriter, r *http.Request) {
	input, err := decodeMemoryInput(w, r)
	if err != nil {
		s.problem(w, err)
		return
	}
	item, err := s.memory.Create(r.Context(), memory.CreateInput{
		Kind: input.Kind, Content: input.Content, Importance: input.Importance, ExpiresAt: input.ExpiresAt,
	})
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) getMemory(w http.ResponseWriter, r *http.Request) {
	item, err := s.memory.Get(r.Context(), r.PathValue("memoryID"))
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) replaceMemory(w http.ResponseWriter, r *http.Request) {
	input, err := decodeMemoryInput(w, r)
	if err != nil {
		s.problem(w, err)
		return
	}
	item, err := s.memory.Replace(r.Context(), r.PathValue("memoryID"), memory.ReplaceInput{
		Kind: input.Kind, Content: input.Content, Importance: input.Importance, ExpiresAt: input.ExpiresAt,
	})
	if err != nil {
		s.problem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	if err := s.memory.Delete(r.Context(), r.PathValue("memoryID")); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type memoryRequest struct {
	Kind       string
	Content    string
	Importance *float64
	ExpiresAt  *time.Time
}

func decodeMemoryInput(w http.ResponseWriter, r *http.Request) (memoryRequest, error) {
	var raw struct {
		Kind       string   `json:"kind"`
		Content    string   `json:"content"`
		Importance *float64 `json:"importance"`
		ExpiresAt  string   `json:"expires_at"`
	}
	if err := decodeJSON(w, r, &raw); err != nil {
		return memoryRequest{}, err
	}
	result := memoryRequest{Kind: raw.Kind, Content: raw.Content, Importance: raw.Importance}
	if value := strings.TrimSpace(raw.ExpiresAt); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return memoryRequest{}, fmt.Errorf("expires_at 必须是 RFC3339 时间，例如 2026-12-31T23:59:59+08:00")
		}
		result.ExpiresAt = &parsed
	}
	return result, nil
}

func (s *Server) problem(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, knowledge.ErrNotFound) || errors.Is(err, memory.ErrNotFound) || errors.Is(err, summary.ErrNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, knowledge.ErrEmbeddingMismatch) {
		status = http.StatusConflict
	}
	if status >= 500 {
		s.logger.Error("request failed", "error", err)
	}
	writeJSON(w, status, map[string]any{"error": userError(err)})
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' data:")
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("panic", "value", recovered, "stack", string(debug.Stack()))
				if !strings.HasPrefix(r.URL.Path, "/api/conversations/") || !strings.HasSuffix(r.URL.Path, "/messages") {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "服务器内部错误"})
				}
			}
			s.logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
		}()
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("JSON 请求体无效：%w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON 请求体只能包含一个对象")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func userError(err error) string {
	if errors.Is(err, contextDeadlineExceeded()) {
		return "请求执行超时，请缩小任务范围后重试"
	}
	if errors.Is(err, contextCanceled()) {
		return "请求已取消"
	}
	return err.Error()
}

type spaHandler struct{ assets fs.FS }

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	// 未命中真实静态资源时返回 index.html，支持前端 History 路由直接刷新。
	info, err := fs.Stat(h.assets, path)
	if err != nil || info.IsDir() {
		path = "index.html"
		info, err = fs.Stat(h.assets, path)
		if err != nil {
			http.Error(w, "Web 应用暂时不可用", http.StatusInternalServerError)
			return
		}
	}
	contents, err := fs.ReadFile(h.assets, path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if path == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, path, info.ModTime(), bytes.NewReader(contents))
}
