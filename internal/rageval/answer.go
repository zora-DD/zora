package rageval

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/zhiruo/zora/internal/knowledge"
)

var citationPattern = regexp.MustCompile(`\[([^\[\]\r\n]+)#([0-9]+)\]`)

// GeneratedAnswer 保留最终回答和该次回答真正看见的工具证据。
// 评测只允许引用这些证据，避免“引用格式正确但文档并未被检索到”的假阳性。
type GeneratedAnswer struct {
	Content  string
	Evidence []knowledge.SearchResult
}

// Answerer 是答案生成的最小边界。命令行使用真实 Agent Runtime，测试可以使用确定性桩。
type Answerer interface {
	Answer(ctx context.Context, question string) (GeneratedAnswer, error)
}

type AnswerReport struct {
	CaseCount            int                `json:"case_count"`
	FactCoverage         float64            `json:"fact_coverage"`
	CitationCoverage     float64            `json:"citation_coverage"`
	CitationFaithfulness float64            `json:"citation_faithfulness"`
	Cases                []AnswerCaseResult `json:"cases"`
	Passed               bool               `json:"passed"`
}

type AnswerCaseResult struct {
	ID                   string             `json:"id"`
	Question             string             `json:"question"`
	Answer               string             `json:"answer"`
	FactCoverage         float64            `json:"fact_coverage"`
	CitationCoverage     float64            `json:"citation_coverage"`
	CitationFaithfulness float64            `json:"citation_faithfulness"`
	Facts                []AnswerFactResult `json:"facts"`
}

type AnswerFactResult struct {
	ID            string   `json:"id"`
	Present       bool     `json:"present"`
	ValidCitation bool     `json:"valid_citation"`
	Supported     bool     `json:"supported"`
	Citations     []string `json:"citations,omitempty"`
}

// EvaluateAnswers 以 expected_facts 为代理，对真实 Agent 回答执行确定性答案级评测。
// 指标定义：事实覆盖=出现事实/期望事实；引用覆盖=带有效引用的已出现事实/已出现事实；
// 引用忠实度=被引用原文支持的事实/带有效引用的事实。
func EvaluateAnswers(ctx context.Context, answerer Answerer, dataset Dataset) (AnswerReport, error) {
	if answerer == nil {
		return AnswerReport{}, fmt.Errorf("RAG 答案生成器不能为空")
	}
	if err := dataset.Validate(); err != nil {
		return AnswerReport{}, err
	}

	report := AnswerReport{CaseCount: len(dataset.Cases), Cases: make([]AnswerCaseResult, 0, len(dataset.Cases))}
	var totalFacts, presentFacts, citedFacts, supportedFacts int
	for _, item := range dataset.Cases {
		if err := ctx.Err(); err != nil {
			return AnswerReport{}, err
		}
		generated, err := answerer.Answer(ctx, item.Question)
		if err != nil {
			return AnswerReport{}, fmt.Errorf("评测问题 %s 生成答案失败：%w", item.ID, err)
		}
		caseResult, counts := scoreAnswer(item, generated)
		report.Cases = append(report.Cases, caseResult)
		totalFacts += counts.total
		presentFacts += counts.present
		citedFacts += counts.cited
		supportedFacts += counts.supported
	}
	report.FactCoverage = ratio(presentFacts, totalFacts)
	report.CitationCoverage = ratio(citedFacts, presentFacts)
	report.CitationFaithfulness = ratio(supportedFacts, citedFacts)
	report.Passed = report.FactCoverage >= dataset.MinimumFactCoverage &&
		report.CitationCoverage >= dataset.MinimumCitationCoverage &&
		report.CitationFaithfulness >= dataset.MinimumCitationFaithfulness
	return report, nil
}

// AttachAnswerReport 把检索和答案两层门禁合并为同一个最终结论。
func AttachAnswerReport(report Report, answerReport AnswerReport) Report {
	report.Answer = &answerReport
	report.Passed = report.Passed && answerReport.Passed
	return report
}

type answerCounts struct {
	total     int
	present   int
	cited     int
	supported int
}

func scoreAnswer(item Case, generated GeneratedAnswer) (AnswerCaseResult, answerCounts) {
	result := AnswerCaseResult{
		ID: item.ID, Question: item.Question, Answer: generated.Content,
		Facts: make([]AnswerFactResult, 0, len(item.ExpectedFacts)),
	}
	evidence := make(map[string]knowledge.SearchResult, len(generated.Evidence))
	for _, chunk := range generated.Evidence {
		evidence[citationKey(chunk.DocumentName, chunk.Ordinal)] = chunk
	}

	counts := answerCounts{total: len(item.ExpectedFacts)}
	lines := nonEmptyLines(generated.Content)
	for _, fact := range item.ExpectedFacts {
		factResult := AnswerFactResult{ID: fact.ID}
		for _, line := range lines {
			if !containsAll(line, fact.AnswerContains) {
				continue
			}
			factResult.Present = true
			for _, citation := range parseCitations(line) {
				factResult.Citations = append(factResult.Citations, citation.label)
				chunk, ok := evidence[citationKey(citation.document, citation.ordinal)]
				if !ok {
					continue
				}
				factResult.ValidCitation = true
				if containsAll(chunk.Content, fact.EvidenceContains) {
					factResult.Supported = true
				}
			}
		}
		if factResult.Present {
			counts.present++
		}
		if factResult.ValidCitation {
			counts.cited++
		}
		if factResult.Supported {
			counts.supported++
		}
		result.Facts = append(result.Facts, factResult)
	}
	result.FactCoverage = ratio(counts.present, counts.total)
	result.CitationCoverage = ratio(counts.cited, counts.present)
	result.CitationFaithfulness = ratio(counts.supported, counts.cited)
	return result, counts
}

type parsedCitation struct {
	label    string
	document string
	ordinal  int
}

func parseCitations(value string) []parsedCitation {
	matches := citationPattern.FindAllStringSubmatch(value, -1)
	result := make([]parsedCitation, 0, len(matches))
	for _, match := range matches {
		ordinal, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		result = append(result, parsedCitation{label: match[0], document: strings.TrimSpace(match[1]), ordinal: ordinal})
	}
	return result
}

func nonEmptyLines(value string) []string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			result = append(result, line)
		}
	}
	return result
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

func citationKey(document string, ordinal int) string {
	return strings.TrimSpace(document) + "#" + strconv.Itoa(ordinal)
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return rounded(float64(numerator) / float64(denominator))
}
