package modelregistry

import (
	"errors"
	"testing"
)

func TestRefreshFailureError_nilErr(t *testing.T) {
	t.Parallel()

	var e refreshFailureError
	if got := e.Error(); got != "modelregistry: refresh failure" {
		t.Fatalf("Error() = %q, want modelregistry: refresh failure", got)
	}
	if e.Unwrap() != nil {
		t.Fatalf("Unwrap() = %v, want nil", e.Unwrap())
	}

	wrapped := refreshFailureError{category: RefreshFailureFetch, err: errors.New("boom")}
	if got := wrapped.Error(); got != "boom" {
		t.Fatalf("Error() = %q, want boom", got)
	}
	if !errors.Is(wrapped, wrapped.err) {
		t.Fatalf("errors.Is failed for wrapped err")
	}
}
