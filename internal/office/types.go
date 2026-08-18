// Package office 实现办公草稿的结构化校验、持久化和 Agent 工具。
// 当前包只管理 Zora 内部预览，不包含任何外部发送、创建或修改操作。
package office

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrStateConflict = errors.New("草稿状态冲突")

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

// Store 由 SQLite/PostgreSQL 实现；SaveDraft 通过 Run + 内容哈希提供重试幂等性。
type Store interface {
	SaveDraft(ctx context.Context, draft Draft) (saved Draft, created bool, err error)
	GetDraft(ctx context.Context, id string) (Draft, error)
	ListDrafts(ctx context.Context, filter ListFilter) ([]Draft, error)
	DeleteDraft(ctx context.Context, id string) error
	TransitionDraft(ctx context.Context, id, expectedStatus, nextStatus string, event DraftEvent) (Draft, error)
	ListDraftEvents(ctx context.Context, draftID string) ([]DraftEvent, error)
}
