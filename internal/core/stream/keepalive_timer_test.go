package stream

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// readyStream yields ready canonical events without delay.
type readyStream struct{}

func (readyStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, nil
}

func (readyStream) Close() error { return nil }

// TestKeepalive_reusesOneIdleTimerAcrossRecv pins the per-stream idle timer
// reuse. A streamed response calls Recv once per canonical event, so a timer
// allocated per call would put a timer allocation and a runtime timer
// registration on the per-event path (see BenchmarkKeepalive_recvReadyEvent).
// Keepalive emission itself stays covered by TestKeepalive_emitsDuringIdle.
func TestKeepalive_reusesOneIdleTimerAcrossRecv(t *testing.T) {
	t.Parallel()

	ka, err := NewKeepalive(readyStream{}, KeepaliveConfig{Interval: time.Hour})
	if err != nil {
		t.Fatalf("NewKeepalive: %v", err)
	}
	defer func() { _ = ka.Close() }()

	ctx := context.Background()
	if _, err := ka.Recv(ctx); err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	timer := ka.idleTimer
	if timer == nil {
		t.Fatal("first Recv did not arm the idle timer")
	}
	for call := 2; call <= 3; call++ {
		if _, err := ka.Recv(ctx); err != nil {
			t.Fatalf("Recv %d: %v", call, err)
		}
		if ka.idleTimer != timer {
			t.Fatalf("Recv %d allocated a new idle timer; want one reused timer per stream", call)
		}
	}
}
