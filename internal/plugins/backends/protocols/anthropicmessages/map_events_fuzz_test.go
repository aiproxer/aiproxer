package anthropicmessages

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/anthropics/anthropic-sdk-go"
)

func FuzzHandleMessageStreamEventUnion(f *testing.F) {
	f.Add([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, 256<<10)
		var u anthropic.MessageStreamEventUnion
		if err := json.Unmarshal(raw, &u); err != nil {
			return
		}
		s := &msgStream{}
		_ = s.handleEvent(u)
	})
}
