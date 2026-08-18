package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zhiruo/zora/internal/office"
	"github.com/zhiruo/zora/internal/store"
)

func (p *Postgres) CreateOperation(ctx context.Context, operation office.Operation, event office.OperationEvent) (office.Operation, bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return office.Operation{}, false, fmt.Errorf("开始创建办公执行任务事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `
INSERT INTO office_operations(
    id, draft_id, kind, status, idempotency_key, executor_name, attempt,
    lease_owner, external_reference, last_error, created_at, updated_at
)
SELECT $1, id, kind, $2, $3, '', 0, '', '', '', $4, $5
FROM office_drafts WHERE id = $6 AND status = 'approved'
ON CONFLICT(draft_id) DO NOTHING`, operation.ID, operation.Status, operation.IdempotencyKey,
		normalizeTime(operation.CreatedAt), normalizeTime(operation.UpdatedAt), operation.DraftID)
	if err != nil {
		return office.Operation{}, false, fmt.Errorf("创建办公执行任务失败：%w", err)
	}
	if result.RowsAffected() == 0 {
		existing, getErr := scanOfficeOperation(tx.QueryRow(ctx, officeOperationSelect+` WHERE draft_id = $1`, operation.DraftID))
		if getErr == nil {
			return existing, false, nil
		}
		if !errors.Is(getErr, pgx.ErrNoRows) {
			return office.Operation{}, false, fmt.Errorf("查询幂等办公执行任务失败：%w", getErr)
		}
		draft, draftErr := scanOfficeDraft(tx.QueryRow(ctx, officeDraftSelect+` WHERE id = $1`, operation.DraftID))
		if errors.Is(draftErr, pgx.ErrNoRows) {
			return office.Operation{}, false, store.ErrNotFound
		}
		if draftErr != nil {
			return office.Operation{}, false, fmt.Errorf("查询办公草稿状态失败：%w", draftErr)
		}
		return office.Operation{}, false, fmt.Errorf("%w：只有 approved 状态的草稿可以准备执行，当前为 %s", office.ErrStateConflict, draft.Status)
	}
	event.OperationID = operation.ID
	if err := insertPostgresOperationEvent(ctx, tx, event); err != nil {
		return office.Operation{}, false, err
	}
	saved, err := scanOfficeOperation(tx.QueryRow(ctx, officeOperationSelect+` WHERE id = $1`, operation.ID))
	if err != nil {
		return office.Operation{}, false, fmt.Errorf("读取新建办公执行任务失败：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return office.Operation{}, false, fmt.Errorf("提交办公执行任务创建事务失败：%w", err)
	}
	return saved, true, nil
}

func (p *Postgres) GetOperation(ctx context.Context, id string) (office.Operation, error) {
	item, err := scanOfficeOperation(p.pool.QueryRow(ctx, officeOperationSelect+` WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return office.Operation{}, store.ErrNotFound
	}
	if err != nil {
		return office.Operation{}, fmt.Errorf("查询办公执行任务失败：%w", err)
	}
	return item, nil
}

func (p *Postgres) GetOperationByDraft(ctx context.Context, draftID string) (office.Operation, error) {
	item, err := scanOfficeOperation(p.pool.QueryRow(ctx, officeOperationSelect+` WHERE draft_id = $1`, draftID))
	if errors.Is(err, pgx.ErrNoRows) {
		return office.Operation{}, store.ErrNotFound
	}
	if err != nil {
		return office.Operation{}, fmt.Errorf("按草稿查询办公执行任务失败：%w", err)
	}
	return item, nil
}

func (p *Postgres) ListOperations(ctx context.Context, filter office.OperationFilter) ([]office.Operation, error) {
	query := officeOperationSelect + ` WHERE 1=1`
	args := make([]any, 0, 3)
	position := 1
	if filter.DraftID != "" {
		query += fmt.Sprintf(" AND draft_id = $%d", position)
		args = append(args, filter.DraftID)
		position++
	}
	if filter.Status != "" {
		query += fmt.Sprintf(" AND status = $%d", position)
		args = append(args, filter.Status)
		position++
	}
	query += fmt.Sprintf(" ORDER BY updated_at DESC LIMIT $%d", position)
	args = append(args, filter.Limit)
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询办公执行任务列表失败：%w", err)
	}
	defer rows.Close()
	return scanOfficeOperations(rows)
}

func (p *Postgres) ClaimOperation(ctx context.Context, id, executorName, leaseOwner string, now, leaseUntil time.Time, operationEvent office.OperationEvent, draftEvent office.DraftEvent) (office.Operation, office.Draft, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("开始领取办公执行任务事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	current, err := scanOfficeOperation(tx.QueryRow(ctx, officeOperationSelect+` WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
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
	draft, err := scanOfficeDraft(tx.QueryRow(ctx, officeDraftSelect+` WHERE id = $1 FOR UPDATE`, current.DraftID))
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("查询执行任务对应草稿失败：%w", err)
	}
	if draft.Status != expectedDraftStatus {
		return office.Operation{}, office.Draft{}, fmt.Errorf("%w：草稿当前为 %s，执行任务要求 %s", office.ErrStateConflict, draft.Status, expectedDraftStatus)
	}
	updated, err := scanOfficeOperation(tx.QueryRow(ctx, `
UPDATE office_operations
SET status = $1, executor_name = $2, attempt = attempt + 1, lease_owner = $3, lease_until = $4,
    last_error = '', updated_at = $5, completed_at = NULL
WHERE id = $6 AND status = $7
RETURNING id, draft_id, kind, status, idempotency_key, executor_name, attempt,
          lease_owner, lease_until, external_reference, last_error, created_at, updated_at, completed_at`,
		office.OperationExecuting, executorName, leaseOwner, normalizeTime(leaseUntil), normalizeTime(now), id, current.Status))
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("领取办公执行任务失败：%w", err)
	}
	updatedDraft, err := scanOfficeDraft(tx.QueryRow(ctx, `
UPDATE office_drafts SET status = $1, updated_at = $2 WHERE id = $3 AND status = $4
RETURNING id, kind, status, conversation_id, source_run_id, title, payload::text,
          content_hash, created_at, updated_at`, office.StatusExecuting, normalizeTime(now), draft.ID, expectedDraftStatus))
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("更新执行中草稿状态失败：%w", err)
	}
	operationEvent.OperationID, operationEvent.FromStatus = id, current.Status
	operationEvent.ToStatus, operationEvent.Attempt = office.OperationExecuting, updated.Attempt
	if err := insertPostgresOperationEvent(ctx, tx, operationEvent); err != nil {
		return office.Operation{}, office.Draft{}, err
	}
	draftEvent.DraftID, draftEvent.FromStatus = draft.ID, expectedDraftStatus
	draftEvent.ToStatus = office.StatusExecuting
	if err := insertPostgresDraftEvent(ctx, tx, draftEvent); err != nil {
		return office.Operation{}, office.Draft{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("提交领取办公执行任务事务失败：%w", err)
	}
	return updated, updatedDraft, nil
}

// CheckpointOperation 原子保存远端对象 ID；只有当前租约持有者可以写入。
func (p *Postgres) CheckpointOperation(ctx context.Context, id, leaseOwner, externalReference string, now time.Time, operationEvent office.OperationEvent) (office.Operation, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return office.Operation{}, fmt.Errorf("开始保存办公执行检查点事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	updated, err := scanOfficeOperation(tx.QueryRow(ctx, `
UPDATE office_operations SET external_reference = $1, updated_at = $2
WHERE id = $3 AND status = $4 AND lease_owner = $5
RETURNING id, draft_id, kind, status, idempotency_key, executor_name, attempt,
          lease_owner, lease_until, external_reference, last_error, created_at, updated_at, completed_at`,
		externalReference, normalizeTime(now), id, office.OperationExecuting, leaseOwner))
	if errors.Is(err, pgx.ErrNoRows) {
		return office.Operation{}, fmt.Errorf("%w：办公执行任务租约已失效，不能保存检查点", office.ErrStateConflict)
	}
	if err != nil {
		return office.Operation{}, fmt.Errorf("保存办公执行检查点失败：%w", err)
	}
	operationEvent.OperationID = id
	operationEvent.FromStatus, operationEvent.ToStatus = office.OperationExecuting, office.OperationExecuting
	operationEvent.Attempt = updated.Attempt
	if err := insertPostgresOperationEvent(ctx, tx, operationEvent); err != nil {
		return office.Operation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return office.Operation{}, fmt.Errorf("提交办公执行检查点事务失败：%w", err)
	}
	return updated, nil
}

func (p *Postgres) FinishOperation(ctx context.Context, id, leaseOwner, nextStatus, externalReference, lastError string, now time.Time, operationEvent office.OperationEvent, draftEvent office.DraftEvent) (office.Operation, office.Draft, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("开始完成办公执行任务事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	current, err := scanOfficeOperation(tx.QueryRow(ctx, officeOperationSelect+` WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return office.Operation{}, office.Draft{}, store.ErrNotFound
	}
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("查询待完成办公执行任务失败：%w", err)
	}
	if current.Status != office.OperationExecuting || current.LeaseOwner != leaseOwner {
		return office.Operation{}, office.Draft{}, fmt.Errorf("%w：执行任务租约已失效或状态已改变", office.ErrStateConflict)
	}
	var completedAt *time.Time
	if nextStatus == office.OperationCompleted {
		value := normalizeTime(now)
		completedAt = &value
	}
	updated, err := scanOfficeOperation(tx.QueryRow(ctx, `
UPDATE office_operations
SET status = $1, lease_owner = '', lease_until = NULL, external_reference = $2, last_error = $3,
    updated_at = $4, completed_at = $5
WHERE id = $6 AND status = $7 AND lease_owner = $8
RETURNING id, draft_id, kind, status, idempotency_key, executor_name, attempt,
          lease_owner, lease_until, external_reference, last_error, created_at, updated_at, completed_at`,
		nextStatus, externalReference, lastError, normalizeTime(now), completedAt,
		id, office.OperationExecuting, leaseOwner))
	if errors.Is(err, pgx.ErrNoRows) {
		return office.Operation{}, office.Draft{}, fmt.Errorf("%w：办公执行任务租约已被回收", office.ErrStateConflict)
	}
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("完成办公执行任务失败：%w", err)
	}
	draftStatus := office.StatusFailed
	if nextStatus == office.OperationCompleted {
		draftStatus = office.StatusCompleted
	}
	updatedDraft, err := scanOfficeDraft(tx.QueryRow(ctx, `
UPDATE office_drafts SET status = $1, updated_at = $2 WHERE id = $3 AND status = $4
RETURNING id, kind, status, conversation_id, source_run_id, title, payload::text,
          content_hash, created_at, updated_at`, draftStatus, normalizeTime(now), current.DraftID, office.StatusExecuting))
	if errors.Is(err, pgx.ErrNoRows) {
		return office.Operation{}, office.Draft{}, fmt.Errorf("%w：办公草稿已不在 executing 状态", office.ErrStateConflict)
	}
	if err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("完成办公草稿执行状态失败：%w", err)
	}
	operationEvent.OperationID = id
	operationEvent.FromStatus, operationEvent.ToStatus = office.OperationExecuting, nextStatus
	operationEvent.Attempt = current.Attempt
	if err := insertPostgresOperationEvent(ctx, tx, operationEvent); err != nil {
		return office.Operation{}, office.Draft{}, err
	}
	draftEvent.DraftID = current.DraftID
	draftEvent.FromStatus, draftEvent.ToStatus = office.StatusExecuting, draftStatus
	if err := insertPostgresDraftEvent(ctx, tx, draftEvent); err != nil {
		return office.Operation{}, office.Draft{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return office.Operation{}, office.Draft{}, fmt.Errorf("提交完成办公执行任务事务失败：%w", err)
	}
	return updated, updatedDraft, nil
}

func (p *Postgres) ListOperationEvents(ctx context.Context, operationID string) ([]office.OperationEvent, error) {
	rows, err := p.pool.Query(ctx, `
SELECT id, operation_id, from_status, to_status, attempt, actor, reason, created_at
FROM office_operation_events WHERE operation_id = $1 ORDER BY created_at, id`, operationID)
	if err != nil {
		return nil, fmt.Errorf("查询办公执行任务审计事件失败：%w", err)
	}
	defer rows.Close()
	items := make([]office.OperationEvent, 0)
	for rows.Next() {
		var item office.OperationEvent
		if err := rows.Scan(&item.ID, &item.OperationID, &item.FromStatus, &item.ToStatus,
			&item.Attempt, &item.Actor, &item.Reason, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("读取办公执行任务审计事件失败：%w", err)
		}
		item.CreatedAt = normalizeTime(item.CreatedAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (p *Postgres) ListExpiredOperations(ctx context.Context, now time.Time, limit int) ([]office.Operation, error) {
	rows, err := p.pool.Query(ctx, officeOperationSelect+`
WHERE status = $1 AND lease_until IS NOT NULL AND lease_until <= $2
ORDER BY lease_until LIMIT $3`, office.OperationExecuting, normalizeTime(now), limit)
	if err != nil {
		return nil, fmt.Errorf("查询租约过期办公执行任务失败：%w", err)
	}
	defer rows.Close()
	return scanOfficeOperations(rows)
}

func insertPostgresOperationEvent(ctx context.Context, tx pgx.Tx, event office.OperationEvent) error {
	_, err := tx.Exec(ctx, `
INSERT INTO office_operation_events(id, operation_id, from_status, to_status, attempt, actor, reason, created_at)
VALUES($1, $2, $3, $4, $5, $6, $7, $8)`, event.ID, event.OperationID, event.FromStatus,
		event.ToStatus, event.Attempt, event.Actor, event.Reason, normalizeTime(event.CreatedAt))
	if err != nil {
		return fmt.Errorf("保存办公执行任务审计事件失败：%w", err)
	}
	return nil
}

func insertPostgresDraftEvent(ctx context.Context, tx pgx.Tx, event office.DraftEvent) error {
	_, err := tx.Exec(ctx, `
INSERT INTO office_draft_events(id, draft_id, from_status, to_status, actor, reason, created_at)
VALUES($1, $2, $3, $4, $5, $6, $7)`, event.ID, event.DraftID, event.FromStatus,
		event.ToStatus, event.Actor, event.Reason, normalizeTime(event.CreatedAt))
	if err != nil {
		return fmt.Errorf("保存办公草稿审计事件失败：%w", err)
	}
	return nil
}

const officeOperationSelect = `
SELECT id, draft_id, kind, status, idempotency_key, executor_name, attempt,
       lease_owner, lease_until, external_reference, last_error, created_at, updated_at, completed_at
FROM office_operations`

type officeOperationRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanOfficeOperation(scanner officeDraftScanner) (office.Operation, error) {
	var item office.Operation
	if err := scanner.Scan(
		&item.ID, &item.DraftID, &item.Kind, &item.Status, &item.IdempotencyKey,
		&item.ExecutorName, &item.Attempt, &item.LeaseOwner, &item.LeaseUntil,
		&item.ExternalReference, &item.LastError, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt,
	); err != nil {
		return office.Operation{}, err
	}
	item.CreatedAt = normalizeTime(item.CreatedAt)
	item.UpdatedAt = normalizeTime(item.UpdatedAt)
	if item.LeaseUntil != nil {
		value := normalizeTime(*item.LeaseUntil)
		item.LeaseUntil = &value
	}
	if item.CompletedAt != nil {
		value := normalizeTime(*item.CompletedAt)
		item.CompletedAt = &value
	}
	return item, nil
}

func scanOfficeOperations(rows officeOperationRows) ([]office.Operation, error) {
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
