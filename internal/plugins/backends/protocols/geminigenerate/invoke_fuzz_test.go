package geminigenerate

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func FuzzBuildToolsParametersJSON(f *testing.F) {
	f.Add([]byte(`{"type":"object"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, aipapi.MaxToolParametersBytes)
		_, _ = buildTools([]aipapi.ToolDef{{Name: "fn", Parameters: raw}})
	})
}

func FuzzMessageToContentToolResultJSON(f *testing.F) {
	f.Add([]byte(`{"ok":true}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, aipapi.MaxPartJSONBytes)
		m := aipapi.Message{
			Role: aipapi.RoleTool,
			Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: "id1",
				ToolName:   "n",
				Content:    raw,
			}},
		}
		_, _ = messageToContent(m)
	})
}
