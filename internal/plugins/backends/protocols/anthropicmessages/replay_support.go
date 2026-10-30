package anthropicmessages

import "github.com/aiproxer/aiproxer/pkg/aipapi"

// ReplaySupport is the static historical-reasoning dialect profile for Anthropic Messages.
func ReplaySupport() aipapi.ReasoningReplaySupport {
	return aipapi.ReasoningReplaySupport{
		Dialects: []aipapi.ReasoningDialect{
			aipapi.ReasoningDialectAnthropicThinkingV1,
			aipapi.ReasoningDialectAnthropicRedactedThinkingV1,
		},
	}
}
