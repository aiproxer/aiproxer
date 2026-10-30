# Findings register — aiproxer architecture review

Generated: 2026-07-07

Severity scale:

- **Critical** — likely to materially harm maintainability soon if ignored.
- **High** — clear architectural debt with high future cost.
- **Medium** — worth fixing, but not urgent.
- **Low** — cleanup / hygiene / optional improvement.

## Summary table

| ID | Severity | Area | Finding | Recommended action |
| --- | --- | --- | --- | --- |
| F-01 | High | Overall | Strong architectural foundation already exists. | Preserve current canonical/contracts/plugins/composition-root model; avoid rewrite. |
| F-02 | Critical | `runtimebundle` | Composition package is too broad and already classified as `extract`. | Keep `Build` facade but split internal build units by concern. |
| F-03 | High | `pluginreg` | Package mixes registry, standard bundle, installer, security metadata, feature migration, and auth renderer concerns. | Move standard bundle composition out of registry package; keep registry boring. |
| F-04 | High | `runtime.Executor` | Executor is becoming a god orchestrator with many fields and many reasons to change. | Extract internal collaborators while preserving `Execute`. |
| F-05 | High | `stdhttp` | Standard HTTP server mixes mounting, diagnostics, admin, middleware, listener, and compatibility build path. | Split by file/concern; standardize canonical runtime build path. |
| F-06 | Medium | `BuildOptions` | Broad optional dependency bag resembles a service locator. | Group options into domain-specific structs. |
| F-07 | Medium | Frontend mounting | Mechanical duplication in standard frontend mounting. | Move mount functions to frontend packages or introduce small shared mount helper. |
| F-08 | Medium | Feature hooks | Legacy hook-only to feature-bundle bridge can become permanent migration debt. | Add retirement plan and move bridge out of registry package. |
| F-09 | High | Guardrails | Existing guardrails are good but too coarse for current growth stage. | Add per-file budgets, import-degree reports, and role-drift checks. |
| F-10 | Medium | Core namespace | `internal/core` covers many kinds of concerns; admission criteria need tightening. | Add explicit core admission checklist and classify packages by policy/use-case/adapter support. |
| F-11 | Low | Dependencies | Single-module `go.mod` necessarily includes provider SDKs and infra deps. | Keep as-is for now; rely on boundary tests. Consider module split only if build/distribution pressure appears. |
| F-12 | Medium | Docs/spec density | Architecture docs are strong but can become fragmented. | Keep a short authoritative architecture map and link specs from it. |

---

## F-01 — Strong architectural foundation already exists

**Severity:** High positive finding

### Evidence

The repository explicitly defines:

- canonical request/event contracts under `pkg/aipapi`;
- plugin SDK contracts under `pkg/aipsdk`;
- core-owned routing, failover, and continuity;
- official frontend/backend/feature plugins under `internal/plugins`;
- explicit standard distribution assembly under `cmd/aipstd`, `internal/pluginreg`, `internal/infra/runtimebundle`, and `internal/stdhttp`;
- architecture guardrails in `internal/archtest` and `internal/core/runtime/boundaries_test.go`.

### Impact

The project has the right primitives to scale cleanly. The main job is preserving and tightening them, not replacing them.

### Recommendation

Do not rewrite. Do not perform a folder-renaming exercise into generic Clean Architecture terms. Refactor targeted hotspots only.

### Acceptance criteria

- Existing public contracts remain stable.
- Core still does not import concrete plugins or provider SDKs.
- Standard distribution still uses explicit construction, not hidden registration.

---

## F-02 — `runtimebundle` is too broad

**Severity:** Critical

### Evidence

`internal/infra/runtimebundle/doc.go` describes the package as assembling continuity store, executor, shared upstream HTTP, resource shutdown hooks, routing health, route observation, and other standard-distribution runtime components.

`internal/infra/runtimebundle/build.go` constructs control-plane runtime, auth event dispatching, HTTP auth providers, metrics, upstream HTTP client, model catalog, backend factories, model registry, continuity store, secure-session runtime, routing defaults, capability resolver, executor, token accounting, price catalog, and diagnostics-related handles.

`testdata/architecture/hexagonal_migration_baseline.json` classifies `./internal/infra/runtimebundle` as `extract` and allows a very wide set of direct `internal/core/*` imports.

### Why this is a smell

Composition roots are allowed to know a lot, but this package now knows too much in one place. It is becoming a second runtime center. Future enterprise features will likely attach here, making it even harder to reason about startup behavior and resource ownership.

### Recommendation

Keep `runtimebundle.Build` as the public facade but split internal construction into focused build units:

- `buildObservabilityRuntime`
- `buildSecurityRuntime`
- `buildAuthRuntime`
- `buildModelRuntime`
- `buildBackendRuntime`
- `buildPersistenceRuntime`
- `buildExtensionRuntime`
- `buildExecutorRuntime`

Each unit should return a small result struct plus closers. Resource cleanup should remain explicit.

### Expected impact

- Lower cognitive load.
- Easier future enterprise composition.
- Cleaner testing of startup subgraphs.
- Reduced risk of accidental cross-concern changes.

### Acceptance criteria

- `runtimebundle.Build` still has the same external behavior.
- New internal build units have clear input/output structs.
- No new package-level global registries or lazy registration.
- Existing architecture tests continue to pass.
- `runtimebundle/build.go` becomes an orchestrator rather than a long constructor body.

---

## F-03 — `pluginreg` is doing more than registry work

**Severity:** High

### Evidence

`internal/pluginreg/reg.go` implements registry maps for backends, frontends, features, backend security profiles, and auth error renderers.

`internal/pluginreg/standard_table.go` imports all standard bundled frontend/backend/feature packages and defines standard distribution tables and installation functions.

`internal/pluginreg/featurebundle.go` wraps legacy hook-only factories into feature bundles.

The hexagonal baseline marks `./internal/pluginreg` as an `exception` with a retirement trigger.

### Why this is a smell

The package name says registry, but the package contains standard bundle assembly and migration helpers. This invites future contributors to put all “plugin-related” code there, even if it belongs to standard distribution assembly or feature bundle merging.

### Recommendation

Move toward this split:

```text
internal/pluginreg/
  registry.go
  factory_contracts.go
  security_profile.go

internal/standardplugins/
  bundle.go
  install.go
  backends.go
  frontends.go
  features.go
  keys.go

internal/featurebundle/
  merge.go
  legacy_hooks.go
```

Exact names can change, but the ownership should be clear: registry is not the standard bundle.

### Expected impact

- Clearer package ownership.
- Easier alternate/enterprise bundles.
- Less chance of registry becoming a dumping ground.
- Cleaner retirement of legacy hook bridges.

### Acceptance criteria

- `pluginreg.Registry` contains registration/lookup behavior only.
- Standard bundle tables live outside `pluginreg` or in an explicitly named standard-bundle package.
- Existing `InstallStandardBundleOn` call sites migrate cleanly or are forwarded temporarily.
- Hexagonal baseline exception for `pluginreg` is narrowed or retired.

---

## F-04 — `runtime.Executor` is a core orchestration gravity well

**Severity:** High

### Evidence

`internal/core/runtime/executor.go` defines a large `Executor` struct with routing, lifecycle, caps, catalog, eligibility, health, tracing, affinity, stream recovery, accounting, metrics, secure session, auth events, audit policy, interleaved-thinking, and memo-store fields.

`Execute` validates the call, attaches runtime snapshot, prepares secure session, starts tracing, prepares submit/A-leg state, starts lifecycle, extracts execution views, parses/defaults route selectors, initializes attempt/TTFT/session state, resolves affinity, loads interleaved state, loops through planning/opening attempts, registers B-legs, creates retry stream, attaches accounting/recovery, and wraps interleaved streams.

### Why this is a smell

The executor is the correct high-level entry point, but too many new concerns are landing directly on it. The field list and method path are becoming an index of the whole product.

### Recommendation

Extract internal collaborators while preserving `Executor.Execute`:

- `RequestPreparer`
- `RouteRequestPlanner`
- `AttemptOpener`
- `StreamAssembler`
- grouped runtime config structs: `RoutingRuntime`, `SecurityRuntime`, `AccountingRuntime`, `ObservabilityRuntime`, `ExtensionRuntime`.

### Expected impact

- Easier review of routing/failover changes.
- Easier testing of no-retry-after-output invariant.
- Clearer secure-session/accounting boundaries.
- Lower risk when adding enterprise policy/audit features.

### Acceptance criteria

- `Executor.Execute` remains the stable public entry point.
- No behavior change in route planning, attempt opening, stream recovery, or secure-session gates.
- Existing executor matrix tests pass.
- New collaborators have focused tests.
- `executor.go` line count and field count decrease materially.

---

## F-05 — `stdhttp` has too many responsibilities in one server file

**Severity:** High

### Evidence

`internal/stdhttp/server.go` contains middleware composition, diagnostics mounting, metrics mounting, inventory handler mounting, route trace, pprof, token accounting admin, secure-session diagnostics, model-catalog diagnostics, control-plane query mounting, bundled frontend mounting, app lifecycle start, handler construction, runtime convenience build, listener creation, shutdown, and closer cleanup.

`stdhttp.Run` itself documents that composition roots should normally call `runtimebundle.Build` once and then pass the built runtime to `RunWithRuntime`.

`internal/stdhttp/wire.go` exposed a thin `BuildExecutor` wrapper over `runtimebundle.BuildExecutor` (both now deleted).

### Why this is a smell

Transport adapters often become overloaded because all HTTP things seem related. Over time, diagnostics/admin/frontend/protocol and runtime lifecycle code become tangled.

### Recommendation

Split by concern inside the same package first:

- `server.go` — listener lifecycle and shutdown;
- `handler.go` — `NewStandardHandler`, `prepareStandardHandler` orchestration;
- `middleware.go` — middleware stack;
- `mount_diagnostics.go` — health, attempts, inventory, route trace, pprof;
- `mount_metrics.go` — Prometheus;
- `mount_admin.go` — accounting/control-plane admin;
- `mount_securesession.go` — secure-session diagnostics;
- `mount_frontends.go` — bundled frontend mounting.

Also decide whether `Run` is a deprecated convenience API or still first-class (BuildExecutor wrappers were deleted).

### Acceptance criteria

- No route/middleware behavior changes.
- `RunWithRuntime` remains the main serve path.
- `Run` is documented as compatibility convenience or removed if unused.
- `BuildExecutor` wrapper is removed (done — both `stdhttp.BuildExecutor` and `runtimebundle.BuildExecutor` deleted).
- Server file size and function size decrease materially.

---

## F-06 — `BuildOptions` is too broad

**Severity:** Medium

### Evidence

`runtimebundle.BuildOptions` spans startup, HTTP client, tracing, clock, control-plane test overrides, registry, wire model, HTTP auth, auth event sinks, remote auth decider, OS identity, auth renderers, session openers, workspace resolvers, tool filters, request transforms, route hints, completion gates, traffic/usage observers, raw capture, redactors, policy observers, timeout budget source, diagnostics, and secure-session store.

### Why this is a smell

An option struct this broad becomes a soft service locator. New concerns are added because the bag exists.

### Recommendation

Group options by concern and migrate internally first:

```go
type BuildOptions struct {
    Startup StartupOptions
    Infra InfraOptions
    Registry RegistryOptions
    Auth AuthOptions
    Extensions ExtensionOptions
    Policy PolicyOptions
    Diagnostics DiagnosticsOptions
    Testing TestingOptions
}
```

### Acceptance criteria

- Call sites continue to compile through compatibility fields or a staged migration.
- Internal build units consume grouped options only.
- Tests clarify which fields are production vs test-only.

---

## F-07 — Frontend mounting duplication

**Severity:** Medium

### Evidence

`internal/pluginreg/frontends_install.go` repeats nearly identical handler field wiring for multiple frontends.

### Recommendation

If pluginreg is split, move each mount function to its frontend package or expose a small common `FrontendRuntimePorts` struct shared across frontend handlers.

### Acceptance criteria

- Mount paths stay protocol-owned.
- No generic abstraction is introduced unless it removes real duplication.
- New frontend addition requires less repeated wiring.

---

## F-08 — Legacy hook bridge may become permanent debt

**Severity:** Medium

### Evidence

`FeatureFactoryFromHooks` bridges hook-only factories to `FeatureBundle`.

### Recommendation

Make the bridge explicitly temporary. Track remaining hook-only feature factories and migrate them to native `FeatureBundle` factories.

### Acceptance criteria

- New feature plugins return `FeatureBundle` directly.
- Bridge moves out of registry package.
- Retirement issue/checklist exists.

---

## F-09 — Guardrails need finer granularity

**Severity:** High

### Evidence

Existing tests enforce tree-level line budgets and important import rules. They do not yet prevent single-file hotspots or option-bag growth.

### Recommendation

Add advisory then enforced guardrails:

- per-file budgets for `executor.go`, `runtimebundle/build.go`, `stdhttp/server.go`, `pluginreg/standard_table.go`;
- direct import fan-out report;
- exported symbol count report for public packages;
- baseline entries for `extract` classifications with next-action metadata;
- package documentation requirement for new `internal/core/*` packages.

### Acceptance criteria

- `make quality-checks` or a new `make arch-report` exposes the metrics.
- Enforcement starts only with high-confidence budgets.
- Refactors update baselines intentionally.

---

## F-10 — Core namespace admission needs tightening

**Severity:** Medium

### Evidence

`internal/core` covers a broad set of subdomains and support surfaces.

### Recommendation

Add a short core-admission rule:

> Code belongs in `internal/core` only if it expresses cross-protocol product policy or use-case orchestration that cannot live in an adapter, plugin, or composition root.

### Acceptance criteria

- New core packages require package docs explaining why they belong in core.
- Architecture PR checklist includes core admission questions.
- Existing core packages are classified as policy/use-case/support/adapter-shim.

---

## F-11 — Dependency surface is acceptable but should be watched

**Severity:** Low

### Evidence

`go.mod` includes direct provider SDKs and infrastructure dependencies. In a single-module app this is expected, because concrete backend plugins live in the same module.

### Recommendation

Keep the current single-module model for now. Do not split modules unless there is a concrete pain:

- build times become problematic;
- public SDK users pull unwanted provider dependencies;
- enterprise/OSS packaging needs separate dependency surfaces;
- provider SDK version conflicts become painful.

Architecture tests already protect the more important concern: provider SDKs should not leak into core or public contracts.

---

## F-12 — Architecture docs are strong but could fragment

**Severity:** Medium

### Evidence

The repo has README, AGENTS, steering docs, ADRs, guardrail docs, active/archive specs, release gates, and conformance docs.

### Recommendation

Keep `docs/architecture.md` as the authoritative short map. Link deeper docs from it, but avoid scattering current architecture truth across too many files.

### Acceptance criteria

- New architectural decisions update one authoritative map plus relevant ADR/test.
- Archived specs are clearly historical and not treated as current implementation truth unless linked from current docs.

