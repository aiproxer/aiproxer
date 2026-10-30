// Package anthropic implements the Anthropic Messages API backend connector using
// github.com/anthropics/anthropic-sdk-go. It maps aipapi.Call to POST /v1/messages and
// emits canonical events from the official SSE stream.
//
// BaseURL must be the API origin only (for example https://api.anthropic.com), without
// a /v1 suffix; the SDK appends /v1/messages. This differs from OpenAI-style backends
// where BaseURL often includes /v1.
//
// Model resolution uses the route candidate when present, otherwise the JSON string
// stored under the anthropic.model extension key (same key as the Anthropic frontend decoder).
package anthropic

// ID is the reserved plugin identifier for the Anthropic backend.
const ID = "anthropic"
