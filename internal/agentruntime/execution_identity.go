package agentruntime

import "context"

// ExecutionIdentity 是一次根 Agent 执行的可信身份，由 Chat Service 注入，模型不能自行填写。
// 后续草稿、审批和外部写操作都使用它建立与 Conversation/Run 的审计关联。
type ExecutionIdentity struct {
	ConversationID string
	RunID          string
}

type executionIdentityKey struct{}

// WithExecutionIdentity 只应由已经创建 AgentRun 的应用层调用。
func WithExecutionIdentity(ctx context.Context, conversationID, runID string) context.Context {
	return context.WithValue(ctx, executionIdentityKey{}, ExecutionIdentity{
		ConversationID: conversationID,
		RunID:          runID,
	})
}

// ExecutionIdentityFromContext 返回可信执行身份；普通后台 Context 不会伪造默认值。
func ExecutionIdentityFromContext(ctx context.Context) (ExecutionIdentity, bool) {
	identity, ok := ctx.Value(executionIdentityKey{}).(ExecutionIdentity)
	return identity, ok && identity.ConversationID != "" && identity.RunID != ""
}
