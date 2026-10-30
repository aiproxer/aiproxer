// Package openairesponses implements the OpenAI Responses API backend connector using
// github.com/openai/openai-go/v3. It maps aipapi.Call to responses.create and streams
// canonical events from the official SSE stream.
package openairesponses

// ID is the reserved plugin identifier for the OpenAI Responses backend.
const ID = "openai-responses"
