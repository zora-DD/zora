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
	"github.com/zhiruo/zora/internal/store"
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
`

type SQLite struct {
	db *sql.DB
}

// Open 创建数据库文件，并执行可重复运行的建表语句。
func Open(path string) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite 的 PRAGMA 是连接级配置。V0.1 固定单连接以保证配置一致；
	// WAL 允许读写更平滑，busy_timeout 用于吸收短暂的写锁竞争。
	// 上层仅依赖 Store 接口，后续可替换为 PostgreSQL 实现。
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure sqlite: %w", err)
	}
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return &SQLite{db: db}, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) CreateConversation(ctx context.Context, c domain.Conversation) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO conversations(id, title, created_at, updated_at) VALUES(?, ?, ?, ?)`,
		c.ID, c.Title, formatTime(c.CreatedAt), formatTime(c.UpdatedAt))
	return wrap("create conversation", err)
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
		return domain.Conversation{}, fmt.Errorf("get conversation: %w", err)
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
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	conversations := make([]domain.Conversation, 0)
	for rows.Next() {
		var c domain.Conversation
		var createdAt, updatedAt string
		if err := rows.Scan(&c.ID, &c.Title, &createdAt, &updatedAt, &c.MessageCount); err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
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
	return affected("rename conversation", result, err)
}

func (s *SQLite) DeleteConversation(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, id)
	return affected("delete conversation", result, err)
}

func (s *SQLite) AddMessage(ctx context.Context, m domain.Message) (domain.Message, error) {
	// 新消息和会话 updated_at 必须原子更新，否则侧边栏排序可能与消息历史不一致。
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Message{}, fmt.Errorf("begin add message: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
INSERT INTO messages(id, conversation_id, role, content, tool_name, tool_call_id, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ConversationID, m.Role, m.Content, m.ToolName, m.ToolCallID, formatTime(m.CreatedAt))
	if err != nil {
		return domain.Message{}, fmt.Errorf("add message: %w", err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return domain.Message{}, fmt.Errorf("message sequence: %w", err)
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE conversations SET updated_at = ? WHERE id = ?`,
		formatTime(m.CreatedAt), m.ConversationID); err != nil {
		return domain.Message{}, fmt.Errorf("touch conversation: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return domain.Message{}, fmt.Errorf("commit message: %w", err)
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
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()

	messages := make([]domain.Message, 0)
	for rows.Next() {
		var m domain.Message
		var createdAt string
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.ToolName, &m.ToolCallID, &m.Sequence, &createdAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
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
	return wrap("create run", err)
}

func (s *SQLite) FinishRun(ctx context.Context, id, status, assistantMessageID, errorMessage string, completedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE agent_runs
SET status = ?, assistant_message_id = NULLIF(?, ''), error = ?, completed_at = ?
WHERE id = ?`, status, assistantMessageID, errorMessage, formatTime(completedAt), id)
	return affected("finish run", result, err)
}

func (s *SQLite) AppendRunEvent(ctx context.Context, event domain.RunEvent) (domain.RunEvent, error) {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("encode run event: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO run_events(id, run_id, type, agent_name, tool_name, payload, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.RunID, event.Type, event.AgentName, event.ToolName, string(payload), formatTime(event.CreatedAt))
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("append run event: %w", err)
	}
	event.Sequence, err = result.LastInsertId()
	return event, err
}

func (s *SQLite) ListRunEvents(ctx context.Context, runID string) ([]domain.RunEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, type, agent_name, tool_name, payload, sequence, created_at
FROM run_events WHERE run_id = ? ORDER BY sequence ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list run events: %w", err)
	}
	defer rows.Close()

	events := make([]domain.RunEvent, 0)
	for rows.Next() {
		var event domain.RunEvent
		var payload, createdAt string
		if err := rows.Scan(&event.ID, &event.RunID, &event.Type, &event.AgentName, &event.ToolName, &payload, &event.Sequence, &createdAt); err != nil {
			return nil, fmt.Errorf("scan run event: %w", err)
		}
		if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
			return nil, fmt.Errorf("decode run event: %w", err)
		}
		if event.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
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
		return time.Time{}, fmt.Errorf("parse stored time: %w", err)
	}
	return t, nil
}
