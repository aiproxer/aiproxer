package taskrunner

import (
	"bytes"
	"context"
	"strconv"
	"testing"
	"time"
)

func TestRunner_NormalExitCapture(t *testing.T) {
	t.Parallel()
	helper := buildHelper(t)
	req := Request{
		Argv:    []string{helper, "-mode=output", "-size=20"},
		Timeout: 5 * time.Second,
		Output:  Capture,
	}
	result := Run(context.Background(), req)
	if result.Kind != Success {
		t.Fatalf("expected Kind == Success, got %v (err: %v)", result.Kind, result.Err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected ExitCode == 0, got %d", result.ExitCode)
	}
	wantStdout := "OOOOOOOOOOoooooooooo"
	wantStderr := "EEEEEEEEEEeeeeeeeeee"
	if string(result.Stdout) != wantStdout {
		t.Fatalf("expected Stdout %q, got %q", wantStdout, string(result.Stdout))
	}
	if string(result.Stderr) != wantStderr {
		t.Fatalf("expected Stderr %q, got %q", wantStderr, string(result.Stderr))
	}
	if result.StdoutTruncated {
		t.Fatalf("expected StdoutTruncated == false, got true")
	}
	if result.StderrTruncated {
		t.Fatalf("expected StderrTruncated == false, got true")
	}
	if result.Cleanup.Attempted {
		t.Fatalf("expected Cleanup.Attempted == false, got true")
	}
}

func TestRunner_TimeoutPath(t *testing.T) {
	t.Parallel()
	helper := buildHelper(t)
	timeout := 100 * time.Millisecond
	req := Request{
		Argv:    []string{helper, "-mode=sleep"},
		Timeout: timeout,
		Output:  Capture,
	}
	start := time.Now()
	result := Run(context.Background(), req)
	elapsed := time.Since(start)

	if result.Kind != DeadlineExceeded {
		t.Fatalf("expected Kind == DeadlineExceeded, got %v", result.Kind)
	}
	if !result.Cleanup.Attempted {
		t.Fatalf("expected Cleanup.Attempted == true, got false")
	}
	if result.Cleanup.Err != nil {
		t.Fatalf("expected Cleanup.Err == nil, got %v", result.Cleanup.Err)
	}
	if elapsed > 10*timeout {
		t.Fatalf("expected prompt return, elapsed %v exceeded deadline bound", elapsed)
	}
	if result.DurationClass != "near_deadline" {
		t.Fatalf("expected DurationClass == 'near_deadline', got %q", result.DurationClass)
	}
}

func TestRunner_StreamMode(t *testing.T) {
	t.Parallel()
	helper := buildHelper(t)
	var outBuf, errBuf bytes.Buffer
	req := Request{
		Argv:      []string{helper, "-mode=output", "-size=20"},
		Timeout:   5 * time.Second,
		Output:    Stream,
		StreamOut: &outBuf,
		StreamErr: &errBuf,
	}
	result := Run(context.Background(), req)
	if result.Kind != Success {
		t.Fatalf("expected Kind == Success, got %v (err: %v)", result.Kind, result.Err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected ExitCode == 0, got %d", result.ExitCode)
	}
	wantStdout := "OOOOOOOOOOoooooooooo"
	wantStderr := "EEEEEEEEEEeeeeeeeeee"
	if outBuf.String() != wantStdout {
		t.Fatalf("expected StreamOut %q, got %q", wantStdout, outBuf.String())
	}
	if errBuf.String() != wantStderr {
		t.Fatalf("expected StreamErr %q, got %q", wantStderr, errBuf.String())
	}
	if len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("expected empty captured stdout/stderr in Stream mode, got stdout=%q, stderr=%q", result.Stdout, result.Stderr)
	}
	if result.Cleanup.Attempted {
		t.Fatalf("expected Cleanup.Attempted == false in clean stream run")
	}
}

// TestRunner_CaptureReturnsCompleteOutput proves capture mode does not return
// before os/exec's output copies finish for an ordinary command. Wait now runs
// immediately (so a leader that exits while a descendant holds the pipes is
// observed), and completeness comes from Wait waiting for its own copy
// goroutines rather than from a caller-side drain barrier.
func TestRunner_CaptureReturnsCompleteOutput(t *testing.T) {
	t.Parallel()
	helper := buildHelper(t)
	const size = 50000
	result := Run(context.Background(), Request{
		Argv:    []string{helper, "-mode=output", "-size=" + strconv.Itoa(size)},
		Timeout: 10 * time.Second,
		Output:  Capture,
	})
	if result.Kind != Success {
		t.Fatalf("kind = %q, err = %v; want success", result.Kind, result.Err)
	}
	if len(result.Stdout) != size || len(result.Stderr) != size {
		t.Fatalf("captured stdout/stderr = %d/%d bytes, want %d each", len(result.Stdout), len(result.Stderr), size)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		t.Fatalf("truncated = %t/%t; a 50 KiB stream fits the default 64 KiB bound", result.StdoutTruncated, result.StderrTruncated)
	}
}
