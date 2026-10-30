// Package openairesponses implements the OpenAI Responses–compatible HTTP frontend:
// JSON decode to aipapi.Call, core execution, and JSON or SSE encode for official clients.
// Routing uses header X-AIP-Route (selector like backend:model) until task 11 wires config.
//
// # v1 subset vs vendor spec (cross-check)
//
// Spec: https://platform.openai.com/docs/api-reference/responses ,
// https://platform.openai.com/docs/api-reference/responses/create ,
// https://platform.openai.com/docs/api-reference/responses-streaming
//
// | Area            | v1 status   | Tests |
// |-----------------|------------|-------|
// | POST …/responses | Supported  | integration_test.go, decode_test.go, encode_test.go |
// | Other endpoints  | Out of scope | — |
// | input string/array of message | Supported | decode_test.go |
// | input item types except message | Rejected | decode_test.go |
// | instructions string | Supported | decode_test.go |
// | instructions non-string | Rejected | decode_test.go |
// | tools (function) | Supported (decode) | decode_test.go |
// | Tool/reasoning on response wire | Supported (encode) | encode_test.go, integration_test.go |
// | Reasoning on wire | Supported (encode) | encode_test.go |
// | Multimodal input_text/image/file | Supported | decode_test.go, integration_test.go |
package openairesponses

// ID is the reserved plugin identifier for the OpenAI Responses frontend.
const ID = "openai-responses"
