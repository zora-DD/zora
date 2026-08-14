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
	for _, mode := range []knowledge.RetrievalMode{
		knowledge.RetrievalVector,
		knowledge.RetrievalKeyword,
		knowledge.RetrievalHybrid,
	} {
		modeResults, err := service.SearchWithMode(context.Background(), "蓝鲸项目发布日期", 3, mode)
		if err != nil {
			t.Fatalf("search mode %s: %v", mode, err)
		}
		if len(modeResults) == 0 || modeResults[0].DocumentName != "release.md" {
			t.Fatalf("search mode %s returned unexpected results: %+v", mode, modeResults)
		}
	}
	if _, err := service.SearchWithMode(context.Background(), "蓝鲸项目", 3, "unknown"); err == nil {
		t.Fatal("unsupported retrieval mode should fail")
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

type fixedCandidateStore struct {
	knowledge.Store
	candidates []knowledge.Candidate
}

func (s *fixedCandidateStore) SearchCandidates(_ context.Context, request knowledge.CandidateRequest) ([]knowledge.Candidate, error) {
	if request.Limit != 50 || request.EmbeddingModel == "" || len(request.QueryTerms) == 0 {
		return nil, errors.New("candidate request is incomplete")
	}
	return append([]knowledge.Candidate(nil), s.candidates...), nil
}

func TestServiceUsesDatabaseCandidatesAndKeepsRRFInService(t *testing.T) {
	t.Parallel()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "candidate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	store := &fixedCandidateStore{Store: database, candidates: []knowledge.Candidate{
		{
			Chunk:       knowledge.Chunk{ID: "both", DocumentID: "doc-both", DocumentName: "融合命中.md", Content: "融合命中"},
			VectorScore: 0.8, KeywordScore: 0.7, VectorRank: 2, KeywordRank: 1,
		},
		{
			Chunk:       knowledge.Chunk{ID: "vector", DocumentID: "doc-vector", DocumentName: "仅向量.md", Content: "仅向量"},
			VectorScore: 0.9, VectorRank: 1,
		},
	}}
	embedder, err := knowledge.NewHashEmbedder(128)
	if err != nil {
		t.Fatal(err)
	}
	service, err := knowledge.NewService(store, embedder, knowledge.ChunkOptions{MaxRunes: 300, OverlapRunes: 40})
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.SearchWithMode(context.Background(), "融合检索", 2, knowledge.RetrievalHybrid)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].DocumentName != "融合命中.md" {
		t.Fatalf("unexpected candidate ranking: %+v", results)
	}
	if results[0].Score <= results[1].Score || results[0].VectorScore != 0.8 || results[0].KeywordScore != 0.7 {
		t.Fatalf("RRF scores were not preserved: %+v", results)
	}
}
