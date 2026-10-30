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

// TestShrinkage_APIKeyCredentialBaselinesArePinnedAndDisjoint pins the
// credential-contract composition growth allowlist, its cap and its immutable
// baseline tree, and proves no allowlisted file is claimed by another overlay.
func TestShrinkage_APIKeyCredentialBaselinesArePinnedAndDisjoint(t *testing.T) {
	t.Parallel()
	if APIKeyCredentialCompositionOverlayMax > 75 {
		t.Fatal("api-key credential composition cap exceeds recorded ceiling 75")
	}
	want := []apiKeyCredentialGrowthFile{
		{"internal/infra/runtimebundle/build_context.go", 34},
		{"internal/infra/runtimebundle/build_model.go", 278},
		{"internal/infra/runtimebundle/candidate_compile.go", 288},
	}
	if len(apiKeyCredentialGrowthFiles) > len(want) {
		t.Fatal("api-key credential composition allowlist exceeds recorded ceiling")
	}
	root := repoRoot(t)
	baseline, err := loadGitCommitFSContext(context.Background(), root, CredentialCompositionGrowthBaselineSHA)
	if err != nil {
		t.Fatal(err)
	}
	m, err := MeasureRuntimeConvergenceShrinkage(root)
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(map[string]string)
	for _, o := range append([]OverlayMeasurement{m.Connector, m.Growth, m.SourceOwnership}, m.PathOverlays...) {
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
	for i, f := range apiKeyCredentialGrowthFiles {
		if f.path != want[i].path || seen[f.path] {
			t.Fatalf("allowlist entry drift/duplicate: %+v", f)
		}
		if f.baseline > want[i].baseline {
			t.Fatalf("allowlist entry %s baseline %d exceeds recorded ceiling %d", f.path, f.baseline, want[i].baseline)
		}
		seen[f.path] = true
		if owner := claimed[f.path]; owner != "" {
			t.Fatalf("api-key credential composition file %s already claimed by %s", f.path, owner)
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
	if m.APIKeyCredential.Name != "API-key credential composition" || m.APIKeyCredential.Max != 75 {
		t.Fatalf("credential overlay identity drift: %+v", m.APIKeyCredential)
	}
	// Task 2's authorized-candidate-snapshot composition adds 18 lines across
	// the three allowlisted files, and the discovered-inventory default route
	// slice adds one more to candidate_compile.go. The 5.4-C cutover shrinks
	// candidate_compile.go below its baseline, so only the build_context.go and
	// build_model.go growth (17) is credited. The overlay must credit only
	// positive growth, never baseline code.
	if m.APIKeyCredential.Lines != 17 {
		t.Fatalf("credential overlay lines = %d, want 17 (task-2 growth outside the shrunk candidate_compile.go)", m.APIKeyCredential.Lines)
	}
	t.Logf("api-key credential actual=%d cap=%d; raw=%+d convergence=%+d required=%+d", m.APIKeyCredential.Lines, m.APIKeyCredential.Max, m.Delta, m.ConvergenceDelta, m.RequiredMax)
}

// TestShrinkage_APIKeyCredentialGrowthBoundaries proves the allowance credits
// only positive growth in allowlisted files: deletions and missing files pass
// with zero credit, unlisted files receive no credit, claimed files are
// skipped, and growth beyond the cap fails both the overlay and the gate.
func TestShrinkage_APIKeyCredentialGrowthBoundaries(t *testing.T) {
	t.Parallel()
	f := apiKeyCredentialGrowthFiles[0]
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
		{name: "actual growth only", growth: 5, want: 5, pass: true},
		{name: "claimed elsewhere", growth: 5, claimed: true, pass: true},
		{name: "exact cap", growth: APIKeyCredentialCompositionOverlayMax, want: APIKeyCredentialCompositionOverlayMax, pass: true},
		{name: "over cap", growth: APIKeyCredentialCompositionOverlayMax + 1, want: APIKeyCredentialCompositionOverlayMax + 1},
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
			for _, name := range []string{"unlisted.go", "build_context_test.go"} {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), name), []byte(strings.Repeat("line\n", 1000)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			claimed := make(map[string]struct{})
			if tc.claimed {
				claimed[f.path] = struct{}{}
			}
			m, err := measureAPIKeyCredentialCompositionOverlay(root, claimed)
			if err != nil {
				t.Fatal(err)
			}
			if m.Name != "API-key credential composition" || m.Max != 75 || m.Lines != tc.want || m.Pass != tc.pass {
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
