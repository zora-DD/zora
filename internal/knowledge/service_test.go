package knowledge_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/store/sqlite"
)

func TestServiceIngestSearchDeduplicateAndDelete(t *testing.T) {
	t.Parallel()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	embedder, err := knowledge.NewHashEmbedder(128)
	if err != nil {
		t.Fatal(err)
	}
	service, err := knowledge.NewService(database, embedder, knowledge.ChunkOptions{MaxRunes: 120, OverlapRunes: 20})
	if err != nil {
		t.Fatal(err)
	}

	content := []byte(strings.Repeat("通用说明：本文档用于验证检索链路。", 8) +
		"\n\n关键事实：蓝鲸项目的发布日是 2026 年 9 月 18 日，上线前必须完成灰度验证。")
	created, err := service.Ingest(context.Background(), knowledge.IngestInput{Name: "release.md", Content: content})
	if err != nil {
		t.Fatal(err)
	}
	if created.Document.ChunkCount < 2 || created.Deduplicated {
		t.Fatalf("unexpected ingest result: %+v", created)
	}
	duplicate, err := service.Ingest(context.Background(), knowledge.IngestInput{Name: "renamed.md", Content: content})
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Deduplicated || duplicate.Document.ID != created.Document.ID {
		t.Fatalf("content hash deduplication failed: %+v", duplicate)
	}
	incompatibleEmbedder, err := knowledge.NewHashEmbedder(256)
	if err != nil {
		t.Fatal(err)
	}
	incompatibleService, err := knowledge.NewService(database, incompatibleEmbedder, knowledge.ChunkOptions{MaxRunes: 120, OverlapRunes: 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := incompatibleService.Ingest(context.Background(), knowledge.IngestInput{Name: "release.md", Content: content}); !errors.Is(err, knowledge.ErrEmbeddingMismatch) {
		t.Fatalf("incompatible re-ingest error = %v, want ErrEmbeddingMismatch", err)
	}

	results, err := service.Search(context.Background(), "蓝鲸项目发布日期和上线要求", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].DocumentName != "release.md" || !strings.Contains(results[0].Content, "2026") {
		t.Fatalf("unexpected search results: %+v", results)
	}
	if results[0].ChunkID == "" || results[0].Ordinal < 0 {
		t.Fatalf("citation information is incomplete: %+v", results[0])
	}

	if err := service.DeleteDocument(context.Background(), created.Document.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteDocument(context.Background(), created.Document.ID); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("second delete error = %v, want ErrNotFound", err)
	}
	results, err = service.Search(context.Background(), "蓝鲸项目", 3)
	if err != nil || len(results) != 0 {
		t.Fatalf("search after delete = %+v, %v", results, err)
	}
}
