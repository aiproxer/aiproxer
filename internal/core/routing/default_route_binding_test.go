package routing_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// currentRegistryResolver is a request-bound registry that knows one model.
type currentRegistryResolver struct {
	rows map[string]string // "backend\x00model" -> native
}

func (c currentRegistryResolver) ResolveModelBinding(backendID, model string) routing.ModelBinding {
	if native, ok := c.rows[backendID+"\x00"+model]; ok {
		return routing.ModelBinding{Kind: routing.ModelBindingExactCanonical, Native: native}
	}
	return routing.ModelBinding{Kind: routing.ModelBindingUnknown}
}

// TestFrozenDefaultModelResolver_prefersTheFrozenBinding proves the
// generation's frozen binding wins for the leaf of its derived default route.
// The proxy chose that route, so it keeps the mapping it was compiled with even
// when the inventory moved on.
func TestFrozenDefaultModelResolver_prefersTheFrozenBinding(t *testing.T) {
	t.Parallel()
	inner := currentRegistryResolver{rows: map[string]string{
		"backend\x00vendor/a": "native-v2",
	}}
	resolver := routing.NewFrozenDefaultModelResolver(inner, routing.DefaultRouteBindings{{
		Backend: "backend",
		Model:   "vendor/a",
		Native:  "native-v1",
	}})
	got := resolver.ResolveModelBinding("backend", "vendor/a")
	if got.Kind != routing.ModelBindingExactCanonical || got.Native != "native-v1" {
		t.Fatalf("binding = %+v want the frozen native-v1", got)
	}
}

// TestFrozenDefaultModelResolver_usesTheCurrentRegistryForOtherRoutes proves
// the frozen binding never overrides the current registry for a route it does
// not own.
func TestFrozenDefaultModelResolver_usesTheCurrentRegistryForOtherRoutes(t *testing.T) {
	t.Parallel()
	inner := currentRegistryResolver{rows: map[string]string{
		"backend\x00vendor/b": "native-b",
	}}
	resolver := routing.NewFrozenDefaultModelResolver(inner, routing.DefaultRouteBindings{{
		Backend: "backend",
		Model:   "vendor/a",
		Native:  "native-v1",
	}})
	got := resolver.ResolveModelBinding("backend", "vendor/b")
	if got.Kind != routing.ModelBindingExactCanonical || got.Native != "native-b" {
		t.Fatalf("binding = %+v want the current native-b", got)
	}
	got = resolver.ResolveModelBinding("backend", "vendor/unknown")
	if got.Kind != routing.ModelBindingUnknown {
		t.Fatalf("binding = %+v want unknown", got)
	}
}

// TestFrozenDefaultModelResolver_keepsTheCurrentWrongBackendRejection proves
// the frozen binding never resurrects a mapping the current registry rejects.
// The executor's wrong-backend protection stays authoritative.
func TestFrozenDefaultModelResolver_keepsTheCurrentWrongBackendRejection(t *testing.T) {
	t.Parallel()
	inner := wrongBackendResolver{}
	resolver := routing.NewFrozenDefaultModelResolver(inner, routing.DefaultRouteBindings{{
		Backend: "backend-a",
		Model:   "vendor/a",
		Native:  "native-v1",
	}})
	got := resolver.ResolveModelBinding("backend-b", "vendor/a")
	if got.Kind != routing.ModelBindingWrongBackend {
		t.Fatalf("binding = %+v want the wrong-backend rejection", got)
	}
}

// wrongBackendResolver rejects every model: the canonical id belongs to
// another backend.
type wrongBackendResolver struct{}

func (wrongBackendResolver) ResolveModelBinding(string, string) routing.ModelBinding {
	return routing.ModelBinding{Kind: routing.ModelBindingWrongBackend}
}

// TestDefaultRouteModelResolver_usesTheFrozenBindingsOnlyForTheDefaultRoute
// proves the request-bound registry stays authoritative for every route except
// the one the proxy chose itself.
//
// The generation's frozen bindings are the mapping its own default route needs.
// A request whose route origin is that default route keeps them; a request the
// client routed, and a request whose route came from a continuation parent's
// lineage, use the current registry.
func TestDefaultRouteModelResolver_usesTheFrozenBindingsOnlyForTheDefaultRoute(t *testing.T) {
	t.Parallel()
	inner := currentRegistryResolver{rows: map[string]string{
		"backend\x00vendor/a": "native-v2",
	}}
	bindings := routing.DefaultRouteBindings{{
		Backend: "backend",
		Model:   "vendor/a",
		Native:  "native-v1",
	}}
	for name, origin := range map[string]routing.RouteOrigin{
		"explicit": routing.RouteOriginExplicit,
		"lineage":  routing.RouteOriginLineage,
		"unset":    routing.RouteOriginUnset,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := routing.WithRouteOrigin(context.Background(), origin)
			resolver := routing.DefaultRouteModelResolver(ctx, inner)
			got := resolver.ResolveModelBinding("backend", "vendor/a")
			if got.Kind != routing.ModelBindingExactCanonical || got.Native != "native-v2" {
				t.Fatalf("binding = %+v want the current native-v2", got)
			}
		})
	}
	t.Run("default route", func(t *testing.T) {
		t.Parallel()
		ctx := routing.WithRouteOrigin(context.Background(), routing.RouteOriginDefault)
		ctx = routing.WithDefaultRouteBindings(ctx, bindings)
		resolver := routing.DefaultRouteModelResolver(ctx, inner)
		got := resolver.ResolveModelBinding("backend", "vendor/a")
		if got.Kind != routing.ModelBindingExactCanonical || got.Native != "native-v1" {
			t.Fatalf("binding = %+v want the frozen native-v1", got)
		}
	})
}

// TestFrozenDefaultRouteModelResolver_bindsOnlyTheFrozenModels proves the wire
// lane's resolver binds the models the derived default route names and leaves
// every other model identity untouched.
func TestFrozenDefaultRouteModelResolver_bindsOnlyTheFrozenModels(t *testing.T) {
	t.Parallel()
	resolver := routing.FrozenDefaultRouteModelResolver(routing.DefaultRouteBindings{{
		Backend: "backend",
		Model:   "vendor/a",
		Native:  "native-a",
	}})
	if resolver == nil {
		t.Fatal("expected a resolver for the frozen bindings")
	}
	got := resolver.ResolveModelBinding("backend", "vendor/a")
	if got.Kind != routing.ModelBindingExactCanonical || got.Native != "native-a" {
		t.Fatalf("binding = %+v want the frozen native-a", got)
	}
	other := resolver.ResolveModelBinding("backend", "vendor/b")
	if other.Kind != routing.ModelBindingUnknown {
		t.Fatalf("binding = %+v want unknown", other)
	}
	if routing.FrozenDefaultRouteModelResolver(nil) != nil {
		t.Fatal("expected no resolver without frozen bindings")
	}
}

// TestRouteOrigin_isCarriedByTheRequestContext proves the route origin travels
// with the request: only a request whose route the proxy chose may keep the
// generation's frozen binding.
func TestRouteOrigin_isCarriedByTheRequestContext(t *testing.T) {
	t.Parallel()
	if routing.RouteOriginFromContext(context.Background()) != routing.RouteOriginUnset {
		t.Fatal("a plain context must carry no route origin")
	}
	ctx := routing.WithRouteOrigin(context.Background(), routing.RouteOriginDefault)
	if routing.RouteOriginFromContext(ctx) != routing.RouteOriginDefault {
		t.Fatal("the route origin must be readable from the request context")
	}
	if !routing.RouteOriginFromContext(ctx).IsDefaultRoute() {
		t.Fatal("the default route origin must report itself as the default route")
	}
	lineage := routing.WithRouteOrigin(context.Background(), routing.RouteOriginLineage)
	if routing.RouteOriginFromContext(lineage).IsDefaultRoute() {
		t.Fatal("a lineage route origin must not report itself as the default route")
	}
}

// TestDefaultRouteBindings_find proves the frozen bindings are found by their
// backend and model, and that an empty native id is not a binding.
func TestDefaultRouteBindings_find(t *testing.T) {
	t.Parallel()
	bindings := routing.DefaultRouteBindings{
		{Backend: "backend", Model: "vendor/a", Native: "native-a"},
		{Backend: "backend", Model: "vendor/b"},
	}
	if native, ok := bindings.Find("backend", "vendor/a"); !ok || native != "native-a" {
		t.Fatalf("find = %q ok=%v want native-a", native, ok)
	}
	if _, ok := bindings.Find("backend", "vendor/b"); ok {
		t.Fatal("a binding without a native id must not resolve")
	}
	if _, ok := bindings.Find("other", "vendor/a"); ok {
		t.Fatal("a binding must not resolve for another backend")
	}
}
