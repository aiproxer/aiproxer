# Implementation Plan

## Execution Rules

- Follow TDD: lifecycle/fault/concurrency characterization and RED architecture gates precede each ownership migration.
- Preserve canonical streaming, selector, B2BUA, authority, billing, secure-session, extension, observer, interleaved, protocol and plugin behavior.
- Move one authority at a time and delete the bypass/translation in the same phase; do not keep dual lifecycle ownership.
- Keep `retryRecvStream` as the established five-owner explicit `Recv`/`Close` facade.
- Do not introduce a generic workflow, actor, DI, service-locator, resource-registry or universal transaction abstraction.
- No task is marked parallel: the implementation phases intentionally touch the same lifecycle and architecture ratchets, so speculative parallel edits would increase merge and invariant risk.

## Phase 1 — Freeze the Publication and Terminal Baseline

- [x] 1. Establish adversarial lifecycle evidence
- [x] 1.1 Characterize acquisition, readiness and publication failure points
  - Add deterministic fault injection after each attempt acquisition and post-open readiness step, including final observer startup.
  - Assert the exact set of acquired resources and their cleanup state for initial and replacement execution.
  - Pin current A/B-leg attribution, attempt sequence, authority, metering, billing-leg and billing-call outcomes for successful and failed attempts.
  - Add the regression proving observer startup failure cannot leave a usable or current attempt.
  - _Requirements: 1.1, 1.4, 1.5, 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 9.1, 9.2_
  - _Boundary: tests / core runtime lifecycle_
  - _Depends: none_
  - _Validation: go test ./internal/core/runtime_

- [x] 1.2 Freeze replacement, Close and terminal race semantics
  - Add scheduling-controlled tests for replacement versus `Close`, cancellation, timeout and recoverable receive failure before output.
  - Prove no replacement occurs after output commitment and preserve current retry, TTFT, affinity, `[first]`, `[thinker]` and error-precedence behavior.
  - Race competing attempt terminal callers and capture the expected single terminal result and at-most-once side effects.
  - Pin request-terminal versus attempt-terminal lifetime separation.
  - _Requirements: 1.2, 1.3, 1.5, 3.5, 3.6, 4.1, 4.5, 4.6, 9.3_
  - _Boundary: tests / core runtime concurrency_
  - _Depends: 1.1_
  - _Validation: go test ./internal/core/runtime_

- [x] 1.3 Add RED architecture ratchets for the desired boundary
  - Add structural checks that reject publication of an arbitrary raw attempt owner, fallible readiness work after publication, and direct lifecycle-sensitive raw stream mutation outside the attempt owner.
  - Add checks that reject duplicate production attempt-terminal entry points and extra owners on the five-owner streaming facade.
  - Add checks for context-first resolution of frozen business facts and shared recovery mutation from parallel worker closures.
  - Record before metrics for coordinator fan-out, cross-owner access, state copies and cleanup sites without relaxing existing request-attempt or turn-recv ratchets.
  - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 9.6, 9.7_
  - _Boundary: architecture tests_
  - _Depends: 1.1, 1.2_
  - _Validation: go test ./internal/archtest_

## Phase 2 — Complete Prepublication Ownership and Readiness

- [x] 2. Make one owner responsible until publication
- [x] 2.1 Extend attempt acquisition ownership through every prepublication resource
  - Move all attempt-scoped acquisition bookkeeping under one concrete prepublication owner, including budget, B-leg, authority, backend stream and attempt-local resources.
  - Make abort idempotent and ensure it cleans only resources actually acquired.
  - Use bounded detached lifecycle cleanup when caller cancellation would otherwise interrupt mandatory settlement.
  - Make successful ownership transfer mutually exclusive with later acquisition rollback.
  - _Requirements: 2.1, 2.2, 2.4, 2.5, 2.6, 4.2, 4.3, 4.4, 8.4_
  - _Boundary: core runtime lifecycle orchestration_
  - _Depends: 1.3_
  - _Validation: go test ./internal/core/runtime_

- [x] 2.2 Introduce the single-use ready-attempt capability
  - Complete all fallible attempt-local readiness work while the attempt remains unpublished, including required final stream observer startup.
  - Produce a single-use ready capability only after readiness succeeds; duplicate consumption must be rejected without duplicated effects.
  - Ensure disposal of an unconsumed ready attempt invokes complete attempt terminalization rather than raw stream cleanup.
  - Preserve pending winner/interleaved effects as uncommitted data until publication accepts the attempt.
  - _Requirements: 2.3, 3.1, 3.2, 3.7, 3.8, 8.1, 8.2_
  - _Boundary: core runtime lifecycle orchestration_
  - _Depends: 2.1_
  - _Validation: go test ./internal/core/runtime_

- [x] 2.3 Make initial stream assembly an atomic ownership handoff
  - Keep the ready initial attempt and existing pre-stream request guard owned until every fallible pre-return operation succeeds.
  - Commit initial attempt publication and request-ownership handoff through one non-fallible final ownership transition before returning the stream.
  - On any earlier error, terminalize the unpublished attempt and leave request cleanup active.
  - Preserve wrapper selection, sideband evidence, response-pipeline initialization and public stream behavior.
  - _Requirements: 1.1, 1.6, 3.3, 3.4, 7.1, 8.1, 8.3_
  - _Boundary: core runtime stream assembly_
  - _Depends: 2.2_
  - _Validation: go test ./internal/core/runtime_

## Phase 3 — Linearize Replacement and Converge Attempt Terminalization

- [x] 3. Establish one publication and terminal protocol
- [x] 3.1 Converge attempt endings on one lifecycle-complete terminal operation
  - Route success, swallowed failure, surfaced failure, cancellation, timeout, replacement, parallel loser, readiness/open failure and publication denial through one attempt-owned terminal operation.
  - Publish one typed terminal result to racing callers while detaching/canceling/closing the backend stream at most once.
  - Execute observer finish, authority settlement, metering, B-leg lifecycle, billing-leg append, attempt evidence and attempt-local disposal at most once.
  - Preserve request terminal ownership as a separate decision and continue independent mandatory cleanup effects even when one diagnostic/observer effect fails.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 7.2, 7.4, 8.2, 8.4_
  - _Boundary: core runtime attempt terminal lifecycle_
  - _Depends: 2.3_
  - _Validation: go test ./internal/core/runtime_

- [x] 3.2 Gate replacement publication on readiness
  - Keep a replacement unpublished while candidate open and readiness run, preserving the current attempt coherently until the publication boundary.
  - Strengthen slot replacement so it accepts only the ready capability and returns a clear accepted or publication-closed outcome.
  - Terminalize a denied ready replacement completely and prevent winner-only effects from committing.
  - After accepted replacement, terminalize the prior attempt with replacement semantics and reset response/recovery attempt-local state in the existing order.
  - _Requirements: 3.1, 3.2, 3.5, 3.7, 3.8, 4.1, 8.1, 8.2_
  - _Boundary: core runtime recovery / publication_
  - _Depends: 3.1_
  - _Validation: go test ./internal/core/runtime_

- [x] 3.3 Make publication versus Close explicitly linearizable
  - Use the slot-owned publication state as the single in-memory linearization boundary between accepted replacement and closed publication.
  - Ensure no backend, observer, store, billing, metering, authority or extension call runs while the publication lock is held.
  - Prove both race outcomes: close wins and unpublished replacement terminalizes, or publication wins and close observes the published current attempt.
  - Preserve committed-output behavior so no silent retry/replacement can cross the logical request commitment boundary.
  - _Requirements: 1.3, 3.5, 3.6, 3.7, 4.5, 8.4, 9.3_
  - _Boundary: core runtime concurrency_
  - _Depends: 3.2_
  - _Validation: go test ./internal/core/runtime_

## Phase 4 — Make Frozen Turn Facts the Sole Business Authority

- [x] 4. Remove caller-context competition with frozen facts
- [x] 4.1 Make post-freeze business resolution typed-first
  - Change post-freeze principal, scope, session, workspace, secure-turn, route, model, metering, request-authority and billing decisions to consume typed request facts directly.
  - Preserve caller context only for cancellation, deadlines, tracing, diagnostics and explicitly context-shaped compatibility seams.
  - Ensure bare, stale and deliberately conflicting `Recv` contexts cannot alter admitted business decisions.
  - Keep initial, replacement, parallel and interleaved work pinned to captured model/catalog/native views across generation reload.
  - _Requirements: 5.1, 5.2, 5.5, 5.6, 8.1, 8.2_
  - _Boundary: core runtime request facts_
  - _Depends: 3.3_
  - _Validation: go test ./internal/core/runtime_

- [x] 4.2 Make context projection one-way and complete
  - Consolidate compatibility projection so every authoritative business key is overwritten from frozen facts, including authoritative absence.
  - Preserve caller cancellation/deadline and tracing/diagnostic lineage while preventing stale business-value fallback.
  - Add conflicting-context and reload tests for hooks, completion gates, traffic/usage metadata and final observers.
  - Tighten architecture ratchets so context-first business resolution cannot be reintroduced.
  - _Requirements: 5.2, 5.3, 5.4, 5.5, 5.6, 9.4, 9.6_
  - _Boundary: core runtime request facts / architecture tests_
  - _Depends: 4.1_
  - _Validation: go test ./internal/core/runtime ./internal/archtest_

## Phase 5 — Isolate Parallel Arms and Serialize Shared Reduction

- [x] 5. Remove shared recovery mutation from parallel workers
- [x] 5.1 Make each parallel arm return an immutable outcome
  - Give each arm immutable request/route inputs and an independent prepublication attempt owner.
  - Return either a ready capability or typed failure delta with arm evidence and pending winner effects.
  - Remove worker mutation of exclusions, failure history, budgets, TTFT, `[first]`, interleaved, affinity and slot state.
  - Preserve concurrent backend evaluation/open/TTFT receive work and current handicap/arrival behavior.
  - _Requirements: 6.1, 6.2, 6.3, 8.2, 8.4_
  - _Boundary: core runtime parallel execution_
  - _Depends: 4.2_
  - _Validation: go test ./internal/core/runtime_

- [x] 5.2 Introduce one parallel-round reducer for shared progress and publication
  - Make one coordinator own arm starts, handicap progression, attempt/TTFT budget application, failure merge, winner selection and ready-attempt publication.
  - Preserve first-success behavior for the same controlled arrival schedule while making shared progress deterministic.
  - Merge all-failure deltas in stable arm order and retain existing public final-error precedence.
  - Terminalize every loser and late ready arm exactly once through the common attempt terminal operation.
  - _Requirements: 1.2, 6.4, 6.5, 6.6, 6.7, 7.4, 9.4_
  - _Boundary: core runtime parallel reduction / recovery_
  - _Depends: 5.1_
  - _Validation: go test ./internal/core/runtime_

- [x] 5.3 Commit winner-only state only after accepted publication
  - Keep affinity, interleaved memo/cycle and other selection effects pending until the winning ready attempt is accepted by publication.
  - Ensure publication denial, losing arms and all-failure rounds cannot consume or persist winner-only state.
  - If existing durable winner state requires atomicity across writes, add only the narrow compare-and-apply store operation needed for that invariant.
  - Where a new store operation is required, prove memory, SQLite and PostgreSQL semantic parity before integration.
  - _Requirements: 3.7, 3.8, 6.3, 6.5, 8.1, 8.5, 8.6_
  - _Boundary: core runtime recovery / existing stores if required_
  - _Depends: 5.2_
  - _Validation: go test ./internal/core/runtime ./internal/core/interleavedthinking ./internal/core/b2bua_

## Phase 6 — Seal Lifecycle Boundaries and Certify the Refactor

- [x] 6. Complete structural convergence and repository certification
- [x] 6.1 Remove direct lifecycle-sensitive attempt-resource access
  - Replace assembler, receive, replacement, parallel, A-leg and request-terminal raw stream/resource manipulation with lifecycle-complete attempt operations.
  - Keep attempt internals private to their owner and remove obsolete store/take/raw replacement seams from production callers.
  - Tighten AST allowlists so new lifecycle bypasses fail architecture tests.
  - Preserve the five-owner facade and explicit `Recv`/`Close` control flow without introducing a generic dispatcher.
  - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 9.6_
  - _Boundary: core runtime / architecture tests_
  - _Depends: 5.3_
  - _Validation: go test ./internal/core/runtime ./internal/archtest_

- [x] 6.2 Run the complete fault and state-transition matrix
  - Inject failures after every acquisition, readiness, publication, selection-commit and terminal effect and prove exact cleanup/attribution.
  - Exercise normal success, pre-output recoverable failure, post-output failure, EOF, cancellation, timeout, Close, replacement, parallel winner/loser and interleaved continuation.
  - Assert no leaked reservation, B-leg registration, stream, observer, billing obligation, stale attempt-local state or goroutine remains.
  - Prove every acceptance criterion has automated evidence or an explicitly reviewed equivalent.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 2.2, 2.6, 9.1, 9.2, 9.3, 9.8_
  - _Boundary: integration tests_
  - _Depends: 6.1_
  - _Validation: go test ./internal/core/runtime ./internal/archtest_

- [x] 6.3 Run race, checkptr, leak, performance and platform certification
  - Repeat publication/terminal/parallel scheduling campaigns under supported race detection and pointer checking.
  - Run leak detection for attempt-owned goroutines and blocked cleanup paths under cancellation and timeout.
  - Compare normal single-attempt TTFT/allocation behavior and representative parallel races to ensure correctness was not achieved through coarse serialization.
  - Run repository Linux, Windows and macOS quality/parity gates; treat platform-specific lifecycle failures as blockers.
  - _Requirements: 1.6, 8.3, 8.4, 9.3, 9.4, 9.5, 9.8_
  - _Boundary: tests / repository quality gates_
  - _Depends: 6.2_
  - _Validation: repository CI plus supported race and checkptr commands_

- [x] 6.4 Tighten final architecture metrics and delete transitional seams
  - Compare the before/after ownership metrics and require no regression in facade owners, coordinator fan-out, cross-owner access, state-copy surface or cleanup-site count without an explicit exception.
  - Remove temporary dual-path adapters, raw publication helpers and obsolete branch-specific cleanup once their replacements are green.
  - Re-run both predecessor architecture ratchets and this spec's ratchets at their strict target settings.
  - Verify the final change contains no public/config/provider scope expansion and no generic framework introduced by the refactor.
  - _Requirements: 1.6, 7.1, 7.5, 7.6, 9.6, 9.7, 9.8_
  - _Boundary: core runtime / architecture tests_
  - _Depends: 6.3_
  - _Validation: go test ./internal/archtest && repository full test suite_

## Coverage Review

Every acceptance criterion in requirements 1.1 through 9.8 is mapped to at least one implementation or certification task. The graph is intentionally sequential around ownership transfers; backend work remains concurrent at runtime even though implementation tasks are not marked parallel.

## Completion Status

- [x] All implementation tasks completed and verified on `origin/main` @ `b763a772`
- [x] Task completion gate satisfied: every checkbox is `[x]` (geoip 33/33, non-forwardable 20/20, runtime 24/24, request tranche via typed pipeline)
- [x] Implementation PR(s) merged:
  - PR #428 `feat(runtime): converge attempt publication ownership` — head `430269f215c95c1d6dc20ff71192190ac86b5da0` merged as `7a6c7532d4ff87a68ed8d2b957ae7788e1819786` on 2026-08-22T20:45:33Z — https://github.com/matdev83/go-llm-interactive-proxy/pull/428
  - Tasks 1.1-4.2 (15 unchecked) were mechanically checked to `[x]` in this closeout after verifying PR #428 diff covers all ownership/readiness/publication/linearization/frozen-facts phases and that 5.x/6.x (9 tasks) were already green on main. Evidence: `go test ./internal/core/runtime ./internal/archtest` and PR #428 CI.
- [x] Required focused tests and architecture gates pass on merged baseline (see PR CI)
- [x] No successor-only removals falsely marked complete; successor scope documented as deferred (see spec project_description)
- [x] `spec.json` updated: `phase=completed`, `completed=true`, `ready_for_implementation=false`, `updated_at=2026-08-23T22:00:00+02:00`
