package conformance

// SentinelCase is an explicit real-stack composition boundary. It is not a
// provider inventory and must not be generated from frontend/backend lists.
type SentinelCase struct {
	ID        string
	Frontend  string
	Backend   string
	Transport ClientTransport
	Negative  bool
	ProfileID string
	Protects  string
}

var boundedSentinelCases = []SentinelCase{
	{ID: "builtin-openresponses-sse", Frontend: FrontendOpenResponses, Backend: BackendOpenResponses, Transport: TransportSSE, Protects: "built-in frontend/core/backend route mounting, streaming event ordering, and terminal ownership"},
	{ID: "compatible-profile-openai-responses-json", Frontend: FrontendOpenResponses, Backend: BackendCompatibleOpenAI, Transport: TransportJSON, ProfileID: "groq", Protects: "validated OpenAI-compatible provider profile family binding and non-stream collection"},
	{ID: "connector-openresponses-sse", Frontend: FrontendOpenResponses, Backend: BackendOpenRouter, Transport: TransportSSE, Protects: "executable connector host negotiation, wiring, and provider request path"},
	{ID: "stateful-openresponses-websocket", Frontend: FrontendOpenResponses, Backend: BackendOpenResponses, Transport: TransportWebSocket, Protects: "stateful frontend session and WebSocket lifecycle composition"},
	{ID: "negative-openresponses-decode", Frontend: FrontendOpenResponses, Backend: BackendOpenResponses, Transport: TransportJSON, Negative: true, Protects: "frontend decode and admission rejection before upstream work"},
	{ID: "negative-openresponses-websocket-store", Frontend: FrontendOpenResponses, Backend: BackendOpenResponses, Transport: TransportWebSocket, Negative: true, Protects: "stateful frontend admission rejection and zero-upstream behavior"},
}

// BoundedSentinelCases returns a defensive copy of the reviewed sentinel policy.
func BoundedSentinelCases() []SentinelCase {
	return append([]SentinelCase(nil), boundedSentinelCases...)
}

const maxBoundedSentinelCases = 8
