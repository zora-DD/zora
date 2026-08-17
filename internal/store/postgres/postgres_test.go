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
		"CREATE TABLE IF NOT EXISTS memories",
		"kind IN ('semantic', 'episodic')",
		"importance >= 0 AND importance <= 1",
		"ADD COLUMN IF NOT EXISTS memory_key",
		"ADD COLUMN IF NOT EXISTS user_edited",
		"INSERT INTO zora_schema_versions(version) VALUES (3)",
		"CREATE TABLE IF NOT EXISTS conversation_summaries",
		"through_sequence BIGINT NOT NULL",
		"INSERT INTO zora_schema_versions(version) VALUES (4)",
	} {
		if !strings.Contains(schema, expected) {
			t.Fatalf("schema does not contain %q", expected)
		}
	}
}
