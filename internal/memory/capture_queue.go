package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/id"
)

const defaultCaptureJobListLimit = 100

// CaptureEnqueueInput 是 Chat 提交事务 Outbox 时需要的关联信息。
type CaptureEnqueueInput struct {
	RunID          string
	ConversationID string
	UserMessageID  string
	Assistant      domain.Message
	TraceParent    string
	MaxAttempts    int
}

// CaptureQueue 负责创建和查询持久化任务，并用轻量通知缩短 Worker 的轮询等待。
// 通知丢失不会丢任务，数据库始终是真实来源。
type CaptureQueue struct {
	store  CaptureJobStore
	now    func() time.Time
	notify chan struct{}
}

func NewCaptureQueue(store CaptureJobStore) (*CaptureQueue, error) {
	if store == nil {
		return nil, fmt.Errorf("长期记忆捕获任务存储不能为空")
	}
	return &CaptureQueue{
		store: store, now: func() time.Time { return time.Now().UTC() }, notify: make(chan struct{}, 1),
	}, nil
}

func (q *CaptureQueue) Enqueue(ctx context.Context, input CaptureEnqueueInput) (domain.Message, CaptureJob, bool, error) {
	if strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.ConversationID) == "" || strings.TrimSpace(input.UserMessageID) == "" {
		return domain.Message{}, CaptureJob{}, false, fmt.Errorf("长期记忆捕获任务必须关联 Run、会话和用户消息")
	}
	if input.Assistant.ID == "" || input.Assistant.Role != domain.RoleAssistant || strings.TrimSpace(input.Assistant.Content) == "" {
		return domain.Message{}, CaptureJob{}, false, fmt.Errorf("长期记忆捕获任务必须关联有效的助手消息")
	}
	if input.MaxAttempts < 1 {
		return domain.Message{}, CaptureJob{}, false, fmt.Errorf("长期记忆捕获任务最大尝试次数必须大于 0")
	}
	now := q.now()
	job := CaptureJob{
		ID: id.New("memory_job"), RunID: input.RunID, ConversationID: input.ConversationID,
		UserMessageID: input.UserMessageID, AssistantMessageID: input.Assistant.ID,
		Status: JobPending, MaxAttempts: input.MaxAttempts, AvailableAt: now,
		TraceParent: strings.TrimSpace(input.TraceParent), CreatedAt: now, UpdatedAt: now,
	}
	message, saved, created, err := q.store.EnqueueCaptureJob(ctx, input.Assistant, job)
	if err != nil {
		return domain.Message{}, CaptureJob{}, false, err
	}
	q.Wake()
	return message, saved, created, nil
}

func (q *CaptureQueue) Get(ctx context.Context, id string) (CaptureJob, error) {
	if strings.TrimSpace(id) == "" {
		return CaptureJob{}, fmt.Errorf("长期记忆捕获任务 ID 不能为空")
	}
	return q.store.GetCaptureJob(ctx, id)
}

func (q *CaptureQueue) List(ctx context.Context, filter CaptureJobFilter) ([]CaptureJob, error) {
	if filter.Status != "" && !validJobStatus(filter.Status) {
		return nil, fmt.Errorf("不支持的长期记忆捕获任务状态：%s", filter.Status)
	}
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = defaultCaptureJobListLimit
	}
	return q.store.ListCaptureJobs(ctx, filter)
}

func (q *CaptureQueue) Wake() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func validJobStatus(status string) bool {
	switch status {
	case JobPending, JobExecuting, JobCompleted, JobFailed:
		return true
	default:
		return false
	}
}
