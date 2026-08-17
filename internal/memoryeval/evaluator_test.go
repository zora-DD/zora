package memoryeval_test

import (
	"context"
	"strings"
	"testing"

	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/memoryeval"
)

type fakeAnswerer struct {
	contaminate bool
}

func (f fakeAnswerer) Answer(_ context.Context, variant, question string) (memoryeval.GeneratedAnswer, error) {
	if variant == memoryeval.VariantControl {
		return memoryeval.GeneratedAnswer{Content: "控制组不知道用户的私人信息。"}, nil
	}
	switch question {
	case "我的语言？":
		return memoryeval.GeneratedAnswer{Content: "主要编程语言是 Go。", RecalledMemoryIDs: []string{"mem_language"}}, nil
	case "我的项目？":
		return memoryeval.GeneratedAnswer{Content: "项目名是 Zora。", RecalledMemoryIDs: []string{"mem_project"}}, nil
	default:
		if f.contaminate {
			return memoryeval.GeneratedAnswer{
				Content: "根据长期记忆，主要编程语言是 Go。", RecalledMemoryIDs: []string{"mem_language"},
			}, nil
		}
		return memoryeval.GeneratedAnswer{Content: "这是一个通用知识问题。"}, nil
	}
}

func TestEvaluateMemoryABMetricsAndGate(t *testing.T) {
	t.Parallel()
	dataset := testDataset()
	report, err := memoryeval.Evaluate(context.Background(), fakeAnswerer{}, dataset)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || report.Comparison.RecallAtK != 1 || report.Comparison.UnexpectedRecallRate != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Control.FactCoverage != 0 || report.Treatment.FactCoverage != 1 || report.Comparison.FactCoverageDelta != 1 {
		t.Fatalf("unexpected answer comparison: %+v", report)
	}
	if report.Control.RecalledMemories != 0 || report.Treatment.RecalledMemories != 2 {
		t.Fatalf("unexpected recall counts: %+v", report)
	}
}

func TestEvaluateFailsOnUnexpectedRecallAndAnswerContamination(t *testing.T) {
	t.Parallel()
	report, err := memoryeval.Evaluate(context.Background(), fakeAnswerer{contaminate: true}, testDataset())
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.Comparison.UnexpectedRecallRate == 0 || report.Treatment.ForbiddenFactRate != 1 {
		t.Fatalf("contamination should fail gate: %+v", report)
	}
	negative := report.Cases[2]
	if len(negative.UnexpectedMemoryIDs) != 1 || negative.UnexpectedMemoryIDs[0] != "mem_language" {
		t.Fatalf("unexpected negative case: %+v", negative)
	}
}

func TestLoadDatasetRejectsUnknownMemoryAndField(t *testing.T) {
	t.Parallel()
	_, err := memoryeval.LoadDataset(strings.NewReader(`{
		"name":"bad","top_k":1,"recall_min_score":0.25,
		"minimum_recall_at_k":0,"maximum_unexpected_recall_rate":1,
		"minimum_treatment_fact_coverage":0,"minimum_fact_coverage_delta":0,
		"maximum_treatment_forbidden_fact_rate":1,
		"memories":[{"id":"mem","kind":"semantic","memory_key":"key","content":"内容","importance":0.5}],
		"cases":[{"id":"q","question":"问题","expected_memory_ids":["missing"],"expected_facts":[{"id":"f","answer_contains":["答案"]}]}]
	}`))
	if err == nil || !strings.Contains(err.Error(), "不存在的记忆") {
		t.Fatalf("error = %v", err)
	}

	_, err = memoryeval.LoadDataset(strings.NewReader(`{"name":"bad","unknown":true}`))
	if err == nil || !strings.Contains(err.Error(), "解析长期记忆评测集失败") {
		t.Fatalf("error = %v", err)
	}
}

func testDataset() memoryeval.Dataset {
	return memoryeval.Dataset{
		Name: "memory-test", TopK: 2, RecallMinScore: 0.25,
		MinimumRecallAtK: 1, MaximumUnexpectedRecallRate: 0,
		MinimumTreatmentFactCoverage: 1, MinimumFactCoverageDelta: 1,
		MaximumTreatmentForbiddenRate: 0,
		Memories: []memoryeval.FixtureMemory{
			{ID: "mem_language", Kind: memory.KindSemantic, MemoryKey: "profile:language", Content: "主要编程语言是 Go", Importance: 0.9},
			{ID: "mem_project", Kind: memory.KindEpisodic, MemoryKey: "project:zora", Content: "项目名是 Zora", Importance: 0.9},
		},
		Cases: []memoryeval.Case{
			{
				ID: "language", Question: "我的语言？", ExpectedMemoryIDs: []string{"mem_language"},
				ExpectedFacts: []memoryeval.Fact{{ID: "go", AnswerContains: []string{"主要编程语言", "Go"}}},
			},
			{
				ID: "project", Question: "我的项目？", ExpectedMemoryIDs: []string{"mem_project"},
				ExpectedFacts: []memoryeval.Fact{{ID: "zora", AnswerContains: []string{"Zora"}}},
			},
			{
				ID: "negative", Question: "通用问题？",
				ForbiddenTreatmentFacts: []memoryeval.Fact{{
					ID: "contamination", AnswerContains: []string{"根据长期记忆", "主要编程语言", "Go"},
				}},
			},
		},
	}
}
