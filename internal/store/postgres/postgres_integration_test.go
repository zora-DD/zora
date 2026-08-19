package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/id"
	"github.com/zhiruo/zora/internal/identity"
	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/store"
	"github.com/zhiruo/zora/internal/summary"
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
	summaryItem := summary.Summary{
		ConversationID: conversationID, Content: "PostgreSQL 摘要测试",
		ThroughSequence: message.Sequence, MessageCount: 1, Model: "integration-test", UpdatedAt: now,
	}
	if err := database.UpsertConversationSummary(ctx, summaryItem); err != nil {
		t.Fatal(err)
	}
	loadedSummary, err := database.GetConversationSummary(ctx, conversationID)
	if err != nil || loadedSummary.Content != summaryItem.Content {
		t.Fatalf("get conversation summary = %+v, %v", loadedSummary, err)
	}
	got, err := database.GetConversation(ctx, conversationID)
	if err != nil || got.MessageCount != 1 {
		t.Fatalf("get conversation = %+v, %v", got, err)
	}
	otherCtx := identity.WithPrincipal(ctx, identity.Principal{
		ID: "github:other", TenantID: "github-user:other", Provider: "github", Subject: "other", Username: "other",
	})
	if _, err := database.GetConversation(otherCtx, conversationID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("其他租户不应读取当前会话，实际错误：%v", err)
	}

	memoryItem := memory.Memory{
		ID: id.New("pg_mem"), Kind: memory.KindSemantic,
		Content: "用户主要使用 Go 语言。", Importance: 0.9,
		SourceType: memory.SourceManual, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateMemory(ctx, memoryItem); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.DeleteMemory(context.Background(), memoryItem.ID) })
	memoryItem.Content = "用户主要使用 Go 开发 Agent。"
	memoryItem.UpdatedAt = now.Add(time.Second)
	if err := database.UpdateMemory(ctx, memoryItem); err != nil {
		t.Fatal(err)
	}
	memories, err := database.ListMemories(ctx, memory.ListFilter{Kind: memory.KindSemantic, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var foundMemory bool
	for _, item := range memories {
		if item.ID == memoryItem.ID && item.Content == memoryItem.Content {
			foundMemory = true
		}
	}
	if !foundMemory {
		t.Fatalf("memory not found in PostgreSQL list: %+v", memories)
	}
	if _, err := database.GetMemory(otherCtx, memoryItem.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("其他租户不应读取当前长期记忆，实际错误：%v", err)
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
	otherService, err := knowledge.NewService(database, embedder, knowledge.ChunkOptions{MaxRunes: 300, OverlapRunes: 40})
	if err != nil {
		t.Fatal(err)
	}
	otherDocuments, err := otherService.ListDocuments(otherCtx)
	if err != nil {
		t.Fatal(err)
	}
	for _, document := range otherDocuments {
		if document.ID == ingested.Document.ID {
			t.Fatal("其他租户不应看到当前租户的私有知识库文档")
		}
	}
	// 内容哈希只能在租户/所有者范围内唯一；不同用户上传同一份资料必须互不冲突。
	otherIngested, err := otherService.Ingest(otherCtx, knowledge.IngestInput{Name: "其他用户的北极星计划.md", Content: content})
	if err != nil {
		t.Fatalf("其他租户上传相同内容失败：%v", err)
	}
	t.Cleanup(func() { _ = otherService.DeleteDocument(otherCtx, otherIngested.Document.ID) })
	if otherIngested.Document.ID == ingested.Document.ID {
		t.Fatal("不同租户上传相同内容不应复用同一文档 ID")
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
