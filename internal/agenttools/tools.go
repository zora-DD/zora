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
	Timezone string `json:"timezone" jsonschema_description:"IANA timezone such as Asia/Shanghai; defaults to Asia/Shanghai"`
}

type timeOutput struct {
	Timezone string `json:"timezone"`
	Time     string `json:"time"`
}

type calculatorInput struct {
	Expression string `json:"expression" jsonschema_description:"Arithmetic expression using numbers, parentheses, +, -, *, and /"`
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
		"Get the exact current time in an IANA timezone.",
		func(_ context.Context, input *timeInput) (*timeOutput, error) {
			zone := input.Timezone
			if zone == "" {
				zone = "Asia/Shanghai"
			}
			location, err := time.LoadLocation(zone)
			if err != nil {
				return nil, fmt.Errorf("unknown timezone %q", zone)
			}
			now := time.Now().In(location)
			return &timeOutput{Timezone: zone, Time: now.Format(time.RFC3339)}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build current_time tool: %w", err)
	}

	calculator, err := utils.InferTool(
		"calculator",
		"Safely evaluate a basic arithmetic expression. Use it instead of mental arithmetic.",
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
		return nil, fmt.Errorf("build calculator tool: %w", err)
	}

	status, err := utils.InferTool(
		"project_status",
		"Describe the capabilities and next milestone of the current Zora project.",
		func(_ context.Context, _ *projectStatusInput) (*projectStatusOutput, error) {
			return &projectStatusOutput{
				Version: "0.1.0",
				Available: []string{
					"streaming chat", "durable conversations", "Eino ReAct loop",
					"read-only tools", "run event audit",
				},
				NextMilestone: "document ingestion and hybrid RAG with citations",
			}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build project_status tool: %w", err)
	}

	return []tool.BaseTool{clock, calculator, status}, nil
}
