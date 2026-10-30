# Requirements Document

**Source context:** GitHub issue [#157 — Reasoning output preservation](https://github.com/matdev83/go-llm-interactive-proxy/issues/157)

## Introduction

The `reasoning-output-preservation` feature protects multi-turn model quality when a client or agent omits reasoning content from later request history. When explicitly enabled, the proxy observes the canonical assistant output it releases to a frontend, retains bounded replay artifacts under the authoritative session scope, compares those artifacts with later client-supplied assistant history, and can restore reasoning that is demonstrably missing before a matching backend/model attempt is submitted.

The feature preserves only reasoning that the proxy actually observed. It does not synthesize chain-of-thought, infer omitted content, or attempt to determine why a client omitted reasoning. Matching is exact and conservative. Ambiguous or conflicting history is never rewritten.

## Boundary Context

- **In scope**: opt-in feature configuration; backend and backend/model rule matching; a versioned built-in compatibility catalog; canonical request-side reasoning parts; hard replay capability negotiation; final canonical stream observation; exact turn matching; per-candidate restoration; bounded session state; adapter replay dialects; safe observability; TDD and release evidence.
- **Out of scope**: generating, summarizing, or reconstructing reasoning that was not observed; fuzzy, semantic, embedding, or LLM-based turn matching; copying reasoning across sessions; exposing stored reasoning through diagnostics; durable cross-process or multi-replica state in v1; changing provider policy about whether hidden reasoning is returned.
- **Adjacent expectations**: preserve ADR 0002 immutable-baseline attempt derivation; remain compatible with Anthropic thinking-signature preservation; preserve interleaved-thinking behavior; preserve B2BUA lineage, capability negotiation, token-accounting checkpoints, completion gates, and the no-retry-after-first-output invariant.
- **Boundary ownership**: shared request semantics and extension contracts are SDK/canonical concerns; matching, state, catalog policy, and restoration are feature-plugin concerns; protocol replay shapes remain frontend/backend adapter concerns; candidate lifecycle ordering remains core-runtime orchestration.
- **Revalidation triggers**: canonical request contracts, feature-bundle schema, capability negotiation, route eligibility and size estimation, sequential/recv failover, parallel races, completion gates, response hooks, secure-session partitioning, adapter parity, diagnostics, metrics, and stream cancellation.

## Requirements

### Requirement 1: Explicit Activation and Matching Policy
**Objective:** As an operator, I want reasoning preservation to be explicitly enabled and narrowly targeted, so existing deployments remain unchanged and only selected backend/model traffic receives special treatment.

#### Acceptance Criteria
1.1. When no enabled `reasoning-output-preservation` feature row is configured, the proxy shall perform no feature-specific state allocation, capture, matching, restoration, logging, metrics, or diagnostics work.
1.2. When the feature is enabled, the operator shall explicitly select `observe` or `restore`; unknown or omitted actions shall fail configuration validation.
1.3. Where a rule names only a backend instance, the proxy shall match that exact configured backend instance for all of its models.
1.4. Where a rule names a backend instance and model keywords, the proxy shall require the exact backend instance and at least one case-insensitive, trimmed model-keyword match.
1.5. The proxy shall apply deterministic rule precedence: explicit disabled backend/model rule, explicit enabled backend/model rule, explicit disabled backend-wide rule, explicit enabled backend-wide rule, built-in catalog rule, then no match.
1.6. The proxy shall ship a versioned built-in compatibility catalog, initially including a conservative Kimi/Moonshot entry derived from issue #157, and shall allow explicit operator rules to override built-in entries.
1.7. While action is `observe`, the proxy shall capture and classify eligible turns without mutating backend-bound calls.
1.8. While action is `restore`, the proxy shall perform the same capture and classification and may restore only turns that satisfy all restoration requirements.

### Requirement 2: Canonical Historical Reasoning Contract
**Objective:** As a protocol adapter and feature implementer, I want historical assistant reasoning represented explicitly in canonical requests, so replay semantics can cross supported protocols without pairwise translators.

#### Acceptance Criteria
2.1. The canonical request model shall represent reasoning as an ordered assistant-message part distinct from visible text, media, tool calls, and tool results.
2.2. A canonical reasoning part shall carry a bounded replay dialect identifier and may carry normalized reasoning text, integrity/signature data, and bounded opaque replay metadata.
2.3. A canonical reasoning part shall contain at least one replay payload and shall be valid only inside an assistant message.
2.4. Canonical validation shall bound reasoning text, dialect, signature, opaque metadata, per-part count, and total reasoning bytes.
2.5. Canonical cloning shall deep-copy all mutable reasoning metadata so attempts, hooks, stores, and callers cannot alias opaque bytes.
2.6. Canonical request sizing, token-counting inputs, checkpoint cloning, equality helpers, and fuzz targets shall account for reasoning parts.
2.7. When a call contains historical reasoning, the proxy shall require a hard `reasoning_replay` capability that cannot be silently downgraded or stripped.
2.8. Canonical contracts shall remain free of provider SDK and transport types.

### Requirement 3: Final-Stream Capture and Artifact Lifecycle
**Objective:** As an operator, I want the proxy to retain only the reasoning associated with the winning output it released, so failed, replaced, or losing attempts cannot contaminate later context.

#### Acceptance Criteria
3.1. When an eligible attempt becomes the active surfaced B-leg, the proxy shall observe canonical events incrementally without buffering the whole completion, delaying TTFT, or changing frontend framing.
3.2. The observer shall receive the final canonical events after response-part hooks and completion-gate resolution and before frontend protocol encoding.
3.3. For each assistant turn, the proxy shall retain an exact non-reasoning anchor, ordered reasoning placement metadata, replay dialects, source backend/model metadata, and bounded reasoning payloads.
3.4. The proxy shall persist an artifact only after the winning stream releases a successful `response_finished` terminal event from the runtime to the frontend.
3.5. If a stream fails, is cancelled, is closed early, is replaced during pre-output recovery, or is superseded by a completion-gate replacement, the proxy shall discard its pending artifact.
3.6. Parallel-race losing arms shall not create persisted artifacts.
3.7. Multiple reasoning blocks, signatures, redacted/opaque blocks, visible content, and interleaved tool calls shall retain their canonical order and per-block association.
3.8. If a pending artifact exceeds configured per-turn bounds, the proxy shall discard it and record a content-safe bounded outcome rather than truncate replay data silently.
3.9. The v1 success boundary shall mean that the runtime released the terminal canonical event; it shall not claim proof that the client transport acknowledged every encoded byte.

### Requirement 4: Exact Preservation Detection
**Objective:** As a maintainer, I want deterministic evidence that a previously observed turn is present or missing, so the proxy never rewrites merely similar conversation history.

#### Acceptance Criteria
4.1. The proxy shall derive anchors from deterministic canonical serialization of assistant messages with reasoning parts excluded, non-reasoning part order preserved, and JSON/tool payloads normalized.
4.2. The proxy shall match only within the authoritative session’s bounded recent artifact window.
4.3. When exactly one stored artifact matches exactly one assistant message and that message has no reasoning, the proxy shall classify the turn as `missing`.
4.4. When exactly one stored artifact matches and the client-supplied reasoning is canonically equivalent, the proxy shall classify the turn as `preserved`.
4.5. When exactly one stored artifact matches and client-supplied reasoning differs in content, order, signature, dialect, or opaque metadata, the proxy shall classify the turn as `conflicting`.
4.6. When duplicate messages or artifacts permit more than one valid association, the proxy shall classify the turn as `ambiguous`.
4.7. When no artifact matches, the proxy shall classify the turn as `unmatched`.
4.8. `conflicting`, `ambiguous`, and `unmatched` classifications shall never overwrite or insert reasoning.
4.9. The proxy shall not use fuzzy text matching, embeddings, external model calls, heuristics based on similarity, or inferred user intent.
4.10. Logs and diagnostics shall describe only observable classifications and shall not claim that a client intentionally trimmed reasoning.

### Requirement 5: Candidate-Specific Restoration and Failover Safety
**Objective:** As a routing operator, I want restoration derived independently for each candidate before final eligibility and accounting checks, so failover and parallel routes remain isolated and correctly sized.

#### Acceptance Criteria
5.1. The proxy shall restore reasoning only when action is `restore`, the target candidate matches effective policy, the turn is uniquely `missing`, and every stored replay dialect is representable by that candidate.
5.2. Restoration shall run on a fresh clone of the immutable post-submit baseline after route selection and interleaved shaping but before required-capability derivation, candidate context eligibility, final token preflight, backend-ingress checkpointing, and backend translation.
5.3. Restoration shall insert each reasoning block at its recorded position relative to the assistant message’s non-reasoning parts.
5.4. Restoration shall be idempotent and shall not duplicate, reorder, normalize away, or overwrite client-supplied reasoning.
5.5. Sequential retries, recv-phase replacements, weighted choices, and parallel race arms shall each derive an independent restored attempt; no restored mutation shall leak back to the baseline or another candidate.
5.6. Candidate context-size and token-accounting decisions shall be recomputed from the restored attempt, even if coarse route expansion used the smaller baseline estimate.
5.7. When a candidate cannot represent a required replay dialect, the default behavior shall exclude that candidate before protected backend work; an explicit `log_skip` policy may continue without restoration.
5.8. When state access or matching fails, the proxy shall follow an explicit bounded failure policy and shall never submit a partially restored call.
5.9. If all candidates are excluded because required reasoning replay is unavailable, the proxy shall return a stable capability/compatibility error rather than silently dropping reasoning.
5.10. Backend-ingress metering and authorization shall observe the restored call, including the restored input size and token exposure.

### Requirement 6: Scoped, Bounded, and Private State
**Objective:** As an operator, I want preservation state isolated and bounded, so hidden reasoning does not become an unbounded or cross-session data store.

#### Acceptance Criteria
6.1. The runtime shall provide the feature an opaque authoritative session partition or A-leg scope; the feature shall not derive authority from raw client-supplied session identifiers.
6.2. V1 state shall be process-local and owned by one configured feature-plugin instance.
6.3. State shall be bounded by TTL, maximum artifacts per session, maximum reasoning bytes per artifact, and maximum total bytes per session.
6.4. Append, lookup snapshot, expiry, eviction, and deletion shall be concurrency-safe and atomic at the session boundary.
6.5. Store reads shall return defensive copies, and writes shall not retain caller-owned mutable slices.
6.6. Artifacts shall be inaccessible across authoritative sessions, principals where session policy separates them, or feature-plugin instances.
6.7. Expiry and eviction shall remove reasoning payloads and anchor material from reachable state.
6.8. When a request reaches another replica or a restarted process without the artifact, the proxy shall treat it as a state miss and shall not fabricate or cross-load reasoning.
6.9. Operator documentation shall state the v1 process-local/sticky-session limitation and the behavior after restart or rebalance.
6.10. Disabled configuration shall construct no preservation store.

### Requirement 7: Adapter Replay Dialects and Protocol Isolation
**Objective:** As a protocol maintainer, I want reasoning decoded and replayed only through legal adapter-owned shapes, so provider-specific metadata cannot leak into incompatible backends.

#### Acceptance Criteria
7.1. The OpenAI-compatible Chat Completions adapters shall decode supported historical reasoning fields into a text replay dialect and shall encode canonical reasoning through the supported backend wire fields.
7.2. The OpenAI Responses adapters shall decode supported reasoning input items and shall preserve the bounded identifiers, summaries/content, and encrypted or opaque replay data required for subsequent input replay.
7.3. The Anthropic Messages adapters shall decode and replay `thinking` and `redacted_thinking` blocks with their required signatures or opaque data, reusing the existing canonical thinking-signature carrier where applicable.
7.4. Each backend candidate shall advertise or resolve the replay dialects it can represent for the selected model.
7.5. OpenRouter and compatible/custom backends shall resolve replay support by effective upstream flavor, backend family, and selected model rather than by instance name alone.
7.6. A replay dialect shall never be serialized through an incompatible provider family merely because both providers expose generic reasoning text.
7.7. Unsupported frontends or backends, including any Gemini path without a legal replay contract, shall fail, exclude, or skip explicitly according to policy and shall never silently discard required reasoning.
7.8. Streaming and non-streaming paths shall be parity-tested wherever the frontend protocol has a legal reasoning representation.
7.9. Where a frontend cannot legally expose reasoning, the observer may still retain the final canonical reasoning released to that frontend adapter, but documentation shall not claim the client received a wire representation it could not encode.

### Requirement 8: Content-Safe Observability
**Objective:** As an operator, I want to know when preservation is working or degraded without exposing hidden reasoning, so the feature can be operated safely.

#### Acceptance Criteria
8.1. The proxy shall emit structured, content-safe outcomes for `observed`, `preserved`, `missing`, `restored`, `ambiguous`, `conflicting`, `unmatched`, `unrepresentable`, `state_error`, `evicted`, and `oversize`.
8.2. Structured records may include trace/A-leg/B-leg correlation, backend instance, bounded rule/catalog identifiers, action, counts, and byte totals, but shall not include reasoning, signatures, opaque data, prompt excerpts, or anchor values.
8.3. Metrics shall use only bounded-cardinality labels and shall not label by arbitrary model strings, raw backend route selectors, session IDs, anchor digests, or user content.
8.4. Diagnostics and extension inventory shall expose only feature enablement, action, catalog version, bounded rule IDs/counts, configured limits, stage occupancy, process-local posture, and aggregate counters.
8.5. Client-visible and operator-visible errors shall be stable and content-safe and shall not include stored payloads or matching anchors.
8.6. The feature shall not create a new raw-capture path; existing privileged traffic-capture policy remains independently authoritative.
8.7. Observability failure shall not mutate output, retry after commitment, or expose partial state.

### Requirement 9: TDD, Compatibility, and Release Evidence
**Objective:** As a maintainer, I want executable contracts written before behavior, so this cross-cutting feature can be implemented and reviewed without interpretation drift.

#### Acceptance Criteria
9.1. Interfaces, canonical shapes, fixtures, and failing acceptance tests shall be committed before production runners, stores, matching logic, adapter behavior, or feature wiring.
9.2. Each implementation phase shall follow red, green, and refactor sequencing and shall end with focused green tests before broader work proceeds.
9.3. Tests shall cover canonical validation/cloning, rule precedence, exact matching, placement, idempotency, state bounds, expiry, and privacy.
9.4. Runtime tests shall cover disabled non-interference, sequential and recv failover, weighted routes, parallel races, completion-gate replacement, response-hook mutation, cancellation, close, and no retry after output.
9.5. Adapter goldens and parity tests shall cover supported streaming/non-streaming request and response shapes and explicit unsupported paths.
9.6. Race tests shall cover the store and observer lifecycle, and fuzz tests shall cover decoders, canonical reasoning validation, JSON normalization, and anchor construction.
9.7. Release validation shall include focused package tests, `make quality-checks`, `make test`, `make parity-checks`, `make test-race` where supported, and `make qa`.
9.8. Sample configuration and operator documentation shall include opt-in posture, catalog/rule precedence, privacy, process-local durability, limits, failure policies, and migration behavior.
