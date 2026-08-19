package memory_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/store/sqlite"
)

type captureRunnerStub struct {
	calls        atomic.Int32
	failuresLeft atomic.Int32
}

func (r *captureRunnerStub) Capture(_ context.Context, _ memory.CaptureInput) (memory.CaptureResult, error) {
	r.calls.Add(1)
	if r.failuresLeft.Add(-1) >= 0 {
		return memory.CaptureResult{}, errors.New("临时模型错误")
	}
	return memory.CaptureResult{Enabled: true, Candidates: 1, Created: 1}, nil
}

func TestCaptureWorkerRetriesAndCompletesDurableJob(t *testing.T) {
	t.Parallel()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	conversation := domain.Conversation{ID: "conv_worker", Title: "worker", CreatedAt: now, UpdatedAt: now}
	if err := database.CreateConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	userMessage, err := database.AddMessage(ctx, domain.Message{
		ID: "msg_worker_user", ConversationID: conversation.ID, Role: domain.RoleUser,
		Content: "请记住我主要使用 Go。", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := domain.AgentRun{ID: "run_worker", ConversationID: conversation.ID, UserMessageID: userMessage.ID, Status: domain.RunRunning, Model: "mock", StartedAt: now}
	if err := database.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	queue, err := memory.NewCaptureQueue(database)
	if err != nil {
		t.Fatal(err)
	}
	_, job, _, err := queue.Enqueue(ctx, memory.CaptureEnqueueInput{
		RunID: run.ID, ConversationID: conversation.ID, UserMessageID: userMessage.ID,
		Assistant: domain.Message{
			ID: "msg_worker_assistant", ConversationID: conversation.ID,
			Role: domain.RoleAssistant, Content: "好的。", CreatedAt: now,
		},
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &captureRunnerStub{}
	runner.failuresLeft.Store(1)
	observed := make(chan memory.CaptureJob, 4)
	worker, err := memory.NewCaptureWorker(queue, runner, slog.New(slog.NewTextHandler(io.Discard, nil)), memory.CaptureWorkerOptions{
		PollInterval:  5 * time.Millisecond,
		TaskTimeout:   time.Second,
		LeaseDuration: 2 * time.Second,
		RetryBase:     10 * time.Millisecond,
		Observer: func(_ context.Context, job memory.CaptureJob, _ *memory.CaptureResult, _ error) {
			observed <- job
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := worker.Stop(stopCtx); err != nil {
			t.Errorf("stop worker: %v", err)
		}
	}()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case state := <-observed:
			if state.Status != memory.JobCompleted {
				continue
			}
			stored, err := queue.Get(ctx, job.ID)
			if err != nil || stored.Attempt != 2 || stored.Result == nil || stored.Result.Created != 1 {
				t.Fatalf("stored job = %+v, %v", stored, err)
			}
			if runner.calls.Load() != 2 {
				t.Fatalf("capture calls = %d, want 2", runner.calls.Load())
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for capture worker")
		}
	}
}
