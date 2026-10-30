// Package controlplane defines the stable, safe control-plane evidence and query
// contracts for the AIProxer runtime.
//
// Go API note (v2-compatible): [Event] carries a single [EventDetail] interface
// field instead of parallel nullable detail pointers. JSON on the wire is
// unchanged — category-keyed detail objects are preserved via custom marshaling.
//
// The package is additive public SDK surface for future feature plugins, internal
// adapters, and protected operator query routes. It is safe-by-construction:
// raw bearer tokens, API keys, OAuth/resume tokens, credential secrets, raw
// transport headers, and raw request/response payloads are never fields on
// these types. Only stable identifiers, correlation fields, safe scope
// attribution, bounded summaries, and explicit availability/redaction state
// are carried.
//
// Boundary rules (enforced by architecture tests):
//   - This package must not import internal/core, internal/infra, internal/stdhttp,
//     internal/plugins, database/sql, Bun, net/http, or any provider SDK.
//   - The only permitted non-stdlib dependency is pkg/aipsdk/scope, which provides
//     the safe, presence-aware principal/scope attribution snapshot.
//
// The contract intentionally does not expose storage, transport, or provider
// types. Storage adapters and HTTP query routes translate into and out of
// these DTOs at their own boundaries.
package controlplane
