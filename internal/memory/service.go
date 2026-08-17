package memory

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhiruo/zora/internal/id"
)

const (
	defaultImportance = 0.5
	maxContentRunes   = 2_000
)

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("长期记忆存储不能为空")
	}
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Memory, error) {
	importance := defaultImportance
	if input.Importance != nil {
		importance = *input.Importance
	}
	now := s.now()
	item := Memory{
		ID: id.New("mem"), Kind: strings.TrimSpace(input.Kind), Content: strings.TrimSpace(input.Content),
		Importance: importance, SourceType: SourceManual,
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
	current.UpdatedAt = s.now()
	if err := validateEditable(current, current.UpdatedAt, false); err != nil {
		return Memory{}, err
	}
	if err := s.store.UpdateMemory(ctx, current); err != nil {
		return Memory{}, err
	}
	return current, nil
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

func validKind(kind string) bool { return kind == KindSemantic || kind == KindEpisodic }

func normalizeOptionalTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}
