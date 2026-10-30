# Requirements Document

## Introduction

This specification makes OpenAI Responses reasoning-output preservation production-grade. The shipped `reasoning-output-preservation` feature already provides default-on, catalog/rule-gated capture and restore with bounded process-local `TurnStore`, Chat and Anthropic HTTP evidence, and hard dialect `reject`/`log_skip` controls. OpenAI Responses remains incomplete: the backend does not ingest provider Responses reasoning stream/output items into canonical exact artifacts; the frontend synthesizes Responses reasoning from text deltas; nonstream collection drops opaque metadata; and Responses reference harnesses lack stateful reasoning replay E2E.

This work extends canonical stream/collection contracts with a minimal dialect-aware exact-part carrier, maps Responses provider events into that carrier, captures and restores exact Responses opaque envelopes, fail-closes Responses replay encoding without summary-only fallback, and proves Chat/Responses combination cells with precise positive vs cross-dialect-negative expectations. Historical completed specs remain historical references only.

**Primary handoff:** `EchoesVault/daily/2026-07-19.md`.
**Spec state:** generated, **not approved**, not ready for implementation.

## Boundary Context

- **In scope**: dialect-aware canonical exact reasoning-part stream/collection extension; OpenAI Responses backend ingestion (streaming + completed equivalence/dedupe + output_index ordering); exact feature capture/store/restore for `openai.responses.reasoning_item.v1`; exact Responses replay encoder fail-closed posture with transform/state_error ownership; Responses frontend stream/nonstream fidelity and input round trip; Responses ref harness; HTTP combination matrix with asymmetric cell meanings; cross-dialect reject/log_skip; default gating/opt-out/inert; privacy/bounds/cancellation/no-retry; fuzz/race/matrix/soak/release evidence; docs/EchoesVault updates only after gates pass.
- **Out of scope**: pairwise Chat<->Responses translators or reasoning conversion; summary-only or lossy synthesis; durable/distributed artifact storage; generating unobserved reasoning; fuzzy matching; Anthropic semantic redesign; provider SDK types outside adapters; a second backend nonstream execution path; claiming Responses coverage before release gates.
- **Adjacent expectations**: preserve immutable-baseline attempt derivation, B2BUA lineage, capability negotiation, completion gates, no-retry-after-first-output, existing Chat/Anthropic preservation, standard default-on catalog injection with unmatched complete no-op, backend-always-streaming architecture.
- **Boundary ownership**: canonical event/collection in `pkg/lipapi` (+ SDK collect helpers as needed); Responses wire mapping in FE/BE adapters; capture/match/restore/TurnStore/catalog/inert in feature plugin; HTTP/ref harness in testkit/ref*/stdhttp.
- **Revalidation triggers**: canonical events/collection, reasoning dialects, adapter parity, final-stream observation, restore dialect checks, stream cancellation, diagnostics/privacy, HTTP matrix/soak.

## Approved Product Decisions (immutable without user)

1. Exact opaque Responses replay only: complete item `id`, required `summary` array, optional `content` array, optional nullable `encrypted_content`; no summary-only fallback; no lossy synthesis.
2. Combination coverage includes Chat FE/Chat BE, Responses FE/Chat BE, Chat FE/Responses BE, Responses FE/Responses BE — with **asymmetric positive criteria** and separate cross-dialect negatives (Requirement 7).
3. No pairwise translators; canonical middle only; cross-dialect uses existing `reject`/`log_skip`; no Chat<->Responses conversion.
4. Feature remains default-on but catalog/rule-gated; unmatched requests fully no-op after eligibility (no event parse/buffer, store I/O, mutation, outcomes/telemetry).
5. State remains bounded process-local TurnStore; durability out of scope unless separately approved.
6. Streaming primary; backend Open is always streaming; frontend nonstream collects the same canonical stream; no retry/failover after first output.

## Compatibility Requirements

- Existing Chat text and Anthropic thinking/redacted paths shall remain behaviorally valid.
- `EventReasoningOpaqueDelta` shall remain the Anthropic redacted_thinking carrier.
- Standard injection, catalog `compatible-auto.v2`, explicit opt-in/opt-out, and inert unmatched semantics shall not regress.
- Hard `reasoning_replay` capability semantics shall remain non-downgradeable.

## Requirement ID Convention

Acceptance criteria use stable **`N.M`** IDs for design/tasks/evidence traceability.

---

## Requirements

### Requirement 1: Dialect-aware canonical exact-part carrier

**Objective:** As an adapter and feature implementer, I want a minimal dialect-aware canonical carrier for complete exact reasoning parts, so Responses opaque envelopes and Anthropic opaque bytes remain distinct and collectible without duplicate Chat+Responses parts from one provider item.

#### Acceptance Criteria

**1.1.** The canonical model shall represent a terminal exact historical reasoning part that includes a `ReasoningDialect` and the dialect payload fields already defined for `ReasoningPart` (text/signature/opaque as applicable).

**1.2.** The carrier shall not overload `EventReasoningOpaqueDelta` as a Responses exact-item envelope; Anthropic redacted_thinking semantics shall remain distinct and documented.

**1.3.** Bare `EventReasoningDelta` events shall remain Chat/Anthropic progressive text carriers. Responses provider progressive summary/text updates shall not be emitted as bare `EventReasoningDelta` that the feature observer would capture as `openai.chat.reasoning_text.v1`.

**1.4.** If progressive Responses client UX is emitted on the canonical stream, those events shall be presentation-only (dialect-tagged as `openai.responses.reasoning_item.v1` or an equivalent non-capturable marker defined in design), the feature observer shall ignore them for artifact capture, and the Responses frontend shall finalize a single wire reasoning item from the terminal exact part without emitting a duplicate item.

**1.5.** Nonstream collection (frontend collect over the canonical stream) shall retain ordered exact reasoning parts (including opaque envelopes) sufficient to rebuild the same `ReasoningPart` sequence later; text-only aggregation shall not be the sole retained form when exact parts were emitted.

**1.6.** Canonical contracts shall remain free of provider SDK and transport types.

**1.7.** Canonical cloning, validation bounds, sizing/counting inputs, sequence validators, and fuzz targets shall account for the new/extended carrier without breaking existing event sequence rules for Anthropic/Chat carriers.

**1.8.** Content-class sequencing shall treat exact-part emissions as content-class events consistent with other reasoning carriers, without enabling post-output retry.

### Requirement 2: OpenAI Responses backend ingestion

**Objective:** As a backend adapter, I want provider Responses reasoning stream and completed output mapped into canonical exact parts with correct ordering and failure timing, so the feature can observe what the provider actually returned.

#### Acceptance Criteria

**2.1.** When the OpenAI Responses backend receives `response.output_item.added` / `done` for a reasoning item, the adapter shall assemble the exact item fields needed for dialect `openai.responses.reasoning_item.v1`.

**2.2.** When the adapter receives `response.reasoning_summary_part.added/done`, `response.reasoning_summary_text.delta/done`, and/or `response.reasoning_text.delta/done`, it shall incorporate those updates into mapper-private assembly without inventing missing fields and without emitting bare Chat-capturable `EventReasoningDelta` (see 1.3–1.4).

**2.3.** When `response.completed` contains reasoning output items, the adapter shall emit equivalent canonical exact parts for any item not already emitted from incremental assembly (completed fallback).

**2.4.** Incremental and completed paths shall be equivalent after dedupe: the same provider item id shall not yield duplicate replayable exact parts.

**2.5.** Exact parts shall preserve semantic presence for allowlisted envelope fields (Requirement 2.10) using output `respjson` presence on ingest: absent vs JSON null vs value shall not be silently coerced.

**2.6.** Assembly and emission shall be keyed by provider `output_index` (and item id). The adapter shall test the assumption that reasoning `output_item.done` for index i is observed before content-class events for index > i against ref/SDK fixtures. If interleaving occurs, the adapter shall use a bounded reorder buffer that delays emission of higher indices until lower indices resolve, and shall never rewrite already-emitted canonical events.

**2.7.** If an ordering hole cannot be resolved without rewriting an already-emitted stream, or buffer bounds are exceeded after downstream content has been released, the adapter shall end the stream with a classified terminal error, discard pending exact artifacts for that attempt, and shall not trigger retry/failover.

**2.8.** Incomplete in-flight assembly shall not be emitted as a terminal exact part.

**2.9.** Mapping shall live in adapter/shared Responses stream packages only; core and `pkg/lipapi` shall not import `openai-go`.

**2.10.** The validated Opaque envelope allowlist shall be: required non-empty string `id`; required array `summary`; optional array `content` (absent vs present); optional nullable string `encrypted_content` (absent vs null vs string); `type` must be `reasoning` (default/preserve); optional `status` when present (`in_progress`|`completed`|`incomplete`). Unknown fields shall fail validation (fail-closed). Arbitrary JSON `summary`/`content` values that are not arrays shall fail validation.

**2.11.** When a malformed/oversize envelope is discovered before any content-class event for the attempt has been released downstream, the adapter may fail the attempt open with a classified error. When discovered after any content-class event has been released, the adapter shall emit a stream terminal error path only (2.7), never candidate retry/failover language or behavior.

### Requirement 3: Exact capture, TurnStore, and restore

**Objective:** As an operator, I want exact Responses reasoning parts captured and restored under existing feature rules, so multi-turn quality is preserved without durability or inert regressions.

#### Acceptance Criteria

**3.1.** When eligible and action allows capture, the feature shall persist a complete `openai.responses.reasoning_item.v1` `ReasoningPart` (exact opaque envelope) into the bounded process-local TurnStore after `success_released`, not summary text alone.

**3.2.** Stored parts shall retain recorded positions among text/tools based on the canonical stream order actually observed (emission order after ordering rules in 2.6).

**3.3.** Incomplete, invalid, or over-bound envelopes shall never become replayable artifacts; bounded outcomes shall be content-safe.

**3.4.** Capture shall be idempotent for the same released turn and concurrency-safe at the session boundary.

**3.5.** Artifacts shall remain isolated to the authoritative session/feature instance; reads/writes shall use defensive copies.

**3.6.** Restore shall inject exact parts only for uniquely `missing` turns when the candidate represents the required dialect(s); it shall never convert Chat/Anthropic parts into Responses envelopes or the reverse.

**3.7.** When a candidate cannot represent a required dialect, the system shall apply configured `reject` or `log_skip` only (`on_unrepresentable`).

**3.8.** After eligibility resolution, unmatched requests shall be completely no-op: no event parse/buffer, no store I/O, no mutation, no feature telemetry/outcomes.

**3.9.** Durability across process restart or multi-replica sharing shall not be claimed or required.

**3.10.** Telemetry/inventory/errors shall remain content-safe (no reasoning bodies, envelopes, anchors, or partitions).

**3.11.** The feature observer shall ignore presentation-only Responses progressive events (1.4) and shall not create a Chat text reasoning part from the same provider Responses item that also yields a terminal exact Responses part.

**3.12.** When a restored or stored Responses envelope fails structural/presence validation at transform time, the feature shall apply `on_state_error` (reject/log_skip per config) with a content-safe error/outcome and shall not submit a partially restored call.

### Requirement 4: Exact Responses replay encoder

**Objective:** As a backend adapter, I want fail-closed exact replay encoding that preserves presence semantics under SDK constraints, so historical Responses reasoning is never silently degraded.

#### Acceptance Criteria

**4.1.** When encoding historical assistant reasoning for OpenAI Responses backend requests, the adapter shall require a validated opaque envelope for dialect `openai.responses.reasoning_item.v1`.

**4.2.** The encoder shall preserve semantic presence for allowlisted fields (2.10), including `encrypted_content` absent vs null vs string and optional `content` absent vs present array. Preservation means the request JSON item matches stored semantics; byte-identical re-serialization of insignificant whitespace is not required.

**4.3.** Replay encoding shall use an adapter-owned strategy: embed the validated stored Opaque object into the request `input` array (preferred), or map fields through SDK param constructors using omit / `param.Null[string]()` / `param.NewOpt` equivalents. The encoder shall not use `ResponseReasoningItem.ToParam()` for exact replay. The encoder shall not silently coerce null to absent or absent to null.

**4.4.** If the pinned SDK path cannot emit a stored presence form, the adapter shall fail closed as unrepresentable/state_error (content-safe) rather than lossy rewrite.

**4.5.** The encoder shall preserve structural ordering of reasoning items relative to other input items.

**4.6.** If the envelope is missing, incomplete, wrong-dialect, or fails validation at `ParamsForCall`, the adapter shall fail closed with a content-safe classified error and shall not succeed via summary-only or synthesized-ID fallback. This path is last-line defense; transform/`on_state_error` (3.12) should prevent most invalid envelopes from reaching Open.

**4.7.** Existing unit coverage that currently permits text/summary fallback success shall be updated so fallback success is forbidden under this specification.

### Requirement 5: Responses frontend fidelity and input round trip

**Objective:** As a client of the Responses frontend, I want stream and nonstream outputs and inputs to preserve exact reasoning items, so round trips do not invent IDs or drop opaque fields.

#### Acceptance Criteria

**5.1.** When encoding canonical exact Responses parts to the client, the Responses frontend shall emit provider-legal reasoning items from the exact envelope rather than synthesizing IDs/summary from text-only deltas when an exact part exists.

**5.2.** Frontend nonstream mode shall collect the same backend-produced canonical stream (backend remains streaming) and shall include the same exact envelope fields as stream mode for terminal exact parts.

**5.3.** When a client submits historical Responses reasoning items as input, the frontend decode path shall produce canonical `ReasoningPart` values with dialect `openai.responses.reasoning_item.v1` and exact opaque envelopes satisfying 2.10.

**5.4.** Frontend decode/encode round trips shall preserve semantic presence for optional/nullable allowlisted fields (4.2).

**5.5.** Text-delta synthesis paths, if retained for cases with no exact part, shall not claim or produce Responses exact replay artifacts.

**5.6.** Cross-dialect outputs shall not convert Anthropic/Chat reasoning into Responses exact items.

**5.7.** When presentation-only progressive events are used (1.4), the frontend shall not emit two wire reasoning items for one provider item after the terminal exact part is applied.

### Requirement 6: Reference harness for stateful Responses reasoning replay

**Objective:** As a maintainer, I want deterministic Responses refbackend/refclient/testkit support, so exact replay can be proven without live providers.

#### Acceptance Criteria

**6.1.** The OpenAI Responses refbackend shall support deterministic stateful scripts that emit reasoning output items (with `output_index`), summary/text deltas, and completed payloads, including fixtures that exercise ordering assumptions and bounded-buffer edge cases.

**6.2.** The OpenAI Responses refclient (or test driver) shall support client policies that drop, preserve, or conflict reasoning on later turns and resubmit exact items when preserved.

**6.3.** Oracles shall assert exact semantic presence for `id`/`summary`/`content`/`encrypted_content` (and `status` when stored) and ordering using content-safe failure traces.

**6.4.** Harness coverage shall include reasoning-only, text+reasoning, tools+reasoning, and multi-turn flows.

**6.5.** Harness packages shall remain test-only and shall not become production dependencies of core.

### Requirement 7: HTTP combination matrix and controls

**Objective:** As a release engineer, I want HTTP proof across Chat/Responses combinations with precise positive vs negative expectations, so production claims are evidence-based.

#### Acceptance Criteria

**7.1.** The suite shall include FE/BE topology cells: Chat/Chat, Responses/Chat, Chat/Responses, Responses/Responses.

**7.2.** **Positive same-dialect cells:** Chat FE/Chat BE shall prove Chat-dialect capture/restore. Responses FE/Responses BE shall prove exact Responses provider output -> canonical exact artifact -> TurnStore -> later exact Responses replay.

**7.3.** **Chat FE / Responses BE (asymmetric positive):** Capture Responses exact parts from the Responses backend. The Chat frontend is not required to expose the opaque Responses item to the client. The stateful client typically omits reasoning on later turns; anchors are computed from Chat-visible non-reasoning content. Restore shall inject Responses exact parts for the Responses backend candidate. This cell does not require Chat FE opaque round-trip.

**7.4.** **Responses FE / Chat BE (asymmetric positive):** Positive restore/replay applies only to **Chat** dialect artifacts captured from the Chat backend, and only if the Responses frontend can legally present that Chat reasoning text to the client without minting a fake Responses exact envelope. If the stored artifact is Responses dialect and the candidate is Chat-only, the cell is negative under 7.5.

**7.5.** **Cross-dialect negatives:** Route/candidate changes that require Chat<->Responses conversion shall prove `reject` and configured `log_skip` with no conversion. These are not positive matrix successes.

**7.6.** Each positive cell shall cover streaming and frontend-nonstream collect where the frontend protocol supports both; backend remains streaming in all cases.

**7.7.** Scenarios shall include reasoning-only, text+reasoning, tools+reasoning, and multiple turns where applicable to the cell.

**7.8.** Controls shall cover default catalog gating, explicit opt-in, explicit opt-out, unmatched inert no-op, authoritative session isolation, anchor exactness, process-local restart loss expectations, and no-retry-after-first-output.

**7.9.** Existing Chat HTTP controls shall not regress.

### Requirement 8: Privacy, bounds, errors, cancellation, and no-retry

**Objective:** As an operator, I want safety invariants preserved while Responses fidelity is added.

#### Acceptance Criteria

**8.1.** Ordinary logs, metrics, inventory, and errors shall not include reasoning bodies, opaque envelopes, signatures, anchors, or session partitions.

**8.2.** Configured byte/turn/TTL bounds shall apply to Responses exact parts; oversize shall discard pending artifacts with content-safe outcomes rather than silently truncate envelopes.

**8.3.** Stream cancellation, early close, failure, gate replacement, parallel losing arms, and post-output stream terminal errors (2.7/2.11) shall discard pending artifacts and shall not enable retry/failover after first output.

**8.4.** Adapter/feature failures shall not submit partially restored calls.

**8.5.** Goroutine ownership on new stream assembly/reorder-buffer paths shall be explicit and leak-free under cancellation tests.

### Requirement 9: Hardening, performance, and soak evidence

> [!warning] DEPRECATED
> The original mandatory Linux-race and full-wide-soak interpretation was superseded for closeout by human release-owner decision on 2026-07-28. Deterministic local gates, targeted fuzz, ordinary concurrency coverage, bounded-retention regressions, and selected 100-turn soak seeds are accepted; Linux `-race`, full 1000×100 soak, and live-provider smoke are waived and were not executed.

**Objective:** As a maintainer, I want proportionate fuzz/concurrency/benchmark/matrix/soak evidence before claiming Responses production coverage.

#### Acceptance Criteria

**9.1.** Unit, golden, and conformance tests shall cover mapper assembly/dedupe/ordering, envelope validation/presence, FE/BE encode-decode, raw-JSON/Opt-Null replay, and feature capture/restore for Responses dialect including presentation-ignore rules.

**9.2.** Fuzz targets shall cover new/extended parsers, envelope validators, and/or collect paths where practical, with a targeted ~30s gate before release claim.

**9.3.** Concurrency-sensitive paths shall have deterministic cancellation, ownership, and ordinary concurrency coverage. Linux race execution is recommended but is not a closeout prerequisite when explicitly waived by the human release owner.

**9.4.** Hot ingest/encode/reorder paths shall have benchmarks or allocation checks if regressions are suspected.

**9.5.** A deterministic seeded matrix and an env-gated soak (or documented soak with minimum local smoke) shall exercise Responses-inclusive scenarios consistent with Requirement 7 cell meanings.

**9.6.** Optional live provider smoke may exist behind env gates but shall not be required for local green.

### Requirement 10: Release gates and documentation honesty

**Objective:** As a release owner, I want explicit gates and honest docs, so Responses coverage is never claimed early.

#### Acceptance Criteria

**10.1.** Before claiming Responses production-grade coverage, focused package tests, applicable quality/unit/parity/QA gates, targeted fuzz, deterministic matrix coverage, and proportionate mixed-protocol soak/smoke evidence shall pass. Linux race, full 1000×100 soak, and live-provider smoke may be waived by explicit human release-owner decision; a waiver shall record that omitted gates were not executed.

**10.2.** Docs and EchoesVault shall stop stating that OpenAI Responses HTTP E2E / fidelity is deferred only after 10.1 evidence exists.

**10.3.** Operator docs shall describe exact-only Responses posture (allowlisted envelope + semantic presence), asymmetric combination cells, dialect mismatch controls, default gating/opt-out, process-local TurnStore non-durability, unmatched inert no-op, and backend-always-streaming / frontend-collect nonstream.

**10.4.** Historical parent/E2E specs shall remain unmodified historical references; this spec is the active contract for Responses fidelity claims.

**10.5.** Implementation shall not begin until `requirements` and `design` are human-approved in `spec.json` and `ready_for_implementation` is true.
