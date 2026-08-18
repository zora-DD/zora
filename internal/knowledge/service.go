package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	pdf "github.com/ledongthuc/pdf"

	"github.com/zhiruo/zora/internal/id"
)

const (
	maxDocumentBytes  = 5 << 20
	maxExtractedBytes = 20 << 20
	maxSearchChunks   = 10_000
	rrfConstant       = 60.0
)

type Service struct {
	store        Store
	embedder     Embedder
	chunkOptions ChunkOptions
	principalID  string
}

type ServiceOption func(*Service)

// WithPrincipal 设置没有登录系统时的服务端固定主体；客户端不能通过上传参数伪造 owner。
func WithPrincipal(principalID string) ServiceOption {
	return func(service *Service) { service.principalID = strings.TrimSpace(principalID) }
}

func NewService(store Store, embedder Embedder, chunkOptions ChunkOptions, options ...ServiceOption) (*Service, error) {
	if store == nil || embedder == nil {
		return nil, fmt.Errorf("知识库存储和 Embedding 器不能为空")
	}
	if _, err := ChunkText(strings.Repeat("x", 100), chunkOptions); err != nil {
		return nil, fmt.Errorf("分块配置无效：%w", err)
	}
	service := &Service{store: store, embedder: embedder, chunkOptions: chunkOptions, principalID: "local-user"}
	for _, option := range options {
		option(service)
	}
	if service.principalID == "" {
		return nil, fmt.Errorf("知识库主体 ID 不能为空")
	}
	return service, nil
}

func (s *Service) EmbeddingModel() string { return s.embedder.Name() }

func (s *Service) RetrievalBackend() string {
	if _, ok := s.store.(CandidateStore); ok {
		return "postgres-pgvector-fts"
	}
	return "sqlite-exact-scan"
}

func (s *Service) Ingest(ctx context.Context, input IngestInput) (IngestResult, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return IngestResult{}, fmt.Errorf("文档名不能为空")
	}
	if len(input.Content) == 0 || len(input.Content) > maxDocumentBytes {
		return IngestResult{}, fmt.Errorf("文档大小必须在 1 字节至 5 MiB 之间")
	}
	principalID := s.principal(ctx)
	if input.SourceType == "" {
		input.SourceType = "upload"
	}
	if input.MIMEType == "" {
		input.MIMEType = "text/plain"
	}
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	if input.Visibility == "" {
		input.Visibility = VisibilityPrivate
	}
	if input.Visibility != VisibilityPrivate && input.Visibility != VisibilityPublic {
		return IngestResult{}, fmt.Errorf("文档可见性仅支持 private 或 public")
	}

	// 哈希加入 owner 命名空间，使不同主体上传相同私有文件时不会互相泄露去重结果。
	hashBytes := sha256.Sum256(append([]byte(principalID+"\x00"), input.Content...))
	contentHash := hex.EncodeToString(hashBytes[:])
	if existing, err := s.store.GetDocumentByHash(ctx, contentHash); err == nil {
		if existing.OwnerID != principalID {
			return IngestResult{}, ErrAccessDenied
		}
		if existing.EmbeddingModel != s.embedder.Name() || existing.EmbeddingDimensions != s.embedder.Dimensions() {
			return IngestResult{}, fmt.Errorf("%w：请删除后重新上传《%s》以重建索引", ErrEmbeddingMismatch, existing.Name)
		}
		return IngestResult{Document: existing, Deduplicated: true}, nil
	} else if !errors.Is(err, ErrNotFound) {
		return IngestResult{}, err
	}

	text, err := extractText(input.MIMEType, input.Content)
	if err != nil {
		return IngestResult{}, err
	}
	textChunks, err := ChunkText(text, s.chunkOptions)
	if err != nil {
		return IngestResult{}, err
	}
	texts := make([]string, len(textChunks))
	for i := range textChunks {
		texts[i] = textChunks[i].Content
	}
	vectors, err := s.embedder.Embed(ctx, texts)
	if err != nil {
		return IngestResult{}, fmt.Errorf("文档向量化失败：%w", err)
	}
	if len(vectors) != len(textChunks) {
		return IngestResult{}, fmt.Errorf("Embedding 器返回了 %d 个向量，但文档被分为 %d 个分块", len(vectors), len(textChunks))
	}

	now := time.Now().UTC()
	groupDigest := sha256.Sum256([]byte(principalID + "\x00" + strings.ToLower(input.Name)))
	document := Document{
		ID: id.New("doc"), VersionGroupID: "doc_group_" + hex.EncodeToString(groupDigest[:12]),
		Name: input.Name, SourceType: input.SourceType,
		MIMEType: input.MIMEType, ContentHash: contentHash,
		OwnerID: principalID, Visibility: input.Visibility,
		EmbeddingModel: s.embedder.Name(), EmbeddingDimensions: s.embedder.Dimensions(),
		ChunkCount: len(textChunks),
		CreatedAt:  now, UpdatedAt: now,
	}
	chunks := make([]Chunk, len(textChunks))
	for i, textChunk := range textChunks {
		if len(vectors[i]) != s.embedder.Dimensions() {
			return IngestResult{}, fmt.Errorf("第 %d 个分块的向量维度为 %d，但配置期望为 %d", i+1, len(vectors[i]), s.embedder.Dimensions())
		}
		terms, tokenCount := termCounts(textChunk.Content)
		chunks[i] = Chunk{
			ID: id.New("chunk"), DocumentID: document.ID,
			Ordinal: textChunk.Ordinal, Content: textChunk.Content,
			StartRune: textChunk.StartRune, EndRune: textChunk.EndRune,
			EmbeddingModel: s.embedder.Name(), Embedding: vectors[i],
			TermCounts: terms, TokenCount: tokenCount, CreatedAt: now,
		}
	}
	saved, err := s.store.CreateDocument(ctx, document, chunks)
	if err != nil {
		return IngestResult{}, err
	}
	return IngestResult{Document: saved}, nil
}

func (s *Service) ListDocuments(ctx context.Context) ([]Document, error) {
	return s.store.ListDocuments(ctx, s.principal(ctx), false, 200)
}

func (s *Service) DeleteDocument(ctx context.Context, id string) error {
	return s.store.DeleteDocument(ctx, id, s.principal(ctx))
}

func (s *Service) ListDocumentVersions(ctx context.Context, documentID string) ([]Document, error) {
	if strings.TrimSpace(documentID) == "" {
		return nil, fmt.Errorf("文档 ID 不能为空")
	}
	return s.store.ListDocumentVersions(ctx, documentID, s.principal(ctx))
}

func (s *Service) Search(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	return s.SearchWithMode(ctx, query, topK, RetrievalHybrid)
}

// SearchWithMode 主要服务于离线评测，用同一批数据对比向量、关键词和混合召回。
// Agent Tool 始终调用 Search，因此线上默认路径不会被评测参数改变。
func (s *Service) SearchWithMode(ctx context.Context, query string, topK int, mode RetrievalMode) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("检索问题不能为空")
	}
	if topK < 1 {
		topK = 5
	}
	if topK > 20 {
		return nil, fmt.Errorf("top_k 最大为 20")
	}
	if mode != RetrievalHybrid && mode != RetrievalVector && mode != RetrievalKeyword {
		return nil, fmt.Errorf("不支持的检索模式：%q", mode)
	}
	if candidateStore, ok := s.store.(CandidateStore); ok {
		var queryVector []float64
		if mode != RetrievalKeyword {
			vector, err := s.embedQuery(ctx, query)
			if err != nil {
				return nil, err
			}
			queryVector = vector
		}
		queryTermCounts, _ := termCounts(query)
		queryTerms := make([]string, 0, len(queryTermCounts))
		for term := range queryTermCounts {
			queryTerms = append(queryTerms, term)
		}
		slices.Sort(queryTerms)
		candidates, err := candidateStore.SearchCandidates(ctx, CandidateRequest{
			Mode: mode, QueryVector: queryVector, QueryTerms: queryTerms,
			EmbeddingModel: s.embedder.Name(), EmbeddingDimensions: s.embedder.Dimensions(),
			Limit: 50, PrincipalID: s.principal(ctx),
		})
		if err != nil {
			return nil, err
		}
		return rankCandidates(candidates, topK, mode), nil
	}

	chunks, err := s.store.ListChunks(ctx, s.principal(ctx), maxSearchChunks)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return []SearchResult{}, nil
	}
	if mode == RetrievalKeyword {
		return rankSearch(query, nil, chunks, topK, mode), nil
	}

	queryVector, err := s.embedQuery(ctx, query)
	if err != nil {
		return nil, err
	}

	compatible := make([]Chunk, 0, len(chunks))
	// 不同模型或维度的向量不在同一坐标系，绝不能直接比较。
	for _, chunk := range chunks {
		if chunk.EmbeddingModel == s.embedder.Name() && len(chunk.Embedding) == s.embedder.Dimensions() {
			compatible = append(compatible, chunk)
		}
	}
	if len(compatible) == 0 {
		return nil, ErrEmbeddingMismatch
	}
	return rankSearch(query, queryVector, compatible, topK, mode), nil
}

func (s *Service) principal(ctx context.Context) string {
	if value, ok := PrincipalFromContext(ctx); ok {
		return value
	}
	return s.principalID
}

type principalContextKey struct{}

// WithPrincipalContext 供未来鉴权中间件和测试注入已经验证的主体，不接受客户端原始 owner 字段。
func WithPrincipalContext(ctx context.Context, principalID string) context.Context {
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return ctx
	}
	return context.WithValue(ctx, principalContextKey{}, principalID)
}

func PrincipalFromContext(ctx context.Context) (string, bool) {
	value, ok := ctx.Value(principalContextKey{}).(string)
	return strings.TrimSpace(value), ok && strings.TrimSpace(value) != ""
}

func extractText(mimeType string, content []byte) (string, error) {
	mimeType = strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if mimeType != "application/pdf" {
		if !utf8.Valid(content) {
			return "", fmt.Errorf("文档必须是 UTF-8 编码的文本")
		}
		return string(content), nil
	}
	reader, err := pdf.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", fmt.Errorf("解析 PDF 失败：%w", err)
	}
	plain, err := reader.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("提取 PDF 文本失败：%w", err)
	}
	extracted, err := io.ReadAll(io.LimitReader(plain, maxExtractedBytes+1))
	if err != nil {
		return "", fmt.Errorf("读取 PDF 文本失败：%w", err)
	}
	if len(extracted) > maxExtractedBytes {
		return "", fmt.Errorf("PDF 提取文本超过 20 MiB 上限")
	}
	text := strings.TrimSpace(string(extracted))
	if text == "" {
		return "", fmt.Errorf("PDF 未提取到可索引文本；当前基础解析不包含 OCR")
	}
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("PDF 提取结果不是有效的 UTF-8 文本")
	}
	return text, nil
}

func (s *Service) embedQuery(ctx context.Context, query string) ([]float64, error) {
	vectors, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("检索问题向量化失败：%w", err)
	}
	if len(vectors) != 1 || len(vectors[0]) != s.embedder.Dimensions() {
		return nil, fmt.Errorf("Embedding 器返回的检索向量无效")
	}
	return vectors[0], nil
}

type scoredChunk struct {
	chunk        Chunk
	vectorScore  float64
	keywordScore float64
	fusedScore   float64
}

func rankSearch(query string, queryVector []float64, chunks []Chunk, topK int, mode RetrievalMode) []SearchResult {
	items := make([]scoredChunk, len(chunks))
	queryTerms, _ := termCounts(query)
	keywordScores := bm25Scores(queryTerms, chunks)
	for i, chunk := range chunks {
		vectorScore := 0.0
		if len(queryVector) > 0 {
			vectorScore = cosineSimilarity(queryVector, chunk.Embedding)
		}
		items[i] = scoredChunk{
			chunk: chunk, vectorScore: vectorScore, keywordScore: keywordScores[i],
		}
	}

	vectorRanking := append([]scoredChunk(nil), items...)
	slices.SortFunc(vectorRanking, func(a, b scoredChunk) int {
		return compareRank(b.vectorScore, a.vectorScore, a.chunk, b.chunk)
	})
	keywordRanking := append([]scoredChunk(nil), items...)
	slices.SortFunc(keywordRanking, func(a, b scoredChunk) int {
		return compareRank(b.keywordScore, a.keywordScore, a.chunk, b.chunk)
	})

	switch mode {
	case RetrievalHybrid:
		fused := make(map[string]float64, len(items))
		// RRF 只使用名次而不直接相加原始分，避免余弦相似度和 BM25 量纲不一致。
		for rank, item := range vectorRanking[:min(len(vectorRanking), 50)] {
			if item.vectorScore > 0 {
				fused[item.chunk.ID] += 1 / (rrfConstant + float64(rank+1))
			}
		}
		for rank, item := range keywordRanking[:min(len(keywordRanking), 50)] {
			if item.keywordScore > 0 {
				fused[item.chunk.ID] += 1 / (rrfConstant + float64(rank+1))
			}
		}
		for i := range items {
			items[i].fusedScore = fused[items[i].chunk.ID]
		}
	case RetrievalVector:
		for i := range items {
			items[i].fusedScore = items[i].vectorScore
		}
	case RetrievalKeyword:
		for i := range items {
			items[i].fusedScore = items[i].keywordScore
		}
	}

	slices.SortFunc(items, func(a, b scoredChunk) int {
		return compareRank(b.fusedScore, a.fusedScore, a.chunk, b.chunk)
	})

	results := make([]SearchResult, 0, topK)
	for _, item := range items {
		if item.fusedScore <= 0 {
			continue
		}
		results = append(results, SearchResult{
			ChunkID: item.chunk.ID, DocumentID: item.chunk.DocumentID,
			DocumentName: item.chunk.DocumentName, Ordinal: item.chunk.Ordinal,
			Content: item.chunk.Content, StartRune: item.chunk.StartRune, EndRune: item.chunk.EndRune,
			Score: item.fusedScore, VectorScore: item.vectorScore, KeywordScore: item.keywordScore,
		})
		if len(results) == topK {
			break
		}
	}
	return results
}

func rankCandidates(candidates []Candidate, topK int, mode RetrievalMode) []SearchResult {
	items := make([]scoredChunk, 0, len(candidates))
	for _, candidate := range candidates {
		score := 0.0
		switch mode {
		case RetrievalHybrid:
			if candidate.VectorRank > 0 {
				score += 1 / (rrfConstant + float64(candidate.VectorRank))
			}
			if candidate.KeywordRank > 0 {
				score += 1 / (rrfConstant + float64(candidate.KeywordRank))
			}
		case RetrievalVector:
			score = candidate.VectorScore
		case RetrievalKeyword:
			score = candidate.KeywordScore
		}
		items = append(items, scoredChunk{
			chunk: candidate.Chunk, vectorScore: candidate.VectorScore,
			keywordScore: candidate.KeywordScore, fusedScore: score,
		})
	}
	slices.SortFunc(items, func(a, b scoredChunk) int {
		return compareRank(b.fusedScore, a.fusedScore, a.chunk, b.chunk)
	})

	results := make([]SearchResult, 0, min(topK, len(items)))
	for _, item := range items {
		if item.fusedScore <= 0 {
			continue
		}
		results = append(results, SearchResult{
			ChunkID: item.chunk.ID, DocumentID: item.chunk.DocumentID,
			DocumentName: item.chunk.DocumentName, Ordinal: item.chunk.Ordinal,
			Content: item.chunk.Content, StartRune: item.chunk.StartRune, EndRune: item.chunk.EndRune,
			Score: item.fusedScore, VectorScore: item.vectorScore, KeywordScore: item.keywordScore,
		})
		if len(results) == topK {
			break
		}
	}
	return results
}

func compareRank(left, right float64, leftChunk, rightChunk Chunk) int {
	if comparison := compareScore(left, right); comparison != 0 {
		return comparison
	}
	if comparison := strings.Compare(leftChunk.DocumentName, rightChunk.DocumentName); comparison != 0 {
		return comparison
	}
	return leftChunk.Ordinal - rightChunk.Ordinal
}

func bm25Scores(queryTerms map[string]int, chunks []Chunk) []float64 {
	scores := make([]float64, len(chunks))
	if len(queryTerms) == 0 || len(chunks) == 0 {
		return scores
	}
	documentFrequency := make(map[string]int, len(queryTerms))
	var totalLength int
	for _, chunk := range chunks {
		totalLength += chunk.TokenCount
		for term := range queryTerms {
			if chunk.TermCounts[term] > 0 {
				documentFrequency[term]++
			}
		}
	}
	averageLength := float64(totalLength) / float64(len(chunks))
	if averageLength == 0 {
		return scores
	}
	const k1, b = 1.5, 0.75
	// 采用经典 BM25 参数，先保持算法可解释，再通过固定评测集调参。
	for i, chunk := range chunks {
		for term := range queryTerms {
			tf := float64(chunk.TermCounts[term])
			if tf == 0 {
				continue
			}
			df := float64(documentFrequency[term])
			idf := math.Log(1 + (float64(len(chunks))-df+0.5)/(df+0.5))
			denominator := tf + k1*(1-b+b*float64(chunk.TokenCount)/averageLength)
			scores[i] += idf * (tf * (k1 + 1)) / denominator
		}
	}
	return scores
}

func compareScore(left, right float64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
