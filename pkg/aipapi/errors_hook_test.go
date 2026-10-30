package aipapi_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestIsHookMutation(t *testing.T) {
	t.Parallel()
	err := &aipapi.HookMutationError{HookID: "h1", Details: "bad part", Cause: aipapi.ErrInvalidCall}
	if !aipapi.IsHookMutation(err) {
		t.Fatal("expected IsHookMutation true")
	}
	if !aipapi.IsHookMutation(errors.Join(aipapi.ErrHookMutation, err)) {
		t.Fatal("expected wrapped mutation")
	}
	if aipapi.IsHookMutation(errors.New("other")) {
		t.Fatal("expected false")
	}
}
