# Implementation Plan

## Delivery Slice
One corrective PR from the approved specification/source baseline, rebased only when shared-seam changes require it. First consumers are the existing streaming frontends. Five leaf tasks, each estimated at 1–3 hours; no testing-only task. Record actual change-size evidence before coding and after accepted tasks with `make dev-delivery-plan DEV_BASE=<implementation-base> DEV_CONSUMER='existing streaming frontends'`.

The current workflow generated documents only. Runtime implementation remains unstarted. Before implementation, reconcile approved adjacent billing/high-concurrency changes against the source/terminal ownership map; do not silently broaden the slice or copy their work.

- [x] 1. Make execution-result delivery transfer-or-dispose for both representations
  - Extend the existing wait producer with typed abandonment disposal and wire both canonical/replay consumers immediately; dispose and clear error-bearing streams in waited and direct modes while preserving the original error.
  - TDD: deterministic rendezvous, cancellation before/after grace, simultaneous readiness, nil result, partial result/error, disposer failure, and informational-writer panic followed by late successful/partial result; preserve informational responses and disabled behavior. Install idempotent abandonment on every exit before callbacks; preserve panic propagation. Use synchronization, not sleeps.
  - Done: each produced stream is either handed to the caller or closed once, late disposal needs no canceled caller or additional cleanup goroutine, and no abandoned writer is accessed.
  - _Requirements: 2.1, 2.2, 2.3, 2.4_
  - _Boundary: Integration — holdalive result ownership and its two frontendpipe consumers; no runtime ownership redesign_
  - _Depends: None_
  - _Validation: `go test -count=1 ./internal/plugins/frontends/holdalive ./internal/plugins/frontends/frontendpipe`_

- [x] 2. (P) Preserve truthful spill cleanup at root and final reader
  - Keep the existing shared storage lifecycle through capture completion; elect one unlink claimant, run removal outside the mutex, retain unresolved failure, and return deferred failure joined with descriptor-close error. Add the narrow safe-notification construction input for the serving consumer in task 5.
  - TDD: immediate/deferred success, already absent, failure/error identity, root versus last reader, descriptor-close barrier, shared capture/completed readers, repeated Close/no retry, and exact-once logical reservation release.
  - Done: failed unlink never records reclamation or loses its private attribution, final reader exposes failure, and notification occurs once outside the lock. No retry/persistence/worker appears.
  - _Requirements: 4.1, 4.2, 4.3, 4.4_
  - _Boundary: Core largebody — storage removal and tracked readers only; production diagnostic binding belongs to task 5_
  - _Depends: None_
  - _Validation: `go test -count=1 ./internal/core/largebody`_

- [x] 3. (P) Audit logical completion from existing request and receive evidence
  - Bind a read-only outcome projection before stream publication; consume existing request-terminal code/state and accepted logical receive evidence in the current wire audit. Make stream teardown panic-safe and ordered before root release; use the existing bounded audit-cleanup context.
  - TDD through the real executor/audit store and terminal-stopping pump: success then Close without EOF, EOF without accepted success, early cancel, partial/encoder/panic/effect failure, durable pending, replaced attempt, and pending-output withdrawal.
  - Done: successful accepted completion stays success without an extra Recv; other terminal paths remain non-success; exactly one audit and unchanged attempt/usage/billing cardinalities. No raw-event classifier, canonical audit rollout, or second terminal owner.
  - _Requirements: 1.3, 3.1, 3.2, 3.3, 3.4_
  - _Boundary: Core runtime — shared receive evidence and existing wire cleanup/audit; no billing-policy changes_
  - _Depends: None_
  - _Validation: `go test -count=1 -run '^(TestLargePayloadLifecycle_|TestBlocker1_WireOwnershipTransfer_|TestWireLifecycleEventStream_|TestDefect1_ReplacementOpen_)' ./internal/core/runtime`_

- [x] 4. Transfer accepted replay through frontend response consumption
  - Remove competing successful-handoff root closure, register returned-stream cleanup before fallible response callbacks, and preserve lexical pre-transfer error/panic/nil-success rollback. Document the internal capability's source-transfer contract; never fall back to canonical execution after commitment.
  - TDD: frontend callback panic and unusable-result cleanup, then extend the stock-host harness with first-open success followed by a receive-time pre-output recoverable error. Use the production assessor/executor, standard frontend, and actual replacement adapter; memory/disk capture and post-content failure are distinct cases.
  - Done: serving replacement reopens exact/rewrite-correct bytes, produces recovered output with two wire opens and zero canonical opens, and reclaims the source; after content, failure creates no replacement. Initial-open or fake-executor retry is not accepted evidence.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 3.3, 3.4_
  - _Boundary: Integration — frontendpipe source transfer, internal capability documentation, and stock-host serving witness; core outcome mechanics remain task 3_
  - _Depends: 1, 3_
  - _Validation: `go test -count=1 ./internal/plugins/frontends/frontendpipe`; `go test -count=1 -run '^(TestLargePayloadLifecycle_|TestStockHost_OpenAIResponses_WireReachability_AndDeclineParity)' ./internal/infra/runtimebundle`_

- [x] 5. Bind pathless cleanup notices to production capture
  - Connect the existing source-owned notification to the frontend logger fallback, keeping only safe bounded correlation; emit a fixed failure reason independently of ignored Close errors. Ensure notification cannot prevent cleanup state publication or contaminate response outcome.
  - TDD through production capture: injected immediate/deferred unlink failure produces one notice even when the root/reader error is ignored; no path, prompt, digest/model, or raw filesystem error appears, and successful output/audit and logical reservation release remain unchanged.
  - Done: both spill-removal timing paths have a serving diagnostic consumer, no abandoned response writer is retained, and no observer/registry/retry service is introduced. Review the coherent diff for single ownership and optional-method preservation, then run applicable complete packages and contract feedback.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 3.4_
  - _Boundary: Integration — frontendpipe capture wiring and existing largebody notification contract; no diagnostics framework or spool-accounting redesign_
  - _Depends: 2, 4_
  - _Validation: `go test -count=1 ./internal/core/largebody ./internal/plugins/frontends/holdalive ./internal/plugins/frontends/frontendpipe ./internal/core/runtime ./internal/infra/runtimebundle`; `make dev-contract-check`_

## Dependency and Parallel Safety
Tasks 1–3 can run independently on separate production files; 2 and 3 carry `(P)`. Task 4 waits for cancellation disposal and core outcome cleanup before changing the frontend handoff. Task 5 waits for storage notification and the final handoff to avoid edits racing in production capture. No task depends on unimplemented financial/performance spec tasks. All new regressions use the `TestLargePayloadLifecycle_` prefix where useful; complete package checks prevent a narrow regex from serving as sole delivery evidence.

## Implementation Evidence and Closeout
For each task record claim/source/mutant, fresh red→green results, source revision, files changed, and skipped scope. Use existing generic architecture/ownership rules, not a new feature gate. Local race runs are forbidden; remote CI owns race evidence, and OS-sensitive unlink/descriptor behavior needs applicable remote platform checks. Existing allocation ratchets may check hot-path regressions; no live-provider/financial benefit is asserted. Spec-generation validation is not runtime completion evidence; archive only after later whole-scope implementation validation.

## Completion Status
All five tasks are `[x]` and merged on `main`.

- [x] Task 1 — transfer-or-dispose for both representations. Merged in #1064 (`383bb36b`).
- [x] Task 2 — truthful spill cleanup at root and final reader. Merged in #1064.
- [x] Task 3 — logical completion audited from request and receive evidence. Merged in #1064.
- [x] Task 4 — accepted replay transferred through frontend response consumption, with the stock-host serving witness. Merged in #1064.
- [x] Task 5 — pathless cleanup notices bound to production capture. Merged in #1064.
- [x] Requirements 1.1–4.4 implemented; each criterion proven by a test whose failure mode is tied to the behavior.
- [x] Corrective rounds merged: #1067 (`ce590fb6`) repaired four review findings in #1064; #1069 (`9db3d930`) repaired three findings that #1067 introduced; #1081 (`9a44bf40`) consolidated duplicated tests without losing a unique proof.

Merged baseline: `9a44bf40394cca8f0a11587b4b0a65c578212fee` (#1081) plus the unrelated flake fix in #1088. Race evidence remains remote-only; the fast path stays default-off and was never production-enabled. Deferred workstreams in `requirements.md` remain deferred.
