package standardplugins

import (
	"errors"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
)

var ErrCustomBackendPrefix = errors.New("custom backend prefix")

const (
	CustomOpenAILegacyCompatibleID    = "custom-openai-legacy-compatible"
	CustomOpenAIResponsesCompatibleID = "custom-openai-responses-compatible"
	CustomAnthropicCompatibleID       = "custom-anthropic-compatible"
)

func IsCustomCompatibleBackendKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case CustomOpenAILegacyCompatibleID, CustomOpenAIResponsesCompatibleID, CustomAnthropicCompatibleID, CustomOpenResponsesCompatibleID, CustomSystemOneCompatibleID:
		return true
	default:
		return false
	}
}

// UsesOpenAINativeUsageMapper reports whether a backend factory kind produces
// provider usage through the OpenAI native usage mapper. Those kinds can emit
// native component measures (for example audio_token) for which stock V2 offers
// have no frozen compatibility proof, so stock V2 admission must fail closed.
// This is a pure factory-kind predicate: it never
// infers a family from the client operation or from an instance ID.
func UsesOpenAINativeUsageMapper(kind string) bool {
	switch strings.TrimSpace(kind) {
	case openairesponses.ID, openailegacy.ID, CustomOpenAILegacyCompatibleID, CustomOpenAIResponsesCompatibleID:
		return true
	default:
		return false
	}
}
