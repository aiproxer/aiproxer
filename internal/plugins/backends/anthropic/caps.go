package anthropic

import (
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/anthropicmessages"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ModelCapabilities returns negotiated backend caps for a resolved Anthropic model id.
// Rules are intentionally small; expand as catalog data grows.
func ModelCapabilities(model string) aipapi.BackendCaps {
	return anthropicmessages.ModelCapabilities(model)
}
