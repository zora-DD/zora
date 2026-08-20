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
	"github.com/zhiruo/zora/internal/inputguard"
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
	// Usage 只承载模型供应商真实返回的统计；未返回时保持 nil，不能用字符数伪装 Token。
	Usage           *ModelUsage
	FinishReason    string
	ReviewVerdict   string
	ReviewIssues    []string
	ReflectionRound int
}

// ModelUsage 是与具体模型 SDK 解耦的单次调用 Token 统计。
type ModelUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	ReasoningTokens  int
}

// Runtime 持有配置完成的 Eino ChatModelAgent，并隐藏框架的具体事件结构。
type Runtime struct {
	runner      *adk.Runner
	model       string
	provider    string
	agentName   string
	specialists map[string]struct{}
	limits      ExecutionLimits
	reviewer    *answerReviewer
	inputGuard  inputguard.Analyzer
}

// NewChatModel 根据配置创建可复用的模型实例。主 Agent 与记忆提取器共享同一模型配置，
// 避免两条链路出现模型名、BaseURL 或超时不一致。
func NewChatModel(ctx context.Context, cfg config.Config) (model.BaseChatModel, error) {
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
			// DeepSeek 等兼容服务会通过扩展字段控制思考模式；配置层禁止在此放入密钥。
			ExtraFields: cfg.ModelExtraFields,
		})
		if err != nil {
			return nil, fmt.Errorf("创建 OpenAI-compatible 模型失败：%w", err)
		}
		chatModel = openAIModel
	default:
		return nil, fmt.Errorf("不支持的模型提供方：%q", cfg.Provider)
	}
	return chatModel, nil
}

// New 根据配置创建模型，并组装 Eino 的 ReAct Agent。
func New(ctx context.Context, cfg config.Config, tools []tool.BaseTool) (*Runtime, error) {
	chatModel, err := NewChatModel(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return NewWithModel(ctx, cfg, tools, chatModel)
}

// NewWithModel 使用已创建的模型组装 Agent，供需要共享模型配置的应用启动流程使用。
func NewWithModel(ctx context.Context, cfg config.Config, tools []tool.BaseTool, chatModel model.BaseChatModel) (*Runtime, error) {
	if chatModel == nil {
		return nil, fmt.Errorf("Chat Model 不能为空")
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

	runtime := newRuntime(ctx, cfg, agent, nil)
	if err := runtime.configureEnhancements(cfg, chatModel); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (r *Runtime) Model() string           { return r.model }
func (r *Runtime) Provider() string        { return r.provider }
func (r *Runtime) AgentName() string       { return r.agentName }
func (r *Runtime) MultiAgentEnabled() bool { return len(r.specialists) > 0 }
func (r *Runtime) ReflectionEnabled() bool { return r.reviewer != nil }
func (r *Runtime) InputGuardEnabled() bool { return r.inputGuard != nil }

func newRuntime(ctx context.Context, cfg config.Config, rootAgent adk.Agent, specialistNames []string) *Runtime {
	specialists := make(map[string]struct{}, len(specialistNames))
	for _, name := range specialistNames {
		specialists[name] = struct{}{}
	}
	return &Runtime{
		runner:      adk.NewRunner(ctx, adk.RunnerConfig{Agent: rootAgent, EnableStreaming: true}),
		model:       cfg.Model,
		provider:    cfg.Provider,
		agentName:   rootAgent.Name(ctx),
		specialists: specialists,
		limits: ExecutionLimits{
			MaxHandoffs: cfg.MultiAgentMaxHandoffs, MaxParallel: cfg.MultiAgentMaxParallel,
			SpecialistTimeout: cfg.MultiAgentSpecialistTimeout, RetryCount: cfg.MultiAgentRetryCount,
		},
	}
}

// Execute 执行一次完整请求，并把模型增量、工具调用和工具结果实时向上层转发。
func (r *Runtime) Execute(ctx context.Context, history []*schema.Message, emit func(Event) error) (string, error) {
	if r.reviewer != nil {
		return r.executeWithReflection(ctx, history, emit)
	}
	return r.executeOnce(ctx, history, emit)
}

func (r *Runtime) executeOnce(ctx context.Context, history []*schema.Message, emit func(Event) error) (string, error) {
	if r.MultiAgentEnabled() {
		ctx = withExecutionState(ctx, r.limits)
	}
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
		rootOutput := event.AgentName == "" || event.AgentName == r.agentName
		message, streamed, err := consumeVariant(ctx, variant, event.AgentName, rootOutput, emit)
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
			// 每条 Assistant 输出对应一次模型调用；即使 Provider 没有返回 Usage，
			// 也记录调用次数并明确 usage 缺失，便于真实模型成本核算。
			modelEvent := Event{Type: "model_call_completed", AgentName: event.AgentName}
			if message.ResponseMeta != nil {
				modelEvent.FinishReason = message.ResponseMeta.FinishReason
				if usage := message.ResponseMeta.Usage; usage != nil {
					modelEvent.Usage = &ModelUsage{
						PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
						TotalTokens: usage.TotalTokens, CachedTokens: usage.PromptTokenDetails.CachedTokens,
						ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
					}
				}
			}
			if err := emit(modelEvent); err != nil {
				return "", err
			}
			// Assistant 消息可能是最终文本，也可能只包含一个或多个 ToolCall。
			for _, call := range message.ToolCalls {
				eventType := "tool_call"
				if r.isSpecialist(call.Function.Name) {
					eventType = "agent_handoff_started"
				}
				if err := emit(Event{
					Type: eventType, AgentName: event.AgentName,
					ToolName: call.Function.Name, ToolCallID: call.ID,
					Arguments: call.Function.Arguments,
				}); err != nil {
					return "", err
				}
			}
			if message.Content != "" && rootOutput {
				if answer.Len() > 0 {
					answer.WriteString("\n")
				}
				answer.WriteString(message.Content)
				if !streamed {
					if err := emit(Event{Type: "delta", AgentName: event.AgentName, Content: message.Content}); err != nil {
						return "", err
					}
				}
			} else if message.Content != "" {
				// 子 Agent 的中间结论只进入协作审计，不拼接进最终回答，
				// 避免用户看到“专家草稿 + Supervisor 定稿”的重复内容。
				if err := emit(Event{Type: "agent_output", AgentName: event.AgentName, Content: message.Content}); err != nil {
					return "", err
				}
			}
		case schema.Tool:
			// 工具结果由 Eino 自动回填给模型，同时作为可观察事件暴露给产品层。
			toolName := variant.ToolName
			if toolName == "" {
				toolName = message.ToolName
			}
			eventType := "tool_result"
			if r.isSpecialist(toolName) {
				eventType = "agent_handoff_completed"
			}
			if err := emit(Event{
				Type: eventType, AgentName: event.AgentName,
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

func (r *Runtime) isSpecialist(name string) bool {
	_, ok := r.specialists[name]
	return ok
}

func consumeVariant(ctx context.Context, variant *adk.MessageVariant, agentName string, emitDeltas bool, emit func(Event) error) (*schema.Message, bool, error) {
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
		if emitDeltas && variant.Role == schema.Assistant && chunk.Content != "" {
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
