package compatmode

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ApplyCapabilityCeiling keeps model-aware family resolution from elevating a
// validated profile above its declared effective capability surface.
func ApplyCapabilityCeiling(be execbackend.Backend, ceiling aipapi.BackendCaps) execbackend.Backend {
	be.Caps = ceiling
	base := be.ResolveCaps
	be.ResolveCaps = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
		resolved := ceiling
		if base != nil {
			resolved = base(ctx, call, cand)
		}
		out := aipapi.NewBackendCaps()
		for capability := range resolved {
			if _, ok := ceiling[capability]; ok {
				out[capability] = struct{}{}
			}
		}
		return out
	}
	return be
}
