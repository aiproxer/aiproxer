# API Standards (Steering)

## Architectural Invariants

1. **Canonical in the middle** — frontends decode wire protocols to `pkg/aipapi`; backends translate canonical calls to provider protocols and canonicalize the resulting events.
2. **No pairwise translators** — protocol A must never be translated directly to protocol B as a product architecture.
3. **Streaming is primary** — non-streaming responses are collected from the same canonical event path.
4. **Wire legality is adapter-owned** — framing, status codes, headers, terminal errors, and transport-specific lifecycle rules remain legal for the active frontend/backend protocol.
5. **Capabilities are explicit** — required semantics are negotiated before upstream execution where possible; unsupported semantics fail rather than disappearing silently.
6. **Canonical contracts are provider-neutral** — a type belongs in `pkg/aipapi` only when it represents cross-protocol product semantics, not because one provider exposes a convenient field.
7. **Opaque semantics stay opaque** — provider-specific signed/structured/continuation artifacts may be carried canonically when preservation is required, but the proxy must not silently reinterpret one provider dialect as another.
8. **Errors cross boundaries deliberately** — internal/provider details are classified and mapped to protocol-legal client errors; stack traces, local paths, secrets, and raw sensitive payloads do not cross the A-leg boundary.

## Ownership Rules

### Frontend adapters

Frontends are driving adapters. They own:

- wire request decoding and validation;
- HTTP/SSE/WebSocket lifecycle and protocol framing;
- canonical-to-wire response/error encoding;
- transport-specific identity/session carriers;
- frontend-only compatibility behavior.

They do **not** own routing policy, backend selection, retry/failover semantics, or provider SDK calls.

### Backend adapters and connectors

Backends are driven adapters. They own:

- canonical-to-provider request translation;
- provider SDK/client use;
- provider transport quirks and authentication mechanics;
- provider response/error translation back to canonical events;
- provider-specific capability declaration.

Provider SDK types must stay inside backend/connector boundaries. Shared compatible-protocol helpers may be reused by a protocol family, but they must not become a second canonical layer.

### Canonical/core packages

Canonical and core packages may define only semantics required to coordinate providers/protocols generically. They must not import provider SDKs or concrete protocol/feature plugins.

## Capability and Dialect Policy

- Required client semantics must be represented in capability negotiation.
- Interoperability first ([`VISION.md`](../../VISION.md)): when a candidate cannot represent a semantic exactly, the default is an attempt-local downgrade whose lossiness is modeled, logged, and observable. An operator-selected strict policy refuses instead. Refuse by default only when no honest projection exists.
- A downgrade never fabricates or reorders conversation history, and never leaks into sibling attempts.
- Provider-specific structured artifacts that must survive failover/continuation should use bounded, versionable opaque dialect carriers rather than polluting generic structs with provider fields.
- Cross-provider conversion of opaque dialects is forbidden unless an explicit, tested semantic conversion contract exists.
- Derived metadata used by policy must be conservative. Unknown inputs should bias toward safety rather than falsely claiming precision or permission.

## Usage Semantics

- Canonical usage keeps the provider's own counters. The producer declares how its input counter relates to the provider cache reads and how it relates to the provider cache writes, with the independent declarations `UsageAccountingMetadata.InputTokensExcludeCacheRead` and `UsageAccountingMetadata.InputTokensExcludeCacheWrite`. A relation is never inferred from a dedupe key, a provider id, or raw usage JSON.
- Each declaration is honored on its own: the cache-read declaration adds only the excluded cache reads to an inclusive input projection, and the cache-write declaration adds only the excluded cache writes. The zero value of either declaration means the input counter already contains that cache subset.
- The declarations travel with the counters across every boundary, including the executable connector ABI. An undeclared producer keeps the legacy inclusive-input interpretation.
- A frontend reports the counters in its own protocol semantics: the OpenAI-family prompt count and the Gemini prompt count contain the cached and rewritten prompt subsets, while the Anthropic input count excludes them and reports them as cache counters.
- The reported total is owned by the frontend protocol. The OpenAI-family total is the reported prompt plus the reported completion, even when the provider reported a different total; that provider total stays in the canonical evidence and never reaches the OpenAI-family wire. The Gemini total is the provider-reported total when it does not fall below the reported prompt plus the reported completion, and the reported prompt plus the reported completion otherwise: the Gemini usage metadata defines the total as the prompt plus the candidates plus the thoughts plus the tool-use prompt tokens, so a total below the prompt would be a protocol-invalid payload, while a larger provider total and its separately represented reasoning tokens are preserved. The Anthropic wire carries no total.

## Identity, Session, and Authority

- Session authority is proxy-owned; client-provided session identifiers or hints are untrusted inputs.
- Product identity on the client leg and provider identity on backend legs are separate concerns.
- Authentication/authorization decisions must be made before backend execution and must not be inferred from protocol convenience fields.
- Derived classification metadata is not authorization by itself.

## Protocol Change Procedure

When adding or changing a protocol surface:

1. **Classify the change**: wire-only, canonical semantic, or shared execution semantic.
2. **Keep the smallest ownership surface**:
   - wire-only → frontend/backend adapter;
   - cross-protocol data semantic → `pkg/aipapi`;
   - provider-neutral orchestration semantic → core/SDK seam.
3. **Define capability behavior** before implementation: supported, explicitly degraded, or rejected.
   Account for compatibility per wire field: unchanged bytes, additive metadata,
   or changed behavior. Resolve conflicting acceptance criteria explicitly;
   distinguish a required semantic correction from byte compatibility.
4. **Preserve the streaming path**; do not add a separate non-streaming executor.
5. **Add contract/conformance coverage** at the family boundary. Avoid frontend×backend Cartesian tests unless a unique cross-boundary invariant truly requires one.
6. **Verify error and cancellation legality** for the affected wire protocol.
7. **Keep provider-specific dependencies at the edge** and run architecture guards.

If a new provider or protocol implementation follows these rules, its addition does not require updating this steering file.

## Current Surface Lookup

For terminal translation, inspect `Event.ResponseStatus` / `Event.FinishReason`
and `Collected` in `pkg/aipapi/events.go`, plus the continuation recorder and
compaction detector consumers. Collection does not necessarily retain every
event field; verify which terminal metadata reaches the non-stream encoder
before assuming parity with streaming.

Do not maintain supported-provider/protocol inventories here. Current state is derived from:

- standard contributions under `internal/standardplugins/`;
- canonical/public contracts under `pkg/aipapi` and `pkg/aipsdk`;
- provider-profile catalogs under `internal/providerprofiles/`;
- executable connector manifests under `connectors/`;
- protocol/operator documentation and contract TCKs under `docs/` and `internal/testkit/`.
