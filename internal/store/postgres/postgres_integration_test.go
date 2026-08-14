package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/id"
	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/store"
)

func TestPostgresConversationAndKnowledgeLifecycle(t *testing.T) {
	dsn := os.Getenv("ZORA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("未配置 ZORA_TEST_POSTGRES_DSN，跳过 PostgreSQL 集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := Open(ctx, Config{DSN: dsn, MaxConns: 4, EmbeddingDimensions: 384})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	now := time.Now().UTC().Truncate(time.Microsecond)
	conversationID := id.New("pg_conv")
	conversation := domain.Conversation{ID: conversationID, Title: "PostgreSQL 测试", CreatedAt: now, UpdatedAt: now}
	if err := database.CreateConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.DeleteConversation(context.Background(), conversationID) })
	message, err := database.AddMessage(ctx, domain.Message{
		ID: id.New("pg_msg"), ConversationID: conversationID,
		Role: domain.RoleUser, Content: "测试 PostgreSQL", CreatedAt: now,
	})
	if err != nil || message.Sequence == 0 {
		t.Fatalf("add message = %+v, %v", message, err)
	}
	got, err := database.GetConversation(ctx, conversationID)
	if err != nil || got.MessageCount != 1 {
		t.Fatalf("get conversation = %+v, %v", got, err)
	}

	embedder, err := knowledge.NewHashEmbedder(384)
	if err != nil {
		t.Fatal(err)
	}
	service, err := knowledge.NewService(database, embedder, knowledge.ChunkOptions{MaxRunes: 300, OverlapRunes: 40})
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("北极星项目计划在 2026 年 11 月 8 日发布，上线前必须完成容量压测和回滚演练。")
	ingested, err := service.Ingest(ctx, knowledge.IngestInput{Name: "北极星发布计划.md", Content: content})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.DeleteDocument(context.Background(), ingested.Document.ID) })
	for _, mode := range []knowledge.RetrievalMode{
		knowledge.RetrievalVector, knowledge.RetrievalKeyword, knowledge.RetrievalHybrid,
	} {
		results, err := service.SearchWithMode(ctx, "北极星项目什么时候发布", 3, mode)
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if len(results) == 0 || results[0].DocumentName != "北极星发布计划.md" {
			t.Fatalf("mode %s results: %+v", mode, results)
		}
	}

	if err := database.DeleteConversation(ctx, conversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetConversation(ctx, conversationID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted conversation error = %v", err)
	}
}
