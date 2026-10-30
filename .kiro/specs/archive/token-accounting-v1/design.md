# Design Document

## Overview

`token-accounting-v1` introduces an additive token-accounting subsystem designed around hexagonal boundaries. The subsystem separates three usage planes:

- `provider_billable`: provider-authoritative or provider-derived usage relevant to upstream billing.
- `client_visible`: usage corresponding to what the client request or response actually sees after proxy transformations.
- `proxy_billable`: proxy-owned usage used for internal billing, quotas, or control-plane accounting.

The v1 runtime implements accounting through package boundaries, ports, contracts, and parity fixtures without leaking tokenizer or provider concerns into the core.

## Scope and Non-Goals

- **This spec owns**: usage-plane semantics, provenance/authority model, adapter seams for provider count APIs and local tokenizers, ledger shape, preflight count service, admin count endpoint, runtime stream accounting, and dependency guardrails.
- **Provider SDK boundary**: provider SDK integration remains isolated to backend plugins; core and public packages depend only on token-accounting ports and canonical metadata.
- **Streaming rule**: counting must preserve the existing streaming-first model; non-streaming accounting views are projections over the streaming/execution path rather than a separate execution engine.
- **No overwrite rule**: provider-billable and client-visible counts are separate facts. The design forbids silent replacement of one plane with another.

## Architectural Placement

### Domain

Candidate domain concepts live in a new bounded area under `internal/core/tokenaccounting/` and remain provider/tokenizer neutral:

- `UsagePlane`: enum-like type for `provider_billable`, `client_visible`, `proxy_billable`.
- `Authority`: `authoritative`, `delegated`, `estimated`, `advisory`, `unavailable`.
- `Provenance`: source metadata describing provider API, local tokenizer, custom metadata, transformed recomputation, or unavailable reason.
- `UsageRecord`: immutable usage fact for a single plane and scope.
- `AttemptUsage`: attempt-scoped collection of usage facts.
- `RequestUsage`: logical-request-scoped collection of attempt and final client-visible facts.

The domain must not import provider SDK packages, tokenizer libraries, filesystem, SQL, or HTTP packages.

### Application

Application services orchestrate counting work and policies:

- `PreflightCounter`: computes request-side counts before backend execution when policy needs them.
- `UsageResolver`: merges provider counts, local estimates, and transform-derived counts while preserving provenance and authority.
- `BudgetEvaluator`: consumes count outputs for context/budget admission decisions.
- `LedgerRecorder`: emits usage records to a configured ledger port.
- `AdminCountService`: serves protected dry-run count operations for admin surfaces.

Application ports are defined where they are consumed and use domain/application DTOs only.

### Adapters

Driven adapters remain at the edges:

- `provider count adapters`: backend-specific components that call provider-native count APIs or extract authoritative usage from backend responses.
- `tokenizer adapters`: wrappers over `tiktoken` or Hugging Face tokenizers.
- `metadata adapters`: model-to-tokenizer mapping loaders and validators.
- `ledger adapters`: memory, SQLite, or future external stores.
- `admin HTTP adapter`: protected HTTP handler using the application service.

Driving adapters must remain thin. Counting policy, authority merging, and ledger semantics stay in the application/core area.

### Composition Root

Concrete dependency construction belongs in existing composition roots such as `internal/infra/runtimebundle` and command wiring. Later phases will build provider count clients, tokenizer adapters, ledger backends, and config-derived registries there. Core packages receive only narrow interfaces.

## Proposed Package Layout

Planned package layout for later phases:

```text
internal/core/tokenaccounting/
internal/core/tokenaccounting/app/
internal/core/tokenaccounting/ledger/
internal/core/tokenaccounting/policy/
internal/core/tokenaccounting/config/
internal/infra/tokenizers/tiktoken/
internal/infra/tokenizers/hftokenizers/
internal/infra/tokenaccounting/ledgerstore/
internal/plugins/backends/.../tokenusage/
internal/stdhttp/admin/tokenaccounting/
testdata/tokenaccounting/
```

Notes:

- exact subpackage names may be refined during implementation, but tokenizer dependencies stay in `internal/infra` or adapter/plugin zones.
- backend-specific provider count integration belongs with backend plugins or backend-adjacent adapters, not in `internal/core`.
- no new public contracts are introduced in `pkg/lipapi` or `pkg/lipsdk` during Phase 0.

## Ports and Contracts

Consumer-owned ports for later phases:

```go
type ProviderCountPort interface {
    CountRequest(ctx context.Context, req CountableRequest) (ProviderCountResult, error)
}

type LocalTokenizerPort interface {
    Count(ctx context.Context, req CountableRequest, meta TokenizerMetadata) (TokenizerCountResult, error)
}

type TokenizerMetadataPort interface {
    Resolve(ctx context.Context, model ModelRef) (TokenizerMetadata, error)
}

type UsageLedgerPort interface {
    Record(ctx context.Context, usage RequestUsage) error
}
```

Contract rules:

- ports accept canonical or application-owned DTOs, never provider SDK types or tokenizer library structs.
- provider count results must carry plane, provenance, authority, and unavailable classification.
- local tokenizer results must carry tokenizer family and revision metadata.
- ledger contracts must remain additive and decoupled from transport wire formats.

## Request and Response Accounting Model

### Request-Side Counting

- preflight counting operates on canonical request data after frontend adaptation and before backend invocation.
- request-side counts are eligible for budget and context checks.
- repeated request counting within one logical request should later reuse memoized results when the canonical payload is unchanged.

### Response-Side Counting

- provider usage extracted from backend responses populates `provider_billable` with authoritative or delegated authority, depending on source semantics.
- proxy transformations after backend generation may require recomputation for `client_visible`.
- if recomputation is impossible or not configured, `client_visible` remains estimated or unavailable rather than borrowing provider values silently.

### Attempt Lineage

- counts attach to backend attempts before client-visible output begins.
- pre-output failures may still generate provider-billable attempt records.
- final request records aggregate attempt lineage without losing per-attempt facts.
- no retry-after-first-output invariant remains unchanged.

## Provider Adapters

Provider count strategy for later phases:

- backend plugins expose optional provider-count capabilities through narrow seams.
- authoritative provider APIs are preferred for `provider_billable`.
- providers without count APIs fall back to local tokenizers or unavailable outcomes according to configuration.
- provider SDK imports remain inside backend plugin or backend-adjacent adapter packages only.

This keeps provider semantics at the edge while the core resolves usage authority and policy centrally.

## Tokenizer Adapters

Tokenizer scope is intentionally narrow:

- initial families: `tiktoken` and Hugging Face tokenizers.
- adapters encapsulate library-specific model encoding lookup, tokenizer construction, and version metadata.
- tokenizer libraries are forbidden in `pkg/lipapi`, `pkg/lipsdk`, and general `internal/core/...` packages.
- later phases may add config for family allowlists, cache paths, or model mapping tables.

## Configuration Plan

Future config likely needs:

- global enable/disable flag.
- per-plane policy toggles.
- provider-count enablement.
- local tokenizer fallback enablement.
- tokenizer metadata overrides.
- timeout and latency budget controls.
- ledger backend settings.
- admin endpoint enablement and protection settings.

Validation rules:

- malformed tokenizer family names fail startup.
- unsupported precedence combinations fail startup.
- insecure admin exposure fails startup under the same safety posture rules used by other diagnostics/admin surfaces.

## Ledger Design

The billing ledger should be core-defined and adapter-backed.

Required record dimensions:

- request ID
- attempt ID or final-response scope
- route/backend/model identifiers
- usage plane
- input/output/total token dimensions when known
- provenance and authority
- created-at timestamp
- unavailable or fallback reason when applicable

Ledger failure policy stays configurable in later phases:

- required for billing-critical modes
- best-effort for advisory modes

## Runtime Integration Plan

Later integration points:

1. frontend adapter produces canonical request.
2. preflight application service optionally computes request-side counts.
3. policy evaluates context and budget before backend open.
4. backend plugin optionally performs provider count API calls or returns provider usage.
5. runtime execution preserves attempt lineage.
6. post-processing computes transformed `client_visible` usage when needed.
7. ledger recorder persists records.
8. protected diagnostics/admin surfaces expose summarized results.

Integration constraints:

- no counting logic inside protocol codec packages beyond translation into core-owned DTOs.
- no tokenizer execution in public packages.
- no silent fallback after timeout or unsupported model; reason must be explicit.

## Errors and Failure Policy

Error classes for later phases:

- `unsupported tokenizer family`
- `provider count unavailable`
- `tokenizer metadata unresolved`
- `count timeout`
- `required count missing`
- `ledger write failed`

Handling rules:

- preflight required-count failures fail before output.
- advisory count failures preserve request flow but emit unavailable reasons.
- admin endpoint errors expose safe classification only.
- logs and diagnostics avoid request content.

## Security Considerations

- provider count adapters send only minimum provider-required content.
- counting logs and metrics never include raw prompts by default.
- admin counting surfaces require explicit protection and follow local-only safe defaults where applicable.
- ledger records store usage facts and identifiers, not raw content, unless another approved feature owns transcript capture.
- tokenizer metadata and diagnostics must not expose credentials, auth headers, cookies, or vendor secrets.

## Observability and Performance

Later phases should emit:

- count source selected
- fallback reason
- unavailable reason
- provider count latency
- local tokenizer latency
- ledger write latency
- count cache hit or reuse indicators

Performance guardrails:

- bounded timeouts for provider count and local tokenizers
- request-scoped reuse to avoid duplicate counting
- no unbounded in-memory caches without explicit ownership/configuration

## Testing Strategy

The implementation is validated by focused runtime, configuration, ledger, observability, admin, and architecture tests.

Later TDD sequence:

1. domain/application contract tests for usage planes, provenance, and merge rules
2. config validation tests
3. provider count adapter contract tests with stubs
4. tokenizer adapter parity tests using fixtures under `testdata/tokenaccounting/`
5. runtime integration tests for preflight and attempt lineage
6. admin endpoint and ledger tests
7. architecture tests guarding tokenizer dependency boundaries

Fixture policy:

- counts must come from deterministic external evidence or remain marked pending.
- fixtures must identify the source toolchain and revision where known.
- no invented exact token counts.
