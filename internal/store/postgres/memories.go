package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zhiruo/zora/internal/memory"
)

func (p *Postgres) CreateMemory(ctx context.Context, item memory.Memory) error {
	_, err := p.pool.Exec(ctx, `
INSERT INTO memories(
    id, kind, content, importance, source_type, source_conversation_id,
    source_message_id, created_at, updated_at, expires_at
) VALUES($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, $9, $10)`,
		item.ID, item.Kind, item.Content, item.Importance, item.SourceType,
		item.SourceConversationID, item.SourceMessageID,
		normalizeTime(item.CreatedAt), normalizeTime(item.UpdatedAt), normalizeOptionalTime(item.ExpiresAt),
	)
	if err != nil {
		return fmt.Errorf("创建长期记忆失败：%w", err)
	}
	return nil
}

func (p *Postgres) GetMemory(ctx context.Context, id string) (memory.Memory, error) {
	item, err := scanMemory(p.pool.QueryRow(ctx, `
SELECT id, kind, content, importance, source_type, source_conversation_id,
       source_message_id, created_at, updated_at, expires_at
FROM memories WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.Memory{}, memory.ErrNotFound
	}
	if err != nil {
		return memory.Memory{}, fmt.Errorf("查询长期记忆失败：%w", err)
	}
	return item, nil
}

func (p *Postgres) ListMemories(ctx context.Context, filter memory.ListFilter) ([]memory.Memory, error) {
	rows, err := p.pool.Query(ctx, `
SELECT id, kind, content, importance, source_type, source_conversation_id,
       source_message_id, created_at, updated_at, expires_at
FROM memories
WHERE ($1 = '' OR kind = $1)
  AND ($2 OR expires_at IS NULL OR expires_at > NOW())
ORDER BY importance DESC, updated_at DESC
LIMIT $3`, filter.Kind, filter.IncludeExpired, filter.Limit)
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

func (p *Postgres) UpdateMemory(ctx context.Context, item memory.Memory) error {
	tag, err := p.pool.Exec(ctx, `
UPDATE memories
SET kind = $1, content = $2, importance = $3, updated_at = $4, expires_at = $5
WHERE id = $6`, item.Kind, item.Content, item.Importance,
		normalizeTime(item.UpdatedAt), normalizeOptionalTime(item.ExpiresAt), item.ID)
	if err != nil {
		return fmt.Errorf("更新长期记忆失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return memory.ErrNotFound
	}
	return nil
}

func (p *Postgres) DeleteMemory(ctx context.Context, id string) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM memories WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("删除长期记忆失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return memory.ErrNotFound
	}
	return nil
}

func scanMemory(row pgx.Row) (memory.Memory, error) {
	var item memory.Memory
	var sourceConversationID, sourceMessageID *string
	var expiresAt *time.Time
	if err := row.Scan(
		&item.ID, &item.Kind, &item.Content, &item.Importance, &item.SourceType,
		&sourceConversationID, &sourceMessageID, &item.CreatedAt, &item.UpdatedAt, &expiresAt,
	); err != nil {
		return memory.Memory{}, err
	}
	if sourceConversationID != nil {
		item.SourceConversationID = *sourceConversationID
	}
	if sourceMessageID != nil {
		item.SourceMessageID = *sourceMessageID
	}
	item.CreatedAt = normalizeTime(item.CreatedAt)
	item.UpdatedAt = normalizeTime(item.UpdatedAt)
	item.ExpiresAt = normalizeOptionalTime(expiresAt)
	return item, nil
}

func normalizeOptionalTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := normalizeTime(*value)
	return &normalized
}
