package stdhttp

import (
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

// DefaultRouteSelector returns the routing selector used by frontends when X-AIP-Route is absent.
// It delegates to config.EffectiveDefaultRouteSelector with standardplugins.DefaultWireModel so default
// models are registry-owned, not duplicated in frontend handlers.
//
// Prefer using [runtimebundle.CandidateHTTPCompile.EffectiveDefaultRoute] so HTTP wiring shares the
// same wire-model selection as the executor (including optional [runtimebundle.BuildOptions.WireModel]).
func DefaultRouteSelector(cfg *config.Config) string {
	return config.EffectiveDefaultRouteSelector(cfg, standardplugins.DefaultWireModel)
}
