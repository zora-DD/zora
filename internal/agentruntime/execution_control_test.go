package agentruntime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type controlledTestTool struct {
	name string
	run  func(context.Context) (string, error)
}

func (t controlledTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "测试专业 Agent"}, nil
}

func (t controlledTestTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	return t.run(ctx)
}

func TestControlledAgentToolLimitsHandoffs(t *testing.T) {
	base := controlledTestTool{name: "test_agent", run: func(context.Context) (string, error) { return "ok", nil }}
	wrappedBase, err := newControlledAgentTool(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := wrappedBase.(tool.InvokableTool)
	ctx := withExecutionState(context.Background(), ExecutionLimits{MaxHandoffs: 1, MaxParallel: 1, SpecialistTimeout: time.Second})
	if _, err := wrapped.InvokableRun(ctx, `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.InvokableRun(ctx, `{}`); err == nil || !strings.Contains(err.Error(), "交接次数已达到上限") {
		t.Fatalf("unexpected budget error: %v", err)
	}
}

func TestControlledAgentToolRetriesTransientFailure(t *testing.T) {
	var calls atomic.Int32
	base := controlledTestTool{name: "retry_agent", run: func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			return "", errors.New("临时失败")
		}
		return "ok", nil
	}}
	wrappedBase, err := newControlledAgentTool(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	ctx := withExecutionState(context.Background(), ExecutionLimits{MaxHandoffs: 1, MaxParallel: 1, SpecialistTimeout: time.Second, RetryCount: 1})
	result, err := wrappedBase.(tool.InvokableTool).InvokableRun(ctx, `{}`)
	if err != nil || result != "ok" || calls.Load() != 2 {
		t.Fatalf("result=%q calls=%d err=%v", result, calls.Load(), err)
	}
}

func TestControlledAgentToolsCanRunInParallel(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	makeTool := func(name string) tool.InvokableTool {
		base := controlledTestTool{name: name, run: func(context.Context) (string, error) {
			entered <- name
			<-release
			return name, nil
		}}
		wrapped, err := newControlledAgentTool(context.Background(), base)
		if err != nil {
			t.Fatal(err)
		}
		return wrapped.(tool.InvokableTool)
	}
	first, second := makeTool("first_agent"), makeTool("second_agent")
	ctx := withExecutionState(context.Background(), ExecutionLimits{MaxHandoffs: 2, MaxParallel: 2, SpecialistTimeout: time.Second})
	done := make(chan error, 2)
	go func() { _, err := first.InvokableRun(ctx, `{}`); done <- err }()
	go func() { _, err := second.InvokableRun(ctx, `{}`); done <- err }()
	for index := 0; index < 2; index++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("两个独立专业 Agent 未并行进入执行阶段")
		}
	}
	close(release)
	for index := 0; index < 2; index++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestControlledAgentToolHonorsSpecialistTimeout(t *testing.T) {
	base := controlledTestTool{name: "slow_agent", run: func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	wrapped, err := newControlledAgentTool(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	ctx := withExecutionState(context.Background(), ExecutionLimits{MaxHandoffs: 1, MaxParallel: 1, SpecialistTimeout: 20 * time.Millisecond})
	_, err = wrapped.(tool.InvokableTool).InvokableRun(ctx, `{}`)
	if err == nil || !strings.Contains(err.Error(), "执行失败") {
		t.Fatalf("unexpected timeout error: %v", err)
	}
}
