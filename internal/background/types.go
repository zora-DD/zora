// Package background 提供文档摄取和会话摘要共用的持久化任务状态机。
package background

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/zhiruo/zora/internal/knowledge"
)

const (
	KindKnowledgeIngestion  = "knowledge_ingestion"
	KindConversationSummary = "conversation_summary"
	StatusPending           = "pending"
	StatusExecuting         = "executing"
	StatusCompleted         = "completed"
	StatusFailed            = "failed"
)

var (
	ErrNotFound  = errors.New("后台任务不存在")
	ErrLeaseLost = errors.New("后台任务租约已失效")
)

type Job struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	DedupeKey      string          `json:"-"`
	RunID          string          `json:"run_id,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
	Status         string          `json:"status"`
	Attempt        int             `json:"attempt"`
	MaxAttempts    int             `json:"max_attempts"`
	AvailableAt    time.Time       `json:"available_at"`
	LeaseOwner     string          `json:"-"`
	LeaseUntil     *time.Time      `json:"lease_until,omitempty"`
	LastError      string          `json:"last_error,omitempty"`
	Payload        json.RawMessage `json:"-"`
	Result         json.RawMessage `json:"result,omitempty"`
	TraceParent    string          `json:"-"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	CompletedAt    *time.Time      `json:"completed_at,omitempty"`
}

type Filter struct {
	Kind, Status string
	Limit        int
}

type Store interface {
	EnqueueBackgroundJob(ctx context.Context, job Job) (Job, bool, error)
	GetBackgroundJob(ctx context.Context, id string) (Job, error)
	ListBackgroundJobs(ctx context.Context, filter Filter) ([]Job, error)
	ClaimBackgroundJob(ctx context.Context, kind, workerID string, now, leaseUntil time.Time) (Job, error)
	CompleteBackgroundJob(ctx context.Context, id, leaseOwner string, result json.RawMessage, now time.Time) (Job, error)
	FailBackgroundJob(ctx context.Context, id, leaseOwner, lastError string, retryAt, now time.Time, terminal bool) (Job, error)
	RetryBackgroundJob(ctx context.Context, id string, now time.Time) (Job, error)
}

type KnowledgePayload struct {
	Input knowledge.IngestInput `json:"input"`
}
type SummaryPayload struct {
	ConversationID string `json:"conversation_id"`
	LatestSequence int64  `json:"latest_sequence"`
}

type EnqueueKnowledgeInput struct {
	Input       knowledge.IngestInput
	MaxAttempts int
}
type EnqueueSummaryInput struct {
	RunID, ConversationID string
	LatestSequence        int64
	TraceParent           string
	MaxAttempts           int
}
