package aipapi

import (
	"errors"
	"testing"
)

func TestStreamError_Error_stable(t *testing.T) {
	t.Parallel()
	err := NewStreamError("code_x", "very long dynamic message")
	if got := err.Error(); got != ErrStreamTerminal.Error() {
		t.Fatalf("Error() = %q want %q", got, ErrStreamTerminal.Error())
	}
	var se *StreamError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As: got %T", err)
	}
	if se.Code != "code_x" || se.Message != "very long dynamic message" {
		t.Fatalf("fields %+v", se)
	}
	if !errors.Is(err, ErrStreamTerminal) {
		t.Fatal("errors.Is(ErrStreamTerminal) = false")
	}
}
