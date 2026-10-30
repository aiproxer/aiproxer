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

// TestShrinkage_OpenCoreServeBaselinesArePinnedAndDisjoint pins the open-core
// serve growth allowlist, its cap and its immutable baseline tree, proves the
// aggregate measurement embeds exactly the overlay it measures, and proves no
// allowlisted file is claimed by another overlay.
func TestShrinkage_OpenCoreServeBaselinesArePinnedAndDisjoint(t *testing.T) {
	t.Parallel()
	if OpenCoreServeGrowthBaselineSHA != "92cba5a2203e681903db52e5dfd760dedb2ce2b1" || OpenCoreServeGrowthOverlayMax != 390 {
		t.Fatal("open-core serve growth baseline SHA/cap drift")
	}
	want := []openCoreServeGrowthFile{
		{"internal/stdhttp/generation_host.go", 202},
		{"pkg/aipruntime/build.go", 48},
		{"pkg/aipruntime/host.go", 68},
		{"pkg/aipruntime/serve.go", 0},
	}
	if len(openCoreServeGrowthFiles) != len(want) {
		t.Fatal("open-core serve growth allowlist drift")
	}
	root := repoRoot(t)
	// The peeled expression must resolve to the literal frozen commit: the same
	// tree, with a distinct loadGitCommitFSContext cache key.
	peeled, err := runCmd(root, "git", "rev-parse", OpenCoreServeGrowthBaselineSHA+"^0")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(peeled)); got != OpenCoreServeGrowthBaselineSHA {
		t.Fatalf("peeled baseline expression resolved to %s, want %s", got, OpenCoreServeGrowthBaselineSHA)
	}
	baseline, err := loadGitCommitFSContext(context.Background(), root, OpenCoreServeGrowthBaselineSHA+"^0")
	if err != nil {
		t.Fatal(err)
	}
	m, err := MeasureRuntimeConvergenceShrinkage(root)
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(map[string]string)
	for _, o := range append([]OverlayMeasurement{m.Connector, m.Growth, m.SourceOwnership, m.APIKeyCredential, m.OpenCoreSeam, m.OpenCoreAdmin}, m.PathOverlays...) {
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
	for _, f := range openCoreAdminGrowthFiles {
		claimed[f.path] = "Open-core admin seam growth allowlist"
	}
	seen := make(map[string]bool)
	for i, f := range openCoreServeGrowthFiles {
		if f != want[i] || seen[f.path] {
			t.Fatalf("allowlist entry drift/duplicate: %+v", f)
		}
		seen[f.path] = true
		if owner := claimed[f.path]; owner != "" {
			t.Fatalf("open-core serve growth file %s already claimed by %s", f.path, owner)
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
	// an unwired or exclusion-skipped file would break this identity. It must
	// also measure exactly the pinned cap: the allowance is the measured C4
	// growth with no extra reserve, so any drift requires a deliberate re-pin.
	direct, err := measureOpenCoreServeGrowthOverlay(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !openCoreSeamOverlayEqual(m.OpenCoreServe, direct) {
		t.Fatalf("aggregate open-core serve measurement %+v != direct measurement %+v", m.OpenCoreServe, direct)
	}
	if direct.Lines != OpenCoreServeGrowthOverlayMax {
		t.Fatalf("open-core serve growth measured %d != pinned cap %d: re-measure and re-pin deliberately", direct.Lines, OpenCoreServeGrowthOverlayMax)
	}
	t.Logf("open-core serve actual=%d cap=%d; raw=%+d convergence=%+d required=%+d", m.OpenCoreServe.Lines, m.OpenCoreServe.Max, m.Delta, m.ConvergenceDelta, m.RequiredMax)
}

// TestShrinkage_OpenCoreServeGrowthBoundaries proves the allowance credits the
// seam's net signed growth, clamped at zero: absent files count zero current
// lines and contribute their full negative baseline (never skipped), in-seam
// shrinkages offset growth, claimed files are skipped, unlisted files receive
// no credit, and growth beyond the cap fails both the overlay and the gate.
func TestShrinkage_OpenCoreServeGrowthBoundaries(t *testing.T) {
	t.Parallel()
	newFile := openCoreServeGrowthFiles[3]   // serve.go, frozen baseline 0
	baselined := openCoreServeGrowthFiles[1] // build.go, frozen baseline 48
	shrunk := openCoreServeGrowthFiles[2]    // host.go, frozen baseline 68
	for _, tc := range []struct {
		name      string
		newLines  int // lines written to the baseline-0 allowlist file
		hostLines int // lines written to the 68-line allowlist file; -1 keeps it absent
		dropBuild bool
		claimed   bool
		want      int
		pass      bool
	}{
		{name: "all allowlist files at baseline", newLines: 0, hostLines: 68, pass: true},
		{name: "new file growth only", newLines: 17, hostLines: 68, want: 17, pass: true},
		{name: "in-seam shrinkage offsets growth", newLines: 150, hostLines: 18, want: 100, pass: true},
		{name: "deleted baseline file offsets growth", newLines: 17, hostLines: 68, dropBuild: true, pass: true},
		{name: "claimed elsewhere", newLines: 17, hostLines: 68, claimed: true, pass: true},
		{name: "exact cap", newLines: OpenCoreServeGrowthOverlayMax, hostLines: 68, want: OpenCoreServeGrowthOverlayMax, pass: true},
		{name: "over cap", newLines: OpenCoreServeGrowthOverlayMax + 1, hostLines: 68, want: OpenCoreServeGrowthOverlayMax + 1},
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
			for _, f := range openCoreServeGrowthFiles {
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
			hostPath := filepath.Join(root, filepath.FromSlash(shrunk.path))
			if tc.hostLines < 0 {
				if err := os.Remove(hostPath); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(hostPath, []byte(strings.Repeat("line\n", tc.hostLines)), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.dropBuild {
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(baselined.path))); err != nil {
					t.Fatal(err)
				}
			}
			// Arbitrary unlisted production and test additions receive no credit.
			for _, name := range []string{"unlisted.go", "serve_test.go"} {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), name), []byte(strings.Repeat("line\n", 1000)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			claimed := make(map[string]struct{})
			if tc.claimed {
				claimed[newFile.path] = struct{}{}
			}
			m, err := measureOpenCoreServeGrowthOverlay(root, claimed)
			if err != nil {
				t.Fatal(err)
			}
			if m.Name != "Open-core serve growth" || m.Max != OpenCoreServeGrowthOverlayMax || m.Lines != tc.want || m.Pass != tc.pass {
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
			switch {
			case tc.want == 0 && len(m.Files) != 0:
				t.Fatalf("credited files=%v want none", m.Files)
			case tc.want > 0 && (len(m.Files) != 1 || m.Files[0] != newFile.path):
				t.Fatalf("credited files=%v want [%s]", m.Files, newFile.path)
			}
		})
	}
}
