package openairesponses

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/openai/openai-go/v3/responses"
)

func FuzzHandleResponseStreamUnion(f *testing.F) {
	f.Add([]byte(`{"type":"response.output_text.delta","delta":"x"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, 256<<10)
		var u responses.ResponseStreamEventUnion
		if err := json.Unmarshal(raw, &u); err != nil {
			return
		}
		s := &sdkStream{}
		_ = s.handleUnion(u)
	})
}
