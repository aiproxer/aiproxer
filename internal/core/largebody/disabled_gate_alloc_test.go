package largebody_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

var sinkDisabledEligible bool

// disabledCandidateEligible mirrors the future Task 7.3 cheap-gate order:
// feature flag first, then executor capability. With the feature disabled it
// must return before any capture/scanner/profile state exists.
func disabledCandidateEligible(cfg config.LargePayloadFastPathConfig, exec aipsdk.ExecutorView) bool {
	if !cfg.Enabled {
		return false
	}
	_, ok := largebody.AsLargeBodyExecutor(exec)
	return ok
}

//nolint:paralleltest // AllocsPerRun forbids parallel tests.
func TestDisabledCandidateGate_NoAllocation(t *testing.T) {
	disabled := config.LargePayloadFastPathConfig{}
	if disabled.Enabled {
		t.Fatal("LargePayloadFastPath must default to disabled (Requirement 1)")
	}
	canonical := &canonicalOnlyExecutor{}
	allocs := testing.AllocsPerRun(1000, func() {
		sinkDisabledEligible = disabledCandidateEligible(disabled, canonical)
	})
	if sinkDisabledEligible {
		t.Fatal("disabled candidate gate must decline to the canonical path")
	}
	if allocs != 0 {
		t.Fatalf("disabled candidate gate allocs = %v, want 0 (trivial branch only, Requirement 1)", allocs)
	}
}

//nolint:paralleltest // AllocsPerRun forbids parallel tests.
func TestCapabilityProbe_NoAllocation(t *testing.T) {
	canonical := &canonicalOnlyExecutor{}
	capable := &capableExecutor{}
	cases := map[string]aipsdk.ExecutorView{
		"nil":       nil,
		"canonical": canonical,
		"capable":   capable,
	}
	for name, exec := range cases {
		allocs := testing.AllocsPerRun(1000, func() {
			_, _ = largebody.AsLargeBodyExecutor(exec)
		})
		if allocs != 0 {
			t.Fatalf("AsLargeBodyExecutor(%s) allocs = %v, want 0 (trivial branch only, Requirement 1)", name, allocs)
		}
	}
}
