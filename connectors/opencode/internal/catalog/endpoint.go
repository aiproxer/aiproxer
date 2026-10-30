package catalog

import "strings"

func EndpointBaseURL(entry ModelEntry, defaultBase string, flavor Flavor) string {
	endpoint := strings.TrimSpace(entry.Endpoint)
	if endpoint == "" {
		endpoint = defaultEndpointForFlavor(defaultBase, flavor)
	}
	switch flavor {
	case FlavorAnthropicMessages:
		return stripKnownSuffixes(endpoint, "/v1/messages", "/messages")
	case FlavorGoogleGemini:
		for _, marker := range []string{"/v1beta/models/", "/models/"} {
			if idx := strings.Index(endpoint, marker); idx > 0 {
				return strings.TrimRight(endpoint[:idx], "/")
			}
		}
		return strings.TrimRight(endpoint, "/")
	default:
		return stripKnownSuffixes(endpoint, "/chat/completions", "/responses")
	}
}

func stripKnownSuffixes(endpoint string, suffixes ...string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	for _, suffix := range suffixes {
		if trimmed, ok := strings.CutSuffix(endpoint, suffix); ok {
			return strings.TrimRight(trimmed, "/")
		}
	}
	return endpoint
}

func defaultEndpointForFlavor(defaultBase string, flavor Flavor) string {
	base := strings.TrimRight(strings.TrimSpace(defaultBase), "/")
	if strings.HasSuffix(base, "/v1") || strings.HasSuffix(base, "/v1beta") {
		if idx := strings.LastIndex(base, "/"); idx >= 0 {
			base = strings.TrimRight(base[:idx], "/")
		}
	}
	switch flavor {
	case FlavorAnthropicMessages:
		return base + "/v1/messages"
	case FlavorGoogleGemini:
		return base + "/v1beta/models/placeholder"
	case FlavorOpenAIResponses:
		return base + "/v1/responses"
	default:
		return base + "/v1/chat/completions"
	}
}
