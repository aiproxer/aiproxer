# Cordis v4 Discipline for AIProxer

## Contents

- §§1–5: existing authorities, acquisition, irreversible effects, dependencies and withdrawal
- §§6–8: semantic identity, reuse ROI and immutable generations
- §§9–12: owned background work, waiter claims, close and fail-closed lifecycle changes
- §13: connector scale
- §§14–16: spec workflow, requirements and design validation
- §§17–19: implementation workflow, review and anti-patterns
- §§20–23: terminology, decision rubric, repository authority and source interpretation

This skill adapts selected Cordis v4 principles to AIProxer. It is **not** a request to make AIProxer Cordis-compliant or to reproduce Cordis's JavaScript runtime model.

The governing rule is:

> **Use a Cordis-derived mechanism only when it makes an existing AIProxer ownership, lifetime, dependency, or reconciliation problem materially simpler or safer. Preserve established AIProxer lifetime authorities instead of replacing them with a generic runtime.**

AIProxer already has strong domain-specific lifecycle machinery. Treat that as the starting point, not as legacy to replace.

## Modes

- **Architecture / spec mode** — requirements, gap analysis, design, design validation, architecture review, Kiro SDD generation.
- **Executor mode** — implementing an approved design, writing RED tests, changing lifecycle/resource code, adding architecture ratchets, validating concurrency and cleanup.
- **Review mode** — reviewing landed code against an approved spec and these rules. Findings must identify a concrete violated invariant or unjustified complexity, not merely a different stylistic preference.

---

# 1. AIProxer Authorities Are Primary

Before proposing any Cordis-derived idea, identify the existing authority for the lifetime or behavior.

| Lifetime / concern | Existing AIProxer authority | Default rule |
| --- | --- | --- |
| Process lifetime | `ProcessServices` / process composition | Long-lived process resources close here unless a narrower established owner exists. |
| Runtime generation | immutable `GenerationRuntime` / `GenerationBundle` | Do not live-mutate a published generation. |
| Generation resources | `ResourceLedger` | Generation phase cleanup stays here; do not create a second generation cleanup engine. |
| Generation publication / retirement | `runtimehost.Manager` | Preserve publish, pin, retain, quiesce, drain, close ordering. |
| External connector process supervision | `processhost.Host` | Launch/IPC/auth/process-tree/reap remain here. |
| Request / attempt / turn state | existing request/B2BUA/runtime owners | Prefer lexical/request-scoped ownership over process registries. |
| Canonical API | `pkg/aipapi` | Provider-neutral; do not leak connector/provider lifecycle concepts here. |
| Plugin SDK / executable ABI | `pkg/aipsdk` / `backendplugin` | Change only for a real public plugin capability, not to expose internal reconciliation. |
| Routing / failover / B2BUA | `internal/core` | Never move routing authority into a generic dependency/effect graph. |

**Rule:** If a proposal creates a new authority parallel to one of these, assume the proposal is wrong until it proves that the existing authority cannot express the required invariant.

---

# 2. Temporal Composability → Explicit Acquire + Inverse Ownership

Cordis's useful temporal idea for AIProxer is not "make everything reversible." It is:

> **When acquiring a long-lived resource, establish the matching release ownership before the resource escapes the construction boundary.**

## Apply this to

- files, sockets, DB handles, durable stores, process handles;
- connector sessions/process resources;
- background loops with cancellation and join obligations;
- process-scoped services;
- generation-scoped resources;
- any resource whose leak can outlive the lexical function that created it.

## Rules

1. A successful owned acquisition MUST establish cleanup ownership before returning the resource to broader code.
2. A failed acquisition MUST NOT register cleanup for a resource that was never successfully acquired.
3. Partial construction MUST unwind already-owned resources in reverse dependency/acquisition order.
4. Cleanup MUST attempt all owned releases and aggregate meaningful errors where the existing owner does so.
5. Physical cleanup MUST have exactly one logical owner. Do not copy the same physical closer into multiple ledgers/owners.
6. Ordinary short lexical resources SHOULD remain ordinary `defer Close()` resources. Do not centralize them merely for uniformity.
7. Do not introduce a generic `Effect`, `ResourceManager`, `Scope`, or container abstraction unless several concrete AIProxer lifetimes demonstrably need the same semantics and the abstraction deletes more complexity than it adds.

## Executor test gate

For each new owned acquisition, test at least the relevant cases:

- success + normal shutdown;
- acquisition failure;
- later construction failure;
- repeated/idempotent cleanup where required;
- cleanup error aggregation where applicable;
- ordering relative to dependencies;
- race with shutdown when concurrency exists.

---

# 3. Reversibility Has a Hard Boundary: External Emissions

Cordis-style inverse effects are appropriate for **host-owned internal resources and state**, not for pretending external world effects can be undone.

The following are generally irreversible in AIProxer:

- provider calls that may consume quota or money;
- tokens already generated upstream;
- client-visible output already emitted;
- externally persisted financial/accounting records except through explicit compensating domain operations;
- external tool or agent actions.

Therefore:

1. Do NOT design an "undo provider call" or "revert emitted output" abstraction.
2. Preserve AIProxer's no-retry/failover-after-first-downstream-content rule.
3. Treat output commit as an irreversibility boundary.
4. Use idempotency, durable evidence, compensation, reconciliation, or terminal state machines for external side effects—not fake inverse functions.
5. A spec MUST distinguish **reversible host effects** from **irreversible external effects** whenever both exist.

---

# 4. Spatial Composability → Explicit Dependencies, Not a DI Runtime

Cordis allows components to activate only when required providers exist. In AIProxer, prefer explicit construction and immutable generation composition over a runtime `requires/provides` graph.

## Adapted rule

> **A component may become usable only after all of its required dependencies/capabilities are validated, but dependency satisfaction should normally happen through typed construction, registration, config compilation, or generation publication—not service lookup.**

## Required behavior

- Missing mandatory capability: fail explicitly before serving or before the affected operation.
- Optional capability: represent it explicitly as optional and preserve safe fallback/no-op semantics.
- Provider-specific semantics: remain in adapters/connectors.
- Core must not infer provider capability from provider names when a typed capability/profile exists.
- Do not add `Get`, `Resolve`, service locator, reflection registration, mutable globals, or runtime dependency lookup to emulate Cordis context injection.

## Prefer, in order

1. constructor arguments / explicit composition;
2. typed immutable config or compiled views;
3. existing plugin registry/profile contracts;
4. generation-local capability projection;
5. only then a narrowly scoped private reconciliation owner if expensive resource identity must survive overlapping generations.

---

# 5. Dependency Withdrawal → Quiesce Dependents Before Providers

The Cordis withdrawal principle maps well to AIProxer lifecycle ordering:

> **A provider must remain available until dependents have stopped creating new work and finished teardown that needs that provider.**

Use the repository's existing phases instead of inventing generic dependency withdrawal.

Typical ordering:

```text
stop admission / mark retiring
        ↓
quiesce generation-owned background work
        ↓
drain retained request / async leases
        ↓
close generation resources
        ↓
close process-scoped resource/reconciliation owners
        ↓
close lower-level process supervisors / transports
```

Examples:

- model-registry refresh must cancel/join before model catalog close;
- generation leases on pooled connector resources release before process pool close;
- backend resource pool closes before `processhost.Host`, because residual resource cleanup may call the host;
- verified executable artifacts close before staging directory deletion on platforms where executable handles keep files locked.

## Spec requirement

For any lifecycle-sensitive design, include an explicit dependency/teardown table:

| Resource | Depends on | Owner | Quiesce point | Final close point | Required relative order |
| --- | --- | --- | --- | --- | --- |

If ordering is not testable, the design is incomplete.

---

# 6. Identity Is Semantic, Not Object Identity

Cordis's provider identity idea is useful where an expensive resource can survive composition changes.

## Rule

> **Reuse only when a complete semantic identity proves two consumers want the same physical resource. Keep semantic identity separate from concrete physical incarnation.**

Use this only for resources expensive enough to justify reconciliation.

### Identity requirements

- Include every effective construction/configuration input that can change resource behavior.
- Prefer one explicit identity choke point near physical construction.
- Secret material must be represented by a private non-reversible fingerprint, never plaintext diagnostics.
- Artifact/binary identity must distinguish executable upgrades.
- If identity completeness cannot be proven, **fail closed to isolated construction**, not unsafe reuse.
- A future construction-input field must force an identity decision through tests/ratchets.

### Incarnation requirements

A semantic identity can have multiple physical incarnations over time:

```text
semantic key K
  ├─ incarnation 17 (old / detached / still leased)
  └─ incarnation 18 (current / reusable)
```

A stale invalidation for incarnation 17 must never detach incarnation 18.

---

# 7. Reconciliation Is Allowed Only at Stable, High-ROI Boundaries

Do not build a generic reconciliation engine because Cordis has one.

An AIProxer reconciliation layer is justified only when all are true:

1. the resource is meaningfully expensive or stateful;
2. overlapping immutable generations currently duplicate/reconstruct it;
3. semantic equality can be defined safely;
4. reuse does not live-mutate published generations;
5. existing owners can retain cleanup authority cleanly;
6. deterministic tests can prove fewer expensive operations;
7. the new mechanism remains outside the request hot path;
8. the implementation is smaller than the maintenance cost it removes at expected scale.

### Good example

Configured external `per_instance` connector resources reused across overlapping generations using:

```text
semantic physical identity
        ↓
process-owned current incarnation
        ↓
per-generation lease claims
        ↓
final claim / process shutdown
        ↓
exactly-once physical cleanup
```

### Poor examples

- generic component graph for all runtime services;
- pooling cheap in-process builtins merely for consistency;
- dynamic provider lookup in request execution;
- moving routing/model/policy projections into a shared mutable pool;
- using one reconciliation runtime for connectors, billing, HTTP middleware, and request state.

---

# 8. Immutable Generations Are AIProxer's Main Spatial Boundary

Do not replace immutable generations with live component recomposition.

When configuration changes:

1. construct/validate a candidate generation;
2. reuse only explicitly safe process-owned resources through leases/immutable handles;
3. rebuild generation-local projections;
4. publish atomically;
5. old requests stay pinned to the old generation;
6. old generation quiesces/drains/closes independently.

A published generation MUST NOT be live-rebound to a replacement backend, policy, route, model view, or feature bundle merely because a newer provider/resource incarnation appeared.

This is a deliberate AIProxer adaptation of Cordis: **coarse immutable reconciliation beats fine-grained reactive mutation for the request plane.**

---

# 9. Structured Background Work Is an Owned Effect

Every long-lived goroutine must have:

- one clear owner;
- one start point;
- one cancellation source;
- one termination condition;
- one join/wait responsibility;
- bounded queues/concurrency where relevant;
- no application work before ownership is established.

## Start-order rule

If registering cleanup can synchronously invoke cleanup (for example because an owner is already quiescing/closed), a new worker must not deadlock waiting for a start gate that cleanup waits to join.

Use a cancellation-aware start barrier or equivalent proof.

## Pool/build rule

For shared resource construction:

- builder lifetime belongs to the pool/process owner, not one Acquire caller;
- caller cancellation releases only that caller's reserved claim;
- shutdown cancels and joins admitted builders;
- no builder may publish a resource after terminal close linearizes;
- external cleanup never runs while holding the pool mutex.

---

# 10. Reference Counting Requires Scheduling-Safe Claims

A naive `singleflight + refcount after wake` design is unsafe.

If multiple consumers wait for one build, each prospective consumer must reserve ownership before the first successful consumer can release the resource to zero, or an equivalent handoff barrier must exist.

Required schedule to test:

```text
caller A starts build
caller B joins / waits
build completes
caller A receives lease
caller A releases immediately
caller B has not run yet
```

The resource MUST remain alive for B if B has not canceled.

Also test:

- waiter cancellation;
- final release racing new Acquire;
- final release racing owner Close;
- invalidation while old leases exist;
- stale invalidation racing replacement publication.

---

# 11. Close Is a Linearization Boundary

For process-owned shared resources, define exactly when shutdown becomes terminal.

After `Close` linearizes:

- no new claims are admitted;
- no pending build may be published/handed off as a success;
- builder cancellation is triggered;
- admitted builders and acquisition handoffs are joined;
- residual owned resources are cleaned exactly once;
- repeated Close calls observe the same completion/result.

Do not rely on comments such as "Close will eventually clean everything" without an enumerable ownership set or equivalent proof.

---

# 12. Fail Closed on New Lifecycle Semantics

A reusable resource is safe only while its lifecycle contract remains compatible with sharing.

If a pooled external backend later gains a meaningful generation-local:

- `Start`;
- `Stop`;
- `Close`;
- `CleanupIdleTransports`;
- mutating `PreflightCapability`;
- or another mutating preparation hook,

then do **not** silently strip or ignore that behavior. The path must become non-shareable until the lifecycle semantics are explicitly redesigned and proven safe.

Architecture tests SHOULD freeze the current standard adapter's pool-compatible lifecycle shape so future additions force review.

---

# 13. Connector Scale: Prefer Contracts and Data Over Cartesian Products

AIProxer may grow from tens to hundreds or more backend connectors. Cordis-inspired modularity does not mean more central runtime abstraction.

For connector scalability prefer:

- manifest-driven discovery;
- generic executable connector registration;
- protocol-family adapters;
- provider-profile data where compatibility is declarative;
- connector SDK/scaffolding;
- contract TCKs and capability tests;
- semantic identity/reconciliation for expensive physical resources;
- architecture tests that prevent provider-specific switches in core.

Reject:

- frontend × backend compatibility matrices as the primary conformance model;
- core `switch providerName` logic;
- one custom core backend implementation for every compatible vendor;
- connector-specific lifecycle logic in the reconciliation pool.

---

# 14. Architecture / Spec Agent Workflow

When creating or validating a Kiro architecture spec involving lifecycle, reload, resources, connectors, or dynamic composition, perform these steps in order.

## Step A — Brownfield authority map

Identify current owners before proposing new types:

```text
resource / state
current constructor
current owner
cleanup path
quiesce path
identity
who can mutate it
who can observe it
```

Do not propose a new lifecycle abstraction until this map exists.

## Step B — Effect inventory

Classify every important effect:

- lexical reversible resource;
- process-owned reversible resource;
- generation-owned reversible resource;
- shared resource with lease lifetime;
- durable/idempotent external effect;
- irreversible external emission.

Do not describe irreversible effects as reversible.

## Step C — Dependency and withdrawal graph

For each long-lived resource, identify:

- what it requires to start;
- what may still call it during quiesce;
- what must stop before it closes;
- which lower-level resources must remain alive during its cleanup.

Specify the exact teardown order.

## Step D — Identity analysis

If reuse is proposed, enumerate every physical construction/configure input.

Ask:

- Can semantically equal resources be identified deterministically?
- Can secrets be fingerprinted privately?
- Can binary/artifact upgrades be distinguished?
- Does the same semantic identity need multiple incarnations after failure?
- What causes fail-closed non-shareability?

If these cannot be answered, do not pool/reconcile the resource.

## Step E — Failure schedule analysis

Write explicit schedules for concurrency-sensitive cases. At minimum consider:

- acquisition failure after earlier resources succeeded;
- candidate rollback;
- shutdown during construction;
- waiter cancellation;
- final release vs Acquire;
- final release vs Close;
- invalidation vs replacement;
- cleanup failure;
- generation quiesce while async work is running.

A design that only describes the happy path is not ready.

## Step F — ROI / evidence gate

For a Cordis-derived refactor, requirements MUST include measurable evidence of the problem and target improvement.

Prefer deterministic operation counts over fragile timing thresholds.

Examples:

- physical activations before/after reload;
- Configure calls;
- number of process/goroutine owners;
- cleanup paths deleted;
- duplicate state maps removed;
- number of locations requiring backend additions;
- architecture-line or change-surface reduction where meaningful.

If the measured problem is negligible, choose **no refactor**.

## Step G — Alternatives

Every architecture spec SHOULD explicitly evaluate:

1. no change;
2. smallest local hardening;
3. narrow Cordis-derived mechanism;
4. generic Cordis/runtime/container-style approach.

The generic approach should win only with extraordinary evidence.

## Step H — Simplification gate

Before design GO, ask:

- Did we create a second owner?
- Did we add a generic registry/container?
- Did we move domain semantics into infrastructure?
- Did we add request-path lookup/locking?
- Did we introduce more lifecycle concepts than we deleted?
- Is an existing owner sufficient with a smaller API change?

If yes, simplify before tasks are generated.

---

# 15. Requirements Rules for Cordis-Derived Specs

Requirements should state **observable invariants**, not prematurely freeze helper names.

Prefer:

> When two overlapping generations request the same complete physical connector identity, the system shall perform zero additional physical activation/configuration work and each generation shall own an independent lease release.

Avoid:

> Implement `ResourcePool.Acquire(key)` with `Entry.refs`.

Implementation shape belongs in design unless a concrete API is itself the compatibility contract.

Every relevant spec SHOULD include requirements for:

- existing-authority preservation;
- cleanup ownership;
- reverse/phase teardown ordering;
- candidate rollback;
- fail-closed identity or capability handling;
- shutdown linearization;
- concurrency schedules;
- no request-hot-path regression;
- no public API/ABI/config expansion unless required;
- architecture anti-regression gates;
- deterministic ROI evidence;
- final simplification review.

---

# 16. Design Validation Rules

For brownfield design validation, issue **NO-GO** if any of these remain unresolved:

1. two owners can physically close the same resource;
2. a resource can escape before cleanup ownership is established;
3. shutdown cannot enumerate or join all owned in-flight work;
4. a stale invalidation can affect a newer incarnation;
5. a candidate can mutate/close last-good shared state;
6. new public/global/service-locator machinery is required only for wiring convenience;
7. published generations would be live-mutated;
8. external irreversible effects are modeled as rollback-able;
9. cleanup order depends on luck rather than a tested owner contract;
10. performance/scale benefit is asserted without a measurable gate;
11. the design ignores a current cross-feature lifecycle owner;
12. a future lifecycle/capability change could be silently ignored rather than fail closed.

A design can be GO with known operational trade-offs only when those trade-offs are explicitly characterized and bounded.

---

# 17. Executor Agent Workflow

When implementing a Cordis-derived approved spec:

## Phase 1 — Characterize before implementation

Write RED tests for the failure schedules and scale problem first.

Do not start by implementing a generic helper.

## Phase 2 — Add the smallest private primitive

Examples of acceptable private primitives:

- one append-only ownership facade over an existing closer stack;
- one ledger-backed cancel+join helper;
- one connector-specific semantic identity type;
- one connector-specific process-owned lease pool.

The primitive must remain package-private unless the approved spec explicitly changes a public contract.

## Phase 3 — Integrate at one choke point

Prefer one construction/configuration seam rather than scattered call-site checks.

For connector reconciliation, the expected shape is:

```text
factory / construction input
        ↓
complete identity
        ↓
eligibility / fail-closed fallback
        ↓
private pool Acquire
        ↓
existing physical builder / processhost
```

Do not teach `processhost` about config-generation semantics unless a separate design proves that responsibility belongs there.

## Phase 4 — Preserve existing owners

- `ResourceLedger` owns generation lease release.
- `ProcessServices` owns process shutdown.
- `runtimehost.Manager` owns generation retirement.
- `processhost.Host` owns external process supervision.
- connector/adapters own provider semantics.

Do not introduce shadow owners.

## Phase 5 — Ratchet the architecture

Extend existing generic architecture rules for demonstrated violations below.
New cross-feature rules belong in their own slice under test proportionality;
avoid feature-specific scanners/ratchets. Cover applicable cases that would:

- export the private mechanism;
- add generic service/resource lookup;
- move reconciliation into request execution;
- pool builtins/shared-artifact paths unintentionally;
- bypass lease cleanup;
- add lifecycle semantics to a pooled adapter without an explicit compatibility decision;
- omit a new physical identity input;
- introduce provider-specific switches into generic infrastructure.

## Phase 6 — Concurrency verification

Use deterministic scheduling barriers where possible, then goleak/fuzz/stress as appropriate.

Never run race checks locally. Race coverage comes from scheduled remote scans under root `AGENTS.md` and is not a completion gate.

## Phase 7 — Final simplification review

Before declaring complete:

- delete superseded cleanup/registration plumbing;
- remove unused helper layers;
- ensure there is one owner per physical resource;
- ensure no new generic framework emerged;
- compare production concepts added vs old concepts deleted;
- verify the measured ROI target;
- run the repository quality/architecture gates appropriate to the change.

---

# 18. Code Review Checklist

When reviewing an implementation against this skill, inspect in this order:

1. **Owner correctness** — who closes each physical resource?
2. **Escape timing** — can it escape before ownership is registered?
3. **Rollback** — does partial failure unwind correctly?
4. **Withdrawal order** — do dependents quiesce before providers close?
5. **Identity** — can distinct physical resources alias?
6. **Incarnation safety** — can stale callbacks poison replacements?
7. **Waiter/refcount schedule** — is ownership reserved before handoff races?
8. **Close linearization** — can late construction publish after shutdown?
9. **Exactly-once physical cleanup** — lease idempotency alone is not enough.
10. **Mutex discipline** — no external cleanup/I/O under central lifecycle locks.
11. **Generation immutability** — no live replacement under published requests.
12. **External-effect boundary** — no retry/rollback fiction after irreversible output/effects.
13. **Cross-feature ownership** — model refresh, billing workers, keep-warm, terminal work, etc. must still quiesce in the correct lifetime.
14. **Scope** — no DI container/reactive graph/generic resource runtime.
15. **Evidence** — required operation-count, race, architecture and regression gates actually ran.

Report findings by severity and concrete violated invariant. Do not demand a refactor solely because code is not expressed in Cordis terminology.

---

# 19. Explicit Anti-Patterns

Reject these unless a separately approved spec provides strong evidence:

- `Context.Get("service")`, `Resolve[T]`, global provider maps, service locators;
- reflection-based dependency injection;
- generic effect/inverse registries for all side effects;
- replacing `ResourceLedger` with a Cordis-like effect engine;
- replacing immutable generations with live component activation/deactivation;
- moving `processhost` supervision into a reconciliation pool;
- pooling resources just because their constructors look similar;
- one lifecycle framework for process, generation, request, connector, and durable financial lifetimes;
- treating provider calls/client output as reversible;
- central provider-name capability tables when contract/profile data can express the fact;
- broad "Cordis compliance" refactors;
- generic worker/scheduler abstractions created only to host one cancel+join loop;
- hiding a newly introduced lifecycle hook by stripping/ignoring it rather than failing closed.

---

# 20. AIProxer Cordis Mapping Cheat Sheet

| Cordis v4 concept | AIProxer adaptation | Do not infer |
| --- | --- | --- |
| Revertible effect | acquire + owned cleanup; `ResourceLedger` phase cleanup | external calls/output can be undone |
| Effect rollback chain | reverse owner/ledger teardown | one global effect stack for all lifetimes |
| Reactive coeffect / dependency requirement | typed constructor/config/capability validation | service locator / runtime DI graph |
| Component activation | candidate compile + validation + publication | fine-grained live request-plane activation |
| Component withdrawal | quiesce → drain → close | immediate provider removal under dependents |
| Provider identity | complete semantic physical resource identity | Go pointer/object identity is sufficient |
| Provider incarnation | concrete connector/session/process incarnation | semantic identity permanently maps to one process |
| Reconciliation | safe reuse/replacement at an expensive stable boundary | general mutable runtime reconciliation |
| Fiber/lifetime | process/generation/request owners and leases | one universal scope type |
| Dynamic recomposition | immutable generation replacement | mutate the published generation in place |

---

# 21. Decision Rubric: Should Cordis Inspire This Change?

Score the proposal before designing it.

### Strong positive signals

- recurring lifecycle leaks or duplicated cleanup ownership;
- expensive resources reconstructed across overlapping generations;
- complex manual rollback/closer propagation;
- dependency withdrawal races;
- hundreds of connector instances make O(N) physical churn operationally material;
- semantic identity can eliminate repeated expensive work;
- existing owner can host the rule without a new framework;
- deterministic tests can prove the benefit.

### Strong negative signals

- resource is cheap and short-lived;
- only one or two static implementations exist;
- problem is ordinary lexical cleanup;
- request hot path would gain lookup/locking;
- provider semantics would move into core;
- design requires generic `requires/provides` runtime machinery;
- benefit depends on hypothetical future scale with no current evidence;
- immutable generation replacement already solves the problem cleanly;
- external effect cannot actually be reversed.

### Outcome

- **GO** — strong measurable lifecycle/scale benefit with a narrow private mechanism.
- **GO WITH MEASUREMENT GATE** — plausible high ROI at expected scale, but implementation must first characterize the cost.
- **NO-GO** — abstraction cost exceeds demonstrated simplification or conflicts with AIProxer authorities.

---

# 22. Repository Execution Rules Still Apply

This skill supplements, never overrides, repository instructions.

Root `AGENTS.md` and `.kiro/steering/` own execution, budgets and verification;
consult `.kiro/AGENTS.md` only for opt-in spec work. Those authorities win when
this reference differs. Command policy is in `docs/development-iteration.md`.

---

# 23. Source Basis and Interpretation Boundary

This skill is an engineering adaptation of Cordis v4's spatiotemporal-composability ideas—especially reversible effects, dependency-aware activation/withdrawal, identity, and reconciliation—to AIProxer's established Go architecture.

It intentionally does **not** import Cordis's full runtime architecture. The adaptation is based on these AIProxer conclusions:

- immutable generations are the main request-plane consistency boundary;
- `ResourceLedger` already supplies generation-scoped reverse/phase cleanup;
- `ProcessServices` already supplies process-scoped cleanup coordination;
- `runtimehost.Manager` already supplies generation retention/retirement;
- `processhost.Host` already supplies executable connector process supervision;
- connector-scale semantic identity + leases are useful only for expensive reusable physical resources;
- external emissions require domain-specific idempotency/commit/reconciliation rather than generalized rollback.

Use Cordis as a **reasoning discipline**, not as an implementation target.
