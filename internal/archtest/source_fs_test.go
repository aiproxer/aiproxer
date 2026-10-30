package archtest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestLoadGitCommitFSContext_CancelledLoadDoesNotPoisonCache proves that a load
// cancelled while git archive is running removes its cache entry, so a later
// live-context load of the same commit succeeds. The pre-cancelled path returns
// before an entry exists; only a cancellation that lands during the archive
// exercises the cleanup in loadGitCommitFSContext's defer, which the pinned-ref
// ownership gates depend on when a caller gives up and retries.
func TestLoadGitCommitFSContext_CancelledLoadDoesNotPoisonCache(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	// Resolve the full SHA: the cache is keyed by root+ref and other tests load
	// "HEAD", so sharing that key would let a warm entry answer this call.
	raw, err := runCmd(root, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(raw))
	if sha == "" {
		t.Fatal("git rev-parse HEAD returned no revision")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := loadGitCommitFSContext(ctx, root, sha); err == nil {
		t.Fatal("loadGitCommitFSContext with an expired context succeeded")
	} else if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("loadGitCommitFSContext error = %v, want deadline/cancel", err)
	}
	fs, err := loadGitCommitFSContext(t.Context(), root, sha)
	if err != nil {
		t.Fatalf("cancelled load poisoned the cache: %v", err)
	}
	if len(fs.files) == 0 {
		t.Fatal("reloaded gitCommitFS has no files")
	}
}
