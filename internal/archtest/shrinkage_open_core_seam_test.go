package archtest

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestShrinkage_OpenCoreSeamBaselinesArePinnedAndDisjoint pins the open-core
// attachment-seam growth allowlist, its cap and its immutable baseline tree,
// proves the aggregate measurement embeds exactly the overlay it measures, and
// proves no allowlisted file is claimed by another overlay.
func TestShrinkage_OpenCoreSeamBaselinesArePinnedAndDisjoint(t *testing.T) {
	t.Parallel()
	if OpenCoreSeamGrowthBaselineSHA != "413c4a9a7516f07dccece56c3110510cace140b9" || OpenCoreSeamGrowthOverlayMax != 130 {
		t.Fatal("open-core seam growth baseline SHA/cap drift")
	}
	want := []openCoreSeamGrowthFile{
		{"internal/infra/runtimebundle/build_observability.go", 107},
		{"cmd/aipstd/wiring.go", 9},
		{"cmd/aipstd/command.go", 420},
		{"pkg/aipruntime/normalize.go", 105},
		{"pkg/aipruntime/options.go", 59},
		{"pkg/aipruntime/capabilities.go", 0},
	}
	if len(openCoreSeamGrowthFiles) != len(want) {
		t.Fatal("open-core seam growth allowlist drift")
	}
	root := repoRoot(t)
	m, err := MeasureRuntimeConvergenceShrinkage(root)
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(map[string]string)
	for _, o := range append([]OverlayMeasurement{m.Connector, m.Growth, m.SourceOwnership, m.APIKeyCredential}, m.PathOverlays...) {
		for _, path := range o.Files {
			claimed[path] = o.Name
		}
	}
	// Check every other growth allowlist in full, including currently missing
	// or reduced files; exclusions in the measurement must not hide an overlap.
	for _, f := range usageEconomicsGrowthFiles {
		claimed[f.path] = "Usage economics allowlist"
	}
	for _, f := range sourceOwnershipGrowthFiles {
		claimed[f.path] = "Config-source ownership allowlist"
	}
	for _, f := range apiKeyCredentialGrowthFiles {
		claimed[f.path] = "API-key credential composition allowlist"
	}
	seen := make(map[string]bool)
	for i, f := range openCoreSeamGrowthFiles {
		if f != want[i] || seen[f.path] {
			t.Fatalf("allowlist entry drift/duplicate: %+v", f)
		}
		seen[f.path] = true
		if owner := claimed[f.path]; owner != "" {
			t.Fatalf("open-core seam growth file %s already claimed by %s", f.path, owner)
		}
		inSurface := false
		for _, surface := range RuntimeConvergenceAffectedSurfaces {
			inSurface = inSurface || strings.HasPrefix(f.path, surface.Tree+"/")
		}
		if !inSurface || !strings.HasSuffix(f.path, ".go") || strings.HasSuffix(f.path, "_test.go") {
			t.Fatalf("allowlist outside production scanned surfaces: %s", f.path)
		}
	}
	t.Run("pinned-baseline", func(t *testing.T) {
		skipMissingPublicBaseline(t, root, OpenCoreSeamGrowthBaselineSHA)
		// Peeling uses a cache key distinct from the cancellation fixture's SHA.
		baseline, err := loadGitCommitFSContext(context.Background(), root, OpenCoreSeamGrowthBaselineSHA+"^0")
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range openCoreSeamGrowthFiles {
			src, err := baseline.ReadFile(f.path)
			if f.baseline == 0 {
				if err == nil {
					t.Fatalf("%s baseline=0, but the frozen tree contains it", f.path)
				}
				continue
			}
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
			if lines != f.baseline {
				t.Fatalf("%s baseline=%d, immutable tree has %d", f.path, f.baseline, lines)
			}
		}
	})
	// The aggregate measurement must embed exactly the overlay it measures;
	// an unwired or exclusion-skipped file would break this identity.
	direct, err := measureOpenCoreSeamGrowthOverlay(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !openCoreSeamOverlayEqual(m.OpenCoreSeam, direct) {
		t.Fatalf("aggregate open-core seam measurement %+v != direct measurement %+v", m.OpenCoreSeam, direct)
	}
	t.Logf("open-core seam actual=%d cap=%d; raw=%+d convergence=%+d required=%+d", m.OpenCoreSeam.Lines, m.OpenCoreSeam.Max, m.Delta, m.ConvergenceDelta, m.RequiredMax)
}

// openCoreSeamOverlayEqual reports whether two measurements of the open-core
// seam overlay are identical, including the credited file list.
func openCoreSeamOverlayEqual(a, b OverlayMeasurement) bool {
	return a.Name == b.Name && a.Max == b.Max && a.Lines == b.Lines && a.Pass == b.Pass && slices.Equal(a.Files, b.Files)
}

// TestShrinkage_OpenCoreSeamGrowthBoundaries proves the allowance credits only
// positive growth in allowlisted files: deletions and missing files pass with
// zero credit, unlisted files receive no credit, claimed files are skipped,
// and growth beyond the cap fails both the overlay and the gate.
func TestShrinkage_OpenCoreSeamGrowthBoundaries(t *testing.T) {
	t.Parallel()
	f := openCoreSeamGrowthFiles[0]
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
		{name: "exact cap", growth: OpenCoreSeamGrowthOverlayMax, want: OpenCoreSeamGrowthOverlayMax, pass: true},
		{name: "over cap", growth: OpenCoreSeamGrowthOverlayMax + 1, want: OpenCoreSeamGrowthOverlayMax + 1},
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
			for _, name := range []string{"unlisted.go", "build_observability_test.go"} {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), name), []byte(strings.Repeat("line\n", 1000)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			claimed := make(map[string]struct{})
			if tc.claimed {
				claimed[f.path] = struct{}{}
			}
			m, err := measureOpenCoreSeamGrowthOverlay(root, claimed)
			if err != nil {
				t.Fatal(err)
			}
			if m.Name != "Open-core seam growth" || m.Max != 130 || m.Lines != tc.want || m.Pass != tc.pass {
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
