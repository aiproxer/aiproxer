package anthropic

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
	opts := EncodeOptions{MessageID: "msg_alloc"}
	allocs := testing.AllocsPerRun(5, func() {
		rec := httptest.NewRecorder()
		es := &benchAnthropicTokenStream{n: n}
		if err := WriteStreamSSE(ctx, rec, call, es, opts); err != nil {
			t.Fatal(err)
		}
	})
	const maxAllocs = 150_000
	if int(allocs) > maxAllocs {
		t.Fatalf("allocs per run=%g (n=%d deltas), want <= %d", allocs, n, maxAllocs)
	}
}
