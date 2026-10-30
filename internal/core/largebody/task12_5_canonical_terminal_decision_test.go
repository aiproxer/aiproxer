package largebody_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// =============================================================================
// Task 12.5: Terminal Decision: either block or implement complete source/continuation parity
//
// Selected implementation: Default block (occupied terminal-decision plane stays
// canonical blocker).
//
// Spec Requirements:
// - Requirement 5: One Generation-Pinned Wire-Eligibility Summary.
//   5.4: Occupied PlaneTerminalDecisionProvider is CanonicalRequired in V1
//        unless bounded terminal evidence AND continuation/source semantics
//        are proven without retaining/reconstructing the request Call.
// - Requirement 13: Content, Policy, Hook, and Traffic Authorities Remain Authoritative.
//   13.5: PlaneTerminalDecisionProvider is a V1 blocker unless bounded
//         terminal/continuation source parity is specifically implemented and certified.
// - Requirement 19: Close Every Post-Commit Full-Call Dependency.
//   19.2: Terminal decision/evidence classified as static pre-assessment blocker.
//   19.4: No generic-dependency change accidentally marks it wire-safe or alters
//         DecisionContinue semantics.
// =============================================================================

// Test 1: Occupied PlaneTerminalDecisionProvider in census causes assessment decline in
// AuthorityAssessmentGate
// (Requirements 5.4, 13.5, 19.2).
func TestTask12_5_TerminalDecisionOccupied_InCensus_Declines(t *testing.T) {
	t.Parallel()
	genID := "gen-task12-5-td"
	summary, census := buildCleanSummaryAndCensus(t, genID)

	idx, ok := largebody.WireEligibilityPlaneIndex("terminal_decision_provider")
	if !ok {
		t.Fatalf("missing plane index for terminal_decision_provider")
	}
	census.Planes[idx].Occupied = true

	// AuthorityAssessmentGate directly
	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	decision, reason := gate.Evaluate()
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for occupied terminal_decision_provider, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", reason)
	}
}

// Test 2: Occupied PlaneTerminalDecisionProvider in WireEligibilitySummary causes
// static blocker bit, definitely-canonical disposition, and assessment decline
// (Requirements 5.4, 13.5, 19.2).
func TestTask12_5_TerminalDecisionOccupied_InSummary_Declines(t *testing.T) {
	t.Parallel()
	genID := "gen-task12-5-summary-td"
	census := largebody.NewStandardDependencyCensus(genID)

	idx, ok := largebody.WireEligibilityPlaneIndex("terminal_decision_provider")
	if !ok {
		t.Fatalf("missing plane index for terminal_decision_provider")
	}
	planes := make([]largebody.PlaneEligibilityInput, len(census.Planes))
	copy(planes, census.Planes)
	planes[idx].Occupied = true

	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    planes,
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	if !summary.HasStaticBlocker() {
		t.Fatalf("summary must report static blocker for occupied terminal_decision_provider")
	}
	if summary.PlaneBlockers()&(1<<uint(idx)) == 0 {
		t.Fatalf("summary must set plane blocker bit for occupied terminal_decision_provider")
	}

	// Static disposition must be DefinitelyCanonical with StaticWireReasonStaticBlocker
	disp, reason := summary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   genID,
		ThresholdBytes: 1024,
		ContentLength:  2048,
	})
	if disp != largebody.DefinitelyCanonical {
		t.Fatalf("expected DefinitelyCanonical, got %v", disp)
	}
	if reason != largebody.StaticWireReasonStaticBlocker {
		t.Fatalf("expected StaticWireReasonStaticBlocker, got %v", reason)
	}

	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	decision, declineReason := gate.Evaluate()
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline from summary blocker, got %v", decision)
	}
	if declineReason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker from summary blocker, got %v", declineReason)
	}
}

// Test 3: Anti-tamper regression proof: attempting to weaken terminal_decision_provider access
// classification in census (e.g. marking MetadataOnly, ResponseOnly, or WireContract) MUST DECLINE
// (Requirements 5.4, 13.5, 19.4).
func TestTask12_5_AntiTamper_WeakenedAccessInCensus_Declines(t *testing.T) {
	t.Parallel()
	weakenedAccesses := []struct {
		name   string
		access largebody.PlaneAccess
	}{
		{name: "MetadataOnly", access: largebody.PlaneAccessMetadataOnly},
		{name: "ResponseOnly", access: largebody.PlaneAccessResponseOnly},
		{name: "WireContract", access: largebody.PlaneAccessWireContract},
	}

	planeID := "terminal_decision_provider"
	for _, wa := range weakenedAccesses {
		t.Run(wa.name, func(t *testing.T) {
			t.Parallel()
			genID := "gen-task12-5-tamper-" + wa.name
			summary, census := buildCleanSummaryAndCensus(t, genID)

			idx, ok := largebody.WireEligibilityPlaneIndex(planeID)
			if !ok {
				t.Fatalf("missing plane index for %s", planeID)
			}
			// Attempt to tamper with access classification to make it appear wire-safe
			census.Planes[idx].Access = wa.access
			census.Planes[idx].Occupied = true

			gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
			decision, reason := gate.Evaluate()
			if decision != largebody.AssessmentDecisionDecline {
				t.Fatalf("anti-tamper regression: gate accepted weakened %s with access %s; MUST decline", planeID, wa.name)
			}
			if reason != largebody.DeclineReasonAuthorityBlocker {
				t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", reason)
			}
		})
	}
}

// Test 4: Anti-tamper regression proof: attempting to compile WireEligibilitySummary with
// weakened access for occupied terminal_decision_provider MUST still register static blocker
// (Requirements 5.4, 13.5, 19.4).
func TestTask12_5_AntiTamper_WeakenedAccessInSummary_SetsBlocker(t *testing.T) {
	t.Parallel()
	weakenedAccesses := []largebody.PlaneAccess{
		largebody.PlaneAccessMetadataOnly,
		largebody.PlaneAccessResponseOnly,
		largebody.PlaneAccessWireContract,
	}

	planeID := "terminal_decision_provider"
	for _, wa := range weakenedAccesses {
		t.Run(string(rune(wa)), func(t *testing.T) {
			t.Parallel()
			genID := "gen-task12-5-summary-tamper"
			census := largebody.NewStandardDependencyCensus(genID)
			planes := make([]largebody.PlaneEligibilityInput, len(census.Planes))
			copy(planes, census.Planes)

			idx, ok := largebody.WireEligibilityPlaneIndex(planeID)
			if !ok {
				t.Fatalf("missing plane index for %s", planeID)
			}
			// Attempt to bypass static blocker by claiming weakened access while occupied
			planes[idx].Access = wa
			planes[idx].Occupied = true

			summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
				GenerationID:              genID,
				Planes:                    planes,
				Hooks:                     largebody.HookEligibilityInput{},
				Ports:                     largebody.NarrowPortEligibilityInput{},
				TwoPhaseExecutorAvailable: true,
			}, 1024)
			if err != nil {
				t.Fatalf("CompileWireEligibilitySummary: %v", err)
			}
			if !summary.HasStaticBlocker() {
				t.Fatalf("anti-tamper regression: summary failed to set static blocker for occupied %s with access %v", planeID, wa)
			}
		})
	}
}

// Test 5: Anti-tamper regression proof: attempting to register an occupied extra port
// for terminal decision as wire-safe MUST decline (Requirements 13.5, 19.4).
func TestTask12_5_AntiTamper_ExtraPort_Declines(t *testing.T) {
	t.Parallel()
	for _, portName := range []string{
		"terminal.decision_provider",
		"terminal_decision.provider",
		"terminal_decision_provider",
		"custom_terminal_decision_evaluator",
	} {
		t.Run(portName, func(t *testing.T) {
			t.Parallel()
			genID := "gen-task12-5-port-" + portName
			summary, census := buildCleanSummaryAndCensus(t, genID)

			// Maliciously register port as WireSafe while occupied
			census.RegisterPort(portName, largebody.DependencyClassWireSafe, true)

			gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
			decision, reason := gate.Evaluate()
			if decision != largebody.AssessmentDecisionDecline {
				t.Fatalf("anti-tamper regression: gate accepted occupied terminal decision port %q registered as wire-safe", portName)
			}
			if reason != largebody.DeclineReasonAuthorityBlocker {
				t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", reason)
			}
		})
	}
}

// Test 6: Feature plane declaration regression proof: SDK plane declaration for
// TerminalDecisionProvider must remain RequestBodyCanonicalRequired (Requirements 5.4, 13.5).
func TestTask12_5_FeatureSDKDeclaration_RemainsCanonicalRequired(t *testing.T) {
	t.Parallel()
	if feature.PlaneTerminalDecisionProvider.RequestAccess != feature.RequestBodyCanonicalRequired {
		t.Fatalf("expected PlaneTerminalDecisionProvider to have RequestBodyCanonicalRequired, got %v", feature.PlaneTerminalDecisionProvider.RequestAccess)
	}
	access := feature.PlaneTerminalDecisionProvider.RequestAccess
	if access != feature.RequestBodyCanonicalRequired {
		t.Fatalf("expected declared request access for PlaneTerminalDecisionProvider to be RequestBodyCanonicalRequired, got %v", access)
	}
	if access == feature.RequestBodyResponseOnly {
		t.Fatalf("PlaneTerminalDecisionProvider must not be ResponseOnly: DecisionContinue requires trajectory/continuation state")
	}
	if access == feature.RequestBodyMetadataOnly {
		t.Fatalf("PlaneTerminalDecisionProvider must not be MetadataOnly")
	}
	if access == feature.RequestBodyWireContract {
		t.Fatalf("PlaneTerminalDecisionProvider must not be WireContract without Task 12 parity")
	}
}

// Test 7: Standard narrow port census contains terminal.decision_provider as DependencyClassBlocker
// and no terminal decision port is wire-safe (Requirements 13.5, 19.4).
func TestTask12_5_StandardNarrowPorts_NoWireSafeTerminalDecision(t *testing.T) {
	t.Parallel()
	class, ok := largebody.LookupStandardPort("terminal.decision_provider")
	if !ok {
		t.Fatalf("expected terminal.decision_provider to be registered in standard narrow port census")
	}
	if class != largebody.DependencyClassBlocker {
		t.Fatalf("expected terminal.decision_provider to be DependencyClassBlocker, got %v", class)
	}

	for _, portName := range []string{
		"terminal.decision_provider",
		"terminal_decision.provider",
		"terminal_decision_provider",
	} {
		c, found := largebody.LookupStandardPort(portName)
		if found && c.IsWireSafe() {
			t.Fatalf("regression: port %q must not be wire-safe", portName)
		}
	}
}

// Test 9: Clean unoccupied TerminalDecisionProvider permits wire assessment
// when all other authorities are clean/unoccupied (Requirements 5.4, 13.5, 19.4).
func TestTask12_5_UnoccupiedTerminalDecision_Accepts(t *testing.T) {
	t.Parallel()
	genID := "gen-task12-5-clean"
	summary, census := buildCleanSummaryAndCensus(t, genID)

	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)

	decision, reason := gate.Evaluate()
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected clean unoccupied setup to accept, got %v / %v", decision, reason)
	}
}

// Test 10: Regression proof: No silent DecisionContinue semantic alteration.
// Proves that when TerminalDecisionProvider is configured, the system declines
// wire consideration entirely (forcing canonical execution where full aipapi.Call
// and continuation transactions remain intact), rather than partially executing wire
// mode and silently breaking or altering DecisionContinue semantics (Task 12.5 guardrail).
func TestTask12_5_NoSilentDecisionContinueSemanticAlteration(t *testing.T) {
	t.Parallel()
	genID := "gen-task12-5-guardrail"
	summary, census := buildCleanSummaryAndCensus(t, genID)

	idx, ok := largebody.WireEligibilityPlaneIndex("terminal_decision_provider")
	if !ok {
		t.Fatalf("missing plane index for terminal_decision_provider")
	}
	// Configure terminal_decision_provider
	census.Planes[idx].Occupied = true

	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	dec, reason := gate.Evaluate()
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected canonical decline to preserve DecisionContinue semantics, got %v / %v", dec, reason)
	}
}
