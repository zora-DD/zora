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

type extractorStub struct {
	candidates []memory.Candidate
}

func (e extractorStub) Extract(_ context.Context, _ memory.ExtractionInput) ([]memory.Candidate, error) {
	return append([]memory.Candidate(nil), e.candidates...), nil
}

func newMemoryStoreStub() *memoryStoreStub {
	return &memoryStoreStub{items: make(map[string]memory.Memory)}
}

func TestServiceCapturesConsolidatesAndProtectsUserEdits(t *testing.T) {
	t.Parallel()
	store := newMemoryStoreStub()
	extractor := &extractorStub{candidates: []memory.Candidate{{
		Kind: memory.KindSemantic, MemoryKey: "preference:programming-language",
		Content: "用户偏好使用 Go。", Importance: 0.75,
	}}}
	service, err := memory.NewService(store, memory.WithExtractor(extractor))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Capture(context.Background(), memory.CaptureInput{
		ConversationID: "conv_1", UserMessageID: "msg_1",
		UserContent: "我喜欢 Go", AssistantContent: "知道了",
	})
	if err != nil || first.Created != 1 {
		t.Fatalf("first capture = %+v, %v", first, err)
	}

	extractor.candidates[0].Content = "用户偏好使用 Java。"
	extractor.candidates[0].Importance = 0.9
	second, err := service.Capture(context.Background(), memory.CaptureInput{
		ConversationID: "conv_2", UserMessageID: "msg_2", UserContent: "我更喜欢 Java",
	})
	if err != nil || second.Updated != 1 {
		t.Fatalf("second capture = %+v, %v", second, err)
	}
	items, err := service.List(context.Background(), memory.KindSemantic, true)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v, %v", items, err)
	}
	item := items[0]
	if item.Content != "用户偏好使用 Java。" || item.SourceType != memory.SourceConversation || item.SourceMessageID != "msg_2" {
		t.Fatalf("unexpected consolidated memory: %+v", item)
	}

	manualImportance := 0.95
	item, err = service.Replace(context.Background(), item.ID, memory.ReplaceInput{
		Kind: memory.KindSemantic, Content: "用户明确选择 Java 作为主要语言。", Importance: &manualImportance,
	})
	if err != nil || !item.UserEdited {
		t.Fatalf("manual correction = %+v, %v", item, err)
	}
	extractor.candidates[0].Content = "用户偏好使用 Rust。"
	third, err := service.Capture(context.Background(), memory.CaptureInput{
		ConversationID: "conv_3", UserMessageID: "msg_3", UserContent: "我喜欢 Rust",
	})
	if err != nil || third.Skipped != 1 {
		t.Fatalf("third capture = %+v, %v", third, err)
	}
	protected, err := service.Get(context.Background(), item.ID)
	if err != nil || protected.Content != "用户明确选择 Java 作为主要语言。" {
		t.Fatalf("protected memory = %+v, %v", protected, err)
	}
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
func (s *memoryStoreStub) GetMemoryByKey(_ context.Context, kind, memoryKey string) (memory.Memory, error) {
	for _, item := range s.items {
		if item.Kind == kind && item.MemoryKey == memoryKey {
			return item, nil
		}
	}
	return memory.Memory{}, memory.ErrNotFound
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

func TestServiceSkipsSensitiveAutomaticCandidate(t *testing.T) {
	t.Parallel()
	service, err := memory.NewService(newMemoryStoreStub(), memory.WithExtractor(extractorStub{
		candidates: []memory.Candidate{{
			Kind: memory.KindSemantic, MemoryKey: "secret:password",
			Content: "用户的密码是 abc123。", Importance: 0.9,
		}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Capture(context.Background(), memory.CaptureInput{
		ConversationID: "conv_1", UserMessageID: "msg_1", UserContent: "请记住我的密码",
	})
	if err != nil || result.Skipped != 1 || result.Created != 0 {
		t.Fatalf("capture result = %+v, %v", result, err)
	}
}
