package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"github.com/zhiruo/zora/internal/memory"
)

func (p *Postgres) CreateMemory(ctx context.Context, item memory.Memory) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始创建长期记忆事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
INSERT INTO memories(
    id, kind, memory_key, content, importance, user_edited, source_type,
    source_conversation_id, source_message_id, created_at, updated_at, expires_at
) VALUES($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), NULLIF($9, ''), $10, $11, $12)`,
		item.ID, item.Kind, item.MemoryKey, item.Content, item.Importance, item.UserEdited, item.SourceType,
		item.SourceConversationID, item.SourceMessageID,
		normalizeTime(item.CreatedAt), normalizeTime(item.UpdatedAt), normalizeOptionalTime(item.ExpiresAt),
	)
	if err != nil {
		return fmt.Errorf("创建长期记忆失败：%w", err)
	}
	if err := upsertPostgresMemoryEmbedding(ctx, tx, item); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交创建长期记忆事务失败：%w", err)
	}
	return nil
}

func (p *Postgres) GetMemory(ctx context.Context, id string) (memory.Memory, error) {
	item, err := scanMemory(p.pool.QueryRow(ctx, `
SELECT id, kind, memory_key, content, importance, user_edited, source_type, source_conversation_id,
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

func (p *Postgres) GetMemoryByKey(ctx context.Context, kind, memoryKey string) (memory.Memory, error) {
	item, err := scanMemory(p.pool.QueryRow(ctx, `
SELECT id, kind, memory_key, content, importance, user_edited, source_type, source_conversation_id,
       source_message_id, created_at, updated_at, expires_at
FROM memories WHERE kind = $1 AND memory_key = $2
ORDER BY updated_at DESC LIMIT 1`, kind, memoryKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.Memory{}, memory.ErrNotFound
	}
	if err != nil {
		return memory.Memory{}, fmt.Errorf("按 Key 查询长期记忆失败：%w", err)
	}
	return item, nil
}

func (p *Postgres) ListMemories(ctx context.Context, filter memory.ListFilter) ([]memory.Memory, error) {
	rows, err := p.pool.Query(ctx, `
SELECT id, kind, memory_key, content, importance, user_edited, source_type, source_conversation_id,
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
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始更新长期记忆事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
UPDATE memories
SET kind = $1, memory_key = $2, content = $3, importance = $4, user_edited = $5,
    source_type = $6, source_conversation_id = NULLIF($7, ''), source_message_id = NULLIF($8, ''),
    updated_at = $9, expires_at = $10
WHERE id = $11`, item.Kind, item.MemoryKey, item.Content, item.Importance, item.UserEdited,
		item.SourceType, item.SourceConversationID, item.SourceMessageID,
		normalizeTime(item.UpdatedAt), normalizeOptionalTime(item.ExpiresAt), item.ID)
	if err != nil {
		return fmt.Errorf("更新长期记忆失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return memory.ErrNotFound
	}
	if err := upsertPostgresMemoryEmbedding(ctx, tx, item); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交更新长期记忆事务失败：%w", err)
	}
	return nil
}

func upsertPostgresMemoryEmbedding(ctx context.Context, tx pgx.Tx, item memory.Memory) error {
	if len(item.Embedding) == 0 {
		return nil
	}
	if item.EmbeddingDimensions != len(item.Embedding) || item.EmbeddingModel == "" || item.IndexVersion < 1 {
		return fmt.Errorf("长期记忆向量元数据无效")
	}
	_, err := tx.Exec(ctx, `
INSERT INTO memory_embeddings(memory_id, kind, embedding_model, embedding_dimensions, index_version, embedding, updated_at)
VALUES($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT(memory_id) DO UPDATE SET
    kind = EXCLUDED.kind, embedding_model = EXCLUDED.embedding_model,
    embedding_dimensions = EXCLUDED.embedding_dimensions, index_version = EXCLUDED.index_version,
    embedding = EXCLUDED.embedding, updated_at = EXCLUDED.updated_at`,
		item.ID, item.Kind, item.EmbeddingModel, item.EmbeddingDimensions, item.IndexVersion,
		pgvector.NewVector(toFloat32(item.Embedding)), normalizeTime(item.UpdatedAt))
	if err != nil {
		return fmt.Errorf("写入长期记忆向量索引失败：%w", err)
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
		&item.ID, &item.Kind, &item.MemoryKey, &item.Content, &item.Importance, &item.UserEdited, &item.SourceType,
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
