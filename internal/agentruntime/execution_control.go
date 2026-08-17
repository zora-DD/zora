package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ExecutionLimits 是多 Agent 单轮执行的保险丝。
// 这些限制按根 Run 隔离，不会让一个慢请求占用其他请求的预算。
type ExecutionLimits struct {
	MaxHandoffs       int
	MaxParallel       int
	SpecialistTimeout time.Duration
	RetryCount        int
}

type executionStateKey struct{}

type executionState struct {
	limits     ExecutionLimits
	parallel   chan struct{}
	mu         sync.Mutex
	handoffNum int
}

func newExecutionState(limits ExecutionLimits) *executionState {
	limits = normalizeExecutionLimits(limits)
	return &executionState{limits: limits, parallel: make(chan struct{}, limits.MaxParallel)}
}

func normalizeExecutionLimits(limits ExecutionLimits) ExecutionLimits {
	if limits.MaxHandoffs <= 0 {
		limits.MaxHandoffs = 6
	}
	if limits.MaxParallel <= 0 {
		limits.MaxParallel = 3
	}
	if limits.SpecialistTimeout <= 0 {
		limits.SpecialistTimeout = 30 * time.Second
	}
	if limits.RetryCount < 0 {
		limits.RetryCount = 0
	}
	return limits
}

func withExecutionState(ctx context.Context, limits ExecutionLimits) context.Context {
	return context.WithValue(ctx, executionStateKey{}, newExecutionState(limits))
}

func executionStateFromContext(ctx context.Context) *executionState {
	state, _ := ctx.Value(executionStateKey{}).(*executionState)
	return state
}

func (s *executionState) reserveHandoff(agentName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handoffNum >= s.limits.MaxHandoffs {
		return fmt.Errorf("多 Agent 交接次数已达到上限 %d，已拒绝继续调用 %s", s.limits.MaxHandoffs, agentName)
	}
	s.handoffNum++
	return nil
}

func (s *executionState) acquire(ctx context.Context) error {
	select {
	case s.parallel <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *executionState) release() { <-s.parallel }

// controlledAgentTool 在 Eino AgentTool 外层统一实现交接预算、并行限流、超时和重试。
// Supervisor 仍然只看到原 AgentTool 的名称、描述和参数 Schema。
type controlledAgentTool struct {
	name     string
	delegate tool.InvokableTool
}

func newControlledAgentTool(ctx context.Context, base tool.BaseTool) (tool.BaseTool, error) {
	delegate, ok := base.(tool.InvokableTool)
	if !ok {
		return nil, fmt.Errorf("专业 AgentTool 不支持同步调用")
	}
	info, err := base.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取专业 AgentTool 信息失败：%w", err)
	}
	return &controlledAgentTool{name: info.Name, delegate: delegate}, nil
}

func (t *controlledAgentTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.delegate.Info(ctx)
}

func (t *controlledAgentTool) InvokableRun(ctx context.Context, arguments string, options ...tool.Option) (string, error) {
	state := executionStateFromContext(ctx)
	if state == nil {
		// 正常 Runtime 一定会注入按 Run 隔离的 state；保留兜底便于独立工具测试。
		state = newExecutionState(ExecutionLimits{})
	}
	if err := state.reserveHandoff(t.name); err != nil {
		return "", err
	}
	if err := state.acquire(ctx); err != nil {
		return "", err
	}
	defer state.release()

	var lastErr error
	for attempt := 0; attempt <= state.limits.RetryCount; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, state.limits.SpecialistTimeout)
		result, err := t.delegate.InvokableRun(attemptCtx, arguments, options...)
		cancel()
		if err == nil {
			return result, nil
		}
		lastErr = err
		// 根请求被取消时立即停止；专业 Agent 自身超时或临时错误可以按配置重试。
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("专业 Agent %s 执行失败（共尝试 %d 次）：%w", t.name, state.limits.RetryCount+1, lastErr)
}
