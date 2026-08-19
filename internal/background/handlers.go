package background

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/summary"
)

func KnowledgeHandler(service *knowledge.Service) Handler {
	return func(ctx context.Context, payload json.RawMessage) (any, error) {
		var input KnowledgePayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return nil, fmt.Errorf("解析文档摄取任务失败：%w", err)
		}
		return service.Ingest(ctx, input.Input)
	}
}
func SummaryHandler(service *summary.Service) Handler {
	return func(ctx context.Context, payload json.RawMessage) (any, error) {
		var input SummaryPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return nil, fmt.Errorf("解析会话摘要任务失败：%w", err)
		}
		return service.Update(ctx, input.ConversationID, input.LatestSequence)
	}
}
