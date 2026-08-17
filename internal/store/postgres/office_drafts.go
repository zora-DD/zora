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
	result, err := p.pool.Exec(ctx, `
INSERT INTO office_drafts(
    id, kind, status, conversation_id, source_run_id, title, payload,
    content_hash, created_at, updated_at
) VALUES($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10)
ON CONFLICT(source_run_id, content_hash) DO NOTHING`,
		draft.ID, draft.Kind, draft.Status, draft.ConversationID, draft.SourceRunID,
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
	row := p.pool.QueryRow(ctx, officeDraftSelect+` WHERE id = $1`, id)
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
	query := officeDraftSelect + ` WHERE 1=1`
	args := make([]any, 0, 3)
	position := 1
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
	result, err := p.pool.Exec(ctx, `DELETE FROM office_drafts WHERE id = $1 AND status = 'draft'`, id)
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

func (p *Postgres) getDraftByRunHash(ctx context.Context, runID, contentHash string) (office.Draft, error) {
	row := p.pool.QueryRow(ctx, officeDraftSelect+` WHERE source_run_id = $1 AND content_hash = $2`, runID, contentHash)
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
