// Package billing defines the minimal typed external monetary host binding.
//
// An external billing integration supplies one Binding: a stable identity and
// version plus typed ports for the cheap pre-route credit screen, the bounded
// quote and atomic exposure admission, the terminal evidence handoff with a
// durable acknowledgement, and explicit owned-resource lifecycle registration.
// Adapters translate these public DTOs to the existing internal billing
// services; the binding itself carries no SQL handles, executors, request
// bodies, or provider-shaped payloads.
//
// A terminal sink may additionally declare RecordCarrier to negotiate the
// version-2 envelope, which carries the exact sealed usage-evidence record
// (key, fingerprint, canonical JSON) as opaque bytes alongside the unchanged
// version-1 fields. Version 1 stays readable byte-for-byte; a sink without the
// declaration keeps receiving it.
//
// The provider-neutral observation/sideband DTOs and the Rater, Quoter,
// StatementImporter, and ReconciliationReader contracts remain defined in
// pkg/aipsdk/metering and pkg/aipsdk/economics. This package reuses them and
// introduces no generic provider-shaped normalizer port and no service-map or
// any-typed payload.
//
// Import DAG: billing -> economics -> metering, billing -> scope (no cycles).
//
// Boundary rules:
//   - Must not import internal/*, database/sql, net/http, Bun/driver types,
//     or provider SDKs and concrete plugins.
//   - DTOs are immutable-style public values using only public packages/types.
//   - No any, no generic maps standing in for typed APIs.
package billing
