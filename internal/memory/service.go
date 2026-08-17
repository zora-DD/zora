package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zhiruo/zora/internal/id"
)

const (
	defaultImportance     = 0.5
	maxContentRunes       = 2_000
	defaultRecallLimit    = 5
	defaultRecallMinScore = 0.25
)

type Service struct {
	store          Store
	extractor      Extractor
	now            func() time.Time
	captureMu      sync.Mutex
	recallLimit    int
	recallMinScore float64
}

type Option func(*Service) error

func WithExtractor(extractor Extractor) Option {
	return func(service *Service) error {
		if extractor == nil {
			return fmt.Errorf("长期记忆提取器不能为空")
		}
		service.extractor = extractor
		return nil
	}
}

func WithRecallOptions(limit int, minScore float64) Option {
	return func(service *Service) error {
		if limit < 1 || limit > 20 {
			return fmt.Errorf("长期记忆召回数量必须在 1 到 20 之间")
		}
		if math.IsNaN(minScore) || math.IsInf(minScore, 0) || minScore < 0 || minScore > 1 {
			return fmt.Errorf("长期记忆最低召回分数必须在 0 到 1 之间")
		}
		service.recallLimit = limit
		service.recallMinScore = minScore
		return nil
	}
}

func NewService(store Store, options ...Option) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("长期记忆存储不能为空")
	}
	service := &Service{
		store: store, now: func() time.Time { return time.Now().UTC() },
		recallLimit: defaultRecallLimit, recallMinScore: defaultRecallMinScore,
	}
	for _, option := range options {
		if err := option(service); err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Memory, error) {
	importance := defaultImportance
	if input.Importance != nil {
		importance = *input.Importance
	}
	now := s.now()
	item := Memory{
		ID: id.New("mem"), Kind: strings.TrimSpace(input.Kind), Content: strings.TrimSpace(input.Content),
		Importance: importance, SourceType: SourceManual, UserEdited: true,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: normalizeOptionalTime(input.ExpiresAt),
	}
	if err := validateEditable(item, now, true); err != nil {
		return Memory{}, err
	}
	if err := s.store.CreateMemory(ctx, item); err != nil {
		return Memory{}, err
	}
	return item, nil
}

func (s *Service) Get(ctx context.Context, id string) (Memory, error) {
	if strings.TrimSpace(id) == "" {
		return Memory{}, fmt.Errorf("长期记忆 ID 不能为空")
	}
	return s.store.GetMemory(ctx, id)
}

func (s *Service) AutoCaptureEnabled() bool { return s.extractor != nil }

func (s *Service) List(ctx context.Context, kind string, includeExpired bool) ([]Memory, error) {
	kind = strings.TrimSpace(kind)
	if kind != "" && !validKind(kind) {
		return nil, fmt.Errorf("长期记忆类型必须是 semantic 或 episodic")
	}
	return s.store.ListMemories(ctx, ListFilter{Kind: kind, IncludeExpired: includeExpired, Limit: 200})
}

func (s *Service) Replace(ctx context.Context, memoryID string, input ReplaceInput) (Memory, error) {
	current, err := s.Get(ctx, memoryID)
	if err != nil {
		return Memory{}, err
	}
	if input.Importance == nil {
		return Memory{}, fmt.Errorf("更新长期记忆时必须提供 importance")
	}
	current.Kind = strings.TrimSpace(input.Kind)
	current.Content = strings.TrimSpace(input.Content)
	current.Importance = *input.Importance
	current.ExpiresAt = normalizeOptionalTime(input.ExpiresAt)
	// 用户修正后的自动记忆仍保留原始来源，但会获得覆盖保护，后续自动合并不得改写。
	current.UserEdited = true
	current.UpdatedAt = s.now()
	if err := validateEditable(current, current.UpdatedAt, false); err != nil {
		return Memory{}, err
	}
	if err := s.store.UpdateMemory(ctx, current); err != nil {
		return Memory{}, err
	}
	return current, nil
}

// Capture 提取并合并一轮对话中的长期记忆。进程内串行化可避免并发请求产生重复 Key；
// 多副本部署仍应在后续用数据库唯一约束或任务队列提供全局串行语义。
func (s *Service) Capture(ctx context.Context, input CaptureInput) (CaptureResult, error) {
	result := CaptureResult{Enabled: s.extractor != nil}
	if s.extractor == nil {
		return result, nil
	}
	if strings.TrimSpace(input.ConversationID) == "" || strings.TrimSpace(input.UserMessageID) == "" {
		return result, fmt.Errorf("自动记忆必须关联来源会话和用户消息")
	}
	candidates, err := s.extractor.Extract(ctx, ExtractionInput{
		UserContent: input.UserContent, AssistantContent: input.AssistantContent,
	})
	if err != nil {
		return result, err
	}
	result.Candidates = len(candidates)
	if len(candidates) == 0 {
		return result, nil
	}

	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	now := s.now()
	for _, candidate := range candidates {
		candidate.Kind = strings.TrimSpace(candidate.Kind)
		candidate.MemoryKey = normalizeMemoryKey(candidate.MemoryKey)
		candidate.Content = strings.TrimSpace(candidate.Content)
		candidate.ExpiresAt = normalizeOptionalTime(candidate.ExpiresAt)
		if err := validateCandidate(candidate, now); err != nil {
			result.Skipped++
			continue
		}
		current, lookupErr := s.store.GetMemoryByKey(ctx, candidate.Kind, candidate.MemoryKey)
		if lookupErr == nil {
			// 用户修正优先于自动推断，避免下一轮对话悄悄覆盖人工纠错。
			if current.UserEdited {
				result.Skipped++
				continue
			}
			if current.Content == candidate.Content && current.Importance >= candidate.Importance && sameOptionalTime(current.ExpiresAt, candidate.ExpiresAt) {
				result.Skipped++
				continue
			}
			current.Content = candidate.Content
			current.Importance = max(current.Importance, candidate.Importance)
			current.SourceConversationID = input.ConversationID
			current.SourceMessageID = input.UserMessageID
			current.UpdatedAt = now
			current.ExpiresAt = candidate.ExpiresAt
			if err := s.store.UpdateMemory(ctx, current); err != nil {
				return result, fmt.Errorf("合并长期记忆失败：%w", err)
			}
			result.Updated++
			continue
		}
		if !errors.Is(lookupErr, ErrNotFound) {
			return result, fmt.Errorf("读取待合并长期记忆失败：%w", lookupErr)
		}

		item := Memory{
			ID: id.New("mem"), Kind: candidate.Kind, MemoryKey: candidate.MemoryKey,
			Content: candidate.Content, Importance: candidate.Importance, SourceType: SourceConversation,
			SourceConversationID: input.ConversationID, SourceMessageID: input.UserMessageID,
			CreatedAt: now, UpdatedAt: now, ExpiresAt: candidate.ExpiresAt,
		}
		if err := s.store.CreateMemory(ctx, item); err != nil {
			return result, fmt.Errorf("保存自动长期记忆失败：%w", err)
		}
		result.Created++
	}
	return result, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("长期记忆 ID 不能为空")
	}
	return s.store.DeleteMemory(ctx, id)
}

func validateEditable(item Memory, now time.Time, requireFutureExpiry bool) error {
	if !validKind(item.Kind) {
		return fmt.Errorf("长期记忆类型必须是 semantic 或 episodic")
	}
	if item.Content == "" || utf8.RuneCountInString(item.Content) > maxContentRunes {
		return fmt.Errorf("长期记忆内容必须包含 1 到 %d 个字符", maxContentRunes)
	}
	if math.IsNaN(item.Importance) || math.IsInf(item.Importance, 0) || item.Importance < 0 || item.Importance > 1 {
		return fmt.Errorf("长期记忆重要性必须在 0 到 1 之间")
	}
	if requireFutureExpiry && item.ExpiresAt != nil && !item.ExpiresAt.After(now) {
		return fmt.Errorf("长期记忆过期时间必须晚于当前时间")
	}
	return nil
}

func validateCandidate(candidate Candidate, now time.Time) error {
	if candidate.MemoryKey == "" || utf8.RuneCountInString(candidate.MemoryKey) > 120 {
		return fmt.Errorf("自动记忆 memory_key 必须包含 1 到 120 个字符")
	}
	if containsSensitiveLabel(candidate.Content) {
		return fmt.Errorf("自动记忆不能保存密码、令牌或证件等敏感凭据")
	}
	return validateEditable(Memory{
		Kind: candidate.Kind, Content: candidate.Content,
		Importance: candidate.Importance, ExpiresAt: candidate.ExpiresAt,
	}, now, true)
}

func sameOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func validKind(kind string) bool { return kind == KindSemantic || kind == KindEpisodic }

func normalizeOptionalTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}
