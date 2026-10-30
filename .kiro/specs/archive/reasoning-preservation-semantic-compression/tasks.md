# Implementation Plan

Implementation is TDD-first. Every production task follows RED → GREEN → refactor. No phase contains more than five tasks, and no task may enable backend-visible surrogate replay before Phase 6.

Hard ordering invariant:

```text
RED exact/disabled/privacy/bounds/source-compat contracts
-> canonical classifier + config + optional Poll + bounded store foundations
-> isolated compressor + egress + raw-result validation domain
-> original-first shadow submission
-> non-blocking shadow adoption
-> destination-gated active replay
-> certification
```

The completed reasoning-preservation, OpenAI Responses preservation, Codex native compaction, and compaction-continuity specifications are prerequisites/constraints, not implementation targets to rewrite.

## 1. Freeze Safety, Compatibility, Privacy, and Resource Contracts With RED Tests

- [x] 1.1 Freeze exact/native/signed/opaque non-compressibility
  - Add RED table tests covering OpenAI Responses exact items, Codex exact/native artifacts, Anthropic signed/redacted/opaque thinking, unknown dialects, and mixed exact-bearing parts.
  - Prove readable text inside an exact-bearing structure never makes it compressor input.
  - Prove compression-disabled behavior is byte/structure equivalent to current reasoning preservation.
  - _Boundary: feature domain / regression tests_
  - _Depends: none_
  - _Validation: `go test -count=1 ./internal/plugins/features/reasoningpreservation/...`_
  - _Requirements: 1, 2, 13_

- [x] 1.2 Freeze surfaced-winner/original-first lifecycle
  - Add RED tests proving compressor work is impossible for failed/cancelled/closed/replaced/gate-replaced streams, swallowed retries, and parallel losers.
  - Prove original `TurnArtifact` append precedes any reservation/provider submission on `success_released`.
  - Prove compressor failure cannot delete or invalidate a committed original.
  - _Boundary: response lifecycle / feature tests_
  - _Depends: none_
  - _Validation: focused reasoning-preservation observer/runtime tests_
  - _Requirements: 4_

- [x] 1.3 Freeze exported `BackgroundClient` source compatibility and optional poll semantics
  - Add a compile/source-compatibility fixture implementing only historical `SubmitCollect`, `Await`, and `Forget`; it must continue to satisfy `auxiliary.BackgroundClient` after this work.
  - Add RED contract tests for a separate optional `BackgroundPoller` capability with pending/completed/failed/not-found states and no blocking.
  - Cover Poll races with completion, Forget, expiry, and shutdown without changing Await semantics.
  - _Boundary: exported SDK / core auxiliary tests_
  - _Depends: none_
  - _Validation: `go test -count=1 ./pkg/lipsdk/auxiliary ./internal/core/auxreq`_
  - _Requirements: 6, 11, 13_

- [x] 1.4 Freeze optional-state per-session and aggregate memory safety
  - Add RED store tests for pending/session, pending/feature-instance, surrogate/turn, surrogate/session, and surrogate/feature-instance bounds.
  - Add multi-session tests proving aggregate exhaustion rejects optional state rather than evicting an authoritative original.
  - Prove delete/expiry/original eviction/stale cleanup decrement aggregate counters exactly once under concurrency.
  - _Boundary: feature store domain / concurrency tests_
  - _Depends: none_
  - _Validation: reasoning-preservation store tests with deterministic clock plus race test_
  - _Requirements: 3, 5, 13_

- [x] 1.5 Freeze ordinary-text privacy and raw-result allocation boundaries
  - Add RED tests showing semantic-text classification does not imply egress approval; cover allow/redact/deny/missing-policy/route-policy-mismatch.
  - Prove required redaction occurs before input budgeting and provider submission, and no unredacted sensitive source reaches the fake compressor.
  - Add oversized raw-response tests where JSON would be valid only beyond `max_output_bytes`; prove rejection occurs before JSON decode/materialization beyond the bound.
  - Distinguish trusted auxiliary control-plane metadata from model-visible `Call.Messages` in prompt-inspection tests.
  - _Boundary: privacy/security + parser allocation tests_
  - _Depends: none_
  - _Validation: focused feature/auxiliary/security tests_
  - _Requirements: 7, 8, 10, 13_

## 2. Implement Minimal Foundations Without Changing Backend-Visible Replay

- [x] 2.1 Implement the canonical replay-semantics classifier
  - Add one pure typed classifier using canonical reasoning dialect and part structure/presence semantics.
  - Return semantic-text only for the narrow proven plain-text case; exact/unknown wins conservatively.
  - Reuse this same classifier later for submission and active selection.
  - _Boundary: feature domain_
  - _Depends: 1.1_
  - _Validation: classifier table/fuzz tests_
  - _Requirements: 1, 2_

- [x] 2.2 Implement nested compression configuration and hard validation
  - Add disabled-by-default `compression` config with explicit `route`, shadow/active mode, timeout/input/output/surrogate/savings bounds, per-session optional limits, feature-instance aggregate limits, and egress policy configuration/reference.
  - Add distinct `max_output_bytes` raw-response bound; do not conflate it with `max_output_tokens` or `max_surrogate_bytes`.
  - Validate aggregate >= corresponding local limits, hard ceilings, ratio policy, explicit route, and default shadow mode.
  - Preserve existing standard injected configuration as compression-disabled.
  - _Boundary: feature config_
  - _Depends: 1.4, 1.5_
  - _Validation: config unit/fuzz/example-config tests_
  - _Requirements: 3, 7, 10_

- [x] 2.3 Add a source-compatible optional background poll capability
  - Define `BackgroundPoller`/`PollResult`/state types separately from historical `BackgroundClient`; do not add a required Poll method to the existing interface.
  - Implement non-blocking Poll on the process-owned scheduler with defensive copies and existing cleanup semantics.
  - Keep Await/Forget behavior unchanged and add compile-time assertions for scheduler capabilities.
  - _Boundary: SDK + core auxiliary infrastructure_
  - _Depends: 1.3_
  - _Validation: `go test -count=1 ./pkg/lipsdk/auxiliary ./internal/core/auxreq`; race-focused scheduler tests_
  - _Requirements: 6, 11, 13_

- [x] 2.4 Extend the reasoning-preservation store with non-destructive optional-state reservation
  - Add internal compression reservation/pending/surrogate operations with artifact ID + original digest + policy-revision CAS checks.
  - Enforce per-session and feature-instance aggregate pending limits before provider submission can occur.
  - Keep optional accounting separate from authoritative `ReasoningBytes` FIFO/TTL budgets.
  - Clear optional state and aggregate counters when original expires/deletes/evicts.
  - _Boundary: feature store_
  - _Depends: 1.4, 2.2_
  - _Validation: store unit/race tests_
  - _Requirements: 4, 5_

- [x] 2.5 Introduce explicit feature-internal compression services/composition
  - Bind generation-local `BackgroundClient`, optional `BackgroundPoller`, and trusted compression egress/sanitizer policy into reasoning-preservation construction without a global service locator.
  - Validate enabled compression has the required poll/egress capabilities; disabled mode requires none.
  - Do not widen provider APIs or `response.Services` merely for convenience unless implementation evidence proves unavoidable.
  - _Boundary: feature composition / runtimebundle_
  - _Depends: 2.2, 2.3, 2.4_
  - _Validation: composition/config tests_
  - _Requirements: 3, 6, 7, 8_

## 3. Implement the Isolated Compressor, Egress Policy, and Bounded Decoder

- [x] 3.1 Implement feature-scoped egress decision and sanitizer contract
  - Define a narrow trusted allow/redact/deny decision for purpose `reasoning_semantic_compression`, explicit route, and originating trusted scope.
  - Reuse existing secret/redaction authority when available; do not add a competing heuristic detector.
  - Deny when required redaction cannot be performed, and keep control-plane policy/scope data out of model messages.
  - _Boundary: feature privacy policy seam_
  - _Depends: 1.5, 2.5_
  - _Validation: fake-policy/sanitizer tests_
  - _Requirements: 7, 8_

- [x] 3.2 Implement bounded semantic-segment preparation
  - Extract only classifier-approved reasoning placements; exclude ordinary answer text, transcript, tools, files/media, signatures, opaque/native data.
  - Apply required redaction before input byte/token accounting.
  - Produce local segment indexes only; never place raw session/account/lineage/anchor/digest IDs into model-visible payload.
  - _Boundary: feature compressor domain_
  - _Depends: 2.1, 3.1_
  - _Validation: preparation/privacy tests_
  - _Requirements: 1, 2, 7, 8_

- [x] 3.3 Build one detached no-tools auxiliary compressor request per artifact
  - Require explicit configured route, private/detached execution, bounded output tokens, and `reasoning-output-preservation` disabled on the child.
  - Carry role/visibility/parent lineage in the trusted auxiliary envelope and rely on existing cloned principal/scope execution context for billing; keep these out of `Call.Messages`.
  - Treat source segment JSON as untrusted quoted data and require strict versioned output schema.
  - _Boundary: feature compressor adapter over auxiliary SDK_
  - _Depends: 3.2, 2.5_
  - _Validation: canonical request validation + prompt/envelope separation tests_
  - _Requirements: 6, 8, 9, 10_

- [x] 3.4 Implement feature-level raw result extraction bounded before decode
  - Reject tool calls/non-text channels first, then iterate collected text fragments with a byte counter.
  - Stop and return `raw_oversize` once `max_output_bytes` is exceeded; do not construct the full string or invoke JSON decode first.
  - Treat the scheduler `MaxResultBytes` as only an outer defense-in-depth ceiling.
  - _Boundary: feature parser/allocation guard_
  - _Depends: 1.5, 2.2_
  - _Validation: oversized raw-response tests including syntactically valid tail beyond limit_
  - _Requirements: 3, 10_

- [x] 3.5 Implement strict decoder, surrogate validation, and savings policy
  - Strict-decode schema version/indexes/text; reject unknown fields, duplicates, missing indexes, invalid controls/UTF-8, empty required text, and malformed output.
  - Enforce decoded `max_surrogate_bytes`, minimum saved bytes, minimum ratio, and strict smaller-than-source behavior.
  - Return typed content-free outcomes and never claim mathematical semantic equivalence.
  - _Boundary: feature compressor domain_
  - _Depends: 3.4_
  - _Validation: table/fuzz tests_
  - _Requirements: 10, 13_

## 4. Wire Original-First Shadow Submission Only

- [x] 4.1 Capture exact parent attribution and artifact correlation after original append
  - Extend observer-owned post-append data only with the trusted scope/lineage/correlation needed for reservation and child execution.
  - Keep raw principal/session/account/lineage out of model payload and content telemetry.
  - Confirm no compression path exists before authoritative append success.
  - _Boundary: feature observer/composition_
  - _Depends: 2.4, 2.5, 3.2_
  - _Validation: surfaced-winner/order/privacy tests_
  - _Requirements: 4, 8, 9_

- [x] 4.2 Reserve optional capacity before any provider submission
  - Reserve pending state using artifact/digest/policy revision under per-session and feature-instance aggregate limits.
  - Skip compression without provider work when reservation fails or source is below threshold/ineligible.
  - Prove reservation cannot evict original reasoning.
  - _Boundary: feature observer + store_
  - _Depends: 4.1, 2.4_
  - _Validation: session/aggregate saturation tests_
  - _Requirements: 4, 5_

- [x] 4.3 Apply egress decision/redaction before request construction
  - Evaluate route/purpose/principal policy after reservation and before submit; clear reservation on deny/missing required policy.
  - Redact locally when required, then re-run bounded input accounting over sanitized text.
  - Prove fake provider never receives denied/unredacted sensitive text.
  - _Boundary: feature observer + privacy seam_
  - _Depends: 3.1, 3.2, 4.2_
  - _Validation: privacy integration tests_
  - _Requirements: 7, 8_

- [x] 4.4 Submit and bind background job without waiting
  - Submit through generation-bound `BackgroundClient` only after original append, reservation, egress approval, and bounded request construction.
  - Bind returned JobID with CAS; on submit failure clear reservation; on post-submit bind failure Forget when safe while retaining billable usage.
  - Keep shadow mode behavior: backend-visible replay remains original regardless of job state.
  - _Boundary: feature observer + auxiliary SDK_
  - _Depends: 3.3, 4.3_
  - _Validation: queue/admission/bind-race tests_
  - _Requirements: 4, 6, 9, 11, 13_

## 5. Adopt Completed Results Non-Blocking While Still Replaying Originals

- [x] 5.1 Add one-shot non-blocking poll to the matching attempt path
  - For matching artifacts with pending state, use optional `BackgroundPoller` once; never Await or busy-wait.
  - Pending/unavailable poll capability => original for this attempt; failed/not-found => clear optional pending state safely.
  - Keep compression poll/store errors separate from authoritative `on_state_error=reject` behavior.
  - _Boundary: feature AttemptTransform_
  - _Depends: 2.3, 4.4_
  - _Validation: pending/failure/not-found/poll-unavailable tests_
  - _Requirements: 6, 11_

- [x] 5.2 Apply raw byte guard before parser invocation
  - Feed completed `Collected` through the bounded raw extractor; reject `raw_oversize` before strict JSON decoding.
  - Record only safe byte counts/outcomes.
  - Forget terminal result after completion handling according to scheduler semantics.
  - _Boundary: attempt/adoption path_
  - _Depends: 3.4, 5.1_
  - _Validation: oversized integration tests_
  - _Requirements: 10, 11, 13_

- [x] 5.3 Validate/correlate and CAS-attach surrogate under aggregate byte budgets
  - Verify pending JobID, artifact/original digest, semantic profile, egress-policy hash/version, and compression-policy revision.
  - Enforce per-turn/per-session/feature-instance surrogate bytes before CAS attachment; reject optional state at exhaustion without original eviction.
  - Atomically move pending count to surrogate bytes and prevent counter drift on stale/replayed results.
  - _Boundary: attempt transform + store_
  - _Depends: 3.5, 5.2_
  - _Validation: stale/CAS/multi-session aggregate/race tests_
  - _Requirements: 5, 10, 11_

- [x] 5.4 Complete shadow-only observability and value evidence
  - Record eligible, privacy, reservation, queue, poll, raw-size, decode, savings, aggregate-budget, and shadow-ready outcomes without content.
  - Always restore original reasoning in shadow mode.
  - Add deterministic metrics/evaluation fixture for hypothetical savings and additional auxiliary cost.
  - _Boundary: feature telemetry / test harness_
  - _Depends: 5.3_
  - _Validation: telemetry privacy/race tests + deterministic shadow fixture_
  - _Requirements: 8, 9, 13_

## 6. Enable Explicit Destination-Gated Active Replay

- [x] 6.1 Revalidate semantic class and destination representability before selection
  - Re-run the canonical classifier over original placements and require existing destination `ReasoningReplaySupport` for the original dialect.
  - Reject stale/unknown/exact placements and preserve existing client-reasoning precedence.
  - Do not add provider-name/model-name exceptions.
  - _Boundary: feature restore/AttemptTransform domain_
  - _Depends: 2.1, 5.3_
  - _Validation: destination capability matrix fixtures_
  - _Requirements: 1, 2, 12_

- [x] 6.2 Build an ephemeral surrogate restoration view without mutating stored originals
  - Copy original placements and replace only validated semantic-text `Reasoning.Text` fields.
  - Preserve `BeforeNonReasoningPart`, dialect, exact/signed/opaque parts, tool IDs/order, ordinary assistant text, files/images, and all non-reasoning structure.
  - Ambiguous placement/correlation => original fallback.
  - _Boundary: feature restore domain_
  - _Depends: 6.1_
  - _Validation: mixed-placement/exact-byte/order tests_
  - _Requirements: 1, 12_

- [x] 6.3 Gate surrogate use strictly on explicit active mode
  - Shadow remains original-only even with a valid surrogate.
  - Active uses surrogate only after all classifier/correlation/destination checks pass; every uncertainty falls back to original/unrepresentable behavior already defined by reasoning preservation.
  - Compression-specific failures never become candidate retry/failover authority.
  - _Boundary: AttemptTransform_
  - _Depends: 6.2, 5.4_
  - _Validation: shadow-vs-active behavior tests_
  - _Requirements: 3, 11, 12, 13_

- [x] 6.4 Verify standard Codex/native companion behavior remains exact
  - Run current Codex reasoning-preservation/native-compaction companion tests with compression enabled in shadow/active configurations.
  - Prove exact/native markers, encrypted items, checkpoint flow, and provider-only accounting are unchanged and never compressor input.
  - Add architecture guard if implementation accidentally couples semantic compression into Codex-specific native logic.
  - _Boundary: standard composition / regression_
  - _Depends: 6.3_
  - _Validation: existing Codex companion/native tests plus focused semantic-compression negatives_
  - _Requirements: 1, 12, 13_

## 7. Certify Economics, Security, Concurrency, Performance, and Repository Quality

- [x] 7.1 Certify billing/admission/workload attribution
  - Prove originating-principal attribution, auxiliary workload role/class, separate child BillingCallID/B-leg evidence, primary protocol usage exclusion, and account/operator aggregate inclusion.
  - Cover pre-submit admission rejection and submitted-but-invalid/raw-oversize/stale/insufficient-savings results.
  - Confirm control-plane principal/scope exists for billing but never enters model prompt.
  - _Boundary: billing/runtime integration_
  - _Depends: 4.4, 5.3_
  - _Validation: focused billing/metering/runtime tests_
  - _Requirements: 8, 9_

- [x] 7.2 Certify privacy and data-egress failure modes
  - Exercise allow/redact/deny/missing-policy/route-policy mismatch with sensitive ordinary reasoning fixtures.
  - Prove redaction precedes input sizing and provider submission and that content-bearing telemetry stays clean.
  - Verify no second secret detector is introduced where existing trusted sanitization can be reused.
  - _Boundary: privacy/security integration_
  - _Depends: 3.1, 4.3, 5.4_
  - _Validation: privacy/security architecture tests_
  - _Requirements: 7, 8, 13_

- [x] 7.3 Certify aggregate memory, concurrency, reload, and shutdown
  - Run race/goleak tests for multi-session reservations, Poll-vs-Finish/Forget/expiry, stale completion, counter updates, generation reload, scheduler shutdown, and original eviction.
  - Prove feature-instance pending/surrogate totals never exceed configured caps and return to correct values after cleanup.
  - Confirm no lock is held across policy/provider calls and no feature-owned poll goroutine exists.
  - _Boundary: concurrency/lifecycle_
  - _Depends: 2.3, 2.4, 5.3_
  - _Validation: focused `-race`/goleak suites where platform-supported_
  - _Requirements: 5, 11, 13_

- [x] 7.4 Certify parser/resource limits and disabled-mode performance
  - Fuzz strict decoder and bounded raw extractor; include huge/malformed/duplicate/control-character cases and ensure raw cap is applied before decode.
  - Benchmark disabled mode against existing reasoning preservation and measure bounded shadow/active overhead.
  - Verify generic scheduler outer result cap plus feature `max_output_bytes` and decoded surrogate caps compose without redundant unbounded copies.
  - _Boundary: parser/performance_
  - _Depends: 3.4, 3.5, 5.2_
  - _Validation: fuzz + benchmark + allocation-sensitive tests_
  - _Requirements: 3, 10, 13_

- [x] 7.5 Run repository gates, shadow-value review, and Kiro closeout
  - Run focused tests, `make quality-checks`, `make test-unit`, applicable parity/architecture/security checks, `go mod verify`, formatting/diff checks, and example-config/docs checks; run wider `make qa` when change surface warrants it.
  - Review a deterministic or approved shadow evidence set for savings, cost, privacy outcomes, raw-limit rejection, aggregate-budget rejection, and failure fallback; do not claim semantic quality improvement without separate task-quality evidence.
  - Revalidate this SDD against current `main` if request/terminal-pipeline simplification specs landed before implementation.
  - Update implementation ledger/spec status only after all required gates pass; keep active mode explicit and non-default.
  - _Boundary: repository/release/Kiro governance_
  - _Depends: 6.4, 7.1, 7.2, 7.3, 7.4_
  - _Validation: repository gate suite + final diff/scope audit_
  - _Requirements: 13_

## Completion Gate

Implementation is complete only when all 32 tasks are green, exact/native continuity remains unchanged, `BackgroundClient` historical source compatibility is proven, ordinary reasoning egress is policy-controlled, raw responses are byte-bounded before decode, optional state is bounded both per session and across the feature instance, shadow evidence exists, and active semantic replay remains an explicit operator opt-in.

## Completion Status

- [x] All 32 implementation tasks completed.
- [x] Implementation merged through PR #444 at `2e822c166118fd02831b42538d8411770a09af96`.
- [x] Required GitHub checks passed, including QA, CodeQL, cross-platform tests, security, coverage, and Linux race detection.
- [x] Merged `main` verification passed for reasoning preservation, public runtime, auxiliary SDK architecture, `cmd/lipstd` build, and CLI startup.
- [x] Active semantic replay remains explicit and non-default; deferred v1 follow-ups remain documented in `design.md`.

## Implementation Notes

- 1.4: Surrogate attach across policy revisions is REPLACEMENT with delta accounting (`cur-oldBytes+new`) on per-session and instance counters; `PendingCompression.SemanticDigest`/`EgressPolicyHash` correlation CAS is reserved for task 2.4.
- Host note: `go test -race` cannot run on this Windows worktree (cgo toolchain limitation); concurrency coverage is deterministic channels/sync + goleak, matching steering's Windows race skip.
