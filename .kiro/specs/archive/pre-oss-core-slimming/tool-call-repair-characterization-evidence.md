# Tool-Call Repair Characterization Evidence

- **Task**: 1.3 (P) Characterize tool-call-repair ownership and behavior before move
- **Spec**: `pre-oss-core-slimming`
- **Boundary**: Tool-Call Repair Feature (`internal/core/toolcallrepair` -> `internal/plugins/features/toolcallrepair`)
- **Status**: Completed & Verified

---

## 1. Executive Summary

Task 1.3 characterizes the complete ownership, inventory, and behavioral contract of `toolcallrepair` before its physical relocation in Phase 2 (Task 3.1).
The objective is to establish the current `internal/core/toolcallrepair` implementation as a replaceable oracle whose behavior is pinned through independent integration tests and standard distribution factory composition.

### Key Characterization Findings

1. **Current Ownership Inversion**:
   - `internal/plugins/features/toolcallrepair` currently owns only `config.go` (decoding and validation).
   - `internal/core/toolcallrepair` contains the full concrete implementation (engine, scanner, schema compiler/cache/prescan/repair, finalizer, and diagnostics).
   - `internal/standardplugins/features_install.go` bridges this by directly importing `internal/core/toolcallrepair` to instantiate `corerepair.NewFinalizer(...)` during feature bundle construction.

2. **Non-Test Import Audit**:
   - Exactly **1 non-test file** in the entire repository imports `internal/core/toolcallrepair`:
     - `internal/standardplugins/features_install.go` (line 6: `corerepair "github.com/matdev83/go-llm-interactive-proxy/internal/core/toolcallrepair"`).
   - **4 test files outside the package** import `internal/core/toolcallrepair`:
     - `internal/standardplugins/tool_call_repair_factory_test.go`
     - `internal/plugins/features/toolcallrepair/defaults_equality_test.go`
     - `internal/core/runtime/safe_tail_finalizer_integration_test.go`
     - `internal/core/runtime/tool_call_assembler_test.go`

3. **Standard Feature Bundle & Extension Planes**:
   - Standard feature factory constructs `toolcall.Finalizer` and contributes to two canonical planes:
     - `lipfeature.PlaneToolCallFinalizers`: slice of finalizers containing `toolcallrepair.Finalizer` (default ID `"tool-call-repair"`, default Order `40`).
     - `lipfeature.PlaneToolCallFinalizationMaxArgsBytes`: scalar byte limit (default `65536` bytes).
   - Custom YAML configuration (`order`, `max_args_bytes`, `on_unrepairable`, `schema.*`) is fully mapped and behaviorally verified through `finalizer.Finalize`.

4. **Disabled and Absent Feature Control**:
   - When the feature is omitted from plugin configurations or explicitly marked `Enabled: false`, realistic composition through `pluginreg.Registry` and `featurebundle.MergeFeatureSurfacesWithHost` produces zero finalizers on `PlaneToolCallFinalizers` and zero on `PlaneToolCallFinalizationMaxArgsBytes`.

---

## 2. Complete Inventory: `internal/core/toolcallrepair`

### 2.1 Production Source Files (18 Files, 2,795 Physical Lines)

| File | Lines | Primary Responsibility / Symbols |
|---|---|---|
| `catalog_index.go` | 62 | Bounded exact/normalized tool definition indexing (`CatalogIndex`, `NewCatalogIndex`) |
| `diagnostic.go` | 27 | Structured repair outcome telemetry and diagnostics |
| `doc.go` | 13 | Package documentation and architectural overview |
| `engine.go` | 328 | Core repair pipeline orchestration (`Engine`, `Repair`, `RepairWithContext`, `Outcome`) |
| `finalizer.go` | 128 | Public SDK `toolcall.Finalizer` adapter (`Finalizer`, `FinalizerPolicy`, `NewFinalizer`) |
| `json_scanner.go` | 147 | Low-level JSON streaming tokenizer and syntactic balance analyzer |
| `json_tail.go` | 388 | Deterministic suffix completion and trailing delimiter repair algorithms |
| `normalize.go` | 20 | Identifier and property name case/delimiter normalization helpers |
| `ordered_json.go` | 132 | Key-preserving JSON parser with duplicate-property detection |
| `pending_value.go` | 44 | Root-level pending property value synthesizer from schema const/enum |
| `schema_cache.go` | 184 | Thread-safe LRU cache for compiled schemas (`SchemaCache`, `NewSchemaCache`) |
| `schema_compile.go` | 157 | jsonschema compiler integration and resource loader isolation (`CompiledSchema`, `compileSchema`) |
| `schema_error.go` | 68 | Structured error classification for schema validation and pre-scan failures |
| `schema_limits.go` | 51 | Resource boundary limits for schema depth, nodes, bytes, and cache capacity |
| `schema_prescan.go` | 236 | Security pre-scan rejecting malicious schemas (excessive depth, external refs, cycle bombs) |
| `schema_repair.go` | 646 | Schema-guided argument tree mutation, required-property synthesis, and pruning |
| `schema_validate.go` | 18 | Direct JSON schema validation against compiled instances |
| `shape_guard.go` | 146 | Fast preflight boundary checks preventing DoS on deep or oversized JSON |

### 2.2 Test Source Files (22 Files, 3,443 Physical Lines)

| File | Lines | Coverage / Scope |
|---|---|---|
| `args_detach_test.go` | 20 | Memory decoupling and slice clone isolation for repaired arguments |
| `catalog_index_test.go` | 131 | Exact and normalized tool name lookup, collision handling, and caching |
| `diagnostic_test.go` | 56 | Repair diagnostics recording and reason code emission |
| `engine_contract_test.go` | 276 | Table-driven fixture contract tests for all canonical repair scenarios |
| `engine_repair_test.go` | 275 | Unit tests for deterministic repair logic across various tool schemas |
| `export_object_fields_test.go` | 5 | Test export bridge for private object field inspection |
| `export_test.go` | 31 | Test export bridge for internal scanner and compiler helpers |
| `finalizer_sdk_test.go` | 288 | SDK `toolcall.Finalizer` contract tests, mapping table, and panic isolation |
| `fuzz_test.go` | 169 | Fuzzing harnesses for JSON suffix, schema compile, tail repair, and engine |
| `hardening_test.go` | 155 | Adversarial input fuzzing, malicious payload rejection, and resource caps |
| `json_scanner_test.go` | 58 | Tokenizer correctness on partial/malformed JSON sequences |
| `main_test.go` | 14 | Package test bootstrap and setup |
| `ordered_json_bench_test.go` | 138 | Benchmarks for ordered JSON parsing and preflight checks |
| `ordered_json_depth_test.go` | 119 | Nesting depth limit enforcement on complex JSON objects |
| `safe_tail_bench_test.go` | 50 | Benchmarks for suffix completion and delimiter balancing |
| `safe_tail_test.go` | 240 | Comprehensive test suite for append-only suffix completion |
| `schema_bench_test.go` | 137 | Benchmarks for schema compilation, validation, and LRU cache hit/miss |
| `schema_cache_test.go` | 119 | Cache eviction, LRU concurrency safety, and size bounding |
| `schema_compile_test.go` | 324 | Draft-2020 validation, external reference blocking, and compile errors |
| `schema_repair_test.go` | 274 | Schema-guided argument mutation, const injection, and type conversions |
| `schema_validate_test.go` | 103 | Instance validation against compiled schemas |
| `shape_guard_test.go` | 461 | Preflight security guards (token counts, depth, string lengths, keys) |

### 2.3 Fuzz Targets (5 Targets)

| Target Name | File | Purpose |
|---|---|---|
| `FuzzCompleteJSONSuffix` | `fuzz_test.go` | Append-only JSON suffix completion fuzzing |
| `FuzzSchemaPreScanCompile` | `fuzz_test.go` | Schema pre-scan and compilation resource limit fuzzing |
| `FuzzJSONTail` | `fuzz_test.go` | JSON tail analysis and delimiter balancing fuzzing |
| `FuzzPendingRootValue` | `fuzz_test.go` | Root pending property synthesis fuzzing |
| `FuzzEngineRepair` | `fuzz_test.go` | Full engine end-to-end repair fuzzing with random arguments |

### 2.4 Benchmark Targets (9 Targets)

| Target Name | File | Purpose |
|---|---|---|
| `BenchmarkOrderedParse` | `ordered_json_bench_test.go` | Ordered JSON AST parsing throughput |
| `BenchmarkOrderedPreflightPlusParse` | `ordered_json_bench_test.go` | Combined preflight validation + parsing throughput |
| `BenchmarkEngineRepair_parserContribution` | `ordered_json_bench_test.go` | Parser portion of total repair execution |
| `BenchmarkSafeTailRepair` | `safe_tail_bench_test.go` | Suffix completion and tail repair latency |
| `BenchmarkSchemaCompile` | `schema_bench_test.go` | Cold schema compilation latency |
| `BenchmarkSchemaValidate` | `schema_bench_test.go` | Instance validation against pre-compiled schema |
| `BenchmarkSchemaCacheHit` | `schema_bench_test.go` | LRU cache hit lookup latency |
| `BenchmarkSchemaCacheMiss` | `schema_bench_test.go` | Cache miss with automatic compilation latency |
| `BenchmarkEngineRepair` | `schema_bench_test.go` | Full engine repair cycle end-to-end latency |

---

## 3. Inventory: `internal/plugins/features/toolcallrepair`

### Production & Test Files (4 Files, 436 Physical Lines: 2 PROD / 254 Lines, 2 TEST / 182 Lines)

| File | Lines | Purpose |
|---|---|---|
| `config.go` | 245 | Feature configuration struct, YAML decoding, range validation, and normalization |
| `doc.go` | 9 | Package documentation |
| `config_acceptance_test.go` | 140 | Unit tests for configuration decoding, defaults, unknown key rejection, and upper bounds |
| `defaults_equality_test.go` | 42 | Config defaults parity verification |

---

## 4. Import Graph & Relocation Destination

```text
Current State (Task 1.3 Baseline):
internal/plugins/features/toolcallrepair (config only)
internal/core/toolcallrepair (engine, scanner, schema, finalizer)
internal/standardplugins/features_install.go ────────────> imports internal/core/toolcallrepair

Target State (Task 3.1 Post-Migration):
internal/plugins/features/toolcallrepair/
├── config.go
├── bundle.go                  # Builds FeatureBundle(cfg)
├── repair/                    # All 18 production files from internal/core/toolcallrepair
│   ├── engine.go
│   ├── finalizer.go
│   ├── schema_*.go
│   ├── json_*.go
│   └── (all 22 test files + fuzz + benchmarks)
internal/standardplugins/features_install.go ────────────> imports internal/plugins/features/toolcallrepair (bundle only)
internal/core/toolcallrepair/  [DELETED]
```

---

## 5. RED Phase Evidence (Task 1.3 Characterization Probe)

To verify the TDD RED->GREEN loop for Task 1.3 characterization, a missing integration assertion probe was executed:

```text
=== RUN   TestToolCallRepair_Characterization_RED_MissingFactoryIntegrationProbe
=== PAUSE TestToolCallRepair_Characterization_RED_MissingFactoryIntegrationProbe
=== CONT  TestToolCallRepair_Characterization_RED_MissingFactoryIntegrationProbe
    tool_call_repair_factory_test.go:100: RED probe: missing comprehensive behavioral characterization for custom YAML mapping, schema limits, on_unrepairable error/pass_through, and realistic disabled/absent composition
--- FAIL: TestToolCallRepair_Characterization_RED_MissingFactoryIntegrationProbe (0.00s)
FAIL
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins	1.317s
FAIL
```

---

## 6. Verification Results

All tests across `internal/core/toolcallrepair`, `internal/plugins/features/toolcallrepair`, and `internal/standardplugins` pass:

```powershell
# Run validation command
go test -count=1 ./internal/core/toolcallrepair ./internal/plugins/features/toolcallrepair ./internal/standardplugins -run "Tool.*Repair|tool.*repair|Finalizer"
# Output:
# ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/toolcallrepair	0.865s
# ok  	github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair	0.423s [no tests to run]
# ok  	github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins	1.305s

# Run all tests in relevant packages
go test -count=1 ./internal/core/toolcallrepair ./internal/plugins/features/toolcallrepair ./internal/standardplugins
# Output:
# ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/toolcallrepair	0.901s
# ok  	github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair	0.441s
# ok  	github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins	1.395s
```
