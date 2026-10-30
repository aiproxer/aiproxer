// Package gemini implements the Gemini generateContent–compatible HTTP frontend.
//
// # v1 subset vs vendor spec (cross-check)
//
// Spec: https://ai.google.dev/gemini-api/docs ,
// https://ai.google.dev/api/all-methods ,
// https://ai.google.dev/gemini-api/docs/text-generation
//
// | Area              | v1 status   | Tests |
// |-------------------|------------|-------|
// | POST …:generateContent / :streamGenerateContent | Supported | path_test.go, integration_test.go |
// | contents, systemInstruction | Supported | decode_test.go |
// | generationConfig, tools, toolConfig | Supported (decode) | decode_test.go |
// | functionCall on response | Supported (encode) | encode_test.go, integration_test.go |
// | safetySettings, cachedContent, … | Out of scope | — |
package gemini

// ID is the reserved plugin identifier for the Gemini frontend.
const ID = "gemini"
