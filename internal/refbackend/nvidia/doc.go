// Package nvidia is a reference backend emulator for NVIDIA NIM's API surface.
// It serves both POST /v1/chat/completions and POST /v1/responses with
// JSON or SSE bodies compatible with github.com/openai/openai-go/v3, providing
// request body/header capture for integration test assertions.
package nvidia
