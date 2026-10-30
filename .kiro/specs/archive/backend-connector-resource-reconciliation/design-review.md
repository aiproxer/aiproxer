# Brownfield Design Validation

## Verdict

**GO after CodeRabbit lifecycle/concurrency hardening.** The selected design remains a focused, high-ROI extension of Go-LIP's existing generation architecture, but the first review correctly identified several places where the original prose was weaker than the safety claim. Those findings were cross-checked against `processhost.Host`, runtimebundle ownership, adapter cleanup, and the standard backend-plugin host Session and have been incorporated into normative requirements/design/tasks.

The resulting design still targets O(N) -> O(K) physical connector reconstruction without introducing a general Cordis runtime, processhost redesign, public ABI/config surface, dynamic discovery, or request-path lookup.

## Review Findings and Disposition

### Detached entries survive invalidation — VALID / FIXED

Original `current[identity]` indexing was insufficient for process shutdown: invalidation can remove a resource from the reusable map while old generations still retain leases. `Pool.Close` could not then enumerate it.

The design now requires a process-owned `owned`/all-entry set containing every successfully constructed incarnation until entry-level physical cleanup completes. Invalidation detaches only from `current`; terminal Close snapshots both current and detached residual entries through that ownership set.

### Pending waiter reservation race — VALID / FIXED

Original prose allowed waiters to increment refs only after a building entry became live. A fast first claimant could therefore release to zero before a scheduled waiter formally acquired its ref.

Every waiter now reserves its prospective lease claim under the pool mutex **before** waiting. Cancellation abandons only that claim. A deterministic scheduling test must hold a waiter after reservation and prove first-lease release cannot physically close the resource.

### Lease once versus physical cleanup once — VALID / FIXED

Per-lease `sync.Once` only prevents one lease from releasing twice. It does not protect physical cleanup when final Release races `Pool.Close`.

The physical entry now owns a separate cleanup-once operation and stored result. Final lease release and fail-safe process shutdown converge on that same operation. Detached invalidation does not create another physical cleanup owner.

### Cleanup ownership handoff — VALID / CLARIFIED

Current construction has two existing cleanup pieces: adapter/session cleanup and `ActivateResult.Cleanup` (`processhost.CloseInstance`). The pool entry consumes one composite per-resource cleanup on a physical miss; generations receive only lease release.

This is **not** a transfer of process supervision out of `processhost.Host`. Host keeps slot/instance state, invalidation, reaping, and `Host.Close` fail-safe authority. The pool owns semantic retention and timing of that existing per-resource cleanup capability.

An exactly-once test must count both composite cleanup pieces and subsequent Host.Close.

### Pool Close can hang on an unbounded builder — VALID / FIXED

`ProcessServices.Close` is contextless/synchronous. The original “wait for builders” language was incomplete because current discovered construction can call Activate with a background lifetime.

The pool now owns a cancelable build root. An absent Acquire starts exactly one pool-owned builder goroutine; caller contexts control only claimant waiting. `Pool.Close` linearizes closing, cancels the build root, then joins builders before physical residual cleanup/host teardown. A blocked-builder test must exit only on that cancellation and prove no late publication.

### Identity scenario coverage — VALID IN PRINCIPLE / LITERAL SUGGESTION NARROWED

CodeRabbit correctly requested stronger proof for artifact, secret, policy, and process-model identity dimensions. However, those dimensions are startup-fixed in the current production discovered-factory closure and are not all hot-reloadable through SIGHUP.

The corrected plan therefore uses two evidence matrices:

1. high-cardinality **generation reload** evidence for dimensions that actually vary during current reload plus invalidation;
2. focused **physical identity/construction** evidence for artifact digest, secret fingerprint, normalized runtime policy, process model, factory/logical identity.

`shared_artifact` remains a non-pooled/restart-required fallback rather than being treated as a pooled replacement scenario. This covers the correctness concern without inventing unsupported hot artifact/policy/process-model reload behavior.

### Acquire/Close linearization — VALID / FIXED

The original requirements said only that Close rejects new acquisitions and is race-safe. That is not enough to prevent a pending builder/Acquire from handing out a resource after residual cleanup begins.

The design now gives Close a mutex-protected terminal linearization point. After `closing=true`, no new claim is reserved and no build result is handed off as post-close success. Close cancels builders, waits builders and Acquire handoffs, then snapshots/cleans residual owned entries.

### Shared connector operation concurrency — VALID / FIXED WITH EXISTING CONTRACT

Cross-checking the standard production host shows:

- `backendplugin/host.Session.Execute` already serializes Execute calls with `lifecycleMu` and serializes Execute versus Close;
- Resolve/ListModels/CountTokens/FinalizeBilling can overlap via the existing host/server instance lease model.

The spec therefore does **not** invent a new connector concurrency flag or remove Session serialization. It now explicitly characterizes retained-old-generation Execute overlapping new-generation Execute through one pooled Session and covers metadata/auxiliary overlap under race/conformance tests.

Sharing one Session extends existing Execute serialization across generations and can remove the incidental transient parallelism provided today by two fresh Sessions. This is now documented as an operational tradeoff rather than hidden behind unconditional observational-equivalence wording. If that measured behavior is unacceptable for target workloads, pooling is re-scoped rather than host concurrency being redesigned here.

## Validation Checklist

### Generation consistency — PASS

Published generations remain immutable. Changed identity builds a replacement before publication; removed resources remain available only through old generation leases. No live substitution exists.

### `ResourceLedger` authority — PASS

Every generation receives one fresh lease release. The physical composite cleanup never enters multiple ledgers. `buildBackends` remains the generation cleanup transfer point.

### Processhost authority — PASS

The pool contains no process launch, peer authentication, process-tree cleanup, slot supervision, or request transport logic. `processhost.Host` remains the physical supervisor and terminal fail-safe.

### Process ownership transfer — PASS

Pool is created beside host before factory installation, captured lexically, then transferred to `ProcessServices`. Reverse close order remains pool -> host -> artifacts -> staging on successful ownership transfer and bootstrap failure.

### Physical cleanup exactly once — PASS after correction

Entry-level cleanup-once unifies final release and pool fail-safe shutdown. Generation lease once remains only a claim-release guard.

### Detached ownership — PASS after correction

All successful physical incarnations remain in process ownership until cleanup completes even if removed from semantic reuse by invalidation.

### Acquire/waiter protocol — PASS after correction

Claim reservation occurs before waiting. The first claimant cannot close the resource while another active waiter remains unaccounted for.

### Acquire/Close shutdown boundary — PASS after correction

Close has an explicit terminal linearization point, cancels pool builders, joins builders/handoffs, and prevents late publication before fail-safe cleanup.

### Builder lifetime — PASS after correction

Pool—not an arbitrary caller—owns physical builder cancellation/join. Caller cancellation is local to its reservation. This aligns with contextless ProcessServices shutdown without creating a permanent worker subsystem.

### Physical identity — PASS

Separate private identity includes/configures treatment for artifact, instance/factory, process model, opaque Configure bytes, normalized policy and secret fingerprint. DTO/input drift is fail-closed.

### Identity evidence realism — PASS after correction

Startup-fixed physical inputs are covered by focused identity/construction tests, not falsely presented as current hot-reload dimensions. High-cardinality reload evidence remains aligned with actual reloadability.

### Candidate last-good isolation — PASS

Reuse hit is query-only for candidate preparation. Candidate rollback only releases its claim and does not mutate/reconfigure/close/invalidate the shared last-good resource.

### Standard Session concurrency — PASS with explicit operational gate

The design preserves the current host Session concurrency implementation. Cross-generation Execute serialization and metadata/auxiliary overlap receive direct tests. No public concurrency ABI is added.

### Request hot path — PASS

Pool operations occur only at generation construction/retirement/process shutdown. Normal request execution uses captured backend functions.

### `shared_artifact` and built-in exclusions — PASS

Neither is pulled into the first implementation. Existing restart-required/shared-process behavior is unchanged.

### Security/ABI — PASS

Verified artifact binding, secure IPC, peer authentication before Configure/secrets, environment restrictions, process-tree cleanup, and current backend-plugin ABI remain unchanged.

### ROI gate — PASS

Deterministic 100-enabled-connector work counts remain the primary justification. Supporting evidence now also records the standard Session cross-generation execution-scheduling tradeoff so the optimization is evaluated as a whole.

## Design-to-Requirement Trace

| Requirement | Design coverage |
|---|---|
| R1 scale evidence | high-cardinality reload count matrix and re-scope gate |
| R2 narrow boundary | discovered overlap-safe per-instance only; pool above processhost |
| R3 identity | complete private key, startup-fixed/reload-varying split, drift gate |
| R4 Acquire/Close | pre-reserved claims, pool-owned builder, linearized Close, no late publish |
| R5 generation semantics | immutable projections, changed/remove/rollback, query-only candidate reuse |
| R6 invalidation | exact incarnation, detached process ownership, fresh replacement |
| R7 cleanup/shutdown | entry-level cleanup once; pool -> host -> artifacts -> staging |
| R8 concurrency/non-interference | preserve Session contract; characterize cross-generation serialization |
| R9 TDD/architecture | scheduling/race/blocked-builder/ownership/identity/ROI gates |

## Simplification Review

The hardened design still rejects:

1. generic resource manager/container;
2. processhost generation awareness;
3. overloading `BackendStateIdentity`;
4. idle TTL cache;
5. public feature/concurrency flags;
6. shared model registry;
7. dynamic plugin reconciliation;
8. Cordis requires/provides graph/fibers;
9. Session concurrency redesign.

The new `owned` entry set, builder cancellation root, and entry cleanup-once are accepted because they close concrete correctness holes introduced by resource sharing; they are not general-purpose framework concepts.

## Implementation Risks to Pin With Tests

- waiter reserved too late and resource reaches zero before handoff;
- invalidated detached entry disappears from process shutdown ownership;
- final release and Pool.Close double-run session/host cleanup;
- Close waits forever for a builder using the wrong lifetime context;
- builder publishes after Close linearizes;
- Acquire hands out a resource after fail-safe cleanup begins;
- stale invalidation detaches a replacement incarnation;
- physical identity omits a Configure/launch input;
- startup-fixed identity dimension is accidentally treated as hot reload without redesign;
- candidate rollback mutates/invalidates last-good shared resource;
- standard Session cross-generation Execute serialization causes hidden deadlock/cancellation regression;
- metadata/Count/Finalize overlap exposes connector race;
- pool begins duplicating processhost or appears in request/public surfaces.

## Final Gate

**GO.** All technically valid CodeRabbit lifecycle/concurrency findings are now represented in normative requirements, design mechanics, and TDD tasks. The identity-matrix suggestion was adopted with a brownfield correction: startup-fixed inputs receive focused identity/construction coverage rather than fictional hot-reload scenarios.

Implementation remains approval-gated by `spec.json`. The implementation must still pass the deterministic scale gate and may be re-scoped if the existing standard Session execution-serialization tradeoff erodes the expected operational ROI.
