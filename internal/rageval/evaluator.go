// Package rageval 提供可重复运行的知识库检索评测。
package rageval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/zhiruo/zora/internal/knowledge"
)

// Dataset 同时保存固定语料和问题，避免评测依赖开发者本机已有数据。
type Dataset struct {
	Name             string            `json:"name"`
	Description      string            `json:"description,omitempty"`
	TopK             int               `json:"top_k"`
	MinimumRecallAtK float64           `json:"minimum_recall_at_k"`
	MinimumMRR       float64           `json:"minimum_mrr"`
	Documents        []FixtureDocument `json:"documents"`
	Cases            []Case            `json:"cases"`
}

type FixtureDocument struct {
	Name     string `json:"name"`
	MIMEType string `json:"mime_type,omitempty"`
	Content  string `json:"content"`
}

type Case struct {
	ID                string   `json:"id"`
	Question          string   `json:"question"`
	RelevantDocuments []string `json:"relevant_documents"`
}

// Searcher 只依赖评测所需的最小检索能力，便于用真实 Service 或测试桩运行。
type Searcher interface {
	SearchWithMode(ctx context.Context, query string, topK int, mode knowledge.RetrievalMode) ([]knowledge.SearchResult, error)
}

type Report struct {
	Dataset        string         `json:"dataset"`
	EmbeddingModel string         `json:"embedding_model,omitempty"`
	TopK           int            `json:"top_k"`
	CaseCount      int            `json:"case_count"`
	Thresholds     Thresholds     `json:"thresholds"`
	Modes          []ModeReport   `json:"modes"`
	Comparison     ModeComparison `json:"comparison"`
	Passed         bool           `json:"passed"`
}

type Thresholds struct {
	MinimumRecallAtK float64 `json:"minimum_recall_at_k"`
	MinimumMRR       float64 `json:"minimum_mrr"`
}

type ModeReport struct {
	Mode             knowledge.RetrievalMode `json:"mode"`
	RecallAtK        float64                 `json:"recall_at_k"`
	MRR              float64                 `json:"mrr"`
	HitRate          float64                 `json:"hit_rate"`
	AverageLatencyMS float64                 `json:"average_latency_ms"`
	Cases            []CaseResult            `json:"cases"`
}

type CaseResult struct {
	ID                 string   `json:"id"`
	Question           string   `json:"question"`
	RelevantDocuments  []string `json:"relevant_documents"`
	RetrievedDocuments []string `json:"retrieved_documents"`
	RecallAtK          float64  `json:"recall_at_k"`
	ReciprocalRank     float64  `json:"reciprocal_rank"`
	LatencyMS          float64  `json:"latency_ms"`
}

type ModeComparison struct {
	HybridRecallDeltaVsVector  float64 `json:"hybrid_recall_delta_vs_vector"`
	HybridRecallDeltaVsKeyword float64 `json:"hybrid_recall_delta_vs_keyword"`
	HybridMRRDeltaVsVector     float64 `json:"hybrid_mrr_delta_vs_vector"`
	HybridMRRDeltaVsKeyword    float64 `json:"hybrid_mrr_delta_vs_keyword"`
}

// LoadDataset 使用严格 JSON 解析，防止字段拼写错误让评测悄悄失真。
func LoadDataset(reader io.Reader) (Dataset, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 5<<20))
	decoder.DisallowUnknownFields()
	var dataset Dataset
	if err := decoder.Decode(&dataset); err != nil {
		return Dataset{}, fmt.Errorf("解析 RAG 评测集失败：%w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Dataset{}, fmt.Errorf("RAG 评测集只能包含一个 JSON 对象")
	}
	if err := dataset.Validate(); err != nil {
		return Dataset{}, err
	}
	return dataset, nil
}

func (d Dataset) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("评测集名称不能为空")
	}
	if d.TopK < 1 || d.TopK > 20 {
		return fmt.Errorf("评测集 top_k 必须在 1 到 20 之间")
	}
	if !validMetric(d.MinimumRecallAtK) || !validMetric(d.MinimumMRR) {
		return fmt.Errorf("评测阈值必须在 0 到 1 之间")
	}
	if len(d.Documents) == 0 || len(d.Cases) == 0 {
		return fmt.Errorf("评测集必须至少包含一份文档和一个问题")
	}

	documentNames := make(map[string]struct{}, len(d.Documents))
	for i, document := range d.Documents {
		name := strings.TrimSpace(document.Name)
		if name == "" || strings.TrimSpace(document.Content) == "" {
			return fmt.Errorf("第 %d 份评测文档的名称和内容不能为空", i+1)
		}
		if _, exists := documentNames[name]; exists {
			return fmt.Errorf("评测文档名称重复：%s", name)
		}
		documentNames[name] = struct{}{}
	}

	caseIDs := make(map[string]struct{}, len(d.Cases))
	for i, item := range d.Cases {
		id := strings.TrimSpace(item.ID)
		if id == "" || strings.TrimSpace(item.Question) == "" {
			return fmt.Errorf("第 %d 个评测问题的 ID 和问题不能为空", i+1)
		}
		if _, exists := caseIDs[id]; exists {
			return fmt.Errorf("评测问题 ID 重复：%s", id)
		}
		caseIDs[id] = struct{}{}
		if len(item.RelevantDocuments) == 0 {
			return fmt.Errorf("评测问题 %s 未配置相关文档", id)
		}
		seenRelevant := make(map[string]struct{}, len(item.RelevantDocuments))
		for _, name := range item.RelevantDocuments {
			name = strings.TrimSpace(name)
			if _, exists := documentNames[name]; !exists {
				return fmt.Errorf("评测问题 %s 引用了不存在的文档：%s", id, name)
			}
			if _, exists := seenRelevant[name]; exists {
				return fmt.Errorf("评测问题 %s 重复引用文档：%s", id, name)
			}
			seenRelevant[name] = struct{}{}
		}
	}
	return nil
}

// Evaluate 对同一问题集依次运行三种召回模式，并以 hybrid 指标判断阈值。
func Evaluate(ctx context.Context, searcher Searcher, dataset Dataset) (Report, error) {
	if searcher == nil {
		return Report{}, fmt.Errorf("RAG 评测检索器不能为空")
	}
	if err := dataset.Validate(); err != nil {
		return Report{}, err
	}

	report := Report{
		Dataset: dataset.Name, TopK: dataset.TopK, CaseCount: len(dataset.Cases),
		Thresholds: Thresholds{MinimumRecallAtK: dataset.MinimumRecallAtK, MinimumMRR: dataset.MinimumMRR},
		Modes:      make([]ModeReport, 0, 3),
	}
	for _, mode := range []knowledge.RetrievalMode{
		knowledge.RetrievalVector,
		knowledge.RetrievalKeyword,
		knowledge.RetrievalHybrid,
	} {
		modeReport, err := evaluateMode(ctx, searcher, dataset, mode)
		if err != nil {
			return Report{}, err
		}
		report.Modes = append(report.Modes, modeReport)
	}

	vector, keyword, hybrid := report.Modes[0], report.Modes[1], report.Modes[2]
	report.Comparison = ModeComparison{
		HybridRecallDeltaVsVector:  rounded(hybrid.RecallAtK - vector.RecallAtK),
		HybridRecallDeltaVsKeyword: rounded(hybrid.RecallAtK - keyword.RecallAtK),
		HybridMRRDeltaVsVector:     rounded(hybrid.MRR - vector.MRR),
		HybridMRRDeltaVsKeyword:    rounded(hybrid.MRR - keyword.MRR),
	}
	report.Passed = hybrid.RecallAtK >= dataset.MinimumRecallAtK && hybrid.MRR >= dataset.MinimumMRR
	return report, nil
}

func evaluateMode(ctx context.Context, searcher Searcher, dataset Dataset, mode knowledge.RetrievalMode) (ModeReport, error) {
	report := ModeReport{Mode: mode, Cases: make([]CaseResult, 0, len(dataset.Cases))}
	var recallSum, reciprocalRankSum, hitSum, latencySum float64
	for _, item := range dataset.Cases {
		startedAt := time.Now()
		results, err := searcher.SearchWithMode(ctx, item.Question, dataset.TopK, mode)
		latencyMS := float64(time.Since(startedAt).Microseconds()) / 1000
		if err != nil {
			return ModeReport{}, fmt.Errorf("评测问题 %s 在 %s 模式下检索失败：%w", item.ID, mode, err)
		}
		caseResult := scoreCase(item, results)
		caseResult.LatencyMS = rounded(latencyMS)
		report.Cases = append(report.Cases, caseResult)
		recallSum += caseResult.RecallAtK
		reciprocalRankSum += caseResult.ReciprocalRank
		latencySum += latencyMS
		if caseResult.ReciprocalRank > 0 {
			hitSum++
		}
	}
	count := float64(len(dataset.Cases))
	report.RecallAtK = rounded(recallSum / count)
	report.MRR = rounded(reciprocalRankSum / count)
	report.HitRate = rounded(hitSum / count)
	report.AverageLatencyMS = rounded(latencySum / count)
	return report, nil
}

func scoreCase(item Case, results []knowledge.SearchResult) CaseResult {
	relevant := make(map[string]struct{}, len(item.RelevantDocuments))
	for _, name := range item.RelevantDocuments {
		relevant[name] = struct{}{}
	}

	retrievedNames := make([]string, 0, len(results))
	retrievedRelevant := make(map[string]struct{}, len(relevant))
	firstRelevantRank := 0
	for i, result := range results {
		retrievedNames = append(retrievedNames, result.DocumentName)
		if _, ok := relevant[result.DocumentName]; !ok {
			continue
		}
		retrievedRelevant[result.DocumentName] = struct{}{}
		if firstRelevantRank == 0 {
			firstRelevantRank = i + 1
		}
	}
	reciprocalRank := 0.0
	if firstRelevantRank > 0 {
		reciprocalRank = 1 / float64(firstRelevantRank)
	}
	return CaseResult{
		ID: item.ID, Question: item.Question,
		RelevantDocuments:  append([]string(nil), item.RelevantDocuments...),
		RetrievedDocuments: retrievedNames,
		RecallAtK:          rounded(float64(len(retrievedRelevant)) / float64(len(relevant))),
		ReciprocalRank:     rounded(reciprocalRank),
	}
}

func validMetric(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func rounded(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}
