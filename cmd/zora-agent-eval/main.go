// zora-agent-eval 在隔离 SQLite 数据库中运行多 Agent 路由与协作评测。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/cloudwego/eino/components/tool"

	"github.com/zhiruo/zora/internal/agentruntime"
	"github.com/zhiruo/zora/internal/agentseval"
	"github.com/zhiruo/zora/internal/agenttools"
	"github.com/zhiruo/zora/internal/chat"
	"github.com/zhiruo/zora/internal/config"
	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/store/sqlite"
)

var errThresholdNotMet = errors.New("多 Agent 路由与协作指标未达到评测集阈值")

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		logger.Error("多 Agent 评测失败", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("zora-agent-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	datasetPath := flags.String("dataset", "./evals/agents.json", "多 Agent 路由评测集 JSON 文件")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("解析多 Agent 评测命令参数失败：%w", err)
	}

	file, err := os.Open(*datasetPath)
	if err != nil {
		return fmt.Errorf("打开多 Agent 评测集失败：%w", err)
	}
	dataset, loadErr := agentseval.LoadDataset(file)
	closeErr := file.Close()
	if loadErr != nil {
		return loadErr
	}
	if closeErr != nil {
		return fmt.Errorf("关闭多 Agent 评测集失败：%w", closeErr)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	temporaryDir, err := os.MkdirTemp("", "zora-agent-eval-")
	if err != nil {
		return fmt.Errorf("创建多 Agent 评测临时目录失败：%w", err)
	}
	defer os.RemoveAll(temporaryDir)
	database, err := sqlite.Open(filepath.Join(temporaryDir, "evaluation.db"))
	if err != nil {
		return err
	}
	defer database.Close()

	embedder, err := knowledge.NewEmbedder(knowledge.EmbedderConfig{
		Provider: cfg.EmbeddingProvider, APIKey: cfg.EmbeddingAPIKey,
		BaseURL: cfg.EmbeddingBaseURL, Model: cfg.EmbeddingModel,
		Dimensions: cfg.EmbeddingDimensions,
		HTTPClient: &http.Client{Timeout: cfg.RequestTimeout},
	})
	if err != nil {
		return err
	}
	knowledgeService, err := knowledge.NewService(database, embedder, knowledge.ChunkOptions{
		MaxRunes: cfg.KnowledgeChunkSize, OverlapRunes: cfg.KnowledgeOverlap,
	})
	if err != nil {
		return err
	}
	for _, document := range dataset.Documents {
		if _, err := knowledgeService.Ingest(ctx, knowledge.IngestInput{
			Name: document.Name, SourceType: "evaluation", MIMEType: "text/markdown",
			Content: []byte(document.Content),
		}); err != nil {
			return fmt.Errorf("摄取评测文档 %s 失败：%w", document.Name, err)
		}
	}
	knowledgeTool, err := knowledge.NewSearchTool(knowledgeService)
	if err != nil {
		return err
	}
	researchTools, err := agenttools.Build()
	if err != nil {
		return err
	}
	chatModel, err := agentruntime.NewChatModel(ctx, cfg)
	if err != nil {
		return err
	}
	agentRuntime, err := agentruntime.NewMultiAgentWithModel(ctx, cfg, agentruntime.SpecialistToolset{
		Research: researchTools,
		Document: []tool.BaseTool{knowledgeTool},
	}, chatModel)
	if err != nil {
		return err
	}
	answerer := chatAnswerer{service: chat.NewService(database, agentRuntime)}
	report, err := agentseval.Evaluate(ctx, answerer, dataset)
	if err != nil {
		return err
	}
	report.Provider = cfg.Provider
	report.Model = cfg.Model
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("输出多 Agent 评测报告失败：%w", err)
	}
	if !report.Passed {
		return errThresholdNotMet
	}
	return nil
}

type chatAnswerer struct{ service *chat.Service }

func (a chatAnswerer) Answer(ctx context.Context, question string) (agentseval.GeneratedAnswer, error) {
	conversation, err := a.service.CreateConversation(ctx, "多 Agent 路由评测")
	if err != nil {
		return agentseval.GeneratedAnswer{}, err
	}
	generated := agentseval.GeneratedAnswer{}
	var runID string
	if err := a.service.Send(ctx, conversation.ID, question, func(event chat.StreamEvent) error {
		if event.RunID != "" {
			runID = event.RunID
		}
		if event.Type == "done" && event.Message != nil {
			generated.Content = event.Message.Content
		}
		return nil
	}); err != nil {
		return agentseval.GeneratedAnswer{}, err
	}
	if generated.Content == "" || runID == "" {
		return agentseval.GeneratedAnswer{}, fmt.Errorf("多 Agent 评测未获得完整回答或 Run ID")
	}
	events, err := a.service.ListRunEvents(ctx, runID)
	if err != nil {
		return agentseval.GeneratedAnswer{}, err
	}
	generated.Handoffs, err = handoffsFromEvents(events)
	if err != nil {
		return agentseval.GeneratedAnswer{}, err
	}
	if err := a.service.DeleteConversation(ctx, conversation.ID); err != nil {
		return agentseval.GeneratedAnswer{}, err
	}
	return generated, nil
}

func handoffsFromEvents(events []domain.RunEvent) ([]string, error) {
	started := make(map[string]string)
	completed := make(map[string]struct{})
	handoffs := make([]string, 0)
	agentOutputs := make(map[string]int)
	for _, event := range events {
		callID, _ := event.Payload["tool_call_id"].(string)
		switch event.Type {
		case "agent_handoff_started":
			if event.ToolName == "" || callID == "" {
				return nil, fmt.Errorf("协作开始事件缺少专业 Agent 或 ToolCall ID")
			}
			started[callID] = event.ToolName
			handoffs = append(handoffs, event.ToolName)
		case "agent_handoff_completed":
			if event.ToolName == "" || callID == "" {
				return nil, fmt.Errorf("协作完成事件缺少专业 Agent 或 ToolCall ID")
			}
			completed[callID] = struct{}{}
		case "agent_output":
			agentOutputs[event.AgentName]++
		}
	}
	for callID, agentName := range started {
		if _, ok := completed[callID]; !ok {
			return nil, fmt.Errorf("专业 Agent %s 的协作事件未闭环", agentName)
		}
		if agentOutputs[agentName] == 0 {
			return nil, fmt.Errorf("专业 Agent %s 没有留下输出审计事件", agentName)
		}
	}
	for callID := range completed {
		if _, ok := started[callID]; !ok {
			return nil, fmt.Errorf("协作完成事件 %s 缺少对应的开始事件", callID)
		}
	}
	return handoffs, nil
}
