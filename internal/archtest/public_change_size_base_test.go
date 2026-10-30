package archtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An absent default base is a skip only for a fresh-history public export;
// private trees must still fail closed, and public trees with a base still gate.
func TestChangedGoFileGate_PublicMissingBase(t *testing.T) {
	f := newGitFixture(t)
	f.write(t, "go.mod", "module fixture\n")
	f.git(t, "add", ".")
	f.git(t, "commit", "-qm", "export")
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
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- SKIP") || !strings.Contains(string(out), "origin/main") {
		t.Fatalf("public missing base must skip explicitly: %v\n%s", err, out)
	}
	if err := os.Mkdir(filepath.Join(f.root, "enterprise"), 0o755); err != nil {
		t.Fatal(err)
	}
	assertChangedGoFileGate(t, f, nil, "git diff count unavailable")
	if err := os.Remove(filepath.Join(f.root, "enterprise")); err != nil {
		t.Fatal(err)
	}
	f.git(t, "update-ref", "refs/remotes/origin/main", "HEAD")
	assertChangedGoFileGate(t, f, nil, "")
}
