# Implementation Plan

## Execution Rules

- Follow TDD: characterization/RED tests and contract gates precede production reuse.
- Keep every task independently reviewable with no more than five concrete actions.
- Preserve public APIs, backend-plugin ABI, configuration schema, immutable generations, `ResourceLedger`, `ProcessServices`, and `processhost.Host` supervision.
- Prefer replacing the touched per-generation physical-cleanup path with lease cleanup rather than layering a competing ownership path.
- Do not broaden scope to builtins, `shared_artifact`, generic resource management, dynamic discovery, Session concurrency redesign, or request-time lookup.

## Phase 1 — Freeze Scale, Identity, Lifetime, and Concurrency Contracts

- [x] 1.1 Build the high-cardinality reload characterization harness

- Add a deterministic runtimebundle fixture with at least 100 enabled synthetic discovered `per_instance` connector instances and no external credentials.
- Count physical build/factory invocation, processhost Activate/launch, Configure, physical cleanup, and later lease acquire/release operations.
- Characterize the current unrelated-material-reload baseline while the old generation remains retained.
- Add RED target assertions for unchanged reload (`0` new physical builds/activations/Configure) and config-changed reload (`K` changed identities -> `K` physical replacements).
- Add a supporting candidate-compilation benchmark without wall-clock correctness thresholds.

_Requirements: 1.1–1.7, 2.2, 9.1, 9.4_

_Boundary: `internal/infra/runtimebundle` characterization tests and benchmarks only; no production reuse_

_Depends: None_

_Validation: `go test -count=1 -run 'Test.*BackendResource.*(HighCardinality|Reload)' ./internal/infra/runtimebundle`; deterministic current O(N) reconstruction is recorded and target count assertions are RED._

- [x] 1.2 Lock complete physical identity and drift behavior with RED tests

- Add table-driven identity tests for logical instance/factory, artifact digest, process model, opaque Configure bytes, normalized RuntimePolicy, and secret fingerprint.
- Prove config/artifact/secret/policy differences cannot alias; prove `shared_artifact` is non-pooled fallback rather than a pooled replacement case.
- Prove secret plaintext never appears in identity/debug/error/status output and `BackendStateIdentity` alone cannot authorize physical reuse.
- Add a Configure/physical-input drift gate forcing intentional identity treatment when DTO/input shape changes.
- Keep reload-varying and startup-fixed evidence distinct so tests do not imply unsupported hot artifact/policy/process-model reload.

_Requirements: 3.1–3.12, 8.1, 8.10, 9.4–9.5_

_Boundary: `internal/infra/runtimebundle` physical-identity contract tests only_

_Depends: None; may proceed in parallel with Task 1.1_

_Validation: `go test -count=1 -run 'TestBackendResourceIdentity' ./internal/infra/runtimebundle`; focused identity/construction tests are RED and fail closed on omitted input treatment._

- [x] 1.3 Freeze reserved-claim and entry-ownership state machine with RED tests

- Add first/live/concurrent Acquire tests where every building-entry waiter reserves its prospective claim before waiting; include a deterministic first-release-before-waiter-wake schedule.
- Add cancellation/failure tests proving waiter cancellation drops only its claim, failed builds are not negative-cached, and a later Acquire can retry.
- Add invalidation tests proving exact-incarnation detach, detached entry retention in process ownership, fresh same-key replacement, and stale invalidation safety.
- Add final Release versus new Acquire and final Release versus Pool.Close races, requiring one entry-level physical cleanup outcome.
- Add an invalidation + outstanding old-generation lease + Pool.Close test proving detached residual entries are enumerated and fail-safe cleaned.

_Requirements: 4.1–4.4, 4.8–4.11, 6.1–6.8, 7.1–7.3, 7.7, 9.1–9.2, 9.7_

_Boundary: `internal/infra/runtimebundle` private pool state-machine tests only_

_Depends: Task 1.2 for the frozen semantic identity test shape_

_Validation: `go test -count=1 -run 'TestBackendResourcePool.*(Acquire|Release|Invalidate|Cleanup)' ./internal/infra/runtimebundle`; tests are RED against the absent pool and pin zero-ref handoff/detached/double-cleanup races._

- [x] 1.4 Freeze Acquire/Close linearization and builder lifetime with RED tests

- Add a terminal Close linearization test proving no new claim can be reserved and no pending build result can be handed off after `closing=true` linearizes.
- Add a physical builder that blocks until its **pool-owned** context is canceled; Pool.Close must cancel it, join it, and prevent late publication before returning.
- Add concurrent Close/Acquire/build-completion schedules proving residual cleanup starts only after builders and acquisition handoffs terminate.
- Prove a successful physical result arriving after Close begins is cleaned exactly once rather than published as reusable.
- Add goleak/race coverage for canceled waiters, pending builders, Close, and late completion.

_Requirements: 4.5–4.7, 7.5, 7.7, 7.9, 9.1–9.3, 9.6_

_Boundary: `internal/infra/runtimebundle` private pool shutdown/cancellation tests only_

_Depends: Task 1.3 for the reserved-claim and entry state-machine contract_

_Validation: `go test -count=1 -run 'TestBackendResourcePool.*(Close|Cancel|Late|Builder)' ./internal/infra/runtimebundle`; shutdown-race tests are RED and define the pool's terminal linearization contract._

- [x] 1.5 Freeze cleanup handoff, candidate isolation, and standard Session concurrency

- Add an ownership test counting adapter/session cleanup, `ActivateResult.Cleanup`/host instance cleanup, pool cleanup, and later `Host.Close`, proving one physical resource is not torn down twice.
- Add candidate rollback with active+candidate sharing: rollback releases only the candidate claim and leaves active execution/query behavior available.
- Add a characterization gate proving pooled external backend values expose no generation-owned physical Close/Start/Stop bypass and reuse-hit preparation is query-only.
- Add standard-host overlapping-generation race/conformance for Resolve/ListModels/CountTokens/FinalizeBilling alongside execution.
- Add a retained-old-generation Execute + new-generation Execute test that explicitly observes existing `Session.Execute` serialization without changing it, plus process teardown order pool -> host -> artifacts -> staging.

_Requirements: 5.4–5.11, 7.1–7.10, 8.2–8.5, 8.11, 9.6–9.7, 9.10_

_Boundary: `internal/infra/runtimebundle` integration contracts plus `pkg/lipsdk/backendplugin/host` standard Session characterization tests_

_Depends: Tasks 1.1, 1.3, and 1.4_

_Validation: `go test -count=1 ./internal/infra/runtimebundle ./pkg/lipsdk/backendplugin/host`; last-good isolation, cleanup ownership, established Session concurrency, and teardown-order assertions are RED._

## Phase 2 — Implement the Minimal Private Identity and Reconciliation Owner

- [x] 2.1 Implement explicit fail-closed physical identity

- Add one package-private identity builder at the discovered physical construction/configure choke point using domain-separated, length-framed SHA-256 inputs.
- Explicitly project every RuntimePolicy field and deterministic opaque Configure bytes; hash sorted length-framed secret names/values without retaining plaintext.
- Include logical instance/factory, exact artifact digest and process model; document startup-fixed inputs while still treating them in focused identity tests.
- Return an explicit shareability decision so incomplete/unsupported input uses current isolated construction.
- Make Task 1.2 identity/privacy/drift tests green without production reflection or a public identity type.

_Requirements: 3.1–3.12, 8.1, 8.10_

_Boundary: `internal/infra/runtimebundle` private physical-input and identity implementation_

_Depends: Task 1.2 RED identity/privacy/drift contracts_

_Validation: `go test -count=1 -run 'TestBackendResourceIdentity' ./internal/infra/runtimebundle`; focused identity tests green and no secret/raw identity leakage exists._

- [x] 2.2 Implement reserved-claim entries and exactly-once physical cleanup

- Add a package-private entry model with building/live/detached/failed state, exact incarnation token, pre-reserved claims, readiness signaling, and current semantic indexing.
- Add a process-owned set of every successful physical entry until its entry-level cleanup-once completes, including invalidated/detached entries.
- Make every generation lease release idempotently drop one claim; only final normal release detaches current and invokes the entry-level cleanup-once operation.
- Store physical cleanup only on the entry and use the same cleanup-once path for final release and process fail-safe shutdown.
- Make Task 1.3 tests green without performing physical cleanup while holding the pool mutex.

_Requirements: 4.1–4.4, 4.8–4.11, 6.1–6.8, 7.1–7.3, 7.7_

_Boundary: `internal/infra/runtimebundle` private connector pool entry, lease, invalidation, and cleanup ownership_

_Depends: Tasks 1.3 and 2.1_

_Validation: `go test -count=1 -run 'TestBackendResourcePool.*(Acquire|Release|Invalidate|Cleanup)' ./internal/infra/runtimebundle`; targeted `-race` where supported._

- [x] 2.3 Implement pool-owned physical builders and terminal Close

- Create one pool-owned cancellable build root; absent identity starts exactly one joined builder goroutine and all callers wait as claimants rather than owning the build.
- Pass pool builder context through pooled `processhost.Activate`/Configure instead of a background lifetime; caller cancellation abandons only that caller's reservation.
- Implement Close linearization under the pool mutex, reject later Acquires, cancel builders, then wait builders and acquisition handoffs before residual cleanup.
- Prevent a build completion after Close from publishing/handoff; clean any completed physical result through the entry cleanup-once path.
- Make Task 1.4 blocked-builder/linearization/goleak/race tests green.

_Requirements: 4.5–4.8, 7.5, 7.7, 7.9_

_Boundary: `internal/infra/runtimebundle` private pool builder lifetime and terminal shutdown_

_Depends: Tasks 1.4 and 2.2_

_Validation: `go test -count=1 -run 'TestBackendResourcePool.*(Close|Cancel|Late|Builder)' ./internal/infra/runtimebundle`; no builder can outlive Pool.Close or publish after its terminal boundary._

- [x] 2.4 Transfer pool ownership through existing process construction

- Create the pool beside `processhost.Host` during discovered-install preparation and capture it lexically in eligible discovered factory closures.
- Extend the private install/process-build ownership bundle so pool lifetime transfers into `ProcessServices` without global/setter lookup.
- Register cleanup in existing process ownership so reverse shutdown is pool -> host -> verified artifacts -> staging while preserving unrelated ProcessServices ordering.
- Mirror the same relative cleanup order on every pre-transfer/bootstrap failure and prevent double cleanup after ownership transfer.
- Keep pool API connector-specific/package-private and make Task 1.5 teardown/ownership tests green.

_Requirements: 2.1, 2.6–2.9, 7.3–7.10_

_Boundary: `internal/infra/runtimebundle` discovered-install and `ProcessServices` composition/ownership wiring_

_Depends: Tasks 1.5 and 2.3_

_Validation: `go test -count=1 -run 'Test.*(Discovered|ProcessServices|Ownership|Teardown)' ./internal/infra/runtimebundle`; success and partial-startup ownership transfer is exactly once and dependency ordered._

## Phase 3 — Integrate Reuse at the Discovered `per_instance` Factory Seam

- [x] 3.1 Split preparation, physical construction, and lease acquisition

- Refactor discovered backend construction into effective input/identity preparation, the current physical Activate/Configure/adapter build, and pool Acquire without provider-specific branches.
- Preserve unique host activation IDs for every **new** per-instance physical incarnation and leave processhost ownership keys unchanged.
- Route only eligible overlap-safe discovered `per_instance` resources through the pool; builtins, `shared_artifact`, and non-shareable resources keep current construction.
- On a pool miss, consume the current composite adapter/session + activation cleanup into the entry; on a hit, return existing backend functions plus a fresh lease release.
- Keep `buildBackends`/`ResourceLedger` as the generation cleanup transfer authority and make unchanged/config-mixed count tests green.

_Requirements: 1.4–1.5, 2.2–2.7, 4.2, 4.9–4.11, 7.1–7.3_

_Boundary: `internal/infra/runtimebundle` discovered `per_instance` factory composition seam only_

_Depends: Tasks 1.1 and 2.1–2.4_

_Validation: `go test -count=1 -run 'Test.*BackendResource.*(HighCardinality|Reload|Reuse)' ./internal/infra/runtimebundle`; unchanged reload performs zero physical reconstruction and K changed configs produce K physical builds._

- [x] 3.2 Bind invalidation to exact pooled incarnation

- Wrap newly built pooled adapter invalidation so it detaches only the exact pool entry/incarnation before/atomically with delegating to `processhost.InvalidateProcessGeneration`.
- Preserve processhost physical reap/recovery and cleanup normalization; invalidation does not decrement generation claims or live-substitute a replacement.
- Keep detached invalidated entry in the pool's process ownership set until entry cleanup completes.
- Prove later same-config Acquire builds one fresh incarnation and old delayed callbacks cannot detach it.
- Keep non-pooled and `shared_artifact` invalidation behavior unchanged.

_Requirements: 6.1–6.8, 7.3, 8.7, 8.9_

_Boundary: `internal/infra/runtimebundle` pooled adapter invalidation binding; existing `processhost` supervision remains unchanged_

_Depends: Tasks 1.3, 2.2, and 3.1_

_Validation: `go test -count=1 -run 'Test.*(Invalidat|Incarnation)' ./internal/infra/runtimebundle ./internal/infra/backendplugins/processhost`; invalidation/replacement tests and existing processhost suites remain green._

- [x] 3.3 Preserve generation-local state and query-only candidate behavior

- Continue building new generation-local inventories, model registry/catalog, executor/routing/policy/billing views, handler, lifecycle context, and `ResourceLedger` from leased backends.
- Ensure a reuse hit performs no Configure/Start/Stop/Close/mutating preflight and candidate rollback never invalidates because the candidate failed.
- Verify changed same-ID config, remove/disable, retained old stream/async work, and candidate failure retain current semantics.
- Verify standard-host metadata/auxiliary overlap across generations under race/conformance while preserving existing Session lifecycle locking.
- Keep canonical request/event, routing, failover, streaming, cancellation, accounting, billing and token-counting code paths unchanged.

_Requirements: 5.1–5.11, 8.4, 8.6–8.11_

_Boundary: `internal/infra/runtimebundle` candidate/reload integration tests and existing generation-local composition; no canonical/core semantics changes_

_Depends: Tasks 1.5, 3.1, and 3.2_

_Validation: `go test -count=1 ./internal/infra/runtimebundle ./pkg/lipsdk/backendplugin/host`; backend recomposition/no-drop plus query-only/cross-generation tests green._

- [x] 3.4 Add architecture fences against scope creep and cleanup bypass

- Prove the pool remains private to runtime composition and is absent from request execution, public SDKs, provider-specific packages, and connector authoring APIs.
- Reject generic service/container/keyed runtime registry APIs introduced for this feature.
- Lock `processhost.Host` as the only process/IPC supervisor; the pool may call existing cleanup/invalidation seams but not duplicate supervision logic.
- Lock generation cleanup to lease release for pooled resources and reject alternate physical lifecycle hooks bypassing entry ownership.
- Assert no public YAML/manifest/ABI/concurrency option was added for reconciliation.

_Requirements: 2.1, 2.5–2.9, 4.10–4.11, 7.3, 8.2, 8.8–8.10, 9.8_

_Boundary: `internal/archtest` static architecture contracts only_

_Depends: Tasks 3.1–3.3 so fences inspect the final production shape_

_Validation: `go test -count=1 ./internal/archtest/...`; representative forbidden architecture fixtures fail and intended private design passes._

## Phase 4 — Certify ROI, Identity, Concurrency, and Simplicity

- [x] 4.1 Certify the high-cardinality generation-reload matrix

- Run the 100-enabled-connector fixture for unchanged, one/K config changes, remove/disable, candidate rollback, and invalidation-then-rebuild.
- Assert physical build/Activate/Configure counts are `0` for unchanged reuse and proportional only to changed/unusable identities.
- Assert physical live-resource overlap avoids duplicating unchanged connectors and candidate rollback of reuse hits performs no physical cleanup of active resources.
- Assert candidate-only new resources clean on rollback/final release and invalidated entries can be fail-safe cleaned at process shutdown.
- Record deterministic before/after counts as implementation/PR evidence.

_Requirements: 1.1–1.7, 4.9, 5.2–5.5, 6.4–6.6, 7.5_

_Boundary: `internal/infra/runtimebundle` deterministic integration evidence only_

_Depends: Tasks 3.1–3.3_

_Validation: `go test -count=1 -run 'Test.*BackendResource.*(HighCardinality|Reload|Matrix)' ./internal/infra/runtimebundle`; structural O(N) physical build -> O(K) physical build claim is green without timing thresholds._

- [x] 4.2 Certify the focused physical identity/construction matrix

- Exercise distinct artifact digests, secret fingerprints, normalized RuntimePolicy values, factory/logical IDs, and process models directly at the physical identity/construction seam.
- Prove each eligible identity difference misses the existing pool entry and creates a fresh resource when construction is otherwise shareable.
- Prove `shared_artifact`/other non-eligible process model uses existing non-pooled/restart-required behavior rather than a pooled replacement.
- Verify no startup-fixed field is falsely documented/tested as current SIGHUP hot-reload support.
- Re-run the DTO/input drift gate against the final production identity projection.

_Requirements: 3.1–3.12, 9.4–9.5_

_Boundary: `internal/infra/runtimebundle` focused identity/construction evidence only_

_Depends: Tasks 2.1 and 3.1_

_Validation: `go test -count=1 -run 'TestBackendResourceIdentity' ./internal/infra/runtimebundle`; all physical identity dimensions are covered without inventing unsupported reload behavior._

- [x] 4.3 Run race, leak, security, conformance, and reload regression gates

- Run targeted `-race`/goleak for reserved claims, Acquire/Close/build cancellation, invalidation, entry cleanup, and overlapping standard-host operations.
- Run processhost activation/cleanup/invalidation and executable backend-plugin security/conformance suites.
- Run ResourceLedger, backend recomposition, discovered overlap/restart-required, candidate rollback, retained-generation, and reload last-good/no-drop suites.
- Run repository formatting/vet/lint/architecture gates without weakening assertions/skips.
- Verify secret/config identity data and opaque YAML do not leak to logs, metrics, errors, statuses or public DTOs.

_Requirements: 7.7–7.10, 8.4–8.10, 9.1–9.9_

_Boundary: repository verification across runtimebundle, processhost, backend-plugin host/security/conformance, reload, and architecture tests_

_Depends: Tasks 3.1–3.4 and 4.1–4.2_

_Validation: focused uncached tests, targeted `go test -race` where supported, `go mod verify`, `make quality-checks`, `make test-unit`, connector/reload/conformance/security suites; all applicable gates green._

- [x] 4.4 Record performance and Session overlap evidence

- Run comparable high-cardinality candidate-build benchmarks and report timing/allocations separately from deterministic work counts.
- Deterministically hold a retained old-generation Execute and start a new-generation Execute on the same pooled standard Session; record the existing serialization behavior and cancellation/close outcome.
- Confirm normal request execution adds no pool lookup/lock and no material regression attributable to reconciliation.
- Document that the claimed gain is reload physical-resource churn/peak overlap reduction, not inference throughput or token latency.
- Re-scope pooled configured-session reuse if the established cross-generation Execute serialization is operationally unacceptable for intended long-lived-stream workloads; do not redesign Session concurrency in this spec.

_Requirements: 1.6–1.7, 8.2–8.3, 8.8, 8.11, 9.10_

_Boundary: `internal/infra/runtimebundle` benchmarks and `pkg/lipsdk/backendplugin/host` Session overlap characterization only_

_Depends: Tasks 3.3 and 4.1_

_Validation: targeted `go test -bench 'BackendResource' -benchmem ./internal/infra/runtimebundle` plus uncached Session overlap tests; ROI evidence includes lifecycle savings and the real overlap-scheduling tradeoff._

- [x] 4.5 Perform final simplification and authority audit

- Remove duplicate ownership stacks, generic wrappers, public knobs, request-path coupling, or unused lifecycle abstractions from the implementation diff.
- Confirm `ProcessServices`, `ResourceLedger`, `processhost.Host`, runtimehost generation refs, and unique activation IDs remain the same authorities.
- Confirm builtins, `shared_artifact`, dynamic discovery, shared model registry, and host Session concurrency redesign remain outside scope.
- Confirm every physical resource has one entry-level cleanup path and every generation owns only one lease release.
- If count/behavior gates do not justify the added lifecycle machinery, revert/re-scope rather than ship speculative architecture.

_Requirements: 1.7, 2.1–2.9, 7.1–7.10, 9.8–9.10_

_Boundary: holistic reviewed feature diff and Kiro implementation evidence; no new feature scope_

_Depends: Tasks 4.1–4.4_

_Validation: complete diff/authority audit, `git diff --check`, changed-file limit check, and all final repository gates; final diff remains narrowly connector-lifecycle focused and evidence-backed._

## Requirement Coverage Matrix

| Requirement | Primary tasks |
|---|---|
| R1 Evidence-first scale | 1.1, 3.1, 4.1, 4.4–4.5 |
| R2 Narrow boundary | 1.5, 2.4, 3.1, 3.4, 4.5 |
| R3 Physical identity | 1.2, 2.1, 4.2 |
| R4 Acquire/Close/claims | 1.3–1.4, 2.2–2.3, 3.1 |
| R5 Immutable generation/candidate | 1.5, 3.1, 3.3, 4.1 |
| R6 Invalidation/detached ownership | 1.3, 2.2, 3.2, 4.1 |
| R7 Cleanup/shutdown | 1.3–1.5, 2.2–2.4, 3.1, 4.3, 4.5 |
| R8 Concurrency/non-interference | 1.5, 3.3–3.4, 4.3–4.4 |
| R9 TDD/architecture | Phase 1, 3.4, Phase 4 |

## Completion Gate

Do not consider this specification implemented unless deterministic reconstruction counts meet the target **and** the hardened ownership/concurrency gates prove: reserved waiter claims, detached-entry shutdown ownership, terminal Acquire/Close linearization, pool-owned builder cancellation, entry-level exactly-once physical cleanup, candidate last-good isolation, and explicit preservation/measurement of standard Session operation concurrency.
