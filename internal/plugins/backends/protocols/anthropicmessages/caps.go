package anthropicmessages

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ModelCapabilities returns negotiated backend caps for a resolved Anthropic model id.
// Rules are intentionally small; expand as catalog data grows.
func ModelCapabilities(model string) aipapi.BackendCaps {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return defaultBackendCaps()
	}
	// Legacy Claude 2 / Instant surfaces had weaker parallel tool-call ergonomics.
	if strings.Contains(m, "claude-2") || strings.Contains(m, "claude-v2") || strings.Contains(m, "claude-instant-1") {
		out := aipapi.NewBackendCaps()
		for c := range defaultBackendCaps() {
			if c == aipapi.CapabilityParallelToolCalls {
				continue
			}
			out[c] = struct{}{}
		}
		return out
	}
	return defaultBackendCaps()
}
