package largebody_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// =============================================================================
// Task 11.4: Prove exact initial route candidate set
//
// Spec Requirements:
// - Requirement 7: Late-bound route authorities need a pre-certified domain.
//   7.3: Exact initial-route candidates and every member of any required late-route
//        domain shall be proven before wire commit.
//   7.4: Every possible outcome is inside proven envelope.
// - Requirement 8: Explicit same-wire backend compatibility.
//   8.1: Backends expose optional pure wire proof.
//   8.3: Exact initial-route candidates proven before wire commit.
//   8.4: Incompatible candidates cause assessment decline; core shall not
//        prune/reorder candidates, disable fallback/race, or otherwise change
//        routing semantics to keep wire mode.
// - Requirement 10: Replay, retry, failover, and race semantics.
//   10.3: Parallel/race attempts require pre-certified backend/body compatibility
//         for every possible candidate.
//   10.4: Sequential/fallback/weighted/race candidate order and membership exact.
// =============================================================================

// testStubBackend implements largebody.WireBackend for initial route gate testing.
type testStubBackend struct {
	compatible        bool
	needsModelRewrite bool
	disallowRewrite   bool
	reason            largebody.WireSupportReason
	calls             int
	lastFacts         largebody.WireRequestFacts
	lastCand          routing.AttemptCandidate
}

func (b *testStubBackend) ResolveWireRequest(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
	b.calls++
	b.lastFacts = facts
	b.lastCand = cand
	needsRewrite := b.needsModelRewrite
	if facts.ClientModel != cand.Primary.WireModel() {
		if b.disallowRewrite {
			return largebody.WireRequestSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonRewriteUnsupported,
			}
		}
		needsRewrite = true
	}
	return largebody.WireRequestSupport{
		Compatible:        b.compatible,
		NeedsModelRewrite: needsRewrite,
		Reason:            b.reason,
	}
}

func (b *testStubBackend) ResolveWireDomain(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
	return largebody.WireDomainSupport{Compatible: b.compatible}
}

var _ largebody.WireBackend = (*testStubBackend)(nil)

func inferenceResolver() routing.BackendExecutionResolver {
	return routing.BackendExecutionResolverFunc(func(backendID string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionInference, true
	})
}

// -----------------------------------------------------------------------------
// Test 1: Exact candidate set, all compatible => Accepts with exact order
// -----------------------------------------------------------------------------

func TestTask11_4_ExactCandidateSet_AllCompatible_Accepts(t *testing.T) {
	t.Parallel()
	be1 := &testStubBackend{compatible: true}
	be2 := &testStubBackend{compatible: true}

	backends := largebody.WireBackendMap{
		"be1": be1,
		"be2": be2,
	}

	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
		BackendResolver:            backends,
	}

	proof := validProof()
	proof.RouteSelector = "be1:m1 | be2:m2"

	decision, reason, cands := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected accept, got %v (reason=%v)", decision, reason)
	}
	if reason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone, got %v", reason)
	}
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(cands))
	}
	if cands[0].Primary.Backend != "be1" || cands[0].Primary.Model != "m1" {
		t.Fatalf("cand[0] unexpected: %#v", cands[0])
	}
	if cands[1].Primary.Backend != "be2" || cands[1].Primary.Model != "m2" {
		t.Fatalf("cand[1] unexpected: %#v", cands[1])
	}
	if be1.calls != 1 || be2.calls != 1 {
		t.Fatalf("expected 1 call each to be1 and be2, got %d and %d", be1.calls, be2.calls)
	}
}

// -----------------------------------------------------------------------------
// Test 2: Fallback: Incompatible candidate declines ENTIRE request (NO PRUNING)
// Requirement 8.4: core shall NOT prune incompatible candidates.
// -----------------------------------------------------------------------------

func TestTask11_4_Fallback_IncompatibleCandidate_DeclinesEntireRequest_NoPruning(t *testing.T) {
	t.Parallel()
	be1 := &testStubBackend{compatible: true}
	be2 := &testStubBackend{compatible: false, reason: largebody.WireSupportReasonModelUnsupported}

	backends := largebody.WireBackendMap{
		"be1": be1,
		"be2": be2,
	}

	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
		BackendResolver:            backends,
	}

	proof := validProof()
	proof.RouteSelector = "be1:m1 | be2:m2"

	decision, reason, _ := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline when fallback arm is incompatible, got %v", decision)
	}
	if reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", reason)
	}
}

// -----------------------------------------------------------------------------
// Test 3: Weighted: Incompatible candidate declines ENTIRE request (NO PRUNING)
// Even if be1 has 95% weight and be2 has 5%, be2 incompatible declines ALL.
// -----------------------------------------------------------------------------

func TestTask11_4_Weighted_IncompatibleCandidate_DeclinesEntireRequest_NoPruning(t *testing.T) {
	t.Parallel()
	be1 := &testStubBackend{compatible: true}
	be2 := &testStubBackend{compatible: false, reason: largebody.WireSupportReasonUnsupported}

	backends := largebody.WireBackendMap{
		"be1": be1,
		"be2": be2,
	}

	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
		BackendResolver:            backends,
	}

	proof := validProof()
	proof.RouteSelector = "[weight=95]be1:m1^[weight=5]be2:m2"

	decision, reason, _ := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline for weighted incompatible candidate, got %v", decision)
	}
	if reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", reason)
	}
}

// -----------------------------------------------------------------------------
// Test 4: Parallel / race: Incompatible leg declines ENTIRE request (NO PRUNING)
// Requirement 10.3: parallel race requires pre-certified compatibility for EVERY candidate.
// -----------------------------------------------------------------------------

func TestTask11_4_ParallelRace_IncompatibleCandidate_DeclinesEntireRequest_NoPruning(t *testing.T) {
	t.Parallel()
	be1 := &testStubBackend{compatible: true}
	be2 := &testStubBackend{compatible: false, reason: largebody.WireSupportReasonDeliveryUnsupported}

	backends := largebody.WireBackendMap{
		"be1": be1,
		"be2": be2,
	}

	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
		BackendResolver:            backends,
	}

	proof := validProof()
	proof.RouteSelector = "be1:m1!be2:m2"

	decision, reason, _ := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline for parallel race incompatible leg, got %v", decision)
	}
	if reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", reason)
	}
}

// -----------------------------------------------------------------------------
// Test 5: Thinker hybrid parallel with all compatible candidates
// -----------------------------------------------------------------------------

func TestTask11_4_ThinkerHybridParallel_AllCompatible_Accepts(t *testing.T) {
	t.Parallel()
	thinkbe := &testStubBackend{compatible: true}
	exec1 := &testStubBackend{compatible: true}
	exec2 := &testStubBackend{compatible: true}

	backends := largebody.WireBackendMap{
		"thinkbe": thinkbe,
		"exec1":   exec1,
		"exec2":   exec2,
	}

	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
		BackendResolver:            backends,
	}

	proof := validProof()
	proof.RouteSelector = "[thinker]thinkbe:m^exec1:m!exec2:m"

	decision, reason, cands := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected accept, got %v (reason=%v)", decision, reason)
	}
	if reason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone, got %v", reason)
	}
	if len(cands) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(cands))
	}
	// Exact planner identity: thinker carries RoleThinker, executors carry
	// RoleExecutor, all share the thinker-aware selector key (planner.go
	// stickyCandidate/pickThinkerCycle contract).
	var selKey string
	for _, c := range cands {
		if c.SelectorKey == "" {
			t.Fatalf("candidate %q missing thinker-aware selector key", c.Key)
		}
		if selKey == "" {
			selKey = c.SelectorKey
		} else if c.SelectorKey != selKey {
			t.Fatalf("candidate %q selector key %q diverges from %q", c.Key, c.SelectorKey, selKey)
		}
	}
	if got := cands[0].InterleavedRole; got != interleavedstate.RoleThinker {
		t.Fatalf("thinker candidate role = %v, want RoleThinker", got)
	}
	for _, c := range cands[1:] {
		if got := c.InterleavedRole; got != interleavedstate.RoleExecutor {
			t.Fatalf("executor candidate %q role = %v, want RoleExecutor", c.Key, got)
		}
	}
}

// -----------------------------------------------------------------------------
// Test 6: Unknown backend declines with DeclineReasonBackendIncompatible
// -----------------------------------------------------------------------------

func TestTask11_4_UnknownBackend_Declines(t *testing.T) {
	t.Parallel()
	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
		BackendResolver:            largebody.WireBackendMap{}, // empty
	}

	proof := validProof()
	proof.RouteSelector = "unknown-backend:m1"

	decision, reason, _ := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline for unknown backend, got %v", decision)
	}
	if reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", reason)
	}
}

// -----------------------------------------------------------------------------
// Test 7: Alias resolution and default backend canonical reuse
// -----------------------------------------------------------------------------

func TestTask11_4_AliasAndDefaultBackend_Reuse(t *testing.T) {
	t.Parallel()
	be := &testStubBackend{compatible: true}
	backends := largebody.WireBackendMap{
		"primary-be": be,
		"default-be": be,
	}

	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: "^alias-route$", Replacement: "primary-be:alias-model"},
	})
	if err != nil {
		t.Fatalf("NewAliasResolver: %v", err)
	}

	gate := largebody.InitialRouteAssessmentGate{
		Aliases:                    aliases,
		DefaultBackend:             "default-be",
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
		BackendResolver:            backends,
	}

	// 1. Alias route
	proofAlias := validProof()
	proofAlias.RouteSelector = "alias-route"
	decision, reason, cands := gate.Evaluate(context.Background(), proofAlias)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected alias accept, got %v / %v", decision, reason)
	}
	if len(cands) != 1 || cands[0].Primary.Backend != "primary-be" || cands[0].Primary.Model != "alias-model" {
		t.Fatalf("unexpected alias candidates: %#v", cands)
	}

	// 2. Model-only route uses default backend
	proofModelOnly := validProof()
	proofModelOnly.RouteSelector = "gpt-5"
	decision, reason, cands = gate.Evaluate(context.Background(), proofModelOnly)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected default backend accept, got %v / %v", decision, reason)
	}
	if len(cands) != 1 || cands[0].Primary.Backend != "default-be" || cands[0].Primary.Model != "gpt-5" {
		t.Fatalf("unexpected default backend candidates: %#v", cands)
	}

	// 3. Model-only with empty default backend declines
	gateNoDefault := gate
	gateNoDefault.DefaultBackend = ""
	decision, reason, _ = gateNoDefault.Evaluate(context.Background(), proofModelOnly)
	if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonRouteIncompatible {
		t.Fatalf("expected RouteIncompatible for unresolved model-only, got %v / %v", decision, reason)
	}
}

// -----------------------------------------------------------------------------
// Test 8: Unsafe execution composition declines
// -----------------------------------------------------------------------------

func TestTask11_4_UnsafeExecutionComposition_Declines(t *testing.T) {
	t.Parallel()
	be := &testStubBackend{compatible: true}
	backends := largebody.WireBackendMap{
		"be1": be,
		"be2": be,
	}

	nonInferenceResolver := routing.BackendExecutionResolverFunc(func(backendID string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionAgentRuntime, true
	})

	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   nonInferenceResolver,
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
		BackendResolver:            backends,
	}

	proof := validProof()
	proof.RouteSelector = "be1:m1 | be2:m2"

	decision, reason, _ := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline for unsafe execution composition, got %v", decision)
	}
	if reason != largebody.DeclineReasonRouteIncompatible {
		t.Fatalf("expected DeclineReasonRouteIncompatible, got %v", reason)
	}
}

// -----------------------------------------------------------------------------
// Test 9: ProveCandidateSet verifies exact order and membership
// -----------------------------------------------------------------------------

func TestTask11_4_ProveCandidateSet_OrderOrMembershipMismatch_Declines(t *testing.T) {
	t.Parallel()
	be1 := &testStubBackend{compatible: true}
	be2 := &testStubBackend{compatible: true}
	backends := largebody.WireBackendMap{
		"be1": be1,
		"be2": be2,
	}

	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
		BackendResolver:            backends,
	}

	proof := validProof()
	proof.RouteSelector = "be1:m1 | be2:m2"

	cand1 := routing.AttemptCandidate{Primary: routing.Primary{Backend: "be1", Model: "m1"}, Key: "be1:m1"}
	cand2 := routing.AttemptCandidate{Primary: routing.Primary{Backend: "be2", Model: "m2"}, Key: "be2:m2"}

	// 1. Exact match in order accepts
	decision, reason := gate.ProveCandidateSet(context.Background(), proof, []routing.AttemptCandidate{cand1, cand2})
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept for matching candidates, got %v / %v", decision, reason)
	}

	// 2. Swapped order declines
	decision, reason = gate.ProveCandidateSet(context.Background(), proof, []routing.AttemptCandidate{cand2, cand1})
	if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonRouteIncompatible {
		t.Fatalf("expected decline for swapped candidate order, got %v / %v", decision, reason)
	}

	// 3. Pruned candidate (only 1 candidate supplied) declines
	decision, reason = gate.ProveCandidateSet(context.Background(), proof, []routing.AttemptCandidate{cand1})
	if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonRouteIncompatible {
		t.Fatalf("expected decline for pruned candidate set, got %v / %v", decision, reason)
	}
}

// ----------------------------------------------------------------------------
// Test 11: Native model resolver binding into candidate wire model
// -----------------------------------------------------------------------------

type testGateNativeResolver struct {
	mapping map[string]routing.ModelBinding
}

func (r testGateNativeResolver) ResolveModelBinding(backendID, model string) routing.ModelBinding {
	if b, ok := r.mapping[backendID+":"+model]; ok {
		return b
	}
	return routing.ModelBinding{Kind: routing.ModelBindingUnknown}
}

func TestTask11_4_NativeModelBinding_WireModelUsed(t *testing.T) {
	t.Parallel()
	be := &testStubBackend{compatible: true}
	backends := largebody.WireBackendMap{"be": be}

	nativeResolver := testGateNativeResolver{
		mapping: map[string]routing.ModelBinding{
			"be:canonical-m": {
				Kind:   routing.ModelBindingExactCanonical,
				Native: "native-m-wire",
			},
		},
	}

	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
		NativeModelResolver:        nativeResolver,
		BackendResolver:            backends,
	}

	proof := validProof()
	proof.RouteSelector = "be:canonical-m"
	proof.ClientModel = "native-m-wire" // client matches wire model, no rewrite needed

	decision, reason, cands := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept for native model binding, got %v / %v", decision, reason)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Primary.NativeModel != "native-m-wire" {
		t.Fatalf("expected NativeModel to be 'native-m-wire', got %q", cands[0].Primary.NativeModel)
	}
	if be.lastFacts.CandidateModel != "native-m-wire" {
		t.Fatalf("expected lastFacts.CandidateModel to be 'native-m-wire', got %q", be.lastFacts.CandidateModel)
	}
}

// -----------------------------------------------------------------------------
// Test 12: Model rewrite unsupported declines
// -----------------------------------------------------------------------------

func TestTask11_4_ModelRewriteUnsupported_Declines(t *testing.T) {
	t.Parallel()
	be := &testStubBackend{compatible: true, needsModelRewrite: true}
	backends := largebody.WireBackendMap{"be": be}

	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
		BackendResolver:            backends,
	}

	// Proof with NoRewrite
	proof := validProof()
	proof.RouteSelector = "be:model-diff"
	proof.ClientModel = "model-client"
	proof.Rewrite = largebody.NewNoRewrite()

	decision, reason, _ := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline when model differs but rewrite unsupported, got %v", decision)
	}
	if reason != largebody.DeclineReasonRewriteUnsupported {
		t.Fatalf("expected DeclineReasonRewriteUnsupported, got %v", reason)
	}
}

// -----------------------------------------------------------------------------
// Test 13: Canceled context declines with DeclineReasonCanceled
// -----------------------------------------------------------------------------

func TestTask11_4_CanceledContext_Declines(t *testing.T) {
	t.Parallel()
	be := &testStubBackend{compatible: true}
	backends := largebody.WireBackendMap{"be": be}

	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
		BackendResolver:            backends,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	proof := validProof()
	proof.RouteSelector = "be:m"

	decision, reason, _ := gate.Evaluate(ctx, proof)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline on canceled context, got %v", decision)
	}
	if reason != largebody.DeclineReasonCanceled {
		t.Fatalf("expected DeclineReasonCanceled on canceled context, got %v", reason)
	}
}

// -----------------------------------------------------------------------------
// Test 14: Empty profile ID declines with DeclineReasonProofUncertain
// -----------------------------------------------------------------------------

func TestTask11_4_EmptyProfileID_Declines(t *testing.T) {
	t.Parallel()
	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
	}

	proof := validProof()
	proof.ProfileID = ""

	decision, reason, _ := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonProofUncertain {
		t.Fatalf("expected DeclineReasonProofUncertain for empty profile ID, got %v / %v", decision, reason)
	}
}

// -----------------------------------------------------------------------------
// Test 15: Empty selector and empty client model declines with DeclineReasonRouteIncompatible
// -----------------------------------------------------------------------------

func TestTask11_4_EmptySelectorAndModel_Declines(t *testing.T) {
	t.Parallel()
	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
	}

	proof := validProof()
	proof.RouteSelector = ""
	proof.ClientModel = ""

	decision, reason, _ := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonRouteIncompatible {
		t.Fatalf("expected DeclineReasonRouteIncompatible for empty selector and model, got %v / %v", decision, reason)
	}
}

// -----------------------------------------------------------------------------
// Test 16: Wrong-backend canonical model declines with DeclineReasonRouteIncompatible
// -----------------------------------------------------------------------------

func TestTask11_4_WrongBackendCanonical_Declines(t *testing.T) {
	t.Parallel()
	be := &testStubBackend{compatible: true}
	backends := largebody.WireBackendMap{"be": be}

	wrongBackendResolver := testGateNativeResolver{
		mapping: map[string]routing.ModelBinding{
			"be:wrong-m": {
				Kind: routing.ModelBindingWrongBackend,
			},
		},
	}

	gate := largebody.InitialRouteAssessmentGate{
		BackendExecutionResolver:   inferenceResolver(),
		ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
		NativeModelResolver:        wrongBackendResolver,
		BackendResolver:            backends,
	}

	proof := validProof()
	proof.RouteSelector = "be:wrong-m"

	decision, reason, _ := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonRouteIncompatible {
		t.Fatalf("expected DeclineReasonRouteIncompatible for wrong backend canonical, got %v / %v", decision, reason)
	}
}
