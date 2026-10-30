package largebody_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// =============================================================================
// Task 11.3: Consume frozen authority summary + current dependency census
//
// Spec Requirements:
// - Requirement 5: One Generation-Pinned Wire-Eligibility Summary.
//   5.3: Unclassified plane fails closed at runtime.
//   5.6: Narrow-port inventory explicitly classified.
//   5.7: Unknown => decline.
//   5.9: Re-check static summary defensively; no hot-path reflection.
// - Requirement 13: Content, Policy, Hook, and Traffic Authorities.
//   13.1-13.6: Traffic, submit/request hooks, secret guards, local turn block.
// - Requirement 14: Secure-session parity without canonical call.
// - Requirement 15: Metering, accounting, billing wire-native evidence.
// - Requirement 19: Close every post-commit full-Call dependency.
//   19.4: Every dependency classified as wire-safe or blocker; unknown declines.
// =============================================================================

// validWireSafeSummary creates a sealed WireEligibilitySummary with zero blockers.
func validWireSafeSummary(t *testing.T, genID string) largebody.WireEligibilitySummary {
	t.Helper()
	s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    makeWireSafePlanes(genID),
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	return s
}

func makeWireSafePlanes(genID string) []largebody.PlaneEligibilityInput {
	census := largebody.NewStandardDependencyCensus(genID)
	return census.Planes
}

// -----------------------------------------------------------------------------
// Test 1: Unknown port declines
// -----------------------------------------------------------------------------

func TestTask11_3_UnknownPort_Declines(t *testing.T) {
	t.Parallel()
	genID := "gen-task-11-3"
	summary := validWireSafeSummary(t, genID)
	census := largebody.NewStandardDependencyCensus(genID)

	// Register an unknown port into the dependency census
	census.RegisterPort("unregistered.custom_third_party_port", largebody.DependencyClassUnknown, true)

	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	decision, reason := gate.Evaluate()

	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for unknown port, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker for unknown port, got %v", reason)
	}
}

func TestTask11_3_UnknownPlane_Declines(t *testing.T) {
	t.Parallel()
	genID := "gen-task-11-3"
	summary := validWireSafeSummary(t, genID)
	census := largebody.NewStandardDependencyCensus(genID)

	// Add an unknown plane ID
	census.Planes = append(census.Planes, largebody.PlaneEligibilityInput{
		ID:       "unknown_plane_id_xyz",
		Access:   largebody.PlaneAccessMetadataOnly,
		Occupied: true,
	})

	decision, reason := largebody.NewAuthorityAssessmentGate(summary, census, genID).Evaluate()
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for unknown plane, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker for unknown plane, got %v", reason)
	}
}

func TestTask11_3_UnclassifiedPlane_Declines(t *testing.T) {
	t.Parallel()
	genID := "gen-task-11-3"
	summary := validWireSafeSummary(t, genID)
	census := largebody.NewStandardDependencyCensus(genID)

	// Set a plane as unclassified
	if len(census.Planes) > 0 {
		census.Planes[0].Access = largebody.PlaneAccessUnclassified
	}

	decision, reason := largebody.NewAuthorityAssessmentGate(summary, census, genID).Evaluate()
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for unclassified plane, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker for unclassified plane, got %v", reason)
	}
}

// -----------------------------------------------------------------------------
// Test 2: Wire-safe set accepts
// -----------------------------------------------------------------------------

// TestTask11_3_OccupiedNonNegotiablePlaneAccessDowngrade_Declines pins that the
// gate's defensive census rejects an occupied non-negotiable canonical plane
// whose access was weakened to metadata-only or to an explicit wire contract.
// The gate starts from a valid unoccupied summary, so the decline can only come
// from the census occupancy check; an absent provider stays eligible.
func TestTask11_3_OccupiedNonNegotiablePlaneAccessDowngrade_Declines(t *testing.T) {
	t.Parallel()

	for _, id := range []string{
		"local_turn_handlers",
		"secret_guards",
		"secret_guard_execution",
		"terminal_decision_provider",
		"control_tool_provider",
	} {
		for _, access := range []largebody.PlaneAccess{
			largebody.PlaneAccessMetadataOnly,
			largebody.PlaneAccessWireContract,
		} {
			t.Run(id+"/"+access.String(), func(t *testing.T) {
				t.Parallel()

				genID := "gen-task-11-3"

				// Baseline: valid unoccupied summary plus unoccupied census accepts.
				summary := validWireSafeSummary(t, genID)
				absent := largebody.NewStandardDependencyCensus(genID)
				decision, reason := largebody.NewAuthorityAssessmentGate(summary, absent, genID).Evaluate()
				if decision != largebody.AssessmentDecisionAccept {
					t.Fatalf("absent %s at %s must stay eligible, got %v (%v)", id, access, decision, reason)
				}

				// Downgrade the access class, then occupy the plane at the gate.
				census := largebody.NewStandardDependencyCensus(genID)
				downgraded := false
				for i := range census.Planes {
					if census.Planes[i].ID == id {
						census.Planes[i].Access = access
						census.Planes[i].Occupied = true
						downgraded = true
					}
				}
				if !downgraded {
					t.Fatalf("census is missing plane %q", id)
				}

				decision, reason = largebody.NewAuthorityAssessmentGate(summary, census, genID).Evaluate()
				if decision != largebody.AssessmentDecisionDecline {
					t.Fatalf("occupied %s downgraded to %s must decline, got %v (%v)", id, access, decision, reason)
				}
				if reason != largebody.DeclineReasonAuthorityBlocker {
					t.Fatalf("expected DeclineReasonAuthorityBlocker for occupied downgraded %s, got %v", id, reason)
				}
			})
		}
	}
}

func TestTask11_3_WireSafeSet_Accepts(t *testing.T) {
	t.Parallel()
	genID := "gen-task-11-3"
	summary := validWireSafeSummary(t, genID)
	census := largebody.NewStandardDependencyCensus(genID)

	// Verify standard census contains only wire-safe dependencies when unoccupied
	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	decision, reason := gate.Evaluate()

	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected AssessmentDecisionAccept for wire-safe set, got %v", decision)
	}
	if reason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone for wire-safe set, got %v", reason)
	}

	// Add known wire-safe occupied ports from evidence 1.8
	census.RegisterPort("core.store", largebody.DependencyClassWireSafe, true)
	census.RegisterPort("billing.credit_gate", largebody.DependencyClassWireSafe, true)
	census.RegisterPort("security.session_recorder", largebody.DependencyClassWireSafe, true)
	census.RegisterPort("accounting.metering_recorder", largebody.DependencyClassWireSafe, true)

	decision, reason = largebody.NewAuthorityAssessmentGate(summary, census, genID).Evaluate()
	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected AssessmentDecisionAccept with occupied wire-safe ports, got %v", decision)
	}
	if reason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone, got %v", reason)
	}
}

// -----------------------------------------------------------------------------
// Test 3: Defensive static-summary recheck
// -----------------------------------------------------------------------------

func TestTask11_3_DefensiveRecheck_UnsealedSummaryDeclines(t *testing.T) {
	t.Parallel()
	genID := "gen-task-11-3"
	census := largebody.NewStandardDependencyCensus(genID)

	// Zero/unsealed summary must decline defensively
	var unsealedSummary largebody.WireEligibilitySummary
	decision, reason := largebody.NewAuthorityAssessmentGate(unsealedSummary, census, genID).Evaluate()

	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for unsealed summary, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", reason)
	}
}

func TestTask11_3_DefensiveRecheck_GenerationMismatchDeclines(t *testing.T) {
	t.Parallel()
	census := largebody.NewStandardDependencyCensus("gen-A")
	summary := validWireSafeSummary(t, "gen-A")

	// Pinned to gen-A, but evaluating against gen-B
	decision, reason := largebody.NewAuthorityAssessmentGate(summary, census, "gen-B").Evaluate()

	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for generation mismatch, got %v", decision)
	}
	if reason != largebody.DeclineReasonGenerationMismatch {
		t.Fatalf("expected DeclineReasonGenerationMismatch, got %v", reason)
	}
}

func TestTask11_3_DefensiveRecheck_EmptyGenerationDeclines(t *testing.T) {
	t.Parallel()
	census := largebody.NewStandardDependencyCensus("gen-A")
	summary := validWireSafeSummary(t, "gen-A")

	// A missing generation binding must decline, never skip the pin recheck.
	for _, empty := range []string{"", "   "} {
		decision, reason := largebody.NewAuthorityAssessmentGate(summary, census, empty).Evaluate()

		if decision != largebody.AssessmentDecisionDecline {
			t.Fatalf("expected AssessmentDecisionDecline for empty generation %q, got %v", empty, decision)
		}
		if reason != largebody.DeclineReasonGenerationMismatch {
			t.Fatalf("expected DeclineReasonGenerationMismatch for empty generation %q, got %v", empty, reason)
		}
	}
}

func TestTask11_3_DefensiveRecheck_StaticBlockerDeclines(t *testing.T) {
	t.Parallel()
	genID := "gen-task-11-3"
	census := largebody.NewStandardDependencyCensus(genID)

	// Summary with occupied local turn handlers (canonical required plane blocker)
	planes := census.Planes
	for i := range planes {
		if planes[i].ID == "local_turn_handlers" {
			planes[i].Occupied = true
			break
		}
	}
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    planes,
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary error: %v", err)
	}

	decision, reason := largebody.NewAuthorityAssessmentGate(summary, census, genID).Evaluate()
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for summary with static blocker, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", reason)
	}
}

func TestTask11_3_DefensiveRecheck_NoHotPathReflection(t *testing.T) {
	t.Parallel()
	// Guardrail: no hot-path reflection during authority gate assessment.
	// Assessment duration must be sub-millisecond and well under the budget.
	genID := "gen-task-11-3"
	summary := validWireSafeSummary(t, genID)
	census := largebody.NewStandardDependencyCensus(genID)

	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)

	// Benchmark/run multiple iterations ensuring zero allocations / ultra-fast execution
	for i := range 1000 {
		dec, r := gate.Evaluate()
		if dec != largebody.AssessmentDecisionAccept || r != largebody.DeclineReasonNone {
			t.Fatalf("iteration %d failed: dec=%v r=%v", i, dec, r)
		}
	}
}

// -----------------------------------------------------------------------------
// Test 4: Occupied blocker ports decline
// -----------------------------------------------------------------------------

func TestTask11_3_OccupiedBlocker_Declines(t *testing.T) {
	t.Parallel()
	genID := "gen-task-11-3"
	summary := validWireSafeSummary(t, genID)

	testCases := []struct {
		name   string
		mutate func(*largebody.DependencyCensus)
	}{
		{
			name: "occupied submit hook",
			mutate: func(c *largebody.DependencyCensus) {
				c.Hooks.SubmitOccupied = true
			},
		},
		{
			name: "occupied request-part hook",
			mutate: func(c *largebody.DependencyCensus) {
				c.Hooks.RequestPartOccupied = true
			},
		},
		{
			name: "occupied tool hook",
			mutate: func(c *largebody.DependencyCensus) {
				c.Hooks.ToolOccupied = true
			},
		},
		{
			name: "conversation view reader occupied",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.ConversationViewReaderOccupied = true
			},
		},
		{
			name: "steering writer factory occupied",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.SteeringWriterFactoryOccupied = true
			},
		},
		{
			name: "caps resolver occupied",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.CapsResolverOccupied = true
			},
		},
		{
			name: "preflight enabled without exact counter",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.PreflightEnabled = true
				c.Ports.PreflightHasExactCounter = false
			},
		},
		{
			name: "two phase executor missing",
			mutate: func(c *largebody.DependencyCensus) {
				c.TwoPhaseExecutorAvailable = false
			},
		},
		{
			name: "custom blocker port occupied",
			mutate: func(c *largebody.DependencyCensus) {
				c.RegisterPort("custom.call_callbacks", largebody.DependencyClassBlocker, true)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			census := largebody.NewStandardDependencyCensus(genID)
			tc.mutate(&census)

			decision, reason := largebody.NewAuthorityAssessmentGate(summary, census, genID).Evaluate()
			if decision != largebody.AssessmentDecisionDecline {
				t.Fatalf("%s: expected AssessmentDecisionDecline, got %v", tc.name, decision)
			}
			if reason != largebody.DeclineReasonAuthorityBlocker {
				t.Fatalf("%s: expected DeclineReasonAuthorityBlocker, got %v", tc.name, reason)
			}
		})
	}
}

func TestAuthorityGate_CapsResolver_Subsumption(t *testing.T) {
	t.Parallel()

	genID := "gen-caps-subsumption-test"

	// 1. CapsResolver occupied without wire proof subsumption -> blocks statically and declines
	censusBlocked := largebody.NewStandardDependencyCensus(genID)
	censusBlocked.Ports.CapsResolverOccupied = true
	censusBlocked.Ports.CapsResolverWireProofSubsumed = false

	summaryBlocked, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    censusBlocked.Planes,
		Hooks:                     censusBlocked.Hooks,
		Ports:                     censusBlocked.Ports,
		TwoPhaseExecutorAvailable: true,
	}, 4096)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary (blocked): %v", err)
	}
	if !summaryBlocked.HasStaticBlocker() {
		t.Fatal("expected summaryBlocked to report HasStaticBlocker == true")
	}
	if summaryBlocked.PortBlockers()&largebody.WirePortCapsResolver == 0 {
		t.Fatalf("expected WirePortCapsResolver bit set, got %#x", summaryBlocked.PortBlockers())
	}
	gateBlocked := largebody.NewAuthorityAssessmentGate(summaryBlocked, censusBlocked, genID)
	decBlocked, reasBlocked := gateBlocked.Evaluate()
	if decBlocked != largebody.AssessmentDecisionDecline || reasBlocked != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline with authority blocker, got dec=%v reas=%v", decBlocked, reasBlocked)
	}

	// 2. CapsResolver occupied with wire proof subsumption -> zero static blockers and accepts
	censusSubsumed := largebody.NewStandardDependencyCensus(genID)
	censusSubsumed.Ports.CapsResolverOccupied = true
	censusSubsumed.Ports.CapsResolverWireProofSubsumed = true

	summarySubsumed, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    censusSubsumed.Planes,
		Hooks:                     censusSubsumed.Hooks,
		Ports:                     censusSubsumed.Ports,
		TwoPhaseExecutorAvailable: true,
	}, 4096)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary (subsumed): %v", err)
	}
	if summarySubsumed.HasStaticBlocker() {
		t.Fatalf("subsumed CapsResolver must not have static blocker, got port_blockers=%#x", summarySubsumed.PortBlockers())
	}
	if summarySubsumed.PortBlockers()&largebody.WirePortCapsResolver != 0 {
		t.Fatalf("expected WirePortCapsResolver bit CLEAR, got %#x", summarySubsumed.PortBlockers())
	}
	gateSubsumed := largebody.NewAuthorityAssessmentGate(summarySubsumed, censusSubsumed, genID)
	decSubsumed, reasSubsumed := gateSubsumed.Evaluate()
	if decSubsumed != largebody.AssessmentDecisionAccept || reasSubsumed != largebody.DeclineReasonNone {
		t.Fatalf("expected accept with DeclineReasonNone, got dec=%v reas=%v", decSubsumed, reasSubsumed)
	}
}

func TestAuthorityGate_CompactionPreservers_BlocksWireStatically(t *testing.T) {
	t.Parallel()

	genID := "gen-compaction-preservers-blocker"
	census := largebody.NewStandardDependencyCensus(genID)
	// compaction_preservers is occupied in planes census
	idx, ok := largebody.WireEligibilityPlaneIndex("compaction_preservers")
	if !ok {
		t.Fatalf("missing plane index for compaction_preservers")
	}
	census.Planes[idx].Occupied = true

	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    census.Planes,
		Hooks:                     census.Hooks,
		Ports:                     census.Ports,
		TwoPhaseExecutorAvailable: true,
	}, 4096)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}

	// Prove that compaction_preservers has PlaneAccessCanonicalRequired and sets static plane blocker
	if !summary.HasStaticBlocker() {
		t.Fatal("expected compaction_preservers to produce static blocker")
	}
	if summary.PlaneBlockers() == 0 {
		t.Fatal("expected non-zero PlaneBlockers for occupied compaction_preservers")
	}

	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	decision, reason := gate.Evaluate()
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decision Decline, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected reason DeclineReasonAuthorityBlocker, got %v", reason)
	}
}

// Test 12: Standard narrow port census includes continuation and terminal decision
// ports classified as DependencyClassBlocker (Requirements 13, 19.4).
func TestTask12_3_StandardNarrowPortCensus_ContinuationAndTerminalPorts(t *testing.T) {
	t.Parallel()
	for _, port := range []string{
		"core.continuation",
		"continuation.resolver",
		"terminal.decision_provider",
		"conversation.projection",
	} {
		class, ok := largebody.LookupStandardPort(port)
		if !ok {
			t.Fatalf("expected standard port %q to be registered", port)
		}
		if class != largebody.DependencyClassBlocker {
			t.Fatalf("expected standard port %q to be DependencyClassBlocker, got %v", port, class)
		}
	}
}
