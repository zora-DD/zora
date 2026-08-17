package summary

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/domain"
)

func TestServiceUpdatesIncrementallyAndKeepsRecentMessages(t *testing.T) {
	t.Parallel()
	store := &summaryStoreStub{messages: makeMessages(10, 100)}
	summarizer := &summarizerStub{content: "用户在开发 Go Agent；当前需要完成会话摘要。"}
	service, err := NewService(store, summarizer, Options{
		TriggerMessages: 6, KeepRecent: 2, MaxRunes: 500, Model: "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.HistoryLimit() != 6 {
		t.Fatalf("history limit = %d, want 6", service.HistoryLimit())
	}
	service.now = func() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) }

	result, err := service.Update(context.Background(), "conv_1", 190)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.ThroughSequence != 170 || result.MessageCount != 8 {
		t.Fatalf("unexpected update result: %+v", result)
	}
	if len(summarizer.input.Messages) != 8 || summarizer.input.Messages[0].Sequence != 100 || summarizer.input.Messages[7].Sequence != 170 {
		t.Fatalf("summarized messages = %+v", summarizer.input.Messages)
	}
	if store.saved.Content != summarizer.content || store.saved.Model != "test-model" {
		t.Fatalf("saved summary = %+v", store.saved)
	}

	// 全局 sequence 即使出现很大间隔，也不能被误判为大量当前会话消息。
	store.messages = append(store.messages, domain.Message{Sequence: 10_000, Role: domain.RoleUser, Content: "只有一条新消息"})
	second, err := service.Update(context.Background(), "conv_1", 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if second.Updated {
		t.Fatalf("single message with sequence gap unexpectedly triggered summary: %+v", second)
	}
}

func TestServiceSummaryFailureDoesNotPersist(t *testing.T) {
	t.Parallel()
	want := errors.New("模型暂时不可用")
	store := &summaryStoreStub{messages: makeMessages(6, 1)}
	service, err := NewService(store, &summarizerStub{err: want}, Options{
		TriggerMessages: 4, KeepRecent: 2, MaxRunes: 500, Model: "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(context.Background(), "conv_1", 51); !errors.Is(err, want) {
		t.Fatalf("update error = %v, want %v", err, want)
	}
	if store.saved.ConversationID != "" {
		t.Fatalf("summary should not be persisted after failure: %+v", store.saved)
	}
}

func TestServiceRejectsSensitiveSummaryOutput(t *testing.T) {
	t.Parallel()
	store := &summaryStoreStub{messages: makeMessages(4, 1)}
	service, err := NewService(store, &summarizerStub{content: "用户的 API Key 是 secret-value。"}, Options{
		TriggerMessages: 4, KeepRecent: 2, MaxRunes: 500, Model: "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(context.Background(), "conv_1", 31); err == nil {
		t.Fatal("expected sensitive summary rejection")
	}
	if store.saved.ConversationID != "" {
		t.Fatalf("sensitive summary should not be persisted: %+v", store.saved)
	}
}

func makeMessages(count int, firstSequence int64) []domain.Message {
	result := make([]domain.Message, 0, count)
	for index := 0; index < count; index++ {
		role := domain.RoleUser
		if index%2 == 1 {
			role = domain.RoleAssistant
		}
		result = append(result, domain.Message{
			ID: "msg", ConversationID: "conv_1", Role: role,
			Content: "消息内容", Sequence: firstSequence + int64(index*10),
		})
	}
	return result
}

type summaryStoreStub struct {
	summary  Summary
	messages []domain.Message
	saved    Summary
}

func (s *summaryStoreStub) GetConversationSummary(_ context.Context, _ string) (Summary, error) {
	if s.summary.ConversationID == "" {
		return Summary{}, ErrNotFound
	}
	return s.summary, nil
}

func (s *summaryStoreStub) UpsertConversationSummary(_ context.Context, item Summary) error {
	s.saved = item
	s.summary = item
	return nil
}

func (s *summaryStoreStub) ListMessagesForSummary(_ context.Context, _ string, afterSequence, throughSequence int64, limit int) ([]domain.Message, error) {
	result := make([]domain.Message, 0)
	for _, message := range s.messages {
		if message.Sequence > afterSequence && message.Sequence <= throughSequence {
			result = append(result, message)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

type summarizerStub struct {
	content string
	err     error
	input   SummarizeInput
}

func (s *summarizerStub) Summarize(_ context.Context, input SummarizeInput) (string, error) {
	s.input = input
	return s.content, s.err
}
