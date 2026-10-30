package runtime

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// wireRouteModelResolver returns the resolver the wire lane composes a route
// with.
//
// The wire lane resolves a route without the request-bound registry, so the
// only model identity it adds is the one the generation's own default route
// needs: a request whose route origin is that default route keeps the
// generation's frozen bindings, so it addresses the model the backend
// published even after an inventory refresh withdrew it. Every other route
// keeps the model identity the lane resolved on its own.
func wireRouteModelResolver(ctx context.Context) routing.NativeModelResolver {
	if !routing.RouteOriginFromContext(ctx).IsDefaultRoute() {
		return nil
	}
	bindings, ok := routing.DefaultRouteBindingsFromContext(ctx)
	if !ok {
		return nil
	}
	return routing.FrozenDefaultRouteModelResolver(bindings)
}
