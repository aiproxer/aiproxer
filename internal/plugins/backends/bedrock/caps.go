package bedrock

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ModelCapabilities returns negotiated backend caps for a Bedrock foundation model id / ARN.
func ModelCapabilities(modelID string) aipapi.BackendCaps {
	low := strings.ToLower(strings.TrimSpace(modelID))
	if low == "" {
		return defaultBackendCaps()
	}
	// Anthropic-on-Bedrock legacy ids mirror Anthropic-hosted narrowing.
	if strings.Contains(low, "anthropic.") &&
		(strings.Contains(low, "claude-2") || strings.Contains(low, "claude-v2") || strings.Contains(low, "claude-instant")) {
		out := aipapi.NewBackendCaps()
		for c := range defaultBackendCaps() {
			if c == aipapi.CapabilityParallelToolCalls {
				continue
			}
			out[c] = struct{}{}
		}
		return out
	}
	// Titan Text is a text-generation family without tools/vision in ConverseStream use cases here.
	if strings.Contains(low, "amazon.titan-text") {
		return aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	}
	return defaultBackendCaps()
}
