package responsestream

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/streampump"
)

// TestMapperDoneOnlyDualFinalNotificationsEmitFallbackArgsOnce pins the
// fallback-argument idempotency shared with the in-process Responses adapter:
// a done-only tool call closed by both final notifications must emit the
// complete arguments exactly once, never as a second delta after the finish.
func TestMapperDoneOnlyDualFinalNotificationsEmitFallbackArgsOnce(t *testing.T) {
	t.Parallel()
	q := streampump.NewPendingEventQueue(0)
	m := New(&q)
	if err := m.FinishToolCallArguments("fc_1", "attempt_completion", `{"result":"Done."}`); err != nil {
		t.Fatal(err)
	}
	if err := m.FinishToolCallArguments("fc_1", "attempt_completion", `{"result":"Done."}`); err != nil {
		t.Fatal(err)
	}
	events := streampump.DrainPending(&q)
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventToolCallStarted,
		aipapi.EventToolCallArgsDelta,
		aipapi.EventToolCallFinished,
	}
	if len(events) != len(want) {
		t.Fatalf("event count = %d, want %d", len(events), len(want))
	}
	for i, kind := range want {
		if events[i].Kind != kind {
			t.Fatalf("events[%d] = %v, want %v", i, events[i].Kind, kind)
		}
	}
	if got := events[3].Delta; got != `{"result":"Done."}` {
		t.Fatalf("fallback args delta = %q, want complete arguments", got)
	}
}
