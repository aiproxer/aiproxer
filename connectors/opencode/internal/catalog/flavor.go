package catalog

import "strings"

type Flavor string

const (
	FlavorOpenAIChat        Flavor = "openai-compatible/chat"
	FlavorOpenAIResponses   Flavor = "openai/responses"
	FlavorAnthropicMessages Flavor = "anthropic/messages"
	FlavorGoogleGemini      Flavor = "google/gemini"
)

func InferFlavor(entry ModelEntry) Flavor {
	if flavor := inferFlavorFromMetadata(entry.Endpoint, entry.AISDKPackage); flavor != "" {
		return flavor
	}
	return inferFlavorFromModelID(entry.RawID)
}

func inferFlavorFromMetadata(endpoint, aiSDK string) Flavor {
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	aiSDK = strings.ToLower(strings.TrimSpace(aiSDK))

	switch {
	case strings.Contains(aiSDK, "@ai-sdk/anthropic") || strings.Contains(endpoint, "/messages"):
		return FlavorAnthropicMessages
	case strings.Contains(aiSDK, "@ai-sdk/google") || strings.Contains(endpoint, "generativelanguage.googleapis.com") || strings.Contains(endpoint, "generatecontent"):
		return FlavorGoogleGemini
	case strings.Contains(aiSDK, "@ai-sdk/openai-compatible") || strings.Contains(endpoint, "/chat/completions"):
		return FlavorOpenAIChat
	case strings.Contains(aiSDK, "@ai-sdk/openai") || strings.Contains(endpoint, "/responses"):
		return FlavorOpenAIResponses
	default:
		return ""
	}
}

func inferFlavorFromModelID(rawID string) Flavor {
	lower := strings.ToLower(strings.TrimSpace(rawID))
	switch {
	case strings.HasPrefix(lower, "claude-"):
		return FlavorAnthropicMessages
	case strings.HasPrefix(lower, "gemini-"):
		return FlavorGoogleGemini
	case strings.HasPrefix(lower, "gpt-"):
		return FlavorOpenAIResponses
	default:
		return FlavorOpenAIChat
	}
}
