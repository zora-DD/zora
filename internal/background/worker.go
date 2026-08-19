package background

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zhiruo/zora/internal/id"
	"github.com/zhiruo/zora/internal/identity"
)

type Handler func(ctx context.Context, payload json.RawMessage) (any, error)
type Observer func(ctx context.Context, job Job, jobErr error)

// Instrumentation 把持久化任务接回创建任务时的 Trace，并记录低基数指标。
// background 包只依赖此接口，避免与具体的 OTel/Prometheus 实现耦合。
type Instrumentation interface {
	BeginBackgroundJob(ctx context.Context, job Job) (context.Context, func(status string, err error))
}

type WorkerOptions struct {
	PollInterval, LeaseDuration, TaskTimeout, RetryBase time.Duration
	Observer                                            Observer
	Instrumentation                                     Instrumentation
}

type Worker struct {
	queue    *Queue
	store    Store
	kind     string
	handler  Handler
	logger   *slog.Logger
	options  WorkerOptions
	workerID string
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewWorker(queue *Queue, kind string, handler Handler, logger *slog.Logger, options WorkerOptions) (*Worker, error) {
	if queue == nil || handler == nil {
		return nil, fmt.Errorf("后台任务 Queue 和处理器不能为空")
	}
	if kind != KindKnowledgeIngestion && kind != KindConversationSummary {
		return nil, fmt.Errorf("不支持的后台任务类型：%s", kind)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if options.PollInterval <= 0 {
		options.PollInterval = time.Second
	}
	if options.TaskTimeout <= 0 {
		options.TaskTimeout = 90 * time.Second
	}
	if options.LeaseDuration <= options.TaskTimeout {
		options.LeaseDuration = options.TaskTimeout + 30*time.Second
	}
	if options.RetryBase <= 0 {
		options.RetryBase = 2 * time.Second
	}
	return &Worker{queue: queue, store: queue.store, kind: kind, handler: handler, logger: logger, options: options, workerID: id.New("worker")}, nil
}
func (w *Worker) Start(parent context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return fmt.Errorf("后台任务 Worker 已启动")
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	w.done = make(chan struct{})
	go w.loop(ctx)
	return nil
}
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待后台任务 Worker 停止失败：%w", ctx.Err())
	}
}
func (w *Worker) loop(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.processNext(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.logger.Error("后台任务处理失败", "类型", w.kind, "错误", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.queue.notify[w.kind]:
		}
	}
}
func (w *Worker) processNext(ctx context.Context) (bool, error) {
	now := time.Now().UTC()
	job, err := w.store.ClaimBackgroundJob(ctx, w.kind, w.workerID, now, now.Add(w.options.LeaseDuration))
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	jobCtx, cancel := context.WithTimeout(ctx, w.options.TaskTimeout)
	jobCtx = identity.WithPrincipal(jobCtx, identity.Principal{
		ID: job.PrincipalID, TenantID: job.TenantID, Provider: "job", Subject: job.PrincipalID, Username: job.PrincipalID,
	})
	finishInstrumentation := func(string, error) {}
	if w.options.Instrumentation != nil {
		jobCtx, finishInstrumentation = w.options.Instrumentation.BeginBackgroundJob(jobCtx, job)
	}
	output, runErr := w.handler(jobCtx, job.Payload)
	cancel()
	if runErr == nil {
		encoded, marshalErr := json.Marshal(output)
		if marshalErr != nil {
			runErr = marshalErr
		} else {
			persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			completed, completeErr := w.store.CompleteBackgroundJob(persistCtx, job.ID, job.LeaseOwner, encoded, time.Now().UTC())
			cancelPersist()
			if completeErr != nil {
				finishInstrumentation(StatusFailed, completeErr)
				return true, completeErr
			}
			finishInstrumentation(completed.Status, nil)
			w.observe(completed, nil)
			return true, nil
		}
	}
	terminal := job.Attempt >= job.MaxAttempts
	retryAt := time.Now().UTC().Add(w.retryDelay(job.Attempt))
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	failed, failErr := w.store.FailBackgroundJob(persistCtx, job.ID, job.LeaseOwner, truncateError(runErr.Error()), retryAt, time.Now().UTC(), terminal)
	cancelPersist()
	if failErr != nil {
		finishInstrumentation(StatusFailed, failErr)
		return true, failErr
	}
	finishInstrumentation(failed.Status, runErr)
	w.observe(failed, runErr)
	if !terminal {
		w.queue.Wake(w.kind)
	}
	return true, nil
}
func (w *Worker) retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := w.options.RetryBase
	for current := 1; current < attempt && delay < time.Minute; current++ {
		delay *= 2
	}
	return min(delay, time.Minute)
}
func (w *Worker) observe(job Job, err error) {
	if w.options.Observer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx = identity.WithPrincipal(ctx, identity.Principal{
		ID: job.PrincipalID, TenantID: job.TenantID, Provider: "job", Subject: job.PrincipalID, Username: job.PrincipalID,
	})
	w.options.Observer(ctx, job, err)
}
func truncateError(value string) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= 1000 {
		return value
	}
	return string([]rune(value)[:1000]) + "…"
}
