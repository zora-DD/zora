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
	scope := requestScope(ctx)
	var item summary.Summary
	err := p.pool.QueryRow(ctx, `
SELECT conversation_id, content, through_sequence, message_count, model, updated_at
FROM conversation_summaries s JOIN conversations c ON c.id=s.conversation_id
WHERE s.conversation_id = $1 AND c.tenant_id=$2 AND c.principal_id=$3`, conversationID, scope.TenantID, scope.ID).Scan(
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
	scope := requestScope(ctx)
	_, err := p.pool.Exec(ctx, `
INSERT INTO conversation_summaries(
    conversation_id, content, through_sequence, message_count, model, updated_at
) SELECT $1, $2, $3, $4, $5, $6
WHERE EXISTS(SELECT 1 FROM conversations WHERE id=$1 AND tenant_id=$7 AND principal_id=$8)
ON CONFLICT(conversation_id) DO UPDATE SET
    content = EXCLUDED.content,
    through_sequence = EXCLUDED.through_sequence,
    message_count = EXCLUDED.message_count,
    model = EXCLUDED.model,
    updated_at = EXCLUDED.updated_at`,
		item.ConversationID, item.Content, item.ThroughSequence,
		item.MessageCount, item.Model, normalizeTime(item.UpdatedAt), scope.TenantID, scope.ID,
	)
	if err != nil {
		return fmt.Errorf("保存会话摘要失败：%w", err)
	}
	return nil
}

func (p *Postgres) ListMessagesForSummary(ctx context.Context, conversationID string, afterSequence, throughSequence int64, limit int) ([]domain.Message, error) {
	scope := requestScope(ctx)
	rows, err := p.pool.Query(ctx, `
SELECT m.id, m.conversation_id, m.role, m.content, m.tool_name, m.tool_call_id, m.sequence, m.created_at
FROM messages m JOIN conversations c ON c.id=m.conversation_id
WHERE m.conversation_id = $1 AND m.sequence > $2 AND m.sequence <= $3
  AND c.tenant_id=$4 AND c.principal_id=$5
ORDER BY m.sequence ASC
LIMIT $6`, conversationID, afterSequence, throughSequence, scope.TenantID, scope.ID, limit)
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
