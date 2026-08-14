// zora-eval 在隔离的临时数据库上运行固定 RAG 评测集，不读写在线服务数据。
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

	"github.com/zhiruo/zora/internal/config"
	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/rageval"
	"github.com/zhiruo/zora/internal/store/sqlite"
)

var errThresholdNotMet = errors.New("混合检索指标未达到评测集阈值")

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		logger.Error("RAG 评测失败", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("zora-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	datasetPath := flags.String("dataset", "./evals/knowledge.json", "RAG 评测集 JSON 文件")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("解析评测命令参数失败：%w", err)
	}

	file, err := os.Open(*datasetPath)
	if err != nil {
		return fmt.Errorf("打开评测集失败：%w", err)
	}
	dataset, err := rageval.LoadDataset(file)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return fmt.Errorf("关闭评测集失败：%w", closeErr)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	embedder, err := knowledge.NewEmbedder(knowledge.EmbedderConfig{
		Provider: cfg.EmbeddingProvider, APIKey: cfg.EmbeddingAPIKey,
		BaseURL: cfg.EmbeddingBaseURL, Model: cfg.EmbeddingModel,
		Dimensions: cfg.EmbeddingDimensions,
		HTTPClient: &http.Client{Timeout: cfg.RequestTimeout},
	})
	if err != nil {
		return err
	}

	// 每次评测都重新摄取固定语料，避免本机历史文档污染指标。
	temporaryDir, err := os.MkdirTemp("", "zora-rag-eval-")
	if err != nil {
		return fmt.Errorf("创建评测临时目录失败：%w", err)
	}
	defer os.RemoveAll(temporaryDir)
	database, err := sqlite.Open(filepath.Join(temporaryDir, "evaluation.db"))
	if err != nil {
		return err
	}
	defer database.Close()

	service, err := knowledge.NewService(database, embedder, knowledge.ChunkOptions{
		MaxRunes: cfg.KnowledgeChunkSize, OverlapRunes: cfg.KnowledgeOverlap,
	})
	if err != nil {
		return err
	}
	for _, document := range dataset.Documents {
		mimeType := document.MIMEType
		if mimeType == "" {
			mimeType = "text/markdown"
		}
		if _, err := service.Ingest(ctx, knowledge.IngestInput{
			Name: document.Name, SourceType: "evaluation", MIMEType: mimeType,
			Content: []byte(document.Content),
		}); err != nil {
			return fmt.Errorf("摄取评测文档《%s》失败：%w", document.Name, err)
		}
	}

	report, err := rageval.Evaluate(ctx, service, dataset)
	if err != nil {
		return err
	}
	report.EmbeddingModel = service.EmbeddingModel()
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("输出评测报告失败：%w", err)
	}
	if !report.Passed {
		return errThresholdNotMet
	}
	return nil
}
