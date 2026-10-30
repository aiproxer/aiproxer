# Implementation Gap Analysis: Model Capabilities Catalog

Generated: 2026-04-24

## Scope

This analysis compares the `model-capabilities-catalog` requirements against the current Go LIP codebase. Requirements are generated but not yet approved in `spec.json`; this analysis proceeds because it can inform design and requirement revisions.

## Current State Investigation

### Existing Assets

- Canonical capability vocabulary exists in `pkg/lipapi/capabilities.go`: `Capability`, `BackendCaps`, `RequiredCapabilities`, `Negotiate`, and downgrade application. This already supports pre-upstream hard reject vs explicit downgrade for known request-shape capabilities.
- Candidate-aware capability resolution exists in `internal/core/capabilities/resolver.go` through `Resolver.DescribeCandidate(ctx, cand, call)` and `MapResolver` keyed by backend id.
- Backend plugins expose static or candidate-aware capability declarations through `internal/core/execbackend/backend.go`, where `Backend.Caps` and optional `Backend.ResolveCaps` feed `EffectiveCaps`.
- Runtime composition wires enabled backends into a `capabilities.MapResolver` in `internal/infra/runtimebundle/build.go`, then assigns it to `runtime.Executor.CapsResolver`.
- The executor negotiates capabilities before opening a backend attempt in `internal/core/runtime/executor_open_attempt.go`. A reject excludes the candidate and continues failover; a downgrade mutates the attempt before upstream execution.
- Routing selector and planner support backend/model candidates, failover, weighted alternatives, exclusions, health, and model-only defaults under `internal/core/routing/`.
- OpenAI-specific model narrowing exists as a small plugin-local catalog in `internal/plugins/backends/openaicaps/caps.go`; `docs/capability-catalogs.md` documents catalog maintenance expectations.
- Diagnostics already include health, attempts, inventory, route trace, and logs through `internal/core/diag`, `internal/core/admin`, and `internal/stdhttp/server.go`.

### Existing Patterns to Preserve

- Core owns routing, eligibility, failover, and pre-output recovery semantics.
- Provider-specific details stay at adapter or infra edges; core must not import provider SDKs or raw provider wire models.
- Capability mismatches must fail explicitly before upstream execution; no post-output transparent failover.
- Runtime wiring is explicit through `runtimebundle.Build`; no DI container or global mutable registry.
- Diagnostics surfaces are config-gated and protected by the existing diagnostics shared-secret wrapper where applicable.
- New background work should be long-lived and owned by composition/startup, not request-scoped goroutines.

## Requirement-to-Asset Map

| Requirement | Current Assets | Gap Classification | Notes |
| --- | --- | --- | --- |
| 1. Catalog availability and refresh | Shared outbound HTTP client in runtime bundle; closers lifecycle; config loader | Missing | No models.dev fetch, schema validation, local cache, atomic snapshot swap, retry/backoff, or stale snapshot status. |
| 2. Catalog configuration | `internal/core/config.Config`, validation, sample config | Missing | Need typed settings for catalog usage, update enablement, intervals, source/cache paths, and invalid config errors. |
| 3. Capability and limit normalization | `lipapi.Capability`, `BackendCaps`, negotiation | Missing / Constraint | Capability mapping can reuse `lipapi`; context/output limits have no current canonical runtime shape. Need avoid widening public contracts unnecessarily. |
| 4. Model name matching | Routing candidates carry backend/model strings; model aliases exist for selector rewrite | Missing | No exact/normalized/ambiguous catalog matching policy. Existing `model_aliases` rewrites routes but does not resolve catalog facts. |
| 5. Operator overrides | Config supports model aliases; plugin config raw maps | Missing | No model-fact override config, no precedence resolver for backend:model > model > catalog > no-match. |
| 6. Candidate compatibility filtering | Executor excludes candidates on `NegotiationReject`; planner respects exclusions | Partial | Existing capability filtering works for backend caps only. Need source-aware no-match behavior and catalog/override-aware effective facts. |
| 7. Context and size compatibility | Canonical call carries message/request structure; request body limits exist at HTTP edge | Missing / Unknown | No request-size/token estimate, session contribution estimate, context-limit fact, or context-limit-specific error. Exact tokenization is out of scope, but estimate rules need design. |
| 8. Request-time safety and failover semantics | Pre-open negotiation and exclusions already happen before `Open`; post-output no-retry invariant exists | Partial | Existing timing is aligned. Need stable per-candidate catalog snapshot semantics during a request and ensure refresh cannot alter committed attempts. |
| 9. Operator diagnostics | Health, attempts, inventory, route trace, decision logs | Partial | Missing catalog state, snapshot generation/time, match confidence, ambiguity details, and catalog-specific exclusion reasons. |
| 10. Privacy and external access boundaries | No catalog refresh exists; shared HTTP client can be configured | Missing / Constraint | Need ensure refresh request has no prompts, session content, or provider API keys; diagnostics must redact secrets. |
| 11. Existing backend declarations | `Backend.Caps`, `ResolveCaps`, plugin-local rules | Partial | Need an effective-fact merge rule that intersects backend adapter capabilities with catalog/admin facts where a match exists, while preserving no-extra-limiting when no admin/catalog match exists. |

## Missing Capabilities and Integration Challenges

### Catalog Lifecycle

The runtime has no generic local snapshot mechanism for external metadata. The feature needs a non-request-time lifecycle that can load a valid local copy, optionally fetch updates, validate before activation, expose freshness, and keep the last good snapshot on failure. The design must decide whether catalog startup failure is ever fatal; requirements currently say no valid snapshot falls back to existing backend declarations and exposes unavailable state.

### Matching and Source Precedence

The requirements define a clear precedence order: exact admin backend/model override, admin model override, models.dev match, then no catalog-based limiting. Current code has no concept of source precedence or match confidence. A source-aware resolution result is likely needed; `lipapi.BackendCaps` alone cannot express no-match vs matched-empty-caps vs ambiguous-match.

### Capability vs Limit Facts

`lipapi.BackendCaps` is only a set of capability strings. Context and output limits are not represented in the current capability resolver. Design needs either a richer internal compatibility result or a separate limit resolver. Widening `pkg/lipapi` should be treated carefully because it is a public contract.

### No-Match No-Limiting Policy

Existing negotiation treats missing backend caps as unsupported. The new policy is different for catalog-derived checks: if no admin override or catalog match applies, this feature must not add proxy-side capability or context limiting. Design must ensure catalog no-match does not accidentally become an empty `BackendCaps` result.

### Request Size Estimation

The code validates and transforms canonical calls, but there is no current tokenizer or approximate size estimator tied to routing. Requirements intentionally avoid exact provider tokenization. Design needs define acceptable estimates, when estimates are unavailable, and how session/continuity contribution is included only when available.

### Diagnostics and Admin Surface

Current route trace entries are coarse and small. Attempts lineage records exist through B2BUA store, while diagnostics endpoints are mounted in `internal/stdhttp/server.go`. Catalog status and match/exclusion reasons could fit route trace, attempts lineage, inventory, or a new catalog diagnostics endpoint. Design should avoid overloading one diagnostics shape with unrelated details.

### Refresh Lifecycle and Shutdown

`runtimebundle.Built` already carries closers and shared clients. A refresh worker needs clear ownership and shutdown. It should not start request-scoped goroutines, and it should not rely on external network access during request handling.

## Implementation Approach Options

### Option A: Extend Existing Capability Resolver Only

Extend `capabilities.MapResolver` / backend `ResolveCaps` flow so it consults catalog and override data before returning `lipapi.BackendCaps`.

**Likely changes**
- `internal/core/config`: catalog and override fields.
- `internal/infra/runtimebundle`: load catalog snapshot and wrap existing cap resolver.
- `internal/core/capabilities`: helper for source-aware resolver or merge behavior.
- `internal/core/runtime`: minimal changes if still returning only `BackendCaps`.

**Pros**
- Minimal changes to executor and routing loop.
- Reuses the existing pre-output negotiation path.
- Lower initial surface area.

**Cons**
- `BackendCaps` cannot express context limits, source confidence, ambiguity, or no-match vs empty match.
- Context-size requirement would need a parallel path anyway.
- Risk of encoding too much policy into a capability set and losing diagnostics detail.

**Feasibility**: viable for capability-only subset, incomplete for full requirements.

### Option B: Create a New Catalog Compatibility Service

Introduce a new internal compatibility resolver that returns source-aware capability and limit facts for each candidate, then have executor/routing consume it before `Open`.

**Likely changes**
- New package for model catalog matching, overrides, effective facts, and resolution output.
- New infra package for models.dev fetch/cache/refresh lifecycle.
- Executor changes to perform both capability and context checks using source-aware results.
- Diagnostics additions for source, match confidence, ambiguity, stale/unavailable catalog state.

**Pros**
- Cleanly models override hierarchy and no-match no-limiting policy.
- Supports context limits and diagnostics without overloading `BackendCaps`.
- Testable in isolation.

**Cons**
- More new code and interfaces.
- Requires careful integration with existing `CapsResolver` so current plugins continue to work.
- Design must avoid architecture sprawl and keep core small.

**Feasibility**: strong fit for full requirements if scoped narrowly.

### Option C: Hybrid Catalog Resolver Plus Existing Negotiation

Add a source-aware catalog/override resolver for model facts, but keep existing backend capability negotiation as the final generic gate. Use catalog facts to narrow or bypass additional limiting according to match source, then feed effective caps into current `lipapi.Negotiate`; perform context-limit checks as a separate pre-open eligibility step.

**Likely changes**
- New internal catalog fact resolver and models.dev refresh/cache infra.
- `runtimebundle.Build` composes existing backend caps resolver with catalog fact resolver.
- Executor gains an eligibility step before backend `Open` that can exclude on catalog/admin capability or context limits while preserving no-match no-limiting.
- Existing plugin `ResolveCaps` remains authoritative for adapter implementation limits.
- Diagnostics records source precedence, match type, and exclusion reasons.

**Pros**
- Preserves current negotiation behavior and plugin contracts.
- Supports full requirements including context limits and diagnostics.
- Keeps provider/catalog data separate from `pkg/lipapi` unless design proves a public contract is needed.
- Incremental rollout is possible behind config.

**Cons**
- More coordination between resolver, executor, diagnostics, and config.
- Requires careful test matrix for source precedence and no-match behavior.
- Must avoid duplicating capability semantics across catalog checks and `lipapi.Negotiate`.

**Feasibility**: best-balanced option for design consideration.

## External Dependency Research Needs

- Research Needed: exact models.dev data format, source URL stability, schema versioning, cache validation expectations, and license/distribution terms.
- Research Needed: whether models.dev provides enough structured fields for the required normalized capabilities and context/output limits, or whether local mapping rules are needed.
- Research Needed: acceptable request size estimation strategy for text, images, files, tools, and session context without provider-specific tokenizer dependencies.
- Research Needed: operational default for update interval, failure backoff, cache path, and maximum stale-age warning threshold.

## Testing Impact

- Config tests for valid/invalid catalog settings, override precedence, disabled usage, disabled updates, and sample config drift.
- Catalog parser tests for valid data, corrupt data, unsupported schema, missing optional fields, and deterministic normalized facts.
- Snapshot tests for last-good fallback, atomic generation changes, and unavailable state when no valid snapshot exists.
- Refresh tests with an HTTP stub proving retries, non-blocking request path, shutdown behavior, and no prompts/API keys in refresh requests.
- Matching tests for exact, normalized prefix-stripped, ambiguous, no-match, and unknown admin override cases.
- Resolver tests for backend/model override > model override > catalog > no-match precedence.
- Executor/routing tests for multi-candidate filtering, all-candidates capability reject, all-candidates context reject, weighted/failover preservation among compatible candidates, and no post-output switching.
- Diagnostics tests for catalog status, generation timestamp, match confidence, ambiguity, exclusion reason, and secret redaction.
- Architecture tests to ensure provider SDKs and raw models.dev wire types do not leak into `pkg/lipapi` or core packages if that becomes a risk.

## Complexity and Risk

- **Effort**: L (1-2 weeks). The feature spans config, lifecycle, catalog parsing, matching, routing eligibility, diagnostics, and tests.
- **Risk**: Medium. Existing candidate-aware negotiation and routing seams reduce architectural risk, but context-size estimation, source precedence, and diagnostics breadth require careful design.

## Recommendations for Design Phase

- Prefer the hybrid approach: source-aware model facts plus existing capability negotiation, with context limits checked as a separate pre-open eligibility step.
- Keep raw models.dev schema and fetch/cache mechanics outside public contracts and outside backend provider plugins unless a plugin needs provider-specific interpretation.
- Define an internal resolution result that distinguishes exact match, normalized match, ambiguous match, no match, stale data, and override source.
- Make no-match behavior explicit in design: no admin/catalog match must not become an empty capability set.
- Decide whether context/output limits belong in `pkg/lipapi`, `internal/core/capabilities`, or a new internal model-catalog package before implementation.
- Design diagnostics early so exclusion reasons and source precedence are observable without leaking prompts, session content, or secrets.
- Carry the external research items forward before finalizing models.dev parser and updater design.

---

# Design Discovery Update: Model Capabilities Catalog

Generated: 2026-04-24

## Summary

- **Feature**: `model-capabilities-catalog`
- **Discovery Scope**: Complex Integration / Extension
- **Key Findings**:
  - Existing candidate-aware capability negotiation is the correct integration seam; the design should wrap or augment it rather than replace routing or backend plugins.
  - models.dev data is a provider-keyed JSON object with provider bundles and model maps; it has no global schema/version field, so the proxy needs its own fetched timestamp and content hash.
  - Context limits and match diagnostics require a richer internal resolution result than `lipapi.BackendCaps`, but public `pkg/lipapi` changes can be avoided.

## Research Log

### Existing Runtime Integration Points
- **Context**: The feature must influence routing before upstream work without changing provider protocols.
- **Sources Consulted**: `pkg/lipapi/capabilities.go`, `internal/core/capabilities/resolver.go`, `internal/core/execbackend/backend.go`, `internal/core/runtime/executor.go`, `internal/core/runtime/executor_open_attempt.go`, `internal/infra/runtimebundle/build.go`, `internal/stdhttp/server.go`.
- **Findings**:
  - `runtime.Executor` already calls `CapsResolver.DescribeCandidate` per candidate before `Open`.
  - `runtimebundle.Build` already composes `capabilities.MapResolver` from backend instances.
  - Diagnostics endpoints are mounted only when enabled and protected with `diag.WrapDiagnosticsProtect` for non-health routes.
  - `runtimebundle.Built` has a `Closers` list suitable for catalog refresher shutdown.
- **Implications**: Catalog integration should be composition-owned and candidate-aware, with a long-lived refresher created during runtime build and an executor eligibility hook before backend open.

### models.dev Schema Snapshot
- **Context**: Requirements depend on models.dev fields for modalities, tool support, reasoning, structured outputs, and limits.
- **Sources Consulted**: `https://models.dev/api.json` fetched on 2026-04-24.
- **Findings**:
  - Top-level shape is `map[providerSlug]Provider`; each provider has `id`, `name`, `api`, `doc`, `env`, `npm`, and `models`.
  - Provider `models` is `map[modelID]Model`; the sampled snapshot contained 115 providers and 4,321 model rows.
  - Common model fields include `id`, `name`, optional `family`, `modalities.input`, `modalities.output`, `attachment`, `reasoning`, `tool_call`, optional `structured_output`, `temperature`, `limit`, `cost`, `release_date`, and `last_updated`.
  - `limit` has integer `context`, `output`, and optional `input`; there is no root `version`, `schema`, or `etag` in the payload.
- **Implications**: Ingestion should decode provider/model maps with optional fields, validate the subset the proxy uses, sort maps for deterministic tests, and attach proxy-owned snapshot metadata such as `fetched_at` and `content_hash`.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
| --- | --- | --- | --- | --- |
| Extend existing `CapsResolver` only | Return catalog-narrowed `lipapi.BackendCaps` from the current resolver seam | Minimal executor changes | Cannot represent context limits, match confidence, ambiguity, or no-match semantics | Rejected as incomplete for requirements 4, 7, 9 |
| New standalone catalog compatibility service | Add a source-aware service used directly by executor/routing | Clean fact model and diagnostics | More new core surface; could duplicate negotiation semantics | Useful concept but too broad alone |
| Hybrid resolver plus eligibility gate | Add source-aware model facts and use existing negotiation for capability set checks, with separate context-limit eligibility | Preserves existing routing and plugin contracts while supporting full requirements | Requires careful merge and diagnostics tests | Selected for design |

## Design Decisions

### Decision: Use Internal Source-Aware Model Facts
- **Context**: `lipapi.BackendCaps` cannot distinguish no match from an empty capability set, and it cannot carry limits or match confidence.
- **Alternatives Considered**:
  1. Extend `pkg/lipapi.BackendCaps` with limits and source metadata.
  2. Keep all facts in backend plugin-local `ResolveCaps` functions.
  3. Add an internal model catalog fact model and convert to `BackendCaps` only for negotiation.
- **Selected Approach**: Use an internal model-catalog package with `EffectiveFacts` containing capabilities, limits, source, match kind, and diagnostics details.
- **Rationale**: Keeps public contracts small while satisfying source precedence, diagnostics, and context-limit requirements.
- **Trade-offs**: Executor needs a small new eligibility integration in addition to existing capability negotiation.
- **Follow-up**: Ensure no-match does not produce an empty `BackendCaps` that would reject everything.

### Decision: Build the models.dev Fetcher With the Standard Library
- **Context**: The feature needs HTTP fetch, JSON parse, cache write/read, and periodic refresh.
- **Alternatives Considered**:
  1. Add a third-party cache or scheduler dependency.
  2. Use only standard library HTTP, JSON, time, atomic file replacement, and context cancellation.
- **Selected Approach**: Use the standard library and the existing shared outbound HTTP client.
- **Rationale**: This matches steering dependency policy and keeps the feature small.
- **Trade-offs**: The implementation must write small retry/backoff and atomic cache helpers locally.
- **Follow-up**: Confirm Windows-safe atomic cache replacement behavior during implementation.

### Decision: Keep Request Size Estimation Internal and Conservative
- **Context**: Requirements explicitly exclude exact provider tokenization while requiring context compatibility when estimates are available.
- **Alternatives Considered**:
  1. Add provider tokenizers.
  2. Count bytes/chars and mark the estimate basis in diagnostics.
  3. Skip context filtering until exact tokenizers exist.
- **Selected Approach**: Define an internal estimator with an explicit `EstimateAvailable` flag and diagnostics basis; if no estimate is available, do not exclude.
- **Rationale**: Satisfies requirements without introducing large dependencies or false precision.
- **Trade-offs**: Estimates may be conservative and not match provider token accounting.
- **Follow-up**: Implementation should start with deterministic text/tool/file metadata estimation and defer provider-specific tokenizers.

### Decision: Keep Capability Rejects Centralized in Canonical Negotiation
- **Context**: Design validation found that capability rejection could be split ambiguously between catalog eligibility and `lipapi.Negotiate`.
- **Alternatives Considered**:
  1. Let `EligibilityResolver` reject both capability and context mismatches.
  2. Let catalog/admin facts produce effective capabilities and keep `lipapi.Negotiate` as the only reject/downgrade authority for capabilities.
- **Selected Approach**: Catalog/admin facts narrow or annotate `EffectiveFacts.EffectiveCaps`; `lipapi.Negotiate` performs all capability reject and downgrade decisions. The separate eligibility step only directly rejects known context-limit mismatches and preserves no-match safeguards.
- **Rationale**: This preserves the existing canonical capability contract, avoids duplicate decision logic, and keeps downgradable capability behavior centralized.
- **Trade-offs**: Executor integration must pass effective caps consistently between catalog resolution, context checks, and negotiation.
- **Follow-up**: Add executor tests proving catalog-derived capability mismatch is surfaced through canonical negotiation while context-limit mismatch is surfaced through the separate context path.

## Risks & Mitigations

- Ambiguous model matching could route incorrectly — expose ambiguity and do not silently pick one match.
- Catalog refresh could mutate data seen by in-flight requests — use immutable snapshots and atomic pointer swaps.
- No-match behavior could accidentally reject candidates — represent no-match explicitly and add resolver/executor regression tests.
- Diagnostics could leak sensitive data — diagnostics should expose source, ids, and failure categories only, never prompts, tool payloads, session transcripts, or API keys.

## References

- `https://models.dev/api.json` — sampled external catalog schema.
- `docs/adr/0004-candidate-aware-capabilities.md` — accepted candidate-aware capability resolver decision.
- `.kiro/steering/product.md` — product boundary and core-owned routing promise.
- `.kiro/steering/tech.md` — dependency, provider SDK, streaming, and routing constraints.
- `.kiro/steering/api-standards.md` — canonical middle and deterministic capability handling.
