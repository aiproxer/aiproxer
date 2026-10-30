package archtest

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const externalBillingModuleRelPath = "testdata/external_billing_binding"

// TestExternalBillingModulePublicOnlyCompileGate proves the Task 15.3
// certification module builds and passes against public packages only
// (requirements 7.1, 8.1, 8.5, 15.2, 15.3, 15.4, 18.3; contract C7). The
// module exercises a custom per-submission/credit offer with a synthetic
// non-token component through BuildWithBilling, reload retention, and
// idempotent Close without importing internal packages, a second host, or
// alternate admission/terminal paths.
func TestExternalBillingModulePublicOnlyCompileGate(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, externalBillingModuleRelPath)

	assertNoInternalImportsInDir(t, dir)

	cmd := exec.Command("go", "test", nativeArchBuildContexts()[0].trimpathFlag(), "-count=1", "./...")
	cmd.Dir = dir
	cmd.Env = enterpriseModuleTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("external billing module go test: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("output=%q", out)
	}
}

// TestExternalBillingModuleRunSmokeGate proves the module smoke entrypoint
// passes outside the test binary as well.
func TestExternalBillingModuleRunSmokeGate(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, externalBillingModuleRelPath)

	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	cmd.Env = enterpriseModuleTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("external billing module go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "external_billing_binding: ok") {
		t.Fatalf("output=%q", out)
	}
}
