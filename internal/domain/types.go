// Package domain contains the application types shared by the runtime,
// persistence, and transport layers.
package domain

import "time"

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

const (
	RunRunning   = "running"
	RunCompleted = "completed"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
	RunRejected  = "rejected"
)

// Conversation is a durable chat thread.
type Conversation struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Message is a user-visible message in a conversation.
type Message struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversation_id"`
	Role           string          `json:"role"`
	Content        string          `json:"content"`
	ToolName       string          `json:"tool_name,omitempty"`
	ToolCallID     string          `json:"tool_call_id,omitempty"`
	Sequence       int64           `json:"sequence"`
	CreatedAt      time.Time       `json:"created_at"`
	Feedback       *AnswerFeedback `json:"feedback,omitempty"`
}

const (
	FeedbackSourceExplicit = "explicit"
	FeedbackSourceImplicit = "implicit"
)

// AnswerFeedback 关联到一条助手消息。显式反馈来自点赞/点踩，隐式反馈来自紧邻的纠错或重复追问。
type AnswerFeedback struct {
	ID             string         `json:"id"`
	ConversationID string         `json:"conversation_id"`
	MessageID      string         `json:"message_id"`
	Source         string         `json:"source"`
	Rating         int            `json:"rating"`
	Reason         string         `json:"reason,omitempty"`
	Signals        map[string]any `json:"signals,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// AgentRun records one user request and its execution outcome.
type AgentRun struct {
	ID                 string     `json:"id"`
	ConversationID     string     `json:"conversation_id"`
	UserMessageID      string     `json:"user_message_id"`
	AssistantMessageID string     `json:"assistant_message_id,omitempty"`
	Status             string     `json:"status"`
	Model              string     `json:"model"`
	Error              string     `json:"error,omitempty"`
	StartedAt          time.Time  `json:"started_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

// AgentTaskRun 记录根 Run 下的一次专业 Agent 交接。
// 它和 RunEvent 分开存储：前者提供当前状态和耗时查询，后者保留不可变事件时间线。
type AgentTaskRun struct {
	ID            string     `json:"id"`
	ParentRunID   string     `json:"parent_run_id"`
	AgentName     string     `json:"agent_name"`
	ToolCallID    string     `json:"tool_call_id"`
	Task          string     `json:"task"`
	Status        string     `json:"status"`
	Attempt       int        `json:"attempt"`
	OutputPreview string     `json:"output_preview,omitempty"`
	Error         string     `json:"error,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// RunEvent is an append-only audit event produced while an agent runs.
type RunEvent struct {
	ID        string         `json:"id"`
	RunID     string         `json:"run_id"`
	Type      string         `json:"type"`
	AgentName string         `json:"agent_name,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	Sequence  int64          `json:"sequence"`
	CreatedAt time.Time      `json:"created_at"`
}
