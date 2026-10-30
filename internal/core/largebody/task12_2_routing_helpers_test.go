package largebody_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// =============================================================================
// Task 12.2: Refactor routing/capability/request-size helpers
//
// Spec Requirements:
// - Requirement 7: Late-Bound Route Authorities Need a Pre-Certified Domain
//   7.3: Exact initial-route candidates proven before wire commit.
// - Requirement 8: Explicit Same-Wire Backend Compatibility
//   8.3: Exact initial-route candidates and late-route domain proven before wire commit.
//   8.4: Incompatible candidates cause assessment decline (no pruning).
// - Requirement 19: Close Every Post-Commit Full-Call Dependency
//   19.2: Routing/request-size/capability requirements classified.
//   19.4: Every dependency classified as exact bounded wire fact/view or blocker.
//   19.7: No fake/partial canonical Call; content-dependent estimator without
//         exact source contract => blocker.
// =============================================================================

// Test 1: routing.PrepareSelector is the shared compilation/validation helper
// shared between canonical buildRoutePlan and wire ComposeInitialCandidates (share-don't-fork).
func TestTask12_2_Routing_PrepareSelector_SharedHelper(t *testing.T) {
	t.Parallel()
	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: "fast", Replacement: "openai:gpt-4o"},
	})
	if err != nil {
		t.Fatalf("NewAliasResolver: %v", err)
	}
	classes := inferenceResolver()
	policy := config.ExecutionCompositionSafe
	nativeResolver := testGateNativeResolver{
		mapping: map[string]routing.ModelBinding{
			"openai:gpt-4o": {
				Kind:   routing.ModelBindingExactCanonical,
				Native: "gpt-4o-native",
			},
		},
	}

	// 1. PrepareSelector directly
	sel, err := routing.PrepareSelector("fast", aliases, "openai", classes, policy, nativeResolver)
	if err != nil {
		t.Fatalf("PrepareSelector failed: %v", err)
	}
	if sel == nil || len(sel.Alternatives) == 0 {
		t.Fatalf("PrepareSelector returned empty selector")
	}
	alt := sel.Alternatives[0]
	if alt.Primary == nil {
		t.Fatalf("expected Primary alternative, got nil")
	}
	if alt.Primary.Backend != "openai" || alt.Primary.Model != "gpt-4o" {
		t.Fatalf("expected openai:gpt-4o, got %s:%s", alt.Primary.Backend, alt.Primary.Model)
	}
	if alt.Primary.NativeModel != "gpt-4o-native" {
		t.Fatalf("expected gpt-4o-native, got %s", alt.Primary.NativeModel)
	}

	// 2. ComposeInitialCandidates must produce identical candidates from PrepareSelector
	cands, compSel, err := routing.ComposeInitialCandidates("fast", aliases, "openai", classes, policy, nativeResolver)
	if err != nil {
		t.Fatalf("ComposeInitialCandidates failed: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Primary.NativeModel != "gpt-4o-native" {
		t.Fatalf("expected candidate native model gpt-4o-native, got %s", cands[0].Primary.NativeModel)
	}
	if compSel.Alternatives[0].Primary.NativeModel != sel.Alternatives[0].Primary.NativeModel {
		t.Fatalf("drift between PrepareSelector and ComposeInitialCandidates")
	}
}

// Test 2: When a selector has request-size constraints, routing requires
// content-dependent token estimation. On the wire path, without an exact
// source contract (WireCounter), it MUST decline with DeclineReasonCountingUnsupported.
func TestTask12_2_RequestSize_ConstraintWithoutExactSourceContract_Declines(t *testing.T) {
	t.Parallel()
	be := &testStubBackend{compatible: true}
	backends := largebody.WireBackendMap{"openai": be}

	gate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"openai",
		inferenceResolver(),
		config.ExecutionCompositionSafe,
		nil,
		backends,
	)

	// Selector with request-size constraints: [max_context=5000]openai:gpt-4o
	proof := validProof()
	proof.RouteSelector = "[max_context=5000]openai:gpt-4o"

	decision, reason, _ := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline for selector with request-size constraints without exact source contract, got %v", decision)
	}
	if reason != largebody.DeclineReasonCountingUnsupported {
		t.Fatalf("expected DeclineReasonCountingUnsupported, got %v", reason)
	}
}

// Test 3: When a selector has NO request-size constraints, exact metadata facts
// suffice, and evaluation succeeds without token estimation.
func TestTask12_2_RequestSize_NoConstraints_ExactMetadataSuffices(t *testing.T) {
	t.Parallel()
	be := &testStubBackend{compatible: true}
	backends := largebody.WireBackendMap{"openai": be}

	gate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"openai",
		inferenceResolver(),
		config.ExecutionCompositionSafe,
		nil,
		backends,
	)

	// Normal selector without request-size constraints: openai:gpt-4o
	proof := validProof()
	proof.RouteSelector = "openai:gpt-4o"

	decision, reason, cands := gate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept for selector without request-size constraints, got %v / %v", decision, reason)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
}

// Test 4: EvaluateRouteFacts and EvaluateTurnFacts directly consume Task 12.1's
// bounded wire facts without constructing a Proof or Call.
func TestTask12_2_EvaluateRouteFacts_DirectWireTurnFacts(t *testing.T) {
	t.Parallel()
	be := &testStubBackend{compatible: true}
	backends := largebody.WireBackendMap{"openai-default": be}

	gate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"openai-default",
		inferenceResolver(),
		config.ExecutionCompositionSafe,
		nil,
		backends,
	)

	turnFacts := largebody.DefaultTestWireTurnFacts()

	decision, reason, cands := gate.EvaluateRouteFacts(context.Background(), turnFacts.Route, turnFacts.Protocol, turnFacts.Rewrite, turnFacts.Source, turnFacts.MaxOutput)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept from WireRouteFacts, got %v / %v", decision, reason)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Primary.Model != "gpt-5" {
		t.Fatalf("expected model gpt-5, got %s", cands[0].Primary.Model)
	}

	// Also test EvaluateTurnFacts convenience method
	decision2, reason2, cands2 := gate.EvaluateTurnFacts(context.Background(), turnFacts)
	if decision2 != largebody.AssessmentDecisionAccept || reason2 != largebody.DeclineReasonNone {
		t.Fatalf("expected accept from WireTurnFacts, got %v / %v", decision2, reason2)
	}
	if len(cands2) != 1 {
		t.Fatalf("expected 1 candidate from EvaluateTurnFacts, got %d", len(cands2))
	}
}

// Test 5: GenerationSelectorValidator exposes HasRequestSizeConstraints to detect
// context-size filters on selectors under current generation rules.
func TestTask12_2_GenerationSelectorValidator_HasRequestSizeConstraints(t *testing.T) {
	t.Parallel()
	v := routing.NewGenerationSelectorValidator(
		nil,
		"openai",
		map[string]struct{}{"openai": {}},
		inferenceResolver(),
		config.ExecutionCompositionSafe,
	)

	// Selector without constraints
	hasConstraints, err := v.HasRequestSizeConstraints("openai:gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasConstraints {
		t.Fatalf("expected false for unconstrained selector")
	}

	// Selector with constraints
	hasConstraints, err = v.HasRequestSizeConstraints("[max_context=2000]openai:gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasConstraints {
		t.Fatalf("expected true for constrained selector")
	}

	// Nil validator fails closed
	var nilV *routing.GenerationSelectorValidator
	_, err = nilV.HasRequestSizeConstraints("openai:gpt-4o")
	if err == nil {
		t.Fatalf("expected error for nil validator")
	}
}

// Test 6: FailoverRequirementSet can be derived from static metadata
// aipapi.ProtocolRequirements without requiring a full aipapi.Call.
func TestTask12_2_Capabilities_NewFailoverRequirementSetFromRequirements(t *testing.T) {
	t.Parallel()
	req := aipapi.ProtocolRequirements{
		Capabilities: []aipapi.Capability{
			aipapi.CapabilityTools,
			aipapi.CapabilityVision,
		},
		ReasoningDialects: []aipapi.DialectRequirement{
			{Kind: "reasoning", Dialect: "standard"},
		},
	}

	set := capabilities.NewFailoverRequirementSetFromRequirements(req)
	if len(set.Required.Capabilities) != 2 {
		t.Fatalf("expected 2 capabilities, got %d", len(set.Required.Capabilities))
	}
	if len(set.Required.ReasoningDialects) != 1 {
		t.Fatalf("expected 1 reasoning dialect, got %d", len(set.Required.ReasoningDialects))
	}

	// Match against compatible supported requirements
	supported := aipapi.ProtocolRequirements{
		Capabilities: []aipapi.Capability{
			aipapi.CapabilityTools,
			aipapi.CapabilityVision,
			aipapi.CapabilityStreaming,
		},
		ReasoningDialects: []aipapi.DialectRequirement{
			{Kind: "reasoning", Dialect: "standard"},
		},
	}
	if !set.CandidateMatchesFailoverRequirements(supported, aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{"standard"}}) {
		t.Fatalf("expected candidate to match failover requirements")
	}
}
