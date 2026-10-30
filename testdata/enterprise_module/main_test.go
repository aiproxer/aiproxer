package main_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnterpriseModule_BuildAndRun(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	cmd := exec.CommandContext(context.Background(), "go", "run", ".")
	cmd.Dir = dir
	cmd.Env = enterpriseTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "enterprise_module: ok") {
		t.Fatalf("output=%q", out)
	}
}

// enterpriseTestEnv forces GOWORK=off and clears AIP_ENTERPRISE_CONFIG so the
// fixture self-bootstraps an essential custom-openai-legacy-compatible upstream
// (no external connector discovery / dogfood staging).
func enterpriseTestEnv() []string {
	out := make([]string, 0, len(os.Environ())+2)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GOWORK=") || strings.HasPrefix(e, "AIP_ENTERPRISE_CONFIG=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "GOWORK=off")
}

// TestEnterpriseModule_NoConfigTempDirLeak proves a fixture run reclaims the
// writeEssentialConfig-created temp dir (aip-enterprise-module-*) on the normal
// success path. Non-parallel so the before/after os.TempDir snapshot cannot race
// the parallel BuildAndRun invocation.
//
//nolint:paralleltest // serial: before/after os.TempDir snapshot cannot race parallel invocations
func TestEnterpriseModule_NoConfigTempDirLeak(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	before := enterpriseModuleTempDirs()
	cmd := exec.CommandContext(context.Background(), "go", "run", ".")
	cmd.Dir = dir
	cmd.Env = enterpriseTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "enterprise_module: ok") {
		t.Fatalf("output=%q", out)
	}
	after := enterpriseModuleTempDirs()
	for d := range after {
		if !before[d] {
			t.Fatalf("fixture run leaked config dir %q", d)
		}
	}
}

func enterpriseModuleTempDirs() map[string]bool {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "aip-enterprise-module-*"))
	set := make(map[string]bool, len(matches))
	for _, m := range matches {
		set[m] = true
	}
	return set
}
