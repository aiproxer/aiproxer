// Package openaicompat contains shared adapter-layer helpers for backend
// plugins that talk to OpenAI-compatible APIs through the openai-go SDK.
//
// This package is intentionally under internal/plugins/backends: SDK and wire
// concerns belong at the backend adapter edge, not in core contracts or
// orchestration packages.
package openaicompat
