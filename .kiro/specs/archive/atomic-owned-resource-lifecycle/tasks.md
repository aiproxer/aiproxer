# Implementation Plan

Implementation is TDD-first. The plan deliberately limits work to the two validated high-ROI ownership seams and finishes with a simplification gate.

## 1. Freeze Ownership Behavior With RED Tests

- [x] 1.1 Define RED process-owner contract tests
  - Pin successful owned acquisition requires a non-nil release and appends it to the authoritative `ProcessServices` closer set before the value can be used by the next construction step.
  - Pin value-only/non-owning construction bypasses the owned helper and an owned-success path cannot silently return a nil release.
  - Pin reverse-order rollback and aggregate cleanup errors through the existing process failure path after a later injected constructor failure.
  - Pin normal `ProcessServices` shutdown consumes the same release set exactly once with no owner handoff or parallel legacy registration.
  - _Requirements: 2.1-2.7, 6.1_
  - _Boundary: runtime composition process lifetime only_
  - _Depends: none_
  - _Validation: focused runtimebundle ownership/process-services tests_

- [x] 1.2 (P) Characterize selected process-builder cleanup ordering
  - Record the current resource close order for the control-plane, authority, persistence, accounting, metering, and terminal-work construction graph.
  - Add fault-injection cases after representative successful acquisitions and before later fallible steps.
  - Prove plugin host/artifact/staging and database pool special ordering remains unchanged.
  - Prove current cleanup error aggregation continues across failures.
  - _Requirements: 3.1-3.6, 5.5, 6.1_
  - _Boundary: ProcessServices construction graph_
  - _Depends: none_
  - _Validation: focused constructor failure/ownership lifecycle tests_

- [x] 1.3 (P) Define RED generation-loop lifetime tests
  - Prove a newly created composition-owned loop performs no application work before lifecycle ownership is established and the start gate exits on cancellation.
  - Prove synchronous immediate `AddClose` cleanup on sealed/quiesced ownership cancels and joins the still-gated loop without deadlock.
  - Prove rollback and quiesce both cancel and join the loop, with model-registry refresh at `PhaseQuiesce` completing before catalog `PhaseClose`.
  - Run leak/race-focused cases with repeated concurrent retirement and preserve rollback refresh-before-catalog ordering.
  - _Requirements: 4.1-4.7, 6.2-6.3_
  - _Boundary: generation composition lifecycle only_
  - _Depends: none_
  - _Validation: focused runtimebundle race/goleak lifecycle tests_

## 2. Add the Minimal Private Ownership Primitives

- [x] 2.1 Implement the private process resource owner and acquire helper
  - Add a construction-only process owner that appends directly into the existing `ProcessServices` closer set; do not add a separate release collection or `ReleaseAll` path.
  - Add an owned-only acquire-plus-release helper that requires a non-nil release before returning a successful value; keep value-only construction outside that helper.
  - Keep constructor rollback and `ProcessServices.Close` as the only consumers of the authoritative process closer set, preserving existing shutdown idempotency.
  - Expose no lookup, provisioning, lazy service, reflection, global registry, or public API.
  - _Requirements: 1.1-1.6, 2.1-2.7_
  - _Boundary: runtimebundle private composition infrastructure_
  - _Depends: 1.1_
  - _Design: Process Resource Owner; Process Acquisition Helper_

- [x] 2.2 (P) Implement the narrow ledger-backed cancel+join loop helper
  - Create the loop behind a cancellation-aware start gate, register a close-only cancel+join action with `ResourceLedger.AddClose`, then release application work only if cancellation has not won.
  - Preserve the caller-selected cleanup phase and ledger error semantics; for model-registry refresh use `PhaseQuiesce` while catalog close remains `PhaseClose`.
  - Handle synchronous immediate cleanup/closing ownership by canceling and joining the still-gated loop without leak or deadlock.
  - Add no scheduler, worker pool, restart policy, supervision tree, or generic async error channel.
  - _Requirements: 1.2-1.6, 4.1-4.6, 5.1_
  - _Boundary: runtimebundle generation-owned background loops_
  - _Depends: 1.3_
  - _Design: Generation Loop Helper_

## 3. Migrate Only the Proven High-ROI Call Sites

- [x] 3.1 Migrate selected process builders from closer propagation to local ownership
  - Change the selected process-owned builders to accept the private process owner and register acquired releases before later fallible construction.
  - Return runtime values/errors without caller-visible closer lists for the migrated paths.
  - Remove the corresponding `NewProcessServices` closer aggregation/adaptation plumbing as each path moves.
  - Leave special pool and plugin staging/artifact teardown explicit where that preserves clearer ordering.
  - _Requirements: 3.1-3.7, 5.5-5.6, 6.5-6.7_
  - _Boundary: ProcessServices and selected process-owned builder families_
  - _Depends: 1.2, 2.1_
  - _Design: Migrated Process Builders; Migration Plan_

- [x] 3.2 Migrate the model-registry refresh loop to structured generation ownership
  - Replace the manual derived-context/cancel/wait-group lifecycle with the private loop helper.
  - Preserve refresh cadence and model-registry behavior; keep refresh cancel+join at `PhaseQuiesce` and catalog close at `PhaseClose`.
  - Prove candidate rollback and superseded-generation quiesce terminate the refresh loop before catalog cleanup, including the existing reverse registration order on rollback.
  - Do not migrate unrelated goroutines unless characterization proves the identical cancel+join lifetime shape.
  - _Requirements: 4.1-4.7, 5.1, 5.4-5.5, 6.2-6.3_
  - _Boundary: generation-owned model-registry refresh lifecycle_
  - _Depends: 2.2_
  - _Design: Generation-Owned Loop; Initial migration_

- [x] 3.3 Lock unchanged backend and generation semantics
  - Retain characterization tests proving backend cleanup remains generation-owned and transfers to the existing ledger.
  - Prove backend plugin factory/ABI contracts and connector process supervision did not change.
  - Prove generation publication, pinning, retention, drain, and retirement behavior remains unchanged.
  - Prove request-path routing/stream/accounting/session outputs are unaffected by the composition refactor.
  - _Requirements: 5.1-5.6_
  - _Boundary: unchanged lifecycle contracts; regression-only_
  - _Depends: 3.1, 3.2_
  - _Validation: existing backend/reload/generation/runtime regression suites_

## 4. Ratchet Scope and Prove the Refactor Pays for Itself

- [x] 4.1 Add architecture/source-shape gates against ownership abstraction creep
  - Fail if the new ownership primitive becomes exported or moves outside runtime composition.
  - Fail if it grows keyed lookup or service-locator concepts such as `Get`, `Resolve`, or `Provide`.
  - Fail if the selected migrated process builders reintroduce caller-visible closer-list ownership.
  - Confirm no public config/API/plugin/canonical contract or external dependency was added.
  - _Requirements: 1.3-1.6, 2.5-2.6, 3.5, 5.2, 6.4-6.5_
  - _Boundary: architecture tests and runtime composition_
  - _Depends: 3.1_
  - _Validation: focused archtest/source-shape tests_

- [x] 4.2 Run lifecycle safety, race, leak, and repository quality gates
  - Run focused process rollback/close and generation-loop tests including fault injection.
  - Run targeted race/leak coverage for repeated generation quiesce/retirement and loop teardown.
  - Run repository quality/architecture checks and default unit tests.
  - Compare existing generation acquire/dispatch/request benchmarks or equivalent hot-path baselines and confirm no material regression.
  - _Requirements: 6.1-6.5, 6.8-6.9_
  - _Boundary: verification only_
  - _Depends: 3.1, 3.2, 3.3, 4.1_
  - _Validation: `make quality-checks`; `make test-unit`; targeted `go test -race`; existing lifecycle benchmarks_

- [x] 4.3 Perform final simplification review
  - Delete superseded caller-side closer aggregation, adapters, and duplicated lifecycle wrappers made unnecessary by the migration.
  - Verify acquisition-to-release ownership is easier to trace in every migrated path than in the baseline.
  - Revert or simplify any migrated call site that adds more lifecycle concepts than it removes.
  - Confirm the final diff remains limited to the frozen high-ROI composition ownership scope.
  - _Requirements: 1.6, 3.4-3.7, 6.6-6.7_
  - _Boundary: final runtimebundle refactor scope_
  - _Depends: 4.2_
  - _Validation: final diff/architecture review_
