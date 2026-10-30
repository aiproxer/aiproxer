# Research notes — Go core reimplementation stage three: runtime hardening and instance identity

Spec name: `go-core-stage-three-runtime-hardening`

**Status:** research informed stage-three hardening, which is now **implementation-complete** and **archived**
(see `spec.json`, `tasks.md` completion summary, and `.kiro/specs/archive/go-core-stage-three-runtime-hardening/`).

## Why this stage exists

The rewrite away from Python was driven by a very specific problem:

- too much coupling
- too much hidden ownership
- too many features layered onto the same central paths
- too much difficulty making safe changes

The current Go repo is still far healthier than the Python codebase it replaces.

But the latest review shows a pattern that must be corrected now:

- identity is still too coarse
- ownership is still too split
- some runtime seams are present in types but not in actual assembly
- some production behavior still relies on deterministic test defaults
- some bundle composition still happens through hidden registration side effects

These are exactly the kinds of small architectural dishonesty that later become “why is this hard to change?” problems.

---

## Why stage three is not a server-creation stage

There is already a standard binary and HTTP server path.

The next correct step is not “build a first server”.
The next correct step is to make the current server/runtime boundary **truthful, owned, and scalable**.

If the project broadens scope first, it risks recreating the Python-era pattern of adding more behavior on top of shaky ownership and identity assumptions.

---

## Main review insights that shaped this stage

### 1. Instance identity is the biggest missing concept

The project’s product value depends heavily on multiple backend instances:

- failover
- weighted balancing
- primary/fallback account patterns
- regional redundancy
- multiple ACP or OpenAI-compatible endpoints

The current `id`-only shape cannot represent that cleanly.

This is not a small config problem.
It is the missing identity model for the entire routing layer.

### 2. Deterministic-by-default runtime behavior is acceptable in tests, not in the standard binary

The review found deterministic clock/RNG fallbacks that are still active unless explicitly overridden by composition.

That means the standard runtime can accidentally behave like a test harness.

This is especially dangerous for weighted routing because the behavior may *look* implemented while being operationally wrong.

### 3. Bundle composition is improved but still too implicit

Registry-driven factories are a real improvement over switchboards, but `init()`-driven global registration is still a hidden composition mechanism.

The stage-three design keeps static linking and explicit bundle assembly, which preserves Go simplicity while avoiding invisible import-time behavior.

### 4. Resource ownership must become explicit before more growth

Durable continuity was a good addition.
But durable resources without a single owner and shutdown path are an early warning sign.

That is why the stage centers runtime ownership and closers.

### 5. Health-aware routing must be real, not just typed

The executor already has sensible seams for health and observation.
The problem is that the standard bundle does not yet fully realize them.

Stage three therefore treats “make typed seams real behavior” as a primary architectural goal.

---

## Why explicit bundle assembly is preferred over native Go plugins

The original rewrite goal was plugin-style extensibility and decoupling, not necessarily runtime binary loading.

Native Go plugin loading is still a poor fit here because:

- platform/tooling support is narrower
- testing and operations are harder
- the problem is primarily about clear boundaries, not hot-loading

Static linking with explicit bundle assembly gives almost all the architectural value with much less operational complexity.

---

## Why instance-aware routing comes before broader protocol work

Additional protocol fidelity, new admin surfaces, and other features are all easier after the architecture can cleanly represent multiple same-kind backend instances.

If the project postpones that split, more code will be written against the wrong assumption and later migrations will become painful.

This is precisely the sort of trap the rewrite is supposed to avoid.

---

## Why the standard bundle should own transports

Backend factories using `http.DefaultClient` are acceptable only as temporary scaffolding.

A real standard distribution needs:

- traceability
- per-instance behavior
- predictable timeouts
- reusable transport pools
- clean shutdown

Shared transport ownership is therefore not a “nice to have”; it is part of making the standard binary a maintainable system rather than a pile of adapters.

---

## Why this stage is the last good moment to intervene cheaply

The codebase still has healthy signs:

- core files are moderate in size
- responsibilities are somewhat separated
- provider SDKs are mostly contained
- package layout is still understandable

That means the architectural correction is still relatively cheap.

If more layers are added before the correction, the same conceptual mistakes will be spread across:

- config
- routing
- diagnostics
- attempt lineage
- transport
- health policy
- server middleware

At that point the rewrite will start accumulating “small Go versions” of the old Python problem.

---

## Stage-three thesis

Stage three is successful if, after it lands, the answer to these questions is “yes”:

1. Can I configure two instances of the same adapter kind without fighting the architecture?
2. Can I see which specific instance a request hit in logs and diagnostics?
3. Can I trust the standard binary to use real time and real routing randomness?
4. Do I know exactly who owns and closes durable runtime resources?
5. Can I understand the standard bundle without hunting `init()` side effects?

If those answers are yes, the rewrite is staying on the right path.
If not, the codebase is drifting toward the same maintenance gravity that forced the rewrite in the first place.
