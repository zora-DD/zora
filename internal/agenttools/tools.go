// Package agenttools 定义 Zora 当前允许调用的安全只读工具。
package agenttools

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type timeInput struct {
	Timezone string `json:"timezone" jsonschema_description:"IANA 时区，例如 Asia/Shanghai；默认为 Asia/Shanghai"`
}

type timeOutput struct {
	Timezone string `json:"timezone"`
	Time     string `json:"time"`
}

type calculatorInput struct {
	Expression string `json:"expression" jsonschema_description:"仅支持数字、括号和 +、-、*、/ 的四则运算表达式"`
}

type calculatorOutput struct {
	Expression string `json:"expression"`
	Result     string `json:"result"`
}

type projectStatusInput struct{}

type projectStatusOutput struct {
	Version       string   `json:"version"`
	Available     []string `json:"available"`
	NextMilestone string   `json:"next_milestone"`
}

// Build 返回显式 allowlist。新增工具必须在这里注册，不能由模型动态加载任意代码。
func Build() ([]tool.BaseTool, error) {
	// InferTool 会从 Go 结构体生成 JSON Schema，并在执行前完成参数反序列化。
	clock, err := utils.InferTool(
		"current_time",
		"获取指定 IANA 时区的精确当前时间。",
		func(_ context.Context, input *timeInput) (*timeOutput, error) {
			zone := input.Timezone
			if zone == "" {
				zone = "Asia/Shanghai"
			}
			location, err := time.LoadLocation(zone)
			if err != nil {
				return nil, fmt.Errorf("未知的时区：%q", zone)
			}
			now := time.Now().In(location)
			return &timeOutput{Timezone: zone, Time: now.Format(time.RFC3339)}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("创建时间工具失败：%w", err)
	}

	calculator, err := utils.InferTool(
		"calculator",
		"安全计算基本四则运算表达式。遇到精确计算时应使用此工具，不要心算。",
		func(_ context.Context, input *calculatorInput) (*calculatorOutput, error) {
			// calculate 使用自研语法解析器，只支持四则运算，不会执行 Go、Shell 或脚本代码。
			value, err := calculate(input.Expression)
			if err != nil {
				return nil, err
			}
			return &calculatorOutput{Expression: input.Expression, Result: formatNumber(value)}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("创建计算器工具失败：%w", err)
	}

	status, err := utils.InferTool(
		"project_status",
		"介绍当前 Zora 项目的已实现能力与下一个里程碑。",
		func(_ context.Context, _ *projectStatusInput) (*projectStatusOutput, error) {
			return &projectStatusOutput{
				Version: "0.5.0-dev",
				Available: []string{
					"流式对话", "持久化会话", "Eino ReAct 循环",
					"只读工具", "执行事件审计", "TXT/Markdown/PDF 知识库摄取",
					"知识库版本链、递归切块与文档级权限",
					"向量与 BM25 混合检索", "知识库引用与答案评测",
					"Semantic/Episodic 长期记忆 Schema", "长期记忆用户管理",
					"对话记忆候选提取", "基于 Memory Key 的去重与冲突合并",
					"长期记忆联合召回与上下文注入",
					"会话增量摘要与上下文压缩",
					"长期记忆有/无 A/B 评测与质量门禁",
					"可选 Supervisor 与研究/文档/写作专业 Agent",
					"专业 Agent 工具隔离、串并行交接与协作事件审计",
					"多 Agent 交接/并行预算、超时、重试和取消",
					"父子 Run、人工审批与 Web 恢复执行",
					"单 Agent / 多 Agent 质量、调用成本代理和耗时对照门禁",
					"官方 MCP Go SDK 客户端、只读文件连接器与最小环境隔离",
					"Microsoft Graph 邮件/日历只读连接器与外部内容安全标记",
					"邮件/日程结构化草稿预览、持久化与 Run 来源追踪",
					"Office 草稿持久化人工确认、一次性决策与状态迁移审计",
					"可恢复 Office Operation 与 Microsoft Graph 幂等写执行器",
					"Microsoft client credentials OAuth、Secret 文件与读写身份隔离",
				},
				NextMilestone: "V0.5 真实 Microsoft 租户读写与最小权限范围验收",
			}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("创建项目状态工具失败：%w", err)
	}

	return []tool.BaseTool{clock, calculator, status}, nil
}
