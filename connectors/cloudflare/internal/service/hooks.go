package service

import (
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// ProviderHooks returns openaicompat request hooks for Cloudflare AI Gateway.
// When gateway_id is configured, cf-aig-gateway-id header is injected.
func ProviderHooks(cfg Config) openaicompat.RequestHooks {
	hooks := openaicompat.RequestHooks{}
	if cfg.GatewayID != "" {
		hooks.ExtraHeaders = make(http.Header)
		hooks.ExtraHeaders.Set("cf-aig-gateway-id", cfg.GatewayID)
	}
	return hooks
}

// ResolveFlavor selects the endpoint flavor: Responses preferred.
// Ambiguous or empty operations default to Responses; only explicit chat completions uses chat.
func ResolveFlavor(call aipapi.Call) openaicompat.Flavor {
	if call.Invocation.Operation == aipapi.OperationOpenAIChatCompletions {
		return openaicompat.FlavorChat
	}
	return openaicompat.FlavorResponses
}

func resolveModel(kind string, inv backendplugin.Invocation, _ aipapi.Call) string {
	return strings.TrimPrefix(strings.TrimSpace(inv.CanonicalModelID), kind+"/")
}

// isResponsesCapableModel filters inventory to Responses-capable text/coding models.
// It drops embedding, rerank, image, audio, and empty/whitespace model IDs.
func isResponsesCapableModel(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	lower := strings.ToLower(id)
	dropPatterns := []string{
		"embed",
		"bge",
		"rerank",
		"image",
		"flux",
		"diffusion",
		"dall-e",
		"whisper",
		"audio",
		"tts",
		"speech",
		"music",
		"asr",
		"video",
	}
	for _, pattern := range dropPatterns {
		if strings.Contains(lower, pattern) {
			return false
		}
	}
	return true
}
