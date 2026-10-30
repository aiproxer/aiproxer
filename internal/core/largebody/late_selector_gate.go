package largebody

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
)

// BoundedRouteDomainContract defines an explicit certified bounded route domain
// for a late selector authority such as a route hint provider or selector mutator
// (Requirements 5, 7.6, 13.3, 19.4).
//
// An authority without this contract is considered full-Call / uncontracted and
// blocks wire execution.
type BoundedRouteDomainContract struct {
	// TargetBackends is the non-empty finite set of backend IDs this authority can target.
	TargetBackends []string

	// TargetModels is the optional finite set of model IDs this authority can target.
	// When empty and UniversalModel is false, models are constrained to the candidate/client model.
	// When UniversalModel is true, backend universal proof (AnyAcceptedModel) is required.
	TargetModels []string

	// UniversalModel specifies whether the authority may select any model accepted by the backend.
	UniversalModel bool
}

// Validate checks internal consistency and budget limits.
func (c BoundedRouteDomainContract) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if len(c.TargetBackends) == 0 {
		return fmt.Errorf("largebody: bounded route domain contract must specify at least one target backend")
	}
	if int64(len(c.TargetBackends)) > maxFactBytes {
		return fmt.Errorf("largebody: target backend count exceeds %d", maxFactBytes)
	}
	for i, be := range c.TargetBackends {
		be = strings.TrimSpace(be)
		if be == "" {
			return fmt.Errorf("largebody: target backend %d must not be empty", i)
		}
		if int64(len(be)) > maxFactBytes {
			return fmt.Errorf("largebody: target backend %d exceeds %d bytes", i, maxFactBytes)
		}
	}
	if c.UniversalModel && len(c.TargetModels) > 0 {
		return fmt.Errorf("largebody: universal bounded route domain must not enumerate models")
	}
	if int64(len(c.TargetModels)) > maxFactBytes {
		return fmt.Errorf("largebody: target model count exceeds %d", maxFactBytes)
	}
	for i, model := range c.TargetModels {
		model = strings.TrimSpace(model)
		if model == "" {
			return fmt.Errorf("largebody: target model %d must not be empty", i)
		}
		if int64(len(model)) > maxFactBytes {
			return fmt.Errorf("largebody: target model %d exceeds %d bytes", i, maxFactBytes)
		}
	}
	return nil
}

// ToWireDomainFacts converts this contract into WireDomainFacts for backend verification.
func (c BoundedRouteDomainContract) ToWireDomainFacts(proof Proof) WireDomainFacts {
	models := c.TargetModels
	if !c.UniversalModel && len(models) == 0 {
		if strings.TrimSpace(proof.ClientModel) != "" {
			models = []string{strings.TrimSpace(proof.ClientModel)}
		}
	}
	return WireDomainFacts{
		ProfileID:            proof.ProfileID,
		Operation:            proof.Operation,
		Delivery:             proof.Delivery,
		BodyMode:             proof.Mode,
		Rewrite:              proof.Rewrite,
		UniversalModel:       c.UniversalModel,
		CandidateModels:      models,
		RequiredCapabilities: proof.RequiredCapabilities,
	}
}

// NewBoundedRouteDomainContractFromKeys parses candidate keys of the form "backend:model"
// into an explicit BoundedRouteDomainContract.
//
// Note: backends and models are collected as independent sets, so the contract
// authorizes the cross product (e.g. ["b1:m1","b2:m2"] permits b1:m2). This is
// safe but imprecise widening: every target backend is still proven wire-compatible
// per-backend via ResolveWireDomain during evaluation, so no unproven backend
// enters the envelope — only unproven backend/model pairs within proven backends.
func NewBoundedRouteDomainContractFromKeys(keys []string) (BoundedRouteDomainContract, error) {
	if len(keys) == 0 {
		return BoundedRouteDomainContract{}, fmt.Errorf("largebody: keys slice must not be empty")
	}
	backendMap := make(map[string]struct{})
	modelMap := make(map[string]struct{})
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		parts := strings.SplitN(k, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return BoundedRouteDomainContract{}, fmt.Errorf("largebody: invalid candidate key %q, expected 'backend:model'", k)
		}
		backendMap[strings.TrimSpace(parts[0])] = struct{}{}
		modelMap[strings.TrimSpace(parts[1])] = struct{}{}
	}
	if len(backendMap) == 0 {
		return BoundedRouteDomainContract{}, fmt.Errorf("largebody: no valid backends found in candidate keys")
	}

	backends := make([]string, 0, len(backendMap))
	for b := range backendMap {
		backends = append(backends, b)
	}
	sort.Strings(backends)

	models := make([]string, 0, len(modelMap))
	for m := range modelMap {
		models = append(models, m)
	}
	sort.Strings(models)

	return BoundedRouteDomainContract{
		TargetBackends: backends,
		TargetModels:   models,
		UniversalModel: false,
	}, nil
}

// BoundedRouteDomainAuthority is an optional interface that a route hint provider
// or selector mutator can implement to declare a certified bounded route-domain contract
// (Requirement 7.6).
type BoundedRouteDomainAuthority interface {
	BoundedRouteDomainContract() (BoundedRouteDomainContract, bool)
}

// RouteHintAuthority describes a route hint authority for wire assessment (Requirement 7.6).
type RouteHintAuthority struct {
	ID                string
	Provider          routehint.Provider
	HasFullCallAccess bool
	Contract          *BoundedRouteDomainContract
}

// SelectorMutatorAuthority describes a selector mutator authority for wire assessment (Requirement 7.6, 13.3).
type SelectorMutatorAuthority struct {
	ID                string
	HasFullCallAccess bool
	Contract          *BoundedRouteDomainContract
}

// LateSelectorAssessmentGate evaluates whether other late selector authorities
// (route hints and selector mutators) are wire-compatible (Task 11.6; Requirements 5, 7, 13, 19).
//
// Invariants (Requirements 5.5, 7.6, 13.2, 13.3, 19.4):
//   - Full-Call route hints and selector mutators without an explicit bounded route-domain contract
//     are canonical-required blockers and decline wire with DeclineReasonAuthorityBlocker.
//   - Mutators and route hints with certified bounded contracts are proven within the domain envelope:
//     every target backend must be known and proven wire-compatible.
//   - Frontend custom route hooks (frontendpipe.Spec.ResolvePreAdmissionRoute and
//     frontendpipe.Spec.ResolveBodyRoute) are gated pre-capture
//     (Requirement 13.2) and are out of scope here.
//   - Pure and side-effect-free: strictly zero live store reads, zero network I/O, zero BeginTurn.
type LateSelectorAssessmentGate struct {
	// RouteHints contains optional SDK routehint.Provider instances.
	// Providers that do not implement BoundedRouteDomainAuthority consume a full *aipapi.Call
	// and are rejected as authority blockers.
	RouteHints []routehint.Provider

	// RouteHintAuthorities contains explicit route hint authority descriptors.
	RouteHintAuthorities []RouteHintAuthority

	// SelectorMutators contains explicit selector mutator authority descriptors.
	SelectorMutators []SelectorMutatorAuthority

	// GenerationValidator validates backends against the active generation.
	GenerationValidator *routing.GenerationSelectorValidator

	// KnownBackends is the set of backends known to the current generation.
	// If GenerationValidator is provided, its KnownBackends is used by default.
	KnownBackends map[string]struct{}

	// AllowedBackends is an optional subset of allowed backend IDs (e.g. initial candidates).
	// If non-nil, contracts targeting backends outside AllowedBackends decline wire.
	AllowedBackends map[string]struct{}

	// BackendResolver resolves configured backend IDs into WireBackend instances.
	BackendResolver WireBackendResolver
}

// NewLateSelectorAssessmentGate constructs a LateSelectorAssessmentGate.
func NewLateSelectorAssessmentGate(
	routeHints []routehint.Provider,
	mutators []SelectorMutatorAuthority,
	val *routing.GenerationSelectorValidator,
	backendResolver WireBackendResolver,
) *LateSelectorAssessmentGate {
	gate := &LateSelectorAssessmentGate{
		RouteHints:          routeHints,
		SelectorMutators:    mutators,
		GenerationValidator: val,
		BackendResolver:     backendResolver,
	}
	if val != nil {
		gate.KnownBackends = val.KnownBackends
	}
	return gate
}

// CandidateBackends returns the sorted set of all backend IDs targeted by
// configured certified late selector authorities.
func (g *LateSelectorAssessmentGate) CandidateBackends() []string {
	if g == nil {
		return nil
	}
	backendMap := make(map[string]struct{})
	for _, p := range g.RouteHints {
		if auth, ok := p.(BoundedRouteDomainAuthority); ok {
			if contract, valid := auth.BoundedRouteDomainContract(); valid {
				for _, be := range contract.TargetBackends {
					backendMap[be] = struct{}{}
				}
			}
		}
	}
	for _, r := range g.RouteHintAuthorities {
		if !r.HasFullCallAccess && r.Contract != nil {
			for _, be := range r.Contract.TargetBackends {
				backendMap[be] = struct{}{}
			}
		}
	}
	for _, m := range g.SelectorMutators {
		if !m.HasFullCallAccess && m.Contract != nil {
			for _, be := range m.Contract.TargetBackends {
				backendMap[be] = struct{}{}
			}
		}
	}
	if len(backendMap) == 0 {
		return nil
	}
	result := make([]string, 0, len(backendMap))
	for be := range backendMap {
		result = append(result, be)
	}
	sort.Strings(result)
	return result
}

// Evaluate evaluates all configured route hints and selector mutators.
// Returns AssessmentDecisionAccept if all authorities are certified and proven within
// the domain envelope (or if no late selector authorities are configured).
func (g *LateSelectorAssessmentGate) Evaluate(ctx context.Context, proof Proof) (AssessmentDecision, DeclineReason, WireDomainFacts) {
	if ctx != nil && ctx.Err() != nil {
		return AssessmentDecisionDecline, DeclineReasonCanceled, WireDomainFacts{}
	}
	if strings.TrimSpace(proof.ProfileID) == "" {
		return AssessmentDecisionDecline, DeclineReasonProofUncertain, WireDomainFacts{}
	}

	knownBackends := g.KnownBackends
	if knownBackends == nil && g.GenerationValidator != nil {
		knownBackends = g.GenerationValidator.KnownBackends
	}

	budget := SemanticFactBudget(ctx)

	var certifiedContracts []BoundedRouteDomainContract

	// 1. Evaluate SDK RouteHints
	for _, p := range g.RouteHints {
		if p == nil {
			continue
		}
		auth, ok := p.(BoundedRouteDomainAuthority)
		if !ok {
			// Standard routehint.Provider receives routehint.Input with full *aipapi.Call.
			// Without an explicit bounded route-domain contract, it is a canonical-required blocker (Req 7.6, 19.4).
			return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker, WireDomainFacts{}
		}
		contract, valid := auth.BoundedRouteDomainContract()
		if !valid {
			return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker, WireDomainFacts{}
		}
		certifiedContracts = append(certifiedContracts, contract)
	}

	// 2. Evaluate explicit RouteHintAuthorities
	for _, r := range g.RouteHintAuthorities {
		if r.HasFullCallAccess || r.Contract == nil {
			return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker, WireDomainFacts{}
		}
		certifiedContracts = append(certifiedContracts, *r.Contract)
	}

	// 3. Evaluate explicit SelectorMutators
	for _, m := range g.SelectorMutators {
		if m.HasFullCallAccess || m.Contract == nil {
			return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker, WireDomainFacts{}
		}
		certifiedContracts = append(certifiedContracts, *m.Contract)
	}

	// 4. If no late selector authorities are configured, accept immediately.
	if len(certifiedContracts) == 0 {
		return AssessmentDecisionAccept, DeclineReasonNone, WireDomainFacts{}
	}

	if g.BackendResolver == nil {
		return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, WireDomainFacts{}
	}

	// 5. Prove every certified bounded contract within the domain envelope.
	var allTargetBackends []string
	var allTargetModels []string
	hasUniversal := false
	hasFinite := false
	// universalProven records per-backend AnyAcceptedModel proof across all
	// contracts. A mixed universal/finite contract set certifies a universal
	// envelope only if every resolved target backend proved AnyAcceptedModel;
	// otherwise the flat domain shape cannot represent the proven set and
	// assessment declines (Requirement 7.2).
	universalProven := make(map[string]bool)

	for _, contract := range certifiedContracts {
		if err := contract.Validate(budget); err != nil {
			return AssessmentDecisionDecline, DeclineReasonProofUncertain, WireDomainFacts{}
		}
		if contract.UniversalModel {
			hasUniversal = true
		} else {
			hasFinite = true
			allTargetModels = append(allTargetModels, contract.TargetModels...)
		}

		domainFacts := contract.ToWireDomainFacts(proof)
		if err := domainFacts.Validate(budget); err != nil {
			return AssessmentDecisionDecline, DeclineReasonProofUncertain, WireDomainFacts{}
		}

		for _, backendID := range contract.TargetBackends {
			backendID = strings.TrimSpace(backendID)

			// Check generation known backends
			if knownBackends != nil {
				if _, ok := knownBackends[backendID]; !ok {
					return AssessmentDecisionDecline, DeclineReasonRouteIncompatible, WireDomainFacts{}
				}
			}

			// Check allowed envelope
			if g.AllowedBackends != nil {
				if _, ok := g.AllowedBackends[backendID]; !ok {
					return AssessmentDecisionDecline, DeclineReasonRouteIncompatible, WireDomainFacts{}
				}
			}

			// Resolve wire backend
			wb, ok := g.BackendResolver.ResolveWireBackend(backendID)
			if !ok || wb == nil {
				return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, WireDomainFacts{}
			}

			support := wb.ResolveWireDomain(ctx, domainFacts)
			if ctx != nil && ctx.Err() != nil {
				return AssessmentDecisionDecline, DeclineReasonCanceled, WireDomainFacts{}
			}

			if !support.Compatible {
				return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, WireDomainFacts{}
			}

			if domainFacts.UniversalModel && !support.AnyAcceptedModel {
				return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, WireDomainFacts{}
			}

			universalProven[backendID] = support.AnyAcceptedModel
			allTargetBackends = append(allTargetBackends, backendID)
		}
	}

	if hasUniversal && hasFinite {
		for _, backendID := range allTargetBackends {
			if !universalProven[backendID] {
				return AssessmentDecisionDecline, DeclineReasonProofUncertain, WireDomainFacts{}
			}
		}
	}

	// Build combined WireDomainFacts. A universal envelope is certified only
	// when every contract is universal (each target backend proved
	// AnyAcceptedModel above) or when a mixed set still has universal proof
	// for every resolved target backend (verified above).
	universalModel := hasUniversal
	if !universalModel {
		allTargetModels = deduplicateAndSort(allTargetModels)
		if len(allTargetModels) == 0 && strings.TrimSpace(proof.ClientModel) != "" {
			allTargetModels = []string{strings.TrimSpace(proof.ClientModel)}
		}
	} else {
		allTargetModels = nil
	}

	combinedDomain := WireDomainFacts{
		ProfileID:            proof.ProfileID,
		Operation:            proof.Operation,
		Delivery:             proof.Delivery,
		BodyMode:             proof.Mode,
		Rewrite:              proof.Rewrite,
		UniversalModel:       universalModel,
		CandidateModels:      allTargetModels,
		RequiredCapabilities: proof.RequiredCapabilities,
	}

	return AssessmentDecisionAccept, DeclineReasonNone, combinedDomain
}

func deduplicateAndSort(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

// unionWireDomainFacts composes two proven domain envelopes into one covering
// every possible outcome of both (Requirement 7.4). An empty envelope
// (ProfileID == "") composes to the other side, so gates without configured
// authorities contribute nothing. Two finite envelopes union their model sets;
// two universal envelopes stay universal. A mixed universal/finite pair cannot
// be represented soundly in the flat domain shape — the universal side's
// backends were proven for arbitrary models while the finite side's were
// proven only for enumerated models — so composition declines with
// DeclineReasonProofUncertain as a conservative same-permit canonical
// fallback. Profile identity and operation facts are carried from the first
// non-empty envelope (both derive from the same proof).
func unionWireDomainFacts(ctx context.Context, a, b WireDomainFacts) (WireDomainFacts, DeclineReason, bool) {
	if a.ProfileID == "" {
		return b, DeclineReasonNone, true
	}
	if b.ProfileID == "" {
		return a, DeclineReasonNone, true
	}
	if a.Rewrite != b.Rewrite {
		// Rewrite contracts must match exactly: the zero value is certified
		// no-rewrite, so any difference (including unset vs certified) declines
		// rather than silently adopting the other side's contract.
		return WireDomainFacts{}, DeclineReasonProofUncertain, false
	}
	if CapabilitiesDigest(a.RequiredCapabilities) != CapabilitiesDigest(b.RequiredCapabilities) {
		return WireDomainFacts{}, DeclineReasonProofUncertain, false
	}
	rewrite := a.Rewrite
	if a.UniversalModel && b.UniversalModel {
		out := a
		out.Rewrite = rewrite
		out.CandidateModels = nil
		return out, DeclineReasonNone, true
	}
	if a.UniversalModel != b.UniversalModel {
		return WireDomainFacts{}, DeclineReasonProofUncertain, false
	}
	models := deduplicateAndSort(append(append([]string{}, a.CandidateModels...), b.CandidateModels...))
	if int64(len(models)) > SemanticFactBudget(ctx) {
		return WireDomainFacts{}, DeclineReasonProofUncertain, false
	}
	out := a
	out.Rewrite = rewrite
	out.CandidateModels = models
	return out, DeclineReasonNone, true
}
