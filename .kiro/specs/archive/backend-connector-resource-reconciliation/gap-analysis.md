# Brownfield Requirements Gap Analysis

## Result

**PASS after requirements corrections.** The current Go-LIP architecture already has the ownership, immutable-generation, executable-process, and identity primitives needed for a focused reconciliation layer. The missing capability is not general dependency management: it is a process-scoped way for overlapping generations to share one unchanged configured external `per_instance` connector resource without sharing generation-local derived state.

The initial requirements were directionally correct but needed several brownfield constraints made explicit before design. Those corrections are recorded below and must be reflected in the final `requirements.md`.

## Existing Brownfield Facts

- `buildBackends` constructs every enabled backend row during generation compilation, so a material reload can reconstruct unchanged backends.
- Discovered executable `per_instance` factories deliberately mint a generation-unique host activation handle so candidate and active generations can coexist safely today.
- `processhost.Host` already supervises process slots, authenticated IPC, configured instances, invalidation, and cleanup; duplicating those responsibilities would be harmful.
- `BackendStateIdentity` already demonstrates identity-sensitive reuse for affinity/health observation state, but its identity is intentionally narrower than the inputs that define a configured physical connector.
- `ProcessServices` now has the private `processResourceOwner`/owned-acquisition discipline from the earlier atomic-owned-resource-lifecycle work.
- The executable plugin host, verified artifacts, and staging directory are created before `ProcessServices`, then ownership transfers into `ProcessServices` before initial generation compilation.
- Installed connector catalog size is not itself the scaling problem: discovery is lazy with respect to process launch and already has 100-manifest no-launch coverage.

## Gaps and Required Corrections

### 1. Installed connector cardinality is not the target scaling dimension

A large trusted catalog does not imply a large live resource set because discovery does not launch every connector. The expensive case is many **enabled configured process-backed instances** combined with material runtime reload.

**Correction:** all scale requirements and benchmarks are defined in terms of enabled eligible connector instances, not installed manifest count.

### 2. `BackendStateIdentity` is too weak for physical-resource reuse

Its `{InstanceID, FactoryKind, ConfigDigest}` identity is appropriate for affinity/health continuity but does not represent exact executable artifact, configure-time runtime policy, secret values, or process model.

**Correction:** define a separate private physical-resource identity. Reusing helpers is allowed, but physical reuse must never be authorized solely by `BackendStateIdentity.Compatible`.

### 3. All configure-affecting inputs must participate in identity or reuse must fail closed

The executable plugin receives opaque YAML, `SecretBundle`, `RuntimePolicy`, factory/instance identity, and negotiated process context. Future additions to configure-time input create a correctness hazard if the identity silently ignores them.

**Correction:** require an explicit identity-construction choke point over the effective physical construction/configure input. If an input cannot be safely fingerprinted or declared process-stable, the resource is non-shareable and uses current generation-local construction. Add an architecture/contract test that forces identity review when configure-time DTO shape changes.

### 4. The pool must exist before discovered lifecycle factories capture it

Production installs discovered factory closures before `NewProcessServices`, while process ownership is transferred afterward. Creating the pool only inside `NewProcessServices` would arrive too late unless the factory used an indirection/service locator, which this project deliberately avoids.

**Correction:** create the private reconciliation owner beside the discovered `processhost.Host` during discovered-install preparation, capture it directly in eligible factory closures, then transfer its lifetime into `ProcessServices`. No global lookup or post-construction setter is needed.

### 5. Pool shutdown ordering is constrained by existing host/artifact/staging ownership

Physical resource cleanup may need adapter/session cleanup followed by host instance cleanup. Therefore the resource pool must be torn down while `processhost.Host` and verified artifacts are still usable.

**Correction:** on successful process ownership transfer, register ordering so normal reverse teardown is: generations drain → reconciliation pool/final physical cleanup → `processhost.Host` → verified artifacts → staging removal. Bootstrap-error release must preserve the same dependency order.

### 6. Generation cleanup and physical cleanup are different ownership concepts

Current `BackendBuildResult.Cleanup` is transferred into `ResourceLedger`. If that same cleanup were copied into two generations, either generation could close a connector still referenced by the other.

**Correction:** the pool owns the physical cleanup exactly once. Each generation receives a fresh idempotent **lease release** as its `BackendBuildResult.Cleanup`; `ResourceLedger` continues to own that cleanup. No backend `Close`/lifecycle hook returned to the generation may bypass the lease and directly close the shared physical resource.

### 7. Semantic identity is not physical incarnation identity

A connector process/session may die while its desired configuration remains unchanged. Reusing by semantic key alone would hand a dead resource to the next candidate. Conversely, an old failure callback could incorrectly invalidate a newly rebuilt resource with the same semantic key.

**Correction:** every physical entry has an incarnation token/version. Invalidation detaches only the exact incarnation, making it unavailable to future acquisitions; a later acquire creates a new incarnation. Stale invalidation cannot evict a newer incarnation.

### 8. `shared_artifact` is a different problem

The current shared-process model has explicit isolation/concurrency declarations and can be restart-required when overlap is unsafe. Mixing it into first-pass reconciliation would entangle process slot sharing with generation resource sharing.

**Correction:** first implementation is `per_instance` discovered external connectors only. `shared_artifact` remains unchanged.

### 9. Generation-local derived state must not be accidentally pooled

A configured external session can be reused while executor maps, routing views, model-registry runtime, model catalog, feature surface, lifecycle context, and policy/accounting composition remain generation-specific.

**Correction:** pool only the configured physical connector adapter/backend resource. Rebuild all current generation projections exactly as today from the leased backend/profile/inventory surface.

### 10. Dynamic inventories/capabilities do not automatically make a resource non-shareable

The adapter already models dynamic facts through runtime operations such as `Resolve` and `ListModels`. Reconstructing a connector merely because an unrelated generation changed would not make those dynamic facts more correct.

**Correction:** reuse is allowed when **construction/configure inputs** are identical; dynamic runtime queries remain dynamic. If a connector requires generation-dependent hidden configuration not represented at configure time, it is non-shareable until that dependency is explicit in identity.

### 11. A timing-only ROI gate would be fragile

The existing candidate compiler is fast on ordinary fixtures, while real executable connector startup cost can vary by platform and connector. Fixed millisecond thresholds would conflate CI noise with architecture value.

**Correction:** deterministic operation counts are the primary acceptance evidence. Wall time, allocations, process count/RSS/FD observations, and benchstat remain supporting evidence.

### 12. The previous Cordis-inspired ownership spec does not already solve reuse

`atomic-owned-resource-lifecycle` correctly hardened ownership locality and deliberately left backend construction unchanged because that work addressed forgotten cleanup, not cross-generation physical reuse. The new concern appears at connector-scale reload cardinality.

**Correction:** reuse the existing ownership primitives; do not replace or generalize them. This specification adds one connector-specific lease/reconciliation owner and nothing broader.

## Brownfield Compatibility Matrix

| Existing subsystem | Required treatment |
|---|---|
| `runtimehost.Manager` / generation leases | unchanged |
| `GenerationRuntime` immutability | unchanged |
| `ResourceLedger` | owns per-generation lease release; unchanged authority |
| `ProcessServices` | gains lifetime ownership of one private connector reconciliation owner |
| `processResourceOwner` | reused for process teardown registration; not replaced |
| `processhost.Host` | unchanged process/IPC supervisor |
| discovered factory install | captures private pool before ProcessServices ownership transfer |
| `BackendBuildResult` | eligible factory returns leased cleanup instead of physical cleanup |
| `BackendStateIdentity` | remains affinity/health identity; not physical reuse authority |
| model registry/catalog | rebuilt per generation |
| plugin discovery/trust | startup-fixed and unchanged |
| backend-plugin ABI | unchanged |
| built-in backends | unchanged |
| `shared_artifact` connectors | unchanged |
| routing/streaming/retry/accounting | unchanged |
| public config/SDK | no new surface |

## Corrected Required Invariants

1. Optimization scope is enabled eligible executable `per_instance` connector resources, not catalog size.
2. Physical reuse requires complete configure/construction identity; incomplete identity falls back safely.
3. Semantic identity and physical incarnation are separate.
4. One physical resource has exactly one physical cleanup owner and many generation lease owners.
5. `ResourceLedger` owns lease release, never a shared physical closer.
6. Candidate rollback is local: releasing a reused lease cannot disturb the last-good generation.
7. Changed/removed resources preserve old-generation availability until drain.
8. Pool shutdown precedes host/artifact teardown and does not become a second process supervisor.
9. Generation-local projections remain generation-local.
10. No generic container, service locator, dynamic dependency graph, watcher, or request-time lookup is introduced.
11. Deterministic high-cardinality operation counts are the primary ROI gate.

## Requirements Correction Status

The final requirements must incorporate gaps 3–7 especially: a configure-input identity choke point, pre-`ProcessServices` pool construction/ownership transfer, strict physical-cleanup versus lease-cleanup separation, incarnation-safe invalidation, and explicit teardown order. Once those are present, the requirements quality gate is **PASS**.
