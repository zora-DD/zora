package postgres

import (
	"strings"
	"testing"
)

func TestBuildSearchTermsIsDeterministicAndKeepsFrequency(t *testing.T) {
	t.Parallel()
	terms := buildSearchTerms(map[string]int{"发布": 2, "zora": 1, "日": 1})
	if terms != "zora 发布 发布 日" {
		t.Fatalf("search terms = %q", terms)
	}
}

func TestSchemaUsesConfiguredVectorDimensions(t *testing.T) {
	t.Parallel()
	schema := strings.Replace(postgresSchema, "%d", "384", 1)
	for _, expected := range []string{
		"embedding vector(384)",
		"USING hnsw (embedding vector_cosine_ops)",
		"USING gin (search_vector)",
	} {
		if !strings.Contains(schema, expected) {
			t.Fatalf("schema does not contain %q", expected)
		}
	}
}
