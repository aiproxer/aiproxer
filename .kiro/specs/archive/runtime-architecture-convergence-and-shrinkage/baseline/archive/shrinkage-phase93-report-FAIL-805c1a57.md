# Phase 9.3 Net Shrinkage Evidence — runtime-architecture-convergence-and-shrinkage

**Task:** 9.3 — Verify net shrinkage and dependency simplification
**Requirements:** 11.5–11.9
**Recorded (UTC):** 2026-07-25
**Baseline SHA:** `efe4624909cea318c7211d5cb3734059d3210802`
**Feature HEAD at measurement:** `805c1a57e3bff2cb573570aea5fbd48fe727f709` (+ local Task 9.3 metrics/gates)
**Method:** recursive `CountNonTestGoLines` (non-test `.go` physical lines, including build-tag alternates). Same method as Hermes pre-task measurement.

## Verdict

**FAIL — Requirement 11.5 not satisfied.**

| Metric | Value |
| --- | ---: |
| baseline_total | 19642 |
| current_total | 20305 |
| delta | **+663** |
| required | ≤ **-800** |
| shortfall | **1463** lines |

Moving unchanged logic between packages does not count (Req 11.6). No surfaces were excluded from the counter.

## Per-surface delta

| Surface | Baseline | Current | Delta |
| --- | ---: | ---: | ---: |
| `internal/infra/runtimebundle` | 9898 | 10418 | +520 |
| `internal/infra/runtimehost` | 3056 | 3850 | +794 |
| `internal/stdhttp` | 4666 | 4509 | -157 |
| `cmd/lipstd` | 985 | 880 | -105 |
| `pkg/lipruntime` | 1037 | 648 | -389 |
| **TOTAL** | **19642** | **20305** | **+663** |

## Why net growth happened

Largest **reductions** (true deletions / contractions):

| Delta | Path |
| ---: | --- |
| -505 | `runtimehost/coordinator.go` (thin orchestration) |
| -271 | `pkg/lipruntime/build.go` |
| -257 | `runtimehost/generation.go` |
| -249 | deleted `runtimebundle/bootstrap_plan.go` |
| -212 | deleted `runtimebundle/request_plane.go` |
| -211 | deleted `runtimehost/lifecycle_worker.go` |
| -159 | `stdhttp/server.go` |
| -127 | deleted `pkg/lipruntime/reload_map.go` |
| -114 | deleted `runtimebundle/built.go` |
| -105 | deleted `runtimebundle/build.go` |

Largest **growth** (required Phase 6–8 ownership; must not be deleted to fake shrinkage):

| Delta | Path | Notes |
| ---: | --- | --- |
| +479 | `runtimehost/attempt_runner.go` | AttemptRunner owner |
| +334 | `runtimehost/attempt_gate.go` | AttemptGate owner |
| +298 | `runtimehost/reload_state.go` | ReloadState owner |
| +250 | `runtimebundle/resource_ledger.go` | ResourceLedger growth |
| +235 | `runtimebundle/reload_host.go` | Host reload surface |
| +225 | `runtimehost/retire.go` | Manager retirement path |
| +220 | `runtimebundle/host_build.go` | sole Host build path |
| +210 | `runtimebundle/initial_generation.go` | publishInitialGeneration |
| +185 | `runtimebundle/validate_distribution.go` | true dry-run validation |

## Deleted symbols / paths (Req 11.8)

Enforced absent by archtest gates; allowlist empty:

- `runtimebundle.Built`, compatibility `runtimebundle.Build`
- `stdhttp.RunWithRuntime`, `requestPlaneAsBuilt`, `NewStandardHandler`, `standardHTTPInputFromBuilt`, `releaseBuiltResources`, `runClosers`, `LegacyClosers`
- `BuildBootstrap` / `BootstrapResult` / `AttachReloadHost` / `LoadBootstrapEffective` / `BootstrapMode`
- `pkg/lipruntime` deprecated Options / `legacy_options` adapter
- `pkg/lipruntime/reload_map.go` mirrored reload model

## Fan-in / fan-out (Req 11.8)

Baseline (`supplemental-metrics.json`, go-list internal imports) → current (`make arch-report` affected-surface table):

| Surface | Fan-out base→now | Fan-in base→now | Notes |
| --- | --- | --- | --- |
| `runtimebundle` | 82 → 85 | 3 → 3 | same importers |
| `runtimehost` | 4 → 4 | 4 → 2 | `cmd/lipstd` + `pkg/lipruntime` no longer import directly |
| `stdhttp` | 20 → 20 | 2 → 2 | unchanged |
| `cmd/lipstd` | 9 → 7 | 0 → 0 | fewer internal deps |
| `pkg/lipruntime` | 5 → 2 | 0 → 0 | facade narrowed |

## Parallel paths (Req 11.9)

**Zero remaining** parallel production runtime composition paths or mirrored reload models:

- `runtime_convergence_allowlist.json` entries: `[]`
- `host_path` / `config_load` permanently zero-tolerance
- Deleted-symbol / bootstrap / serve production scanners: clean

## Machine-checkable gate

- `internal/archtest.MeasureRuntimeConvergenceShrinkage` / `FormatRuntimeConvergenceShrinkage`
- `TestShrinkage_NetReductionMeetsRequirement115` fails while delta > -800
- `make arch-report` exits non-zero on FAIL after printing the full report

## Prioritized remediation (to reach ≤ -800)

Need **≥1463** true non-test production line deletions inside the five surfaces without removing required owners (AttemptGate, AttemptRunner, ReloadState, ResourceLedger, Manager retirement, Host.Close) and without move-only accounting.

| Priority | Candidate | Est. lines | Risk |
| ---: | --- | ---: | --- |
| 1 | Collapse overbuilt attempt/reload seams inside `attempt_runner.go` + `attempt_gate.go` + `reload_state.go` (shared helpers, remove redundant state copies) while keeping sole owners | 250–450 | High — concurrency/reload correctness |
| 2 | Contract `resource_ledger.go` (+250 vs baseline) and `reload_host.go` (+235) duplicate bookkeeping / pass-through | 200–350 | High — lifecycle/close ordering |
| 3 | Slim `validate_distribution.go`, `initial_generation.go`, `host_build.go` after behavior characterization proves unused branches | 200–350 | Medium |
| 4 | Remove remaining facade pass-through thickness in `host_queries.go` / `pkg/lipruntime/host.go` / `facade.go` where SDK already exposes the same surface | 100–200 | Medium — public API |
| 5 | Broader simplify pass across `runtimebundle` gravity wells (`terminal_work.go`, `control_plane.go`, `build_executor.go`) deleting dead paths only | 300–500 | Medium–High |

**Not acceptable:** excluding packages from the counter, raising budgets, or moving code out of the five surfaces to invent delta.

## Package-tree budgets note (Req 11.7)

Current exact freezes (`PackageTreeBudgets`) still sit **above** the Phase 1 baseline for `runtimebundle` (10418 > 9898). Even if other surfaces stay contracted, Requirement 11.5 cannot pass until the five-surface sum drops by ≥800 net — which requires material deletion inside `runtimebundle` and/or `runtimehost`, not further documentation.
