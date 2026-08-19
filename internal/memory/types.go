// Package memory 实现可追溯、可由用户控制的长期记忆。
package memory

import (
	"context"
	"errors"
	"time"

	"github.com/zhiruo/zora/internal/domain"
)

var (
	ErrNotFound     = errors.New("长期记忆不存在")
	ErrJobNotFound  = errors.New("长期记忆捕获任务不存在")
	ErrJobLeaseLost = errors.New("长期记忆捕获任务租约已失效")
)

const (
	KindSemantic = "semantic"
	KindEpisodic = "episodic"

	SourceManual       = "manual"
	SourceConversation = "conversation"

	JobPending   = "pending"
	JobExecuting = "executing"
	JobCompleted = "completed"
	JobFailed    = "failed"
)

// Memory 是经过筛选后的长期信息，不等同于原始聊天消息。
// Source 字段用于解释记忆来源；自动提取会关联到具体会话和用户消息。
type Memory struct {
	ID                   string     `json:"id"`
	Kind                 string     `json:"kind"`
	MemoryKey            string     `json:"memory_key,omitempty"`
	Content              string     `json:"content"`
	Importance           float64    `json:"importance"`
	UserEdited           bool       `json:"user_edited"`
	SourceType           string     `json:"source_type"`
	SourceConversationID string     `json:"source_conversation_id,omitempty"`
	SourceMessageID      string     `json:"source_message_id,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	// 以下字段属于可重建的派生索引，不作为业务数据返回给客户端。
	EmbeddingModel      string    `json:"-"`
	EmbeddingDimensions int       `json:"-"`
	IndexVersion        int       `json:"-"`
	Embedding           []float64 `json:"-"`
}

// VectorSearchResult 是 Memory 对语义索引依赖的最小返回契约。
type VectorSearchResult struct {
	MemoryID string
	Score    float64
}

// VectorIndex 由 semantic.Service 实现。Memory 包只声明能力，避免反向依赖具体索引实现。
type VectorIndex interface {
	VectorizeMemories(ctx context.Context, items []Memory) ([]Memory, error)
	SearchMemoryVectors(ctx context.Context, query string, limit int) ([]VectorSearchResult, error)
}

type ListFilter struct {
	Kind           string
	IncludeExpired bool
	Limit          int
}

// Store 只描述长期记忆持久化所需能力，避免 Memory Service 依赖聊天或知识库接口。
type Store interface {
	CreateMemory(ctx context.Context, item Memory) error
	GetMemory(ctx context.Context, id string) (Memory, error)
	GetMemoryByKey(ctx context.Context, kind, memoryKey string) (Memory, error)
	ListMemories(ctx context.Context, filter ListFilter) ([]Memory, error)
	UpdateMemory(ctx context.Context, item Memory) error
	DeleteMemory(ctx context.Context, id string) error
}

type CreateInput struct {
	Kind       string
	Content    string
	Importance *float64
	ExpiresAt  *time.Time
}

type ReplaceInput struct {
	Kind       string
	Content    string
	Importance *float64
	ExpiresAt  *time.Time
}

// Candidate 是提取器输出的待审记忆。MemoryKey 表示同一事实槽位，
// 例如 profile:primary-programming-language；同 Key 的新值会进入冲突合并。
type Candidate struct {
	Kind       string
	MemoryKey  string
	Content    string
	Importance float64
	ExpiresAt  *time.Time
}

type ExtractionInput struct {
	UserContent      string
	AssistantContent string
}

type Extractor interface {
	Extract(ctx context.Context, input ExtractionInput) ([]Candidate, error)
}

type CaptureInput struct {
	ConversationID   string
	UserMessageID    string
	UserContent      string
	AssistantContent string
}

// CaptureResult 可直接进入 Run 审计和 SSE，避免把候选内容暴露到执行轨迹中。
type CaptureResult struct {
	Enabled         bool `json:"enabled"`
	MessagesIndexed int  `json:"messages_indexed,omitempty"`
	Candidates      int  `json:"candidates"`
	Created         int  `json:"created"`
	Updated         int  `json:"updated"`
	Skipped         int  `json:"skipped"`
}

// CaptureJob 是一轮对话的长期记忆异步捕获任务。
// Outbox 只保存消息 ID，不复制用户与助手正文，Worker 处理时再读取原始消息。
type CaptureJob struct {
	ID                 string         `json:"id"`
	TenantID           string         `json:"-"`
	PrincipalID        string         `json:"-"`
	RunID              string         `json:"run_id"`
	ConversationID     string         `json:"conversation_id"`
	UserMessageID      string         `json:"user_message_id"`
	AssistantMessageID string         `json:"assistant_message_id"`
	Status             string         `json:"status"`
	Attempt            int            `json:"attempt"`
	MaxAttempts        int            `json:"max_attempts"`
	AvailableAt        time.Time      `json:"available_at"`
	LeaseOwner         string         `json:"-"`
	LeaseUntil         *time.Time     `json:"lease_until,omitempty"`
	LastError          string         `json:"last_error,omitempty"`
	Result             *CaptureResult `json:"result,omitempty"`
	TraceParent        string         `json:"-"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	CompletedAt        *time.Time     `json:"completed_at,omitempty"`
}

type CaptureJobFilter struct {
	Status string
	Limit  int
}

// CaptureJobStore 定义事务 Outbox 与租约 Worker 所需的最小持久化能力。
// EnqueueCaptureJob 必须在同一事务内保存助手消息和任务，避免进程中断造成任务丢失。
type CaptureJobStore interface {
	EnqueueCaptureJob(ctx context.Context, assistant domain.Message, job CaptureJob) (savedMessage domain.Message, savedJob CaptureJob, created bool, err error)
	GetCaptureJob(ctx context.Context, id string) (CaptureJob, error)
	ListCaptureJobs(ctx context.Context, filter CaptureJobFilter) ([]CaptureJob, error)
	ClaimCaptureJob(ctx context.Context, workerID string, now, leaseUntil time.Time) (CaptureJob, error)
	CompleteCaptureJob(ctx context.Context, id, leaseOwner string, result CaptureResult, now time.Time) (CaptureJob, error)
	FailCaptureJob(ctx context.Context, id, leaseOwner, lastError string, retryAt, now time.Time, terminal bool) (CaptureJob, error)
	GetMessage(ctx context.Context, id string) (domain.Message, error)
}
