package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cloudwego/eino/components/tool"

	"github.com/zhiruo/zora/internal/agentruntime"
	"github.com/zhiruo/zora/internal/agenttools"
	"github.com/zhiruo/zora/internal/approval"
	"github.com/zhiruo/zora/internal/chat"
	"github.com/zhiruo/zora/internal/config"
	"github.com/zhiruo/zora/internal/httpapi"
	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/store"
	"github.com/zhiruo/zora/internal/store/postgres"
	"github.com/zhiruo/zora/internal/store/sqlite"
	"github.com/zhiruo/zora/internal/summary"
)

type applicationStore interface {
	store.Store
	knowledge.Store
	memory.Store
	approval.Store
	summary.Store
}

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
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), cfg.RequestTimeout)
	defer cancelStartup()
	database, err := buildStore(startupCtx, cfg)
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
	chatModel, err := agentruntime.NewChatModel(context.Background(), cfg)
	if err != nil {
		return err
	}
	memoryOptions := make([]memory.Option, 0, 1)
	if cfg.MemoryAutoCapture {
		var extractor memory.Extractor
		if cfg.Provider == "mock" {
			extractor, err = memory.NewRuleExtractor(cfg.MemoryMaxCandidates)
		} else {
			extractor, err = memory.NewModelExtractor(chatModel, cfg.MemoryMaxCandidates)
		}
		if err != nil {
			return err
		}
		memoryOptions = append(memoryOptions, memory.WithExtractor(extractor))
	}
	memoryOptions = append(memoryOptions, memory.WithRecallOptions(cfg.MemoryRecallLimit, cfg.MemoryRecallMinScore))
	memoryService, err := memory.NewService(database, memoryOptions...)
	if err != nil {
		return err
	}
	var runtime *agentruntime.Runtime
	if cfg.MultiAgentEnabled {
		// Supervisor 只看得到三个专业 Agent；底层工具按职责注入，避免越权调用。
		runtime, err = agentruntime.NewMultiAgentWithModel(context.Background(), cfg, agentruntime.SpecialistToolset{
			Research: registeredTools,
			Document: []tool.BaseTool{knowledgeTool},
		}, chatModel)
	} else {
		runtime, err = agentruntime.NewWithModel(context.Background(), cfg, append(registeredTools, knowledgeTool), chatModel)
	}
	if err != nil {
		return err
	}
	chatOptions := []chat.Option{chat.WithMemoryCapturer(memoryService)}
	var approvalService *approval.Service
	if cfg.MultiAgentEnabled && cfg.MultiAgentApprovalMode != approval.ModeOff {
		approvalService, err = approval.NewService(database, approval.Options{
			Mode: cfg.MultiAgentApprovalMode, Timeout: cfg.MultiAgentApprovalTimeout,
		})
		if err != nil {
			return err
		}
		chatOptions = append(chatOptions, chat.WithApprovalGate(approvalService))
	}
	if cfg.MemoryRecallEnabled {
		chatOptions = append(chatOptions, chat.WithMemoryRecaller(memoryService))
	}
	if cfg.SummaryEnabled {
		var summarizer summary.Summarizer
		if cfg.Provider == "mock" {
			summarizer, err = summary.NewRuleSummarizer(cfg.SummaryMaxRunes)
		} else {
			summarizer, err = summary.NewModelSummarizer(chatModel)
		}
		if err != nil {
			return err
		}
		summaryService, err := summary.NewService(database, summarizer, summary.Options{
			TriggerMessages: cfg.SummaryTriggerMessages,
			KeepRecent:      cfg.SummaryKeepRecent,
			MaxRunes:        cfg.SummaryMaxRunes,
			Model:           cfg.Model,
		})
		if err != nil {
			return err
		}
		chatOptions = append(chatOptions, chat.WithConversationSummarizer(summaryService))
	}
	chatService := chat.NewService(database, runtime, chatOptions...)
	httpOptions := make([]httpapi.Option, 0, 1)
	if approvalService != nil {
		httpOptions = append(httpOptions, httpapi.WithApprovalService(approvalService))
	}
	handler, err := httpapi.New(chatService, knowledgeService, memoryService, logger, cfg.RequestTimeout, httpOptions...)
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
		logger.Info("zora is ready", "addr", cfg.Addr, "store", cfg.StoreProvider,
			"provider", cfg.Provider, "model", cfg.Model,
			"embedding_provider", cfg.EmbeddingProvider, "embedding_model", embedder.Name(),
			"memory_auto_capture", cfg.MemoryAutoCapture, "memory_recall", cfg.MemoryRecallEnabled,
			"conversation_summary", cfg.SummaryEnabled, "multi_agent", cfg.MultiAgentEnabled)
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

func buildStore(ctx context.Context, cfg config.Config) (applicationStore, error) {
	switch cfg.StoreProvider {
	case "sqlite":
		return sqlite.Open(filepath.Join(cfg.DataDir, "zora.db"))
	case "postgres":
		return postgres.Open(ctx, postgres.Config{
			DSN: cfg.PostgresDSN, MaxConns: int32(cfg.PostgresMaxConns),
			EmbeddingDimensions: cfg.EmbeddingDimensions,
		})
	default:
		return nil, fmt.Errorf("不支持的存储提供方：%q", cfg.StoreProvider)
	}
}

func buildEmbedder(cfg config.Config) (knowledge.Embedder, error) {
	return knowledge.NewEmbedder(knowledge.EmbedderConfig{
		Provider: cfg.EmbeddingProvider, APIKey: cfg.EmbeddingAPIKey,
		BaseURL: cfg.EmbeddingBaseURL, Model: cfg.EmbeddingModel,
		Dimensions: cfg.EmbeddingDimensions,
		HTTPClient: &http.Client{Timeout: cfg.RequestTimeout},
	})
}
