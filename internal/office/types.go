// Package office 实现办公草稿的结构化校验、持久化和 Agent 工具。
// 当前仓库提供受控执行边界，但不内置真实外部写执行器。
package office

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrStateConflict       = errors.New("草稿状态冲突")
	ErrExecutorUnavailable = errors.New("办公执行器尚未配置")
)

const (
	KindEmail    = "email"
	KindCalendar = "calendar"

	StatusDraft               = "draft"
	StatusPendingConfirmation = "pending_confirmation"
	StatusApproved            = "approved"
	StatusExecuting           = "executing"
	StatusCompleted           = "completed"
	StatusRejected            = "rejected"
	StatusFailed              = "failed"
	StatusCancelled           = "cancelled"

	OperationPending   = "pending"
	OperationExecuting = "executing"
	OperationCompleted = "completed"
	OperationFailed    = "failed"
)

// Draft 是准备进入写前确认状态机的不可执行快照。
// Payload 保存规范化 JSON；访问令牌等凭据永远不能写入其中。
type Draft struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	Status         string          `json:"status"`
	ConversationID string          `json:"conversation_id,omitempty"`
	SourceRunID    string          `json:"source_run_id,omitempty"`
	Title          string          `json:"title"`
	Payload        json.RawMessage `json:"payload"`
	ContentHash    string          `json:"content_hash"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type EmailDraft struct {
	To      []string `json:"to"`
	CC      []string `json:"cc,omitempty"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
}

type CalendarDraft struct {
	Attendees []string `json:"attendees,omitempty"`
	Subject   string   `json:"subject"`
	Start     string   `json:"start"`
	End       string   `json:"end"`
	TimeZone  string   `json:"timezone,omitempty"`
	Location  string   `json:"location,omitempty"`
	Body      string   `json:"body,omitempty"`
	IsAllDay  bool     `json:"is_all_day"`
}

type ListFilter struct {
	Kind   string
	Status string
	Limit  int
}

// DraftEvent 记录不可变的草稿状态迁移，供确认过程审计和并发冲突排查。
// Actor 当前固定为 user；接入身份系统后应替换为真实主体 ID。
type DraftEvent struct {
	ID         string    `json:"id"`
	DraftID    string    `json:"draft_id"`
	FromStatus string    `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	Actor      string    `json:"actor"`
	Reason     string    `json:"reason,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Operation 是独立于 Agent Run 的持久化外部写任务。
// IdempotencyKey 在失败重试和进程重启后保持不变，执行器必须据此吸收重复调用。
type Operation struct {
	ID                string     `json:"id"`
	DraftID           string     `json:"draft_id"`
	Kind              string     `json:"kind"`
	Status            string     `json:"status"`
	IdempotencyKey    string     `json:"idempotency_key"`
	ExecutorName      string     `json:"executor_name,omitempty"`
	Attempt           int        `json:"attempt"`
	LeaseOwner        string     `json:"-"`
	LeaseUntil        *time.Time `json:"lease_until,omitempty"`
	ExternalReference string     `json:"external_reference,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

type OperationFilter struct {
	DraftID string
	Status  string
	Limit   int
}

// OperationEvent 是执行任务的追加式审计记录；Attempt 可定位具体一次重试。
type OperationEvent struct {
	ID          string    `json:"id"`
	OperationID string    `json:"operation_id"`
	FromStatus  string    `json:"from_status"`
	ToStatus    string    `json:"to_status"`
	Attempt     int       `json:"attempt"`
	Actor       string    `json:"actor"`
	Reason      string    `json:"reason,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// ExecutionResult 只有在 ExternalEffect=true 时才能把任务提交为 completed。
// ExternalReference 应保存远端邮件或日程 ID，不能保存访问令牌等凭据。
type ExecutionResult struct {
	ExternalEffect    bool
	ExternalReference string
}

// Executor 是真实外部写入的权限边界。实现必须保证同一幂等键可安全重放。
type Executor interface {
	Name() string
	IdempotencySafe() bool
	Execute(ctx context.Context, draft Draft, idempotencyKey string) (ExecutionResult, error)
}

type ExecutionOutcome struct {
	Operation      Operation `json:"operation"`
	Draft          Draft     `json:"draft"`
	ExternalEffect bool      `json:"external_effect"`
}

// Store 由 SQLite/PostgreSQL 实现；SaveDraft 通过 Run + 内容哈希提供重试幂等性。
type Store interface {
	SaveDraft(ctx context.Context, draft Draft) (saved Draft, created bool, err error)
	GetDraft(ctx context.Context, id string) (Draft, error)
	ListDrafts(ctx context.Context, filter ListFilter) ([]Draft, error)
	DeleteDraft(ctx context.Context, id string) error
	TransitionDraft(ctx context.Context, id, expectedStatus, nextStatus string, event DraftEvent) (Draft, error)
	ListDraftEvents(ctx context.Context, draftID string) ([]DraftEvent, error)

	CreateOperation(ctx context.Context, operation Operation, event OperationEvent) (saved Operation, created bool, err error)
	GetOperation(ctx context.Context, id string) (Operation, error)
	GetOperationByDraft(ctx context.Context, draftID string) (Operation, error)
	ListOperations(ctx context.Context, filter OperationFilter) ([]Operation, error)
	ClaimOperation(ctx context.Context, id, executorName, leaseOwner string, now, leaseUntil time.Time, operationEvent OperationEvent, draftEvent DraftEvent) (Operation, Draft, error)
	FinishOperation(ctx context.Context, id, leaseOwner, nextStatus, externalReference, lastError string, now time.Time, operationEvent OperationEvent, draftEvent DraftEvent) (Operation, Draft, error)
	ListOperationEvents(ctx context.Context, operationID string) ([]OperationEvent, error)
	ListExpiredOperations(ctx context.Context, now time.Time, limit int) ([]Operation, error)
}
