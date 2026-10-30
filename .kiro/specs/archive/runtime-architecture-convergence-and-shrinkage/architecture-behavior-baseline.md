# Architecture and Behavior Baseline — runtime-architecture-convergence-and-shrinkage

**Task:** 1.1 — Record exact architecture and behavior baselines
**Requirements:** 1.1–1.10, 11.1–11.9, 13.1–13.10
**Recorded (UTC):** 2026-07-23T13:24:32Z
**Reviewed production baseline SHA:** `efe4624909cea318c7211d5cb3734059d3210802`
**Feature-branch HEAD at recording (metadata only):** `3d7584c2686e72c49b7d31906e4d1eda5c15c61c`

This document freezes reproducible architecture and benchmark evidence **before** production refactoring. Measurements were taken from a temporary detached Git worktree at the reviewed SHA so feature-branch Kiro metadata cannot skew production package metrics.

## 1. Reproducibility

| Item | Value |
| --- | --- |
| Reviewed SHA | `efe4624909cea318c7211d5cb3734059d3210802` (`feat: add versioned runtime-reloadable proxy configuration (#194)`) |
| Measurement worktree | temporary detached Git worktree at reviewed SHA; feature worktree holds durable artifacts |
| Go toolchain | `go1.26.5 linux/amd64` (`/usr/local/go`) |
| OS / kernel | Linux 6.17.0-1018-oracle x86_64 (Ubuntu 24.04) |
| CPU | AMD EPYC 9J14 96-Core Processor (2 online CPUs / `nproc=2`) |
| Bench workers | Go default for this host (`-2` suffix → `GOMAXPROCS=2`) |
| `benchstat` | **not installed**; raw repeated outputs preserved for later comparison |
| Capture helper | [`baseline/capture_baseline.py`](./baseline/capture_baseline.py) (+ [`baseline/count_exports.go`](./baseline/count_exports.go)) |

### Exact reproducible capture procedure

Paths are parameterized. Every variable below is defined before use. This is the
authoritative procedure to regenerate architecture/inventory evidence at the
reviewed SHA (literal one-off historical shell history is not required).

```bash
FEATURE_WORKTREE=/home/ubuntu/src/github.com/matdev83/go-llm-interactive-proxy-runtime-architecture-convergence-and-shrinkage
BASELINE_WORKTREE=/tmp/lip-baseline-efe4624
REVIEWED_SHA=efe4624909cea318c7211d5cb3734059d3210802
OUT_DIR="$FEATURE_WORKTREE/.kiro/specs/archive/runtime-architecture-convergence-and-shrinkage/baseline"
HELPER="$OUT_DIR/capture_baseline.py"

cd "$FEATURE_WORKTREE"
git worktree add --detach "$BASELINE_WORKTREE" "$REVIEWED_SHA"

# 1) Full advisory arch-report at reviewed SHA → durable artifact in feature OUT_DIR
( cd "$BASELINE_WORKTREE" && make arch-report ) > "$OUT_DIR/arch-report-efe46249.md"

# 2) Supplemental metrics + migration inventory (deterministic; verifies HEAD==REVIEWED_SHA)
python3 "$HELPER" \
  --repo-root "$BASELINE_WORKTREE" \
  --reviewed-sha "$REVIEWED_SHA" \
  --out-dir "$OUT_DIR"

# Writes:
#   $OUT_DIR/supplemental-metrics.json
#   $OUT_DIR/migration-inventory.json

# 3) Task-relevant repeated benchmarks; tee into feature OUT_DIR (not the detached tree)
BENCH_RUN='BenchmarkManager_AcquireRelease$|BenchmarkManager_Publish$|BenchmarkGenerationDispatcher_AcquireLease$|BenchmarkCandidateCompilation$'
( cd "$BASELINE_WORKTREE" && go test -bench="$BENCH_RUN" -benchmem -count=10 -run='^$' \
    ./internal/infra/runtimehost/ ./internal/infra/runtimebundle/ ) | tee "$OUT_DIR/bench-runA.txt"
( cd "$BASELINE_WORKTREE" && go test -bench="$BENCH_RUN" -benchmem -count=10 -run='^$' \
    ./internal/infra/runtimehost/ ./internal/infra/runtimebundle/ ) | tee "$OUT_DIR/bench-runB.txt"

# Optional host metadata (already recorded for this baseline):
#   $OUT_DIR/measurement-host.txt

git worktree remove "$BASELINE_WORKTREE"
```

Helper deterministic inputs/outputs:

| Input | Meaning |
| --- | --- |
| `--repo-root` | Worktree root whose `HEAD` must equal `--reviewed-sha` |
| `--reviewed-sha` | Reviewed production baseline object name |
| Go package graph | `go list -e -json -test=false ./...` (line counts via `GoFiles` newline counts; fan-in/out via `Imports`) |
| Export AST helper | `go run baseline/count_exports.go` (same rules as `scripts/arch-report.go`) |
| Inventory scan | Fixed identifier patterns + role heuristics inside `capture_baseline.py` |

| Output | Contents |
| --- | --- |
| `supplemental-metrics.json` | Named-surface lines, stdhttp recursive lines, hotspot file lines, fan-in/out, exports |
| `migration-inventory.json` | Per-concept file/caller inventory with `file_class` and roles (`production_caller` only on production files) |

Future comparison (once `benchstat` is available without adding a runtime module dependency):

```bash
FEATURE_WORKTREE=/home/ubuntu/src/github.com/matdev83/go-llm-interactive-proxy-runtime-architecture-convergence-and-shrinkage
OUT_DIR="$FEATURE_WORKTREE/.kiro/specs/archive/runtime-architecture-convergence-and-shrinkage/baseline"
# Set AFTER_RUN to the Phase 9 raw `go test -bench=...` output file when that evidence exists:
AFTER_RUN="$FEATURE_WORKTREE/.kiro/specs/archive/runtime-architecture-convergence-and-shrinkage/baseline/bench-phase9.txt"

# tool install is optional/local; do not add to go.mod for this feature
go install golang.org/x/perf/cmd/benchstat@latest
benchstat "$OUT_DIR/bench-runA.txt" "$OUT_DIR/bench-runB.txt"   # self-consistency
benchstat "$OUT_DIR/bench-runA.txt" "$AFTER_RUN"                # Phase 9 comparison
```

Committed raw artifacts under this spec:

| Artifact | Path |
| --- | --- |
| Capture helper | [`baseline/capture_baseline.py`](./baseline/capture_baseline.py) |
| Export counter (`//go:build ignore`) | [`baseline/count_exports.go`](./baseline/count_exports.go) |
| Full `make arch-report` output | [`baseline/arch-report-efe46249.md`](./baseline/arch-report-efe46249.md) |
| Supplemental metrics JSON | [`baseline/supplemental-metrics.json`](./baseline/supplemental-metrics.json) |
| Bench series A (`-count=10`) | [`baseline/bench-runA.txt`](./baseline/bench-runA.txt) |
| Bench series B (`-count=10`) | [`baseline/bench-runB.txt`](./baseline/bench-runB.txt) |
| Migration caller inventory | [`baseline/migration-inventory.json`](./baseline/migration-inventory.json) |
| Host metadata | [`baseline/measurement-host.txt`](./baseline/measurement-host.txt) |


## 2. Architecture metrics at `efe46249`

Line counting method: **`baseline/capture_baseline.py`**, identical to `scripts/arch-report.go` — `go list` package `GoFiles` only (excludes `_test.go` and build-tag-excluded files), then physical newline counts. Machine-readable copy: [`baseline/supplemental-metrics.json`](./baseline/supplemental-metrics.json). Moving code between files must never be treated as shrinkage (Requirement 11.6).

### 2.1 Affected package non-test production lines

| Package | Non-test lines | Notes |
| --- | ---: | --- |
| `internal/infra/runtimebundle` | **9898** | Also appears in arch-report top-25 |
| `internal/infra/runtimehost` | **3056** | Also appears in arch-report top-25 |
| `internal/stdhttp` (root package only) | **2106** | Also appears in arch-report top-25 |
| `cmd/lipstd` | **935** | Supplemental (not in arch-report top-25) |
| `pkg/lipruntime` | **1037** | Supplemental (not in arch-report top-25) |
| **Sum (named surfaces)** | **17032** | Shrinkage target (Req 11.5): reduce by ≥800 vs this sum's comparable after-state |

`internal/stdhttp/...` recursive (all subpackages, same method) totals **4648** lines (root 2106 + auth 939 + admin/controlplane 761 + admin/configreload 658 + admin/tokenaccounting 184). Requirement 11.5 names the `stdhttp` surface; Phase 9 must state whether comparison uses root-only (arch-report) or recursive totals. **This baseline locks both.**

### 2.2 Critical hotspot files (arch-report `CriticalFileBudgets`)

From `make arch-report` at reviewed SHA:

| File | Lines | Current budget (`CriticalFileBudgets`) |
| --- | ---: | ---: |
| `internal/core/runtime/executor.go` | 125 | 150 |
| `internal/infra/runtimebundle/build.go` | 105 | 220 |
| `internal/infra/runtimebundle/options.go` | 233 | 240 |
| `internal/standardplugins/standard_table.go` | 283 | 320 |
| `internal/pluginreg/reg.go` | 312 | 320 |
| `internal/stdhttp/server.go` | 167 | 300 |

### 2.3 Migration-relevant hotspot file sizes (supplemental; from `supplemental-metrics.json`)

These are the contraction gravity wells named by Requirements 11.1–11.3 / design. Task 1.2 will freeze budgets from these measured values.

| File | Lines | Spec final ceiling (Req 11.3) |
| --- | ---: | ---: |
| `internal/infra/runtimehost/coordinator.go` | **797** | ≤300 (orchestration) |
| `internal/infra/runtimehost/generation.go` | **575** | ≤400 (generation state) |
| `internal/infra/runtimebundle/candidate_compile.go` | **440** | ≤350 (candidate compilation) |
| `internal/infra/runtimebundle/compile_generation.go` | **257** | (related compile path) |
| `internal/infra/runtimebundle/process_services.go` | **364** | ≤300 process construction (closest current owner; exact file may change in 1.2) |
| `internal/infra/runtimebundle/build.go` | **105** | (orchestrator already slim) |
| `pkg/lipruntime/build.go` | **367** | ≤150 public build/facade assembly |
| `pkg/lipruntime/normalize.go` | **263** | deprecated Options conversion |
| `pkg/lipruntime/reload.go` | **176** | duplicate public reload contract |
| `pkg/lipruntime/reload_map.go` | **127** | mirror mapping to delete |
| `internal/stdhttp/request_plane.go` | **175** | hosts `requestPlaneAsBuilt` |
| `internal/infra/runtimebundle/reload_host.go` | **188** | `AttachReloadHost` |

### 2.4 Direct internal fan-out / fan-in (affected surfaces)

Fan-out counts **direct imports of `module/internal/...` only** (arch-report definition). Fan-in counts production packages (`go list ./...` Imports, not TestImports) that import the target. Values from `supplemental-metrics.json`.

| Package | Fan-out (internal) | Fan-in (importers) | Importers (production) |
| --- | ---: | ---: | --- |
| `internal/infra/runtimebundle` | **82** | **3** | `cmd/lipstd`, `internal/stdhttp`, `pkg/lipruntime` |
| `internal/infra/runtimehost` | **4** | **4** | `cmd/lipstd`, `runtimebundle`, `stdhttp`, `pkg/lipruntime` |
| `internal/stdhttp` | **20** | **2** | `cmd/lipstd`, `pkg/lipruntime` |
| `cmd/lipstd` | **9** | **0** | (binary root) |
| `pkg/lipruntime` | **5** internal imports | **0** in-repo production importers | consumed by external modules / `testdata/enterprise_module` |

### 2.5 Exported public symbol counts

Method: AST count of exported type/value/func declarations in non-test `.go` files of the package directory via `baseline/count_exports.go` (same rules as `scripts/arch-report.go` for `pkg/lipapi` / `pkg/lipsdk`).

| Package | Exported symbols | Source |
| --- | ---: | --- |
| `pkg/lipapi` | **319** | `make arch-report` |
| `pkg/lipsdk` (root only) | **39** | `make arch-report` |
| `pkg/lipruntime` | **46** | `supplemental-metrics.json` / `count_exports.go` |

Note: `pkg/lipsdk/...` subpackages are outside the arch-report export table; only the root `pkg/lipsdk` directory is counted, matching existing tooling.

## 3. Benchmark baselines

### 3.1 Existing benchmarks covered

| Required area | Benchmark name | Package | Status |
| --- | --- | --- | --- |
| Generation acquire/release | `BenchmarkManager_AcquireRelease` | `internal/infra/runtimehost` | Measured |
| Generation publication | `BenchmarkManager_Publish` | `internal/infra/runtimehost` | Measured |
| Dispatcher / request dispatch | `BenchmarkGenerationDispatcher_AcquireLease` | `internal/infra/runtimehost` | Measured |
| Candidate / generation compilation | `BenchmarkCandidateCompilation` | `internal/infra/runtimebundle` | Measured |
| Successful reload | — | — | **GAP** — no benchmark exists at reviewed SHA |
| No-op reload | — | — | **GAP** — no benchmark exists at reviewed SHA |
| Full BuildHost (design optional) | — | — | **GAP** — no benchmark exists; design lists it under Performance |

**Evidence gap ownership:** Task **9.5** (`Run benchmark/security gates and publish final release evidence`) must compare via `benchstat` against this baseline and therefore must ensure successful-reload and no-op-reload benchmarks exist before that comparison. Prefer adding them in the phase that owns reload decomposition (Phase 6/7) or as a prerequisite note under 9.5 — **do not invent numbers here**.

### 3.2 Summarized repeated-run results (`-count=10`, two consecutive series)

Values are **medians** across 10 samples per series. Allocation counts were stable; wall-time shows normal host noise (especially compilation).

| Benchmark | RunA median ns/op | RunB median ns/op | Median B/op (A) | Median allocs/op (A) |
| --- | ---: | ---: | ---: | ---: |
| `BenchmarkManager_AcquireRelease` | 26.20 | 26.23 | 16 | 1 |
| `BenchmarkGenerationDispatcher_AcquireLease` | 417.6 | 362.3 | 784 | 9 |
| `BenchmarkManager_Publish` | 454.7 | 443.7 | 480 | 4 |
| `BenchmarkCandidateCompilation` | 4,815,047 | 5,040,023 | 4,635,202 | 19,616 |

Observed ranges (documenting noise, not regressions):

| Benchmark | RunA ns/op range | RunB ns/op range |
| --- | --- | --- |
| `BenchmarkManager_AcquireRelease` | 26.07–27.20 | 26.05–26.39 |
| `BenchmarkGenerationDispatcher_AcquireLease` | 356.8–474.6 | 352.0–409.9 |
| `BenchmarkManager_Publish` | 417.1–591.9 | 413.3–530.5 |
| `BenchmarkCandidateCompilation` | 4.54e6–5.05e6 | 4.81e6–7.22e6 (one outlier at 7.22e6) |

Requirement 13.7 (≤10% compile regression) must use `benchstat` on equivalent-host multi-sample files, not single medians.

## 4. Migration concept caller inventory

Complete machine-readable inventory: [`baseline/migration-inventory.json`](./baseline/migration-inventory.json) (regenerated by `capture_baseline.py`).
Below: every file that mentions each concept, classified. Roles distinguish declarations, callers, adapters, field/type usage, and comment-only mentions. **`production_caller` is reserved for production `file_class` only**; test and sample call sites use neutral `caller` / `caller_lipruntime`.

### 4.1 Inventory summary

| Concept | Hits | Files | Production files | Test files | Arch-check files | Other |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| `Built` | 181 | 60 | 25 | 32 | 3 | 0 |
| compatibility `Build` (`runtimebundle.Build` / `lipruntime.Build`) | 204 | 67 | 3 | 63 | 0 | 1 sample module |
| `RunWithRuntime` | 32 | 13 | 9 | 4 | 0 | 0 |
| `RequestPlane` | 71 | 19 | 12 | 6 | 1 | 0 |
| `requestPlaneAsBuilt` | 3 | 1 | 1 | 0 | 0 | 0 |
| `AttachReloadHost` | 11 | 7 | 5 | 2 | 0 | 0 |
| duplicate reload contracts / mappings | 33 | 5 | 4 | 1 | 0 | 0 |
| deprecated public `Options` + conversion | 68 | 7 | 3 | 4 | 0 | 0 |

### 4.2 Key production findings (compatibility inventory)

1. **`runtimebundle.Built`** is declared in `internal/infra/runtimebundle/built.go` and remains the broad dependency bag consumed by `internal/stdhttp` mount helpers (`handler.go`, `middleware.go`, `mount_*.go`, `server.go`) and constructed by `Build` / `requestPlaneAsBuilt`.
2. **Compatibility `runtimebundle.Build`** has exactly **one production caller**: `internal/infra/runtimebundle/bootstrap_plan.go` (legacy Built path inside `BuildBootstrap`). Public `pkg/lipruntime.Build` is declared in `pkg/lipruntime/build.go` and calls `BuildBootstrap` + `AttachReloadHost` (not `runtimebundle.Build` directly). All other `runtimebundle.Build(` call sites are tests (plus `testdata/enterprise_module` for `lipruntime.Build`).
3. **`stdhttp.RunWithRuntime`** is **declared** in `internal/stdhttp/server.go` but has **no production callers** at reviewed SHA; production serve uses `BuildBootstrap` + `AttachReloadHost`. Remaining production hits are comment references. Tests still call it directly.
4. **`requestPlaneAsBuilt`** exists only in `internal/stdhttp/request_plane.go` (declaration + single caller path composing mounts from a `RequestPlane`).
5. **`AttachReloadHost`** production callers: `cmd/lipstd/command.go`, `pkg/lipruntime/build.go`; declaration in `internal/infra/runtimebundle/reload_host.go`.
6. **Duplicate reload contracts:** closed vocabulary types are declared in both `internal/core/configreload/model.go` and mirrored in `pkg/lipruntime/reload.go`, with field-for-field mapping in `pkg/lipruntime/reload_map.go`. HTTP admin uses `configreload` types via DTOs (`internal/stdhttp/admin/configreload/dto.go`) rather than a third domain copy.
7. **Deprecated public Options:** fields `RequestProviders`, `AttemptProviders`, `ConcurrencyProvider`, `Rater`, `ProviderDescriptors` live in `pkg/lipruntime/options.go`; conversion/quarantine logic in `pkg/lipruntime/normalize.go`.

### 4.3 Complete file inventories by concept


### `Built`

#### Classification: `production` (25 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/core/terminalwork/app/generation_pin_tracker.go` | comment_mention | 1 (377) |
| `internal/infra/runtimebundle/bootstrap_host.go` | field_usage | 1 (138) |
| `internal/infra/runtimebundle/bootstrap_plan.go` | comment_mention, field_usage, type_usage | 3 (54, 66, 245) |
| `internal/infra/runtimebundle/build.go` | comment_mention, construction, type_usage | 3 (15, 21, 70) |
| `internal/infra/runtimebundle/build_extension.go` | comment_mention | 1 (32) |
| `internal/infra/runtimebundle/build_model.go` | comment_mention | 1 (94) |
| `internal/infra/runtimebundle/built.go` | comment_mention, declaration | 4 (28, 29, 42, 50) |
| `internal/infra/runtimebundle/candidate_compile.go` | comment_mention | 1 (334) |
| `internal/infra/runtimebundle/generation_bundle.go` | comment_mention | 1 (32) |
| `internal/infra/runtimebundle/options.go` | comment_mention | 1 (197) |
| `internal/infra/runtimebundle/request_plane.go` | comment_mention | 1 (43) |
| `internal/infra/runtimebundle/resource_ledger.go` | comment_mention | 1 (280) |
| `internal/infra/runtimebundle/terminal_work.go` | type_usage | 2 (516, 569) |
| `internal/infra/runtimehost/generation.go` | comment_mention | 1 (50) |
| `internal/infra/runtimehost/request_binding.go` | comment_mention | 1 (14) |
| `internal/infra/runtimehost/request_plane.go` | comment_mention | 1 (9) |
| `internal/stdhttp/handler.go` | field_usage, type_usage | 11 (39, 61, 69, 76, 80, 87, 90, 93, 96, 141, 155) |
| `internal/stdhttp/middleware.go` | field_usage, type_usage | 3 (20, 42, 44) |
| `internal/stdhttp/mount_admin.go` | field_usage, type_usage | 6 (22, 29, 57, 70, 118, 125) |
| `internal/stdhttp/mount_diagnostics.go` | field_usage, type_usage | 6 (27, 39, 61, 104, 108, 138) |
| `internal/stdhttp/mount_metrics.go` | field_usage, type_usage | 2 (22, 30) |
| `internal/stdhttp/mount_securesession.go` | field_usage, type_usage | 2 (22, 30) |
| `internal/stdhttp/request_plane.go` | comment_mention, compatibility_adapter, field_usage | 12 (58, 65, 72, 76, 82, 85, 88, 91, 135, 139, 141, 142) |
| `internal/stdhttp/route.go` | comment_mention | 1 (12) |
| `internal/stdhttp/server.go` | type_usage | 2 (30, 125) |

#### Classification: `test_only` (32 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/core/runtime/secret_guard_real_feature_test.go` | type_usage | 2 (40, 289) |
| `internal/infra/runtimebundle/bootstrap_compatibility_test.go` | code_reference, comment_mention, field_usage | 3 (307, 321, 322) |
| `internal/infra/runtimebundle/bootstrap_plan_internal_test.go` | code_reference, field_usage | 4 (64, 65, 121, 122) |
| `internal/infra/runtimebundle/bootstrap_plan_test.go` | code_reference, field_usage | 8 (38, 39, 63, 64, 100, 103, 106, 109) |
| `internal/infra/runtimebundle/build_modelcatalog_test.go` | type_usage | 1 (182) |
| `internal/infra/runtimebundle/build_policy_test.go` | code_reference | 1 (170) |
| `internal/infra/runtimebundle/build_test.go` | code_reference | 2 (65, 68) |
| `internal/infra/runtimebundle/catalog_ownership_order_test.go` | comment_mention | 1 (44) |
| `internal/infra/runtimebundle/compile_generation_test.go` | code_reference, field_usage | 2 (392, 393) |
| `internal/infra/runtimebundle/control_plane_build_test.go` | type_usage | 1 (24) |
| `internal/infra/runtimebundle/initial_generation_test.go` | code_reference, field_usage | 9 (47, 48, 104, 122, 123, 124, 125, 133, 134) |
| `internal/infra/runtimebundle/modelregistry_build_test.go` | type_usage | 1 (500) |
| `internal/infra/runtimebundle/ownership_inventory_test.go` | code_reference, comment_mention, field_usage | 16 (65, 102, 104, 107, 217, 228, 282, 285, 288, 295, 297, 300, … (+4 more)) |
| `internal/infra/runtimebundle/phase45_process_metrics_prom_red_test.go` | type_usage | 1 (91) |
| `internal/infra/runtimebundle/pooled_postgres_runtime_integration_test.go` | type_usage | 1 (155) |
| `internal/infra/runtimebundle/production_options_test.go` | code_reference | 1 (87) |
| `internal/infra/runtimebundle/readiness_report_test.go` | code_reference | 1 (27) |
| `internal/infra/runtimebundle/reasoning_preservation_bootstrap_test.go` | code_reference, field_usage | 4 (39, 40, 42, 43) |
| `internal/infra/runtimebundle/secure_session_build_test.go` | code_reference | 3 (49, 195, 228) |
| `internal/infra/runtimebundle/secure_session_restart_e2e_test.go` | type_usage | 5 (105, 144, 180, 207, 219) |
| `internal/infra/runtimebundle/token_accounting_build_test.go` | code_reference | 1 (100) |
| `internal/stdhttp/authority_mount_test.go` | field_usage, type_usage | 2 (199, 211) |
| `internal/stdhttp/control_plane_mount_test.go` | field_usage, type_usage | 2 (39, 52) |
| `internal/stdhttp/dogfood_smoke_test.go` | field_usage | 1 (57) |
| `internal/stdhttp/identity_frontend_passthrough_test.go` | field_usage | 1 (113) |
| `internal/stdhttp/modelcatalog_diag_test.go` | field_usage | 3 (159, 165, 168) |
| `internal/stdhttp/mount_test.go` | field_usage | 5 (65, 102, 126, 230, 284) |
| `internal/stdhttp/outer_recovery_test.go` | field_usage | 4 (110, 114, 150, 154) |
| `internal/stdhttp/reasoning_preservation_http_harness_test.go` | field_usage | 1 (344) |
| `internal/stdhttp/server_identity_stack_test.go` | field_usage | 10 (83, 116, 134, 150, 168, 195, 201, 215, 221, 521) |
| `internal/stdhttp/stack_http_test.go` | field_usage | 8 (32, 34, 84, 86, 141, 143, 245, 247) |
| `pkg/lipruntime/reload_facade_external_test.go` | code_reference | 1 (260) |

#### Classification: `architecture_check` (3 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/archtest/guardrails_test.go` | comment_mention | 2 (242, 272) |
| `internal/archtest/reload_ownership_gates_test.go` | type_usage | 1 (656) |
| `internal/archtest/reload_ownership_scan.go` | code_reference, field_usage | 3 (560, 565, 574) |

### Compatibility `Build` (`runtimebundle.Build` / `lipruntime.Build`)

#### Classification: `production` (3 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/infra/runtimebundle/bootstrap_plan.go` | production_caller | 1 (209) |
| `internal/infra/runtimebundle/build.go` | declaration_runtimebundle | 1 (21) |
| `pkg/lipruntime/build.go` | declaration_lipruntime | 1 (44) |

#### Classification: `test_only` (63 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/core/runtime/secret_guard_real_feature_test.go` | caller | 1 (202) |
| `internal/infra/runtimebundle/auth_compose_test.go` | caller | 13 (57, 99, 151, 196, 241, 287, 317, 381, 438, 483, 532, 581, … (+1 more)) |
| `internal/infra/runtimebundle/auth_events_test.go` | caller | 7 (24, 43, 74, 98, 115, 134, 151) |
| `internal/infra/runtimebundle/auth_secret_leak_test.go` | caller | 1 (68) |
| `internal/infra/runtimebundle/authority_build_test.go` | caller | 7 (29, 46, 54, 79, 90, 116, 141) |
| `internal/infra/runtimebundle/authority_coord_registration_test.go` | caller | 6 (36, 85, 110, 172, 229, 249) |
| `internal/infra/runtimebundle/authority_evidence_test.go` | caller | 3 (112, 200, 242) |
| `internal/infra/runtimebundle/backend_closer_test.go` | caller | 4 (48, 105, 159, 210) |
| `internal/infra/runtimebundle/build_modelcatalog_test.go` | caller | 3 (49, 104, 161) |
| `internal/infra/runtimebundle/build_policy_test.go` | caller | 5 (52, 82, 117, 136, 157) |
| `internal/infra/runtimebundle/build_registry_required_test.go` | caller | 4 (20, 35, 50, 68) |
| `internal/infra/runtimebundle/build_test.go` | caller | 7 (36, 91, 123, 161, 195, 215, 242) |
| `internal/infra/runtimebundle/circuit_and_observer_test.go` | caller | 3 (24, 51, 71) |
| `internal/infra/runtimebundle/codex_catalog_access_test.go` | caller | 9 (70, 125, 177, 230, 276, 319, 369, 420, 470) |
| `internal/infra/runtimebundle/concurrency_authority_test.go` | caller | 3 (19, 54, 105) |
| `internal/infra/runtimebundle/control_plane_build_test.go` | caller | 1 (26) |
| `internal/infra/runtimebundle/control_plane_clock_test.go` | caller | 1 (51) |
| `internal/infra/runtimebundle/control_plane_closer_leak_test.go` | caller | 3 (51, 85, 122) |
| `internal/infra/runtimebundle/cursorsdk_close_rollback_test.go` | caller | 2 (92, 152) |
| `internal/infra/runtimebundle/cursorsdk_coexistence_integration_test.go` | caller | 1 (79) |
| `internal/infra/runtimebundle/dual_backend_test.go` | caller | 3 (39, 77, 106) |
| `internal/infra/runtimebundle/exclusive_registry_test.go` | caller | 1 (63) |
| `internal/infra/runtimebundle/interleaved_build_test.go` | caller | 4 (64, 86, 121, 138) |
| `internal/infra/runtimebundle/managed_postgres_runtime_integration_test.go` | caller | 1 (48) |
| `internal/infra/runtimebundle/managed_postgres_runtime_test.go` | caller | 4 (37, 70, 105, 136) |
| `internal/infra/runtimebundle/minimal_registry_test.go` | caller | 1 (42) |
| `internal/infra/runtimebundle/modelcatalog_override_set_test.go` | caller | 1 (110) |
| `internal/infra/runtimebundle/modelregistry_build_test.go` | caller | 10 (48, 75, 111, 161, 205, 263, 314, 352, 397, 438) |
| `internal/infra/runtimebundle/opencode_zen_registry_live_test.go` | caller | 1 (54) |
| `internal/infra/runtimebundle/pending_wire_default_test.go` | caller | 1 (21) |
| `internal/infra/runtimebundle/phase45_composition_effect_red_test.go` | caller | 2 (108, 205) |
| `internal/infra/runtimebundle/phase45_process_metrics_prom_red_test.go` | caller | 1 (40) |
| `internal/infra/runtimebundle/phase45_readiness_safe_red_test.go` | caller | 2 (56, 137) |
| `internal/infra/runtimebundle/phase45_terminal_wiring_red_test.go` | caller | 1 (41) |
| `internal/infra/runtimebundle/phase53_executable_generation_red_test.go` | caller | 1 (98) |
| `internal/infra/runtimebundle/phase55_executable_readiness_red_test.go` | caller | 2 (37, 90) |
| `internal/infra/runtimebundle/phase5_remediation_e2e_red_test.go` | caller | 2 (55, 140) |
| `internal/infra/runtimebundle/pooled_postgres_runtime_integration_test.go` | caller | 1 (210) |
| `internal/infra/runtimebundle/postgres_pool_build_abort_test.go` | caller | 1 (73) |
| `internal/infra/runtimebundle/process_services_test.go` | caller | 1 (225) |
| `internal/infra/runtimebundle/production_options_test.go` | caller | 1 (71) |
| `internal/infra/runtimebundle/readiness_report_test.go` | caller | 1 (17) |
| `internal/infra/runtimebundle/secret_guard_leak_test.go` | caller | 1 (93) |
| `internal/infra/runtimebundle/secure_session_build_test.go` | caller | 6 (35, 65, 86, 131, 172, 214) |
| `internal/infra/runtimebundle/secure_session_restart_e2e_test.go` | caller | 3 (75, 85, 95) |
| `internal/infra/runtimebundle/security_policy_test.go` | caller | 6 (97, 132, 176, 201, 225, 255) |
| `internal/infra/runtimebundle/snapshot_generation_test.go` | caller | 5 (21, 119, 166, 217, 248) |
| `internal/infra/runtimebundle/sqlite_closer_test.go` | caller | 1 (34) |
| `internal/infra/runtimebundle/terminal_work_test.go` | caller | 3 (47, 86, 137) |
| `internal/infra/runtimebundle/token_accounting_build_test.go` | caller | 6 (93, 177, 263, 308, 365, 410) |
| `internal/stdhttp/backend_closer_shutdown_test.go` | caller | 1 (79) |
| `internal/stdhttp/cursorsdk_composed_integration_test.go` | caller | 1 (80) |
| `internal/stdhttp/decode_admission_shared_test.go` | caller | 1 (24) |
| `internal/stdhttp/default_route_frontends_test.go` | caller | 1 (190) |
| `internal/stdhttp/registry_required_test.go` | caller | 1 (92) |
| `internal/stdhttp/server_test.go` | caller | 9 (66, 115, 159, 202, 248, 283, 321, 366, 428) |
| `internal/stdhttp/standard_wiring_roundtrip_test.go` | caller | 1 (63) |
| `pkg/lipruntime/build_test.go` | caller_lipruntime | 3 (75, 184, 214) |
| `pkg/lipruntime/compatibility_test.go` | caller_lipruntime | 2 (23, 47) |
| `pkg/lipruntime/observer_compat_test.go` | caller_lipruntime | 3 (25, 53, 76) |
| `pkg/lipruntime/phase55_facade_red_test.go` | caller_lipruntime | 1 (23) |
| `pkg/lipruntime/registration_compose_test.go` | caller_lipruntime | 13 (18, 66, 83, 107, 119, 148, 169, 196, 213, 246, 272, 299, … (+1 more)) |
| `pkg/lipruntime/reload_host_integration_test.go` | caller_lipruntime | 5 (19, 57, 75, 117, 175) |

#### Classification: `sample_external_module` (1 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `testdata/enterprise_module/main.go` | caller_lipruntime | 1 (133) |

### `RunWithRuntime`

#### Classification: `production` (9 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/core/config/diagnostics_posture.go` | comment_mention | 1 (37) |
| `internal/core/runtime/app.go` | comment_mention | 2 (62, 107) |
| `internal/infra/runtimebundle/built.go` | comment_mention | 1 (47) |
| `internal/stdhttp/handler.go` | comment_mention | 3 (21, 33, 146) |
| `internal/stdhttp/middleware.go` | comment_mention | 2 (16, 30) |
| `internal/stdhttp/mount_diagnostics.go` | comment_mention | 1 (37) |
| `internal/stdhttp/mount_metrics.go` | comment_mention | 1 (28) |
| `internal/stdhttp/mount_securesession.go` | comment_mention | 1 (28) |
| `internal/stdhttp/server.go` | comment_mention, declaration | 3 (23, 25, 90) |

#### Classification: `test_only` (4 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/infra/runtimebundle/ownership_inventory_test.go` | code_reference | 3 (132, 133, 134) |
| `internal/stdhttp/registry_required_test.go` | caller | 1 (99) |
| `internal/stdhttp/runwithruntime_posture_test.go` | caller, comment_mention | 2 (13, 39) |
| `internal/stdhttp/server_test.go` | caller, code_reference, comment_mention | 11 (77, 126, 166, 208, 254, 256, 288, 297, 325, 341, 370) |

### `RequestPlane`

#### Classification: `production` (12 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/infra/runtimebundle/compile_generation.go` | code_reference | 2 (108, 170) |
| `internal/infra/runtimebundle/generation_provider_resolver.go` | code_reference | 1 (62) |
| `internal/infra/runtimebundle/reload_host.go` | code_reference | 1 (157) |
| `internal/infra/runtimebundle/request_plane.go` | code_reference, comment_mention, declaration | 36 (33, 41, 46, 83, 86, 94, 102, 110, 113, 116, 119, 122, … (+24 more)) |
| `internal/infra/runtimehost/coordinator.go` | code_reference | 1 (741) |
| `internal/infra/runtimehost/generation.go` | code_reference, comment_mention | 3 (247, 248, 259) |
| `internal/infra/runtimehost/generation_dispatcher.go` | code_reference | 1 (47) |
| `internal/infra/runtimehost/generation_executor.go` | code_reference | 1 (113) |
| `internal/infra/runtimehost/lease.go` | code_reference, comment_mention | 3 (31, 32, 36) |
| `internal/infra/runtimehost/lifecycle_worker.go` | code_reference | 1 (85) |
| `internal/stdhttp/request_plane.go` | code_reference, comment_mention, composer | 3 (25, 139, 141) |
| `pkg/lipruntime/build.go` | code_reference | 1 (197) |

#### Classification: `test_only` (6 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/infra/runtimebundle/compile_generation_repair_test.go` | code_reference | 7 (40, 79, 173, 217, 238, 361, 380) |
| `internal/infra/runtimebundle/compile_generation_test.go` | code_reference | 3 (290, 402, 409) |
| `internal/infra/runtimebundle/frontend_feature_generation_test.go` | code_reference | 2 (301, 352) |
| `internal/infra/runtimebundle/initial_generation_test.go` | code_reference | 1 (94) |
| `internal/infra/runtimebundle/reload_backend_recomposition_test.go` | code_reference | 1 (221) |
| `internal/infra/runtimehost/request_plane_test.go` | code_reference | 2 (46, 68) |

#### Classification: `architecture_check` (1 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/archtest/guardrails_test.go` | comment_mention | 1 (268) |

### `requestPlaneAsBuilt`

#### Classification: `production` (1 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/stdhttp/request_plane.go` | caller, comment_mention, declaration | 3 (49, 139, 141) |

### `AttachReloadHost`

#### Classification: `production` (5 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `cmd/lipstd/command.go` | caller | 1 (286) |
| `cmd/lipstd/serve_rollback.go` | comment_mention | 1 (37) |
| `internal/infra/runtimebundle/bootstrap_plan.go` | comment_mention | 1 (74) |
| `internal/infra/runtimebundle/reload_host.go` | comment_mention, declaration | 2 (50, 54) |
| `pkg/lipruntime/build.go` | caller | 1 (102) |

#### Classification: `test_only` (2 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/infra/runtimebundle/reload_host_stream_recovery_test.go` | caller, code_reference, comment_mention | 3 (54, 58, 60) |
| `internal/stdhttp/runtime_config_reload_nodrop_cert_test.go` | caller, code_reference | 2 (345, 347) |

### Duplicate reload contract declarations / mappings

#### Classification: `production` (4 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/core/configreload/history.go` | code_reference | 1 (12) |
| `internal/core/configreload/model.go` | code_reference | 11 (9, 12, 13, 17, 24, 27, 28, 41, 42, 55, 68) |
| `pkg/lipruntime/reload.go` | code_reference | 12 (12, 15, 16, 20, 27, 30, 31, 44, 45, 58, 70, 86) |
| `pkg/lipruntime/reload_map.go` | code_reference | 6 (7, 26, 55, 66, 82, 105) |

#### Classification: `test_only` (1 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `internal/stdhttp/config_reload_management_ref_test.go` | code_reference | 3 (40, 46, 57) |

### Deprecated public `Options` fields / conversion

#### Classification: `production` (3 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `pkg/lipruntime/build.go` | comment_mention | 1 (163) |
| `pkg/lipruntime/normalize.go` | code_reference | 30 (23, 24, 37, 38, 40, 41, 43, 44, 46, 47, 50, 51, … (+18 more)) |
| `pkg/lipruntime/options.go` | code_reference, comment_mention | 12 (37, 38, 40, 41, 42, 44, 45, 48, 59, 61, 73, 76) |

#### Classification: `test_only` (4 files)

| File | Roles | Hit lines |
| --- | --- | ---: |
| `pkg/lipruntime/build_test.go` | code_reference | 1 (81) |
| `pkg/lipruntime/observer_compat_test.go` | code_reference | 2 (29, 55) |
| `pkg/lipruntime/phase55_facade_red_test.go` | code_reference | 1 (37) |
| `pkg/lipruntime/registration_compose_test.go` | code_reference | 21 (85, 101, 109, 112, 121, 122, 150, 151, 174, 198, 202, 206, … (+9 more)) |

## 5. Phase 9 comparison table (before metrics)

Copy/compare against this table in task 9.5 release evidence. Empty after-columns are intentional.

| Metric | Before (`efe46249`) | After (final SHA) | Δ | Notes |
| --- | ---: | ---: | ---: | --- |
| `runtimebundle` non-test lines | 9898 |  |  |  |
| `runtimehost` non-test lines | 3056 |  |  |  |
| `stdhttp` root non-test lines | 2106 |  |  |  |
| `stdhttp/...` recursive non-test lines | 4648 |  |  | optional comparator |
| `cmd/lipstd` non-test lines | 935 |  |  |  |
| `pkg/lipruntime` non-test lines | 1037 |  |  |  |
| Named-surface sum (root stdhttp) | 17032 |  |  | Req ≥800 (11.5) |
| `runtimebundle` fan-out | 82 |  |  |  |
| `runtimehost` fan-out | 4 |  |  |  |
| `stdhttp` fan-out | 20 |  |  |  |
| `pkg/lipapi` exported symbols | 319 |  |  |  |
| `pkg/lipsdk` exported symbols | 39 |  |  | root only |
| `pkg/lipruntime` exported symbols | 46 |  |  |  |
| `coordinator.go` lines | 797 |  |  | final ≤300 |
| `generation.go` lines | 575 |  |  | final ≤400 |
| `candidate_compile.go` lines | 440 |  |  | final ≤350 |
| `pkg/lipruntime/build.go` lines | 367 |  |  | final ≤150 |
| Production `requestPlaneAsBuilt` | present |  |  | must be absent |
| Production `RunWithRuntime` callers | 0 (decl remains) |  |  | decl must be deleted |
| Production `runtimebundle.Build` callers | 1 (`bootstrap_plan.go`) |  |  | must be deleted with legacy path |
| Duplicate reload contract packages | `configreload` + `lipruntime` + `reload_map.go` |  |  | single contract |
| `BenchmarkManager_AcquireRelease` median ns/op (RunA) | 26.20 |  |  | benchstat |
| `BenchmarkManager_Publish` median ns/op (RunA) | 454.7 |  |  | benchstat |
| `BenchmarkGenerationDispatcher_AcquireLease` median ns/op (RunA) | 417.6 |  |  | benchstat |
| `BenchmarkCandidateCompilation` median ns/op (RunA) | 4.815e6 |  |  | ≤10% (13.7) |
| Successful reload bench | GAP |  |  | add before 9.5 |
| No-op reload bench | GAP |  |  | add before 9.5 |

## 6. Compatibility inventory (explicit)

| Old path / concept | Production status at baseline | Deletion depends on |
| --- | --- | --- |
| `runtimebundle.Built` | Live aggregate; stdhttp mounts depend on it | Phases 3–4 |
| `runtimebundle.Build` | Live; one production caller via bootstrap legacy path | Phases 3–4 |
| `pkg/lipruntime.Build` | Live public facade entrypoint | Phases 8–9 (shape may remain as thin facade) |
| `stdhttp.RunWithRuntime` | Declaration + tests; no production caller | Phase 4 |
| `RequestPlane` broad bag | Live generation publish surface | Phases 3–5 |
| `requestPlaneAsBuilt` | Live adapter stdhttp ← RequestPlane | Phase 3 |
| `AttachReloadHost` two-step host attach | Live (`lipstd`, `lipruntime`) | Phase 5 |
| Mirrored reload types + `reload_map.go` | Live public mirror | Phase 2 |
| Deprecated Options fields + `normalize.go` legacy conversion | Live quarantine path | Phase 8 / public major |

## 7. Known measurement limitations

1. **Host noise:** 2-vCPU shared AMD EPYC slice; compile bench shows multi-ms jitter and one RunB outlier. Phase 9 must use `benchstat`, not raw median deltas.
2. **`benchstat` unavailable** in this environment without installing a tool binary; raw `-count=10` dual-run artifacts are the authoritative baseline.
3. **No successful/no-op reload benchmarks** exist yet — recorded as explicit gaps, not estimated.
4. **Fan-in excludes test imports** (`TestImports` / `XTestImports`), matching arch-report production graph semantics.
5. **Export counts for `pkg/lipsdk` are root-package only**, matching `make arch-report`; subpackage APIs are out of that table.
6. **Comment-only mentions** are inventoried but are not migration callers; deletion gates must key off production code roles.
7. **Feature branch SHA differs** from reviewed baseline by approved Kiro metadata only; all production metrics are pinned to `efe46249`, not the feature worktree.
8. **`make bench` full suite** includes many unrelated packages; task-relevant baselines are the four named benchmarks above. Full-suite timing is a validation smoke, not the comparison series.

## 8. Validation performed while authoring / repairing

Commands run for this evidence (behavior-neutral; no production edits):

- `python3 baseline/capture_baseline.py --help`
- `python3 baseline/capture_baseline.py --repo-root "$BASELINE_WORKTREE" --reviewed-sha efe4624909cea318c7211d5cb3734059d3210802 --out-dir "$OUT_DIR"`
- `python3 -m json.tool baseline/migration-inventory.json >/dev/null`
- `python3 -m json.tool baseline/supplemental-metrics.json >/dev/null`
- Focused self-check: regenerated metrics match section 2 tables; inventory hit counts match section 4.1; no `production_caller` on non-production files
- `git diff --check` / `git status --short`
- Hermes previously verified `make arch-report` byte-match vs `arch-report-efe46249.md` and full `make bench`; those gates were not re-run for this repair because benchmark/arch-report raw artifacts were not modified

No production code, public API, budget, or `tasks.md` checkbox was modified by this task.
