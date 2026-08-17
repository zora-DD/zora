// Package agentseval 提供多 Agent 路由与协作链路的离线质量门禁。
package agentseval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/zhiruo/zora/internal/agentruntime"
)

type Dataset struct {
	Name                       string     `json:"name"`
	Description                string     `json:"description,omitempty"`
	MinimumRouteAccuracy       float64    `json:"minimum_route_accuracy"`
	MaximumUnexpectedAgentRate float64    `json:"maximum_unexpected_agent_rate"`
	MinimumAnswerCompletion    float64    `json:"minimum_answer_completion"`
	MinimumQualityGain         float64    `json:"minimum_quality_gain"`
	MaximumLatencyRatio        float64    `json:"maximum_latency_ratio"`
	MaximumInvocationRatio     float64    `json:"maximum_invocation_ratio"`
	Documents                  []Document `json:"documents,omitempty"`
	Cases                      []Case     `json:"cases"`
}

type Document struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type Case struct {
	ID                     string   `json:"id"`
	Question               string   `json:"question"`
	ExpectedAgents         []string `json:"expected_agents"`
	ExpectedAnswerContains []string `json:"expected_answer_contains,omitempty"`
}

type GeneratedAnswer struct {
	Content  string
	Handoffs []string
}

type Answerer interface {
	Answer(ctx context.Context, question string) (GeneratedAnswer, error)
}

type Report struct {
	Dataset    string            `json:"dataset"`
	Provider   string            `json:"provider,omitempty"`
	Model      string            `json:"model,omitempty"`
	CaseCount  int               `json:"case_count"`
	Thresholds Thresholds        `json:"thresholds"`
	Metrics    Metrics           `json:"metrics"`
	Cases      []CaseResult      `json:"cases"`
	Passed     bool              `json:"passed"`
	Comparison *ComparisonReport `json:"comparison,omitempty"`
}

type ComparisonReport struct {
	ControlName   string                 `json:"control_name"`
	TreatmentName string                 `json:"treatment_name"`
	Thresholds    ComparisonThresholds   `json:"thresholds"`
	Metrics       ComparisonMetrics      `json:"metrics"`
	Cases         []ComparisonCaseResult `json:"cases"`
	Passed        bool                   `json:"passed"`
}

type ComparisonThresholds struct {
	MinimumQualityGain     float64 `json:"minimum_quality_gain"`
	MaximumLatencyRatio    float64 `json:"maximum_latency_ratio"`
	MaximumInvocationRatio float64 `json:"maximum_invocation_ratio"`
}

type ComparisonMetrics struct {
	ControlQuality              float64 `json:"control_quality"`
	TreatmentQuality            float64 `json:"treatment_quality"`
	QualityGain                 float64 `json:"quality_gain"`
	ControlAverageLatencyMS     float64 `json:"control_average_latency_ms"`
	TreatmentAverageLatencyMS   float64 `json:"treatment_average_latency_ms"`
	LatencyRatio                float64 `json:"latency_ratio"`
	ControlAverageInvocations   float64 `json:"control_average_invocations"`
	TreatmentAverageInvocations float64 `json:"treatment_average_invocations"`
	InvocationRatio             float64 `json:"invocation_ratio"`
}

type ComparisonCaseResult struct {
	ID                   string  `json:"id"`
	ControlQuality       float64 `json:"control_quality"`
	TreatmentQuality     float64 `json:"treatment_quality"`
	ControlLatencyMS     float64 `json:"control_latency_ms"`
	TreatmentLatencyMS   float64 `json:"treatment_latency_ms"`
	ControlInvocations   int     `json:"control_invocations"`
	TreatmentInvocations int     `json:"treatment_invocations"`
}

type Thresholds struct {
	MinimumRouteAccuracy       float64 `json:"minimum_route_accuracy"`
	MaximumUnexpectedAgentRate float64 `json:"maximum_unexpected_agent_rate"`
	MinimumAnswerCompletion    float64 `json:"minimum_answer_completion"`
}

type Metrics struct {
	RouteAccuracy       float64 `json:"route_accuracy"`
	UnexpectedAgentRate float64 `json:"unexpected_agent_rate"`
	AnswerCompletion    float64 `json:"answer_completion"`
	AverageLatencyMS    float64 `json:"average_latency_ms"`
}

type CaseResult struct {
	ID               string   `json:"id"`
	Question         string   `json:"question"`
	ExpectedAgents   []string `json:"expected_agents"`
	ActualAgents     []string `json:"actual_agents"`
	UnexpectedAgents []string `json:"unexpected_agents,omitempty"`
	RouteMatched     bool     `json:"route_matched"`
	AnswerCompleted  bool     `json:"answer_completed"`
	Answer           string   `json:"answer"`
	LatencyMS        float64  `json:"latency_ms"`
}

func LoadDataset(reader io.Reader) (Dataset, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 5<<20))
	decoder.DisallowUnknownFields()
	var dataset Dataset
	if err := decoder.Decode(&dataset); err != nil {
		return Dataset{}, fmt.Errorf("解析多 Agent 评测集失败：%w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Dataset{}, fmt.Errorf("多 Agent 评测集只能包含一个 JSON 对象")
	}
	if err := dataset.Validate(); err != nil {
		return Dataset{}, err
	}
	return dataset, nil
}

func (d Dataset) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("多 Agent 评测集名称不能为空")
	}
	metrics := []struct {
		name  string
		value float64
	}{
		{name: "minimum_route_accuracy", value: d.MinimumRouteAccuracy},
		{name: "maximum_unexpected_agent_rate", value: d.MaximumUnexpectedAgentRate},
		{name: "minimum_answer_completion", value: d.MinimumAnswerCompletion},
	}
	for _, metric := range metrics {
		if math.IsNaN(metric.value) || math.IsInf(metric.value, 0) || metric.value < 0 || metric.value > 1 {
			return fmt.Errorf("多 Agent 评测阈值 %s 必须在 0 到 1 之间", metric.name)
		}
	}
	if math.IsNaN(d.MinimumQualityGain) || math.IsInf(d.MinimumQualityGain, 0) || d.MinimumQualityGain < -1 || d.MinimumQualityGain > 1 {
		return fmt.Errorf("多 Agent 评测阈值 minimum_quality_gain 必须在 -1 到 1 之间")
	}
	if math.IsNaN(d.MaximumLatencyRatio) || math.IsInf(d.MaximumLatencyRatio, 0) || d.MaximumLatencyRatio < 0 {
		return fmt.Errorf("多 Agent 评测阈值 maximum_latency_ratio 不能小于 0")
	}
	if math.IsNaN(d.MaximumInvocationRatio) || math.IsInf(d.MaximumInvocationRatio, 0) || d.MaximumInvocationRatio < 0 {
		return fmt.Errorf("多 Agent 评测阈值 maximum_invocation_ratio 不能小于 0")
	}
	if len(d.Cases) == 0 {
		return fmt.Errorf("多 Agent 评测集必须至少包含一个问题")
	}
	for index, document := range d.Documents {
		if strings.TrimSpace(document.Name) == "" || strings.TrimSpace(document.Content) == "" {
			return fmt.Errorf("第 %d 份评测文档的名称和内容不能为空", index+1)
		}
	}
	allowedAgents := map[string]struct{}{
		agentruntime.ResearchAgentName: {},
		agentruntime.DocumentAgentName: {},
		agentruntime.WriterAgentName:   {},
	}
	caseIDs := make(map[string]struct{}, len(d.Cases))
	for index, item := range d.Cases {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Question) == "" {
			return fmt.Errorf("第 %d 个评测问题的 ID 和问题不能为空", index+1)
		}
		if _, exists := caseIDs[item.ID]; exists {
			return fmt.Errorf("评测问题 ID 重复：%s", item.ID)
		}
		caseIDs[item.ID] = struct{}{}
		seenAgents := make(map[string]struct{}, len(item.ExpectedAgents))
		for _, name := range item.ExpectedAgents {
			if _, exists := allowedAgents[name]; !exists {
				return fmt.Errorf("评测问题 %s 使用了未知专业 Agent：%s", item.ID, name)
			}
			if _, exists := seenAgents[name]; exists {
				return fmt.Errorf("评测问题 %s 重复配置专业 Agent：%s", item.ID, name)
			}
			seenAgents[name] = struct{}{}
		}
		for _, anchor := range item.ExpectedAnswerContains {
			if strings.TrimSpace(anchor) == "" {
				return fmt.Errorf("评测问题 %s 包含空答案锚点", item.ID)
			}
		}
	}
	return nil
}
