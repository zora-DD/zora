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
	"github.com/redis/go-redis/v9"

	"github.com/zhiruo/zora/internal/agentruntime"
	"github.com/zhiruo/zora/internal/agenttools"
	"github.com/zhiruo/zora/internal/approval"
	"github.com/zhiruo/zora/internal/authn"
	"github.com/zhiruo/zora/internal/background"
	"github.com/zhiruo/zora/internal/chat"
	"github.com/zhiruo/zora/internal/config"
	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/httpapi"
	"github.com/zhiruo/zora/internal/id"
	"github.com/zhiruo/zora/internal/identity"
	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/mcpbridge"
	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/observability"
	"github.com/zhiruo/zora/internal/office"
	"github.com/zhiruo/zora/internal/security"
	"github.com/zhiruo/zora/internal/semantic"
	"github.com/zhiruo/zora/internal/store"
	"github.com/zhiruo/zora/internal/store/postgres"
	"github.com/zhiruo/zora/internal/store/sqlite"
	"github.com/zhiruo/zora/internal/summary"
)

type applicationStore interface {
	store.Store
	knowledge.Store
	memory.Store
	memory.CaptureJobStore
	semantic.Store
	security.QuotaStore
	background.Store
	approval.Store
	office.Store
	summary.Store
	Ping(context.Context) error
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("Zora 已停止", "错误", err)
		os.Exit(1)
	}
}

func configForModel(base config.Config, profile config.ModelProfile) config.Config {
	base.Provider = profile.Provider
	base.Model = profile.Model
	base.APIKey = profile.APIKey
	base.BaseURL = profile.BaseURL
	base.ModelExtraFields = profile.ExtraFields
	return base
}

func run(logger *slog.Logger) error {
	// 启动顺序遵循“配置 -> 持久化 -> 工具/Runtime -> HTTP”，任一步失败都立即退出。
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), cfg.RequestTimeout)
	defer cancelStartup()
	telemetry, err := observability.NewTelemetry(startupCtx, observability.TelemetryConfig{
		ServiceName: cfg.OTelServiceName, ServiceVersion: "0.12.0-dev",
		Environment: cfg.OTelEnvironment, TracingEnabled: cfg.OTelEnabled,
		OTLPEndpoint: cfg.OTelEndpoint, TraceSampleRatio: cfg.OTelSampleRatio,
		PrometheusEnabled: cfg.PrometheusEnabled,
	})
	if err != nil {
		return err
	}
	defer func() {
		// 关闭阶段会刷新 BatchSpanProcessor，避免进程退出前丢失最后一批 Trace。
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := telemetry.Shutdown(shutdownCtx); shutdownErr != nil {
			logger.Warn("关闭可观察性组件失败", "错误", shutdownErr)
		}
	}()
	database, err := buildStore(startupCtx, cfg)
	if err != nil {
		return err
	}
	defer database.Close()
	redisClient, err := buildRedis(startupCtx, cfg.RedisURL)
	if err != nil {
		return err
	}
	if redisClient != nil {
		defer redisClient.Close()
	}
	authManager, err := authn.New(authn.Config{
		Enabled: cfg.AuthEnabled, ClientID: cfg.GitHubOAuthClientID,
		ClientSecret: cfg.GitHubOAuthClientSecret, RedirectURL: cfg.GitHubOAuthRedirectURL,
		SessionTTL: cfg.AuthSessionTTL, CookieSecure: cfg.CookieSecure,
		CookieDomain: cfg.AuthCookieDomain,
	}, authn.NewRedisSessionStore(redisClient), logger, identity.LocalPrincipal(cfg.KnowledgePrincipalID))
	if err != nil {
		return fmt.Errorf("初始化 GitHub 登录失败：%w", err)
	}
	securityOptions := make([]security.Option, 0, 1)
	if redisClient != nil && cfg.RateLimitEnabled {
		limiter, limiterErr := security.NewRedisRateLimiter(redisClient, cfg.RateLimitRequestsPerSecond, cfg.RateLimitBurst, "")
		if limiterErr != nil {
			return fmt.Errorf("初始化 Redis 分布式限流失败：%w", limiterErr)
		}
		securityOptions = append(securityOptions, security.WithRateLimiter(limiter))
	}
	securityManager, err := security.New(security.Config{
		RateLimitEnabled: cfg.RateLimitEnabled, RequestsPerSecond: cfg.RateLimitRequestsPerSecond,
		Burst: cfg.RateLimitBurst, DailyRequestQuota: cfg.DailyRequestQuota,
		DailyChatQuota: cfg.DailyChatQuota, DailyUploadByteQuota: cfg.DailyUploadBytesQuota,
		CSRFEnabled: cfg.CSRFEnabled, CookieSecure: cfg.CookieSecure,
		AllowedOrigins: cfg.CORSAllowedOrigins, TrustedProxyCIDRs: cfg.TrustedProxyCIDRs,
		PrincipalID: cfg.KnowledgePrincipalID,
	}, database, securityOptions...)
	if err != nil {
		return fmt.Errorf("初始化 API 安全边界失败：%w", err)
	}

	registeredTools, err := agenttools.Build()
	if err != nil {
		return err
	}
	registeredTools, err = telemetry.WrapTools(startupCtx, registeredTools)
	if err != nil {
		return fmt.Errorf("包装内置工具可观察性失败：%w", err)
	}
	var officeExecutor *mcpbridge.OfficeExecutor
	if cfg.OfficeExecutor == "microsoft_graph" {
		officeExecutor, err = mcpbridge.ConnectOfficeExecutor(startupCtx, mcpbridge.OfficeExecutorConfig{
			Command: cfg.OfficeExecutorCommand,
			Args:    cfg.OfficeExecutorArgs,
			// 只把 Microsoft Graph 专用变量交给子进程，模型密钥和数据库连接串不会被继承。
			PassEnv: []string{
				"ZORA_OFFICE_MICROSOFT_ACCESS_TOKEN",
				"ZORA_OFFICE_MICROSOFT_ACCESS_TOKEN_FILE",
				"ZORA_OFFICE_MICROSOFT_TENANT_ID",
				"ZORA_OFFICE_MICROSOFT_CLIENT_ID",
				"ZORA_OFFICE_MICROSOFT_CLIENT_SECRET_FILE",
				"ZORA_OFFICE_MICROSOFT_OAUTH_BASE_URL",
				"ZORA_OFFICE_MICROSOFT_OAUTH_SCOPE",
				"ZORA_OFFICE_MICROSOFT_BASE_URL",
				"ZORA_OFFICE_MICROSOFT_USER_ID",
				"ZORA_OFFICE_MICROSOFT_WRITE_ENABLED",
			},
		}, mcpbridge.Options{
			ConnectTimeout: cfg.MCPConnectTimeout,
			CallTimeout:    cfg.MCPCallTimeout,
			MaxOutputRunes: cfg.MCPMaxOutputRunes,
		})
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := officeExecutor.Close(); closeErr != nil {
				logger.Debug("Microsoft 办公执行器关闭时返回错误", "error", closeErr)
			}
		}()
	}
	officeOptions := make([]office.ServiceOption, 0, 1)
	if officeExecutor != nil {
		officeOptions = append(officeOptions, office.WithExecutor(officeExecutor))
	}
	officeService, err := office.NewService(database, officeOptions...)
	if err != nil {
		return err
	}
	// 进程异常退出可能遗留 executing 租约；启动时先恢复为 failed，后续仍复用原幂等键。
	recoveredOperations, err := officeService.RecoverExpiredOperations(startupCtx)
	if err != nil {
		return fmt.Errorf("恢复过期办公执行任务失败：%w", err)
	}
	if recoveredOperations > 0 {
		logger.Warn("已恢复租约过期的办公执行任务", "任务数", recoveredOperations)
	}
	draftTools, err := office.NewDraftTools(officeService)
	if err != nil {
		return err
	}
	draftTools, err = telemetry.WrapTools(startupCtx, draftTools)
	if err != nil {
		return fmt.Errorf("包装办公草稿工具可观察性失败：%w", err)
	}
	var mcpManager *mcpbridge.Manager
	var mcpTools []tool.BaseTool
	if cfg.MCPEnabled {
		serverConfigs := make([]mcpbridge.ServerConfig, 0, len(cfg.MCPServers))
		for _, server := range cfg.MCPServers {
			serverConfigs = append(serverConfigs, mcpbridge.ServerConfig{
				Name: server.Name, Command: server.Command, Args: server.Args,
				AllowedTools: server.AllowedTools, PassEnv: server.PassEnv,
			})
		}
		mcpManager, err = mcpbridge.Connect(startupCtx, serverConfigs, mcpbridge.Options{
			ConnectTimeout: cfg.MCPConnectTimeout,
			CallTimeout:    cfg.MCPCallTimeout,
			MaxOutputRunes: cfg.MCPMaxOutputRunes,
		})
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := mcpManager.Close(); closeErr != nil {
				// 终端信号通常会同时送达主进程和 stdio 子进程；关闭错误只记调试日志，不覆盖主服务终态。
				logger.Debug("MCP 连接器关闭时返回错误", "error", closeErr)
			}
		}()
		// MCP 工具已经过 Server 名称空间、白名单和只读声明校验，之后才进入 Agent 工具集合。
		mcpTools = mcpManager.Tools()
		mcpTools, err = telemetry.WrapTools(startupCtx, mcpTools)
		if err != nil {
			return fmt.Errorf("包装 MCP 工具可观察性失败：%w", err)
		}
	}
	embedder, err := buildEmbedder(cfg)
	if err != nil {
		return err
	}
	// Embedding 在知识库写入和查询时共用此包装，二者会自然挂到调用方当前 Trace 下。
	embedder = telemetry.WrapEmbedder(embedder, cfg.EmbeddingProvider)
	knowledgeService, err := knowledge.NewService(database, embedder, knowledge.ChunkOptions{
		MaxRunes: cfg.KnowledgeChunkSize, OverlapRunes: cfg.KnowledgeOverlap,
	}, knowledge.WithPrincipal(cfg.KnowledgePrincipalID))
	if err != nil {
		return err
	}
	semanticService, err := semantic.NewService(database, embedder, semantic.Options{
		MessageRecallLimit: cfg.MessageRecallLimit, MessageRecallMinScore: cfg.MessageRecallMinScore,
	})
	if err != nil {
		return err
	}
	backgroundQueue, err := background.NewQueue(database)
	if err != nil {
		return err
	}
	// 知识库检索和时间、计算器一样走统一 Tool 协议，便于后续加入多 Agent 调度。
	knowledgeTool, err := knowledge.NewSearchTool(knowledgeService)
	if err != nil {
		return err
	}
	knowledgeTool, err = telemetry.WrapTool(startupCtx, knowledgeTool)
	if err != nil {
		return fmt.Errorf("包装知识库工具可观察性失败：%w", err)
	}
	chatModel, err := agentruntime.NewChatModel(context.Background(), cfg)
	if err != nil {
		return err
	}
	chatModel = telemetry.WrapChatModel(chatModel, cfg.Provider, cfg.Model)
	memoryOptions := make([]memory.Option, 0, 2)
	memoryOptions = append(memoryOptions, memory.WithVectorIndex(semanticService))
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
	// Capture Job 同时负责消息向量化；即使关闭自动记忆，也必须保留此持久化 Worker。
	memoryQueue, err := memory.NewCaptureQueue(database)
	if err != nil {
		return err
	}
	runtimeProfiles := make([]chat.RuntimeProfile, 0, len(cfg.ModelProfiles))
	var defaultRuntime *agentruntime.Runtime
	for _, profile := range cfg.ModelProfiles {
		profileConfig := configForModel(cfg, profile)
		profileChatModel := chatModel
		if profile.ID != cfg.DefaultModelID {
			profileChatModel, err = agentruntime.NewChatModel(context.Background(), profileConfig)
			if err != nil {
				return fmt.Errorf("创建模型配置 %s 失败：%w", profile.ID, err)
			}
			profileChatModel = telemetry.WrapChatModel(profileChatModel, profile.Provider, profile.Model)
		}
		var profileRuntime *agentruntime.Runtime
		if cfg.MultiAgentEnabled {
			// Supervisor 只看得到三个专业 Agent；文件、邮件、日历等 MCP 只读工具归 Document，不向 Research 扩权。
			profileRuntime, err = agentruntime.NewMultiAgentWithModel(context.Background(), profileConfig, agentruntime.SpecialistToolset{
				Research: registeredTools,
				Document: append([]tool.BaseTool{knowledgeTool}, mcpTools...),
				Writer:   draftTools,
				Observe:  telemetry.WrapTool,
			}, profileChatModel)
		} else {
			singleAgentTools := append(append([]tool.BaseTool{}, registeredTools...), mcpTools...)
			singleAgentTools = append(singleAgentTools, knowledgeTool)
			singleAgentTools = append(singleAgentTools, draftTools...)
			profileRuntime, err = agentruntime.NewWithModel(context.Background(), profileConfig, singleAgentTools, profileChatModel)
		}
		if err != nil {
			return fmt.Errorf("装配模型配置 %s 的 Agent Runtime 失败：%w", profile.ID, err)
		}
		if profile.ID == cfg.DefaultModelID {
			defaultRuntime = profileRuntime
		}
		runtimeProfiles = append(runtimeProfiles, chat.RuntimeProfile{
			ModelProfile: chat.ModelProfile{
				ID: profile.ID, Name: profile.Name, Provider: profile.Provider, Model: profile.Model,
			},
			Runtime: profileRuntime,
		})
	}
	if defaultRuntime == nil {
		return fmt.Errorf("默认模型配置 %q 未完成 Runtime 装配", cfg.DefaultModelID)
	}
	chatOptions := []chat.Option{
		chat.WithRuntimeProfiles(cfg.DefaultModelID, runtimeProfiles),
		chat.WithTelemetry(telemetry),
	}
	if locker, ok := database.(interface {
		LockConversation(context.Context, string) (func(), error)
	}); ok {
		chatOptions = append(chatOptions, chat.WithConversationLocker(locker))
	}
	if memoryQueue != nil {
		chatOptions = append(chatOptions, chat.WithMemoryCaptureQueue(memoryQueue, cfg.MemoryWorkerMaxAttempts))
	}
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
	if cfg.MessageRecallEnabled {
		chatOptions = append(chatOptions, chat.WithMessageRecaller(semanticService))
	}
	var summaryService *summary.Service
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
		summaryService, err = summary.NewService(database, summarizer, summary.Options{
			TriggerMessages: cfg.SummaryTriggerMessages,
			KeepRecent:      cfg.SummaryKeepRecent,
			MaxRunes:        cfg.SummaryMaxRunes,
			Model:           cfg.Model,
		})
		if err != nil {
			return err
		}
		chatOptions = append(chatOptions, chat.WithConversationSummarizer(summaryService))
		chatOptions = append(chatOptions, chat.WithSummaryQueue(backgroundQueue, cfg.BackgroundWorkerMaxAttempts))
	}
	chatService := chat.NewService(database, defaultRuntime, chatOptions...)
	var memoryWorker *memory.CaptureWorker
	if memoryQueue != nil {
		memoryWorker, err = memory.NewCaptureWorker(memoryQueue, memoryService, logger, memory.CaptureWorkerOptions{
			PollInterval:    cfg.MemoryWorkerPollInterval,
			LeaseDuration:   cfg.MemoryWorkerLeaseDuration,
			TaskTimeout:     cfg.MemoryWorkerTaskTimeout,
			RetryBase:       cfg.MemoryWorkerRetryBase,
			Instrumentation: telemetry,
			MessageIndexer:  semanticService,
			Observer: func(ctx context.Context, job memory.CaptureJob, result *memory.CaptureResult, _ error) {
				eventType := "memory_capture_completed"
				payload := map[string]any{"job_id": job.ID, "attempt": job.Attempt, "status": job.Status}
				if result != nil {
					payload["candidates"], payload["created"] = result.Candidates, result.Created
					payload["updated"], payload["skipped"] = result.Updated, result.Skipped
				} else if job.Status == memory.JobPending {
					eventType = "memory_capture_retry_scheduled"
					payload["available_at"], payload["error"] = job.AvailableAt, job.LastError
				} else {
					eventType = "memory_capture_failed"
					payload["error"] = job.LastError
				}
				_, appendErr := database.AppendRunEvent(ctx, domain.RunEvent{
					ID: id.New("evt"), RunID: job.RunID, Type: eventType,
					AgentName: defaultRuntime.AgentName(), Payload: payload, CreatedAt: time.Now().UTC(),
				})
				if appendErr != nil {
					logger.Warn("记录长期记忆捕获任务审计事件失败", "任务ID", job.ID, "错误", appendErr)
				}
			},
		})
		if err != nil {
			return err
		}
		if err = memoryWorker.Start(context.Background()); err != nil {
			return err
		}
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if stopErr := memoryWorker.Stop(stopCtx); stopErr != nil {
				logger.Warn("关闭长期记忆 Capture Worker 失败", "错误", stopErr)
			}
		}()
	}
	backgroundWorkers := make([]*background.Worker, 0, 2)
	workerOptions := background.WorkerOptions{
		PollInterval: cfg.BackgroundWorkerPollInterval, LeaseDuration: cfg.BackgroundWorkerLeaseDuration,
		TaskTimeout: cfg.BackgroundWorkerTaskTimeout, RetryBase: cfg.BackgroundWorkerRetryBase,
		Instrumentation: telemetry,
	}
	ingestionWorker, err := background.NewWorker(backgroundQueue, background.KindKnowledgeIngestion, background.KnowledgeHandler(knowledgeService), logger, workerOptions)
	if err != nil {
		return err
	}
	backgroundWorkers = append(backgroundWorkers, ingestionWorker)
	if summaryService != nil {
		summaryOptions := workerOptions
		summaryOptions.Observer = func(ctx context.Context, job background.Job, jobErr error) {
			if job.RunID == "" {
				return
			}
			eventType := "conversation_summary_completed"
			payload := map[string]any{"job_id": job.ID, "status": job.Status, "attempt": job.Attempt}
			if jobErr != nil {
				eventType = "conversation_summary_failed"
				payload["error"] = job.LastError
			}
			_, appendErr := database.AppendRunEvent(ctx, domain.RunEvent{ID: id.New("evt"), RunID: job.RunID, Type: eventType, AgentName: defaultRuntime.AgentName(), Payload: payload, CreatedAt: time.Now().UTC()})
			if appendErr != nil {
				logger.Warn("记录会话摘要后台任务审计事件失败", "任务ID", job.ID, "错误", appendErr)
			}
		}
		summaryWorker, workerErr := background.NewWorker(backgroundQueue, background.KindConversationSummary, background.SummaryHandler(summaryService), logger, summaryOptions)
		if workerErr != nil {
			return workerErr
		}
		backgroundWorkers = append(backgroundWorkers, summaryWorker)
	}
	for index, worker := range backgroundWorkers {
		if err := worker.Start(context.Background()); err != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			for _, started := range backgroundWorkers[:index] {
				_ = started.Stop(stopCtx)
			}
			cancel()
			return err
		}
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, worker := range backgroundWorkers {
			if stopErr := worker.Stop(stopCtx); stopErr != nil {
				logger.Warn("关闭后台任务 Worker 失败", "错误", stopErr)
			}
		}
	}()
	httpOptions := make([]httpapi.Option, 0, 3)
	if approvalService != nil {
		httpOptions = append(httpOptions, httpapi.WithApprovalService(approvalService))
	}
	mcpToolCount := 0
	if mcpManager != nil {
		mcpToolCount = len(mcpManager.Tools())
	}
	httpOptions = append(httpOptions, httpapi.WithMCPInfo(cfg.MCPEnabled, mcpToolCount))
	httpOptions = append(httpOptions, httpapi.WithOfficeService(officeService))
	httpOptions = append(httpOptions, httpapi.WithTelemetry(telemetry))
	if memoryQueue != nil {
		httpOptions = append(httpOptions, httpapi.WithMemoryCaptureQueue(memoryQueue))
	}
	httpOptions = append(httpOptions, httpapi.WithSemanticService(semanticService))
	httpOptions = append(httpOptions, httpapi.WithBackgroundQueue(backgroundQueue))
	httpOptions = append(httpOptions, httpapi.WithSecurity(securityManager))
	httpOptions = append(httpOptions, httpapi.WithAuth(authManager))
	httpOptions = append(httpOptions, httpapi.WithMetricsToken(cfg.MetricsToken))
	httpOptions = append(httpOptions, httpapi.WithReadinessCheck(func(ctx context.Context) error {
		if err := database.Ping(ctx); err != nil {
			return fmt.Errorf("数据库不可用：%w", err)
		}
		if redisClient != nil {
			if err := redisClient.Ping(ctx).Err(); err != nil {
				return fmt.Errorf("Redis/Tair 不可用：%w", err)
			}
		}
		return nil
	}))
	handler, err := httpapi.New(chatService, knowledgeService, memoryService, logger, cfg.RequestTimeout, httpOptions...)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serveErrors := make(chan error, 1)
	// HTTP 服务放入 goroutine，主 goroutine 同时监听系统信号和异常退出。
	go func() {
		logger.Info("Zora 已就绪", "监听地址", cfg.Addr, "存储", cfg.StoreProvider,
			"模型提供方", cfg.Provider, "模型", cfg.Model,
			"模型配置数", len(cfg.ModelProfiles), "默认模型ID", cfg.DefaultModelID,
			"向量提供方", cfg.EmbeddingProvider, "向量模型", embedder.Name(),
			"自动记忆", cfg.MemoryAutoCapture, "记忆召回", cfg.MemoryRecallEnabled,
			"消息召回", cfg.MessageRecallEnabled,
			"会话摘要", cfg.SummaryEnabled, "多Agent", cfg.MultiAgentEnabled,
			"MCP已启用", cfg.MCPEnabled, "MCP工具数", mcpToolCount,
			"办公执行器", cfg.OfficeExecutor,
			"OTel Trace", cfg.OTelEnabled, "Prometheus", cfg.PrometheusEnabled,
			"GitHub登录", cfg.AuthEnabled, "副本数", cfg.ReplicaCount)
		serveErrors <- server.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-signals:
		logger.Info("正在关闭服务", "信号", sig.String())
	case err := <-serveErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	// 滚动升级时尽量让正在执行的 Agent/SSE 在请求预算内完成；上限与 Kubernetes 120 秒终止窗口配合。
	shutdownTimeout := cfg.RequestTimeout + 10*time.Second
	if shutdownTimeout < 30*time.Second {
		shutdownTimeout = 30 * time.Second
	}
	if shutdownTimeout > 110*time.Second {
		shutdownTimeout = 110 * time.Second
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func buildRedis(ctx context.Context, rawURL string) (*redis.Client, error) {
	if rawURL == "" {
		return nil, nil
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("解析 Redis 连接地址失败：%w", err)
	}
	client := redis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("连接 Redis/Tair 失败：%w", err)
	}
	return client, nil
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
