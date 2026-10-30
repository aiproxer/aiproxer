package largebody_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// Task 3 (large-payload-eligibility-consolidation): defensive authority
// assessment must evaluate current hook/narrow-port facts independently of
// the earlier sealed summary, using the same policy as static compilation.
//
// Every expected mask and reason below is hardcoded from the V1 policy
// matrix (research.md "Current Policy Matrix", design Decision 3); no
// expectation is computed through compileHookChains/compilePortBlockers.
// A clean sealed summary plus a separately altered census must still
// decline; positive exemption controls must still accept.

// hookFactCases pins the four occupied hook chains. wantBit is the literal
// HookChain bit position (Submit=1<<0, RequestPart=1<<1, ResponsePart=1<<2,
// Tool=1<<3); Blocker 2 keeps response-part blocking when occupied. Each
// occupied chain must be the static summary's only blocker bit.
func TestEligibilityConsolidation_CurrentHookFactsDeclineDespiteCleanSummary(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		set     func(*largebody.HookEligibilityInput)
		wantBit largebody.HookChain
	}{
		{"submit", func(h *largebody.HookEligibilityInput) { h.SubmitOccupied = true }, largebody.HookChain(1 << 0)},
		{"request_part", func(h *largebody.HookEligibilityInput) { h.RequestPartOccupied = true }, largebody.HookChain(1 << 1)},
		{"response_part", func(h *largebody.HookEligibilityInput) { h.ResponsePartOccupied = true }, largebody.HookChain(1 << 2)},
		{"tool", func(h *largebody.HookEligibilityInput) { h.ToolOccupied = true }, largebody.HookChain(1 << 3)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			genID := "gen-eligibility-consolidation-hooks"
			summary := validWireSafeSummary(t, genID)
			if summary.HasStaticBlocker() {
				t.Fatalf("baseline summary must be clean, got %v", summary)
			}

			census := largebody.NewStandardDependencyCensus(genID)
			tc.set(&census.Hooks)

			// Static stage on the same facts names the hardcoded bit.
			staged, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
				GenerationID:              genID,
				Planes:                    census.Planes,
				Hooks:                     census.Hooks,
				Ports:                     census.Ports,
				TwoPhaseExecutorAvailable: census.TwoPhaseExecutorAvailable,
			}, 4096)
			if err != nil {
				t.Fatalf("CompileWireEligibilitySummary: %v", err)
			}
			if !staged.HasStaticBlocker() {
				t.Fatalf("occupied %s hook must block statically, got %v", tc.name, staged)
			}
			if staged.HookBlockers() != tc.wantBit {
				t.Fatalf("static hook mask for %s = %#x, want exactly %#x", tc.name, uint8(staged.HookBlockers()), uint8(tc.wantBit))
			}

			// Defensive stage with a clean sealed summary plus the altered
			// current census must still decline with the same reason.
			decision, reason := largebody.NewAuthorityAssessmentGate(summary, census, genID).Evaluate()
			if decision != largebody.AssessmentDecisionDecline {
				t.Fatalf("occupied %s hook with clean summary must decline, got %v", tc.name, decision)
			}
			if reason != largebody.DeclineReasonAuthorityBlocker {
				t.Fatalf("occupied %s hook must decline with AuthorityBlocker, got %v", tc.name, reason)
			}
		})
	}
}

// portFactCases pins every V1 narrow-port blocker plus the unavailable
// two-phase capability. wantBit is the literal WirePortBlocker position in
// declaration order (BackendsEmpty=1<<0 ... TwoPhaseExecutorMissing=1<<18);
// 19 bits total, no width change.
func TestEligibilityConsolidation_CurrentPortFactsDeclineDespiteCleanSummary(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		set     func(c *largebody.DependencyCensus)
		wantBit uint32
	}{
		{"backends_empty", func(c *largebody.DependencyCensus) { c.Ports.BackendsEmpty = true }, 1 << 0},
		{"conversation_view_reader", func(c *largebody.DependencyCensus) { c.Ports.ConversationViewReaderOccupied = true }, 1 << 1},
		{"conversation_view_tagger", func(c *largebody.DependencyCensus) { c.Ports.ConversationViewTaggerOccupied = true }, 1 << 2},
		{"steering_writer_factory", func(c *largebody.DependencyCensus) { c.Ports.SteeringWriterFactoryOccupied = true }, 1 << 3},
		{"exposure_admission", func(c *largebody.DependencyCensus) { c.Ports.ExposureAdmissionOccupied = true }, 1 << 4},
		{"billing_identity_callbacks", func(c *largebody.DependencyCensus) { c.Ports.BillingIdentityCustomCallbacks = true }, 1 << 5},
		{"caps_resolver", func(c *largebody.DependencyCensus) { c.Ports.CapsResolverOccupied = true }, 1 << 6},
		{"catalog_resolver", func(c *largebody.DependencyCensus) { c.Ports.CatalogResolverOccupied = true }, 1 << 7},
		{"eligibility_resolver", func(c *largebody.DependencyCensus) { c.Ports.EligibilityResolverOccupied = true }, 1 << 8},
		{"request_token_estimator", func(c *largebody.DependencyCensus) { c.Ports.RequestTokenEstimatorOccupied = true }, 1 << 9},
		{"preflight_count_only", func(c *largebody.DependencyCensus) { c.Ports.PreflightEnabled = true }, 1 << 10},
		{"stream_usage", func(c *largebody.DependencyCensus) { c.Ports.StreamUsageOccupied = true }, 1 << 11},
		{"admin_count_service", func(c *largebody.DependencyCensus) { c.Ports.AdminCountServiceOccupied = true }, 1 << 12},
		{"interleaved_processor", func(c *largebody.DependencyCensus) { c.Ports.InterleavedProcessorOccupied = true }, 1 << 13},
		{"compaction_detector", func(c *largebody.DependencyCensus) { c.Ports.CompactionDetectorOccupied = true }, 1 << 14},
		{"traffic_capturing", func(c *largebody.DependencyCensus) { c.Ports.TrafficCapturing = true }, 1 << 15},
		{"token_counting", func(c *largebody.DependencyCensus) { c.Ports.TokenCountingRequired = true }, 1 << 16},
		{"custom_call_callbacks", func(c *largebody.DependencyCensus) { c.Ports.CustomCallCallbacksPresent = true }, 1 << 17},
		{"two_phase_executor_missing", func(c *largebody.DependencyCensus) { c.TwoPhaseExecutorAvailable = false }, 1 << 18},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			genID := "gen-eligibility-consolidation-ports"
			summary := validWireSafeSummary(t, genID)
			if summary.HasStaticBlocker() {
				t.Fatalf("baseline summary must be clean, got %v", summary)
			}

			census := largebody.NewStandardDependencyCensus(genID)
			tc.set(&census)

			// Static stage on the same facts names the hardcoded bit and no other port bit.
			staged, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
				GenerationID:              genID,
				Planes:                    census.Planes,
				Hooks:                     census.Hooks,
				Ports:                     census.Ports,
				TwoPhaseExecutorAvailable: census.TwoPhaseExecutorAvailable,
			}, 4096)
			if err != nil {
				t.Fatalf("CompileWireEligibilitySummary: %v", err)
			}
			if !staged.HasStaticBlocker() {
				t.Fatalf("%s must block statically, got %v", tc.name, staged)
			}
			if uint32(staged.PortBlockers()) != tc.wantBit {
				t.Fatalf("static port mask for %s = %#x, want exactly %#x", tc.name, uint32(staged.PortBlockers()), tc.wantBit)
			}

			// Defensive stage with a clean sealed summary plus the altered
			// current census must still decline with the same reason.
			decision, reason := largebody.NewAuthorityAssessmentGate(summary, census, genID).Evaluate()
			if decision != largebody.AssessmentDecisionDecline {
				t.Fatalf("%s with clean summary must decline, got %v", tc.name, decision)
			}
			if reason != largebody.DeclineReasonAuthorityBlocker {
				t.Fatalf("%s must decline with AuthorityBlocker, got %v", tc.name, reason)
			}
		})
	}
}

// TestEligibilityConsolidation_PositiveExemptionControlsRemainNonblocking
// pins the five conditional narrow-port exemptions: satisfied exemption
// conditions stay eligible for assessment in the defensive stage.
func TestEligibilityConsolidation_PositiveExemptionControlsRemainNonblocking(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		set  func(c *largebody.DependencyCensus)
	}{
		{"preflight_with_exact_counter", func(c *largebody.DependencyCensus) {
			c.Ports.PreflightEnabled = true
			c.Ports.PreflightHasExactCounter = true
		}},
		{"token_counting_with_exact_counter", func(c *largebody.DependencyCensus) {
			c.Ports.TokenCountingRequired = true
			c.Ports.TokenCountingHasExactCounter = true
		}},
		{"caps_resolver_wire_proof_subsumed", func(c *largebody.DependencyCensus) {
			c.Ports.CapsResolverOccupied = true
			c.Ports.CapsResolverWireProofSubsumed = true
		}},
		{"conversation_reader_fresh_aleg_supported", func(c *largebody.DependencyCensus) {
			c.Ports.ConversationViewReaderOccupied = true
			c.Ports.ConversationReaderFreshALegSupported = true
		}},
		{"compaction_detector_wire_supported", func(c *largebody.DependencyCensus) {
			c.Ports.CompactionDetectorOccupied = true
			c.Ports.CompactionDetectorWireSupported = true
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			genID := "gen-eligibility-consolidation-exemptions"
			summary := validWireSafeSummary(t, genID)

			census := largebody.NewStandardDependencyCensus(genID)
			tc.set(&census)

			decision, reason := largebody.NewAuthorityAssessmentGate(summary, census, genID).Evaluate()
			if decision != largebody.AssessmentDecisionAccept {
				t.Fatalf("%s must stay eligible for assessment, got %v (%v)", tc.name, decision, reason)
			}
			if reason != largebody.DeclineReasonNone {
				t.Fatalf("%s must carry DeclineReasonNone, got %v", tc.name, reason)
			}
		})
	}
}

// TestEligibilityConsolidation_CustomDefensesPreserved pins the custom-port
// rules independently of the hook/port consolidation: unknown class
// declines even when unoccupied, occupied blockers decline, and occupied
// protected local-turn/secret-guard/terminal-decision names decline even
// when marked wire-safe. No control-tool substring rule is added.
func TestEligibilityConsolidation_CustomDefensesPreserved(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		register func(c *largebody.DependencyCensus)
	}{
		{"unknown_unoccupied", func(c *largebody.DependencyCensus) {
			c.RegisterPort("vendor.future_port", largebody.DependencyClassUnknown, false)
		}},
		{"unknown_occupied", func(c *largebody.DependencyCensus) {
			c.RegisterPort("vendor.future_port", largebody.DependencyClassUnknown, true)
		}},
		{"occupied_blocker", func(c *largebody.DependencyCensus) {
			c.RegisterPort("custom.call_callbacks", largebody.DependencyClassBlocker, true)
		}},
		{"wiresafe_local_turn", func(c *largebody.DependencyCensus) {
			c.RegisterPort("custom.local_turn_bridge", largebody.DependencyClassWireSafe, true)
		}},
		{"wiresafe_secret_guard", func(c *largebody.DependencyCensus) {
			c.RegisterPort("custom.secret_guard_proxy", largebody.DependencyClassWireSafe, true)
		}},
		{"wiresafe_terminal_decision", func(c *largebody.DependencyCensus) {
			c.RegisterPort("custom.terminal_decision_proxy", largebody.DependencyClassWireSafe, true)
		}},
		{"wiresafe_terminal_decision_dotted", func(c *largebody.DependencyCensus) {
			c.RegisterPort("custom.terminal.decision_proxy", largebody.DependencyClassWireSafe, true)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			genID := "gen-eligibility-consolidation-custom"
			summary := validWireSafeSummary(t, genID)
			census := largebody.NewStandardDependencyCensus(genID)
			tc.register(&census)

			decision, reason := largebody.NewAuthorityAssessmentGate(summary, census, genID).Evaluate()
			if decision != largebody.AssessmentDecisionDecline {
				t.Fatalf("%s must decline, got %v", tc.name, decision)
			}
			if reason != largebody.DeclineReasonAuthorityBlocker {
				t.Fatalf("%s must decline with AuthorityBlocker, got %v", tc.name, reason)
			}
		})
	}
}

// TestEligibilityConsolidation_SealPinSummaryPrecedenceUnchanged pins the
// gate order around the consolidation: unsealed beats pin, empty/mismatched
// generation beats summary blockers, and summary blockers beat current
// facts. Reasons are hardcoded.
func TestEligibilityConsolidation_SealPinSummaryPrecedenceUnchanged(t *testing.T) {
	t.Parallel()
	genID := "gen-eligibility-consolidation-order"
	census := largebody.NewStandardDependencyCensus(genID)

	t.Run("unsealed_beats_pin", func(t *testing.T) {
		t.Parallel()
		var unsealed largebody.WireEligibilitySummary
		decision, reason := largebody.NewAuthorityAssessmentGate(unsealed, census, "other-gen").Evaluate()
		if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
			t.Fatalf("unsealed summary must decline with AuthorityBlocker, got %v (%v)", decision, reason)
		}
	})

	t.Run("empty_generation_mismatch", func(t *testing.T) {
		t.Parallel()
		summary := validWireSafeSummary(t, genID)
		decision, reason := largebody.NewAuthorityAssessmentGate(summary, census, "").Evaluate()
		if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonGenerationMismatch {
			t.Fatalf("empty generation must decline with GenerationMismatch, got %v (%v)", decision, reason)
		}
	})

	t.Run("mismatched_generation", func(t *testing.T) {
		t.Parallel()
		summary := validWireSafeSummary(t, genID)
		decision, reason := largebody.NewAuthorityAssessmentGate(summary, census, "gen-other").Evaluate()
		if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonGenerationMismatch {
			t.Fatalf("mismatched generation must decline with GenerationMismatch, got %v (%v)", decision, reason)
		}
	})

	t.Run("summary_blockers_beat_clean_census", func(t *testing.T) {
		t.Parallel()
		blocked := largebody.NewStandardDependencyCensus(genID)
		blocked.Hooks.ToolOccupied = true
		summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
			GenerationID:              genID,
			Planes:                    blocked.Planes,
			Hooks:                     blocked.Hooks,
			Ports:                     blocked.Ports,
			TwoPhaseExecutorAvailable: blocked.TwoPhaseExecutorAvailable,
		}, 4096)
		if err != nil {
			t.Fatalf("CompileWireEligibilitySummary: %v", err)
		}
		decision, reason := largebody.NewAuthorityAssessmentGate(summary, census, genID).Evaluate()
		if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
			t.Fatalf("summary blockers must decline with AuthorityBlocker, got %v (%v)", decision, reason)
		}
	})
}
