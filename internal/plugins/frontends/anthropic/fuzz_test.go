package anthropic_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

func FuzzDecodeMessageRequest(f *testing.F) {
	f.Add([]byte{0, '{', '}'})
	f.Add(append([]byte{0}, []byte(`{"model":"claude-3-5-haiku-20241022","max_tokens":10,"messages":[{"role":"user","content":"x"}]}`)...))

	f.Fuzz(func(t *testing.T, packed []byte) {
		packed = testkit.CapBytes(packed, 256<<10)
		sel, body := testkit.SplitFuzzPackedRouteBody(packed, 64<<10, 256<<10)
		_, _ = anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{RouteSelector: sel})
	})
}
