package qa

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

const maxDirtyGoFiles = 100

// largeChangeOverrideEnv is the maintainer-only change-size override that
// tools/changesize and the commit hooks honor; this budget honors it too.
const largeChangeOverrideEnv = "AIP_ALLOW_LARGE_CHANGE"

func dirtyGoLimitExceeded(count int) bool {
	return count > maxDirtyGoFiles
}

// largeChangeOverride reports whether the maintainer override is set.
func largeChangeOverride() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(largeChangeOverrideEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// mergeInProgress reports whether the repository is mid-merge.
//
// The dirty-Go-file budget exists to catch one large UNREVIEWED change. While a
// merge from the integration branch is in progress, every dirty file is content
// that already passed review on that branch, so counting it against this budget
// makes a legitimate sync unsatisfiable no matter how it is split. The exemption
// is deliberately scoped to an in-progress merge only: outside a merge every
// ordinary commit is still held to the full limit.
func mergeInProgress(root string) bool {
	cmd := exec.Command("git", "rev-parse", "-q", "--verify", "MERGE_HEAD")
	cmd.Dir = root
	return cmd.Run() == nil
}

func listDirtyGoFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	cmd.Dir = root
	// root selects the repository to report on. Git exports GIT_DIR to every hook
	// it runs, so an inherited GIT_DIR would report the ambient repository's
	// status instead of the requested one.
	cmd.Env = gitscope.Environ()
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("git status: %w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git status: %w", err)
	}
	return parsePorcelainGoPaths(out), nil
}

func parsePorcelainGoPaths(raw []byte) []string {
	raw = bytes.TrimRight(raw, "\x00")
	if len(raw) == 0 {
		return nil
	}
	fields := bytes.Split(raw, []byte{0})
	seen := make(map[string]struct{})
	var paths []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || !strings.HasSuffix(path, ".go") {
			return
		}
		path = filepath.ToSlash(path)
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		if len(field) < 4 || field[2] != ' ' {
			continue
		}
		x, y := field[0], field[1]
		add(string(field[3:]))
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			i++
			if i < len(fields) {
				add(string(fields[i]))
			}
		}
	}
	return paths
}
