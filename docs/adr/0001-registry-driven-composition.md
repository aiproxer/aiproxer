# ADR 0001: Registry-driven composition for the standard bundle

## Status

Accepted (stage two). For **optional backend connectors**, composition is superseded by [ADR 0008](0008-hybrid-backend-connector-plugins.md) (essential static builtins + executable gRPC plugins). This ADR still governs essential/static frontend, feature, and essential-backend registry tables.

## Context

The standard distribution must stay statically linked while avoiding central `switch` wiring that drifts from `plugins` configuration. Config rows for frontends, backends, and features must map to real constructors and mount paths.

## Decision

- Use `internal/pluginreg` as the **registry type** (value type [`pluginreg.Registry`](../../internal/pluginreg/reg.go)): factories are installed with **`standardplugins.InstallStandardBundleOn(reg, keys)`** (per-surface tables in `internal/standardplugins/standard_table.go` / `*_install.go`). The standard binary (`cmd/aipstd`) owns a dedicated `reg := pluginreg.NewRegistry()`, installs the bundle on `reg`, validates mandatory ids, and passes `reg` through **`runtimebundle.BuildOptions.PluginRegistry`** and stdhttp entrypoints. Tests follow the same rule: **`standardplugins.InstallStandardBundleOn(r, keys)`** on a fresh [`pluginreg.NewRegistry`](../../internal/pluginreg/reg.go), then inject `r` into build/mount options. There is **no** separate `internal/standardbundle` implementation package; the former `internal/pluginreg/standardbundle` façade has been deleted. The mandatory id list lives in `pkg/aipsdk.StandardDistributionRequirements`.
- Composition roots (`cmd/aipstd`, `internal/stdhttp`) resolve plugins **only** through registry APIs (`BuildBackend`, `MountFrontend`, `BuildFeatureHooks`, etc.) on the registry instance they were given. **`internal/infra/runtimebundle`** assembles enabled backends (using a shared upstream `*http.Client` from `internal/infra/httpclient` unless tests inject one), continuity store, and the core executor—including optional **routing health** (circuit breaker from `routing.health.circuit_breaker`) and **route observation** (structured `aip.route` logs when a logger is present).
- `pkg/aipsdk` holds stable **registration and factory contracts**; bundled plugins implement those contracts without being imported by `internal/core`. HTTP frontends share **`internal/plugins/frontends/execerr`** to map executor errors to HTTP status (reject vs internal) without duplicating classification.

## Consequences

- Adding a bundled plugin requires a registry entry and config documentation; no new switch arms in the core.
- Duplicate ids must be rejected at registration time (see registry validation tasks).
- Operator-facing behavior for routing cooldown and observability is configured in YAML (`routing.health`, executor logging) and documented in the main README “Current state” section and [`docs/routing-health-circuit-breaker.md`](../routing-health-circuit-breaker.md).
- Architecture budgets and import guardrails are enforced in [`internal/archtest`](../../internal/archtest/guardrails_test.go) and ADR 0005.

## Updates

- **2026-07-08 (arch review final closure):** The standard bundle registration tables (`standard_table.go`, `*_install.go`) and `InstallStandardBundleOn` moved from `internal/pluginreg` to `internal/standardplugins`. The registry value type (`pluginreg.Registry`, `NewRegistry`, `BuildBackend`, `BuildFeatureBundle`) remains in `internal/pluginreg`. Feature merge lives in `internal/featurebundle` (`MergeFeatureSurface` over SDK hook slices); `BuildFeatureHooks` and `hooks.New` construction moved to `internal/infra/runtimebundle` (composition root). The registry-driven composition decision above is unchanged — only the package locations of the standard tables and hook-bus construction narrowed. See `docs/architecture.md` and `testdata/architecture/hexagonal_migration_baseline.json` (schema v3) for the current map.
- **2026-07-08 (cleanup):** The `internal/pluginreg/standardbundle` façade package was deleted; `bootstrap_plan.go` now calls `standardplugins.InstallStandardBundleOn` directly. The `standardplugins/types.go` alias trampoline was removed; all `standardplugins` code uses qualified `pluginreg.X` references. The `runtimebundle.BuildExecutor` legacy wrapper was deleted; callers use `runtimebundle.Build` directly. `MergeFeatureSurface` was simplified with `MergeBundles`/`Append` helpers. The hexagonal baseline now includes a `role` field (`composition_root` for `runtimebundle`). Stale `pluginreg.DefaultWireModel` comment references were corrected to `standardplugins.DefaultWireModel`.
