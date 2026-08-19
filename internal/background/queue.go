package background

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zhiruo/zora/internal/id"
)

type Queue struct {
	store  Store
	now    func() time.Time
	notify map[string]chan struct{}
}

func NewQueue(store Store) (*Queue, error) {
	if store == nil {
		return nil, fmt.Errorf("后台任务存储不能为空")
	}
	return &Queue{store: store, now: func() time.Time { return time.Now().UTC() }, notify: map[string]chan struct{}{
		KindKnowledgeIngestion: make(chan struct{}, 1), KindConversationSummary: make(chan struct{}, 1),
	}}, nil
}

func (q *Queue) EnqueueKnowledge(ctx context.Context, input EnqueueKnowledgeInput) (Job, bool, error) {
	if len(input.Input.Content) == 0 {
		return Job{}, false, fmt.Errorf("文档摄取任务内容不能为空")
	}
	hash := sha256.Sum256(input.Input.Content)
	payload, err := json.Marshal(KnowledgePayload{Input: input.Input})
	if err != nil {
		return Job{}, false, err
	}
	job := q.newJob(KindKnowledgeIngestion, "content:"+hex.EncodeToString(hash[:]), "", "", payload, "", input.MaxAttempts)
	return q.enqueue(ctx, job)
}

func (q *Queue) EnqueueSummary(ctx context.Context, input EnqueueSummaryInput) (Job, bool, error) {
	if strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.ConversationID) == "" || input.LatestSequence <= 0 {
		return Job{}, false, fmt.Errorf("会话摘要任务必须关联 Run、会话和有效消息序号")
	}
	payload, err := json.Marshal(SummaryPayload{ConversationID: input.ConversationID, LatestSequence: input.LatestSequence})
	if err != nil {
		return Job{}, false, err
	}
	job := q.newJob(KindConversationSummary, "run:"+input.RunID, input.RunID, input.ConversationID, payload, input.TraceParent, input.MaxAttempts)
	return q.enqueue(ctx, job)
}

func (q *Queue) newJob(kind, dedupeKey, runID, conversationID string, payload json.RawMessage, traceParent string, maxAttempts int) Job {
	if maxAttempts < 1 {
		maxAttempts = 5
	}
	now := q.now()
	return Job{ID: id.New("job"), Kind: kind, DedupeKey: dedupeKey, RunID: runID, ConversationID: conversationID,
		Status: StatusPending, MaxAttempts: maxAttempts, AvailableAt: now, Payload: payload,
		TraceParent: strings.TrimSpace(traceParent), CreatedAt: now, UpdatedAt: now}
}

func (q *Queue) enqueue(ctx context.Context, job Job) (Job, bool, error) {
	saved, created, err := q.store.EnqueueBackgroundJob(ctx, job)
	if err != nil {
		return Job{}, false, err
	}
	q.Wake(job.Kind)
	return saved, created, nil
}

func (q *Queue) Get(ctx context.Context, id string) (Job, error) {
	if strings.TrimSpace(id) == "" {
		return Job{}, fmt.Errorf("后台任务 ID 不能为空")
	}
	return q.store.GetBackgroundJob(ctx, id)
}
func (q *Queue) Retry(ctx context.Context, id string) (Job, error) {
	if strings.TrimSpace(id) == "" {
		return Job{}, fmt.Errorf("后台任务 ID 不能为空")
	}
	job, err := q.store.RetryBackgroundJob(ctx, id, q.now())
	if err != nil {
		return Job{}, err
	}
	q.Wake(job.Kind)
	return job, nil
}
func (q *Queue) List(ctx context.Context, filter Filter) ([]Job, error) {
	if filter.Kind != "" && filter.Kind != KindKnowledgeIngestion && filter.Kind != KindConversationSummary {
		return nil, fmt.Errorf("不支持的后台任务类型：%s", filter.Kind)
	}
	if filter.Status != "" && filter.Status != StatusPending && filter.Status != StatusExecuting && filter.Status != StatusCompleted && filter.Status != StatusFailed {
		return nil, fmt.Errorf("不支持的后台任务状态：%s", filter.Status)
	}
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 100
	}
	return q.store.ListBackgroundJobs(ctx, filter)
}
func (q *Queue) Wake(kind string) {
	channel := q.notify[kind]
	if channel == nil {
		return
	}
	select {
	case channel <- struct{}{}:
	default:
	}
}
