package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestHashEmbedderIsDeterministic(t *testing.T) {
	t.Parallel()
	embedder, err := NewHashEmbedder(128)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := embedder.Embed(context.Background(), []string{
		"蓝鲸项目发布日期", "蓝鲸项目发布日期", "咖啡机清洁步骤",
	})
	if err != nil {
		t.Fatal(err)
	}
	if similarity := cosineSimilarity(vectors[0], vectors[1]); similarity < 0.999999 {
		t.Fatalf("identical text similarity = %f", similarity)
	}
	if similarity := cosineSimilarity(vectors[0], vectors[2]); similarity >= 0.9 {
		t.Fatalf("unrelated text similarity = %f, want < 0.9", similarity)
	}
}

func TestOpenAIEmbedderBatchesAndPreservesIndexes(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected request path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var request struct {
			Input      []string `json:"input"`
			Model      string   `json:"model"`
			Dimensions int      `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return nil, err
		}
		if len(request.Input) > 10 || request.Model != "embedding-test" || request.Dimensions != 3 {
			t.Errorf("unexpected embedding request: %+v", request)
		}
		data := make([]map[string]any, 0, len(request.Input))
		// 故意倒序返回，验证客户端按 index 恢复原输入顺序。
		for index := len(request.Input) - 1; index >= 0; index-- {
			data = append(data, map[string]any{"index": index, "embedding": []float64{float64(index + 1), 1, 0}})
		}
		var response bytes.Buffer
		if err := json.NewEncoder(&response).Encode(map[string]any{"data": data}); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(response.Bytes())),
		}, nil
	})}

	embedder, err := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey: "test-key", BaseURL: "http://embedding.test/v1", Model: "embedding-test",
		Dimensions: 3, HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]string, 11)
	for index := range inputs {
		inputs[index] = "text"
	}
	vectors, err := embedder.Embed(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 11 || calls.Load() != 2 {
		t.Fatalf("vectors=%d calls=%d, want 11 and 2", len(vectors), calls.Load())
	}
	if vectors[0][0] >= vectors[1][0] {
		t.Fatalf("response indexes were not restored: first=%v second=%v", vectors[0], vectors[1])
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
