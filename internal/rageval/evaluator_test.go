package rageval_test

import (
	"context"
	"strings"
	"testing"

	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/rageval"
)

type fakeSearcher struct{}

func (fakeSearcher) SearchWithMode(_ context.Context, query string, _ int, mode knowledge.RetrievalMode) ([]knowledge.SearchResult, error) {
	if query == "问题一" {
		switch mode {
		case knowledge.RetrievalVector:
			return []knowledge.SearchResult{{DocumentName: "干扰.md"}, {DocumentName: "答案.md"}}, nil
		case knowledge.RetrievalKeyword:
			return []knowledge.SearchResult{{DocumentName: "答案.md"}}, nil
		default:
			return []knowledge.SearchResult{{DocumentName: "答案.md"}}, nil
		}
	}
	return []knowledge.SearchResult{{DocumentName: "干扰.md"}}, nil
}

func TestEvaluateMetricsAndComparison(t *testing.T) {
	t.Parallel()
	dataset := rageval.Dataset{
		Name: "test", TopK: 2, MinimumRecallAtK: 0.5, MinimumMRR: 0.5,
		Documents: []rageval.FixtureDocument{
			{Name: "答案.md", Content: "答案"},
			{Name: "干扰.md", Content: "干扰"},
		},
		Cases: []rageval.Case{
			{ID: "one", Question: "问题一", RelevantDocuments: []string{"答案.md"}},
			{ID: "two", Question: "问题二", RelevantDocuments: []string{"答案.md"}},
		},
	}
	report, err := rageval.Evaluate(context.Background(), fakeSearcher{}, dataset)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || report.CaseCount != 2 || len(report.Modes) != 3 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Modes[0].RecallAtK != 0.5 || report.Modes[0].MRR != 0.25 {
		t.Fatalf("unexpected vector metrics: %+v", report.Modes[0])
	}
	if report.Modes[2].RecallAtK != 0.5 || report.Modes[2].MRR != 0.5 {
		t.Fatalf("unexpected hybrid metrics: %+v", report.Modes[2])
	}
	if report.Comparison.HybridMRRDeltaVsVector != 0.25 {
		t.Fatalf("unexpected comparison: %+v", report.Comparison)
	}
}

func TestLoadDatasetRejectsUnknownAndMissingDocuments(t *testing.T) {
	t.Parallel()
	_, err := rageval.LoadDataset(strings.NewReader(`{
		"name":"bad","top_k":3,"minimum_recall_at_k":0,"minimum_mrr":0,
		"documents":[{"name":"a.md","content":"a"}],
		"cases":[{"id":"q","question":"q","relevant_documents":["missing.md"]}]
	}`))
	if err == nil || !strings.Contains(err.Error(), "不存在的文档") {
		t.Fatalf("error = %v", err)
	}

	_, err = rageval.LoadDataset(strings.NewReader(`{"name":"bad","unknown":true}`))
	if err == nil || !strings.Contains(err.Error(), "解析 RAG 评测集失败") {
		t.Fatalf("error = %v", err)
	}
}
