# Final runtime-architecture-convergence release evidence

**Phase:** final compatibility, architecture, performance, and release certification
**Recorded (UTC):** 2026-07-26
**Reviewed baseline:** `efe4624909cea318c7211d5cb3734059d3210802`
**Certified implementation SHA:** `a5a2d375c767b3dad8225de0879f5a6c6f4b1ee5`
**Evidence commit:** evidence-only descendant of the certified implementation; no production or benchmark source changed after performance capture.

## Verdict

**PASS — all approved release gates are satisfied.**

- Runtime-convergence shrinkage: **-810 non-test Go lines**; requirement is at least -800.
- Architecture and compatibility gates: pass; historical named Runtime methods are present as compatibility wrappers.
- Local exact-implementation `make test` and `make lint`: pass.
- Remote exact-implementation QA: pass, including PostgreSQL integration, unit tests, golangci-lint, and govulncheck.
- CodeQL actions, Go, and JavaScript/TypeScript analyses: pass.
- Complete isolated baseline-versus-final benchmark matrix: pass, with one explicit maintainer-approved publication exception documented in `bench-final-notes.md`.

## Runtime-convergence net shrinkage (Requirement 11.5)

Method: recursive `CountNonTestGoLines` over non-test `.go` physical lines, including build-tag alternates. Move-only changes do not count as shrinkage and no surface is excluded.

| Surface | Baseline | Final | Delta |
| --- | ---: | ---: | ---: |
| `internal/infra/runtimebundle` | 9898 | 9449 | -449 |
| `internal/infra/runtimehost` | 3056 | 3653 | +597 |
| `internal/stdhttp` | 4666 | 4313 | -353 |
| `cmd/lipstd` | 985 | 880 | -105 |
| `pkg/lipruntime` | 1037 | 537 | -500 |
| **TOTAL** | **19642** | **18832** | **-810** |

Required: delta <= -800.
Verdict: **PASS**.

## Exact final critical-file budgets

| File | Lines | Budget |
| --- | ---: | ---: |
| `internal/core/runtime/executor.go` | 125 | 125 |
| `internal/infra/runtimebundle/options.go` | 233 | 233 |
| `internal/standardplugins/standard_table.go` | 283 | 283 |
| `internal/pluginreg/reg.go` | 312 | 312 |
| `internal/stdhttp/server.go` | 8 | 8 |
| `internal/infra/runtimehost/coordinator.go` | 292 | 292 |
| `internal/infra/runtimehost/generation.go` | 316 | 316 |
| `internal/infra/runtimebundle/candidate_compile.go` | 259 | 259 |
| `internal/infra/runtimebundle/handler_composer.go` | 25 | 25 |
| `internal/infra/runtimebundle/compile_generation.go` | 292 | 292 |
| `internal/stdhttp/request_plane.go` | 65 | 65 |
| `internal/infra/runtimebundle/process_services.go` | 233 | 233 |
| `pkg/lipruntime/build.go` | 96 | 96 |
| `pkg/lipruntime/host.go` | 68 | 68 |
| `pkg/lipruntime/facade.go` | 72 | 72 |
| `cmd/lipstd/command.go` | 360 | 360 |
| `pkg/lipruntime/reload.go` | 89 | 89 |
| `pkg/lipruntime/reload_aliases.go` | 35 | 35 |

Every measured file is at or below its exact ratchet.

## Performance certification

The complete required matrix compares baseline `efe46249` with implementation `a5a2d375` using 10 isolated ABBA samples per revision:

- candidate compilation: **-19.68% time, -15.44% bytes, -12.56% allocations**;
- BuildHost: **-10.07% time**;
- successful reload: **-2.09% time**;
- no-op reload: **-1.82% time**;
- Manager Acquire/Release: statistically unchanged;
- generation dispatch: statistically unchanged;
- Manager publication: +1.804 microseconds and +5 allocations per successful reload publication, explicitly approved by the maintainer because it is a reload-only cost of required asynchronous manager-owned retirement, not a request-path cost.

See:

- `bench-final-notes.md` — protocol, verdict, approval, and checksums;
- `bench-final-runA.txt` — raw baseline samples;
- `bench-final-runB.txt` — raw final samples;
- `benchstat-final.txt` — statistical comparison;
- `benchmark-baseline-overlay.patch` — baseline-only equivalent harness.

## Runtime architecture disposition

The convergence allowlist is empty; host-path and config-load gates are permanently zero-tolerance. Enforced-absent parallel production paths include:

- `runtimebundle.Built` and the compatibility `runtimebundle.Build` orchestrator;
- `stdhttp.RunWithRuntime`;
- `requestPlaneAsBuilt`;
- `NewStandardHandler`;
- `standardHTTPInputFromBuilt`;
- `releaseBuiltResources`, `runClosers`, and `LegacyClosers`;
- `BuildBootstrap`, `BootstrapResult`, `AttachReloadHost`, `LoadBootstrapEffective`, and `BootstrapMode`;
- deprecated `pkg/lipruntime` Options adapters and the mirrored reload model.

Required historical public Runtime method names remain as thin compatibility wrappers over the canonical host/reload implementation; they do not reintroduce ownership or composition paths.

## Related evidence

- `release-gate-results.md` — current exact-implementation local and remote gate status;
- `measurement-host.txt` — measurement host identity;
- `docs/legacy-options-migration.md` — alpha migration guidance;
- `baseline/archive/shrinkage-phase93-report-FAIL-805c1a57.md` — archived superseded failure report.

This file and the linked benchmark artifacts supersede the earlier evidence tied to `e8019859` / `5446b0df` and the two noisy final-versus-final runs.
