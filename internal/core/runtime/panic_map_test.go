package runtime

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func mustPanicError(t *testing.T) *safety.PanicError {
	t.Helper()
	_, err := safety.CallValue(safety.BoundaryBackend, "test_op", func() (int, error) {
		panic(struct{ x int }{1})
	})
	if err == nil {
		t.Fatal("expected panic error")
	}
	var pe *safety.PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("want *safety.PanicError, got %T", err)
	}
	return pe
}

func TestMapBackendPanic_postOutputNeverRecoverable(t *testing.T) {
	t.Parallel()
	pe := mustPanicError(t)
	err := mapBackendPanic(pe, true, "cand:key")
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatal("committed output panic must not be recoverable pre-output")
	}
	var uf *aipapi.UpstreamFailureError
	if !errors.As(err, &uf) {
		t.Fatalf("want UpstreamFailureError, got %T", err)
	}
	if uf.Phase != aipapi.PhasePostOutput || uf.Recoverable {
		t.Fatalf("upstream: phase=%s recoverable=%v", uf.Phase, uf.Recoverable)
	}
	if uf.CandidateKey != "cand:key" {
		t.Fatalf("candidate key: %q", uf.CandidateKey)
	}
}

func TestMapBackendPanic_preOutputRecoverable(t *testing.T) {
	t.Parallel()
	pe := mustPanicError(t)
	err := mapBackendPanic(pe, false, "k1")
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatal("pre-output panic must map to recoverable pre-output")
	}
	if !errors.Is(err, aipapi.ErrRecoverablePreOutput) {
		t.Fatal("expected sentinel in chain")
	}
}

func TestMapStreamPanic_postOutputNotRecoverable(t *testing.T) {
	t.Parallel()
	pe := mustPanicError(t)
	err := mapStreamPanic(pe, true)
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatal("committed stream panic must not be recoverable pre-output")
	}
	var uf *aipapi.UpstreamFailureError
	if !errors.As(err, &uf) || uf.Phase != aipapi.PhasePostOutput {
		t.Fatalf("got %v", err)
	}
}

func TestMapStreamPanic_preOutputRecoverable(t *testing.T) {
	t.Parallel()
	pe := mustPanicError(t)
	err := mapStreamPanic(pe, false)
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatal("pre-output stream panic must be recoverable")
	}
}
