package largebody_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
)

// stubStandardFullCallRouteHintProvider implements routehint.Provider.
// By default, routehint.Input receives a full *aipapi.Call, making any uncontracted
// routehint.Provider a full-Call route hint that must decline wire (Requirements 5, 7.6, 13.3, 19.4).
type stubStandardFullCallRouteHintProvider struct {
	id string
}

func (p *stubStandardFullCallRouteHintProvider) ID() string { return p.id }
func (p *stubStandardFullCallRouteHintProvider) Order() int { return 0 }
func (p *stubStandardFullCallRouteHintProvider) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailOpen
}

func (p *stubStandardFullCallRouteHintProvider) Hint(_ context.Context, _ routehint.Input) (routehint.Result, error) {
	return routehint.Result{PreferredCandidateKeys: []string{"backend-1:gpt-4o"}}, nil
}

// stubCertifiedBoundedRouteHintProvider implements both routehint.Provider and
// largebody.BoundedRouteDomainAuthority, declaring an explicit certified bounded route-domain contract.
type stubCertifiedBoundedRouteHintProvider struct {
	id       string
	contract largebody.BoundedRouteDomainContract
}

func (p *stubCertifiedBoundedRouteHintProvider) ID() string { return p.id }
func (p *stubCertifiedBoundedRouteHintProvider) Order() int { return 0 }
func (p *stubCertifiedBoundedRouteHintProvider) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailOpen
}

func (p *stubCertifiedBoundedRouteHintProvider) Hint(_ context.Context, _ routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

func (p *stubCertifiedBoundedRouteHintProvider) BoundedRouteDomainContract() (largebody.BoundedRouteDomainContract, bool) {
	return p.contract, true
}

// Test 1: Nil / empty route hints and selector mutators accept with DeclineReasonNone (Req 7.6).
func TestLateSelectorAssessmentGate_EmptyAuthoritiesAccept(t *testing.T) {
	t.Parallel()
	gate := &largebody.LateSelectorAssessmentGate{}
	decision, reason, facts := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected AssessmentDecisionAccept for empty authorities, got %v", decision)
	}
	if reason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone for empty authorities, got %v", reason)
	}
	if facts.ProfileID != "" {
		t.Fatalf("expected empty WireDomainFacts for empty authorities, got %+v", facts)
	}
}

// Test 2: Standard full-Call routehint.Provider without bounded contract declines wire with DeclineReasonAuthorityBlocker (Req 5, 7.6, 13.3, 19.4).
func TestLateSelectorAssessmentGate_StandardFullCallRouteHint_Declines(t *testing.T) {
	t.Parallel()
	provider := &stubStandardFullCallRouteHintProvider{id: "uncontracted-hint-1"}
	gate := &largebody.LateSelectorAssessmentGate{
		RouteHints: []routehint.Provider{provider},
	}
	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for full-Call route hint, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker for full-Call route hint, got %v", reason)
	}
}

// Test 3: Explicit RouteHintAuthority marked HasFullCallAccess or with nil contract declines wire (Req 7.6, 19.4).
func TestLateSelectorAssessmentGate_RouteHintAuthority_NoContract_Declines(t *testing.T) {
	t.Parallel()
	gate := &largebody.LateSelectorAssessmentGate{
		RouteHintAuthorities: []largebody.RouteHintAuthority{
			{
				ID:                "custom-hint-authority",
				HasFullCallAccess: true,
				Contract:          nil,
			},
		},
	}
	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for uncontracted route hint authority, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", reason)
	}
}

// Test 4: SelectorMutatorAuthority marked HasFullCallAccess or with nil contract declines wire (Req 7.6, 13.3).
func TestLateSelectorAssessmentGate_SelectorMutator_NoContract_Declines(t *testing.T) {
	t.Parallel()
	gate := &largebody.LateSelectorAssessmentGate{
		SelectorMutators: []largebody.SelectorMutatorAuthority{
			{
				ID:                "submit-rewrite-mutator",
				HasFullCallAccess: true,
				Contract:          nil,
			},
		},
	}
	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for uncontracted selector mutator, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", reason)
	}
}

// Test 5: Route hint with certified bounded contract proven within domain envelope accepts (Req 7.6).
func TestLateSelectorAssessmentGate_CertifiedBoundedRouteHint_Accepts(t *testing.T) {
	t.Parallel()
	provider := &stubCertifiedBoundedRouteHintProvider{
		id: "certified-hint",
		contract: largebody.BoundedRouteDomainContract{
			TargetBackends: []string{"backend-1"},
			TargetModels:   []string{"gpt-4o"},
		},
	}
	resolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{
			compatible:       true,
			anyAcceptedModel: false,
		},
	}
	gate := &largebody.LateSelectorAssessmentGate{
		RouteHints:      []routehint.Provider{provider},
		BackendResolver: resolver,
		KnownBackends:   map[string]struct{}{"backend-1": {}},
	}

	decision, reason, facts := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected AssessmentDecisionAccept for certified bounded route hint, got %v (reason: %v)", decision, reason)
	}
	if reason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone, got %v", reason)
	}
	if facts.ProfileID != "openai-responses-v1" {
		t.Fatalf("expected ProfileID openai-responses-v1, got %q", facts.ProfileID)
	}
	if len(facts.CandidateModels) != 1 || facts.CandidateModels[0] != "gpt-4o" {
		t.Fatalf("expected CandidateModels [gpt-4o], got %v", facts.CandidateModels)
	}
}

// Test 6: Route hint with certified bounded contract targeting incompatible backend declines with DeclineReasonBackendIncompatible (Req 8.4).
func TestLateSelectorAssessmentGate_CertifiedBoundedRouteHint_IncompatibleBackend_Declines(t *testing.T) {
	t.Parallel()
	provider := &stubCertifiedBoundedRouteHintProvider{
		id: "certified-hint",
		contract: largebody.BoundedRouteDomainContract{
			TargetBackends: []string{"backend-incompatible"},
			TargetModels:   []string{"gpt-4o"},
		},
	}
	resolver := largebody.WireBackendMap{
		"backend-incompatible": &testStubDomainBackend{
			compatible: false,
			reason:     largebody.WireSupportReasonModelUnsupported,
		},
	}
	gate := &largebody.LateSelectorAssessmentGate{
		RouteHints:      []routehint.Provider{provider},
		BackendResolver: resolver,
		KnownBackends:   map[string]struct{}{"backend-incompatible": {}},
	}

	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for incompatible backend, got %v", decision)
	}
	if reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", reason)
	}
}

// Test 7: Certified contract targeting backend outside KnownBackends declines with DeclineReasonRouteIncompatible (Req 7.2).
func TestLateSelectorAssessmentGate_CertifiedContract_UnknownBackend_Declines(t *testing.T) {
	t.Parallel()
	provider := &stubCertifiedBoundedRouteHintProvider{
		id: "certified-hint",
		contract: largebody.BoundedRouteDomainContract{
			TargetBackends: []string{"backend-unknown"},
			TargetModels:   []string{"gpt-4o"},
		},
	}
	resolver := largebody.WireBackendMap{
		"backend-unknown": &testStubDomainBackend{compatible: true},
	}
	gate := &largebody.LateSelectorAssessmentGate{
		RouteHints:      []routehint.Provider{provider},
		BackendResolver: resolver,
		KnownBackends:   map[string]struct{}{"backend-known": {}},
	}

	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for unknown backend, got %v", decision)
	}
	if reason != largebody.DeclineReasonRouteIncompatible {
		t.Fatalf("expected DeclineReasonRouteIncompatible, got %v", reason)
	}
}

// Test 8: Certified contract targeting backend outside AllowedBackends declines with DeclineReasonRouteIncompatible (Req 7.6).
func TestLateSelectorAssessmentGate_CertifiedContract_OutsideAllowedBackends_Declines(t *testing.T) {
	t.Parallel()
	provider := &stubCertifiedBoundedRouteHintProvider{
		id: "certified-hint",
		contract: largebody.BoundedRouteDomainContract{
			TargetBackends: []string{"backend-2"},
			TargetModels:   []string{"gpt-4o"},
		},
	}
	resolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true},
		"backend-2": &testStubDomainBackend{compatible: true},
	}
	gate := &largebody.LateSelectorAssessmentGate{
		RouteHints:      []routehint.Provider{provider},
		BackendResolver: resolver,
		KnownBackends:   map[string]struct{}{"backend-1": {}, "backend-2": {}},
		AllowedBackends: map[string]struct{}{"backend-1": {}}, // Only backend-1 is in the allowed envelope
	}

	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for backend outside allowed envelope, got %v", decision)
	}
	if reason != largebody.DeclineReasonRouteIncompatible {
		t.Fatalf("expected DeclineReasonRouteIncompatible, got %v", reason)
	}
}

// Test 9: Certified contract requiring UniversalModel: true declines if backend lacks AnyAcceptedModel (Req 7.3, 8.2).
func TestLateSelectorAssessmentGate_UniversalModel_RequiresAnyAcceptedModel(t *testing.T) {
	t.Parallel()
	provider := &stubCertifiedBoundedRouteHintProvider{
		id: "universal-hint",
		contract: largebody.BoundedRouteDomainContract{
			TargetBackends: []string{"backend-1"},
			UniversalModel: true,
		},
	}
	resolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{
			compatible:       true,
			anyAcceptedModel: false, // does NOT support AnyAcceptedModel
		},
	}
	gate := &largebody.LateSelectorAssessmentGate{
		RouteHints:      []routehint.Provider{provider},
		BackendResolver: resolver,
		KnownBackends:   map[string]struct{}{"backend-1": {}},
	}

	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for missing AnyAcceptedModel, got %v", decision)
	}
	if reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", reason)
	}
}

// Test 9a: Mixed universal + finite contracts decline when a finite-contract
// backend lacks AnyAcceptedModel: the combined universal envelope would authorize
// backend/model pairs never proven (Req 7.2).
func TestLateSelectorAssessmentGate_MixedUniversalFiniteWithoutUniversalProof_Declines(t *testing.T) {
	t.Parallel()
	universalHint := &stubCertifiedBoundedRouteHintProvider{
		id: "universal-hint",
		contract: largebody.BoundedRouteDomainContract{
			TargetBackends: []string{"backend-1"},
			UniversalModel: true,
		},
	}
	finiteHint := &stubCertifiedBoundedRouteHintProvider{
		id: "finite-hint",
		contract: largebody.BoundedRouteDomainContract{
			TargetBackends: []string{"backend-2"},
			TargetModels:   []string{"gpt-4o"},
		},
	}
	resolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{
			compatible:       true,
			anyAcceptedModel: true,
		},
		"backend-2": &testStubDomainBackend{
			compatible:       true,
			anyAcceptedModel: false, // proven only for its finite model set
		},
	}
	gate := &largebody.LateSelectorAssessmentGate{
		RouteHints:      []routehint.Provider{universalHint, finiteHint},
		BackendResolver: resolver,
		KnownBackends:   map[string]struct{}{"backend-1": {}, "backend-2": {}},
	}

	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for mixed universal/finite without universal proof, got %v", decision)
	}
	if reason != largebody.DeclineReasonProofUncertain {
		t.Fatalf("expected DeclineReasonProofUncertain, got %v", reason)
	}
}

// Test 9b: Mixed universal + finite contracts accept a universal envelope when
// every resolved target backend proved AnyAcceptedModel (Req 7.2).
func TestLateSelectorAssessmentGate_MixedUniversalFiniteWithUniversalProof_Accepts(t *testing.T) {
	t.Parallel()
	universalHint := &stubCertifiedBoundedRouteHintProvider{
		id: "universal-hint",
		contract: largebody.BoundedRouteDomainContract{
			TargetBackends: []string{"backend-1"},
			UniversalModel: true,
		},
	}
	finiteHint := &stubCertifiedBoundedRouteHintProvider{
		id: "finite-hint",
		contract: largebody.BoundedRouteDomainContract{
			TargetBackends: []string{"backend-2"},
			TargetModels:   []string{"gpt-4o"},
		},
	}
	resolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{
			compatible:       true,
			anyAcceptedModel: true,
		},
		"backend-2": &testStubDomainBackend{
			compatible:       true,
			anyAcceptedModel: true,
		},
	}
	gate := &largebody.LateSelectorAssessmentGate{
		RouteHints:      []routehint.Provider{universalHint, finiteHint},
		BackendResolver: resolver,
		KnownBackends:   map[string]struct{}{"backend-1": {}, "backend-2": {}},
	}

	decision, reason, facts := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected AssessmentDecisionAccept for universally-proven mixed contracts, got %v (reason: %v)", decision, reason)
	}
	if reason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone, got %v", reason)
	}
	if !facts.UniversalModel {
		t.Fatalf("expected universal combined envelope, got %+v", facts)
	}
	if len(facts.CandidateModels) != 0 {
		t.Fatalf("expected no enumerated models in universal envelope, got %v", facts.CandidateModels)
	}
}

// Test 10: Certified selector mutator proven within domain envelope accepts (Req 7.6, 13.3).
func TestLateSelectorAssessmentGate_CertifiedSelectorMutator_Accepts(t *testing.T) {
	t.Parallel()
	mutator := largebody.SelectorMutatorAuthority{
		ID:                "bounded-mutator",
		HasFullCallAccess: false,
		Contract: &largebody.BoundedRouteDomainContract{
			TargetBackends: []string{"backend-1"},
			TargetModels:   []string{"gpt-4o"},
		},
	}
	resolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{
			compatible:       true,
			anyAcceptedModel: false,
		},
	}
	gate := &largebody.LateSelectorAssessmentGate{
		SelectorMutators: []largebody.SelectorMutatorAuthority{mutator},
		BackendResolver:  resolver,
		KnownBackends:    map[string]struct{}{"backend-1": {}},
	}

	decision, reason, facts := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected AssessmentDecisionAccept for certified selector mutator, got %v (reason: %v)", decision, reason)
	}
	if reason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone, got %v", reason)
	}
	if facts.ProfileID != "openai-responses-v1" {
		t.Fatalf("expected ProfileID openai-responses-v1, got %q", facts.ProfileID)
	}
}

// Test 11: Nil BackendResolver with configured certified contract declines DeclineReasonBackendIncompatible.
func TestLateSelectorAssessmentGate_NilBackendResolver_Declines(t *testing.T) {
	t.Parallel()
	provider := &stubCertifiedBoundedRouteHintProvider{
		id: "certified-hint",
		contract: largebody.BoundedRouteDomainContract{
			TargetBackends: []string{"backend-1"},
			TargetModels:   []string{"gpt-4o"},
		},
	}
	gate := &largebody.LateSelectorAssessmentGate{
		RouteHints:      []routehint.Provider{provider},
		BackendResolver: nil,
	}

	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for nil backend resolver, got %v", decision)
	}
	if reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", reason)
	}
}

// Test 12: Context cancellation declines with DeclineReasonCanceled.
func TestLateSelectorAssessmentGate_CanceledContext_Declines(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	gate := &largebody.LateSelectorAssessmentGate{}
	decision, reason, _ := gate.Evaluate(ctx, validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for canceled context, got %v", decision)
	}
	if reason != largebody.DeclineReasonCanceled {
		t.Fatalf("expected DeclineReasonCanceled, got %v", reason)
	}
}

// Test 13: GenerationSelectorValidator integration extracts KnownBackends and enforces domain bounds.
func TestLateSelectorAssessmentGate_GenerationValidatorIntegration(t *testing.T) {
	t.Parallel()
	knownBackends := map[string]struct{}{"backend-1": {}}
	val := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		knownBackends,
		nil,
		config.ExecutionCompositionSafe,
	)
	resolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true},
	}

	// 1. Constructor binds known backends from validator
	gate := largebody.NewLateSelectorAssessmentGate(
		nil,
		[]largebody.SelectorMutatorAuthority{
			{
				ID: "mutator-1",
				Contract: &largebody.BoundedRouteDomainContract{
					TargetBackends: []string{"backend-1"},
					TargetModels:   []string{"gpt-4o"},
				},
			},
		},
		val,
		resolver,
	)

	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected AssessmentDecisionAccept, got %v (reason: %v)", decision, reason)
	}
	if reason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone, got %v", reason)
	}

	// 2. Mutator targeting backend not in validator's known backends declines
	gateInvalid := largebody.NewLateSelectorAssessmentGate(
		nil,
		[]largebody.SelectorMutatorAuthority{
			{
				ID: "mutator-invalid",
				Contract: &largebody.BoundedRouteDomainContract{
					TargetBackends: []string{"backend-unknown"},
					TargetModels:   []string{"gpt-4o"},
				},
			},
		},
		val,
		resolver,
	)
	decInv, rInv, _ := gateInvalid.Evaluate(context.Background(), validTestProof())
	if decInv != largebody.AssessmentDecisionDecline || rInv != largebody.DeclineReasonRouteIncompatible {
		t.Fatalf("expected Decline with RouteIncompatible for unknown backend, got %v / %v", decInv, rInv)
	}
}

// Test 15: Custom route hook separation characterization (Req 13.2).
// Explicitly documents and verifies that frontendpipe custom route hooks
// are pre-capture authorities and are not part of this post-capture assessment gate.
func TestLateSelectorAssessmentGate_LegacyFullBodyResolverSeparation(t *testing.T) {
	t.Parallel()
	// The late selector gate evaluates only post-capture late selector authorities
	// (route hints and selector mutators). Custom route hooks
	// (ResolvePreAdmissionRoute/ResolveBodyRoute) run before capture or under
	// decode admission and are gated
	// at static disposition / pre-capture (Requirement 13.2).
	gate := &largebody.LateSelectorAssessmentGate{}
	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected clean accept when no late selector authorities are present, got %v / %v", decision, reason)
	}
}

// Test 16: NewBoundedRouteDomainContractFromKeys parses candidate keys of format "backend:model"
func TestBoundedRouteDomainContract_NewFromKeys(t *testing.T) {
	t.Parallel()
	// Valid keys
	keys := []string{"backend-2:gpt-4o", "backend-1:gpt-4o", "backend-1:gpt-4o-mini"}
	contract, err := largebody.NewBoundedRouteDomainContractFromKeys(keys)
	if err != nil {
		t.Fatalf("unexpected error parsing valid keys: %v", err)
	}
	if len(contract.TargetBackends) != 2 || contract.TargetBackends[0] != "backend-1" || contract.TargetBackends[1] != "backend-2" {
		t.Fatalf("unexpected TargetBackends: %v", contract.TargetBackends)
	}
	if len(contract.TargetModels) != 2 || contract.TargetModels[0] != "gpt-4o" || contract.TargetModels[1] != "gpt-4o-mini" {
		t.Fatalf("unexpected TargetModels: %v", contract.TargetModels)
	}
	if contract.UniversalModel {
		t.Fatal("expected UniversalModel to be false")
	}

	// Invalid keys: empty
	_, errEmpty := largebody.NewBoundedRouteDomainContractFromKeys(nil)
	if errEmpty == nil {
		t.Fatal("expected error for empty keys")
	}

	// Invalid keys: missing colon
	_, errNoColon := largebody.NewBoundedRouteDomainContractFromKeys([]string{"invalid"})
	if errNoColon == nil {
		t.Fatal("expected error for key without colon")
	}
}

// Test 17: Multiple authorities combined: all certified accept; one uncontracted declines with blocker
func TestLateSelectorAssessmentGate_MultipleAuthoritiesCombined(t *testing.T) {
	t.Parallel()
	provider := &stubCertifiedBoundedRouteHintProvider{
		id: "certified-hint",
		contract: largebody.BoundedRouteDomainContract{
			TargetBackends: []string{"backend-1"},
			TargetModels:   []string{"gpt-4o"},
		},
	}
	mutator := largebody.SelectorMutatorAuthority{
		ID:                "certified-mutator",
		HasFullCallAccess: false,
		Contract: &largebody.BoundedRouteDomainContract{
			TargetBackends: []string{"backend-2"},
			TargetModels:   []string{"gpt-4o-mini"},
		},
	}
	resolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true},
		"backend-2": &testStubDomainBackend{compatible: true},
	}

	// Case A: Both certified -> accepts and combines domain facts
	gateBothCertified := &largebody.LateSelectorAssessmentGate{
		RouteHints:       []routehint.Provider{provider},
		SelectorMutators: []largebody.SelectorMutatorAuthority{mutator},
		BackendResolver:  resolver,
		KnownBackends:    map[string]struct{}{"backend-1": {}, "backend-2": {}},
	}
	decision, reason, facts := gateBothCertified.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept for both certified, got %v / %v", decision, reason)
	}
	if len(facts.CandidateModels) != 2 {
		t.Fatalf("expected 2 combined models, got %v", facts.CandidateModels)
	}

	// Case B: One certified + one uncontracted full-Call hint -> declines as authority blocker
	uncontractedHint := &stubStandardFullCallRouteHintProvider{id: "full-call"}
	gateMixed := &largebody.LateSelectorAssessmentGate{
		RouteHints:       []routehint.Provider{provider, uncontractedHint},
		SelectorMutators: []largebody.SelectorMutatorAuthority{mutator},
		BackendResolver:  resolver,
		KnownBackends:    map[string]struct{}{"backend-1": {}, "backend-2": {}},
	}
	decMixed, rMixed, _ := gateMixed.Evaluate(context.Background(), validTestProof())
	if decMixed != largebody.AssessmentDecisionDecline || rMixed != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker for mixed authorities, got %v / %v", decMixed, rMixed)
	}
}

// Test 19: Pure side-effect sentinel test (Req 6.2).
// Confirms that LateSelectorAssessmentGate evaluates without side effects:
// it holds no store reader and performs no I/O (Req 6.2).
func TestLateSelectorAssessmentGate_SideEffectSentinel(t *testing.T) {
	t.Parallel()
	val := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}},
		nil,
		config.ExecutionCompositionSafe,
	)
	resolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true},
	}
	gate := largebody.NewLateSelectorAssessmentGate(
		[]routehint.Provider{
			&stubCertifiedBoundedRouteHintProvider{
				id: "certified",
				contract: largebody.BoundedRouteDomainContract{
					TargetBackends: []string{"backend-1"},
					TargetModels:   []string{"gpt-4o"},
				},
			},
		},
		nil,
		val,
		resolver,
	)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Evaluate panicked due to unexpected side effect: %v", r)
		}
	}()
	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected clean accept, got %v / %v", decision, reason)
	}
}
