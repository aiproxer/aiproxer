package qa

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTestStagedIsolatesGitEnvironmentAndRetainsAllPackages(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	caller := newQAGitFixture(t)
	foreign := t.TempDir()
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "calls")
	shim := `#!/usr/bin/env bash
set -euo pipefail
for name in GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR GIT_OBJECT_DIRECTORY; do
  if [[ -v "$name" ]]; then
    echo "leaked Git pin: $name" >&2
    exit 12
  fi
done
case "$1" in
  list)
    printf '%s\n' example.test/core example.test/internal/archtest example.test/other
    ;;
  test)
    printf '%s\n' "$*" >> "$TEST_GO_CALLS"
    if [[ ! -d "$TEST_FOREIGN_REPO/.git" ]]; then
      git -C "$TEST_FOREIGN_REPO" init -q
      git -C "$TEST_FOREIGN_REPO" config core.bare true
    fi
    ;;
  *) exit 13 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts/test-staged.sh"))
	cmd.Dir = root
	gitDir := filepath.Join(caller.root, ".git")
	cmd.Env = append(qaGitEnv(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_DIR="+gitDir, "GIT_WORK_TREE="+caller.root,
		"GIT_INDEX_FILE="+filepath.Join(gitDir, "index"),
		"GIT_COMMON_DIR="+gitDir, "GIT_OBJECT_DIRECTORY="+filepath.Join(gitDir, "objects"),
		"TEST_GO_CALLS="+record, "TEST_FOREIGN_REPO="+foreign,
		"AIP_TEST_PRECOMMIT=1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("test runner: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(caller.git(t, "config", "--get", "core.bare")); got != "false" {
		t.Fatalf("caller repository configuration changed: core.bare=%q", got)
	}
	calls, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected ordinary and architecture lanes, got %q", calls)
	}
	for _, pkg := range []string{"example.test/core", "example.test/internal/archtest", "example.test/other"} {
		if strings.Count(string(calls), pkg) != 1 {
			t.Fatalf("package %s must run exactly once: %q", pkg, calls)
		}
	}
	for _, line := range lines {
		if !strings.Contains(line, "-tags=precommit") {
			t.Fatalf("precommit tags lost: %q", line)
		}
	}
	if strings.Contains(lines[0], "/internal/archtest") || !strings.Contains(lines[1], "/internal/archtest") {
		t.Fatalf("architecture tests must run separately after other packages: %q", calls)
	}
}
