package qa

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A built Go binary is always larger than the release check's blob cap, so the
// cap keeps compiled command outputs from being committed even when a manifest
// pattern such as `connectors/**` approves their path.
func TestReleaseClean_RejectsOversizedBlobs(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		// Bash is not guaranteed on Windows; the remote Ubuntu workflow executes
		// the authoritative shell check.
		return
	}

	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	writeModuleCheckFixture(t,
		filepath.Join(root, "scripts", "check-release-clean.sh"),
		readRepositoryFile(t, "scripts", "check-release-clean.sh"),
		0o755,
	)
	writeModuleCheckFixture(t, filepath.Join(root, ".release-files"), ".release-files\nscripts/**\nbin/**\n", 0o644)
	writeModuleCheckFixture(t, filepath.Join(root, "bin", "small"), strings.Repeat("x", 4096), 0o644)
	git("add", "-A")

	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", append([]string{filepath.Join(root, "scripts", "check-release-clean.sh")}, args...)...)
		cmd.Dir = root
		return cmd.CombinedOutput()
	}

	if output, err := run("--staged"); err != nil {
		t.Fatalf("release check rejected a small approved file: %v\n%s", err, output)
	}

	writeModuleCheckFixture(t, filepath.Join(root, "bin", "tool"), string(bytes.Repeat([]byte{0x7f}, 1<<20+1)), 0o755)
	git("add", "-A")
	output, err := run("--staged")
	if err == nil {
		t.Fatalf("release check accepted a staged blob over the size cap:\n%s", output)
	}
	if !strings.Contains(string(output), "bin/tool") {
		t.Fatalf("staged size violation did not name the file: %q", output)
	}

	git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "oversized")
	output, err = run("--ref", "HEAD")
	if err == nil {
		t.Fatalf("release check accepted a committed blob over the size cap:\n%s", output)
	}
	if !strings.Contains(string(output), "bin/tool") {
		t.Fatalf("ref size violation did not name the file: %q", output)
	}
}
