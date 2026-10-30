//go:build linux || darwin

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/tools/devcheck/internal/evidence"
)

func TestCLI_MetadataDeadlinePublishesFailureWithoutRunningChecks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	for name, contents := range map[string]string{
		"go.mod": "module example.invalid/probe\n\ngo 1.26.9\n",
		"go":     "#!/bin/sh\necho $$ > probe.pid\nsleep 600 & echo $! > child.pid\nwait\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestDevcheckSignalHelper$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "DEVCHECK_CLI_FIXTURE="+path, "DEVCHECK_CLI_TIMEOUT=300ms")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = time.Second
	t.Cleanup(func() {
		for _, name := range []string{"probe.pid", "child.pid"} {
			data, _ := os.ReadFile(filepath.Join(dir, name))
			pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				process, _ := os.FindProcess(pid)
				_ = process.Kill()
			}
		}
	})
	if err := cmd.Run(); err == nil || ctx.Err() != nil || cmd.ProcessState.ExitCode() != 1 {
		t.Fatalf("metadata outlived its deadline: exit=%d error=%v parent=%v", cmd.ProcessState.ExitCode(), err, ctx.Err())
	}
	manifest := readManifest(t, path)
	if manifest.Outcome != evidence.OutcomeFailed || len(manifest.Steps) != 0 || !strings.Contains(manifest.FailureReason, "deadline") {
		t.Fatalf("interrupted metadata claimed checks ran or passed: %+v", manifest)
	}
}
