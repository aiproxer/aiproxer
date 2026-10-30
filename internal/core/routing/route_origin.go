package routing

import (
	"context"
	"strings"
)

// RouteOrigin names why a request's route selector was chosen.
//
// The origin is the route's provenance. It decides which model binding a
// request may use: only a route the proxy chose itself, because the client
// named none, may keep the generation's frozen binding. Every other route —
// one the client named, one restored from a continuation parent's lineage —
// uses the current request-bound registry, which stays authoritative for
// explicit traffic.
//
// The origin is never reconstructed from the selector text: a client may
// intentionally name the route the proxy would have chosen, and that request
// is an explicit one.
type RouteOrigin string

const (
	// RouteOriginUnset is the zero value: the origin was not established. The
	// request uses the current request-bound registry.
	RouteOriginUnset RouteOrigin = ""
	// RouteOriginExplicit is the origin of a route the client named.
	RouteOriginExplicit RouteOrigin = "explicit"
	// RouteOriginDefault is the origin of the route the proxy chose because
	// the client named none.
	RouteOriginDefault RouteOrigin = "default"
	// RouteOriginOverride is the origin of a route a later core authority
	// selected: an administrator's route override, or a request hook that
	// rewrote the selector. The route belongs to that authority, not to the
	// frontend that first resolved one.
	RouteOriginOverride RouteOrigin = "override"
	// RouteOriginLineage is the origin of a route restored from a
	// continuation parent's lineage.
	RouteOriginLineage RouteOrigin = "lineage"
)

// IsDefaultRoute reports whether the origin is the proxy's own default route.
func (o RouteOrigin) IsDefaultRoute() bool {
	return o == RouteOriginDefault
}

// ResolvedRoute is a route selector together with the origin that produced it.
type ResolvedRoute struct {
	// Selector is the resolved route selector.
	Selector string
	// Origin names why the selector was chosen.
	Origin RouteOrigin
}

// ExplicitRoute returns the resolved route as an explicit one: the client (or
// an operator default) named it. An empty selector names no route, and the
// origin stays unset.
func ExplicitRoute(selector string) ResolvedRoute {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return ResolvedRoute{}
	}
	return ResolvedRoute{Selector: selector, Origin: RouteOriginExplicit}
}

// DefaultRoute returns the resolved route as the proxy's own default route: it
// was chosen because the client named none. An empty selector names no route,
// and the origin stays unset: an empty configured default is not a route the
// proxy selected.
func DefaultRoute(selector string) ResolvedRoute {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return ResolvedRoute{}
	}
	return ResolvedRoute{Selector: selector, Origin: RouteOriginDefault}
}

type routeOriginKey struct{}

// WithRouteOrigin records why the request's route was chosen.
func WithRouteOrigin(ctx context.Context, origin RouteOrigin) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if origin == RouteOriginUnset {
		return ctx
	}
	return context.WithValue(ctx, routeOriginKey{}, origin)
}

// RouteOriginFromContext returns why the request's route was chosen. It is
// [RouteOriginUnset] when the origin was never established, and the request
// then uses the current request-bound registry.
func RouteOriginFromContext(ctx context.Context) RouteOrigin {
	if ctx == nil {
		return RouteOriginUnset
	}
	origin, ok := ctx.Value(routeOriginKey{}).(RouteOrigin)
	if !ok {
		return RouteOriginUnset
	}
	return origin
}
