// Package store 定义可持久化的存储边界。
package store

import (
	"context"
	"errors"
	"time"

	"github.com/zhiruo/zora/internal/domain"
)

// ErrNotFound 在请求的资源不存在时返回。
var ErrNotFound = errors.New("请求的资源不存在")

// Store 是会话服务使用的持久化契约。
// 后续可以用 PostgreSQL 实现替换 SQLite，而不需修改 Agent 代码。
type Store interface {
	Close() error
	CreateConversation(ctx context.Context, conversation domain.Conversation) error
	GetConversation(ctx context.Context, id string) (domain.Conversation, error)
	ListConversations(ctx context.Context, limit int) ([]domain.Conversation, error)
	RenameConversation(ctx context.Context, id, title string) error
	DeleteConversation(ctx context.Context, id string) error

	AddMessage(ctx context.Context, message domain.Message) (domain.Message, error)
	ListMessages(ctx context.Context, conversationID string, limit int) ([]domain.Message, error)

	CreateRun(ctx context.Context, run domain.AgentRun) error
	FinishRun(ctx context.Context, id, status, assistantMessageID, errorMessage string, completedAt time.Time) error
	AppendRunEvent(ctx context.Context, event domain.RunEvent) (domain.RunEvent, error)
	ListRunEvents(ctx context.Context, runID string) ([]domain.RunEvent, error)
}
