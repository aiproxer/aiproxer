# Baseline Evidence: Post-Extension-Correction Architecture & Seam Baseline (Task 1.1)

- **Spec:** `pre-oss-core-slimming`
- **Task:** 1.1 (Capture the exact post-extension-correction baseline)
- **Date:** 2026-09-02
- **Base Commit SHA:** `ae69b2f9aa63ed48f677e81f5520a26a8eb4e9d6`
- **Branch:** `feat/pre-oss-core-slimming`
- **Worktree:** `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-pre-oss-core-slimming`
- **Host OS:** Microsoft Windows NT 10.0.19045.0 (Windows 10 Pro 64-bit amd64)
- **CPU:** AMD Ryzen 7 5800X 8-Core Processor (8 physical cores, 16 logical processors, 3801 MHz max clock)
- **Go Version:** `go version go1.26.6 windows/amd64`
- **GOMAXPROCS:** `12`
- **Power Scheme:** `0e0ed2d3-ff6e-4391-908e-0f050851b6ce` (Najwyższa wydajność / Ultimate Performance)
- **Scope Status:** Pre-implementation architecture and benchmark baseline. Does NOT claim Issue #394 performance neutrality; establishes the durable baseline for all subsequent migration waves and Task 8.2 verification.

---

## 1. Environment & Base Commit Integrity

### 1.1 Git Status Cleanliness
- **Command:** `git status --short`
- **Exit Code:** `0`
- **Output:** *(clean working tree prior to baseline capture)*

### 1.2 Exact Base HEAD
- **Command:** `git rev-parse HEAD`
- **Exit Code:** `0`
- **Output:**
  ```
  ae69b2f9aa63ed48f677e81f5520a26a8eb4e9d6
  ```

### 1.3 Recent Commit History on Base
- **Command:** `git log -n 5 --oneline`
- **Exit Code:** `0`
- **Output:**
  ```
  ae69b2f9 feat(quality): add parallel multi-module linting to local pre-commit and quality gates (#574)
  107fc62b fix(quality): clear repository and connector modules lint debt (#570)
  024fdb94 feat(test): ratchet tagged QA hotspot cost (#568)
  b7bd8ddb spec: align downstream cache affinity with lean core architecture (#569)
  13392a21 spec: complete downstream prompt-cache affinity hinting (#565)
  ```

---

## 2. Generated Code & Architecture Verification Gates

### 2.1 Generated Feature Planes Currency Check
- **Command:** `go run ./scripts/generate-feature-planes.go -check`
- **Exit Code:** `0`
- **Output:**
  ```
  generated feature planes file is up to date.
  ```

### 2.2 Architecture Report Summary (`make arch-report`)
- **Command:** `make arch-report`
- **Exit Code:** `0`
- **Active Migration Wave:** `W5c_Residual` (baseline)
- **Active Forbidden Mirror Violations:** `0` (zero-tolerance)
- **Total Hand-Authored Mirror Instances Across Repository:** `0`
- **Runtime Convergence Parallel Composition Paths:** `0` (empty allowlist)
- **Public Contract Exported Symbols:**
  - `pkg/lipapi`: `472`
  - `pkg/lipsdk`: `73`

---

## 3. Standard Plane Catalog & Manifest Inventory

- **Canonical Declaration Manifest:** `pkg/lipsdk/feature/plane_manifest.go`
- **Generated Typed Storage & Adapters:** `pkg/lipsdk/feature/plane_generated.go`
- **Total Standard Plane Count:** **25 planes**

| # | Plane ID | Wave Family | Multiplicity | Exported Contract Type |
| :-: | :--- | :--- | :--- | :--- |
| 1 | `attempt_transforms` | W3_RequestShaping (RequestShaping) | ordered | `AttemptTransform` |
| 2 | `compaction_observers` | W5a_GuardsCompaction (GuardsCompaction) | ordered | `CompactionObserver` |
| 3 | `compaction_preservers` | W5a_GuardsCompaction (GuardsCompaction) | ordered | `CompactionPreserver` |
| 4 | `completion_gates` | W3_RequestShaping (RequestShaping) | ordered | `CompletionGate` |
| 5 | `local_turn_handlers` | W5b_LocalTurnTerminal (LocalTurnTerminal) | ordered | `LocalTurnHandler` |
| 6 | `pre_request_handlers` | W3_RequestShaping (RequestShaping) | ordered | `PreRequestHandler` |
| 7 | `raw_capture_sinks` | W2_Observers (Observers) | ordered | `RawCaptureSink` |
| 8 | `request_part_hooks` | W1_HookBus (HookBus) | ordered | `RequestPartHook` |
| 9 | `request_transforms` | W3_RequestShaping (RequestShaping) | ordered | `RequestTransform` |
| 10 | `response_part_hooks` | W1_HookBus (HookBus) | ordered | `ResponsePartHook` |
| 11 | `route_hint_providers` | W3_RequestShaping (RequestShaping) | ordered | `RouteHintProvider` |
| 12 | `secret_guards` | W5a_GuardsCompaction (GuardsCompaction) | ordered | `SecretGuard` |
| 13 | `session_openers` | W3_RequestShaping (RequestShaping) | ordered | `SessionOpener` |
| 14 | `stream_observer_factories` | W2_Observers (Observers) | ordered | `StreamObserverFactory` |
| 15 | `submit_hooks` | W1_HookBus (HookBus) | ordered | `SubmitHook` |
| 16 | `terminal_decision_provider` | W5b_LocalTurnTerminal (LocalTurnTerminal) | exclusive | `TerminalDecisionProvider` |
| 17 | `tool_call_finalization_max_args_bytes` | W4_Tools (Tools) | ordered | `ToolCallFinalizationMaxArgsBytes` |
| 18 | `tool_call_finalizers` | W4_Tools (Tools) | ordered | `ToolCallFinalizer` |
| 19 | `tool_call_policies` | W4_Tools (Tools) | ordered | `ToolCallPolicy` |
| 20 | `tool_catalog_filters` | W4_Tools (Tools) | ordered | `ToolCatalogFilter` |
| 21 | `tool_reactors` | W1_HookBus (HookBus) | ordered | `ToolReactor` |
| 22 | `traffic_observers` | W2_Observers (Observers) | ordered | `TrafficObserver` |
| 23 | `traffic_redactors` | W2_Observers (Observers) | ordered | `TrafficRedactor` |
| 24 | `usage_observers` | W2_Observers (Observers) | ordered | `UsageObserver` |
| 25 | `workspace_resolvers` | W3_RequestShaping (RequestShaping) | ordered | `WorkspaceResolver` |

---

## 4. Codebase Line Counts & Architectural Budgets

### 4.1 Recursive Non-Test Line Counts (`CountNonTestGoLines`)
Line counts measured using the repository-standard `archtest.CountNonTestGoLines` physical line-scanning algorithm (`bufio.Scanner` physical newline count across all non-test `.go` files).

- **Measurement Command / Method:**
  ```powershell
  pwsh -NoProfile -Command '
  $scopes = @("internal/core", "internal/infra/runtimebundle", "internal/stdhttp", "internal/pluginreg", "cmd/lipstd", "pkg/lipruntime")
  foreach ($s in $scopes) {
      $all = Get-ChildItem -Path $s -Recurse -Filter "*.go" -File
      $nonTest = $all | Where-Object { $_.Name -notlike "*_test.go" }
      $ntLines = ($nonTest | ForEach-Object { [System.IO.File]::ReadAllLines($_.FullName).Count } | Measure-Object -Sum).Sum
      $totLines = ($all | ForEach-Object { [System.IO.File]::ReadAllLines($_.FullName).Count } | Measure-Object -Sum).Sum
      Write-Host "$s: NonTestFiles=$($nonTest.Count), NonTestLines=$ntLines, TotalGoFiles=$($all.Count), TotalGoLines=$totLines"
  }'
  ```

| Scope | Non-Test Go Files | Non-Test Lines | Total Go Files (inc. Tests) | Total Go Lines (inc. Tests) | Current Budget Ceiling | Headroom |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: |
| `internal/core` | 624 | **94,777** | 1,466 | 283,174 | 95,095 (`LineBudgets`) | 318 |
| `internal/infra/runtimebundle` | 94 | **12,796** | 310 | 69,537 | 12,933 (`PackageTreeBudgets` / `LineBudgets`) | 137 |
| `internal/stdhttp` | 55 | **6,693** | 148 | 28,527 | 6,693 (`PackageTreeBudgets` / `LineBudgets`) | 0 |
| `internal/pluginreg` | 9 | **1,146** | 22 | 2,379 | 1,174 (`LineBudgets`) | 28 |
| `cmd/lipstd` | 12 | **962** | 33 | 4,337 | 979 (`PackageTreeBudgets` / `LineBudgets`) | 17 |
| `pkg/lipruntime` | 9 | **694** | 30 | 4,036 | 720 (`PackageTreeBudgets` / `LineBudgets`) | 26 |

### 4.2 Target Slimming Packages (Current Line Breakdown)

| Package | Non-Test Go Files | Non-Test Lines | Test Go Files | Test Lines | Other Files | Total Package Lines | Total Package Files |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| `internal/core/toolcallrepair` | 18 | **2,795** | 22 | 3,443 | 0 (0 lines) | 6,238 | 40 |
| `internal/core/secretguard` | 10 | **942** | 12 | 2,047 | 1 (29 lines md) | 3,018 | 23 |
| `internal/core/compactiondetect` | 4 | **1,206** | 5 | 1,306 | 0 (0 lines) | 2,512 | 9 |
| `internal/plugins/features/toolcallrepair` | 2 | **254** | 2 | 182 | 0 (0 lines) | 436 | 4 |
| `internal/plugins/features/secretguard` | 6 | **902** | 7 | 1,636 | 0 (0 lines) | 2,538 | 13 |
| **Combined Core Subsystems to Move / Invert** | **32** | **4,943** | **39** | **6,796** | **1 (29 lines)** | **11,768** | **72** |
| **Grand Total Across All 5 Packages** | **40** | **6,099** | **48** | **8,614** | **1 (29 lines)** | **14,742** | **89** |

---

## 5. Direct-Import Census for `internal/plugins/features/*`

### 5.1 From `internal/core`
- **Direct Imports:** **0** files (0 direct imports across all 624 non-test Go files in `internal/core`).
- **Status:** Baseline already satisfies zero direct feature imports from core.

### 5.2 From `internal/infra/runtimebundle`
Exactly **5 production files** directly import concrete feature packages:

1. `internal/infra/runtimebundle/compaction_continuity_result_adapter.go`:
   - Line 9: `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/resultmerge`
2. `internal/infra/runtimebundle/composition_root.go`:
   - Line 13: `featuresg "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard"`
3. `internal/infra/runtimebundle/reasoning_preservation_compression_options.go`:
   - Line 4: `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation`
4. `internal/infra/runtimebundle/reasoning_preservation_compression.go`:
   - Line 9: `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation`
5. `internal/infra/runtimebundle/secret_guard_runtime.go`:
   - Line 13: `featuresg "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard"`

*Migration Goal:* All 5 concrete feature imports in `internal/infra/runtimebundle` will be extracted into dedicated composition adapters (`internal/infra/reasoningcompose`, `internal/infra/secretguardcompose`, etc.), achieving 0 direct feature imports from runtimebundle.

### 5.3 From `internal/standardplugins`
Exactly **4 production files** import concrete feature packages:

1. `internal/standardplugins/features_install.go`:
   - Lines 7–24: imports 18 feature packages for canonical feature registration table.
2. `internal/standardplugins/reasoning_preservation_inject.go`:
   - Line 10: `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation`
3. `internal/standardplugins/standard_table.go`:
   - Lines 7–24: imports 18 feature packages for standard bundle table.
4. `internal/standardplugins/tool_call_repair_inject.go`:
   - Line 5: `github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair`

---

## 6. Comprehensive Package & File Inventories for Migration Targets

### 6.1 `internal/core/toolcallrepair` (To be migrated to `internal/plugins/features/toolcallrepair`)
Total: 40 files (18 PROD, 22 TEST), 6,238 lines.

| File Path | Kind | Lines | Role / Architectural Purpose |
| :--- | :---: | ---: | :--- |
| `args_detach_test.go` | TEST | 20 | Detached argument buffer isolation tests |
| `catalog_index.go` | PROD | 62 | Indexed tool definition catalog for schema resolution |
| `catalog_index_test.go` | TEST | 131 | Catalog index resolution and indexing unit tests |
| `diagnostic.go` | PROD | 27 | Structured repair diagnostic events and error records |
| `diagnostic_test.go` | TEST | 56 | Repair diagnostics and structured event verification |
| `doc.go` | PROD | 13 | Package documentation and domain overview |
| `engine.go` | PROD | 328 | Core repair orchestration engine and dispatch |
| `engine_contract_test.go` | TEST | 276 | Repair engine contract and lifecycle tests |
| `engine_repair_test.go` | TEST | 275 | End-to-end tool call repair scenarios and edge cases |
| `export_object_fields_test.go` | TEST | 5 | Test export bridge for private object fields |
| `export_test.go` | TEST | 31 | Package internal testing export bindings |
| `finalizer.go` | PROD | 128 | Tool call finalizer implementation implementing SDK contract |
| `finalizer_sdk_test.go` | TEST | 288 | SDK finalizer plane integration and order tests |
| `fuzz_test.go` | TEST | 169 | Fuzzing suite for malformed and adversarial JSON tails |
| `hardening_test.go` | TEST | 155 | Robustness tests against pathological inputs |
| `json_scanner.go` | PROD | 147 | Streaming JSON token scanner for partial argument buffers |
| `json_scanner_test.go` | TEST | 58 | JSON token scanner unit tests |
| `json_tail.go` | PROD | 388 | Incremental tail-repair parser for truncated JSON structures |
| `main_test.go` | TEST | 14 | Test package initialization and logging hooks |
| `normalize.go` | PROD | 20 | JSON schema normalization helpers |
| `ordered_json.go` | PROD | 132 | Deterministic key-preserving JSON AST container |
| `ordered_json_bench_test.go` | TEST | 138 | Benchmark suite for ordered JSON parsing and manipulation |
| `ordered_json_depth_test.go` | TEST | 119 | Deeply nested JSON structure stress tests |
| `pending_value.go` | PROD | 44 | Deferred argument value accumulator |
| `safe_tail_bench_test.go` | TEST | 50 | Benchmark suite for tail repair throughput |
| `safe_tail_test.go` | TEST | 240 | Safe tail completion and syntax error recovery tests |
| `schema_bench_test.go` | TEST | 137 | Benchmark suite for schema compilation and validation |
| `schema_cache.go` | PROD | 184 | Thread-safe compiled JSON schema cache |
| `schema_cache_test.go` | TEST | 119 | Concurrency and cache hit/miss tests for schema cache |
| `schema_compile.go` | PROD | 157 | JSON schema validator compilation and optimization |
| `schema_compile_test.go` | TEST | 324 | JSON schema compilation table-driven tests |
| `schema_error.go` | PROD | 68 | Schema-guided structural error classification |
| `schema_limits.go` | PROD | 51 | Resource ceiling and depth limit enforcement |
| `schema_prescan.go` | PROD | 236 | Pre-parsing scanner for argument boundaries |
| `schema_repair.go` | PROD | 646 | Schema-guided argument tree repair and default synthesis |
| `schema_repair_test.go` | TEST | 274 | Schema repair rule table-driven tests |
| `schema_validate.go` | PROD | 18 | Quick-path argument validation against compiled schemas |
| `schema_validate_test.go` | TEST | 103 | Schema validator unit tests |
| `shape_guard.go` | PROD | 146 | Structural type and shape invariant enforcer |
| `shape_guard_test.go` | TEST | 461 | Shape guard invariant regression and adversarial suite |

### 6.2 `internal/core/secretguard` (To be migrated to `internal/plugins/features/secretguard`)
Total: 23 files (10 PROD, 12 TEST, 1 OTHER), 3,018 lines.

| File Path | Kind | Lines | Role / Architectural Purpose |
| :--- | :---: | ---: | :--- |
| `aho_corasick.go` | PROD | 89 | Multi-pattern Aho-Corasick string searching automaton |
| `catalog.go` | PROD | 175 | Secret pattern and category catalog definitions |
| `catalog_test.go` | TEST | 121 | Catalog entry and category parsing tests |
| `compose.go` | PROD | 19 | Composition helper for building active matcher sets |
| `compose_test.go` | TEST | 74 | Guard composition and matcher assembly tests |
| `doc.go` | PROD | 9 | Package overview and security documentation |
| `export_test.go` | TEST | 6 | Package internal testing export bindings |
| `frontend_conformance_test.go` | TEST | 404 | End-to-end secret redaction across frontend protocols |
| `frontend_matrix_test.go` | TEST | 24 | Multi-protocol frontend integration matrix tests |
| `known_prefix.go` | PROD | 58 | High-confidence vendor prefix detector (sk-, ghp-, etc.) |
| `known_prefix_test.go` | TEST | 82 | Prefix detection accuracy and false-positive tests |
| `matcher.go` | PROD | 192 | Core secret matcher engine executing Aho-Corasick + regex |
| `matcher_bench_test.go` | TEST | 92 | Performance benchmarks for secret matching hot paths |
| `matcher_fuzz_test.go` | TEST | 52 | Fuzzing suite for matcher input streams |
| `matcher_test.go` | TEST | 499 | Table-driven secret matching and redaction suite |
| `popular_env.go` | PROD | 105 | Standard environment variable name catalog |
| `popular_env.md` | OTHER | 29 | Documentation of popular environment variable patterns |
| `popular_env_test.go` | TEST | 49 | Popular environment variable recognition tests |
| `proxy_inventory.go` | PROD | 117 | Proxy configuration and credential scanner |
| `sdk_adapter.go` | PROD | 61 | Adapter bridging core matcher to lipsdk feature plane |
| `source.go` | PROD | 117 | Secret source provider and environment loader |
| `source_catalog_test.go` | TEST | 573 | Source catalog resolution and filtering unit tests |
| `source_isolation_test.go` | TEST | 71 | Security isolation tests (multi-user zero environment reads) |

### 6.3 `internal/core/compactiondetect` (To be inverted via `pkg/lipsdk/compaction` interface)
Total: 9 files (4 PROD, 5 TEST), 2,512 lines.

| File Path | Kind | Lines | Role / Architectural Purpose |
| :--- | :---: | ---: | :--- |
| `content_free_test.go` | TEST | 67 | Content-free detection privacy and redaction tests |
| `detector.go` | PROD | 715 | Core conversation compaction detector and state tracker |
| `doc.go` | PROD | 12 | Package documentation and algorithm description |
| `heuristic.go` | PROD | 173 | Heuristic scoring for compaction boundary candidates |
| `heuristic_test.go` | TEST | 196 | Compaction heuristic calculation and threshold tests |
| `preview_test.go` | TEST | 160 | Preview response compaction evaluation tests |
| `rules.go` | PROD | 306 | Compaction recognition and trigger rules |
| `rules_test.go` | TEST | 619 | Rule-matching table-driven tests |
| `transactions_test.go` | TEST | 264 | Transactional state consistency and rollback tests |

### 6.4 `internal/plugins/features/toolcallrepair` (Current Feature Shell)
Total: 4 files (2 PROD, 2 TEST), 436 lines.

| File Path | Kind | Lines | Role / Architectural Purpose |
| :--- | :---: | ---: | :--- |
| `config.go` | PROD | 245 | YAML configuration decoding and validation for tool repair |
| `config_acceptance_test.go` | TEST | 140 | Configuration acceptance and default loading tests |
| `defaults_equality_test.go` | TEST | 42 | Config defaults parity verification |
| `doc.go` | PROD | 9 | Feature plugin package documentation |

### 6.5 `internal/plugins/features/secretguard` (Current Feature Shell)
Total: 13 files (6 PROD, 7 TEST), 2,538 lines.

| File Path | Kind | Lines | Role / Architectural Purpose |
| :--- | :---: | ---: | :--- |
| `bundle.go` | PROD | 17 | Feature bundle construction entry point |
| `config.go` | PROD | 218 | YAML configuration model and parser |
| `config_test.go` | TEST | 226 | Config parsing and validation tests |
| `fuzz_test.go` | TEST | 94 | Fuzz testing for JSON payload redactor |
| `generation_uniqueness_test.go` | TEST | 34 | Unique feature contribution validation |
| `guard.go` | PROD | 141 | Feature plane SecretGuard implementation |
| `guard_test.go` | TEST | 898 | Comprehensive feature guard execution tests |
| `import_boundary_test.go` | TEST | 40 | Package import boundary enforcement tests |
| `json_redact.go` | PROD | 215 | JSON-aware secret value redactor |
| `json_redact_test.go` | TEST | 231 | JSON redaction unit tests |
| `runtime_compose.go` | PROD | 81 | Generation-time runtime composition builder |
| `runtime_compose_test.go` | TEST | 113 | Runtime composition and bundle binding tests |
| `scan.go` | PROD | 230 | In-flight payload scanner and redactor |

---

## 7. Extension Plane Seam 10-Sample Benchmark Suite

### 7.1 Execution Parameters & Hardware Specification
- **Execution Command:**
  ```powershell
  go test -run '^$' -bench 'Benchmark.*(Completion|Traffic|Secret|Compaction|Terminal)' -benchmem -count=10 ./internal/core/extensions/...
  ```
- **Total Samples:** Exactly 10 samples for all 31 benchmark cases (310 benchmark records).
- **Execution Elapsed Time:** 366.984s
- **Host CPU:** AMD Ryzen 7 5800X 8-Core Processor (16 threads)
- **Go Version:** `go version go1.26.6 windows/amd64`
- **GOMAXPROCS:** `12`
- **Power Profile:** Ultimate Performance (`0e0ed2d3-ff6e-4391-908e-0f050851b6ce`)

### 7.2 10-Sample Statistical Summary Table

| Benchmark Case | Samples | Mean ns/op | Min ns/op | Max ns/op | Median ns/op | B/op | allocs/op | Baseline Allocation Invariant |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :--- |
| `BenchmarkCompletionGates_Populated` | 10 | 27.01 | 26.59 | 28.09 | 26.94 | 32 | 1 | Defensive Clone (32 B/op, 1 alloc/op) |
| `BenchmarkCompletionGates_Empty` | 10 | 4.81 | 4.76 | 4.84 | 4.81 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkCompletionGates_NilSnapshot` | 10 | 3.78 | 3.77 | 3.81 | 3.77 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkCompletionGatesFromContext_Populated` | 10 | 31.62 | 31.30 | 31.96 | 31.63 | 32 | 1 | Defensive Clone (32 B/op, 1 alloc/op) |
| `BenchmarkCompletionGatesFromContext_Empty` | 10 | 9.96 | 9.90 | 10.03 | 9.96 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkCompletionGatesFromContext_NilContextFallback` | 10 | 32.70 | 32.34 | 33.15 | 32.60 | 32 | 1 | Defensive Clone (32 B/op, 1 alloc/op) |
| `BenchmarkCompletionGatesFromContext_nilFallback_empty` | 10 | 6.37 | 6.32 | 6.43 | 6.36 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkCompletionGatesFromContext_fallbackNilGates_empty` | 10 | 11.21 | 11.14 | 11.27 | 11.23 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkCompletionGatesFromContext_withGates` | 10 | 30.80 | 30.45 | 31.49 | 30.68 | 16 | 1 | Defensive Clone (16 B/op, 1 alloc/op) |
| `BenchmarkTrafficPortBundle_Populated` | 10 | 31.73 | 31.47 | 32.08 | 31.63 | 32 | 1 | Defensive Clone (32 B/op, 1 alloc/op) |
| `BenchmarkTrafficPortBundle_Empty` | 10 | 9.31 | 9.28 | 9.39 | 9.30 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkTrafficPortBundle_NilSnapshot` | 10 | 6.16 | 6.12 | 6.24 | 6.14 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkTrafficObserver_Populated` | 10 | 0.88 | 0.87 | 0.89 | 0.87 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkTrafficRedactors_Populated` | 10 | 26.66 | 26.41 | 26.93 | 26.64 | 32 | 1 | Defensive Clone (32 B/op, 1 alloc/op) |
| `BenchmarkTrafficRedactors_Empty` | 10 | 5.19 | 5.17 | 5.20 | 5.19 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkTrafficRedactors_NilSnapshot` | 10 | 3.75 | 3.74 | 3.77 | 3.75 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkSecretGuardPlane_Populated` | 10 | 37.08 | 36.65 | 37.37 | 37.10 | 32 | 1 | Defensive Clone (32 B/op, 1 alloc/op) |
| `BenchmarkSecretGuardPlane_Empty` | 10 | 14.59 | 14.56 | 14.64 | 14.59 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkSecretGuardPlane_NilSnapshot` | 10 | 5.81 | 5.80 | 5.82 | 5.82 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkSecretGuardExecutionPlane_Populated` | 10 | 13.21 | 13.15 | 13.26 | 13.22 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkSecretGuardExecutionPlane_Empty` | 10 | 13.23 | 13.14 | 13.34 | 13.21 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkSecretGuardExecutionPlane_NilSnapshot` | 10 | 1.50 | 1.49 | 1.51 | 1.50 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkCompactionObservers_Populated` | 10 | 23.81 | 23.64 | 24.02 | 23.81 | 16 | 1 | Defensive Clone (16 B/op, 1 alloc/op) |
| `BenchmarkCompactionObservers_Empty` | 10 | 4.77 | 4.73 | 4.81 | 4.77 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkCompactionObservers_NilSnapshot` | 10 | 3.75 | 3.74 | 3.76 | 3.76 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkCompactionPreservers_Populated` | 10 | 24.00 | 23.84 | 24.43 | 23.93 | 16 | 1 | Defensive Clone (16 B/op, 1 alloc/op) |
| `BenchmarkCompactionPreservers_Empty` | 10 | 4.76 | 4.71 | 4.81 | 4.76 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkCompactionPreservers_NilSnapshot` | 10 | 3.74 | 3.73 | 3.76 | 3.73 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkTerminalDecisionProvider_Populated` | 10 | 5.25 | 5.23 | 5.26 | 5.25 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkTerminalDecisionProvider_Empty` | 10 | 4.20 | 4.18 | 4.21 | 4.20 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |
| `BenchmarkTerminalDecisionProvider_NilSnapshot` | 10 | 3.74 | 3.73 | 3.77 | 3.74 | 0 | 0 | Zero Alloc (0 B/op, 0 allocs/op) |

### 7.3 Baseline Allocation Invariants
- **Total Cases Evaluated:** 31 distinct benchmark cases (10 samples each = 310 benchmark records).
- **Zero-Allocation Invariants:** Exactly **24 of 31** benchmark cases operate with zero allocations (`0 B/op`, `0 allocs/op`).
- **Defensive-Cloning Invariants:** Exactly **7 of 31** populated cases execute defensive cloning with exactly 1 allocation (`16 B/op` or `32 B/op`).
- **Pre-Implementation Characterization:** Establishes the authoritative memory allocation baseline for future candidate comparisons.

---

## 8. Verbatim Raw Benchmark Output

```
goos: windows
goarch: amd64
pkg: github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions
cpu: AMD Ryzen 7 5800X 8-Core Processor
BenchmarkCompletionGates_Populated-12                            	44261332	        27.12 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGates_Populated-12                            	45142820	        27.10 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGates_Populated-12                            	44876420	        27.04 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGates_Populated-12                            	46065258	        26.59 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGates_Populated-12                            	44653820	        26.84 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGates_Populated-12                            	45549784	        28.09 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGates_Populated-12                            	45246138	        27.05 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGates_Populated-12                            	45460572	        26.59 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGates_Populated-12                            	45079564	        26.84 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGates_Populated-12                            	45717420	        26.80 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGates_Empty-12                                	251243497	         4.777 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_Empty-12                                	248357682	         4.811 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_Empty-12                                	249511269	         4.789 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_Empty-12                                	246567015	         4.835 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_Empty-12                                	250385646	         4.804 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_Empty-12                                	252090354	         4.762 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_Empty-12                                	249105865	         4.810 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_Empty-12                                	250251032	         4.821 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_Empty-12                                	249277562	         4.817 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_Empty-12                                	248878387	         4.841 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_NilSnapshot-12                          	317826742	         3.772 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_NilSnapshot-12                          	316872426	         3.775 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_NilSnapshot-12                          	315661048	         3.779 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_NilSnapshot-12                          	309161538	         3.810 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_NilSnapshot-12                          	318093643	         3.771 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_NilSnapshot-12                          	313961966	         3.785 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_NilSnapshot-12                          	315494236	         3.775 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_NilSnapshot-12                          	314532007	         3.808 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_NilSnapshot-12                          	317922313	         3.766 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_NilSnapshot-12                          	319929018	         3.774 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_Populated-12                 	37063461	        31.72 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_Populated-12                 	38502508	        31.37 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_Populated-12                 	37967354	        31.42 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_Populated-12                 	38011491	        31.89 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_Populated-12                 	37784088	        31.61 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_Populated-12                 	37785397	        31.55 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_Populated-12                 	37868348	        31.70 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_Populated-12                 	36694890	        31.96 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_Populated-12                 	37945863	        31.66 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_Populated-12                 	37733000	        31.30 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_Empty-12                     	120599112	         9.959 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_Empty-12                     	120282771	         9.965 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_Empty-12                     	120777067	         9.940 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_Empty-12                     	100000000	        10.00 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_Empty-12                     	120768960	         9.947 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_Empty-12                     	120278215	         9.960 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_Empty-12                     	100000000	        10.03 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_Empty-12                     	120303717	         9.960 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_Empty-12                     	121029976	         9.910 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_Empty-12                     	121077577	         9.901 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_NilContextFallback-12        	35599645	        32.68 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_NilContextFallback-12        	37219342	        32.80 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_NilContextFallback-12        	35293909	        32.50 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_NilContextFallback-12        	35777320	        33.15 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_NilContextFallback-12        	36532682	        32.43 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_NilContextFallback-12        	36916942	        33.12 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_NilContextFallback-12        	36709483	        32.52 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_NilContextFallback-12        	37007568	        32.49 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_NilContextFallback-12        	37472012	        32.34 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_NilContextFallback-12        	36524787	        33.01 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_nilFallback_empty-12         	188939660	         6.362 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_nilFallback_empty-12         	188812808	         6.347 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_nilFallback_empty-12         	190012770	         6.322 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_nilFallback_empty-12         	188572357	         6.363 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_nilFallback_empty-12         	189480186	         6.324 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_nilFallback_empty-12         	185538739	         6.415 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_nilFallback_empty-12         	188581218	         6.349 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_nilFallback_empty-12         	185861542	         6.431 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_nilFallback_empty-12         	189983318	         6.325 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_nilFallback_empty-12         	187882310	         6.418 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_fallbackNilGates_empty-12    	100000000	        11.25 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_fallbackNilGates_empty-12    	100000000	        11.24 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_fallbackNilGates_empty-12    	100000000	        11.18 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_fallbackNilGates_empty-12    	100000000	        11.14 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_fallbackNilGates_empty-12    	98047225	        11.17 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_fallbackNilGates_empty-12    	100000000	        11.23 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_fallbackNilGates_empty-12    	100000000	        11.17 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_fallbackNilGates_empty-12    	100000000	        11.22 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_fallbackNilGates_empty-12    	100000000	        11.26 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_fallbackNilGates_empty-12    	100000000	        11.27 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_withGates-12                 	39009292	        30.61 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_withGates-12                 	39117253	        30.77 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_withGates-12                 	39670470	        30.57 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_withGates-12                 	38754556	        30.45 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_withGates-12                 	39114830	        30.74 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_withGates-12                 	38070099	        31.49 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_withGates-12                 	39309076	        31.21 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_withGates-12                 	38144037	        30.92 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_withGates-12                 	37611304	        30.62 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_withGates-12                 	39485244	        30.63 ns/op	      16 B/op	       1 allocs/op
BenchmarkTrafficPortBundle_Populated-12                          	38705306	        31.64 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficPortBundle_Populated-12                          	38245670	        32.04 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficPortBundle_Populated-12                          	38515855	        31.81 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficPortBundle_Populated-12                          	38002945	        32.08 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficPortBundle_Populated-12                          	37383876	        31.47 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficPortBundle_Populated-12                          	37975885	        31.63 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficPortBundle_Populated-12                          	37653082	        31.83 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficPortBundle_Populated-12                          	38081697	        31.63 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficPortBundle_Populated-12                          	38121135	        31.50 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficPortBundle_Populated-12                          	38308304	        31.63 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficPortBundle_Empty-12                              	129091626	         9.290 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_Empty-12                              	129372520	         9.280 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_Empty-12                              	129118183	         9.330 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_Empty-12                              	128129276	         9.355 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_Empty-12                              	128991313	         9.297 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_Empty-12                              	128802200	         9.314 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_Empty-12                              	127508612	         9.390 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_Empty-12                              	128895379	         9.305 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_Empty-12                              	128960289	         9.303 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_Empty-12                              	129082086	         9.284 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_NilSnapshot-12                        	192085438	         6.243 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_NilSnapshot-12                        	195672154	         6.151 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_NilSnapshot-12                        	195327823	         6.131 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_NilSnapshot-12                        	195757828	         6.153 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_NilSnapshot-12                        	193148102	         6.166 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_NilSnapshot-12                        	196037973	         6.136 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_NilSnapshot-12                        	195875354	         6.139 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_NilSnapshot-12                        	195605493	         6.137 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_NilSnapshot-12                        	195802579	         6.120 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_NilSnapshot-12                        	195696087	         6.242 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficObserver_Populated-12                            	1000000000	         0.8903 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficObserver_Populated-12                            	1000000000	         0.8767 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficObserver_Populated-12                            	1000000000	         0.8749 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficObserver_Populated-12                            	1000000000	         0.8723 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficObserver_Populated-12                            	1000000000	         0.8755 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficObserver_Populated-12                            	1000000000	         0.8749 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficObserver_Populated-12                            	1000000000	         0.8742 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficObserver_Populated-12                            	1000000000	         0.8787 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficObserver_Populated-12                            	1000000000	         0.8728 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficObserver_Populated-12                            	1000000000	         0.8727 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_Populated-12                           	43914060	        26.65 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficRedactors_Populated-12                           	45802749	        26.48 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficRedactors_Populated-12                           	45412400	        26.67 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficRedactors_Populated-12                           	43726200	        26.93 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficRedactors_Populated-12                           	45007369	        26.80 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficRedactors_Populated-12                           	46096579	        26.53 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficRedactors_Populated-12                           	45637614	        26.41 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficRedactors_Populated-12                           	45048933	        26.90 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficRedactors_Populated-12                           	44043808	        26.63 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficRedactors_Populated-12                           	45506601	        26.61 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficRedactors_Empty-12                               	230566332	         5.196 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_Empty-12                               	230771937	         5.189 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_Empty-12                               	231654775	         5.194 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_Empty-12                               	230927460	         5.190 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_Empty-12                               	231224564	         5.191 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_Empty-12                               	232119541	         5.181 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_Empty-12                               	231428578	         5.183 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_Empty-12                               	231448442	         5.191 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_Empty-12                               	231561571	         5.174 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_Empty-12                               	232002322	         5.180 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_NilSnapshot-12                         	320010922	         3.751 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_NilSnapshot-12                         	319859858	         3.767 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_NilSnapshot-12                         	318947642	         3.760 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_NilSnapshot-12                         	319467468	         3.751 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_NilSnapshot-12                         	321361458	         3.741 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_NilSnapshot-12                         	320225011	         3.749 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_NilSnapshot-12                         	318374676	         3.761 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_NilSnapshot-12                         	319649748	         3.750 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_NilSnapshot-12                         	318858909	         3.751 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_NilSnapshot-12                         	319866679	         3.750 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_Populated-12                           	27948183	        37.02 ns/op	      32 B/op	       1 allocs/op
BenchmarkSecretGuardPlane_Populated-12                           	32994044	        36.78 ns/op	      32 B/op	       1 allocs/op
BenchmarkSecretGuardPlane_Populated-12                           	32848813	        36.65 ns/op	      32 B/op	       1 allocs/op
BenchmarkSecretGuardPlane_Populated-12                           	32588593	        37.22 ns/op	      32 B/op	       1 allocs/op
BenchmarkSecretGuardPlane_Populated-12                           	32544314	        37.35 ns/op	      32 B/op	       1 allocs/op
BenchmarkSecretGuardPlane_Populated-12                           	31649241	        37.36 ns/op	      32 B/op	       1 allocs/op
BenchmarkSecretGuardPlane_Populated-12                           	31885213	        37.18 ns/op	      32 B/op	       1 allocs/op
BenchmarkSecretGuardPlane_Populated-12                           	32985247	        37.37 ns/op	      32 B/op	       1 allocs/op
BenchmarkSecretGuardPlane_Populated-12                           	32684544	        36.90 ns/op	      32 B/op	       1 allocs/op
BenchmarkSecretGuardPlane_Populated-12                           	32601519	        36.96 ns/op	      32 B/op	       1 allocs/op
BenchmarkSecretGuardPlane_Empty-12                               	81024144	        14.61 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_Empty-12                               	79777686	        14.56 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_Empty-12                               	74699334	        14.61 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_Empty-12                               	82527542	        14.56 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_Empty-12                               	74486508	        14.64 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_Empty-12                               	79598821	        14.58 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_Empty-12                               	76295594	        14.60 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_Empty-12                               	79525497	        14.60 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_Empty-12                               	81168282	        14.58 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_Empty-12                               	81350416	        14.57 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_NilSnapshot-12                         	206418230	         5.821 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_NilSnapshot-12                         	206330707	         5.816 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_NilSnapshot-12                         	206288674	         5.813 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_NilSnapshot-12                         	207088143	         5.802 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_NilSnapshot-12                         	206539167	         5.803 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_NilSnapshot-12                         	206663022	         5.810 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_NilSnapshot-12                         	205980506	         5.816 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_NilSnapshot-12                         	205995639	         5.821 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_NilSnapshot-12                         	205625396	         5.819 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_NilSnapshot-12                         	205475757	         5.819 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Populated-12                  	88011382	        13.22 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Populated-12                  	88032688	        13.24 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Populated-12                  	88340522	        13.23 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Populated-12                  	82625848	        13.26 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Populated-12                  	88699662	        13.20 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Populated-12                  	89186838	        13.18 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Populated-12                  	90421363	        13.22 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Populated-12                  	90861594	        13.15 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Populated-12                  	90099559	        13.21 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Populated-12                  	83445172	        13.23 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Empty-12                      	83784254	        13.34 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Empty-12                      	90633756	        13.14 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Empty-12                      	83460842	        13.18 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Empty-12                      	91208281	        13.31 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Empty-12                      	80787406	        13.27 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Empty-12                      	87080200	        13.20 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Empty-12                      	89633175	        13.24 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Empty-12                      	91089890	        13.17 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Empty-12                      	87340693	        13.21 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Empty-12                      	90561253	        13.20 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_NilSnapshot-12                	803418814	         1.493 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_NilSnapshot-12                	802839912	         1.494 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_NilSnapshot-12                	793126762	         1.506 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_NilSnapshot-12                	793029795	         1.502 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_NilSnapshot-12                	790712814	         1.503 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_NilSnapshot-12                	797541975	         1.502 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_NilSnapshot-12                	799896013	         1.504 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_NilSnapshot-12                	799869886	         1.500 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_NilSnapshot-12                	798414879	         1.500 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_NilSnapshot-12                	793197013	         1.499 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_Populated-12                        	48576898	        23.88 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionObservers_Populated-12                        	50147097	        23.75 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionObservers_Populated-12                        	47665009	        23.87 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionObservers_Populated-12                        	50039197	        24.02 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionObservers_Populated-12                        	50218869	        23.79 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionObservers_Populated-12                        	50036275	        23.68 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionObservers_Populated-12                        	50520997	        23.72 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionObservers_Populated-12                        	51837644	        23.64 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionObservers_Populated-12                        	51448268	        23.94 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionObservers_Populated-12                        	50740386	        23.84 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionObservers_Empty-12                            	253251484	         4.761 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_Empty-12                            	251322584	         4.777 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_Empty-12                            	251185437	         4.747 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_Empty-12                            	251038881	         4.772 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_Empty-12                            	252521160	         4.730 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_Empty-12                            	253387687	         4.757 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_Empty-12                            	250884786	         4.806 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_Empty-12                            	251372914	         4.786 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_Empty-12                            	251505890	         4.740 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_Empty-12                            	252241480	         4.781 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_NilSnapshot-12                      	318517491	         3.756 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_NilSnapshot-12                      	320618065	         3.744 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_NilSnapshot-12                      	320744983	         3.762 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_NilSnapshot-12                      	320586201	         3.745 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_NilSnapshot-12                      	319811694	         3.751 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_NilSnapshot-12                      	319203946	         3.755 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_NilSnapshot-12                      	320293388	         3.748 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_NilSnapshot-12                      	318957985	         3.756 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_NilSnapshot-12                      	319490092	         3.761 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_NilSnapshot-12                      	319103191	         3.759 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_Populated-12                       	47731366	        23.96 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionPreservers_Populated-12                       	49581244	        23.84 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionPreservers_Populated-12                       	50648938	        23.91 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionPreservers_Populated-12                       	50105430	        23.89 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionPreservers_Populated-12                       	49710435	        23.95 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionPreservers_Populated-12                       	50574440	        23.98 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionPreservers_Populated-12                       	49627996	        23.87 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionPreservers_Populated-12                       	49843201	        24.43 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionPreservers_Populated-12                       	49208319	        24.25 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionPreservers_Populated-12                       	50581688	        23.91 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionPreservers_Empty-12                           	249405945	         4.761 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_Empty-12                           	250953361	         4.777 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_Empty-12                           	254787130	         4.709 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_Empty-12                           	251404670	         4.757 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_Empty-12                           	251427057	         4.744 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_Empty-12                           	251655472	         4.749 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_Empty-12                           	248670699	         4.807 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_Empty-12                           	252042912	         4.716 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_Empty-12                           	251282587	         4.763 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_Empty-12                           	249921950	         4.772 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_NilSnapshot-12                     	320597421	         3.733 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_NilSnapshot-12                     	320369235	         3.738 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_NilSnapshot-12                     	321060010	         3.759 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_NilSnapshot-12                     	322066291	         3.732 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_NilSnapshot-12                     	321508947	         3.729 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_NilSnapshot-12                     	320789140	         3.734 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_NilSnapshot-12                     	320700580	         3.735 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_NilSnapshot-12                     	320743868	         3.748 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_NilSnapshot-12                     	321377466	         3.739 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_NilSnapshot-12                     	322474020	         3.729 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Populated-12                   	228452806	         5.256 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Populated-12                   	228883494	         5.243 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Populated-12                   	228324055	         5.253 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Populated-12                   	228847744	         5.249 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Populated-12                   	228786399	         5.247 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Populated-12                   	229351221	         5.234 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Populated-12                   	229338247	         5.243 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Populated-12                   	228628999	         5.246 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Populated-12                   	229002476	         5.241 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Populated-12                   	229013882	         5.244 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Empty-12                       	285400208	         4.208 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Empty-12                       	286994280	         4.185 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Empty-12                       	284919292	         4.193 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Empty-12                       	286404998	         4.184 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Empty-12                       	285415210	         4.202 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Empty-12                       	287530036	         4.195 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Empty-12                       	285614252	         4.199 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Empty-12                       	285465590	         4.192 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Empty-12                       	285874034	         4.198 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Empty-12                       	286037098	         4.197 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_NilSnapshot-12                 	321022477	         3.736 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_NilSnapshot-12                 	321648211	         3.738 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_NilSnapshot-12                 	320470878	         3.731 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_NilSnapshot-12                 	322415276	         3.733 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_NilSnapshot-12                 	321877446	         3.730 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_NilSnapshot-12                 	316642825	         3.766 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_NilSnapshot-12                 	318389457	         3.747 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_NilSnapshot-12                 	321996117	         3.755 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_NilSnapshot-12                 	320167767	         3.737 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_NilSnapshot-12                 	320697837	         3.737 ns/op	       0 B/op	       0 allocs/op
PASS
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions	366.984s
```
