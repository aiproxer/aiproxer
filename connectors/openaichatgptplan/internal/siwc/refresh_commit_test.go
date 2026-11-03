package siwc

import (
	"errors"
	"fmt"
	"testing"
)

// withRefreshCommit must record the commit outcome on the classified failure
// whether the failure arrives directly or wrapped: the terminal check on the
// same value classifies through wrapping (errors.Is), so the annotation must
// not depend on a direct type assertion.
func TestWithRefreshCommitAnnotatesWrappedRefreshError(t *testing.T) {
	failure := NewRefreshError(RefreshClassTerminal, 0, "invalid_grant", "", nil)
	wrapped := fmt.Errorf("refresh request: %w", failure)
	if !isTerminalRefresh(wrapped) {
		t.Fatalf("wrapped error does not classify terminal: %v", wrapped)
	}
	if got := withRefreshCommit(wrapped, RefreshCommitQuarantined); got != wrapped {
		t.Fatalf("withRefreshCommit returned %v, want the input error unchanged", got)
	}
	if failure.Commit != RefreshCommitQuarantined {
		t.Fatalf("commit = %q, want %q", failure.Commit, RefreshCommitQuarantined)
	}
}

func TestWithRefreshCommitAnnotatesDirectRefreshError(t *testing.T) {
	failure := NewRefreshError(RefreshClassTerminal, 0, "invalid_grant", "", nil)
	if got := withRefreshCommit(failure, RefreshCommitQuarantined); got != error(failure) {
		t.Fatalf("withRefreshCommit returned %v, want the input error unchanged", got)
	}
	if failure.Commit != RefreshCommitQuarantined {
		t.Fatalf("commit = %q, want %q", failure.Commit, RefreshCommitQuarantined)
	}
}

func TestWithRefreshCommitLeavesOtherFailuresUnchanged(t *testing.T) {
	other := errors.New("siwc: storage unavailable")
	wrapped := fmt.Errorf("refresh request: %w", other)
	if got := withRefreshCommit(wrapped, RefreshCommitQuarantined); got != wrapped {
		t.Fatalf("withRefreshCommit returned %v, want the input error unchanged", got)
	}
}
