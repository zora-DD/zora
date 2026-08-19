package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/memory"
)

const captureJobColumns = `
id, tenant_id, principal_id, run_id, conversation_id, user_message_id, assistant_message_id,
status, attempt, max_attempts, available_at, lease_owner, lease_until,
last_error, result, trace_parent, created_at, updated_at, completed_at`

func (p *Postgres) EnqueueCaptureJob(ctx context.Context, assistant domain.Message, job memory.CaptureJob) (domain.Message, memory.CaptureJob, bool, error) {
	scope := requestScope(ctx)
	job.TenantID, job.PrincipalID = scope.TenantID, scope.ID
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("开始提交长期记忆任务事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	// 同一 Run 的并发重试先串行化，再检查唯一任务；避免两个事务都在快照中看不到记录。
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, job.RunID); err != nil {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("获取长期记忆任务幂等锁失败：%w", err)
	}

	existing, err := scanCaptureJob(tx.QueryRow(ctx, `SELECT `+captureJobColumns+` FROM memory_capture_jobs WHERE run_id = $1 AND tenant_id = $2 AND principal_id = $3`, job.RunID, scope.TenantID, scope.ID))
	if err == nil {
		message, messageErr := scanMessage(tx.QueryRow(ctx, `
SELECT m.id, m.conversation_id, m.role, m.content, m.tool_name, m.tool_call_id, m.sequence, m.created_at
FROM messages m JOIN conversations c ON c.id=m.conversation_id
WHERE m.id = $1 AND c.tenant_id=$2 AND c.principal_id=$3`, existing.AssistantMessageID, scope.TenantID, scope.ID))
		if messageErr != nil {
			return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("读取已入队助手消息失败：%w", messageErr)
		}
		return message, existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("检查长期记忆任务幂等键失败：%w", err)
	}

	if err = tx.QueryRow(ctx, `
INSERT INTO messages(id, conversation_id, role, content, tool_name, tool_call_id, created_at)
SELECT $1, $2, $3, $4, $5, $6, $7
WHERE EXISTS(SELECT 1 FROM conversations WHERE id=$2 AND tenant_id=$8 AND principal_id=$9)
RETURNING sequence`,
		assistant.ID, assistant.ConversationID, assistant.Role, assistant.Content,
		assistant.ToolName, assistant.ToolCallID, normalizeTime(assistant.CreatedAt), scope.TenantID, scope.ID,
	).Scan(&assistant.Sequence); err != nil {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("保存助手消息失败：%w", err)
	}
	job.AssistantMessageID = assistant.ID
	if _, err = tx.Exec(ctx, `UPDATE conversations SET updated_at = $1 WHERE id = $2 AND tenant_id=$3 AND principal_id=$4`,
		normalizeTime(assistant.CreatedAt), assistant.ConversationID, scope.TenantID, scope.ID); err != nil {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("更新对话时间失败：%w", err)
	}
	if _, err = tx.Exec(ctx, `
INSERT INTO memory_capture_jobs(
    id, tenant_id, principal_id, run_id, conversation_id, user_message_id, assistant_message_id,
    status, attempt, max_attempts, available_at, lease_owner, lease_until,
    last_error, result, trace_parent, created_at, updated_at, completed_at
) VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb, $16, $17, $18, $19)`,
		job.ID, scope.TenantID, scope.ID, job.RunID, job.ConversationID, job.UserMessageID, job.AssistantMessageID,
		job.Status, job.Attempt, job.MaxAttempts, normalizeTime(job.AvailableAt), job.LeaseOwner,
		normalizeOptionalTime(job.LeaseUntil), job.LastError, `{}`, job.TraceParent,
		normalizeTime(job.CreatedAt), normalizeTime(job.UpdatedAt), normalizeOptionalTime(job.CompletedAt),
	); err != nil {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("创建长期记忆捕获任务失败：%w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("提交长期记忆任务事务失败：%w", err)
	}
	return assistant, job, true, nil
}

func (p *Postgres) GetCaptureJob(ctx context.Context, id string) (memory.CaptureJob, error) {
	scope := requestScope(ctx)
	job, err := scanCaptureJob(p.pool.QueryRow(ctx, `SELECT `+captureJobColumns+` FROM memory_capture_jobs WHERE id = $1 AND tenant_id=$2 AND principal_id=$3`, id, scope.TenantID, scope.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.CaptureJob{}, memory.ErrJobNotFound
	}
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("查询长期记忆捕获任务失败：%w", err)
	}
	return job, nil
}

func (p *Postgres) ListCaptureJobs(ctx context.Context, filter memory.CaptureJobFilter) ([]memory.CaptureJob, error) {
	scope := requestScope(ctx)
	rows, err := p.pool.Query(ctx, `
SELECT `+captureJobColumns+` FROM memory_capture_jobs
WHERE tenant_id=$1 AND principal_id=$2 AND ($3 = '' OR status = $3)
ORDER BY created_at DESC, id DESC LIMIT $4`, scope.TenantID, scope.ID, filter.Status, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("查询长期记忆捕获任务列表失败：%w", err)
	}
	defer rows.Close()
	items := make([]memory.CaptureJob, 0)
	for rows.Next() {
		job, scanErr := scanCaptureJob(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("读取长期记忆捕获任务失败：%w", scanErr)
		}
		items = append(items, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历长期记忆捕获任务失败：%w", err)
	}
	return items, nil
}

func (p *Postgres) ClaimCaptureJob(ctx context.Context, workerID string, now, leaseUntil time.Time) (memory.CaptureJob, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("开始领取长期记忆捕获任务事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
UPDATE memory_capture_jobs
SET status = 'failed', lease_owner = '', lease_until = NULL,
    last_error = '最后一次尝试因进程退出或租约超时而中断', updated_at = $1, completed_at = $1
WHERE status = 'executing' AND lease_until <= $1 AND attempt >= max_attempts`, normalizeTime(now)); err != nil {
		return memory.CaptureJob{}, fmt.Errorf("恢复过期长期记忆任务失败：%w", err)
	}

	var jobID string
	err = tx.QueryRow(ctx, `
SELECT id FROM memory_capture_jobs
WHERE (status = 'pending' AND available_at <= $1)
   OR (status = 'executing' AND lease_until <= $1 AND attempt < max_attempts)
ORDER BY available_at, created_at, id
	FOR UPDATE SKIP LOCKED LIMIT 1`, normalizeTime(now)).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		// 即使当前没有可领取任务，也要提交上面的过期终态恢复。
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return memory.CaptureJob{}, fmt.Errorf("提交过期长期记忆任务恢复失败：%w", commitErr)
		}
		return memory.CaptureJob{}, memory.ErrJobNotFound
	}
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("选择长期记忆捕获任务失败：%w", err)
	}
	job, err := scanCaptureJob(tx.QueryRow(ctx, `
UPDATE memory_capture_jobs
SET status = 'executing', attempt = attempt + 1, lease_owner = $1, lease_until = $2, updated_at = $3
WHERE id = $4 RETURNING `+captureJobColumns,
		workerID, normalizeTime(leaseUntil), normalizeTime(now), jobID))
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("领取长期记忆捕获任务失败：%w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return memory.CaptureJob{}, fmt.Errorf("提交领取长期记忆捕获任务事务失败：%w", err)
	}
	return job, nil
}

func (p *Postgres) CompleteCaptureJob(ctx context.Context, id, leaseOwner string, captureResult memory.CaptureResult, now time.Time) (memory.CaptureJob, error) {
	encoded, err := json.Marshal(captureResult)
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("编码长期记忆捕获结果失败：%w", err)
	}
	job, err := scanCaptureJob(p.pool.QueryRow(ctx, `
UPDATE memory_capture_jobs
SET status = 'completed', lease_owner = '', lease_until = NULL, last_error = '',
    result = $1::jsonb, updated_at = $2, completed_at = $2
WHERE id = $3 AND status = 'executing' AND lease_owner = $4
RETURNING `+captureJobColumns, string(encoded), normalizeTime(now), id, leaseOwner))
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.CaptureJob{}, memory.ErrJobLeaseLost
	}
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("完成长期记忆捕获任务失败：%w", err)
	}
	return job, nil
}

func (p *Postgres) FailCaptureJob(ctx context.Context, id, leaseOwner, lastError string, retryAt, now time.Time, terminal bool) (memory.CaptureJob, error) {
	status := memory.JobPending
	var completedAt *time.Time
	if terminal {
		status = memory.JobFailed
		value := normalizeTime(now)
		completedAt = &value
	}
	job, err := scanCaptureJob(p.pool.QueryRow(ctx, `
UPDATE memory_capture_jobs
SET status = $1, available_at = $2, lease_owner = '', lease_until = NULL,
    last_error = $3, updated_at = $4, completed_at = $5
WHERE id = $6 AND status = 'executing' AND lease_owner = $7
RETURNING `+captureJobColumns,
		status, normalizeTime(retryAt), lastError, normalizeTime(now), completedAt, id, leaseOwner))
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.CaptureJob{}, memory.ErrJobLeaseLost
	}
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("更新长期记忆捕获任务失败状态失败：%w", err)
	}
	return job, nil
}

func scanCaptureJob(scanner pgx.Row) (memory.CaptureJob, error) {
	var job memory.CaptureJob
	var encodedResult []byte
	if err := scanner.Scan(
		&job.ID, &job.TenantID, &job.PrincipalID, &job.RunID, &job.ConversationID, &job.UserMessageID, &job.AssistantMessageID,
		&job.Status, &job.Attempt, &job.MaxAttempts, &job.AvailableAt, &job.LeaseOwner, &job.LeaseUntil,
		&job.LastError, &encodedResult, &job.TraceParent, &job.CreatedAt, &job.UpdatedAt, &job.CompletedAt,
	); err != nil {
		return memory.CaptureJob{}, err
	}
	job.AvailableAt = normalizeTime(job.AvailableAt)
	job.CreatedAt = normalizeTime(job.CreatedAt)
	job.UpdatedAt = normalizeTime(job.UpdatedAt)
	job.LeaseUntil = normalizeOptionalTime(job.LeaseUntil)
	job.CompletedAt = normalizeOptionalTime(job.CompletedAt)
	if job.Status == memory.JobCompleted {
		var result memory.CaptureResult
		if err := json.Unmarshal(encodedResult, &result); err != nil {
			return memory.CaptureJob{}, fmt.Errorf("解析长期记忆捕获结果失败：%w", err)
		}
		job.Result = &result
	}
	return job, nil
}
