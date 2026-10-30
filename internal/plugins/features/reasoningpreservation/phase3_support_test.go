package reasoningpreservation_test

import "github.com/aiproxer/aiproxer/pkg/aipapi"

func exactResponsesSupport() aipapi.ReasoningReplaySupport {
	return aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIResponsesItemV1}}
}
