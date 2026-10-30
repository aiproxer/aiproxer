package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// TestWireRouteModelResolver_bindsOnlyTheDerivedDefaultRoute proves the wire
// lane binds the model of a derived default route with the generation's frozen
// binding, and leaves every other route's model identity untouched.
//
// The wire lane resolves a route without the request-bound registry, so the
// only model identity it may add is the one the generation's own default route
// needs.
func TestWireRouteModelResolver_bindsOnlyTheDerivedDefaultRoute(t *testing.T) {
	t.Parallel()
	bindings := routing.DefaultRouteBindings{{
		Backend: "backend",
		Model:   "vendor/a",
		Native:  "native-a",
	}}

	t.Run("default route", func(t *testing.T) {
		t.Parallel()
		ctx := routing.WithRouteOrigin(context.Background(), routing.RouteOriginDefault)
		ctx = routing.WithDefaultRouteBindings(ctx, bindings)
		resolver := wireRouteModelResolver(ctx)
		if resolver == nil {
			t.Fatal("expected a resolver for the derived default route")
		}
		got := resolver.ResolveModelBinding("backend", "vendor/a")
		if got.Kind != routing.ModelBindingExactCanonical || got.Native != "native-a" {
			t.Fatalf("binding = %+v want the frozen native-a", got)
		}
		other := resolver.ResolveModelBinding("backend", "vendor/b")
		if other.Kind != routing.ModelBindingUnknown {
			t.Fatalf("binding = %+v want unknown", other)
		}
	})

	for name, origin := range map[string]routing.RouteOrigin{
		"explicit": routing.RouteOriginExplicit,
		"lineage":  routing.RouteOriginLineage,
		"unset":    routing.RouteOriginUnset,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := routing.WithRouteOrigin(context.Background(), origin)
			ctx = routing.WithDefaultRouteBindings(ctx, bindings)
			if resolver := wireRouteModelResolver(ctx); resolver != nil {
				t.Fatalf("resolver = %+v want none for a %s route", resolver, origin)
			}
		})
	}
}
