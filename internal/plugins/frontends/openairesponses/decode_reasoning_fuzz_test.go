package openairesponses

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
)

func FuzzDecodeCreateRequest_reasoningItems(f *testing.F) {
	f.Add([]byte(`{"model":"gpt-4o-mini","input":[{"type":"reasoning","id":"rs_1","summary":[]}]}`))
	f.Add([]byte(`{"model":"gpt-4o-mini","input":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"s"}],"encrypted_content":null}]}`))
	f.Add([]byte(`{"model":"m","input":[{"type":"reasoning","id":"","summary":[]}]}`))
	f.Add([]byte(`{"model":"m","input":[{"type":"reasoning","id":"rs_1","summary":[],"extra":1}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, 64<<10)
		_, _ = DecodeCreateRequest(raw, DecodeOptions{RouteSelector: "stub:m"})
	})
}
