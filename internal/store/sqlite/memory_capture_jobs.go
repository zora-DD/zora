package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/memory"
)

const captureJobColumns = `
id, run_id, conversation_id, user_message_id, assistant_message_id,
status, attempt, max_attempts, available_at, lease_owner, lease_until,
last_error, result, trace_parent, created_at, updated_at, completed_at`

// EnqueueCaptureJob 在同一事务内保存助手消息与 Outbox 任务。
// run_id 唯一约束使客户端重试不会产生第二个捕获任务。
func (s *SQLite) EnqueueCaptureJob(ctx context.Context, assistant domain.Message, job memory.CaptureJob) (domain.Message, memory.CaptureJob, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("开始提交长期记忆任务事务失败：%w", err)
	}
	defer tx.Rollback()

	existing, err := scanCaptureJob(tx.QueryRowContext(ctx, `SELECT `+captureJobColumns+` FROM memory_capture_jobs WHERE run_id = ?`, job.RunID))
	if err == nil {
		message, messageErr := scanMessage(tx.QueryRowContext(ctx, `
SELECT id, conversation_id, role, content, tool_name, tool_call_id, sequence, created_at
FROM messages WHERE id = ?`, existing.AssistantMessageID))
		if messageErr != nil {
			return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("读取已入队助手消息失败：%w", messageErr)
		}
		return message, existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("检查长期记忆任务幂等键失败：%w", err)
	}

	result, err := tx.ExecContext(ctx, `
INSERT INTO messages(id, conversation_id, role, content, tool_name, tool_call_id, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?)`, assistant.ID, assistant.ConversationID, assistant.Role,
		assistant.Content, assistant.ToolName, assistant.ToolCallID, formatTime(assistant.CreatedAt))
	if err != nil {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("保存助手消息失败：%w", err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("获取助手消息顺序号失败：%w", err)
	}
	assistant.Sequence = sequence
	job.AssistantMessageID = assistant.ID
	if _, err = tx.ExecContext(ctx, `UPDATE conversations SET updated_at = ? WHERE id = ?`,
		formatTime(assistant.CreatedAt), assistant.ConversationID); err != nil {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("更新对话时间失败：%w", err)
	}
	if _, err = tx.ExecContext(ctx, `
INSERT INTO memory_capture_jobs(
    id, run_id, conversation_id, user_message_id, assistant_message_id,
    status, attempt, max_attempts, available_at, lease_owner, lease_until,
    last_error, result, trace_parent, created_at, updated_at, completed_at
) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.RunID, job.ConversationID, job.UserMessageID, job.AssistantMessageID,
		job.Status, job.Attempt, job.MaxAttempts, formatTime(job.AvailableAt), job.LeaseOwner,
		optionalTimeString(job.LeaseUntil), job.LastError, `{}`, job.TraceParent,
		formatTime(job.CreatedAt), formatTime(job.UpdatedAt), optionalTimeString(job.CompletedAt),
	); err != nil {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("创建长期记忆捕获任务失败：%w", err)
	}
	if err = tx.Commit(); err != nil {
		return domain.Message{}, memory.CaptureJob{}, false, fmt.Errorf("提交长期记忆任务事务失败：%w", err)
	}
	return assistant, job, true, nil
}

func (s *SQLite) GetCaptureJob(ctx context.Context, id string) (memory.CaptureJob, error) {
	job, err := scanCaptureJob(s.db.QueryRowContext(ctx, `SELECT `+captureJobColumns+` FROM memory_capture_jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.CaptureJob{}, memory.ErrJobNotFound
	}
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("查询长期记忆捕获任务失败：%w", err)
	}
	return job, nil
}

func (s *SQLite) ListCaptureJobs(ctx context.Context, filter memory.CaptureJobFilter) ([]memory.CaptureJob, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT `+captureJobColumns+` FROM memory_capture_jobs
WHERE (? = '' OR status = ?)
ORDER BY created_at DESC, id DESC LIMIT ?`, filter.Status, filter.Status, filter.Limit)
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
	return items, rows.Err()
}

func (s *SQLite) ClaimCaptureJob(ctx context.Context, workerID string, now, leaseUntil time.Time) (memory.CaptureJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("开始领取长期记忆捕获任务事务失败：%w", err)
	}
	defer tx.Rollback()
	nowText := formatTime(now)
	// 最后一次尝试期间进程退出时不再重复执行，直接落为终态失败，避免任务永久卡在 executing。
	if _, err = tx.ExecContext(ctx, `
UPDATE memory_capture_jobs
SET status = 'failed', lease_owner = '', lease_until = NULL,
    last_error = '最后一次尝试因进程退出或租约超时而中断', updated_at = ?, completed_at = ?
WHERE status = 'executing' AND lease_until <= ? AND attempt >= max_attempts`, nowText, nowText, nowText); err != nil {
		return memory.CaptureJob{}, fmt.Errorf("恢复过期长期记忆任务失败：%w", err)
	}
	job, err := scanCaptureJob(tx.QueryRowContext(ctx, `
SELECT `+captureJobColumns+` FROM memory_capture_jobs
WHERE (status = 'pending' AND available_at <= ?)
   OR (status = 'executing' AND lease_until <= ? AND attempt < max_attempts)
	ORDER BY available_at, created_at, id LIMIT 1`, nowText, nowText))
	if errors.Is(err, sql.ErrNoRows) {
		// 即使当前没有可领取任务，也要提交上面的过期终态恢复。
		if commitErr := tx.Commit(); commitErr != nil {
			return memory.CaptureJob{}, fmt.Errorf("提交过期长期记忆任务恢复失败：%w", commitErr)
		}
		return memory.CaptureJob{}, memory.ErrJobNotFound
	}
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("选择长期记忆捕获任务失败：%w", err)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE memory_capture_jobs
SET status = 'executing', attempt = attempt + 1, lease_owner = ?, lease_until = ?, updated_at = ?
WHERE id = ? AND ((status = 'pending' AND available_at <= ?)
 OR (status = 'executing' AND lease_until <= ? AND attempt < max_attempts))`,
		workerID, formatTime(leaseUntil), nowText, job.ID, nowText, nowText)
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("领取长期记忆捕获任务失败：%w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return memory.CaptureJob{}, memory.ErrJobLeaseLost
	}
	job, err = scanCaptureJob(tx.QueryRowContext(ctx, `SELECT `+captureJobColumns+` FROM memory_capture_jobs WHERE id = ?`, job.ID))
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("读取已领取长期记忆捕获任务失败：%w", err)
	}
	if err = tx.Commit(); err != nil {
		return memory.CaptureJob{}, fmt.Errorf("提交领取长期记忆捕获任务事务失败：%w", err)
	}
	return job, nil
}

func (s *SQLite) CompleteCaptureJob(ctx context.Context, id, leaseOwner string, captureResult memory.CaptureResult, now time.Time) (memory.CaptureJob, error) {
	encoded, err := json.Marshal(captureResult)
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("编码长期记忆捕获结果失败：%w", err)
	}
	nowText := formatTime(now)
	result, err := s.db.ExecContext(ctx, `
UPDATE memory_capture_jobs
SET status = 'completed', lease_owner = '', lease_until = NULL, last_error = '',
    result = ?, updated_at = ?, completed_at = ?
WHERE id = ? AND status = 'executing' AND lease_owner = ?`, string(encoded), nowText, nowText, id, leaseOwner)
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("完成长期记忆捕获任务失败：%w", err)
	}
	if count, countErr := result.RowsAffected(); countErr != nil || count != 1 {
		return memory.CaptureJob{}, memory.ErrJobLeaseLost
	}
	return s.GetCaptureJob(ctx, id)
}

func (s *SQLite) FailCaptureJob(ctx context.Context, id, leaseOwner, lastError string, retryAt, now time.Time, terminal bool) (memory.CaptureJob, error) {
	status := memory.JobPending
	var completedAt any
	if terminal {
		status = memory.JobFailed
		completedAt = formatTime(now)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE memory_capture_jobs
SET status = ?, available_at = ?, lease_owner = '', lease_until = NULL,
    last_error = ?, updated_at = ?, completed_at = ?
WHERE id = ? AND status = 'executing' AND lease_owner = ?`,
		status, formatTime(retryAt), lastError, formatTime(now), completedAt, id, leaseOwner)
	if err != nil {
		return memory.CaptureJob{}, fmt.Errorf("更新长期记忆捕获任务失败状态失败：%w", err)
	}
	if count, countErr := result.RowsAffected(); countErr != nil || count != 1 {
		return memory.CaptureJob{}, memory.ErrJobLeaseLost
	}
	return s.GetCaptureJob(ctx, id)
}

func scanCaptureJob(scanner rowScanner) (memory.CaptureJob, error) {
	var job memory.CaptureJob
	var availableAt, createdAt, updatedAt string
	var leaseUntil, completedAt sql.NullString
	var encodedResult string
	if err := scanner.Scan(
		&job.ID, &job.RunID, &job.ConversationID, &job.UserMessageID, &job.AssistantMessageID,
		&job.Status, &job.Attempt, &job.MaxAttempts, &availableAt, &job.LeaseOwner, &leaseUntil,
		&job.LastError, &encodedResult, &job.TraceParent, &createdAt, &updatedAt, &completedAt,
	); err != nil {
		return memory.CaptureJob{}, err
	}
	var err error
	if job.AvailableAt, err = parseTime(availableAt); err != nil {
		return memory.CaptureJob{}, err
	}
	if job.CreatedAt, err = parseTime(createdAt); err != nil {
		return memory.CaptureJob{}, err
	}
	if job.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return memory.CaptureJob{}, err
	}
	if leaseUntil.Valid {
		parsed, parseErr := parseTime(leaseUntil.String)
		if parseErr != nil {
			return memory.CaptureJob{}, parseErr
		}
		job.LeaseUntil = &parsed
	}
	if completedAt.Valid {
		parsed, parseErr := parseTime(completedAt.String)
		if parseErr != nil {
			return memory.CaptureJob{}, parseErr
		}
		job.CompletedAt = &parsed
	}
	if job.Status == memory.JobCompleted {
		var captureResult memory.CaptureResult
		if err := json.Unmarshal([]byte(encodedResult), &captureResult); err != nil {
			return memory.CaptureJob{}, fmt.Errorf("解析长期记忆捕获结果失败：%w", err)
		}
		job.Result = &captureResult
	}
	return job, nil
}
