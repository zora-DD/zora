package observability

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/zhiruo/zora/internal/memory"
)

func TestTraceConnectsHTTPRunModelToolAndEmbedding(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	telemetry := &Telemetry{
		tracer: provider.Tracer(instrumentationName),
		meter:  noop.NewMeterProvider().Meter(instrumentationName),
		propagator: propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{}, propagation.Baggage{},
		),
	}
	if err := telemetry.createInstruments(); err != nil {
		t.Fatalf("创建测试指标失败：%v", err)
	}

	request := httptest.NewRequest("POST", "/api/conversations/conv_1/messages", nil)
	request, httpSpan, httpStarted := telemetry.StartHTTP(request)
	runCtx, runSpan, runStarted := telemetry.StartRun(
		request.Context(), "run_1", "conv_1", "zora-assistant", "openai", "deepseek-chat",
	)

	chatModel := telemetry.WrapChatModel(staticChatModel{}, "openai", "deepseek-chat")
	if _, err := chatModel.Generate(runCtx, []*schema.Message{schema.UserMessage("测试")}); err != nil {
		t.Fatalf("调用测试模型失败：%v", err)
	}

	embedder := telemetry.WrapEmbedder(staticEmbedder{}, "openai")
	baseTool := embeddingSearchTool{embedder: embedder}
	observedTool, err := telemetry.WrapTool(runCtx, baseTool)
	if err != nil {
		t.Fatalf("包装测试工具失败：%v", err)
	}
	if _, err := observedTool.(tool.InvokableTool).InvokableRun(runCtx, `{"query":"测试"}`); err != nil {
		t.Fatalf("调用测试工具失败：%v", err)
	}
	jobCtx, finishJob := telemetry.BeginMemoryCaptureJob(context.Background(), memory.CaptureJob{
		ID: "memory_job_1", RunID: "run_1", Attempt: 1,
		TraceParent: telemetry.TraceParent(runCtx), CreatedAt: time.Now().UTC(),
	})
	_ = jobCtx
	finishJob(memory.JobCompleted, nil)

	telemetry.EndRun(runCtx, runSpan, runStarted, "openai", "deepseek-chat", "completed", nil)
	telemetry.EndHTTP(request.Context(), httpSpan, httpStarted, "POST", "POST /api/conversations/{conversationID}/messages", 200)

	spans := tracetest.SpanStubsFromReadOnlySpans(recorder.Ended())
	byName := make(map[string]tracetest.SpanStub, len(spans))
	for _, span := range spans {
		byName[span.Name] = span
	}
	for _, name := range []string{
		"HTTP POST /api/conversations/{conversationID}/messages",
		"agent.run", "gen_ai.chat", "tool.knowledge_search", "embedding.generate", "memory.capture",
	} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("缺少 Span %q，实际为：%v", name, spanNames(spans))
		}
	}

	httpRecorded := byName["HTTP POST /api/conversations/{conversationID}/messages"]
	runRecorded := byName["agent.run"]
	modelRecorded := byName["gen_ai.chat"]
	toolRecorded := byName["tool.knowledge_search"]
	embeddingRecorded := byName["embedding.generate"]
	memoryRecorded := byName["memory.capture"]
	if runRecorded.Parent.SpanID() != httpRecorded.SpanContext.SpanID() {
		t.Fatal("Run Span 没有挂在 HTTP Span 下")
	}
	if modelRecorded.Parent.SpanID() != runRecorded.SpanContext.SpanID() {
		t.Fatal("模型 Span 没有挂在 Run Span 下")
	}
	if toolRecorded.Parent.SpanID() != runRecorded.SpanContext.SpanID() {
		t.Fatal("工具 Span 没有挂在 Run Span 下")
	}
	if embeddingRecorded.Parent.SpanID() != toolRecorded.SpanContext.SpanID() {
		t.Fatal("Embedding Span 没有挂在知识库工具 Span 下")
	}
	if memoryRecorded.Parent.SpanID() != runRecorded.SpanContext.SpanID() {
		t.Fatal("异步 Memory Span 没有通过持久化 traceparent 接回 Run Span")
	}
	if httpRecorded.SpanContext.TraceID() != embeddingRecorded.SpanContext.TraceID() {
		t.Fatal("HTTP 与 Embedding 没有进入同一条 Trace")
	}
}

func TestPrometheusHandlerExportsBusinessMetrics(t *testing.T) {
	telemetry, err := NewTelemetry(context.Background(), TelemetryConfig{
		ServiceName: "zora-test", ServiceVersion: "test", Environment: "test",
		PrometheusEnabled: true,
	})
	if err != nil {
		t.Fatalf("创建 Prometheus 测试实例失败：%v", err)
	}
	t.Cleanup(func() { _ = telemetry.Shutdown(context.Background()) })

	ctx, span, started := telemetry.StartRun(context.Background(), "run_1", "conv_1", "zora-assistant", "mock", "zora-mock")
	telemetry.EndRun(ctx, span, started, "mock", "zora-mock", "completed", nil)
	recorder := httptest.NewRecorder()
	telemetry.MetricsHandler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if recorder.Code != 200 {
		t.Fatalf("抓取指标返回 %d：%s", recorder.Code, body)
	}
	for _, metricName := range []string{"zora_agent_runs", "zora_agent_run_duration"} {
		if !strings.Contains(body, metricName) {
			t.Fatalf("指标输出缺少 %q：\n%s", metricName, body)
		}
	}
}

func TestTelemetryRejectsInvalidOTLPEndpoint(t *testing.T) {
	_, err := NewTelemetry(context.Background(), TelemetryConfig{
		TracingEnabled: true, OTLPEndpoint: "://invalid", TraceSampleRatio: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "OTLP Endpoint") {
		t.Fatalf("期望非法 OTLP Endpoint 被拒绝，实际错误：%v", err)
	}
}

func TestSpanErrorDoesNotRecordSensitiveMessage(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	telemetry := &Telemetry{
		tracer:     provider.Tracer(instrumentationName),
		meter:      noop.NewMeterProvider().Meter(instrumentationName),
		propagator: propagation.TraceContext{},
	}
	if err := telemetry.createInstruments(); err != nil {
		t.Fatalf("创建测试指标失败：%v", err)
	}
	ctx, span, started := telemetry.StartTool(context.Background(), "sensitive_tool", 20)
	telemetry.EndTool(ctx, span, started, "sensitive_tool", errors.New("不应进入 Trace 的敏感正文"))

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("结束 Span 数量 = %d，期望 1", len(ended))
	}
	serialized := fmt.Sprint(ended[0].Attributes(), ended[0].Events(), ended[0].Status())
	if strings.Contains(serialized, "不应进入 Trace 的敏感正文") {
		t.Fatalf("Trace 泄露了原始错误正文：%s", serialized)
	}
}

func spanNames(spans tracetest.SpanStubs) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name)
	}
	return names
}

type staticChatModel struct{}

func (staticChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("完成", nil), nil
}

func (staticChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](1)
	writer.Send(schema.AssistantMessage("完成", nil), nil)
	writer.Close()
	return reader, nil
}

type staticEmbedder struct{}

func (staticEmbedder) Name() string    { return "text-embedding-test" }
func (staticEmbedder) Dimensions() int { return 3 }
func (staticEmbedder) Embed(context.Context, []string) ([][]float64, error) {
	return [][]float64{{1, 0, 0}}, nil
}

type embeddingSearchTool struct {
	embedder interface {
		Embed(context.Context, []string) ([][]float64, error)
	}
}

func (embeddingSearchTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "knowledge_search", Desc: "测试知识库检索"}, nil
}

func (t embeddingSearchTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	_, err := t.embedder.Embed(ctx, []string{"测试"})
	return "完成", err
}
