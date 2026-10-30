// Package openailegacy implements the legacy OpenAI Chat Completions backend connector using
// github.com/openai/openai-go/v3. It maps aipapi.Call to chat/completions and streams canonical
// events from the official SSE stream.
package openailegacy

// ID is the reserved plugin identifier for the legacy OpenAI-compatible backend.
const ID = "openai-legacy"
