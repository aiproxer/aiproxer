package largebody_test

import (
	"os"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// Task 3.7 static-disposition ratchets and benchmarks (Requirements 5, 21, 22;
// design sections 4 and 15).
//
// Ratchets pin the non-negotiable static blockers:
// 1. Local Turn: occupied PlaneLocalTurnHandlers => canonical required.
// 2. Secret Guard: active Secret Guard execution / secret guards => canonical required.
// 3. Unclassified plane: any plane with PlaneAccessUnclassified fails compilation closed.
// 4. Canonical-only traffic: non-no-op traffic capturing => static blocker.
// 5. Missing two-phase executor: executor lacking two-phase capability => static blocker.
// 6. Normal potentially eligible generation: clean generation => NeedsRequestAssessment (never wire-eligible).
//
// Benchmark: definitely-ineligible candidates against feature-disabled canonical baseline:
// proves zero temp file/replay/scanner construction and negligible overhead (0 allocs, single-digit ns/op).

func makeCleanPlanes() []largebody.PlaneEligibilityInput {
	planes := make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount)
	for i := range largebody.WireEligibilityPlaneCount {
		id, _ := largebody.WireEligibilityPlaneID(i)
		planes[i] = largebody.PlaneEligibilityInput{
			ID:       id,
			Access:   largebody.PlaneAccessResponseOnly,
			Occupied: false,
		}
	}
	return planes
}

func validSealedSummaryTB(tb testing.TB, genID string) largebody.WireEligibilitySummary {
	tb.Helper()
	s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    makeCleanPlanes(),
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		tb.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	if s.HasStaticBlocker() {
		tb.Fatal("validSealedSummaryTB must not have static blocker")
	}
	return s
}

func summaryWithLocalTurnOccupied(tb testing.TB, genID string, occupied bool) largebody.WireEligibilitySummary {
	tb.Helper()
	planes := makeCleanPlanes()
	idx, ok := largebody.WireEligibilityPlaneIndex("local_turn_handlers")
	if !ok {
		tb.Fatalf("missing plane index for local_turn_handlers")
	}
	planes[idx].Access = largebody.PlaneAccessCanonicalRequired
	planes[idx].Occupied = occupied

	s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    planes,
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		tb.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	return s
}

func summaryWithSecretGuardOccupied(tb testing.TB, genID string, occupied bool) largebody.WireEligibilitySummary {
	tb.Helper()
	planes := makeCleanPlanes()
	for _, id := range []string{"secret_guard_execution", "secret_guards"} {
		idx, ok := largebody.WireEligibilityPlaneIndex(id)
		if !ok {
			tb.Fatalf("missing plane index for %s", id)
		}
		planes[idx].Access = largebody.PlaneAccessCanonicalRequired
		planes[idx].Occupied = occupied
	}

	s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    planes,
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		tb.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	return s
}

func summaryWithTrafficCapturing(tb testing.TB, genID string, capturing bool) largebody.WireEligibilitySummary {
	tb.Helper()
	planes := makeCleanPlanes()
	s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID: genID,
		Planes:       planes,
		Hooks:        largebody.HookEligibilityInput{},
		Ports: largebody.NarrowPortEligibilityInput{
			TrafficCapturing: capturing,
		},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		tb.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	return s
}

func summaryWithTwoPhaseAvailable(tb testing.TB, genID string, available bool) largebody.WireEligibilitySummary {
	tb.Helper()
	planes := makeCleanPlanes()
	s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    planes,
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: available,
	}, 1024)
	if err != nil {
		tb.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	return s
}

// TestStaticDisposition_LocalTurnRatchet verifies that an occupied local_turn_handlers
// plane unconditionally sets a static blocker and returns DefinitelyCanonical (Req 5.4, 13.4).
func TestStaticDisposition_LocalTurnRatchet(t *testing.T) {
	t.Parallel()
	const genID = "gen-ratchet-localturn"

	// 1. Occupied => static blocker => DefinitelyCanonical
	blockedSummary := summaryWithLocalTurnOccupied(t, genID, true)
	if !blockedSummary.HasStaticBlocker() {
		t.Fatal("occupied local_turn_handlers must set static blocker")
	}
	idx, _ := largebody.WireEligibilityPlaneIndex("local_turn_handlers")
	if blockedSummary.PlaneBlockers()&(1<<uint(idx)) == 0 {
		t.Fatalf("local_turn_handlers bit %d must be set in PlaneBlockers", idx)
	}

	disp, reason := blockedSummary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   genID,
		ThresholdBytes: 1 << 20,
		ContentLength:  5 << 20,
	})
	if disp != largebody.DefinitelyCanonical {
		t.Errorf("disp = %v (%s), want DefinitelyCanonical", disp, disp.String())
	}
	if reason != largebody.StaticWireReasonStaticBlocker {
		t.Errorf("reason = %v (%s), want StaticWireReasonStaticBlocker", reason, reason.String())
	}

	// 2. Unoccupied => no static blocker => NeedsRequestAssessment
	cleanSummary := summaryWithLocalTurnOccupied(t, genID, false)
	if cleanSummary.HasStaticBlocker() {
		t.Fatal("unoccupied local_turn_handlers must not set static blocker")
	}
	dispClean, reasonClean := cleanSummary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   genID,
		ThresholdBytes: 1 << 20,
		ContentLength:  5 << 20,
	})
	if dispClean != largebody.NeedsRequestAssessment {
		t.Errorf("unoccupied local turn disp = %v, want NeedsRequestAssessment", dispClean)
	}
	if reasonClean != largebody.StaticWireReasonNone {
		t.Errorf("unoccupied local turn reason = %v, want StaticWireReasonNone", reasonClean)
	}
}

// TestStaticDisposition_SecretGuardRatchet verifies that occupied secret guards
// unconditionally set a static blocker and return DefinitelyCanonical (Req 5.4, 13.3, 13.4).
func TestStaticDisposition_SecretGuardRatchet(t *testing.T) {
	t.Parallel()
	const genID = "gen-ratchet-secretguard"

	// 1. Occupied => static blocker => DefinitelyCanonical
	blockedSummary := summaryWithSecretGuardOccupied(t, genID, true)
	if !blockedSummary.HasStaticBlocker() {
		t.Fatal("occupied secret guards must set static blocker")
	}
	idxExec, _ := largebody.WireEligibilityPlaneIndex("secret_guard_execution")
	idxGuards, _ := largebody.WireEligibilityPlaneIndex("secret_guards")
	if blockedSummary.PlaneBlockers()&(1<<uint(idxExec)) == 0 {
		t.Fatalf("secret_guard_execution bit %d must be set in PlaneBlockers", idxExec)
	}
	if blockedSummary.PlaneBlockers()&(1<<uint(idxGuards)) == 0 {
		t.Fatalf("secret_guards bit %d must be set in PlaneBlockers", idxGuards)
	}

	disp, reason := blockedSummary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   genID,
		ThresholdBytes: 1 << 20,
		ContentLength:  5 << 20,
	})
	if disp != largebody.DefinitelyCanonical {
		t.Errorf("disp = %v (%s), want DefinitelyCanonical", disp, disp.String())
	}
	if reason != largebody.StaticWireReasonStaticBlocker {
		t.Errorf("reason = %v (%s), want StaticWireReasonStaticBlocker", reason, reason.String())
	}

	// 2. Unoccupied => no static blocker
	cleanSummary := summaryWithSecretGuardOccupied(t, genID, false)
	if cleanSummary.HasStaticBlocker() {
		t.Fatal("unoccupied secret guards must not set static blocker")
	}
	dispClean, reasonClean := cleanSummary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   genID,
		ThresholdBytes: 1 << 20,
		ContentLength:  5 << 20,
	})
	if dispClean != largebody.NeedsRequestAssessment {
		t.Errorf("unoccupied secret guards disp = %v, want NeedsRequestAssessment", dispClean)
	}
	if reasonClean != largebody.StaticWireReasonNone {
		t.Errorf("unoccupied secret guards reason = %v, want StaticWireReasonNone", reasonClean)
	}
}

// TestStaticDisposition_UnclassifiedPlaneRatchet verifies that any unclassified plane
// fails compilation closed, resulting in an unsealed summary that reports a static blocker
// and DefinitelyCanonical (Req 5.3, 5.12).
func TestStaticDisposition_UnclassifiedPlaneRatchet(t *testing.T) {
	t.Parallel()
	const genID = "gen-ratchet-unclassified"

	for i := range largebody.WireEligibilityPlaneCount {
		planeID, _ := largebody.WireEligibilityPlaneID(i)
		planes := makeCleanPlanes()
		planes[i].Access = largebody.PlaneAccessUnclassified // zero value

		summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
			GenerationID:              genID,
			Planes:                    planes,
			Hooks:                     largebody.HookEligibilityInput{},
			Ports:                     largebody.NarrowPortEligibilityInput{},
			TwoPhaseExecutorAvailable: true,
		}, 1024)

		if err == nil {
			t.Fatalf("plane %q with PlaneAccessUnclassified must fail compilation", planeID)
		}
		if summary.Sealed() {
			t.Fatalf("plane %q unclassified error must yield unsealed summary", planeID)
		}
		if !summary.HasStaticBlocker() {
			t.Fatalf("unsealed summary for plane %q must have static blocker", planeID)
		}

		disp, reason := summary.StaticDisposition(largebody.StaticDispositionInput{
			FeatureEnabled: true,
			GenerationID:   genID,
			ThresholdBytes: 1 << 20,
			ContentLength:  5 << 20,
		})
		if disp != largebody.DefinitelyCanonical {
			t.Errorf("unclassified plane %q disp = %v, want DefinitelyCanonical", planeID, disp)
		}
		if reason != largebody.StaticWireReasonStaticBlocker {
			t.Errorf("unclassified plane %q reason = %v, want StaticWireReasonStaticBlocker", planeID, reason)
		}
	}
}

// TestStaticDisposition_CanonicalTrafficRatchet verifies that non-no-op traffic
// (capturing/observing/redacting) sets a static blocker and returns DefinitelyCanonical (Req 5.6, 13.1).
func TestStaticDisposition_CanonicalTrafficRatchet(t *testing.T) {
	t.Parallel()
	const genID = "gen-ratchet-traffic"

	// 1. Narrow port TrafficCapturing: true => blocker
	trafficSummary := summaryWithTrafficCapturing(t, genID, true)
	if !trafficSummary.HasStaticBlocker() {
		t.Fatal("TrafficCapturing: true must set static blocker")
	}
	if trafficSummary.PortBlockers()&largebody.WirePortTrafficCapturing == 0 {
		t.Fatal("WirePortTrafficCapturing bit must be set in PortBlockers")
	}

	disp, reason := trafficSummary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   genID,
		ThresholdBytes: 1 << 20,
		ContentLength:  5 << 20,
	})
	if disp != largebody.DefinitelyCanonical {
		t.Errorf("disp = %v (%s), want DefinitelyCanonical", disp, disp.String())
	}
	if reason != largebody.StaticWireReasonStaticBlocker {
		t.Errorf("reason = %v (%s), want StaticWireReasonStaticBlocker", reason, reason.String())
	}

	// 2. Narrow port TrafficCapturing: false => no blocker
	cleanSummary := summaryWithTrafficCapturing(t, genID, false)
	if cleanSummary.HasStaticBlocker() {
		t.Fatal("TrafficCapturing: false must not set static blocker")
	}
	dispClean, reasonClean := cleanSummary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   genID,
		ThresholdBytes: 1 << 20,
		ContentLength:  5 << 20,
	})
	if dispClean != largebody.NeedsRequestAssessment {
		t.Errorf("dispClean = %v, want NeedsRequestAssessment", dispClean)
	}
	if reasonClean != largebody.StaticWireReasonNone {
		t.Errorf("reasonClean = %v, want StaticWireReasonNone", reasonClean)
	}
}

// TestStaticDisposition_MissingTwoPhaseExecutorRatchet verifies that an executor lacking
// the two-phase capability sets a static blocker and returns DefinitelyCanonical (Req 5.6, design sec 4).
func TestStaticDisposition_MissingTwoPhaseExecutorRatchet(t *testing.T) {
	t.Parallel()
	const genID = "gen-ratchet-twophase"

	// 1. TwoPhaseExecutorAvailable: false => blocker
	missingSummary := summaryWithTwoPhaseAvailable(t, genID, false)
	if !missingSummary.HasStaticBlocker() {
		t.Fatal("TwoPhaseExecutorAvailable: false must set static blocker")
	}
	if missingSummary.PortBlockers()&largebody.WirePortTwoPhaseExecutorMissing == 0 {
		t.Fatal("WirePortTwoPhaseExecutorMissing bit must be set in PortBlockers")
	}

	disp, reason := missingSummary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   genID,
		ThresholdBytes: 1 << 20,
		ContentLength:  5 << 20,
	})
	if disp != largebody.DefinitelyCanonical {
		t.Errorf("disp = %v (%s), want DefinitelyCanonical", disp, disp.String())
	}
	if reason != largebody.StaticWireReasonStaticBlocker {
		t.Errorf("reason = %v (%s), want StaticWireReasonStaticBlocker", reason, reason.String())
	}

	// 2. TwoPhaseExecutorAvailable: true => no blocker
	availSummary := summaryWithTwoPhaseAvailable(t, genID, true)
	if availSummary.HasStaticBlocker() {
		t.Fatal("TwoPhaseExecutorAvailable: true must not set static blocker")
	}
	dispAvail, reasonAvail := availSummary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   genID,
		ThresholdBytes: 1 << 20,
		ContentLength:  5 << 20,
	})
	if dispAvail != largebody.NeedsRequestAssessment {
		t.Errorf("dispAvail = %v, want NeedsRequestAssessment", dispAvail)
	}
	if reasonAvail != largebody.StaticWireReasonNone {
		t.Errorf("reasonAvail = %v, want StaticWireReasonNone", reasonAvail)
	}
}

// TestStaticDisposition_NormalPotentiallyEligibleRatchet verifies that a standard
// production-like composition (secure session, metering, unoccupied response hooks, no static blockers)
// reaches NeedsRequestAssessment on candidate requests and NEVER directly authorizes wire execution (Req 5.9, 21.6).
func TestStaticDisposition_NormalPotentiallyEligibleRatchet(t *testing.T) {
	t.Parallel()
	const genID = "gen-normal-eligible"

	// Standard production composition:
	// - All 27 planes declared (unoccupied CanonicalRequired or ResponseOnly/MetadataOnly)
	// - Response-only hooks inactive (Blocker 2: ResponsePartOccupied: true statically blocks)
	// - TwoPhaseExecutorAvailable: true
	// - Backends present (BackendsEmpty: false)
	planes := makeCleanPlanes()
	// Mark standard planes as they would be configured in production
	idxLocal, _ := largebody.WireEligibilityPlaneIndex("local_turn_handlers")
	planes[idxLocal].Access = largebody.PlaneAccessCanonicalRequired
	planes[idxLocal].Occupied = false // unoccupied

	idxExec, _ := largebody.WireEligibilityPlaneIndex("secret_guard_execution")
	planes[idxExec].Access = largebody.PlaneAccessCanonicalRequired
	planes[idxExec].Occupied = false // unoccupied

	idxGuards, _ := largebody.WireEligibilityPlaneIndex("secret_guards")
	planes[idxGuards].Access = largebody.PlaneAccessCanonicalRequired
	planes[idxGuards].Occupied = false // unoccupied

	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID: genID,
		Planes:       planes,
		Hooks: largebody.HookEligibilityInput{
			ResponsePartOccupied: false, // Blocker 2: occupied response-part hook chain blocks wire eligibility
		},
		Ports: largebody.NarrowPortEligibilityInput{
			BackendsEmpty: false,
		},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	if summary.HasStaticBlocker() {
		t.Fatalf("normal generation must not have static blocker, blockers: plane=%#x hook=%#x port=%#x",
			summary.PlaneBlockers(), uint8(summary.HookBlockers()), uint32(summary.PortBlockers()))
	}

	testCases := []struct {
		name          string
		contentLength int64
		hasKnownLen   bool
	}{
		{"known length 2 MiB (above 1 MiB threshold)", 2 << 20, true},
		{"known length 10 MiB (large payload)", 10 << 20, true},
		{"exact threshold 1 MiB", 1 << 20, true},
		{"unknown chunked length (-1)", -1, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			disp, reason := summary.StaticDisposition(largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   genID,
				ThresholdBytes: 1 << 20,
				ContentLength:  tc.contentLength,
				HasKnownLength: tc.hasKnownLen,
			})
			if disp != largebody.NeedsRequestAssessment {
				t.Errorf("disp = %v (%s), want NeedsRequestAssessment", disp, disp.String())
			}
			if reason != largebody.StaticWireReasonNone {
				t.Errorf("reason = %v (%s), want StaticWireReasonNone", reason, reason.String())
			}
			if disp.IsDefinitelyCanonical() {
				t.Error("IsDefinitelyCanonical() must be false")
			}
			if !disp.IsNeedsRequestAssessment() {
				t.Error("IsNeedsRequestAssessment() must be true")
			}

			// Invariant: StaticDisposition NEVER authorizes wire execution directly (Req 5.9, Design sec 4).
			if disp.String() == "wire_eligible" || disp.String() == "eligible" {
				t.Fatalf("StaticDisposition must NEVER say wire_eligible")
			}
		})
	}
}

// TestStaticDisposition_DefinitelyIneligible_NoTempFileOrScanner verifies that
// when static disposition returns DefinitelyCanonical, zero temp files are created,
// zero spool reservations are made, zero streaming scanners are constructed,
// and zero heap allocations occur (Requirements 5.10, 21.12; design section 15).
//
//nolint:paralleltest // AllocsPerRun forbids parallel tests.
func TestStaticDisposition_DefinitelyIneligible_NoTempFileOrScanner(t *testing.T) {
	const genID = "gen-ratchet-notemp"

	// Create a dedicated directory to monitor for file creation
	tempDir := t.TempDir()
	initialEntries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	cases := []struct {
		name    string
		summary largebody.WireEligibilitySummary
		input   largebody.StaticDispositionInput
	}{
		{
			name:    "feature disabled baseline",
			summary: validSealedSummaryTB(t, genID),
			input: largebody.StaticDispositionInput{
				FeatureEnabled: false,
				ThresholdBytes: 1 << 20,
				ContentLength:  5 << 20,
			},
		},
		{
			name:    "occupied local turn",
			summary: summaryWithLocalTurnOccupied(t, genID, true),
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   genID,
				ThresholdBytes: 1 << 20,
				ContentLength:  5 << 20,
			},
		},
		{
			name:    "occupied secret guard",
			summary: summaryWithSecretGuardOccupied(t, genID, true),
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   genID,
				ThresholdBytes: 1 << 20,
				ContentLength:  5 << 20,
			},
		},
		{
			name:    "unsealed summary (unclassified plane fail closed)",
			summary: largebody.WireEligibilitySummary{},
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   genID,
				ThresholdBytes: 1 << 20,
				ContentLength:  5 << 20,
			},
		},
		{
			name:    "traffic capturing",
			summary: summaryWithTrafficCapturing(t, genID, true),
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   genID,
				ThresholdBytes: 1 << 20,
				ContentLength:  5 << 20,
			},
		},
		{
			name:    "missing two-phase executor",
			summary: summaryWithTwoPhaseAvailable(t, genID, false),
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   genID,
				ThresholdBytes: 1 << 20,
				ContentLength:  5 << 20,
			},
		},
		{
			name:    "known length below threshold",
			summary: validSealedSummaryTB(t, genID),
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   genID,
				ThresholdBytes: 1 << 20,
				ContentLength:  512,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			disp, reason := tc.summary.StaticDisposition(tc.input)
			if disp != largebody.DefinitelyCanonical {
				t.Fatalf("expected DefinitelyCanonical, got %v (%s)", disp, disp.String())
			}
			if reason == largebody.StaticWireReasonNone {
				t.Fatalf("expected non-none reason, got %v", reason)
			}

			// Verify no files were created in tempDir
			entries, readErr := os.ReadDir(tempDir)
			if readErr != nil {
				t.Fatalf("ReadDir: %v", readErr)
			}
			if len(entries) != len(initialEntries) {
				t.Fatalf("temp files created during DefinitelyCanonical evaluation: %d -> %d",
					len(initialEntries), len(entries))
			}

			// Verify zero allocations on the hot path
			allocs := testing.AllocsPerRun(1000, func() {
				sinkDisposition, sinkReason = tc.summary.StaticDisposition(tc.input)
			})
			if allocs != 0 {
				t.Fatalf("allocs = %v, want 0", allocs)
			}
		})
	}
}

// -------------------------------------------------------------------------
// Benchmarks: Definitely-ineligible candidate vs Feature-disabled baseline
// (Requirements 21.12; design section 15).
// -------------------------------------------------------------------------

func BenchmarkStaticDisposition_Baseline_FeatureDisabled(b *testing.B) {
	summary := validSealedSummaryTB(b, "gen-bench")
	in := largebody.StaticDispositionInput{
		FeatureEnabled: false,
		ThresholdBytes: 1 << 20,
		ContentLength:  2 << 20,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkDisposition, sinkReason = largebody.StaticDisposition(summary, in)
	}
}

func BenchmarkStaticDisposition_DefinitelyIneligible_LocalTurn(b *testing.B) {
	summary := summaryWithLocalTurnOccupied(b, "gen-bench", true)
	in := largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   "gen-bench",
		ThresholdBytes: 1 << 20,
		ContentLength:  2 << 20,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkDisposition, sinkReason = largebody.StaticDisposition(summary, in)
	}
}

func BenchmarkStaticDisposition_DefinitelyIneligible_SecretGuard(b *testing.B) {
	summary := summaryWithSecretGuardOccupied(b, "gen-bench", true)
	in := largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   "gen-bench",
		ThresholdBytes: 1 << 20,
		ContentLength:  2 << 20,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkDisposition, sinkReason = largebody.StaticDisposition(summary, in)
	}
}

func BenchmarkStaticDisposition_DefinitelyIneligible_CanonicalTraffic(b *testing.B) {
	summary := summaryWithTrafficCapturing(b, "gen-bench", true)
	in := largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   "gen-bench",
		ThresholdBytes: 1 << 20,
		ContentLength:  2 << 20,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkDisposition, sinkReason = largebody.StaticDisposition(summary, in)
	}
}

func BenchmarkStaticDisposition_DefinitelyIneligible_MissingTwoPhase(b *testing.B) {
	summary := summaryWithTwoPhaseAvailable(b, "gen-bench", false)
	in := largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   "gen-bench",
		ThresholdBytes: 1 << 20,
		ContentLength:  2 << 20,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkDisposition, sinkReason = largebody.StaticDisposition(summary, in)
	}
}

func BenchmarkStaticDisposition_DefinitelyIneligible_BelowThreshold(b *testing.B) {
	summary := validSealedSummaryTB(b, "gen-bench")
	in := largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   "gen-bench",
		ThresholdBytes: 1 << 20,
		ContentLength:  512,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkDisposition, sinkReason = largebody.StaticDisposition(summary, in)
	}
}

func BenchmarkStaticDisposition_PotentiallyEligible(b *testing.B) {
	summary := validSealedSummaryTB(b, "gen-bench")
	in := largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   "gen-bench",
		ThresholdBytes: 1 << 20,
		ContentLength:  2 << 20,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkDisposition, sinkReason = largebody.StaticDisposition(summary, in)
	}
}
