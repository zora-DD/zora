package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zhiruo/zora/internal/approval"
	"github.com/zhiruo/zora/internal/store"
)

var _ approval.Store = (*Postgres)(nil)

func (p *Postgres) CreateApproval(ctx context.Context, item approval.Approval) error {
	_, err := p.pool.Exec(ctx, `
INSERT INTO approval_requests(
    id, run_id, conversation_id, user_message_id, status, trigger_reason, requested_at
) VALUES($1, $2, $3, $4, $5, $6, $7)`,
		item.ID, item.RunID, item.ConversationID, item.UserMessageID,
		item.Status, item.TriggerReason, normalizeTime(item.RequestedAt))
	if err != nil {
		return fmt.Errorf("创建人工审批记录失败：%w", err)
	}
	return nil
}

func (p *Postgres) GetApproval(ctx context.Context, id string) (approval.Approval, error) {
	row := p.pool.QueryRow(ctx, `
SELECT id, run_id, conversation_id, user_message_id, status, trigger_reason,
       decision_reason, requested_at, decided_at
FROM approval_requests WHERE id = $1`, id)
	item, err := scanApproval(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return approval.Approval{}, store.ErrNotFound
	}
	if err != nil {
		return approval.Approval{}, fmt.Errorf("查询人工审批记录失败：%w", err)
	}
	return item, nil
}

func (p *Postgres) ListApprovals(ctx context.Context, status string, limit int) ([]approval.Approval, error) {
	query := `
SELECT id, run_id, conversation_id, user_message_id, status, trigger_reason,
       decision_reason, requested_at, decided_at
FROM approval_requests`
	args := make([]any, 0, 2)
	if status != "" {
		query += " WHERE status = $1 ORDER BY requested_at DESC LIMIT $2"
		args = append(args, status, limit)
	} else {
		query += " ORDER BY requested_at DESC LIMIT $1"
		args = append(args, limit)
	}
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询人工审批列表失败：%w", err)
	}
	defer rows.Close()
	items := make([]approval.Approval, 0)
	for rows.Next() {
		item, scanErr := scanApproval(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("读取人工审批记录失败：%w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历人工审批记录失败：%w", err)
	}
	return items, nil
}

func (p *Postgres) ResolveApproval(ctx context.Context, id, status, decisionReason string, decidedAt time.Time) (approval.Approval, error) {
	row := p.pool.QueryRow(ctx, `
UPDATE approval_requests
SET status = $1, decision_reason = $2, decided_at = $3
WHERE id = $4 AND status = 'pending'
RETURNING id, run_id, conversation_id, user_message_id, status, trigger_reason,
          decision_reason, requested_at, decided_at`,
		status, decisionReason, normalizeTime(decidedAt), id)
	item, err := scanApproval(row)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := p.GetApproval(ctx, id); getErr != nil {
			return approval.Approval{}, getErr
		}
		return approval.Approval{}, fmt.Errorf("审批已经处理，不能重复提交决定")
	}
	if err != nil {
		return approval.Approval{}, fmt.Errorf("更新人工审批状态失败：%w", err)
	}
	return item, nil
}

type approvalScanner interface {
	Scan(dest ...any) error
}

func scanApproval(scanner approvalScanner) (approval.Approval, error) {
	var item approval.Approval
	if err := scanner.Scan(
		&item.ID, &item.RunID, &item.ConversationID, &item.UserMessageID,
		&item.Status, &item.TriggerReason, &item.DecisionReason, &item.RequestedAt, &item.DecidedAt,
	); err != nil {
		return approval.Approval{}, err
	}
	item.RequestedAt = normalizeTime(item.RequestedAt)
	if item.DecidedAt != nil {
		decidedAt := normalizeTime(*item.DecidedAt)
		item.DecidedAt = &decidedAt
	}
	return item, nil
}
