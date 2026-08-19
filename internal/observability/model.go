package observability

import (
	"context"
	"errors"
	"io"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type observedChatModel struct {
	inner     model.BaseChatModel
	telemetry *Telemetry
	provider  string
	model     string
}

type observedToolCallingChatModel struct {
	*observedChatModel
	toolCalling model.ToolCallingChatModel
}

// WrapChatModel 在模型真实 Generate/Stream 边界记录耗时和 Provider Usage，
// 不采集 Prompt、回答正文或 Tool 参数内容。
func (t *Telemetry) WrapChatModel(inner model.BaseChatModel, provider, modelName string) model.BaseChatModel {
	if inner == nil || t == nil {
		return inner
	}
	base := &observedChatModel{inner: inner, telemetry: t, provider: provider, model: modelName}
	if toolCalling, ok := inner.(model.ToolCallingChatModel); ok {
		return &observedToolCallingChatModel{observedChatModel: base, toolCalling: toolCalling}
	}
	return base
}

func (m *observedChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	callCtx, span, started := m.telemetry.StartModel(ctx, m.provider, m.model, len(input))
	message, err := m.inner.Generate(callCtx, input, opts...)
	m.telemetry.EndModel(callCtx, span, started, m.provider, m.model, tokenUsage(message), err)
	return message, err
}

func (m *observedChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	callCtx, span, started := m.telemetry.StartModel(ctx, m.provider, m.model, len(input))
	reader, err := m.inner.Stream(callCtx, input, opts...)
	if err != nil {
		m.telemetry.EndModel(callCtx, span, started, m.provider, m.model, ModelTokenUsage{}, err)
		return nil, err
	}
	output, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		defer reader.Close()
		usage := ModelTokenUsage{}
		var streamErr error
		defer func() {
			m.telemetry.EndModel(callCtx, span, started, m.provider, m.model, usage, streamErr)
		}()
		for {
			message, recvErr := reader.Recv()
			if errors.Is(recvErr, io.EOF) {
				return
			}
			if recvErr != nil {
				streamErr = recvErr
				writer.Send(nil, recvErr)
				return
			}
			if current := tokenUsage(message); current.Reported {
				usage = current
			}
			if closed := writer.Send(message, nil); closed {
				streamErr = callCtx.Err()
				return
			}
		}
	}()
	return output, nil
}

func (m *observedToolCallingChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	bound, err := m.toolCalling.WithTools(tools)
	if err != nil {
		return nil, err
	}
	wrapped := m.telemetry.WrapChatModel(bound, m.provider, m.model)
	result, ok := wrapped.(model.ToolCallingChatModel)
	if !ok {
		return nil, errors.New("带工具模型包装后未实现 ToolCallingChatModel")
	}
	return result, nil
}

func tokenUsage(message *schema.Message) ModelTokenUsage {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		return ModelTokenUsage{}
	}
	usage := message.ResponseMeta.Usage
	return ModelTokenUsage{
		Reported: true, PromptTokens: usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens,
		CachedTokens:    usage.PromptTokenDetails.CachedTokens,
		ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
	}
}
