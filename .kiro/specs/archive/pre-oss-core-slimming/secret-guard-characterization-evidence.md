# Secret-Guard Characterization Evidence

- **Task**: 1.4 (P) Characterize secret-guard source, matcher, audit, and security invariants
- **Spec**: `pre-oss-core-slimming`
- **Boundary**: Secret Guard Feature / Composition (`internal/core/secretguard` -> `internal/plugins/features/secretguard/engine` + `internal/infra/secretguardcompose`)
- **Status**: Completed & Verified

---

## 1. Executive Summary

Task 1.4 characterizes the complete ownership, inventory, and security/runtime invariants of `secretguard` before its physical relocation in Phase 4 (Tasks 4.1, 4.2, 4.3).
The objective is to establish the current `internal/core/secretguard` implementation as a replaceable oracle whose behavioral and security invariants are pinned through unit tests, characterization tests, and composition integration tests.

### Key Characterization Invariants Pinned

1. **Single-User Catalog Discovery, Prefix Preservation, and Source Policy**:
   - Sparse proxy credential discovery (`OPENAI_API_KEY`, `OPENAI_API_KEY_2`, etc.) automatically snapshots process environment.
   - Values below `min_secret_bytes` (default 8) are dropped before catalog insertion.
   - Identical secret values are deduplicated: lexicographically first name becomes primary ref name; remaining names become sorted aliases.
   - Known public prefixes (e.g., `sk-`, `sk-or-`, `sk-ant-`, `ghp_`) are recognized via longest-prefix matching. When `PreserveKnownPrefixes` is enabled, redaction masks only the trailing secret bytes while preserving the public prefix.
   - When `MatcherConfigured` is `false` in `SingleUserOptions`, `NewSingleUserSource` defaults `PreserveKnownPrefixes` to `true` and mask byte to `'*'`.
   - When `MatcherConfigured` is `true`, caller-specified `MatcherOptions` (e.g. `PreserveKnownPrefixes: false`, custom `MaskByte`) are strictly honored.
   - Include/exclude rules: `ExcludeEnv` explicitly removes variables; `IncludeEnv` loads variables directly (via `Lookup` if absent from initial snapshot) and overrides public-prefix exclusions (e.g., `NEXT_PUBLIC_*`).
   - Popular environment inference: `IncludePopularEnv: true` infers `_API_KEY` and `_TOKEN` suffixes while excluding forbidden IDs/paths (`AWS_ACCESS_KEY_ID`, `GOOGLE_APPLICATION_CREDENTIALS`, etc.).

2. **Multi-User Zero-Environment Calls (Security Boundary)**:
   - `NewMultiUserSource` and `ComposeSource(ModeMultiUser, ...)` never invoke `Environment.Lookup` or `Environment.Snapshot`.
   - Verified via `panicEnvironment` asserting 0 calls during creation, inspection (`EntryCount()`, `SourceCategories()`, `MatcherResolver()`), and matcher resolution.
   - Multi-user source reports `EntryCount = 0`, `AccessMode = ModeMultiUser`, `SourceCategories = ["request_credential"]`.
   - Resolving matcher with empty context returns `(nil, nil)`; resolving with `sdk.WithRequestMatcher` returns the request-scoped matcher.

3. **Disabled Zero-Environment Calls**:
   - `NewDisabledSource` and `ComposeSource(..., featureEnabled=false)` never invoke `Environment`.
   - Disabled source reports `EntryCount = 0`, `AccessMode = ModeSingleUser` (neutral default posture), `SourceCategories = nil`.
   - `MatcherResolver().Resolve(ctx)` returns `(nil, nil)`.

4. **Runtime Composition, Uniqueness, Audit Policy, and Observer Chaining**:
   - Feature uniqueness: duplicate enabled secrets-guard registrations fail closed before compilation with exact error: `"runtimebundle: multiple enabled secrets-guard registrations"`. Matching is case-insensitive on factory kind.
   - Multiple registrations where only one is enabled succeed. Zero enabled registrations succeed with empty runtime plane.
   - Nil logger fail-closed: when secrets-guard is enabled or guards are injected without an explicit observer, a nil logger fails with exact error: `"runtimebundle: secrets-guard audit requires a non-nil logger"`.
   - Audit failure policy: `AuditFailClosed` chains observer errors; `AuditBestEffort` swallows observer errors.
   - Observer chaining: non-nil observer is chained with failure policy; typed-nil observer and untyped-nil observer fall back to default slog observer logging `lip.secret_guard.decision`.
   - Bounded inventory metadata: `SecretGuardCatalogEntryCount`, `SecretGuardSourceCategories`, `SecretGuardAccessMode`, `SecretGuardAction`.

5. **Safe Diagnostics and Anti-Leak Invariants**:
   - Finding metadata (`SecretRefName`, `Aliases`, `SourceCategory`, `Location`) never carries raw secret values or content excerpts.
   - Bounded error classifications and diagnostic text shapes are pinned against leaking secret material or configuration values.

---

## 2. Complete Inventory: `internal/core/secretguard`

### 2.1 Production Source Files (10 Files, 942 Physical Lines)

| File | Lines | Primary Responsibility / Symbols |
|---|---|---|
| `aho_corasick.go` | 89 | Exact multi-pattern string matching automaton (`ahoCorasick`, `buildAhoCorasick`, `findAll`) |
| `catalog.go` | 175 | Immutable deduplicated secret catalog (`Catalog`, `CatalogInput`, `BuildCatalog`, `EntryCount`, `SourceCategories`) |
| `compose.go` | 19 | Core source policy selector (`ComposeSource`) |
| `doc.go` | 9 | Package documentation and architectural overview |
| `known_prefix.go` | 58 | Known public credential prefix recognition (`detectKnownPublicPrefix`, prefix tables) |
| `matcher.go` | 192 | Opaque secret scanning and redaction engine (`Matcher`, `MatcherOptions`, `ScanBytes`, `RedactBytes`, `ScanString`, `RedactString`) |
| `popular_env.go` | 105 | Popular secret environment variable inventory and heuristics (`PopularSecretEnvNames`, `popularInference`) |
| `proxy_inventory.go` | 117 | Proxy provider credential discovery and inventory assembly (`collectSingleUserInventory`, `catalogInputsFromInventory`) |
| `sdk_adapter.go` | 61 | Public SDK adapter bridges (`AsMatcher`, `staticMatcherResolver`, `NewStaticMatcherResolver`) |
| `source.go` | 117 | Source interface and constructors (`Source`, `Environment`, `SingleUserOptions`, `NewSingleUserSource`, `NewMultiUserSource`, `NewDisabledSource`) |

### 2.2 Non-Go Documentation (1 File, 29 Physical Lines)

| File | Lines | Primary Responsibility |
|---|---|---|
| `popular_env.md` | 29 | Documentation of popular environment variable rules |

### 2.3 Test Source Files (13 Files Total, 2,231 Physical Lines)

#### Baseline Pre-existing Tests (12 Files, 2,047 Physical Lines)
| File | Lines | Coverage / Scope |
|---|---|---|
| `catalog_test.go` | 121 | Catalog deduplication, min secret length capping, empty/nil safety |
| `compose_test.go` | 74 | `ComposeSource` mode selection, multi-user/disabled zero-env invariants |
| `export_test.go` | 6 | Test export bridges for unexported helper testing |
| `frontend_conformance_test.go` | 404 | Frontend-specific field coverage and scanner conformance |
| `frontend_matrix_test.go` | 24 | Test matrix across frontend data shapes |
| `known_prefix_test.go` | 82 | Known public prefix detection, longest-prefix matching, catalog attachment |
| `matcher_bench_test.go` | 92 | Performance benchmarks for Aho-Corasick scanning and redaction |
| `matcher_fuzz_test.go` | 52 | Fuzzing harnesses for matcher scanning and byte redaction |
| `matcher_test.go` | 499 | Matcher unit tests, concurrency, longest-match overlap, case-sensitivity, anti-leak checks |
| `popular_env_test.go` | 49 | Popular environment variable list uniqueness, non-emptiness, and exclusion validation |
| `source_catalog_test.go` | 573 | Single-user environment discovery, include/exclude precedence, popular inference |
| `source_isolation_test.go` | 71 | Strict isolation tests proving zero environment calls and reflection audit |

#### Task 1.4 Added Tests (1 File, 184 Physical Lines)
| File | Lines | Coverage / Scope |
|---|---|---|
| `source_characterization_test.go` | 184 | Task 1.4 characterization for matcher options, prefix defaults, multi-user/disabled source, and nil-safety |

---

## 3. Complete Inventory: `internal/plugins/features/secretguard`

### 3.1 Production Source Files (6 Files, 902 Physical Lines)

| File | Lines | Primary Responsibility / Symbols |
|---|---|---|
| `bundle.go` | 17 | Public FeatureBundle constructor and Descriptor export |
| `config.go` | 218 | YAML configuration decoding, defaults, and validation (`Config`, `RuntimeConfig`, `Validate`) |
| `guard.go` | 141 | SDK `Guard` evaluation pipeline (`guard`, `Evaluate`, `evalBlock`, `evalRedact`, `evalLog`) |
| `json_redact.go` | 215 | JSON payload structured AST walker and token-safe redaction |
| `runtime_compose.go` | 81 | Runtime configuration composer and registration filtering (`ComposeRuntimeConfig`, `EnabledRegistrations`) |
| `scan.go` | 230 | `lipapi.Call` comprehensive field scanner across prompts, messages, tool calls, and completions |

### 3.2 Test Source Files (7 Files, 1,636 Physical Lines)

| File | Lines | Coverage / Scope |
|---|---|---|
| `config_test.go` | 226 | YAML configuration parsing, validation error text shapes, boundary caps |
| `fuzz_test.go` | 94 | Fuzz tests for JSON redaction and call scanning |
| `generation_uniqueness_test.go` | 34 | Generation-level registration uniqueness checks |
| `guard_test.go` | 898 | Guard evaluation under block/redact/log actions, scan limits, failure modes |
| `import_boundary_test.go` | 40 | Architectural import boundary asserting zero imports of core/runtimebundle/stdhttp |
| `json_redact_test.go` | 231 | JSON string/number/boolean/array/object redaction tests |
| `runtime_compose_test.go` | 113 | Registration resolution, duplicate enabled rejection, factory kind precedence |

---

## 4. Selected `internal/infra/runtimebundle` Secret Guard Files (Filtered by `*secret*` Pattern)

*Note: This section inventories the selected secret-guard-related files within `internal/infra/runtimebundle` matching `*secret*`, rather than the entire runtimebundle package.*

### 4.1 Production Files (1 Selected File, 123 Physical Lines)

| File | Lines | Responsibility |
|---|---|---|
| `secret_guard_runtime.go` | 123 | Runtime composition root (`buildSecretGuardRuntime`, `composeSecretGuardSingleUser`, `secretGuardRuntime`) |

### 4.2 Test Files (10 Selected Files, 2,277 Physical Lines)

| File | Lines | Coverage / Scope |
|---|---|---|
| `auth_secret_leak_test.go` | 127 | Auth credential redaction and leak prevention |
| `build_extension_secret_guard_test.go` | 45 | Extension plane construction integration |
| `build_secret_guard_helpers_test.go` | 21 | Test helpers for secret guard runtime building |
| `build_secret_guard_test.go` | 312 | Build options immutability, injected guards, catalog loading, typed-nil fallback |
| `build_secret_guard_yaml_test.go` | 225 | Decoded YAML option mapping, multi-user single_user rejection |
| `secret_guard_audit_policy_test.go` | 107 | Audit failure policy chaining, best_effort error swallowing, disabled observer skipping |
| `secret_guard_leak_test.go` | 141 | Anti-leakage tests across execution planes and runtime diagnostics |
| `secret_guard_parity_characterization_test.go` | 663 | Parity characterization: uniqueness, config validation errors, nil logger, source policy |
| `secret_guard_projection_parity_test.go` | 513 | Registration order preservation, snapshot sorted materialization, nil/empty preservation |
| `secret_guard_serve_internal_test.go` | 123 | Server-level secret guard integration smoke |

---

## 5. Non-Test Import Audit: `internal/core/secretguard`

### 5.1 Production Import Census (Exactly 4 Non-Test Files)

All 4 non-test files importing `internal/core/secretguard` are confined to `internal/infra/runtimebundle`:
1. `internal/infra/runtimebundle/host_build.go` (line 15)
2. `internal/infra/runtimebundle/options.go` (line 13)
3. `internal/infra/runtimebundle/secret_guard_runtime.go` (line 11)
4. `internal/infra/runtimebundle/validate_distribution.go` (line 11)

In Phase 4 (Task 4.2), these imports will be replaced by the dedicated typed composition adapter `internal/infra/secretguardcompose`.

### 5.2 External Test Files Importing `internal/core/secretguard` (9 Files)

1. `internal/archtest/plane_rules_test.go`
2. `internal/archtest/reasoning_preservation_privacy_test.go`
3. `internal/infra/runtimebundle/build_secret_guard_yaml_test.go`
4. `internal/infra/runtimebundle/candidate_options_test.go`
5. `internal/infra/runtimebundle/extension_projection_characterization_test.go`
6. `internal/infra/runtimebundle/overlay_rules_characterization_test.go`
7. `internal/infra/runtimebundle/secret_guard_leak_test.go`
8. `internal/infra/runtimebundle/secret_guard_parity_characterization_test.go`
9. `internal/infra/runtimebundle/secret_guard_projection_parity_test.go`

---

## 6. Verification Evidence

### 6.1 Validation Command

```bash
go test -count=1 ./internal/core/secretguard ./internal/plugins/features/secretguard ./internal/infra/runtimebundle -run 'Secret|secret|Matcher|Catalog'
```

Output:
```text
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/secretguard	1.488s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard	1.006s
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	8.832s
```

### 6.2 Characterization Suite Execution

```bash
go test -v -count=1 ./internal/core/secretguard -run TestCharacterize
```

Output:
```text
=== RUN   TestCharacterize_SingleUserMatcherConfiguredAndPrefixPreservation
=== RUN   TestCharacterize_SingleUserNilEnvAndEmptyOptions
=== RUN   TestCharacterize_MultiUserSourceInvariants
=== RUN   TestCharacterize_DisabledSourceInvariants
=== RUN   TestCharacterize_NilAndOpaqueMatcherSafety
--- PASS: TestCharacterize_DisabledSourceInvariants (0.00s)
--- PASS: TestCharacterize_MultiUserSourceInvariants (0.00s)
--- PASS: TestCharacterize_SingleUserNilEnvAndEmptyOptions (0.00s)
--- PASS: TestCharacterize_SingleUserMatcherConfiguredAndPrefixPreservation (0.00s)
    --- PASS: TestCharacterize_SingleUserMatcherConfiguredAndPrefixPreservation/matcher_unconfigured_defaults_to_preserve_prefix_and_asterisk_mask (0.00s)
    --- PASS: TestCharacterize_SingleUserMatcherConfiguredAndPrefixPreservation/matcher_configured_respects_custom_mask_and_prefix_policy (0.00s)
--- PASS: TestCharacterize_NilAndOpaqueMatcherSafety (0.00s)
    --- PASS: TestCharacterize_NilAndOpaqueMatcherSafety/typed_nil_Matcher_pointer (0.00s)
    --- PASS: TestCharacterize_NilAndOpaqueMatcherSafety/NewStaticMatcherResolver_nil_catalog (0.00s)
    --- PASS: TestCharacterize_NilAndOpaqueMatcherSafety/AsMatcher_nil_adapter (0.00s)
PASS
ok  	github.com/matdev83/go-llm-interactive-proxy/internal/core/secretguard	1.302s
```
