package observability

import (
	"context"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type observedInvokableTool struct {
	inner     tool.InvokableTool
	telemetry *Telemetry
	name      string
}

func (t *Telemetry) WrapTool(ctx context.Context, base tool.BaseTool) (tool.BaseTool, error) {
	if base == nil || t == nil {
		return base, nil
	}
	invokable, ok := base.(tool.InvokableTool)
	if !ok {
		// 当前 Zora 内置、MCP 和 AgentTool 均为 InvokableTool；未知扩展保持原样，
		// 避免错误改变其 Streamable/Enhanced 接口能力。
		return base, nil
	}
	info, err := base.Info(ctx)
	if err != nil {
		return nil, err
	}
	return &observedInvokableTool{inner: invokable, telemetry: t, name: info.Name}, nil
}

func (t *Telemetry) WrapTools(ctx context.Context, tools []tool.BaseTool) ([]tool.BaseTool, error) {
	result := make([]tool.BaseTool, 0, len(tools))
	for _, base := range tools {
		wrapped, err := t.WrapTool(ctx, base)
		if err != nil {
			return nil, err
		}
		result = append(result, wrapped)
	}
	return result, nil
}

func (t *observedInvokableTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *observedInvokableTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	callCtx, span, started := t.telemetry.StartTool(ctx, t.name, utf8.RuneCountInString(argumentsInJSON))
	result, err := t.inner.InvokableRun(callCtx, argumentsInJSON, opts...)
	t.telemetry.EndTool(callCtx, span, started, t.name, err)
	return result, err
}
