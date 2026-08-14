package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"strings"
)

// Embedder 把文本批量转换成同一向量空间中的稠密向量。
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float64, error)
	Name() string
	Dimensions() int
}

// EmbedderConfig 是不同 Embedding Provider 共用的创建参数。
// 入口程序和离线评测命令共用该工厂，保证两条链路使用完全相同的实现。
type EmbedderConfig struct {
	Provider   string
	APIKey     string
	BaseURL    string
	Model      string
	Dimensions int
	HTTPClient *http.Client
}

func NewEmbedder(config EmbedderConfig) (Embedder, error) {
	switch strings.ToLower(strings.TrimSpace(config.Provider)) {
	case "hash":
		return NewHashEmbedder(config.Dimensions)
	case "openai":
		return NewOpenAIEmbedder(OpenAIEmbedderConfig{
			APIKey: config.APIKey, BaseURL: config.BaseURL, Model: config.Model,
			Dimensions: config.Dimensions, HTTPClient: config.HTTPClient,
		})
	default:
		return nil, fmt.Errorf("不支持的 Embedding 提供方：%q", config.Provider)
	}
}

// HashEmbedder 用 feature hashing 提供零密钥、确定性的本地向量。
// 它适合开发和链路测试，不应替代生产语义 Embedding 模型。
type HashEmbedder struct{ dimensions int }

func NewHashEmbedder(dimensions int) (*HashEmbedder, error) {
	if dimensions < 64 {
		return nil, fmt.Errorf("Hash Embedding 维度至少为 64")
	}
	return &HashEmbedder{dimensions: dimensions}, nil
}

func (e *HashEmbedder) Name() string    { return fmt.Sprintf("zora-hash-%d-v1", e.dimensions) }
func (e *HashEmbedder) Dimensions() int { return e.dimensions }

func (e *HashEmbedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	result := make([][]float64, len(texts))
	for i, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		counts, _ := termCounts(text)
		vector := make([]float64, e.dimensions)
		for token, count := range counts {
			hasher := fnv.New64a()
			_, _ = hasher.Write([]byte(token))
			hash := hasher.Sum64()
			index := int(hash % uint64(e.dimensions))
			sign := 1.0
			if hash&(1<<63) != 0 {
				sign = -1
			}
			vector[index] += sign * (1 + math.Log(float64(count)))
		}
		normalize(vector)
		result[i] = vector
	}
	return result, nil
}

type OpenAIEmbedderConfig struct {
	APIKey     string
	BaseURL    string
	Model      string
	Dimensions int
	HTTPClient *http.Client
}

type OpenAIEmbedder struct {
	apiKey     string
	endpoint   string
	model      string
	dimensions int
	client     *http.Client
}

func NewOpenAIEmbedder(config OpenAIEmbedderConfig) (*OpenAIEmbedder, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("Embedding API Key 不能为空")
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, fmt.Errorf("Embedding BaseURL 不能为空")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("Embedding 模型名不能为空")
	}
	if config.Dimensions < 1 {
		return nil, fmt.Errorf("Embedding 维度必须为正整数")
	}
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &OpenAIEmbedder{
		apiKey: config.APIKey, endpoint: strings.TrimRight(config.BaseURL, "/") + "/embeddings",
		model: config.Model, dimensions: config.Dimensions, client: client,
	}, nil
}

func (e *OpenAIEmbedder) Name() string    { return e.model }
func (e *OpenAIEmbedder) Dimensions() int { return e.dimensions }

func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return [][]float64{}, nil
	}
	const batchSize = 10 // text-embedding-v4 OpenAI 兼容接口的单批上限。
	result := make([][]float64, 0, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		end := min(start+batchSize, len(texts))
		batch, err := e.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
	}
	return result, nil
}

func (e *OpenAIEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	payload, err := json.Marshal(map[string]any{
		"model": e.model, "input": texts, "dimensions": e.dimensions, "encoding_format": "float",
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("创建 Embedding 请求失败：%w", err)
	}
	request.Header.Set("Authorization", "Bearer "+e.apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := e.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("调用 Embedding API 失败：%w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 Embedding 响应失败：%w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Embedding API 返回异常状态 %d：%s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("解析 Embedding 响应失败：%w", err)
	}
	if len(decoded.Data) != len(texts) {
		return nil, fmt.Errorf("Embedding API 返回了 %d 个向量，但请求中有 %d 条文本", len(decoded.Data), len(texts))
	}
	result := make([][]float64, len(texts))
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(result) {
			return nil, fmt.Errorf("Embedding API 返回了无效的向量索引 %d", item.Index)
		}
		if len(item.Embedding) != e.dimensions {
			return nil, fmt.Errorf("Embedding 向量维度为 %d，但配置期望为 %d", len(item.Embedding), e.dimensions)
		}
		normalize(item.Embedding)
		result[item.Index] = item.Embedding
	}
	for i, vector := range result {
		if vector == nil {
			return nil, fmt.Errorf("Embedding API 未返回索引 %d 的向量", i)
		}
	}
	return result, nil
}

func normalize(vector []float64) {
	var sum float64
	for _, value := range vector {
		sum += value * value
	}
	if sum == 0 {
		return
	}
	norm := math.Sqrt(sum)
	for i := range vector {
		vector[i] /= norm
	}
}

func cosineSimilarity(left, right []float64) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	var dot float64
	for i := range left {
		dot += left[i] * right[i]
	}
	return dot
}
