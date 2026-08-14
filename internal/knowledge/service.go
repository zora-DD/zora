package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhiruo/zora/internal/id"
)

const (
	maxDocumentBytes = 5 << 20
	maxSearchChunks  = 10_000
	rrfConstant      = 60.0
)

type Service struct {
	store        Store
	embedder     Embedder
	chunkOptions ChunkOptions
}

func NewService(store Store, embedder Embedder, chunkOptions ChunkOptions) (*Service, error) {
	if store == nil || embedder == nil {
		return nil, fmt.Errorf("知识库存储和 Embedding 器不能为空")
	}
	if _, err := ChunkText(strings.Repeat("x", 100), chunkOptions); err != nil {
		return nil, fmt.Errorf("分块配置无效：%w", err)
	}
	return &Service{store: store, embedder: embedder, chunkOptions: chunkOptions}, nil
}

func (s *Service) EmbeddingModel() string { return s.embedder.Name() }

func (s *Service) Ingest(ctx context.Context, input IngestInput) (IngestResult, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return IngestResult{}, fmt.Errorf("文档名不能为空")
	}
	if len(input.Content) == 0 || len(input.Content) > maxDocumentBytes {
		return IngestResult{}, fmt.Errorf("文档大小必须在 1 字节至 5 MiB 之间")
	}
	if !utf8.Valid(input.Content) {
		return IngestResult{}, fmt.Errorf("文档必须是 UTF-8 编码的文本")
	}
	if input.SourceType == "" {
		input.SourceType = "upload"
	}
	if input.MIMEType == "" {
		input.MIMEType = "text/plain"
	}

	hashBytes := sha256.Sum256(input.Content)
	contentHash := hex.EncodeToString(hashBytes[:])
	if existing, err := s.store.GetDocumentByHash(ctx, contentHash); err == nil {
		if existing.EmbeddingModel != s.embedder.Name() || existing.EmbeddingDimensions != s.embedder.Dimensions() {
			return IngestResult{}, fmt.Errorf("%w：请删除后重新上传《%s》以重建索引", ErrEmbeddingMismatch, existing.Name)
		}
		return IngestResult{Document: existing, Deduplicated: true}, nil
	} else if !errors.Is(err, ErrNotFound) {
		return IngestResult{}, err
	}

	textChunks, err := ChunkText(string(input.Content), s.chunkOptions)
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
	document := Document{
		ID: id.New("doc"), Name: input.Name, SourceType: input.SourceType,
		MIMEType: input.MIMEType, ContentHash: contentHash,
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
	if err := s.store.CreateDocument(ctx, document, chunks); err != nil {
		return IngestResult{}, err
	}
	return IngestResult{Document: document}, nil
}

func (s *Service) ListDocuments(ctx context.Context) ([]Document, error) {
	return s.store.ListDocuments(ctx, 200)
}

func (s *Service) DeleteDocument(ctx context.Context, id string) error {
	return s.store.DeleteDocument(ctx, id)
}

func (s *Service) Search(ctx context.Context, query string, topK int) ([]SearchResult, error) {
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

	vectors, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("检索问题向量化失败：%w", err)
	}
	if len(vectors) != 1 || len(vectors[0]) != s.embedder.Dimensions() {
		return nil, fmt.Errorf("Embedding 器返回的检索向量无效")
	}
	chunks, err := s.store.ListChunks(ctx, maxSearchChunks)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return []SearchResult{}, nil
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
	return hybridSearch(query, vectors[0], compatible, topK), nil
}

type scoredChunk struct {
	chunk        Chunk
	vectorScore  float64
	keywordScore float64
	fusedScore   float64
}

func hybridSearch(query string, queryVector []float64, chunks []Chunk, topK int) []SearchResult {
	items := make([]scoredChunk, len(chunks))
	queryTerms, _ := termCounts(query)
	keywordScores := bm25Scores(queryTerms, chunks)
	for i, chunk := range chunks {
		items[i] = scoredChunk{
			chunk: chunk, vectorScore: cosineSimilarity(queryVector, chunk.Embedding),
			keywordScore: keywordScores[i],
		}
	}

	vectorRanking := append([]scoredChunk(nil), items...)
	slices.SortFunc(vectorRanking, func(a, b scoredChunk) int {
		return compareScore(b.vectorScore, a.vectorScore)
	})
	keywordRanking := append([]scoredChunk(nil), items...)
	slices.SortFunc(keywordRanking, func(a, b scoredChunk) int {
		return compareScore(b.keywordScore, a.keywordScore)
	})

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
	slices.SortFunc(items, func(a, b scoredChunk) int {
		if comparison := compareScore(b.fusedScore, a.fusedScore); comparison != 0 {
			return comparison
		}
		return compareScore(b.vectorScore, a.vectorScore)
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
