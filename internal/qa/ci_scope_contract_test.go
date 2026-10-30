package qa

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
	"gopkg.in/yaml.v3"
)

func TestCIScopeClassifier_SelfTest(t *testing.T) {
	t.Parallel()
	script := repositoryFile(t, "scripts", "ci-scope.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Bash is not guaranteed on Windows; the remote Ubuntu workflow executes
		// the authoritative shell self-test.
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", script, "--self-test")
	// The shell self-test builds throwaway repositories with `git -C "$tmp"
	// init/commit`. Git exports GIT_DIR to every hook it runs, so an inherited
	// GIT_DIR would initialise and commit inside the real repository, destroying
	// its index and refs.
	cmd.Env = gitscope.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("scope self-test failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "OK: CI scope self-test") {
		t.Fatalf("scope self-test marker missing: %q", output)
	}
}

func TestCIPreflight_LocalScanHasCheckoutWithoutEnablingGoForDocs(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("workflow expressions execute through Bash on the Ubuntu runner")
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				If   string `yaml:"if"`
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", "ci.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, result, code, event string
		checkout, scan, goSetup   bool
	}{
		{name: "spec PR", result: "success", code: "false", event: "pull_request", checkout: true, scan: true},
		{name: "code PR", result: "success", code: "true", event: "pull_request", checkout: true, scan: true, goSetup: true},
		{name: "failed scope", result: "failure", code: "false", event: "pull_request"},
		{name: "docs push", result: "success", code: "false", event: "push"},
		{name: "code push", result: "success", code: "true", event: "push", checkout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			checkedOut, sawScan, sawGo := false, false, false
			for _, step := range workflow.Jobs["preflight"].Steps {
				checkout := strings.HasPrefix(step.Uses, "actions/checkout@")
				setupGo := strings.HasPrefix(step.Uses, "actions/setup-go@")
				scan := strings.Contains(step.Run, "bash scripts/check-local-specifics.sh")
				if !checkout && !setupGo && !scan {
					continue
				}
				condition := strings.NewReplacer(
					"needs.changes.result", "'"+tc.result+"'",
					"needs.changes.outputs.code", "'"+tc.code+"'",
					"github.event_name", "'"+tc.event+"'",
				).Replace(step.If)
				cmd := exec.CommandContext(ctx, "bash", "-c", "[[ "+condition+" ]]")
				cmd.Env = gitscope.Environ()
				out, err := cmd.CombinedOutput()
				enabled := err == nil
				if err != nil {
					exit, ok := err.(*exec.ExitError)
					if !ok || exit.ExitCode() != 1 {
						t.Fatalf("evaluate workflow condition %q: %v\n%s", step.If, err, out)
					}
				}
				if checkout {
					checkedOut = enabled
				}
				if setupGo {
					sawGo = true
					if enabled != tc.goSetup {
						t.Fatalf("Go setup enabled=%v, want %v", enabled, tc.goSetup)
					}
				}
				if scan {
					sawScan = true
					if enabled != tc.scan {
						t.Fatalf("local scan enabled=%v, want %v", enabled, tc.scan)
					}
					if enabled && !checkedOut {
						t.Fatal("local-environment scan runs before repository checkout")
					}
				}
			}
			if !sawScan || !sawGo {
				t.Fatal("preflight scan or Go setup step missing")
			}
			if checkedOut != tc.checkout {
				t.Fatalf("checkout enabled=%v, want %v", checkedOut, tc.checkout)
			}
		})
	}
}
