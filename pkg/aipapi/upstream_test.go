package aipapi_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestRecoverablePreOutputError_nil(t *testing.T) {
	t.Parallel()
	if aipapi.RecoverablePreOutputError(nil) != nil {
		t.Fatal("expected nil")
	}
}

func TestRecoverablePreOutputError_preservesCauseChain(t *testing.T) {
	t.Parallel()
	original := errors.New("upstream reset")
	err := aipapi.RecoverablePreOutputError(original)
	if !errors.Is(err, aipapi.ErrRecoverablePreOutput) {
		t.Fatal("expected errors.Is ErrRecoverablePreOutput")
	}
	if !errors.Is(err, original) {
		t.Fatal("expected errors.Is original cause")
	}
}

func TestIsRecoverablePreOutput_sentinelWrapped(t *testing.T) {
	t.Parallel()
	err := aipapi.RecoverablePreOutputError(errors.New("timeout"))
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatal("expected recoverable")
	}
}

func TestIsRecoverablePreOutput_upstreamFailure(t *testing.T) {
	t.Parallel()
	err := &aipapi.UpstreamFailureError{
		Phase:       aipapi.PhasePreOutput,
		Recoverable: true,
		Reason:      "rate limit",
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatal("expected recoverable")
	}
	if !errors.Is(err, aipapi.ErrRecoverablePreOutput) {
		t.Fatal("expected errors.Is ErrRecoverablePreOutput")
	}
}

func TestIsRecoverablePreOutput_postOutputNotRecoverableForRetry(t *testing.T) {
	t.Parallel()
	err := &aipapi.UpstreamFailureError{
		Phase:       aipapi.PhasePostOutput,
		Recoverable: true,
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatal("post-output must not match pre-output recoverable predicate")
	}
	if errors.Is(err, aipapi.ErrRecoverablePreOutput) {
		t.Fatal("Unwrap must not expose sentinel for post-output")
	}
}
