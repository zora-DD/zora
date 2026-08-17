// Package memoryeval 提供长期记忆有/无对照评测与质量门禁。
package memoryeval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/zhiruo/zora/internal/memory"
)

const (
	VariantControl   = "control"
	VariantTreatment = "treatment"
)

// Dataset 将固定记忆、问题、事实锚点与阈值放在同一版本化文件中。
type Dataset struct {
	Name                          string          `json:"name"`
	Description                   string          `json:"description,omitempty"`
	TopK                          int             `json:"top_k"`
	RecallMinScore                float64         `json:"recall_min_score"`
	MinimumRecallAtK              float64         `json:"minimum_recall_at_k"`
	MaximumUnexpectedRecallRate   float64         `json:"maximum_unexpected_recall_rate"`
	MinimumTreatmentFactCoverage  float64         `json:"minimum_treatment_fact_coverage"`
	MinimumFactCoverageDelta      float64         `json:"minimum_fact_coverage_delta"`
	MaximumTreatmentForbiddenRate float64         `json:"maximum_treatment_forbidden_fact_rate"`
	Memories                      []FixtureMemory `json:"memories"`
	Cases                         []Case          `json:"cases"`
}

type FixtureMemory struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	MemoryKey  string  `json:"memory_key"`
	Content    string  `json:"content"`
	Importance float64 `json:"importance"`
}

type Case struct {
	ID                      string   `json:"id"`
	Question                string   `json:"question"`
	ExpectedMemoryIDs       []string `json:"expected_memory_ids"`
	ExpectedFacts           []Fact   `json:"expected_facts,omitempty"`
	ForbiddenTreatmentFacts []Fact   `json:"forbidden_treatment_facts,omitempty"`
}

type Fact struct {
	ID             string   `json:"id"`
	AnswerContains []string `json:"answer_contains"`
}

// GeneratedAnswer 来自真实 Chat 链路；RecalledMemoryIDs 从该 Run 的审计事件提取。
type GeneratedAnswer struct {
	Content           string
	RecalledMemoryIDs []string
}

type Answerer interface {
	Answer(ctx context.Context, variant string, question string) (GeneratedAnswer, error)
}

type Report struct {
	Dataset    string        `json:"dataset"`
	Provider   string        `json:"provider,omitempty"`
	Model      string        `json:"model,omitempty"`
	TopK       int           `json:"top_k"`
	CaseCount  int           `json:"case_count"`
	Thresholds Thresholds    `json:"thresholds"`
	Control    VariantReport `json:"control"`
	Treatment  VariantReport `json:"treatment"`
	Comparison Comparison    `json:"comparison"`
	Cases      []CaseResult  `json:"cases"`
	Passed     bool          `json:"passed"`
}

type Thresholds struct {
	RecallMinScore                float64 `json:"recall_min_score"`
	MinimumRecallAtK              float64 `json:"minimum_recall_at_k"`
	MaximumUnexpectedRecallRate   float64 `json:"maximum_unexpected_recall_rate"`
	MinimumTreatmentFactCoverage  float64 `json:"minimum_treatment_fact_coverage"`
	MinimumFactCoverageDelta      float64 `json:"minimum_fact_coverage_delta"`
	MaximumTreatmentForbiddenRate float64 `json:"maximum_treatment_forbidden_fact_rate"`
}

type VariantReport struct {
	FactCoverage      float64 `json:"fact_coverage"`
	ForbiddenFactRate float64 `json:"forbidden_fact_rate"`
	AverageLatencyMS  float64 `json:"average_latency_ms"`
	RecalledMemories  int     `json:"recalled_memories"`
}

type Comparison struct {
	RecallAtK                float64 `json:"recall_at_k"`
	UnexpectedRecallRate     float64 `json:"unexpected_recall_rate"`
	FactCoverageDelta        float64 `json:"fact_coverage_delta"`
	AverageLatencyOverheadMS float64 `json:"average_latency_overhead_ms"`
}

type CaseResult struct {
	ID                  string            `json:"id"`
	Question            string            `json:"question"`
	ExpectedMemoryIDs   []string          `json:"expected_memory_ids"`
	UnexpectedMemoryIDs []string          `json:"unexpected_memory_ids,omitempty"`
	RecallAtK           float64           `json:"recall_at_k"`
	Control             VariantCaseResult `json:"control"`
	Treatment           VariantCaseResult `json:"treatment"`
}

type VariantCaseResult struct {
	Answer                  string   `json:"answer"`
	RecalledMemoryIDs       []string `json:"recalled_memory_ids"`
	PresentFactIDs          []string `json:"present_fact_ids,omitempty"`
	PresentForbiddenFactIDs []string `json:"present_forbidden_fact_ids,omitempty"`
	FactCoverage            float64  `json:"fact_coverage"`
	ForbiddenFactRate       float64  `json:"forbidden_fact_rate"`
	LatencyMS               float64  `json:"latency_ms"`
}

// LoadDataset 使用严格 JSON，避免字段拼写错误让评测静默失真。
func LoadDataset(reader io.Reader) (Dataset, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 5<<20))
	decoder.DisallowUnknownFields()
	var dataset Dataset
	if err := decoder.Decode(&dataset); err != nil {
		return Dataset{}, fmt.Errorf("解析长期记忆评测集失败：%w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Dataset{}, fmt.Errorf("长期记忆评测集只能包含一个 JSON 对象")
	}
	if err := dataset.Validate(); err != nil {
		return Dataset{}, err
	}
	return dataset, nil
}

func (d Dataset) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("长期记忆评测集名称不能为空")
	}
	if d.TopK < 1 || d.TopK > 20 {
		return fmt.Errorf("长期记忆评测集 top_k 必须在 1 到 20 之间")
	}
	metrics := []struct {
		name  string
		value float64
	}{
		{name: "recall_min_score", value: d.RecallMinScore},
		{name: "minimum_recall_at_k", value: d.MinimumRecallAtK},
		{name: "maximum_unexpected_recall_rate", value: d.MaximumUnexpectedRecallRate},
		{name: "minimum_treatment_fact_coverage", value: d.MinimumTreatmentFactCoverage},
		{name: "minimum_fact_coverage_delta", value: d.MinimumFactCoverageDelta},
		{name: "maximum_treatment_forbidden_fact_rate", value: d.MaximumTreatmentForbiddenRate},
	}
	for _, metric := range metrics {
		if !validMetric(metric.value) {
			return fmt.Errorf("长期记忆评测阈值 %s 必须在 0 到 1 之间", metric.name)
		}
	}
	if len(d.Memories) == 0 || len(d.Cases) == 0 {
		return fmt.Errorf("长期记忆评测集必须至少包含一条记忆和一个问题")
	}

	memoryIDs := make(map[string]struct{}, len(d.Memories))
	for index, item := range d.Memories {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.MemoryKey) == "" || strings.TrimSpace(item.Content) == "" {
			return fmt.Errorf("第 %d 条评测记忆的 ID、memory_key 和内容不能为空", index+1)
		}
		if item.Kind != memory.KindSemantic && item.Kind != memory.KindEpisodic {
			return fmt.Errorf("评测记忆 %s 的类型必须是 semantic 或 episodic", item.ID)
		}
		if !validMetric(item.Importance) {
			return fmt.Errorf("评测记忆 %s 的重要性必须在 0 到 1 之间", item.ID)
		}
		if _, exists := memoryIDs[item.ID]; exists {
			return fmt.Errorf("评测记忆 ID 重复：%s", item.ID)
		}
		memoryIDs[item.ID] = struct{}{}
	}

	caseIDs := make(map[string]struct{}, len(d.Cases))
	var expectedMemoryCount, expectedFactCount int
	for index, item := range d.Cases {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Question) == "" {
			return fmt.Errorf("第 %d 个评测问题的 ID 和问题不能为空", index+1)
		}
		if _, exists := caseIDs[item.ID]; exists {
			return fmt.Errorf("评测问题 ID 重复：%s", item.ID)
		}
		caseIDs[item.ID] = struct{}{}
		seenMemoryIDs := make(map[string]struct{}, len(item.ExpectedMemoryIDs))
		for _, memoryID := range item.ExpectedMemoryIDs {
			if _, exists := memoryIDs[memoryID]; !exists {
				return fmt.Errorf("评测问题 %s 引用了不存在的记忆：%s", item.ID, memoryID)
			}
			if _, exists := seenMemoryIDs[memoryID]; exists {
				return fmt.Errorf("评测问题 %s 重复引用记忆：%s", item.ID, memoryID)
			}
			seenMemoryIDs[memoryID] = struct{}{}
		}
		if len(item.ExpectedMemoryIDs) > 0 && len(item.ExpectedFacts) == 0 {
			return fmt.Errorf("正向评测问题 %s 必须配置 expected_facts", item.ID)
		}
		if err := validateFacts(item.ID, item.ExpectedFacts, item.ForbiddenTreatmentFacts); err != nil {
			return err
		}
		expectedMemoryCount += len(item.ExpectedMemoryIDs)
		expectedFactCount += len(item.ExpectedFacts)
	}
	if expectedMemoryCount == 0 || expectedFactCount == 0 {
		return fmt.Errorf("长期记忆评测集必须包含正向召回问题和答案事实")
	}
	return nil
}

func validateFacts(caseID string, expected, forbidden []Fact) error {
	ids := make(map[string]struct{}, len(expected)+len(forbidden))
	for _, group := range [][]Fact{expected, forbidden} {
		for _, fact := range group {
			if strings.TrimSpace(fact.ID) == "" || len(fact.AnswerContains) == 0 {
				return fmt.Errorf("评测问题 %s 的答案事实配置不完整", caseID)
			}
			if _, exists := ids[fact.ID]; exists {
				return fmt.Errorf("评测问题 %s 的答案事实 ID 重复：%s", caseID, fact.ID)
			}
			ids[fact.ID] = struct{}{}
			for _, anchor := range fact.AnswerContains {
				if strings.TrimSpace(anchor) == "" {
					return fmt.Errorf("评测问题 %s 的答案事实 %s 包含空锚点", caseID, fact.ID)
				}
			}
		}
	}
	return nil
}

func validMetric(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}
