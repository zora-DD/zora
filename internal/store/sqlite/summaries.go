package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/summary"
)

func (s *SQLite) GetConversationSummary(ctx context.Context, conversationID string) (summary.Summary, error) {
	var item summary.Summary
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT conversation_id, content, through_sequence, message_count, model, updated_at
FROM conversation_summaries WHERE conversation_id = ?`, conversationID).Scan(
		&item.ConversationID, &item.Content, &item.ThroughSequence,
		&item.MessageCount, &item.Model, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return summary.Summary{}, summary.ErrNotFound
	}
	if err != nil {
		return summary.Summary{}, fmt.Errorf("查询会话摘要失败：%w", err)
	}
	item.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return summary.Summary{}, err
	}
	return item, nil
}

func (s *SQLite) UpsertConversationSummary(ctx context.Context, item summary.Summary) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO conversation_summaries(
    conversation_id, content, through_sequence, message_count, model, updated_at
) VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(conversation_id) DO UPDATE SET
    content = excluded.content,
    through_sequence = excluded.through_sequence,
    message_count = excluded.message_count,
    model = excluded.model,
    updated_at = excluded.updated_at`,
		item.ConversationID, item.Content, item.ThroughSequence,
		item.MessageCount, item.Model, formatTime(item.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("保存会话摘要失败：%w", err)
	}
	return nil
}

// ListMessagesForSummary 使用开区间/闭区间读取尚未摘要的消息，确保增量边界不会重复。
func (s *SQLite) ListMessagesForSummary(ctx context.Context, conversationID string, afterSequence, throughSequence int64, limit int) ([]domain.Message, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, conversation_id, role, content, tool_name, tool_call_id, sequence, created_at
FROM messages
WHERE conversation_id = ? AND sequence > ? AND sequence <= ?
ORDER BY sequence ASC
LIMIT ?`, conversationID, afterSequence, throughSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("查询待摘要消息失败：%w", err)
	}
	defer rows.Close()

	messages := make([]domain.Message, 0)
	for rows.Next() {
		var message domain.Message
		var createdAt string
		if err := rows.Scan(
			&message.ID, &message.ConversationID, &message.Role, &message.Content,
			&message.ToolName, &message.ToolCallID, &message.Sequence, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("读取待摘要消息失败：%w", err)
		}
		if message.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历待摘要消息失败：%w", err)
	}
	return messages, nil
}
