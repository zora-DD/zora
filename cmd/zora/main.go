package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zhiruo/zora/internal/agentruntime"
	"github.com/zhiruo/zora/internal/agenttools"
	"github.com/zhiruo/zora/internal/chat"
	"github.com/zhiruo/zora/internal/config"
	"github.com/zhiruo/zora/internal/httpapi"
	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/store/sqlite"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("zora stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	// 启动顺序遵循“配置 -> 持久化 -> 工具/Runtime -> HTTP”，任一步失败都立即退出。
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	database, err := sqlite.Open(filepath.Join(cfg.DataDir, "zora.db"))
	if err != nil {
		return err
	}
	defer database.Close()

	registeredTools, err := agenttools.Build()
	if err != nil {
		return err
	}
	embedder, err := buildEmbedder(cfg)
	if err != nil {
		return err
	}
	knowledgeService, err := knowledge.NewService(database, embedder, knowledge.ChunkOptions{
		MaxRunes: cfg.KnowledgeChunkSize, OverlapRunes: cfg.KnowledgeOverlap,
	})
	if err != nil {
		return err
	}
	// 知识库检索和时间、计算器一样走统一 Tool 协议，便于后续加入多 Agent 调度。
	knowledgeTool, err := knowledge.NewSearchTool(knowledgeService)
	if err != nil {
		return err
	}
	registeredTools = append(registeredTools, knowledgeTool)
	runtime, err := agentruntime.New(context.Background(), cfg, registeredTools)
	if err != nil {
		return err
	}
	chatService := chat.NewService(database, runtime)
	handler, err := httpapi.New(chatService, knowledgeService, logger, cfg.RequestTimeout)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveErrors := make(chan error, 1)
	// HTTP 服务放入 goroutine，主 goroutine 同时监听系统信号和异常退出。
	go func() {
		logger.Info("zora is ready", "addr", cfg.Addr, "provider", cfg.Provider, "model", cfg.Model,
			"embedding_provider", cfg.EmbeddingProvider, "embedding_model", embedder.Name())
		serveErrors <- server.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-signals:
		logger.Info("shutting down", "signal", sig.String())
	case err := <-serveErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	// 给正在执行的 SSE 请求留出退出窗口，超时后由 net/http 强制结束。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func buildEmbedder(cfg config.Config) (knowledge.Embedder, error) {
	switch cfg.EmbeddingProvider {
	case "hash":
		return knowledge.NewHashEmbedder(cfg.EmbeddingDimensions)
	case "openai":
		return knowledge.NewOpenAIEmbedder(knowledge.OpenAIEmbedderConfig{
			APIKey: cfg.EmbeddingAPIKey, BaseURL: cfg.EmbeddingBaseURL,
			Model: cfg.EmbeddingModel, Dimensions: cfg.EmbeddingDimensions,
			HTTPClient: &http.Client{Timeout: cfg.RequestTimeout},
		})
	default:
		return nil, errors.New("不支持的 Embedding 提供方")
	}
}
