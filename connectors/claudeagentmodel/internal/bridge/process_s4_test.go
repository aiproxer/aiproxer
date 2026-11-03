package bridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestProcess_SecretIsEnvironmentOnly proves the connector-owned launch keeps a
// secret out of argv; only the isolated child environment carries it.
func TestProcess_SecretIsEnvironmentOnly(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the executable-wrapper assertion is Linux-hosted")
	}
	realNode, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("testdata", "fake-sidecar.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "argv")
	envFile := filepath.Join(dir, "env")
	wrapper := filepath.Join(dir, "node")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + strconv.Quote(argsFile) +
		"\nprintf '%s' \"$ANTHROPIC_API_KEY\" > " + strconv.Quote(envFile) +
		"\nexec " + strconv.Quote(realNode) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	const canary = "sk-ant-argv-canary-value"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proc, err := Start(ctx, Options{
		Node:   wrapper,
		Script: script,
		Env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + t.TempDir(),
			"ANTHROPIC_API_KEY=" + canary,
		},
		Dir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Close() }()
	if _, err := proc.AwaitHello(ctx); err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(argv), canary) {
		t.Fatalf("secret leaked into argv: %q", argv)
	}
	environment, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(environment) != canary {
		t.Fatalf("secret missing from the isolated child environment")
	}
}
