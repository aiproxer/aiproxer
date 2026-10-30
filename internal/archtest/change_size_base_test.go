package archtest

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Exercise the actual gate in this test binary, not just its Git counter: an
// invalid comparison must fail rather than turn into a successful test skip.
func TestChangedGoFileGate_LocalStackedBase(t *testing.T) {
	f := newGitFixture(t)
	f.write(t, "go.mod", "module fixture\n")
	f.git(t, "add", ".")
	f.git(t, "commit", "-qm", "root")
	f.git(t, "update-ref", "refs/remotes/origin/main", "HEAD")
	for i := range 101 {
		f.write(t, fmt.Sprintf("previous/p%d.go", i), "package previous\n")
	}
	f.git(t, "add", ".")
	f.git(t, "commit", "-qm", "predecessor")
	predecessor := strings.TrimSpace(f.git(t, "rev-parse", "HEAD"))
	f.git(t, "branch", "frozen-predecessor")
	for i := range 100 {
		f.write(t, fmt.Sprintf("slice/p%d.go", i), "package slice\n")
	}
	f.git(t, "add", ".")
	f.git(t, "commit", "-qm", "slice at limit")
	// Neither the staged index nor unstaged/untracked successors belong to the
	// committed comparison. The staged hook checks its own independent scope.
	f.write(t, "staged.go", "package fixture\n")
	f.git(t, "add", "staged.go")
	f.write(t, "untracked.go", "package fixture\n")
	f.write(t, "slice/p0.go", "package successor\n")
	tree := strings.TrimSpace(f.git(t, "rev-parse", "HEAD^{tree}"))
	nonancestor := strings.TrimSpace(f.git(t, "commit-tree", tree, "-m", "unrelated root"))
	for _, tc := range []struct {
		name, base, ci, want string
		setBase              bool
	}{
		{name: "default aggregate", want: "changed *.go files 201 exceeds gate 100"},
		{name: "frozen predecessor", base: "frozen-predecessor", setBase: true},
		{name: "pinned predecessor", base: predecessor, setBase: true},
		{name: "explicit aggregate", base: "origin/main", setBase: true, want: "changed *.go files 201 exceeds gate 100"},
		{name: "empty", setBase: true, want: "DEV_BASE must name a commit"},
		{name: "whitespace", base: " ", setBase: true, want: "DEV_BASE must name a commit"},
		{name: "missing", base: "missing-predecessor", setBase: true, want: "resolve DEV_BASE"},
		{name: "not a commit", base: tree, setBase: true, want: "resolve DEV_BASE"},
		{name: "nonancestor", base: nonancestor, setBase: true, want: "must be an ancestor of HEAD"},
		{name: "CI rejects local base", base: predecessor, setBase: true, ci: "CI=true", want: "DEV_BASE is local-only"},
		{name: "GitHub rejects local base", base: predecessor, setBase: true, ci: "GITHUB_ACTIONS=true", want: "DEV_BASE is local-only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := []string{}
			if tc.setBase {
				env = append(env, "DEV_BASE="+tc.base)
			}
			if tc.ci != "" {
				env = append(env, tc.ci)
			}
			assertChangedGoFileGate(t, f, env, tc.want)
		})
	}
	f.git(t, "update-ref", "refs/remotes/origin/main", predecessor)
	assertChangedGoFileGate(t, f, nil, "") // Unset DEV_BASE still compares to origin/main.
	assertChangedGoFileGate(t, f, []string{"CI=true"}, "")
	f.git(t, "commit", "-qm", "slice over limit")
	assertChangedGoFileGate(t, f, []string{"DEV_BASE=" + predecessor}, "changed *.go files 101 exceeds gate 100")
	f.git(t, "update-ref", "-d", "refs/remotes/origin/main")
	assertChangedGoFileGate(t, f, nil, "git diff count unavailable")
	f.root = t.TempDir()
	f.write(t, "go.mod", "module fixture\n")
	assertChangedGoFileGate(t, f, nil, "git diff count unavailable")
}

func assertChangedGoFileGate(t *testing.T, f gitFixture, extraEnv []string, wantFailure string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestConversationViewChangedGoFileCountUnderGate$", "-test.v")
	cmd.Dir = f.root
	for _, kv := range f.env {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "DEV_BASE", "CI", "GITHUB_ACTIONS", "AIP_ALLOW_LARGE_CHANGE":
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), "--- SKIP") {
		t.Fatalf("gate skipped instead of checking comparison: %s", out)
	}
	if wantFailure == "" {
		if err != nil {
			t.Fatalf("gate rejected bounded slice: %v\n%s", err, out)
		}
	} else if err == nil || !strings.Contains(string(out), wantFailure) {
		t.Fatalf("want gate failure %q, got %v\n%s", wantFailure, err, out)
	}
}
