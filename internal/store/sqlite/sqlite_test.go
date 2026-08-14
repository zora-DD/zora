package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/store"
)

func TestConversationLifecycle(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	conversation := domain.Conversation{ID: "conv_1", Title: "test", CreatedAt: now, UpdatedAt: now}
	if err := database.CreateConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	message, err := database.AddMessage(ctx, domain.Message{
		ID: "msg_1", ConversationID: conversation.ID, Role: domain.RoleUser,
		Content: "hello", CreatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if message.Sequence == 0 {
		t.Fatal("message sequence was not assigned")
	}

	got, err := database.GetConversation(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MessageCount != 1 {
		t.Fatalf("message count = %d, want 1", got.MessageCount)
	}
	if err := database.DeleteConversation(ctx, conversation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetConversation(ctx, conversation.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get deleted conversation error = %v, want ErrNotFound", err)
	}
}
