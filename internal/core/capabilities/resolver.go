package capabilities

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Resolver resolves effective backend capabilities for one routing candidate before upstream
// negotiation (requirement 7.1). Composition wires a registry-backed implementation without
// importing concrete backend plugins from core orchestration.
type Resolver interface {
	DescribeCandidate(ctx context.Context, cand routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps
}

// BackendCapsFunc resolves caps for a single backend id; used to build MapResolver at composition.
type BackendCapsFunc func(ctx context.Context, cand routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps

// MapResolver dispatches to a per-backend-id function (typically wrapping per-plugin ResolveCaps shims
// from the executor backend seam in internal/core/execbackend).
type MapResolver map[string]BackendCapsFunc

// DescribeCandidate implements Resolver.
func (m MapResolver) DescribeCandidate(ctx context.Context, cand routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps {
	if m == nil {
		return aipapi.BackendCaps{}
	}
	id := cand.Primary.Backend
	fn := m[id]
	if fn == nil {
		return aipapi.BackendCaps{}
	}
	return fn(ctx, cand, call)
}

var _ Resolver = MapResolver(nil)
