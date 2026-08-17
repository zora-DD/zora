package agentruntime

import (
	"context"
	"encoding/json"
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
	query := latestUserContent(input)
	lower := strings.ToLower(query)
	// 新版 Eino 会通过调用级 Option 注入工具，不能只读取 WithTools 保存的字段。
	availableTools := model.GetCommonOptions(&model.Options{Tools: m.tools}, opts...).Tools
	if last.Role == schema.Tool {
		prefix := "工具返回"
		switch last.ToolName {
		case DocumentAgentName:
			// 复合办公任务先取证，再把原任务和证据交给写作专家。
			if hasWritingIntent(lower) && hasTool(availableTools, WriterAgentName) {
				request := fmt.Sprintf("写作任务：%s\n\n文档专家提供的证据：\n%s", query, last.Content)
				return m.toolCall(WriterAgentName, fmt.Sprintf(`{"request":%q}`, request)), nil
			}
			return schema.AssistantMessage(last.Content, nil), nil
		case ResearchAgentName, WriterAgentName:
			return schema.AssistantMessage(last.Content, nil), nil
		case "current_time":
			prefix = "当前时间"
		case "calculator":
			prefix = "计算结果"
		case "project_status":
			prefix = "项目状态"
		case "knowledge_search":
			return schema.AssistantMessage(formatKnowledgeResult(last.Content), nil), nil
		}
		return schema.AssistantMessage(prefix+"："+last.Content, nil), nil
	}

	memoryFacts := recalledMemoryFacts(input)
	conversationSummary := conversationSummaryFact(input)
	switch {
	case systemHasAgentRole(input, "writer"):
		return schema.AssistantMessage(formatWriterDraft(query), nil), nil
	// “发布日期”等资料字段会包含“日期”。知识库意图必须优先于时间意图，
	// 否则本地 Mock 会错误地把“查文档里的日期”理解成“查询当前日期”。
	case hasDocumentRetrievalIntent(lower) && hasTool(availableTools, "knowledge_search"):
		return m.toolCall("knowledge_search", fmt.Sprintf(`{"query":%q,"top_k":5}`, query)), nil
	case hasConversationSummaryIntent(lower) && conversationSummary != "":
		return schema.AssistantMessage("根据这段对话的历史摘要：\n\n"+conversationSummary, nil), nil
	case hasMemoryQuestionIntent(lower) && len(memoryFacts) > 0:
		return schema.AssistantMessage("根据长期记忆，我找到了这些相关信息：\n\n- "+strings.Join(memoryFacts, "\n- "), nil), nil
	case hasDocumentRetrievalIntent(lower) && hasWritingIntent(lower) && hasTool(availableTools, DocumentAgentName):
		return m.toolCall(DocumentAgentName, fmt.Sprintf(`{"request":%q}`, query)), nil
	case hasWritingIntent(lower) && hasTool(availableTools, WriterAgentName):
		return m.toolCall(WriterAgentName, fmt.Sprintf(`{"request":%q}`, query)), nil
	case hasDocumentRetrievalIntent(lower) && hasTool(availableTools, DocumentAgentName):
		return m.toolCall(DocumentAgentName, fmt.Sprintf(`{"request":%q}`, query)), nil
	case hasResearchIntent(lower) && hasTool(availableTools, ResearchAgentName):
		return m.toolCall(ResearchAgentName, fmt.Sprintf(`{"request":%q}`, query)), nil
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
	case systemHasAgentRole(input, "research"):
		return schema.AssistantMessage("研究专家分析：\n\n"+query+"\n\n当前没有可进一步核验的外部资料，以上结论仅基于任务中提供的信息。", nil), nil
	default:
		return schema.AssistantMessage(
			"这是 Zora 的本地演示模型。我已经收到：\n\n"+query+
				"\n\n你可以让我查询当前时间、计算算式、检索上传的知识文档或介绍项目能力。配置 OpenAI-compatible 模型后，我会处理开放式问题。",
			nil,
		), nil
	}
}

func latestUserContent(input []*schema.Message) string {
	for index := len(input) - 1; index >= 0; index-- {
		if input[index].Role == schema.User {
			return input[index].Content
		}
	}
	return input[len(input)-1].Content
}

func systemHasAgentRole(input []*schema.Message, role string) bool {
	marker := "[ZORA_AGENT_ROLE:" + role + "]"
	for _, message := range input {
		if message.Role == schema.System && strings.Contains(message.Content, marker) {
			return true
		}
	}
	return false
}

func hasDocumentRetrievalIntent(query string) bool {
	return containsAny(query,
		"知识库", "上传的文档", "上传文档", "我上传的", "根据文档", "文档中", "文档里",
		"资料中", "资料里", "根据资料", "手册中", "手册里", "制度中", "制度里", "knowledge")
}

func hasWritingIntent(query string) bool {
	return containsAny(query, "写一", "写份", "起草", "撰写", "润色", "改写", "整理成", "生成邮件", "生成通知", "写邮件", "写通知", "文案")
}

func hasResearchIntent(query string) bool {
	return containsAny(query, "调研", "研究一下", "分析一下", "对比", "比较", "项目状态", "项目能力", "roadmap", "功能", "计算", "算一下", "几点", "当前时间")
}

func formatWriterDraft(task string) string {
	return "写作专家草稿：\n\n" + task + "\n\n以上内容已按任务整理；涉及事实和日期的部分请以提供的证据为准。"
}

func hasConversationSummaryIntent(query string) bool {
	return containsAny(query, "刚才聊", "之前聊", "对话摘要", "总结对话", "我们聊过")
}

func conversationSummaryFact(input []*schema.Message) string {
	for _, message := range input {
		if message.Role != schema.System || !strings.HasPrefix(message.Content, "[ZORA_CONVERSATION_SUMMARY]") {
			continue
		}
		start := strings.Index(message.Content, "{")
		if start < 0 {
			return ""
		}
		var payload struct {
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal([]byte(message.Content[start:]), &payload); err != nil {
			return ""
		}
		return strings.TrimSpace(payload.Summary)
	}
	return ""
}

func hasMemoryQuestionIntent(query string) bool {
	return strings.ContainsAny(query, "?？") || containsAny(query, "记得", "了解我", "关于我", "总结一下我", "我的信息")
}

func recalledMemoryFacts(input []*schema.Message) []string {
	for _, message := range input {
		if message.Role != schema.System || !strings.HasPrefix(message.Content, "[ZORA_RECALLED_MEMORY]") {
			continue
		}
		start := strings.Index(message.Content, "{")
		if start < 0 {
			return nil
		}
		var payload struct {
			Memories []struct {
				Content string `json:"content"`
			} `json:"memories"`
		}
		if err := json.Unmarshal([]byte(message.Content[start:]), &payload); err != nil {
			return nil
		}
		facts := make([]string, 0, len(payload.Memories))
		for _, item := range payload.Memories {
			if content := strings.TrimSpace(item.Content); content != "" {
				facts = append(facts, content)
			}
		}
		return facts
	}
	return nil
}

func formatKnowledgeResult(raw string) string {
	var output struct {
		Results []struct {
			DocumentName string `json:"document_name"`
			Ordinal      int    `json:"ordinal"`
			Content      string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		return "知识库工具已返回结果，但本地 Mock 无法解析证据：" + truncateRunes(raw, 240)
	}
	if len(output.Results) == 0 {
		return "没有在已上传的文档中找到相关证据。"
	}
	var answer strings.Builder
	answer.WriteString("本地 Mock 找到以下证据：\n\n")
	for _, result := range output.Results[:min(len(output.Results), 5)] {
		content := strings.Join(strings.Fields(result.Content), " ")
		fmt.Fprintf(&answer, "- [%s#%d] %s\n", result.DocumentName, result.Ordinal, truncateRunes(content, 140))
	}
	answer.WriteString("\n以上是用于验证 RAG 链路的确定性回答；接入真实 Chat Model 后会根据证据综合作答。")
	return answer.String()
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
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
