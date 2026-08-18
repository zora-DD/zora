// Package sqlite 使用嵌入式 SQLite 实现应用持久化接口。
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/office"
	"github.com/zhiruo/zora/internal/store"
	"github.com/zhiruo/zora/internal/summary"
)

// schema 同时保存用户可见消息和 Agent 的内部执行轨迹。
// sequence 使用数据库自增值，保证事件按实际落库顺序稳定回放。
const schema = `
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS conversations (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),
    content TEXT NOT NULL,
    tool_name TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
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
    started_at TEXT NOT NULL,
    completed_at TEXT
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
    started_at TEXT NOT NULL,
    completed_at TEXT,
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
    requested_at TEXT NOT NULL,
    decided_at TEXT
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
    payload TEXT NOT NULL CHECK (json_valid(payload)),
    content_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
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
    created_at TEXT NOT NULL
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
    lease_until TEXT,
    external_reference TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT
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
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_office_operation_events_operation_created
    ON office_operation_events(operation_id, created_at, id);
CREATE TABLE IF NOT EXISTS run_events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    agent_name TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL DEFAULT '',
    payload TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_run_events_run_sequence
    ON run_events(run_id, sequence);
CREATE TABLE IF NOT EXISTS conversation_summaries (
    conversation_id TEXT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
    content TEXT NOT NULL,
    through_sequence INTEGER NOT NULL CHECK (through_sequence >= 0),
    message_count INTEGER NOT NULL CHECK (message_count >= 0),
    model TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS memories (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('semantic', 'episodic')),
    memory_key TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    importance REAL NOT NULL CHECK (importance >= 0 AND importance <= 1),
    user_edited INTEGER NOT NULL DEFAULT 0 CHECK (user_edited IN (0, 1)),
    source_type TEXT NOT NULL CHECK (source_type IN ('manual', 'conversation')),
    source_conversation_id TEXT REFERENCES conversations(id) ON DELETE SET NULL,
    source_message_id TEXT REFERENCES messages(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    expires_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_memories_kind_updated
    ON memories(kind, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_memories_expiry
    ON memories(expires_at);
CREATE TABLE IF NOT EXISTS knowledge_documents (
    id TEXT PRIMARY KEY,
    version_group_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    is_latest INTEGER NOT NULL CHECK (is_latest IN (0, 1)),
    name TEXT NOT NULL,
    source_type TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    content_hash TEXT NOT NULL UNIQUE,
    owner_id TEXT NOT NULL,
    visibility TEXT NOT NULL CHECK (visibility IN ('private', 'public')),
    embedding_model TEXT NOT NULL,
    embedding_dimensions INTEGER NOT NULL,
    chunk_count INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_knowledge_documents_created
    ON knowledge_documents(created_at DESC);
CREATE TABLE IF NOT EXISTS knowledge_chunks (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    document_id TEXT NOT NULL REFERENCES knowledge_documents(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    content TEXT NOT NULL,
    start_rune INTEGER NOT NULL,
    end_rune INTEGER NOT NULL,
    embedding_model TEXT NOT NULL,
    embedding TEXT NOT NULL,
    term_counts TEXT NOT NULL,
    token_count INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(document_id, ordinal)
);
CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_document
    ON knowledge_chunks(document_id, ordinal);
`

type SQLite struct {
	db *sql.DB
}

var (
	_ store.Store     = (*SQLite)(nil)
	_ knowledge.Store = (*SQLite)(nil)
	_ memory.Store    = (*SQLite)(nil)
	_ office.Store    = (*SQLite)(nil)
	_ summary.Store   = (*SQLite)(nil)
)

// Open 创建数据库文件，并执行可重复运行的建表语句。
func Open(path string) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败：%w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 数据库失败：%w", err)
	}
	// SQLite 的 PRAGMA 是连接级配置。本地模式固定单连接以保证配置一致；
	// WAL 允许读写更平滑，busy_timeout 用于吸收短暂的写锁竞争。
	// 上层仅依赖 Store 接口，后续可替换为 PostgreSQL 实现。
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("配置 SQLite 失败：%w", err)
	}
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("执行 SQLite 表结构迁移失败：%w", err)
	}
	if err = ensureMemoryColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = ensureKnowledgeColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLite{db: db}, nil
}

// ensureKnowledgeColumns 把早期单版本知识库平滑迁移为带版本和 ACL 的结构。
func ensureKnowledgeColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(knowledge_documents)`)
	if err != nil {
		return fmt.Errorf("读取 SQLite 知识库表结构失败：%w", err)
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("解析 SQLite 知识库表结构失败：%w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("遍历 SQLite 知识库表结构失败：%w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭 SQLite 知识库表结构结果失败：%w", err)
	}
	migrations := []struct{ column, statement string }{
		{"version_group_id", `ALTER TABLE knowledge_documents ADD COLUMN version_group_id TEXT NOT NULL DEFAULT ''`},
		{"version", `ALTER TABLE knowledge_documents ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)`},
		{"is_latest", `ALTER TABLE knowledge_documents ADD COLUMN is_latest INTEGER NOT NULL DEFAULT 1 CHECK (is_latest IN (0, 1))`},
		{"owner_id", `ALTER TABLE knowledge_documents ADD COLUMN owner_id TEXT NOT NULL DEFAULT 'local-user'`},
		{"visibility", `ALTER TABLE knowledge_documents ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'public'))`},
	}
	for _, migration := range migrations {
		if !columns[migration.column] {
			if _, err := db.Exec(migration.statement); err != nil {
				return fmt.Errorf("迁移 SQLite 知识库字段 %s 失败：%w", migration.column, err)
			}
		}
	}
	if _, err := db.Exec(`UPDATE knowledge_documents SET version_group_id = id WHERE version_group_id = ''`); err != nil {
		return fmt.Errorf("迁移 SQLite 知识库版本组失败：%w", err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_knowledge_documents_group_version ON knowledge_documents(version_group_id, version)`); err != nil {
		return fmt.Errorf("创建 SQLite 知识库版本索引失败：%w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_knowledge_documents_acl_latest ON knowledge_documents(owner_id, visibility, is_latest)`); err != nil {
		return fmt.Errorf("创建 SQLite 知识库权限索引失败：%w", err)
	}
	return nil
}

// ensureMemoryColumns 兼容已经由 V0.3 第一阶段创建的数据库。
// SQLite 不支持所有版本上的 ADD COLUMN IF NOT EXISTS，因此先读取表结构再迁移。
func ensureMemoryColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(memories)`)
	if err != nil {
		return fmt.Errorf("读取 SQLite 长期记忆表结构失败：%w", err)
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("解析 SQLite 长期记忆表结构失败：%w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("遍历 SQLite 长期记忆表结构失败：%w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭 SQLite 表结构结果失败：%w", err)
	}
	if !columns["memory_key"] {
		if _, err := db.Exec(`ALTER TABLE memories ADD COLUMN memory_key TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("迁移 SQLite memory_key 字段失败：%w", err)
		}
	}
	if !columns["user_edited"] {
		if _, err := db.Exec(`ALTER TABLE memories ADD COLUMN user_edited INTEGER NOT NULL DEFAULT 0 CHECK (user_edited IN (0, 1))`); err != nil {
			return fmt.Errorf("迁移 SQLite user_edited 字段失败：%w", err)
		}
	}
	if _, err := db.Exec(`UPDATE memories SET user_edited = 1 WHERE source_type = 'manual'`); err != nil {
		return fmt.Errorf("迁移 SQLite 手动记忆保护标记失败：%w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_memories_kind_key ON memories(kind, memory_key)`); err != nil {
		return fmt.Errorf("创建 SQLite 长期记忆合并索引失败：%w", err)
	}
	return nil
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) CreateConversation(ctx context.Context, c domain.Conversation) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO conversations(id, title, created_at, updated_at) VALUES(?, ?, ?, ?)`,
		c.ID, c.Title, formatTime(c.CreatedAt), formatTime(c.UpdatedAt))
	return wrap("创建对话", err)
}

func (s *SQLite) GetConversation(ctx context.Context, id string) (domain.Conversation, error) {
	var c domain.Conversation
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT c.id, c.title, c.created_at, c.updated_at, COUNT(m.id)
FROM conversations c
LEFT JOIN messages m ON m.conversation_id = c.id
WHERE c.id = ?
GROUP BY c.id`, id).Scan(&c.ID, &c.Title, &createdAt, &updatedAt, &c.MessageCount)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Conversation{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Conversation{}, fmt.Errorf("查询对话失败：%w", err)
	}
	c.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return domain.Conversation{}, err
	}
	c.UpdatedAt, err = parseTime(updatedAt)
	return c, err
}

func (s *SQLite) ListConversations(ctx context.Context, limit int) ([]domain.Conversation, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.title, c.created_at, c.updated_at, COUNT(m.id)
FROM conversations c
LEFT JOIN messages m ON m.conversation_id = c.id
GROUP BY c.id
ORDER BY c.updated_at DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询对话列表失败：%w", err)
	}
	defer rows.Close()

	conversations := make([]domain.Conversation, 0)
	for rows.Next() {
		var c domain.Conversation
		var createdAt, updatedAt string
		if err := rows.Scan(&c.ID, &c.Title, &createdAt, &updatedAt, &c.MessageCount); err != nil {
			return nil, fmt.Errorf("读取对话数据失败：%w", err)
		}
		if c.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		if c.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, err
		}
		conversations = append(conversations, c)
	}
	return conversations, rows.Err()
}

func (s *SQLite) RenameConversation(ctx context.Context, id, title string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET title = ?, updated_at = ? WHERE id = ?`,
		title, formatTime(time.Now().UTC()), id)
	return affected("重命名对话", result, err)
}

func (s *SQLite) DeleteConversation(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, id)
	return affected("删除对话", result, err)
}

func (s *SQLite) AddMessage(ctx context.Context, m domain.Message) (domain.Message, error) {
	// 新消息和会话 updated_at 必须原子更新，否则侧边栏排序可能与消息历史不一致。
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Message{}, fmt.Errorf("开始保存消息事务失败：%w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
INSERT INTO messages(id, conversation_id, role, content, tool_name, tool_call_id, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ConversationID, m.Role, m.Content, m.ToolName, m.ToolCallID, formatTime(m.CreatedAt))
	if err != nil {
		return domain.Message{}, fmt.Errorf("保存消息失败：%w", err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return domain.Message{}, fmt.Errorf("获取消息顺序号失败：%w", err)
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE conversations SET updated_at = ? WHERE id = ?`,
		formatTime(m.CreatedAt), m.ConversationID); err != nil {
		return domain.Message{}, fmt.Errorf("更新对话时间失败：%w", err)
	}
	if err = tx.Commit(); err != nil {
		return domain.Message{}, fmt.Errorf("提交消息事务失败：%w", err)
	}
	m.Sequence = sequence
	return m, nil
}

func (s *SQLite) ListMessages(ctx context.Context, conversationID string, limit int) ([]domain.Message, error) {
	// 子查询先取“最近 N 条”，外层再恢复为正序，确保送给模型的上下文时间顺序正确。
	rows, err := s.db.QueryContext(ctx, `
SELECT id, conversation_id, role, content, tool_name, tool_call_id, sequence, created_at
FROM (
    SELECT id, conversation_id, role, content, tool_name, tool_call_id, sequence, created_at
    FROM messages WHERE conversation_id = ?
    ORDER BY sequence DESC LIMIT ?
)
ORDER BY sequence ASC`, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询消息列表失败：%w", err)
	}
	defer rows.Close()

	messages := make([]domain.Message, 0)
	for rows.Next() {
		var m domain.Message
		var createdAt string
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.ToolName, &m.ToolCallID, &m.Sequence, &createdAt); err != nil {
			return nil, fmt.Errorf("读取消息数据失败：%w", err)
		}
		if m.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func (s *SQLite) CreateRun(ctx context.Context, run domain.AgentRun) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO agent_runs(id, conversation_id, user_message_id, status, model, started_at)
VALUES(?, ?, ?, ?, ?, ?)`,
		run.ID, run.ConversationID, run.UserMessageID, run.Status, run.Model, formatTime(run.StartedAt))
	return wrap("创建执行记录", err)
}

func (s *SQLite) GetRun(ctx context.Context, id string) (domain.AgentRun, error) {
	run, err := scanAgentRun(s.db.QueryRowContext(ctx, `
SELECT id, conversation_id, user_message_id, assistant_message_id, status, model, error, started_at, completed_at
FROM agent_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentRun{}, store.ErrNotFound
	}
	if err != nil {
		return domain.AgentRun{}, fmt.Errorf("查询执行记录失败：%w", err)
	}
	return run, nil
}

func (s *SQLite) ListRuns(ctx context.Context, limit int) ([]domain.AgentRun, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, conversation_id, user_message_id, assistant_message_id, status, model, error, started_at, completed_at
FROM agent_runs ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询执行记录列表失败：%w", err)
	}
	defer rows.Close()
	runs := make([]domain.AgentRun, 0)
	for rows.Next() {
		run, scanErr := scanAgentRun(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("读取执行记录失败：%w", scanErr)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func scanAgentRun(scanner rowScanner) (domain.AgentRun, error) {
	var run domain.AgentRun
	var assistantMessageID, completedAt sql.NullString
	var startedAt string
	if err := scanner.Scan(
		&run.ID, &run.ConversationID, &run.UserMessageID, &assistantMessageID,
		&run.Status, &run.Model, &run.Error, &startedAt, &completedAt,
	); err != nil {
		return domain.AgentRun{}, err
	}
	run.AssistantMessageID = assistantMessageID.String
	parsedStartedAt, err := parseTime(startedAt)
	if err != nil {
		return domain.AgentRun{}, err
	}
	run.StartedAt = parsedStartedAt
	if completedAt.Valid {
		parsed, parseErr := parseTime(completedAt.String)
		if parseErr != nil {
			return domain.AgentRun{}, parseErr
		}
		run.CompletedAt = &parsed
	}
	return run, nil
}

func (s *SQLite) FinishRun(ctx context.Context, id, status, assistantMessageID, errorMessage string, completedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE agent_runs
SET status = ?, assistant_message_id = NULLIF(?, ''), error = ?, completed_at = ?
WHERE id = ?`, status, assistantMessageID, errorMessage, formatTime(completedAt), id)
	return affected("完成执行记录", result, err)
}

func (s *SQLite) CreateAgentTaskRun(ctx context.Context, run domain.AgentTaskRun) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO agent_task_runs(
    id, parent_run_id, agent_name, tool_call_id, task, status, attempt, started_at
) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.ParentRunID, run.AgentName, run.ToolCallID, run.Task,
		run.Status, run.Attempt, formatTime(run.StartedAt))
	return wrap("创建专业 Agent 执行记录", err)
}

func (s *SQLite) FinishAgentTaskRun(ctx context.Context, id, status, outputPreview, errorMessage string, completedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE agent_task_runs
SET status = ?, output_preview = ?, error = ?, completed_at = ?
WHERE id = ?`, status, outputPreview, errorMessage, formatTime(completedAt), id)
	return affected("完成专业 Agent 执行记录", result, err)
}

func (s *SQLite) ListAgentTaskRuns(ctx context.Context, parentRunID string) ([]domain.AgentTaskRun, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, parent_run_id, agent_name, tool_call_id, task, status, attempt,
       output_preview, error, started_at, completed_at
FROM agent_task_runs WHERE parent_run_id = ? ORDER BY started_at ASC, id ASC`, parentRunID)
	if err != nil {
		return nil, fmt.Errorf("查询专业 Agent 执行记录失败：%w", err)
	}
	defer rows.Close()
	runs := make([]domain.AgentTaskRun, 0)
	for rows.Next() {
		var run domain.AgentTaskRun
		var startedAt string
		var completedAt sql.NullString
		if err := rows.Scan(
			&run.ID, &run.ParentRunID, &run.AgentName, &run.ToolCallID, &run.Task,
			&run.Status, &run.Attempt, &run.OutputPreview, &run.Error, &startedAt, &completedAt,
		); err != nil {
			return nil, fmt.Errorf("读取专业 Agent 执行记录失败：%w", err)
		}
		if run.StartedAt, err = parseTime(startedAt); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			parsed, parseErr := parseTime(completedAt.String)
			if parseErr != nil {
				return nil, parseErr
			}
			run.CompletedAt = &parsed
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *SQLite) AppendRunEvent(ctx context.Context, event domain.RunEvent) (domain.RunEvent, error) {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("编码执行事件失败：%w", err)
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO run_events(id, run_id, type, agent_name, tool_name, payload, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.RunID, event.Type, event.AgentName, event.ToolName, string(payload), formatTime(event.CreatedAt))
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("保存执行事件失败：%w", err)
	}
	event.Sequence, err = result.LastInsertId()
	return event, err
}

func (s *SQLite) ListRunEvents(ctx context.Context, runID string) ([]domain.RunEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, type, agent_name, tool_name, payload, sequence, created_at
FROM run_events WHERE run_id = ? ORDER BY sequence ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("查询执行事件失败：%w", err)
	}
	defer rows.Close()

	events := make([]domain.RunEvent, 0)
	for rows.Next() {
		var event domain.RunEvent
		var payload, createdAt string
		if err := rows.Scan(&event.ID, &event.RunID, &event.Type, &event.AgentName, &event.ToolName, &payload, &event.Sequence, &createdAt); err != nil {
			return nil, fmt.Errorf("读取执行事件失败：%w", err)
		}
		if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
			return nil, fmt.Errorf("解析执行事件失败：%w", err)
		}
		if event.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// CreateDocument 在同一事务中分配版本号、切换 latest 并写入全部分块。
func (s *SQLite) CreateDocument(ctx context.Context, document knowledge.Document, chunks []knowledge.Chunk) (knowledge.Document, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return knowledge.Document{}, fmt.Errorf("开始保存知识库文档事务失败：%w", err)
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM knowledge_documents WHERE version_group_id = ?`, document.VersionGroupID).Scan(&document.Version); err != nil {
		return knowledge.Document{}, fmt.Errorf("计算知识库文档版本失败：%w", err)
	}
	document.IsLatest = true
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_documents SET is_latest = 0, updated_at = ? WHERE version_group_id = ? AND is_latest = 1`, formatTime(document.UpdatedAt), document.VersionGroupID); err != nil {
		return knowledge.Document{}, fmt.Errorf("更新知识库旧版本状态失败：%w", err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO knowledge_documents(
    id, version_group_id, version, is_latest, name, source_type, mime_type, content_hash,
    owner_id, visibility, embedding_model, embedding_dimensions, chunk_count, created_at, updated_at
) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		document.ID, document.VersionGroupID, document.Version, 1, document.Name, document.SourceType,
		document.MIMEType, document.ContentHash, document.OwnerID, document.Visibility,
		document.EmbeddingModel, document.EmbeddingDimensions, document.ChunkCount,
		formatTime(document.CreatedAt), formatTime(document.UpdatedAt))
	if err != nil {
		return knowledge.Document{}, fmt.Errorf("保存知识库文档失败：%w", err)
	}
	for _, chunk := range chunks {
		embedding, err := json.Marshal(chunk.Embedding)
		if err != nil {
			return knowledge.Document{}, fmt.Errorf("编码分块向量失败：%w", err)
		}
		terms, err := json.Marshal(chunk.TermCounts)
		if err != nil {
			return knowledge.Document{}, fmt.Errorf("编码分块词频失败：%w", err)
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO knowledge_chunks(
    id, document_id, ordinal, content, start_rune, end_rune, embedding_model,
    embedding, term_counts, token_count, created_at
) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			chunk.ID, chunk.DocumentID, chunk.Ordinal, chunk.Content, chunk.StartRune, chunk.EndRune,
			chunk.EmbeddingModel, string(embedding), string(terms), chunk.TokenCount, formatTime(chunk.CreatedAt))
		if err != nil {
			return knowledge.Document{}, fmt.Errorf("保存第 %d 个知识库分块失败：%w", chunk.Ordinal+1, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return knowledge.Document{}, fmt.Errorf("提交知识库文档事务失败：%w", err)
	}
	return document, nil
}

const sqliteKnowledgeDocumentSelect = `
SELECT id, version_group_id, version, is_latest, name, source_type, mime_type, content_hash,
       owner_id, visibility, embedding_model, embedding_dimensions, chunk_count, created_at, updated_at
FROM knowledge_documents`

func (s *SQLite) GetDocumentByHash(ctx context.Context, contentHash string) (knowledge.Document, error) {
	document, err := scanKnowledgeDocument(s.db.QueryRowContext(ctx, sqliteKnowledgeDocumentSelect+` WHERE content_hash = ?`, contentHash))
	if errors.Is(err, sql.ErrNoRows) {
		return knowledge.Document{}, knowledge.ErrNotFound
	}
	if err != nil {
		return knowledge.Document{}, fmt.Errorf("按哈希查询知识库文档失败：%w", err)
	}
	return document, nil
}

func (s *SQLite) ListDocuments(ctx context.Context, principalID string, includeHistory bool, limit int) ([]knowledge.Document, error) {
	historyFilter := ` AND is_latest = 1`
	if includeHistory {
		historyFilter = ""
	}
	rows, err := s.db.QueryContext(ctx, sqliteKnowledgeDocumentSelect+` WHERE (owner_id = ? OR visibility = 'public')`+historyFilter+` ORDER BY updated_at DESC, version DESC LIMIT ?`, principalID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询知识库文档列表失败：%w", err)
	}
	defer rows.Close()
	return scanKnowledgeDocuments(rows)
}

func (s *SQLite) ListDocumentVersions(ctx context.Context, documentID, principalID string) ([]knowledge.Document, error) {
	var groupID string
	err := s.db.QueryRowContext(ctx, `SELECT version_group_id FROM knowledge_documents WHERE id = ? AND (owner_id = ? OR visibility = 'public')`, documentID, principalID).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, knowledge.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询知识库文档版本组失败：%w", err)
	}
	rows, err := s.db.QueryContext(ctx, sqliteKnowledgeDocumentSelect+` WHERE version_group_id = ? AND (owner_id = ? OR visibility = 'public') ORDER BY version DESC`, groupID, principalID)
	if err != nil {
		return nil, fmt.Errorf("查询知识库文档版本列表失败：%w", err)
	}
	defer rows.Close()
	return scanKnowledgeDocuments(rows)
}

func (s *SQLite) DeleteDocument(ctx context.Context, id, principalID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始删除知识库文档事务失败：%w", err)
	}
	defer tx.Rollback()
	var ownerID, groupID string
	var latest int
	err = tx.QueryRowContext(ctx, `SELECT owner_id, version_group_id, is_latest FROM knowledge_documents WHERE id = ?`, id).Scan(&ownerID, &groupID, &latest)
	if errors.Is(err, sql.ErrNoRows) {
		return knowledge.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("查询待删除知识库文档失败：%w", err)
	}
	if ownerID != principalID {
		return knowledge.ErrAccessDenied
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM knowledge_documents WHERE id = ?`, id); err != nil {
		return fmt.Errorf("删除知识库文档失败：%w", err)
	}
	if latest == 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE knowledge_documents SET is_latest = 1, updated_at = ? WHERE id = (SELECT id FROM knowledge_documents WHERE version_group_id = ? ORDER BY version DESC LIMIT 1)`, formatTime(time.Now().UTC()), groupID); err != nil {
			return fmt.Errorf("恢复知识库上一版本失败：%w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交删除知识库文档事务失败：%w", err)
	}
	return nil
}

func (s *SQLite) ListChunks(ctx context.Context, principalID string, limit int) ([]knowledge.Chunk, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.document_id, d.name, c.ordinal, c.content, c.start_rune, c.end_rune,
       c.embedding_model, c.embedding, c.term_counts, c.token_count, c.created_at
FROM knowledge_chunks c
JOIN knowledge_documents d ON d.id = c.document_id
WHERE d.is_latest = 1 AND (d.owner_id = ? OR d.visibility = 'public')
ORDER BY c.sequence ASC LIMIT ?`, principalID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询知识库分块失败：%w", err)
	}
	defer rows.Close()
	chunks := make([]knowledge.Chunk, 0)
	for rows.Next() {
		var chunk knowledge.Chunk
		var embedding, terms, createdAt string
		if err := rows.Scan(&chunk.ID, &chunk.DocumentID, &chunk.DocumentName, &chunk.Ordinal, &chunk.Content,
			&chunk.StartRune, &chunk.EndRune, &chunk.EmbeddingModel, &embedding, &terms, &chunk.TokenCount, &createdAt); err != nil {
			return nil, fmt.Errorf("读取知识库分块失败：%w", err)
		}
		if err := json.Unmarshal([]byte(embedding), &chunk.Embedding); err != nil {
			return nil, fmt.Errorf("解析分块向量失败：%w", err)
		}
		if err := json.Unmarshal([]byte(terms), &chunk.TermCounts); err != nil {
			return nil, fmt.Errorf("解析分块词频失败：%w", err)
		}
		if chunk.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

type rowScanner interface{ Scan(dest ...any) error }

func scanKnowledgeDocument(row rowScanner) (knowledge.Document, error) {
	var document knowledge.Document
	var isLatest int
	var createdAt, updatedAt string
	if err := row.Scan(&document.ID, &document.VersionGroupID, &document.Version, &isLatest,
		&document.Name, &document.SourceType, &document.MIMEType, &document.ContentHash,
		&document.OwnerID, &document.Visibility, &document.EmbeddingModel, &document.EmbeddingDimensions,
		&document.ChunkCount, &createdAt, &updatedAt); err != nil {
		return knowledge.Document{}, err
	}
	document.IsLatest = isLatest == 1
	var err error
	if document.CreatedAt, err = parseTime(createdAt); err != nil {
		return knowledge.Document{}, err
	}
	if document.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return knowledge.Document{}, err
	}
	return document, nil
}

func scanKnowledgeDocuments(rows *sql.Rows) ([]knowledge.Document, error) {
	documents := make([]knowledge.Document, 0)
	for rows.Next() {
		document, err := scanKnowledgeDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("读取知识库文档失败：%w", err)
		}
		documents = append(documents, document)
	}
	return documents, rows.Err()
}

func affected(operation string, result sql.Result, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", operation, err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func wrap(operation string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("解析存储时间失败：%w", err)
	}
	return t, nil
}
