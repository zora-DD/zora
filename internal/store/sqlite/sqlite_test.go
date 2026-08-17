package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/store"
)

func TestOpenMigratesV03MemoryColumns(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy-memory.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
CREATE TABLE conversations (id TEXT PRIMARY KEY, title TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE messages (sequence INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE, role TEXT NOT NULL, content TEXT NOT NULL, tool_name TEXT NOT NULL DEFAULT '', tool_call_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE memories (
  id TEXT PRIMARY KEY, kind TEXT NOT NULL, content TEXT NOT NULL, importance REAL NOT NULL,
  source_type TEXT NOT NULL, source_conversation_id TEXT, source_message_id TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL, expires_at TEXT
);`)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	now := time.Now().UTC().Truncate(time.Microsecond)
	item := memory.Memory{
		ID: "mem_migrated", Kind: memory.KindSemantic, MemoryKey: "profile:language",
		Content: "用户偏好 Go。", Importance: 0.8, UserEdited: true,
		SourceType: memory.SourceManual, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateMemory(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetMemory(context.Background(), item.ID)
	if err != nil || got.MemoryKey != item.MemoryKey || !got.UserEdited {
		t.Fatalf("migrated memory = %+v, %v", got, err)
	}
}

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
		ID: "mem_active", Kind: memory.KindSemantic, MemoryKey: "preference:language",
		Content: "偏好 Go", Importance: 0.8, UserEdited: true,
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
	if err != nil || got.Content != active.Content || got.MemoryKey != active.MemoryKey || !got.UserEdited {
		t.Fatalf("updated memory = %+v, %v", got, err)
	}
	if err := database.DeleteMemory(ctx, active.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetMemory(ctx, active.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("get deleted memory error = %v", err)
	}
}
