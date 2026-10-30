package backendplugin_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestEventKind_ValidateRejectsUnknown(t *testing.T) {
	t.Parallel()
	for _, kind := range []backendplugin.EventKind{
		backendplugin.EventUnspecified,
		backendplugin.EventKind("evil_kind"),
		backendplugin.EventKind("text_delta\n"),
		backendplugin.EventKind("TEXT_DELTA"),
	} {
		if err := backendplugin.ValidateEventKind(kind); !errors.Is(err, backendplugin.ErrUnknownEventKind) {
			t.Fatalf("kind %q: want ErrUnknownEventKind, got %v", kind, err)
		}
	}
	if err := backendplugin.ValidateEventKind(backendplugin.EventTextDelta); err != nil {
		t.Fatal(err)
	}
}

func TestServerFrame_ValidateShapeRejectsUnknownEventKind(t *testing.T) {
	t.Parallel()
	err := (backendplugin.ServerFrame{
		Kind:  backendplugin.ServerFrameEvent,
		Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventKind("not_a_real_event")},
	}).ValidateShape()
	if !errors.Is(err, backendplugin.ErrUnknownEventKind) {
		t.Fatalf("got %v", err)
	}
}
