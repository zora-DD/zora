package office

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type emailDraftToolOutput struct {
	Draft           Draft  `json:"draft"`
	Created         bool   `json:"created"`
	ExternalEffect  bool   `json:"external_effect"`
	ConfirmationTip string `json:"confirmation_tip"`
}

type calendarDraftToolOutput struct {
	Draft           Draft  `json:"draft"`
	Created         bool   `json:"created"`
	ExternalEffect  bool   `json:"external_effect"`
	ConfirmationTip string `json:"confirmation_tip"`
}

// NewDraftTools 暴露两个“只生成内部预览”的 Agent 工具。
// 工具名称故意使用 preview，避免模型把成功 ToolResult 描述成已经发送或创建。
func NewDraftTools(service *Service) ([]tool.BaseTool, error) {
	if service == nil {
		return nil, fmt.Errorf("办公草稿服务不能为空")
	}
	emailTool, err := utils.InferTool(
		"preview_email_draft",
		"创建并保存邮件草稿预览。此工具不会发送邮件；当用户要求起草、拟一封或预览邮件时使用。必须向用户说明仍需人工确认。",
		func(ctx context.Context, input *EmailDraft) (*emailDraftToolOutput, error) {
			draft, created, err := service.CreateEmailDraft(ctx, *input)
			if err != nil {
				return nil, err
			}
			return &emailDraftToolOutput{
				Draft: draft, Created: created, ExternalEffect: false,
				ConfirmationTip: "当前仅保存 Zora 内部草稿，尚未发送；后续外部写入必须再次人工确认。",
			}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("创建邮件草稿预览工具失败：%w", err)
	}
	calendarTool, err := utils.InferTool(
		"preview_calendar_draft",
		"创建并保存日程草稿预览。此工具不会写入日历；当用户要求拟定、安排或预览会议日程时使用。开始和结束时间必须是带时区的 RFC3339。",
		func(ctx context.Context, input *CalendarDraft) (*calendarDraftToolOutput, error) {
			draft, created, err := service.CreateCalendarDraft(ctx, *input)
			if err != nil {
				return nil, err
			}
			return &calendarDraftToolOutput{
				Draft: draft, Created: created, ExternalEffect: false,
				ConfirmationTip: "当前仅保存 Zora 内部草稿，尚未创建日程；后续外部写入必须再次人工确认。",
			}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("创建日程草稿预览工具失败：%w", err)
	}
	return []tool.BaseTool{emailTool, calendarTool}, nil
}
