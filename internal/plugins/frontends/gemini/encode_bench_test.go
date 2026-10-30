package gemini

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type benchGeminiTokenStream struct {
	n int
	i int
}

func (s *benchGeminiTokenStream) Recv(ctx context.Context) (aipapi.Event, error) {
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

func (s *benchGeminiTokenStream) Close() error { return nil }

// BenchmarkWriteStreamSSE_textDeltas measures one full WriteStreamSSE per iteration:
// new httptest.ResponseRecorder, WrapRecoveryKeepalive (goroutine + channels), n Recv calls,
// n SSE JSON flushes, and terminal handling. allocs/op is therefore much larger than
// "per text delta" alone; see BenchmarkGemini_flushTextDelta_only for the encode hot spot.
func BenchmarkWriteStreamSSE_textDeltas(b *testing.B) {
	const n = 256
	ctx := context.Background()
	call := &aipapi.Call{}
	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		es := &benchGeminiTokenStream{n: n}
		if err := WriteStreamSSE(ctx, rec, call, es, EncodeOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGemini_flushTextDelta_only measures only the pooled SSE+JSON encode for one
// text delta (reused scratch + recorder), excluding keepalive wrapper and stream Recv.
func BenchmarkGemini_flushTextDelta_only(b *testing.B) {
	rec := httptest.NewRecorder()
	var scratch gemStreamWireScratch
	scratch.initFrame()
	b.ReportAllocs()
	for b.Loop() {
		if err := scratch.flushTextDelta(rec, rec, "x"); err != nil {
			b.Fatal(err)
		}
		rec.Body.Reset()
	}
}
