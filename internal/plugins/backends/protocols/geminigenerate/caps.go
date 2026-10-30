package geminigenerate

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ModelCapabilities returns negotiated backend caps for a resolved Gemini model id.
func ModelCapabilities(model string) aipapi.BackendCaps {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return defaultBackendCaps()
	}
	// Early Gemini 1.0 Pro text models were not multimodal on the same surface as 1.5+.
	if strings.Contains(m, "gemini-1.0-pro") && !strings.Contains(m, "vision") {
		out := aipapi.NewBackendCaps()
		for c := range defaultBackendCaps() {
			if c == aipapi.CapabilityVision || c == aipapi.CapabilityDocuments {
				continue
			}
			out[c] = struct{}{}
		}
		return out
	}
	return defaultBackendCaps()
}
