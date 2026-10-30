package routing

import (
	"context"
	"strings"
)

// DefaultModelBinding is one canonical-to-native mapping a generation's
// derived default route needs.
//
// A backend instance discovers its models at runtime, so the mapping between
// the canonical id the route names and the id the backend serves can change
// when the inventory is refreshed. The generation keeps the mapping it was
// compiled with for the route it derived itself, so the route keeps addressing
// the model the backend published.
type DefaultModelBinding struct {
	// Backend is the backend instance the default route's leaf names.
	Backend string
	// Model is the canonical model the leaf names.
	Model string
	// Native is the id the backend serves the model as.
	Native string
}

// DefaultRouteBindings is the generation-owned set of canonical-to-native
// mappings its derived default route needs, one per leaf.
type DefaultRouteBindings []DefaultModelBinding

// Find returns the native id the backend serves the canonical model as, and
// whether the generation kept that mapping.
func (b DefaultRouteBindings) Find(backendID, model string) (string, bool) {
	backendID = strings.TrimSpace(backendID)
	model = strings.TrimSpace(model)
	for _, binding := range b {
		if strings.TrimSpace(binding.Backend) == backendID && strings.TrimSpace(binding.Model) == model {
			if native := strings.TrimSpace(binding.Native); native != "" {
				return native, true
			}
		}
	}
	return "", false
}

type defaultRouteBindingsKey struct{}

// WithDefaultRouteBindings attaches the generation's frozen model bindings.
func WithDefaultRouteBindings(ctx context.Context, bindings DefaultRouteBindings) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(bindings) == 0 {
		return ctx
	}
	return context.WithValue(ctx, defaultRouteBindingsKey{}, bindings)
}

// DefaultRouteBindingsFromContext returns the generation's frozen model
// bindings, and whether the request carries them.
func DefaultRouteBindingsFromContext(ctx context.Context) (DefaultRouteBindings, bool) {
	if ctx == nil {
		return nil, false
	}
	bindings, ok := ctx.Value(defaultRouteBindingsKey{}).(DefaultRouteBindings)
	if !ok || len(bindings) == 0 {
		return nil, false
	}
	return bindings, true
}

// FrozenDefaultModelResolver prefers the generation's frozen bindings for the
// leaves of a derived default route.
//
// It is used only for requests whose route origin is the default route: the
// proxy chose that route, so it keeps the mapping the generation was compiled
// with even when a later inventory refresh withdrew the model. Every other
// request uses the current request-bound registry, which stays authoritative
// for explicit routes: a remapped canonical id resolves to its current native
// id, and a canonical id that moved to another backend is rejected.
//
// A nil inner resolver answers with no binding for every model the frozen
// bindings do not own. The wire lane uses that form: it binds only the models
// the derived default route names and leaves every other route's model
// identity untouched.
type FrozenDefaultModelResolver struct {
	inner    NativeModelResolver
	bindings DefaultRouteBindings
}

// NewFrozenDefaultModelResolver returns a resolver that prefers the frozen
// bindings for the leaves of the derived default route.
func NewFrozenDefaultModelResolver(inner NativeModelResolver, bindings DefaultRouteBindings) NativeModelResolver {
	if len(bindings) == 0 {
		return inner
	}
	return &FrozenDefaultModelResolver{inner: inner, bindings: bindings}
}

// ResolveModelBinding implements [NativeModelResolver].
func (f *FrozenDefaultModelResolver) ResolveModelBinding(backendID, model string) ModelBinding {
	if f == nil {
		return ModelBinding{Kind: ModelBindingUnknown}
	}
	if native, ok := f.bindings.Find(backendID, model); ok {
		return ModelBinding{Kind: ModelBindingExactCanonical, Native: native}
	}
	if f.inner == nil {
		return ModelBinding{Kind: ModelBindingUnknown}
	}
	return f.inner.ResolveModelBinding(backendID, model)
}

// DefaultRouteModelResolver returns the resolver a request must use for its
// route.
//
// The request-bound registry is authoritative for every route. The
// generation's frozen bindings are consulted only for a request whose route
// origin is the generation's derived default route: the proxy chose that
// route, so it keeps the mapping it was compiled with.
func DefaultRouteModelResolver(ctx context.Context, inner NativeModelResolver) NativeModelResolver {
	if inner == nil {
		return nil
	}
	if !RouteOriginFromContext(ctx).IsDefaultRoute() {
		return inner
	}
	bindings, ok := DefaultRouteBindingsFromContext(ctx)
	if !ok {
		return inner
	}
	return NewFrozenDefaultModelResolver(inner, bindings)
}

// FrozenDefaultRouteModelResolver returns the resolver the wire lane uses for
// a derived default route: the generation's frozen bindings, and no other
// binding.
//
// The wire lane resolves a route without the request-bound registry, so the
// only model identity it may add is the one the generation's own default route
// needs. Every other route keeps the model identity the lane resolved on its
// own.
func FrozenDefaultRouteModelResolver(bindings DefaultRouteBindings) NativeModelResolver {
	if len(bindings) == 0 {
		return nil
	}
	return NewFrozenDefaultModelResolver(nil, bindings)
}
