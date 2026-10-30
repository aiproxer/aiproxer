package changesurface

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

// GitStatusReport classifies tracked, staged, and untracked paths at HEAD.
// It intentionally uses porcelain output rather than shell formatting, making
// path separators and rename handling stable across operating systems.
func GitStatusReport(repoRoot string) (Report, error) {
	cmd := exec.Command("git", "-C", repoRoot, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	cmd.Env = gitscope.Environ()
	data, err := cmd.Output()
	if err != nil {
		return Report{}, fmt.Errorf("git status: %w", err)
	}
	paths, err := ParsePorcelainZ(data)
	if err != nil {
		return Report{}, err
	}
	return Build(paths), nil
}

// GitBaseReport classifies the complete implementation delta from base to the
// current worktree. Untracked files are included so evidence can be generated
// before the final orchestration commit.
func GitBaseReport(repoRoot, base string) (Report, error) {
	cmd := exec.Command("git", "-C", repoRoot, "diff", "--name-only", "--no-renames", "-z", base)
	cmd.Env = gitscope.Environ()
	diffData, err := cmd.Output()
	if err != nil {
		return Report{}, fmt.Errorf("git diff from %s: %w", base, err)
	}
	statusReport, err := GitStatusReport(repoRoot)
	if err != nil {
		return Report{}, err
	}
	paths := splitNULPaths(diffData)
	for _, categoryPaths := range statusReport.Paths {
		// Status paths include all modified files, including files already in the
		// base diff. Build deduplicates the union and keeps untracked additions.
		paths = append(paths, categoryPaths...)
	}
	sort.Strings(paths)
	return Build(paths), nil
}

func splitNULPaths(data []byte) []string {
	fields := strings.Split(string(data), "\x00")
	paths := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != "" {
			paths = append(paths, field)
		}
	}
	return paths
}
