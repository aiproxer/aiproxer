package openailegacy

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Guardrail: streaming encode should not regress to per-chunk map+json.Marshal on the hot path.
//
//nolint:paralleltest // allocation budget is sensitive to cross-test scheduling vs httptest/pool state
func TestWriteStreamSSE_AllocBudget_textOnly(t *testing.T) {
	const n = 200
	ctx := context.Background()
	call := &aipapi.Call{}
	opts := EncodeOptions{CompletionID: "chatcmpl_alloc", CreatedAt: 1}
	allocs := testing.AllocsPerRun(5, func() {
		rec := httptest.NewRecorder()
		es := &benchTokenStream{n: n}
		if err := WriteStreamSSE(ctx, rec, call, es, opts); err != nil {
			t.Fatal(err)
		}
	})
	// Baseline (Linux/macOS/WSL typical): ~1.9k–2.5k allocs/run for n=200 (scaled from BenchmarkWriteStreamSSE_textDeltas @ n=256 ~2345 allocs/op).
	// Cap stays above that with room for recorder variance across OS and parallel load.
	const maxAllocs = 5500
	if int(allocs) > maxAllocs {
		t.Fatalf("allocs per run=%g (n=%d deltas), want <= %d", allocs, n, maxAllocs)
	}
}
