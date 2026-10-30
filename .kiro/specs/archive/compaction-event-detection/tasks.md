# Implementation Plan

Implementation is TDD-first. Each task is deliberately bounded to no more than five concrete actions.

## 1. Freeze Public and Detector Contracts With RED Tests

- [x] 1.1 Define RED SDK observer and FeatureBundle tests
  - Specify `Phase`, `Evidence`, metadata-only `Event`, and `Observer` behavior.
  - Require additive `FeatureBundle.CompactionObservers`, merge ordering, and snapshot defensive-copy/freeze semantics.
  - Prove callback error and panic are fail-open and one listener cannot suppress later listeners.
  - Prove event payload exposes no canonical/raw request or response content.
  - _Requirements: 1.1-1.4, 2.1-2.6, 8.1, 8.3_
  - _Validation: `go test ./pkg/lipsdk/... ./internal/featurebundle/... ./internal/core/extensions/...`_

- [x] 1.2 Define RED strict rule-matrix tests
  - Create table-driven positive cases for every versioned rule in `research.md`, including explicit canonical compact operation/item.
  - Add near-miss negatives for ordinary summarization, no-tools calls, partial marker overlap, and customizable prompt portions.
  - Prove indistinguishable Pi/OpenClaw traffic keeps a shared/neutral rule identity.
  - Prove protocol-strict evidence takes precedence over text signatures when both are present.
  - _Requirements: 3.1, 3.3-3.7, 4.1-4.8, 8.1, 8.6_
  - _Validation: `go test ./internal/core/compactiondetect/...`_

- [x] 1.3 Define RED heuristic and transaction-state tests
  - Pin deterministic request fingerprint semantics and concrete absolute/relative reduction thresholds.
  - Prove same-A-leg + retained two-item tail + removed older prefix can complete heuristically; reset/new-A-leg/near-threshold cases do not.
  - Cover `single`, `series`, and `completion-only`, including Pi/OpenClaw/Gemini/Aider repeated utility calls.
  - Cover strict-post versus heuristic dedupe, old-complete-before-new-start ordering, expiry/max-entry cleanup, and content-free retained state.
  - _Requirements: 5.1-5.7, 6.1-6.7, 7.3-7.7, 8.1, 8.6_
  - _Validation: `go test ./internal/core/compactiondetect/...`_

## 2. Implement the Minimal SDK and Detector

- [x] 2.1 Implement typed compaction observer surface
  - Add `pkg/lipsdk/compaction` types exactly as frozen by 1.1.
  - Add observer slice to FeatureBundle, single merge point, and frozen snapshot accessor.
  - Add a small ordered dispatcher that isolates panic/error and never mutates traffic.
  - Do not add a StageID, FailureMode, asynchronous queue, or new dependency.
  - _Requirements: 1.1-1.6, 2.1-2.6, 8.2-8.3_
  - _Design: Public SDK Contract; Observer Dispatch_

- [x] 2.2 Implement canonical strict/signature rule catalog
  - Add concrete `internal/core/compactiondetect` detector with static versioned rule table and explicit match functions.
  - Reuse `lipapi.NormalizedItems`/walkers plus existing operation/item semantics; add no provider DTO imports.
  - Implement the primary eight-agent rule families plus Gemini/Roo/Aider/Crush extras and near-miss-safe conjunction matching.
  - Keep unsupported `compaction_trigger` / `context_management` request controls out of canonical admission/detection.
  - _Requirements: 3.1, 3.3-3.7, 4.1-4.8, 8.2_
  - _Design: Core Detector; Strict Detection; Protocol Compatibility Boundary_

- [x] 2.3 Implement bounded A-leg heuristic and transaction state
  - Add process-safe `ALegID -> legState` map with one minimal mutex and deterministic transaction IDs.
  - Store only counts/timestamps/bounded SHA-256 semantic hashes and transaction metadata.
  - Implement conservative history comparison and strict-over-heuristic precedence.
  - Implement single/series/completion-only coalescing plus lazy TTL/max-entry eviction; no goroutine or persistence.
  - _Requirements: 5.1-5.7, 6.1-6.7, 7.1-7.7_
  - _Design: History Heuristic; Transaction State; Lifetime/Bounds/Concurrency_

## 3. Wire Detection Into Existing Runtime Choke Points

- [x] 3.1 Make detector process-owned across generations
  - Construct one detector in `runtimebundle.ProcessServices` and pass a non-owning reference into generated runtime executors.
  - Preserve nil/no-op behavior where process services are absent in focused tests.
  - Add generation-rebuild test proving one A-leg fingerprint/active transaction survives runtime snapshot replacement.
  - Do not add DB schema, B2BUA record fields, or lifecycle persistence.
  - _Requirements: 7.1-7.2, 7.5-7.6, 8.6_
  - _Design: Lifetime, Bounds, and Concurrency_

- [x] 3.2 Integrate request detection only after successful upstream open
  - Analyze/store the effective canonical baseline associated with authoritative A-leg/trace correlation.
  - Invoke `RequestOpened` after initial backend Open succeeds; dispatch returned events to the frozen observer set.
  - Ensure replacement/failover B-leg opens for the same logical request cannot duplicate start/transaction state.
  - Add tests for signature-looking request rejected before Open and successful explicit context-compaction Open.
  - _Requirements: 3.1-3.2, 3.5, 4.6, 5.1, 8.3, 8.5_
  - _Design: Request-side Flow_

- [x] 3.3 Integrate response detection at the final canonical release seam
  - Route every event actually returned by `retryRecvStream` through one observation helper after existing finalizer/reactor/hook/gate/recovery selection.
  - Feed released event plus A-leg/B-leg/attempt correlation to `ResponseReleased`, dispatching derived events fail-open.
  - Prove live, gated, tool-finalizer, synthesized/recovery drain paths are observed exactly once and the returned canonical event is byte/field equivalent.
  - Prove `ItemKindCompaction`/explicit compact terminal completion deduplicates correctly.
  - _Requirements: 3.3-3.5, 4.7, 8.3-8.4_
  - _Design: Response-side Flow_

## 4. Cross-Agent and Architecture Quality Gates

- [x] 4.1 Run comprehensive cross-agent detector regression matrix
  - Exercise Codex, Pi/OpenClaw, Cline, OpenCode, Hermes, KiloCode, Claude snapshot, Gemini, Roo, Aider, and Crush fixtures on canonical calls.
  - Exercise local-only compaction heuristic with resets, forks/new A-legs, short contexts, same-size rewrites, and reordered tails as negatives.
  - Prove series utilities produce one start/one maximum completion and completion-only rules never invent starts.
  - Verify no listener event or stored detector state contains fixture prompt text.
  - _Requirements: 1.5-1.6, 4.3-4.8, 5.1-5.7, 6.1-6.7, 7.3, 7.7, 8.6_
  - _Validation: focused detector/runtime suites_

- [x] 4.2 Run race, repository quality, and simplification gates
  - Run detector/runtime race tests with concurrent A-legs and overlapping turns.
  - Run formatting, vet, architecture/line-budget checks, and deterministic repository unit suites required by current CI.
  - Confirm no provider/frontend signature code, new external dependency, durable store, background worker, mutating observer, or detection-only `lipapi.Event` field was added.
  - Review final diff and remove interfaces/configuration/generalized frameworks that are not required by the frozen design.
  - _Requirements: 2.3-2.6, 7.4-7.6, 8.2-8.8_
  - _Validation: `make quality-checks`; `make test-unit`; targeted `go test -race`_

## Completion Status

- [x] Tasks 1.1-4.2 are implemented and merged by PR [#376](https://github.com/matdev83/go-llm-interactive-proxy/pull/376) at `2b30116cb0cfb2fc6e1b49aafe8838a5e64403b8`.
- [x] The cross-agent rules, heuristic-negative, transaction-series, completion-only, and content-free-state matrices are present in the merged detector suites.
- [x] PR #376 passed protected QA, repository hygiene, security, CodeQL, compliance, coverage, process-tree, and Linux/macOS/Windows test checks.
- [x] The successor continuity implementation and certification are merged through PR [#385](https://github.com/matdev83/go-llm-interactive-proxy/pull/385) on `main` at `d606571735701ad6f767bd35b939597b4f8ad44f`.
- [x] Windows local race execution remained unavailable because ThreadSanitizer failed allocation with error 87; repeated, checkptr, goleak-backed, and protected multi-platform CI coverage supplied the recorded concurrency evidence.
