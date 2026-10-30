package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestRunVersion(t *testing.T) {
	oldVersion := version
	version = "1.2.3-test"
	t.Cleanup(func() { version = oldVersion })

	for _, arg := range []string{"--version", "version"} {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(t.Context(), []string{arg}, &stdout, &stderr); code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
			}
			if got := strings.TrimSpace(stdout.String()); got != "aipstd 1.2.3-test" {
				t.Fatalf("output = %q", got)
			}
		})
	}
}

func TestRunVersionWriteFailure(t *testing.T) {
	var stderr bytes.Buffer
	if code := run(t.Context(), []string{"--version"}, failingWriter{}, &stderr); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
	if !strings.Contains(stderr.String(), "write version") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
