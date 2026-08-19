package observability

import (
	"context"

	"github.com/zhiruo/zora/internal/knowledge"
)

type observedEmbedder struct {
	inner     knowledge.Embedder
	telemetry *Telemetry
	provider  string
}

func (t *Telemetry) WrapEmbedder(inner knowledge.Embedder, provider string) knowledge.Embedder {
	if inner == nil || t == nil {
		return inner
	}
	return &observedEmbedder{inner: inner, telemetry: t, provider: provider}
}

func (e *observedEmbedder) Name() string    { return e.inner.Name() }
func (e *observedEmbedder) Dimensions() int { return e.inner.Dimensions() }

func (e *observedEmbedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	callCtx, span, started := e.telemetry.StartEmbedding(ctx, e.provider, e.Name(), e.Dimensions(), len(texts))
	vectors, err := e.inner.Embed(callCtx, texts)
	e.telemetry.EndEmbedding(callCtx, span, started, e.provider, e.Name(), len(texts), err)
	return vectors, err
}
