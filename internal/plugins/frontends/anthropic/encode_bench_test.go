package anthropic

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type benchAnthropicTokenStream struct {
	n int
	i int
}

func (s *benchAnthropicTokenStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	if s.i < s.n {
		s.i++
		return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, nil
	}
	if s.i == s.n {
		s.i++
		return aipapi.Event{Kind: aipapi.EventResponseFinished}, nil
	}
	return aipapi.Event{}, io.EOF
}

func (s *benchAnthropicTokenStream) Close() error { return nil }

func BenchmarkWriteStreamSSE_textDeltas(b *testing.B) {
	const n = 256
	ctx := context.Background()
	call := &aipapi.Call{}
	opts := EncodeOptions{MessageID: "msg_bench"}
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		es := &benchAnthropicTokenStream{n: n}
		if err := WriteStreamSSE(ctx, rec, call, es, opts); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWriteStreamSSE_textDeltasRequestContext measures the same stream with the
// context a handler actually holds: one cancellable request context per streamed
// response, exactly as net/http supplies it. The stream wrapper observes that
// context on every event, so this is the shape in which per-event context
// bookkeeping is visible.
func BenchmarkWriteStreamSSE_textDeltasRequestContext(b *testing.B) {
	const n = 256
	call := &aipapi.Call{}
	opts := EncodeOptions{MessageID: "msg_bench"}
	b.ReportAllocs()
	for b.Loop() {
		ctx, cancel := context.WithCancel(context.Background())
		rec := httptest.NewRecorder()
		es := &benchAnthropicTokenStream{n: n}
		if err := WriteStreamSSE(ctx, rec, call, es, opts); err != nil {
			cancel()
			b.Fatal(err)
		}
		cancel()
	}
}
