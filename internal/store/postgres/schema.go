package postgres

// postgresSchema 仅允许用经过整数校验的向量维度替换 %d。
const postgresSchema = `
CREATE TABLE IF NOT EXISTS zora_schema_versions (
    version INTEGER PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS conversations (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
    sequence BIGSERIAL PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),
    content TEXT NOT NULL,
    tool_name TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_conversation_sequence
    ON messages(conversation_id, sequence);

CREATE TABLE IF NOT EXISTS agent_runs (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    assistant_message_id TEXT,
    status TEXT NOT NULL,
    model TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_runs_conversation_started
    ON agent_runs(conversation_id, started_at);

CREATE TABLE IF NOT EXISTS agent_task_runs (
    id TEXT PRIMARY KEY,
    parent_run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    agent_name TEXT NOT NULL,
    tool_call_id TEXT NOT NULL,
    task TEXT NOT NULL,
    status TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 1 CHECK (attempt > 0),
    output_preview TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    UNIQUE(parent_run_id, tool_call_id)
);
CREATE INDEX IF NOT EXISTS idx_agent_task_runs_parent_started
    ON agent_task_runs(parent_run_id, started_at);

CREATE TABLE IF NOT EXISTS approval_requests (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected', 'expired')),
    trigger_reason TEXT NOT NULL,
    decision_reason TEXT NOT NULL DEFAULT '',
    requested_at TIMESTAMPTZ NOT NULL,
    decided_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_approval_requests_status_requested
    ON approval_requests(status, requested_at DESC);

CREATE TABLE IF NOT EXISTS office_drafts (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('email', 'calendar')),
    status TEXT NOT NULL CHECK (status IN ('draft', 'pending_confirmation', 'approved', 'executing', 'completed', 'rejected', 'failed', 'cancelled')),
    conversation_id TEXT REFERENCES conversations(id) ON DELETE SET NULL,
    source_run_id TEXT REFERENCES agent_runs(id) ON DELETE SET NULL,
    title TEXT NOT NULL,
    payload JSONB NOT NULL,
    content_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE(source_run_id, content_hash)
);
CREATE INDEX IF NOT EXISTS idx_office_drafts_status_updated
    ON office_drafts(status, updated_at DESC);

CREATE TABLE IF NOT EXISTS office_draft_events (
    id TEXT PRIMARY KEY,
    draft_id TEXT NOT NULL REFERENCES office_drafts(id) ON DELETE CASCADE,
    from_status TEXT NOT NULL,
    to_status TEXT NOT NULL,
    actor TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_office_draft_events_draft_created
    ON office_draft_events(draft_id, created_at, id);

CREATE TABLE IF NOT EXISTS office_operations (
    id TEXT PRIMARY KEY,
    draft_id TEXT NOT NULL UNIQUE REFERENCES office_drafts(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('email', 'calendar')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'executing', 'completed', 'failed')),
    idempotency_key TEXT NOT NULL UNIQUE,
    executor_name TEXT NOT NULL DEFAULT '',
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    external_reference TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_office_operations_status_updated
    ON office_operations(status, updated_at DESC);

CREATE TABLE IF NOT EXISTS office_operation_events (
    id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL REFERENCES office_operations(id) ON DELETE CASCADE,
    from_status TEXT NOT NULL,
    to_status TEXT NOT NULL,
    attempt INTEGER NOT NULL CHECK (attempt >= 0),
    actor TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_office_operation_events_operation_created
    ON office_operation_events(operation_id, created_at, id);

CREATE TABLE IF NOT EXISTS run_events (
    sequence BIGSERIAL PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    agent_name TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL DEFAULT '',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_run_events_run_sequence
    ON run_events(run_id, sequence);

CREATE TABLE IF NOT EXISTS conversation_summaries (
    conversation_id TEXT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
    content TEXT NOT NULL,
    through_sequence BIGINT NOT NULL CHECK (through_sequence >= 0),
    message_count INTEGER NOT NULL CHECK (message_count >= 0),
    model TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS memories (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('semantic', 'episodic')),
    memory_key TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    importance DOUBLE PRECISION NOT NULL CHECK (importance >= 0 AND importance <= 1),
    user_edited BOOLEAN NOT NULL DEFAULT FALSE,
    source_type TEXT NOT NULL CHECK (source_type IN ('manual', 'conversation')),
    source_conversation_id TEXT REFERENCES conversations(id) ON DELETE SET NULL,
    source_message_id TEXT REFERENCES messages(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ
);
ALTER TABLE memories ADD COLUMN IF NOT EXISTS memory_key TEXT NOT NULL DEFAULT '';
ALTER TABLE memories ADD COLUMN IF NOT EXISTS user_edited BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE memories SET user_edited = TRUE WHERE source_type = 'manual';
CREATE INDEX IF NOT EXISTS idx_memories_kind_updated
    ON memories(kind, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_memories_kind_key
    ON memories(kind, memory_key);
CREATE INDEX IF NOT EXISTS idx_memories_expiry
    ON memories(expires_at);

CREATE TABLE IF NOT EXISTS knowledge_documents (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    source_type TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    content_hash TEXT NOT NULL UNIQUE,
    embedding_model TEXT NOT NULL,
    embedding_dimensions INTEGER NOT NULL,
    chunk_count INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_knowledge_documents_created
    ON knowledge_documents(created_at DESC);

CREATE TABLE IF NOT EXISTS knowledge_chunks (
    sequence BIGSERIAL PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    document_id TEXT NOT NULL REFERENCES knowledge_documents(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    content TEXT NOT NULL,
    start_rune INTEGER NOT NULL,
    end_rune INTEGER NOT NULL,
    embedding_model TEXT NOT NULL,
    embedding vector(%d) NOT NULL,
    term_counts JSONB NOT NULL,
    search_terms TEXT NOT NULL,
    search_vector TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple', search_terms)) STORED,
    token_count INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE(document_id, ordinal)
);
CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_document
    ON knowledge_chunks(document_id, ordinal);
CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_embedding_hnsw
    ON knowledge_chunks USING hnsw (embedding vector_cosine_ops);
CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_search_gin
    ON knowledge_chunks USING gin (search_vector);

INSERT INTO zora_schema_versions(version) VALUES (1) ON CONFLICT DO NOTHING;
INSERT INTO zora_schema_versions(version) VALUES (2) ON CONFLICT DO NOTHING;
INSERT INTO zora_schema_versions(version) VALUES (3) ON CONFLICT DO NOTHING;
INSERT INTO zora_schema_versions(version) VALUES (4) ON CONFLICT DO NOTHING;
INSERT INTO zora_schema_versions(version) VALUES (5) ON CONFLICT DO NOTHING;
INSERT INTO zora_schema_versions(version) VALUES (6) ON CONFLICT DO NOTHING;
INSERT INTO zora_schema_versions(version) VALUES (7) ON CONFLICT DO NOTHING;
INSERT INTO zora_schema_versions(version) VALUES (8) ON CONFLICT DO NOTHING;
`
