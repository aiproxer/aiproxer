package gemini_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

func FuzzDecodeGenerateContentRequest(f *testing.F) {
	f.Add([]byte{0, '{', '}'})
	f.Add(append([]byte{0}, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)...))

	f.Fuzz(func(t *testing.T, packed []byte) {
		packed = testkit.CapBytes(packed, 256<<10)
		sel, body := testkit.SplitFuzzPackedRouteBody(packed, 64<<10, 256<<10)
		_, _ = gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
			RouteSelector: sel,
			Model:         "gemini-2.0-flash",
		})
	})
}
