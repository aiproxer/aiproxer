package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/archtest/tools/changesurface"
)

func TestRun_BaseFlagUsesSelectedRevision(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", ".", "-base", "HEAD", "-json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run exit=%d stderr=%q", code, stderr.String())
	}
	var rep changesurface.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("base report invalid JSON: %v, output=%q", err, stdout.String())
	}
	if rep.Counts == nil || rep.Paths == nil {
		t.Fatalf("base report JSON missing counts or paths structure: %+v", rep)
	}
}

func TestRun_BaseFlagRejectsUnknownRevision(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", ".", "-base", "definitely-not-a-real-revision", "-json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("invalid base exit=%d, want 2; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "git diff from") {
		t.Fatalf("invalid base error did not come from Git diff: %q", stderr.String())
	}
}

func TestRun_UnknownFlagFails(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-does-not-exist"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown flag exit=%d, want 2; stderr=%q", code, stderr.String())
	}
}
