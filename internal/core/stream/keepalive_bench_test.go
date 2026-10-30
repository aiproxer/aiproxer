package stream_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// immediateStream yields an unbounded sequence of ready canonical events.
type immediateStream struct{ i int }

func (s *immediateStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	s.i++
	return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, nil
}

func (s *immediateStream) Close() error { return nil }

// BenchmarkKeepalive_recvReadyEvent measures one keepalive-wrapped Recv of an
// immediately available canonical event. That is the steady state of every
// streamed response: PumpSSE wraps the event stream with the recovery
// keepalive and calls Recv once per event. The idle interval is long enough
// that the keepalive timer never fires, so the measurement isolates the
// per-event wait setup rather than idle emission.
func BenchmarkKeepalive_recvReadyEvent(b *testing.B) {
	ka, err := stream.NewKeepalive(&immediateStream{}, stream.KeepaliveConfig{Interval: time.Minute})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = ka.Close() }()

	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ka.Recv(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
