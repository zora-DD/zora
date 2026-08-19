package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/zhiruo/zora/internal/office"
	"github.com/zhiruo/zora/internal/store"
)

func (p *Postgres) SaveDraft(ctx context.Context, draft office.Draft) (office.Draft, bool, error) {
	scope := requestScope(ctx)
	result, err := p.pool.Exec(ctx, `
INSERT INTO office_drafts(
    id, tenant_id, principal_id, kind, status, conversation_id, source_run_id, title, payload,
    content_hash, created_at, updated_at
) VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12)
ON CONFLICT(source_run_id, content_hash) DO NOTHING`,
		draft.ID, scope.TenantID, scope.ID, draft.Kind, draft.Status, draft.ConversationID, draft.SourceRunID,
		draft.Title, string(draft.Payload), draft.ContentHash,
		normalizeTime(draft.CreatedAt), normalizeTime(draft.UpdatedAt))
	if err != nil {
		return office.Draft{}, false, fmt.Errorf("保存办公草稿失败：%w", err)
	}
	if result.RowsAffected() == 0 {
		existing, getErr := p.getDraftByRunHash(ctx, draft.SourceRunID, draft.ContentHash)
		return existing, false, getErr
	}
	saved, err := p.GetDraft(ctx, draft.ID)
	return saved, true, err
}

func (p *Postgres) GetDraft(ctx context.Context, id string) (office.Draft, error) {
	scope := requestScope(ctx)
	row := p.pool.QueryRow(ctx, officeDraftSelect+` WHERE id = $1 AND tenant_id=$2 AND principal_id=$3`, id, scope.TenantID, scope.ID)
	item, err := scanOfficeDraft(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return office.Draft{}, store.ErrNotFound
	}
	if err != nil {
		return office.Draft{}, fmt.Errorf("查询办公草稿失败：%w", err)
	}
	return item, nil
}

func (p *Postgres) ListDrafts(ctx context.Context, filter office.ListFilter) ([]office.Draft, error) {
	scope := requestScope(ctx)
	query := officeDraftSelect + ` WHERE tenant_id=$1 AND principal_id=$2`
	args := []any{scope.TenantID, scope.ID}
	position := 3
	if filter.Kind != "" {
		query += fmt.Sprintf(" AND kind = $%d", position)
		args = append(args, filter.Kind)
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
		return nil, fmt.Errorf("查询办公草稿列表失败：%w", err)
	}
	defer rows.Close()
	items := make([]office.Draft, 0)
	for rows.Next() {
		item, scanErr := scanOfficeDraft(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("读取办公草稿失败：%w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历办公草稿失败：%w", err)
	}
	return items, nil
}

func (p *Postgres) DeleteDraft(ctx context.Context, id string) error {
	scope := requestScope(ctx)
	result, err := p.pool.Exec(ctx, `DELETE FROM office_drafts WHERE id = $1 AND tenant_id=$2 AND principal_id=$3 AND status = 'draft'`, id, scope.TenantID, scope.ID)
	if err != nil {
		return fmt.Errorf("删除办公草稿失败：%w", err)
	}
	if result.RowsAffected() == 0 {
		if _, getErr := p.GetDraft(ctx, id); getErr != nil {
			return getErr
		}
		return fmt.Errorf("只有 draft 状态的草稿可以删除")
	}
	return nil
}

// TransitionDraft 通过 compare-and-swap 更新状态，并在同一事务写入不可变审计事件。
func (p *Postgres) TransitionDraft(ctx context.Context, id, expectedStatus, nextStatus string, event office.DraftEvent) (office.Draft, error) {
	scope := requestScope(ctx)
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return office.Draft{}, fmt.Errorf("开始办公草稿状态事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	updated, err := scanOfficeDraft(tx.QueryRow(ctx, `
UPDATE office_drafts SET status = $1, updated_at = $2
WHERE id = $3 AND tenant_id=$4 AND principal_id=$5 AND status = $6
RETURNING id, kind, status, conversation_id, source_run_id, title, payload::text,
          content_hash, created_at, updated_at`, nextStatus, normalizeTime(event.CreatedAt), id, scope.TenantID, scope.ID, expectedStatus))
	if errors.Is(err, pgx.ErrNoRows) {
		current, getErr := scanOfficeDraft(tx.QueryRow(ctx, officeDraftSelect+` WHERE id = $1 AND tenant_id=$2 AND principal_id=$3`, id, scope.TenantID, scope.ID))
		if errors.Is(getErr, pgx.ErrNoRows) {
			return office.Draft{}, store.ErrNotFound
		}
		if getErr != nil {
			return office.Draft{}, fmt.Errorf("查询办公草稿当前状态失败：%w", getErr)
		}
		return office.Draft{}, fmt.Errorf("%w：当前为 %s，不能按 %s 处理", office.ErrStateConflict, current.Status, expectedStatus)
	}
	if err != nil {
		return office.Draft{}, fmt.Errorf("更新办公草稿状态失败：%w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO office_draft_events(id, draft_id, from_status, to_status, actor, reason, created_at)
VALUES($1, $2, $3, $4, $5, $6, $7)`, event.ID, id, expectedStatus, nextStatus,
		event.Actor, event.Reason, normalizeTime(event.CreatedAt)); err != nil {
		return office.Draft{}, fmt.Errorf("保存办公草稿审计事件失败：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return office.Draft{}, fmt.Errorf("提交办公草稿状态事务失败：%w", err)
	}
	return updated, nil
}

func (p *Postgres) ListDraftEvents(ctx context.Context, draftID string) ([]office.DraftEvent, error) {
	scope := requestScope(ctx)
	rows, err := p.pool.Query(ctx, `
SELECT id, draft_id, from_status, to_status, actor, reason, created_at
FROM office_draft_events e JOIN office_drafts d ON d.id=e.draft_id
WHERE e.draft_id = $1 AND d.tenant_id=$2 AND d.principal_id=$3
ORDER BY e.created_at, e.id`, draftID, scope.TenantID, scope.ID)
	if err != nil {
		return nil, fmt.Errorf("查询办公草稿审计事件失败：%w", err)
	}
	defer rows.Close()
	items := make([]office.DraftEvent, 0)
	for rows.Next() {
		var item office.DraftEvent
		if err := rows.Scan(&item.ID, &item.DraftID, &item.FromStatus, &item.ToStatus,
			&item.Actor, &item.Reason, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("读取办公草稿审计事件失败：%w", err)
		}
		item.CreatedAt = normalizeTime(item.CreatedAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历办公草稿审计事件失败：%w", err)
	}
	return items, nil
}

func (p *Postgres) getDraftByRunHash(ctx context.Context, runID, contentHash string) (office.Draft, error) {
	scope := requestScope(ctx)
	row := p.pool.QueryRow(ctx, officeDraftSelect+` WHERE source_run_id = $1 AND content_hash = $2 AND tenant_id=$3 AND principal_id=$4`, runID, contentHash, scope.TenantID, scope.ID)
	item, err := scanOfficeDraft(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return office.Draft{}, store.ErrNotFound
	}
	if err != nil {
		return office.Draft{}, fmt.Errorf("查询幂等办公草稿失败：%w", err)
	}
	return item, nil
}

const officeDraftSelect = `
SELECT id, kind, status, conversation_id, source_run_id, title, payload::text,
       content_hash, created_at, updated_at
FROM office_drafts`

type officeDraftScanner interface {
	Scan(dest ...any) error
}

func scanOfficeDraft(scanner officeDraftScanner) (office.Draft, error) {
	var item office.Draft
	var conversationID, sourceRunID *string
	var payload string
	if err := scanner.Scan(
		&item.ID, &item.Kind, &item.Status, &conversationID, &sourceRunID,
		&item.Title, &payload, &item.ContentHash, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return office.Draft{}, err
	}
	if !json.Valid([]byte(payload)) {
		return office.Draft{}, fmt.Errorf("草稿 %s 的 payload 不是合法 JSON", item.ID)
	}
	item.Payload = json.RawMessage(payload)
	if conversationID != nil {
		item.ConversationID = *conversationID
	}
	if sourceRunID != nil {
		item.SourceRunID = *sourceRunID
	}
	item.CreatedAt = normalizeTime(item.CreatedAt)
	item.UpdatedAt = normalizeTime(item.UpdatedAt)
	return item, nil
}
