//go:build precommit

package qa

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	visionDigestBegin = "<!-- vision-digest:begin -->"
	visionDigestEnd   = "<!-- vision-digest:end -->"
)

// TestVisionDigest_AgentsMatchesVision keeps the decision-principles block in
// AGENTS.md a verbatim copy of the owner-ratified block in VISION.md, so agents
// that only load AGENTS.md act on the same principles.
func TestVisionDigest_AgentsMatchesVision(t *testing.T) {
	t.Parallel()

	rootDir, err := projectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	want := visionDigest(t, filepath.Join(rootDir, "VISION.md"))
	got := visionDigest(t, filepath.Join(rootDir, "AGENTS.md"))
	if got != want {
		t.Errorf("AGENTS.md vision digest differs from VISION.md; copy the %s block from VISION.md verbatim", visionDigestBegin)
	}
}

func visionDigest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.Count(text, visionDigestBegin) != 1 || strings.Count(text, visionDigestEnd) != 1 {
		t.Fatalf("%s: want exactly one %s ... %s block", path, visionDigestBegin, visionDigestEnd)
	}
	_, rest, _ := strings.Cut(text, visionDigestBegin)
	block, _, _ := strings.Cut(rest, visionDigestEnd)
	if strings.TrimSpace(block) == "" {
		t.Fatalf("%s: empty vision digest block", path)
	}
	return strings.TrimSpace(block)
}
