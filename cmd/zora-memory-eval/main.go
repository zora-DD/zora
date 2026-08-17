// zora-memory-eval 在隔离 SQLite 数据库中运行长期记忆有/无 A/B 评测。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zhiruo/zora/internal/agentruntime"
	"github.com/zhiruo/zora/internal/agenttools"
	"github.com/zhiruo/zora/internal/chat"
	"github.com/zhiruo/zora/internal/config"
	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/memoryeval"
	"github.com/zhiruo/zora/internal/store/sqlite"
)

var errThresholdNotMet = errors.New("长期记忆 A/B 指标未达到评测集阈值")

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		logger.Error("长期记忆 A/B 评测失败", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("zora-memory-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	datasetPath := flags.String("dataset", "./evals/memory.json", "长期记忆 A/B 评测集 JSON 文件")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("解析长期记忆评测命令参数失败：%w", err)
	}

	file, err := os.Open(*datasetPath)
	if err != nil {
		return fmt.Errorf("打开长期记忆评测集失败：%w", err)
	}
	dataset, loadErr := memoryeval.LoadDataset(file)
	closeErr := file.Close()
	if loadErr != nil {
		return loadErr
	}
	if closeErr != nil {
		return fmt.Errorf("关闭长期记忆评测集失败：%w", closeErr)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	temporaryDir, err := os.MkdirTemp("", "zora-memory-eval-")
	if err != nil {
		return fmt.Errorf("创建长期记忆评测临时目录失败：%w", err)
	}
	defer os.RemoveAll(temporaryDir)
	database, err := sqlite.Open(filepath.Join(temporaryDir, "evaluation.db"))
	if err != nil {
		return err
	}
	defer database.Close()

	now := time.Now().UTC()
	for _, fixture := range dataset.Memories {
		if err := database.CreateMemory(ctx, memory.Memory{
			ID: fixture.ID, Kind: fixture.Kind, MemoryKey: fixture.MemoryKey,
			Content: fixture.Content, Importance: fixture.Importance,
			UserEdited: true, SourceType: memory.SourceManual,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return fmt.Errorf("写入评测记忆 %s 失败：%w", fixture.ID, err)
		}
	}

	memoryService, err := memory.NewService(database, memory.WithRecallOptions(dataset.TopK, dataset.RecallMinScore))
	if err != nil {
		return err
	}
	registeredTools, err := agenttools.Build()
	if err != nil {
		return err
	}
	agentRuntime, err := agentruntime.New(ctx, cfg, registeredTools)
	if err != nil {
		return err
	}
	answerer := chatABAnswerer{
		control:   chat.NewService(database, agentRuntime),
		treatment: chat.NewService(database, agentRuntime, chat.WithMemoryRecaller(memoryService)),
	}
	report, err := memoryeval.Evaluate(ctx, answerer, dataset)
	if err != nil {
		return err
	}
	report.Provider = cfg.Provider
	report.Model = cfg.Model
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("输出长期记忆评测报告失败：%w", err)
	}
	if !report.Passed {
		return errThresholdNotMet
	}
	return nil
}

type chatABAnswerer struct {
	control   *chat.Service
	treatment *chat.Service
}

func (a chatABAnswerer) Answer(ctx context.Context, variant, question string) (memoryeval.GeneratedAnswer, error) {
	service := a.control
	if variant == memoryeval.VariantTreatment {
		service = a.treatment
	} else if variant != memoryeval.VariantControl {
		return memoryeval.GeneratedAnswer{}, fmt.Errorf("未知的长期记忆评测变体：%s", variant)
	}
	conversation, err := service.CreateConversation(ctx, "长期记忆 A/B 评测")
	if err != nil {
		return memoryeval.GeneratedAnswer{}, err
	}
	generated := memoryeval.GeneratedAnswer{}
	var runID string
	if err := service.Send(ctx, conversation.ID, question, func(event chat.StreamEvent) error {
		if event.RunID != "" {
			runID = event.RunID
		}
		if event.Type == "done" && event.Message != nil {
			generated.Content = event.Message.Content
		}
		return nil
	}); err != nil {
		return memoryeval.GeneratedAnswer{}, err
	}
	if generated.Content == "" || runID == "" {
		return memoryeval.GeneratedAnswer{}, fmt.Errorf("长期记忆评测未获得完整回答或 Run ID")
	}
	events, err := service.ListRunEvents(ctx, runID)
	if err != nil {
		return memoryeval.GeneratedAnswer{}, err
	}
	generated.RecalledMemoryIDs, err = recalledMemoryIDs(events, variant == memoryeval.VariantTreatment)
	if err != nil {
		return memoryeval.GeneratedAnswer{}, err
	}
	if err := service.DeleteConversation(ctx, conversation.ID); err != nil {
		return memoryeval.GeneratedAnswer{}, err
	}
	return generated, nil
}

func recalledMemoryIDs(events []domain.RunEvent, requireRecallEvent bool) ([]string, error) {
	for _, event := range events {
		switch event.Type {
		case "memory_recall_failed":
			return nil, fmt.Errorf("长期记忆召回失败：%v", event.Payload["error"])
		case "memory_recall_completed":
			payload, err := json.Marshal(event.Payload)
			if err != nil {
				return nil, fmt.Errorf("编码召回审计事件失败：%w", err)
			}
			var decoded struct {
				Matches []struct {
					MemoryID string `json:"memory_id"`
				} `json:"matches"`
			}
			if err := json.Unmarshal(payload, &decoded); err != nil {
				return nil, fmt.Errorf("解析召回审计事件失败：%w", err)
			}
			result := make([]string, 0, len(decoded.Matches))
			for _, match := range decoded.Matches {
				if match.MemoryID != "" {
					result = append(result, match.MemoryID)
				}
			}
			return result, nil
		}
	}
	if requireRecallEvent {
		return nil, fmt.Errorf("开启召回的评测 Run 缺少 memory_recall_completed 审计事件")
	}
	return nil, nil
}
