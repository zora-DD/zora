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
				Version: "0.3.0-dev",
				Available: []string{
					"流式对话", "持久化会话", "Eino ReAct 循环",
					"只读工具", "执行事件审计", "TXT/Markdown 知识库摄取",
					"向量与 BM25 混合检索", "知识库引用与答案评测",
					"Semantic/Episodic 长期记忆 Schema", "长期记忆用户管理",
					"对话记忆候选提取", "基于 Memory Key 的去重与冲突合并",
					"长期记忆联合召回与上下文注入",
					"会话增量摘要与上下文压缩",
					"长期记忆有/无 A/B 评测与质量门禁",
				},
				NextMilestone: "V0.4 Supervisor 与专业 Agent 的可评估协作",
			}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("创建项目状态工具失败：%w", err)
	}

	return []tool.BaseTool{clock, calculator, status}, nil
}
