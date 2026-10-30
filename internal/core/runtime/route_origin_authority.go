package runtime

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// finalRouteOrigin returns the route origin that describes the route the
// request actually runs.
//
// The frontend established the origin when it resolved the route, but the core
// can still replace that route afterwards: an administrator's route override,
// or a request hook that rewrites the selector. A replaced route belongs to
// that later authority, so it must not keep the default route's generation-frozen
// model binding. The authority decides, not the text: an override that selects
// exactly the expression the frontend would have selected is still an override,
// and the current request-bound registry stays authoritative for it.
func finalRouteOrigin(
	frontendOrigin routing.RouteOrigin,
	frontendSelector string,
	effectiveSelector string,
	overridden bool,
) routing.RouteOrigin {
	if overridden || strings.TrimSpace(effectiveSelector) != strings.TrimSpace(frontendSelector) {
		return routing.RouteOriginOverride
	}
	return frontendOrigin
}
