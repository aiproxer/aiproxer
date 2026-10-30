package runtime

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// CatalogResolver merges administrator overrides, catalog facts, and backend capability maps
// for one routing candidate. Defined here (consumer package) per ports-and-adapters guidance.
type CatalogResolver interface {
	Resolve(
		ctx context.Context,
		candidate routing.AttemptCandidate,
		call aipapi.Call,
		backend aipapi.BackendCaps,
	) modelcatalog.EffectiveFacts
}

// EligibilityResolver runs context-limit checks after successful capability negotiation.
type EligibilityResolver interface {
	Check(
		ctx context.Context,
		candidate routing.AttemptCandidate,
		call aipapi.Call,
		facts modelcatalog.EffectiveFacts,
	) modelcatalog.EligibilityDecision
}

// RequestTokenEstimator supplies a provider-neutral request-size estimate for routing constraints.
// Unavailable estimates fail open in the routing planner.
type RequestTokenEstimator interface {
	EstimateRequestTokens(ctx context.Context, call aipapi.Call) modelcatalog.SizeEstimate
}

var (
	_ CatalogResolver       = (*modelcatalog.CatalogResolverImpl)(nil)
	_ EligibilityResolver   = (*modelcatalog.EligibilityResolverImpl)(nil)
	_ RequestTokenEstimator = modelcatalog.DefaultSizeEstimator{}
)
