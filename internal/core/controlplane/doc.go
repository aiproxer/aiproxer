// Package controlplane owns the core control-plane normalization, recording
// policy, query semantics, status state, validation, and runtime policy for
// the AIProxer.
//
// Core ownership rules (enforced by architecture tests):
//   - This package must not import provider SDKs, concrete plugins, frontend
//     wire packages, SQL/Bun, net/http, or internal/stdhttp.
//   - It depends on the stable public contract in pkg/aipsdk/controlplane and
//     on pkg/aipsdk/scope for safe attribution.
//   - Storage, transport, and provider translation stay in adapters; this
//     package defines ports consumed by those adapters.
package controlplane
