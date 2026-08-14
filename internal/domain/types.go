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
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	ToolName       string    `json:"tool_name,omitempty"`
	ToolCallID     string    `json:"tool_call_id,omitempty"`
	Sequence       int64     `json:"sequence"`
	CreatedAt      time.Time `json:"created_at"`
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
