package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/memory"
)

type memoryStoreStub struct {
	items map[string]memory.Memory
}

func newMemoryStoreStub() *memoryStoreStub {
	return &memoryStoreStub{items: make(map[string]memory.Memory)}
}

func (s *memoryStoreStub) CreateMemory(_ context.Context, item memory.Memory) error {
	s.items[item.ID] = item
	return nil
}
func (s *memoryStoreStub) GetMemory(_ context.Context, id string) (memory.Memory, error) {
	item, ok := s.items[id]
	if !ok {
		return memory.Memory{}, memory.ErrNotFound
	}
	return item, nil
}
func (s *memoryStoreStub) ListMemories(_ context.Context, filter memory.ListFilter) ([]memory.Memory, error) {
	result := make([]memory.Memory, 0, len(s.items))
	for _, item := range s.items {
		if filter.Kind == "" || filter.Kind == item.Kind {
			result = append(result, item)
		}
	}
	return result, nil
}
func (s *memoryStoreStub) UpdateMemory(_ context.Context, item memory.Memory) error {
	if _, ok := s.items[item.ID]; !ok {
		return memory.ErrNotFound
	}
	s.items[item.ID] = item
	return nil
}
func (s *memoryStoreStub) DeleteMemory(_ context.Context, id string) error {
	if _, ok := s.items[id]; !ok {
		return memory.ErrNotFound
	}
	delete(s.items, id)
	return nil
}

func TestServiceManualMemoryLifecycle(t *testing.T) {
	t.Parallel()
	store := newMemoryStoreStub()
	service, err := memory.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	importance := 0.8
	expiresAt := time.Now().UTC().Add(time.Hour)
	created, err := service.Create(context.Background(), memory.CreateInput{
		Kind: memory.KindSemantic, Content: " 用户偏好使用 Go 编写后端服务。 ",
		Importance: &importance, ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.SourceType != memory.SourceManual || created.Content != "用户偏好使用 Go 编写后端服务。" {
		t.Fatalf("unexpected created memory: %+v", created)
	}

	updatedImportance := 0.6
	updated, err := service.Replace(context.Background(), created.ID, memory.ReplaceInput{
		Kind: memory.KindEpisodic, Content: "用户在 2026 年完成了 Zora V0.2。", Importance: &updatedImportance,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Kind != memory.KindEpisodic || updated.ExpiresAt != nil || updated.SourceType != memory.SourceManual {
		t.Fatalf("unexpected updated memory: %+v", updated)
	}

	if err := service.Delete(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), created.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("get deleted memory error = %v", err)
	}
}

func TestServiceRejectsInvalidMemory(t *testing.T) {
	t.Parallel()
	service, err := memory.NewService(newMemoryStoreStub())
	if err != nil {
		t.Fatal(err)
	}
	importance := 1.1
	if _, err := service.Create(context.Background(), memory.CreateInput{
		Kind: "history", Content: "invalid", Importance: &importance,
	}); err == nil {
		t.Fatal("expected invalid kind or importance to fail")
	}
	past := time.Now().UTC().Add(-time.Minute)
	if _, err := service.Create(context.Background(), memory.CreateInput{
		Kind: memory.KindSemantic, Content: "invalid expiry", ExpiresAt: &past,
	}); err == nil {
		t.Fatal("expected past expiry to fail")
	}
}
