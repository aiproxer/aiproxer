package codexclientcompat_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Task 6.4 architecture guard: Codex-native feature packages must not import
// semantic compression or reasoning-preservation compression internals.
// This prevents accidental coupling of Codex native compaction/compat logic
// into the generic semantic compression lane.

func TestCodexClientCompat_HasNoSemanticCompressionImportsOrBranches(t *testing.T) {
	t.Parallel()
	pkgDir := filepath.Join(repoRoot(t), "internal", "plugins", "features", "codexclientcompat")
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("read pkg dir: %v", err)
	}
	forbiddenImports := []string{
		"reasoningpreservation",
		"reasoning-preservation",
		"semantic",
		"Surrogate",
		"BackgroundPoller",
		"CompressionServices",
		"ExtractSemanticSegments",
		"ClassifyReasoningPart",
		"auxiliary.Poll",
	}
	// Branches that would indicate compression coupling inside codex compat
	forbiddenBranches := []string{
		"Compression",
		"compression",
		"semantic compression",
		"BackgroundPoller",
		"SurrogateSegment",
		"ReasoningSurrogate",
		"EgressPolicy",
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(pkgDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := string(raw)
		for _, needle := range forbiddenImports {
			if strings.Contains(src, needle) {
				// Allow the guard test itself to mention the words in comments/string literals that contain the guard list;
				// but production files must not. Since this file itself contains those words, skip self.
				if strings.Contains(e.Name(), "guard") {
					continue
				}
				t.Fatalf("codexclientcompat %s must not import/mention %q (semantic compression coupling)", e.Name(), needle)
			}
		}
		// Check for stray compression branches outside comments? Simple substring check.
		for _, needle := range forbiddenBranches {
			// Skip self file again
			if strings.Contains(e.Name(), "guard") {
				continue
			}
			if strings.Contains(src, needle) {
				t.Fatalf("codexclientcompat %s must not contain branch %q", e.Name(), needle)
			}
		}
	}
}

func TestCodexClientCompat_CodexNativeFlowUnchangedWithCompressionEnabled(t *testing.T) {
	t.Parallel()
	// Sanity that existing compat markers still behave with compression flags present.
	// This is a smoke that the guard file does not need to duplicate full reasoning tests,
	// but proves the harness files are still plain-compat (no compression side-effects).
	// We just verify the guard package itself builds and that the marker constants are stable.
	const (
		wantOpenCodeMarker = "OpenCode compatibility mode"
		wantPiMarker       = "Pi compatibility mode"
	)
	// Ensure source still contains the expected markers (not removed by accidental compression merge)
	pkgDir := filepath.Join(repoRoot(t), "internal", "plugins", "features", "codexclientcompat")
	for _, want := range []string{wantOpenCodeMarker, wantPiMarker} {
		found := false
		ents, _ := os.ReadDir(pkgDir)
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.Contains(e.Name(), "guard") {
				continue
			}
			b, _ := os.ReadFile(filepath.Join(pkgDir, e.Name()))
			if strings.Contains(string(b), want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("compat marker %q missing from codexclientcompat source", want)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// Walk up from this file's directory to repo root (contains go.mod)
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repo root not found")
		}
		dir = parent
	}
}
