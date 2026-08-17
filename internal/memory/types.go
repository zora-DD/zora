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
// Source 字段用于解释记忆来源；自动提取阶段会关联到具体会话和消息。
type Memory struct {
	ID                   string     `json:"id"`
	Kind                 string     `json:"kind"`
	Content              string     `json:"content"`
	Importance           float64    `json:"importance"`
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
