package rageval_test

import (
	"context"
	"testing"

	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/rageval"
)

type fakeAnswerer struct{}

func (fakeAnswerer) Answer(_ context.Context, question string) (rageval.GeneratedAnswer, error) {
	switch question {
	case "正确问题":
		return rageval.GeneratedAnswer{
			Content: "住宿标准是每晚 600 元。[制度.md#0]",
			Evidence: []knowledge.SearchResult{{
				DocumentName: "制度.md", Ordinal: 0, Content: "住宿费报销上限为每晚 600 元。",
			}},
		}, nil
	case "伪造引用":
		return rageval.GeneratedAnswer{
			Content:  "响应时间是 5 分钟。[手册.md#9]",
			Evidence: []knowledge.SearchResult{{DocumentName: "手册.md", Ordinal: 0, Content: "响应时间是 5 分钟。"}},
		}, nil
	default:
		return rageval.GeneratedAnswer{
			Content:  "默认端口是 8088。[规范.md#0]",
			Evidence: []knowledge.SearchResult{{DocumentName: "规范.md", Ordinal: 0, Content: "默认端口是 9090。"}},
		}, nil
	}
}

func TestEvaluateAnswersScoresCoverageAndFaithfulness(t *testing.T) {
	t.Parallel()
	dataset := rageval.Dataset{
		Name: "answer-test", TopK: 1,
		MinimumFactCoverage: 1, MinimumCitationCoverage: 0.6, MinimumCitationFaithfulness: 0.4,
		Documents: []rageval.FixtureDocument{
			{Name: "制度.md", Content: "制度"},
			{Name: "手册.md", Content: "手册"},
			{Name: "规范.md", Content: "规范"},
		},
		Cases: []rageval.Case{
			{
				ID: "good", Question: "正确问题", RelevantDocuments: []string{"制度.md"},
				ExpectedFacts: []rageval.ExpectedFact{{
					ID: "limit", AnswerContains: []string{"每晚", "600元"}, EvidenceContains: []string{"每晚", "600元"},
				}},
			},
			{
				ID: "fake", Question: "伪造引用", RelevantDocuments: []string{"手册.md"},
				ExpectedFacts: []rageval.ExpectedFact{{
					ID: "response", AnswerContains: []string{"5分钟"}, EvidenceContains: []string{"5分钟"},
				}},
			},
			{
				ID: "unsupported", Question: "原文不支持", RelevantDocuments: []string{"规范.md"},
				ExpectedFacts: []rageval.ExpectedFact{{
					ID: "port", AnswerContains: []string{"8088"}, EvidenceContains: []string{"8088"},
				}},
			},
		},
	}

	report, err := rageval.EvaluateAnswers(context.Background(), fakeAnswerer{}, dataset)
	if err != nil {
		t.Fatal(err)
	}
	if report.FactCoverage != 1 || report.CitationCoverage != 0.666667 || report.CitationFaithfulness != 0.5 {
		t.Fatalf("unexpected answer metrics: %+v", report)
	}
	if !report.Passed {
		t.Fatalf("expected thresholds to pass: %+v", report)
	}
	if !report.Cases[0].Facts[0].Supported {
		t.Fatalf("expected first fact to be supported: %+v", report.Cases[0])
	}
	if report.Cases[1].Facts[0].ValidCitation {
		t.Fatalf("forged citation must not resolve: %+v", report.Cases[1])
	}
	if !report.Cases[2].Facts[0].ValidCitation || report.Cases[2].Facts[0].Supported {
		t.Fatalf("valid but contradicting evidence must be unfaithful: %+v", report.Cases[2])
	}
}

func TestAttachAnswerReportCombinesGates(t *testing.T) {
	t.Parallel()
	report := rageval.AttachAnswerReport(rageval.Report{Passed: true}, rageval.AnswerReport{Passed: false})
	if report.Passed || report.Answer == nil {
		t.Fatalf("unexpected combined report: %+v", report)
	}
}
