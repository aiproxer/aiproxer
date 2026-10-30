package aipapi

import (
	"encoding/json"
	"strings"
)

func isProxyInternalExtensionKey(key string) bool {
	key = strings.TrimSpace(key)
	return strings.HasPrefix(key, "aip.")
}

// isFrontendWireMetadataExtensionKey reports extension keys stored by frontends for
// encode/decode round-trip only. They must not drive protocol requirement admission.
func isFrontendWireMetadataExtensionKey(key string) bool {
	key = strings.TrimSpace(key)
	switch key {
	case "openairesponses.model", "openailegacy.model", "gemini.model", "anthropic.model",
		"openailegacy.stream_options":
		return true
	}
	return strings.HasPrefix(key, "nvidia.extra_body.") || strings.HasPrefix(key, "openrouter.")
}

func isNonProtocolExtensionKey(key string) bool {
	return isProxyInternalExtensionKey(key) || isFrontendWireMetadataExtensionKey(key)
}

func nonInternalExtensionKeys(extensions map[string]json.RawMessage) []string {
	if len(extensions) == 0 {
		return nil
	}
	keys := make([]string, 0, len(extensions))
	for key := range extensions {
		if isNonProtocolExtensionKey(key) {
			continue
		}
		keys = append(keys, key)
	}
	return keys
}
