package largebody_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// trackingWireBackend records received facts for exact and domain resolvers
// to prove that immutable body mode and rewrite semantics are threaded to every resolver.
type trackingWireBackend struct {
	compatRequest    bool
	compatDomain     bool
	anyAcceptedModel bool
	reason           largebody.WireSupportReason
	needsRewrite     bool

	requestCalls []largebody.WireRequestFacts
	domainCalls  []largebody.WireDomainFacts
}

func (b *trackingWireBackend) ResolveWireRequest(_ context.Context, facts largebody.WireRequestFacts, _ routing.AttemptCandidate) largebody.WireRequestSupport {
	b.requestCalls = append(b.requestCalls, facts)
	if !b.compatRequest {
		r := b.reason
		if r == largebody.WireSupportReasonNone {
			r = largebody.WireSupportReasonUnsupported
		}
		return largebody.WireRequestSupport{Compatible: false, Reason: r}
	}
	return largebody.WireRequestSupport{
		Compatible:        true,
		NeedsModelRewrite: b.needsRewrite,
	}
}

func (b *trackingWireBackend) ResolveWireDomain(_ context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
	b.domainCalls = append(b.domainCalls, facts)
	if !b.compatDomain {
		r := b.reason
		if r == largebody.WireSupportReasonNone {
			r = largebody.WireSupportReasonUnsupported
		}
		return largebody.WireDomainSupport{Compatible: false, Reason: r}
	}
	return largebody.WireDomainSupport{
		Compatible:       true,
		AnyAcceptedModel: b.anyAcceptedModel,
	}
}

func makeTask11_7Proof(routeSelector, clientModel string, mode largebody.BodyMode, rewrite largebody.RewriteSemantics) largebody.Proof {
	return largebody.Proof{
		ProfileID:       "openai-responses-v1",
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		RouteSelector:   routeSelector,
		ClientModel:     clientModel,
		MaxOutputTokens: 2048,
		Facts: largebody.ProtocolFacts{
			RequirementsID: "req-11-7",
			ControlCount:   1,
		},
		Mode:      mode,
		Rewrite:   rewrite,
		Identity:  largebody.NewIdentityDigest([32]byte{11, 7, 1}),
		Source:    largebody.NewSourceDigest([32]byte{11, 7, 2}),
		BodyBytes: 4096,
	}
}

// -----------------------------------------------------------------------------
// Test 1: Pass immutable body/rewrite facts to EVERY resolver (Task 11.7; Req 8.1, 8.2, 9)
// -----------------------------------------------------------------------------

func TestTask11_7_ImmutableBodyAndRewriteFactsThreadedToEveryResolver(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	backendResolver := largebody.WireBackendMap{"backend-1": be1}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)

	proofGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, backendResolver)

	span := largebody.Span{Offset: 12, Length: 8}
	certifiedRewrite, err := largebody.NewModelTokenRewrite(span)
	if err != nil {
		t.Fatalf("failed to construct model token rewrite: %v", err)
	}

	proof := makeTask11_7Proof("backend-1:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, certifiedRewrite)

	decision, reason, wireReq, wireDomain, cands := proofGate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept, got %v / %v", decision, reason)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}

	// 1. Verify exact resolver received immutable body mode and rewrite semantics
	if len(be1.requestCalls) != 1 {
		t.Fatalf("expected 1 request call, got %d", len(be1.requestCalls))
	}
	reqFacts := be1.requestCalls[0]
	if reqFacts.BodyMode != largebody.BodyModeIdentityJSON {
		t.Errorf("expected BodyMode %v, got %v", largebody.BodyModeIdentityJSON, reqFacts.BodyMode)
	}
	if reqFacts.Rewrite != certifiedRewrite {
		t.Errorf("expected Rewrite %v, got %v", certifiedRewrite, reqFacts.Rewrite)
	}
	if reqFacts.Rewrite.Kind() != largebody.RewriteKindModelToken {
		t.Errorf("expected RewriteKindModelToken, got %v", reqFacts.Rewrite.Kind())
	}
	if reqFacts.Rewrite.Span() != span {
		t.Errorf("expected Span %v, got %v", span, reqFacts.Rewrite.Span())
	}

	// 2. Verify domain resolver received immutable body mode and rewrite semantics
	if len(be1.domainCalls) != 1 {
		t.Fatalf("expected 1 domain call, got %d", len(be1.domainCalls))
	}
	domFacts := be1.domainCalls[0]
	if domFacts.BodyMode != largebody.BodyModeIdentityJSON {
		t.Errorf("expected domain BodyMode %v, got %v", largebody.BodyModeIdentityJSON, domFacts.BodyMode)
	}
	if domFacts.Rewrite != certifiedRewrite {
		t.Errorf("expected domain Rewrite %v, got %v", certifiedRewrite, domFacts.Rewrite)
	}

	// 3. Verify returned wire facts carry immutable semantics
	if wireReq.BodyMode != largebody.BodyModeIdentityJSON || wireReq.Rewrite != certifiedRewrite {
		t.Errorf("wireReq does not carry immutable facts: %+v", wireReq)
	}
	if wireDomain.BodyMode != largebody.BodyModeIdentityJSON || wireDomain.Rewrite != certifiedRewrite {
		t.Errorf("wireDomain does not carry immutable facts: %+v", wireDomain)
	}
}

// -----------------------------------------------------------------------------
// Test 2: Homogeneous same-wire domain ACCEPTS (Task 11.7; Requirements 7, 8, 21)
// -----------------------------------------------------------------------------

func TestTask11_7_HomogeneousSameWireDomain_Accepts(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	be2 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}

	backendResolver := largebody.WireBackendMap{
		"backend-1": be1,
		"backend-2": be2,
	}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}, "backend-2": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)

	proofGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, backendResolver)

	proof := makeTask11_7Proof("backend-1:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())

	decision, reason, _, wireDomain, _ := proofGate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accepted homogeneous same-wire domain, got %v / %v", decision, reason)
	}
	if !wireDomain.UniversalModel {
		t.Errorf("expected universal model envelope in accepted domain")
	}
	if len(be1.domainCalls) != 1 || len(be2.domainCalls) != 1 {
		t.Errorf("expected both backends in homogeneous domain to be proven via ResolveWireDomain, got be1=%d, be2=%d", len(be1.domainCalls), len(be2.domainCalls))
	}
}

// -----------------------------------------------------------------------------
// Test 3: Heterogeneous incompatible domain DECLINES ENTIRE request (NO PRUNING)
// Requirement 7.5, 7.7, 8.4: any domain member incompatibility => decline before BeginTurn.
// -----------------------------------------------------------------------------

func TestTask11_7_HeterogeneousIncompatibleDomain_DeclinesEntireRequest_NoPruning(t *testing.T) {
	t.Parallel()

	beWire := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	beIncompatible := &trackingWireBackend{compatRequest: false, compatDomain: false, reason: largebody.WireSupportReasonProfileUnsupported}

	backendResolver := largebody.WireBackendMap{
		"backend-wire":         beWire,
		"backend-incompatible": beIncompatible,
	}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionInference, true
	})

	// Initial route points to the wire-compatible backend-wire
	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-wire",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	// But the generation's route-override domain includes backend-incompatible
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-wire",
		map[string]struct{}{"backend-wire": {}, "backend-incompatible": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)

	proofGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, backendResolver)

	proof := makeTask11_7Proof("backend-wire:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())

	decision, reason, _, _, _ := proofGate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline for heterogeneous incompatible domain, got %v", decision)
	}
	if reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", reason)
	}
}

// -----------------------------------------------------------------------------
// Test 4: Any initial candidate incompatibility declines entire request (Req 8.4)
// -----------------------------------------------------------------------------

func TestTask11_7_AnyCandidateIncompatibility_DeclinesEntireRequest(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	be2 := &trackingWireBackend{compatRequest: false, compatDomain: true, anyAcceptedModel: true, reason: largebody.WireSupportReasonModelUnsupported}

	backendResolver := largebody.WireBackendMap{
		"backend-1": be1,
		"backend-2": be2,
	}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	proofGate := largebody.NewBackendWireProofGate(initialGate, nil, nil, backendResolver)

	proof := makeTask11_7Proof("backend-1:gpt-4o | backend-2:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())

	decision, reason, _, _, _ := proofGate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline when candidate is incompatible, got %v", decision)
	}
	if reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", reason)
	}
}

// -----------------------------------------------------------------------------
// Test 7: Rewrite semantics enforced across domain and post-BeginTurn override (Req 9)
// -----------------------------------------------------------------------------

func TestTask11_7_RewriteSemantics_DomainAndCandidateParity(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true, needsRewrite: true}
	backendResolver := largebody.WireBackendMap{"backend-1": be1}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	proofGate := largebody.NewBackendWireProofGate(initialGate, nil, nil, backendResolver)

	// Proof with NO rewrite, but backend requires rewrite
	proofNoRewrite := makeTask11_7Proof("backend-1:gpt-4o-native", "gpt-4o-client", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())

	decision, reason, _, _, _ := proofGate.Evaluate(context.Background(), proofNoRewrite)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline when rewrite required but not certified, got %v", decision)
	}
	if reason != largebody.DeclineReasonRewriteUnsupported {
		t.Fatalf("expected DeclineReasonRewriteUnsupported, got %v", reason)
	}

	// Now with certified model rewrite
	span := largebody.Span{Offset: 10, Length: 13}
	certifiedRewrite, err := largebody.NewModelTokenRewrite(span)
	if err != nil {
		t.Fatalf("rewrite creation: %v", err)
	}
	proofWithRewrite := makeTask11_7Proof("backend-1:gpt-4o-native", "gpt-4o-client", largebody.BodyModeIdentityJSON, certifiedRewrite)

	decision, reason, _, _, _ = proofGate.Evaluate(context.Background(), proofWithRewrite)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept with certified rewrite, got %v / %v", decision, reason)
	}
}

// -----------------------------------------------------------------------------
// Test 8: Pure side-effect sentinel test (Req 6.2)
// -----------------------------------------------------------------------------

func TestTask11_7_PureSideEffectSentinel(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	backendResolver := largebody.WireBackendMap{"backend-1": be1}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)

	proofGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, backendResolver)

	proof := makeTask11_7Proof("backend-1:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())
	decision, reason, _, _, _ := proofGate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept without store reads, got %v / %v", decision, reason)
	}
	if sentinel.snapshotCalled {
		t.Fatal("sentinel snapshot was called during assessment")
	}
}

// -----------------------------------------------------------------------------
// TestBackendWireProofGate_CombinedLateDomainEnvelopes ports the unique combined
// late-domain claims to the retained BackendWireProofGate.Evaluate composition
// with literal expected facts (no alternate-model oracle).
// -----------------------------------------------------------------------------

func TestBackendWireProofGate_CombinedLateDomainEnvelopes(t *testing.T) {
	t.Parallel()

	buildCombinedProofGate := func(t *testing.T, overrideModels []string, lateContract *largebody.BoundedRouteDomainContract) *largebody.BackendWireProofGate {
		t.Helper()
		be := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
		resolver := largebody.WireBackendMap{"backend-1": be}
		execResolver := routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
			return aipsdk.BackendExecutionInference, true
		})
		validator := routing.NewGenerationSelectorValidator(
			nil,
			"backend-1",
			map[string]struct{}{"backend-1": {}},
			execResolver,
			config.ExecutionCompositionSafe,
		)
		overrideGate := largebody.NewRouteOverrideAssessmentGate(&panicSentinelOverrideReader{}, validator, resolver)
		overrideGate.CandidateModels = overrideModels
		var lateGate *largebody.LateSelectorAssessmentGate
		if lateContract != nil {
			lateGate = &largebody.LateSelectorAssessmentGate{
				RouteHintAuthorities: []largebody.RouteHintAuthority{
					{ID: "late-hint", Contract: lateContract},
				},
				BackendResolver: resolver,
				KnownBackends:   map[string]struct{}{"backend-1": {}},
			}
		}
		return largebody.NewBackendWireProofGate(nil, overrideGate, lateGate, resolver)
	}

	combinedProof := func() largebody.Proof {
		proof := makeTask11_7Proof("backend-1:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())
		proof.RequiredCapabilities = []aipapi.Capability{aipapi.CapabilityStreaming}
		return proof
	}

	t.Run("finite override plus finite late union model sets", func(t *testing.T) {
		t.Parallel()
		proofGate := buildCombinedProofGate(t,
			[]string{"gpt-4o-mini"},
			&largebody.BoundedRouteDomainContract{
				TargetBackends: []string{"backend-1"},
				TargetModels:   []string{"gpt-4o"},
			},
		)
		proof := combinedProof()
		decision, reason, _, domainFacts, _ := proofGate.Evaluate(context.Background(), proof)
		if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
			t.Fatalf("expected accept for unioned finite domain, got %v / %v", decision, reason)
		}
		if domainFacts.UniversalModel {
			t.Fatalf("expected finite unioned envelope, got universal: %+v", domainFacts)
		}
		if len(domainFacts.CandidateModels) != 2 || domainFacts.CandidateModels[0] != "gpt-4o" || domainFacts.CandidateModels[1] != "gpt-4o-mini" {
			t.Fatalf("expected unioned models [gpt-4o gpt-4o-mini], got %v", domainFacts.CandidateModels)
		}
		if domainFacts.ProfileID != "openai-responses-v1" {
			t.Fatalf("expected ProfileID openai-responses-v1, got %q", domainFacts.ProfileID)
		}
		if domainFacts.BodyMode != largebody.BodyModeIdentityJSON {
			t.Fatalf("expected BodyMode %v, got %v", largebody.BodyModeIdentityJSON, domainFacts.BodyMode)
		}
		if domainFacts.Rewrite != largebody.NewNoRewrite() {
			t.Fatalf("expected bound no-rewrite contract, got %v", domainFacts.Rewrite)
		}
		if len(domainFacts.RequiredCapabilities) != 1 || domainFacts.RequiredCapabilities[0] != aipapi.CapabilityStreaming {
			t.Fatalf("expected bound capabilities [streaming], got %v", domainFacts.RequiredCapabilities)
		}
	})

	t.Run("universal override plus finite late declines mixed envelope", func(t *testing.T) {
		t.Parallel()
		proofGate := buildCombinedProofGate(t,
			nil,
			&largebody.BoundedRouteDomainContract{
				TargetBackends: []string{"backend-1"},
				TargetModels:   []string{"gpt-4o"},
			},
		)
		decision, reason, _, _, _ := proofGate.Evaluate(context.Background(), combinedProof())
		if decision != largebody.AssessmentDecisionDecline {
			t.Fatalf("expected decline for mixed universal/finite envelopes, got %v", decision)
		}
		if reason != largebody.DeclineReasonProofUncertain {
			t.Fatalf("expected DeclineReasonProofUncertain for mixed envelopes, got %v", reason)
		}
	})

	t.Run("universal override plus universal late stays universal", func(t *testing.T) {
		t.Parallel()
		proofGate := buildCombinedProofGate(t,
			nil,
			&largebody.BoundedRouteDomainContract{
				TargetBackends: []string{"backend-1"},
				UniversalModel: true,
			},
		)
		decision, reason, _, domainFacts, _ := proofGate.Evaluate(context.Background(), combinedProof())
		if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
			t.Fatalf("expected accept for universal envelopes, got %v / %v", decision, reason)
		}
		if !domainFacts.UniversalModel {
			t.Fatalf("expected universal envelope, got %+v", domainFacts)
		}
		if len(domainFacts.CandidateModels) != 0 {
			t.Fatalf("expected no enumerated models in universal envelope, got %v", domainFacts.CandidateModels)
		}
	})
}

// -----------------------------------------------------------------------------
// Test 10: Canceled context immediately declines (Req 8.5)
// -----------------------------------------------------------------------------

func TestTask11_7_CanceledContext_Declines(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	backendResolver := largebody.WireBackendMap{"backend-1": be1}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	proofGate := largebody.NewBackendWireProofGate(initialGate, nil, nil, backendResolver)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	proof := makeTask11_7Proof("backend-1:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())

	decision, reason, _, _, _ := proofGate.Evaluate(ctx, proof)
	if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonCanceled {
		t.Fatalf("expected DeclineReasonCanceled, got %v / %v", decision, reason)
	}
}
