package knowledge

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type searchToolInput struct {
	Query string `json:"query" jsonschema_description:"需在已上传知识库中查找答案的问题或关键词"`
	TopK  int    `json:"top_k,omitempty" jsonschema_description:"返回的证据分块数量，范围 1 到 8"`
}

type searchToolOutput struct {
	EmbeddingModel string         `json:"embedding_model"`
	Results        []SearchResult `json:"results"`
}

// NewSearchTool 把混合检索暴露为 Agent 工具，结果保留文档和 chunk 引用。
func NewSearchTool(service *Service) (tool.BaseTool, error) {
	if service == nil {
		return nil, fmt.Errorf("知识库服务不能为空")
	}
	return utils.InferTool(
		"knowledge_search",
		"检索用户已上传的文档。涉及用户文件、私有知识或上传资料时应使用此工具；最终回答中必须引用 document_name 和 ordinal。",
		func(ctx context.Context, input *searchToolInput) (*searchToolOutput, error) {
			topK := input.TopK
			if topK == 0 {
				topK = 5
			}
			if topK > 8 {
				topK = 8
			}
			results, err := service.Search(ctx, input.Query, topK)
			if err != nil {
				return nil, err
			}
			return &searchToolOutput{EmbeddingModel: service.EmbeddingModel(), Results: results}, nil
		},
	)
}
