package aipapi

type Operation string

const (
	// OperationOpenAIChatCompletions identifies OpenAI-compatible Chat Completions requests.
	OperationOpenAIChatCompletions Operation = "openai.chat_completions"
	// OperationOpenAIResponses identifies OpenAI Responses API requests.
	OperationOpenAIResponses Operation = "openai.responses"
	// OperationOpenResponsesCreate identifies OpenResponses create requests.
	OperationOpenResponsesCreate Operation = "openresponses.create"
	// OperationAnthropicMessages identifies Anthropic Messages API requests.
	OperationAnthropicMessages Operation = "anthropic.messages"
	// OperationGeminiGenerateContent identifies Gemini generateContent requests.
	OperationGeminiGenerateContent Operation = "gemini.generate_content"
	// OperationContextCompaction identifies protocol-neutral context compaction.
	OperationContextCompaction Operation = "context.compaction"
	// OperationDecisionEvaluate identifies protocol-neutral typed decision evaluation.
	OperationDecisionEvaluate Operation = "decision.evaluate"
)

// DeliveryMode records whether the client requested streaming or non-streaming delivery.
type DeliveryMode string

const (
	// DeliveryModeStreaming means the client requested incremental response delivery.
	DeliveryModeStreaming DeliveryMode = "streaming"
	// DeliveryModeNonStreaming means the client requested one completed response body.
	DeliveryModeNonStreaming DeliveryMode = "non_streaming"
)

// Invocation carries protocol operation, delivery, and selected transport metadata from driving adapters to backends.
type Invocation struct {
	Operation     Operation
	DeliveryMode  DeliveryMode
	TransportMode TransportMode
	// ClientUserAgent is the trimmed inbound HTTP User-Agent captured by frontends.
	// It is protocol-neutral invocation metadata and must not be serialized as provider JSON.
	ClientUserAgent string `json:"-"`
}

// DeliveryModeFromClientStream converts protocol stream flags into canonical delivery mode metadata.
func DeliveryModeFromClientStream(stream bool) DeliveryMode {
	if stream {
		return DeliveryModeStreaming
	}
	return DeliveryModeNonStreaming
}
