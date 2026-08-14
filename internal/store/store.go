// Package store defines durable persistence boundaries.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/zhiruo/zora/internal/domain"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("not found")

// Store is the persistence contract used by the chat service.
// A PostgreSQL implementation can replace SQLite without changing agent code.
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
