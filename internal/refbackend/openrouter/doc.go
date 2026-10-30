// Package openrouter is a reference backend emulator for OpenRouter's API surface.
// It serves both POST /api/v1/chat/completions and POST /api/v1/responses with
// JSON or SSE bodies compatible with github.com/openai/openai-go/v3, adding
// OpenRouter-specific header/body capture for integration test assertions.
package openrouter
