package routing_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// TestRouteOrigin_carriesTheRouteProvenance proves the route origin travels
// with the request and distinguishes the routes the proxy chose from the ones
// the client named.
func TestRouteOrigin_carriesTheRouteProvenance(t *testing.T) {
	t.Parallel()
	if got := routing.RouteOriginFromContext(context.Background()); got != routing.RouteOriginUnset {
		t.Fatalf("origin = %q want unset", got)
	}
	ctx := routing.WithRouteOrigin(context.Background(), routing.RouteOriginDefault)
	if got := routing.RouteOriginFromContext(ctx); got != routing.RouteOriginDefault {
		t.Fatalf("origin = %q want default", got)
	}
	if !routing.RouteOriginDefault.IsDefaultRoute() {
		t.Fatal("the default origin must report itself as the default route")
	}
	for _, origin := range []routing.RouteOrigin{routing.RouteOriginUnset, routing.RouteOriginExplicit, routing.RouteOriginLineage} {
		if origin.IsDefaultRoute() {
			t.Fatalf("origin %q must not report itself as the default route", origin)
		}
	}
}

// TestRouteOrigin_anUnsetOriginIsNotRecorded proves an unestablished origin is
// not recorded: the request then uses the current request-bound registry.
func TestRouteOrigin_anUnsetOriginIsNotRecorded(t *testing.T) {
	t.Parallel()
	ctx := routing.WithRouteOrigin(context.Background(), routing.RouteOriginUnset)
	if got := routing.RouteOriginFromContext(ctx); got != routing.RouteOriginUnset {
		t.Fatalf("origin = %q want unset", got)
	}
}

// TestResolvedRoute_explicitAndDefaultRoutes proves the two route constructors
// report the origin the route actually has.
func TestResolvedRoute_explicitAndDefaultRoutes(t *testing.T) {
	t.Parallel()
	explicit := routing.ExplicitRoute(" wire-backend:good-a ")
	if explicit.Selector != "wire-backend:good-a" || explicit.Origin != routing.RouteOriginExplicit {
		t.Fatalf("explicit route = %+v", explicit)
	}
	def := routing.DefaultRoute(" wire-backend:good-a ")
	if def.Selector != "wire-backend:good-a" || def.Origin != routing.RouteOriginDefault {
		t.Fatalf("default route = %+v", def)
	}
	if empty := routing.ExplicitRoute("  "); empty.Origin != routing.RouteOriginUnset || empty.Selector != "" {
		t.Fatalf("empty route = %+v want an unset route", empty)
	}
	if emptyDef := routing.DefaultRoute("  "); emptyDef.Origin != routing.RouteOriginUnset || emptyDef.Selector != "" {
		t.Fatalf("empty default route = %+v want an unset route: an empty default is not a selected route", emptyDef)
	}
}
