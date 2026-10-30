package taskrunner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	helperOnce sync.Once
	helperPath string
	helperErr  error
)

func buildHelper(t *testing.T) string {
	t.Helper()
	helperOnce.Do(func() {
		dir, err := os.MkdirTemp("", "taskrunner-helper-")
		if err != nil {
			helperErr = err
			return
		}
		path := filepath.Join(dir, "taskrunner-testhelper"+exeSuffix())
		cmd := exec.Command("go", "build", "-o", path, "./testhelper")
		cmd.Dir = "."
		if output, err := cmd.CombinedOutput(); err != nil {
			helperErr = fmt.Errorf("build test helper: %w\n%s", err, output)
			return
		}
		helperPath = path
	})
	if helperErr != nil {
		t.Fatal(helperErr)
	}
	return helperPath
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func runHelper(t *testing.T, helper string, args ...string) Result {
	t.Helper()
	return Run(context.Background(), Request{
		Argv:    append([]string{helper}, args...),
		Timeout: 10 * time.Second,
		Output:  Capture,
	})
}

func TestRunner_Success(t *testing.T) {
	t.Parallel()
	result := runHelper(t, buildHelper(t), "-mode=success")
	if result.Kind != Success || result.ExitCode != 0 {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(string(result.Stdout), "success\n") {
		t.Fatalf("stdout = %q", result.Stdout)
	}
}

func TestRunner_ChildFailure(t *testing.T) {
	t.Parallel()
	result := runHelper(t, buildHelper(t), "-mode=fail", "-exit=7")
	if result.Kind != ChildFailure || result.ExitCode != 7 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunner_DeadlineExceeded(t *testing.T) {
	t.Parallel()
	result := Run(context.Background(), Request{
		Argv:    []string{buildHelper(t), "-mode=sleep"},
		Timeout: 100 * time.Millisecond,
		Output:  Capture,
	})
	if result.Kind != DeadlineExceeded {
		t.Fatalf("kind = %q, err = %v", result.Kind, result.Err)
	}
	if result.Cleanup.Attempted && result.Cleanup.Err != nil {
		t.Fatalf("cleanup failed: %v", result.Cleanup.Err)
	}
}

func TestRunner_Cancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := Run(ctx, Request{Argv: []string{"does-not-need-to-start"}, Timeout: time.Minute})
	if result.Kind != DeadlineExceeded {
		t.Fatalf("kind = %q, err = %v", result.Kind, result.Err)
	}
}

func TestRunner_WorkingDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	result := Run(context.Background(), Request{
		Argv: []string{buildHelper(t), "-mode=print-cwd"},
		Dir:  dir,
		// Generous guard budget: the child is started from a race-instrumented
		// test binary and this assertion needs its output; a 1s budget can be
		// blown by startup alone under -race on a loaded runner.
		Timeout: 30 * time.Second,
		Output:  Capture,
	})
	if result.Kind != Success || !strings.Contains(string(result.Stdout), dir) {
		t.Fatalf("result = %#v, cwd = %q", result, result.Stdout)
	}
	if result.Dir != dir {
		t.Fatalf("normalized dir = %q, want %q", result.Dir, dir)
	}
}

func TestRunner_RelativeWorkingDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(wd, dir)
	if err != nil {
		t.Fatal(err)
	}
	result := Run(context.Background(), Request{
		Argv:    []string{buildHelper(t), "-mode=print-cwd"},
		Dir:     relative,
		Timeout: 30 * time.Second,
		Output:  Capture,
	})
	if result.Kind != Success || !strings.Contains(string(result.Stdout), dir) {
		t.Fatalf("result = %#v", result)
	}
	if result.Dir != dir {
		t.Fatalf("normalized dir = %q, want absolute %q", result.Dir, dir)
	}
}

//nolint:paralleltest // mutates process-wide current working directory
func TestRunner_WorkingDirectoryAbsError(t *testing.T) {
	// This test changes the process-wide cwd to exercise filepath.Abs failure;
	// it must not run concurrently with tests that build or launch helpers.
	helper := buildHelper(t)
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Skipf("cannot remove the current working directory on this platform: %v", err)
	}
	result := Run(context.Background(), Request{
		Argv:    []string{helper, "-mode=success"},
		Dir:     "relative-child-dir",
		Timeout: time.Second,
	})
	if result.Kind != InvalidRequest || !strings.Contains(result.Err.Error(), "resolve cwd") {
		t.Fatalf("kind = %q, err = %v, want invalid_request resolve cwd", result.Kind, result.Err)
	}
}

func TestRunner_ChildEnvironment(t *testing.T) {
	t.Parallel()
	const key = "AIP_TASKRUNNER_CHILD_ONLY"
	_ = os.Unsetenv(key)
	result := Run(context.Background(), Request{
		Argv:      []string{buildHelper(t), "-mode=print-env", "-key", key},
		Env:       []string{key + "=child-value"},
		ClearEnv:  true,
		Timeout:   30 * time.Second,
		Output:    Capture,
		StreamOut: &bytes.Buffer{},
	})
	if result.Kind != Success || !strings.Contains(string(result.Stdout), "child-value") {
		t.Fatalf("result = %#v", result)
	}
	if _, ok := os.LookupEnv(key); ok {
		t.Fatal("runner mutated parent environment")
	}
}

func TestRunner_InvalidRequest(t *testing.T) {
	t.Parallel()
	tests := []Request{
		{Timeout: time.Second},
		{Argv: []string{"echo"}},
		{Argv: []string{"echo"}, Timeout: -time.Second},
		{Argv: []string{"echo"}, Timeout: time.Second, Env: []string{"INVALID"}},
		{Argv: []string{"echo"}, Timeout: time.Second, StdoutLimit: -1},
	}
	for i, request := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			if result := Run(context.Background(), request); result.Kind != InvalidRequest {
				t.Fatalf("kind = %q, result = %#v", result.Kind, result)
			}
		})
	}
}

func TestRunner_StreamDoesNotCapture(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	result := Run(context.Background(), Request{
		Argv:      []string{buildHelper(t), "-mode=output", "-size=128"},
		Timeout:   30 * time.Second,
		Output:    Stream,
		StreamOut: &stdout,
		StreamErr: &stderr,
	})
	if result.Kind != Success || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("result = %#v", result)
	}
	if stdout.Len() != 128 || stderr.Len() != 128 {
		t.Fatalf("stream sizes = %d/%d", stdout.Len(), stderr.Len())
	}
}

func TestRunner_CaptureHeadTailAndAggregateBounds(t *testing.T) {
	t.Parallel()
	result := Run(context.Background(), Request{
		Argv: []string{buildHelper(t), "-mode=output", "-size=100000"},
		// Guard budget, not an assertion: the child emits 200 KiB total. At the
		// 1s edge under -race on a loaded runner, the guard can expire while
		// stderr capture is still in flight (observed StderrTruncated=false with
		// a clean child exit). Deadline propagation stays covered by
		// TestRunner_DeadlineExceeded and TestRunner_Cancellation.
		Timeout:        30 * time.Second,
		Output:         Capture,
		StdoutLimit:    65536,
		StderrLimit:    65536,
		AggregateLimit: 262144,
		HeadLimit:      32768,
		TailLimit:      32768,
	})
	if result.Kind != Success {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Stdout) > 65536 || len(result.Stderr) > 65536 || len(result.Stdout)+len(result.Stderr) > 262144 {
		t.Fatalf("capture bounds exceeded: %d/%d", len(result.Stdout), len(result.Stderr))
	}
	if !result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("truncation = %v/%v", result.StdoutTruncated, result.StderrTruncated)
	}
	if !bytes.HasPrefix(result.Stdout, bytes.Repeat([]byte{'O'}, 32768)) || !bytes.HasSuffix(result.Stdout, bytes.Repeat([]byte{'o'}, 32768)) {
		t.Fatalf("stdout does not retain head and tail")
	}
}

func TestRunner_RedactsCapturedSecrets(t *testing.T) {
	t.Parallel()
	result := Run(context.Background(), Request{
		Argv:       []string{buildHelper(t), "-mode=print", "-text", "secret-token"},
		Timeout:    30 * time.Second,
		Output:     Capture,
		Redactions: []string{"secret-token"},
	})
	if result.Kind != Success || strings.Contains(string(result.Stdout), "secret-token") || !strings.Contains(string(result.Stdout), "[REDACTED]") {
		t.Fatalf("result=%#v redacted output = %q", result, result.Stdout)
	}
}
