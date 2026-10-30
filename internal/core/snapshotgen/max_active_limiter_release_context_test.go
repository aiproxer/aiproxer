package snapshotgen_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/runtimegen"
)

// errReleaseContextDropped marks a lease release handed a context without the
// caller's cleanup bound and values.
var errReleaseContextDropped = errors.New("limiter release received a context without the caller cleanup bound")

type releaseContextKey struct{}

// releaseContextProbe completes a release only when the caller's cleanup
// deadline reaches it, so it distinguishes a propagated cleanup context from a
// discarded one without sleeping.
type releaseContextProbe struct {
	stubConcurrencyProvider
}

func (p *releaseContextProbe) ReleaseLease(ctx context.Context, _ authority.LeaseRelease) error {
	if ctx == nil || ctx.Done() == nil || ctx.Value(releaseContextKey{}) != "cleanup" {
		return errReleaseContextDropped
	}
	<-ctx.Done()
	return ctx.Err()
}

// TestMaxActiveLimiter_ReleaseLeaseHonorsCallerCleanupContext pins the release
// boundary: the generation max-active limiter must pass the request
// coordinator's detached, deadline-bounded cleanup context through to the
// wrapped concurrency store instead of substituting context.Background().
func TestMaxActiveLimiter_ReleaseLeaseHonorsCallerCleanupContext(t *testing.T) {
	t.Parallel()

	probe := &releaseContextProbe{stubConcurrencyProvider: stubConcurrencyProvider{id: "release-context"}}
	exec, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID:                "static",
		Version:                 "release-context",
		State:                   economics.SnapshotReady,
		ConcurrencyRegistration: concReg("release-context", probe),
		MaxActiveRequests:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exec.RequestCoord == nil || exec.RequestCoord.Concurrency == nil {
		t.Fatal("generation must own a live request coordinator")
	}
	exec.RequestCoord.CleanupTimeout = 25 * time.Millisecond

	admission, err := exec.RequestCoord.Admit(context.Background(), remediationAdmit("req-release-context"))
	if err != nil || admission.Kind != authority.DecisionAllow || admission.Lease.LeaseID == "" {
		t.Fatalf("admit: err=%v decision=%+v", err, admission)
	}
	// The coordinator propagates caller values onto its detached cleanup
	// context; the probe also rejects a replaced context that only looks bounded.
	parent := context.WithValue(context.Background(), releaseContextKey{}, "cleanup")
	err = exec.RequestCoord.ReleaseLease(parent,
		admission.Lease.LeaseID, "req-release-context", "release-context-regression")

	if errors.Is(err, errReleaseContextDropped) {
		t.Fatalf("limiter dropped the caller cleanup context: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("release err=%v want context.DeadlineExceeded", err)
	}
}
