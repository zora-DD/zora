package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/store"
)

func (p *Postgres) UpsertAnswerFeedback(ctx context.Context, item domain.AnswerFeedback) (domain.AnswerFeedback, error) {
	scope := requestScope(ctx)
	signals, err := json.Marshal(item.Signals)
	if err != nil {
		return domain.AnswerFeedback{}, fmt.Errorf("编码答案反馈信号失败：%w", err)
	}
	row := p.pool.QueryRow(ctx, `
INSERT INTO answer_feedback(id, conversation_id, message_id, source, rating, reason, signals, created_at, updated_at)
SELECT $1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9
WHERE EXISTS(
    SELECT 1 FROM messages m JOIN conversations c ON c.id = m.conversation_id
    WHERE m.id = $3 AND m.conversation_id = $2 AND m.role = 'assistant'
      AND c.tenant_id = $10 AND c.principal_id = $11
)
ON CONFLICT(message_id, source) DO UPDATE SET
    rating = excluded.rating, reason = excluded.reason, signals = excluded.signals, updated_at = excluded.updated_at
RETURNING id, conversation_id, message_id, source, rating, reason, signals, created_at, updated_at`,
		item.ID, item.ConversationID, item.MessageID, item.Source, item.Rating, item.Reason,
		string(signals), normalizeTime(item.CreatedAt), normalizeTime(item.UpdatedAt), scope.TenantID, scope.ID)
	result, err := scanAnswerFeedback(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AnswerFeedback{}, store.ErrNotFound
	}
	return result, err
}

func (p *Postgres) ListAnswerFeedback(ctx context.Context, conversationID string) ([]domain.AnswerFeedback, error) {
	scope := requestScope(ctx)
	rows, err := p.pool.Query(ctx, `
SELECT f.id, f.conversation_id, f.message_id, f.source, f.rating, f.reason, f.signals, f.created_at, f.updated_at
FROM answer_feedback f JOIN conversations c ON c.id = f.conversation_id
WHERE f.conversation_id = $1 AND c.tenant_id = $2 AND c.principal_id = $3
ORDER BY f.updated_at ASC, f.id ASC`, conversationID, scope.TenantID, scope.ID)
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
	var signals []byte
	if err := scanner.Scan(
		&item.ID, &item.ConversationID, &item.MessageID, &item.Source, &item.Rating,
		&item.Reason, &signals, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return domain.AnswerFeedback{}, err
	}
	if err := json.Unmarshal(signals, &item.Signals); err != nil {
		return domain.AnswerFeedback{}, fmt.Errorf("解析答案反馈信号失败：%w", err)
	}
	item.CreatedAt = normalizeTime(item.CreatedAt)
	item.UpdatedAt = normalizeTime(item.UpdatedAt)
	return item, nil
}
