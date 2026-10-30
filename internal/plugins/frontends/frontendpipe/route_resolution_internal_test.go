package frontendpipe

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
)

// TestResolveRoute_reportsTheOriginOfEveryRouteSource proves the pipeline
// resolves the request's route once and reports the origin that produced it.
//
// The origin is the route's provenance: only a route the proxy chose because
// the client named none is a default route. A client that names a route keeps
// it, even when it names the route the default would have.
func TestResolveRoute_reportsTheOriginOfEveryRouteSource(t *testing.T) {
	t.Parallel()
	prefixes := routeselect.NewPrefixSet([]string{"wire-backend"})
	newRequest := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	}
	newRequestWithRouteHeader := func() *http.Request {
		req := newRequest()
		req.Header.Set("X-AIP-Route", "wire-backend:named-model")
		return req
	}

	t.Run("route header", func(t *testing.T) {
		t.Parallel()
		spec := &Spec[struct{}]{}
		resolved := resolvePreAdmissionRoute(spec, newRequestWithRouteHeader().Header, PathMatch{})
		if resolved.Selector != "wire-backend:named-model" {
			t.Fatalf("selector = %q", resolved.Selector)
		}
		if resolved.Origin != routing.RouteOriginExplicit {
			t.Fatalf("origin = %q want explicit", resolved.Origin)
		}
	})

	t.Run("frontend resolver", func(t *testing.T) {
		t.Parallel()
		spec := &Spec[struct{}]{}
		spec.ResolvePreAdmissionRoute = func(_ http.Header, _ PathMatch) routing.ResolvedRoute {
			return routing.DefaultRoute("wire-backend:default-model")
		}
		resolved := resolvePreAdmissionRoute(spec, newRequest().Header, PathMatch{})
		if resolved.Selector != "wire-backend:default-model" {
			t.Fatalf("selector = %q", resolved.Selector)
		}
		if resolved.Origin != routing.RouteOriginDefault {
			t.Fatalf("origin = %q want default", resolved.Origin)
		}
	})

	t.Run("body model", func(t *testing.T) {
		t.Parallel()
		spec := &Spec[struct{}]{}
		spec.RoutePrefixes = prefixes
		spec.RouteFromBodyModel = true
		spec.DefaultRouteSelector = "wire-backend:default-model"
		body := []byte(`{"model":"plain-model"}`)
		resolved := resolvePreAdmissionRoute(spec, newRequest().Header, PathMatch{})
		// The body-dependent half runs under the decode-admission permit.
		resolved = resolveBodyModelRoute(spec, resolved, body)
		if resolved.Selector != "wire-backend:default-model" {
			t.Fatalf("selector = %q", resolved.Selector)
		}
		if resolved.Origin != routing.RouteOriginDefault {
			t.Fatalf("origin = %q want default", resolved.Origin)
		}
	})

	t.Run("body model with a known prefix", func(t *testing.T) {
		t.Parallel()
		spec := &Spec[struct{}]{}
		spec.RoutePrefixes = prefixes
		spec.RouteFromBodyModel = true
		spec.DefaultRouteSelector = "wire-backend:default-model"
		body := []byte(`{"model":"wire-backend:model-a"}`)
		resolved := resolvePreAdmissionRoute(spec, newRequest().Header, PathMatch{})
		// The body-dependent half runs under the decode-admission permit.
		resolved = resolveBodyModelRoute(spec, resolved, body)
		if resolved.Selector != "wire-backend:model-a" {
			t.Fatalf("selector = %q", resolved.Selector)
		}
		if resolved.Origin != routing.RouteOriginExplicit {
			t.Fatalf("origin = %q want explicit", resolved.Origin)
		}
	})
}
