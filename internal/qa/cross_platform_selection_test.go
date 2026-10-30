package qa

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

//nolint:paralleltest // Each subtest mutates the same temporary git checkout and must remain serial.
func TestCrossPlatformSelection(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("selector uses the repository's POSIX bash runtime")
	}

	root := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = gitscope.Environ()
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	runGit("init", "-q")
	runGit("config", "user.email", "qa@example.com")
	runGit("config", "user.name", "QA")
	runGit("config", "commit.gpgsign", "false")
	for _, path := range []string{"connectors/foo/release.yaml", "connectors/bar/release.yaml", "base.txt"} {
		writeSelectionFixture(t, root, path)
	}
	runGit("add", ".")
	runGit("commit", "-qm", "base")
	base := gitRevision(t, root)

	script := filepath.Join(root, "cross-platform-selection.sh")
	contents, err := os.ReadFile(repositoryFile(t, "scripts", "cross-platform-selection.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	// Keep the selector in the base commit. Otherwise the first case stages
	// it, and the next checkout removes the script along with that case.
	runGit("add", "cross-platform-selection.sh")
	runGit("commit", "-qm", "selector fixture")
	base = gitRevision(t, root)

	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "connector", path: "connectors/foo/source.go", want: "foo"},
		{name: "support", path: "connector-support/acp/client.go", want: "acp,agycliacp,claudeagentacp,codex,cursorcliacp,geminicliacp"},
		{name: "module metadata", path: "connectors/foo/go.mod", want: ""},
		{name: "docs only", path: "docs.md", want: ""},
		{name: "unknown connector", path: "connectors/new/source.go", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Cases intentionally mutate one checkout. Parallelism here races
			// Git's index and mixes the changed-file sets under test.
			runGit("checkout", "-q", base)
			writeSelectionFixture(t, root, tc.path)
			runGit("add", ".")
			runGit("commit", "-qm", tc.name)
			head := gitRevision(t, root)

			cmd := exec.Command("bash", script, base, head)
			cmd.Dir = root
			// The selector script resolves its repository from its own git
			// calls, so it must not inherit an ambient GIT_DIR either.
			cmd.Env = gitscope.Environ()
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("selector: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != tc.want {
				t.Fatalf("selection = %q, want %q", got, tc.want)
			}
		})
	}
}

func writeSelectionFixture(t *testing.T, root, path string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRevision(t *testing.T, root string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = root
	cmd.Env = gitscope.Environ()
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}
