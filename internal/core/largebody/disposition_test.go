package largebody_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

var (
	sinkDisposition largebody.StaticWireDisposition
	sinkReason      largebody.StaticWireReason
)

func validSealedSummary(t *testing.T, genID string) largebody.WireEligibilitySummary {
	t.Helper()
	planes := make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount)
	for i := range largebody.WireEligibilityPlaneCount {
		id, ok := largebody.WireEligibilityPlaneID(i)
		if !ok {
			t.Fatalf("missing plane id for index %d", i)
		}
		planes[i] = largebody.PlaneEligibilityInput{
			ID:       id,
			Access:   largebody.PlaneAccessResponseOnly,
			Occupied: false,
		}
	}
	s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    planes,
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	if s.HasStaticBlocker() {
		t.Fatal("validSealedSummary must not have static blocker")
	}
	return s
}

func summaryWithPlaneBlocker(t *testing.T, genID string) largebody.WireEligibilitySummary {
	t.Helper()
	planes := make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount)
	for i := range largebody.WireEligibilityPlaneCount {
		id, _ := largebody.WireEligibilityPlaneID(i)
		planes[i] = largebody.PlaneEligibilityInput{
			ID:       id,
			Access:   largebody.PlaneAccessResponseOnly,
			Occupied: false,
		}
	}
	// Mark submit_hooks as CanonicalRequired and Occupied
	planes[0].Access = largebody.PlaneAccessCanonicalRequired
	planes[0].Occupied = true

	s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    planes,
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	return s
}

func summaryWithHookBlocker(t *testing.T, genID string) largebody.WireEligibilitySummary {
	t.Helper()
	planes := make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount)
	for i := range largebody.WireEligibilityPlaneCount {
		id, _ := largebody.WireEligibilityPlaneID(i)
		planes[i] = largebody.PlaneEligibilityInput{
			ID:       id,
			Access:   largebody.PlaneAccessResponseOnly,
			Occupied: false,
		}
	}
	s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID: genID,
		Planes:       planes,
		Hooks: largebody.HookEligibilityInput{
			SubmitOccupied: true,
		},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	return s
}

func summaryWithPortBlocker(t *testing.T, genID string) largebody.WireEligibilitySummary {
	t.Helper()
	planes := make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount)
	for i := range largebody.WireEligibilityPlaneCount {
		id, _ := largebody.WireEligibilityPlaneID(i)
		planes[i] = largebody.PlaneEligibilityInput{
			ID:       id,
			Access:   largebody.PlaneAccessResponseOnly,
			Occupied: false,
		}
	}
	s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    planes,
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{BackendsEmpty: true},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	return s
}

func TestStaticDisposition_DefinitelyCanonical_Cases(t *testing.T) {
	t.Parallel()
	const genID = "gen-test-3.6"
	cleanSummary := validSealedSummary(t, genID)

	tests := []struct {
		name       string
		summary    largebody.WireEligibilitySummary
		input      largebody.StaticDispositionInput
		wantDisp   largebody.StaticWireDisposition
		wantReason largebody.StaticWireReason
	}{
		{
			name:    "feature disabled",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled: false,
				ThresholdBytes: 1 << 20,
				ContentLength:  2 << 20,
			},
			wantDisp:   largebody.DefinitelyCanonical,
			wantReason: largebody.StaticWireReasonFeatureDisabled,
		},
		{
			name:    "static blocker - plane blocker present",
			summary: summaryWithPlaneBlocker(t, genID),
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 1 << 20,
				ContentLength:  2 << 20,
			},
			wantDisp:   largebody.DefinitelyCanonical,
			wantReason: largebody.StaticWireReasonStaticBlocker,
		},
		{
			name:    "static blocker - hook blocker present",
			summary: summaryWithHookBlocker(t, genID),
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 1 << 20,
				ContentLength:  2 << 20,
			},
			wantDisp:   largebody.DefinitelyCanonical,
			wantReason: largebody.StaticWireReasonStaticBlocker,
		},
		{
			name:    "static blocker - port blocker present",
			summary: summaryWithPortBlocker(t, genID),
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 1 << 20,
				ContentLength:  2 << 20,
			},
			wantDisp:   largebody.DefinitelyCanonical,
			wantReason: largebody.StaticWireReasonStaticBlocker,
		},
		{
			name:    "static blocker - unsealed summary",
			summary: largebody.WireEligibilitySummary{},
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 1 << 20,
				ContentLength:  2 << 20,
			},
			wantDisp:   largebody.DefinitelyCanonical,
			wantReason: largebody.StaticWireReasonStaticBlocker,
		},
		{
			name:    "static blocker - generation mismatch",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   "different-generation-id",
				ThresholdBytes: 1 << 20,
				ContentLength:  2 << 20,
			},
			wantDisp:   largebody.DefinitelyCanonical,
			wantReason: largebody.StaticWireReasonStaticBlocker,
		},
		{
			name:    "below threshold - known positive content length",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 1 << 20, // 1 MiB
				ContentLength:  512,     // 512 bytes < 1 MiB
			},
			wantDisp:   largebody.DefinitelyCanonical,
			wantReason: largebody.StaticWireReasonBelowThreshold,
		},
		{
			name:    "below threshold - known zero content length",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 1 << 20,
				HasKnownLength: true,
				ContentLength:  0,
			},
			wantDisp:   largebody.DefinitelyCanonical,
			wantReason: largebody.StaticWireReasonBelowThreshold,
		},
		{
			name:    "below threshold - default threshold used when unset",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 0,   // should default to 1 MiB
				ContentLength:  512, // 512 bytes < 1 MiB
			},
			wantDisp:   largebody.DefinitelyCanonical,
			wantReason: largebody.StaticWireReasonBelowThreshold,
		},
		{
			name:    "gzip compressed",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 1 << 20,
				ContentLength:  5 << 20, // above threshold but gzip
				GzipCompressed: true,
			},
			wantDisp:   largebody.DefinitelyCanonical,
			wantReason: largebody.StaticWireReasonGzipCompressed,
		},
		{
			name:    "gzip compressed with unknown length",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 1 << 20,
				ContentLength:  -1,
				GzipCompressed: true,
			},
			wantDisp:   largebody.DefinitelyCanonical,
			wantReason: largebody.StaticWireReasonGzipCompressed,
		},
		{
			name:    "legacy resolver configured",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled:           true,
				ThresholdBytes:           1 << 20,
				ContentLength:            5 << 20, // above threshold but legacy resolver
				LegacyResolverConfigured: true,
			},
			wantDisp:   largebody.DefinitelyCanonical,
			wantReason: largebody.StaticWireReasonLegacyResolverConfigured,
		},
		{
			name:    "legacy resolver configured with unknown length",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled:           true,
				ThresholdBytes:           1 << 20,
				ContentLength:            -1,
				LegacyResolverConfigured: true,
			},
			wantDisp:   largebody.DefinitelyCanonical,
			wantReason: largebody.StaticWireReasonLegacyResolverConfigured,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			disp, reason := largebody.StaticDisposition(tc.summary, tc.input)
			if disp != tc.wantDisp {
				t.Errorf("StaticDisposition() disp = %v (%s), want %v (%s)",
					disp, disp.String(), tc.wantDisp, tc.wantDisp.String())
			}
			if reason != tc.wantReason {
				t.Errorf("StaticDisposition() reason = %v (%s), want %v (%s)",
					reason, reason.String(), tc.wantReason, tc.wantReason.String())
			}
			if !disp.IsDefinitelyCanonical() {
				t.Errorf("IsDefinitelyCanonical() = false, want true")
			}
			if disp.IsNeedsRequestAssessment() {
				t.Errorf("IsNeedsRequestAssessment() = true, want false")
			}
			// Receiver method parity
			rDisp, rReason := tc.summary.StaticDisposition(tc.input)
			if rDisp != disp || rReason != reason {
				t.Errorf("receiver method mismatch: got (%v, %v), want (%v, %v)", rDisp, rReason, disp, reason)
			}
			// ResolveStaticDisposition parity
			resDisp, resReason := largebody.ResolveStaticDisposition(tc.summary, tc.input)
			if resDisp != disp || resReason != reason {
				t.Errorf("ResolveStaticDisposition mismatch: got (%v, %v), want (%v, %v)", resDisp, resReason, disp, reason)
			}
		})
	}
}

func TestStaticDisposition_NeedsRequestAssessment_Cases(t *testing.T) {
	t.Parallel()
	const genID = "gen-test-3.6"
	cleanSummary := validSealedSummary(t, genID)

	tests := []struct {
		name  string
		input largebody.StaticDispositionInput
	}{
		{
			name: "known length above threshold",
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   genID,
				ThresholdBytes: 1 << 20, // 1 MiB
				ContentLength:  2 << 20, // 2 MiB >= 1 MiB
			},
		},
		{
			name: "known length exact threshold",
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   genID,
				ThresholdBytes: 1 << 20,
				ContentLength:  1 << 20, // exactly threshold
			},
		},
		{
			name: "unknown / chunked length (-1)",
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   genID,
				ThresholdBytes: 1 << 20,
				ContentLength:  -1,
				HasKnownLength: false,
			},
		},
		{
			name: "unknown length (unspecified zero length without HasKnownLength)",
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   genID,
				ThresholdBytes: 1 << 20,
				ContentLength:  0,
				HasKnownLength: false,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			disp, reason := largebody.StaticDisposition(cleanSummary, tc.input)
			if disp != largebody.NeedsRequestAssessment {
				t.Errorf("StaticDisposition() disp = %v (%s), want NeedsRequestAssessment",
					disp, disp.String())
			}
			if reason != largebody.StaticWireReasonNone {
				t.Errorf("StaticDisposition() reason = %v (%s), want StaticWireReasonNone",
					reason, reason.String())
			}
			if disp.IsDefinitelyCanonical() {
				t.Errorf("IsDefinitelyCanonical() = true, want false")
			}
			if !disp.IsNeedsRequestAssessment() {
				t.Errorf("IsNeedsRequestAssessment() = false, want true")
			}
		})
	}
}

func TestStaticDisposition_NeverSaysWireEligible(t *testing.T) {
	t.Parallel()
	// Exhaustively verify the StaticWireDisposition type only has
	// Unknown, DefinitelyCanonical, NeedsRequestAssessment values.
	// Static disposition must NEVER authorize wire execution directly (Req 5.9, Design sec 4).
	dispositions := []largebody.StaticWireDisposition{
		largebody.StaticWireUnknown,
		largebody.StaticWireDefinitelyCanonical,
		largebody.StaticWireNeedsRequestAssessment,
	}

	for _, d := range dispositions {
		name := d.String()
		if name == "wire_eligible" || name == "eligible" || name == "wire" {
			t.Fatalf("StaticWireDisposition must NEVER represent wire eligible, found %q", name)
		}
	}

	const genID = "gen-test-3.6"
	cleanSummary := validSealedSummary(t, genID)
	// Even a 100 MiB payload on a perfectly clean generation only receives NeedsRequestAssessment
	disp, reason := largebody.StaticDisposition(cleanSummary, largebody.StaticDispositionInput{
		FeatureEnabled: true,
		ThresholdBytes: 1 << 20,
		ContentLength:  100 << 20,
	})
	if disp != largebody.NeedsRequestAssessment {
		t.Fatalf("expected NeedsRequestAssessment, got %v", disp)
	}
	if reason != largebody.StaticWireReasonNone {
		t.Fatalf("expected StaticWireReasonNone, got %v", reason)
	}
}

func TestStaticDisposition_StringRepresentationsBounded(t *testing.T) {
	t.Parallel()
	dispCases := []struct {
		disp largebody.StaticWireDisposition
		want string
	}{
		{largebody.StaticWireUnknown, "unknown"},
		{largebody.StaticWireDefinitelyCanonical, "definitely_canonical"},
		{largebody.StaticWireNeedsRequestAssessment, "needs_request_assessment"},
		{largebody.StaticWireDisposition(99), "unknown"},
	}
	for _, tc := range dispCases {
		if got := tc.disp.String(); got != tc.want {
			t.Errorf("disp(%d).String() = %q, want %q", uint8(tc.disp), got, tc.want)
		}
	}

	reasonCases := []struct {
		reason largebody.StaticWireReason
		want   string
	}{
		{largebody.StaticWireReasonNone, "none"},
		{largebody.StaticWireReasonFeatureDisabled, "feature_disabled"},
		{largebody.StaticWireReasonStaticBlocker, "static_blocker"},
		{largebody.StaticWireReasonBelowThreshold, "below_threshold"},
		{largebody.StaticWireReasonGzipCompressed, "gzip_compressed"},
		{largebody.StaticWireReasonLegacyResolverConfigured, "legacy_resolver_configured"},
		{largebody.StaticWireReason(999), "unknown"},
	}
	for _, tc := range reasonCases {
		if got := tc.reason.String(); got != tc.want {
			t.Errorf("reason(%d).String() = %q, want %q", uint16(tc.reason), got, tc.want)
		}
	}
}

//nolint:paralleltest // AllocsPerRun forbids parallel tests.
func TestStaticDisposition_ZeroAllocations(t *testing.T) {
	const genID = "gen-test-3.6"
	cleanSummary := validSealedSummary(t, genID)
	blockedSummary := summaryWithPlaneBlocker(t, genID)

	inputs := []struct {
		name    string
		summary largebody.WireEligibilitySummary
		input   largebody.StaticDispositionInput
	}{
		{
			name:    "definitely canonical - disabled",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled: false,
				ThresholdBytes: 1 << 20,
				ContentLength:  2 << 20,
			},
		},
		{
			name:    "definitely canonical - static blocker",
			summary: blockedSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 1 << 20,
				ContentLength:  2 << 20,
			},
		},
		{
			name:    "definitely canonical - below threshold",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 1 << 20,
				ContentLength:  512,
			},
		},
		{
			name:    "definitely canonical - gzip",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 1 << 20,
				ContentLength:  2 << 20,
				GzipCompressed: true,
			},
		},
		{
			name:    "definitely canonical - legacy resolver",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled:           true,
				ThresholdBytes:           1 << 20,
				ContentLength:            2 << 20,
				LegacyResolverConfigured: true,
			},
		},
		{
			name:    "needs request assessment - known length",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 1 << 20,
				ContentLength:  2 << 20,
			},
		},
		{
			name:    "needs request assessment - unknown length",
			summary: cleanSummary,
			input: largebody.StaticDispositionInput{
				FeatureEnabled: true,
				ThresholdBytes: 1 << 20,
				ContentLength:  -1,
			},
		},
	}

	for _, tc := range inputs {
		allocs := testing.AllocsPerRun(1000, func() {
			sinkDisposition, sinkReason = largebody.StaticDisposition(tc.summary, tc.input)
		})
		if allocs != 0 {
			t.Fatalf("%s: allocs = %v, want 0 (allocation-free hot path required, Requirement 5.10, 21.12)",
				tc.name, allocs)
		}
	}
}
