package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
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
	if systemHasAgentRole(input, "answer_reviewer") {
		return schema.AssistantMessage(`{"verdict":"pass","issues":[],"rewrite_instruction":""}`, nil), nil
	}
	// 新版 Eino 会通过调用级 Option 注入工具，不能只读取 WithTools 保存的字段。
	availableTools := model.GetCommonOptions(&model.Options{Tools: m.tools}, opts...).Tools
	if last.Role == schema.Tool {
		if trailing := trailingToolMessages(input); len(trailing) > 1 {
			var combined strings.Builder
			combined.WriteString("多 Agent 并行结果：\n\n")
			for _, result := range trailing {
				fmt.Fprintf(&combined, "### %s\n%s\n\n", agentDisplayNameForMock(result.ToolName), result.Content)
			}
			return schema.AssistantMessage(strings.TrimSpace(combined.String()), nil), nil
		}
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
		case "preview_email_draft", "preview_calendar_draft":
			return schema.AssistantMessage(formatOfficeDraftResult(last.ToolName, last.Content), nil), nil
		}
		if strings.HasPrefix(last.ToolName, "mcp_") {
			return schema.AssistantMessage(formatMCPResult(last.ToolName, last.Content), nil), nil
		}
		return schema.AssistantMessage(prefix+"："+last.Content, nil), nil
	}

	memoryFacts := recalledMemoryFacts(input)
	conversationSummary := conversationSummaryFact(input)
	switch {
	case systemHasAgentRole(input, "writer") && hasEmailDraftIntent(lower) && hasTool(availableTools, "preview_email_draft"):
		arguments, prompt := mockEmailDraftArguments(query)
		if prompt != "" {
			return schema.AssistantMessage(prompt, nil), nil
		}
		return m.toolCall("preview_email_draft", arguments), nil
	case systemHasAgentRole(input, "writer") && hasCalendarDraftIntent(lower) && hasTool(availableTools, "preview_calendar_draft"):
		arguments, prompt := mockCalendarDraftArguments(query)
		if prompt != "" {
			return schema.AssistantMessage(prompt, nil), nil
		}
		return m.toolCall("preview_calendar_draft", arguments), nil
	case systemHasAgentRole(input, "writer"):
		return schema.AssistantMessage(formatWriterDraft(query), nil), nil
	case hasEmailDraftIntent(lower) && hasTool(availableTools, "preview_email_draft"):
		arguments, prompt := mockEmailDraftArguments(query)
		if prompt != "" {
			return schema.AssistantMessage(prompt, nil), nil
		}
		return m.toolCall("preview_email_draft", arguments), nil
	case hasCalendarDraftIntent(lower) && hasTool(availableTools, "preview_calendar_draft"):
		arguments, prompt := mockCalendarDraftArguments(query)
		if prompt != "" {
			return schema.AssistantMessage(prompt, nil), nil
		}
		return m.toolCall("preview_calendar_draft", arguments), nil
	case hasMCPEmailReadIntent(lower) && toolNameWithSuffix(availableTools, "_search_emails") != "":
		keyword := extractMCPKeyword(query, []string{"搜索邮件", "查找邮件", "search email", "search mail"})
		return m.toolCall(toolNameWithSuffix(availableTools, "_search_emails"), fmt.Sprintf(`{"query":%q,"limit":10}`, keyword)), nil
	case hasMCPCalendarReadIntent(lower) && toolNameWithSuffix(availableTools, "_list_calendar_events") != "":
		keyword := extractMCPKeyword(query, []string{"搜索日程", "查询日程", "搜索日历", "search calendar"})
		return m.toolCall(toolNameWithSuffix(availableTools, "_list_calendar_events"), fmt.Sprintf(`{"query":%q,"limit":10}`, keyword)), nil
	case hasMCPListFilesIntent(lower) && toolNameWithSuffix(availableTools, "_list_files") != "":
		return m.toolCall(toolNameWithSuffix(availableTools, "_list_files"), `{"path":".","recursive":false,"limit":100}`), nil
	case hasMCPReadFileIntent(lower) && toolNameWithSuffix(availableTools, "_read_text_file") != "":
		path := extractRequestedFilePath(query)
		if path == "" {
			return schema.AssistantMessage("请提供要读取的相对文件路径，例如：读取文件 `docs/周报.md`。", nil), nil
		}
		return m.toolCall(toolNameWithSuffix(availableTools, "_read_text_file"), fmt.Sprintf(`{"path":%q}`, path)), nil
	case (hasMCPEmailReadIntent(lower) || hasMCPCalendarReadIntent(lower) || hasMCPListFilesIntent(lower) || hasMCPReadFileIntent(lower)) && hasTool(availableTools, DocumentAgentName):
		return m.toolCall(DocumentAgentName, fmt.Sprintf(`{"request":%q}`, query)), nil
	case hasMCPEmailReadIntent(lower):
		return schema.AssistantMessage("MCP 邮件连接器尚未启用，请先配置 Microsoft Graph 只读连接器。", nil), nil
	case hasMCPCalendarReadIntent(lower):
		return schema.AssistantMessage("MCP 日历连接器尚未启用，请先配置 Microsoft Graph 只读连接器。", nil), nil
	case hasMCPListFilesIntent(lower) || hasMCPReadFileIntent(lower):
		return schema.AssistantMessage("MCP 文件连接器尚未启用，请先完成连接器配置。", nil), nil
	// “发布日期”等资料字段会包含“日期”。知识库意图必须优先于时间意图，
	// 否则本地 Mock 会错误地把“查文档里的日期”理解成“查询当前日期”。
	case hasDocumentRetrievalIntent(lower) && hasTool(availableTools, "knowledge_search"):
		return m.toolCall("knowledge_search", fmt.Sprintf(`{"query":%q,"top_k":5}`, query)), nil
	case hasConversationSummaryIntent(lower) && conversationSummary != "":
		return schema.AssistantMessage("根据这段对话的历史摘要：\n\n"+conversationSummary, nil), nil
	case hasMemoryQuestionIntent(lower) && len(memoryFacts) > 0:
		return schema.AssistantMessage("根据长期记忆，我找到了这些相关信息：\n\n- "+strings.Join(memoryFacts, "\n- "), nil), nil
	case hasParallelSpecialistIntent(lower) && hasTool(availableTools, ResearchAgentName) && hasTool(availableTools, WriterAgentName):
		return m.toolCalls([]namedToolCall{
			{name: ResearchAgentName, arguments: fmt.Sprintf(`{"request":%q}`, query)},
			{name: WriterAgentName, arguments: fmt.Sprintf(`{"request":%q}`, query)},
		}), nil
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

func hasMCPListFilesIntent(query string) bool {
	return containsAny(query, "列出文件", "查看文件列表", "有哪些办公文件", "办公目录", "list files")
}

func hasMCPReadFileIntent(query string) bool {
	return containsAny(query, "读取文件", "打开文件", "读取办公文件", "查看文件内容", "read file")
}

func hasMCPEmailReadIntent(query string) bool {
	return containsAny(query,
		"查邮件", "查询邮件", "搜索邮件", "查找邮件", "最近邮件", "最新邮件", "未读邮件", "收件箱",
		"邮件里", "邮件中", "根据邮件", "email inbox", "search email", "search mail", "recent email")
}

func hasMCPCalendarReadIntent(query string) bool {
	return containsAny(query,
		"查日程", "查询日程", "搜索日程", "最近日程", "今日日程", "今天的日程", "明日日程", "明天的日程",
		"未来日程", "本周日程", "下周日程", "查看日历", "查询日历", "日历里", "日历中", "会议安排",
		"calendar events", "search calendar", "my calendar")
}

func extractMCPKeyword(query string, markers []string) string {
	if quoted := extractRequestedFilePath(query); quoted != "" {
		return quoted
	}
	lower := strings.ToLower(query)
	for _, marker := range markers {
		if index := strings.Index(lower, marker); index >= 0 {
			value := strings.TrimSpace(query[index+len(marker):])
			return strings.Trim(strings.TrimSpace(value), "：:，,。？?!！")
		}
	}
	return ""
}

func extractRequestedFilePath(query string) string {
	for _, delimiters := range [][2]string{{"`", "`"}, {"“", "”"}, {`"`, `"`}, {"'", "'"}} {
		start := strings.Index(query, delimiters[0])
		if start < 0 {
			continue
		}
		remaining := query[start+len(delimiters[0]):]
		end := strings.Index(remaining, delimiters[1])
		if end >= 0 {
			return strings.TrimSpace(remaining[:end])
		}
	}
	for _, marker := range []string{"读取办公文件", "查看文件内容", "读取文件", "打开文件", "read file"} {
		if index := strings.Index(strings.ToLower(query), marker); index >= 0 {
			value := strings.TrimSpace(query[index+len(marker):])
			return strings.Trim(strings.TrimSpace(value), "：:。？?!！")
		}
	}
	return ""
}

func toolNameWithSuffix(tools []*schema.ToolInfo, suffix string) string {
	for _, item := range tools {
		if item != nil && strings.HasPrefix(item.Name, "mcp_") && strings.HasSuffix(item.Name, suffix) {
			return item.Name
		}
	}
	return ""
}

func formatMCPResult(toolName, content string) string {
	if strings.HasSuffix(toolName, "_search_emails") {
		var payload struct {
			Emails []struct {
				ID      string `json:"id"`
				Subject string `json:"subject"`
				From    struct {
					Name    string `json:"name"`
					Address string `json:"address"`
				} `json:"from"`
				ReceivedDateTime string `json:"received_date_time"`
				BodyPreview      string `json:"body_preview"`
				IsRead           bool   `json:"is_read"`
				HasAttachments   bool   `json:"has_attachments"`
			} `json:"emails"`
			ContentWarning string `json:"content_warning"`
		}
		if err := json.Unmarshal([]byte(content), &payload); err == nil {
			var builder strings.Builder
			builder.WriteString("Microsoft 邮件查询结果：")
			if len(payload.Emails) == 0 {
				builder.WriteString("\n\n没有找到匹配的邮件。")
			}
			for _, email := range payload.Emails {
				readState := "未读"
				if email.IsRead {
					readState = "已读"
				}
				attachment := ""
				if email.HasAttachments {
					attachment = "，含附件"
				}
				sender := strings.TrimSpace(email.From.Name)
				if sender == "" {
					sender = email.From.Address
				} else if email.From.Address != "" {
					sender += " <" + email.From.Address + ">"
				}
				fmt.Fprintf(&builder, "\n\n- **%s**（%s%s）\n  发件人：%s\n  时间：%s\n  摘要：%s\n  邮件 ID：`%s`", email.Subject, readState, attachment, sender, email.ReceivedDateTime, email.BodyPreview, email.ID)
			}
			appendMCPContentWarning(&builder, payload.ContentWarning)
			return builder.String()
		}
	}
	if strings.HasSuffix(toolName, "_get_email") {
		var payload struct {
			Email struct {
				ID      string `json:"id"`
				Subject string `json:"subject"`
				From    struct {
					Name    string `json:"name"`
					Address string `json:"address"`
				} `json:"from"`
				ReceivedDateTime string `json:"received_date_time"`
				BodyPreview      string `json:"body_preview"`
				HasAttachments   bool   `json:"has_attachments"`
			} `json:"email"`
			ContentWarning string `json:"content_warning"`
		}
		if err := json.Unmarshal([]byte(content), &payload); err == nil {
			var builder strings.Builder
			fmt.Fprintf(&builder, "邮件 **%s**\n\n- 发件人：%s <%s>\n- 接收时间：%s\n- 邮件 ID：`%s`\n\n%s", payload.Email.Subject, payload.Email.From.Name, payload.Email.From.Address, payload.Email.ReceivedDateTime, payload.Email.ID, payload.Email.BodyPreview)
			if payload.Email.HasAttachments {
				builder.WriteString("\n\n[此邮件含附件；只读连接器未下载附件]")
			}
			appendMCPContentWarning(&builder, payload.ContentWarning)
			return builder.String()
		}
	}
	if strings.HasSuffix(toolName, "_list_calendar_events") {
		var payload struct {
			WindowStart string `json:"window_start"`
			WindowEnd   string `json:"window_end"`
			Events      []struct {
				ID        string                              `json:"id"`
				Subject   string                              `json:"subject"`
				Start     struct{ DateTime, TimeZone string } `json:"start"`
				End       struct{ DateTime, TimeZone string } `json:"end"`
				Location  string                              `json:"location"`
				Organizer struct {
					Name    string `json:"name"`
					Address string `json:"address"`
				} `json:"organizer"`
				IsCancelled bool `json:"is_cancelled"`
			} `json:"events"`
			ContentWarning string `json:"content_warning"`
		}
		if err := json.Unmarshal([]byte(content), &payload); err == nil {
			var builder strings.Builder
			fmt.Fprintf(&builder, "Microsoft 日历查询结果（%s 至 %s）：", payload.WindowStart, payload.WindowEnd)
			if len(payload.Events) == 0 {
				builder.WriteString("\n\n没有找到匹配的日程。")
			}
			for _, event := range payload.Events {
				state := ""
				if event.IsCancelled {
					state = " [已取消]"
				}
				fmt.Fprintf(&builder, "\n\n- **%s**%s\n  时间：%s — %s（%s）\n  地点：%s\n  组织者：%s <%s>\n  日程 ID：`%s`", event.Subject, state, event.Start.DateTime, event.End.DateTime, event.Start.TimeZone, valueOrDefault(event.Location, "未填写"), event.Organizer.Name, event.Organizer.Address, event.ID)
			}
			appendMCPContentWarning(&builder, payload.ContentWarning)
			return builder.String()
		}
	}
	if strings.HasSuffix(toolName, "_get_calendar_event") {
		var payload struct {
			Event struct {
				ID          string                              `json:"id"`
				Subject     string                              `json:"subject"`
				Start       struct{ DateTime, TimeZone string } `json:"start"`
				End         struct{ DateTime, TimeZone string } `json:"end"`
				Location    string                              `json:"location"`
				BodyPreview string                              `json:"body_preview"`
			} `json:"event"`
			ContentWarning string `json:"content_warning"`
		}
		if err := json.Unmarshal([]byte(content), &payload); err == nil {
			var builder strings.Builder
			fmt.Fprintf(&builder, "日程 **%s**\n\n- 时间：%s — %s（%s）\n- 地点：%s\n- 日程 ID：`%s`\n\n%s", payload.Event.Subject, payload.Event.Start.DateTime, payload.Event.End.DateTime, payload.Event.Start.TimeZone, valueOrDefault(payload.Event.Location, "未填写"), payload.Event.ID, payload.Event.BodyPreview)
			appendMCPContentWarning(&builder, payload.ContentWarning)
			return builder.String()
		}
	}
	if strings.HasSuffix(toolName, "_list_files") {
		var payload struct {
			Directory string `json:"directory"`
			Entries   []struct {
				Path  string `json:"path"`
				IsDir bool   `json:"is_dir"`
			} `json:"entries"`
			Truncated bool `json:"truncated"`
		}
		if err := json.Unmarshal([]byte(content), &payload); err == nil {
			var builder strings.Builder
			fmt.Fprintf(&builder, "授权目录 `%s` 中的文件：", payload.Directory)
			if len(payload.Entries) == 0 {
				builder.WriteString("\n\n当前目录为空。")
			}
			for _, entry := range payload.Entries {
				kind := "文件"
				if entry.IsDir {
					kind = "目录"
				}
				fmt.Fprintf(&builder, "\n\n- [%s] %s", kind, entry.Path)
			}
			if payload.Truncated {
				builder.WriteString("\n\n结果已达到列表上限，请指定更具体的子目录。")
			}
			return builder.String()
		}
	}
	if strings.HasSuffix(toolName, "_read_text_file") {
		var payload struct {
			Path      string `json:"path"`
			Content   string `json:"content"`
			Truncated bool   `json:"truncated"`
		}
		if err := json.Unmarshal([]byte(content), &payload); err == nil {
			answer := fmt.Sprintf("文件 `%s` 的内容：\n\n%s", payload.Path, payload.Content)
			if payload.Truncated {
				answer += "\n\n[内容已按字符上限截断]"
			}
			return answer
		}
	}
	return "MCP 办公工具返回：\n\n" + content
}

func formatOfficeDraftResult(toolName, content string) string {
	var payload struct {
		Draft struct {
			ID      string          `json:"id"`
			Kind    string          `json:"kind"`
			Status  string          `json:"status"`
			Title   string          `json:"title"`
			Payload json.RawMessage `json:"payload"`
		} `json:"draft"`
		ExternalEffect  bool   `json:"external_effect"`
		ConfirmationTip string `json:"confirmation_tip"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil || payload.Draft.ID == "" {
		return "办公草稿工具返回：\n\n" + content
	}
	var builder strings.Builder
	if strings.HasSuffix(toolName, "preview_email_draft") {
		var email struct {
			To      []string `json:"to"`
			CC      []string `json:"cc"`
			Subject string   `json:"subject"`
			Body    string   `json:"body"`
		}
		if err := json.Unmarshal(payload.Draft.Payload, &email); err == nil {
			fmt.Fprintf(&builder, "邮件草稿已保存：\n\n- 草稿 ID：`%s`\n- 状态：%s\n- 收件人：%s\n- 抄送：%s\n- 主题：%s\n\n%s", payload.Draft.ID, payload.Draft.Status, strings.Join(email.To, "、"), valueOrDefault(strings.Join(email.CC, "、"), "无"), email.Subject, email.Body)
		}
	} else {
		var event struct {
			Attendees []string `json:"attendees"`
			Subject   string   `json:"subject"`
			Start     string   `json:"start"`
			End       string   `json:"end"`
			TimeZone  string   `json:"timezone"`
			Location  string   `json:"location"`
			Body      string   `json:"body"`
		}
		if err := json.Unmarshal(payload.Draft.Payload, &event); err == nil {
			fmt.Fprintf(&builder, "日程草稿已保存：\n\n- 草稿 ID：`%s`\n- 状态：%s\n- 主题：%s\n- 时间：%s — %s\n- 时区：%s\n- 地点：%s\n- 参与人：%s\n\n%s", payload.Draft.ID, payload.Draft.Status, event.Subject, event.Start, event.End, valueOrDefault(event.TimeZone, "按时间中的偏移量"), valueOrDefault(event.Location, "未填写"), valueOrDefault(strings.Join(event.Attendees, "、"), "无"), event.Body)
		}
	}
	if builder.Len() == 0 {
		fmt.Fprintf(&builder, "办公草稿 `%s` 已保存，状态为 %s。", payload.Draft.ID, payload.Draft.Status)
	}
	if payload.ConfirmationTip != "" {
		builder.WriteString("\n\n> " + payload.ConfirmationTip)
	} else if !payload.ExternalEffect {
		builder.WriteString("\n\n> 当前只保存了内部预览，没有产生任何外部写操作。")
	}
	return builder.String()
}

func appendMCPContentWarning(builder *strings.Builder, warning string) {
	if warning = strings.TrimSpace(warning); warning != "" {
		builder.WriteString("\n\n> 安全提示：" + warning)
	}
}

func valueOrDefault(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func hasWritingIntent(query string) bool {
	return containsAny(query, "写一", "写份", "起草", "撰写", "润色", "改写", "整理成", "生成邮件", "生成通知", "写邮件", "写通知", "文案", "安排会议", "创建日程", "新建日程", "日程草稿")
}

func hasEmailDraftIntent(query string) bool {
	return containsAny(query, "起草邮件", "起草一封邮件", "邮件草稿", "写邮件", "写一封邮件", "写封邮件", "拟一封邮件", "生成邮件", "发邮件")
}

func hasCalendarDraftIntent(query string) bool {
	return containsAny(query, "安排会议", "创建日程", "新建日程", "日程草稿", "会议草稿", "拟定日程")
}

var (
	mockEmailPattern   = regexp.MustCompile(`[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+`)
	mockRFC3339Pattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)
)

func mockEmailDraftArguments(query string) (string, string) {
	addresses := mockEmailPattern.FindAllString(query, -1)
	if len(addresses) == 0 {
		return "", "请提供至少一个收件人邮箱，并尽量给出主题和正文。例如：起草邮件，收件人 dev@example.com，主题：发布通知，正文：项目将在周五发布。"
	}
	subject := extractDraftField(query, "主题")
	if subject == "" {
		subject = "待确认邮件草稿"
	}
	body := extractDraftField(query, "正文")
	if body == "" {
		body = "请根据以下需求确认并完善邮件正文：\n\n" + query
	}
	encoded, _ := json.Marshal(map[string]any{"to": addresses, "subject": subject, "body": body})
	return string(encoded), ""
}

func mockCalendarDraftArguments(query string) (string, string) {
	times := mockRFC3339Pattern.FindAllString(query, -1)
	if len(times) < 2 {
		return "", "请提供带时区的开始和结束时间。例如：创建日程，主题：发布评审，开始：2026-08-20T10:00:00+08:00，结束：2026-08-20T11:00:00+08:00。"
	}
	subject := extractDraftField(query, "主题")
	if subject == "" {
		subject = "待确认日程"
	}
	body := extractDraftField(query, "正文")
	encoded, _ := json.Marshal(map[string]any{
		"attendees": mockEmailPattern.FindAllString(query, -1),
		"subject":   subject, "start": times[0], "end": times[1],
		"timezone": "Asia/Shanghai", "body": body,
	})
	return string(encoded), ""
}

func extractDraftField(query, field string) string {
	for _, marker := range []string{field + "：", field + ":"} {
		index := strings.Index(query, marker)
		if index < 0 {
			continue
		}
		value := strings.TrimSpace(query[index+len(marker):])
		separators := []string{"\n", "；", ";"}
		if field == "主题" {
			separators = append(separators, "，正文：", ",正文:", "，开始：", ",开始:")
		}
		for _, separator := range separators {
			if end := strings.Index(value, separator); end >= 0 {
				value = value[:end]
			}
		}
		return strings.TrimSpace(value)
	}
	return ""
}

func hasResearchIntent(query string) bool {
	return containsAny(query, "调研", "研究一下", "分析一下", "对比", "比较", "项目状态", "项目能力", "roadmap", "功能", "计算", "算一下", "几点", "当前时间")
}

func hasParallelSpecialistIntent(query string) bool {
	return containsAny(query, "同时", "分别", "并且") && hasResearchIntent(query) && hasWritingIntent(query)
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
	return m.toolCalls([]namedToolCall{{name: name, arguments: arguments}})
}

type namedToolCall struct {
	name      string
	arguments string
}

func (m *mockModel) toolCalls(calls []namedToolCall) *schema.Message {
	toolCalls := make([]schema.ToolCall, 0, len(calls))
	for _, call := range calls {
		id := fmt.Sprintf("mock_call_%d", m.counter.Add(1))
		toolCalls = append(toolCalls, schema.ToolCall{
			ID: id, Type: "function",
			Function: schema.FunctionCall{Name: call.name, Arguments: call.arguments},
		})
	}
	return schema.AssistantMessage("", toolCalls)
}

func trailingToolMessages(input []*schema.Message) []*schema.Message {
	start := len(input)
	for start > 0 && input[start-1].Role == schema.Tool {
		start--
	}
	return input[start:]
}

func agentDisplayNameForMock(name string) string {
	switch name {
	case ResearchAgentName:
		return "研究专家"
	case DocumentAgentName:
		return "文档专家"
	case WriterAgentName:
		return "写作专家"
	default:
		return name
	}
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
