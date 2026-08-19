package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhiruo/zora/internal/background"
)

const postgresBackgroundJobColumns = `id,kind,dedupe_key,run_id,conversation_id,status,attempt,max_attempts,available_at,lease_owner,lease_until,last_error,payload,result,trace_parent,created_at,updated_at,completed_at`

func (p *Postgres) EnqueueBackgroundJob(ctx context.Context, job background.Job) (background.Job, bool, error) {
	tag, err := p.pool.Exec(ctx, `INSERT INTO background_jobs(`+postgresBackgroundJobColumns+`) VALUES($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) ON CONFLICT(kind,dedupe_key) DO NOTHING`, job.ID, job.Kind, job.DedupeKey, job.RunID, job.ConversationID, job.Status, job.Attempt, job.MaxAttempts, normalizeTime(job.AvailableAt), job.LeaseOwner, normalizeOptionalTime(job.LeaseUntil), job.LastError, string(job.Payload), `{}`, job.TraceParent, normalizeTime(job.CreatedAt), normalizeTime(job.UpdatedAt), normalizeOptionalTime(job.CompletedAt))
	if err != nil {
		return background.Job{}, false, fmt.Errorf("创建后台任务失败：%w", err)
	}
	stored, err := scanPostgresBackgroundJob(p.pool.QueryRow(ctx, `SELECT `+postgresBackgroundJobColumns+` FROM background_jobs WHERE kind=$1 AND dedupe_key=$2`, job.Kind, job.DedupeKey))
	return stored, tag.RowsAffected() == 1, err
}
func (p *Postgres) GetBackgroundJob(ctx context.Context, id string) (background.Job, error) {
	job, err := scanPostgresBackgroundJob(p.pool.QueryRow(ctx, `SELECT `+postgresBackgroundJobColumns+` FROM background_jobs WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return background.Job{}, background.ErrNotFound
	}
	return job, err
}
func (p *Postgres) ListBackgroundJobs(ctx context.Context, filter background.Filter) ([]background.Job, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+postgresBackgroundJobColumns+` FROM background_jobs WHERE ($1='' OR kind=$1) AND ($2='' OR status=$2) ORDER BY created_at DESC,id DESC LIMIT $3`, filter.Kind, filter.Status, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]background.Job, 0)
	for rows.Next() {
		item, err := scanPostgresBackgroundJob(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (p *Postgres) ClaimBackgroundJob(ctx context.Context, kind, workerID string, now, leaseUntil time.Time) (background.Job, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return background.Job{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE background_jobs SET status='failed',lease_owner='',lease_until=NULL,last_error='最后一次尝试因进程退出或租约超时而中断',updated_at=$1,completed_at=$1 WHERE kind=$2 AND status='executing' AND lease_until<=$1 AND attempt>=max_attempts`, now, kind)
	if err != nil {
		return background.Job{}, err
	}
	job, err := scanPostgresBackgroundJob(tx.QueryRow(ctx, `SELECT `+postgresBackgroundJobColumns+` FROM background_jobs WHERE kind=$1 AND ((status='pending' AND available_at<=$2) OR (status='executing' AND lease_until<=$2 AND attempt<max_attempts)) ORDER BY available_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, kind, now))
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return background.Job{}, err
		}
		return background.Job{}, background.ErrNotFound
	}
	if err != nil {
		return background.Job{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE background_jobs SET status='executing',attempt=attempt+1,lease_owner=$1,lease_until=$2,updated_at=$3 WHERE id=$4`, workerID, leaseUntil, now, job.ID)
	if err != nil || tag.RowsAffected() != 1 {
		return background.Job{}, background.ErrLeaseLost
	}
	job, err = scanPostgresBackgroundJob(tx.QueryRow(ctx, `SELECT `+postgresBackgroundJobColumns+` FROM background_jobs WHERE id=$1`, job.ID))
	if err != nil {
		return background.Job{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return background.Job{}, err
	}
	return job, nil
}
func (p *Postgres) CompleteBackgroundJob(ctx context.Context, id, leaseOwner string, result json.RawMessage, now time.Time) (background.Job, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE background_jobs SET status='completed',lease_owner='',lease_until=NULL,last_error='',payload='{}'::jsonb,result=$1,updated_at=$2,completed_at=$2 WHERE id=$3 AND status='executing' AND lease_owner=$4`, string(result), now, id, leaseOwner)
	if err != nil {
		return background.Job{}, err
	}
	if tag.RowsAffected() != 1 {
		return background.Job{}, background.ErrLeaseLost
	}
	return p.GetBackgroundJob(ctx, id)
}
func (p *Postgres) FailBackgroundJob(ctx context.Context, id, leaseOwner, lastError string, retryAt, now time.Time, terminal bool) (background.Job, error) {
	status := background.StatusPending
	var completedAt *time.Time
	if terminal {
		status = background.StatusFailed
		completedAt = &now
	}
	tag, err := p.pool.Exec(ctx, `UPDATE background_jobs SET status=$1,available_at=$2,lease_owner='',lease_until=NULL,last_error=$3,updated_at=$4,completed_at=$5 WHERE id=$6 AND status='executing' AND lease_owner=$7`, status, retryAt, lastError, now, completedAt, id, leaseOwner)
	if err != nil {
		return background.Job{}, err
	}
	if tag.RowsAffected() != 1 {
		return background.Job{}, background.ErrLeaseLost
	}
	return p.GetBackgroundJob(ctx, id)
}
func (p *Postgres) RetryBackgroundJob(ctx context.Context, id string, now time.Time) (background.Job, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE background_jobs SET status='pending',attempt=0,available_at=$1,lease_owner='',lease_until=NULL,last_error='',updated_at=$1,completed_at=NULL WHERE id=$2 AND status='failed'`, now, id)
	if err != nil {
		return background.Job{}, fmt.Errorf("重试后台任务失败：%w", err)
	}
	if tag.RowsAffected() != 1 {
		return background.Job{}, fmt.Errorf("只有 failed 后台任务可以重试")
	}
	return p.GetBackgroundJob(ctx, id)
}
func scanPostgresBackgroundJob(scanner pgx.Row) (background.Job, error) {
	var job background.Job
	var runID, conversationID *string
	var payload, result []byte
	if err := scanner.Scan(&job.ID, &job.Kind, &job.DedupeKey, &runID, &conversationID, &job.Status, &job.Attempt, &job.MaxAttempts, &job.AvailableAt, &job.LeaseOwner, &job.LeaseUntil, &job.LastError, &payload, &result, &job.TraceParent, &job.CreatedAt, &job.UpdatedAt, &job.CompletedAt); err != nil {
		return background.Job{}, err
	}
	if runID != nil {
		job.RunID = *runID
	}
	if conversationID != nil {
		job.ConversationID = *conversationID
	}
	job.Payload = json.RawMessage(payload)
	if string(result) != "{}" {
		job.Result = json.RawMessage(result)
	}
	job.AvailableAt = normalizeTime(job.AvailableAt)
	job.CreatedAt = normalizeTime(job.CreatedAt)
	job.UpdatedAt = normalizeTime(job.UpdatedAt)
	job.LeaseUntil = normalizeOptionalTime(job.LeaseUntil)
	job.CompletedAt = normalizeOptionalTime(job.CompletedAt)
	return job, nil
}
