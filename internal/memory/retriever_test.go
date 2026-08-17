package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/memory"
)

func TestRecallCombinesRelevanceImportanceAndRecency(t *testing.T) {
	t.Parallel()
	store := newMemoryStoreStub()
	now := time.Now().UTC()
	for _, item := range []memory.Memory{
		{ID: "language", Kind: memory.KindSemantic, MemoryKey: "profile:programming-language", Content: "用户的主要编程语言是 Go。", Importance: 0.8, SourceType: memory.SourceConversation, CreatedAt: now, UpdatedAt: now},
		{ID: "drink", Kind: memory.KindSemantic, MemoryKey: "preference:drink", Content: "用户喜欢无糖咖啡。", Importance: 1, SourceType: memory.SourceManual, CreatedAt: now, UpdatedAt: now},
		{ID: "old-language", Kind: memory.KindEpisodic, MemoryKey: "event:old-language", Content: "用户曾经学习过 Java 编程语言。", Importance: 0.4, SourceType: memory.SourceConversation, CreatedAt: now.Add(-365 * 24 * time.Hour), UpdatedAt: now.Add(-365 * 24 * time.Hour)},
	} {
		store.items[item.ID] = item
	}
	service, err := memory.NewService(store, memory.WithRecallOptions(2, 0.25))
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.Recall(context.Background(), "我的主要编程语言是什么？")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].Memory.ID != "language" {
		t.Fatalf("unexpected recall results: %+v", results)
	}
	if results[0].Relevance <= 0 || results[0].Score <= results[0].Importance*0.2 {
		t.Fatalf("unexpected score components: %+v", results[0])
	}
	for _, result := range results {
		if result.Memory.ID == "drink" {
			t.Fatalf("unrelated high-importance memory was recalled: %+v", results)
		}
	}
}

func TestRecallSupportsExplicitMemoryOverviewIntent(t *testing.T) {
	t.Parallel()
	store := newMemoryStoreStub()
	now := time.Now().UTC()
	store.items["preference"] = memory.Memory{
		ID: "preference", Kind: memory.KindSemantic, Content: "用户喜欢无糖咖啡。",
		Importance: 0.9, SourceType: memory.SourceManual, CreatedAt: now, UpdatedAt: now,
	}
	service, err := memory.NewService(store, memory.WithRecallOptions(5, 0.2))
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.Recall(context.Background(), "你还记得我的偏好吗？")
	if err != nil || len(results) != 1 {
		t.Fatalf("overview recall = %+v, %v", results, err)
	}
	unrelated, err := service.Recall(context.Background(), "上海今天下雨吗？")
	if err != nil || len(unrelated) != 0 {
		t.Fatalf("unrelated recall = %+v, %v", unrelated, err)
	}
}
