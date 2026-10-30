# Extension Authoring and Certification

AIProxer keeps the canonical contract and runtime policy independent from provider population. Choose the smallest extension seam that safely represents the behavior.

## Decision Tree

1. **Provider profile:** Is the provider wire-compatible with an already certified family, and are endpoint, auth references, bounded headers, model discovery, accounting, capabilities, dialects, and closed quirks enough? Add typed profile data. Do not add a Go registration, frontend, core, ABI, or sentinel entry.
2. **Family adapter:** Does the provider share a family boundary but require new deterministic wire interpretation, transport, auth handshake, or lifecycle code? Extend or add a backend-family adapter and certify it once against the backend TCK.
3. **Executable connector:** Does it require optional/local process lifecycle, a stateful session protocol, unique transport, or behavior that cannot safely live in the profile schema? Implement an out-of-process connector behind `pkg/aipsdk/backendplugin` and run the connector TCK.
4. **Frontend:** A new client wire protocol is a frontend adapter, not a backend-family change. Translate wire data to canonical calls and canonical events back to wire output.

If the choice is unclear, prefer a profile only when the family can prove the effective behavior. Otherwise graduate; never hide executable behavior in profile data.

## Profile Schema and Security

Profiles use the versioned typed `aip.provider-profile/v1` schema. They contain identifiers and secret/environment names, never secret values. Validation is fail-closed for unknown versions, families, unsupported quirks, capability elevation, invalid endpoints, unsafe auth/header combinations, unknown fields, and excessive counts or bytes. A quirk is accepted only after its family adapter implements and certifies the behavior.

Profiles cannot contain code, scripts, templates, expressions, regex rewrite programs, arbitrary request/response transformations, commands, process launches, unbounded headers, or provider-network calls during validation. Endpoint policy reuses the runtime HTTPS/loopback rules. Static headers are bounded and cannot replace family-owned authorization. Reload compiles profiles into the immutable generation; it does not create a second mutable registry.

## Contribution Facets

Built-in extensions declare one explicit contribution composed from narrow facets: registration/factory, route claims, diagnostics projection, contract declaration, security posture, and compatible-family/profile source where applicable. Composition derives standard registrations, route ownership, diagnostics inventory, and contract subjects from those contributions. Profiles bind to a family contribution; 1,000 profiles do not become 1,000 factories or contributions.

## TCK Entry Points

- **Frontend TCK:** `internal/testkit/contract/frontend`; uses a capturing canonical executor and protocol-owned fixtures. It does not construct a backend.
- **Canonical core TCK:** `internal/testkit/contract/core`; tests requirements, projections, admission, failover freezing, output commitment, and terminal semantics directly.
- **Backend TCK:** `internal/testkit/contract/backend`; selects positive scenarios from effective capability/dialect declarations and proves hard-negative rejection with zero upstream work.
- **Connector TCK:** `pkg/aipsdk/backendplugin/contracttest`; connector authors drive negotiation, configure, execute, cancellation, close, and semantic carrier round trips through the supported host seam.
- **Profile certification:** `internal/providerprofiles`; validates schema, family binding, effective capabilities, endpoint/auth/model behavior, and quirks without frontend multiplication.

The bounded end-to-end sentinel is a small explicit real-stack smoke test. It protects composition-root wiring, route mounting, middleware order, generation assembly, and representative connector/family paths. It is not a compatibility matrix and must not grow per profile.

## Canonical and ABI Promotion

Before adding a first-class `pkg/aipapi` field, record: (1) which core policy consumes its meaning, (2) which second protocol family shares it, (3) why a bounded dialect/extension carrier cannot preserve residual fidelity, (4) which admission/projection behavior depends on it, and (5) the public API migration cost. Adapter-only wire fidelity stays in a bounded, negotiated carrier and is never a raw request/response tunnel.

Backend-plugin ABI additions are named for canonical semantics or transport capabilities, not protocols or providers. Preserve v1.0-v1.3 negotiation and the `exact_openresponses_fields` compatibility vocabulary. Unknown required carriers fail deterministically; they are never silently dropped.

## Change-Surface Review

Run `go run ./internal/archtest/tools/changesurface/cmd` for the human report or add `-json` for CI evidence. Categories distinguish extension-owned production, provider-profile data, shared composition, canonical contracts, core runtime/routing, ABI, generated artifacts, dedicated tests/reference evidence, and docs/spec. Generated and dedicated evidence breadth is reported but does not itself imply coupling; generated classification has precedence over owning zones, while tests colocated with protected production zones retain their owning zone and cannot hide profile-only boundary edits. Operational scripts are not automatically treated as evidence and therefore fail the profile-only ratchet unless explicitly classified as a safe documentation/test path.
A profile-only review uses `-profile-only` and requires zero canonical, core, frontend/extension-owned, ABI, or shared-composition paths.

The release gate is TCK certification plus protocol-owned compliance and the bounded sentinel. Cross-product diagnostics may be useful during migration, but a full frontend-by-provider product is not authoritative release architecture.
