package openailegacy

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/openai/openai-go/v3"
)

func FuzzHandleChatCompletionChunk(f *testing.F) {
	f.Add([]byte(`{"choices":[{"index":0,"delta":{"content":"x"}}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, 256<<10)
		var ch openai.ChatCompletionChunk
		if err := json.Unmarshal(raw, &ch); err != nil {
			return
		}
		s := &chatStream{}
		_ = s.handleChunk(ch)
	})
}
