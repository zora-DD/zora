package observability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/zhiruo/zora/internal/background"
	"github.com/zhiruo/zora/internal/memory"
)

func (t *Telemetry) StartHTTP(r *http.Request) (*http.Request, trace.Span, time.Time) {
	ctx := t.propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := t.tracer.Start(ctx, "HTTP "+r.Method,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("url.path", r.URL.Path),
		),
	)
	t.metrics.httpActive.Add(ctx, 1, metric.WithAttributes(attribute.String("http.request.method", r.Method)))
	return r.WithContext(ctx), span, time.Now()
}

func (t *Telemetry) EndHTTP(ctx context.Context, span trace.Span, started time.Time, method, route string, statusCode int) {
	route = strings.TrimSpace(route)
	// Go 1.22+ 的 Request.Pattern 形如 "POST /path/{id}"；Method 已是独立 Attribute，避免重复。
	route = strings.TrimPrefix(route, method+" ")
	if route == "" {
		route = "unmatched"
	}
	span.SetName(fmt.Sprintf("HTTP %s %s", method, route))
	span.SetAttributes(
		attribute.String("http.route", route),
		attribute.Int("http.response.status_code", statusCode),
	)
	if statusCode >= http.StatusInternalServerError {
		span.SetStatus(codes.Error, http.StatusText(statusCode))
	}
	attrs := metric.WithAttributes(
		attribute.String("http.request.method", method),
		attribute.String("http.route", route),
		attribute.String("http.response.status_code", strconv.Itoa(statusCode)),
	)
	t.metrics.httpRequests.Add(ctx, 1, attrs)
	t.metrics.httpDuration.Record(ctx, time.Since(started).Seconds(), attrs)
	t.metrics.httpActive.Add(ctx, -1, metric.WithAttributes(attribute.String("http.request.method", method)))
	span.End()
}

func (t *Telemetry) StartRun(ctx context.Context, runID, conversationID, agentName, provider, model string) (context.Context, trace.Span, time.Time) {
	ctx, span := t.tracer.Start(ctx, "agent.run", trace.WithAttributes(
		attribute.String("zora.run.id", runID),
		attribute.String("zora.conversation.id", conversationID),
		attribute.String("gen_ai.agent.name", agentName),
		attribute.String("gen_ai.provider.name", provider),
		attribute.String("gen_ai.request.model", model),
	))
	return ctx, span, time.Now()
}

func (t *Telemetry) EndRun(ctx context.Context, span trace.Span, started time.Time, provider, model, runStatus string, runErr error) {
	status := runStatus
	if status == "" || status == "running" {
		status = "failed"
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			status = "cancelled"
		}
	}
	span.SetAttributes(attribute.String("zora.run.status", status))
	setSpanError(span, runErr)
	attrs := metric.WithAttributes(
		attribute.String("zora.run.status", status),
		attribute.String("gen_ai.provider.name", provider),
		attribute.String("gen_ai.request.model", model),
	)
	t.metrics.runs.Add(ctx, 1, attrs)
	t.metrics.runDuration.Record(ctx, time.Since(started).Seconds(), attrs)
	span.End()
}

func (t *Telemetry) StartModel(ctx context.Context, provider, model string, inputMessages int) (context.Context, trace.Span, time.Time) {
	ctx, span := t.tracer.Start(ctx, "gen_ai.chat", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.provider.name", provider),
		attribute.String("gen_ai.request.model", model),
		attribute.Int("gen_ai.input.messages", inputMessages),
	))
	return ctx, span, time.Now()
}

func (t *Telemetry) EndModel(ctx context.Context, span trace.Span, started time.Time, provider, model string, usage ModelTokenUsage, callErr error) {
	status := statusLabel(callErr)
	span.SetAttributes(attribute.String("zora.call.status", status))
	if usage.Reported {
		span.SetAttributes(
			attribute.Int("gen_ai.usage.input_tokens", usage.PromptTokens),
			attribute.Int("gen_ai.usage.output_tokens", usage.CompletionTokens),
		)
	}
	setSpanError(span, callErr)
	attrs := metric.WithAttributes(
		attribute.String("gen_ai.provider.name", provider),
		attribute.String("gen_ai.request.model", model),
		attribute.String("zora.call.status", status),
	)
	t.metrics.modelCalls.Add(ctx, 1, attrs)
	t.metrics.modelDuration.Record(ctx, time.Since(started).Seconds(), attrs)
	if usage.Reported {
		t.recordTokens(ctx, provider, model, usage)
	}
	span.End()
}

type ModelTokenUsage struct {
	Reported         bool
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	ReasoningTokens  int
}

func (t *Telemetry) recordTokens(ctx context.Context, provider, model string, usage ModelTokenUsage) {
	values := []struct {
		kind  string
		value int
	}{
		{kind: "prompt", value: usage.PromptTokens},
		{kind: "completion", value: usage.CompletionTokens},
		{kind: "total", value: usage.TotalTokens},
		{kind: "cached", value: usage.CachedTokens},
		{kind: "reasoning", value: usage.ReasoningTokens},
	}
	for _, item := range values {
		if item.value <= 0 {
			continue
		}
		t.metrics.modelTokens.Add(ctx, int64(item.value), metric.WithAttributes(
			attribute.String("gen_ai.provider.name", provider),
			attribute.String("gen_ai.request.model", model),
			attribute.String("gen_ai.token.type", item.kind),
		))
	}
}

func (t *Telemetry) StartEmbedding(ctx context.Context, provider, model string, dimensions, inputs int) (context.Context, trace.Span, time.Time) {
	ctx, span := t.tracer.Start(ctx, "embedding.generate", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("gen_ai.operation.name", "embeddings"),
		attribute.String("gen_ai.provider.name", provider),
		attribute.String("gen_ai.request.model", model),
		attribute.Int("zora.embedding.dimensions", dimensions),
		attribute.Int("zora.embedding.input_count", inputs),
	))
	return ctx, span, time.Now()
}

func (t *Telemetry) EndEmbedding(ctx context.Context, span trace.Span, started time.Time, provider, model string, inputs int, callErr error) {
	status := statusLabel(callErr)
	span.SetAttributes(attribute.String("zora.call.status", status))
	setSpanError(span, callErr)
	attrs := metric.WithAttributes(
		attribute.String("gen_ai.provider.name", provider),
		attribute.String("gen_ai.request.model", model),
		attribute.String("zora.call.status", status),
	)
	t.metrics.embeddingCalls.Add(ctx, 1, attrs)
	t.metrics.embeddingDuration.Record(ctx, time.Since(started).Seconds(), attrs)
	if inputs > 0 {
		t.metrics.embeddingInputs.Add(ctx, int64(inputs), metric.WithAttributes(
			attribute.String("gen_ai.provider.name", provider),
			attribute.String("gen_ai.request.model", model),
		))
	}
	span.End()
}

func (t *Telemetry) StartTool(ctx context.Context, name string, argumentRunes int) (context.Context, trace.Span, time.Time) {
	ctx, span := t.tracer.Start(ctx, "tool."+name, trace.WithAttributes(
		attribute.String("gen_ai.tool.name", name),
		attribute.Int("zora.tool.argument_characters", argumentRunes),
	))
	return ctx, span, time.Now()
}

func (t *Telemetry) EndTool(ctx context.Context, span trace.Span, started time.Time, name string, callErr error) {
	status := statusLabel(callErr)
	span.SetAttributes(attribute.String("zora.call.status", status))
	setSpanError(span, callErr)
	attrs := metric.WithAttributes(
		attribute.String("gen_ai.tool.name", name),
		attribute.String("zora.call.status", status),
	)
	t.metrics.toolCalls.Add(ctx, 1, attrs)
	t.metrics.toolDuration.Record(ctx, time.Since(started).Seconds(), attrs)
	span.End()
}

// TraceParent 序列化当前 Trace 上下文供事务 Outbox 持久化。
// 只保存标准 traceparent，不保存用户内容、密钥或 Baggage。
func (t *Telemetry) TraceParent(ctx context.Context) string {
	if t == nil || t.propagator == nil {
		return ""
	}
	carrier := propagation.MapCarrier{}
	t.propagator.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// BeginMemoryCaptureJob 将后台 Worker Span 接回产生任务的原 Trace。
func (t *Telemetry) BeginMemoryCaptureJob(ctx context.Context, job memory.CaptureJob) (context.Context, func(status string, err error)) {
	if t == nil {
		return ctx, func(string, error) {}
	}
	if job.TraceParent != "" {
		ctx = t.propagator.Extract(ctx, propagation.MapCarrier{"traceparent": job.TraceParent})
	}
	ctx, span := t.tracer.Start(ctx, "memory.capture", trace.WithSpanKind(trace.SpanKindConsumer), trace.WithAttributes(
		attribute.String("zora.memory.job.id", job.ID),
		attribute.String("zora.run.id", job.RunID),
		attribute.Int("zora.memory.job.attempt", job.Attempt),
	))
	started := time.Now()
	if !job.CreatedAt.IsZero() {
		t.metrics.memoryJobQueueDelay.Record(ctx, time.Since(job.CreatedAt).Seconds())
	}
	return ctx, func(status string, jobErr error) {
		span.SetAttributes(attribute.String("zora.memory.job.status", status))
		setSpanError(span, jobErr)
		attrs := metric.WithAttributes(attribute.String("zora.memory.job.status", status))
		t.metrics.memoryJobs.Add(ctx, 1, attrs)
		t.metrics.memoryJobDuration.Record(ctx, time.Since(started).Seconds(), attrs)
		span.End()
	}
}

// BeginBackgroundJob 为文档摄取、会话摘要创建 Consumer Span。
// kind 是固定枚举，可安全用于 Prometheus Label；任务 ID 仅进入 Trace，避免指标基数爆炸。
func (t *Telemetry) BeginBackgroundJob(ctx context.Context, job background.Job) (context.Context, func(status string, err error)) {
	if t == nil {
		return ctx, func(string, error) {}
	}
	if job.TraceParent != "" {
		ctx = t.propagator.Extract(ctx, propagation.MapCarrier{"traceparent": job.TraceParent})
	}
	ctx, span := t.tracer.Start(ctx, "background."+job.Kind, trace.WithSpanKind(trace.SpanKindConsumer), trace.WithAttributes(
		attribute.String("zora.background.job.id", job.ID),
		attribute.String("zora.background.job.kind", job.Kind),
		attribute.String("zora.run.id", job.RunID),
		attribute.String("zora.conversation.id", job.ConversationID),
		attribute.Int("zora.background.job.attempt", job.Attempt),
	))
	started := time.Now()
	if !job.CreatedAt.IsZero() {
		t.metrics.backgroundJobQueueDelay.Record(ctx, time.Since(job.CreatedAt).Seconds(), metric.WithAttributes(
			attribute.String("zora.background.job.kind", job.Kind),
		))
	}
	return ctx, func(status string, jobErr error) {
		span.SetAttributes(attribute.String("zora.background.job.status", status))
		setSpanError(span, jobErr)
		attrs := metric.WithAttributes(
			attribute.String("zora.background.job.kind", job.Kind),
			attribute.String("zora.background.job.status", status),
		)
		t.metrics.backgroundJobs.Add(ctx, 1, attrs)
		t.metrics.backgroundJobDuration.Record(ctx, time.Since(started).Seconds(), attrs)
		span.End()
	}
}

func TraceIDs(ctx context.Context) (traceID, spanID string) {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return "", ""
	}
	return spanContext.TraceID().String(), spanContext.SpanID().String()
}

func setSpanError(span trace.Span, err error) {
	if err == nil {
		return
	}
	// Provider/Tool 错误可能包含上游响应或用户数据；Trace 只记录错误类型，不写原始错误正文。
	span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
	span.SetStatus(codes.Error, "调用失败")
}

func statusLabel(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled"
	}
	return "error"
}
