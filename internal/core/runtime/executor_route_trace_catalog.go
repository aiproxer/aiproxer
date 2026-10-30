package runtime

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// routeTraceCatalogJSON builds a compact, content-free catalog view for [diag.RouteTraceEntry].Catalog.
// facts carries negotiation-time effective facts (match metadata for the pre-negotiation resolution path).
// When context eligibility ran, catalog-oriented fields are taken from elig.Facts so trace matches the
// same snapshot and match classification as the eligibility check (post-negotiation attempt shape).
// catalogRouteTraceIfEnabled avoids allocating trace DTOs when route tracing is off (request hot path).
func catalogRouteTraceIfEnabled(
	e *Executor,
	facts modelcatalog.EffectiveFacts,
	neg aipapi.NegotiationResult,
	elig *modelcatalog.EligibilityDecision,
	ranContextEligibility bool,
) *diag.RouteTraceCatalog {
	if e == nil || e.RouteTrace == nil {
		return nil
	}
	return routeTraceCatalogJSON(facts, neg, elig, ranContextEligibility)
}

func routeTraceCatalogJSON(
	facts modelcatalog.EffectiveFacts,
	neg aipapi.NegotiationResult,
	elig *modelcatalog.EligibilityDecision,
	ranContextEligibility bool,
) *diag.RouteTraceCatalog {
	cat := facts
	if ranContextEligibility && elig != nil {
		cat = elig.Facts
	}
	m := &diag.RouteTraceCatalog{
		MatchKind:   cat.Match.Kind.String(),
		FactSource:  cat.Facts.Source.String(),
		Negotiation: string(neg.Kind),
	}
	if id := strings.TrimSpace(cat.Match.MatchedID); id != "" {
		m.MatchedModelID = id
	}
	if !ranContextEligibility || elig == nil {
		m.Eligibility = "skipped"
		return m
	}
	m.Eligibility = elig.Reason.String()
	if b := strings.TrimSpace(elig.Estimate.Basis); b != "" {
		m.EstimateBasis = b
	}
	return m
}
