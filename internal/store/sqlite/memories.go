package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zhiruo/zora/internal/memory"
)

func (s *SQLite) CreateMemory(ctx context.Context, item memory.Memory) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO memories(
    id, kind, memory_key, content, importance, user_edited, source_type,
    source_conversation_id, source_message_id, created_at, updated_at, expires_at
) VALUES(?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`,
		item.ID, item.Kind, item.MemoryKey, item.Content, item.Importance, boolInt(item.UserEdited), item.SourceType,
		item.SourceConversationID, item.SourceMessageID,
		formatTime(item.CreatedAt), formatTime(item.UpdatedAt), optionalTimeString(item.ExpiresAt),
	)
	if err != nil {
		return fmt.Errorf("创建长期记忆失败：%w", err)
	}
	return nil
}

func (s *SQLite) GetMemory(ctx context.Context, id string) (memory.Memory, error) {
	item, err := scanMemory(s.db.QueryRowContext(ctx, `
SELECT id, kind, memory_key, content, importance, user_edited, source_type, source_conversation_id,
       source_message_id, created_at, updated_at, expires_at
FROM memories WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Memory{}, memory.ErrNotFound
	}
	if err != nil {
		return memory.Memory{}, fmt.Errorf("查询长期记忆失败：%w", err)
	}
	return item, nil
}

func (s *SQLite) GetMemoryByKey(ctx context.Context, kind, memoryKey string) (memory.Memory, error) {
	item, err := scanMemory(s.db.QueryRowContext(ctx, `
SELECT id, kind, memory_key, content, importance, user_edited, source_type, source_conversation_id,
       source_message_id, created_at, updated_at, expires_at
FROM memories WHERE kind = ? AND memory_key = ?
ORDER BY updated_at DESC LIMIT 1`, kind, memoryKey))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Memory{}, memory.ErrNotFound
	}
	if err != nil {
		return memory.Memory{}, fmt.Errorf("按 Key 查询长期记忆失败：%w", err)
	}
	return item, nil
}

func (s *SQLite) ListMemories(ctx context.Context, filter memory.ListFilter) ([]memory.Memory, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, kind, memory_key, content, importance, user_edited, source_type, source_conversation_id,
       source_message_id, created_at, updated_at, expires_at
FROM memories
WHERE (? = '' OR kind = ?)
  AND (? = 1 OR expires_at IS NULL OR expires_at > ?)
ORDER BY importance DESC, updated_at DESC
LIMIT ?`, filter.Kind, filter.Kind, boolInt(filter.IncludeExpired), formatTimeNow(), filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("查询长期记忆列表失败：%w", err)
	}
	defer rows.Close()

	items := make([]memory.Memory, 0)
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("读取长期记忆失败：%w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历长期记忆失败：%w", err)
	}
	return items, nil
}

func (s *SQLite) UpdateMemory(ctx context.Context, item memory.Memory) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE memories
SET kind = ?, memory_key = ?, content = ?, importance = ?, user_edited = ?,
    source_type = ?, source_conversation_id = NULLIF(?, ''), source_message_id = NULLIF(?, ''),
    updated_at = ?, expires_at = ?
WHERE id = ?`, item.Kind, item.MemoryKey, item.Content, item.Importance, boolInt(item.UserEdited),
		item.SourceType, item.SourceConversationID, item.SourceMessageID,
		formatTime(item.UpdatedAt), optionalTimeString(item.ExpiresAt), item.ID)
	if err != nil {
		return fmt.Errorf("更新长期记忆失败：%w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("确认长期记忆更新结果失败：%w", err)
	}
	if count == 0 {
		return memory.ErrNotFound
	}
	return nil
}

func (s *SQLite) DeleteMemory(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除长期记忆失败：%w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("确认长期记忆删除结果失败：%w", err)
	}
	if count == 0 {
		return memory.ErrNotFound
	}
	return nil
}

func scanMemory(row rowScanner) (memory.Memory, error) {
	var item memory.Memory
	var sourceConversationID, sourceMessageID, expiresAt sql.NullString
	var userEdited int
	var createdAt, updatedAt string
	if err := row.Scan(
		&item.ID, &item.Kind, &item.MemoryKey, &item.Content, &item.Importance, &userEdited, &item.SourceType,
		&sourceConversationID, &sourceMessageID, &createdAt, &updatedAt, &expiresAt,
	); err != nil {
		return memory.Memory{}, err
	}
	item.SourceConversationID = sourceConversationID.String
	item.SourceMessageID = sourceMessageID.String
	item.UserEdited = userEdited == 1
	var err error
	if item.CreatedAt, err = parseTime(createdAt); err != nil {
		return memory.Memory{}, err
	}
	if item.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return memory.Memory{}, err
	}
	if expiresAt.Valid {
		value, err := parseTime(expiresAt.String)
		if err != nil {
			return memory.Memory{}, err
		}
		item.ExpiresAt = &value
	}
	return item, nil
}

func optionalTimeString(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func formatTimeNow() string { return formatTime(time.Now().UTC()) }

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
