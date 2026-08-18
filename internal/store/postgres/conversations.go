package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/store"
)

func (p *Postgres) CreateConversation(ctx context.Context, conversation domain.Conversation) error {
	_, err := p.pool.Exec(ctx, `
INSERT INTO conversations(id, title, created_at, updated_at) VALUES($1, $2, $3, $4)`,
		conversation.ID, conversation.Title, normalizeTime(conversation.CreatedAt), normalizeTime(conversation.UpdatedAt))
	if err != nil {
		return fmt.Errorf("创建对话失败：%w", err)
	}
	return nil
}

func (p *Postgres) GetConversation(ctx context.Context, id string) (domain.Conversation, error) {
	var conversation domain.Conversation
	err := p.pool.QueryRow(ctx, `
SELECT c.id, c.title, c.created_at, c.updated_at, COUNT(m.id)
FROM conversations c
LEFT JOIN messages m ON m.conversation_id = c.id
WHERE c.id = $1
GROUP BY c.id`, id).Scan(
		&conversation.ID, &conversation.Title, &conversation.CreatedAt,
		&conversation.UpdatedAt, &conversation.MessageCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Conversation{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Conversation{}, fmt.Errorf("查询对话失败：%w", err)
	}
	conversation.CreatedAt = normalizeTime(conversation.CreatedAt)
	conversation.UpdatedAt = normalizeTime(conversation.UpdatedAt)
	return conversation, nil
}

func (p *Postgres) ListConversations(ctx context.Context, limit int) ([]domain.Conversation, error) {
	rows, err := p.pool.Query(ctx, `
SELECT c.id, c.title, c.created_at, c.updated_at, COUNT(m.id)
FROM conversations c
LEFT JOIN messages m ON m.conversation_id = c.id
GROUP BY c.id
ORDER BY c.updated_at DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询对话列表失败：%w", err)
	}
	defer rows.Close()

	conversations := make([]domain.Conversation, 0)
	for rows.Next() {
		var conversation domain.Conversation
		if err := rows.Scan(
			&conversation.ID, &conversation.Title, &conversation.CreatedAt,
			&conversation.UpdatedAt, &conversation.MessageCount,
		); err != nil {
			return nil, fmt.Errorf("读取对话数据失败：%w", err)
		}
		conversation.CreatedAt = normalizeTime(conversation.CreatedAt)
		conversation.UpdatedAt = normalizeTime(conversation.UpdatedAt)
		conversations = append(conversations, conversation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历对话数据失败：%w", err)
	}
	return conversations, nil
}

func (p *Postgres) RenameConversation(ctx context.Context, id, title string) error {
	tag, err := p.pool.Exec(ctx,
		`UPDATE conversations SET title = $1, updated_at = $2 WHERE id = $3`,
		title, time.Now().UTC(), id)
	return affected("重命名对话", tag, err)
}

func (p *Postgres) DeleteConversation(ctx context.Context, id string) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM conversations WHERE id = $1`, id)
	return affected("删除对话", tag, err)
}

func (p *Postgres) AddMessage(ctx context.Context, message domain.Message) (domain.Message, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Message{}, fmt.Errorf("开始保存消息事务失败：%w", err)
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
INSERT INTO messages(id, conversation_id, role, content, tool_name, tool_call_id, created_at)
VALUES($1, $2, $3, $4, $5, $6, $7)
RETURNING sequence`,
		message.ID, message.ConversationID, message.Role, message.Content,
		message.ToolName, message.ToolCallID, normalizeTime(message.CreatedAt),
	).Scan(&message.Sequence)
	if err != nil {
		return domain.Message{}, fmt.Errorf("保存消息失败：%w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE conversations SET updated_at = $1 WHERE id = $2`,
		normalizeTime(message.CreatedAt), message.ConversationID); err != nil {
		return domain.Message{}, fmt.Errorf("更新对话时间失败：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Message{}, fmt.Errorf("提交消息事务失败：%w", err)
	}
	return message, nil
}

func (p *Postgres) ListMessages(ctx context.Context, conversationID string, limit int) ([]domain.Message, error) {
	rows, err := p.pool.Query(ctx, `
SELECT id, conversation_id, role, content, tool_name, tool_call_id, sequence, created_at
FROM (
    SELECT id, conversation_id, role, content, tool_name, tool_call_id, sequence, created_at
    FROM messages WHERE conversation_id = $1
    ORDER BY sequence DESC LIMIT $2
) recent
ORDER BY sequence ASC`, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询消息列表失败：%w", err)
	}
	defer rows.Close()

	messages := make([]domain.Message, 0)
	for rows.Next() {
		var message domain.Message
		if err := rows.Scan(
			&message.ID, &message.ConversationID, &message.Role, &message.Content,
			&message.ToolName, &message.ToolCallID, &message.Sequence, &message.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("读取消息数据失败：%w", err)
		}
		message.CreatedAt = normalizeTime(message.CreatedAt)
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历消息数据失败：%w", err)
	}
	return messages, nil
}

func (p *Postgres) CreateRun(ctx context.Context, run domain.AgentRun) error {
	_, err := p.pool.Exec(ctx, `
INSERT INTO agent_runs(id, conversation_id, user_message_id, status, model, started_at)
VALUES($1, $2, $3, $4, $5, $6)`,
		run.ID, run.ConversationID, run.UserMessageID, run.Status, run.Model, normalizeTime(run.StartedAt))
	if err != nil {
		return fmt.Errorf("创建执行记录失败：%w", err)
	}
	return nil
}

func (p *Postgres) GetRun(ctx context.Context, id string) (domain.AgentRun, error) {
	run, err := scanAgentRun(p.pool.QueryRow(ctx, `
SELECT id, conversation_id, user_message_id, COALESCE(assistant_message_id, ''),
       status, model, error, started_at, completed_at
FROM agent_runs WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AgentRun{}, store.ErrNotFound
	}
	if err != nil {
		return domain.AgentRun{}, fmt.Errorf("查询执行记录失败：%w", err)
	}
	return run, nil
}

func (p *Postgres) ListRuns(ctx context.Context, limit int) ([]domain.AgentRun, error) {
	rows, err := p.pool.Query(ctx, `
SELECT id, conversation_id, user_message_id, COALESCE(assistant_message_id, ''),
       status, model, error, started_at, completed_at
FROM agent_runs ORDER BY started_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询执行记录列表失败：%w", err)
	}
	defer rows.Close()
	runs := make([]domain.AgentRun, 0)
	for rows.Next() {
		run, scanErr := scanAgentRun(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("读取执行记录失败：%w", scanErr)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历执行记录失败：%w", err)
	}
	return runs, nil
}

func scanAgentRun(scanner rowScanner) (domain.AgentRun, error) {
	var run domain.AgentRun
	if err := scanner.Scan(
		&run.ID, &run.ConversationID, &run.UserMessageID, &run.AssistantMessageID,
		&run.Status, &run.Model, &run.Error, &run.StartedAt, &run.CompletedAt,
	); err != nil {
		return domain.AgentRun{}, err
	}
	run.StartedAt = normalizeTime(run.StartedAt)
	if run.CompletedAt != nil {
		completedAt := normalizeTime(*run.CompletedAt)
		run.CompletedAt = &completedAt
	}
	return run, nil
}

func (p *Postgres) FinishRun(ctx context.Context, id, status, assistantMessageID, errorMessage string, completedAt time.Time) error {
	tag, err := p.pool.Exec(ctx, `
UPDATE agent_runs
SET status = $1, assistant_message_id = NULLIF($2, ''), error = $3, completed_at = $4
WHERE id = $5`, status, assistantMessageID, errorMessage, normalizeTime(completedAt), id)
	return affected("完成执行记录", tag, err)
}

func (p *Postgres) CreateAgentTaskRun(ctx context.Context, run domain.AgentTaskRun) error {
	_, err := p.pool.Exec(ctx, `
INSERT INTO agent_task_runs(
    id, parent_run_id, agent_name, tool_call_id, task, status, attempt, started_at
) VALUES($1, $2, $3, $4, $5, $6, $7, $8)`,
		run.ID, run.ParentRunID, run.AgentName, run.ToolCallID, run.Task,
		run.Status, run.Attempt, normalizeTime(run.StartedAt))
	if err != nil {
		return fmt.Errorf("创建专业 Agent 执行记录失败：%w", err)
	}
	return nil
}

func (p *Postgres) FinishAgentTaskRun(ctx context.Context, id, status, outputPreview, errorMessage string, completedAt time.Time) error {
	tag, err := p.pool.Exec(ctx, `
UPDATE agent_task_runs
SET status = $1, output_preview = $2, error = $3, completed_at = $4
WHERE id = $5`, status, outputPreview, errorMessage, normalizeTime(completedAt), id)
	return affected("完成专业 Agent 执行记录", tag, err)
}

func (p *Postgres) ListAgentTaskRuns(ctx context.Context, parentRunID string) ([]domain.AgentTaskRun, error) {
	rows, err := p.pool.Query(ctx, `
SELECT id, parent_run_id, agent_name, tool_call_id, task, status, attempt,
       output_preview, error, started_at, completed_at
FROM agent_task_runs WHERE parent_run_id = $1 ORDER BY started_at ASC, id ASC`, parentRunID)
	if err != nil {
		return nil, fmt.Errorf("查询专业 Agent 执行记录失败：%w", err)
	}
	defer rows.Close()
	runs := make([]domain.AgentTaskRun, 0)
	for rows.Next() {
		var run domain.AgentTaskRun
		if err := rows.Scan(
			&run.ID, &run.ParentRunID, &run.AgentName, &run.ToolCallID, &run.Task,
			&run.Status, &run.Attempt, &run.OutputPreview, &run.Error, &run.StartedAt, &run.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("读取专业 Agent 执行记录失败：%w", err)
		}
		run.StartedAt = normalizeTime(run.StartedAt)
		if run.CompletedAt != nil {
			completedAt := normalizeTime(*run.CompletedAt)
			run.CompletedAt = &completedAt
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历专业 Agent 执行记录失败：%w", err)
	}
	return runs, nil
}

func (p *Postgres) AppendRunEvent(ctx context.Context, event domain.RunEvent) (domain.RunEvent, error) {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("编码执行事件失败：%w", err)
	}
	err = p.pool.QueryRow(ctx, `
INSERT INTO run_events(id, run_id, type, agent_name, tool_name, payload, created_at)
VALUES($1, $2, $3, $4, $5, $6::jsonb, $7)
RETURNING sequence`,
		event.ID, event.RunID, event.Type, event.AgentName, event.ToolName,
		string(payload), normalizeTime(event.CreatedAt),
	).Scan(&event.Sequence)
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("保存执行事件失败：%w", err)
	}
	return event, nil
}

func (p *Postgres) ListRunEvents(ctx context.Context, runID string) ([]domain.RunEvent, error) {
	rows, err := p.pool.Query(ctx, `
SELECT id, run_id, type, agent_name, tool_name, payload, sequence, created_at
FROM run_events WHERE run_id = $1 ORDER BY sequence ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("查询执行事件失败：%w", err)
	}
	defer rows.Close()

	events := make([]domain.RunEvent, 0)
	for rows.Next() {
		var event domain.RunEvent
		var payload []byte
		if err := rows.Scan(
			&event.ID, &event.RunID, &event.Type, &event.AgentName, &event.ToolName,
			&payload, &event.Sequence, &event.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("读取执行事件失败：%w", err)
		}
		if err := json.Unmarshal(payload, &event.Payload); err != nil {
			return nil, fmt.Errorf("解析执行事件失败：%w", err)
		}
		event.CreatedAt = normalizeTime(event.CreatedAt)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历执行事件失败：%w", err)
	}
	return events, nil
}
