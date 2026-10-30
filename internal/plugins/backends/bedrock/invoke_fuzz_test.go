package bedrock

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func FuzzAssistantPartsToContentBlocksJSON(f *testing.F) {
	f.Add([]byte(`{"tool_use_id":"t","name":"fn","input":{}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, aipapi.MaxPartJSONBytes)
		_, _ = assistantPartsToContentBlocks([]aipapi.Part{{
			Kind:    aipapi.PartJSON,
			Content: raw,
		}})
	})
}
