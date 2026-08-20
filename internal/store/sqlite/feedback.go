package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/store"
)

func (s *SQLite) UpsertAnswerFeedback(ctx context.Context, item domain.AnswerFeedback) (domain.AnswerFeedback, error) {
	signals, err := json.Marshal(item.Signals)
	if err != nil {
		return domain.AnswerFeedback{}, fmt.Errorf("编码答案反馈信号失败：%w", err)
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO answer_feedback(id, conversation_id, message_id, source, rating, reason, signals, created_at, updated_at)
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?
WHERE EXISTS(
    SELECT 1 FROM messages WHERE id = ? AND conversation_id = ? AND role = 'assistant'
)
ON CONFLICT(message_id, source) DO UPDATE SET
    rating = excluded.rating, reason = excluded.reason, signals = excluded.signals, updated_at = excluded.updated_at`,
		item.ID, item.ConversationID, item.MessageID, item.Source, item.Rating, item.Reason,
		string(signals), formatTime(item.CreatedAt), formatTime(item.UpdatedAt), item.MessageID, item.ConversationID)
	if err != nil {
		return domain.AnswerFeedback{}, fmt.Errorf("保存答案反馈失败：%w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return domain.AnswerFeedback{}, store.ErrNotFound
	}
	return scanAnswerFeedback(s.db.QueryRowContext(ctx, `
SELECT id, conversation_id, message_id, source, rating, reason, signals, created_at, updated_at
FROM answer_feedback WHERE message_id = ? AND source = ?`, item.MessageID, item.Source))
}

func (s *SQLite) ListAnswerFeedback(ctx context.Context, conversationID string) ([]domain.AnswerFeedback, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, conversation_id, message_id, source, rating, reason, signals, created_at, updated_at
FROM answer_feedback WHERE conversation_id = ? ORDER BY updated_at ASC, id ASC`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("查询答案反馈失败：%w", err)
	}
	defer rows.Close()
	items := make([]domain.AnswerFeedback, 0)
	for rows.Next() {
		item, scanErr := scanAnswerFeedback(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanAnswerFeedback(scanner rowScanner) (domain.AnswerFeedback, error) {
	var item domain.AnswerFeedback
	var signals, createdAt, updatedAt string
	if err := scanner.Scan(
		&item.ID, &item.ConversationID, &item.MessageID, &item.Source, &item.Rating,
		&item.Reason, &signals, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.AnswerFeedback{}, store.ErrNotFound
		}
		return domain.AnswerFeedback{}, fmt.Errorf("读取答案反馈失败：%w", err)
	}
	if err := json.Unmarshal([]byte(signals), &item.Signals); err != nil {
		return domain.AnswerFeedback{}, fmt.Errorf("解析答案反馈信号失败：%w", err)
	}
	var err error
	if item.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.AnswerFeedback{}, err
	}
	if item.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.AnswerFeedback{}, err
	}
	return item, nil
}
