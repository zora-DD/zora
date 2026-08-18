package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zhiruo/zora/internal/office"
	"github.com/zhiruo/zora/internal/store"
)

func (s *SQLite) CreateOperation(ctx context.Context, operation office.Operation, event office.OperationEvent) (office.Operation, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return office.Operation{}, false, fmt.Errorf("开始创建办公执行任务事务失败：%w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
INSERT INTO office_operations(
    id, draft_id, kind, status, idempotency_key, executor_name, attempt,
    lease_owner, external_reference, last_error, created_at, updated_at
)
SELECT ?, id, kind, ?, ?, '', 0, '', '', '', ?, ?
FROM office_drafts WHERE id = ? AND status = 'approved'
ON CONFLICT(draft_id) DO NOTHING`, operation.ID, operation.Status, operation.IdempotencyKey,
		formatTime(operation.CreatedAt), formatTime(operation.UpdatedAt), operation.DraftID)
	if err != nil {
		return office.Operation{}, false, fmt.Errorf("创建办公执行任务失败：%w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return office.Operation{}, false, fmt.Errorf("读取办公执行任务创建结果失败：%w", err)
	}
	if affected == 0 {
		existing, getErr := scanOfficeOperation(tx.QueryRowContext(ctx, officeOperationSelect+` WHERE draft_id = ?`, operation.DraftID))
		if getErr == nil {
			return existing, false, nil
		}
		if !errors.Is(getErr, sql.ErrNoRows) {
			return office.Operation{}, false, fmt.Errorf("查询幂等办公执行任务失败：%w", getErr)
		}
		draft, draftErr := scanOfficeDraft(tx.QueryRowContext(ctx, officeDraftSelect+` WHERE id = ?`, operation.DraftID))
		if errors.Is(draftErr, sql.ErrNoRows) {
			return office.Operation{}, false, store.ErrNotFound
		}
		if draftErr != nil {
			return office.Operation{}, false, fmt.Errorf("查询办公草稿状态失败：%w", draftErr)
		}
		return office.Operation{}, false, fmt.Errorf("%w：只有 approved 状态的草稿可以准备执行，当前为 %s", office.ErrStateConflict, draft.Status)
	}
	event.OperationID = operation.ID
	if _, err := tx.ExecContext(ctx, `
INSERT INTO office_operation_events(id, operation_id, from_status, to_status, attempt, actor, reason, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, event.OperationID, event.FromStatus, event.ToStatus,
		event.Attempt, event.Actor, event.Reason, formatTime(event.CreatedAt)); err != nil {
		return office.Operation{}, false, fmt.Errorf("保存办公执行任务审计事件失败：%w", err)
	}
	saved, err := scanOfficeOperation(tx.QueryRowContext(ctx, officeOperationSelect+` WHERE id = ?`, operation.ID))
	if err != nil {
		return office.Operation{}, false, fmt.Errorf("读取新建办公执行任务失败：%w", err)
	}
	if err := tx.Commit(); err != nil {
		return office.Operation{}, false, fmt.Errorf("提交办公执行任务创建事务失败：%w", err)
	}
	return saved, true, nil
}

func (s *SQLite) GetOperation(ctx context.Context, id string) (office.Operation, error) {
	item, err := scanOfficeOperation(s.db.QueryRowContext(ctx, officeOperationSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return office.Operation{}, store.ErrNotFound
	}
	if err != nil {
		return office.Operation{}, fmt.Errorf("查询办公执行任务失败：%w", err)
	}
	return item, nil
}

func (s *SQLite) GetOperationByDraft(ctx context.Context, draftID string) (office.Operation, error) {
	item, err := scanOfficeOperation(s.db.QueryRowContext(ctx, officeOperationSelect+` WHERE draft_id = ?`, draftID))
	if errors.Is(err, sql.ErrNoRows) {
		return office.Operation{}, store.ErrNotFound
	}
	if err != nil {
		return office.Operation{}, fmt.Errorf("按草稿查询办公执行任务失败：%w", err)
	}
	return item, nil
}

func (s *SQLite) ListOperations(ctx context.Context, filter office.OperationFilter) ([]office.Operation, error) {
	query := officeOperationSelect + ` WHERE 1=1`
	args := make([]any, 0, 3)
	if filter.DraftID != "" {
		query += ` AND draft_id = ?`
		args = append(args, filter.DraftID)
	}
	if filter.Status != "" {
		query += ` AND status = ?`
		args = append(args, filter.Status)
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询办公执行任务列表失败：%w", err)
	}
	defer rows.Close()
	return scanOfficeOperations(rows)
}

func (s *SQLite) ClaimOperation(ctx context.Context, id, executorName, leaseOwner string, now, leaseUntil time.Time, operationEvent office.OperationEvent, draftEvent office.DraftEvent) (office.Operation, office.Draft, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("开始领取办公执行任务事务失败：%w", err)
	}
	defer tx.Rollback()
	current, err := scanOfficeOperation(tx.QueryRowContext(ctx, officeOperationSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return office.Operation{}, office.Draft{}, store.ErrNotFound
	}
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("查询待领取办公执行任务失败：%w", err)
	}
	if current.Status != office.OperationPending && current.Status != office.OperationFailed {
		return office.Operation{}, office.Draft{}, fmt.Errorf("%w：执行任务当前为 %s，不能领取", office.ErrStateConflict, current.Status)
	}
	expectedDraftStatus := office.StatusApproved
	if current.Status == office.OperationFailed {
		expectedDraftStatus = office.StatusFailed
	}
	draft, err := scanOfficeDraft(tx.QueryRowContext(ctx, officeDraftSelect+` WHERE id = ?`, current.DraftID))
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("查询执行任务对应草稿失败：%w", err)
	}
	if draft.Status != expectedDraftStatus {
		return office.Operation{}, office.Draft{}, fmt.Errorf("%w：草稿当前为 %s，执行任务要求 %s", office.ErrStateConflict, draft.Status, expectedDraftStatus)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE office_operations
SET status = ?, executor_name = ?, attempt = attempt + 1, lease_owner = ?, lease_until = ?,
    last_error = '', updated_at = ?, completed_at = NULL
WHERE id = ? AND status = ?`, office.OperationExecuting, executorName, leaseOwner,
		formatTime(leaseUntil), formatTime(now), id, current.Status)
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("领取办公执行任务失败：%w", err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return office.Operation{}, office.Draft{}, fmt.Errorf("%w：执行任务已被其他请求领取", office.ErrStateConflict)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE office_drafts SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		office.StatusExecuting, formatTime(now), draft.ID, expectedDraftStatus); err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("更新执行中草稿状态失败：%w", err)
	}
	updated, err := scanOfficeOperation(tx.QueryRowContext(ctx, officeOperationSelect+` WHERE id = ?`, id))
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("读取已领取办公执行任务失败：%w", err)
	}
	operationEvent.OperationID, operationEvent.FromStatus = id, current.Status
	operationEvent.ToStatus, operationEvent.Attempt = office.OperationExecuting, updated.Attempt
	if err := insertSQLiteOperationEvent(ctx, tx, operationEvent); err != nil {
		return office.Operation{}, office.Draft{}, err
	}
	draftEvent.DraftID, draftEvent.FromStatus = draft.ID, expectedDraftStatus
	draftEvent.ToStatus = office.StatusExecuting
	if err := insertSQLiteDraftEvent(ctx, tx, draftEvent); err != nil {
		return office.Operation{}, office.Draft{}, err
	}
	updatedDraft, err := scanOfficeDraft(tx.QueryRowContext(ctx, officeDraftSelect+` WHERE id = ?`, draft.ID))
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("读取执行中办公草稿失败：%w", err)
	}
	if err := tx.Commit(); err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("提交领取办公执行任务事务失败：%w", err)
	}
	return updated, updatedDraft, nil
}

func (s *SQLite) FinishOperation(ctx context.Context, id, leaseOwner, nextStatus, externalReference, lastError string, now time.Time, operationEvent office.OperationEvent, draftEvent office.DraftEvent) (office.Operation, office.Draft, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("开始完成办公执行任务事务失败：%w", err)
	}
	defer tx.Rollback()
	current, err := scanOfficeOperation(tx.QueryRowContext(ctx, officeOperationSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return office.Operation{}, office.Draft{}, store.ErrNotFound
	}
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("查询待完成办公执行任务失败：%w", err)
	}
	if current.Status != office.OperationExecuting || current.LeaseOwner != leaseOwner {
		return office.Operation{}, office.Draft{}, fmt.Errorf("%w：执行任务租约已失效或状态已改变", office.ErrStateConflict)
	}
	var completedAt any
	if nextStatus == office.OperationCompleted {
		completedAt = formatTime(now)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE office_operations
SET status = ?, lease_owner = '', lease_until = NULL, external_reference = ?, last_error = ?,
    updated_at = ?, completed_at = ?
WHERE id = ? AND status = ? AND lease_owner = ?`, nextStatus, externalReference, lastError,
		formatTime(now), completedAt, id, office.OperationExecuting, leaseOwner)
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("完成办公执行任务失败：%w", err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return office.Operation{}, office.Draft{}, fmt.Errorf("%w：办公执行任务租约已被回收", office.ErrStateConflict)
	}
	draftStatus := office.StatusFailed
	if nextStatus == office.OperationCompleted {
		draftStatus = office.StatusCompleted
	}
	result, err = tx.ExecContext(ctx, `UPDATE office_drafts SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		draftStatus, formatTime(now), current.DraftID, office.StatusExecuting)
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("完成办公草稿执行状态失败：%w", err)
	}
	affected, _ = result.RowsAffected()
	if affected != 1 {
		return office.Operation{}, office.Draft{}, fmt.Errorf("%w：办公草稿已不在 executing 状态", office.ErrStateConflict)
	}
	operationEvent.OperationID = id
	operationEvent.FromStatus, operationEvent.ToStatus = office.OperationExecuting, nextStatus
	operationEvent.Attempt = current.Attempt
	if err := insertSQLiteOperationEvent(ctx, tx, operationEvent); err != nil {
		return office.Operation{}, office.Draft{}, err
	}
	draftEvent.DraftID = current.DraftID
	draftEvent.FromStatus, draftEvent.ToStatus = office.StatusExecuting, draftStatus
	if err := insertSQLiteDraftEvent(ctx, tx, draftEvent); err != nil {
		return office.Operation{}, office.Draft{}, err
	}
	updated, err := scanOfficeOperation(tx.QueryRowContext(ctx, officeOperationSelect+` WHERE id = ?`, id))
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("读取已完成办公执行任务失败：%w", err)
	}
	updatedDraft, err := scanOfficeDraft(tx.QueryRowContext(ctx, officeDraftSelect+` WHERE id = ?`, current.DraftID))
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("读取已完成办公草稿失败：%w", err)
	}
	if err := tx.Commit(); err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("提交完成办公执行任务事务失败：%w", err)
	}
	return updated, updatedDraft, nil
}

func (s *SQLite) ListOperationEvents(ctx context.Context, operationID string) ([]office.OperationEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, operation_id, from_status, to_status, attempt, actor, reason, created_at
FROM office_operation_events WHERE operation_id = ? ORDER BY created_at, id`, operationID)
	if err != nil {
		return nil, fmt.Errorf("查询办公执行任务审计事件失败：%w", err)
	}
	defer rows.Close()
	items := make([]office.OperationEvent, 0)
	for rows.Next() {
		var item office.OperationEvent
		var createdAt string
		if err := rows.Scan(&item.ID, &item.OperationID, &item.FromStatus, &item.ToStatus,
			&item.Attempt, &item.Actor, &item.Reason, &createdAt); err != nil {
			return nil, fmt.Errorf("读取办公执行任务审计事件失败：%w", err)
		}
		item.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("解析办公执行任务审计时间失败：%w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) ListExpiredOperations(ctx context.Context, now time.Time, limit int) ([]office.Operation, error) {
	rows, err := s.db.QueryContext(ctx, officeOperationSelect+`
WHERE status = ? AND lease_until IS NOT NULL AND lease_until <= ?
ORDER BY lease_until LIMIT ?`, office.OperationExecuting, formatTime(now), limit)
	if err != nil {
		return nil, fmt.Errorf("查询租约过期办公执行任务失败：%w", err)
	}
	defer rows.Close()
	return scanOfficeOperations(rows)
}

func insertSQLiteOperationEvent(ctx context.Context, tx *sql.Tx, event office.OperationEvent) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO office_operation_events(id, operation_id, from_status, to_status, attempt, actor, reason, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, event.OperationID, event.FromStatus, event.ToStatus,
		event.Attempt, event.Actor, event.Reason, formatTime(event.CreatedAt))
	if err != nil {
		return fmt.Errorf("保存办公执行任务审计事件失败：%w", err)
	}
	return nil
}

func insertSQLiteDraftEvent(ctx context.Context, tx *sql.Tx, event office.DraftEvent) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO office_draft_events(id, draft_id, from_status, to_status, actor, reason, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?)`, event.ID, event.DraftID, event.FromStatus, event.ToStatus,
		event.Actor, event.Reason, formatTime(event.CreatedAt))
	if err != nil {
		return fmt.Errorf("保存办公草稿审计事件失败：%w", err)
	}
	return nil
}

const officeOperationSelect = `
SELECT id, draft_id, kind, status, idempotency_key, executor_name, attempt,
       lease_owner, lease_until, external_reference, last_error, created_at, updated_at, completed_at
FROM office_operations`

func scanOfficeOperation(scanner officeDraftScanner) (office.Operation, error) {
	var item office.Operation
	var leaseUntil, completedAt sql.NullString
	var createdAt, updatedAt string
	if err := scanner.Scan(
		&item.ID, &item.DraftID, &item.Kind, &item.Status, &item.IdempotencyKey,
		&item.ExecutorName, &item.Attempt, &item.LeaseOwner, &leaseUntil,
		&item.ExternalReference, &item.LastError, &createdAt, &updatedAt, &completedAt,
	); err != nil {
		return office.Operation{}, err
	}
	var err error
	if item.CreatedAt, err = parseTime(createdAt); err != nil {
		return office.Operation{}, err
	}
	if item.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return office.Operation{}, err
	}
	if leaseUntil.Valid {
		parsed, parseErr := parseTime(leaseUntil.String)
		if parseErr != nil {
			return office.Operation{}, parseErr
		}
		item.LeaseUntil = &parsed
	}
	if completedAt.Valid {
		parsed, parseErr := parseTime(completedAt.String)
		if parseErr != nil {
			return office.Operation{}, parseErr
		}
		item.CompletedAt = &parsed
	}
	return item, nil
}

func scanOfficeOperations(rows *sql.Rows) ([]office.Operation, error) {
	items := make([]office.Operation, 0)
	for rows.Next() {
		item, err := scanOfficeOperation(rows)
		if err != nil {
			return nil, fmt.Errorf("读取办公执行任务失败：%w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历办公执行任务失败：%w", err)
	}
	return items, nil
}
