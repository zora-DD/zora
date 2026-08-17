// Package memory 实现可追溯、可由用户控制的长期记忆。
package memory

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("长期记忆不存在")

const (
	KindSemantic = "semantic"
	KindEpisodic = "episodic"

	SourceManual       = "manual"
	SourceConversation = "conversation"
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
	Enabled    bool `json:"enabled"`
	Candidates int  `json:"candidates"`
	Created    int  `json:"created"`
	Updated    int  `json:"updated"`
	Skipped    int  `json:"skipped"`
}
