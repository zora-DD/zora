// Package summary 实现长对话的增量摘要与持久化。
package summary

import (
	"context"
	"errors"
	"time"

	"github.com/zhiruo/zora/internal/domain"
)

var ErrNotFound = errors.New("会话摘要不存在")

// Summary 记录已经压缩到哪个消息序号，避免同一批历史被反复总结。
type Summary struct {
	ConversationID  string    `json:"conversation_id"`
	Content         string    `json:"content"`
	ThroughSequence int64     `json:"through_sequence"`
	MessageCount    int       `json:"message_count"`
	Model           string    `json:"model"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Store interface {
	GetConversationSummary(ctx context.Context, conversationID string) (Summary, error)
	UpsertConversationSummary(ctx context.Context, item Summary) error
	ListMessagesForSummary(ctx context.Context, conversationID string, afterSequence, throughSequence int64, limit int) ([]domain.Message, error)
}

type SummarizeInput struct {
	PreviousSummary string
	Messages        []domain.Message
}

type Summarizer interface {
	Summarize(ctx context.Context, input SummarizeInput) (string, error)
}

type Options struct {
	TriggerMessages int
	KeepRecent      int
	MaxRunes        int
	Model           string
}

type UpdateResult struct {
	Updated         bool  `json:"updated"`
	ThroughSequence int64 `json:"through_sequence,omitempty"`
	MessageCount    int   `json:"message_count,omitempty"`
	Characters      int   `json:"characters,omitempty"`
}
