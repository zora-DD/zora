// Package observability 将持久化 AgentRun 与 RunEvent 聚合为面向产品的运行指标。
package observability

import (
	"encoding/json"
	"math"
	"time"

	"github.com/zhiruo/zora/internal/domain"
)

// RunMetrics 只统计能够从真实运行轨迹核对的数据。
// Token Usage 缺失时 UsageComplete=false，不用字符数或经验公式伪造成本。
type RunMetrics struct {
	DurationMS                  int64  `json:"duration_ms"`
	TimeToFirstTokenMS          *int64 `json:"time_to_first_token_ms,omitempty"`
	ModelCalls                  int    `json:"model_calls"`
	UsageReportedCalls          int    `json:"usage_reported_calls"`
	UsageComplete               bool   `json:"usage_complete"`
	PromptTokens                int    `json:"prompt_tokens"`
	CompletionTokens            int    `json:"completion_tokens"`
	TotalTokens                 int    `json:"total_tokens"`
	CachedTokens                int    `json:"cached_tokens"`
	ReasoningTokens             int    `json:"reasoning_tokens"`
	ToolCalls                   int    `json:"tool_calls"`
	ToolCompleted               int    `json:"tool_completed"`
	ToolDurationMS              int64  `json:"tool_duration_ms"`
	MaximumToolDuration         int64  `json:"maximum_tool_duration_ms"`
	AgentHandoffs               int    `json:"agent_handoffs"`
	AgentHandoffsCompleted      int    `json:"agent_handoffs_completed"`
	AgentHandoffDurationMS      int64  `json:"agent_handoff_duration_ms"`
	MaximumAgentHandoffDuration int64  `json:"maximum_agent_handoff_duration_ms"`
}

type RunSummary struct {
	Run     domain.AgentRun `json:"run"`
	Metrics RunMetrics      `json:"metrics"`
}

func Aggregate(run domain.AgentRun, events []domain.RunEvent) RunSummary {
	metrics := RunMetrics{}
	if run.CompletedAt != nil {
		metrics.DurationMS = milliseconds(run.CompletedAt.Sub(run.StartedAt))
	} else if len(events) > 0 {
		metrics.DurationMS = milliseconds(events[len(events)-1].CreatedAt.Sub(run.StartedAt))
	}
	for _, event := range events {
		switch event.Type {
		case "first_token":
			if metrics.TimeToFirstTokenMS == nil {
				value := int64Number(event.Payload["latency_ms"])
				metrics.TimeToFirstTokenMS = &value
			}
		case "model_call_completed":
			metrics.ModelCalls++
			if boolValue(event.Payload["usage_reported"]) {
				metrics.UsageReportedCalls++
				metrics.PromptTokens += intNumber(event.Payload["prompt_tokens"])
				metrics.CompletionTokens += intNumber(event.Payload["completion_tokens"])
				metrics.TotalTokens += intNumber(event.Payload["total_tokens"])
				metrics.CachedTokens += intNumber(event.Payload["cached_tokens"])
				metrics.ReasoningTokens += intNumber(event.Payload["reasoning_tokens"])
			}
		case "tool_call":
			metrics.ToolCalls++
		case "tool_result":
			metrics.ToolCompleted++
			addToolDuration(&metrics, int64Number(event.Payload["duration_ms"]))
		case "agent_handoff_started":
			metrics.AgentHandoffs++
		case "agent_handoff_completed":
			metrics.AgentHandoffsCompleted++
			addAgentHandoffDuration(&metrics, int64Number(event.Payload["duration_ms"]))
		}
	}
	metrics.UsageComplete = metrics.ModelCalls > 0 && metrics.UsageReportedCalls == metrics.ModelCalls
	return RunSummary{Run: run, Metrics: metrics}
}

func addAgentHandoffDuration(metrics *RunMetrics, duration int64) {
	if duration < 0 {
		return
	}
	metrics.AgentHandoffDurationMS += duration
	if duration > metrics.MaximumAgentHandoffDuration {
		metrics.MaximumAgentHandoffDuration = duration
	}
}

func addToolDuration(metrics *RunMetrics, duration int64) {
	if duration < 0 {
		return
	}
	metrics.ToolDurationMS += duration
	if duration > metrics.MaximumToolDuration {
		metrics.MaximumToolDuration = duration
	}
}

func milliseconds(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	return duration.Milliseconds()
}

func intNumber(value any) int {
	number := int64Number(value)
	if number > int64(math.MaxInt) {
		return math.MaxInt
	}
	if number < 0 {
		return 0
	}
	return int(number)
}

func int64Number(value any) int64 {
	switch typed := value.(type) {
	case int:
		if typed < 0 {
			return 0
		}
		return int64(typed)
	case int64:
		if typed < 0 {
			return 0
		}
		return typed
	case float64:
		if typed <= 0 {
			return 0
		}
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		if parsed < 0 {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}
