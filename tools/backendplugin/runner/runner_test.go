package runner

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

func TestErrorIncludesEachBoundedDiagnosticOnce(t *testing.T) {
	t.Parallel()
	result := taskrunner.Result{
		Kind:   taskrunner.ChildFailure,
		Label:  "selector:test",
		Stdout: []byte("test-looking stdout"),
		Stderr: []byte("child stderr"),
		Err:    errors.New("exit status 7"),
		Cleanup: taskrunner.CleanupResult{
			Attempted: true,
			Err:       errors.New("cleanup failed"),
		},
	}

	diagnostic := Error(result).Error()
	for _, marker := range []string{"test-looking stdout", "child stderr", "exit status 7", "cleanup: cleanup failed"} {
		if got := strings.Count(diagnostic, marker); got != 1 {
			t.Errorf("%q count = %d in %q", marker, got, diagnostic)
		}
	}
}
