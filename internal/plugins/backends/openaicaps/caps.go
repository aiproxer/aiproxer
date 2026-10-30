// Package openaicaps holds small, shared capability rules for OpenAI-hosted backends.
package openaicaps

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// HostedFull is the default capability surface for modern OpenAI Chat and Responses routes.
var HostedFull = aipapi.NewBackendCaps(
	aipapi.CapabilityStreaming,
	aipapi.CapabilityTools,
	aipapi.CapabilityVision,
	aipapi.CapabilityDocuments,
	aipapi.CapabilityReasoning,
	aipapi.CapabilityParallelToolCalls,
)

// ForHostedModel applies conservative, model-id-based capability narrowing.
// Rules are intentionally small and explicit; expand here as catalog data grows.
func ForHostedModel(model string) aipapi.BackendCaps {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return HostedFull
	}
	// gpt-3.5 Chat models do not accept multimodal image/file inputs on the hosted API.
	if strings.Contains(m, "gpt-3.5") {
		out := aipapi.NewBackendCaps()
		for c := range HostedFull {
			if c == aipapi.CapabilityVision || c == aipapi.CapabilityDocuments {
				continue
			}
			out[c] = struct{}{}
		}
		return out
	}
	// o-series hosted models use a conservative tool/vision matrix in this catalog.
	if strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") {
		out := aipapi.NewBackendCaps()
		for c := range HostedFull {
			if c == aipapi.CapabilityParallelToolCalls {
				continue
			}
			out[c] = struct{}{}
		}
		return out
	}
	return HostedFull
}
