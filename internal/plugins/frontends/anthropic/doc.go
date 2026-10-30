// Package anthropic implements the Anthropic Messages–compatible HTTP frontend.
//
// # v1 subset vs vendor spec (cross-check)
//
// Spec: https://docs.anthropic.com/en/api/messages
//
// | Area              | v1 status   | Tests |
// |-------------------|------------|-------|
// | POST …/messages   | Supported  | integration_test.go, decode_test.go, encode_test.go |
// | system string/blocks (text) | Supported | decode_test.go |
// | messages user/assistant | Supported | decode_test.go |
// | tools, tool_choice | Supported (decode) | decode_test.go |
// | max_tokens, sampling | Supported | decode_test.go |
// | metadata          | Ignored (not mapped to Call) | decode_test.go |
// | top_k             | Ignored (not mapped to Call) | decode_test.go |
// | anthropic-version header | Passed via [DecodeOptions] (not validated; wire compatibility) | integration_test.go |
// | tool_use on response (stream + JSON) | Supported (encode) | encode_test.go, integration_test.go |
// | thinking / extended blocks | Supported (encode, thinking + signature) | encode_test.go |
package anthropic

// ID is the reserved plugin identifier for the Anthropic frontend.
const ID = "anthropic"
