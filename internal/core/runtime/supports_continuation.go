package runtime

import "github.com/aiproxer/aiproxer/pkg/aipapi"

// isOperationSupportsContinuation reports whether the A-side frontend operation
// can legally stitch a continuation leg onto the same logical response.
// Conservative: empty or unknown operation => false.
// Both streaming and non-streaming are legal if canonical collection supports
// continuation; legality is A-side protocol, not delivery mode.
func isOperationSupportsContinuation(op aipapi.Operation) bool {
	switch op {
	case aipapi.OperationOpenAIChatCompletions,
		aipapi.OperationOpenAIResponses,
		aipapi.OperationOpenResponsesCreate,
		aipapi.OperationAnthropicMessages,
		aipapi.OperationGeminiGenerateContent:
		return true
	default:
		return false
	}
}

func supportsContinuationForCall(call aipapi.Call) bool {
	return isOperationSupportsContinuation(call.Invocation.Operation)
}
