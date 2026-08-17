package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/memory"
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

func TestMemoryLifecycleAndExpiryFilter(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	past := now.Add(-time.Hour)
	active := memory.Memory{
		ID: "mem_active", Kind: memory.KindSemantic, Content: "偏好 Go", Importance: 0.8,
		SourceType: memory.SourceManual, CreatedAt: now, UpdatedAt: now,
	}
	expired := memory.Memory{
		ID: "mem_expired", Kind: memory.KindEpisodic, Content: "旧事件", Importance: 0.4,
		SourceType: memory.SourceManual, CreatedAt: now, UpdatedAt: now, ExpiresAt: &past,
	}
	for _, item := range []memory.Memory{active, expired} {
		if err := database.CreateMemory(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	items, err := database.ListMemories(ctx, memory.ListFilter{Limit: 20})
	if err != nil || len(items) != 1 || items[0].ID != active.ID {
		t.Fatalf("active memories = %+v, %v", items, err)
	}
	all, err := database.ListMemories(ctx, memory.ListFilter{IncludeExpired: true, Limit: 20})
	if err != nil || len(all) != 2 {
		t.Fatalf("all memories = %+v, %v", all, err)
	}

	active.Content = "偏好使用 Go 构建 Agent"
	active.UpdatedAt = now.Add(time.Minute)
	if err := database.UpdateMemory(ctx, active); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetMemory(ctx, active.ID)
	if err != nil || got.Content != active.Content {
		t.Fatalf("updated memory = %+v, %v", got, err)
	}
	if err := database.DeleteMemory(ctx, active.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetMemory(ctx, active.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("get deleted memory error = %v", err)
	}
}
