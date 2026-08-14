package agentruntime

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"unicode"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// mockModel 是确定性模型，但仍实现 Eino 的工具调用接口。
// 因此本地模式和真实模型会经过同一套 ReAct、ToolNode 与事件链。
type mockModel struct {
	tools   []*schema.ToolInfo
	counter *atomic.Uint64
}

func newMockModel() *mockModel {
	return &mockModel{counter: &atomic.Uint64{}}
}

func (m *mockModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	clone := &mockModel{tools: append([]*schema.ToolInfo(nil), tools...), counter: m.counter}
	return clone, nil
}

func (m *mockModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(input) == 0 {
		return schema.AssistantMessage("请先告诉我你想解决的问题。", nil), nil
	}

	last := input[len(input)-1]
	if last.Role == schema.Tool {
		prefix := "工具返回"
		switch last.ToolName {
		case "current_time":
			prefix = "当前时间"
		case "calculator":
			prefix = "计算结果"
		case "project_status":
			prefix = "项目状态"
		}
		return schema.AssistantMessage(prefix+"："+last.Content, nil), nil
	}

	query := last.Content
	lower := strings.ToLower(query)
	// 新版 Eino 会通过调用级 Option 注入工具，不能只读取 WithTools 保存的字段。
	availableTools := model.GetCommonOptions(&model.Options{Tools: m.tools}, opts...).Tools
	switch {
	case containsAny(lower, "几点", "时间", "日期", "date", "time") && hasTool(availableTools, "current_time"):
		return m.toolCall("current_time", `{"timezone":"Asia/Shanghai"}`), nil
	case containsAny(lower, "计算", "算一下", "calculator", "calculate") && hasTool(availableTools, "calculator"):
		expression := extractExpression(query)
		if expression == "" {
			return schema.AssistantMessage("请提供包含数字和 +、-、*、/、括号的算式。", nil), nil
		}
		return m.toolCall("calculator", fmt.Sprintf(`{"expression":%q}`, expression)), nil
	case containsAny(lower, "项目状态", "项目能力", "roadmap", "功能") && hasTool(availableTools, "project_status"):
		return m.toolCall("project_status", `{}`), nil
	default:
		return schema.AssistantMessage(
			"这是 Zora 的本地演示模型。我已经收到：\n\n"+query+
				"\n\n你可以让我查询当前时间、计算算式或介绍项目能力。配置 OpenAI-compatible 模型后，我会处理开放式问题。",
			nil,
		), nil
	}
}

func (m *mockModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	if len(message.ToolCalls) > 0 {
		// 工具调用必须放在首个 chunk，便于 Eino 及时识别并进入 ToolNode。
		return schema.StreamReaderFromArray([]*schema.Message{message}), nil
	}

	parts := chunkRunes(message.Content, 8)
	chunks := make([]*schema.Message, 0, len(parts))
	for i, part := range parts {
		role := schema.RoleType("")
		if i == 0 {
			role = schema.Assistant
		}
		chunks = append(chunks, &schema.Message{Role: role, Content: part})
	}
	return schema.StreamReaderFromArray(chunks), nil
}

func (m *mockModel) toolCall(name, arguments string) *schema.Message {
	id := fmt.Sprintf("mock_call_%d", m.counter.Add(1))
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: arguments,
		},
	}})
}

func hasTool(tools []*schema.ToolInfo, name string) bool {
	for _, info := range tools {
		if info.Name == name {
			return true
		}
	}
	return false
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func extractExpression(value string) string {
	var best, current []rune
	for _, r := range value {
		if unicode.IsDigit(r) || unicode.IsSpace(r) || strings.ContainsRune(".+-*/()", r) {
			current = append(current, r)
			continue
		}
		if countDigits(current) > countDigits(best) {
			best = append(best[:0], current...)
		}
		current = current[:0]
	}
	if countDigits(current) > countDigits(best) {
		best = current
	}
	return strings.TrimSpace(string(best))
}

func countDigits(value []rune) int {
	count := 0
	for _, r := range value {
		if unicode.IsDigit(r) {
			count++
		}
	}
	return count
}

func chunkRunes(value string, size int) []string {
	runes := []rune(value)
	if len(runes) == 0 {
		return []string{""}
	}
	parts := make([]string, 0, (len(runes)+size-1)/size)
	for start := 0; start < len(runes); start += size {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		parts = append(parts, string(runes[start:end]))
	}
	return parts
}
