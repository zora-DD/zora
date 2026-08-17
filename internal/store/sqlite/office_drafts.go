package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/zhiruo/zora/internal/office"
	"github.com/zhiruo/zora/internal/store"
)

func (s *SQLite) SaveDraft(ctx context.Context, draft office.Draft) (office.Draft, bool, error) {
	result, err := s.db.ExecContext(ctx, `
INSERT INTO office_drafts(
    id, kind, status, conversation_id, source_run_id, title, payload,
    content_hash, created_at, updated_at
) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(source_run_id, content_hash) DO NOTHING`,
		draft.ID, draft.Kind, draft.Status, draft.ConversationID, draft.SourceRunID,
		draft.Title, string(draft.Payload), draft.ContentHash,
		formatTime(draft.CreatedAt), formatTime(draft.UpdatedAt))
	if err != nil {
		return office.Draft{}, false, fmt.Errorf("保存办公草稿失败：%w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return office.Draft{}, false, fmt.Errorf("读取办公草稿保存结果失败：%w", err)
	}
	if affected == 0 {
		existing, getErr := s.getDraftByRunHash(ctx, draft.SourceRunID, draft.ContentHash)
		return existing, false, getErr
	}
	saved, err := s.GetDraft(ctx, draft.ID)
	return saved, true, err
}

func (s *SQLite) GetDraft(ctx context.Context, id string) (office.Draft, error) {
	row := s.db.QueryRowContext(ctx, officeDraftSelect+` WHERE id = ?`, id)
	item, err := scanOfficeDraft(row)
	if err == sql.ErrNoRows {
		return office.Draft{}, store.ErrNotFound
	}
	if err != nil {
		return office.Draft{}, fmt.Errorf("查询办公草稿失败：%w", err)
	}
	return item, nil
}

func (s *SQLite) ListDrafts(ctx context.Context, filter office.ListFilter) ([]office.Draft, error) {
	query := officeDraftSelect + ` WHERE 1=1`
	args := make([]any, 0, 3)
	if filter.Kind != "" {
		query += ` AND kind = ?`
		args = append(args, filter.Kind)
	}
	if filter.Status != "" {
		query += ` AND status = ?`
		args = append(args, filter.Status)
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
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

func (s *SQLite) DeleteDraft(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM office_drafts WHERE id = ? AND status = 'draft'`, id)
	if err != nil {
		return fmt.Errorf("删除办公草稿失败：%w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取办公草稿删除结果失败：%w", err)
	}
	if affected == 0 {
		if _, getErr := s.GetDraft(ctx, id); getErr != nil {
			return getErr
		}
		return fmt.Errorf("只有 draft 状态的草稿可以删除")
	}
	return nil
}

func (s *SQLite) getDraftByRunHash(ctx context.Context, runID, contentHash string) (office.Draft, error) {
	row := s.db.QueryRowContext(ctx, officeDraftSelect+` WHERE source_run_id = ? AND content_hash = ?`, runID, contentHash)
	item, err := scanOfficeDraft(row)
	if err == sql.ErrNoRows {
		return office.Draft{}, store.ErrNotFound
	}
	if err != nil {
		return office.Draft{}, fmt.Errorf("查询幂等办公草稿失败：%w", err)
	}
	return item, nil
}

const officeDraftSelect = `
SELECT id, kind, status, conversation_id, source_run_id, title, payload,
       content_hash, created_at, updated_at
FROM office_drafts`

type officeDraftScanner interface {
	Scan(dest ...any) error
}

func scanOfficeDraft(scanner officeDraftScanner) (office.Draft, error) {
	var item office.Draft
	var conversationID, sourceRunID sql.NullString
	var payload, createdAt, updatedAt string
	if err := scanner.Scan(
		&item.ID, &item.Kind, &item.Status, &conversationID, &sourceRunID,
		&item.Title, &payload, &item.ContentHash, &createdAt, &updatedAt,
	); err != nil {
		return office.Draft{}, err
	}
	if !json.Valid([]byte(payload)) {
		return office.Draft{}, fmt.Errorf("草稿 %s 的 payload 不是合法 JSON", item.ID)
	}
	item.Payload = json.RawMessage(payload)
	item.ConversationID = conversationID.String
	item.SourceRunID = sourceRunID.String
	var err error
	if item.CreatedAt, err = parseTime(createdAt); err != nil {
		return office.Draft{}, err
	}
	if item.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return office.Draft{}, err
	}
	return item, nil
}
