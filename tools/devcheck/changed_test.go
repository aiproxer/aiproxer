package main

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/tools/devcheck/internal/testscope"
)

type failedDiagnosticWriter struct{}

func (failedDiagnosticWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestPlanReportingFailureIsNotSuccess(t *testing.T) {
	t.Parallel()
	if err := runTestPlan(t.Context(), t.TempDir(), testscope.Plan{}, testPlanOptions{jobs: 1, repeat: 1, dry: true}, io.Discard, failedDiagnosticWriter{}); err == nil {
		t.Fatal("ignored plan-reporting failure")
	}
}

func TestChangedScopeRejectsManualAndNonTestOptions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ task, module, packages string }{
		{"test", "connectors/one", ""},
		{"test", ".", "./base"},
		{"build", ".", ""},
	} {
		if err := validateChangedScope(tc.task, tc.module, tc.packages); err == nil {
			t.Errorf("accepted automatic scope with %+v", tc)
		}
	}
}

func TestPlanOnlyDoesNotExecuteOrValidateTestModules(t *testing.T) {
	t.Parallel()
	plan := testscope.Plan{Base: "base", Modules: []testscope.Module{{Directory: "missing", Packages: []string{"./..."}}}}
	var output, diagnostics strings.Builder
	err := runTestPlan(t.Context(), t.TempDir(), plan, testPlanOptions{jobs: 1, repeat: 2, dry: true}, &output, &diagnostics)
	if err != nil || !strings.Contains(diagnostics.String(), "plan-only") || output.Len() != 0 {
		t.Fatalf("dry-run err=%v output=%s diagnostics=%s", err, output.String(), diagnostics.String())
	}
}

func TestSelectedExecutionRepeatAndFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "repeat", true: "failure"}[failure], func(t *testing.T) {
			modules := []testscope.Module{{Directory: ".", Packages: []string{"./one"}}}
			var diagnostics strings.Builder
			calls := 0
			err := repeatTestModules(modules, 2, &diagnostics, func(module testscope.Module, iteration int) error {
				calls++
				if iteration != calls || module.Directory != "." || len(module.Packages) != 1 || module.Packages[0] != "./one" {
					t.Fatalf("changed repeat scope: module=%+v iteration=%d", module, iteration)
				}
				if failure {
					return errors.New("test process failed")
				}
				return nil
			})
			if (err != nil) != failure {
				t.Fatalf("failure=%v err=%v", failure, err)
			}
			if !failure && (calls != 2 || strings.Count(diagnostics.String(), "execution_elapsed=") != 2) {
				t.Fatalf("repeat did not execute the same plan: %s", diagnostics.String())
			}
			if failure && calls != 1 {
				t.Fatal("continued after failed tests")
			}
		})
	}
}
