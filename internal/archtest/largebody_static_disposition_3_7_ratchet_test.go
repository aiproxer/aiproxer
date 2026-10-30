package archtest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// Task 3.7 architecture ratchets for static pre-capture disposition (Requirements
// 5, 21, 22; design sections 4 and 15).
//
// These ratchets prevent architectural drift:
// 1. All 27 production planes must be explicitly classified (no unclassified planes allowed).
// 2. Occupied Local Turn, occupied Secret Guard, canonical-only traffic, and missing two-phase executor
//    unconditionally yield DefinitelyCanonical with StaticWireReasonStaticBlocker.
// 3. Static disposition never says wire-eligible.
// 4. Hot path for definitely-ineligible cases is allocation-free.

func makeCleanArchPlanes() []largebody.PlaneEligibilityInput {
	planes := make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount)
	for i := range largebody.WireEligibilityPlaneCount {
		id, ok := largebody.WireEligibilityPlaneID(i)
		if !ok {
			panic("missing plane id")
		}
		planes[i] = largebody.PlaneEligibilityInput{
			ID:       id,
			Access:   largebody.PlaneAccessResponseOnly,
			Occupied: false,
		}
	}
	return planes
}

// TestArch_StaticDisposition_27PlanesExplicitlyClassified verifies that all 27
// standard production planes have an explicit, non-unclassified request access class
// and that unclassified planes fail closed (Requirements 5.1, 5.2, 5.3, 5.12, 22.4).
func TestArch_StaticDisposition_27PlanesExplicitlyClassified(t *testing.T) {
	t.Parallel()

	require.Len(t, feature.StandardPlanes, largebody.WireEligibilityPlaneCount,
		"feature.StandardPlanes count must match largebody.WireEligibilityPlaneCount (27 planes)")

	seen := make(map[string]bool)
	for _, decl := range feature.StandardPlanes {
		id := decl.PlaneID()
		assert.NotEmpty(t, id, "plane ID must not be empty")
		assert.False(t, seen[id], "duplicate plane ID %q", id)
		seen[id] = true

		// Must map onto largebody plane census index
		idx, ok := largebody.WireEligibilityPlaneIndex(id)
		assert.True(t, ok, "plane %q must be recognized by largebody.WireEligibilityPlaneIndex", id)
		censusID, ok := largebody.WireEligibilityPlaneID(idx)
		assert.True(t, ok, "largebody.WireEligibilityPlaneID(%d) must exist", idx)
		assert.Equal(t, id, censusID, "census ID must match plane ID")
	}

	// Concrete non-negotiable planes must stay canonical-required
	assert.Equal(t, feature.RequestBodyCanonicalRequired, feature.PlaneLocalTurnHandlers.RequestAccess,
		"PlaneLocalTurnHandlers must stay canonical-required")
	assert.Equal(t, feature.RequestBodyCanonicalRequired, feature.PlaneSecretGuards.RequestAccess,
		"PlaneSecretGuards must stay canonical-required")
	assert.Equal(t, feature.RequestBodyCanonicalRequired, feature.PlaneSecretGuardExecution.RequestAccess,
		"PlaneSecretGuardExecution must stay canonical-required")
	assert.Equal(t, feature.RequestBodyCanonicalRequired, feature.PlaneTerminalDecisionProvider.RequestAccess,
		"PlaneTerminalDecisionProvider must stay canonical-required")

	// Unclassified plane in CompileWireEligibilitySummary must fail closed
	planes := makeCleanArchPlanes()
	planes[0].Access = largebody.PlaneAccessUnclassified
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              "gen-arch-unclassified",
		Planes:                    planes,
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}, 1024)

	assert.Error(t, err, "unclassified plane must fail compilation closed")
	assert.False(t, summary.Sealed(), "unclassified plane must yield unsealed summary")
	assert.True(t, summary.HasStaticBlocker(), "unsealed summary must report static blocker")

	disp, reason := summary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   "gen-arch-unclassified",
		ThresholdBytes: 1 << 20,
		ContentLength:  5 << 20,
	})
	assert.Equal(t, largebody.DefinitelyCanonical, disp)
	assert.Equal(t, largebody.StaticWireReasonStaticBlocker, reason)
}

// TestArch_StaticDisposition_NonNegotiableBlockers pins that Local Turn, Secret Guard,
// Canonical Traffic, and Missing Two-Phase Executor all unconditionally produce DefinitelyCanonical
// with StaticWireReasonStaticBlocker (Requirements 5.4, 5.6, 13.1, 13.3, 13.4; design section 4).
func TestArch_StaticDisposition_NonNegotiableBlockers(t *testing.T) {
	t.Parallel()
	const genID = "gen-arch-blockers"

	t.Run("occupied Local Turn", func(t *testing.T) {
		t.Parallel()
		planes := makeCleanArchPlanes()
		idx, ok := largebody.WireEligibilityPlaneIndex("local_turn_handlers")
		require.True(t, ok)
		planes[idx].Access = largebody.PlaneAccessCanonicalRequired
		planes[idx].Occupied = true

		s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
			GenerationID:              genID,
			Planes:                    planes,
			TwoPhaseExecutorAvailable: true,
		}, 1024)
		require.NoError(t, err)
		assert.True(t, s.HasStaticBlocker())

		disp, reason := s.StaticDisposition(largebody.StaticDispositionInput{
			FeatureEnabled: true,
			GenerationID:   genID,
			ThresholdBytes: 1 << 20,
			ContentLength:  2 << 20,
		})
		assert.Equal(t, largebody.DefinitelyCanonical, disp)
		assert.Equal(t, largebody.StaticWireReasonStaticBlocker, reason)
	})

	t.Run("occupied Secret Guard", func(t *testing.T) {
		t.Parallel()
		planes := makeCleanArchPlanes()
		for _, id := range []string{"secret_guard_execution", "secret_guards"} {
			idx, ok := largebody.WireEligibilityPlaneIndex(id)
			require.True(t, ok)
			planes[idx].Access = largebody.PlaneAccessCanonicalRequired
			planes[idx].Occupied = true
		}

		s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
			GenerationID:              genID,
			Planes:                    planes,
			TwoPhaseExecutorAvailable: true,
		}, 1024)
		require.NoError(t, err)
		assert.True(t, s.HasStaticBlocker())

		disp, reason := s.StaticDisposition(largebody.StaticDispositionInput{
			FeatureEnabled: true,
			GenerationID:   genID,
			ThresholdBytes: 1 << 20,
			ContentLength:  2 << 20,
		})
		assert.Equal(t, largebody.DefinitelyCanonical, disp)
		assert.Equal(t, largebody.StaticWireReasonStaticBlocker, reason)
	})

	t.Run("canonical-only traffic", func(t *testing.T) {
		t.Parallel()
		planes := makeCleanArchPlanes()
		s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
			GenerationID: genID,
			Planes:       planes,
			Ports: largebody.NarrowPortEligibilityInput{
				TrafficCapturing: true,
			},
			TwoPhaseExecutorAvailable: true,
		}, 1024)
		require.NoError(t, err)
		assert.True(t, s.HasStaticBlocker())
		assert.NotZero(t, s.PortBlockers()&largebody.WirePortTrafficCapturing)

		disp, reason := s.StaticDisposition(largebody.StaticDispositionInput{
			FeatureEnabled: true,
			GenerationID:   genID,
			ThresholdBytes: 1 << 20,
			ContentLength:  2 << 20,
		})
		assert.Equal(t, largebody.DefinitelyCanonical, disp)
		assert.Equal(t, largebody.StaticWireReasonStaticBlocker, reason)
	})

	t.Run("missing two-phase executor", func(t *testing.T) {
		t.Parallel()
		planes := makeCleanArchPlanes()
		s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
			GenerationID:              genID,
			Planes:                    planes,
			TwoPhaseExecutorAvailable: false,
		}, 1024)
		require.NoError(t, err)
		assert.True(t, s.HasStaticBlocker())
		assert.NotZero(t, s.PortBlockers()&largebody.WirePortTwoPhaseExecutorMissing)

		disp, reason := s.StaticDisposition(largebody.StaticDispositionInput{
			FeatureEnabled: true,
			GenerationID:   genID,
			ThresholdBytes: 1 << 20,
			ContentLength:  2 << 20,
		})
		assert.Equal(t, largebody.DefinitelyCanonical, disp)
		assert.Equal(t, largebody.StaticWireReasonStaticBlocker, reason)
	})
}

// TestArch_StaticDisposition_PotentiallyEligibleNeverAuthorizesWire verifies that
// a normal, potentially eligible generation yields NeedsRequestAssessment and NEVER
// authorizes wire execution directly (Requirements 5.9, 21.6; design section 4).
func TestArch_StaticDisposition_PotentiallyEligibleNeverAuthorizesWire(t *testing.T) {
	t.Parallel()
	const genID = "gen-arch-eligible"

	planes := makeCleanArchPlanes()
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
	require.NoError(t, err)
	require.False(t, summary.HasStaticBlocker())

	disp, reason := summary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   genID,
		ThresholdBytes: 1 << 20,
		ContentLength:  2 << 20,
		HasKnownLength: true,
	})

	assert.Equal(t, largebody.NeedsRequestAssessment, disp)
	assert.Equal(t, largebody.StaticWireReasonNone, reason)
	assert.False(t, disp.IsDefinitelyCanonical())
	assert.True(t, disp.IsNeedsRequestAssessment())

	// StaticWireDisposition must have no variant meaning wire-eligible
	assert.NotEqual(t, "wire_eligible", disp.String())
	assert.NotEqual(t, "eligible", disp.String())
}

//nolint:paralleltest // AllocsPerRun forbids parallel tests.
func TestArch_StaticDisposition_HotPathZeroAllocations(t *testing.T) {
	const genID = "gen-arch-alloc"
	cleanPlanes := makeCleanArchPlanes()
	cleanSummary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    cleanPlanes,
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}

	blockedPlanes := makeCleanArchPlanes()
	idxLocal, _ := largebody.WireEligibilityPlaneIndex("local_turn_handlers")
	blockedPlanes[idxLocal].Access = largebody.PlaneAccessCanonicalRequired
	blockedPlanes[idxLocal].Occupied = true
	blockedSummary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    blockedPlanes,
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}

	var sinkD largebody.StaticWireDisposition
	var sinkR largebody.StaticWireReason

	// 1. Feature disabled baseline
	allocsDisabled := testing.AllocsPerRun(1000, func() {
		sinkD, sinkR = cleanSummary.StaticDisposition(largebody.StaticDispositionInput{
			FeatureEnabled: false,
			ThresholdBytes: 1 << 20,
			ContentLength:  2 << 20,
		})
	})
	assert.Equal(t, float64(0), allocsDisabled, "feature disabled baseline must be 0 allocs")

	// 2. Definitely ineligible (occupied local turn)
	allocsBlocked := testing.AllocsPerRun(1000, func() {
		sinkD, sinkR = blockedSummary.StaticDisposition(largebody.StaticDispositionInput{
			FeatureEnabled: true,
			GenerationID:   genID,
			ThresholdBytes: 1 << 20,
			ContentLength:  2 << 20,
		})
	})
	assert.Equal(t, float64(0), allocsBlocked, "definitely ineligible candidate must be 0 allocs")

	// 3. Potentially eligible candidate
	allocsEligible := testing.AllocsPerRun(1000, func() {
		sinkD, sinkR = cleanSummary.StaticDisposition(largebody.StaticDispositionInput{
			FeatureEnabled: true,
			GenerationID:   genID,
			ThresholdBytes: 1 << 20,
			ContentLength:  2 << 20,
		})
	})
	assert.Equal(t, float64(0), allocsEligible, "potentially eligible candidate must be 0 allocs")

	_ = sinkD
	_ = sinkR
}
