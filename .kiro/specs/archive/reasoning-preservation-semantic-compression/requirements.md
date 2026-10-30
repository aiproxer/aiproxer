# Requirements Document

## Introduction

Issue #369 asks Go-LIP to reduce the future context and cost of replaying very large preserved textual reasoning without breaking reasoning continuity. The brownfield repository already contains three distinct authorities that must remain separate:

1. `reasoning-output-preservation` owns surfaced-winner capture, bounded session-local reasoning artifacts, exact matching, and later restoration.
2. provider/native continuity paths such as direct Codex own exact encrypted/opaque reasoning and native compaction/checkpoints.
3. the generic auxiliary/background execution, generation pinning, routing, billing, detached-child, and workload-attribution machinery introduced for `compaction-continuity` provides reusable infrastructure for auxiliary inference.

This SDD is a **follow-up**, not a replacement for those completed specifications. It adds only the missing capability: an optional validated semantic replay surrogate for artifact classes whose canonical artifact/dialect profile explicitly permits lossy textual preservation.

The core safety invariant is:

> The original preserved reasoning artifact is always authoritative. Semantic compression may attach an optional replay surrogate; it must never destructively replace, mutate, evict, or become the only copy of the original artifact.

## Relationship to Existing Specifications

This specification does **not** supersede:

- `.kiro/specs/archive/reasoning-output-preservation/`;
- `.kiro/specs/archive/reasoning-preservation-e2e-validation/`;
- `.kiro/specs/archive/openai-responses-reasoning-preservation/`;
- `.kiro/specs/archive/openai-codex-native-compaction/`;
- `.kiro/specs/archive/compaction-continuity-preservation/`.

Those specifications remain historical authorities for the behavior they implemented. This specification depends on their landed contracts where stated and must not duplicate their feature-specific logic.

`compaction-event-detection` is **not** a dependency of this feature. The reusable dependency is the generic auxiliary/background infrastructure that now exists in the repository; semantic reasoning compression must not couple itself to compaction detection or to the continuity-capsule schema.

## Boundary Context

### In scope

- explicit canonical artifact/dialect semantics distinguishing exact/native continuity from semantically compressible textual reasoning;
- optional nested compression policy within reasoning preservation;
- original-first storage plus separately bounded optional surrogate/pending-job state;
- detached, no-tools auxiliary compression using the generic auxiliary routing/execution path and an explicit independently configured compressor route;
- originating-principal billing/admission/accounting for compressor inference;
- source-compatible optional non-blocking background-result inspection and stale-result protection;
- shadow mode and active semantic replay mode;
- destination representability revalidation using existing reasoning replay support before surrogate replay;
- explicit ordinary-text data-egress approval/redaction-or-denial policy before any remote compressor receives preserved reasoning;
- strict raw compressor-response byte bounds before decode plus decoded surrogate bounds after validation;
- per-session and feature-instance aggregate optional-state limits;
- content-free observability and savings measurement;
- deterministic/local compressor implementations only behind the same contract, where their semantic guarantees are explicitly truthful;
- regression and release evidence covering exact/native continuity, parallel/failover lifecycle, billing, privacy, concurrency, reload, performance, source compatibility, and bounded memory.

### Out of scope

- compressing encrypted/opaque provider reasoning;
- rewriting signed reasoning/thinking blocks or signatures;
- modifying native provider compaction/checkpoints;
- replacing Codex Responses Compaction V2 or other provider-native compaction;
- changing reasoning/action/tool ordering;
- summarizing ordinary visible reasoning that is not persisted by reasoning preservation;
- introducing another provider client, another routing engine, another money ledger, or another generic background task runtime;
- durable/distributed reasoning-preservation state in v1;
- inheriting/reconstructing the primary route for the compressor in v1;
- a general-purpose privacy/compliance platform unrelated to this feature;
- claiming semantic equivalence from deterministic truncation, token dropping, or other lossy local transforms that cannot establish it.

## Requirements

### Requirement 1: Preserve Exact and Native Continuity as an Untouchable Baseline

**Objective:** As an operator, I want semantic compression to be structurally incapable of corrupting exact provider continuity.

#### Acceptance Criteria

1. When a preserved reasoning artifact is encrypted, opaque, provider-signed, signature-bearing where mutation invalidates replay, an exact OpenAI Responses reasoning item, native compaction/checkpoint material, or otherwise exact-replay-required, the system shall classify it as non-compressible and shall not submit its payload to a semantic compressor.
2. When an artifact contains both textual fields and exact/native authority, exact/native semantics shall win; readable text shall not make the artifact compressible.
3. Anthropic signed thinking and redacted/opaque thinking shall not be altered, stripped, summarized, or converted as a workaround for size reduction.
4. When direct Codex native context is active, this feature shall not rewrite encrypted reasoning items, continuity markers, native checkpoints, or `/responses/compact` replacement material.
5. When semantic compression is disabled, absent, unsupported, denied, failed, stale, or rejected, existing reasoning-preservation behavior shall remain available unchanged.
6. No generic core package shall infer compressibility from provider-name/model-name string matching; exactness/compressibility shall come from a typed canonical artifact/dialect profile with unknown values failing closed.
7. Architecture tests shall fail if exact/native/signed artifact classes are routed into the semantic compressor or if a provider-specific compressor branch is introduced into generic core orchestration.

### Requirement 2: Define Explicit Replay Semantics Before Compression

**Objective:** As a maintainer, I want one explicit semantic classification so capture, compression, storage, and replay cannot disagree about safety.

#### Acceptance Criteria

1. The implementation shall define a bounded typed canonical artifact/dialect replay/compression classification equivalent to at least: exact replay required, semantic textual replay permitted, and unknown/not applicable.
2. Classification shall derive from canonical reasoning dialect plus artifact structure/presence semantics, not from ad hoc provider/model string checks.
3. In v1, plain historical reasoning such as `openai.chat.reasoning_text.v1` may be classified semantically compressible only when its canonical structure contains ordinary text without signature/opaque/exact authority.
4. OpenAI Responses exact items, Anthropic signed/redacted/opaque reasoning, unknown dialects, contradictory artifact structure, and mixed artifacts without a provably safe textual subset shall fail closed to original/exact replay.
5. The same canonical semantic classification authority shall be used by compressor submission and later surrogate selection.
6. Destination use shall additionally require existing negotiated `ReasoningReplaySupport` to represent the original dialect; no new provider-specific semantic capability field is required for v1 unless implementation evidence proves the canonical profile is insufficient.
7. If implementation discovers a provider that uses the same canonical plain-text dialect but requires exact-byte replay, implementation shall stop and revise the semantic profile contract rather than add provider-name exceptions.

### Requirement 3: Compression Must Be Explicitly Configured, Bounded, and Inert by Default

**Objective:** As an operator, I want a safe opt-in rollout with zero behavior/cost change unless I deliberately enable it.

#### Acceptance Criteria

1. Reasoning preservation shall accept an additive nested `compression` configuration owned by `reasoning-output-preservation`, not an independent feature with duplicate state ownership.
2. When `compression.enabled` is omitted or false, no compressor request, pending compression state, surrogate allocation, compression-specific telemetry, or additional billable inference shall occur.
3. Enabled v1 configuration shall require an explicit independently configured compressor `route` and shall support at least: timeout, `max_input_tokens`, `max_input_bytes`, `max_output_tokens`, **`max_output_bytes` for the raw collected compressor response**, `max_surrogate_bytes`, minimum source size, minimum saved bytes, minimum savings ratio, maximum pending entries per session, maximum surrogate bytes per session, maximum pending entries per feature instance, and maximum surrogate bytes per feature instance.
4. Enabled configuration shall support `shadow` and `active`; mode shall default to `shadow`, and shadow mode shall never substitute a surrogate into a backend request.
5. Unknown compression fields, missing/blank route, non-positive bounds, unsafe maxima, invalid ratio policy, aggregate limits smaller than their corresponding per-session/per-turn limits, or enabled mode without a valid compressor execution path shall fail configuration/generation validation.
6. Compression shall remain off in standard injected/default reasoning-preservation configuration unless an operator explicitly enables it.
7. An explicitly disabled reasoning-preservation feature shall continue to suppress all storage, compression, and replay behavior.
8. Configuration shall not imply data-egress approval merely because a route string is explicit; Requirement 7 privacy/egress policy remains independently mandatory before text leaves the current trust boundary.

### Requirement 4: Original Artifact Must Commit Before Any Compression Work

**Objective:** As a maintainer, I want compression to be an optimization after correctness state exists, never a prerequisite to preserving the response.

#### Acceptance Criteria

1. When a final stream ends with `success_released`, the existing observer shall first validate and append the original `TurnArtifact` using the current authoritative session partition and anchor semantics.
2. Only after the original artifact append succeeds may semantic compression work be considered.
3. If original append fails, is oversized, is ineligible, or the stream outcome is failed/cancelled/closed/replaced/gate-replaced, no compressor work shall be submitted.
4. Parallel-race losers, swallowed retries/failovers, completion-gate-discarded streams, and any B-leg other than the surfaced successful release shall never generate compression work.
5. Compressor submission or optional-state admission failure shall not retroactively fail, delete, rewrite, or invalidate the original artifact or client-visible response.
6. The observer shall not synchronously wait for remote semantic compression before completing its final-stream lifecycle.

### Requirement 5: Store Optional State Non-Destructively With Per-Session and Aggregate Bounds

**Objective:** As an operator, I want semantic surrogates without sacrificing authoritative state or allowing aggregate memory growth across many sessions.

#### Acceptance Criteria

1. A retained artifact shall continue to contain the original reasoning placements and payloads as the authoritative representation.
2. Compression state shall be additive and bounded, representing at most pending compression metadata and/or one validated surrogate for the original artifact revision.
3. A surrogate shall carry enough immutable correlation to prove artifact ID, original anchor/digest/revision, canonical semantic profile, and compression-policy version without exposing reasoning contents in diagnostics.
4. Pending and surrogate attachment shall use compare-and-set/equivalent stale-write protection so late work cannot attach to a replaced, expired, evicted, mismatched, or differently-policy-versioned artifact.
5. Surrogate and pending lifetime shall never exceed the original artifact lifetime; original expiry/eviction shall clear or make all optional compression state unusable.
6. Pending state shall have an explicit per-session count bound and surrogate state explicit per-turn/per-session byte bounds.
7. The feature instance shall additionally enforce aggregate limits across all sessions for total pending compression references and total retained surrogate bytes; exhaustion shall reject new optional state rather than expand unboundedly.
8. Aggregate counters shall be updated atomically with attach/delete/expiry/eviction and shall not drift under concurrent sessions or repeated stale results.
9. Optional compression-state admission shall be independent from authoritative original FIFO byte eviction: pending/surrogate attachment shall never evict or destroy an otherwise-retained original solely to make room for the optimization.
10. Pending-job attachment failure or budget rejection after submission shall cause the retained result to be forgotten when safely possible, while leaving the original artifact intact; incurred provider work remains billable.
11. Tests shall cover multi-session aggregate exhaustion and prove one session cannot bypass feature-instance limits by creating many authoritative session partitions.
12. Process restart behavior shall remain honest: v1 adds no durable/distributed reasoning-compression state beyond the existing reasoning-preservation store contract.

### Requirement 6: Reuse Generic Auxiliary Execution Without Breaking Exported SDK Compatibility

**Objective:** As a maintainer, I want compressor inference to reuse proven infrastructure while preserving source compatibility for existing external SDK implementations.

#### Acceptance Criteria

1. An LLM-based compressor shall execute through `pkg/lipsdk/auxiliary` and the normal Executor/routing/B2BUA/billing path, not through a feature-local provider SDK/client.
2. The feature shall reuse the process-owned bounded auxiliary/background scheduler rather than spawn an unbounded goroutine per artifact or create a second generic task runner.
3. Reasoning compression shall not import/depend on `compactioncontinuity` capsule, source, resultmerge, extractor, detector, or feature semantics; only generic infrastructure may be reused.
4. The auxiliary child shall use detached/private execution, no tools, no primary secure-session turn/transcript/resume mutation, and the explicit compression route.
5. The child request shall disable `reasoning-output-preservation` for itself to prevent recursive preservation/compression.
6. **Existing exported `auxiliary.BackgroundClient` source compatibility shall be preserved.** `Poll`/non-blocking inspection shall not be added as a required method on the existing interface.
7. Non-blocking inspection shall be exposed through a separate optional capability interface (for example `BackgroundPoller`) or a source-compatible adapter that existing `SubmitCollect/Await/Forget` implementations need not implement.
8. Standard process-owned scheduler implementations used by this feature shall implement both existing `BackgroundClient` and the optional poll capability; feature composition shall validate the poll capability when compression is enabled.
9. Compile-time/source-compatibility tests shall prove an external-style implementation satisfying only the historical `BackgroundClient` method set still compiles and remains usable after this feature lands.

### Requirement 7: Protect Ordinary Reasoning Text Before Auxiliary Egress

**Objective:** As an operator, I want plain textual reasoning—which can contain credentials, personal data, proprietary code, or other sensitive content—to receive explicit data-processing policy before it is sent to another model/provider.

#### Acceptance Criteria

1. `SemanticText` classification shall mean only that the reasoning representation is structurally safe for lossy semantic transformation; it shall **not** mean the contents are non-sensitive or approved for external processing.
2. Before any remote/out-of-trust-boundary compressor submission, the selected `compression.route` and originating policy context shall pass a trusted data-egress decision covering the operator's applicable retention, residency, consent/legal-basis, and provider-processing constraints.
3. The policy outcome shall be bounded and equivalent to `allow`, `redact_then_allow`, or `deny`; `deny` shall skip semantic compression and retain/replay the original.
4. Where existing secret/redaction policy is available, this feature shall reuse it rather than invent a second independent secret detector. If policy requires redaction but no trusted sanitizer can satisfy it, the compressor submission shall be denied/fail-open to the original.
5. Redaction shall occur **before** input-size accounting, prompt construction, and provider submission; the compressor shall never receive the unredacted source when policy required redaction.
6. A sanitized input shall retain local segment identity/placement correlation but shall not gain model-visible session/account/credential/lineage identifiers.
7. Explicit route configuration alone shall never be interpreted as consent or data-processing approval.
8. Tests shall cover sensitive ordinary-text reasoning under allow, redact, deny, missing-policy, and route-policy-mismatch scenarios, including proof that denied/unredacted sensitive text is not submitted.
9. This requirement is feature-scoped; it does not require building a general compliance registry beyond the narrow trusted policy/configuration seam needed to decide compressor egress.

### Requirement 8: Keep Control-Plane Metadata Separate From Model-Visible Compressor Content

**Objective:** As a security maintainer, I want auxiliary authorization/correlation metadata preserved without accidentally exposing it to the compressor model.

#### Acceptance Criteria

1. Auxiliary request envelope metadata required by the existing execution path—`Role`, `Visibility`, detached `SessionMode`, parent trace/A-leg/B-leg/branch lineage, and cloned trusted principal/scope in execution context—shall remain available for authorization, routing, correlation, generation ownership, and billing.
2. Those control-plane values shall not be copied into the canonical child `Call.Messages`, compressor JSON payload, or other model-visible content.
3. Content-bearing telemetry/logs shall not include raw principal/account/session/lineage identifiers, reasoning text, surrogate text, signatures, opaque payloads, or secrets; bounded hashes/classes/counts may be used where existing policy permits.
4. `role=reasoning_preservation_compressor`, private visibility, parent lineage, and principal/scope propagation shall be tested as control-plane metadata independently from model-facing prompt inspection.
5. Documentation shall avoid the inaccurate claim that session/account identity is absent from the entire auxiliary request; the correct boundary is that trusted identity remains in control-plane metadata while excluded from model input/content-bearing telemetry.

### Requirement 9: Auxiliary Compression Must Follow Ordinary Billing and Admission

**Objective:** As an operator, I want additional inference to be visible, attributable, and unable to bypass economic controls.

#### Acceptance Criteria

1. Each submitted LLM compressor inference shall be attributed by default to the same authenticated principal/account that caused the originating preserved artifact.
2. Compressor execution shall participate in ordinary credit/exposure admission, routing, usage metering, provider-cost accounting, retry/failover accounting, and terminal settlement.
3. Compressor work shall use a bounded workload identity equivalent to `class=auxiliary` and `role=reasoning_preservation_compressor`.
4. Primary frontend protocol-visible usage shall not falsely include compressor usage as though it were primary output, while account/operator totals shall include incurred compressor usage/cost.
5. If admission rejects the compressor before provider submission, no compressor provider work shall occur and the original artifact shall remain authoritative.
6. Once provider work is submitted, incurred usage shall remain accountable even if the compressor result is invalid, stale, uneconomical, late, denied for adoption, or never used.
7. A future operator-funded compressor policy is out of scope unless separately specified; originating-user attribution is the default contract.

### Requirement 10: Bound and Validate Raw Results Before Decode, Then Validate Decoded Surrogates

**Objective:** As an operator, I want a misbehaving compressor/provider to be unable to allocate or decode an unbounded response and only worthwhile text to become a surrogate.

#### Acceptance Criteria

1. The compressor request shall forbid tools, side effects, and non-text result channels.
2. One artifact shall use at most one auxiliary compressor inference; the request may contain multiple locally indexed eligible reasoning segments so placement can be preserved without one call per segment.
3. The compressor shall return a strict bounded versioned schema containing only expected local segment indexes and textual surrogate values; unknown/missing/duplicate indexes or unknown fields shall be rejected.
4. The collected/raw compressor response shall be subject to `max_output_bytes` and a hard implementation ceiling **before JSON/schema decoding or unbounded string construction**; oversized raw responses shall be rejected without parsing the excess.
5. `max_output_tokens` remains an upstream generation bound, but it shall not substitute for the local raw byte bound because provider accounting/token compliance cannot be trusted as an allocation guard.
6. After decode, each surrogate and the aggregate decoded surrogate payload shall be subject to `max_surrogate_bytes`, structural validation, UTF-8/control validation, and exact segment-index validation.
7. Results containing tool calls, opaque/provider objects, malformed encoding, empty required text, pathological disallowed controls, or output exceeding configured/hard raw or decoded limits shall be rejected.
8. The validator shall enforce minimum saved bytes, minimum savings ratio, and strict smaller-than-source behavior before attachment.
9. A result that is not smaller enough shall be discarded as `insufficient_savings` rather than stored/replayed.
10. Input token/byte bounds, raw output token/byte bounds, decoded surrogate bounds, and timeout shall be hard upper limits independent of model cooperation.
11. Tests shall include an oversized raw response that is syntactically valid only after the configured byte limit, proving the implementation rejects it before JSON decoding.
12. Validation shall not claim semantic equivalence beyond the selected compressor contract; active replay is an operator-approved lossy mode, not a guarantee of identical internal reasoning.
13. A deterministic/local compressor may implement the same interface only if limitations are documented; truncation/token dropping shall not be mislabeled as semantic preservation.

### Requirement 11: Adopt Background Results Without Response or Replay Waiting

**Objective:** As a user, I want compression to reduce future context without making primary response release or ordinary follow-up turns wait for auxiliary inference.

#### Acceptance Criteria

1. Background compressor submission shall return without waiting for compressor completion.
2. The optional background-poll capability shall distinguish `pending`, `completed`, `failed`, and `not_found/expired` without timing tricks or zero-duration race-prone waits.
3. The artifact shall record only a bounded pending job reference plus immutable validation/correlation data needed to adopt the result later.
4. On a later matching replay attempt, the feature may inspect an already-submitted result once without blocking; if pending or poll capability is unavailable, it shall replay the original for that attempt.
5. When a completed result is first observed, the feature shall apply raw-result bounds, parse/validate, CAS-attach a surrogate if still current and budgets permit, forget/release the background result, and then apply shadow/active policy.
6. Compression-specific poll/result/store errors shall fail open to the original and shall not be mapped onto authoritative reasoning `on_state_error=reject` behavior.
7. V1 shall not add an auxiliary completion callback that mutates retired feature state asynchronously and shall not add a feature-owned maintenance goroutine solely to consume results.
8. Any future bounded wait-before-replay optimization requires separate evidence/configuration and is not required for v1.

### Requirement 12: Surrogate Replay Must Revalidate the Destination Candidate and Preserve Placement

**Objective:** As a maintainer, I want a surrogate created safely at capture time to be used only where the destination can legally represent the same semantic-text dialect.

#### Acceptance Criteria

1. Existing reasoning-preservation `AttemptTransform` shall remain the owner of historical reasoning reinjection.
2. Before substitution, the transform shall re-evaluate the original artifact/segment with the same canonical semantic classifier used at submission and verify destination `ReasoningReplaySupport` represents that dialect.
3. If artifact profile is exact/unknown, destination cannot represent the dialect, correlation is stale/invalid, or policy no longer permits the surrogate, the transform shall use the original or existing unrepresentable policy; it shall never force the surrogate.
4. Shadow mode shall always replay the original even when a valid surrogate exists.
5. Active mode shall substitute only eligible textual reasoning payloads while preserving existing `BeforeNonReasoningPart` placement and all non-reasoning structure.
6. In mixed turns, only explicitly compressible textual reasoning may use surrogates; exact/signed/opaque parts shall remain byte/structure equivalent to original and shall never enter compressor input.
7. Tool calls, tool outputs, IDs, signatures, images/files, ordinary assistant text, and reasoning/action/observation ordering shall remain unchanged by surrogate substitution.
8. Client-supplied reasoning shall continue to win according to existing classification; compression shall not overwrite reasoning the client already preserved.

### Requirement 13: Shadow Evidence and Repository Certification Must Precede Active Rollout

**Objective:** As an operator, I want evidence of safety and value before backend-visible replay changes.

#### Acceptance Criteria

1. In shadow mode, eligible artifacts may be compressed/validated, but every backend replay shall still use the original artifact.
2. Content-free observability shall record bounded original size, raw-result size, decoded surrogate size, hypothetical saved bytes/tokens, compressor latency, categorical outcome, and ordinary billing/workload evidence without logging contents.
3. Outcome taxonomy shall distinguish at least: exact/ineligible, privacy denied/redacted, below threshold, submitted, aggregate-budget denied, queue saturated, admission denied, raw oversize, decode invalid, insufficient savings, stale/evicted, shadow-ready, active-used, and original fallback.
4. Active mode shall remain explicitly configured and shall not become a default merely because implementation completes.
5. Release evidence shall include deterministic/fake-backend validation plus a shadow dataset/evaluation showing compression ratio, raw-bound rejection, aggregate-budget behavior, privacy-policy behavior, and failure fallback. Quality claims require separate semantic/agent-task evaluation.
6. Tests shall prove no retry/failover occurs after downstream content commitment and semantic compression never becomes retry authority for the primary request.
7. Tests shall prove process reload/shutdown cannot orphan retained generation ownership or corrupt optional-state counters; race/goleak/fuzz tests shall cover poll, stale completion, parser limits, and multi-session aggregate budgets.
8. Architecture tests shall prevent provider SDK leakage into core, direct provider compressor clients, feature-owned billing ledgers, second transcript stores, duplicate background runtimes, mandatory `Poll` expansion of exported `BackgroundClient`, and coupling to compaction-continuity feature semantics.
9. Disabled-mode performance shall remain effectively equivalent to existing reasoning preservation; shadow/active overhead shall be bounded and measured.
10. Validation shall use canonical semantic/exact fixtures and existing protocol/routing lifecycle suites rather than provider-by-provider Cartesian compatibility matrices.
11. If active request/terminal-pipeline simplification specs merge before implementation, this SDD shall be revalidated against current `main` before production work starts.

## Implementation Readiness

Requirements are generated and internally reconciled for brownfield implementation planning, but they remain intentionally unapproved in `spec.json`. Implementation must follow the dependency order in `tasks.md`, with backend-visible semantic substitution prohibited until the shadow capture/adoption path and all safety contracts above are green.