package archtest

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShrinkage_SourceOwnershipBaselinesArePinnedAndDisjoint(t *testing.T) {
	t.Parallel()
	if SourceOwnershipOverlayMax > 750 {
		t.Fatal("source ownership cap exceeds recorded ceiling 750")
	}
	want := []sourceOwnershipGrowthFile{
		{"internal/infra/runtimebundle/bootstrap_effective.go", 95},
		{"internal/infra/runtimebundle/reload_host.go", 284},
		{"internal/infra/runtimebundle/resource_ledger.go", 424},
		{"internal/infra/runtimehost/attempt_gate.go", 242},
		{"internal/infra/runtimehost/attempt_runner.go", 380},
		{"internal/infra/runtimehost/coordinator.go", 292},
		{"internal/infra/runtimehost/manager.go", 370},
		{"internal/infra/runtimehost/reload_ports.go", 67},
		{"internal/infra/runtimehost/reload_state.go", 214},
	}
	if len(sourceOwnershipGrowthFiles) > len(want) {
		t.Fatal("source ownership allowlist exceeds recorded ceiling")
	}
	root := repoRoot(t)
	baseline, err := loadGitCommitFSContext(context.Background(), root, SourceOwnershipGrowthBaselineSHA)
	if err != nil {
		t.Fatal(err)
	}
	m, err := MeasureRuntimeConvergenceShrinkage(root)
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(map[string]string)
	for _, o := range append([]OverlayMeasurement{m.Connector, m.Growth}, m.PathOverlays...) {
		for _, path := range o.Files {
			claimed[path] = o.Name
		}
	}
	// Check the entire economics allowlist too, including currently missing or
	// reduced files; exclusions in the measurement must not hide an overlap.
	for _, f := range usageEconomicsGrowthFiles {
		claimed[f.path] = "Usage economics allowlist"
	}
	seen := make(map[string]bool)
	for i, f := range sourceOwnershipGrowthFiles {
		if f.path != want[i].path || seen[f.path] {
			t.Fatalf("allowlist entry drift/duplicate: %+v", f)
		}
		if f.baseline > want[i].baseline {
			t.Fatalf("allowlist entry %s baseline %d exceeds recorded ceiling %d", f.path, f.baseline, want[i].baseline)
		}
		seen[f.path] = true
		if owner := claimed[f.path]; owner != "" {
			t.Fatalf("source ownership file %s already claimed by %s", f.path, owner)
		}
		inSurface := false
		for _, surface := range RuntimeConvergenceAffectedSurfaces {
			inSurface = inSurface || strings.HasPrefix(f.path, surface.Tree+"/")
		}
		if !inSurface || !strings.HasSuffix(f.path, ".go") || strings.HasSuffix(f.path, "_test.go") {
			t.Fatalf("allowlist outside production scanned surfaces: %s", f.path)
		}
		src, err := baseline.ReadFile(f.path)
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(bytes.NewReader(src))
		lines := 0
		for scanner.Scan() {
			lines++
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		if lines < f.baseline {
			t.Fatalf("%s baseline=%d exceeds immutable-tree lines %d", f.path, f.baseline, lines)
		}
	}
	t.Logf("source ownership actual=%d cap=%d; raw=%+d convergence=%+d required=%+d", m.SourceOwnership.Lines, m.SourceOwnership.Max, m.Delta, m.ConvergenceDelta, m.RequiredMax)
}

func TestShrinkage_SourceOwnershipGrowthBoundaries(t *testing.T) {
	t.Parallel()
	f := sourceOwnershipGrowthFiles[0]
	for _, tc := range []struct {
		name    string
		growth  int
		missing bool
		claimed bool
		want    int
		pass    bool
	}{
		{name: "missing", missing: true, pass: true},
		{name: "shrunk", growth: -1, pass: true},
		{name: "unchanged", pass: true},
		{name: "actual growth only", growth: 17, want: 17, pass: true},
		{name: "claimed elsewhere", growth: 17, claimed: true, pass: true},
		{name: "exact cap", growth: SourceOwnershipOverlayMax, want: SourceOwnershipOverlayMax, pass: true},
		{name: "over cap", growth: SourceOwnershipOverlayMax + 1, want: SourceOwnershipOverlayMax + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, surface := range RuntimeConvergenceAffectedSurfaces {
				if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(surface.Tree)), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, filepath.FromSlash(f.path))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if !tc.missing {
				if err := os.WriteFile(path, []byte(strings.Repeat("line\n", f.baseline+tc.growth)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// Arbitrary unlisted production and test additions receive no credit.
			for _, name := range []string{"unlisted.go", "bootstrap_effective_test.go"} {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), name), []byte(strings.Repeat("line\n", 1000)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			claimed := make(map[string]struct{})
			if tc.claimed {
				claimed[f.path] = struct{}{}
			}
			m, err := measureSourceOwnershipGrowthOverlay(root, claimed)
			if err != nil {
				t.Fatal(err)
			}
			if m.Name != "Config-source ownership" || m.Max != 750 || m.Lines != tc.want || m.Pass != tc.pass {
				t.Fatalf("measurement=%+v want lines=%d pass=%v", m, tc.want, tc.pass)
			}
			if !tc.claimed {
				aggregate, err := MeasureRuntimeConvergenceShrinkage(root)
				if err != nil {
					t.Fatal(err)
				}
				// This fixture easily meets the historical reduction. Over-cap
				// growth must still fail the combined gate independently.
				if aggregate.Pass != tc.pass {
					t.Fatalf("aggregate pass=%v want %v (convergence=%d)", aggregate.Pass, tc.pass, aggregate.ConvergenceDelta)
				}
			}
			if tc.want == 0 && len(m.Files) != 0 || tc.want > 0 && (len(m.Files) != 1 || m.Files[0] != f.path) {
				t.Fatalf("credited files=%v", m.Files)
			}
		})
	}
}
