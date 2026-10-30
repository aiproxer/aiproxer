# Code review — stage-two fixes and current `go-llm-interactive-proxy` implementation

Reviewed target: public `main` branch snapshot on 2026-04-21.

## Executive summary

Stage two is **real progress**.

The Go rewrite now has:

- a runnable standard binary and HTTP composition layer
- a canonical `lipapi` / `lipsdk` split
- a small-ish executor and routing core
- real frontend and backend plugins
- durable continuity via SQLite
- a much healthier package layout than the Python codebase

The project is **not yet back in Python-style dependency hell**.

However, there are still several issues that can push it back in that direction if more features are added on top of the current shape. The most important risks are:

1. **plugin kind and instance identity are still conflated**
2. **production runtime behavior still falls back to deterministic test defaults**
3. **runtime resource ownership is split and incomplete**
4. **routing-health / observer seams exist but are not actually wired as first-class runtime behavior**
5. **standard-bundle composition still relies on `init()`-driven global registries**

My recommendation is clear:

> **Do not broaden feature scope yet.**
> Stage three should harden identity, runtime ownership, resource lifecycle, and routing truthfulness before adding more product surface.

---

## What improved materially since the previous review

### 1. There is already a real server application

The repo is no longer just contracts and stubs. There is a real standard distribution entrypoint plus HTTP runner and mounted frontends.

Relevant files inspected:

- `cmd/lipstd/main.go`
- `internal/stdhttp/server.go`
- `internal/stdhttp/mount.go`
- `internal/plugins/frontends/openairesponses/handler.go`

This matters because the next phase should not be framed as “finally create a server app”. A server app already exists. The right question is whether the **current server/runtime boundary is architecturally sound enough** to grow on.

### 2. Stage-two bundle composition is meaningfully better than the earlier hardcoded shape

The move toward registry-driven backend/frontend/feature construction is a real improvement. The current composition is much closer to the original rewrite goals than the earlier switchboard-style wiring.

Relevant files inspected:

- `internal/pluginreg/reg.go`
- `internal/pluginreg/init.go`
- `internal/pluginreg/backends_install.go`
- `internal/pluginreg/frontends_install.go`
- `internal/pluginreg/features_install.go`

### 3. Durable continuity actually landed

Stage two added a continuity store selector and a SQLite-backed implementation for durable B2BUA/session continuity and attempt lineage.

Relevant files inspected:

- `internal/core/continuity/store.go`
- `internal/core/continuity/sqlitestore/store.go`

That is a strong step forward and worth keeping.

### 4. The core is still relatively small

From the local snapshot I reviewed, the key files are still within a sane range:

- `internal/core/runtime/executor.go` — 328 LOC
- `internal/core/routing/parser.go` — 206 LOC
- `internal/pluginreg/reg.go` — 127 LOC
- `internal/core/hooks/bus.go` — 147 LOC
- `internal/stdhttp/wire.go` — 61 LOC

That does **not** look like the old Python shape yet. The drift risk is real, but it is still early enough to correct cleanly.

---

## Must-fix findings

### F1 — Critical: backend/plugin **kind** and configured **instance** are still the same field

**Where**

- `internal/core/config/model.go:72-76`
- `internal/core/config/registrations.go:16-38`
- `pkg/lipsdk/registration.go:50-75`
- `internal/stdhttp/wire.go:25-35`

**What is happening**

The current config model only has `PluginConfig.ID`. That single field is doing two incompatible jobs:

- selecting the bundled plugin factory kind (`openai-responses`, `anthropic`, etc.)
- acting as the configured runtime identity for the mounted/backend instance

The validation path then rejects duplicates by `(kind, id)`, and the executor wiring stores backends in a `map[string]runtime.Backend` keyed by `p.ID`.

**Why this is a serious architecture problem**

This blocks one of the core reasons the proxy exists: multiple instances of the same backend flavor.

Examples the architecture must support cleanly:

- `openai-responses` primary account + `openai-responses` fallback account
- multiple Bedrock regions
- multiple ACP agents of the same adapter kind
- multiple OpenAI-compatible endpoints behind the same adapter kind

With the current shape, even if duplicate validation were loosened, `BuildExecutor()` would still overwrite later instances with the same key.

**Why this matters specifically for LIP**

Dynamic routing, failover, load balancing, first-request steering, and B2BUA-like multi-leg behavior all become much less valuable if the routing graph can only distinguish backend *kinds* instead of backend *instances*.

**Required fix direction**

Split identity in the config and SDK:

- `kind` / `factory_id` = bundled adapter kind
- `instance_id` / `name` = configured runtime identity

Route selectors and diagnostics should target `instance_id`, not `kind`.

**Release gate**

No new major feature work should land before this split is implemented.

---

### F2 — Critical: production runtime still inherits deterministic test defaults for clock and randomness

**Where**

- `internal/core/runtime/executor.go:98-133`
- `internal/stdhttp/wire.go:52-59`
- `internal/plugins/frontends/openairesponses/handler.go:85-92`

**What is happening**

`Executor` falls back to:

- a fixed deterministic wall-clock timestamp
- a fixed-seed `math/rand` source

when `Now` and `Rand` are not injected.

`BuildExecutor()` currently does not inject either one.

**Why this is severe**

1. **weighted routing is not genuinely weighted/random in production**
2. attempt timestamps can be synthetic/stable instead of real
3. frontend response timestamps can remain synthetic because `WallClock()` returns `nil` unless `Now` is set

Deterministic behavior is excellent for tests. It is wrong as the default runtime behavior for the standard binary.

**Required fix direction**

- production composition must always inject a real clock and non-deterministic entropy source
- deterministic clocks/RNG must move into tests and dedicated harness wiring only
- add tests that verify:
  - production executor has non-nil clock/RNG defaults
  - deterministic test executor remains easy to build

**Release gate**

This must be corrected before relying on weighted routing/load balancing as a real production feature.

---

### F3 — High: runtime resource ownership is still split and incomplete

**Where**

- `internal/stdhttp/wire.go:36-59`
- `internal/stdhttp/server.go:28-31, 80-95`
- `internal/core/continuity/sqlitestore/store.go:85-90`

**What is happening**

The continuity store is opened in `stdhttp.BuildExecutor()`, returned as `b2bua.Store`, and then used by diagnostics and executor wiring.

The SQLite store has a real `Close()` method, but there is no matching lifecycle/resource-owner path that closes it during shutdown.

Also, `runtime.App` does not actually own all runtime resources; `stdhttp.Run()` still assembles important runtime pieces itself.

**Why this matters**

This splits ownership across multiple packages:

- `runtime.App` owns hook bus + feature lifecycles
- `stdhttp` owns executor construction and store opening
- concrete stores/clients may own closers but are not enrolled into shutdown

This is how “small components” slowly turn into an operationally confusing system.

**Required fix direction**

Move to one resource-owning composition root for the standard bundle:

- build store(s), transports, executor, observer sinks, and server dependencies in one place
- register closers/lifecycles in one owner
- shut down in a defined order

Suggested order:

1. stop accepting HTTP traffic
2. drain / cancel in-flight work
3. stop lifecycles
4. close stores/transports/observers

---

### F4 — High: continuity retention semantics are inconsistent between memory and SQLite

**Where**

- `internal/core/config/model.go:53-63`
- `internal/core/continuity/store.go:14-21, 43-58`
- `internal/core/continuity/sqlitestore/store.go`

**What is happening**

`ContinuityConfig` exposes `ttl` and `max_legs` as continuity-level settings.

But in the current store factory:

- memory store receives TTL / max-legs behavior
- SQLite path only receives `sqlite_path`

The SQLite store therefore ignores the retention-related config fields.

**Why this matters**

The config shape implies store-agnostic continuity retention controls, but the implementation currently provides those semantics only for memory.

That leads to:

- operator confusion
- behavior drift between dev and durable modes
- unbounded durable growth unless separate retention logic is added later

**Required fix direction**

Pick one of these and make it explicit:

1. **Support retention semantics for SQLite too** (preferred), or
2. declare TTL/max-legs memory-only and move them into memory-store-specific config

Right now the interface says one thing and the implementation says another.

---

## Important but slightly lower-priority findings

### F5 — High: routing health and observer seams exist, but the standard bundle does not wire them as real behavior

**Where**

- `internal/core/runtime/executor.go:63-69, 136-155`
- `internal/stdhttp/wire.go:52-59`
- `internal/stdhttp/server.go:50-54`

**What is happening**

`Executor` has fields for:

- `CandidateHealth`
- `RouteObserver`
- `RouteTrace`
- `Log`

But the standard wiring only sets a route trace buffer conditionally; the other routing seams are not actually assembled into the runtime.

**Why this matters**

The current runtime shape can fool you into thinking health-aware routing is “there” because the type surface exists. But the standard distribution still behaves more like a thin placeholder than a fully wired routing-runtime.

**Required fix direction**

Stage three should either:

- fully wire health/circuit/observer behavior, or
- remove/park dead seams until they become real

My recommendation is to wire them properly now.

---

### F6 — Medium: the bundle registry still relies on package-global mutable maps and `init()` registration

**Where**

- `internal/pluginreg/reg.go:25-30, 34-69`
- `internal/pluginreg/init.go:3-6`

**Why it matters**

This is much better than editing switch statements everywhere, but it is still a hidden composition model.

Risks if left as-is:

- harder test isolation
- hidden side effects during imports
- bundle definition spread across `init()` order rather than one explicit table
- future coupling if more registries/resources are added

**Required fix direction**

Replace the implicit `init()` bundle registration with an explicit compile-time bundle definition, for example:

- a `standardbundle` package exporting the full registry/table
- `cmd/lipstd` choosing that bundle explicitly

That keeps the architecture honest and makes test bundles trivial.

---

### F7 — Medium: backend factories still use `http.DefaultClient` for ACP and Bedrock

**Where**

- `internal/pluginreg/backends_install.go:124-147`

**Why it matters**

This makes transport behavior global and hard to tune.

The standard bundle should be able to configure per-instance or shared transport properties such as:

- timeout
- proxy
- connection pooling
- idle limits
- TLS knobs
- user-agent / tracing wrappers

`http.DefaultClient` is fine for experiments, not for a production credibility stage.

**Required fix direction**

Introduce a shared transport/client factory owned by the standard runtime/bundle.

---

### F8 — Medium: request correlation middleware is coupled to diagnostics enablement

**Where**

- `internal/stdhttp/server.go:65-69`

**Why it matters**

Request IDs and trace propagation are useful whether or not admin/diagnostic endpoints are turned on.

Right now the standard bundle only installs those middlewares when diagnostics are enabled.

**Required fix direction**

Install correlation middleware unconditionally.
Let `diagnostics.enabled` control endpoints, not basic request tracing.

---

### F9 — Medium: plugin decoupling is improved, but not yet “complete”

**Where**

- `internal/pluginreg/reg.go:72-99`

**What I mean**

Backends still return `runtime.Backend`, and frontend mounts still target `*http.ServeMux`.

This is acceptable for the **standard distribution bundle**, but it is not yet the end-state promised by the rewrite philosophy.

**Interpretation**

I would not block release on this alone, but I would stop it from growing worse. Stage three should improve the boundary model instead of deepening the coupling.

---

### F10 — Medium: frontend error mapping is still thin and repetitive

**Where**

- `internal/plugins/frontends/openairesponses/handler.go:79-112`

**Why it matters**

A richer shared runtime error taxonomy will reduce duplication across frontends and make cross-API behavior more predictable.

This is a good stage-three candidate after identity/lifecycle/defaults are fixed.

---

## Positive findings worth preserving

### P1 — The executor is still small enough to reason about

At 328 LOC in the reviewed snapshot, `internal/core/runtime/executor.go` is carrying real responsibility without yet becoming the Python-style mega-object.

Preserve that.

### P2 — Routing code is still separated into parser/planner/weighted helpers

That is a healthy sign. Keep routing policy split from executor flow.

### P3 — Durable continuity is a good addition

The SQLite store is absolutely the right next step for a single-binary Go distribution.

### P4 — Hooks are still phase-specific

Separate submit hooks, request part hooks, response part hooks, and tool reactors are healthier than a generic “feature does anything” API.

### P5 — The repo still enforces a QA-oriented workflow

The repo layout and README show attention to quality gates, tests, race checks, linting, and reproducibility. Keep that discipline.

---

## Drift assessment: are we sliding back toward Python-era maintainability problems?

**Not yet.**

But the dangerous pressure is visible.

The current Go code still has a real chance to remain healthy because the main drift is happening at the **composition and identity** level, not yet as giant business-logic files.

That is the best possible time to intervene.

If the project adds more features before fixing:

- instance identity
- production runtime defaults
- resource ownership
- explicit bundle composition

then the rewrite will start recreating the same kind of hidden complexity, just in smaller Go packages.

---

## Required follow-up instructions for the execution agent

Treat the next phase as an **architecture-hardening stage**, not a feature-expansion stage.

### Order of execution

1. **Split plugin kind from configured instance identity**
2. **Move standard-bundle ownership to one explicit assembler/resource owner**
3. **Inject real clock/RNG in production composition**
4. **Finish routing-health / observer wiring**
5. **Fix continuity retention semantics and closers**
6. **Replace `init()`-driven standard bundle registration**
7. **Only then** expand additional user-visible/server capabilities

### Mandatory constraints for the next stage

- no core package may import bundled plugin packages
- no new “god object” bootstrapper may appear
- no route selector or diagnostic surface may continue to target plugin-kind IDs when runtime instance identity exists
- deterministic clocks/RNG are allowed only in tests or explicit harnesses
- every opened resource must have a clearly owned shutdown path
- keep core files under tight size budgets (target <= 400 LOC, justify exceptions explicitly)

### Tests that must be added in the next stage

1. configure two backend instances of the same kind and prove they both exist and are independently routable
2. prove weighted routing is non-deterministic in production wiring and deterministic in test wiring
3. prove SQLite store closes on shutdown
4. prove route-health exclusions affect candidate planning in the standard bundle
5. prove diagnostics and route traces show instance identity, not just plugin kind

---

## Final judgment

Stage two is **good progress** and should not be thrown away.

But stage three must be a **hardening and identity-correction stage**.

That is the fork in the road:

- choose hardening now, and the rewrite stays healthy
- keep adding surface area now, and the project starts rebuilding the same maintenance trap that motivated the rewrite
