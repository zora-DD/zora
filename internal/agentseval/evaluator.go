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

// EvaluateComparison 对同一数据集运行单 Agent Control 与多 Agent Treatment。
// invocations 使用“根 Agent + 专业 Agent 交接数”作为稳定的成本代理；真实 Token 成本仍需线上采集。
func EvaluateComparison(ctx context.Context, control, treatment Answerer, dataset Dataset) (ComparisonReport, error) {
	if control == nil || treatment == nil {
		return ComparisonReport{}, fmt.Errorf("单 Agent 和多 Agent 评测答案生成器不能为空")
	}
	if err := dataset.Validate(); err != nil {
		return ComparisonReport{}, err
	}
	maximumLatencyRatio := dataset.MaximumLatencyRatio
	if maximumLatencyRatio == 0 {
		maximumLatencyRatio = 100
	}
	maximumInvocationRatio := dataset.MaximumInvocationRatio
	if maximumInvocationRatio == 0 {
		maximumInvocationRatio = 10
	}
	report := ComparisonReport{
		ControlName: "single-agent", TreatmentName: "multi-agent",
		Thresholds: ComparisonThresholds{
			MinimumQualityGain:     dataset.MinimumQualityGain,
			MaximumLatencyRatio:    maximumLatencyRatio,
			MaximumInvocationRatio: maximumInvocationRatio,
		},
		Cases: make([]ComparisonCaseResult, 0, len(dataset.Cases)),
	}
	var controlQuality, treatmentQuality float64
	var controlLatency, treatmentLatency float64
	var controlInvocations, treatmentInvocations int
	for _, item := range dataset.Cases {
		if err := ctx.Err(); err != nil {
			return ComparisonReport{}, err
		}
		controlStart := time.Now()
		controlAnswer, err := control.Answer(ctx, item.Question)
		controlCaseLatency := float64(time.Since(controlStart).Microseconds()) / 1000
		if err != nil {
			return ComparisonReport{}, fmt.Errorf("Control 问题 %s 执行失败：%w", item.ID, err)
		}
		treatmentStart := time.Now()
		treatmentAnswer, err := treatment.Answer(ctx, item.Question)
		treatmentCaseLatency := float64(time.Since(treatmentStart).Microseconds()) / 1000
		if err != nil {
			return ComparisonReport{}, fmt.Errorf("Treatment 问题 %s 执行失败：%w", item.ID, err)
		}
		controlCaseQuality := answerQuality(controlAnswer.Content, item.ExpectedAnswerContains)
		treatmentCaseQuality := answerQuality(treatmentAnswer.Content, item.ExpectedAnswerContains)
		controlCaseInvocations := 1 + len(controlAnswer.Handoffs)
		treatmentCaseInvocations := 1 + len(treatmentAnswer.Handoffs)
		controlQuality += controlCaseQuality
		treatmentQuality += treatmentCaseQuality
		controlLatency += controlCaseLatency
		treatmentLatency += treatmentCaseLatency
		controlInvocations += controlCaseInvocations
		treatmentInvocations += treatmentCaseInvocations
		report.Cases = append(report.Cases, ComparisonCaseResult{
			ID: item.ID, ControlQuality: controlCaseQuality, TreatmentQuality: treatmentCaseQuality,
			ControlLatencyMS: round(controlCaseLatency), TreatmentLatencyMS: round(treatmentCaseLatency),
			ControlInvocations: controlCaseInvocations, TreatmentInvocations: treatmentCaseInvocations,
		})
	}
	caseCount := float64(len(dataset.Cases))
	controlAverageQuality := controlQuality / caseCount
	treatmentAverageQuality := treatmentQuality / caseCount
	controlAverageLatency := controlLatency / caseCount
	treatmentAverageLatency := treatmentLatency / caseCount
	controlAverageInvocations := float64(controlInvocations) / caseCount
	treatmentAverageInvocations := float64(treatmentInvocations) / caseCount
	report.Metrics = ComparisonMetrics{
		ControlQuality: round(controlAverageQuality), TreatmentQuality: round(treatmentAverageQuality),
		QualityGain:             round(treatmentAverageQuality - controlAverageQuality),
		ControlAverageLatencyMS: round(controlAverageLatency), TreatmentAverageLatencyMS: round(treatmentAverageLatency),
		LatencyRatio:              safeRatio(treatmentAverageLatency, controlAverageLatency),
		ControlAverageInvocations: round(controlAverageInvocations), TreatmentAverageInvocations: round(treatmentAverageInvocations),
		InvocationRatio: safeRatio(treatmentAverageInvocations, controlAverageInvocations),
	}
	report.Passed = report.Metrics.QualityGain >= dataset.MinimumQualityGain &&
		report.Metrics.LatencyRatio <= maximumLatencyRatio &&
		report.Metrics.InvocationRatio <= maximumInvocationRatio
	return report, nil
}

func answerQuality(content string, anchors []string) float64 {
	if len(anchors) == 0 {
		if strings.TrimSpace(content) == "" {
			return 0
		}
		return 1
	}
	matched := 0
	for _, anchor := range anchors {
		if strings.Contains(content, anchor) {
			matched++
		}
	}
	return ratio(matched, len(anchors))
}

func safeRatio(numerator, denominator float64) float64 {
	if denominator <= 0 {
		if numerator <= 0 {
			return 1
		}
		// JSON 不支持 Inf；使用足够大的有限值表达“基线为零而实验组非零”。
		return 1_000_000_000
	}
	return round(numerator / denominator)
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
