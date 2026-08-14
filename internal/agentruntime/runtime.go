// Package agentruntime 将 Eino ADK 的执行事件转换为 Zora 自己的领域事件。
package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/zhiruo/zora/internal/config"
)

const AgentName = "zora-assistant"

// Event 与 HTTP/SSE 解耦，未来 CLI、WebSocket 或任务队列可以复用同一 Runtime。
type Event struct {
	Type       string
	AgentName  string
	Content    string
	ToolName   string
	ToolCallID string
	Arguments  string
}

// Runtime 持有配置完成的 Eino ChatModelAgent，并隐藏框架的具体事件结构。
type Runtime struct {
	runner   *adk.Runner
	model    string
	provider string
}

// New 根据配置创建模型，并组装 Eino 的 ReAct Agent。
func New(ctx context.Context, cfg config.Config, tools []tool.BaseTool) (*Runtime, error) {
	var chatModel model.BaseChatModel
	switch cfg.Provider {
	case "mock":
		// Mock 同样实现 Eino BaseChatModel，不会绕过 Agent 和工具调用链。
		chatModel = newMockModel()
	case "openai":
		// Eino 的 OpenAI 适配器支持自定义 BaseURL，因此也可连接通义千问。
		client := &http.Client{Timeout: cfg.RequestTimeout}
		openAIModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
			APIKey:     cfg.APIKey,
			BaseURL:    cfg.BaseURL,
			Model:      cfg.Model,
			HTTPClient: client,
		})
		if err != nil {
			return nil, fmt.Errorf("创建 OpenAI-compatible 模型失败：%w", err)
		}
		chatModel = openAIModel
	default:
		return nil, fmt.Errorf("不支持的模型提供方：%q", cfg.Provider)
	}

	// MaxIterations 是 Agent 的保险丝；工具只从显式 allowlist 注入。
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          AgentName,
		Description:   "可以使用安全只读工具的通用中文助手",
		Instruction:   cfg.Instruction,
		Model:         chatModel,
		MaxIterations: cfg.MaxIterations,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("创建 Eino Agent 失败：%w", err)
	}

	return &Runtime{
		runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true}),
		model:  cfg.Model, provider: cfg.Provider,
	}, nil
}

func (r *Runtime) Model() string    { return r.model }
func (r *Runtime) Provider() string { return r.provider }

// Execute 执行一次完整请求，并把模型增量、工具调用和工具结果实时向上层转发。
func (r *Runtime) Execute(ctx context.Context, history []*schema.Message, emit func(Event) error) (string, error) {
	iterator := r.runner.Run(ctx, history)
	var answer strings.Builder

	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return "", event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		variant := event.Output.MessageOutput
		message, streamed, err := consumeVariant(ctx, variant, event.AgentName, emit)
		if err != nil {
			return "", err
		}
		if message == nil {
			continue
		}

		// 流式事件的角色通常在 variant 上；非流式实现可能只写在 Message 中。
		role := variant.Role
		if role == "" {
			role = message.Role
		}
		switch role {
		case schema.Assistant:
			// Assistant 消息可能是最终文本，也可能只包含一个或多个 ToolCall。
			for _, call := range message.ToolCalls {
				if err := emit(Event{
					Type: "tool_call", AgentName: event.AgentName,
					ToolName: call.Function.Name, ToolCallID: call.ID,
					Arguments: call.Function.Arguments,
				}); err != nil {
					return "", err
				}
			}
			if message.Content != "" {
				if answer.Len() > 0 {
					answer.WriteString("\n")
				}
				answer.WriteString(message.Content)
				if !streamed {
					if err := emit(Event{Type: "delta", AgentName: event.AgentName, Content: message.Content}); err != nil {
						return "", err
					}
				}
			}
		case schema.Tool:
			// 工具结果由 Eino 自动回填给模型，同时作为可观察事件暴露给产品层。
			toolName := variant.ToolName
			if toolName == "" {
				toolName = message.ToolName
			}
			if err := emit(Event{
				Type: "tool_result", AgentName: event.AgentName,
				ToolName: toolName, ToolCallID: message.ToolCallID,
				Content: message.Content,
			}); err != nil {
				return "", err
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(answer.String()) == "" {
		return "", errors.New("Agent 未返回助手回答")
	}
	return answer.String(), nil
}

func consumeVariant(ctx context.Context, variant *adk.MessageVariant, agentName string, emit func(Event) error) (*schema.Message, bool, error) {
	if !variant.IsStreaming {
		return variant.Message, false, nil
	}
	if variant.MessageStream == nil {
		return nil, true, errors.New("流式事件缺少消息流")
	}
	defer variant.MessageStream.Close()

	// ToolCall 的名称和参数可能分散在多个 chunk 中，因此一边发送文本增量，
	// 一边保留所有 chunk，最后交给 Eino 合并成结构完整的 Message。
	chunks := make([]*schema.Message, 0, 8)
	for {
		if err := ctx.Err(); err != nil {
			return nil, true, err
		}
		chunk, err := variant.MessageStream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, true, err
		}
		chunks = append(chunks, chunk)
		if variant.Role == schema.Assistant && chunk.Content != "" {
			if err := emit(Event{Type: "delta", AgentName: agentName, Content: chunk.Content}); err != nil {
				return nil, true, err
			}
		}
	}
	if len(chunks) == 0 {
		return nil, true, nil
	}
	message, err := schema.ConcatMessages(chunks)
	return message, true, err
}
