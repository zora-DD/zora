package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/zhiruo/zora/internal/approval"
	"github.com/zhiruo/zora/internal/store"
)

var _ approval.Store = (*SQLite)(nil)

func (s *SQLite) CreateApproval(ctx context.Context, item approval.Approval) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO approval_requests(
    id, run_id, conversation_id, user_message_id, status, trigger_reason, requested_at
) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.RunID, item.ConversationID, item.UserMessageID,
		item.Status, item.TriggerReason, formatTime(item.RequestedAt))
	return wrap("创建人工审批记录", err)
}

func (s *SQLite) GetApproval(ctx context.Context, id string) (approval.Approval, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, run_id, conversation_id, user_message_id, status, trigger_reason,
       decision_reason, requested_at, decided_at
FROM approval_requests WHERE id = ?`, id)
	item, err := scanApproval(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return approval.Approval{}, store.ErrNotFound
		}
		return approval.Approval{}, fmt.Errorf("查询人工审批记录失败：%w", err)
	}
	return item, nil
}

func (s *SQLite) ListApprovals(ctx context.Context, status string, limit int) ([]approval.Approval, error) {
	query := `
SELECT id, run_id, conversation_id, user_message_id, status, trigger_reason,
       decision_reason, requested_at, decided_at
FROM approval_requests`
	var args []any
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, status)
	}
	query += " ORDER BY requested_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
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
	return items, rows.Err()
}

func (s *SQLite) ResolveApproval(ctx context.Context, id, status, decisionReason string, decidedAt time.Time) (approval.Approval, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE approval_requests
SET status = ?, decision_reason = ?, decided_at = ?
WHERE id = ? AND status = 'pending'`, status, decisionReason, formatTime(decidedAt), id)
	if err != nil {
		return approval.Approval{}, fmt.Errorf("更新人工审批状态失败：%w", err)
	}
	affectedRows, err := result.RowsAffected()
	if err != nil {
		return approval.Approval{}, fmt.Errorf("读取人工审批更新结果失败：%w", err)
	}
	if affectedRows == 0 {
		if _, getErr := s.GetApproval(ctx, id); getErr != nil {
			return approval.Approval{}, getErr
		}
		return approval.Approval{}, fmt.Errorf("审批已经处理，不能重复提交决定")
	}
	return s.GetApproval(ctx, id)
}

type approvalScanner interface {
	Scan(dest ...any) error
}

func scanApproval(scanner approvalScanner) (approval.Approval, error) {
	var item approval.Approval
	var requestedAt string
	var decidedAt sql.NullString
	if err := scanner.Scan(
		&item.ID, &item.RunID, &item.ConversationID, &item.UserMessageID,
		&item.Status, &item.TriggerReason, &item.DecisionReason, &requestedAt, &decidedAt,
	); err != nil {
		return approval.Approval{}, err
	}
	var err error
	if item.RequestedAt, err = parseTime(requestedAt); err != nil {
		return approval.Approval{}, err
	}
	if decidedAt.Valid {
		parsed, parseErr := parseTime(decidedAt.String)
		if parseErr != nil {
			return approval.Approval{}, parseErr
		}
		item.DecidedAt = &parsed
	}
	return item, nil
}
