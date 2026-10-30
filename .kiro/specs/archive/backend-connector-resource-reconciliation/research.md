# Research & Design Decisions

## Summary

- **Feature**: `backend-connector-resource-reconciliation`
- **Discovery Scope**: brownfield runtime optimization / connector-scale lifecycle refactor
- **Selected Cordis principle**: semantic provider identity + physical incarnation + dependent retention/reconciliation.
- **Not selected**: generic Cordis component runtime, reactive dependency graph, fibers, service locator, HMR, or generic effect/resource framework.
- **Primary target**: unchanged discovered executable `per_instance` connector resources reconstructed across overlapping immutable generations.
- **Post-review hardening**: detached-entry ownership, reserved waiter claims, entry-level exactly-once physical cleanup, pool-owned builder cancellation, explicit Acquire/Close linearization, exact cleanup handoff to processhost, identity-evidence split, and established host-session concurrency characterization.

## Brownfield Findings

### Generation correctness is already solved at the right coarse boundary

Go-LIP already provides immutable `GenerationRuntime`, request/async generation retention, transactional last-good publication, `ResourceLedger` rollback/retirement, and manager-owned drain. Those mechanisms remain authoritative. A Cordis-like dynamic component graph would duplicate existing correctness machinery.

### The scale pressure is enabled live connector count, not manifest count

Discovery is manifest/trust oriented and does not launch every installed connector. The expensive case is many **enabled configured external connector instances** rebuilt during a material generation change. `buildBackends` constructs every enabled backend row for every candidate generation.

### `per_instance` discovered connectors deliberately duplicate across generations today

`buildDiscoveredBackend` mints unique host activation IDs for `per_instance` so active and candidate generations can safely coexist. This is correct for changed connectors but also means an unchanged connector is physically Activated/Configured again during unrelated material reload.

### Reconciliation belongs above processhost

`processhost.Host` already owns lazy process launch, secure local IPC, peer authentication, slot/instance bookkeeping, process-generation invalidation, and process-tree cleanup. It should not learn Go-LIP semantic generation/config identity. The new owner is therefore a private runtimebundle connector-resource reconciliation layer that calls the existing physical builder/host.

### Prior Cordis-derived ownership work remains valid

The archived `atomic-owned-resource-lifecycle` spec hardened process acquisition/cleanup locality and generation loop ownership but deliberately left backend lifecycle alone because `BackendBuildResult` already paired backend + cleanup and `buildBackends` transferred cleanup to `ResourceLedger`. This spec addresses a different problem: **one expensive physical backend resource may be retained by multiple overlapping generations**. It reuses rather than replaces those ownership authorities.

## Physical Identity Research

### Existing `BackendStateIdentity` is precedent, not the key

Go-LIP already reuses process-owned affinity/health observation state across compatible generations using `{InstanceID, FactoryKind, ConfigDigest}`. That demonstrates identity-sensitive continuity is locally idiomatic, but physical connector interchangeability is stricter.

### Physical construction inputs

Current discovered construction uses/captures:

- logical `InstanceID`;
- factory kind;
- exact verified artifact digest;
- process model/sharing profile;
- opaque YAML Configure bytes;
- normalized `RuntimePolicy`;
- `SecretBundle` when supplied;
- negotiation/session behavior from the fixed executable/host protocol.

Therefore a separate private physical identity must treat artifact, process model, config bytes, policy, secrets, factory and logical instance deliberately. Secret values are locally hashed with deterministic length framing and never surfaced.

### Startup-fixed versus reload-varying facts

The production discovered factory closure is installed at startup. Artifact/process model and `DiscoveredInstallOptions.RuntimePolicy` are captured there. Current SIGHUP reload does not rediscover/replace the artifact. The current discovered builder also does not inject changing secret material through the shown production path.

Consequences:

- these fields still belong in the **identity contract** because they define physical semantics and may evolve later;
- high-cardinality **generation reload** evidence should not pretend artifact/process-model/policy changes are currently hot-reloadable;
- focused identity/construction tests exercise those dimensions directly;
- `shared_artifact` remains a non-pooled/restart-required fallback rather than a “changed pooled process model” case.

This resolves CodeRabbit's request for broader identity coverage without inventing unsupported reload capabilities.

## Physical Cleanup and processhost Ownership

### Current composite cleanup

The current discovered builder combines:

1. adapter `BuildResult.Cleanup()` -> `session.Close(...)`, which closes the configured connector instance/host session;
2. `ActivateResult.Cleanup` -> `processhost.Host.CloseInstance(hostActivationID)`, which updates host instance ownership and reaps the slot/process when appropriate.

The adapter BuildResult itself is once-guarded, and processhost reaping is idempotent, but pooling adds another possible caller (`Pool.Close`). A **lease-level** once guard alone is therefore insufficient.

### Selected ownership wording

`processhost.Host` keeps supervisory ownership: process/slot tables, peer identity, invalidation, reaping, and `Host.Close` fail-safe behavior stay there.

The pool entry consumes the existing **per-resource composite cleanup capability** when a physical build succeeds. Generations never receive that composite; they receive only lease release. The pool therefore controls *when* the per-resource cleanup may be invoked, while processhost remains the component that actually supervises/reaps physical processes.

An entry-level cleanup-once state is shared by normal final lease release and process shutdown. This avoids a final-release-versus-Pool.Close double cleanup race.

## Detached Resource Ownership

Initial design only indexed `current[semanticIdentity]`. That is insufficient after invalidation:

```text
identity X -> incarnation 7 current
invalidate 7
current[X] removed
Gen17 still has lease to incarnation 7
```

If ProcessServices then closes before that lease is released, a Pool.Close that only walks `current` cannot enumerate incarnation 7.

Selected correction: retain an `owned`/all-entry set containing every successfully constructed physical incarnation until entry-level physical cleanup completes. Invalidation only detaches from `current`; it does not surrender process-level ownership bookkeeping. Pool Close snapshots all residual owned entries, including detached/invalidated ones, and invokes the same once-guarded physical cleanup.

## Acquire/Waiter Concurrency

### Why post-publication waiter ref increments are unsafe

The original pending-entry idea let waiters observe `building`, wait on readiness, and increment refs only after publication. A scheduler can then do:

1. first caller builds, receives lease;
2. waiter is still sleeping/not yet incremented;
3. first caller releases, refs reach zero, physical resource closes;
4. waiter wakes and attempts to acquire the now-closed entry.

Selected correction: **every waiter reserves its prospective claim under the pool mutex before waiting**. The initiating caller is also just a claimant. A waiter cancellation abandons only its own reservation.

### Builder ownership

The initiating Acquire must not own the physical builder lifetime. Otherwise caller cancellation and contextless `ProcessServices.Close()` can leave an unbounded build blocking shutdown.

Selected correction:

- absent Acquire creates a pending entry and reserves its claim;
- the pool starts one short-lived builder goroutine under a pool-owned cancellation context;
- all Acquires wait on entry readiness or their own context;
- Pool.Close sets closing, cancels the build root, then waits builders;
- build completion after closing may clean a result but cannot publish it as reusable.

This uses existing processhost/transport context cancellation rather than adding a background worker subsystem.

## Acquire/Close Linearization

`ProcessServices.Close` is contextless and synchronous, so the pool needs a precise terminal contract.

Selected contract:

- `Close` linearizes by setting `closing=true` under the same mutex used for Acquire claim reservation;
- after that point no new claim reservation is accepted and no pending builder result is handed off as a post-close success;
- Close cancels pool-owned builders;
- Close waits builders and acquisition handoff activity;
- only then does it snapshot/clean residual owned entries;
- no physical build/cleanup/wait happens while the mutex is held.

This is stronger than simply “reject new Acquire” and directly addresses the lease-after-cleanup race CodeRabbit identified.

## Invalidation and Incarnations

Semantic identity says the desired configuration is unchanged. Physical incarnation says which concrete process/session currently realizes it.

Invalidation:

- compares exact entry/incarnation;
- detaches that entry from `current` before/with host invalidation;
- leaves the detached entry in process ownership until cleanup;
- does not decrement existing generation claims or live-swap a replacement;
- allows a future candidate to build a fresh incarnation for the same semantic key;
- stale old-incarnation callbacks cannot remove the new current entry.

## Candidate Isolation

Fresh physical processes currently give candidate preparation strong failure-domain isolation. Reuse is allowed only for query-shaped candidate preparation:

- no Configure/Start/Stop/Close/mutating preflight on a reuse hit;
- rollback releases only the candidate lease;
- candidate failure alone does not invalidate the resource;
- future mutating preparation makes the path non-shareable until separately designed.

Generation-local model registry/catalog/routing/policy/billing state remains generation-owned.

## Established Operation Concurrency

### Standard host Session behavior

Cross-checking `pkg/lipsdk/backendplugin/host/session.go` shows:

- `Session.Execute` holds `lifecycleMu` across the complete Execute RPC, so two Execute calls on one Session are serialized and Close cannot race underneath an active Execute;
- `Resolve`, `ListModels`, CountTokens, and FinalizeBilling are not serialized by that client lifecycle mutex;
- server-side instance leasing already allows metadata/auxiliary calls to overlap an active configured instance operation while protecting Close.

Therefore pooling one production Session across generations does **not** justify inventing a new concurrency contract. The implementation must preserve the existing standard-host behavior and test it.

### Important overlap tradeoff

Today Gen17 and Gen18 fresh physical Sessions create two independent Execute serialization domains during candidate/retirement overlap. Pooling an unchanged connector makes retained-old and new-generation Execute calls share one Session serialization domain.

That can reduce transient overlap concurrency / create head-of-line waiting for long streams. It is not a canonical request mutation, but it is operationally material and must not be hidden behind an unconditional “observationally equivalent” claim.

Selected treatment:

- do not change `Session.Execute` concurrency in this spec;
- characterize a long retained old-generation Execute overlapping a new-generation Execute;
- cover metadata/Count/Finalize/execution overlap under race/conformance tests;
- if established serialization makes the optimization unacceptable for target workloads, re-scope pooling rather than widening this spec into ABI/session-concurrency redesign;
- injected/non-standard session paths are non-shareable unless they satisfy the same established host contract.

## Scale/ROI Evidence Strategy

Primary correctness/ROI is deterministic work count:

```text
before: O(N enabled eligible connectors) physical build per material generation
after:  O(K changed/unusable physical builds) + O(N) cheap claims/projections
```

Use at least 100 synthetic enabled discovered `per_instance` connectors. Count physical build, Activate/launch, Configure, physical cleanup, lease acquire/release. Wall time/allocation is supporting evidence only.

Two matrices are required:

1. **Reload matrix**: unchanged, config changes, remove/disable, candidate rollback, invalidation/rebuild.
2. **Physical identity matrix**: artifact, secret, normalized runtime policy, process model, factory/logical identity.

Also record the cross-generation Session Execute serialization behavior so lifecycle savings are evaluated together with the actual overlap scheduling tradeoff.

## Architecture Pattern Evaluation

| Option | Decision | Reason |
|---|---|---|
| Full Cordis runtime | Reject | duplicates generation/runtime ownership machinery |
| Put semantic reconciliation in processhost | Reject | conflates physical supervision with LIP config semantics |
| Reuse `BackendStateIdentity` | Reject | identity incomplete for physical reuse |
| Return same physical cleanup to generations | Reject | early/double teardown |
| Only track current entries | Reject | detached invalidated resources disappear from shutdown ownership |
| Waiter refs after readiness | Reject | zero-ref handoff race |
| Caller-owned physical build | Reject | shutdown cancellation/lifetime ambiguity |
| Pool-owned connector entries + generation leases | **Select** | focused ROI; preserves existing authorities |
| Idle TTL cache | Reject | speculative retention/eviction policy |
| Change Session concurrency here | Reject | broadens scope; preserve/measure existing behavior |
| No change for non-shareable paths | Keep | safest fallback |

## Risks & Mitigations

- **Identity omission** -> one choke point, fail-closed fallback, DTO/input drift test.
- **Detached resource leak** -> process-owned all-entry set until cleanup completes.
- **Waiter zero-ref race** -> reserve claim before waiting.
- **Shutdown hangs on builder** -> pool-owned cancelable build context + joined builder tests.
- **Acquire after shutdown cleanup** -> explicit Acquire/Close linearization and handoff join.
- **Double cleanup** -> entry-level `cleanupOnce`, not only lease once.
- **Pool/processhost ownership confusion** -> pool owns timing of composite per-resource cleanup; host remains physical supervisor/fail-safe.
- **Stale invalidation** -> exact incarnation comparison.
- **Candidate mutates last-good** -> query-only reuse; mutating path non-shareable.
- **Cross-generation Execute head-of-line blocking** -> preserve standard Session semantics, characterize, re-scope if unacceptable.
- **Scope creep** -> package-private connector-specific API and architecture tests.
- **Speculative complexity** -> 100-connector count gate and final simplification/re-scope gate.

## References

- User-supplied paper: *A Programming Paradigm for Spatiotemporal Composability* — semantic provider identity, dependent retention, reconciliation, revertible cleanup concepts.
- `internal/infra/runtimebundle/discovered_factories.go` — current external connector construction and unique per-generation activation handles.
- `internal/infra/backendplugins/processhost/host.go` — process/instance supervision, invalidation and cleanup.
- `internal/infra/backendplugins/adapter/backend.go` and `processhost/build_result.go` — backend/session cleanup shape.
- `pkg/lipsdk/backendplugin/host/session.go` — standard host operation concurrency and Session lifecycle serialization.
- `pkg/lipsdk/backendplugin/server.go` — configured-instance leasing around RPC calls and Close.
- `internal/infra/runtimebundle/backend_state_identity.go` — existing narrower semantic identity precedent.
- `internal/infra/runtimebundle/process_services.go` / `resource_ledger.go` — process/generation cleanup authorities.
- archived `atomic-owned-resource-lifecycle` and runtime convergence specs — explicit ownership/no-container architecture constraints.
