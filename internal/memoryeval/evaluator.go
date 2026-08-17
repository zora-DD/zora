package memoryeval

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Evaluate 对每个问题运行关闭召回的 control 和开启召回的 treatment。
// 两种变体交替先后执行，降低首次调用预热对延迟差值的系统性影响。
func Evaluate(ctx context.Context, answerer Answerer, dataset Dataset) (Report, error) {
	if answerer == nil {
		return Report{}, fmt.Errorf("长期记忆 A/B 答案生成器不能为空")
	}
	if err := dataset.Validate(); err != nil {
		return Report{}, err
	}
	report := Report{
		Dataset: dataset.Name, TopK: dataset.TopK, CaseCount: len(dataset.Cases),
		Thresholds: Thresholds{
			RecallMinScore:                dataset.RecallMinScore,
			MinimumRecallAtK:              dataset.MinimumRecallAtK,
			MaximumUnexpectedRecallRate:   dataset.MaximumUnexpectedRecallRate,
			MinimumTreatmentFactCoverage:  dataset.MinimumTreatmentFactCoverage,
			MinimumFactCoverageDelta:      dataset.MinimumFactCoverageDelta,
			MaximumTreatmentForbiddenRate: dataset.MaximumTreatmentForbiddenRate,
		},
		Cases: make([]CaseResult, 0, len(dataset.Cases)),
	}

	var totalExpectedMemories, recalledExpectedMemories, unexpectedMemories int
	var totalExpectedFacts, controlPresentFacts, treatmentPresentFacts int
	var totalForbiddenFacts, controlForbiddenFacts, treatmentForbiddenFacts int
	var controlLatency, treatmentLatency float64
	for index, item := range dataset.Cases {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		var control, treatment VariantCaseResult
		var err error
		if index%2 == 0 {
			control, err = evaluateVariant(ctx, answerer, VariantControl, item)
			if err == nil {
				treatment, err = evaluateVariant(ctx, answerer, VariantTreatment, item)
			}
		} else {
			treatment, err = evaluateVariant(ctx, answerer, VariantTreatment, item)
			if err == nil {
				control, err = evaluateVariant(ctx, answerer, VariantControl, item)
			}
		}
		if err != nil {
			return Report{}, fmt.Errorf("评测问题 %s 执行失败：%w", item.ID, err)
		}

		expected := stringSet(item.ExpectedMemoryIDs)
		recalled := stringSet(treatment.RecalledMemoryIDs)
		recalledExpected := 0
		unexpectedIDs := make([]string, 0)
		for memoryID := range recalled {
			if _, ok := expected[memoryID]; ok {
				recalledExpected++
			} else {
				unexpectedIDs = append(unexpectedIDs, memoryID)
			}
		}
		sort.Strings(unexpectedIDs)
		caseRecall := ratio(recalledExpected, len(expected))
		if len(expected) == 0 {
			caseRecall = 1
		}
		report.Cases = append(report.Cases, CaseResult{
			ID: item.ID, Question: item.Question,
			ExpectedMemoryIDs:   append([]string{}, item.ExpectedMemoryIDs...),
			UnexpectedMemoryIDs: unexpectedIDs, RecallAtK: caseRecall,
			Control: control, Treatment: treatment,
		})

		totalExpectedMemories += len(expected)
		recalledExpectedMemories += recalledExpected
		unexpectedMemories += len(unexpectedIDs)
		totalExpectedFacts += len(item.ExpectedFacts)
		controlPresentFacts += len(control.PresentFactIDs)
		treatmentPresentFacts += len(treatment.PresentFactIDs)
		totalForbiddenFacts += len(item.ForbiddenTreatmentFacts)
		controlForbiddenFacts += len(control.PresentForbiddenFactIDs)
		treatmentForbiddenFacts += len(treatment.PresentForbiddenFactIDs)
		controlLatency += control.LatencyMS
		treatmentLatency += treatment.LatencyMS
		report.Control.RecalledMemories += len(control.RecalledMemoryIDs)
		report.Treatment.RecalledMemories += len(treatment.RecalledMemoryIDs)
	}

	count := float64(len(dataset.Cases))
	report.Control.FactCoverage = ratio(controlPresentFacts, totalExpectedFacts)
	report.Treatment.FactCoverage = ratio(treatmentPresentFacts, totalExpectedFacts)
	report.Control.ForbiddenFactRate = ratio(controlForbiddenFacts, totalForbiddenFacts)
	report.Treatment.ForbiddenFactRate = ratio(treatmentForbiddenFacts, totalForbiddenFacts)
	report.Control.AverageLatencyMS = rounded(controlLatency / count)
	report.Treatment.AverageLatencyMS = rounded(treatmentLatency / count)
	report.Comparison = Comparison{
		RecallAtK:                ratio(recalledExpectedMemories, totalExpectedMemories),
		UnexpectedRecallRate:     ratio(unexpectedMemories, report.Treatment.RecalledMemories),
		FactCoverageDelta:        rounded(report.Treatment.FactCoverage - report.Control.FactCoverage),
		AverageLatencyOverheadMS: rounded(report.Treatment.AverageLatencyMS - report.Control.AverageLatencyMS),
	}
	report.Passed = report.Control.RecalledMemories == 0 &&
		report.Comparison.RecallAtK >= dataset.MinimumRecallAtK &&
		report.Comparison.UnexpectedRecallRate <= dataset.MaximumUnexpectedRecallRate &&
		report.Treatment.FactCoverage >= dataset.MinimumTreatmentFactCoverage &&
		report.Comparison.FactCoverageDelta >= dataset.MinimumFactCoverageDelta &&
		report.Treatment.ForbiddenFactRate <= dataset.MaximumTreatmentForbiddenRate
	return report, nil
}

func evaluateVariant(ctx context.Context, answerer Answerer, variant string, item Case) (VariantCaseResult, error) {
	startedAt := time.Now()
	generated, err := answerer.Answer(ctx, variant, item.Question)
	latencyMS := float64(time.Since(startedAt).Microseconds()) / 1000
	if err != nil {
		return VariantCaseResult{}, err
	}
	result := VariantCaseResult{
		Answer:            generated.Content,
		RecalledMemoryIDs: uniqueSorted(generated.RecalledMemoryIDs),
		LatencyMS:         rounded(latencyMS),
	}
	for _, fact := range item.ExpectedFacts {
		if containsAll(generated.Content, fact.AnswerContains) {
			result.PresentFactIDs = append(result.PresentFactIDs, fact.ID)
		}
	}
	for _, fact := range item.ForbiddenTreatmentFacts {
		if containsAll(generated.Content, fact.AnswerContains) {
			result.PresentForbiddenFactIDs = append(result.PresentForbiddenFactIDs, fact.ID)
		}
	}
	result.FactCoverage = ratio(len(result.PresentFactIDs), len(item.ExpectedFacts))
	result.ForbiddenFactRate = ratio(len(result.PresentForbiddenFactIDs), len(item.ForbiddenTreatmentFacts))
	return result, nil
}

func containsAll(value string, anchors []string) bool {
	normalizedValue := normalize(value)
	for _, anchor := range anchors {
		if !strings.Contains(normalizedValue, normalize(anchor)) {
			return false
		}
	}
	return true
}

func normalize(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func uniqueSorted(values []string) []string {
	set := stringSet(values)
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return rounded(float64(numerator) / float64(denominator))
}

func rounded(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}
