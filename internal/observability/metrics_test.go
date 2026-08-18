package observability

import (
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/domain"
)

func TestAggregateRunMetrics(t *testing.T) {
	t.Parallel()
	startedAt := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(1250 * time.Millisecond)
	run := domain.AgentRun{ID: "run_1", Status: domain.RunCompleted, StartedAt: startedAt, CompletedAt: &completedAt}
	events := []domain.RunEvent{
		{Type: "first_token", Payload: map[string]any{"latency_ms": float64(120)}},
		{Type: "model_call_completed", Payload: map[string]any{
			"usage_reported": true, "prompt_tokens": float64(100), "completion_tokens": float64(20),
			"total_tokens": float64(120), "cached_tokens": float64(60), "reasoning_tokens": float64(4),
		}},
		{Type: "tool_call"},
		{Type: "tool_result", Payload: map[string]any{"duration_ms": float64(75)}},
		{Type: "agent_handoff_started"},
		{Type: "agent_handoff_completed", Payload: map[string]any{"duration_ms": float64(230)}},
	}
	summary := Aggregate(run, events)
	metrics := summary.Metrics
	if metrics.DurationMS != 1250 || metrics.TimeToFirstTokenMS == nil || *metrics.TimeToFirstTokenMS != 120 {
		t.Fatalf("unexpected latency metrics: %+v", metrics)
	}
	if !metrics.UsageComplete || metrics.ModelCalls != 1 || metrics.TotalTokens != 120 || metrics.CachedTokens != 60 || metrics.ReasoningTokens != 4 {
		t.Fatalf("unexpected usage metrics: %+v", metrics)
	}
	if metrics.ToolCalls != 1 || metrics.ToolCompleted != 1 || metrics.ToolDurationMS != 75 || metrics.MaximumToolDuration != 75 {
		t.Fatalf("unexpected tool metrics: %+v", metrics)
	}
	if metrics.AgentHandoffs != 1 || metrics.AgentHandoffsCompleted != 1 || metrics.AgentHandoffDurationMS != 230 || metrics.MaximumAgentHandoffDuration != 230 {
		t.Fatalf("unexpected execution metrics: %+v", metrics)
	}
}

func TestAggregateMarksPartialUsage(t *testing.T) {
	t.Parallel()
	run := domain.AgentRun{ID: "run_2", Status: domain.RunRunning, StartedAt: time.Now().UTC()}
	metrics := Aggregate(run, []domain.RunEvent{
		{Type: "model_call_completed", Payload: map[string]any{"usage_reported": true, "total_tokens": 10}},
		{Type: "model_call_completed", Payload: map[string]any{"usage_reported": false}},
	}).Metrics
	if metrics.UsageComplete || metrics.ModelCalls != 2 || metrics.UsageReportedCalls != 1 || metrics.TotalTokens != 10 {
		t.Fatalf("unexpected partial usage metrics: %+v", metrics)
	}
}
