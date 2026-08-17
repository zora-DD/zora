package agentruntime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/zhiruo/zora/internal/agenttools"
	"github.com/zhiruo/zora/internal/config"
)

func TestMockRuntimeExecutesToolThroughEino(t *testing.T) {
	t.Parallel()
	registeredTools, err := agenttools.Build()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(context.Background(), config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "Be helpful.",
		RequestTimeout: time.Second, MaxIterations: 5,
	}, registeredTools)
	if err != nil {
		t.Fatal(err)
	}

	var eventTypes []string
	answer, err := runtime.Execute(context.Background(), []*schema.Message{
		schema.UserMessage("帮我计算 (128 + 72) * 3.5"),
	}, func(event Event) error {
		eventTypes = append(eventTypes, event.Type)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "700") {
		t.Fatalf("answer %q does not contain calculator result", answer)
	}
	joined := strings.Join(eventTypes, ",")
	if !strings.Contains(joined, "tool_call") || !strings.Contains(joined, "tool_result") || !strings.Contains(joined, "delta") {
		t.Fatalf("event chain %q is incomplete", joined)
	}
}

func TestMultiAgentRoutesCompositeDocumentWritingTask(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runtime, err := NewMultiAgentWithModel(ctx, config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "请使用中文回答。",
		RequestTimeout: time.Second, MaxIterations: 8,
	}, SpecialistToolset{Document: []tool.BaseTool{staticKnowledgeTool{}}}, newMockModel())
	if err != nil {
		t.Fatal(err)
	}

	var handoffs, agentOutputs []string
	answer, err := runtime.Execute(ctx, []*schema.Message{
		schema.UserMessage("根据我上传的文档，写一份项目发布通知"),
	}, func(event Event) error {
		switch event.Type {
		case "agent_handoff_started":
			handoffs = append(handoffs, event.ToolName)
		case "agent_output":
			agentOutputs = append(agentOutputs, event.AgentName)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(handoffs, ",") != DocumentAgentName+","+WriterAgentName {
		t.Fatalf("handoffs = %v", handoffs)
	}
	if !containsString(agentOutputs, DocumentAgentName) || !containsString(agentOutputs, WriterAgentName) {
		t.Fatalf("agent outputs = %v", agentOutputs)
	}
	if strings.Count(answer, "写作专家草稿") != 1 || !strings.Contains(answer, "2026 年 9 月 18 日") {
		t.Fatalf("unexpected supervisor answer: %q", answer)
	}
}

func TestMultiAgentRoutesCalculationToResearchAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	researchTools, err := agenttools.Build()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewMultiAgentWithModel(ctx, config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "请使用中文回答。",
		RequestTimeout: time.Second, MaxIterations: 8,
	}, SpecialistToolset{Research: researchTools}, newMockModel())
	if err != nil {
		t.Fatal(err)
	}

	var handoffs []string
	answer, err := runtime.Execute(ctx, []*schema.Message{
		schema.UserMessage("帮我计算 (128 + 72) * 3.5"),
	}, func(event Event) error {
		if event.Type == "agent_handoff_started" {
			handoffs = append(handoffs, event.ToolName)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(handoffs) != 1 || handoffs[0] != ResearchAgentName {
		t.Fatalf("handoffs = %v", handoffs)
	}
	if !strings.Contains(answer, "700") {
		t.Fatalf("answer %q does not contain calculation result", answer)
	}
}

func TestMultiAgentIssuesIndependentTasksInParallel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	researchTools, err := agenttools.Build()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewMultiAgentWithModel(ctx, config.Config{
		Provider: "mock", Model: "zora-mock", Instruction: "请使用中文回答。",
		RequestTimeout: time.Second, MaxIterations: 8,
		MultiAgentMaxHandoffs: 4, MultiAgentMaxParallel: 2,
		MultiAgentSpecialistTimeout: time.Second,
	}, SpecialistToolset{Research: researchTools}, newMockModel())
	if err != nil {
		t.Fatal(err)
	}

	var handoffs []string
	answer, err := runtime.Execute(ctx, []*schema.Message{
		schema.UserMessage("请同时计算 6*7，并且写一条结果通知"),
	}, func(event Event) error {
		if event.Type == "agent_handoff_started" {
			handoffs = append(handoffs, event.ToolName)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(handoffs, ",") != ResearchAgentName+","+WriterAgentName {
		t.Fatalf("parallel handoffs = %v", handoffs)
	}
	if !strings.Contains(answer, "42") || !strings.Contains(answer, "写作专家草稿") {
		t.Fatalf("unexpected parallel answer: %q", answer)
	}
}

type staticKnowledgeTool struct{}

func (staticKnowledgeTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "knowledge_search", Desc: "检索测试知识库。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: schema.String, Required: true},
			"top_k": {Type: schema.Integer},
		}),
	}, nil
}

func (staticKnowledgeTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return `{"results":[{"document_name":"项目发布计划.md","ordinal":0,"content":"项目计划于 2026 年 9 月 18 日正式发布，上线前需完成灰度验证。"}]}`, nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
