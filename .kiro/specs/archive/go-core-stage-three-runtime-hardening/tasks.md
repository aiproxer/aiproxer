# Tasks — Go core reimplementation stage three: runtime hardening and instance identity

Spec name: `go-core-stage-three-runtime-hardening`

Regression checklist (F1–F10): [`stage2_regression_checklist.md`](stage2_regression_checklist.md).

## Spec status

**Implementation complete; spec archived** under `.kiro/specs/archive/go-core-stage-three-runtime-hardening/`.
`spec.json` uses `phase: "implementation-complete"` and `ready_for_implementation: false`.

Legend (historical task markers below):

- `[x]` delivered
- `[~]` was partially complete during the spec (none remain open for this stage)
- `[ ]` only **optional future work** explicitly called out (e.g. SQLite pruning); not required to close this spec

---

## Phase 0 — freeze the stage-three boundary

### Status

`[x]` complete

### Remaining tasks

- `[x]` record an ADR explaining why stage three is hardening-first rather than new-surface-first
- `[x]` define explicit complexity budgets for core/runtime/composition packages and encode them in docs or checks
- `[x]` list the stage-two review findings as a durable regression checklist linked from this spec
- `[x]` connect the checklist to concrete tests/guardrails added in later phases

### Remaining deliverables

- architecture guardrails doc or ADR addendum with file-size / complexity budgets
- regression checklist linked from this spec

### Remaining acceptance criteria

- the team has a durable written reason for not expanding surface area yet
- every must-fix stage-two review item is listed as a regression target and mapped to code/tests

---

## Phase 1 — split adapter kind from configured instance identity

### Status

`[x]` complete

### Completed tasks

- `[x]` redesign `config.PluginConfig` into instance-aware shape
- `[x]` redesign `lipsdk.Registration` to carry factory kind and instance identity separately
- `[x]` update validation to reject duplicate instance identities, not duplicate factory kinds
- `[x]` update config loader/validation with a deterministic migration path for old `id`-only rows
- `[x]` update diagnostics inventory output to expose both instance and factory kind

### Completed acceptance criteria

- `[x]` two backend instances of the same adapter kind can coexist
- `[x]` duplicate instance identities are rejected
- `[x]` old single-instance configs migrate deterministically
- `[x]` ambiguous configs fail clearly

---

## Phase 2 — make route selectors and executor instance-aware

### Status

`[x]` complete

### Completed tasks

- `[x]` update route resolution to target backend instance IDs
- `[x]` update default route semantics and examples
- `[x]` update diagnostics/attempt records to store/report backend instance identity
- `[x]` keep reporting the adapter kind for operator clarity
- `[x]` add tests for failover and weighted selectors across same-kind backend instances

### Completed acceptance criteria

- `[x]` routing can target two same-kind backend instances independently
- `[x]` weighted routing works across same-kind instances
- `[x]` failover chains preserve instance identity in diagnostics

---

## Phase 3 — replace implicit bundle registration with explicit bundle assembly

### Status

`[x]` complete

### Completed tasks

- `[x]` remove `init()`-driven registration from the standard bundle path
- `[x]` make the standard binary and tests call `pluginreg.InstallStandardBundleOn(reg, ...)` on an owned `*pluginreg.Registry` (no `init()` registration)

### Remaining tasks

- `[x]` decide whether the design should still require a dedicated `internal/standardbundle` package, or whether `internal/pluginreg` + `internal/infra/runtimebundle` is the accepted explicit assembly surface — **accepted: no separate implementation package; `pluginreg/standardbundle` documents only**
- `[x]` replace remaining package-global registry maps with an explicit value-style registry/bundle object if we still want bundle composition to be visible in one place without process-global mutation — **`pluginreg.Registry` + `Default`; tests inject via `BuildOptions.PluginRegistry`**
- `[x]` make tests able to assemble partial/minimal bundles without mutating global package state — **`InstallStandardBackendsOn` + `minimal_registry_test`**
- `[x]` keep static linking; do not add dynamic plugin loading

### Remaining deliverables

- explicit bundle assembly/value registry design decision
- no required process-global mutable registry state for minimal bundle assembly
- minimal/partial bundle assembly tests

### Remaining acceptance criteria

- `[x]` standard binary starts without import-side-effect registration
- `[x]` minimal test bundles can be assembled deterministically without mutating global package state
- `[x]` bundle composition is represented as an explicit value-owned registry (`pluginreg.Registry`) with a documented default

---

## Phase 4 — create one runtime owner for all resources

### Status

`[x]` complete

### Completed tasks

- `[x]` add a runtime-owning assembly type in `internal/infra/runtimebundle`
- `[x]` move executor/store construction out of `stdhttp.Run`
- `[x]` add closer/lifecycle enrollment for durable stores and future resource owners
- `[x]` define deterministic startup and shutdown order in the standard server path
- `[x]` update the standard server to use the assembled owner rather than constructing hidden runtime resources itself

### Completed acceptance criteria

- `[x]` SQLite store closes on shutdown
- `[x]` HTTP server does not construct executor/store behind the back of the owner
- `[x]` resource ownership is clear from one entrypoint (`runtimebundle.Build` + `RunWithRuntime`)

---

## Phase 5 — inject real production defaults for clock, entropy, and transport

### Status

`[x]` complete

### Completed tasks

- `[x]` introduce runtime environment injection points for clock and RNG via `runtimebundle.BuildOptions`
- `[x]` make the standard bundle inject real clock and non-deterministic RNG
- `[x]` preserve deterministic helpers for tests
- `[x]` introduce shared HTTP transport/client factory for standard bundle backends
- `[x]` remove `http.DefaultClient` usage from bundle backend factories

### Completed acceptance criteria

- `[x]` standard binary uses real timestamps
- `[x]` weighted routing is not fixed-seed deterministic in standard wiring
- `[x]` tests can still pin clock and RNG deterministically
- `[x]` ACP/Bedrock do not use `http.DefaultClient` directly in bundle factories

---

## Phase 6 — make routing health and observation real

### Status

`[x]` complete

Config-enabled circuit-breaker health and route observation are real in the standard bundle. Policy and recovery are documented; no separate breaker admin JSON in this stage.

### Completed tasks

- `[x]` implement the standard candidate-health source as a config-enabled built-in circuit breaker
- `[x]` implement route observation in the assembled runtime (structured `lip.route` logging when logger is present)
- `[x]` wire route health/observer into the assembled runtime
- `[x]` add tests proving unhealthy candidates are skipped in standard runtime wiring

### Remaining tasks

- `[x]` decide whether swallowed pre-output failures should count toward circuit-breaker opening, or whether only surfaced failures should trip the breaker — **both surfaced and swallowed count** (see `docs/routing-health-circuit-breaker.md`)
- `[x]` decide whether the standard breaker needs half-open probing / gradual recovery instead of pure time-based reopen — **deferred; time-based cooldown only**
- `[x]` decide whether candidate-health state needs a first-class diagnostics/admin surface beyond logs and route traces — **deferred; logs + route traces**

### Remaining deliverables

- circuit-breaker semantics note (what counts as failure, how recovery works)
- optional health diagnostics surface if that policy is accepted — **not added in this stage**

### Remaining acceptance criteria

- `[x]` unhealthy candidates are excluded in standard runtime wiring
- `[x]` route observation is visible and correlated by request/trace ID when logging is enabled
- `[x]` route traces refer to backend instance IDs through candidate keys
- `[x]` final breaker semantics are explicitly documented and tested

---

## Phase 7 — align continuity retention semantics

### Status

`[x]` complete for the chosen store-specific design

The repo now chooses store-specific retention semantics: memory supports `ttl` / `max_legs`; SQLite rejects those fields until durable pruning exists.

### Completed tasks

- `[x]` decide store-agnostic vs store-specific retention semantics
- `[x]` redesign config/validation to make store-specific retention explicit
- `[x]` ensure durable continuity behavior is documented and tested
- `[x]` keep cleanup lifecycle/closer behavior wired for SQLite ownership

### Optional future work (not required for this stage)

- `[ ]` if durable retention becomes a product requirement, implement SQLite pruning as a later spec rather than reopening this hardening stage

### Completed acceptance criteria

- `[x]` continuity config no longer implies silently ignored behavior
- `[x]` durable store growth policy is explicit
- `[x]` operators can reason about retention in both memory and SQLite modes

---

## Phase 8 — normalize runtime error taxonomy and request correlation

### Status

`[x]` complete for current scope

Always-on request correlation is done, and bundled frontends share a common execute-error classifier. Richer shared kinds are deferred (see `docs/execerr-classification.md`).

### Completed tasks

- `[x]` update bundled frontends to map shared execute-error kinds consistently through `internal/plugins/frontends/execerr`
- `[x]` install request-correlation middleware unconditionally in standard server
- `[x]` ensure logs, route observers, and diagnostics share trace/request identity in the standard path

### Remaining tasks

- `[x]` decide whether to add richer shared runtime error classification beyond `reject` vs `internal` — **deferred** (documented in `docs/execerr-classification.md`)
- `[x]` if richer kinds are added, keep protocol-specific wire strings inside frontend adapters — **N/A until kinds are added**
- `[x]` add focused regression tests for any new shared error kinds before expanding the classifier — **N/A until kinds are added**

### Remaining deliverables

- optional richer execute-error classification design note — **`docs/execerr-classification.md`**
- additional shared-classifier tests if new kinds are accepted — **none in this stage**

### Remaining acceptance criteria

- `[x]` common runtime execute failures now produce smaller, shared frontend classification paths
- `[x]` request IDs/traces exist even when diagnostics endpoints are disabled
- `[x]` frontend handlers are more codec-focused than before
- `[x]` if richer runtime error kinds are adopted later, they are shared without leaking protocol strings into core — **policy captured in `docs/execerr-classification.md`**

---

## Phase 9 — protect against architectural drift

### Status

`[x]` complete

### Remaining tasks

- `[x]` add architecture tests or CI checks for:
  - no core imports of bundled plugins
  - no `init()` registration in the standard bundle path
  - file-size / complexity budgets (or another agreed architectural drift metric)
- `[x]` add regression tests for all stage-two review blockers; many are covered now, but the checklist is not yet explicit or complete — **see `stage2_regression_checklist.md`**
- `[x]` update sample config to demonstrate multiple instances of one adapter kind and instance-aware routing
- `[x]` update docs and examples; README/ADR work landed, but operator/sample config coverage is still incomplete — **`config/config.multi-instance.example.yaml`, README links**

### Remaining deliverables

- architecture guardrail tests or CI checks
- updated sample config demonstrating multi-instance routing
- explicit regression checklist mapped to tests

### Remaining acceptance criteria

- stage-two review regressions are permanently encoded and discoverable
- sample config demonstrates multiple instances of one adapter kind
- the repo actively resists drifting back toward hidden composition and oversized-core complexity

---

## Completion summary

Stage three runtime hardening is complete for the approved scope of this spec. This directory lives under
**`.kiro/specs/archive/`**; active specs stay under **`.kiro/specs/`** per Kiro hygiene.

Completed close-out items:

- explicit registry threading now covers backend construction, feature composition, bundled-frontend mounting, and standard-binary validation
- custom/minimal bundle assembly is regression-tested without implicit dependence on `pluginreg.Default`
- test-side `init()` bundle registration has been removed and guarded against regression
- stage-two review regressions are documented, encoded, and discoverable

Optional future work belongs in follow-on specs rather than reopening this stage:

1. richer shared execute-error kinds if real product needs emerge
2. candidate-health admin/diagnostics surface beyond logs and route traces
3. SQLite pruning if durable retention policy becomes a product requirement
4. further bundle-shape refactoring only if the current `pluginreg` + `runtimebundle` split proves limiting
