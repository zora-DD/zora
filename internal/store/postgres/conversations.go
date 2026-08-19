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
	scope := requestScope(ctx)
	_, err := p.pool.Exec(ctx, `
INSERT INTO conversations(id, tenant_id, principal_id, title, created_at, updated_at) VALUES($1, $2, $3, $4, $5, $6)`,
		conversation.ID, scope.TenantID, scope.ID, conversation.Title, normalizeTime(conversation.CreatedAt), normalizeTime(conversation.UpdatedAt))
	if err != nil {
		return fmt.Errorf("创建对话失败：%w", err)
	}
	return nil
}

func (p *Postgres) GetConversation(ctx context.Context, id string) (domain.Conversation, error) {
	scope := requestScope(ctx)
	var conversation domain.Conversation
	err := p.pool.QueryRow(ctx, `
SELECT c.id, c.title, c.created_at, c.updated_at, COUNT(m.id)
FROM conversations c
LEFT JOIN messages m ON m.conversation_id = c.id
WHERE c.id = $1 AND c.tenant_id = $2 AND c.principal_id = $3
GROUP BY c.id`, id, scope.TenantID, scope.ID).Scan(
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
	scope := requestScope(ctx)
	rows, err := p.pool.Query(ctx, `
SELECT c.id, c.title, c.created_at, c.updated_at, COUNT(m.id)
FROM conversations c
LEFT JOIN messages m ON m.conversation_id = c.id
WHERE c.tenant_id = $1 AND c.principal_id = $2
GROUP BY c.id
ORDER BY c.updated_at DESC
LIMIT $3`, scope.TenantID, scope.ID, limit)
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
	scope := requestScope(ctx)
	tag, err := p.pool.Exec(ctx,
		`UPDATE conversations SET title = $1, updated_at = $2 WHERE id = $3 AND tenant_id = $4 AND principal_id = $5`,
		title, time.Now().UTC(), id, scope.TenantID, scope.ID)
	return affected("重命名对话", tag, err)
}

func (p *Postgres) DeleteConversation(ctx context.Context, id string) error {
	scope := requestScope(ctx)
	tag, err := p.pool.Exec(ctx, `DELETE FROM conversations WHERE id = $1 AND tenant_id = $2 AND principal_id = $3`, id, scope.TenantID, scope.ID)
	return affected("删除对话", tag, err)
}

func (p *Postgres) AddMessage(ctx context.Context, message domain.Message) (domain.Message, error) {
	scope := requestScope(ctx)
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Message{}, fmt.Errorf("开始保存消息事务失败：%w", err)
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
INSERT INTO messages(id, conversation_id, role, content, tool_name, tool_call_id, created_at)
SELECT $1, $2, $3, $4, $5, $6, $7
WHERE EXISTS(SELECT 1 FROM conversations WHERE id = $2 AND tenant_id = $8 AND principal_id = $9)
RETURNING sequence`,
		message.ID, message.ConversationID, message.Role, message.Content,
		message.ToolName, message.ToolCallID, normalizeTime(message.CreatedAt), scope.TenantID, scope.ID,
	).Scan(&message.Sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Message{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Message{}, fmt.Errorf("保存消息失败：%w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE conversations SET updated_at = $1 WHERE id = $2 AND tenant_id = $3 AND principal_id = $4`,
		normalizeTime(message.CreatedAt), message.ConversationID, scope.TenantID, scope.ID); err != nil {
		return domain.Message{}, fmt.Errorf("更新对话时间失败：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Message{}, fmt.Errorf("提交消息事务失败：%w", err)
	}
	return message, nil
}

func (p *Postgres) GetMessage(ctx context.Context, id string) (domain.Message, error) {
	scope := requestScope(ctx)
	message, err := scanMessage(p.pool.QueryRow(ctx, `
SELECT m.id, m.conversation_id, m.role, m.content, m.tool_name, m.tool_call_id, m.sequence, m.created_at
FROM messages m JOIN conversations c ON c.id = m.conversation_id
WHERE m.id = $1 AND c.tenant_id = $2 AND c.principal_id = $3`, id, scope.TenantID, scope.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Message{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Message{}, fmt.Errorf("查询消息失败：%w", err)
	}
	return message, nil
}

func (p *Postgres) ListMessages(ctx context.Context, conversationID string, limit int) ([]domain.Message, error) {
	scope := requestScope(ctx)
	rows, err := p.pool.Query(ctx, `
SELECT id, conversation_id, role, content, tool_name, tool_call_id, sequence, created_at
FROM (
    SELECT m.id, m.conversation_id, m.role, m.content, m.tool_name, m.tool_call_id, m.sequence, m.created_at
    FROM messages m JOIN conversations c ON c.id = m.conversation_id
    WHERE m.conversation_id = $1 AND c.tenant_id = $2 AND c.principal_id = $3
    ORDER BY sequence DESC LIMIT $4
) recent
ORDER BY sequence ASC`, conversationID, scope.TenantID, scope.ID, limit)
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

func scanMessage(scanner pgx.Row) (domain.Message, error) {
	var message domain.Message
	if err := scanner.Scan(
		&message.ID, &message.ConversationID, &message.Role, &message.Content,
		&message.ToolName, &message.ToolCallID, &message.Sequence, &message.CreatedAt,
	); err != nil {
		return domain.Message{}, err
	}
	message.CreatedAt = normalizeTime(message.CreatedAt)
	return message, nil
}

func (p *Postgres) CreateRun(ctx context.Context, run domain.AgentRun) error {
	scope := requestScope(ctx)
	tag, err := p.pool.Exec(ctx, `
INSERT INTO agent_runs(id, conversation_id, user_message_id, status, model, started_at)
SELECT $1, $2, $3, $4, $5, $6
WHERE EXISTS(SELECT 1 FROM conversations WHERE id = $2 AND tenant_id = $7 AND principal_id = $8)`,
		run.ID, run.ConversationID, run.UserMessageID, run.Status, run.Model, normalizeTime(run.StartedAt), scope.TenantID, scope.ID)
	if err != nil {
		return fmt.Errorf("创建执行记录失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (p *Postgres) GetRun(ctx context.Context, id string) (domain.AgentRun, error) {
	scope := requestScope(ctx)
	run, err := scanAgentRun(p.pool.QueryRow(ctx, `
SELECT r.id, r.conversation_id, r.user_message_id, COALESCE(r.assistant_message_id, ''),
       r.status, r.model, r.error, r.started_at, r.completed_at
FROM agent_runs r JOIN conversations c ON c.id = r.conversation_id
WHERE r.id = $1 AND c.tenant_id = $2 AND c.principal_id = $3`, id, scope.TenantID, scope.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AgentRun{}, store.ErrNotFound
	}
	if err != nil {
		return domain.AgentRun{}, fmt.Errorf("查询执行记录失败：%w", err)
	}
	return run, nil
}

func (p *Postgres) ListRuns(ctx context.Context, limit int) ([]domain.AgentRun, error) {
	scope := requestScope(ctx)
	rows, err := p.pool.Query(ctx, `
SELECT r.id, r.conversation_id, r.user_message_id, COALESCE(r.assistant_message_id, ''),
       r.status, r.model, r.error, r.started_at, r.completed_at
FROM agent_runs r JOIN conversations c ON c.id = r.conversation_id
WHERE c.tenant_id = $1 AND c.principal_id = $2
ORDER BY r.started_at DESC, r.id DESC LIMIT $3`, scope.TenantID, scope.ID, limit)
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
	scope := requestScope(ctx)
	tag, err := p.pool.Exec(ctx, `
UPDATE agent_runs
SET status = $1, assistant_message_id = NULLIF($2, ''), error = $3, completed_at = $4
WHERE id = $5 AND EXISTS(
    SELECT 1 FROM conversations c WHERE c.id = agent_runs.conversation_id AND c.tenant_id = $6 AND c.principal_id = $7
)`, status, assistantMessageID, errorMessage, normalizeTime(completedAt), id, scope.TenantID, scope.ID)
	return affected("完成执行记录", tag, err)
}

func (p *Postgres) CreateAgentTaskRun(ctx context.Context, run domain.AgentTaskRun) error {
	scope := requestScope(ctx)
	tag, err := p.pool.Exec(ctx, `
INSERT INTO agent_task_runs(
    id, parent_run_id, agent_name, tool_call_id, task, status, attempt, started_at
) SELECT $1, $2, $3, $4, $5, $6, $7, $8
WHERE EXISTS(
    SELECT 1 FROM agent_runs r JOIN conversations c ON c.id = r.conversation_id
    WHERE r.id = $2 AND c.tenant_id = $9 AND c.principal_id = $10
)`,
		run.ID, run.ParentRunID, run.AgentName, run.ToolCallID, run.Task,
		run.Status, run.Attempt, normalizeTime(run.StartedAt), scope.TenantID, scope.ID)
	if err != nil {
		return fmt.Errorf("创建专业 Agent 执行记录失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (p *Postgres) FinishAgentTaskRun(ctx context.Context, id, status, outputPreview, errorMessage string, completedAt time.Time) error {
	scope := requestScope(ctx)
	tag, err := p.pool.Exec(ctx, `
UPDATE agent_task_runs
SET status = $1, output_preview = $2, error = $3, completed_at = $4
WHERE id = $5 AND EXISTS(
    SELECT 1 FROM agent_runs r JOIN conversations c ON c.id = r.conversation_id
    WHERE r.id = agent_task_runs.parent_run_id AND c.tenant_id = $6 AND c.principal_id = $7
)`, status, outputPreview, errorMessage, normalizeTime(completedAt), id, scope.TenantID, scope.ID)
	return affected("完成专业 Agent 执行记录", tag, err)
}

func (p *Postgres) ListAgentTaskRuns(ctx context.Context, parentRunID string) ([]domain.AgentTaskRun, error) {
	scope := requestScope(ctx)
	rows, err := p.pool.Query(ctx, `
SELECT t.id, t.parent_run_id, t.agent_name, t.tool_call_id, t.task, t.status, t.attempt,
       t.output_preview, t.error, t.started_at, t.completed_at
FROM agent_task_runs t
JOIN agent_runs r ON r.id = t.parent_run_id
JOIN conversations c ON c.id = r.conversation_id
WHERE t.parent_run_id = $1 AND c.tenant_id = $2 AND c.principal_id = $3
ORDER BY t.started_at ASC, t.id ASC`, parentRunID, scope.TenantID, scope.ID)
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
	scope := requestScope(ctx)
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("编码执行事件失败：%w", err)
	}
	err = p.pool.QueryRow(ctx, `
INSERT INTO run_events(id, run_id, type, agent_name, tool_name, payload, created_at)
SELECT $1, $2, $3, $4, $5, $6::jsonb, $7
WHERE EXISTS(
    SELECT 1 FROM agent_runs r JOIN conversations c ON c.id = r.conversation_id
    WHERE r.id = $2 AND c.tenant_id = $8 AND c.principal_id = $9
)
RETURNING sequence`,
		event.ID, event.RunID, event.Type, event.AgentName, event.ToolName,
		string(payload), normalizeTime(event.CreatedAt), scope.TenantID, scope.ID,
	).Scan(&event.Sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RunEvent{}, store.ErrNotFound
	}
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("保存执行事件失败：%w", err)
	}
	return event, nil
}

func (p *Postgres) ListRunEvents(ctx context.Context, runID string) ([]domain.RunEvent, error) {
	scope := requestScope(ctx)
	rows, err := p.pool.Query(ctx, `
SELECT e.id, e.run_id, e.type, e.agent_name, e.tool_name, e.payload, e.sequence, e.created_at
FROM run_events e
JOIN agent_runs r ON r.id = e.run_id
JOIN conversations c ON c.id = r.conversation_id
WHERE e.run_id = $1 AND c.tenant_id = $2 AND c.principal_id = $3
ORDER BY e.sequence ASC`, runID, scope.TenantID, scope.ID)
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
