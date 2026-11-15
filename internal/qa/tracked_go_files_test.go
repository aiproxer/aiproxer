package qa

import (
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

// trackedGoFiles lists the worktree's tracked Go files as sorted,
// repository-relative slash paths. Reading the index instead of walking the
// tree keeps scratch files out of repository checks, and an exported tree
// (fresh history, a subset of the files) measures exactly what it ships: an
// absent recorded path is a removal, never a failure.
func trackedGoFiles(t *testing.T, root string) []string {
	t.Helper()

	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	cmd.Env = gitscope.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, name := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if strings.HasSuffix(name, ".go") {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	return files
}
