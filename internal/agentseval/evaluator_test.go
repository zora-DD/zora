package agentseval

import (
	"context"
	"strings"
	"testing"
)

func TestEvaluateRouteAndCompletionMetrics(t *testing.T) {
	t.Parallel()
	dataset := Dataset{
		Name: "test", MinimumRouteAccuracy: 1, MaximumUnexpectedAgentRate: 0, MinimumAnswerCompletion: 1,
		Cases: []Case{
			{ID: "document", Question: "查文档", ExpectedAgents: []string{"document_agent"}, ExpectedAnswerContains: []string{"证据"}},
			{ID: "direct", Question: "你好", ExpectedAgents: []string{}},
		},
	}
	answers := answererStub{answers: map[string]GeneratedAnswer{
		"查文档": {Content: "找到证据", Handoffs: []string{"document_agent"}},
		"你好":  {Content: "你好", Handoffs: []string{}},
	}}
	report, err := Evaluate(context.Background(), answers, dataset)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || report.Metrics.RouteAccuracy != 1 || report.Metrics.AnswerCompletion != 1 || report.Metrics.UnexpectedAgentRate != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestEvaluateRejectsUnexpectedAgent(t *testing.T) {
	t.Parallel()
	dataset := Dataset{
		Name: "test", MinimumRouteAccuracy: 1, MaximumUnexpectedAgentRate: 0, MinimumAnswerCompletion: 1,
		Cases: []Case{{ID: "direct", Question: "你好", ExpectedAgents: []string{}}},
	}
	report, err := Evaluate(context.Background(), answererStub{answers: map[string]GeneratedAnswer{
		"你好": {Content: "你好", Handoffs: []string{"writer_agent"}},
	}}, dataset)
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.Metrics.UnexpectedAgentRate != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestEvaluateComparisonMeasuresQualityCostAndLatency(t *testing.T) {
	t.Parallel()
	dataset := Dataset{
		Name: "comparison", MinimumQualityGain: 0.5,
		MaximumLatencyRatio: 100, MaximumInvocationRatio: 2,
		Cases: []Case{{
			ID: "writing", Question: "写通知",
			ExpectedAnswerContains: []string{"事实", "草稿"},
		}},
	}
	control := answererStub{answers: map[string]GeneratedAnswer{
		"写通知": {Content: "只有事实"},
	}}
	treatment := answererStub{answers: map[string]GeneratedAnswer{
		"写通知": {Content: "事实与草稿", Handoffs: []string{"writer_agent"}},
	}}
	report, err := EvaluateComparison(context.Background(), control, treatment, dataset)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || report.Metrics.QualityGain != 0.5 || report.Metrics.InvocationRatio != 2 {
		t.Fatalf("unexpected comparison: %+v", report)
	}
}

func TestLoadDatasetUsesStrictJSON(t *testing.T) {
	t.Parallel()
	_, err := LoadDataset(strings.NewReader(`{"name":"bad","minimum_route_accuracy":1,"maximum_unexpected_agent_rate":0,"minimum_answer_completion":1,"unknown":true,"cases":[]}`))
	if err == nil {
		t.Fatal("expected strict JSON error")
	}
}

type answererStub struct{ answers map[string]GeneratedAnswer }

func (a answererStub) Answer(_ context.Context, question string) (GeneratedAnswer, error) {
	return a.answers[question], nil
}
