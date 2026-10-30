package frontendpipe

import (
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// resolvePreAdmissionRoute resolves the request's route from the inputs the
// pipeline can inspect before it admits decode resources: the frontend's own
// metadata-only resolver (a URL-path model, for example) and the route header.
//
// The route origin is the route's provenance: it is the default origin only
// when the proxy chose the route because the client named none. A client that
// names a route keeps it, even when it names the route the default would have.
//
// A route that depends on the request body is resolved separately, under the
// decode-admission permit: parsing a full request body is exactly the work that
// permit bounds, so it must never run before the permit is granted.
func resolvePreAdmissionRoute[Opts any](spec *Spec[Opts], headers http.Header, pm PathMatch) routing.ResolvedRoute {
	if spec.ResolvePreAdmissionRoute != nil {
		if resolved := spec.ResolvePreAdmissionRoute(headers, pm); strings.TrimSpace(resolved.Selector) != "" {
			return resolved
		}
	}
	header := strings.TrimSpace(spec.HTTPHeaders.RouteSelector(headers))
	if header == "" {
		return routing.ResolvedRoute{}
	}
	return routing.ExplicitRoute(header)
}

// resolveBodyModelRoute resolves the route from the body model when the request
// does not already name one. It must run under the decode-admission permit,
// together with the protocol decode it prepares.
func resolveBodyModelRoute[Opts any](spec *Spec[Opts], current routing.ResolvedRoute, body []byte) routing.ResolvedRoute {
	if current.Selector != "" || !spec.RouteFromBodyModel {
		return current
	}
	return spec.RoutePrefixes.ResolveFromModelOrDefault(body, spec.DefaultRouteSelector)
}
