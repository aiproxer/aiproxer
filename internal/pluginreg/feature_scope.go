package pluginreg

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
)

type featureInstanceIDProvider interface {
	FeatureInstanceID() string
}

type scopedRequestTransform struct {
	instanceID string
	inner      request.Transform
}

func (w scopedRequestTransform) FeatureInstanceID() string { return w.instanceID }
func (w scopedRequestTransform) ID() string                { return w.inner.ID() }
func (w scopedRequestTransform) Order() int                { return w.inner.Order() }
func (w scopedRequestTransform) FailureMode() sdkhooks.FailureMode {
	return w.inner.FailureMode()
}

func (w scopedRequestTransform) Handle(ctx context.Context, call *aipapi.Call, meta request.RequestMeta, svc request.Services) error {
	return w.inner.Handle(ctx, call, meta, svc)
}

type scopedToolCatalogFilter struct {
	instanceID string
	inner      toolcatalog.Filter
}

func (w scopedToolCatalogFilter) FeatureInstanceID() string { return w.instanceID }
func (w scopedToolCatalogFilter) ID() string                { return w.inner.ID() }
func (w scopedToolCatalogFilter) Order() int                { return w.inner.Order() }
func (w scopedToolCatalogFilter) FailureMode() sdkhooks.FailureMode {
	return w.inner.FailureMode()
}

func (w scopedToolCatalogFilter) Handle(ctx context.Context, call *aipapi.Call, meta toolcatalog.CatalogMeta, svc toolcatalog.Services) error {
	return w.inner.Handle(ctx, call, meta, svc)
}

type scopedCompletionGate struct {
	instanceID string
	inner      completion.Gate
}

func (w scopedCompletionGate) FeatureInstanceID() string { return w.instanceID }
func (w scopedCompletionGate) ID() string                { return w.inner.ID() }
func (w scopedCompletionGate) Order() int                { return w.inner.Order() }
func (w scopedCompletionGate) FailureMode() sdkhooks.FailureMode {
	return w.inner.FailureMode()
}

func (w scopedCompletionGate) Handle(ctx context.Context, meta completion.Meta, buf completion.Buffered, svc completion.Services) (completion.Outcome, error) {
	return w.inner.Handle(ctx, meta, buf, svc)
}

func wrapRequestTransforms(instanceID string, in []request.Transform) []request.Transform {
	if len(in) == 0 {
		return nil
	}
	out := make([]request.Transform, 0, len(in))
	for _, tr := range in {
		if tr == nil {
			continue
		}
		out = append(out, scopedRequestTransform{instanceID: instanceID, inner: tr})
	}
	return out
}

func wrapToolCatalogFilters(instanceID string, in []toolcatalog.Filter) []toolcatalog.Filter {
	if len(in) == 0 {
		return nil
	}
	out := make([]toolcatalog.Filter, 0, len(in))
	for _, f := range in {
		if f == nil {
			continue
		}
		out = append(out, scopedToolCatalogFilter{instanceID: instanceID, inner: f})
	}
	return out
}

func wrapCompletionGates(instanceID string, in []completion.Gate) []completion.Gate {
	if len(in) == 0 {
		return nil
	}
	out := make([]completion.Gate, 0, len(in))
	for _, g := range in {
		if g == nil {
			continue
		}
		out = append(out, scopedCompletionGate{instanceID: instanceID, inner: g})
	}
	return out
}
