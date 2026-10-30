# Implementation Plan

## Execution and Delivery Conditions
Four sequential leaf tasks, each 1–3 hours, one net-shrinking PR. No parallel markers: successive tasks share assessment tests and prerequisites. The owner subsequently authorized implementation; all four tasks are implemented and independently reviewed.

Before task 1, record the actual source base, confirm the dedicated task branch, refresh the research consumer inventory, and reconcile lifecycle/proof/financial/performance changes. Prefer correctness-first coordinated delivery, but do not pretend an adjacent approved spec is implemented. If a retained regression requires a sibling repair, integrate that repair into the base first rather than skip evidence or bundle behavior changes. No new runtime/setup dependency is introduced.

Use characterization-first TDD for equivalent deletion: observe meaningful serving claims before editing; preserve or migrate them with independent expected values; verify after removing the unused code. For unique migrated assertions, deliberately invalidate the relevant fact/validator in external scratch and confirm rejection is observable, then restore and rerun. Do not invent a failing absence-of-symbol test or a generic test framework. Measure the actual slice using existing delivery reporting after each task; estimates in design are not authorization to exceed gates.

- [x] 1. Retire unused authority and initial-assessment alternatives
  - Refresh all serving/test/embedding/public-alias/connector references for the selected families. Retain any newly serving or public-contract candidate and revalidate the boundary rather than deleting it.
  - Characterize concrete authority and initial-route decisions, then remove the alternative adapters/helper and exclusively dependent duplicate assertions, keeping conservative census, static eligibility and optional capability contracts intact.
  - Prune obsolete local-turn/secret-guard/terminal-decision wrapper assertions without changing occupied-plane policy. Remove the non-injected sentinel assertion; keep meaningful serving purity evidence.
  - Done: no selected alternative constructions remain, concrete-gate and production-assessor behavior is unchanged, and the task independently builds with passing affected package tests.
  - _Requirements: 1.1, 1.2, 1.3, 2.1, 2.2, 3.4_
  - _Boundary: Core large-request authority and initial assessment; directly dependent tests only_
  - _Depends: None_
  - _Validation: go test -count=1 ./internal/core/largebody ./internal/core/runtime; refreshed caller inventory; delivery report_

- [x] 2. Retire alternative route and backend assessment without losing domain proof
  - Characterize finite/universal domains, aliases, immutable rewrite threading and actual override rejection before removing alternative route/late/backend assessors and simulated override execution.
  - Preserve unique combined late-domain finite union, mixed-envelope decline and universal union outcomes through the existing concrete proof composition, with literal expected facts rather than alternate-model oracles.
  - Own both backend- and late-selector-wrapper-dependent cases in the stamp tests now; migrate request/domain capability-tamper assertions and classify remaining affected claims before deleting the families. Begin with a valid control and matching unrelated generations; assert the precise capability disagreement and prove the migrated assertion rejects a targeted fault.
  - Delete redundant wrapper/override-helper assertions rather than recreating them in a new harness. Keep actual production-assessor and executor capability/domain semantics; preserve all concrete gates and shared union logic.
  - Done: alternative route/backend families and their simulation methods have no remaining references, unique proof claims execute against retained serving surfaces, and this task builds independently before the stamp/DTO task starts.
  - _Requirements: 1.1, 1.2, 1.3, 2.1, 2.2, 2.3, 3.4_
  - _Boundary: Core large-request route/backend proof composition and direct-validator claim migration; directly dependent tests_
  - _Depends: 1_
  - _Validation: go test -count=1 ./internal/core/largebody ./internal/core/runtime; focused combined-domain and capability non-vacuity probes; delivery report_

- [x] 3. Retire conservative dependency projections and the alternative stamp executor
  - Characterize direct profile/body-mode/rewrite/domain/source disagreement and invariant error identity, retaining unique cases not already covered by real execution.
  - Delete the unused dependency DTO/gate family and stamp executor adapter; remove projection/boolean-only assertions without changing actual fresh-session, compaction, response-authority, admission or census policy.
  - Move the existing narrow-port continuation/terminal census assertion unchanged to the remaining authority tests. Do not port the obsolete blanket response-only wire blocker.
  - Keep valid direct-validator controls and the earlier capability-tamper cases; faults must fail the targeted oracle, not an unrelated generation mismatch.
  - Done: the projection/gate file and stamp adapter are gone, all unique preserved invariants are observed at existing validators/census/real executor, and affected suites pass without relaxed error expectations.
  - _Requirements: 1.1, 1.2, 1.3, 2.1, 2.2, 2.3, 3.3, 3.4_
  - _Boundary: Core large-request direct stamp validation and serving dependency census; dependent tests_
  - _Depends: 2_
  - _Validation: go test -count=1 ./internal/core/largebody ./internal/core/runtime; refreshed no-consumer evidence; delivery report_

- [x] 4. Retire the isolated race-transfer entrance and align the current serving explanation
  - Characterize real execution race winner-content, handicap, noncooperative-loser and panic isolation; sequential two-turn weighted-first/store failure; pre-output recovery, post-content prohibition and cancellation/EOF/early-close outcomes.
  - Remove the production-resident test race input/result/helper and its special stream-transfer chain without touching real route planning, attempt payloads, publication/consumption, recovery or terminal owners.
  - Delete unsupported synthetic parallel weighted-first, harness private-field equality and retired transfer-method assertions; keep actual serving race/recovery/ownership evidence. Add at most a small existing-test-local helper only for a demonstrated unique claim, never a replacement engine.
  - Explicit core/docs integration: update only the current operator explanation that names the retired harness. Leave archived specs and historical evidence intact.
  - Done: all deletion-set references are absent from serving code, public contracts and runtime policy are unchanged, preserved regressions execute, the current doc points to actual shared execution, and the measured slice is substantially net-shrinking.
  - _Requirements: 1.1, 1.2, 1.3, 2.1, 2.2, 2.3, 3.1, 3.2, 3.3, 3.4_
  - _Boundary: Explicit integration of core runtime test-entrance deletion, real execution evidence, and current operator documentation_
  - _Depends: 3_
  - _Validation: go test -count=1 ./internal/core/largebody ./internal/core/runtime ./internal/infra/runtimebundle ./internal/plugins/frontends/frontendpipe; go test -count=1 -tags=integration ./internal/infra/runtimebundle -run '^TestStockHost_'; go test -count=1 ./internal/archtest; make dev-contract-check DEV_FRESH=1; make dev-lint PKGS='./internal/core/largebody ./internal/core/runtime'; delivery report and applicable hook/CI gates_

## Acceptance Limits
No new architecture gate, documentation-content test, benchmark target, provider load test, paid-wire activation or public contract. If any serving concurrency owner changes, revalidate scope and obtain applicable remote CI race evidence; never run race locally. Passing local characterization does not alone certify final delivery or sibling correctness. Record skipped/blocked required evidence and baseline failures explicitly.

## Completion Status
- [x] Task 1: `e5cf9aef8`; independent phase review APPROVED.
- [x] Task 2: `571ba870d`; independent phase review APPROVED.
- [x] Task 3: `ba89faf19`; independent phase review APPROVED.
- [x] Task 4: `fd028dafc`; independent phase review APPROVED.
- [x] Branch-level regression, stock-host integration, architecture, contract, lint, build and CLI smoke checks passed; see [validation](validation.md).

Archived before PR submission at the owner's explicit request. Implementation base: `d786a87e1`; verified implementation head: `fd028dafc`. PR CI and merged-main verification remain pending, not certified by this archive. Deferred sibling work remains outside this slice.
