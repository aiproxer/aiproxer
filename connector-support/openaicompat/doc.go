// Package openaicompat provides provider-neutral OpenAI-compatible HTTP mapping
// for external backend connector modules (chat completions, Responses API,
// SSE stream decoding, model inventory, and error classification).
//
// This module must not name concrete hosted providers, import root internal/
// packages, or own HTTP policy: callers supply *http.Client and optional
// request mutation hooks. Parsers are byte-bounded.
package openaicompat
