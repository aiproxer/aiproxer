package runtimehost

import "context"

// ModelViewBinder optionally binds immutable model-registry and catalog
// publication views into a request context after generation lease acquisition
// and before the generation handler runs (req 9.4-9.5).
//
// Implementations capture generation-owned publications exactly once. Ordinary
// test request planes that do not implement this interface retain prior
// behavior (no model-view binding).
type ModelViewBinder interface {
	BindModelViews(ctx context.Context) context.Context
}

// BindModelViewsIfPresent invokes plane.BindModelViews when plane implements
// ModelViewBinder; otherwise returns ctx unchanged.
func BindModelViewsIfPresent(ctx context.Context, plane PublishedRequestPlane) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if plane == nil {
		return ctx
	}
	b, ok := plane.(ModelViewBinder)
	if !ok || b == nil {
		return ctx
	}
	return b.BindModelViews(ctx)
}
