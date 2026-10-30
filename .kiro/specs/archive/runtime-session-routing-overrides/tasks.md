# Implementation Plan

## 1. Freeze Brownfield Behavior and Add RED Contracts

- [x] 1.1 Characterize current per-turn route-plan lifetime and no-override behavior
  - Add focused tests proving the current client selector becomes one request-local route plan reused by failover/retry/interleaved B-legs.
  - Characterize client traffic/session recording versus the effective baseline so later admin substitution cannot silently change what is attributed to the client.
  - Add deterministic test barriers around the point after authoritative A-leg resolution and before route-plan construction; do not use sleeps for concurrency ordering.
  - Observable completion: all characterization tests pass on current behavior, while explicit override expectations remain RED/unimplemented.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.6, 3.7, 5.5, 10.1, 10.2, 10.7_
  - _Design rules: D3, D4, D5_
  - _Boundary: internal/core/runtime tests only_
  - _Depends: none_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/routing/..._

- [x] 1.2 Define RED route-override state/store semantics and shared contract suite
  - Define focused internal state/reader/store contracts without changing base `b2bua.Store` or public `pkg/lipsdk/continuity.Store`.
  - Specify first set, replace, identical PUT/no-op, clear, repeated clear/no-op, revision 0 inactive state, not-found, revision overflow, A-leg deletion, `LastSeenAt` refresh and value-copy semantics.
  - Build one store contract suite that can run against memory, SQLite and PostgreSQL adapters.
  - Add concurrent writer/snapshot scenarios proving complete-state reads and deterministic committed revision order, plus deterministic delete/recreate-versus-Replace/Clear barriers proving no orphan state or inheritance by a new A-leg.
  - Observable completion: contract types/tests compile and fail because standard continuity stores do not yet implement the capability.
  - _Requirements: 1.1, 2.1, 2.2, 2.3, 2.4, 2.6, 2.7, 2.8, 3.8, 7.1, 7.2, 7.3, 7.6, 7.7, 10.1_
  - _Design rules: D1, D2, D8, D10_
  - _Boundary: internal/core routeoverride contracts + tests_
  - _Depends: 1.1_
  - _Validation: go test ./internal/core/routeoverride/... ./internal/core/b2bua/... ./internal/core/continuity/bunstore/..._

- [x] 1.3 Define RED generation selector-preflight and side-effect contracts
  - Specify a narrow selector validator that reuses alias/parse/default-backend behavior from normal route planning.
  - Test direct/model-only/alias/failover/weighted/race/TTFT/affinity/`[first]`/`[thinker]` accepted forms and malformed/unresolved forms.
  - Add probes proving validation allocates no B-leg, opens no backend/connector, consumes no model usage, and mutates no weighted-first/interleaved/affinity state.
  - Observable completion: tests require a shared pure compile/preflight helper and fail until route planning exposes it without semantic duplication.
  - _Requirements: 4.1, 4.2, 4.3, 4.6, 4.7, 4.8, 5.6, 8.7, 10.1_
  - _Design rules: D6, D9_
  - _Boundary: internal/core/routing + runtime tests_
  - _Depends: 1.1_
  - _Validation: go test ./internal/core/routing/... ./internal/core/runtime/..._

- [x] 1.4 Add RED admin/security and architecture boundary contracts
  - Define GET/PUT/DELETE handler behavior, `application/json` PUT media-type validation, strict JSON decoding, body/selector bounds, inactive DTO omission of `selector`, stable 400/413/415/404/503 mappings, idempotent methods, typed not-found/invalid/store errors and disabled-by-default mounting.
  - Add protection tests for operator secret and non-loopback protected-surface validation.
  - Add architecture tests proving no override field enters `pkg/lipapi`, no frontend/backend/connector imports routeoverride, and base public continuity Store remains unchanged.
  - Observable completion: handler/mount/boundary tests compile or intentionally fail at missing service/wiring points before production code exists.
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7, 8.8, 8.9, 9.6, 10.3, 10.4, 10.5, 10.6_
  - _Design rules: D8, D9, D11_
  - _Boundary: admin contract/config/arch tests_
  - _Depends: 1.2, 1.3_
  - _Validation: go test ./internal/stdhttp/admin/... ./internal/core/config/... ./internal/archtest/..._

## 2. Implement Revisioned A-Leg Persistence

- [x] 2.1 Implement the memory-backed route-override capability
  - Add A-leg-owned active/inactive override state to the existing memory continuity lifecycle under its current synchronization discipline.
  - Implement atomic Snapshot/Replace/Clear with normalized selectors, monotonic revision, state-change idempotency, context cancellation and defensive value copies.
  - Refresh A-leg `LastSeenAt` on every successful Snapshot/Get/Replace/Clear, including idempotent no-ops, without changing override revision/update time unless effective override state changes.
  - Ensure TTL/max-leg eviction, continuity-key replacement and explicit A-leg removal cannot leave override state behind.
  - Make the RED memory store contract suite green without changing the base public continuity interface.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.6, 2.7, 3.8, 7.1, 7.3, 7.7_
  - _Design rules: D2, D8, D10_
  - _Boundary: internal/core/b2bua memory adapter_
  - _Depends: 1.2_
  - _Validation: go test ./internal/core/routeoverride/... ./internal/core/b2bua/..._

- [x] 2.2 Implement durable SQLite/PostgreSQL override state and migration
  - Add one-to-one A-leg-owned persistence for active flag, raw selector, revision and update time with referential cleanup/cascade semantics.
  - Implement same-A-leg state transitions transactionally for both supported dialects; refresh A-leg `LastSeenAt` consistently with existing durable Fetch/mutation semantics while refusing revision overflow and invalid stored bounds.
  - Preserve legacy A-leg rows as inactive revision 0 without backfilling one row per session.
  - Prove reopen/restart persistence and A-leg deletion cleanup through the shared store contract suite.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.6, 2.7, 7.2, 7.3, 7.6_
  - _Design rules: D2, D8, D10_
  - _Boundary: internal/core/continuity/bunstore schema + adapter_
  - _Depends: 2.1_
  - _Validation: go test ./internal/core/continuity/bunstore/..._

- [x] 2.3 Harden cross-implementation persistence concurrency and failure behavior
  - Run the identical state transition suite against memory and SQLite, plus PostgreSQL when the integration DSN is available.
  - Add two-writer races with barriers so the observed higher revision always corresponds to the later committed effective state.
  - Add deterministic barriers for A-leg deletion or continuity-key recreation racing with both Replace and Clear; prove mutation-before-delete is cleaned up, delete/recreate-before-mutation returns not-found, no orphan override row commits, and the new A-leg never inherits old state.
  - Prove failed/invalid mutations leave previous state byte-for-byte/effectively unchanged.
  - Prove snapshot/read failures are surfaced rather than converted to inactive state.
  - _Requirements: 2.2, 2.3, 3.5, 3.8, 7.2, 7.6, 8.7_
  - _Design rules: D2, D3, D10_
  - _Boundary: store contract/integration tests_
  - _Depends: 2.1, 2.2_
  - _Validation: go test ./internal/core/routeoverride/... ./internal/core/b2bua/... ./internal/core/continuity/bunstore/..._

## 3. Integrate Immutable Override Snapshots Into Runtime Routing

- [x] 3.1 Implement the shared pure selector compile/preflight path
  - Extract the minimum pure alias/parse/model-only-default/unresolved-selector logic so admin validation and `buildRoutePlan` call the same behavior.
  - Preserve native-model binding, affinity, request-size, dynamic state and request-dependent capability/catalog work in their existing real-turn positions.
  - Make RED equivalence and no-side-effect tests from 1.3 green.
  - _Requirements: 4.1, 4.2, 4.3, 4.6, 4.8, 5.6_
  - _Design rules: D6, D9_
  - _Boundary: internal/core/routing + runtime integration seam_
  - _Depends: 1.3_
  - _Validation: go test ./internal/core/routing/... ./internal/core/runtime/..._

- [x] 3.2 Wire process-owned override persistence and generation-bound validation
  - Detect/construct the focused override-store capability from process-owned continuity without changing the base continuity Store; standard memory/Bun continuity must expose and wire the runtime reader regardless of HTTP admin enablement.
  - Bind a current-generation selector validator and command service during generation compilation; include the admin handler/service in the complete generation-specific HTTP handler so the stable GenerationDispatcher naturally rebinds later admin requests after publish. Keep the persistence owner process-scoped across generation retirement.
  - Fail configuration/assembly coherently if override administration is enabled with a continuity implementation that cannot provide required override storage; disabling the HTTP surface must not remove a standard runtime reader or suspend persisted-state enforcement.
  - Do not introduce a second mutable global registry, atomic admin-service swap, service locator, or generation-local override copy.
  - _Requirements: 7.4, 7.5, 7.6, 7.7, 8.1, 10.6_
  - _Design rules: D8, D9, D10_
  - _Boundary: runtimebundle/process-services/generation composition_
  - _Depends: 2.1, 2.2, 3.1_
  - _Validation: go test ./internal/infra/runtimebundle/... ./internal/infra/runtimehost/..._

- [x] 3.3 Snapshot once per turn and build a separate effective routing baseline
  - Read override state immediately after authoritative A-leg fetch and copy the complete state into request-local preparation metadata.
  - Preserve the client/work call for existing CTP/client-turn evidence; after pre-request mutation, clone an effective routing call and replace only its selector when the snapshot is active.
  - Run route hinting against the effective routing call, then freeze the existing prepared baseline and route plan from it.
  - When a custom continuity implementation exposes no reader capability, or when the wired reader reports an inactive state, preserve current execution with no observable selector changes; standard memory/Bun readers remain wired independently of admin endpoint exposure.
  - Surface configured override-store read failure as request preparation failure rather than silently using client routing.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 3.1, 3.5, 5.1, 5.2, 5.3, 5.4, 5.5, 10.2_
  - _Design rules: D1, D3, D4, D5_
  - _Boundary: internal/core/runtime request preparation only_
  - _Depends: 3.2_
  - _Validation: go test ./internal/core/runtime/..._

- [x] 3.4 Prove in-flight B-leg non-interference across replace and clear
  - Hold Turn N after snapshot, commit a different override revision, then allow Turn N to open/failover/race and assert every B-leg uses the old baseline/revision.
  - Repeat with clear: current turn remains overridden while the next turn uses its current client selector.
  - Include the case where mutation commits after snapshot but before the first B-leg opens.
  - Prove post-output updates trigger no retry, cancel, stream close, route rebuild or connection disruption.
  - _Requirements: 2.5, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7_
  - _Design rules: D3, D4, D5_
  - _Boundary: internal/core/runtime concurrency/integration tests_
  - _Depends: 3.3_
  - _Validation: go test ./internal/core/runtime/..._

## 4. Implement Protected Admin Commands and Explainability

- [x] 4.1 Implement generation-bound Get/Replace/Clear command service
  - Normalize/validate A-leg and selector input, enforce selector bounds, invoke pure generation preflight, and only then mutate persistence.
  - Return resulting active/inactive revision state for every successful command including idempotent no-ops.
  - Map unknown A-leg, invalid selector, store failure and revision exhaustion to stable typed errors without changing prior state.
  - Ensure command execution creates no sessions/B-legs and touches no provider/dynamic-routing state.
  - _Requirements: 2.1, 2.4, 2.8, 4.6, 4.7, 8.3, 8.4, 8.7, 8.9_
  - _Design rules: D2, D9_
  - _Boundary: internal/core/routeoverride application service_
  - _Depends: 2.3, 3.1, 3.2_
  - _Validation: go test ./internal/core/routeoverride/..._

- [x] 4.2 Implement opt-in GET/PUT/DELETE admin HTTP surface and security posture
  - Add typed routing override admin config with disabled-by-default enablement, bounded path prefix and body size.
  - Mount a focused handler under `internal/stdhttp/admin` using the existing operator-secret wrapper; extend non-loopback protected-surface validation.
  - Require PUT media type `application/json` (allowing valid parameters), returning 415 for missing/malformed/unsupported media types before decode; strictly reject malformed/oversized/multi-value/unknown-field bodies and nonconforming method/body combinations.
  - Return protected state DTOs for GET/PUT/DELETE with `selector` omitted whenever inactive (and `updated_at` omitted only for revision 0), plus stable bounded 400/413/415/404/503 error mappings.
  - Add route-collision/mount tests, prove the handler is not reachable through client frontend protocol paths, and prove disabling the route registration does not disable runtime enforcement of an already-persisted override.
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7, 8.8, 8.9, 9.1, 9.6_
  - _Design rules: D11_
  - _Boundary: internal/core/config + internal/stdhttp/admin/stdhttp composition_
  - _Depends: 4.1_
  - _Validation: go test ./internal/stdhttp/admin/... ./internal/stdhttp/... ./internal/core/config/..._

- [x] 4.3 Add bounded selector-source/revision diagnostics and mutation audit metadata
  - Record whether each routed turn used client or admin selector source and the snapshotted revision without exposing raw route expressions in ordinary logs/metrics.
  - Keep `AttemptRecord.EffectiveModel` as the actual B-leg outcome authority and avoid duplicating backend/model lineage in override state.
  - Emit mutation action/outcome/revision plus selector digest/byte length and bounded/hashed A-leg identity according to existing logging conventions.
  - Add tests proving raw selector/A-leg values never become metrics labels and protected admin responses are the only default raw-selector output.
  - _Requirements: 9.1, 9.2, 9.3, 9.4, 9.5, 9.6_
  - _Design rules: D12_
  - _Boundary: route diagnostics/logging/admin DTOs_
  - _Depends: 3.3, 4.2_
  - _Validation: go test ./internal/core/diag/... ./internal/core/runtime/... ./internal/stdhttp/... ./internal/infra/metrics/..._

## 5. Validate Advanced Routing, Reload, and Session Continuity

- [x] 5.1 (P) Prove direct/alias/failover/weighted/race/TTFT/affinity equivalence
  - For each selector class, compare an admin-active turn with the same selector supplied directly by the client on an equivalent A-leg state.
  - Verify route aliases, model-only defaulting, candidate order/race structure, TTFT parameters and affinity semantics are owned by the existing planner.
  - Replace the override between turns and prove only the newer revision is used by later turns.
  - Clear it and prove the next turn uses that turn's client selector.
  - _Requirements: 2.1, 2.5, 4.1, 4.2, 5.6, 6.5, 6.6_
  - _Design rules: D3, D6, D7_
  - _Boundary: routing/runtime integration tests_
  - _Depends: 3.4, 4.1_
  - _Validation: go test ./internal/core/routing/... ./internal/core/runtime/..._

- [x] 5.2 (P) Prove `[first]` and `[thinker]` preserve existing A-leg state
  - Test override activation before/after `WeightedFirstConsumed` and prove set/replace/clear does not reset it.
  - Test thinker override with no memo, existing memo, visible/hidden paths and replacement/clear transitions without deleting interleaved state.
  - Assert all thinker/executor B-legs within a turn remain on the turn's snapshotted selector revision even when admin state changes mid-cycle.
  - Compare behavior to equivalent client selector changes rather than adding admin-only thinker semantics.
  - _Requirements: 4.2, 6.1, 6.2, 6.3, 6.4, 6.6_
  - _Design rules: D3, D7_
  - _Boundary: routing/interleaved/runtime tests_
  - _Depends: 3.4, 4.1_
  - _Validation: go test ./internal/core/routing/... ./internal/core/interleavedthinking/... ./internal/core/runtime/..._

- [x] 5.3 (P) Prove generation reload reinterprets raw overrides without mutating in-flight turns
  - Set an alias-based override, hold an old-generation turn, publish a generation with changed alias/default/backend configuration, and prove the old turn stays pinned.
  - Issue a PUT after the new generation is published and prove the request is handled by the newly published generation-specific admin handler/service and validated with the new selector validator/config; do not satisfy this with only a turn-after-reload test.
  - Prove a new turn reads the same persisted revision but resolves it with the new generation.
  - Disable only the admin HTTP surface in a later generation and prove the endpoint is absent while the persisted active override remains enforced by new turns.
  - Remove/break the alias in a candidate generation and prove the affected later turn fails normal route planning rather than falling back to client selector.
  - Verify override state itself is not copied into or lost with generation retirement.
  - _Requirements: 4.3, 4.4, 4.5, 7.4, 7.5_
  - _Design rules: D6, D10_
  - _Boundary: runtimebundle/configreload/runtime integration tests_
  - _Depends: 3.2, 3.3, 4.1_
  - _Validation: go test ./internal/infra/runtimebundle/... ./internal/core/configreload/... ./internal/core/runtime/..._

- [x] 5.4 (P) Prove cross-frontend resume and durable restart semantics
  - Resume the same authoritative A-leg through at least two bundled frontend/transport paths and assert the active override revision follows the A-leg rather than the connection/protocol.
  - Reopen supported durable continuity and prove the surviving A-leg retains active or cleared revision state; prove memory continuity makes no restart durability claim.
  - Exercise A-leg deletion/continuity-key replacement and prove old override state cannot attach to the new A-leg.
  - Where PostgreSQL integration is available, prove a second store/process view observes committed revisions without an indefinite process-local cache.
  - _Requirements: 1.1, 1.4, 1.5, 7.1, 7.2, 7.3, 7.6, 10.8_
  - _Design rules: D1, D8, D10_
  - _Boundary: secure-session/frontend/runtimebundle/store integration tests_
  - _Depends: 2.2, 3.3, 4.2_
  - _Validation: go test ./internal/plugins/frontends/... ./internal/core/securesession/... ./internal/infra/runtimebundle/... ./internal/core/continuity/bunstore/..._

## 6. Final Concurrency, Architecture, and Quality Gates

- [x] 6.1 Run targeted race/stress tests and fix any snapshot/mutation synchronization defects
  - Exercise concurrent Snapshot/Get/Replace/Clear against the memory store and runtime request admission under `-race` where supported.
  - Stress failover/race/thinker continuations while admin mutations commit; assert no data races, deadlocks, goroutine leaks, torn revisions or in-flight route changes.
  - Keep synchronization simple; remove redundant locks/caches discovered during stress testing rather than adding compensating state.
  - _Requirements: 2.2, 2.3, 3.3, 3.6, 3.8, 7.6, 10.9_
  - _Design rules: D2, D3, D10_
  - _Boundary: concurrency tests + minimal fixes_
  - _Depends: 5.1, 5.2, 5.3, 5.4_
  - _Validation: go test -race ./internal/core/routeoverride/... ./internal/core/b2bua/... ./internal/core/runtime/... where supported_

- [x] 6.2 Run architecture, unit, parity, quality and wide QA gates; simplify before completion
  - Run focused package tests first, then `make test-unit`, `make quality-checks`, and architecture checks.
  - Run `make parity-checks` to confirm no frontend/backend compatibility regression from composition changes.
  - Run `make test-race` where supported and `make qa` for final wide verification.
  - Review the final diff for accidental public API expansion, provider/frontend/backend branching, duplicate selector compilation, stale caches, raw-selector logging, or unnecessary abstractions and refactor them out.
  - Do not mark the spec implementation complete unless every acceptance criterion has direct test evidence and all skipped environment-dependent checks are explicitly reported.
  - _Requirements: 10.2, 10.3, 10.4, 10.5, 10.6, 10.7, 10.8, 10.9, 10.10_
  - _Design rules: D1–D12_
  - _Boundary: repository-wide verification/refactor only_
  - _Depends: 6.1_
  - _Validation: make test-unit && make quality-checks && make parity-checks && make qa_

## Implementation Notes

- Standard memory/Bun continuity always wires `routeoverride.Reader`; `routing.override_admin.enabled` only mounts HTTP.
- Parallel-race tests must hold both Opens before either can win (`tryOpenParallelGroup` can skip a loser after `winnerIdx`).
- Windows: `make test-race` / `go test -race` cannot initialize (cgo); Linux CI remains the race surface.
- Postgres override contracts are `//go:build integration` and `SkipUnlessPostgres` (`LIP_REQUIRE_POSTGRES=1`).
- Full `make lint`/`make qa` can still fail on HEAD-identical billing test lint (gofumpt/paralleltest/context-as-argument); `golangci-lint run --new-from-rev=HEAD` is clean for this feature.

