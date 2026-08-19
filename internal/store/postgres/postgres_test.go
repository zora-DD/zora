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
	schema := strings.ReplaceAll(postgresSchema, "%d", "384")
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
		"CREATE TABLE IF NOT EXISTS office_drafts",
		"CREATE TABLE IF NOT EXISTS office_draft_events",
		"idx_office_draft_events_draft_created",
		"CREATE TABLE IF NOT EXISTS office_operations",
		"CREATE TABLE IF NOT EXISTS memory_capture_jobs",
		"CREATE TABLE IF NOT EXISTS message_embeddings",
		"CREATE TABLE IF NOT EXISTS memory_embeddings",
		"INSERT INTO zora_schema_versions(version) VALUES (11)",
		"CREATE TABLE IF NOT EXISTS background_jobs",
		"idx_background_jobs_kind_status_available",
		"INSERT INTO zora_schema_versions(version) VALUES (12)",
		"idx_memory_capture_jobs_status_available",
		"CREATE TABLE IF NOT EXISTS office_operation_events",
		"idempotency_key TEXT NOT NULL UNIQUE",
		"idx_office_operation_events_operation_created",
		"UNIQUE(source_run_id, content_hash)",
		"INSERT INTO zora_schema_versions(version) VALUES (6)",
		"INSERT INTO zora_schema_versions(version) VALUES (7)",
		"INSERT INTO zora_schema_versions(version) VALUES (8)",
		"INSERT INTO zora_schema_versions(version) VALUES (10)",
	} {
		if !strings.Contains(schema, expected) {
			t.Fatalf("schema does not contain %q", expected)
		}
	}
}
