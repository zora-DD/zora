package agentseval

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

func Evaluate(ctx context.Context, answerer Answerer, dataset Dataset) (Report, error) {
	if answerer == nil {
		return Report{}, fmt.Errorf("多 Agent 评测答案生成器不能为空")
	}
	if err := dataset.Validate(); err != nil {
		return Report{}, err
	}
	report := Report{
		Dataset: dataset.Name, CaseCount: len(dataset.Cases),
		Thresholds: Thresholds{
			MinimumRouteAccuracy:       dataset.MinimumRouteAccuracy,
			MaximumUnexpectedAgentRate: dataset.MaximumUnexpectedAgentRate,
			MinimumAnswerCompletion:    dataset.MinimumAnswerCompletion,
		},
		Cases: make([]CaseResult, 0, len(dataset.Cases)),
	}
	var matchedRoutes, completedAnswers, totalHandoffs, unexpectedHandoffs int
	var totalLatency float64
	for _, item := range dataset.Cases {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		startedAt := time.Now()
		generated, err := answerer.Answer(ctx, item.Question)
		latency := float64(time.Since(startedAt).Microseconds()) / 1000
		if err != nil {
			return Report{}, fmt.Errorf("评测问题 %s 执行失败：%w", item.ID, err)
		}
		actual := append([]string{}, generated.Handoffs...)
		routeMatched := slices.Equal(actual, item.ExpectedAgents)
		if routeMatched {
			matchedRoutes++
		}
		expectedSet := make(map[string]struct{}, len(item.ExpectedAgents))
		for _, name := range item.ExpectedAgents {
			expectedSet[name] = struct{}{}
		}
		unexpected := make([]string, 0)
		for _, name := range actual {
			if _, ok := expectedSet[name]; !ok {
				unexpected = append(unexpected, name)
			}
		}
		answerCompleted := strings.TrimSpace(generated.Content) != ""
		for _, anchor := range item.ExpectedAnswerContains {
			answerCompleted = answerCompleted && strings.Contains(generated.Content, anchor)
		}
		if answerCompleted {
			completedAnswers++
		}
		totalHandoffs += len(actual)
		unexpectedHandoffs += len(unexpected)
		totalLatency += latency
		report.Cases = append(report.Cases, CaseResult{
			ID: item.ID, Question: item.Question,
			ExpectedAgents: append([]string{}, item.ExpectedAgents...), ActualAgents: actual,
			UnexpectedAgents: unexpected, RouteMatched: routeMatched,
			AnswerCompleted: answerCompleted, Answer: generated.Content, LatencyMS: round(latency),
		})
	}
	report.Metrics = Metrics{
		RouteAccuracy:       ratio(matchedRoutes, len(dataset.Cases)),
		UnexpectedAgentRate: ratio(unexpectedHandoffs, totalHandoffs),
		AnswerCompletion:    ratio(completedAnswers, len(dataset.Cases)),
		AverageLatencyMS:    round(totalLatency / float64(len(dataset.Cases))),
	}
	report.Passed = report.Metrics.RouteAccuracy >= dataset.MinimumRouteAccuracy &&
		report.Metrics.UnexpectedAgentRate <= dataset.MaximumUnexpectedAgentRate &&
		report.Metrics.AnswerCompletion >= dataset.MinimumAnswerCompletion
	return report, nil
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return round(float64(numerator) / float64(denominator))
}

func round(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}
