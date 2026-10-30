package hooks

import (
	"errors"
	"testing"
)

func TestIsSubmitReject(t *testing.T) {
	t.Parallel()
	err := &SubmitRejectError{HookID: "h1", Reason: "blocked"}
	if !IsSubmitReject(err) {
		t.Fatal("expected IsSubmitReject true")
	}
	if !errors.Is(err, ErrSubmitRejected) {
		t.Fatal("expected SubmitRejectError to unwrap to ErrSubmitRejected")
	}
	if IsSubmitReject(errors.New("other")) {
		t.Fatal("expected false")
	}
}

func TestErrSubmitRejectedPrefix(t *testing.T) {
	t.Parallel()
	if got := ErrSubmitRejected.Error(); got != "aipsdk/hooks: submit hook rejected request" {
		t.Fatalf("ErrSubmitRejected = %q", got)
	}
}
