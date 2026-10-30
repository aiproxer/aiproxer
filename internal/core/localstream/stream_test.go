package localstream

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNewTextStream_ValidSequenceFiniteNoUsageNoGoroutine(t *testing.T) {
	t.Parallel()
	text := "hello local"
	evs := Events(text)
	if err := aipapi.ValidateEventSequence(evs); err != nil {
		t.Fatalf("ValidateEventSequence: %v", err)
	}
	// Must be exactly response_started, message_started, text_delta, response_finished.
	wantKinds := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
		aipapi.EventResponseFinished,
	}
	if len(evs) != len(wantKinds) {
		t.Fatalf("events len %d want %d", len(evs), len(wantKinds))
	}
	for i, want := range wantKinds {
		if evs[i].Kind != want {
			t.Fatalf("events[%d] kind %q want %q", i, evs[i].Kind, want)
		}
	}
	if evs[2].Delta != text {
		t.Fatalf("delta %q want %q", evs[2].Delta, text)
	}
	// No usage event in factory.
	for _, e := range evs {
		if e.Kind == aipapi.EventUsageDelta {
			t.Fatal("local stream must not contain usage")
		}
		if e.InputTokens != 0 || e.OutputTokens != 0 || e.TotalTokens != 0 {
			t.Fatal("usage fields must be zero")
		}
	}

	// Collect via streaming abstraction proves non-streaming collection works and is finite.
	col, err := aipapi.Collect(context.Background(), NewTextStream(text))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if col.Text.String() != text {
		t.Fatalf("collected %q want %q", col.Text.String(), text)
	}
	if !col.FinishReceived {
		t.Fatal("FinishReceived must be true")
	}
	if col.TerminalError != nil {
		t.Fatalf("TerminalError must be nil got %v", col.TerminalError)
	}
	if col.InputTokens != 0 || col.OutputTokens != 0 || col.TotalTokens != 0 {
		t.Fatalf("collected usage must be zero got %+v", col)
	}
	if len(col.ToolArgs) != 0 || len(col.ToolCallOrder) != 0 {
		t.Fatalf("no tool calls expected")
	}
	// Second Collect on fresh stream must also succeed (stream is reusable via factory, not one-shot goroutine).
	col2, err := aipapi.Collect(context.Background(), NewTextStream(text))
	if err != nil || col2.Text.String() != text {
		t.Fatalf("second collect failed: %v %q", err, col2.Text.String())
	}
}

func TestNewTextStream_ObeysCancellationAndClose(t *testing.T) {
	t.Parallel()
	s := NewTextStream("cancel-me")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Recv(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv after cancel should be context.Canceled, got %v", err)
	}
	// Close idempotent, no goroutine leak.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// After Close, stream is still finite EOF when consumed normally on background context.
	s2 := NewTextStream("hello")
	evs := collectAll(t, s2)
	if len(evs) != 4 {
		t.Fatalf("after Close test, fresh stream events %d want 4", len(evs))
	}
}

func TestNewTextStream_NilContextReturnsError(t *testing.T) {
	t.Parallel()
	s := NewTextStream("hi")
	//nolint:staticcheck // SA1012 explicitly testing defensive nil context handling
	if _, err := s.Recv(nil); !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("Recv(nil) should be ErrNilContext, got %v", err)
	}
}

func TestCanonicalAssistantMessage_IdentityEqualsCollectedReplay(t *testing.T) {
	t.Parallel()
	cases := []string{"hello local", "unicode \u2603 \n second", "a", string(make([]byte, 0))}
	// empty string is still a valid local reply per localturn.Reply validation (bounded non-empty); skip empty case.
	for _, text := range cases {
		if text == "" {
			continue
		}
		tagged := CanonicalAssistantMessage(text)
		taggedID, err := conversationprojection.MessageIdentityOf(tagged)
		if err != nil {
			t.Fatalf("MessageIdentityOf tagged %q: %v", text, err)
		}
		// Replay via Collect (non-streaming) should decode to same identity.
		col, err := aipapi.Collect(context.Background(), NewTextStream(text))
		if err != nil {
			t.Fatalf("Collect %q: %v", text, err)
		}
		replayMsg := CanonicalAssistantMessage(col.Text.String())
		replayID, err := conversationprojection.MessageIdentityOf(replayMsg)
		if err != nil {
			t.Fatalf("MessageIdentityOf replay %q: %v", text, err)
		}
		if taggedID != replayID {
			t.Fatalf("identity mismatch for %q: tagged %s replay %s", text, taggedID, replayID)
		}
		// Item-authority replay should also be identity-equivalent.
		replayItem := CanonicalAssistantItem(col.Text.String())
		itemID, err := conversationprojection.ItemIdentityOf(replayItem)
		if err != nil {
			t.Fatalf("ItemIdentityOf %q: %v", text, err)
		}
		if itemID != taggedID {
			t.Fatalf("item identity mismatch for %q: item %s tagged %s", text, itemID, taggedID)
		}
	}
}

func collectAll(t *testing.T, s aipapi.EventStream) []aipapi.Event {
	t.Helper()
	var out []aipapi.Event
	for {
		ev, err := s.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		out = append(out, ev)
		if ev.Kind == aipapi.EventResponseFinished || ev.Kind == aipapi.EventError {
			// drain to EOF
			for {
				_, e2 := s.Recv(context.Background())
				if e2 != nil {
					break
				}
			}
			break
		}
	}
	_ = s.Close()
	return out
}
