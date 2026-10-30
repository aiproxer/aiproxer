package reasoningpreservation_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

const streamObserverAccumulationConfig = `
action: restore
use_builtin_catalog: false
rules:
  - id: accumulate-be
    backend: be
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1048576
  max_session_bytes: 2097152
`

func newAccumulationFactory(tb testing.TB) (*reasoningpreservation.StreamObserverFactory, reasoningpreservation.TurnStore) {
	tb.Helper()
	store, err := reasoningpreservation.NewMemoryTurnStore(reasoningpreservation.StoreOptions{
		TTL:                      time.Hour,
		MaxTurnsPerSession:       4,
		MaxReasoningBytesPerTurn: 1 << 20,
		MaxSessionBytes:          1 << 21,
		Now:                      time.Now,
	})
	if err != nil {
		tb.Fatalf("NewMemoryTurnStore: %v", err)
	}
	return reasoningpreservation.NewStreamObserverFactory(decodeValidConfig(tb, streamObserverAccumulationConfig), store), store
}

func accumulationMeta(sessionID string) response.StreamMeta {
	return response.StreamMeta{
		BackendID: "be",
		Model:     "model-accumulate",
		Session:   session.SessionView{AuthoritativeSessionID: sessionID},
	}
}

func repeatEvents(n int, ev aipapi.Event) []aipapi.Event {
	out := make([]aipapi.Event, n)
	for i := range out {
		out[i] = ev
	}
	return out
}

// TestStreamObserver_manyDeltasMatchSingleEvent proves that splitting a
// contiguous text and reasoning segment into many small streamed deltas yields
// the same artifact as delivering the same payload in one event: same anchor,
// text, and signature.
func TestStreamObserver_manyDeltasMatchSingleEvent(t *testing.T) {
	t.Parallel()
	const (
		textChunk      = "abcdefgh"
		reasoningChunk = "plan-"
		signatureChunk = "sig-"
		textRepeats    = 256
		reasonRepeats  = 64
		sigRepeats     = 8
	)
	streamed := append(repeatEvents(textRepeats, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: textChunk}),
		repeatEvents(reasonRepeats, aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: reasoningChunk})...)
	streamed = append(streamed, repeatEvents(sigRepeats, aipapi.Event{Kind: aipapi.EventReasoningSignatureDelta, Signature: signatureChunk})...)
	single := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: strings.Repeat(textChunk, textRepeats)},
		{Kind: aipapi.EventReasoningDelta, Delta: strings.Repeat(reasoningChunk, reasonRepeats)},
		{Kind: aipapi.EventReasoningSignatureDelta, Signature: strings.Repeat(signatureChunk, sigRepeats)},
	}

	factory, store := newAccumulationFactory(t)
	runTurn := func(sessionID string, events []aipapi.Event) reasoningpreservation.TurnArtifact {
		t.Helper()
		obs, err := factory.Open(context.Background(), accumulationMeta(sessionID), response.Services{})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		for _, ev := range events {
			if err := obs.Observe(context.Background(), ev); err != nil {
				t.Fatalf("Observe: %v", err)
			}
		}
		if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition(sessionID))
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(snap) != 1 || len(snap[0].Reasoning) != 1 || snap[0].Reasoning[0].Part.Reasoning == nil {
			t.Fatalf("want one artifact with one reasoning part, got %d artifacts", len(snap))
		}
		return snap[0]
	}

	gotStreamed := runTurn("sess-many-deltas", streamed)
	gotSingle := runTurn("sess-single-event", single)
	if gotStreamed.Anchor != gotSingle.Anchor {
		t.Fatal("anchor mismatch between many-delta and single-event turns")
	}
	part, want := gotStreamed.Reasoning[0].Part.Reasoning, gotSingle.Reasoning[0].Part.Reasoning
	if part.Text != want.Text || part.Signature != want.Signature {
		t.Fatal("many-delta reasoning payload differs from the single-event payload")
	}
}

// BenchmarkStreamObserverContiguousDeltas measures one contiguous text or
// reasoning segment accumulated from many small deltas, the shape produced by
// token streaming. A per-delta copy of the growing segment shows up here as
// quadratic bytes/op and ns/op.
func BenchmarkStreamObserverContiguousDeltas(b *testing.B) {
	cases := []struct {
		name string
		kind aipapi.EventKind
		n    int
	}{
		{"text", aipapi.EventTextDelta, 4096},
		{"reasoning", aipapi.EventReasoningDelta, 2048},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			factory, _ := newAccumulationFactory(b)
			events := repeatEvents(tc.n, aipapi.Event{Kind: tc.kind, Delta: "abcdefgh"})
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				obs, err := factory.Open(ctx, accumulationMeta("sess-bench"), response.Services{})
				if err != nil {
					b.Fatalf("Open: %v", err)
				}
				for i := range events {
					if err := obs.Observe(ctx, events[i]); err != nil {
						b.Fatalf("Observe: %v", err)
					}
				}
			}
		})
	}
}
