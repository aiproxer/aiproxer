# Implementation Plan

Implement the generic provisional-terminal extension in strict RED -> minimal implementation -> GREEN -> refactor cycles. Tasks below own the platform only; concrete Agent Loop Guard policy is implemented by the dependent `agent-loop-breach-prevention` spec after this spec's contract and integration tasks are complete. Do not add provider-specific branches, speculative abstractions, or unrelated cleanup.

## Foundation and Baseline

- [x] 1. Establish baseline evidence and contract test harness
- [x] 1.1 (P) Characterize terminal, bundle, lifecycle, policy, and cleanup ownership counts
  - Record deterministic counts for current logical terminal claim sites, FeatureBundle contribution fields, concrete ALG references in core, policy owners, and continuation cleanup paths.
  - Capture the current no-provider behavior and existing reload/withdrawal order as executable characterization tests or bounded reports.
  - The baseline artifact is reproducible and identifies the target operation counts used by the simplification gate.
  - _Requirements: 11.1, 11.2_
  - _Boundary: tests and architecture evidence_
  - _Validation: focused architecture/characterization tests and `git diff --check`_

- [x] 1.2 (P) Add deterministic schedule fixtures for platform failure paths
  - Define barriers for provider timeout/error/malformed result, cancellation races, B1 settlement versus B2 admission, generation withdrawal, policy write versus snapshot, and stale overlay cleanup.
  - Each fixture records the expected single terminal, settlement, cleanup, and policy state outcome without relying on timing sleeps.
  - The schedule harness can be reused by the terminal, policy, and lifecycle tests without creating a generic scheduler runtime.
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.6, 9.4_
  - _Boundary: internal/testkit platform schedules_
  - _Validation: focused schedule fixture tests_

## Provider Contract and Composition

- [x] 2. Add the exclusive provider contract and FeatureBundle contribution
- [x] 2.1 (P) Write RED contract tests for bounded canonical provider decisions
  - Test canonical input bounds, immutable policy snapshot, allow-stop and continue-intent validation, unknown decision rejection, and absence of provider SDK/raw transport types.
  - Test that a provider cannot claim a terminal, mutate a request snapshot, open a backend, or supply unbounded control content through the contract.
  - The RED tests fail until the provider-neutral DTOs and validation behavior exist.
  - _Requirements: 1.1, 1.4, 1.5, 3.1, 9.4, 10.1_
  - _Boundary: SDK/public provider contract_
  - _Validation: focused `go test ./pkg/lipsdk/terminaldecision/...`_

- [x] 2.2 Define the singular FeatureBundle field and exclusive merge behavior
  - Add one provider contribution to the feature contract and immutable request/generation projection; reject nil/typed-nil and malformed providers.
  - Make the merge point accept zero or one provider, and fail candidate generation compilation with both identities when two are contributed.
  - Preserve all existing feature fields and no-provider behavior; do not create a provider slice, ordering chain, or service lookup.
  - _Requirements: 1.1, 1.2, 1.3, 4.1, 10.2, 10.4, 12.1_
  - _Boundary: feature bundle and composition merge_
  - _Depends: 2.1_
  - _Validation: focused feature merge tests and existing feature bundle tests_

## Core Terminal Boundary

- [x] 3. Implement the single core terminal decision chokepoint
- [x] 3.1 (P) Add RED tests for candidate holdback and exactly-once terminal ownership
  - Exercise normal, transport-derived, limit, refusal, cancellation, and provider-error candidate paths through one chokepoint.
  - Prove no A-side final marker is emitted while a decision is pending, each B attempt settles once, and one A terminal wins under cancellation/close/provider races.
  - Prove post-output decisions cannot be labeled retry or failover and no-provider behavior remains unchanged.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 8.1, 8.2, 9.1_
  - _Boundary: core terminal decision and terminal owner integration_
  - _Validation: focused runtime/terminal tests and targeted race command where supported_

- [x] 3.2 Route all logical terminal candidates through the core chokepoint
  - Place the provider call immediately before the existing logical terminal claim without adding alternate terminal owners or a second finish path.
  - Normalize timeout, error, panic, malformed, unknown, and unsupported results to one allow-stop outcome with bounded evidence.
  - Preserve authoritative cancellation/refusal/authority outcomes and existing pre-output transport recovery semantics.
  - The observable runtime has one terminal claim boundary and no provisional terminal leakage.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 8.1, 9.1, 10.3, 10.4_
  - _Boundary: core terminal decision orchestration_
  - _Depends: 2.2, 3.1_
  - _Validation: focused runtime tests, terminal ownership tests, architecture import ratchets_

## Core Continuation Transaction

- [x] 4. Implement core-owned continuation validation and transaction
- [x] 4.1 (P) Add RED tests for safe continuation and failure schedules
  - Prove B2 is prepared/opened and atomically published as current before B1 is settled; pre-publication failure leaves B1 unsettled and finalizes the original request normally, while post-publication B1 settlement loss/error keeps B2 current without fabricated rollback.
  - Prove materialization, authority, protocol, steering, admission, and backend-open failures preserve committed output and claim one final outcome.
  - Prove completed tool results are not re-executed, cancellation wins, and all work/leases/overlays are joined or deactivated.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.6, 8.2, 8.4, 9.2, 9.4_
  - _Boundary: core continuation transaction_
  - _Validation: focused continuation/runtime integration tests_

- [x] 4.2 Implement the bounded continuation transaction and canonical hidden-content lifecycle
  - Validate intent before side effects; prepare/open and atomically publish B2 through existing materialization and normal admission paths before settling B1; never classify B2 as retry/replacement.
  - On any failure before publication, deactivate partial steering and finalize B1 normally without pre-settlement; after publication, settle B1 exactly once and retain B2 if settlement reports loss/error, with bounded diagnostics and no rollback.
  - Register hidden control content through the canonical steering writer, resolve the accepted user-ingress anchor, freeze the next turn snapshot, and reassert it exactly once before backend open.
  - Deactivate the fixed provider-scoped overlay on final terminal, cancellation, exhaustion, open failure, and external ingress; not-found/inactive is an idempotent no-op and persistence failure fails closed.
  - Direct `Call.Messages`/`Call.Items` append and ad-hoc hidden fields are absent from continuation logic.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 8.2, 8.4, 9.2, 10.3, 10.4_
  - _Boundary: core continuation and conversation-view integration_
  - _Depends: 3.2, 4.1_
  - _Validation: focused runtime/conversation-view tests and architecture ratchets_

## Immutable Generation and Process Policy

- [x] 5. Wire immutable provider activation and withdrawal
- [x] 5.1 (P) Add RED lifecycle tests for candidate rollback and pinned requests
  - Prove provider construction/validation failure leaves the last-good generation serving and unwinds partial acquisition in reverse order.
  - Prove reload does not mutate a published request, withdrawal quiesces/drains before close, and disabled generations contain no provider for new requests.
  - Prove an admitted request retains its provider until normal generation retirement completes.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 8.3, 8.5_
  - _Boundary: runtimebundle generation composition_
  - _Validation: focused generation/reload lifecycle tests_

- [x] 5.2 Implement explicit generation projection and withdrawal wiring
  - Compose the singular provider into the immutable generation/request snapshot and construct all concrete dependencies at the composition root.
  - Preserve manager publish, pin, retain, quiesce, drain, and close ordering; do not live-rebind provider or policy fields.
  - Candidate failure disposes acquired resources before returning and leaves the last-good generation untouched.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 10.2, 10.3, 12.1, 12.2_
  - _Boundary: runtimebundle composition root and generation lifecycle_
  - _Depends: 2.2, 5.1_
  - _Validation: runtimebundle lifecycle tests and focused reload suite_

- [x] 6. Add the bounded process-owned secure-session policy
- [x] 6.1 (P) Write RED policy truth-table, bounds, revision, and lifecycle tests
  - Cover all client/operator tri-state combinations and prove any explicit disable wins, otherwise enable wins, otherwise generation default applies.
  - Cover bounded key/value capacity, rejection without mutation, serialized key-boundary revision linearization without request-side expected revisions, unauthorized scope access, write-vs-snapshot ordering, reload/disable-reenable retention, and restart reset.
  - Prove policy writes do not mutate an admitted request's frozen snapshot.
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 6.1, 6.2, 6.3, 6.4, 6.5, 8.6, 9.4_
  - _Boundary: process terminal-decision policy store_
  - _Validation: focused policy package tests and deterministic concurrency fixtures_

- [x] 6.2 Implement policy store ownership, effective state, and request snapshot
  - Construct exactly one bounded store under `ProcessServices`; keep it outside generation ownership and close it exactly once with process shutdown.
  - Store separate client/operator tri-state values, revision metadata, and safe scope identity; apply the disable-first effective rule atomically.
  - Resolve policy once during request admission and pass the immutable value to the provider/chokepoint; do not perform mutable policy lookup on the stream hot path.
  - The store survives reload and provider disable/re-enable, starts empty after restart, and rejects writes while closing.
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 6.1, 6.2, 6.3, 6.4, 6.5, 8.6, 10.3_
  - _Boundary: ProcessServices and request admission policy wiring_
  - _Depends: 6.1, 5.2_
  - _Validation: focused policy/runtimebundle tests and process close tests_

## Generic Policy Endpoints

- [x] 7. Mount generic client and operator policy surfaces
- [x] 7.1 (P) Add RED endpoint contract and authorization tests
  - Test the exact client and operator paths, bodies, self-scope/target authorization, unknown features, invalid bodies, capacity rejection, and store-closing failures.
  - Prove GET returns bounded state plus `revision` and never `applies_from`; successful PUT/DELETE additionally return `applies_from: next_request` without changing an in-flight snapshot; no request-side expected revision is accepted.
  - Prove the shared endpoint contract: 405 `method_not_allowed` plus `Allow`; 415 `unsupported_media_type`; 413 `body_too_large`; 400 `invalid_request`; 401 `unauthorized` for unauthenticated client and distinguished-upstream unauthenticated operator; 403 `secure_session_required` for missing client authority or `forbidden` for diagnostics shared-secret mismatch and authenticated operator lacking target authorization; 404 `feature_not_found` or `session_not_found` for an authorized operator's invalid target; 409 `policy_capacity`; and 503 `policy_unavailable`, with no mutation on any error.
  - Prove bounded client response fields are `feature_id`, `available`, `client_state`, `effective_enabled`, and `revision`, with operator `operator_state`; DELETE is actor inherit. Revision is response/internal linearization evidence only, and serialized key-boundary writes prevent mixed or lost state.
  - Prove route names/diagnostics contain no concrete provider or ALG name and no credential/body leakage occurs.
  - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 9.3_
  - _Boundary: standard HTTP driving adapters_
  - _Validation: focused `httptest` endpoint suite_

- [x] 7.2 Implement authenticated provider-neutral endpoint adapters
  - Mount exactly `GET|PUT|DELETE /v1/lip/session/features/{feature_id}` and `GET|PUT|DELETE /admin/session-features/{session_id}/{feature_id}` through existing secure-session and operator authorization middleware.
  - Decode only the exact bounded PUT body, emit the shared status/error codes and `Allow` behavior, and report provider-active status without naming a provider.
  - Keep surfaces mountable when no provider is active so process state survives disable/re-enable; fail closed when the store is unavailable.
  - _Requirements: 5.4, 5.5, 6.1, 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 9.3, 10.2_
  - _Boundary: stdhttp composition and policy endpoint adapters_
  - _Depends: 6.2, 7.1_
  - _Validation: focused HTTP/auth tests and route-overlap validation_

## Observability and Architecture Ratchets

- [x] 8. Add bounded diagnostics and anti-regression architecture tests
- [x] 8.1 (P) Add RED observability, privacy, and ownership ratchets
  - Assert bounded candidate/provider/action/outcome/reason telemetry, A/B trace and billing lineage, no conversational payloads in labels/log defaults, and configured deadline/size/cardinality bounds.
  - Assert one physical owner for provider, policy store, transaction, overlay, and terminal claim, with no duplicate close paths.
  - Assert exact schedules for provider failure, cancellation, withdrawal, continuation-open failure, policy race, and stale cleanup.
  - Assert continuation ordering: B2 prepare/open/publication precedes B1 settlement; pre-publication failure leaves B1 unsettled and normalizes the original request, while post-publication settlement loss/error retains B2 with no fabricated rollback.
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 9.1, 9.2, 9.3, 9.4, 10.3, 10.4_
  - _Boundary: architecture and observability tests_
  - _Validation: focused archtest/telemetry/schedule tests_

- [x] 8.2 Implement bounded telemetry and architecture ratchets
  - Emit only bounded cause/provider/action/outcome/reason/revision dimensions and preserve existing trace/lineage/billing relationships.
  - Ratchet singular FeatureBundle provider contribution, one terminal claim chokepoint, immutable generations, process-owned policy, no request-hot-path lookup, and no direct hidden-content append.
  - Ratchet zero concrete ALG imports/provider-name switches in core and zero Go native plugin, DI, service locator, reflection registry, or generic effect runtime additions.
  - _Requirements: 9.1, 9.2, 9.3, 9.4, 10.1, 10.2, 10.3, 10.4, 11.2_
  - _Boundary: internal/archtest and existing observability paths_
  - _Depends: 3.2, 4.2, 6.2, 7.2, 8.1_
  - _Validation: `go test ./internal/archtest/...` and focused observability tests_

## Integration, Removal, and GO Gate

- [x] 9. Prove concrete-feature removal and dependent-spec handoff
- [x] 9.1 Add a provider-neutral fake feature integration and removal test
  - Register a fake provider through FeatureBundle, exercise allow-stop and continue intent through the core chokepoint, then remove the fake registration and prove no-provider behavior compiles and remains unchanged.
  - Prove the platform contains no ALG classifier, verifier, instruction, or provider-specific endpoint dependency.
  - Record the platform task IDs required before `agent-loop-breach-prevention` provider integration begins.
  - _Requirements: 1.3, 2.1, 3.1, 3.2, 4.5, 10.1, 12.1, 12.2, 12.3, 12.4_
  - _Boundary: platform integration tests and spec handoff_
  - _Depends: 8.2, 5.2, 7.2_
  - _Validation: focused fake-provider runtime tests and no-provider build test_

- [x] 10. Run simplification, ROI, and release-quality gates
- [x] 10.1 Perform the final platform simplification review
  - Compare baseline and target counts for provider fields, terminal claim sites, concrete ALG core references, policy owners, continuation cleanup paths, request-hot-path lookups, and lifecycle concepts.
  - Remove any helper, registry, owner, or abstraction that is not required by a measured invariant; if material simplification is not demonstrated, narrow or reject the seam.
  - The review records a GO decision only when one provider, one chokepoint, one process policy owner, zero provider-specific core policy, and no speculative runtime are proven.
  - _Requirements: 10.3, 11.1, 11.2, 11.3, 11.4, 12.4_
  - _Boundary: architecture/ROI review and platform scope_
  - _Depends: 1.1, 5.2, 6.2, 8.2, 9.1_
  - _Validation: deterministic baseline/target report and self-review gate_

- [x] 10.2 Run focused and repository quality verification
  - Run focused contract, feature merge, terminal, continuation, policy, endpoint, lifecycle, architecture, and integration tests; run race coverage where supported and retain Linux CI requirements when Windows TSAN is unavailable.
  - Run `go test ./...`, `make quality-checks`, `make test`, and `make qa` when the repository environment permits; report skipped external/integration gates plainly.
  - Confirm `git diff --check`, JSON/spec dependency checks, no-provider compatibility, and dependent ALG handoff readiness.
  - _Requirements: 1.1, 1.2, 1.3, 2.1, 2.2, 2.4, 3.2, 3.4, 4.1, 4.4, 5.2, 6.1, 6.2, 7.3, 8.1, 8.2, 8.4, 9.1, 9.3, 10.1, 10.2, 10.4, 11.4, 12.1, 12.2, 12.3_
  - _Boundary: repository verification_
  - _Depends: 10.1_
  - _Validation: all applicable commands listed above_

## Dependency and Parallel Waves

```text
1.1, 1.2 -> 2.1 -> 2.2 -> 3.1 -> 3.2
                                  ├-> 4.1 -> 4.2
                                  ├-> 5.1 -> 5.2
                                  ├-> 6.1 -> 6.2
                                  └-> 7.1 -> 7.2
4.2, 5.2, 6.2, 7.2 -> 8.1 -> 8.2 -> 9.1 -> 10.1 -> 10.2
```

Wave 1 contains only characterization and schedule fixtures. After the provider contract and merge are stable, terminal, continuation, lifecycle, policy, and endpoint tests may proceed in parallel because their boundaries do not overlap; integration and ratchets remain sequential. No task authorizes commits, branch operations, Kiro status changes, or PR work.

## Requirement Coverage Matrix

| Requirement | Tasks |
|---|---|
| 1 | 1.1, 2.1, 2.2, 3.1, 3.2, 9.1, 10.2 |
| 2 | 3.1, 3.2, 4.1, 4.2, 8.1, 9.1, 10.2 |
| 3 | 2.1, 4.1, 4.2, 8.1, 9.1, 10.2 |
| 4 | 2.2, 5.1, 5.2, 8.1, 9.1, 10.2 |
| 5 | 6.1, 6.2, 7.2, 10.2 |
| 6 | 1.1, 6.1, 6.2, 7.1, 7.2, 10.2 |
| 7 | 7.1, 7.2, 6.2, 10.2 |
| 8 | 1.2, 3.1, 4.1, 5.1, 6.1, 8.1, 10.2 |
| 9 | 1.2, 3.1, 4.1, 6.1, 7.1, 8.1, 8.2, 10.2 |
| 10 | 1.1, 2.1, 2.2, 3.2, 4.2, 5.2, 8.2, 9.1, 10.1, 10.2 |
| 11 | 1.1, 8.2, 9.1, 10.1, 10.2 |
| 12 | 2.2, 5.2, 9.1, 10.1, 10.2 |

## Completion Status

- [x] All 29 task entries are complete at branch baseline `eb1d85908d249cbf7300814b82be578b14572a08`.
- [x] Focused SDK, policy, runtime, HTTP, lifecycle, plugin-integration, and architecture tests pass; `make quality-checks` passes on the exact rewritten tree.
- [x] Final ROI evidence records one exclusive provider contribution, one core terminal chokepoint, one process policy owner, zero concrete ALG policy branches in core, and no generic runtime owner.
- [x] No-provider compatibility and concrete-feature removal are covered; the dependent ALG implementation is complete in the same changeset.
- [x] Windows race execution is unavailable in this worktree; Linux CI retains race ownership.
- [x] This archive is intentionally included in the same implementation changeset and upcoming PR under user override, so merged-main SHA verification is pending PR delivery.
