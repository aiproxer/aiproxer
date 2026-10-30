package largebody_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// =============================================================================
// Task 12.4: Keep Local Turn and Secret Guard canonical in V1
//
// Spec Requirements:
// - Requirement 5: One Generation-Pinned Wire-Eligibility Summary.
//   5.4: Occupied PlaneLocalTurnHandlers, PlaneSecretGuards, and PlaneSecretGuardExecution
//        are CanonicalRequired static blockers in V1.
// - Requirement 13: Content, Policy, Hook, and Traffic Authorities Remain Authoritative.
//   13.3: Active secret guards, local-turn content logic block unless explicitly certified.
//   13.4: PlaneLocalTurnHandlers and PlaneSecretGuardExecution are explicit V1 blockers
//         when occupied, not generic feature assumptions.
// - Requirement 19: Close Every Post-Commit Full-Call Dependency.
//   19.2: Local-turn handling and secret guards classified as static pre-assessment blockers.
//   19.4: No generic-dependency change accidentally marks them wire-safe.
// =============================================================================

// Helper to build a clean baseline summary and census for genID.
func buildCleanSummaryAndCensus(t *testing.T, genID string) (largebody.WireEligibilitySummary, largebody.DependencyCensus) {
	t.Helper()
	census := largebody.NewStandardDependencyCensus(genID)
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    census.Planes,
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	if summary.HasStaticBlocker() {
		t.Fatalf("expected clean summary to have no static blocker")
	}
	return summary, census
}

// Test 1: Occupied PlaneLocalTurnHandlers in census causes assessment decline in
// AuthorityAssessmentGate (Requirements 5.4, 13.4, 19.2).
func TestTask12_4_LocalTurnOccupied_InCensus_Declines(t *testing.T) {
	t.Parallel()
	genID := "gen-task12-4-lt"
	summary, census := buildCleanSummaryAndCensus(t, genID)

	// Occupy local_turn_handlers in census
	idx, ok := largebody.WireEligibilityPlaneIndex("local_turn_handlers")
	if !ok {
		t.Fatalf("missing plane index for local_turn_handlers")
	}
	census.Planes[idx].Occupied = true

	// AuthorityAssessmentGate directly
	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	decision, reason := gate.Evaluate()
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for occupied local_turn_handlers, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", reason)
	}
}

// Test 2: Occupied PlaneSecretGuards or PlaneSecretGuardExecution in census causes
// assessment decline in AuthorityAssessmentGate
// (Requirements 5.4, 13.3, 13.4, 19.2).
func TestTask12_4_SecretGuardOccupied_InCensus_Declines(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		planeID string
	}{
		{name: "occupied secret_guards", planeID: "secret_guards"},
		{name: "occupied secret_guard_execution", planeID: "secret_guard_execution"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			genID := "gen-task12-4-sg-" + tc.planeID
			summary, census := buildCleanSummaryAndCensus(t, genID)

			idx, ok := largebody.WireEligibilityPlaneIndex(tc.planeID)
			if !ok {
				t.Fatalf("missing plane index for %s", tc.planeID)
			}
			census.Planes[idx].Occupied = true

			// Authority gate
			gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
			decision, reason := gate.Evaluate()
			if decision != largebody.AssessmentDecisionDecline {
				t.Fatalf("expected decline for %s, got %v", tc.name, decision)
			}
			if reason != largebody.DeclineReasonAuthorityBlocker {
				t.Fatalf("expected DeclineReasonAuthorityBlocker for %s, got %v", tc.name, reason)
			}
		})
	}
}

// Test 3: Both Local Turn and Secret Guard planes occupied simultaneously decline
// (Requirements 5.4, 13.4, 19.2).
func TestTask12_4_BothLocalTurnAndSecretGuardOccupied_Declines(t *testing.T) {
	t.Parallel()
	genID := "gen-task12-4-both"
	summary, census := buildCleanSummaryAndCensus(t, genID)

	for _, id := range []string{"local_turn_handlers", "secret_guards", "secret_guard_execution"} {
		idx, ok := largebody.WireEligibilityPlaneIndex(id)
		if !ok {
			t.Fatalf("missing plane index for %s", id)
		}
		census.Planes[idx].Occupied = true
	}

	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	decision, reason := gate.Evaluate()
	if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for both occupied, got %v / %v", decision, reason)
	}
}

// Test 4: Occupied Local Turn in WireEligibilitySummary causes AuthorityAssessmentGate
// to decline (Requirements 5.4, 13.4, 19.2).
func TestTask12_4_LocalTurnOccupied_InSummary_Declines(t *testing.T) {
	t.Parallel()
	genID := "gen-task12-4-summary-lt"
	census := largebody.NewStandardDependencyCensus(genID)

	// Mark local_turn_handlers occupied in summary input
	planes := make([]largebody.PlaneEligibilityInput, len(census.Planes))
	copy(planes, census.Planes)
	idx, ok := largebody.WireEligibilityPlaneIndex("local_turn_handlers")
	if !ok {
		t.Fatalf("missing plane index for local_turn_handlers")
	}
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
		t.Fatalf("summary must report static blocker for occupied local_turn_handlers")
	}

	// The serving authority gate observes the sealed summary blocker directly.
	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	decision, reason := gate.Evaluate()
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline from summary blocker, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker from summary blocker, got %v", reason)
	}
}

// Test 5: Occupied Secret Guard in WireEligibilitySummary causes AuthorityAssessmentGate
// to decline (Requirements 5.4, 13.3, 13.4, 19.2).
func TestTask12_4_SecretGuardOccupied_InSummary_Declines(t *testing.T) {
	t.Parallel()
	genID := "gen-task12-4-summary-sg"
	census := largebody.NewStandardDependencyCensus(genID)

	for _, id := range []string{"secret_guards", "secret_guard_execution"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			planes := make([]largebody.PlaneEligibilityInput, len(census.Planes))
			copy(planes, census.Planes)
			idx, ok := largebody.WireEligibilityPlaneIndex(id)
			if !ok {
				t.Fatalf("missing plane index for %s", id)
			}
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
				t.Fatalf("summary must report static blocker for occupied %s", id)
			}

			gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
			decision, reason := gate.Evaluate()
			if decision != largebody.AssessmentDecisionDecline {
				t.Fatalf("expected AssessmentDecisionDecline from summary blocker, got %v", decision)
			}
			if reason != largebody.DeclineReasonAuthorityBlocker {
				t.Fatalf("expected DeclineReasonAuthorityBlocker from summary blocker, got %v", reason)
			}
		})
	}
}

// Test 6: Anti-tamper regression proof: attempting to weaken Local Turn or Secret Guard access
// classification in census (e.g. marking MetadataOnly, ResponseOnly, or WireContract) MUST DECLINE
// (Requirements 5.4, 13.4, 19.4).
func TestTask12_4_AntiTamper_WeakenedAccessInCensus_Declines(t *testing.T) {
	t.Parallel()
	weakenedAccesses := []struct {
		name   string
		access largebody.PlaneAccess
	}{
		{name: "MetadataOnly", access: largebody.PlaneAccessMetadataOnly},
		{name: "ResponseOnly", access: largebody.PlaneAccessResponseOnly},
		{name: "WireContract", access: largebody.PlaneAccessWireContract},
	}

	canonicalPlanes := []string{
		"local_turn_handlers",
		"secret_guards",
		"secret_guard_execution",
	}

	for _, planeID := range canonicalPlanes {
		for _, wa := range weakenedAccesses {
			t.Run(planeID+"_"+wa.name, func(t *testing.T) {
				t.Parallel()
				genID := "gen-task12-4-tamper-" + planeID + "-" + wa.name
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
}

// Test 7: Anti-tamper regression proof: attempting to compile WireEligibilitySummary with
// weakened access for occupied Local Turn or Secret Guard MUST still register static blocker
// (Requirements 5.4, 13.4, 19.4).
func TestTask12_4_AntiTamper_WeakenedAccessInSummary_SetsBlocker(t *testing.T) {
	t.Parallel()
	weakenedAccesses := []largebody.PlaneAccess{
		largebody.PlaneAccessMetadataOnly,
		largebody.PlaneAccessResponseOnly,
		largebody.PlaneAccessWireContract,
	}

	for _, planeID := range []string{"local_turn_handlers", "secret_guards", "secret_guard_execution"} {
		for _, wa := range weakenedAccesses {
			genID := "gen-task12-4-summary-tamper"
			census := largebody.NewStandardDependencyCensus(genID)
			planes := make([]largebody.PlaneEligibilityInput, len(census.Planes))
			copy(planes, census.Planes)

			idx, ok := largebody.WireEligibilityPlaneIndex(planeID)
			if !ok {
				t.Fatalf("missing plane index for %s", planeID)
			}
			// Attempt to bypass static blocker by claiming weakened access
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
		}
	}
}

// Test 8: Feature plane declaration regression proof: SDK plane declarations for
// Local Turn and Secret Guard must be RequestBodyCanonicalRequired (Requirements 5.4, 13.4).
func TestTask12_4_FeatureSDKDeclarations_RemainCanonicalRequired(t *testing.T) {
	t.Parallel()
	if feature.PlaneLocalTurnHandlers.RequestAccess != feature.RequestBodyCanonicalRequired {
		t.Fatalf("expected PlaneLocalTurnHandlers to have RequestBodyCanonicalRequired, got %v", feature.PlaneLocalTurnHandlers.RequestAccess)
	}
	if feature.PlaneSecretGuards.RequestAccess != feature.RequestBodyCanonicalRequired {
		t.Fatalf("expected PlaneSecretGuards to have RequestBodyCanonicalRequired, got %v", feature.PlaneSecretGuards.RequestAccess)
	}
	if feature.PlaneSecretGuardExecution.RequestAccess != feature.RequestBodyCanonicalRequired {
		t.Fatalf("expected PlaneSecretGuardExecution to have RequestBodyCanonicalRequired, got %v", feature.PlaneSecretGuardExecution.RequestAccess)
	}
}

// Test 9: Standard narrow port census does not contain any wire-safe entries for
// Local Turn or Secret Guard (Requirements 13.4, 19.4).
func TestTask12_4_StandardNarrowPorts_NoWireSafeLocalTurnOrSecretGuard(t *testing.T) {
	t.Parallel()
	for _, portName := range []string{
		"local_turn.handlers",
		"local_turn.handler",
		"secret_guard.execution",
		"secret_guard.guards",
		"secret_guard.evaluator",
	} {
		class, ok := largebody.LookupStandardPort(portName)
		if ok && class.IsWireSafe() {
			t.Fatalf("regression: standard port %q must not be classified as wire-safe", portName)
		}
	}
}

// Test 10: Clean unoccupied Local Turn and Secret Guard permits wire assessment
// (Requirements 5.4, 13.4, 19.4).
func TestTask12_4_UnoccupiedLocalTurnAndSecretGuard_Accepts(t *testing.T) {
	t.Parallel()
	genID := "gen-task12-4-clean"
	summary, census := buildCleanSummaryAndCensus(t, genID)

	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)

	decision, reason := gate.Evaluate()
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected clean unoccupied setup to accept, got %v / %v", decision, reason)
	}
}
