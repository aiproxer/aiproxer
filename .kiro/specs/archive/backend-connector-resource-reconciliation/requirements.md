# Requirements Document

## Introduction

Go-LIP shall reduce unnecessary reconstruction of expensive executable backend connector resources across immutable runtime generations when a material configuration reload does not change those connectors. The optimization shall preserve the existing generation publication model: every request remains bound to one immutable `GenerationRuntime`; changed resources are constructed before publication; old generations and their resources remain valid until their existing work drains; candidate failure leaves the last-good generation untouched.

This specification borrows only the Cordis-v4 ideas that fit this problem: semantic provider identity, reconciliation of unchanged desired resources, explicit physical-incarnation identity, and retention while dependent generations still hold the provider. It does **not** introduce a Cordis component runtime, reactive dependency graph, fibers, dependency injection, service location, HMR, or a generic effect/resource system.

The first implementation is intentionally narrow. It targets discovered executable backend connectors whose declared process model is `per_instance` and whose current reload policy permits candidate/active overlap. Installed-but-disabled connectors, statically linked backends, and `shared_artifact` connectors remain outside the first implementation.

## Boundary Context

- In scope: deterministic scale evidence, private physical connector-resource identity, process-lifetime reconciliation, per-generation leases, candidate rollback, invalidation/incarnation behavior, Acquire/Close linearization, shutdown ordering, established host-session concurrency, and focused executable-connector integration.
- Out of scope: general component reconciliation, frontend/feature reconciliation, dynamic plugin install/uninstall, discovery watchers, new backend-plugin ABI fields, new public configuration knobs, built-in backend pooling, `shared_artifact` behavior changes, request migration between generations, live mutation of a published backend instance, or redesign of host-session execution concurrency.
- Existing authorities remain: `ProcessServices` for process-owned resources, `ResourceLedger` for generation-owned cleanup, `processhost.Host` for executable process/IPC supervision, runtimehost generation leases for request/async lifetime, and the existing backend-plugin ABI for connector behavior.
- Performance intent: eliminate redundant connector construction work during material reloads. This is not a request-hot-path optimization.

## Requirement 1: Evidence-First Scale Justification

1.1. Before production reuse is enabled, add a deterministic high-cardinality characterization harness that compiles overlapping generations with at least 100 enabled synthetic host-backed `per_instance` connector instances.
1.2. The harness shall count at minimum physical connector builds, `processhost` activations or launches, Configure operations, and physical cleanup operations; timing/allocation benchmarks may supplement but shall not replace these deterministic counters.
1.3. The baseline shall demonstrate current reconstruction behavior for an unrelated material reload in which connector-defining generation inputs are unchanged.
1.4. After implementation, an unrelated material reload with `N` unchanged eligible live connectors shall perform **zero** new physical connector builds, activations/launches, and Configure operations for those `N` connectors; it may perform `N` lightweight lease acquisitions and rebuild normal generation-local projections.
1.5. For a candidate with `K` changed or unusable eligible connector identities and all remaining eligible connectors unchanged, physical construction shall be proportional only to `K`.
1.6. Test acceptance shall not depend on fixed wall-clock thresholds vulnerable to CI host variance. Benchstat or equivalent measurements may be supporting evidence.
1.7. If these count-based gains cannot be achieved without changing request semantics, weakening shutdown/reload safety, or introducing a general runtime/container abstraction, implementation shall stop or re-scope rather than preserve speculative infrastructure.

## Requirement 2: Narrow Eligibility and Ownership Boundary

2.1. Reconciliation shall be private to runtime composition and shall not become a public SDK capability or request-time service locator.
2.2. The initial eligible set shall be discovered executable connectors using `ProcessModelPerInstance` and an overlap-safe reload policy.
2.3. Built-in/in-process backend factories shall retain current generation ownership unless a later evidence-backed specification separately justifies reuse.
2.4. `ProcessModelSharedArtifact` connectors shall retain their current explicit sharing/restart-required semantics; this specification shall not weaken their isolation or overlap gates.
2.5. Plugin discovery/trust shall remain startup-fixed. Connector installation, removal, directory rescanning, executable upgrade discovery, and automatic file watching are not added.
2.6. `processhost.Host` shall remain the sole executable process/IPC supervisor. The reconciliation layer shall not duplicate launch, peer authentication, process-tree cleanup, slot/instance supervision, or transport management.
2.7. No new public YAML field, manifest field, CLI flag, environment variable, or backend-plugin ABI field shall be required merely to enable this internal optimization.
2.8. Because discovered lifecycle factory closures are installed before `ProcessServices` construction, the private reconciliation owner shall be created beside the discovered `processhost.Host`, captured lexically by eligible factory closures, and then have its lifetime transferred into `ProcessServices`; no global registry, service locator, or mutable post-construction lookup may bridge this timing boundary.
2.9. The reconciliation owner shall remain connector-specific and package-private and shall expose no generic keyed `Get`/`Resolve` API for unrelated runtime services.

## Requirement 3: Semantic Physical Resource Identity

3.1. Reuse shall require an exact private identity representing the configured physical connector resource, not Go object equality and not only the logical backend instance ID.
3.2. At minimum, identity treatment shall cover logical instance ID, factory kind, exact verified executable artifact digest, process model, opaque connector configuration content, effective configure-time runtime policy, and configure-time secret material by a non-reversible fingerprint.
3.3. Identity construction shall be deterministic for semantically identical effective inputs within one process.
3.4. Secret plaintext shall never be retained in the identity, logged, emitted in diagnostics, or exposed through public status. Secret fingerprinting shall be local, length-framed, deterministic for equality, and private.
3.5. Executable artifact replacement shall produce a distinct identity even when logical instance ID and YAML configuration are unchanged.
3.6. Credential/secret change shall produce a distinct identity when secret material is part of effective Configure input.
3.7. Runtime-policy change shall produce a distinct identity when the normalized policy differs.
3.8. Process-model change shall not accidentally reuse a `per_instance` entry; unsupported/non-eligible process models shall use their existing non-pooled path.
3.9. `BackendStateIdentity` may provide precedent or low-level hashing helpers, but its current `{InstanceID, FactoryKind, ConfigDigest}` contract is insufficient proof that two physical connector resources are interchangeable.
3.10. Identity shall be derived at one explicit construction/configuration choke point. A future configure-time or launch-identity input shall require an intentional identity decision rather than being silently omitted.
3.11. If a construction/configure input varies between generations and cannot be represented safely and deterministically, that resource shall be non-shareable and use current generation-local construction.
3.12. Facts that are startup-fixed in the current production path, including discovered artifact/process-model and install-time runtime policy, need not be presented as hot-reload dimensions; focused identity/construction tests shall still prove they cannot alias if exercised directly, and drift tests shall force review if their lifetime changes.

## Requirement 4: Process-Scoped Acquire, Waiter Reservation, and Close Linearization

4.1. An eligible physical connector resource shall have one process-scoped reconciliation entry and zero or more generation lease claims.
4.2. Acquiring an exact live identity that is current shall reserve a claim under the pool mutex and reuse the existing configured resource rather than invoking physical construction.
4.3. For an absent identity, exactly one pool-owned physical builder shall be started. Every caller waiting on a building entry, including the initiating caller, shall reserve its prospective lease claim **before** waiting; caller cancellation releases only that reserved claim and does not cancel a builder needed by other callers.
4.4. A fast first claimant shall not be able to release the resource to zero while another uncanceled waiter for the same build has not yet completed its handoff. The reserved-claim protocol or an equivalent barrier shall make this scheduling race impossible.
4.5. The physical builder shall run under a pool-owned cancellation context, not a caller-owned/background lifetime. Pool shutdown shall cancel that build context before joining builders.
4.6. `Acquire` success and `Close` shall have an explicit linearization order under the same synchronization boundary: once `Close` marks the pool closing, no new Acquire may reserve a claim and no pending Acquire may publish/hand off a newly built resource as a post-close success.
4.7. `Close` shall reject new acquisitions, cancel pool-owned builders, wait for in-flight builders and acquisition handoffs to terminate, and only then perform residual physical cleanup. A builder finishing after closure begins shall clean its result without publishing it as reusable.
4.8. A failed physical build/configure shall not be permanently negative-cached; waiters observe the failure, reservations are released, and a later independent acquisition may retry.
4.9. Each generation-facing lease release shall be idempotent. Releasing one of several claims shall not close the physical resource; final normal release detaches the entry and invokes entry-owned physical cleanup.
4.10. The physical cleanup returned by underlying adapter/process construction shall be retained only by the reconciliation entry. Every generation-facing `BackendBuildResult.Cleanup` for a pooled resource shall be a fresh lease release, never the physical cleanup function.
4.11. Eligible pooled backend values/lifecycle hooks shall expose no alternate generation-owned `Close`/`Stop` path capable of bypassing the lease and tearing down a resource retained by another generation.

## Requirement 5: Preserve Immutable Generation and Candidate-Isolation Semantics

5.1. Published `GenerationRuntime` objects remain immutable; no connector resource shall be reconfigured or replaced underneath a published generation.
5.2. If connector identity changes, the candidate shall construct a distinct replacement before publication while the old generation retains its old resource until its leases drain.
5.3. If a connector is removed or disabled, the candidate shall acquire no replacement lease; old generations may continue using the removed connector until their retained work drains.
5.4. A failed candidate that leased an existing resource shall release only its candidate lease and shall not disturb the active generation's lease/resource.
5.5. A failed candidate that created a new resource shall release it through rollback; if no other claim exists, physical cleanup shall complete before rollback completes.
5.6. Generation-owned derived state—including executor maps/views, routing views, model-registry runtime, model catalog, feature composition, policy state, billing composition, and generation lifecycle context—shall remain generation-owned and shall not move into the connector pool.
5.7. Existing no-drop, retained-generation, old-stream/async, and last-good reload guarantees shall remain unchanged.
5.8. A reused configured connector may contribute the same underlying backend/session functions to multiple generation-local executor maps, but each generation shall rebuild its own normal projections and lifecycle structures.
5.9. On a reuse hit, candidate preparation shall not invoke `Configure`, `Start`, `Stop`, `Close`, mutating preflight, or another generation-local mutation on the shared physical connector. Candidate rejection/rollback shall never invalidate the shared resource merely because the candidate was rejected.
5.10. Query-shaped operations already represented by the backend-plugin contract, such as `Resolve` and `ListModels`, may remain part of generation-local preparation/refresh subject to Requirement 8 concurrency gates.
5.11. If future generation preparation or an external adapter requires a mutating lifecycle action against the configured connector, that resource path shall become non-shareable until a separate design proves safe reuse.

## Requirement 6: Invalidation, Detached Entries, and Physical Incarnations

6.1. Semantic resource identity and physical resource incarnation shall be distinct concepts: the same semantic identity may later require a fresh physical incarnation after failure.
6.2. When the existing connector/process invalidation path declares a physical incarnation unusable, that exact entry shall become non-acquirable for future candidates before or atomically with delegating to processhost invalidation.
6.3. Invalidation shall remove only the exact failed incarnation from the `current` semantic index. A stale callback from an older incarnation shall not detach a newer current incarnation.
6.4. Detached/invalidated entries may remain referenced by generations that already leased them and shall remain tracked by the process reconciliation owner until their physical cleanup has completed.
6.5. The pool shall maintain an ownership set or equivalent enumeration of **all successfully constructed but not-yet-physically-cleaned entries**, including detached entries, so terminal process shutdown can fail-safe clean them.
6.6. A future acquisition for the same semantic identity after invalidation shall build a fresh incarnation rather than returning the detached entry.
6.7. Invalidation shall not live-swap a replacement into existing generations or decrement their lease claims merely because the physical incarnation failed.
6.8. Existing `processhost` generation invalidation/reap behavior remains authoritative for the physical process. Reconciliation controls only future reuse eligibility, dependent retention, and the timing of the per-resource cleanup capability.

## Requirement 7: Exactly-Once Physical Cleanup and Shutdown Ownership

7.1. There shall be exactly one logical physical cleanup capability for each pooled configured connector resource. It shall be an idempotent composite of the existing adapter/session cleanup and `ActivateResult.Cleanup`/`processhost.CloseInstance` path.
7.2. The reconciliation entry—not each lease—shall own the physical-cleanup once-state and stored cleanup result. Final lease release, pool shutdown, and invalidation-related terminal cleanup shall all converge on that same entry-level exactly-once operation.
7.3. `processhost.Host` does **not** transfer supervisory ownership of processes/slots/instances to the pool. It retains launch, instance tables, invalidation, reaping, and `Host.Close` fail-safe authority; the pool owns only the decision of when to invoke the existing per-resource composite cleanup while dependents still exist.
7.4. The reconciliation owner shall close before `processhost.Host` during normal process teardown so residual per-resource cleanup can still call the live host/session ownership paths.
7.5. Normal successful shutdown ordering shall remain: generation admission stops and generations drain/release leases; pool close linearizes/cancels and joins any builders; pool fail-safe cleans any residual current **and detached** entries; `processhost.Host` closes; verified artifact handles close; staging removal runs.
7.6. Bootstrap/error cleanup before process ownership transfer shall preserve the same relative pool -> host -> artifacts -> staging order for resources already acquired.
7.7. Pool shutdown shall be idempotent. `Close` racing a final lease release shall still execute physical cleanup exactly once; a later lease release after fail-safe shutdown cleanup shall be harmless.
7.8. Existing cleanup-error normalization/error-join behavior shall be preserved. Final normal lease-triggered cleanup errors surface through existing generation rollback/close aggregation; process-shutdown residual cleanup errors join the existing process close aggregation.
7.9. Partial construction failure shall not leak sessions, host instances, processes, IPC connections, pool entries, reserved claims, or builder goroutines.
7.10. `ProcessServices` shall own reconciliation shutdown through its existing private process-resource ownership mechanism; no second closer stack or process shutdown coordinator shall be introduced.

## Requirement 8: Connector Concurrency and Non-Interference

8.1. Reuse is valid only when the complete construction/configure identity and established host-session behavior make one configured resource safe to retain across overlapping generations; otherwise use isolated construction.
8.2. The standard production `backendplugin/host.Session` concurrency behavior shall remain unchanged. In particular, its existing `lifecycleMu` serialization of `Execute` versus `Execute`/`Close` shall not be removed or bypassed by this specification.
8.3. Sharing one standard Session therefore extends the existing per-session Execute serialization across overlapping generations. The implementation shall explicitly characterize a retained old-generation Execute overlapping a new-generation Execute and shall not claim preservation of the incidental extra execution parallelism provided today by two separately constructed sessions.
8.4. Query/auxiliary RPCs that the current standard host already permits to overlap—`Resolve`, `ListModels`, optional `CountTokens`, and optional `FinalizeBilling`—shall receive race/conformance coverage when invoked through one pooled Session across overlapping generations and alongside execution.
8.5. A non-standard/injected connector session path that cannot satisfy the established standard-host operation-concurrency contract shall be non-shareable rather than gaining a new public concurrency flag in this specification.
8.6. Dynamic provider facts modeled through runtime calls continue through those calls and do not require connector reconstruction on unrelated generation changes.
8.7. The optimization shall not alter canonical requests/events, route selection, retries, failover, output-commit rules, stream ordering within an attempt, cancellation, billing finalization semantics, accounting evidence, token counting, or provider-specific translation semantics.
8.8. No resource-pool lookup or lock shall be added to normal request execution; acquisition/release occurs at generation construction/retirement boundaries.
8.9. Existing backend-plugin security invariants—verified artifact binding, secure local IPC, peer authentication before Configure/secrets, environment restrictions, and process-tree cleanup—shall remain unchanged.
8.10. Connector-specific configuration parsing remains inside the connector. The host may fingerprint opaque Configure bytes but shall not learn provider-specific schemas merely to decide reuse.
8.11. Functional/canonical behavior for unchanged resources shall remain equivalent to the current overlap model except for explicitly documented physical-resource reuse and the resulting extension of the existing per-session Execute serialization across retained generations.

## Requirement 9: TDD, Concurrency, and Architecture Gates

9.1. Add RED tests for high-cardinality construction counts, identity discrimination, waiter reservation, lease lifetime, detached-entry ownership, rollback, invalidation/incarnation behavior, Acquire/Close linearization, builder cancellation, exactly-once cleanup, operation concurrency, and shutdown ordering before enabling reuse.
9.2. Include scheduling-sensitive tests for: first claimant release before a waiter wakes; waiter cancellation; final release racing new Acquire; final release racing Pool.Close; invalidation followed by Pool.Close with an outstanding old-generation lease; and stale invalidation racing replacement publication.
9.3. Include a blocked physical builder that exits only on its pool-owned context cancellation; Pool.Close must cancel it, wait for it, clean any partial/success result, and prevent late publication.
9.4. Split identity evidence into two scopes: the high-cardinality generation-reload matrix covers inputs that actually vary through current reload plus invalidation; a focused physical identity/construction matrix independently proves artifact digest, secret fingerprint, process model, and normalized runtime-policy differences cannot hit the same pooled entry. `shared_artifact` remains a non-pooled fallback, not a pooled replacement case.
9.5. Add a DTO/input drift gate forcing deliberate identity review when the external configure-time physical input surface changes.
9.6. Add race/goleak coverage for the private pool/builders and overlapping standard-host metadata/auxiliary/execution operations across retained generations.
9.7. Add an exactly-once ownership regression that counts adapter/session cleanup, `ActivateResult.Cleanup`/host instance cleanup, pool fail-safe cleanup, and later `Host.Close`, proving the same pooled physical resource cannot be torn down twice through competing ownership paths.
9.8. Architecture tests shall reject a generic service registry/container API, request-time pool lookup, public reusable-resource framework, provider-specific switch, or migration of `processhost` supervision responsibility.
9.9. Repository quality, focused executable-plugin security/conformance tests, existing backend recomposition/overlap tests, ResourceLedger lifecycle tests, and reload no-drop/last-good tests shall remain green.
9.10. Final implementation review shall remove unused abstraction layers and preserve the smallest private design that satisfies deterministic scale and correctness gates; pooled reuse shall be re-scoped if the established Session concurrency model makes the measured overlap behavior operationally unacceptable.
