package compatibleparity

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// DrainEvents reads a managed stream to completion (or first error).
func DrainEvents(t *testing.T, es aipapi.ManagedEventStream) ([]aipapi.Event, error) {
	t.Helper()
	return drainEventsWithContext(context.Background(), es)
}

func drainEventsWithContext(ctx context.Context, es aipapi.ManagedEventStream) ([]aipapi.Event, error) {
	defer func() { _ = es.Close() }()
	var out []aipapi.Event
	for {
		ev, err := es.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
}

// EventKinds returns the kind sequence for comparison.
func EventKinds(events []aipapi.Event) []aipapi.EventKind {
	out := make([]aipapi.EventKind, len(events))
	for i, ev := range events {
		out[i] = ev.Kind
	}
	return out
}

// AssertContainsKinds checks that wantKinds appear in order (not necessarily contiguous).
func AssertContainsKinds(t *testing.T, got []aipapi.EventKind, want []aipapi.EventKind) {
	t.Helper()
	i := 0
	for _, k := range got {
		if i < len(want) && k == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("kind sequence missing expected order\ngot:  %v\nwant (in order): %v", got, want)
	}
}

// AssertTerminalLast ensures ResponseFinished is the final lifecycle event when present.
func AssertTerminalLast(t *testing.T, events []aipapi.Event) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("expected events")
	}
	last := events[len(events)-1]
	if last.Kind != aipapi.EventResponseFinished && last.Kind != aipapi.EventError {
		t.Fatalf("terminal ordering: last kind = %v, want ResponseFinished or Error", last.Kind)
	}
	for i := range len(events) - 1 {
		if events[i].Kind == aipapi.EventResponseFinished {
			t.Fatalf("terminal ordering: ResponseFinished at index %d before final event", i)
		}
	}
}

// OpenAndCollect opens a backend for fx and drains events.
func OpenAndCollect(t *testing.T, be execbackend.Backend, fx Fixture) ([]aipapi.Event, error) {
	t.Helper()
	if fx.CancelAfterOpen {
		return openAndCancel(t, be, fx)
	}
	es, err := be.Open(context.Background(), fx.Call, CandidateFor(fx))
	if err != nil {
		return nil, err
	}
	return DrainEvents(t, es)
}

func openAndCancel(t *testing.T, be execbackend.Backend, fx Fixture) ([]aipapi.Event, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel while Open is still negotiating the first event.
	time.AfterFunc(25*time.Millisecond, cancel)
	es, err := be.Open(ctx, fx.Call, CandidateFor(fx))
	if err != nil {
		return nil, err
	}
	return drainEventsWithContext(ctx, es)
}

// TextFromEvents concatenates text deltas.
func TextFromEvents(events []aipapi.Event) string {
	var b []byte
	for _, ev := range events {
		if ev.Kind == aipapi.EventTextDelta {
			b = append(b, ev.Delta...)
		}
	}
	return string(b)
}

// ReasoningFromEvents concatenates reasoning deltas and reasoning-part payloads.
func ReasoningFromEvents(events []aipapi.Event) string {
	var b []byte
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventReasoningDelta:
			b = append(b, ev.Delta...)
		case aipapi.EventReasoningPart:
			if ev.Reasoning != nil {
				b = append(b, ev.Reasoning.Text...)
				b = append(b, ev.Reasoning.Opaque...)
			}
		}
	}
	return string(b)
}

// ToolNameFromEvents returns the first tool call name.
func ToolNameFromEvents(events []aipapi.Event) string {
	for _, ev := range events {
		if ev.Kind == aipapi.EventToolCallStarted {
			return ev.ToolName
		}
	}
	return ""
}
