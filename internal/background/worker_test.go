package background_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/background"
	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/store/sqlite"
	"github.com/zhiruo/zora/internal/summary"
)

func TestKnowledgeAndSummaryJobsRunOnIndependentWorkers(t *testing.T) {
	t.Parallel()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "background.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	embedder, _ := knowledge.NewHashEmbedder(64)
	knowledgeService, err := knowledge.NewService(database, embedder, knowledge.ChunkOptions{MaxRunes: 100, OverlapRunes: 20})
	if err != nil {
		t.Fatal(err)
	}
	summarizer, _ := summary.NewRuleSummarizer(500)
	summaryService, err := summary.NewService(database, summarizer, summary.Options{TriggerMessages: 4, KeepRecent: 2, MaxRunes: 500, Model: "rule"})
	if err != nil {
		t.Fatal(err)
	}
	queue, err := background.NewQueue(database)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	options := background.WorkerOptions{PollInterval: 5 * time.Millisecond, TaskTimeout: time.Second, LeaseDuration: 2 * time.Second, RetryBase: 5 * time.Millisecond}
	ingestionWorker, _ := background.NewWorker(queue, background.KindKnowledgeIngestion, background.KnowledgeHandler(knowledgeService), logger, options)
	summaryWorker, _ := background.NewWorker(queue, background.KindConversationSummary, background.SummaryHandler(summaryService), logger, options)
	ctx := context.Background()
	_ = ingestionWorker.Start(ctx)
	_ = summaryWorker.Start(ctx)
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = ingestionWorker.Stop(stopCtx)
		_ = summaryWorker.Stop(stopCtx)
	})

	ingestion, created, err := queue.EnqueueKnowledge(ctx, background.EnqueueKnowledgeInput{Input: knowledge.IngestInput{Name: "async.md", SourceType: "upload", MIMEType: "text/markdown", Content: []byte("# 异步文档\n发布日期是 2026 年 9 月 18 日。")}, MaxAttempts: 2})
	if err != nil || !created {
		t.Fatalf("enqueue ingestion=%+v created=%v err=%v", ingestion, created, err)
	}
	completed := waitJob(t, queue, ingestion.ID)
	if len(completed.Payload) != 2 || string(completed.Payload) != "{}" {
		t.Fatalf("completed payload was not cleared: %s", completed.Payload)
	}
	var ingestResult knowledge.IngestResult
	if err := json.Unmarshal(completed.Result, &ingestResult); err != nil || ingestResult.Document.Name != "async.md" {
		t.Fatalf("ingest result=%+v err=%v", ingestResult, err)
	}
	_, created, err = queue.EnqueueKnowledge(ctx, background.EnqueueKnowledgeInput{Input: knowledge.IngestInput{Name: "renamed.md", SourceType: "upload", MIMEType: "text/markdown", Content: []byte("# 异步文档\n发布日期是 2026 年 9 月 18 日。")}, MaxAttempts: 2})
	if err != nil || created {
		t.Fatalf("duplicate created=%v err=%v", created, err)
	}

	now := time.Now().UTC()
	conversation := domain.Conversation{ID: "conv_async_summary", Title: "summary", CreatedAt: now, UpdatedAt: now}
	if err := database.CreateConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	var latest int64
	var firstUserID string
	for index := 0; index < 4; index++ {
		message, err := database.AddMessage(ctx, domain.Message{ID: "msg_async_" + string(rune('a'+index)), ConversationID: conversation.ID, Role: []string{domain.RoleUser, domain.RoleAssistant}[index%2], Content: "第一个需求与结论", CreatedAt: now.Add(time.Duration(index) * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		latest = message.Sequence
	}
	firstUserID = "msg_async_a"
	if err := database.CreateRun(ctx, domain.AgentRun{ID: "run_async_summary", ConversationID: conversation.ID, UserMessageID: firstUserID, Status: domain.RunCompleted, Model: "mock", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	summaryJob, _, err := queue.EnqueueSummary(ctx, background.EnqueueSummaryInput{RunID: "run_async_summary", ConversationID: conversation.ID, LatestSequence: latest, MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, summaryJob.ID)
	storedSummary, err := summaryService.Get(ctx, conversation.ID)
	if err != nil || storedSummary.ThroughSequence <= 0 {
		t.Fatalf("summary=%+v err=%v", storedSummary, err)
	}
}

func TestFailedBackgroundJobCanBeRetried(t *testing.T) {
	t.Parallel()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	queue, _ := background.NewQueue(database)
	var succeed atomic.Bool
	handler := func(context.Context, json.RawMessage) (any, error) {
		if !succeed.Load() {
			return nil, errors.New("临时限流")
		}
		return map[string]any{"ok": true}, nil
	}
	worker, _ := background.NewWorker(queue, background.KindKnowledgeIngestion, handler, slog.New(slog.NewTextHandler(io.Discard, nil)), background.WorkerOptions{PollInterval: 5 * time.Millisecond, TaskTimeout: time.Second, LeaseDuration: 2 * time.Second, RetryBase: 5 * time.Millisecond})
	_ = worker.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = worker.Stop(ctx)
	})
	job, _, err := queue.EnqueueKnowledge(context.Background(), background.EnqueueKnowledgeInput{Input: knowledge.IngestInput{Name: "retry.md", SourceType: "upload", MIMEType: "text/markdown", Content: []byte("retry")}, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitJobStatus(t, queue, job.ID, background.StatusFailed)
	succeed.Store(true)
	if _, err := queue.Retry(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	waitJobStatus(t, queue, job.ID, background.StatusCompleted)
}

func waitJob(t *testing.T, queue *background.Queue, id string) background.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := queue.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == background.StatusCompleted {
			return job
		}
		if job.Status == background.StatusFailed {
			t.Fatalf("job failed: %+v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待后台任务完成超时")
	return background.Job{}
}

func waitJobStatus(t *testing.T, queue *background.Queue, id, status string) background.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := queue.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == status {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待后台任务状态 %s 超时", status)
	return background.Job{}
}
