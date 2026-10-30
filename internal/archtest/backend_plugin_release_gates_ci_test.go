package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendPluginReleaseGates_gitignoreReportArtifact(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), ".aip-release-gates-report.json") {
		t.Fatal(".gitignore must ignore release gates report")
	}
}
