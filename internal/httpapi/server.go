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
	"runtime/debug"
	"strings"
	"time"

	"github.com/zhiruo/zora/internal/chat"
	"github.com/zhiruo/zora/internal/store"
)

//go:embed web/*
var webFiles embed.FS

type Server struct {
	chat           *chat.Service
	logger         *slog.Logger
	requestTimeout time.Duration
}

func New(chatService *chat.Service, logger *slog.Logger, requestTimeout time.Duration) (http.Handler, error) {
	server := &Server{chat: chatService, logger: logger, requestTimeout: requestTimeout}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", server.health)
	mux.HandleFunc("GET /api/info", server.info)
	mux.HandleFunc("GET /api/conversations", server.listConversations)
	mux.HandleFunc("POST /api/conversations", server.createConversation)
	mux.HandleFunc("GET /api/conversations/{conversationID}/messages", server.listMessages)
	mux.HandleFunc("PATCH /api/conversations/{conversationID}", server.renameConversation)
	mux.HandleFunc("DELETE /api/conversations/{conversationID}", server.deleteConversation)
	mux.HandleFunc("POST /api/conversations/{conversationID}/messages", server.sendMessage)
	mux.HandleFunc("GET /api/runs/{runID}/events", server.listRunEvents)

	// 前端资源编译进 Go 二进制，部署时不需要额外静态文件服务器。
	assets, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, fmt.Errorf("open embedded web assets: %w", err)
	}
	mux.Handle("/", spaHandler{assets: assets})
	return server.middleware(mux), nil
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})
}

func (s *Server) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name": "Zora", "version": "0.1.0",
		"provider": s.chat.Provider(), "model": s.chat.Model(),
		"capabilities": []string{"chat", "streaming", "tools", "persistence", "run-audit"},
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
		s.problem(w, errors.New("streaming is not supported by this server"))
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

func (s *Server) problem(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
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
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON body must contain one object")
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
			http.Error(w, "web application unavailable", http.StatusInternalServerError)
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
