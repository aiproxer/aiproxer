//go:build precommit

package qa

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

// allowedRootMarkdown is the set of markdown files allowed at repository root.
var allowedRootMarkdown = map[string]bool{
	"AGENTS.md": true,
	"CLAUDE.md": true,
	"README.md": true,
	"VISION.md": true,
}

func TestRootHygiene_MarkdownFiles(t *testing.T) {
	t.Parallel()

	rootDir, err := projectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}

	entries, err := os.ReadDir(rootDir)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}

	var violations []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(strings.ToLower(name), ".md") && !allowedRootMarkdown[name] {
			violations = append(violations, name)
		}
	}
	if len(violations) > 0 {
		t.Errorf("disallowed markdown in repo root: %v (only AGENTS.md, CLAUDE.md, README.md, VISION.md)", violations)
	}
}

func TestLayout_CanonicalInternalDirsExist(t *testing.T) {
	t.Parallel()
	rootDir, err := projectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	dirs := []string{
		"internal/pluginreg",
		"internal/stdhttp",
		"internal/infra/runtimebundle",
		"internal/refbackend",
		"internal/refclient",
		"internal/qa",
		"internal/archtest",
		"internal/plugins/stores",
	}
	for _, rel := range dirs {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			st, err := os.Stat(filepath.Join(rootDir, rel))
			if err != nil {
				t.Fatalf("stat %s: %v", rel, err)
			}
			if !st.IsDir() {
				t.Fatalf("%s: expected directory", rel)
			}
		})
	}
}

func TestRootHygiene_NoLooseTextOrLogFiles(t *testing.T) {
	t.Parallel()

	rootDir, err := projectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}

	entries, err := os.ReadDir(rootDir)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}

	var bad []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		n := strings.ToLower(entry.Name())
		if !strings.HasSuffix(n, ".txt") && !strings.HasSuffix(n, ".log") {
			continue
		}
		if isGitIgnored(rootDir, entry.Name()) {
			continue
		}
		bad = append(bad, entry.Name())
	}
	if len(bad) > 0 {
		t.Errorf("unexpected .txt/.log files in repo root: %v", bad)
	}
}

// isGitIgnored reports whether an untracked repo-root file is matched by git
// ignore rules. Files covered by .gitignore are local artifacts (agents,
// builds, logs) that cannot be committed, so they do not violate repo hygiene.
// A file tracked in the index was deliberately committed and is always
// reported, as is an untracked file not matched by ignore rules (a plain
// `git add .` would stage it). When git is unavailable or a check errors,
// conservatively treat the file as not ignored so violations are still surfaced.
func isGitIgnored(rootDir, name string) bool {
	tracked := exec.Command("git", "ls-files", "--error-unmatch", "--", name)
	tracked.Dir = rootDir
	// rootDir selects the repository to inspect. Git exports GIT_DIR to every hook
	// it runs, so an inherited GIT_DIR would answer for the ambient repository.
	tracked.Env = gitscope.Environ()
	if tracked.Run() == nil {
		return false
	}
	ignored := exec.Command("git", "check-ignore", "-q", name)
	ignored.Dir = rootDir
	ignored.Env = gitscope.Environ()
	return ignored.Run() == nil
}

func projectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
