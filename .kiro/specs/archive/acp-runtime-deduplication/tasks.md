# Tasks: ACP Runtime Deduplication

## Implementation Overview

This task list enforces TDD: architecture and testkit importers are updated first (RED / TEST-FIRST), followed by deletion of `internal/plugins/backends/acp` and repo hygiene updates (GREEN / IMPLEMENTATION), thorough verification across module boundaries (VERIFY), and final PR #239 rebase reconciliation (RECONCILE).

---

## Tasks

### Phase 1: Architecture & Conformance Testkit Migration (RED / TEST-FIRST)

- [x] **Task 1.1: Update Architecture Boundary Tests**
  - **_Boundary:_**: `internal/archtest`
  - **_Depends:_**: None
  - **Goal**: Add explicit architecture checks requiring the deletion of `internal/plugins/backends/acp` and single ownership in `connector-support/acp`.
  - **Files**:
    - `internal/archtest/acp_plugin_architecture_test.go`
    - `internal/archtest/identity_transport_boundaries_test.go`
  - **Details**:
    - Add `TestACP_internalBackendPackageDeleted` in `acp_plugin_architecture_test.go` asserting `internal/plugins/backends/acp` is absent.
    - Remove `./internal/plugins/backends/acp` from `identity_transport_boundaries_test.go`.
  - **Validation**: Observe RED test failure by running `go test ./internal/archtest -run 'TestACP_|TestIdentityTransport_'` (must fail prior to package deletion because `internal/plugins/backends/acp` still exists). Record RED observation.

- [x] **Task 1.2: Add Conformance Retirement Guard in `parity_acp_test.go`**
  - **_Boundary:_**: `internal/testkit/conformance`
  - **_Depends:_**: None
  - **Goal**: Add explicit test guard protecting ACP retirement from static built-in backend conformance tables.
  - **Files**:
    - `internal/testkit/conformance/parity_acp_test.go`
  - **Details**:
    - Implement `TestParity_ACP_retiredFromStaticMatrix(t *testing.T)` asserting no cell in `AllCells()` has `cell.Backend == "acp"`.
  - **Validation**: Observe RED test failure by running `go test ./internal/testkit/conformance/... -run TestParity_ACP_retiredFromStaticMatrix` (fails prior to removing ACP from static matrix in `matrix.go`). Record RED observation.

- [x] **Task 1.3: Refactor Module Check Scripts for Single Implementation**
  - **_Boundary:_**: `scripts/`
  - **_Depends:_**: None
  - **Goal**: Remove obsolete byte-for-byte mirror comparisons and replace them with single-implementation architecture guards.
  - **Files**:
    - `scripts/backend-plugin-module-checks.sh`
    - `scripts/backend-plugin-module-checks.ps1`
  - **Details**: Remove `$MirrorFiles` / `MIRROR_FILES` loops. Preserve module discovery and root-without-connectors build tests.
  - **Validation**: Execute script syntax check for `scripts/backend-plugin-module-checks.ps1` and `scripts/backend-plugin-module-checks.sh`.

- [x] **Task 1.4: Migrate Root Conformance Testkit Importers**
  - **_Boundary:_**: `internal/testkit/conformance`
  - **_Depends:_**: Task 1.2 (observing Task 1.2 RED failure)
  - **Goal**: Remove `internal/plugins/backends/acp` imports from root conformance testkit packages across all 6 owned conformance files.
  - **Files**:
    - `internal/testkit/conformance/matrix.go`
    - `internal/testkit/conformance/harness.go`
    - `internal/testkit/conformance/refparity.go`
    - `internal/testkit/conformance/error_upstream.go`
    - `internal/testkit/conformance/sanity_emulator_wiring_test.go`
    - `internal/testkit/conformance/parity_acp_test.go`
  - **Details**:
    - Remove `"acp"` from `BundledBackendIDs()` in `matrix.go`.
    - Remove `acp` package import and `case acp.ID:` in `harness.go`, `refparity.go`, `error_upstream.go`, and `sanity_emulator_wiring_test.go`.
  - **Validation**: Confirm `go test ./internal/testkit/conformance/...` compiles cleanly and `TestParity_ACP_retiredFromStaticMatrix` turns GREEN.

---

### Phase 2: Codebase Cleanup & Deletion (GREEN / IMPLEMENTATION)

- [x] **Task 2.1: Delete `internal/plugins/backends/acp` Package**
  - **_Boundary:_**: `internal/plugins/backends/acp`
  - **_Depends:_**: Task 1.1 (observing Task 1.1 RED failure) & Task 1.4 (conformance testkit migration complete)
  - **Goal**: Remove the duplicated internal ACP package entirely.
  - **Files**:
    - Delete directory `internal/plugins/backends/acp/` and all 71 contained files and testdata.
  - **Validation**: Confirm directory `internal/plugins/backends/acp` is removed. Run `go test ./internal/archtest -run TestACP_internalBackendPackageDeleted` and verify it turns GREEN.

- [x] **Task 2.2: Update `.release-files` Manifest**
  - **_Boundary:_**: `.release-files`
  - **_Depends:_**: Task 2.1
  - **Goal**: Keep release manifest aligned with codebase state.
  - **Files**:
    - `.release-files`
  - **Details**: Remove lines 3186–3256 (`internal/plugins/backends/acp/*`) and ensure `internal/testkit/conformance/parity_acp_test.go` is present.
  - **Validation**: `git grep -n 'internal/plugins/backends/acp' .release-files` returns 0 matches.

- [x] **Task 2.3: Update Scripts, Makefile, and Documentation References**
  - **_Boundary:_**: `Makefile`, `scripts/`, `docs/`
  - **_Depends:_**: Task 2.1
  - **Goal**: Remove stale path references to the deleted internal package across repo tools and docs.
  - **Files**:
    - `Makefile`
    - `scripts/check-adhoc-goroutines.sh`
    - `scripts/check-adhoc-goroutines.ps1`
    - `docs/release-gates.md`
    - `docs/testing-determinism.md`
  - **Details**:
    - `Makefile`: Remove `./internal/plugins/backends/acp` line from `parity-acp-plugin`.
    - `scripts/check-adhoc-goroutines.{sh,ps1}`: Remove `internal/plugins/backends/acp/transport_stdio.go`.
    - `docs/release-gates.md` and `docs/testing-determinism.md`: Update path references to `connector-support/acp`.
  - **Validation**: Run `git grep -n 'internal/plugins/backends/acp'` across the repository and verify that all remaining matches are strictly classified as either (a) architecture regression test assertions in `internal/archtest/acp_plugin_architecture_test.go` or (b) archived spec documentation under `.kiro/specs/archive/acp-runtime-deduplication/`, with zero matches in production code, testkit helpers, build manifests, scripts, Makefile, or docs.

---

### Phase 3: Verification & Parity Gate Validation (VERIFY)

- [x] **Task 3.1: Execute Canonical ACP Support Unit, Fuzz, and Race Tests**
  - **_Boundary:_**: `connector-support/acp`
  - **_Depends:_**: Task 2.1, Task 2.3
  - **Goal**: Verify `connector-support/acp` unit, fuzz, and concurrency tests pass cleanly without weakening assertions.
  - **Validation**:
    - Unit tests: `(cd connector-support/acp && GOWORK=off go test ./...)`
    - Seed corpus verification: `(cd connector-support/acp && GOWORK=off go test -run 'FuzzParseNDJSONLine|FuzzMapSessionUpdateToEvents|FuzzMergeHandshakeProfileExtensions')`
    - Active 30-second fuzz campaigns (in PowerShell/sh enter `connector-support/acp` directory or use subshell):
      - `(cd connector-support/acp && GOWORK=off go test -fuzz=^FuzzParseNDJSONLine$ -fuzztime=30s -run=^$ .)`
      - `(cd connector-support/acp && GOWORK=off go test -fuzz=^FuzzMapSessionUpdateToEvents$ -fuzztime=30s -run=^$ .)`
      - `(cd connector-support/acp && GOWORK=off go test -fuzz=^FuzzMergeHandshakeProfileExtensions$ -fuzztime=30s -run=^$ .)`
    - Concurrency/race tests (where supported): `(cd connector-support/acp && GOWORK=off go test -race ./...)` (note pre-existing observed Windows blocking/timeout in `TestExecutableCache_ResetConcurrentWithLookups` handled truthfully).

- [x] **Task 3.2: Execute Executable Connector Parity Gates**
  - **_Boundary:_**: `connectors/` & `internal/refbackend/acp`
  - **_Depends:_**: Task 2.1
  - **Goal**: Validate executable ACP connectors function properly against canonical support module.
  - **Validation**:
    - `make parity-acp-plugin`
    - `make parity-cli-acp-plugins`

- [x] **Task 3.3: Execute Root Architecture, Module Isolation, and Quality Checks**
  - **_Boundary:_**: Repo root & `internal/archtest`
  - **_Depends:_**: Task 2.1, Task 2.2, Task 2.3
  - **Goal**: Confirm root build, arch tests, and module checks succeed without internal ACP.
  - **Validation**:
    - `make backend-plugin-module-checks`
    - `go test ./internal/archtest/...`
    - `make quality-checks`

- [x] **Task 3.4: Validate Root Module Isolation without Optional Modules**
  - **_Boundary:_**: `cmd/lipstd`
  - **_Depends:_**: Task 1.3, Task 2.1
  - **Goal**: Prove `cmd/lipstd` builds without `connectors/` or `connector-support/`.
  - **Validation**:
    - Verify temporary build check in `make backend-plugin-module-checks` succeeds.

---

### Phase 4: PR #239 Upstream Reconciliation (RECONCILE)

- [x] **Task 4.1: Rebase / Reconcile Branch onto `origin/main` Pre-submission**
  - **_Boundary:_**: Git workspace / PR readiness
  - **_Depends:_**: Task 3.1, Task 3.2, Task 3.3, Task 3.4
  - **Goal**: Ensure final PR is rebased on `origin/main` after PR #239 merges without losing structured error propagation.
  - **Details**:
    - Fetch `origin/main` after PR #239 is merged.
    - Rebase `refactor/acp-runtime-dedup` branch.
    - Confirm structured JSON-RPC error propagation in `connector-support/acp/rpc_error.go` remains intact and tested.
  - **Validation**: `git log` clean merge/rebase state; `(cd connector-support/acp && GOWORK=off go test -run TestRPCError ./...)` passes.

## Closeout evidence (2026-08-08)

The implementation was delivered by PR [#242](https://github.com/matdev83/go-llm-interactive-proxy/pull/242), merged at `70d500d9935363da31c4368b7e320590bc6c9f6a` after PR [#239](https://github.com/matdev83/go-llm-interactive-proxy/pull/239) (`79605bb8e783399c424f3c00cf865459360c302f`) supplied the structured ACP error propagation prerequisite. The current archive was prepared from merged `origin/main` at `8eed2636549f4c6bdf1783a6d747ee94815a7135`.

- **Task 3.1:** In the exact merged tree, `GOWORK=off go test ./...`, the three fuzz seed invocations, `TestRPCError`, and three 30-second campaigns (`FuzzParseNDJSONLine`, `FuzzMapSessionUpdateToEvents`, and `FuzzMergeHandshakeProfileExtensions`) passed in `connector-support/acp`. The local Windows race command cannot execute because the Windows cgo toolchain exits before tests start. Strict Linux workflow [31265106123](https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/31265106123) failed on unrelated root-module findings in `internal/refclient/openresponses` and `tools/backendplugin`; it did not report an ACP failure. PR #242's cross-platform/QA CI checks passed. The dedicated ACP Linux race follow-up is recorded in `phase3-task31-race-blocker.md`.
- **Task 3.2:** `make parity-acp-plugin` and `make parity-cli-acp-plugins` passed in the exact merged tree.
- **Task 3.3 / 3.4:** `go test ./internal/archtest/...` and `make quality-checks` passed. The Windows `make backend-plugin-module-checks` run completed root discovery/build/module checks but stopped at unrelated existing Windows/root test failures (`TestDocs_Architecture_OneRuntimeOwnershipContract` and `TestProcessTree_WindowsJobObjectDirect`); this limitation is recorded rather than hidden. PR #242's required CI test, QA, cross-platform, process-tree, and platform-smoke checks passed.
- **Task 4.1:** Reconciliation is complete: PR #239 merged before PR #242, PR #242 is merged to `main`, and the focused ACP RPC error test passes. No ACP implementation branch remains required.

This closeout changes only ACP specification bookkeeping and release-manifest paths. The ongoing OpenAI Codex native compaction/encrypted-reasoning specification and implementation state were not modified.
