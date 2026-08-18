package knowledge_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func TestDocumentVersionOnlySearchesLatestAndCanRollback(t *testing.T) {
	t.Parallel()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "versions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	service := newKnowledgeService(t, database, "alice")

	v1, err := service.Ingest(context.Background(), knowledge.IngestInput{
		Name: "项目计划.md", Content: []byte("旧版本唯一标记 oldversionmarker，发布日期是 2026 年 9 月 1 日。"),
	})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := service.Ingest(context.Background(), knowledge.IngestInput{
		Name: "项目计划.md", Content: []byte("新版本唯一标记 newversionmarker，发布日期调整为 2026 年 10 月 8 日。"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if v1.Document.Version != 1 || v2.Document.Version != 2 || !v2.Document.IsLatest || v1.Document.VersionGroupID != v2.Document.VersionGroupID {
		t.Fatalf("unexpected version chain: v1=%+v v2=%+v", v1.Document, v2.Document)
	}
	versions, err := service.ListDocumentVersions(context.Background(), v2.Document.ID)
	if err != nil || len(versions) != 2 || versions[0].Version != 2 || versions[1].Version != 1 {
		t.Fatalf("versions = %+v, err=%v", versions, err)
	}
	oldResults, err := service.SearchWithMode(context.Background(), "oldversionmarker", 3, knowledge.RetrievalKeyword)
	if err != nil || len(oldResults) != 0 {
		t.Fatalf("old version must not be searchable: %+v, %v", oldResults, err)
	}
	newResults, err := service.SearchWithMode(context.Background(), "newversionmarker", 3, knowledge.RetrievalKeyword)
	if err != nil || len(newResults) != 1 || newResults[0].DocumentID != v2.Document.ID {
		t.Fatalf("latest version search = %+v, %v", newResults, err)
	}

	if err := service.DeleteDocument(context.Background(), v2.Document.ID); err != nil {
		t.Fatal(err)
	}
	documents, err := service.ListDocuments(context.Background())
	if err != nil || len(documents) != 1 || documents[0].ID != v1.Document.ID || !documents[0].IsLatest {
		t.Fatalf("rollback document = %+v, %v", documents, err)
	}
	oldResults, err = service.SearchWithMode(context.Background(), "oldversionmarker", 3, knowledge.RetrievalKeyword)
	if err != nil || len(oldResults) != 1 || oldResults[0].DocumentID != v1.Document.ID {
		t.Fatalf("rollback search = %+v, %v", oldResults, err)
	}
}

func TestDocumentACLFiltersListSearchAndDelete(t *testing.T) {
	t.Parallel()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "acl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	alice := newKnowledgeService(t, database, "alice")
	bob := newKnowledgeService(t, database, "bob")
	privateDocument, err := alice.Ingest(context.Background(), knowledge.IngestInput{
		Name: "私有资料.md", Visibility: knowledge.VisibilityPrivate, Content: []byte("私有标记 aliceprivatemarker"),
	})
	if err != nil {
		t.Fatal(err)
	}
	publicDocument, err := alice.Ingest(context.Background(), knowledge.IngestInput{
		Name: "公开资料.md", Visibility: knowledge.VisibilityPublic, Content: []byte("公开标记 sharedpublicmarker"),
	})
	if err != nil {
		t.Fatal(err)
	}
	documents, err := bob.ListDocuments(context.Background())
	if err != nil || len(documents) != 1 || documents[0].ID != publicDocument.Document.ID {
		t.Fatalf("bob documents = %+v, %v", documents, err)
	}
	privateResults, err := bob.SearchWithMode(context.Background(), "aliceprivatemarker", 3, knowledge.RetrievalKeyword)
	if err != nil || len(privateResults) != 0 {
		t.Fatalf("private search leaked: %+v, %v", privateResults, err)
	}
	publicResults, err := bob.SearchWithMode(context.Background(), "sharedpublicmarker", 3, knowledge.RetrievalKeyword)
	if err != nil || len(publicResults) != 1 || publicResults[0].DocumentID != publicDocument.Document.ID {
		t.Fatalf("public search = %+v, %v", publicResults, err)
	}
	if err := bob.DeleteDocument(context.Background(), publicDocument.Document.ID); !errors.Is(err, knowledge.ErrAccessDenied) {
		t.Fatalf("bob delete public document error = %v, want ErrAccessDenied", err)
	}
	if _, err := bob.ListDocumentVersions(context.Background(), privateDocument.Document.ID); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("bob private version lookup error = %v, want ErrNotFound", err)
	}
}

func TestServiceExtractsTextLayerFromPDF(t *testing.T) {
	t.Parallel()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "pdf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	service := newKnowledgeService(t, database, "alice")
	result, err := service.Ingest(context.Background(), knowledge.IngestInput{
		Name: "release.pdf", MIMEType: "application/pdf",
		Content: minimalPDF("ProjectCodenameNebula release date is 2026-11-09"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Document.MIMEType != "application/pdf" || result.Document.ChunkCount == 0 {
		t.Fatalf("unexpected PDF ingest: %+v", result)
	}
	results, err := service.SearchWithMode(context.Background(), "ProjectCodenameNebula", 3, knowledge.RetrievalKeyword)
	if err != nil || len(results) != 1 || !strings.Contains(results[0].Content, "2026-11-09") {
		t.Fatalf("PDF search = %+v, %v", results, err)
	}
}

func newKnowledgeService(t *testing.T, database *sqlite.SQLite, principalID string) *knowledge.Service {
	t.Helper()
	embedder, err := knowledge.NewHashEmbedder(128)
	if err != nil {
		t.Fatal(err)
	}
	service, err := knowledge.NewService(database, embedder,
		knowledge.ChunkOptions{MaxRunes: 120, OverlapRunes: 20}, knowledge.WithPrincipal(principalID))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// minimalPDF 构造带 Helvetica 文本层和有效 xref 的小型 PDF，避免测试依赖外部命令。
func minimalPDF(text string) []byte {
	escaped := strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text)
	stream := "BT /F1 12 Tf 72 720 Td (" + escaped + ") Tj ET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
	}
	var buffer bytes.Buffer
	buffer.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = buffer.Len()
		fmt.Fprintf(&buffer, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := buffer.Len()
	fmt.Fprintf(&buffer, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for index := 1; index < len(offsets); index++ {
		fmt.Fprintf(&buffer, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&buffer, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return buffer.Bytes()
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
