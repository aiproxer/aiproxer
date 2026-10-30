package largebody_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// panicSentinelOverrideReader implements routeoverride.Reader and panics if Snapshot is called.
// This proves the assessment gate performs zero live store reads (Requirements 6.2, 7.3).
type panicSentinelOverrideReader struct {
	snapshotCalled bool
}

func (r *panicSentinelOverrideReader) Snapshot(_ context.Context, _ string) (routeoverride.State, error) {
	r.snapshotCalled = true
	panic("SENTINEL TRAP: RouteOverrideReader.Snapshot called during pure assessment")
}

// testStubDomainBackend implements largebody.WireBackend for route override domain tests.
type testStubDomainBackend struct {
	compatible       bool
	anyAcceptedModel bool
	reason           largebody.WireSupportReason
	requestCompat    bool
}

func (b *testStubDomainBackend) ResolveWireRequest(_ context.Context, _ largebody.WireRequestFacts, _ routing.AttemptCandidate) largebody.WireRequestSupport {
	if b.requestCompat {
		return largebody.WireRequestSupport{Compatible: true}
	}
	return largebody.WireRequestSupport{Compatible: false, Reason: largebody.WireSupportReasonUnsupported}
}

func (b *testStubDomainBackend) ResolveWireDomain(_ context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
	if b.compatible {
		return largebody.WireDomainSupport{
			Compatible:       true,
			AnyAcceptedModel: b.anyAcceptedModel,
		}
	}
	reason := b.reason
	if reason == largebody.WireSupportReasonNone {
		reason = largebody.WireSupportReasonUnsupported
	}
	return largebody.WireDomainSupport{
		Compatible:       false,
		AnyAcceptedModel: false,
		Reason:           reason,
	}
}

func validTestProof() largebody.Proof {
	return largebody.Proof{
		ProfileID:       "openai-responses-v1",
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		RouteSelector:   "backend-1:gpt-4o",
		ClientModel:     "gpt-4o",
		MaxOutputTokens: 4096,
		Facts: largebody.ProtocolFacts{
			RequirementsID: "std-req-v1",
			ControlCount:   1,
		},
		Mode:      largebody.BodyModeIdentityJSON,
		Rewrite:   largebody.NewNoRewrite(),
		Identity:  largebody.NewIdentityDigest([32]byte{1, 2, 3}),
		Source:    largebody.NewSourceDigest([32]byte{4, 5, 6}),
		BodyBytes: 1024,
	}
}

// Test 1: Nil RouteOverrideReader accepts without blocking
func TestRouteOverrideAssessmentGate_NilReaderAccepts(t *testing.T) {
	t.Parallel()
	gate := &largebody.RouteOverrideAssessmentGate{
		OverrideReader: nil,
	}
	decision, reason, facts := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected AssessmentDecisionAccept for nil reader, got %v", decision)
	}
	if reason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone for nil reader, got %v", reason)
	}
	if facts.ProfileID != "" {
		t.Fatalf("expected empty WireDomainFacts for nil reader, got %+v", facts)
	}
}

// Test 2: Mere presence of RouteOverrideReader does NOT block when universal proof is available (Req 7.1)
func TestRouteOverrideAssessmentGate_ReaderPresenceDoesNotBlock_WhenUniversalProof(t *testing.T) {
	t.Parallel()
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}},
		routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
			return aipsdk.BackendExecutionInference, true
		}),
		config.ExecutionCompositionSafe,
	)
	backendResolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true, anyAcceptedModel: true},
	}

	gate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	decision, reason, facts := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected AssessmentDecisionAccept, got %v (reason: %v)", decision, reason)
	}
	if reason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone, got %v", reason)
	}
	if !facts.UniversalModel {
		t.Fatal("expected UniversalModel to be true for unbounded override model domain")
	}
	if sentinel.snapshotCalled {
		t.Fatal("RouteOverrideReader.Snapshot was called during assessment")
	}
}

// Test 3: Zero live store reads (sentinel trap does not panic)
func TestRouteOverrideAssessmentGate_NoLiveStoreReadsSentinel(t *testing.T) {
	t.Parallel()
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}},
		nil,
		config.ExecutionCompositionSafe,
	)
	backendResolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true, anyAcceptedModel: true},
	}

	gate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Evaluate panicked due to store access: %v", r)
		}
	}()
	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept/none, got %v / %v", decision, reason)
	}
	if sentinel.snapshotCalled {
		t.Fatal("Snapshot must not be called")
	}
}

// Test 4: Homogeneous backends all supporting universal model accept (Req 7.7)
func TestRouteOverrideAssessmentGate_HomogeneousBackendsAccept(t *testing.T) {
	t.Parallel()
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{
			"backend-1": {},
			"backend-2": {},
		},
		routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
			return aipsdk.BackendExecutionInference, true
		}),
		config.ExecutionCompositionSafe,
	)
	backendResolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true, anyAcceptedModel: true},
		"backend-2": &testStubDomainBackend{compatible: true, anyAcceptedModel: true},
	}

	gate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	decision, reason, facts := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected homogeneous backends to accept, got %v / %v", decision, reason)
	}
	if !facts.UniversalModel {
		t.Fatal("expected UniversalModel to be true")
	}
}

// Test 5: Heterogeneous backends where one backend is incompatible declines without pruning (Req 7.5, 7.7, 8.4)
func TestRouteOverrideAssessmentGate_HeterogeneousBackendsDecline_WithoutPruning(t *testing.T) {
	t.Parallel()
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{
			"backend-1": {},
			"backend-2": {},
		},
		routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
			return aipsdk.BackendExecutionInference, true
		}),
		config.ExecutionCompositionSafe,
	)
	backendResolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true, anyAcceptedModel: true},
		"backend-2": &testStubDomainBackend{compatible: false, reason: largebody.WireSupportReasonUnsupported},
	}

	gate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline for incompatible heterogeneous backend, got %v", decision)
	}
	if reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", reason)
	}
}

// Test 6: Unbounded override model domain requires AnyAcceptedModel; otherwise decline (Req 7.3, 8.2)
func TestRouteOverrideAssessmentGate_UnboundedModelDomain_RequiresAnyAcceptedModel(t *testing.T) {
	t.Parallel()
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}},
		nil,
		config.ExecutionCompositionSafe,
	)
	// Backend is compatible, but does NOT provide AnyAcceptedModel universal proof
	backendResolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true, anyAcceptedModel: false},
	}

	gate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline when unbounded domain lacks AnyAcceptedModel, got %v", decision)
	}
	if reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", reason)
	}
}

// Test 7: Finite model domain does NOT require AnyAcceptedModel if models are explicitly enumerated (Req 7.2, 8.2)
func TestRouteOverrideAssessmentGate_FiniteModelDomain_DoesNotRequireAnyAcceptedModel(t *testing.T) {
	t.Parallel()
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}},
		nil,
		config.ExecutionCompositionSafe,
	)
	// Backend returns Compatible: true, but AnyAcceptedModel: false
	backendResolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true, anyAcceptedModel: false},
	}

	gate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	// Explicit finite model catalog
	gate.CandidateModels = []string{"model-a", "model-b"}

	decision, reason, facts := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept for finite domain with compatible models, got %v / %v", decision, reason)
	}
	if facts.UniversalModel {
		t.Fatal("expected UniversalModel to be false for finite domain")
	}
	if len(facts.CandidateModels) != 2 {
		t.Fatalf("expected 2 candidate models, got %d", len(facts.CandidateModels))
	}
}

// Test 8: Missing backend in resolver declines with DeclineReasonBackendIncompatible (Req 8.2, 8.3)
func TestRouteOverrideAssessmentGate_MissingBackendInResolver_Declines(t *testing.T) {
	t.Parallel()
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{
			"backend-1": {},
			"backend-2": {},
		},
		nil,
		config.ExecutionCompositionSafe,
	)
	// backend-2 missing from resolver
	backendResolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true, anyAcceptedModel: true},
	}

	gate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline for missing backend in resolver, got %v", decision)
	}
	if reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", reason)
	}
}

// Test 9: Nil KnownBackends in validator declines with DeclineReasonRouteIncompatible (Req 7.2, 7.5)
func TestRouteOverrideAssessmentGate_NilKnownBackends_Declines(t *testing.T) {
	t.Parallel()
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		nil, // unconstrained backends
		nil,
		config.ExecutionCompositionSafe,
	)
	backendResolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true, anyAcceptedModel: true},
	}

	gate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline for nil KnownBackends, got %v", decision)
	}
	if reason != largebody.DeclineReasonRouteIncompatible {
		t.Fatalf("expected DeclineReasonRouteIncompatible, got %v", reason)
	}
}

// Test 10: Empty KnownBackends declines with DeclineReasonRouteIncompatible
func TestRouteOverrideAssessmentGate_EmptyKnownBackends_Declines(t *testing.T) {
	t.Parallel()
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{}, // empty
		nil,
		config.ExecutionCompositionSafe,
	)
	backendResolver := largebody.WireBackendMap{}

	gate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	decision, reason, _ := gate.Evaluate(context.Background(), validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline for empty KnownBackends, got %v", decision)
	}
	if reason != largebody.DeclineReasonRouteIncompatible {
		t.Fatalf("expected DeclineReasonRouteIncompatible, got %v", reason)
	}
}

// Test 11: Canceled context declines with DeclineReasonCanceled
func TestRouteOverrideAssessmentGate_CanceledContext_Declines(t *testing.T) {
	t.Parallel()
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}},
		nil,
		config.ExecutionCompositionSafe,
	)
	backendResolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true, anyAcceptedModel: true},
	}

	gate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	decision, reason, _ := gate.Evaluate(ctx, validTestProof())
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline on canceled context, got %v", decision)
	}
	if reason != largebody.DeclineReasonCanceled {
		t.Fatalf("expected DeclineReasonCanceled, got %v", reason)
	}
}

// Test 12: Empty profile ID declines with DeclineReasonProofUncertain
func TestRouteOverrideAssessmentGate_EmptyProfileID_Declines(t *testing.T) {
	t.Parallel()
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}},
		nil,
		config.ExecutionCompositionSafe,
	)
	backendResolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true, anyAcceptedModel: true},
	}

	gate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	proof := validTestProof()
	proof.ProfileID = ""

	decision, reason, _ := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline for empty profile ID, got %v", decision)
	}
	if reason != largebody.DeclineReasonProofUncertain {
		t.Fatalf("expected DeclineReasonProofUncertain, got %v", reason)
	}
}

// Test 13: Semantic fact budget exceeded declines with DeclineReasonProofUncertain
func TestRouteOverrideAssessmentGate_FactBudgetExceeded_Declines(t *testing.T) {
	t.Parallel()
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}},
		nil,
		config.ExecutionCompositionSafe,
	)
	backendResolver := largebody.WireBackendMap{
		"backend-1": &testStubDomainBackend{compatible: true, anyAcceptedModel: true},
	}

	gate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	proof := validTestProof()

	// Set tiny budget of 5 bytes so profile ID "openai-responses-v1" (19 bytes) fails validation
	ctx := largebody.WithSemanticFactBudget(context.Background(), 5)

	decision, reason, _ := gate.Evaluate(ctx, proof)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline on budget exceeded, got %v", decision)
	}
	if reason != largebody.DeclineReasonProofUncertain {
		t.Fatalf("expected DeclineReasonProofUncertain, got %v", reason)
	}
}
