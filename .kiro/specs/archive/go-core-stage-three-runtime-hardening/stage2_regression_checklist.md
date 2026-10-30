# Stage-two review regressions (F1–F10)

Source review: [`.kiro/specs/archive/go-core-reimplementation-stage-two/stage2_code_review.md`](../go-core-reimplementation-stage-two/stage2_code_review.md).

Each item lists where it is guarded in code or tests. Update this file when adding or renaming tests.

| ID | Topic | Guard / verification |
| --- | --- | --- |
| F1 | Split adapter **kind** vs configured **instance** identity | Config + SDK: `internal/core/config/model.go`, `pkg/lipsdk/registration.go`. Tests: `internal/core/config/*_test.go`, `pkg/lipsdk/*_test.go`, `internal/infra/runtimebundle/dual_backend_test.go`. |
| F2 | Production clock / RNG defaults (no deterministic fallbacks in standard path) | `internal/infra/runtimebundle/build.go`, `internal/infra/runtimebundle/options.go`. Tests exercise injected clock/RNG in runtimebundle tests. |
| F3 | Single composition root owns stores, executor, closers | `internal/infra/runtimebundle/built.go`, `internal/stdhttp/server.go`. Tests: `internal/infra/runtimebundle/sqlite_closer_test.go`, `internal/infra/runtimebundle/build.go` wiring. |
| F4 | Store-specific continuity retention semantics | `internal/core/config/validate.go`, continuity packages. Tests: `internal/core/config/validate_test.go`, continuity store tests. |
| F5 | Routing health + route observation wired in standard bundle | `internal/infra/routinghealth/config_health.go` (`CandidateHealthFromConfig` from `internal/infra/runtimebundle/build.go`), executor fields. Tests: `internal/infra/runtimebundle/circuit_and_observer_test.go`, `internal/infra/routinghealth/config_health_test.go`. |
| F6 | Explicit bundle registration (no `init()` in standard path); registry visibility | `pluginreg.NewRegistry` + `pluginreg.InstallStandardBundleOn`, [`pluginreg.Registry`](../../../../internal/pluginreg/reg.go) + [`runtimebundle.BuildOptions.PluginRegistry`](../../../../internal/infra/runtimebundle/options.go), `internal/archtest/guardrails_test.go` (`TestStandardBundlePackagesHaveNoInitFunctions`). ADR 0001 / 0005. |
| F7 | Shared upstream HTTP client (not `http.DefaultClient` in bundle factories) | `internal/infra/httpclient/standard.go`, backend factories in `internal/pluginreg/backends_install.go`. |
| F8 | Request correlation not gated on diagnostics | `internal/stdhttp/server.go` middleware order. |
| F9 | Plugin boundary / avoid deepening coupling | Ongoing design; `internal/core/runtime/boundaries_test.go` (core vs plugins). |
| F10 | Shared frontend execute-error classification | `internal/plugins/frontends/execerr/`. Tests: `internal/plugins/frontends/*/encode_test.go`, `handler` tests. Future kinds: [`docs/execerr-classification.md`](../../../../docs/execerr-classification.md). |

Related: **no core imports of `internal/plugins`** — `internal/core/runtime/boundaries_test.go` (`TestCorePackagesDoNotDependOnConcretePluginPackages`).
