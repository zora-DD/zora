package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/summary"
)

func (p *Postgres) GetConversationSummary(ctx context.Context, conversationID string) (summary.Summary, error) {
	var item summary.Summary
	err := p.pool.QueryRow(ctx, `
SELECT conversation_id, content, through_sequence, message_count, model, updated_at
FROM conversation_summaries WHERE conversation_id = $1`, conversationID).Scan(
		&item.ConversationID, &item.Content, &item.ThroughSequence,
		&item.MessageCount, &item.Model, &item.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return summary.Summary{}, summary.ErrNotFound
	}
	if err != nil {
		return summary.Summary{}, fmt.Errorf("查询会话摘要失败：%w", err)
	}
	item.UpdatedAt = normalizeTime(item.UpdatedAt)
	return item, nil
}

func (p *Postgres) UpsertConversationSummary(ctx context.Context, item summary.Summary) error {
	_, err := p.pool.Exec(ctx, `
INSERT INTO conversation_summaries(
    conversation_id, content, through_sequence, message_count, model, updated_at
) VALUES($1, $2, $3, $4, $5, $6)
ON CONFLICT(conversation_id) DO UPDATE SET
    content = EXCLUDED.content,
    through_sequence = EXCLUDED.through_sequence,
    message_count = EXCLUDED.message_count,
    model = EXCLUDED.model,
    updated_at = EXCLUDED.updated_at`,
		item.ConversationID, item.Content, item.ThroughSequence,
		item.MessageCount, item.Model, normalizeTime(item.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("保存会话摘要失败：%w", err)
	}
	return nil
}

func (p *Postgres) ListMessagesForSummary(ctx context.Context, conversationID string, afterSequence, throughSequence int64, limit int) ([]domain.Message, error) {
	rows, err := p.pool.Query(ctx, `
SELECT id, conversation_id, role, content, tool_name, tool_call_id, sequence, created_at
FROM messages
WHERE conversation_id = $1 AND sequence > $2 AND sequence <= $3
ORDER BY sequence ASC
LIMIT $4`, conversationID, afterSequence, throughSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("查询待摘要消息失败：%w", err)
	}
	defer rows.Close()

	messages := make([]domain.Message, 0)
	for rows.Next() {
		var message domain.Message
		if err := rows.Scan(
			&message.ID, &message.ConversationID, &message.Role, &message.Content,
			&message.ToolName, &message.ToolCallID, &message.Sequence, &message.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("读取待摘要消息失败：%w", err)
		}
		message.CreatedAt = normalizeTime(message.CreatedAt)
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历待摘要消息失败：%w", err)
	}
	return messages, nil
}
