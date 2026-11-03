package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var shellFence = regexp.MustCompile("(?s)```(?:sh|bash)\\n(.*?)```")

// TestCertificationDocs_CertificationCommandsForceFreshRun rejects a published
// certification, record or smoke command that omits -count=1. Installed bytes and
// child processes are external cache inputs, so a cached PASS is not evidence.
func TestCertificationDocs_CertificationCommandsForceFreshRun(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	files := []string{
		filepath.Join(root, "..", "..", "docs", "claude-agent-model.md"),
		filepath.Join(root, "certification", "README.md"),
	}
	found := 0
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, block := range shellFence.FindAllStringSubmatch(string(body), -1) {
			joined := strings.ReplaceAll(block[1], "\\\n", " ")
			for line := range strings.SplitSeq(joined, "\n") {
				line = strings.TrimSpace(line)
				if !certificationGoTestLine(line) {
					continue
				}
				found++
				if !strings.Contains(line, "-count=1") {
					t.Errorf("%s shell block %d certification command lacks -count=1: %s", filepath.Base(file), i+1, line)
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("no published certification/smoke go test commands were found")
	}
}

func certificationGoTestLine(line string) bool {
	if !strings.Contains(line, "go ") || !strings.Contains(line, " test") || !strings.Contains(line, "-run") {
		return false
	}
	for _, marker := range []string{"TestCertification_", "TestPrivateSmoke_", "TestPackage_ClaudeAgentModel"} {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// TestCertificationDocs_ShellBlocksParse runs bash -n on every published shell
// example. A prose census cannot catch a syntactically invalid operator command;
// this check exercises the documented command text itself.
func TestCertificationDocs_ShellBlocksParse(t *testing.T) {
	t.Parallel()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	root := moduleRoot(t)
	files := []string{
		filepath.Join(root, "..", "..", "docs", "claude-agent-model.md"),
		filepath.Join(root, "certification", "README.md"),
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		blocks := shellFence.FindAllStringSubmatch(string(body), -1)
		if len(blocks) == 0 {
			t.Fatalf("%s has no shell blocks", file)
		}
		for i, block := range blocks {
			cmd := exec.Command(bash, "-n")
			cmd.Stdin = strings.NewReader(block[1])
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s shell block %d is invalid: %v\n%s", filepath.Base(file), i+1, err, out)
			}
		}
	}
}
