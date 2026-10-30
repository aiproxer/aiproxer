package geminigenerate

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"google.golang.org/genai"
)

func FuzzHandleGenerateContentResponse(f *testing.F) {
	f.Add([]byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, 256<<10)
		var resp genai.GenerateContentResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return
		}
		s := &genaiStream{}
		_ = s.handleResponse(&resp)
	})
}
