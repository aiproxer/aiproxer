package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestReasoningPreservationLifecycle_NoFeatureOwnedPollGoroutine ensures the
// reasoning-preservation feature does not spawn a background poll loop.
func TestReasoningPreservationLifecycle_NoFeatureOwnedPollGoroutine(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	pkg := "internal/plugins/features/reasoningpreservation"
	err := WalkProductionGoFiles(root, func(rel, _ string, src []byte) error {
		if PackageDirFromRel(rel) != pkg {
			return nil
		}
		lower := strings.ToLower(string(src))
		_ = filepath.Join // keep import
		// Forbid feature-owned polling goroutine markers.
		if strings.Contains(lower, "go poll") || strings.Contains(lower, "go s.poll") || strings.Contains(lower, "time.tick") && strings.Contains(lower, "poll") {
			t.Fatalf("%s appears to own a poll goroutine", rel)
		}
		// Forbid ticker-based poll loop and generic poll loop.
		if strings.Contains(lower, "for {") && strings.Contains(lower, "poller.poll") {
			t.Fatalf("%s contains a poll loop", rel)
		}
		// Specific markers: a feature file that imports time and contains a Poll loop with sleep/ticker
		if strings.Contains(lower, "pollresult") && strings.Contains(lower, "for {") && strings.Contains(lower, "sleep") {
			t.Fatalf("%s contains a blocking poll loop", rel)
		}
		// Ensure no `go func` that calls Poll in a loop
		if strings.Contains(lower, "backgroundpoller") && strings.Contains(lower, "go ") {
			t.Fatalf("%s contains a background poller goroutine", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Also assert via AST: no file in feature contains a function that spawns a goroutine
	// and calls Poll in a loop — simpler check: count `go ` occurrences in feature production files
	// that also contain Poll.
	goFiles := 0
	pollGoFiles := 0
	err = WalkProductionGoFiles(root, func(rel, _ string, src []byte) error {
		if PackageDirFromRel(rel) != pkg {
			return nil
		}
		goFiles++
		s := string(src)
		if strings.Contains(s, "go ") && strings.Contains(s, "Poll(") {
			pollGoFiles++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pollGoFiles > 0 {
		t.Fatalf("feature package must not contain Poll() inside a goroutine: found %d file(s)", pollGoFiles)
	}
	_ = goFiles
}

// TestReasoningPreservationLifecycle_CompressionServicesComposition ensures
// generation-local compression services are explicitly constructed and not
// accessed via a global locator.
func TestReasoningPreservationLifecycle_CompressionServicesComposition(t *testing.T) {
	t.Parallel()
	assertProductionDirectImportsExclude(t, []string{"internal/plugins/features/reasoningpreservation"}, []string{
		"github.com/aiproxer/aiproxer/internal/infra/runtimebundle",
		"github.com/aiproxer/aiproxer/internal/core/auxreq",
	})
	// Feature must not import runtimebundle to fetch scheduler; composition passes it explicitly.
}
