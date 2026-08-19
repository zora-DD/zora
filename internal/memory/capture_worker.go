package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/id"
)

const maxCaptureJobErrorRunes = 1_000

type captureRunner interface {
	Capture(ctx context.Context, input CaptureInput) (CaptureResult, error)
}

// CaptureJobInstrumentation 将异步任务接回可观察性系统；实现不得记录消息正文。
type CaptureJobInstrumentation interface {
	BeginMemoryCaptureJob(ctx context.Context, job CaptureJob) (context.Context, func(status string, err error))
}

type MessageIndexer interface {
	IndexMessages(ctx context.Context, messages []domain.Message) error
}

// CaptureJobObserver 把 Worker 终态回写到原 Agent Run 的追加式审计日志。
type CaptureJobObserver func(ctx context.Context, job CaptureJob, result *CaptureResult, jobErr error)

type CaptureWorkerOptions struct {
	PollInterval    time.Duration
	LeaseDuration   time.Duration
	TaskTimeout     time.Duration
	RetryBase       time.Duration
	Observer        CaptureJobObserver
	Instrumentation CaptureJobInstrumentation
	MessageIndexer  MessageIndexer
}

// CaptureWorker 以数据库租约领取任务。单个进程串行执行以避免同一事实槽位并发合并；
// PostgreSQL 的 Claim 实现允许多副本安全领取不同任务，租约过期后任务可自动恢复。
type CaptureWorker struct {
	queue    *CaptureQueue
	store    CaptureJobStore
	runner   captureRunner
	logger   *slog.Logger
	options  CaptureWorkerOptions
	workerID string

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewCaptureWorker(queue *CaptureQueue, runner captureRunner, logger *slog.Logger, options CaptureWorkerOptions) (*CaptureWorker, error) {
	if queue == nil || runner == nil {
		return nil, fmt.Errorf("长期记忆 Capture Queue 和执行器不能为空")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if options.PollInterval <= 0 {
		options.PollInterval = time.Second
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = 2 * time.Minute
	}
	if options.TaskTimeout <= 0 {
		options.TaskTimeout = 90 * time.Second
	}
	if options.LeaseDuration <= options.TaskTimeout {
		return nil, fmt.Errorf("长期记忆任务租约必须大于单任务超时时间")
	}
	if options.RetryBase <= 0 {
		options.RetryBase = 2 * time.Second
	}
	return &CaptureWorker{
		queue: queue, store: queue.store, runner: runner, logger: logger,
		options: options, workerID: id.New("memory_worker"),
	}, nil
}

func (w *CaptureWorker) Start(parent context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return fmt.Errorf("长期记忆 Capture Worker 已经启动")
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	w.done = make(chan struct{})
	go w.loop(ctx)
	return nil
}

func (w *CaptureWorker) Stop(ctx context.Context) error {
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
		return fmt.Errorf("等待长期记忆 Capture Worker 停止失败：%w", ctx.Err())
	}
}

func (w *CaptureWorker) loop(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.processNext(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.logger.Error("长期记忆异步捕获任务处理失败", "错误", err)
		}
		if worked {
			// 队列还有任务时立即继续领取，不额外等待下一次 Tick。
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.queue.notify:
		}
	}
}

func (w *CaptureWorker) processNext(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	job, err := w.store.ClaimCaptureJob(ctx, w.workerID, now, now.Add(w.options.LeaseDuration))
	if errors.Is(err, ErrJobNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("领取长期记忆捕获任务失败：%w", err)
	}

	jobCtx, cancel := context.WithTimeout(ctx, w.options.TaskTimeout)
	defer cancel()
	finishInstrumentation := func(string, error) {}
	if w.options.Instrumentation != nil {
		jobCtx, finishInstrumentation = w.options.Instrumentation.BeginMemoryCaptureJob(jobCtx, job)
	}
	result, captureErr := w.capture(jobCtx, job)
	if captureErr == nil {
		persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		completed, completeErr := w.store.CompleteCaptureJob(persistCtx, job.ID, job.LeaseOwner, result, time.Now().UTC())
		cancelPersist()
		if completeErr != nil {
			finishInstrumentation(JobFailed, completeErr)
			return true, fmt.Errorf("提交长期记忆捕获任务完成状态失败：%w", completeErr)
		}
		finishInstrumentation(JobCompleted, nil)
		w.observe(completed, &result, nil)
		return true, nil
	}

	terminal := job.Attempt >= job.MaxAttempts
	status := JobPending
	if terminal {
		status = JobFailed
	}
	retryAt := time.Now().UTC().Add(w.retryDelay(job.Attempt))
	message := truncateCaptureJobError(captureErr.Error())
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	failed, finishErr := w.store.FailCaptureJob(
		persistCtx, job.ID, job.LeaseOwner, message, retryAt, time.Now().UTC(), terminal,
	)
	cancelPersist()
	if finishErr != nil {
		finishInstrumentation(JobFailed, finishErr)
		return true, fmt.Errorf("提交长期记忆捕获任务失败状态出错：%w", finishErr)
	}
	finishInstrumentation(status, captureErr)
	w.observe(failed, nil, captureErr)
	if !terminal {
		w.queue.Wake()
	}
	return true, nil
}

func (w *CaptureWorker) capture(ctx context.Context, job CaptureJob) (CaptureResult, error) {
	userMessage, err := w.store.GetMessage(ctx, job.UserMessageID)
	if err != nil {
		return CaptureResult{}, fmt.Errorf("读取长期记忆任务的用户消息失败：%w", err)
	}
	assistantMessage, err := w.store.GetMessage(ctx, job.AssistantMessageID)
	if err != nil {
		return CaptureResult{}, fmt.Errorf("读取长期记忆任务的助手消息失败：%w", err)
	}
	if userMessage.ConversationID != job.ConversationID || assistantMessage.ConversationID != job.ConversationID ||
		userMessage.Role != domain.RoleUser || assistantMessage.Role != domain.RoleAssistant {
		return CaptureResult{}, fmt.Errorf("长期记忆任务关联的消息类型或会话不一致")
	}
	if w.options.MessageIndexer != nil {
		if err := w.options.MessageIndexer.IndexMessages(ctx, []domain.Message{userMessage, assistantMessage}); err != nil {
			return CaptureResult{}, fmt.Errorf("更新消息向量索引失败：%w", err)
		}
	}
	result, err := w.runner.Capture(ctx, CaptureInput{
		ConversationID: job.ConversationID, UserMessageID: userMessage.ID,
		UserContent: userMessage.Content, AssistantContent: assistantMessage.Content,
	})
	if err == nil && w.options.MessageIndexer != nil {
		result.MessagesIndexed = 2
	}
	return result, err
}

func (w *CaptureWorker) retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := w.options.RetryBase
	for current := 1; current < attempt && delay < time.Minute; current++ {
		delay *= 2
	}
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}

func (w *CaptureWorker) observe(job CaptureJob, result *CaptureResult, jobErr error) {
	if w.options.Observer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	w.options.Observer(ctx, job, result, jobErr)
}

func truncateCaptureJobError(value string) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maxCaptureJobErrorRunes {
		return value
	}
	return string([]rune(value)[:maxCaptureJobErrorRunes]) + "…"
}
