# Tasks: Windows Task Reliability

## Implementation Overview

This plan is TDD-first. Contract tests use the exact package paths and test names below before implementation tasks change behavior. The implementation is limited to the tooling runner/routes, existing backend-plugin tooling, ACP support, and the SQLite storage adapter named by the requirements. No task authorizes production application, public SDK, Make target coverage reduction, staging, or committing.

Ordinary Make prerequisites receive independent phase budgets. Only task 3.4's explicit profile coordinator owns an aggregate profile deadline.

## Phase 1: Contract Tests First

- [x] 1.1 Add runner request/result contract tests
  - _Boundary:_ `tools/taskrunner/runner_contract_test.go`
  - _Depends:_ none
  - Add exact tests `TestRunner_Success`, `TestRunner_ChildFailure`, `TestRunner_DeadlineExceeded`, `TestRunner_WorkingDirectory`, `TestRunner_ChildEnvironment`, `TestRunner_InvalidRequest`, `TestRunner_StreamDoesNotCapture`, `TestRunner_CaptureHeadTailAndAggregateBounds`, and `TestRunner_RedactsCapturedSecrets`.
  - Use a helper executable implemented by `tools/taskrunner/testhelper/main.go` with `-mode=success|fail|sleep|print-env|print-cwd|spawn-grandchild`; use channels/pipe markers rather than sleep-based success assertions.
  - Assert exact result kinds, raw child exit status, child-only cwd/env, no parent env/cwd mutation, numeric 64 KiB per-stream/256 KiB aggregate capture bounds, 32 KiB head/tail limits, and stream mode's zero retained bytes.
  - _Validation:_ `go test ./tools/taskrunner -run 'TestRunner_(Success|ChildFailure|DeadlineExceeded|WorkingDirectory|ChildEnvironment|InvalidRequest|StreamDoesNotCapture|CaptureHeadTailAndAggregateBounds|RedactsCapturedSecrets)' -count=1 -timeout=2m`
  - _Requirements: 1.1-1.7, 9.1-9.3_

- [x] 1.2 Add Windows Job Object and POSIX process-group contract tests
  - _Boundary:_ `tools/taskrunner/process_tree_windows_test.go`, `tools/taskrunner/process_tree_posix_test.go`, `tools/taskrunner/testhelper/main.go`
  - _Depends:_ 1.1
  - Add exact tests `TestProcessTree_WindowsJobObjectDirect`, `TestProcessTree_WindowsJobObjectShell`, and `TestProcessTree_POSIXProcessGroup`; platform files shall use `windows` and `!windows` build constraints.
  - The helper shall write a grandchild PID/ready marker, and tests shall assert timeout classification, cleanup result, direct-child reap, and descendant termination. The shell case shall use `cmd.exe /C` on Windows and `sh -c` on POSIX only where appropriate.
  - Use bounded test contexts and process-state polling only for cleanup observation; do not assert a fixed elapsed duration.
  - _Validation:_ `go test ./tools/taskrunner -run 'TestProcessTree_(WindowsJobObjectDirect|WindowsJobObjectShell|POSIXProcessGroup)' -count=1 -timeout=2m`
  - _Requirements: 2.1-2.5_

- [x] 1.3 Add exact Make target and Linux evidence contracts
  - _Boundary:_ `internal/qa/windows_task_reliability_contract_test.go`
  - _Depends:_ none
  - Add exact tests `TestWindowsTaskReliability_TargetTableComplete`, `TestWindowsTaskReliability_WindowsRoutes`, `TestWindowsTaskReliability_FuzzAndParitySelectors`, `TestWindowsTaskReliability_RaceSkip`, `TestWindowsTaskReliability_LinuxEvidence`, `TestWindowsTaskReliability_PostgresAndExternalBlockers`, and `TestWindowsTaskReliability_CallSiteAudit`.
  - Parse `.PHONY` from `Makefile` and compare every name against the design table; inspect Makefile, both quality scripts, both module scripts, `scripts/race-check.ps1`, all current fuzz/parity recipes, and the workflow/job/command strings named in the design.
  - Assert Windows paths reject POSIX inline env/`cd`/`/dev/null`/`command -v`, preserve selectors and `GOWORK=off`, distinguish `SKIP` from `PASS`, and never classify missing PostgreSQL/linter/vulnerability/package prerequisites as unsupported.
  - _Validation:_ `go test ./internal/qa -run 'TestWindowsTaskReliability_(TargetTableComplete|WindowsRoutes|FuzzAndParitySelectors|RaceSkip|LinuxEvidence|PostgresAndExternalBlockers|CallSiteAudit)' -count=1 -timeout=4m`
  - _Requirements: 3.1-3.6, 5.1-5.5, 6.1-6.3, 9.1-9.3_

- [x] 1.4 Add bounded backend-plugin orchestration contracts
  - _Boundary:_ `tools/backendplugin/bounded_orchestration_contract_test.go`
  - _Depends:_ 1.1, 1.3
  - Add exact tests `TestBoundedOrchestration_ModulePhaseLabels`, `TestBoundedOrchestration_StopsDependentPhase`, `TestBoundedOrchestration_CleansDescendants`, `TestBoundedOrchestration_StaticAndFullProfiles`, and `TestBoundedOrchestration_NoUnboundedProductionExec`.
  - Use a fake runner/child command seam; assert root isolation, dynamic discovery, synthetic discovery, root-without-connectors coverage, phase labels, `GOWORK=off`, per-phase timeout propagation, and no dependent phase after failure.
  - Inspect all inventory rows in `design.md`; fail the contract if selected production tooling uses a direct `os/exec` wait without a documented `parent-bounded` exemption.
  - _Validation:_ `go test ./tools/backendplugin -run 'TestBoundedOrchestration_' -count=1 -timeout=8m`
  - _Requirements: 4.1-4.4, 6.1-6.3, 9.1_

- [x] 1.5 Add ACP reset-generation and real smoke contracts
  - _Boundary:_ `connector-support/acp/lookpath_test.go`
  - _Depends:_ none
  - Add exact tests `TestExecutableCache_ResetGeneration`, `TestExecutableCache_DeterministicConcurrency`, `TestExecutableCache_InstanceOwnership`, and `TestExecutableCache_RealLookupSmoke`.
  - Use `NewExecutableCache` with a blocking resolver and a fixed channel schedule to prove old in-flight lookups may finish, post-reset lookups use a fresh entry, no reset cancels the resolver, and caches do not share entries.
  - The real smoke must resolve `go` and `lip-acp-reliability-known-missing-7f4d`; missing `go` calls `t.Fatalf` with PATH/install guidance and never skips.
  - _Validation:_ `cd connector-support/acp && GOWORK=off go test . -run 'TestExecutableCache_(ResetGeneration|DeterministicConcurrency|InstanceOwnership|RealLookupSmoke)' -count=1 -timeout=2m` (run inside the nested module with `GOWORK=off`)
  - _Requirements: 7.1-7.4, 9.2_

- [x] 1.6 Add SQLite retry contracts and observer seam tests
  - _Boundary:_ `internal/infra/metering/journalstore/sqlite_retry_contract_test.go`
  - _Depends:_ none
  - Add exact tests `TestSQLiteRetry_BusyEventuallySucceeds`, `TestSQLiteRetry_WholeTransactionRestarts`, `TestSQLiteRetry_CancellationStopsBeforeNextAttempt`, `TestSQLiteRetry_ObserverUsesDeterministicClock`, `TestSQLiteRetry_NonBusyDoesNotRetry`, `TestSQLiteRetry_PostgresDoesNotRetry`, and `TestSQLiteRetry_LockReleased`.
  - Use a file-backed modernc SQLite fixture and injected retry clock/sleeper/observer; assert 12 total attempts, exact 5/10/20/40/80/160ms backoffs then a 250ms cap, 2s budget, transaction restart, idempotency, rollback/commit release, and no blanket test retry.
  - _Validation:_ `go test ./internal/infra/metering/journalstore -run 'TestSQLiteRetry_' -count=1 -timeout=4m`
  - _Requirements: 8.1-8.6, 9.2_

## Phase 2: Implement the Runner and Routing

- [x] 2.1 Implement `tools/taskrunner` request validation, env/cwd, outcomes, and bounded output
  - _Boundary:_ `tools/taskrunner/*.go`
  - _Depends:_ 1.1
  - Implement `Request`, `Result`, `Kind`, `OutputMode`, `Run`, child context deadline selection, copied environment construction, child-only cwd, stream mode, capture head/tail/aggregate buffers, exact redaction, pipe drain, wait/reap, and exit mapping.
  - Keep all output limits numeric and reject invalid limits before spawn. Preserve raw child exit status while mapping CLI status separately.
  - _Validation:_ `go test ./tools/taskrunner -run 'TestRunner_' -count=1 -timeout=2m`
  - _Requirements: 1.1-1.7, 9.1-9.3_

- [x] 2.2 Implement platform cleanup adapters with stdlib/x/sys only
  - _Boundary:_ `tools/taskrunner/process_windows.go`, `tools/taskrunner/process_posix.go`, `tools/taskrunner/process_other.go`
  - _Depends:_ 1.2, 2.1
  - Implement Windows Job Object creation/configuration/assignment/close and assignment race behavior with `golang.org/x/sys/windows`; implement POSIX `Setpgid` and negative-PID group kill; make cleanup idempotent and preserve original outcome.
  - Do not use `taskkill` in `tools/taskrunner`; keep existing ACP/Cursor lifecycle cleanup separate.
  - _Validation:_ `go test ./tools/taskrunner -run 'TestProcessTree_' -count=1 -timeout=2m`, `go test ./tools/taskrunner -run 'TestRunner_DeadlineExceeded' -count=1 -timeout=2m`
  - _Requirements: 2.1-2.5_

- [x] 2.3 Implement exact CLI and profile command
  - _Boundary:_ `tools/taskrunner/cmd/lip-taskrunner/main.go`
  - _Depends:_ 2.1, 2.2
  - Implement all flags and exit codes from requirements 1.5-1.6. Require `--` before the child argv in normal mode. Implement `profile --name windows-full-release --root .` with a 120-minute coordinator context and exact phase labels; distinguish child timeout from profile timeout.
  - Do not make ordinary Make recipes call the profile command or claim a Make-graph aggregate deadline.
  - _Validation:_ `go test ./tools/taskrunner/... -count=1 -timeout=2m`
  - _Requirements: 1.1-1.6, 3.5-3.6, 9.1_

- [x] 2.4 Route Windows scripts and Make nested commands through the CLI
  - _Boundary:_ `Makefile`, `scripts/quality-checks.ps1`, `scripts/quality-checks.sh`, `scripts/backend-plugin-module-checks.ps1`, `scripts/backend-plugin-module-checks.sh`, and the parity/fuzz recipes in `Makefile`
  - _Depends:_ 1.3, 2.3
  - Replace every remaining inline Windows command with exact `go run ./tools/taskrunner/cmd/lip-taskrunner` invocations, explicit `--cwd`, `--env GOWORK=off`, labels, and target phase budgets. Preserve all current fuzz and parity command arguments.
  - Keep PowerShell `$env:GOWORK` and `Set-Location` out of parent-scoped orchestration; use child options. Keep POSIX scripts semantically equivalent and use the runner where descendant ownership is required.
  - Implement Windows linter discovery with `Get-Command`, preserving preferred `golangci-lint`, fallback `staticcheck`, and failure guidance.
  - _Validation:_ `go test ./internal/qa -run 'TestWindowsTaskReliability_' -count=1 -timeout=4m`, `git diff --check`
  - _Requirements: 3.1-3.5, 5.1, 5.3, 6.2_

## Phase 3: Integrate Tooling and Storage

- [x] 3.1 Integrate `taskrunner.Run` into backend-plugin tools
  - _Boundary:_ `tools/backendplugin/release_gates/*.go`, `tools/backendplugin/package_plugins/main.go`, `tools/backendplugin/crossplatform_qa/main.go`, `tools/backendplugin/isolated_root_qa/main.go`, `tools/backendplugin/installed_plugin_smoke/main.go`
  - _Depends:_ 1.4, 2.1, 2.2
  - Replace each `runner/context-bounded` inventory call with labeled runner requests; preserve selectors, `GOWORK=off`, capture/report redaction, temp artifact cleanup, static/full modes, and fail-fast phase order. Keep the interactive installed smoke server parent-bounded with explicit context/readiness/cleanup.
  - _Validation:_ `go test ./tools/backendplugin -run 'TestBoundedOrchestration_' -count=1 -timeout=8m`, `go test ./tools/backendplugin/... -run 'Test(ReleaseGates|CrossPlatformQA|Package|DiscoverModules)' -count=1 -timeout=8m`
  - _Requirements: 4.1-4.4, 6.1, 6.3, 9.1_

- [x] 3.2 Implement ACP resolver constructor and reset generation
  - _Boundary:_ `connector-support/acp/lookpath.go`
  - _Depends:_ 1.5
  - Add `type Resolver func(string) (string, error)`, `NewExecutableCache`, generation-safe reset, zero-value default resolver behavior, and preserve all connector constructors/instance ownership.
  - _Validation:_ `cd connector-support/acp && GOWORK=off go test . -run 'TestExecutableCache_' -count=1 -timeout=2m` (run inside the nested module with `GOWORK=off`)
  - _Requirements: 7.1-7.4_

- [x] 3.3 Implement SQLite-only whole-transaction retry
  - _Boundary:_ `internal/infra/metering/journalstore/durable.go`, `internal/infra/metering/journalstore/sqlite_retry.go`, `internal/infra/metering/journalstore/sqlite_retry_contract_test.go`
  - _Depends:_ 1.6
  - Add exact `DurableConfig` retry seams and production defaults; wrap the full current `Append` transaction body; classify modernc base codes 5/6 only for SQLite; implement 12 attempts, 2s budget, exact backoffs capped at 250ms, context-aware sleep, rollback before retry, terminal classifications, and observer calls.
  - Remove the existing `retrySQLiteBusy` test helper as a reliability mechanism; fixtures may coordinate lock ownership but must call `Append` directly.
  - _Validation:_ `go test ./internal/infra/metering/journalstore -run 'TestSQLiteRetry_' -count=1 -timeout=4m`, `go test ./internal/infra/metering/journalstore -count=1 -timeout=10m`
  - _Requirements: 8.1-8.6_

- [x] 3.4 Add and document the explicit full profile coordinator
  - _Boundary:_ `tools/taskrunner/cmd/lip-taskrunner/profile.go`, `tools/taskrunner/cmd/lip-taskrunner/profile_test.go`
  - _Depends:_ 2.3, 2.4, 3.1
  - Add exact test `TestProfile_WindowsFullReleaseDeadlinePropagation` using a shared parent context/deadline and an injectable phase runner seam; assert one 120-minute profile context, sequential phase stop on failure, child deadline propagation, and distinct profile-timeout exit (2) versus child failure (1).
  - Implement the coordinator because the explicit full profile is the only place this spec promises an aggregate deadline. Never convert GNU Make prerequisite semantics into an aggregate promise by implication.
  - _Validation:_ `go test ./tools/taskrunner/cmd/lip-taskrunner -run 'TestProfile_WindowsFullReleaseDeadlinePropagation' -count=1 -timeout=2m`
  - _Requirements: 3.5-3.6, 9.2_

## Phase 4: Evidence and Final Review

- [x] 4.1 Add static call-site audit and target coverage enforcement
  - _Boundary:_ `internal/qa/windows_task_reliability_contract_test.go`, `tools/backendplugin/bounded_orchestration_contract_test.go`
  - _Depends:_ 2.4, 3.1
  - Enforce the complete `.PHONY` table and inventory; include Make fuzz/parity recipes, both quality scripts, both module scripts, direct `os/exec` call sites, parent-bounded exemptions, and excluded lifecycle rationale. Fail on unbounded new selected tooling calls.
  - _Validation:_ `go test ./internal/qa -run 'TestWindowsTaskReliability_(TargetTableComplete|CallSiteAudit|FuzzAndParitySelectors)' -count=1 -timeout=4m`, `go test ./tools/backendplugin -run 'TestBoundedOrchestration_NoUnboundedProductionExec' -count=1 -timeout=8m`
  - _Requirements: 3.1, 4.1, 5.1, 6.1-6.3_

- [x] 4.2 Add race-skip and Linux-authority preservation checks
  - _Boundary:_ `internal/qa/windows_task_reliability_contract_test.go`
  - _Depends:_ 1.3, 4.1
  - Assert exact `SKIP:` semantics in `scripts/race-check.ps1`, Linux `-race`/security/release/conformance/fuzz workflow commands and job names, required/existing scheduled status, and that Windows `SKIP`/`external_blocker` cannot satisfy Linux evidence.
  - _Validation:_ `go test ./internal/qa -run 'TestWindowsTaskReliability_(RaceSkip|LinuxEvidence|PostgresAndExternalBlockers)' -count=1 -timeout=4m`
  - _Requirements: 5.2-5.5, 9.1_

- [x] 4.3 Run focused contract and repository verification
  - _Boundary:_ specification acceptance
  - _Depends:_ 3.2, 3.3, 4.1, 4.2
  - Run the exact focused commands in `design.md`; inspect diagnostics for target/phase/module/platform/outcome/next-action fields.
  - Verify `spec.json` reflects the approved `tasks-approved` state with all approval flags true, verify `.release-files` is sorted and contains exactly the five spec artifacts in the existing manifest position, and confirm all changed files remain within the approved Windows reliability/performance/lint scope (including the user-requested `t.Parallel`/modernize/archtest/backend-plugin optimization follow-ups) with no unrelated changes.
  - _Validation:_ `git diff --check`, `git status --short`, `git diff --stat`, and the manifest checker; do not stage or commit.
  - _Requirements: 1.4, 3.1-3.6, 4.2, 5.1-5.5, 6.1-6.3, 7.2-7.4, 8.1-8.6, 9.1-9.4_

## Completion Gate

Requirements, design, and tasks are approved. Implementation tasks 1.1-1.6, 2.1-2.4, 3.1-3.4, 4.1, and 4.2-4.3 are complete with their contract tests passing; the focused final validation pass for 4.2 and 4.3 ran successfully. Completion requires every mandatory contract test, bounded child/process-tree evidence, truthful Windows race skip, SQLite observer evidence, and authoritative Linux workflow evidence. The spec is not archived yet; a final human review pass remains. This session does not stage or commit anything.
