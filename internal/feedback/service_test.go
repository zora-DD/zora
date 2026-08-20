package feedback

import (
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/domain"
)

func TestDetectImplicitRequiresCorrectionOrRepeatedQuestion(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	previous := []domain.Message{
		{ID: "user-1", Role: domain.RoleUser, Content: "Go 的调度器怎么工作？", CreatedAt: now},
		{ID: "assistant-1", Role: domain.RoleAssistant, Content: "初始回答", CreatedAt: now},
	}
	if signal := DetectImplicit("conv-1", "能再举个例子吗？", previous); signal != nil {
		t.Fatalf("ordinary follow-up should not be negative feedback: %+v", signal)
	}
	signal := DetectImplicit("conv-1", "不对，你理解错了，请重新回答", previous)
	if signal == nil || signal.AssistantMessageID != "assistant-1" || signal.Signals["negative_marker"] == nil {
		t.Fatalf("correction signal = %+v", signal)
	}
	emotion := DetectImplicit("conv-1", "你这个回答让我很失望", previous)
	if emotion == nil || emotion.Signals["negative_emotion"] == nil {
		t.Fatalf("emotion signal = %+v", emotion)
	}
}

func TestPreviousNegativePrefersExplicitFeedback(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	items := []domain.AnswerFeedback{
		{ID: "explicit", MessageID: "assistant-1", Source: domain.FeedbackSourceExplicit, Rating: -1, UpdatedAt: now},
		{ID: "implicit", MessageID: "assistant-1", Source: domain.FeedbackSourceImplicit, Rating: -1, UpdatedAt: now.Add(time.Minute)},
	}
	selected := PreviousNegative(items, "assistant-1")
	if selected == nil || selected.ID != "explicit" {
		t.Fatalf("selected = %+v", selected)
	}
}

func TestPreviousForNextAnswerUsesPositiveExplicitFeedback(t *testing.T) {
	t.Parallel()
	items := []domain.AnswerFeedback{
		{ID: "explicit-like", MessageID: "assistant-1", Source: domain.FeedbackSourceExplicit, Rating: 1, UpdatedAt: time.Now().UTC()},
	}
	selected := PreviousForNextAnswer(items, "assistant-1")
	if selected == nil || selected.ID != "explicit-like" || selected.Rating != 1 {
		t.Fatalf("selected = %+v", selected)
	}
}
