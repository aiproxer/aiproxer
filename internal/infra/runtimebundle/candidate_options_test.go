package runtimebundle

import (
	"context"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

type dummyOptLifecycle struct {
	id string
}

func (dummyOptLifecycle) Start(context.Context) error { return nil }
func (dummyOptLifecycle) Stop(context.Context) error  { return nil }

func TestMergeCandidateBuildOptions_NilOverlay_ReturnsIndependentPointerAndIsolatesFeaturePlanes(t *testing.T) {
	t.Parallel()

	process := &BuildOptions{
		ReplaceCandidateSurface: false,
	}
	merged := mergeCandidateBuildOptions(process, nil)
	if merged == nil {
		t.Fatal("expected non-nil merged options")
	}
	if merged == process {
		t.Fatal("mergeCandidateBuildOptions(process, nil) must return an independent pointer, got shared pointer")
	}

	// Mutating merged.FeaturePlanes must not affect process.
	contrib := aipfeature.NewContributionSet()
	frozen := contrib.Freeze()
	merged.FeaturePlanes = frozen

	if !process.FeaturePlanes.IsZero() {
		t.Fatalf("mutating merged.FeaturePlanes mutated process.FeaturePlanes: %+v", process.FeaturePlanes)
	}
}

func TestMergeCandidateBuildOptions_OverlaySemantics_PreservesOrReplacesFeaturePlanes(t *testing.T) {
	t.Parallel()

	processPlanes := aipfeature.NewContributionSet().Freeze()
	process := &BuildOptions{
		FeaturePlanes: processPlanes,
	}

	// Case 1: Overlay with ReplaceCandidateSurface == false and zero FeaturePlanes preserves process planes.
	overlayNoReplace := &BuildOptions{
		ReplaceCandidateSurface: false,
	}
	merged1 := mergeCandidateBuildOptions(process, overlayNoReplace)
	if merged1 == process {
		t.Fatal("expected independent pointer")
	}
	if merged1.FeaturePlanes.IsZero() {
		t.Fatal("expected process FeaturePlanes to be preserved when overlay FeaturePlanes is zero and ReplaceCandidateSurface is false")
	}

	// Case 2: Overlay with non-zero FeaturePlanes overrides process planes.
	overlayPlanes := aipfeature.NewContributionSet().Freeze()
	overlayWithPlanes := &BuildOptions{
		FeaturePlanes:           overlayPlanes,
		ReplaceCandidateSurface: false,
	}
	merged2 := mergeCandidateBuildOptions(process, overlayWithPlanes)
	if merged2.FeaturePlanes.IsZero() {
		t.Fatal("expected overlay FeaturePlanes to be present")
	}

	// Case 3: Overlay with ReplaceCandidateSurface == true replaces even with zero FeaturePlanes.
	overlayReplaceTrue := &BuildOptions{
		ReplaceCandidateSurface: true,
	}
	merged3 := mergeCandidateBuildOptions(process, overlayReplaceTrue)
	if !merged3.FeaturePlanes.IsZero() {
		t.Fatal("expected zero FeaturePlanes when ReplaceCandidateSurface is true and overlay FeaturePlanes is zero")
	}

	// Case 4: process == nil
	if res := mergeCandidateBuildOptions(nil, nil); res != nil {
		t.Fatalf("expected nil when both are nil, got %+v", res)
	}
	overlayOnly := &BuildOptions{
		ReplaceCandidateSurface: true,
		FeaturePlanes:           overlayPlanes,
	}
	res := mergeCandidateBuildOptions(nil, overlayOnly)
	if res == nil {
		t.Fatal("expected non-nil merged options when overlay is non-nil")
	}
	if res == overlayOnly {
		t.Fatal("mergeCandidateBuildOptions(nil, overlay) must return an independent pointer, got shared pointer")
	}
	if res.ReplaceCandidateSurface {
		t.Fatal("expected ReplaceCandidateSurface to be reset to false on returned options")
	}
	if res.FeaturePlanes.IsZero() {
		t.Fatal("expected FeaturePlanes from overlay to be preserved")
	}
}

func TestMergeCandidateBuildOptions_ProcessNilOverlayNonNil_IsolationAndCloning(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	planes := aipfeature.NewContributionSet().Freeze()
	lc1 := dummyOptLifecycle{id: "lc1"}
	lc2 := dummyOptLifecycle{id: "lc2"}

	overlay := &BuildOptions{
		PluginRegistry:          reg,
		FeaturePlanes:           planes,
		FeatureLifecycles:       []aipplugin.Lifecycle{lc1, lc2},
		ReplaceCandidateSurface: true,
	}

	merged := mergeCandidateBuildOptions(nil, overlay)
	if merged == nil {
		t.Fatal("expected non-nil merged options")
	}
	if merged == overlay {
		t.Fatal("expected independent pointer, got shared pointer")
	}
	if merged.ReplaceCandidateSurface {
		t.Fatal("expected ReplaceCandidateSurface to be reset to false")
	}
	if merged.PluginRegistry != reg {
		t.Fatalf("expected PluginRegistry preserved from overlay, got %v", merged.PluginRegistry)
	}
	if merged.FeaturePlanes.IsZero() {
		t.Fatal("expected non-zero FeaturePlanes preserved from overlay")
	}

	// Two-way slice mutation isolation for FeatureLifecycles:
	merged.FeatureLifecycles[0] = dummyOptLifecycle{id: "mutated-merged-lc"}
	if overlay.FeatureLifecycles[0] != lc1 {
		t.Fatalf("mutating merged.FeatureLifecycles mutated overlay.FeatureLifecycles: got %v want %v", overlay.FeatureLifecycles[0], lc1)
	}
	overlay.FeatureLifecycles[1] = dummyOptLifecycle{id: "mutated-overlay-lc"}
	if merged.FeatureLifecycles[1] != lc2 {
		t.Fatalf("mutating overlay.FeatureLifecycles mutated merged.FeatureLifecycles: got %v want %v", merged.FeatureLifecycles[1], lc2)
	}
}

func TestMergeCandidateBuildOptions_TwoWaySliceMutationIsolation_WithProcessAndOverlay(t *testing.T) {
	t.Parallel()

	t.Run("replace_true_clones_and_isolates_slices", func(t *testing.T) {
		t.Parallel()

		procLC := dummyOptLifecycle{id: "proc-lc"}
		candLC := dummyOptLifecycle{id: "cand-lc"}

		process := &BuildOptions{
			FeatureLifecycles: []aipplugin.Lifecycle{procLC},
		}

		overlay := &BuildOptions{
			ReplaceCandidateSurface: true,
			FeatureLifecycles:       []aipplugin.Lifecycle{candLC},
		}

		merged := mergeCandidateBuildOptions(process, overlay)
		if merged == process || merged == overlay {
			t.Fatal("expected independent pointer")
		}

		// Two-way lifecycle mutation
		merged.FeatureLifecycles[0] = dummyOptLifecycle{id: "mutated"}
		if overlay.FeatureLifecycles[0] != candLC || process.FeatureLifecycles[0] != procLC {
			t.Fatal("lifecycle mutation leaked")
		}
		overlay.FeatureLifecycles[0] = dummyOptLifecycle{id: "mutated-overlay"}
		if merged.FeatureLifecycles[0] == overlay.FeatureLifecycles[0] {
			t.Fatal("overlay lifecycle mutation leaked to merged")
		}
	})

	t.Run("nil_and_empty_slices_preserved_accurately", func(t *testing.T) {
		t.Parallel()

		process := &BuildOptions{
			FeatureLifecycles: nil,
		}
		overlay := &BuildOptions{
			ReplaceCandidateSurface: true,
			FeatureLifecycles:       []aipplugin.Lifecycle{},
		}

		merged := mergeCandidateBuildOptions(process, overlay)
		if merged.FeatureLifecycles == nil {
			t.Fatal("expected non-nil empty FeatureLifecycles")
		}
		if len(merged.FeatureLifecycles) != 0 {
			t.Fatal("expected 0 length FeatureLifecycles")
		}

		process2 := &BuildOptions{
			FeatureLifecycles: []aipplugin.Lifecycle{},
		}
		overlay2 := &BuildOptions{
			ReplaceCandidateSurface: true,
			FeatureLifecycles:       nil,
		}
		merged2 := mergeCandidateBuildOptions(process2, overlay2)
		if merged2.FeatureLifecycles != nil {
			t.Fatal("expected nil FeatureLifecycles")
		}
	})
}

func TestPrependGeneratedLifecycles_TableShapesAndTwoWayAliasing(t *testing.T) {
	t.Parallel()

	l1 := dummyOptLifecycle{id: "l1"}
	l2 := dummyOptLifecycle{id: "l2"}
	l3 := dummyOptLifecycle{id: "l3"}
	l4 := dummyOptLifecycle{id: "l4"}

	tests := []struct {
		name          string
		gen           []aipplugin.Lifecycle
		overlay       []aipplugin.Lifecycle
		wantNil       bool
		wantNonNilEmp bool
		wantElements  []aipplugin.Lifecycle
	}{
		{
			name:    "nil_nil_returns_nil",
			gen:     nil,
			overlay: nil,
			wantNil: true,
		},
		{
			name:          "nil_empty_returns_nonnil_empty",
			gen:           nil,
			overlay:       []aipplugin.Lifecycle{},
			wantNonNilEmp: true,
		},
		{
			name:          "empty_nil_returns_nonnil_empty",
			gen:           []aipplugin.Lifecycle{},
			overlay:       nil,
			wantNonNilEmp: true,
		},
		{
			name:          "empty_empty_returns_nonnil_empty",
			gen:           []aipplugin.Lifecycle{},
			overlay:       []aipplugin.Lifecycle{},
			wantNonNilEmp: true,
		},
		{
			name:         "nil_pop_returns_overlay",
			gen:          nil,
			overlay:      []aipplugin.Lifecycle{l1, l2},
			wantElements: []aipplugin.Lifecycle{l1, l2},
		},
		{
			name:         "pop_nil_returns_gen",
			gen:          []aipplugin.Lifecycle{l1, l2},
			overlay:      nil,
			wantElements: []aipplugin.Lifecycle{l1, l2},
		},
		{
			name:         "empty_pop_returns_overlay",
			gen:          []aipplugin.Lifecycle{},
			overlay:      []aipplugin.Lifecycle{l1, l2},
			wantElements: []aipplugin.Lifecycle{l1, l2},
		},
		{
			name:         "pop_empty_returns_gen",
			gen:          []aipplugin.Lifecycle{l1, l2},
			overlay:      []aipplugin.Lifecycle{},
			wantElements: []aipplugin.Lifecycle{l1, l2},
		},
		{
			name:         "pop_and_overlay_preserves_order",
			gen:          []aipplugin.Lifecycle{l1, l2},
			overlay:      []aipplugin.Lifecycle{l3, l4},
			wantElements: []aipplugin.Lifecycle{l1, l2, l3, l4},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Independent input clones and immutable pre-call expected snapshots
			genInput := slices.Clone(tc.gen)
			overlayInput := slices.Clone(tc.overlay)
			genSnapshot := slices.Clone(tc.gen)
			overlaySnapshot := slices.Clone(tc.overlay)

			result := prependGeneratedLifecycles(genInput, overlayInput)

			if tc.wantNil {
				if result != nil {
					t.Fatalf("expected nil result, got %+v", result)
				}
				return
			}

			if tc.wantNonNilEmp {
				if result == nil {
					t.Fatal("expected non-nil empty slice, got nil")
				}
				if len(result) != 0 {
					t.Fatalf("expected empty slice, got len %d", len(result))
				}
				return
			}

			if len(result) != len(tc.wantElements) {
				t.Fatalf("len mismatch: got %d want %d", len(result), len(tc.wantElements))
			}
			if !slices.Equal(result, tc.wantElements) {
				t.Fatalf("result elements mismatch: got %+v want %+v", result, tc.wantElements)
			}

			// Two-way aliasing checks:
			// 1. Mutate result[0] -> assert both genInput and overlayInput exactly equal their pre-call snapshots
			if len(result) > 0 {
				result[0] = dummyOptLifecycle{id: "mutated-result"}
				if !slices.Equal(genInput, genSnapshot) {
					t.Fatalf("mutating result[0] affected genInput: got %+v want %+v", genInput, genSnapshot)
				}
				if !slices.Equal(overlayInput, overlaySnapshot) {
					t.Fatalf("mutating result[0] affected overlayInput: got %+v want %+v", overlayInput, overlaySnapshot)
				}
				// Restore result[0] for subsequent input mutation checks
				result[0] = tc.wantElements[0]
			}

			// 2. Mutate genInput separately -> assert result stays expected
			if len(genInput) > 0 {
				genInput[0] = dummyOptLifecycle{id: "mutated-gen"}
				if !slices.Equal(result, tc.wantElements) {
					t.Fatalf("mutating genInput affected result: got %+v want %+v", result, tc.wantElements)
				}
				genInput[0] = genSnapshot[0]
			}

			// 3. Mutate overlayInput separately -> assert result stays expected
			if len(overlayInput) > 0 {
				overlayInput[0] = dummyOptLifecycle{id: "mutated-overlay"}
				if !slices.Equal(result, tc.wantElements) {
					t.Fatalf("mutating overlayInput affected result: got %+v want %+v", result, tc.wantElements)
				}
				overlayInput[0] = overlaySnapshot[0]
			}
		})
	}
}

func TestMergeCandidateBuildOptions_ParallelConcurrentMerge(t *testing.T) {
	t.Parallel()

	process := &BuildOptions{
		ReplaceCandidateSurface: false,
	}

	const goroutines = 64
	done := make(chan struct{}, goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			for range 50 {
				merged := mergeCandidateBuildOptions(process, nil)
				if merged == process {
					t.Errorf("got shared pointer in goroutine %d", idx)
					return
				}
				contrib := aipfeature.NewContributionSet()
				merged.FeaturePlanes = contrib.Freeze()
			}
		}(i)
	}

	for range goroutines {
		<-done
	}

	if !process.FeaturePlanes.IsZero() {
		t.Fatalf("process FeaturePlanes mutated during parallel merges")
	}
}

func TestMergeCandidateBuildOptions_LifecyclesOverlay(t *testing.T) {
	t.Parallel()

	procLC := dummyOptLifecycle{id: "proc-lc"}
	process := &BuildOptions{
		FeatureLifecycles: []aipplugin.Lifecycle{procLC},
	}

	candLC := dummyOptLifecycle{id: "cand-lc"}
	overlay := &BuildOptions{
		ReplaceCandidateSurface: false,
		FeatureLifecycles:       []aipplugin.Lifecycle{candLC},
	}

	merged := mergeCandidateBuildOptions(process, overlay)
	if len(merged.FeatureLifecycles) != 1 || merged.FeatureLifecycles[0] != candLC {
		t.Fatalf("expected overlay FeatureLifecycles, got %+v", merged.FeatureLifecycles)
	}

	// Defensive copy: mutating merged.FeatureLifecycles must not mutate overlay.FeatureLifecycles
	merged.FeatureLifecycles[0] = dummyOptLifecycle{id: "mutated"}
	if overlay.FeatureLifecycles[0] != candLC {
		t.Fatalf("mutating merged mutated overlay.FeatureLifecycles")
	}
	if process.FeatureLifecycles[0] != procLC {
		t.Fatalf("mutating merged mutated process.FeatureLifecycles")
	}
}
