package service

import (
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// ProviderHooks returns openaicompat request hooks for Infomaniak AI.
func ProviderHooks(_ Config) openaicompat.RequestHooks {
	return openaicompat.RequestHooks{}
}

// ResolveFlavor selects the endpoint flavor: Chat is default and preferred on Infomaniak.
// Ambiguous or empty operations default to Chat; explicit Responses operations use Responses.
func ResolveFlavor(call aipapi.Call) openaicompat.Flavor {
	if call.Invocation.Operation == aipapi.OperationOpenAIResponses {
		return openaicompat.FlavorResponses
	}
	return openaicompat.FlavorChat
}

func resolveModel(kind string, inv backendplugin.Invocation, _ aipapi.Call) string {
	m := strings.TrimSpace(inv.CanonicalModelID)
	if m == kind {
		return ""
	}
	return strings.TrimPrefix(m, kind+"/")
}

// isCodingCapableModel filters inventory to coding and tool-capable language models.
// It drops embedding, rerank, image, audio, and empty model IDs.
func isCodingCapableModel(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	lower := strings.ToLower(id)
	dropPatterns := []string{
		"embed",
		"bge",
		"gte",
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
