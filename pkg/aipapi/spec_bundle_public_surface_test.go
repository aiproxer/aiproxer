package aipapi_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Black-box regression anchors for exported error and helper surfaces (specification bundle).
func TestPublicStreamErrorUnwrapsTerminalRoot(t *testing.T) {
	t.Parallel()
	err := aipapi.NewStreamError("x", "y")
	if !errors.Is(err, aipapi.ErrStreamTerminal) {
		t.Fatalf("NewStreamError must unwrap to ErrStreamTerminal")
	}
	var se *aipapi.StreamError
	if !errors.As(err, &se) || se.Code != "x" || se.Message != "y" {
		t.Fatalf("As StreamError: %#v", err)
	}
	if got := err.Error(); got != aipapi.ErrStreamTerminal.Error() {
		t.Fatalf("StreamError.Error() = %q want stable root text %q", got, aipapi.ErrStreamTerminal.Error())
	}
}

func TestPublicSentinelRootsAreDistinct(t *testing.T) {
	t.Parallel()
	roots := []error{
		aipapi.ErrInvalidCall,
		aipapi.ErrCollectLimitExceeded,
		aipapi.ErrNilEventStream,
		aipapi.ErrNilContext,
		aipapi.ErrNilFixedEventStream,
		aipapi.ErrStreamTerminal,
		aipapi.ErrMaxRouteAttempts,
		aipapi.ErrUnresolvedModelOnlySelector,
		aipapi.ErrAllCandidatesContextLimitExceeded,
		aipapi.ErrCapabilityReject,
		aipapi.ErrHookMutation,
	}
	seen := map[string]struct{}{}
	for _, e := range roots {
		s := e.Error()
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate sentinel message %q", s)
		}
		seen[s] = struct{}{}
	}
}

func TestPublicNegotiationRejectHelpers(t *testing.T) {
	t.Parallel()
	rej := &aipapi.RejectError{Reason: "nope"}
	if !errors.Is(rej, aipapi.ErrCapabilityReject) {
		t.Fatal("RejectError must unwrap ErrCapabilityReject")
	}
	if !aipapi.IsReject(rej) {
		t.Fatal("IsReject must recognize RejectError")
	}
	if aipapi.IsReject(nil) {
		t.Fatal("IsReject(nil) must be false")
	}
}

func TestPublicHookMutationHelpers(t *testing.T) {
	t.Parallel()
	hm := &aipapi.HookMutationError{Cause: aipapi.ErrHookMutation}
	if !aipapi.IsHookMutation(hm) {
		t.Fatal("IsHookMutation must recognize HookMutationError with ErrHookMutation cause")
	}
	if !aipapi.IsHookMutation(aipapi.ErrHookMutation) {
		t.Fatal("IsHookMutation must recognize ErrHookMutation")
	}
	if aipapi.IsHookMutation(nil) {
		t.Fatal("IsHookMutation(nil) must be false")
	}
}

func TestPublicBackendCapsConstruction(t *testing.T) {
	t.Parallel()
	c := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	if len(c) != 2 {
		t.Fatalf("expected two capabilities, got %d", len(c))
	}
	if _, ok := c[aipapi.CapabilityStreaming]; !ok {
		t.Fatal("missing streaming")
	}
	if _, ok := c[aipapi.CapabilityTools]; !ok {
		t.Fatal("missing tools")
	}
}
