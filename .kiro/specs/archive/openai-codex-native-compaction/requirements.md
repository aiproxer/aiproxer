# Requirements Document

## Introduction

The direct `openai-codex` backend can now transport exact OpenAI Responses reasoning items and compaction items through the canonical/backend-plugin boundary. That codec work is necessary but not sufficient to reproduce the context behavior used by Codex CLI and described by OpenAI's ARC-AGI-3 harness analysis.

Codex CLI requests `reasoning.encrypted_content` on normal sampling requests, records completed native response items—including reasoning—into durable thread history, rebuilds every later prompt from that item history, and applies native compaction to the reasoning-complete history. Its WebSocket `previous_response_id` optimization is turn-scoped; exact encrypted item history is the durable cross-turn foundation.

This specification therefore expands native compaction into a coordinated **native context continuity** feature:

1. request, capture, and restore exact encrypted reasoning across model invocations;
2. preserve native reasoning/tool/action ordering;
3. create and replay provider-bound compaction checkpoints from reasoning-complete history; and
4. measure quality, not only request size and token savings.

The feature is part of the current direct-Codex production scope. Native context is default-on for direct `openai-codex`: omitted `native_context` enables encrypted-reasoning preservation and automatic native compaction. Operators may explicitly opt out. Automatic reasoning restoration uses the existing `reasoning-output-preservation` feature through a standard backend-specific Codex rule and a bounded eligibility marker. Full client history remains the authoritative fallback.

## Boundary Context

- **In scope**: direct `openai-codex` HTTP backend; exact reasoning request/capture/replay; explicit integration with `reasoning-output-preservation`; action-level item ordering; provider-native history construction; Responses Compaction V2; account/model/session-bound checkpoints; WebSocket continuation interaction; usage/quality evidence; configuration and rollout safety.
- **Out of scope**: `openai-codex-app-server`; client-facing `/responses/compact`; canonical generic compaction; decryption or semantic inspection of ciphertext; cross-provider/model/account replay; durable distributed checkpoint storage; automatic HTTP response-ID chaining; changing general routing/failover semantics.
- **Adjacent expectations**: preserve PR #235 exact-item support; keep `openresponses-api-support` authoritative for portable compaction; retain the existing surfaced-output ownership of `reasoning-output-preservation`; preserve no-retry-after-output.
- **Boundary ownership**: optional Codex backend connector plus the existing reasoning-preservation feature and its internal matching contracts; no new canonical item kind or core routing concept.
- **Revalidation triggers**: canonical reasoning dialect; backend-plugin ABI; attempt-transform ordering; surfaced-output observation; managed OAuth rotation; WebSocket continuation; prompt-cache identity; usage accounting; configuration composition.

## Requirements

### Requirement 1: Default-On Configuration and Safe Modes

**Objective:** As an operator, I want native context enabled by default for direct Codex while retaining explicit, complete opt-outs and independent controls for safe operations and evaluation.

#### Acceptance Criteria

1. For a direct `openai-codex` connector, omitting `native_context` shall activate full native context: encrypted-reasoning requests, required surfaced-reasoning continuity, and automatic native compaction.
2. `native_context.enabled: false` shall be a complete native-context opt-out: it shall disable automatic encrypted-reasoning request shaping, automatic reasoning restoration activation for that backend, compaction planning/execution, checkpoint allocation, and continuation reset caused by compaction.
3. Exact client-supplied compatible reasoning replay already supported by the connector shall remain unaffected by `native_context.enabled: false`.
4. An explicit `native_context` block shall expose separate controls for encrypted-reasoning requests, reasoning-continuity mode, and compaction enablement. `compaction.enabled: false` shall disable only automatic compaction/checkpoints while retaining explicitly enabled reasoning controls.
5. The default full mode shall require the trusted surfaced-continuity marker before creating a compaction checkpoint; missing eligibility shall use the full-history fallback.
6. Configuration shall support reasoning-only, compaction-only evaluation, both, and neither through explicit controls without changing the default-on production semantics.
7. If a setting is negative, internally inconsistent, above a hard safety bound, or enabled for `openai-codex-app-server`, the connector shall reject configuration before serving. App-server native context remains unsupported/off.
8. While compaction is disabled, the connector shall not issue a compaction request, allocate checkpoint state, rewrite history with a compaction item, or reset continuation because of compaction.
9. Diagnostics shall expose effective mode, source/precedence, and bounded numeric settings without exposing prompts, session keys, reasoning envelopes, or ciphertext.
10. Runtime close or replacement shall clear connector-private checkpoint and cooldown state.

### Requirement 2: Encrypted Reasoning Request and Exact Item Fidelity

**Objective:** As a long-running agent client, I want every eligible model invocation to return replayable private reasoning state, so later steps do not reconstruct strategy from visible text alone.

#### Acceptance Criteria

1. When an eligible Codex attempt has reasoning continuity enabled, the connector shall request `reasoning.encrypted_content` even when the caller did not explicitly set `reasoning_effort`.
2. The connector shall send a valid reasoning request object using the selected model's supported/default reasoning controls rather than inventing an unsupported effort.
3. Existing explicit caller or route reasoning-effort overrides shall retain precedence.
4. When the backend emits a completed OpenAI Responses reasoning item, the connector shall preserve the allowlisted bounded envelope exactly and emit the existing `openai.responses.reasoning_item.v1` canonical dialect.
5. When a canonical request contains a compatible exact reasoning part, the connector shall replay the original envelope rather than synthesizing text or summary fallback.
6. When the dedicated unary compaction response returns a retained output list and completed `compaction_summary`, the connector shall preserve the complete bounded replacement list and summary envelope.
7. The connector shall treat `encrypted_content` as opaque and shall not decrypt, decode, summarize, edit, log, audit, or tokenize it as ordinary text.
8. Malformed, oversized, duplicate, unsupported, or incomplete reasoning/compaction items shall not enter retained state.
9. Opaque replay items shall remain bound to the Codex implementor, selected account, exact model lineage, and compatible compaction hash when available.
10. Errors and diagnostics shall identify only stable validation categories and item counts.

### Requirement 3: Surfaced-Response Reasoning Continuity

**Objective:** As an agent user, I want private reasoning retained across requests and tool actions, so the model can build on prior strategy instead of repeatedly rediscovering it.

#### Acceptance Criteria

1. Automatic cross-request reasoning retention shall use the existing `reasoning-output-preservation` feature rather than a connector-local store that cannot distinguish surfaced winners from swallowed or losing attempts.
2. Only reasoning observed from the successful surfaced B-leg after response hooks and completion gates shall become restorable state.
3. The standard runtime shall install and activate a backend-specific direct-Codex reasoning-preservation rule by default; the rule shall not rely on a broad GPT matcher or version ceiling.
4. An explicit operator feature row shall take precedence over the injected rule: a matching disabled feature row shall opt out, while an explicit enabled rule may customize policy. The backend's explicit `native_context.enabled: false` remains a complete local opt-out and shall not be overridden by the feature row.
5. When the rule is eligible, the attempt transform shall mark the candidate call with a bounded internal continuity marker before backend open.
6. When a later exact assistant trajectory omits previously observed reasoning and matches one unique artifact, the feature shall restore exact reasoning at its recorded positions.
7. Client-supplied exact reasoning shall be preserved and shall never be overwritten, duplicated, or reordered.
8. Ambiguous, conflicting, unmatched, expired, oversize, or state-error cases shall follow configured reject/log-skip policy and shall not guess.
9. Session partitioning shall use authoritative session identity and shall not trust client-only session hints.
10. Restart, rebalance, expiry, or another feature instance shall produce a state miss rather than fabricated continuity.
11. The continuity marker shall be removed or ignored before provider serialization and shall never be forwarded upstream.
12. Native compaction in required-continuity mode shall not run unless the eligible continuity marker is present.
13. Exact reasoning capture/replay shall continue after a compaction checkpoint is installed.

### Requirement 4: Native Action-Trajectory Ordering

**Objective:** As a model, I want reasoning, actions, and observations replayed in their original order, so prior plans remain causally aligned with tool outcomes.

#### Acceptance Criteria

1. The system shall preserve reasoning placement relative to non-reasoning assistant parts.
2. A trajectory containing reasoning followed by a function call shall replay the reasoning item before that function call.
3. A trajectory containing reasoning, function call, function output, and later reasoning shall preserve that complete order.
4. Multiple reasoning items in one assistant trajectory shall remain distinct and ordered.
5. Function-call and output identities shall remain paired across restore, compaction planning, and replay.
6. If the current request exposes no callable tools, ordinary generation may retain its existing safe text projection, but native compaction planning shall use a separate exact-history view before that projection.
7. If exact structured history cannot be constructed without an orphan call/output or unsupported item, the connector shall skip compaction or fail before upstream work according to hard-limit policy.
8. Rollback, edit, fork, truncation, or reordered client history shall invalidate restoration and checkpoint-prefix matching.
9. Tests shall cover action-level reasoning continuity, not only final assistant messages.
10. The exact-history representation shall remain connector/feature private and shall not create a new public canonical authority.

### Requirement 5: Model Metadata and Compaction Planning

**Objective:** As an operator, I want compaction triggered from model-aware limits and reasoning-complete effective history, so it runs only when beneficial and semantically safe.

#### Acceptance Criteria

1. The connector shall preserve `auto_compact_token_limit` and `comp_hash` from the Codex model catalog when provided.
2. Trigger precedence shall be explicit override, catalog threshold, then conservative context-window fraction.
3. The planner shall apply the named `CodexHarnessHeadroomV1` policy. Exact catalog/model metadata wins for headline hard context and provider auto-compact threshold; the planner still enforces a conservative usable ceiling and safe trigger below it.
4. When exact catalog metadata is unavailable, the planner shall use the exact `gpt-5.3-codex-spark` fallback rule: headline hard context `128000`, usable ceiling `96000`, and safe trigger `80000` tokens. It shall not treat the 128K headline as fully usable.
5. For other GPT-5.x models without exact catalog metadata, the planner shall use a conservative usable ceiling of `250000` tokens after harness/tooling/glue/output reservation and a safe trigger of `220000` tokens. This fallback shall not widen eligibility matching.
6. Explicit trigger overrides shall be validated against the usable ceiling, retained window, and named headroom; invalid or unsafe overrides shall fail configuration rather than silently exceed the safe trigger.
7. Before planning compaction, the connector shall operate on the candidate call after reasoning restoration and continuity marking.
8. The planner shall estimate effective upstream input after any valid existing checkpoint rewrite.
9. The compactable prefix shall exclude the latest live instruction turn: the most recent user message and every later item shall remain verbatim.
10. The split shall not cross function-call/output pairing or divide one assistant action trajectory.
11. If no safe prefix exists, the live tail alone is too large, or expected savings are below a configured minimum, the connector shall bypass automatic compaction.
12. The planner shall allow at most one compaction attempt in flight per lineage.
13. A compaction-hash change or model downshift shall invalidate reuse and may require compaction under the previous compatible model before switching, subject to live endpoint support.

### Requirement 6: Native Compaction Request and Replacement History

**Objective:** As a maintainer, I want validated reasoning-aware checkpoints, so later turns can replace old history without losing learned strategy.

#### Acceptance Criteria

1. Where enabled and planned, the connector shall issue a pre-output dedicated `POST /responses/compact` request using the unary compaction contract; it shall not append a streamed compaction trigger to that request. The legacy streamed trigger parser remains compatibility-only.
2. The compaction input shall be the exact reasoning-complete native history view, not the unaugmented client transcript or no-tools text projection.
3. The request shall use the same selected account, model, instructions, tools, prompt-cache identity, reasoning controls, conversation identity, and required Codex metadata as the pending normal request.
4. The internal compaction request shall omit `previous_response_id`.
5. The collector shall accept only one successful unary `response.compaction` completion whose output contains retained message items followed by exactly one completed `compaction_summary` item.
6. Assistant text outside the retained-message output, tool calls, multiple/zero summaries, malformed output, or a non-success completion shall reject the candidate.
7. The replacement shall install the bounded unary output list directly: retained recent user/developer/system context and bounded non-final agent context followed by the validated `compaction_summary` item.
8. Final-answer-style agent messages and assistant/tool items already represented by the opaque checkpoint shall not be redundantly retained.
9. System instructions shall remain in their normal request field unless the active Codex wire mode requires an equivalent developer prefix.
10. The checkpoint shall store source-prefix fingerprints, replacement items, account/model/comp-hash identity, static request fingerprints, creation time, and provider usage evidence.
11. Internal compaction output shall never become ordinary client-visible assistant output.
12. The legacy streamed compaction trigger/result parser shall remain compatibility-only; the dedicated `/responses/compact` endpoint is the initial implementation path.

### Requirement 7: Checkpoint Isolation and Exact-Prefix Reuse

**Objective:** As a security-conscious operator, I want checkpoints tightly scoped and substituted only for matching history, so opaque state cannot leak or corrupt forks.

#### Acceptance Criteria

1. Checkpoint keys shall include authoritative session, connector instance, selected account, model, prompt-cache key, client family, comp hash, instructions fingerprint, tools fingerprint, and continuity mode.
2. Checkpoints shall remain process-local memory with bounded TTL, entry count, per-entry bytes, and deterministic eviction.
3. Reuse shall require an exact source-prefix fingerprint match.
4. Rewriting shall replace only the matched prefix and append the untouched current suffix.
5. Any mismatch in history, account, model, tools, instructions, cache identity, client family, or compatibility identity shall produce a miss.
6. Managed OAuth rotation shall never reuse another account's reasoning or compaction state.
7. One active reservation per key shall prevent duplicate concurrent compaction.
8. Failed, cancelled, incomplete, or rejected attempts shall leave the previous committed checkpoint unchanged.
9. Lookup miss, expiry, incompatibility, or eviction shall be an optimization miss rather than authoritative conversation loss.
10. Stored bodies shall never appear in diagnostics, logs, metrics labels, or ordinary audit.
11. A later checkpoint may summarize a prior checkpoint plus newly accumulated exact reasoning and actions.
12. Closing the connector shall make later commits impossible and clear all opaque state.

### Requirement 8: Response-Chain and Transport Semantics

**Objective:** As a client, I want transport optimizations to complement exact reasoning history without becoming a conflicting source of truth.

#### Acceptance Criteria

1. Exact encrypted reasoning/compaction item replay shall be the durable cross-request correctness baseline.
2. Existing WebSocket `previous_response_id` continuation may remain an optimization when the request is an exact incremental extension with matching static request properties.
3. The initial implementation shall not add automatic cross-turn HTTP `previous_response_id` chaining.
4. Committing a new checkpoint shall invalidate the prior WebSocket continuation entry for the same lineage.
5. The first normal request after checkpoint installation shall omit `previous_response_id` and start a new native chain.
6. After that request completes successfully, WebSocket continuation may establish a new baseline.
7. The connector shall never combine a checkpoint with a response ID from an incompatible history authority.
8. If WebSocket continuation is missing or rejected, exact item replay/full history shall remain sufficient for correctness.
9. Turn-scoped Codex sticky-routing metadata shall not be persisted across logical turns unless the upstream contract explicitly requires it.
10. HTTP and WebSocket normal transports shall produce equivalent reasoning-continuity and compaction semantics.

### Requirement 9: Failure Handling, Accounting, and Privacy

**Objective:** As an operator, I want native context behavior contained and observable, so failures do not corrupt valid turns or hide cost.

#### Acceptance Criteria

1. Reasoning restoration and compaction shall complete before client-visible output is committed.
2. If compaction fails while full reasoning-complete history fits the hard limit, the connector shall continue once with that full history.
3. If compaction fails and the full history cannot fit, the connector shall return a deterministic pre-output context/compaction error.
4. No reasoning/compaction retry, lineage switch, or compaction-triggered failover shall occur after visible output.
5. Cancellation shall close the internal request, release reservations, and preserve prior committed state.
6. Compatibility/protocol failure shall activate a bounded per-lineage cooldown.
7. Managed-account auth/rate-limit rotation shall rebuild candidate history for the next account and shall not transfer opaque state.
8. Provider-reported compaction usage shall be accounted separately from the normal response without double counting.
9. Estimated usage shall remain distinguishable from authoritative provider usage.
10. Metrics shall cover reasoning requested/captured/restored/preserved/missed, compaction attempts/results, checkpoint hits/misses, before/after input, bytes, latency, and cooldown.
11. No metric, error, log, trace, or diagnostic shall contain opaque reasoning/compaction payloads or raw prompts.
12. Disabled-path parity shall show no compaction overhead and no unexpected request-shape changes beyond existing PR #235 behavior.

### Requirement 10: Quality Evaluation and Rollout Evidence

**Objective:** As a maintainer, I want controlled evidence of quality and efficiency, so enablement decisions reflect real agent performance rather than assumed token savings.

#### Acceptance Criteria

1. Tests shall be written before behavior changes for every new boundary and failure mode.
2. Deterministic integration tests shall cover static/managed accounts and HTTP/WebSocket transports.
3. Integration tests shall prove encrypted reasoning is requested without explicit effort when continuity is eligible.
4. Integration tests shall prove exact reasoning survives reasoning → tool call → output → reasoning sequences across multiple client requests.
5. Integration tests shall prove compaction input includes restored reasoning and exact structured action history.
6. An environment-gated live Codex test shall verify normal reasoning capture, later exact replay, V2 compaction acceptance, checkpoint replay, and post-compaction reasoning capture.
7. A four-mode evaluation shall compare neither, reasoning-only, compaction-only, and both.
8. The evaluation shall measure completion quality, repeated/contradictory tool actions, rediscovery, turns, tool calls, input/output/reasoning tokens, latency, context size, and failures.
9. Performance reporting shall include one-time compaction cost and break-even turns.
10. Quality claims shall distinguish observed evidence from inference and shall not extrapolate ARC-AGI-3 gains to coding tasks without data.
11. Race tests shall cover feature-store observation, attempt restoration, checkpoint reservation, continuation invalidation, and close.
12. Fuzz tests shall cover opaque item parsing, ordering, compaction stream collection, and bounded failure diagnostics.
13. Architecture tests shall prevent provider payload leakage into core and prevent connector-local capture of non-surfaced reasoning.
14. Quality evaluation shall distinguish observed evidence from inference and shall not claim coding-quality improvement without measured evidence; quality evidence shall inform tuning and release reporting, not reintroduce a separate default-on promotion gate for this approved scope.
15. Disabling the feature shall be a complete rollback with no state migration; explicit operator opt-out remains supported regardless of quality evidence.
