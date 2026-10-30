// package codex implements the OpenAI Codex Responses backend connector using
// stdlib HTTP and a local SSE parser. It maps aipapi.Call to Codex /responses JSON
// and streams canonical events from the Codex SSE wire format.
package codex

// ID is the reserved plugin identifier for the OpenAI Codex backend.
const ID = "openai-codex"
