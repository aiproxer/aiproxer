# Requirements: ACP Runtime Deduplication

## Goal and Context

The repository currently maintains two near-identical ACP protocol/runtime implementations:
1. `connector-support/acp`: the independent support module used by executable connector plugins (`connectors/acp`, `connectors/agycliacp`, `connectors/geminicliacp`, `connectors/cursorcliacp`).
2. `internal/plugins/backends/acp`: a legacy duplicated mirror package in the root module.

The goal of this spec is to eliminate code duplication by deleting `internal/plugins/backends/acp` and establishing `connector-support/acp` as the sole canonical ACP protocol and runtime implementation in the repository.

As established in ADR 0008 and Phase 6/8 plugin architecture cutovers, ACP is an executable connector plugin rather than a static built-in backend. Zero root production packages (`internal/core`, `internal/standardplugins`, `internal/pluginreg`, `internal/infra/runtimebundle`, `internal/stdhttp`, `cmd/lipstd`) import `internal/plugins/backends/acp`. Therefore, `internal/plugins/backends/acp` is retired production code.

## Functional Requirements (EARS Syntax)

### Requirement 1: Single Ownership and Canonical Runtime Location

- **R1.1**: *When* ACP protocol, transport, process management, session lifecycle, NDJSON event mapping, or error handling is executed or modified, the system *shall* utilize `connector-support/acp` as the sole canonical owner of ACP runtime logic.
- **R1.2**: *When* production and standard bundle imports are verified, the codebase *shall* contain zero root production package imports of ACP, permitting total deletion of `internal/plugins/backends/acp`.

### Requirement 2: Root Module Isolation and Independent Build Boundary

- **R2.1**: *When* `connectors/` and `connector-support/` directories are absent from the working directory (e.g. isolated build environments), the root module (`cmd/lipstd`) *shall* compile and build successfully without errors.
- **R2.2**: *While* compiling any package in the root module, no root production package *shall* import `github.com/matdev83/go-llm-interactive-proxy/connector-support/acp` or any module under `connectors/`.

### Requirement 3: Root Conformance Testkit Migration

- **R3.1**: *When* running root conformance matrix tests in `internal/testkit/conformance`, the test harness *shall* test static built-in backend plugins (OpenAI Responses, OpenAI Legacy, Anthropic, Gemini, Bedrock) without importing `internal/plugins/backends/acp`.
- **R3.2**: *When* validating ACP executable connector behavior, the system *shall* verify plugin parity against `internal/refbackend/acp` using executable connector test gates rather than duplicating runtime code inside root testkit packages.

### Requirement 4: Architecture Guards and Mirror Script Retirement

- **R4.1**: *When* backend plugin module verification scripts execute, the verification *shall* enforce root module isolation and executable plugin discovery without performing byte-for-byte mirror comparisons between `internal/plugins/backends/acp` and `connector-support/acp`.
- **R4.2**: *While* executing architecture boundary checks, the test suite *shall* assert that `internal/plugins/backends/acp` does not exist and that `connector-support/acp` remains the sole ACP runtime package.

### Requirement 5: Behavior and Error Surface Preservation (PR #239)

- **R5.1**: *When* ACP JSON-RPC errors are received from upstream agents or processes, `connector-support/acp` *shall* preserve the structured JSON-RPC error propagation (`RPCError`, `RPCErrorDetails`, `MapRPCErrorToCanonical`) introduced in PR #239.
- **R5.2**: *When* managing ACP transports, sessions, process trees, and model indexes, `connector-support/acp` *shall* maintain full feature parity for stdio and HTTP transport, handshake, session reuse, process-tree cleanup, runtime pool ensure/claim, and model index mapping.

### Requirement 6: Repository and Release Hygiene

- **R6.1**: *When* `.release-files` is validated, all 71 deleted `internal/plugins/backends/acp/*` paths *shall* be absent from the release manifest.
- **R6.2**: *When* production imports, testkit imports, build manifests, developer scripts, Makefile targets, and documentation are checked, all stale references to `internal/plugins/backends/acp` *shall* be removed or updated to `connector-support/acp`, explicitly permitting the architecture regression guard (`internal/archtest/acp_plugin_architecture_test.go`) and archived spec (`.kiro/specs/archive/acp-runtime-deduplication/`) to name the deleted path for enforcement and tracking.

### Requirement 7: Fuzz Target, Corpus, and Concurrency Test Preservation

- **R7.1**: *When* fuzz testing is performed, the system *shall* execute seed corpus verification and active fuzzing campaigns against `connector-support/acp`, preserving all canonical fuzz targets (`FuzzParseNDJSONLine`, `FuzzMapSessionUpdateToEvents`, `FuzzMergeHandshakeProfileExtensions`) and test corpora without loss of coverage.
- **R7.2**: *While* running concurrency and race detection, `connector-support/acp` *shall* maintain full race coverage and concurrency guarantees without weakening test assertions.
- **R7.3**: *When* concurrency tests are executed on Windows where `TestExecutableCache_ResetConcurrentWithLookups` is observed to occasionally block or time out in full package runs, execution guidelines and documentation *shall* acknowledge this pre-existing observed test behavior truthfully without weakening or deleting source assertions.

### Requirement 8: PR #239 Upstream Reconciliation

- **R8.1**: *Where* PR #239 is merged into `origin/main` prior to submitting this refactor PR, the branch *shall* be rebased or merged with `origin/main` to ensure commit history and PR #239 changes remain fully reconciled without losing error detail functionality.
