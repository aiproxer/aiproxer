package runtime

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func (e *Executor) effectiveFactsForAttempt(
	ctx context.Context,
	be execbackend.Backend,
	attempt aipapi.Call,
	c routing.AttemptCandidate,
) modelcatalog.EffectiveFacts {
	base := e.backendCapsForAttempt(ctx, be, attempt, c)
	if e == nil || e.CatalogResolver == nil {
		return syntheticBackendOnlyFacts(base, c)
	}
	return e.CatalogResolver.Resolve(ctx, c, attempt, base)
}

func (e *Executor) backendCapsForAttempt(
	ctx context.Context,
	be execbackend.Backend,
	attempt aipapi.Call,
	c routing.AttemptCandidate,
) aipapi.BackendCaps {
	wire := routing.BackendFacingCandidate(c)
	if e != nil && e.CapsResolver != nil {
		return e.CapsResolver.DescribeCandidate(ctx, wire, attempt)
	}
	return execbackend.EffectiveCaps(ctx, be, attempt, wire)
}

func syntheticBackendOnlyFacts(base aipapi.BackendCaps, c routing.AttemptCandidate) modelcatalog.EffectiveFacts {
	be := modelcatalog.CloneBackendCaps(base)
	input := strings.TrimSpace(c.Primary.Model)
	return modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			Source:    modelcatalog.FactSourceBackendDeclaration,
			MatchKind: modelcatalog.MatchNone,
		},
		BackendCaps:   base,
		EffectiveCaps: be,
		Matched:       false,
		Match:         modelcatalog.MatchResult{Kind: modelcatalog.MatchNone, InputModel: input},
	}
}
