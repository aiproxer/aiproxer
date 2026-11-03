package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// TestMain also serves as the supervised child for
// TestVerifyProductSupervisorPropagatesExitCode. The supervisor starts its -Go
// argument with the product-smoke arguments, so this test binary exits with
// AIP_VERIFY_PRODUCT_STUB_EXIT instead of running tests when that variable is
// set. That lets the test observe exit-code propagation without a helper build.
func TestMain(m *testing.M) {
	if code, ok := os.LookupEnv("AIP_VERIFY_PRODUCT_STUB_EXIT"); ok {
		parsed, err := strconv.Atoi(code)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid AIP_VERIFY_PRODUCT_STUB_EXIT %q: %v\n", code, err)
			os.Exit(125)
		}
		os.Exit(parsed)
	}
	os.Exit(m.Run())
}

// TestVerifyProductSupervisorPropagatesExitCode runs the real outer supervisor
// with a child that succeeds and one that fails. On Windows this uses Windows
// PowerShell 5.1, the interpreter the Makefile invokes: a supervisor that turns
// the failed child into exit 0 blocks the product-smoke verdict from reaching
// CI. A missing pwsh on other hosts skips rather than pretending coverage.
func TestVerifyProductSupervisorPropagatesExitCode(t *testing.T) {
	t.Parallel()
	shell, err := verifyProductShell()
	if err != nil {
		t.Skip(err)
	}
	if _, err := os.Stat(shell); err != nil {
		t.Skipf("supervisor interpreter unavailable: %v", err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "verify-product.ps1")
	child, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []int{0, 7} {
		want := want
		t.Run(fmt.Sprintf("child_exit_%d", want), func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
				"-File", script, "-Go", child)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "AIP_VERIFY_PRODUCT_STUB_EXIT="+strconv.Itoa(want))
			output, err := cmd.CombinedOutput()
			if cmd.ProcessState == nil {
				t.Fatalf("supervisor did not run: %v\n%s", err, output)
			}
			if got := cmd.ProcessState.ExitCode(); got != want {
				t.Fatalf("supervisor exit code = %d, want %d (child code must be the verdict): %v\n%s", got, want, err, output)
			}
			if want == 0 && err != nil {
				t.Fatalf("supervisor reported a successful child as failure: %v\n%s", err, output)
			}
		})
	}
}

func verifyProductShell() (string, error) {
	if runtime.GOOS == "windows" {
		return exec.LookPath("powershell.exe")
	}
	return exec.LookPath("pwsh")
}
