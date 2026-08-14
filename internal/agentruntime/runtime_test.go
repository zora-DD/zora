package agentruntime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/zhiruo/zora/internal/agenttools"
	"github.com/zhiruo/zora/internal/config"
)

func TestMockRuntimeExecutesToolThroughEino(t *testing.T) {
	t.Parallel()
	registeredTools, err := agenttools.Build()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(context.Background(), config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "Be helpful.",
		RequestTimeout: time.Second, MaxIterations: 5,
	}, registeredTools)
	if err != nil {
		t.Fatal(err)
	}

	var eventTypes []string
	answer, err := runtime.Execute(context.Background(), []*schema.Message{
		schema.UserMessage("帮我计算 (128 + 72) * 3.5"),
	}, func(event Event) error {
		eventTypes = append(eventTypes, event.Type)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "700") {
		t.Fatalf("answer %q does not contain calculator result", answer)
	}
	joined := strings.Join(eventTypes, ",")
	if !strings.Contains(joined, "tool_call") || !strings.Contains(joined, "tool_result") || !strings.Contains(joined, "delta") {
		t.Fatalf("event chain %q is incomplete", joined)
	}
}
