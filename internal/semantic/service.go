package semantic

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/memory"
)

const defaultBatchSize = 50

type Options struct {
	MessageRecallLimit    int
	MessageRecallMinScore float64
}

type Service struct {
	store                 Store
	embedder              knowledge.Embedder
	messageRecallLimit    int
	messageRecallMinScore float64
	now                   func() time.Time
}

func NewService(store Store, embedder knowledge.Embedder, options Options) (*Service, error) {
	if store == nil || embedder == nil {
		return nil, fmt.Errorf("语义索引存储和 Embedding 服务不能为空")
	}
	if options.MessageRecallLimit <= 0 {
		options.MessageRecallLimit = 3
	}
	if options.MessageRecallLimit > 20 {
		return nil, fmt.Errorf("消息语义召回数量不能超过 20")
	}
	if math.IsNaN(options.MessageRecallMinScore) || math.IsInf(options.MessageRecallMinScore, 0) || options.MessageRecallMinScore < 0 || options.MessageRecallMinScore > 1 {
		return nil, fmt.Errorf("消息语义召回最低分必须在 0 到 1 之间")
	}
	if options.MessageRecallMinScore == 0 {
		options.MessageRecallMinScore = 0.55
	}
	return &Service{
		store: store, embedder: embedder, messageRecallLimit: options.MessageRecallLimit,
		messageRecallMinScore: options.MessageRecallMinScore, now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *Service) Model() string     { return s.embedder.Name() }
func (s *Service) Dimensions() int   { return s.embedder.Dimensions() }
func (s *Service) IndexVersion() int { return CurrentIndexVersion }

// IndexMessages 批量向量化用户和助手消息。工具消息不进入跨会话语义召回，
// 避免把工具原始结果再次注入模型造成提示词污染。
func (s *Service) IndexMessages(ctx context.Context, messages []domain.Message) error {
	filtered := make([]domain.Message, 0, len(messages))
	texts := make([]string, 0, len(messages))
	for _, item := range messages {
		if (item.Role != domain.RoleUser && item.Role != domain.RoleAssistant) || strings.TrimSpace(item.Content) == "" {
			continue
		}
		filtered = append(filtered, item)
		texts = append(texts, item.Content)
	}
	if len(filtered) == 0 {
		return nil
	}
	vectors, err := s.embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("生成消息向量失败：%w", err)
	}
	now := s.now()
	items := make([]MessageEmbedding, len(filtered))
	for index, message := range filtered {
		if len(vectors[index]) != s.embedder.Dimensions() {
			return fmt.Errorf("消息 %s 的向量维度无效：%w", message.ID, ErrEmbeddingMismatch)
		}
		items[index] = MessageEmbedding{
			MessageID: message.ID, ConversationID: message.ConversationID, Role: message.Role,
			EmbeddingModel: s.embedder.Name(), EmbeddingDimensions: s.embedder.Dimensions(),
			IndexVersion: CurrentIndexVersion, Embedding: vectors[index], UpdatedAt: now,
		}
	}
	return s.store.UpsertMessageEmbeddings(ctx, items)
}

// VectorizeMemories 只计算派生向量；memory.Store 会把 Memory 与向量放在同一事务中提交。
func (s *Service) VectorizeMemories(ctx context.Context, items []memory.Memory) ([]memory.Memory, error) {
	if len(items) == 0 {
		return []memory.Memory{}, nil
	}
	texts := make([]string, len(items))
	for index, item := range items {
		texts[index] = strings.TrimSpace(item.MemoryKey + " " + item.Content)
	}
	vectors, err := s.embedder.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("生成长期记忆向量失败：%w", err)
	}
	result := append([]memory.Memory(nil), items...)
	for index := range result {
		if len(vectors[index]) != s.embedder.Dimensions() {
			return nil, fmt.Errorf("长期记忆 %s 的向量维度无效：%w", result[index].ID, ErrEmbeddingMismatch)
		}
		result[index].EmbeddingModel = s.embedder.Name()
		result[index].EmbeddingDimensions = s.embedder.Dimensions()
		result[index].IndexVersion = CurrentIndexVersion
		result[index].Embedding = vectors[index]
	}
	return result, nil
}

func (s *Service) SearchMemoryVectors(ctx context.Context, query string, limit int) ([]memory.VectorSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []memory.VectorSearchResult{}, nil
	}
	vector, err := s.embedQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	hits, err := s.store.SearchMemoryEmbeddings(ctx, MemorySearchRequest{
		QueryVector: vector, EmbeddingModel: s.embedder.Name(), EmbeddingDimensions: s.embedder.Dimensions(),
		IndexVersion: CurrentIndexVersion, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	result := make([]memory.VectorSearchResult, 0, len(hits))
	for _, hit := range hits {
		result = append(result, memory.VectorSearchResult{MemoryID: hit.Memory.ID, Score: clampScore(hit.Score)})
	}
	return result, nil
}

// RecallMessages 只召回其他会话中的用户原话；助手输出虽然建索引，但默认不回灌模型。
func (s *Service) RecallMessages(ctx context.Context, query, excludeConversationID string) ([]MessageHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []MessageHit{}, nil
	}
	vector, err := s.embedQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	hits, err := s.store.SearchMessageEmbeddings(ctx, MessageSearchRequest{
		QueryVector: vector, EmbeddingModel: s.embedder.Name(), EmbeddingDimensions: s.embedder.Dimensions(),
		IndexVersion: CurrentIndexVersion, ExcludeConversation: excludeConversationID,
		Role: domain.RoleUser, Limit: s.messageRecallLimit * 4,
	})
	if err != nil {
		return nil, err
	}
	result := make([]MessageHit, 0, s.messageRecallLimit)
	for _, hit := range hits {
		hit.Score = clampScore(hit.Score)
		if hit.Score < s.messageRecallMinScore {
			continue
		}
		result = append(result, hit)
		if len(result) == s.messageRecallLimit {
			break
		}
	}
	return result, nil
}

func (s *Service) Reindex(ctx context.Context) (ReindexResult, error) {
	var result ReindexResult
	var afterSequence int64
	for {
		messages, err := s.store.ListMessagesForEmbedding(ctx, afterSequence, defaultBatchSize)
		if err != nil {
			return result, err
		}
		if len(messages) == 0 {
			break
		}
		if err := s.IndexMessages(ctx, messages); err != nil {
			return result, err
		}
		result.Messages += len(messages)
		afterSequence = messages[len(messages)-1].Sequence
	}
	var afterID string
	for {
		items, err := s.store.ListMemoriesForEmbedding(ctx, afterID, defaultBatchSize)
		if err != nil {
			return result, err
		}
		if len(items) == 0 {
			break
		}
		vectorized, err := s.VectorizeMemories(ctx, items)
		if err != nil {
			return result, err
		}
		embeddings := make([]MemoryEmbedding, len(vectorized))
		for index, item := range vectorized {
			embeddings[index] = memoryEmbedding(item, s.now())
		}
		if err := s.store.UpsertMemoryEmbeddings(ctx, embeddings); err != nil {
			return result, err
		}
		result.Memories += len(items)
		afterID = items[len(items)-1].ID
	}
	return result, nil
}

func (s *Service) embedQuery(ctx context.Context, query string) ([]float64, error) {
	vectors, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("生成语义检索查询向量失败：%w", err)
	}
	if len(vectors) != 1 || len(vectors[0]) != s.embedder.Dimensions() {
		return nil, ErrEmbeddingMismatch
	}
	return vectors[0], nil
}

func memoryEmbedding(item memory.Memory, updatedAt time.Time) MemoryEmbedding {
	return MemoryEmbedding{
		MemoryID: item.ID, Kind: item.Kind, EmbeddingModel: item.EmbeddingModel,
		EmbeddingDimensions: item.EmbeddingDimensions, IndexVersion: item.IndexVersion,
		Embedding: item.Embedding, UpdatedAt: updatedAt,
	}
}

func CosineSimilarity(left, right []float64) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	var dot, leftNorm, rightNorm float64
	for index := range left {
		dot += left[index] * right[index]
		leftNorm += left[index] * left[index]
		rightNorm += right[index] * right[index]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / math.Sqrt(leftNorm*rightNorm)
}

func SortMessageHits(hits []MessageHit) {
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
}

func SortMemoryHits(hits []MemoryHit) {
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
}

func clampScore(score float64) float64 { return max(0, min(1, score)) }
