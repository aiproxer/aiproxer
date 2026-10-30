package openailegacy

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func FuzzBuildChatToolsParametersJSON(f *testing.F) {
	f.Add([]byte(`{"type":"object"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, aipapi.MaxToolParametersBytes)
		_, _ = buildChatTools([]aipapi.ToolDef{{Name: "fn", Parameters: raw}})
	})
}
