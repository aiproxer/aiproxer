package gemini

import (
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/geminigenerate"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ModelCapabilities returns negotiated backend caps for a resolved Gemini model id.
func ModelCapabilities(model string) aipapi.BackendCaps {
	return geminigenerate.ModelCapabilities(model)
}
