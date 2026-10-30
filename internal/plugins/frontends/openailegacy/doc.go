// Package openailegacy implements the legacy OpenAI Chat Completions–compatible HTTP frontend:
// JSON decode to aipapi.Call, core execution, and JSON or SSE encode for official clients.
// Routing uses header X-AIP-Route (selector like backend:model) until task 11 wires config.
// POST /v1/chat/completions (decode → executor → encode).
//
// # v1 subset vs vendor spec (cross-check)
//
// Spec: https://platform.openai.com/docs/api-reference/chat ,
// https://platform.openai.com/docs/api-reference/chat/create
//
// | Area              | v1 status   | Tests |
// |-------------------|------------|-------|
// | POST …/chat/completions | Supported | integration_test.go, decode_test.go, encode_test.go |
// | messages, tools, tool_choice | Supported (decode) | decode_test.go |
// | stream_options    | Preserved in extensions | decode_test.go |
// | Assistant tool_calls in request | Supported (decode) | decode_test.go |
// | Tool response on wire (tool_calls / stream deltas) | Supported (encode) | encode_test.go, integration_test.go |
// | logprobs, n, json_schema response_format, … | Out of scope | — |
package openailegacy

// ID is the reserved plugin identifier for the legacy OpenAI-compatible frontend.
const ID = "openai-legacy"
