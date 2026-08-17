package config

import "testing"

func TestLoadKnowledgeDefaults(t *testing.T) {
	clearEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EmbeddingProvider != "hash" || cfg.EmbeddingDimensions != 384 {
		t.Fatalf("unexpected embedding defaults: %+v", cfg)
	}
	if cfg.StoreProvider != "sqlite" || cfg.PostgresMaxConns != 10 {
		t.Fatalf("unexpected store defaults: %+v", cfg)
	}
	if cfg.Addr != ":8088" {
		t.Fatalf("default addr = %q, want :8088", cfg.Addr)
	}
	if cfg.KnowledgeChunkSize != 800 || cfg.KnowledgeOverlap != 120 {
		t.Fatalf("unexpected chunk defaults: %+v", cfg)
	}
	if !cfg.MemoryAutoCapture || cfg.MemoryMaxCandidates != 3 {
		t.Fatalf("unexpected memory defaults: %+v", cfg)
	}
}

func TestLoadPostgresStoreRequiresDSN(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("ZORA_STORE_PROVIDER", "postgres")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing PostgreSQL DSN error")
	}
	t.Setenv("ZORA_POSTGRES_DSN", "postgres://zora:secret@localhost:5432/zora?sslmode=disable")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StoreProvider != "postgres" || cfg.PostgresDSN == "" {
		t.Fatalf("unexpected PostgreSQL config: %+v", cfg)
	}
}

func TestLoadOpenAIEmbeddingUsesIndependentCredentials(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("ZORA_EMBEDDING_PROVIDER", "openai")
	t.Setenv("ZORA_EMBEDDING_API_KEY", "embedding-key")
	t.Setenv("ZORA_EMBEDDING_BASE_URL", "https://embedding.example/v1/")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EmbeddingDimensions != 1024 || cfg.EmbeddingAPIKey != "embedding-key" || cfg.EmbeddingBaseURL != "https://embedding.example/v1" {
		t.Fatalf("unexpected OpenAI embedding config: %+v", cfg)
	}
}

func TestLoadRejectsInvalidChunkOverlap(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("ZORA_KNOWLEDGE_CHUNK_SIZE", "200")
	t.Setenv("ZORA_KNOWLEDGE_CHUNK_OVERLAP", "100")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid overlap error")
	}
}

func clearEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"ZORA_ADDR", "ZORA_DATA_DIR", "ZORA_MODEL_PROVIDER", "ZORA_MODEL", "ZORA_API_KEY",
		"ZORA_BASE_URL", "ZORA_SYSTEM_PROMPT", "ZORA_REQUEST_TIMEOUT", "ZORA_MAX_ITERATIONS",
		"ZORA_STORE_PROVIDER", "ZORA_POSTGRES_DSN", "ZORA_POSTGRES_MAX_CONNS",
		"ZORA_EMBEDDING_PROVIDER", "ZORA_EMBEDDING_MODEL", "ZORA_EMBEDDING_API_KEY",
		"ZORA_EMBEDDING_BASE_URL", "ZORA_EMBEDDING_DIMENSIONS", "ZORA_KNOWLEDGE_CHUNK_SIZE",
		"ZORA_KNOWLEDGE_CHUNK_OVERLAP",
		"ZORA_MEMORY_AUTO_CAPTURE", "ZORA_MEMORY_MAX_CANDIDATES",
	} {
		t.Setenv(key, "")
	}
}
