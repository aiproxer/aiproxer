package aipapi_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestValidateEventEnvelope_deltaWithinLimit(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ok"}
	if err := aipapi.ValidateEventEnvelope(ev); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEventEnvelope_deltaTooLarge(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{
		Kind:  aipapi.EventTextDelta,
		Delta: strings.Repeat("x", aipapi.MaxEventDeltaBytes+1),
	}
	if err := aipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateEventEnvelope_assistantImageRequiresRef(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{Kind: aipapi.EventAssistantImageRef, AssistantRef: " "}
	if err := aipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateEventEnvelope_errorMessageTooLarge(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{
		Kind:         aipapi.EventError,
		ErrorCode:    "x",
		ErrorMessage: strings.Repeat("e", aipapi.MaxEventDiagMessageBytes+1),
	}
	if err := aipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateEventEnvelope_nilEventIsValidationError(t *testing.T) {
	t.Parallel()
	err := aipapi.ValidateEventEnvelope(nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := errors.AsType[*aipapi.ValidationError](err); !ok {
		t.Fatalf("expected ValidationError, got %T", err)
	}
	if !errors.Is(err, aipapi.ErrInvalidCall) {
		t.Fatal("expected error to unwrap to ErrInvalidCall")
	}
}
