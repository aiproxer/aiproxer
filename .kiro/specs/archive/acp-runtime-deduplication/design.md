# Design: ACP Runtime Deduplication

## 1. Executive Summary & Architecture Context

This design eliminates the duplicated ACP protocol and runtime implementation in `internal/plugins/backends/acp` by establishing `connector-support/acp` as the sole canonical ACP module in the repository.

Per ADR 0008 (Hybrid Backend Connector Plugins), ACP is an optional backend plugin implemented as executable gRPC connectors under `connectors/` (`connectors/acp`, `connectors/agycliacp`, `connectors/geminicliacp`, `connectors/cursorcliacp`). These executable connectors import `connector-support/acp` for shared ACP protocol parsing, transport, session management, NDJSON mapping, and process pool lifecycle.

Investigation confirmed that zero root production packages (`internal/core`, `internal/standardplugins`, `internal/pluginreg`, `internal/infra/runtimebundle`, `internal/stdhttp`, `cmd/lipstd`) import `internal/plugins/backends/acp`. The root distribution `cmd/lipstd` does not contain static ACP builtins. The only remaining importers of `internal/plugins/backends/acp` were root testkit helpers (`internal/testkit/conformance`) and architecture boundary tests.

By migrating root conformance helpers to focus on static built-in backends and relying on `connectors/acp` for executable ACP plugin parity, `internal/plugins/backends/acp` can be safely deleted in its entirety (71 files removed).

---

## 2. Boundary Ownership and Architecture Rules

### What This Spec Owns

- Elimination of `internal/plugins/backends/acp` (total deletion).
- Consolidation of canonical ACP runtime logic in `connector-support/acp`.
- Migration of `internal/testkit/conformance` files (`harness.go`, `refparity.go`, `error_upstream.go`, `sanity_emulator_wiring_test.go`, `matrix.go`, `parity_acp_test.go`).
- Replacement of mirror file scripts (`scripts/backend-plugin-module-checks.{sh,ps1}`) with an architecture guard in `internal/archtest`.
- Updating release manifests (`.release-files`), Makefile, scripts (`scripts/check-adhoc-goroutines.{sh,ps1}`), and documentation (`docs/release-gates.md`, `docs/testing-determinism.md`).

### What This Spec Does Not Own

- Protocol translation changes for non-ACP backends (OpenAI, Anthropic, Gemini, Bedrock).
- Modification of external executable ACP connector ABIs under `connectors/`.
- Fundamental redesign of `connector-support/acp` internal transport or NDJSON streaming logic.

### Kiro Design Guardrail Checklist

1. **Core-owned vs Plugin-owned**: ACP protocol/runtime is plugin-owned and lives in `connector-support/acp` for consumption by `connectors/*`. Core runtime (`internal/core`) has zero ACP imports.
2. **Provider SDK Leakage**: Provider SDK types do not leak into core; `connector-support/acp` depends only on public root module contracts (such as `pkg/lipapi` and `pkg/lipsdk/modelinventory`) and stdlib without importing root production packages (`internal/core`, `internal/plugins`, etc.) or introducing reverse dependencies from root production code.
3. **Root Module Isolation**: Root module (`cmd/lipstd`) has zero dependencies on `connector-support/` or `connectors/`, ensuring `cmd/lipstd` builds without optional connector trees.
4. **Streaming Invariant**: `connector-support/acp` maintains streaming-first NDJSON mapping without buffering or retry after the first output event.
5. **PR #239 Preservation**: Structured JSON-RPC error propagation (`RPCError`, `RPCErrorDetails`, `MapRPCErrorToCanonical`) in `connector-support/acp/rpc_error.go` is preserved.

---

## 3. Package Migration and Detailed Changes

### A. Root Conformance Testkit Migration (`internal/testkit/conformance`)

Root conformance matrix tests verify static built-in backend plugins (OpenAI Responses, OpenAI Legacy, Anthropic, Gemini, Bedrock).
- **`matrix.go`**: Remove `"acp"` from `BundledBackendIDs()`.
- **`harness.go`**: Remove `acp` import and `case acp.ID:` handling in `BackendFor` and `BackendForDualCredential`.
- **`refparity.go`**: Remove `acp` import and `backendID == acp.ID` handling in `NewSuccessRefBackend`.
- **`error_upstream.go`**: Remove `acp` import and `case acp.ID:` handling in `NewUpstream400Server`.
- **`sanity_emulator_wiring_test.go`**: Remove `acp.ID` from `TestSanityTask13_BundledBackendsWiredForRefParity`.
- **`parity_acp_test.go`**: Add `TestParity_ACP_retiredFromStaticMatrix(t *testing.T)`, which iterates through `AllCells()` and asserts that no static matrix cell has `cell.Backend == "acp"`. This enforces continuous conformance verification that ACP is retired from static built-in backend tables.

*Parity Coverage Strategy*: ACP executable plugin parity and conformance coverage is provided by `connectors/acp` and `connector-support/acp` against `internal/refbackend/acp` via `make parity-acp-plugin` and `make parity-cli-acp-plugins`. This ensures complete test coverage without maintaining a duplicate internal ACP package.

### B. Deletion of `internal/plugins/backends/acp`

Delete `internal/plugins/backends/acp/` directory and all 71 contained files, tests, and fuzz corpus entries.

### C. Architecture Guard Updates (`internal/archtest`)

- **`acp_plugin_architecture_test.go`**:
  - Add assertion `TestACP_internalBackendPackageDeleted` verifying `internal/plugins/backends/acp` directory does not exist.
  - Retain `TestACP_supportModuleExistsIndependently`, `TestACP_supportSourceHasNoInternalCoreImports`, `TestACP_productPackagesRemovedFromRoot`, and `TestACP_externalConnectorModulesPresent`.
- **`identity_transport_boundaries_test.go`**:
  - Remove `./internal/plugins/backends/acp` from `excludedPkgs` in `TestIdentityTransport_excludedPackagesDoNotImportHTTPIdentity` (since the package is deleted).

### D. Mirror Checks & Script Refactoring (`scripts/backend-plugin-module-checks.{sh,ps1}`)

Remove the `$MirrorFiles` byte-comparison loop from `scripts/backend-plugin-module-checks.sh` and `scripts/backend-plugin-module-checks.ps1`.
Retain:
- Root module `go list` and `go build ./cmd/lipstd` checks.
- Asserting root `go list -m all` does not contain connector modules.
- Executable module discovery and isolation checks.
- Temporary root build check with `connectors/` and `connector-support/` absent.

### E. Release & Repository Hygiene Updates

- **`.release-files`**: Delete lines 3186–3256 (all `internal/plugins/backends/acp/*` paths). Ensure `internal/testkit/conformance/parity_acp_test.go` is in the manifest.
- **Fuzz & Concurrency Targets**: Retain seed corpus verification and run targeted fuzzing campaigns (`go test -fuzz=^FuzzName$ -fuzztime=30s -run=^$ .`) for `FuzzParseNDJSONLine`, `FuzzMapSessionUpdateToEvents`, and `FuzzMergeHandshakeProfileExtensions` in `connector-support/acp`.
- **`Makefile`**: Update `parity-acp-plugin` target to remove the line running tests in `./internal/plugins/backends/acp`.
- **`scripts/check-adhoc-goroutines.sh` & `.ps1`**: Remove `internal/plugins/backends/acp/transport_stdio.go` path from the check script lists.
- **`docs/release-gates.md`**: Update fuzz test locations for `FuzzParseNDJSONLine`, `FuzzMapSessionUpdateToEvents`, and `FuzzMergeHandshakeProfileExtensions` to `connector-support/acp`.
- **`docs/testing-determinism.md`**: Update reference from `internal/plugins/backends/acp/prompt_msg.go` to `connector-support/acp/prompt_msg.go`.
- **Reference Classification**: All stale/import/build/release/script/docs references to `internal/plugins/backends/acp` are eliminated. The path is exclusively permitted in `internal/archtest/acp_plugin_architecture_test.go` (assertion that it is deleted) and archived spec documentation under `.kiro/specs/archive/acp-runtime-deduplication/`.

---

## 4. PR #239 Reconciliation Strategy

PR #239 ("fix: surface ACP error details") introduced structured JSON-RPC error mapping (`RPCError`, `RPCErrorDetails`, `MapRPCErrorToCanonical`).

Both trees on this worktree branch (`refactor/acp-runtime-dedup` at commit `89c39ec1`) already incorporate the PR #239 changes. `connector-support/acp/rpc_error.go` and `connector-support/acp/rpc_error_test.go` contain the complete PR #239 implementation.

*Final Task Gate*: Prior to opening the final PR for dedup, when PR #239 merges to `main`, rebase/reconcile `refactor/acp-runtime-dedup` onto `origin/main` to ensure git history is clean and PR #239 features remain 100% active in `connector-support/acp`.

---

## 5. Known Windows Concurrency Timeout Note

As documented in the handoff, a full `connector-support/acp` test run on Windows may occasionally encounter an observed blocking or timeout in `TestExecutableCache_ResetConcurrentWithLookups` inside `connector-support/acp/lookpath_race_test.go`. Handoff establishes that this blocking predates this deduplication change. All concurrency tests and assertions must be preserved without weakening source assertions. Focused testing (`go test -run TestName`) or running in CI/Linux confirms clean pass rates.
