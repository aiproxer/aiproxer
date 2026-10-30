# Final runtime-architecture-convergence release evidence

**Phase:** PR D — compatibility reconciliation, documentation, and final certification
**Recorded (UTC):** 2026-07-25T21:17:05Z
**Measurement parent SHA (worktree HEAD before Hermes commit):** `e80198596ba8d130589e6061e8f5c31af340084b`
**Final commit SHA:** 5446b0dface54772b7b62e2230ac0318c1c64ce1
**Baseline SHA:** `efe4624909cea318c7211d5cb3734059d3210802`
**Method:** recursive `CountNonTestGoLines` (non-test `.go` physical lines, including build-tag alternates). Same method as `internal/archtest.MeasureRuntimeConvergenceShrinkage` / `make arch-report`.

## Verdict

**PASS — Requirement 11.5 satisfied.**

Move-only changes do **not** count as shrinkage (Req 11.6). No surfaces were excluded from the counter.

## Runtime-convergence net shrinkage (Req 11.5)

Baseline SHA: `efe4624909cea318c7211d5cb3734059d3210802`

Method: recursive `CountNonTestGoLines` (non-test `.go` physical lines, including build-tag alternates). Moving unchanged logic between packages is not shrinkage (Req 11.6).

| Surface | Baseline | Current | Delta |
| --- | ---: | ---: | ---: |
| `internal/infra/runtimebundle` | 9898 | 9468 | -430 |
| `internal/infra/runtimehost` | 3056 | 3653 | +597 |
| `internal/stdhttp` | 4666 | 4301 | -365 |
| `cmd/lipstd` | 985 | 880 | -105 |
| `pkg/lipruntime` | 1037 | 536 | -501 |
| **TOTAL** | **19642** | **18838** | **-804** |

Required: delta ≤ -800 (remove ≥ 800 lines).

Verdict: **PASS**

## Current critical file sizes

## Hotspot files (critical-file budgets)

| File | Lines | Budget |
| --- | --- | --- |
| `internal/core/runtime/executor.go` | 125 | 150 |
| `internal/infra/runtimebundle/options.go` | 233 | 240 |
| `internal/standardplugins/standard_table.go` | 283 | 320 |
| `internal/pluginreg/reg.go` | 312 | 320 |
| `internal/stdhttp/server.go` | 8 | 8 |
| `internal/infra/runtimehost/coordinator.go` | 292 | 292 |
| `internal/infra/runtimehost/generation.go` | 316 | 316 |
| `internal/infra/runtimebundle/candidate_compile.go` | 259 | 259 |
| `internal/infra/runtimebundle/handler_composer.go` | 25 | 25 |
| `internal/infra/runtimebundle/compile_generation.go` | 292 | 292 |
| `internal/stdhttp/request_plane.go` | 65 | 65 |
| `internal/infra/runtimebundle/process_services.go` | 245 | 245 |
| `pkg/lipruntime/build.go` | 96 | 96 |
| `pkg/lipruntime/host.go` | 68 | 68 |
| `pkg/lipruntime/facade.go` | 57 | 57 |
| `cmd/lipstd/command.go` | 360 | 360 |
| `pkg/lipruntime/reload.go` | 89 | 89 |
| `pkg/lipruntime/reload_aliases.go` | 35 | 35 |

## Package fan-in / fan-out (affected surfaces)

## Runtime-convergence affected-surface fan-in/out

| Surface | Fan-out (direct internal) | Fan-in | Importers |
| --- | ---: | ---: | --- |
| `internal/infra/runtimebundle` | 85 | 3 | `cmd/lipstd`, `internal/stdhttp`, `pkg/lipruntime` |
| `internal/infra/runtimehost` | 4 | 2 | `internal/infra/runtimebundle`, `internal/stdhttp` |
| `internal/stdhttp` | 20 | 2 | `cmd/lipstd`, `pkg/lipruntime` |
| `cmd/lipstd` | 7 | 0 | (none) |
| `pkg/lipruntime` | 2 | 0 | (none) |

## Exported public symbol delta

| Package | Baseline (`efe46249`) | Current | Delta |
| --- | ---: | ---: | ---: |
| `pkg/lipapi` | 319 | 319 | 0 |
| `pkg/lipsdk` (root package) | 39 | 39 | 0 |
| `pkg/lipruntime` | 46 | 45 | -1 |

Remaining compatibility exceptions: **none** except explicitly documented public aliases in `pkg/lipruntime` / `pkg/lipsdk/configreload` (reload DTO aliases). Runtime-convergence allowlist is empty; host_path/config_load are permanently zero-tolerance.

## Deleted production symbols / paths

## Runtime-convergence deleted production symbols/paths

Enforced absent by `internal/archtest` deleted-symbol / bootstrap / serve gates (allowlist empty):

- `runtimebundle.Built`
- `runtimebundle.Build (compatibility orchestrator)`
- `stdhttp.RunWithRuntime`
- `requestPlaneAsBuilt`
- `NewStandardHandler`
- `standardHTTPInputFromBuilt`
- `releaseBuiltResources`
- `runClosers`
- `LegacyClosers`
- `BuildBootstrap / BootstrapResult / AttachReloadHost`
- `LoadBootstrapEffective / BootstrapMode`
- `pkg/lipruntime deprecated Options / legacy_options adapter`
- `pkg/lipruntime/reload_map.go (mirrored reload model)`

Parallel production runtime composition paths and mirrored reload models: **zero remaining** (empty `runtime_convergence_allowlist.json`; host_path/config_load permanently zero-tolerance).

## Stale FAIL report disposition

The contradictory FAIL report previously committed as `shrinkage-phase93-report.md` (measured at `805c1a57`, verdict FAIL / +663) is archived at:

`baseline/archive/shrinkage-phase93-report-FAIL-805c1a57.md`

This file is the sole authoritative final shrinkage + architecture evidence for the convergence.

## Related artifacts

- `baseline/measurement-host.txt` — host identity + `benchstat_installed=yes`
- `baseline/bench-final-runA.txt` / `bench-final-runB.txt` / `benchstat-final.txt` — final benchmark evidence (PR D3)
- `baseline/release-gate-results.md` — raw exit statuses (PR D4)
- `docs/legacy-options-migration.md` — alpha Options field migration (PR D1)
