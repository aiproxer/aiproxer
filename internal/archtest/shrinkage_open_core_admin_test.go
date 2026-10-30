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

// TestShrinkage_OpenCoreAdminBaselinesArePinnedAndDisjoint pins the open-core
// admin-seam growth allowlist, its cap and its immutable baseline tree, proves
// the aggregate measurement embeds exactly the overlay it measures, and proves
// no allowlisted file is claimed by another overlay.
func TestShrinkage_OpenCoreAdminBaselinesArePinnedAndDisjoint(t *testing.T) {
	t.Parallel()
	if OpenCoreAdminGrowthBaselineSHA != "cbd1762ddabccfef071d60924cb98c0539228e15" || OpenCoreAdminGrowthOverlayMax != 233 {
		t.Fatal("open-core admin seam growth baseline SHA/cap drift")
	}
	want := []openCoreAdminGrowthFile{
		{"internal/infra/runtimebundle/admin_mounts.go", 0},
		{"internal/infra/runtimebundle/authority_queries.go", 0},
		{"internal/infra/runtimebundle/compile_generation.go", 439},
		{"internal/stdhttp/admin/controlplane/accounting_authority.go", 187},
		{"internal/stdhttp/admin/controlplane/adapt.go", 45},
		{"internal/stdhttp/contract/http_input.go", 250},
		{"internal/stdhttp/handler.go", 109},
		{"internal/stdhttp/mount_admin.go", 283},
		{"internal/stdhttp/request_plane.go", 80},
	}
	if len(openCoreAdminGrowthFiles) != len(want) {
		t.Fatal("open-core admin seam growth allowlist drift")
	}
	root := repoRoot(t)
	// The peeled expression must resolve to the literal frozen commit: the same
	// tree, with a distinct loadGitCommitFSContext cache key.
	peeled, err := runCmd(root, "git", "rev-parse", OpenCoreAdminGrowthBaselineSHA+"^0")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(peeled)); got != OpenCoreAdminGrowthBaselineSHA {
		t.Fatalf("peeled baseline expression resolved to %s, want %s", got, OpenCoreAdminGrowthBaselineSHA)
	}
	// Load the pinned commit through its peeled expression: the commit tree is
	// identical, but the cache key stays distinct from the raw full-SHA load the
	// cancellation fixture performs for HEAD, and the policy tree's HEAD equals
	// this frozen tree, so neither test can serve the other a context outcome.
	baseline, err := loadGitCommitFSContext(context.Background(), root, OpenCoreAdminGrowthBaselineSHA+"^0")
	if err != nil {
		t.Fatal(err)
	}
	m, err := MeasureRuntimeConvergenceShrinkage(root)
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(map[string]string)
	for _, o := range append([]OverlayMeasurement{m.Connector, m.Growth, m.SourceOwnership, m.APIKeyCredential, m.OpenCoreSeam}, m.PathOverlays...) {
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
	for _, f := range openCoreSeamGrowthFiles {
		claimed[f.path] = "Open-core seam growth allowlist"
	}
	seen := make(map[string]bool)
	for i, f := range openCoreAdminGrowthFiles {
		if f != want[i] || seen[f.path] {
			t.Fatalf("allowlist entry drift/duplicate: %+v", f)
		}
		seen[f.path] = true
		if owner := claimed[f.path]; owner != "" {
			t.Fatalf("open-core admin seam growth file %s already claimed by %s", f.path, owner)
		}
		inSurface := false
		for _, surface := range RuntimeConvergenceAffectedSurfaces {
			inSurface = inSurface || strings.HasPrefix(f.path, surface.Tree+"/")
		}
		if !inSurface || !strings.HasSuffix(f.path, ".go") || strings.HasSuffix(f.path, "_test.go") {
			t.Fatalf("allowlist outside production scanned surfaces: %s", f.path)
		}
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
	// The aggregate measurement must embed exactly the overlay it measures;
	// an unwired or exclusion-skipped file would break this identity. The
	// comparison reuses the task-4.1 seam pin's overlay-measurement equality.
	direct, err := measureOpenCoreAdminGrowthOverlay(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !openCoreSeamOverlayEqual(m.OpenCoreAdmin, direct) {
		t.Fatalf("aggregate open-core admin seam measurement %+v != direct measurement %+v", m.OpenCoreAdmin, direct)
	}
	t.Logf("open-core admin seam actual=%d cap=%d; raw=%+d convergence=%+d required=%+d", m.OpenCoreAdmin.Lines, m.OpenCoreAdmin.Max, m.Delta, m.ConvergenceDelta, m.RequiredMax)
}

// TestShrinkage_OpenCoreAdminGrowthBoundaries proves the allowance credits the
// seam's net signed growth, clamped at zero: absent files count zero current
// lines and contribute their full negative baseline (never skipped), in-seam
// shrinkages reduce the credit, claimed files are skipped, unlisted files
// receive no credit, and growth beyond the cap fails both the overlay and the
// gate.
func TestShrinkage_OpenCoreAdminGrowthBoundaries(t *testing.T) {
	t.Parallel()
	newFile := openCoreAdminGrowthFiles[0] // admin_mounts.go, frozen baseline 0
	shrunk := openCoreAdminGrowthFiles[2]  // compile_generation.go, frozen baseline 439
	for _, tc := range []struct {
		name             string
		newLines         int // lines written to the baseline-0 allowlist file; -1 keeps it absent
		compileLines     int // lines written to the 439-line allowlist file; -1 keeps it absent
		accountingAbsent bool
		claimed          bool
		want             int
		pass             bool
	}{
		{name: "all allowlist files at baseline", newLines: 0, compileLines: 439, pass: true},
		{name: "absent baseline absorbs growth", newLines: 600, compileLines: -1, want: 161, pass: true},
		{name: "absent baselines clamp below zero", newLines: 100, compileLines: -1, accountingAbsent: true, pass: true},
		{name: "in-seam shrinkage reduces the net", newLines: 150, compileLines: 339, want: 50, pass: true},
		{name: "growth only", newLines: 17, compileLines: 439, want: 17, pass: true},
		{name: "claimed elsewhere", newLines: 17, compileLines: 439, claimed: true, pass: true},
		{name: "exact cap", newLines: 233, compileLines: 439, want: 233, pass: true},
		{name: "over cap", newLines: 234, compileLines: 439, want: 234},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, surface := range RuntimeConvergenceAffectedSurfaces {
				if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(surface.Tree)), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			// Every allowlisted file starts at its frozen baseline, so each case
			// varies only the files it names.
			for _, f := range openCoreAdminGrowthFiles {
				path := filepath.Join(root, filepath.FromSlash(f.path))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(strings.Repeat("line\n", f.baseline)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, filepath.FromSlash(newFile.path))
			if err := os.WriteFile(path, []byte(strings.Repeat("line\n", tc.newLines)), 0o600); err != nil {
				t.Fatal(err)
			}
			compilePath := filepath.Join(root, filepath.FromSlash(shrunk.path))
			if tc.compileLines < 0 {
				if err := os.Remove(compilePath); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(compilePath, []byte(strings.Repeat("line\n", tc.compileLines)), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.accountingAbsent {
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(openCoreAdminGrowthFiles[3].path))); err != nil {
					t.Fatal(err)
				}
			}
			// Arbitrary unlisted production and test additions receive no credit.
			for _, name := range []string{"unlisted.go", "admin_mounts_test.go"} {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), name), []byte(strings.Repeat("line\n", 1000)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			claimed := make(map[string]struct{})
			if tc.claimed {
				claimed[newFile.path] = struct{}{}
			}
			m, err := measureOpenCoreAdminGrowthOverlay(root, claimed)
			if err != nil {
				t.Fatal(err)
			}
			if m.Name != "Open-core admin seam growth" || m.Max != 233 || m.Lines != tc.want || m.Pass != tc.pass {
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
			if tc.want == 0 && len(m.Files) != 0 || tc.want > 0 && (len(m.Files) != 1 || m.Files[0] != newFile.path) {
				t.Fatalf("credited files=%v", m.Files)
			}
		})
	}
}
