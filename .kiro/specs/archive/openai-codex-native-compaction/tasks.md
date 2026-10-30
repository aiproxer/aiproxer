# Implementation Plan

## 0. Approved policy delta: default-on native context and conservative budgets

These tasks record the approved default-on policy delta and are checked only when
their implementation and evidence are present. The checked tasks below preserve
completed implementation history from the prior default-off plan; that historical
baseline is not reused as evidence for this policy change.

- [x] 0.1 Add failing default-on and opt-out contract tests
  - Prove omitted direct-Codex `native_context` normalizes to enabled encrypted-reasoning continuity plus enabled compaction.
  - Prove `native_context.enabled:false` is a complete automatic native-context opt-out while exact client-supplied replay remains unaffected.
  - Prove `compaction.enabled:false` disables only automatic compaction/checkpoints and that reasoning/continuity controls remain independently selectable.
  - Prove app-server native context is rejected/off and diagnostics expose effective mode and precedence without payloads.
  - _Requirements: 1.1-1.10_
  - _Boundary: config/wiring, tests_
  - _Depends: 1.2_
  - _Validation: go test ./connectors/codex/internal/codex/... ./connectors/codex/internal/service/...

- [x] 0.2 Implement default-on native-context normalization and explicit precedence
  - Make omitted direct-Codex configuration full mode; preserve explicit field overrides and complete local opt-out.
  - Keep exact client-supplied replay independent from automatic continuity.
  - Ensure disabled paths allocate no checkpoint/coordinator state and do not reset continuation.
  - _Requirements: 1.1-1.10_
  - _Boundary: connector config/wiring_
  - _Depends: 0.1_
  - _Validation: go test ./connectors/codex/internal/codex/... ./connectors/codex/internal/service/...

- [x] 0.3 Add failing standard-composition and precedence tests for the Codex companion rule
  - Prove standard runtime injects/activates a backend-specific direct-Codex rule by default.
  - Prove explicit matching feature rows, including `enabled:false`, suppress injection and retain operator ownership.
  - Prove the rule does not use a broad GPT matcher/version ceiling and backend native-context opt-out remains authoritative.
  - _Requirements: 3.3-3.5_
  - _Boundary: standard feature composition, reasoning-preservation feature, tests_
  - _Depends: 0.2_
  - _Validation: go test ./internal/standardplugins/... ./internal/plugins/features/reasoningpreservation/...

- [x] 0.4 Implement and test `CodexHarnessHeadroomV1` model-aware planning
  - Preserve exact catalog hard-context and `auto_compact_token_limit` metadata when safe.
  - Add exact Spark fallback (128K headline, 96K usable, 80K safe trigger, 32K reserve) before generic GPT-5.x fallback.
  - Add GPT-5.x fallback (250K usable ceiling, 220K safe trigger, 30K named reserve) without guessing a headline limit or widening matching.
  - Validate explicit trigger overrides against usable ceiling, retained window, hard limit, and reserve; reject unsafe overrides.
  - _Requirements: 5.1-5.6_
  - _Boundary: connector catalog/planner, tests_
  - _Depends: 0.2, 5.2, 5.4_
  - _Validation: go test ./connectors/codex/internal/catalog/... ./connectors/codex/internal/codex/...

- [x] 0.5 Synchronize operator docs, examples, and spec hygiene checks
  - Replace stale opt-in/default-off/separate-review wording with default-on plus explicit opt-out semantics.
  - Document `CodexHarnessHeadroomV1`, exact Spark behavior, GPT-5.x fallback, and override validation.
  - Preserve live endpoint facts: dedicated `/responses/compact`, unary `response.compaction`, retained messages plus `compaction_summary`, and no streamed trigger on the dedicated request.
  - Run the relevant Kiro spec/docs checks without claiming quality evidence that has not been observed.
  - _Requirements: 1.1-1.10, 5.1-5.6, 10.9, 10.14-10.15_
  - _Boundary: documentation/spec artifacts_
  - _Depends: 0.1, 0.3, 0.4_
  - _Validation: make kiro-spec-check SPEC=openai-codex-native-compaction; repository docs/spec diff review

## 1. Re-baseline exact Codex item support and configuration

- [x] 1.1 Add failing native-context configuration contract tests
  - Historical baseline: prove explicit disabled configuration leaves compaction disabled and constructs no checkpoint state.
  - Cover separate reasoning request, continuity mode, compaction, bounds, and evaluation-only mode controls.
  - Reject invalid combinations and enabled app-server use.
  - Observable completion: tests define all defaults and invalid states before runtime changes.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.8, 1.9, 1.10_
  - _Boundary: config/wiring, tests_
  - _Depends: none_
  - _Validation: go test ./internal/service/... ./internal/codex/..._

- [x] 1.2 Implement typed native-context configuration and lifecycle
  - Normalize reviewed defaults and hard caps.
  - Keep compaction coordinator/store absent on the disabled path.
  - Clear checkpoint/cooldown state on close.
  - Preserve existing exact client-supplied replay when automatic continuity is disabled.
  - Observable completion: configuration tests pass with no disabled-path request changes.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 1.9, 1.10, 10.15_
  - _Boundary: config/wiring, backend connector composition_
  - _Depends: 1.1_
  - _Validation: go test ./internal/service/... ./internal/codex/..._

- [x] 1.3 Characterize merged PR #235 exact-item behavior
  - Add/retain tests proving completed reasoning capture, canonical exact dialect emission, exact replay, compaction-item retention, and plugin ABI transport.
  - Mark already-delivered behavior as a prerequisite rather than reimplementing it.
  - Cover malformed/oversize/duplicate items and content-safe errors.
  - Observable completion: the baseline suite passes on current main and identifies only new continuity/compaction gaps.
  - _Requirements: 2.4, 2.5, 2.6, 2.7, 2.8, 2.9, 2.10, 10.1_
  - _Boundary: backend connector, SDK contract tests_
  - _Depends: none_
  - _Validation: go test ./internal/codex/... && make parity-checks_

## 2. Integrate surfaced-response reasoning continuity

- [x] 2.1 Add failing Codex eligibility and marker tests
  - Configure an explicit backend-only reasoning-preservation rule with no model keywords.
  - Prove it matches the selected Codex instance regardless of GPT minor version while unrelated backends remain unchanged.
  - Prove client-supplied marker values are removed and only the internal transform can set the trusted marker.
  - Observable completion: tests fail until eligible Codex attempts receive the marker and ineligible attempts do not.
  - _Requirements: 3.1, 3.3, 3.4, 3.8, 3.10, 3.11, 10.13_
  - _Boundary: feature plugin, tests_
  - _Depends: 1.3_
  - _Validation: go test ./internal/plugins/features/reasoningpreservation/..._

- [x] 2.2 Implement bounded continuity marker emission
  - Set the marker after eligibility, artifact classification, replay-support validation, and configured state policy.
  - Emit it for first-turn/no-artifact, preserved, and restored exact Codex attempts.
  - Do not emit it after exclusion, ambiguity/conflict requiring skip, or state failure that cannot guarantee continuity.
  - Ensure the marker contains fixed posture only and is never provider serialized.
  - Observable completion: marker security and eligibility tests pass.
  - _Requirements: 3.3, 3.4, 3.7, 3.10, 3.11_
  - _Boundary: feature plugin, backend connector integration contract_
  - _Depends: 2.1_
  - _Validation: go test ./internal/plugins/features/reasoningpreservation/... ./connectors/codex/internal/codex/..._

- [x] 2.3 Extend surfaced-winner continuity tests
  - Prove only successful surfaced output is observed.
  - Prove parallel losers, swallowed retries, cancelled attempts, and gate-replaced output do not commit reasoning.
  - Prove authoritative-session partitioning, expiry, restart miss, and policy behavior.
  - Observable completion: no connector-local or non-surfaced reasoning becomes restorable.
  - _Requirements: 3.1, 3.2, 3.7, 3.8, 3.9, 9.11, 10.11, 10.13_
  - _Boundary: feature plugin, core integration tests_
  - _Depends: 2.2_
  - _Validation: go test ./internal/plugins/features/reasoningpreservation/... ./internal/core/runtime/..._

## 3. Request and replay exact reasoning on every eligible attempt

- [x] 3.1 Add failing request-policy tests
  - Cover eligible continuity with no explicit reasoning effort.
  - Cover explicit call/route effort precedence, configured/default effort, unsupported levels, and summary behavior.
  - Assert `reasoning.encrypted_content` is included on normal and internal compaction requests.
  - Assert no internal marker reaches upstream JSON.
  - Observable completion: current conditional request behavior fails the new tests.
  - _Requirements: 2.1, 2.2, 2.3, 3.10, 9.11, 10.3_
  - _Boundary: backend connector driven adapter, tests_
  - _Depends: 2.2_
  - _Validation: go test ./connectors/codex/internal/codex/..._

- [x] 3.2 Implement model-aware encrypted-reasoning request policy
  - Build a valid reasoning object for eligible attempts without inventing an effort.
  - Preserve explicit and route overrides.
  - Request exact encrypted reasoning for continuity-marked attempts and compaction requests.
  - Strip internal marker before serialization.
  - Observable completion: request snapshots pass across supported model profiles.
  - _Requirements: 2.1, 2.2, 2.3, 2.7, 2.9, 3.10_
  - _Boundary: backend connector driven adapter_
  - _Depends: 3.1_
  - _Validation: go test ./connectors/codex/internal/codex/..._

- [x] 3.3 Add failing action-trajectory restoration tests
  - Cover reasoning before a function call, multiple reasoning parts, tool output, and later reasoning across client requests.
  - Cover client-preserved, missing, conflicting, ambiguous, rollback, edit, fork, and reordered trajectories.
  - Assert exact placement and call/output identity.
  - Observable completion: tests define native causal ordering beyond final assistant messages.
  - _Requirements: 3.5, 3.6, 3.7, 3.12, 4.1, 4.2, 4.3, 4.4, 4.5, 4.8, 4.9, 10.4_
  - _Boundary: feature plugin, canonical adapter tests_
  - _Depends: 1.3, 2.2_
  - _Validation: go test ./internal/plugins/features/reasoningpreservation/... ./connectors/codex/internal/codex/..._

- [x] 3.4 Complete exact action-trajectory replay support
  - Extend only the placement/assistant-trajectory behavior proven missing by 3.3.
  - Preserve exact reasoning envelopes and structured call order.
  - Reject unrepresentable dialects before upstream work.
  - Continue capture/replay after a compaction item.
  - Observable completion: all action-level tables and post-compaction continuity tests pass.
  - _Requirements: 3.5, 3.6, 3.7, 3.12, 4.1, 4.2, 4.3, 4.4, 4.5, 4.9_
  - _Boundary: feature plugin, backend connector canonical adapter_
  - _Depends: 3.3_
  - _Validation: go test ./internal/plugins/features/reasoningpreservation/... ./connectors/codex/internal/codex/... && make parity-checks_

## 4. Build reasoning-complete exact native history

- [x] 4.1 Add failing exact-history builder tests
  - Cover messages, exact reasoning, structured calls/outputs, multiple assistant trajectories, and deterministic fingerprints.
  - Prove no-tools normal projection does not affect compaction history.
  - Reject orphan outputs, split pairs, unsupported opaque dialects, and malformed ordering.
  - Observable completion: tests distinguish exact compaction history from normal payload projection.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 4.7, 4.8, 4.10, 6.2, 10.5_
  - _Boundary: backend connector domain policy, tests_
  - _Depends: 3.4_
  - _Validation: go test ./connectors/codex/internal/codex/..._

- [x] 4.2 Implement the exact native history builder
  - Consume the post-transform canonical call.
  - Preserve reasoning placement, structured action identities, and user/assistant trajectory boundaries.
  - Produce deterministic item fingerprints and pair-safe split metadata.
  - Leave ordinary generation projection unchanged.
  - Observable completion: exact-history tests pass and no public canonical authority is added.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 4.7, 4.10, 5.4, 6.2_
  - _Boundary: backend connector domain policy_
  - _Depends: 4.1_
  - _Validation: go test ./connectors/codex/internal/codex/..._

- [x] 4.3 Fuzz ordering and exact-history validation
  - Fuzz reasoning placement, item discriminators, call/output identities, malformed JSON, oversized opaque fields, and boundary mutation.
  - Assert bounded memory, stable errors, no panic, and no payload leakage.
  - Observable completion: targeted fuzz runs complete without invariant violation.
  - _Requirements: 2.7, 2.8, 2.10, 4.5, 4.7, 9.11, 10.12_
  - _Boundary: tests_
  - _Depends: 4.2_
  - _Validation: go test -fuzz=Fuzz -fuzztime=30s ./connectors/codex/internal/codex/..._

## 5. Resolve model metadata and pure compaction planning

- [x] 5.1 Add failing catalog/profile tests
  - Cover `auto_compact_token_limit`, `comp_hash`, context windows, reasoning defaults/levels, missing/malformed fields, and fallback catalog.
  - Cover trigger precedence and model/hash incompatibility.
  - Observable completion: current catalog omissions are captured by failing tests.
  - _Requirements: 5.1, 5.2, 5.3, 5.10_
  - _Boundary: backend connector inventory, tests_
  - _Depends: none_
  - _Validation: go test ./connectors/codex/internal/catalog/..._

- [x] 5.2 Implement connector-private compaction model profiles
  - Preserve catalog metadata without widening root inventory contracts.
  - Resolve hard limit, trigger, retained budget, safety headroom, and default reasoning policy.
  - Require exact model equality for replay; use comp hash only for incompatibility/model-switch decisions.
  - Observable completion: profile tests pass for discovered and fallback models.
  - _Requirements: 2.2, 2.9, 5.1, 5.2, 5.3, 5.10, 7.5_
  - _Boundary: backend connector inventory_
  - _Depends: 5.1_
  - _Validation: go test ./connectors/codex/internal/catalog/... ./connectors/codex/internal/codex/..._

- [x] 5.3 Add failing planner and estimator tables
  - Cover disabled/missing-marker bypass, checkpoint reuse, threshold crossing, latest-user live tail, pair/trajectory boundaries, minimum savings, hard-limit failure, and one in-flight attempt.
  - Estimate from effective rewritten exact history and opaque-state metadata rather than ciphertext tokenization.
  - Observable completion: pure decisions and stable reason codes are fully specified.
  - _Requirements: 5.3, 5.4, 5.5, 5.6, 5.7, 5.8, 5.9, 7.3, 7.9, 9.2, 9.3_
  - _Boundary: backend connector domain policy, tests_
  - _Depends: 1.2, 4.2, 5.2_
  - _Validation: go test ./connectors/codex/internal/codex/..._

- [x] 5.4 Implement payload estimation and pure planning
  - Prefer valid checkpoint reuse, then compare effective history to trigger.
  - Preserve latest live turn and pair-safe trajectories.
  - Require configured minimum savings and continuity marker in required mode.
  - Produce bypass/reuse/create/hard-failure decisions without side effects.
  - Observable completion: planner tables pass and ciphertext is never fed to normal tokenizer.
  - _Requirements: 5.3, 5.4, 5.5, 5.6, 5.7, 5.8, 5.9, 7.3, 7.4, 7.9, 9.2, 9.3_
  - _Boundary: backend connector domain policy_
  - _Depends: 5.3_
  - _Validation: go test ./connectors/codex/internal/codex/..._

## 6. Implement Responses Compaction V2 and retained history

- [x] 6.1 Add failing V2 request/collector tests
  - Assert exact history plus one trigger, same account/model/static request shape, required metadata, encrypted reasoning include, and cleared response ID.
  - Cover exactly-one compaction item, assistant/tool output rejection, duplicate/missing item, malformed events, cancellation, retry budget, and usage capture.
  - Observable completion: tests define the complete internal protocol contract.
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.10, 6.11, 6.12, 9.5, 9.8_
  - _Boundary: backend connector driven adapter, tests_
  - _Depends: 3.2, 4.2, 5.4_
  - _Validation: go test ./connectors/codex/internal/codex/..._

- [x] 6.2 Implement the bounded V2 compaction client
  - Reuse existing HTTP/SSE transport/auth/error infrastructure.
  - Build a private stream collector and never expose internal output as assistant content.
  - Enforce event/byte/item bounds and content-safe errors.
  - Preserve provider usage and response ID as internal evidence only.
  - Observable completion: all request/collector tests pass.
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.10, 6.11, 6.12, 9.5, 9.8, 9.11_
  - _Boundary: backend connector driven adapter_
  - _Depends: 6.1_
  - _Validation: go test ./connectors/codex/internal/codex/..._

- [x] 6.3 Add failing Codex-aligned replacement-policy tests
  - Cover retained user/developer/system items, bounded non-final agent context, final-answer exclusion, per-agent cap, total budget, image bounds, and exactly one final compaction item.
  - Assert no redundant reasoning/call/output retention.
  - Observable completion: replacement policy matches the documented current Codex predicate.
  - _Requirements: 6.7, 6.8, 6.9, 6.10_
  - _Boundary: backend connector domain policy, tests_
  - _Depends: 4.2, 5.2_
  - _Validation: go test ./connectors/codex/internal/codex/..._

- [x] 6.4 Implement retained replacement construction
  - Apply the versioned Codex-aligned predicate and configured budget.
  - Preserve original retained-item order and append one validated compaction item.
  - Record source/result token evidence for checkpoints.
  - Observable completion: replacement tests pass with deterministic output.
  - _Requirements: 6.7, 6.8, 6.9, 6.10, 7.11_
  - _Boundary: backend connector domain policy_
  - _Depends: 6.3_
  - _Validation: go test ./connectors/codex/internal/codex/..._

## 7. Build isolated checkpoint state and exact-prefix rewrite

- [x] 7.1 Add failing checkpoint-store tests
  - Cover all key dimensions, TTL/LRU/byte caps, reserve/commit/abort, previous committed state survival, cooldown, defensive copies, close, and managed-account rotation.
  - Run concurrent reservation and stale-commit scenarios.
  - Observable completion: race-safe state semantics are fixed before implementation.
  - _Requirements: 7.1, 7.2, 7.5, 7.6, 7.7, 7.8, 7.9, 7.10, 7.12, 9.5, 9.6, 9.7, 10.11_
  - _Boundary: backend connector state adapter, tests_
  - _Depends: 1.2_
  - _Validation: go test ./connectors/codex/internal/codex/..._

- [x] 7.2 Implement bounded checkpoint store
  - Maintain immutable committed state and one reservation per key.
  - Enforce TTL/LRU/entry-byte limits and cooldown.
  - Reject commits after close and preserve old checkpoints after failed candidates.
  - Observable completion: store tests and race tests pass.
  - _Requirements: 7.1, 7.2, 7.6, 7.7, 7.8, 7.9, 7.10, 7.12, 9.5, 9.6_
  - _Boundary: backend connector state adapter_
  - _Depends: 7.1_
  - _Validation: go test -race ./connectors/codex/internal/codex/..._

- [x] 7.3 Add failing exact-prefix rewrite tests
  - Cover exact match, append-only suffix, edits, rollback, fork, truncation, reordered items, static-shape drift, model/account change, and later checkpoint-over-checkpoint compaction.
  - Assert the latest live suffix is byte/semantically unchanged.
  - Observable completion: all authority conflicts fail closed to full history.
  - _Requirements: 4.8, 7.3, 7.4, 7.5, 7.11, 8.7_
  - _Boundary: backend connector domain policy, tests_
  - _Depends: 4.2, 6.4, 7.2_
  - _Validation: go test ./connectors/codex/internal/codex/..._

- [x] 7.4 Implement exact-prefix checkpoint rewriting
  - Replace only the committed source prefix.
  - Append untouched suffix and recompute effective fingerprints.
  - Permit later checkpoint creation over an existing checkpoint under normal validation.
  - Observable completion: rewrite tests pass and mismatch never causes conversation loss.
  - _Requirements: 7.3, 7.4, 7.5, 7.9, 7.11, 8.7_
  - _Boundary: backend connector domain policy_
  - _Depends: 7.3_
  - _Validation: go test ./connectors/codex/internal/codex/..._

## 8. Orchestrate accounts, transports, continuation, and failures

- [x] 8.1 Add failing coordinator matrix tests
  - Cover static/managed account × HTTP/WebSocket × disabled/reasoning-only/compaction-only/full modes.
  - Prove preparation occurs after selected account/model and after reasoning transform.
  - Cover fail-open, hard-limit error, cancellation, cooldown, auth/rate-limit rotation, and no post-output retry.
  - Observable completion: one deterministic matrix defines the end-to-end preparation contract.
  - _Requirements: 3.11, 5.4, 5.9, 7.6, 8.1, 8.8, 8.10, 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 9.7, 10.2_
  - _Boundary: backend connector app orchestration, tests_
  - _Depends: 3.2, 5.4, 6.2, 6.4, 7.4_
  - _Validation: go test ./connectors/codex/internal/codex/..._

- [x] 8.2 Implement per-account native-context coordinator
  - Verify marker, build exact history, derive key, plan, reserve, compact, commit, rewrite, and return internal usage.
  - Rebuild independently for every managed-account attempt.
  - Fall back once to full reasoning-complete history when safe.
  - Observable completion: coordinator matrix passes without changing core routing.
  - _Requirements: 3.11, 5.4, 5.9, 7.6, 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 9.7_
  - _Boundary: backend connector app orchestration_
  - _Depends: 8.1_
  - _Validation: go test ./connectors/codex/internal/codex/..._

- [x] 8.3 Add failing continuation-authority tests
  - Prove exact item replay works with no response ID.
  - Prove WebSocket response ID is used only on exact incremental extension.
  - Prove checkpoint installation invalidates old continuation and first post-checkpoint request omits response ID.
  - Prove no automatic HTTP response-ID chain or cross-turn sticky state is added.
  - Observable completion: response-ID optimization cannot conflict with checkpoint/full-history authority.
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7, 8.8, 8.9, 8.10_
  - _Boundary: backend connector transport/state, tests_
  - _Depends: 7.4, 8.2_
  - _Validation: go test ./connectors/codex/internal/codex/..._

- [x] 8.4 Integrate chain reset with HTTP/WebSocket paths
  - Run native-context preparation before WebSocket continuation trimming.
  - Invalidate old continuation on checkpoint commit and record a new baseline only after success.
  - Preserve full payload rollback for stale response IDs.
  - Keep HTTP stateless exact-history behavior.
  - Observable completion: continuation tests and transport matrix pass.
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7, 8.8, 8.9, 8.10_
  - _Boundary: backend connector transport/state_
  - _Depends: 8.3_
  - _Validation: go test ./connectors/codex/internal/codex/..._

## 9. Account for usage and prove privacy

- [x] 9.1 Add failing usage and diagnostics tests
  - Cover separate provider compaction usage, estimated fallback authority, no double count, before/after context/bytes, reasoning outcomes, and fixed diagnostic categories.
  - Scan logs/errors/metrics/traces for synthetic ciphertext and prompt markers.
  - Observable completion: current implementation cannot satisfy the full evidence contract.
  - _Requirements: 9.8, 9.9, 9.10, 9.11, 9.12, 10.9_
  - _Boundary: backend connector accounting/observability, tests_
  - _Depends: 6.2, 8.2_
  - _Validation: go test ./connectors/codex/internal/codex/... ./internal/core/tokenaccounting/..._

- [x] 9.2 Implement usage aggregation and safe telemetry
  - Attach compaction usage as separate provider-billable evidence.
  - Use checkpoint/provider metadata before conservative opaque estimates.
  - Emit only fixed outcomes and aggregate counts/sizes/latencies.
  - Preserve disabled-path accounting semantics.
  - Observable completion: usage/privacy tests pass with zero payload leakage.
  - _Requirements: 2.7, 2.10, 7.10, 9.8, 9.9, 9.10, 9.11, 9.12_
  - _Boundary: backend connector accounting/observability_
  - _Depends: 9.1_
  - _Validation: go test ./connectors/codex/internal/codex/... ./internal/core/tokenaccounting/..._

## 10. Build quality and compatibility evidence

- [x] 10.1 Add deterministic full-path native-context integration scenarios
  - Drive multiple client requests through runtime, reasoning feature, connector, and HTTP/WS emulators.
  - Prove encrypted reasoning request, surfaced capture, exact action restore, reasoning-complete compaction input, checkpoint replay, and post-checkpoint capture.
  - Include disabled zero-extra-request controls.
  - Observable completion: full path passes for static/managed and HTTP/WebSocket cases.
  - _Requirements: 3.2, 3.5, 3.12, 4.9, 6.2, 8.10, 10.2, 10.3, 10.4, 10.5_
  - _Boundary: tests/integration_
  - _Depends: 3.4, 8.4, 9.2_
  - _Validation: go test ./internal/stdhttp/... ./connectors/codex/..._

- [x] 10.2 Add environment-gated live Codex validation
  - Require explicit credentials, model, and opt-in environment for the billable live probe.
  - Verify normal encrypted reasoning, stateless replay, V2 compaction, checkpoint replay, post-compaction reasoning, and content-safe cleanup.
  - Probe structured history with empty current tools and previous-model compaction as classified optional subtests.
  - Observable completion: compatibility is recorded as pass/fail/unsupported without leaking artifacts.
  - _Requirements: 2.1, 3.12, 5.10, 6.3, 6.5, 10.6_
  - _Boundary: tests/live integration_
  - _Depends: 10.1_
  - _Validation: environment-gated Codex live test_

- [x] 10.3 Build the four-mode quality evaluation harness
  - Run fixed long-horizon repository tasks in baseline, reasoning-only, compaction-only, and full modes.
  - Record task quality, repeated/contradictory actions, rediscovery, turns, tools, tokens, latency, context, compaction cost, and failures.
  - Use fixed seeds and environment snapshots with paired reports.
  - Observable completion: the harness produces comparable machine-readable results and a concise evidence summary.
  - _Requirements: 1.4, 10.7, 10.8, 10.9, 10.10_
  - _Boundary: tests/evaluation_
  - _Depends: 10.1_
  - _Validation: targeted evaluation command documented by the test package_

- [x] 10.4 Add race, fuzz, and architecture gates
  - Run concurrent observer/restore/checkpoint/continuation/close tests.
  - Fuzz opaque parsing, event order, exact history, and collector bounds.
  - Assert no provider payload type enters core and no connector-local non-surfaced reasoning store is introduced.
  - Observable completion: race/fuzz/architecture gates pass.
  - _Requirements: 10.11, 10.12, 10.13_
  - _Boundary: tests/architecture_
  - _Depends: 4.3, 7.2, 8.4, 9.2_
  - _Validation: go test -race ./connectors/codex/... ./internal/plugins/features/reasoningpreservation/... && make quality-checks_

- [x] 10.5 Execute release-quality checks and preserve the prior default-off rollout baseline
  - Run focused tests, unit suite, parity, quality checks, and relevant precommit integration.
  - Record live tests as passed or explicitly skipped.
  - Historical baseline: verify the prior examples kept compaction disabled and rollback required no migration.
  - The approved policy delta is tracked in completed tasks 0.1-0.5; this historical checkbox remains evidence only for the release checks listed here, not for the default-on policy itself.
  - Observable completion: deterministic gates pass and remaining uncertainty is only documented live evidence.
  - _Requirements: 6.12, 9.12, 10.1, 10.14, 10.15_
  - _Boundary: tests/release gating_
  - _Depends: 10.2, 10.3, 10.4_
  - _Validation: make test-unit && make parity-checks && make quality-checks_
