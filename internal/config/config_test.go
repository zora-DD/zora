package config

import (
	"testing"
	"time"
)

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
	if !cfg.MemoryRecallEnabled || cfg.MemoryRecallLimit != 5 || cfg.MemoryRecallMinScore != 0.25 {
		t.Fatalf("unexpected memory recall defaults: %+v", cfg)
	}
	if !cfg.SummaryEnabled || cfg.SummaryTriggerMessages != 20 || cfg.SummaryKeepRecent != 12 || cfg.SummaryMaxRunes != 4000 {
		t.Fatalf("unexpected summary defaults: %+v", cfg)
	}
	if cfg.MultiAgentEnabled {
		t.Fatal("multi-agent should be opt-in by default")
	}
	if cfg.MultiAgentMaxHandoffs != 6 || cfg.MultiAgentMaxParallel != 3 || cfg.MultiAgentSpecialistTimeout != 30*time.Second || cfg.MultiAgentRetryCount != 1 {
		t.Fatalf("unexpected multi-agent execution defaults: %+v", cfg)
	}
	if cfg.MultiAgentApprovalMode != "risky" || cfg.MultiAgentApprovalTimeout != time.Minute {
		t.Fatalf("unexpected multi-agent approval defaults: %+v", cfg)
	}
	if cfg.MCPEnabled || len(cfg.MCPServers) != 0 || cfg.MCPConnectTimeout != 10*time.Second || cfg.MCPCallTimeout != 20*time.Second || cfg.MCPMaxOutputRunes != 12000 {
		t.Fatalf("unexpected MCP defaults: %+v", cfg)
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

func TestLoadRejectsInvalidMemoryRecallScore(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("ZORA_MEMORY_RECALL_MIN_SCORE", "NaN")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid memory recall score error")
	}
}

func TestLoadRejectsInvalidSummaryWindow(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("ZORA_SUMMARY_TRIGGER_MESSAGES", "10")
	t.Setenv("ZORA_SUMMARY_KEEP_RECENT", "10")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid summary window error")
	}
}

func TestLoadMultiAgentOptIn(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("ZORA_MULTI_AGENT_ENABLED", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MultiAgentEnabled {
		t.Fatal("multi-agent config should be enabled")
	}
}

func TestLoadMCPRequiresExplicitAllowlistAndIsolatesCoreCredentials(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("ZORA_MCP_ENABLED", "true")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing MCP server config error")
	}

	t.Setenv("ZORA_MCP_SERVERS_JSON", `[{"name":"files","command":"./bin/zora-mcp-files","allowed_tools":["list_files","read_text_file"],"pass_env":["PATH","ZORA_MCP_FILES_ROOT"]}]`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MCPEnabled || len(cfg.MCPServers) != 1 || cfg.MCPServers[0].Name != "files" {
		t.Fatalf("unexpected MCP config: %+v", cfg)
	}

	t.Setenv("ZORA_MCP_SERVERS_JSON", `[{"name":"files","command":"./bin/zora-mcp-files","allowed_tools":["list_files"],"pass_env":["ZORA_API_KEY"]}]`)
	if _, err := Load(); err == nil {
		t.Fatal("expected Zora core credential inheritance error")
	}
}

func clearEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"ZORA_ADDR", "ZORA_DATA_DIR", "ZORA_MODEL_PROVIDER", "ZORA_MODEL", "ZORA_API_KEY",
		"ZORA_BASE_URL", "ZORA_SYSTEM_PROMPT", "ZORA_REQUEST_TIMEOUT", "ZORA_MAX_ITERATIONS",
		"ZORA_MULTI_AGENT_ENABLED", "ZORA_MULTI_AGENT_MAX_HANDOFFS", "ZORA_MULTI_AGENT_MAX_PARALLEL",
		"ZORA_MULTI_AGENT_SPECIALIST_TIMEOUT", "ZORA_MULTI_AGENT_RETRY_COUNT",
		"ZORA_MULTI_AGENT_APPROVAL_MODE", "ZORA_MULTI_AGENT_APPROVAL_TIMEOUT",
		"ZORA_STORE_PROVIDER", "ZORA_POSTGRES_DSN", "ZORA_POSTGRES_MAX_CONNS",
		"ZORA_EMBEDDING_PROVIDER", "ZORA_EMBEDDING_MODEL", "ZORA_EMBEDDING_API_KEY",
		"ZORA_EMBEDDING_BASE_URL", "ZORA_EMBEDDING_DIMENSIONS", "ZORA_KNOWLEDGE_CHUNK_SIZE",
		"ZORA_KNOWLEDGE_CHUNK_OVERLAP",
		"ZORA_MEMORY_AUTO_CAPTURE", "ZORA_MEMORY_MAX_CANDIDATES",
		"ZORA_MEMORY_RECALL_ENABLED", "ZORA_MEMORY_RECALL_LIMIT", "ZORA_MEMORY_RECALL_MIN_SCORE",
		"ZORA_SUMMARY_ENABLED", "ZORA_SUMMARY_TRIGGER_MESSAGES", "ZORA_SUMMARY_KEEP_RECENT", "ZORA_SUMMARY_MAX_RUNES",
		"ZORA_MCP_ENABLED", "ZORA_MCP_SERVERS_JSON", "ZORA_MCP_CONNECT_TIMEOUT", "ZORA_MCP_CALL_TIMEOUT", "ZORA_MCP_MAX_OUTPUT_RUNES",
	} {
		t.Setenv(key, "")
	}
}
