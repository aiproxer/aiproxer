// Package openresponses is an independent OpenResponses remote backend emulator.
//
// It implements black-box server behavior for the OpenResponses 2026-04-24 profile:
// HTTP JSON create, SSE create, standalone compaction, and direct WebSocket turns.
// It serves portable and opaque items, tools, reasoning, assistant phase, prefixed
// extensions, and full required response presence; it can force auth, rate-limit,
// 4xx/5xx, malformed-event/resource/content-type, disconnect, virtual-delay,
// slow-write, backpressure, and cancellation-observation modes.
//
// The server keeps an atomic request counter and a redacted bounded capture so
// tests can prove zero-upstream rejection and strict request shape.
//
// Independence: this package is test-only support. Its non-test source MUST NOT
// import production OpenResponses protocol, frontend, backend, or state-machine
// packages, and it must not reuse their wire structs or parsers. Only stdlib plus
// github.com/gorilla/websocket are used here. The immutable official fixtures under
// the reference client's testdata are the only protocol inputs shared with production.
package openresponses

// The emulator deliberately has no exported dependency surface beyond the script
// server, capture, clock, and independent wire builders used by its own tests and
// the direct wire suite (internal/refbackend/openresponses/direct_wire_test.go).
