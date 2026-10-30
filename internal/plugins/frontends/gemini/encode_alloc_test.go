package gemini

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

//nolint:paralleltest // allocation budget is sensitive to cross-test scheduling vs httptest/pool state
func TestWriteStreamSSE_AllocBudget_textOnly(t *testing.T) {
	const n = 200
	ctx := context.Background()
	call := &aipapi.Call{}
	allocs := testing.AllocsPerRun(5, func() {
		rec := httptest.NewRecorder()
		es := &benchGeminiTokenStream{n: n}
		if err := WriteStreamSSE(ctx, rec, call, es, EncodeOptions{}); err != nil {
			t.Fatal(err)
		}
	})
	// ~2k allocs per 200-delta stream after wire scratch reuse (see BenchmarkWriteStreamSSE_textDeltas -benchmem).
	const maxAllocs = 40_000
	if int(allocs) > maxAllocs {
		t.Fatalf("allocs per run=%g (n=%d deltas), want <= %d", allocs, n, maxAllocs)
	}
}
