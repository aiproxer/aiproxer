package anthropicmessages

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func FuzzToolInputSchemaParametersJSON(f *testing.F) {
	f.Add([]byte(`{"type":"object","properties":{}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, aipapi.MaxToolParametersBytes)
		_, _ = toolInputSchema(aipapi.ToolDef{Name: "fn", Parameters: raw})
	})
}
