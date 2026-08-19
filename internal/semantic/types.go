// Package semantic 为聊天消息和长期记忆提供彼此隔离的向量索引。
// 知识库 Chunk 仍由 knowledge 包管理，三类实体不会混入同一集合。
package semantic

import (
	"context"
	"errors"
	"time"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/memory"
)

const CurrentIndexVersion = 1

var ErrEmbeddingMismatch = errors.New("语义索引的模型、维度或版本不匹配")

// MessageEmbedding 和 MemoryEmbedding 明确保存集合边界与索引元数据。
// Embedding 是派生数据，不会通过 HTTP 返回。
type MessageEmbedding struct {
	MessageID           string
	ConversationID      string
	Role                string
	EmbeddingModel      string
	EmbeddingDimensions int
	IndexVersion        int
	Embedding           []float64
	UpdatedAt           time.Time
}

type MemoryEmbedding struct {
	MemoryID            string
	Kind                string
	EmbeddingModel      string
	EmbeddingDimensions int
	IndexVersion        int
	Embedding           []float64
	UpdatedAt           time.Time
}

type MessageSearchRequest struct {
	QueryVector         []float64
	EmbeddingModel      string
	EmbeddingDimensions int
	IndexVersion        int
	ExcludeConversation string
	Role                string
	Limit               int
}

type MemorySearchRequest struct {
	QueryVector         []float64
	EmbeddingModel      string
	EmbeddingDimensions int
	IndexVersion        int
	Limit               int
}

type MessageHit struct {
	Message domain.Message `json:"message"`
	Score   float64        `json:"score"`
}

type MemoryHit struct {
	Memory memory.Memory `json:"memory"`
	Score  float64       `json:"score"`
}

type ReindexResult struct {
	Messages int `json:"messages"`
	Memories int `json:"memories"`
}

// Store 通过两组独立方法强制消息和长期记忆落入不同物理索引。
type Store interface {
	UpsertMessageEmbeddings(ctx context.Context, items []MessageEmbedding) error
	UpsertMemoryEmbeddings(ctx context.Context, items []MemoryEmbedding) error
	SearchMessageEmbeddings(ctx context.Context, request MessageSearchRequest) ([]MessageHit, error)
	SearchMemoryEmbeddings(ctx context.Context, request MemorySearchRequest) ([]MemoryHit, error)
	ListMessagesForEmbedding(ctx context.Context, afterSequence int64, limit int) ([]domain.Message, error)
	ListMemoriesForEmbedding(ctx context.Context, afterID string, limit int) ([]memory.Memory, error)
}
