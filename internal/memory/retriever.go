package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

const recencyHalfLife = 90 * 24 * time.Hour
const minimumTopicalRelevance = 0.20

// RecallResult 保留总分及三个可解释分量，方便调参、审计和后续 A/B 评估。
type RecallResult struct {
	Memory     Memory  `json:"memory"`
	Score      float64 `json:"score"`
	Relevance  float64 `json:"relevance"`
	Importance float64 `json:"importance"`
	Recency    float64 `json:"recency"`
}

// Recall 使用轻量词项相关性作为 V0.3 的可解释基线，再融合重要性和时效性。
// 只有相关性通过门槛的记忆才能进入结果，避免高重要性但无关的内容污染上下文。
func (s *Service) Recall(ctx context.Context, query string) ([]RecallResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("长期记忆召回查询不能为空")
	}
	items, err := s.store.ListMemories(ctx, ListFilter{Limit: 1_000})
	if err != nil {
		return nil, err
	}
	queryTerms := recallTerms(query)
	if len(queryTerms) == 0 {
		return []RecallResult{}, nil
	}
	memoryIntent := containsAnyText(strings.ToLower(query), "记得", "记住", "偏好", "我的信息", "了解我", "关于我")
	now := s.now()
	results := make([]RecallResult, 0, min(len(items), s.recallLimit))
	for _, item := range items {
		contentTerms := recallTerms(item.Content + " " + item.MemoryKey)
		relevance := cosineTermOverlap(queryTerms, contentTerms)
		if relevance == 0 && memoryIntent {
			// 用户显式询问自身信息时允许浏览高价值记忆，但仍保持很低的相关性先验。
			relevance = 0.08
		}
		// 重要性和时效性不能把只有一两个偶然重合词的记忆抬进上下文。
		// 显式“你记得我的偏好吗”属于总览意图，允许使用上面的低相关性先验。
		if relevance == 0 || (!memoryIntent && relevance < minimumTopicalRelevance) {
			continue
		}
		recency := recencyScore(now, item.UpdatedAt)
		score := 0.65*relevance + 0.20*item.Importance + 0.15*recency
		if score < s.recallMinScore {
			continue
		}
		results = append(results, RecallResult{
			Memory: item, Score: score, Relevance: relevance,
			Importance: item.Importance, Recency: recency,
		})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].Memory.UpdatedAt.After(results[j].Memory.UpdatedAt)
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > s.recallLimit {
		results = results[:s.recallLimit]
	}
	return results, nil
}

func recallTerms(value string) map[string]struct{} {
	terms := make(map[string]struct{})
	runes := []rune(strings.ToLower(value))
	for index := 0; index < len(runes); {
		r := runes[index]
		switch {
		case unicode.Is(unicode.Han, r):
			end := index + 1
			for end < len(runes) && unicode.Is(unicode.Han, runes[end]) {
				end++
			}
			run := runes[index:end]
			if len(run) == 1 {
				terms[string(run)] = struct{}{}
			} else {
				for i := 0; i+1 < len(run); i++ {
					terms[string(run[i:i+2])] = struct{}{}
				}
			}
			index = end
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			end := index + 1
			for end < len(runes) && (unicode.IsLetter(runes[end]) || unicode.IsDigit(runes[end]) || runes[end] == '-' || runes[end] == '_') {
				end++
			}
			terms[string(runes[index:end])] = struct{}{}
			index = end
		default:
			index++
		}
	}
	return terms
}

func cosineTermOverlap(left, right map[string]struct{}) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	intersection := 0
	for term := range left {
		if _, ok := right[term]; ok {
			intersection++
		}
	}
	return float64(intersection) / math.Sqrt(float64(len(left)*len(right)))
}

func recencyScore(now, updatedAt time.Time) float64 {
	age := now.Sub(updatedAt)
	if age <= 0 {
		return 1
	}
	return math.Exp(-math.Ln2 * float64(age) / float64(recencyHalfLife))
}
