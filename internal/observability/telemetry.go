package observability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const instrumentationName = "github.com/zhiruo/zora/internal/observability"

var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120}

// TelemetryConfig 只包含可观察性基础设施配置，不包含任何业务数据或密钥。
type TelemetryConfig struct {
	ServiceName       string
	ServiceVersion    string
	Environment       string
	TracingEnabled    bool
	OTLPEndpoint      string
	TraceSampleRatio  float64
	PrometheusEnabled bool
}

// Telemetry 统一持有 Trace、Metric 和上下文传播能力。
// 业务代码只依赖此对象，不直接依赖 Jaeger 或 Prometheus 的具体实现。
type Telemetry struct {
	tracer       trace.Tracer
	meter        metric.Meter
	propagator   propagation.TextMapPropagator
	metrics      metricInstruments
	traceCloser  func(context.Context) error
	metricCloser func(context.Context) error
	metricHTTP   http.Handler
}

type metricInstruments struct {
	httpRequests      metric.Int64Counter
	httpDuration      metric.Float64Histogram
	httpActive        metric.Int64UpDownCounter
	runs              metric.Int64Counter
	runDuration       metric.Float64Histogram
	modelCalls        metric.Int64Counter
	modelDuration     metric.Float64Histogram
	modelTokens       metric.Int64Counter
	embeddingCalls    metric.Int64Counter
	embeddingDuration metric.Float64Histogram
	embeddingInputs   metric.Int64Counter
	toolCalls         metric.Int64Counter
	toolDuration      metric.Float64Histogram
}

// NewTelemetry 按配置创建 OTLP Trace 导出器和 Prometheus Metric Reader。
// 两项均关闭时返回完整的 No-op 实现，调用方无需到处判断 nil。
func NewTelemetry(ctx context.Context, cfg TelemetryConfig) (*Telemetry, error) {
	cfg.ServiceName = strings.TrimSpace(cfg.ServiceName)
	if cfg.ServiceName == "" {
		cfg.ServiceName = "zora"
	}
	if cfg.TraceSampleRatio < 0 || cfg.TraceSampleRatio > 1 {
		return nil, fmt.Errorf("Trace 采样率必须在 0 到 1 之间")
	}
	if cfg.TracingEnabled && strings.TrimSpace(cfg.OTLPEndpoint) == "" {
		return nil, fmt.Errorf("启用 OTel Trace 时必须配置 OTLP Endpoint")
	}
	if cfg.TracingEnabled {
		endpoint, err := url.ParseRequestURI(strings.TrimSpace(cfg.OTLPEndpoint))
		if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
			return nil, fmt.Errorf("OTLP Endpoint 必须是包含主机的 http 或 https URL")
		}
	}

	res, err := resource.New(ctx, resource.WithAttributes(
		attribute.String("service.name", cfg.ServiceName),
		attribute.String("service.version", strings.TrimSpace(cfg.ServiceVersion)),
		attribute.String("deployment.environment.name", strings.TrimSpace(cfg.Environment)),
	))
	if err != nil {
		return nil, fmt.Errorf("创建 OTel Resource 失败：%w", err)
	}

	telemetry := &Telemetry{
		propagator: propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{}, propagation.Baggage{},
		),
	}
	if err := telemetry.configureTrace(ctx, cfg, res); err != nil {
		return nil, err
	}
	if err := telemetry.configureMetrics(cfg, res); err != nil {
		_ = telemetry.traceCloser(context.Background())
		return nil, err
	}
	if err := telemetry.createInstruments(); err != nil {
		_ = telemetry.Shutdown(context.Background())
		return nil, err
	}
	return telemetry, nil
}

func (t *Telemetry) configureTrace(ctx context.Context, cfg TelemetryConfig, res *resource.Resource) error {
	if !cfg.TracingEnabled {
		provider := tracenoop.NewTracerProvider()
		t.tracer = provider.Tracer(instrumentationName)
		t.traceCloser = func(context.Context) error { return nil }
		return nil
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(strings.TrimSpace(cfg.OTLPEndpoint)))
	if err != nil {
		return fmt.Errorf("创建 OTLP/HTTP Trace 导出器失败：%w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.TraceSampleRatio))),
		sdktrace.WithBatcher(exporter),
	)
	t.tracer = provider.Tracer(instrumentationName)
	t.traceCloser = provider.Shutdown
	// Eino 或后续第三方组件若使用 OTel 全局 API，也会加入同一条 Trace。
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(t.propagator)
	return nil
}

func (t *Telemetry) configureMetrics(cfg TelemetryConfig, res *resource.Resource) error {
	if !cfg.PrometheusEnabled {
		provider := metricnoop.NewMeterProvider()
		t.meter = provider.Meter(instrumentationName)
		t.metricCloser = func(context.Context) error { return nil }
		return nil
	}
	registry := promclient.NewRegistry()
	exporter, err := otelprometheus.New(otelprometheus.WithRegisterer(registry))
	if err != nil {
		return fmt.Errorf("创建 Prometheus Metric Reader 失败：%w", err)
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(exporter))
	t.meter = provider.Meter(instrumentationName)
	t.metricCloser = provider.Shutdown
	t.metricHTTP = promhttp.HandlerFor(registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
	otel.SetMeterProvider(provider)
	return nil
}

func (t *Telemetry) createInstruments() error {
	var err error
	if t.metrics.httpRequests, err = t.meter.Int64Counter("zora.http.server.requests", metric.WithUnit("{request}"), metric.WithDescription("HTTP 请求总数")); err != nil {
		return err
	}
	if t.metrics.httpDuration, err = t.meter.Float64Histogram("zora.http.server.duration", metric.WithUnit("s"), metric.WithDescription("HTTP 请求耗时"), metric.WithExplicitBucketBoundaries(latencyBuckets...)); err != nil {
		return err
	}
	if t.metrics.httpActive, err = t.meter.Int64UpDownCounter("zora.http.server.active_requests", metric.WithUnit("{request}"), metric.WithDescription("正在处理的 HTTP 请求数")); err != nil {
		return err
	}
	if t.metrics.runs, err = t.meter.Int64Counter("zora.agent.runs", metric.WithUnit("{run}"), metric.WithDescription("Agent Run 总数")); err != nil {
		return err
	}
	if t.metrics.runDuration, err = t.meter.Float64Histogram("zora.agent.run.duration", metric.WithUnit("s"), metric.WithDescription("Agent Run 耗时"), metric.WithExplicitBucketBoundaries(latencyBuckets...)); err != nil {
		return err
	}
	if t.metrics.modelCalls, err = t.meter.Int64Counter("zora.gen_ai.client.calls", metric.WithUnit("{call}"), metric.WithDescription("模型调用总数")); err != nil {
		return err
	}
	if t.metrics.modelDuration, err = t.meter.Float64Histogram("zora.gen_ai.client.duration", metric.WithUnit("s"), metric.WithDescription("模型调用耗时"), metric.WithExplicitBucketBoundaries(latencyBuckets...)); err != nil {
		return err
	}
	if t.metrics.modelTokens, err = t.meter.Int64Counter("zora.gen_ai.client.tokens", metric.WithUnit("{token}"), metric.WithDescription("Provider 返回的真实 Token Usage")); err != nil {
		return err
	}
	if t.metrics.embeddingCalls, err = t.meter.Int64Counter("zora.embedding.calls", metric.WithUnit("{call}"), metric.WithDescription("Embedding 调用总数")); err != nil {
		return err
	}
	if t.metrics.embeddingDuration, err = t.meter.Float64Histogram("zora.embedding.duration", metric.WithUnit("s"), metric.WithDescription("Embedding 调用耗时"), metric.WithExplicitBucketBoundaries(latencyBuckets...)); err != nil {
		return err
	}
	if t.metrics.embeddingInputs, err = t.meter.Int64Counter("zora.embedding.inputs", metric.WithUnit("{text}"), metric.WithDescription("Embedding 输入文本数")); err != nil {
		return err
	}
	if t.metrics.toolCalls, err = t.meter.Int64Counter("zora.tool.calls", metric.WithUnit("{call}"), metric.WithDescription("工具调用总数")); err != nil {
		return err
	}
	if t.metrics.toolDuration, err = t.meter.Float64Histogram("zora.tool.duration", metric.WithUnit("s"), metric.WithDescription("工具调用耗时"), metric.WithExplicitBucketBoundaries(latencyBuckets...)); err != nil {
		return err
	}
	return nil
}

// MetricsHandler 返回独立 Registry 的 Prometheus Handler；未启用时返回 nil。
func (t *Telemetry) MetricsHandler() http.Handler {
	if t == nil {
		return nil
	}
	return t.metricHTTP
}

// Shutdown 先停止 Metric Reader，再刷新并关闭 Trace Provider。
func (t *Telemetry) Shutdown(ctx context.Context) error {
	if t == nil {
		return nil
	}
	var metricErr, traceErr error
	if t.metricCloser != nil {
		metricErr = t.metricCloser(ctx)
	}
	if t.traceCloser != nil {
		traceErr = t.traceCloser(ctx)
	}
	return errors.Join(metricErr, traceErr)
}
