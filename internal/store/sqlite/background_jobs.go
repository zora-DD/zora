package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zhiruo/zora/internal/background"
)

const backgroundJobColumns = `id,kind,dedupe_key,run_id,conversation_id,status,attempt,max_attempts,available_at,lease_owner,lease_until,last_error,payload,result,trace_parent,created_at,updated_at,completed_at`

func (s *SQLite) EnqueueBackgroundJob(ctx context.Context, job background.Job) (background.Job, bool, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO background_jobs(`+backgroundJobColumns+`) VALUES(?,?,?,NULLIF(?,''),NULLIF(?,''),?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(kind,dedupe_key) DO NOTHING`,
		job.ID, job.Kind, job.DedupeKey, job.RunID, job.ConversationID, job.Status, job.Attempt, job.MaxAttempts, formatTime(job.AvailableAt), job.LeaseOwner, optionalTimeString(job.LeaseUntil), job.LastError, string(job.Payload), `{}`, job.TraceParent, formatTime(job.CreatedAt), formatTime(job.UpdatedAt), optionalTimeString(job.CompletedAt))
	if err != nil {
		return background.Job{}, false, fmt.Errorf("创建后台任务失败：%w", err)
	}
	stored, err := scanBackgroundJob(s.db.QueryRowContext(ctx, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE kind=? AND dedupe_key=?`, job.Kind, job.DedupeKey))
	if err != nil {
		return background.Job{}, false, err
	}
	return stored, stored.ID == job.ID, nil
}
func (s *SQLite) GetBackgroundJob(ctx context.Context, id string) (background.Job, error) {
	job, err := scanBackgroundJob(s.db.QueryRowContext(ctx, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return background.Job{}, background.ErrNotFound
	}
	if err != nil {
		return background.Job{}, fmt.Errorf("查询后台任务失败：%w", err)
	}
	return job, nil
}
func (s *SQLite) ListBackgroundJobs(ctx context.Context, filter background.Filter) ([]background.Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE (?='' OR kind=?) AND (?='' OR status=?) ORDER BY created_at DESC,id DESC LIMIT ?`, filter.Kind, filter.Kind, filter.Status, filter.Status, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("查询后台任务列表失败：%w", err)
	}
	defer rows.Close()
	items := make([]background.Job, 0)
	for rows.Next() {
		item, err := scanBackgroundJob(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *SQLite) ClaimBackgroundJob(ctx context.Context, kind, workerID string, now, leaseUntil time.Time) (background.Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return background.Job{}, err
	}
	defer tx.Rollback()
	nowText := formatTime(now)
	_, err = tx.ExecContext(ctx, `UPDATE background_jobs SET status='failed',lease_owner='',lease_until=NULL,last_error='最后一次尝试因进程退出或租约超时而中断',updated_at=?,completed_at=? WHERE kind=? AND status='executing' AND lease_until<=? AND attempt>=max_attempts`, nowText, nowText, kind, nowText)
	if err != nil {
		return background.Job{}, err
	}
	job, err := scanBackgroundJob(tx.QueryRowContext(ctx, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE kind=? AND ((status='pending' AND available_at<=?) OR (status='executing' AND lease_until<=? AND attempt<max_attempts)) ORDER BY available_at,created_at,id LIMIT 1`, kind, nowText, nowText))
	if errors.Is(err, sql.ErrNoRows) {
		if commitErr := tx.Commit(); commitErr != nil {
			return background.Job{}, commitErr
		}
		return background.Job{}, background.ErrNotFound
	}
	if err != nil {
		return background.Job{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE background_jobs SET status='executing',attempt=attempt+1,lease_owner=?,lease_until=?,updated_at=? WHERE id=? AND ((status='pending' AND available_at<=?) OR (status='executing' AND lease_until<=? AND attempt<max_attempts))`, workerID, formatTime(leaseUntil), nowText, job.ID, nowText, nowText)
	if err != nil {
		return background.Job{}, err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return background.Job{}, background.ErrLeaseLost
	}
	job, err = scanBackgroundJob(tx.QueryRowContext(ctx, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE id=?`, job.ID))
	if err != nil {
		return background.Job{}, err
	}
	if err = tx.Commit(); err != nil {
		return background.Job{}, err
	}
	return job, nil
}
func (s *SQLite) CompleteBackgroundJob(ctx context.Context, id, leaseOwner string, result json.RawMessage, now time.Time) (background.Job, error) {
	nowText := formatTime(now)
	tag, err := s.db.ExecContext(ctx, `UPDATE background_jobs SET status='completed',lease_owner='',lease_until=NULL,last_error='',payload='{}',result=?,updated_at=?,completed_at=? WHERE id=? AND status='executing' AND lease_owner=?`, string(result), nowText, nowText, id, leaseOwner)
	if err != nil {
		return background.Job{}, err
	}
	count, _ := tag.RowsAffected()
	if count != 1 {
		return background.Job{}, background.ErrLeaseLost
	}
	return s.GetBackgroundJob(ctx, id)
}
func (s *SQLite) FailBackgroundJob(ctx context.Context, id, leaseOwner, lastError string, retryAt, now time.Time, terminal bool) (background.Job, error) {
	status := background.StatusPending
	var completed any
	if terminal {
		status = background.StatusFailed
		completed = formatTime(now)
	}
	tag, err := s.db.ExecContext(ctx, `UPDATE background_jobs SET status=?,available_at=?,lease_owner='',lease_until=NULL,last_error=?,updated_at=?,completed_at=? WHERE id=? AND status='executing' AND lease_owner=?`, status, formatTime(retryAt), lastError, formatTime(now), completed, id, leaseOwner)
	if err != nil {
		return background.Job{}, err
	}
	count, _ := tag.RowsAffected()
	if count != 1 {
		return background.Job{}, background.ErrLeaseLost
	}
	return s.GetBackgroundJob(ctx, id)
}
func (s *SQLite) RetryBackgroundJob(ctx context.Context, id string, now time.Time) (background.Job, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE background_jobs SET status='pending',attempt=0,available_at=?,lease_owner='',lease_until=NULL,last_error='',updated_at=?,completed_at=NULL WHERE id=? AND status='failed'`, formatTime(now), formatTime(now), id)
	if err != nil {
		return background.Job{}, fmt.Errorf("重试后台任务失败：%w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return background.Job{}, fmt.Errorf("只有 failed 后台任务可以重试")
	}
	return s.GetBackgroundJob(ctx, id)
}
func scanBackgroundJob(scanner rowScanner) (background.Job, error) {
	var job background.Job
	var runID, conversationID, leaseUntil, completedAt sql.NullString
	var availableAt, createdAt, updatedAt, payload, result string
	if err := scanner.Scan(&job.ID, &job.Kind, &job.DedupeKey, &runID, &conversationID, &job.Status, &job.Attempt, &job.MaxAttempts, &availableAt, &job.LeaseOwner, &leaseUntil, &job.LastError, &payload, &result, &job.TraceParent, &createdAt, &updatedAt, &completedAt); err != nil {
		return background.Job{}, err
	}
	job.RunID = runID.String
	job.ConversationID = conversationID.String
	job.Payload = json.RawMessage(payload)
	if result != "{}" {
		job.Result = json.RawMessage(result)
	}
	var err error
	if job.AvailableAt, err = parseTime(availableAt); err != nil {
		return job, err
	}
	if job.CreatedAt, err = parseTime(createdAt); err != nil {
		return job, err
	}
	if job.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return job, err
	}
	if leaseUntil.Valid {
		value, e := parseTime(leaseUntil.String)
		if e != nil {
			return job, e
		}
		job.LeaseUntil = &value
	}
	if completedAt.Valid {
		value, e := parseTime(completedAt.String)
		if e != nil {
			return job, e
		}
		job.CompletedAt = &value
	}
	return job, nil
}
