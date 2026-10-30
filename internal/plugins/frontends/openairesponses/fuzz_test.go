package openairesponses_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

func FuzzDecodeCreateRequest(f *testing.F) {
	f.Add([]byte{0, '{', '}'}) // default selector, body {}
	f.Add(append([]byte{0}, []byte(`{"model":"gpt-4o-mini","input":"hello"}`)...))
	f.Add(append([]byte{10}, append([]byte("stub:custom"), []byte(`{"model":"x","input":"hi"}`)...)...))

	f.Fuzz(func(t *testing.T, packed []byte) {
		packed = testkit.CapBytes(packed, 256<<10)
		sel, body := testkit.SplitFuzzPackedRouteBody(packed, 64<<10, 256<<10)
		_, _ = openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
			RouteSelector: sel,
		})
	})
}
