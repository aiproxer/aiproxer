# Implementation Plan

Spec-driven product implementation is complete. All tasks target the **external**
`connectors/cursorsdk` artifact. `internal/plugins/backends/cursorsdk` was never reintroduced.

- [x] 0. Spec gates (Task 8.3 of backend-connector-plugin-architecture) ÔÇö COMPLETE when `make kiro-spec-check SPEC=cursor-sdk-backend` passes
  - Keep requirements/design/tasks/file-plan/packaging/AGENTS aligned with external connector architecture.
  - Evidence (2026-07-20T10:42:51+02:00): `make kiro-spec-check SPEC=cursor-sdk-backend` PASS; archtest `TestCursorSDK_*` PASS; product implementation still blocked (`ready_for_implementation: false`).
  - _Validation: `make kiro-spec-check SPEC=cursor-sdk-backend`_

- [x] 1. Lock the exact SDK and bridge contracts with tests first
- [x] 1.1 Revalidate the pinned Cursor SDK local-agent contract
  - Isolated probe under `connectors/cursorsdk/bridge-node` for the exact pinned SDK version.
  - Convert validated shapes into sanitized fixtures before production bridge behavior.
  - Evidence: `bridge-node/src/liveProbe.ts` + `liveProbeLib.ts` (isolated probe); sanitized fixtures `internal/product/testdata/fixtures/sdk_contract.json` and `sdk_setting_sources_1.0.23.txt` (pinned SDK 1.0.23).
  - _Boundary: External connector research_
  - _Depends: none_
  - _Validation: `(cd connectors/cursorsdk/bridge-node && npm test -- --runInBand)`_
  - _Requirements: 2.6, 4.1, 4.6, 4.7, 4.8, 6.3, 6.4, 7.1, 9.5, 12.1, 12.3, 13.6_

- [x] 1.2 Define the versioned NDJSON bridge protocol
  - Failing Go + TypeScript contract tests for initialize/version, correlation, bounds, unknown mandatory, duplicate terminals.
  - Methods: models/list, agent/create, agent/send, run/cancel, agent/dispose, bridge/health, bridge/shutdown.
  - Evidence: `internal/product/protocol/` (`consts.go`, `frame.go`, `decode.go`, `validate.go`, `sequence.go`, `params.go`) with contract/hardening/fuzz tests (`protocol_test.go`, `hardening_test.go`, `fuzz_test.go`, `params_test.go`) and NDJSON fixtures under `internal/product/testdata/fixtures/protocol/`; TypeScript twin `bridge-node/src/protocol.ts` + `protocol.test.ts`.
  - _Boundary: External connector bridge contract_
  - _Depends: 1.1_
  - _Validation: `(cd connectors/cursorsdk && GOWORK=off go test ./...) && (cd connectors/cursorsdk/bridge-node && npm test)`_
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.7, 6.10, 11.1, 11.2, 12.1, 12.3, 13.2_

- [x] 1.3 Build deterministic fake-bridge harnesses
  - Fake companion executable for Go tests; mocked SDK runtime for Node tests.
  - Default Go tests require neither Node nor network/account access.
  - Evidence: `internal/product/fakebridge/` (`cmd/fake-cursor-sdk-bridge`, `build_exe.go`, `harness.go`, `script.go` + harness/subprocess tests); Node side `bridge-node/src/sdkMock.ts` + `sdkMock.test.ts`.
  - _Boundary: Tests_
  - _Depends: 1.2_
  - _Validation: `(cd connectors/cursorsdk && GOWORK=off go test ./...)`_
  - _Requirements: 2.5, 7.3, 7.4, 7.7, 8.5, 8.9, 8.10, 12.2, 12.3_

- [x] 2. Connector module skeleton and packaging
- [x] 2.1 Create `connectors/cursorsdk` module, release.yaml, closed manifest
  - Export `cursorsdk` with static / local_only / per_instance.
  - Declare bridge-node private companion; no root Node deps.
  - Archtests: forbidden root paths; no root require/replace.
  - Evidence: `connectors/cursorsdk/go.mod`, `release.yaml`, `manifest/template.backendplugin.json` (closed `golip.backendplugin.manifest/v1`), `bridge-node/package.json` + `package-lock.json` (private companion); archtest `TestCursorSDK_forbiddenInternalPathAbsent`, `TestCursorSDK_rootGoModHasNoCursorsdkModule`, `TestCursorSDK_rootPackageJSONHasNoCursorSDK`, `TestCursorSDK_connectorModulePresent` all PASS.
  - _Boundary: External connector packaging_
  - _Depends: 1.3_
  - _Validation: `make kiro-spec-check SPEC=cursor-sdk-backend && go test ./internal/archtest -run CursorSDK`_
  - _Requirements: 1.1, 3.4, 3.5, 13.1, 13.4, 13.5, 13.6, 13.7_

- [x] 2.2 Implement backendplugin service surface
  - Describe/Configure/Execute/ListModels/Close via public ABI; conformance against fake bridge.
  - Secrets via Configure secrets map; never argv.
  - Evidence: `internal/service/service.go` + `config.go` implement the public `pkg/lipsdk/backendplugin` surface; `service_test.go` and `internal/product/bridge_identity_parity_test.go` exercise Describe/Configure/Execute/ListModels/Close against the fake bridge; security policy in `internal/product/security_policy.go` (+ tests).
  - _Boundary: External connector ABI_
  - _Depends: 2.1_
  - _Validation: `(cd connectors/cursorsdk && GOWORK=off go test ./... -run 'TestDescribe_|TestConfigure_|TestParity_|TestConformance')`_
  - _Requirements: 3.1, 3.2, 3.3, 3.7, 3.8, 13.2, 13.3_

- [x] 3. Node bridge companion
- [x] 3.1 Startup, version, models/list
  - Evidence: `bridge-node/src/main.ts`, `server.ts` (+ `server.test.ts`), `models.ts` (+ `models.test.ts`), protocol initialize/version handshake in `protocol.ts`.
  - _Validation: `(cd connectors/cursorsdk/bridge-node && npm test && npm run typecheck)`_
  - _Requirements: 2.1, 2.2, 2.4, 2.5, 2.6, 4.1, 4.4, 11.1, 11.2, 12.3, 13.6_

- [x] 3.2 Local agent create/send streaming
  - Evidence: `bridge-node/src/agents.ts` (+ `agents.test.ts`), `inMemoryLocalAgentStore.ts` (+ test), `sdk_runtime.ts` (+ `sdk_runtime.test.ts`), live scenario coverage in `liveScenarios*.ts` (opt-in).
  - _Requirements: 5.1, 5.6, 6.2, 6.3, 6.4, 6.5, 6.6, 6.8, 9.1, 9.2, 9.4, 9.5, 9.6, 9.7_

- [x] 3.3 Cancel, dispose, shutdown, open-handle tests
  - Evidence: `bridge-node/src/server.test.ts`, `sdk_runtime.test.ts`, `agents.test.ts` (cancel/dispose/shutdown); Go-side `internal/product/cancel_timeout_kill_test.go`, `run_stream_close_cancel_test.go`, `run_death_terminal_test.go`.
  - _Requirements: 5.6, 5.7, 7.1, 7.2, 7.7, 8.1, 8.5, 8.7, 9.3, 12.3, 13.9_

- [x] 3.4 Package companion reproducibly (lockfile, engines, doctor --version)
  - Evidence: `bridge-node/package-lock.json` (locked), `package.json` engines + `bin/lip-cursor-sdk-bridge.js` entry, `bridge-node/src/package.test.ts` (packaging/doctor version contract), `tsconfig.build.json` reproducible build.
  - _Requirements: 2.6, 3.5, 3.6, 3.7, 11.1, 11.6, 12.5, 13.6_

- [x] 4. Go bridge owner, pool, stream
- [x] 4.1 Bridge process owner (singleflight, handshake, bounded IO, kill tree)
  - Evidence: `internal/product/bridge_process.go`, `bridge_proc.go`, `bridge_proc_os_{unix,windows}.go` with `bridge_process_*_test.go`, `bridge_proc_os_*_test.go`, `cancel_timeout_kill_test.go`; CI `process-tree` matrix (ubuntu/macOS/Windows) SUCCESS on PR #208; `codex-race` SUCCESS (Linux race evidence).
  - _Validation: `(cd connectors/cursorsdk && GOWORK=off go test -race ./... -run 'Bridge|Process|Kill')`_ (Linux CI)
  - _Requirements: 2.7, 3.5, 3.8, 7.3, 7.4, 7.7, 8.1, 8.5, 8.7, 8.8, 8.9, 8.10, 13.9_

- [x] 4.2 History coordination and agent pool
  - Evidence: `internal/product/history.go` (+ `history_test.go`, `parallel_race_history_test.go`), `session_pool.go` (+ `session_pool_test.go`), `bridge_agent.go`, `agent_key.go`, `pending_queue.go` (+ test).
  - _Requirements: 5.1ÔÇô5.8, 8.2, 8.3, 8.4, 8.10, 9.2_

- [x] 4.3 Managed canonical stream mapping
  - Evidence: `internal/product/stream.go` (+ `stream_test.go`), `event_mapper.go` (+ `event_mapper_test.go`, `event_mapper_apikey_sanitize_test.go`, `fuzz_event_mapper_test.go`), `normalize.go` (+ `normalize_canonical_test.go`), `run_sub_*_test.go` stream lifecycle suites.
  - _Requirements: 6.1ÔÇô6.10, 7.5, 7.6, 10.*, 13.8_

- [x] 5. Host integration proofs
- [x] 5.1 Windows/Linux BuildBootstrap e2e with staged plugin + fake bridge companion
  - Real discovery/trust/IPC; no TestLauncher/Fake DialSession; no live Cursor account.
  - Evidence: host-level coverage realized in the connector module after the external-delivery retarget: `internal/product/live_bridge_*_test.go` (staged plugin + fake bridge companion), `platform_smoke*_test.go`, `generation_isolation_test.go`, `coexist/modelregistry_coexistence_test.go`; CI `platform-smoke` (ubuntu/macOS/Windows) and the dedicated `Cursor SDK Platform Smoke` workflow SUCCESS on PRs #193/#208. Original `internal/infra/runtimebundle` probe was superseded when delivery moved to the external connector module (merge note below).
  - _Validation: `go test ./internal/infra/runtimebundle -run TestPhase.*CursorSDK`_ (superseded; see evidence)
  - _Requirements: 13.3, 12.2, 12.5_

- [x] 5.2 Absence and mandatory checks
  - Root builds with connector absent; configured-missing fails closed.
  - Evidence: archtest `TestCursorSDK_forbiddenInternalPathAbsent`, `TestCursorSDK_standardDistributionDoesNotMandateCursorSDK`, `TestCoreAndProviderBoundaryDoNotImportCursorSDKBackend`, `TestCursorSDKNpmImportsStayInsideBridgeBoundary`; `internal/standardplugins` posture/inventory tests keep `cursorsdk` out of essential/static tables; isolated-root CI smoke on PRs #193/#208.
  - _Requirements: 1.1, 13.7_

- [x] 6. Documentation and experimental labeling
  - Evidence: `docs/cursor-sdk-backend.md` (operator guide, experimental labeling), `docs/cursor-sdk-comparison-report.md`, `config/examples/cursor-sdk-experimental.yaml`, `EchoesVault/pages/cursor-sdk-backend.md`, README pointer.
  - _Requirements: 1.5, 11.6, 12.7ÔÇô12.9_

## Merge note (2026-07-27T00:45:00+02:00)

Main completed substantial in-tree `internal/plugins/backends/cursorsdk` work (checked tasks on main). That tree has been moved to `connectors/cursorsdk` on the hybrid connector branch. Remaining open tasks below continue the external-connector checklist (ABI conformance polish, packaging proofs, host e2e). Do not reintroduce optional connectors into the essential/static root table.

## Closeout evidence (2026-07-28)

- PR [#193](https://github.com/matdev83/go-llm-interactive-proxy/pull/193) (merged 2026-07-19): `qa`, `qa-run`, `platform-smoke` (ubuntu/macOS/Windows), `CodeQL`, `Analyze` all SUCCESS.
- PR [#208](https://github.com/matdev83/go-llm-interactive-proxy/pull/208) (merged 2026-07-27): `qa`, `qa-run`, `platform-smoke` + `process-tree` + `cross-platform-qa` (ubuntu/macOS/Windows), `codex-race`, and the dedicated `Cursor SDK Platform Smoke` workflow all SUCCESS. The single `release-gates (ubuntu-latest)` FAILURE belongs to parent spec `backend-connector-plugin-architecture` task 9.5 and is a documented external blocker (`phase9-task95-external-release-blocker.md`); it is not part of this spec's checklist.
- Closeout local run (2026-07-28): `GOWORK=off go test ./...` in `connectors/cursorsdk` PASS; `go test ./internal/archtest -run CursorSDK` PASS.
- Spec archived to `.kiro/specs/archive/cursor-sdk-backend/`; development-gate speccheck registration retired (architecture invariants remain enforced by `internal/archtest` `TestCursorSDK_*` / `TestCursorSDKNpmImportsStayInsideBridgeBoundary`).
