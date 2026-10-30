package anthropicmessages

import "github.com/aiproxer/aiproxer/pkg/aipapi"

func defaultBackendCaps() aipapi.BackendCaps {
	return aipapi.NewBackendCaps(
		aipapi.CapabilityStreaming,
		aipapi.CapabilityTools,
		aipapi.CapabilityVision,
		aipapi.CapabilityDocuments,
		aipapi.CapabilityParallelToolCalls,
		aipapi.CapabilityReasoningReplay,
	)
}
