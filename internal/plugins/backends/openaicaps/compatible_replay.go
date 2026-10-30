package openaicaps

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation/reasoningreplay"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	FlavorChat      = "chat"
	FlavorResponses = "responses"
)

func CompatibleReplayEligible(model string, backendPrefixes []string) bool {
	return reasoningreplay.Eligible(model, backendPrefixes)
}

func ForHostedModelCompatibleReplay(model string, backendPrefixes []string) aipapi.BackendCaps {
	caps := ForHostedModel(model)
	if !CompatibleReplayEligible(model, backendPrefixes) {
		return caps
	}
	out := aipapi.NewBackendCaps()
	for c := range caps {
		out[c] = struct{}{}
	}
	out[aipapi.CapabilityReasoningReplay] = struct{}{}
	return out
}

func ResolveCompatibleReplaySupport(flavor, model string, backendPrefixes []string) aipapi.ReasoningReplaySupport {
	if !CompatibleReplayEligible(model, backendPrefixes) {
		return aipapi.ReasoningReplaySupport{}
	}
	switch strings.ToLower(strings.TrimSpace(flavor)) {
	case FlavorResponses:
		return aipapi.ReasoningReplaySupport{
			Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIResponsesItemV1},
		}
	default:
		return aipapi.ReasoningReplaySupport{
			Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1},
		}
	}
}
