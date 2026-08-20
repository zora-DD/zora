package chat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/agentruntime"
	"github.com/zhiruo/zora/internal/config"
	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/feedback"
	"github.com/zhiruo/zora/internal/inputguard"
	"github.com/zhiruo/zora/internal/store/sqlite"
)

func TestUnsafeInputIsBlockedBeforeMessageAndRunPersistence(t *testing.T) {
	t.Parallel()
	database, runtime, service := newQualityLoopService(t, config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "请使用中文回答。",
		RequestTimeout: time.Second, MaxIterations: 4, InputGuardEnabled: true, TopicRelevanceThreshold: 0.08,
	}, false)
	conversation, err := service.CreateConversation(context.Background(), "安全测试")
	if err != nil {
		t.Fatal(err)
	}
	err = service.Send(context.Background(), conversation.ID, "API_KEY=sk-1234567890abcdefghijklmnop", func(StreamEvent) error { return nil })
	if !inputguard.IsBlocked(err) {
		t.Fatalf("expected blocked error, got %v", err)
	}
	messages, err := database.ListMessages(context.Background(), conversation.ID, 10)
	if err != nil || len(messages) != 0 {
		t.Fatalf("blocked messages=%+v err=%v", messages, err)
	}
	runs, err := database.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("blocked runs=%+v err=%v runtime=%v", runs, err, runtime)
	}
}

func TestTopicShiftProducesPersistedGuidance(t *testing.T) {
	t.Parallel()
	_, _, service := newQualityLoopService(t, config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "请使用中文回答。",
		RequestTimeout: time.Second, MaxIterations: 4, InputGuardEnabled: true, TopicRelevanceThreshold: 0.08,
	}, false)
	ctx := context.Background()
	conversation, err := service.CreateConversation(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Send(ctx, conversation.ID, "请介绍 Go goroutine 调度器", func(StreamEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var sawShift bool
	if err := service.Send(ctx, conversation.ID, "推荐几个巴黎的博物馆", func(event StreamEvent) error {
		sawShift = sawShift || event.Type == "topic_shift"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	messages, err := service.ListMessages(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := messages[len(messages)-1]
	if !sawShift || last.Role != domain.RoleAssistant || !strings.HasPrefix(last.Content, "> 话题提示：") {
		t.Fatalf("sawShift=%v last=%+v", sawShift, last)
	}
}

func TestImplicitCorrectionIsAppliedOnlyToNextAnswer(t *testing.T) {
	t.Parallel()
	database, _, service := newQualityLoopService(t, config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "请使用中文回答。",
		RequestTimeout: time.Second, MaxIterations: 4,
	}, true)
	ctx := context.Background()
	conversation, err := service.CreateConversation(ctx, "隐式反馈")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Send(ctx, conversation.ID, "解释一下 goroutine", func(StreamEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	firstMessages, err := database.ListMessages(ctx, conversation.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	firstAssistant := firstMessages[len(firstMessages)-1]
	if err := service.Send(ctx, conversation.ID, "不对，你理解错了，请重新回答", func(StreamEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListAnswerFeedback(ctx, conversation.ID)
	if err != nil || len(items) != 1 || items[0].Source != domain.FeedbackSourceImplicit || items[0].MessageID != firstAssistant.ID {
		t.Fatalf("feedback=%+v err=%v", items, err)
	}
	runs, err := database.ListRuns(ctx, 10)
	if err != nil || len(runs) < 2 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	events, err := database.ListRunEvents(ctx, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var applied bool
	for _, event := range events {
		applied = applied || event.Type == "feedback_applied"
	}
	if !applied {
		t.Fatalf("second run did not apply feedback: %+v", events)
	}
}

func newQualityLoopService(t *testing.T, cfg config.Config, enableFeedback bool) (*sqlite.SQLite, *agentruntime.Runtime, *Service) {
	t.Helper()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "quality-loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	runtime, err := agentruntime.New(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	options := make([]Option, 0, 1)
	if enableFeedback {
		feedbackService, err := feedback.NewService(database)
		if err != nil {
			t.Fatal(err)
		}
		options = append(options, WithFeedback(feedbackService, true))
	}
	return database, runtime, NewService(database, runtime, options...)
}
