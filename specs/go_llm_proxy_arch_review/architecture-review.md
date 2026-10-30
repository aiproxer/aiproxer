# Detailed architecture review — aiproxer

Generated: 2026-07-07
Repository: `aiproxer/aiproxer-enterprise`

## 1. Review scope and method

This review focuses on architecture maintainability, hexagonal architecture discipline, Go package design, coupling, de-slopification, and long-term ability to grow without turning the repository into a hard-to-maintain “big ball of composition”.

The review is static. It is based on repository files, architecture documents, guardrail tests, and hotspot source inspection. It does not claim local test execution.

The most important inspected materials were:

- `README.md`
- `AGENTS.md`
- `.kiro/steering/structure.md`
- `docs/architecture.md`
- `docs/architecture-guardrails.md`
- `docs/adr/0005-architecture-guardrails-and-complexity-budgets.md`
- `internal/archtest/*`
- `testdata/architecture/hexagonal_migration_baseline.json`
- `internal/infra/runtimebundle/*`
- `internal/pluginreg/*`
- `internal/stdhttp/*`
- `internal/core/runtime/executor.go`

## 2. Overall assessment

The repository is in a better state than many Go server/proxy projects at a comparable feature count. It has explicit architectural intent, a documented package map, public contracts separated from concrete adapters, and machine-checked guardrails.

The main risk is **not** that the project lacks architecture. The main risk is that the project has enough architecture that new features can hide inside large “acceptable” zones without obviously breaking rules.

In practical terms:

- The core is not importing concrete plugins. Good.
- Public contracts are protected from internal packages and vendor SDKs. Good.
- Standard bundle registration is explicit, not hidden behind `init()`. Good.
- But the standard distribution assembly path is growing into a large second center. Risky.
- Runtime orchestration is still cleanly separated from adapters, but `Executor` itself is now very broad. Risky.
- Guardrails exist, but they need to move from coarse tree budgets toward finer-grained role and file-level budgets. Needed soon.

The project should continue with its current architecture, but with stronger pressure toward **smaller composition units**, **narrower package ownership**, and **more explicit internal collaborators**.

## 3. Architectural strengths

### 3.1 Clear canonical middle

The product is built around a canonical request model and canonical event stream rather than pairwise protocol translators. This is one of the strongest decisions in the codebase.

Why it matters:

- Prevents `frontend × backend` translation explosion.
- Keeps protocol compatibility decisions local to adapters.
- Allows routing, failover, continuity, accounting, capability negotiation, and extension hooks to operate on stable shapes.
- Enables conformance tests to reason about behavior across frontends/backends.

This is aligned with the repository’s stated direction: frontends decode to `pkg/aipapi.Call`; backends emit canonical event streams; protocol-specific error rendering stays in adapters.

### 3.2 Stable public contracts are separated from internal implementation

The project has a clean conceptual split:

- `pkg/aipapi` — canonical request, event, capability, validation, collection, and error contracts.
- `pkg/aipsdk` — plugin SDK and feature extension contracts.
- `internal/core` — orchestration and shared product policy.
- `internal/plugins` — bundled protocol/provider/frontend/backend/feature adapters.
- `internal/pluginreg`, `internal/infra/runtimebundle`, `internal/stdhttp` — standard distribution assembly.

This is Go-idiomatic. The project is not overfitting to textbook Clean Architecture directory names. It is using Go packages as ownership and dependency-boundary tools.

### 3.3 Architecture guardrails are real, not aspirational

The repository has explicit architecture tests for several high-value rules:

- `internal/core` must not depend on `internal/plugins`.
- production packages must not depend on reference emulators.
- `pkg/aipapi` and `pkg/aipsdk` must not import `internal/...`, composition roots, `stdhttp`, or provider SDKs.
- feature plugins must not import `internal/core`.
- core must not import `internal/stdhttp` or concrete frontend/backend/feature plugins.
- core runtime must not import `net/http`.
- standard bundle registration paths must not use `init()`.
- composition layers must not call standard bundle installation themselves.
- composition roots must not grow package-level plugin registry or `sync.Once` singletons.
- high-risk trees have line-count budgets.

This is a major strength. Many Go projects only document boundaries; this one enforces important parts of them.

### 3.4 Explicit composition beats hidden registration

The accepted direction is:

```text
cmd/aipstd
  -> pluginreg.NewRegistry()
  -> pluginreg.InstallStandardBundleOn(reg, keys)
  -> validate bundle
  -> runtimebundle.Build(..., PluginRegistry: reg)
  -> stdhttp.RunWithRuntime(...)
```

That is exactly the right shape for a Go server/proxy where you want predictable startup, testable composition, and no hidden `init()` side effects.

### 3.5 Streaming-first execution model

The project treats streaming as the primary execution path and non-streaming as collection over streaming. This avoids split behavior and is the correct model for an LLM proxy.

The no-retry-after-first-output invariant is also important. It prevents subtle correctness issues where downstream clients receive partial content from one backend and then silently continue from another backend.

### 3.6 Feature seams are intentionally placed outside core business logic

The extension platform is designed to handle request shaping, tools, completion gates, workspace/state, traffic observation, auxiliary calls, and compatibility hooks through SDK facades. This is a good protection against core bloat.

The architecture documents correctly state that advanced request/response mutation belongs behind hooks/extensions, not inside routing or core orchestration.

## 4. Main architectural risks and smells

### 4.1 `internal/core` is becoming too broad as a namespace

`internal/core` currently includes runtime orchestration, routing, affinity, policy, B2BUA, continuity, secure sessions, auth, admin, HTTP helpers, safety, diagnostics, config, streaming, hooks, extensions, model catalog/registry, accounting, token accounting, traffic, workspace, state, and auxiliary request handling.

This is not automatically wrong. In a Go app, `internal/core` can reasonably contain several policy packages. But the risk is conceptual drift: “core” becomes a bucket for anything important.

Smell:

- The line budget for `internal/core` is already large.
- Multiple subpackages are policy-like, orchestration-like, adapter-like, or operator-facing.
- The package map tries to explain the difference, but the filesystem name alone does not.

Why it matters:

- New contributors may assume anything reusable belongs in core.
- Enterprise features may accidentally land in core because they feel cross-cutting.
- Boundary tests can still pass while core becomes too cognitively large.

Recommendation:

Do not rename everything. Instead, formalize a **core admission rule**:

A new `internal/core/*` package is allowed only if it owns cross-protocol product semantics that cannot live in a plugin, adapter, or composition root. Otherwise it belongs under `internal/infra`, `internal/stdhttp`, `internal/plugins`, or a feature bundle.

Add a short checklist to PR review and architecture docs:

- Does this package contain provider or protocol details? If yes, not core.
- Does this package exist only for HTTP/operator presentation? If yes, probably not core unless it defines canonical query contracts.
- Does this package perform durable I/O directly? If yes, isolate driver details in adapters.
- Is this policy shared by multiple frontends/backends? If no, consider plugin/adapter placement.

### 4.2 `runtimebundle` is the highest-risk hotspot

`internal/infra/runtimebundle` is described as the standard-distribution composition root. That is appropriate. The problem is its breadth.

Observed responsibilities include:

- startup context handling;
- control-plane runtime creation;
- auth event dispatcher construction;
- backend security validation;
- session audit policy;
- HTTP auth provider resolution;
- metrics bundle creation;
- upstream HTTP client creation and wrapping;
- model catalog startup;
- backend construction through `pluginreg`;
- model registry runtime startup;
- token-accounting strictness checks;
- continuity store opening;
- secure-session runtime building;
- default route and alias resolution;
- capability resolver construction;
- executor creation;
- interleaved-thinking application;
- token-accounting runtime creation;
- price catalog wiring;
- secure-session executor wiring;
- metrics sink wiring;
- model catalog attachment;
- shutdown closer management.

The machine-readable hexagonal baseline classifies `./internal/infra/runtimebundle` as `extract`. That is the correct classification.

This package is not violating hexagonal architecture by depending on core packages. Composition roots are allowed to know many things. The issue is maintainability: one package now has too many reasons to change.

Risk indicators:

- A wide import list across many `internal/core/*` packages.
- `BuildOptions` is a broad optional dependency bag.
- `Build` has a long linear startup procedure with many side effects and rollback paths.
- The package is already near a budgeted high-risk zone.
- Future enterprise composition will almost certainly want to hook into the same startup path.

Recommended direction:

Keep `runtimebundle.Build` as the facade, but split internal responsibilities into named build units with typed inputs/outputs.

A good target shape:

```text
runtimebundle.Build
  -> buildObservabilityRuntime
  -> buildSecurityRuntime
  -> buildPluginRuntime
  -> buildModelRuntime
  -> buildPersistenceRuntime
  -> buildExtensionRuntime
  -> buildExecutorRuntime
  -> return Built
```

Each unit should return a small result struct and closers. This preserves explicit composition while reducing the “everything is in Build” smell.

Avoid a DI container. The current explicit construction style is correct.

### 4.3 `BuildOptions` is drifting toward service-locator shape

`runtimebundle.BuildOptions` includes startup context, HTTP client, tracing, clock, control-plane store override, plugin registry, wire model, HTTP auth providers, auth event sink, remote decider, OS identity, auth renderers, session openers, workspace resolvers, tool filters, request transforms, pre-request handlers, route hints, completion gates, traffic observers, usage observers, raw capture sinks, redactors, policy observers, timeout budget source, policy diagnostics, and secure-session store.

This is understandable during fast growth, but the shape is dangerous:

- The name `BuildOptions` hides multiple domains.
- Optional fields accumulate because it is the easiest extension point.
- Tests and enterprise overlays may set fields in surprising combinations.
- It becomes difficult to know which options are infrastructure, security, feature surface, testing override, or diagnostics.

Recommendation:

Refactor into grouped option structs without changing behavior:

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

This does not require changing every call site immediately. You can add grouped options, keep old fields temporarily, and migrate internal code first.

### 4.4 `pluginreg` has too many package roles

`internal/pluginreg` currently does all of these:

- registry maps for backend/frontend/feature factories;
- backend credential/access-scope metadata;
- auth error renderer registration;
- standard bundle table;
- standard frontend/backend/feature installer functions;
- wrappers from legacy hook config to feature bundles;
- backend factory helper functions;
- config validation helpers;
- API key environment resolution;
- default wire model metadata.

The registry object itself is reasonably small. The package is too broad.

The hexagonal baseline classifies `./internal/pluginreg` as an `exception` and includes a retirement trigger: narrow or retire it when feature hook merge and inventory-facing feature bundle access move behind a composition-local helper or slimmer SDK-facing merger surface.

That is the right signal. The package name `pluginreg` should mean “registry”, not “all standard plugin composition”.

Recommended target:

```text
internal/pluginreg/
  registry.go            // Registry, factory contracts, registration/lookup only
  security_profile.go    // if metadata remains part of registry contract

internal/standardplugins/ or internal/stdlib/plugins/
  bundle.go              // StandardBundle table
  install.go             // InstallStandardBundleOn
  backend_factories.go   // backend adapters -> factories
  frontend_mounts.go     // frontend adapters -> mounts
  feature_factories.go   // feature adapters -> factories
  keys.go                // standard provider env-key resolution, if standard-distribution-specific

internal/featurebundle/ or internal/infra/featurebundle/
  merge.go               // feature-bundle-to-runtime-surface merge logic
  legacy_hooks.go        // temporary hook-only migration adapter, with retirement target
```

This change would reduce `pluginreg` from a broad composition package into a stable registry contract.

### 4.5 `runtime.Executor` is a core orchestration gravity well

The `Executor` is correctly the central runtime entry point. A proxy needs one place where validation, hooks, routing, capability negotiation, B2BUA, and backend stream opening come together.

The issue is that `Executor` now carries many fields and concerns:

- B2BUA store;
- hooks bus;
- runtime snapshot;
- backend map;
- A-leg lifecycle coordinator;
- random source and time source;
- routing defaults and aliases;
- caps/catalog/eligibility/request-size resolvers;
- candidate health and route observers;
- route tracing;
- affinity;
- pending wire event limits;
- stream recovery;
- transport fallback;
- accounting price catalog;
- preflight, stream usage, ledger, ledger write policy;
- token-accounting observability and admin service;
- metrics and extension metrics;
- secure-session manager, recorder, denial mapper, metrics, workspace requirements;
- auth event dispatcher and audit policy;
- interleaved-thinking config and memo store.

The `Execute` path coordinates validation, context snapshot publishing, secure-session preparation, tracing, submit/A-leg preparation, lifecycle scope, exec views, route preference extraction, selector aliasing/parsing/defaulting, attempt budgets, TTFT budget, session routing state, exclusions, request-size estimate, affinity key resolution, interleaved state loading, candidate planning/opening, B-leg registration, retry stream construction, accounting tracker, recovery policy, and interleaved stream wrapping.

This is a lot.

The risk is not that `Executor` is wrong. The risk is that every new feature will continue to add one more field, one more branch, and one more stream wrapper.

Recommended direction:

Keep `Executor.Execute(ctx, call)` as the public entry point. Internally extract collaborators:

- `RequestPreparer` — validation, runtime snapshot, secure session, submit hooks, A-leg preparation.
- `RouteRequestPlanner` — selector aliasing/defaulting, TTFT budget, attempt budget, route prefs, affinity, request-size estimate.
- `AttemptOpener` — candidate selection, capability negotiation, context-limit eligibility, backend open, B-leg lifecycle registration.
- `StreamAssembler` — retry stream creation, accounting tracker, recovery policy, secure-session recording, interleaved wrappers.
- `ExecutorRuntime` grouped config — routing/security/accounting/observability/extension sub-configs.

This should be done incrementally with no external API break and no behavior change.

### 4.6 `stdhttp` mixes transport, diagnostics, admin, mounting, and lifecycle

`internal/stdhttp/server.go` is doing many jobs:

- middleware stack composition;
- recovery, access logging, tracing, metrics;
- diagnostics endpoints;
- route trace buffer mounting;
- pprof mounting;
- token accounting admin mounting;
- secure-session diagnostics mounting;
- model-catalog diagnostics mounting;
- control-plane query route mounting;
- bundled frontend mounting;
- app lifecycle start;
- handler construction;
- convenience runtime build;
- TCP listener creation;
- shutdown and closer handling.

This is a standard Go-server smell. It is not a disaster, but it tends to worsen over time.

Recommended direction:

Keep `internal/stdhttp` as the standard HTTP adapter package, but split the file/function responsibilities:

```text
server.go              // RunWithRuntime, listener lifecycle only
handler.go             // NewStandardHandler, prepareStandardHandler orchestration
middleware.go          // stackHTTPHandler and middleware order
mount_frontends.go     // frontend mounting call
mount_diagnostics.go   // health, attempts, inventory, route trace, pprof
mount_admin.go         // token accounting admin, control-plane query
mount_securesession.go // secure-session diagnostics
mount_metrics.go       // Prometheus mounting
```

Do not create too many subpackages unless import cycles demand it. File-level separation is likely enough initially.

Also standardize one canonical build path. `stdhttp.Run` already documents that composition roots should normally build once and call `RunWithRuntime`. That implies `Run` is transitional convenience. Either deprecate it clearly or keep it as an intentionally thin compatibility wrapper.

### 4.7 Frontend mounting has mechanical duplication

`internal/pluginreg/frontends_install.go` repeats the same handler wiring pattern for OpenAI Responses, OpenAI legacy, Anthropic, and Gemini. Each handler receives `Exec`, default route selector, route prefixes, max request body bytes, traffic ports, pre-request keepalive, and decoded config.

The duplication is small, but it is a signal that standard frontend mounting is not yet expressed as a common adapter contract.

Recommended options:

- Light option: keep as-is; not worth over-abstracting until more frontends are added.
- Medium option: each frontend package exposes its own `Mount(mux, opts)` function, and the standard bundle table references those functions directly. This moves protocol-specific mount paths closer to the frontend package.
- Heavy option: introduce a generic `FrontendFactory` with `DecodeConfig` and `NewHandler`. Avoid this unless the codebase adds many more frontends.

Best current recommendation: **medium option** if you want to shrink `pluginreg`; otherwise leave it until the pluginreg split.

### 4.8 Transitional feature-bundle bridge should have a retirement plan

`FeatureFactoryFromHooks` wraps hook-only factories into the newer feature-bundle composition path. This is a reasonable migration mechanism. But permanent bridges tend to preserve old abstractions indefinitely.

Recommendation:

- Mark the hook-only bridge as compatibility/migration-only.
- Track remaining users.
- Move it out of `pluginreg` when `pluginreg` is slimmed.
- Eventually require new bundled features to return `feature.FeatureBundle` directly.

### 4.9 Guardrails are strong but need finer granularity

Current guardrails are good, especially because they are executable. But line-count budgets at large tree level can be gamed or miss key issues.

Missing or underdeveloped guardrails:

- per-file budgets for `executor.go`, `runtimebundle/build.go`, `stdhttp/server.go`, `pluginreg/standard_table.go`;
- package role drift checks, e.g. `pluginreg` importing many concrete plugins should be isolated to a single standard bundle package;
- import-degree metrics: top packages by number of direct internal imports;
- direct import baseline retirement task enforcement for `extract` classifications, not just `exception`;
- exported API growth budgets for public packages;
- package documentation requirement for new `internal/core/*` packages.

Recommendation:

Add a `make arch-report` or `go test ./internal/archtest -run Report` style tool that outputs:

- non-test lines by package and file;
- direct imports per package;
- forbidden imports;
- top fan-in/fan-out packages;
- current baseline classifications;
- packages over warning threshold.

This should be advisory first, then enforced selectively.

## 5. Hexagonal architecture fit

The repository’s own steering document gives the right interpretation: hexagonal architecture should be applied as an ownership and dependency-direction discipline, not a directory-renaming exercise.

For this project, a practical map is:

| Hexagonal concept | Project-specific equivalent |
| --- | --- |
| Domain / policy center | `pkg/aipapi` canonical contracts plus core policy packages under `internal/core` |
| Application/use-case orchestration | `internal/core/runtime`, routing, continuity, extension execution, secure-session turn handling |
| Driving adapters | HTTP frontends, CLI commands, diagnostics/admin HTTP routes, transport auth |
| Driven adapters | Backend plugins, store implementations, model/catalog providers, tokenizers, metrics/tracing exporters |
| Composition roots | `cmd/aipstd`, standard plugin bundle installation, runtime bundle assembly, HTTP server mounting |

The repo follows this model well in several key areas:

- core does not import concrete plugins;
- provider SDKs are kept away from public contracts and core;
- HTTP is kept out of core runtime;
- protocol adapters translate through canonical forms;
- non-streaming is not a separate execution path.

The main hexagonal weakness is not dependency inversion in the classic sense. It is **composition-layer overgrowth**. In large Go services, the composition root can become a second application layer if not kept explicit and boring. That is the primary risk here.

## 6. DRY, YAGNI, and code-reduction opportunities

### 6.1 Strong candidates for simplification

| Area | Opportunity |
| --- | --- |
| `stdhttp.Run` | Already removed. The canonical path is `Build + RunWithRuntime`; use `runtimebundle.Build` directly. |
| `stdhttp.BuildExecutor` | Already removed. `runtimebundle.BuildExecutor` was also deleted; use `runtimebundle.Build` directly. |
| Frontend mount wiring | Move per-frontend mount functions into frontend packages or introduce a small common mount helper. |
| `runtimebundle.BuildOptions` | Group fields by concern to reduce mental overhead and misuse. |
| Feature hook bridge | Track and retire hook-only bridge after feature bundles are first-class everywhere. |
| Architecture reports | Automate package/file size reports instead of manually tracking architectural mass. |

### 6.2 Things that look verbose but should not be prematurely simplified

| Area | Why not simplify aggressively yet |
| --- | --- |
| Architecture tests | They are valuable friction. Do not remove because they look bureaucratic. |
| Canonical event stream model | This is central to correctness. Do not add non-streaming shortcuts. |
| Explicit plugin registration | Verbose but safe. Do not replace with `init()` or reflection registration. |
| SDK facades | Necessary for plugin isolation. Refactor only when they become unused or too broad. |
| Conformance fixtures/goldens | Likely essential for protocol compatibility. Reduce only with evidence. |

## 7. Risk matrix

| Risk | Likelihood | Impact | Overall | Comment |
| --- | --- | --- | --- | --- |
| `runtimebundle` grows into second core | High | High | Critical | Already broad and classified as `extract`. |
| `pluginreg` becomes plugin-adjacent dumping ground | High | Medium/High | High | Current name is narrower than actual role. |
| `Executor` becomes too hard to reason about | Medium/High | High | High | The entry point is right, but internals need collaborators. |
| `stdhttp` accumulates operator/admin/protocol mounting complexity | Medium/High | Medium | High | Typical Go server drift pattern. |
| Feature bridge preserves old hook model forever | Medium | Medium | Medium | Needs retirement plan. |
| Public contracts leak provider specifics | Low currently | High | Medium | Tests make likelihood low; keep them. |
| Core imports concrete plugin/provider SDK | Low currently | High | Medium | Tests make likelihood low; keep them. |
| Over-refactoring into abstract clean architecture | Medium | High | High | Avoid generic `ports/services` churn. |

## 8. Recommended architectural target

The target should not be a radically different repo. The target should be the same architecture with sharper ownership:

```text
pkg/aipapi                  stable canonical contracts
pkg/aipsdk                  stable plugin SDK contracts
internal/core               cross-protocol product policy and use-case orchestration only
internal/plugins            concrete frontend/backend/feature adapters
internal/pluginreg          narrow registry/factory map only
internal/standardplugins    standard distribution plugin table and installers
internal/infra/runtimebundle facade over smaller build units
internal/stdhttp            standard HTTP adapter, split by mounting/lifecycle concern
cmd/aipstd                  explicit binary composition root
internal/archtest           stronger architecture budget/check suite
```

The highest-value structural outcome is this:

- `runtimebundle.Build` remains easy to call.
- Internally, `runtimebundle` no longer feels like one large constructor script.
- `pluginreg.Registry` becomes a stable, boring map of factories.
- Standard bundle tables become standard-distribution code, not registry code.
- `Executor.Execute` remains stable but reads as orchestration over named collaborators.
- `stdhttp` reads as HTTP adapter code, not runtime assembly code.

## 9. Final judgment

The project is architecturally promising and already has unusually good self-discipline. The strongest concern is future maintainability, not current correctness.

The next phase should be explicitly framed as **architecture debt retirement**:

- shrink broad composition packages;
- add finer guardrails;
- reduce transitional wrappers;
- extract executor collaborators;
- prevent core admission drift.

This will protect the codebase as backend/provider support, enterprise features, audit/search, auth, policy, billing, and observability inevitably grow.

