// Package acp is a reference backend emulator for the Agent Client Protocol (ACP)
// prompt-turn subset used by integration tests.
//
// # Test-only transport (not normative stdio)
//
// ACP normatively uses newline-delimited JSON-RPC over stdio between client and agent
// subprocess. This package instead exposes an HTTP surface so tests can use
// net/http/httptest like other internal/refbackend emulators:
//
//	POST /v1/acp — body is one JSON-RPC 2.0 request (object).
//
// Methods that complete with a single JSON-RPC response (initialize, authenticate,
// session/new, session/load) receive Content-Type application/json with one JSON-RPC
// response object.
//
// session/prompt uses the same POST path; the response is application/x-ndjson: each
// line is a complete JSON value, either a JSON-RPC notification (session/update, no id)
// or the terminal JSON-RPC response for the prompt request id. The handler flushes
// after each line so clients can stream-parse.
//
// session/cancel is a JSON-RPC notification (no id). The server responds with HTTP 204
// No Content and signals cancellation to any in-flight session/prompt for that sessionId.
//
// This framing is a documented custom transport for tests only; it must not be imported
// from internal/core or protocol plugins. See internal/refbackend/doc.go.
package acp
