package taskrunner

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestWaitDelayCause_DistinguishesObservedFromSuspected pins the cleanup
// classification contract: both branches wrap exec.ErrWaitDelay, but only the
// signal os/exec actually reports is asserted as an observed pipe timeout. A
// wait that merely consumed the whole bound after the kill is reported as
// suspected, because a stalled scheduler or a slow exit produces the same time.
func TestWaitDelayCause_DistinguishesObservedFromSuspected(t *testing.T) {
	t.Parallel()

	if got := waitDelayCause(exec.ErrWaitDelay, time.Time{}); got != exec.ErrWaitDelay {
		t.Fatalf("observed cause = %v, want the reported exec.ErrWaitDelay unchanged", got)
	}

	suspected := waitDelayCause(errors.New("signal: killed"), time.Now().Add(-processWaitDelay))
	if !errors.Is(suspected, exec.ErrWaitDelay) {
		t.Fatalf("suspected cause = %v, want an error wrapping exec.ErrWaitDelay", suspected)
	}
	if !strings.Contains(suspected.Error(), "suspected") {
		t.Fatalf("suspected cause = %q, want it described as suspected rather than observed", suspected)
	}

	if got := waitDelayCause(errors.New("signal: killed"), time.Time{}); got != nil {
		t.Fatalf("cause without a recorded kill = %v, want nil", got)
	}
	if got := waitDelayCause(errors.New("signal: killed"), time.Now()); got != nil {
		t.Fatalf("cause before the bound elapsed = %v, want nil", got)
	}
}
