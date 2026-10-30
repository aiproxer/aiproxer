# Design: Windows Task Reliability

## Overview

This design chooses a small reusable Go runner in `tools/taskrunner` and an exact CLI in `tools/taskrunner/cmd/lip-taskrunner`. It bounds each spawned child/process tree and the explicit coordinators that own a sequence. It does not claim that GNU Make's prerequisite graph has one aggregate deadline: Make may schedule or invoke prerequisites independently, so each target recipe remains a phase-bounded contract. The explicit full profile coordinator is the only aggregate-budget owner.

The design also defines Windows Job Object cleanup, POSIX process groups, exact Make target classifications, the direct subprocess audit, deterministic ACP reset semantics, SQLite retry defaults, and authoritative Linux evidence. It changes no production or test file; these are implementation contracts for the next approved phase.

## Goals and Non-Goals

### Goals

- Bound every supported child process tree and report actionable target/phase/module context.
- Preserve current Make selectors, module isolation, fuzz targets, QA gates, and Linux workflow status.
- Avoid parent environment/cwd mutation and avoid unbounded captured output.
- Replace ACP real-PATH stress as the default concurrency proof with a deterministic seam while retaining a required real `go` smoke.
- Move SQLite lock retry to the whole production transaction, not a blanket test retry.

### Non-goals

- No production process supervisor, public SDK contract, application runtime change, or cgo dependency.
- No Windows race-detector support and no claim that a Windows skip is race evidence.
- No one-deadline promise for ordinary GNU Make prerequisite graphs.
- No retries for PostgreSQL, arbitrary database errors, or whole Make targets.

## Repository Fit and Ownership

The repository already uses `tools/backendplugin/**` for Go tooling and has a root `go.mod` with `golang.org/x/sys`. The selected paths are therefore:

- `tools/taskrunner/`: reusable package, consumer-owned tooling API; no `pkg/lipapi`, `pkg/lipsdk`, or `internal/core` imports.
- `tools/taskrunner/cmd/lip-taskrunner/`: exact command-line adapter and optional explicit profile coordinator entry point.
- `tools/backendplugin/**`: replace bounded tooling call sites with the package; keep existing command-specific orchestration and report formats.
- `scripts/*.ps1` and `scripts/*.sh`: route recipes and pass explicit labels/cwd/env; do not duplicate process-tree code.
- `connector-support/acp`: instance-owned executable-cache resolver seam.
- `internal/infra/metering/journalstore`: SQLite-only transaction retry policy.

No new external dependency is needed. Windows process APIs use `golang.org/x/sys/windows` (already in `go.mod`); POSIX uses `syscall` in platform files. The runner is not used for ACP connector runtime lifecycle, Cursor bridge lifecycle, or provider discovery: those have their own context/process contracts and are audited below.

## Runner API and CLI

### Go package

The package exposes the following stable shape (names may be unexported internally, but implementation must satisfy this contract):

```go
type OutputMode uint8
const (
    Stream OutputMode = iota
    Capture
)

type Request struct {
    Argv           []string
    Dir            string
    Env            []string
    ClearEnv       bool
    Timeout        time.Duration
    Context        context.Context
    Output         OutputMode
    StreamOut      io.Writer
    StreamErr      io.Writer
    StdoutLimit    int // capture only; default 64 KiB
    StderrLimit    int // capture only; default 64 KiB
    AggregateLimit int // capture only; default 256 KiB
    HeadLimit      int // capture only; default 32 KiB
    TailLimit      int // capture only; default 32 KiB
    Label          string
    Redactions     []string
}

type Kind string
const (
    Success Kind = "success"
    ChildFailure Kind = "child_failure"
    DeadlineExceeded Kind = "deadline_exceeded"
    StartFailure Kind = "start_failure"
    CleanupFailure Kind = "cleanup_failure"
    InvalidRequest Kind = "invalid_request"
)

type Result struct {
    Kind Kind
    ExitCode int
    Label string
    Dir string
    DurationClass string // fast, normal, near_deadline; not a wall-clock assertion
    Stdout, Stderr []byte
    StdoutTruncated, StderrTruncated bool
    Cleanup CleanupResult
    Err error
}

func Run(ctx context.Context, req Request) Result
```

`Request.Context` is optional only when the `Run` argument is non-nil; `Run` rejects a nil caller context, empty argv, non-positive timeout, negative limits, a missing/non-directory `Dir`, or invalid environment entries. The implementation derives a child context with `min(caller deadline, now+Timeout)`. It validates that the timeout is not later than a caller deadline only as an effective deadline calculation; it does not require callers to precompute budgets.

The CLI is `go run ./tools/taskrunner/cmd/lip-taskrunner`. Flags and exit codes are normative exactly as stated in requirements 1.5-1.6. The `profile` subcommand is the only coordinator mode and has this exact call:

```text
go run ./tools/taskrunner/cmd/lip-taskrunner profile --name windows-full-release --root .
```

It invokes this exact ordered phase list sequentially via `taskrunner.Run`, passing the profile context to each child: `make quality-checks`, `make test-unit`, `make parity-checks`, `make test-fuzz`, `make qa-tests`, `make lint`, `make vuln`, `make backend-plugin-module-checks`, `make backend-plugin-security-checks`, `make backend-plugin-cross-platform-qa`, and `make backend-plugin-release-gates`. It does not invoke `make test` or `make qa`, because those targets add prerequisite graphs that would duplicate earlier phases. The profile has one 120-minute context deadline and reports child timeout versus profile timeout separately. Ordinary Make targets have no such coordinator guarantee.

### Output policy

Streaming is the developer default: stdout/stderr are copied to their destinations through `io.Copy`, and no child output is retained. Capture is for machine-readable diagnostics only and uses a head/tail bounded buffer per stream plus an aggregate cap; limits are validated and numeric. The runner never sends captured output to an unbounded log sink. Redaction is applied only to captured excerpts and structured diagnostics; streaming is intentionally not intercepted or redacted because doing so would change developer-visible output and requires a separate secret-safe terminal policy.

## Process Lifecycle

### Shared lifecycle order

1. Validate request, copy env, create child context, and create pipes or stream writers.
2. Construct `exec.Cmd` with `Dir`, `Env`, and platform attributes; start the process.
3. On Windows, create and configure the Job Object before start and assign the process immediately after start. On POSIX, start in a new process group.
4. Drain/copy both output streams concurrently. Wait for both drains and call `Wait` exactly once.
5. On normal completion, close owned handles/pipes, then return child status. On deadline/cancellation, signal the owned tree, drain/close, wait/reap, close Job Object or finish process-group cleanup, then return timeout with cleanup status.

### Windows Job Object

The Windows adapter uses `x/sys/windows`: `CreateJobObject`, `SetInformationJobObject(JobObjectExtendedLimitInformation, JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE)`, `AssignProcessToJobObject`, `TerminateJobObject` only as a fallback if the context expires before close, `WaitForSingleObject` through `cmd.Wait`, and `CloseHandle`. The Job Object handle is owned by the runner and is never inherited by unrelated commands.

Assignment behavior is explicit: if assignment succeeds, the runner owns the tree; timeout closes/terminates that Job once and waits. If assignment fails while the process is unsignaled, the runner kills the direct process, waits, closes all handles, and returns the original start/timeout/failure plus cleanup failure. If `GetExitCodeProcess`/wait shows the process exited before assignment, the runner reaps it and records `assignment_not_required`; it does not kill a possible reused PID. Cleanup is idempotent and protected by a once state. A shell child is just another process in the Job, so `cmd.exe /C ...` descendants are covered. Tests use a helper child that creates a grandchild and verify direct and shell cases.

### POSIX process group

The POSIX adapter sets `SysProcAttr.Setpgid = true` before `Start`; timeout/cancellation calls `syscall.Kill(-pid, SIGKILL)`, then `Wait` reaps the direct child and pipes are drained/closed. The negative-PID group kill result is separate from child result. Tests use the existing Unix process-group convention and a direct/shell descendant fixture.

## Target Routes, Budgets, and Classification

### Normative Windows phase budgets

These are target-local recipe/coordinator limits with headroom over the observed Windows cold/warm baseline: the ACP default lookup stress alone was about 53 seconds, while CI jobs are 45 minutes for cross-platform QA, 120 minutes for full backend release, and 45 minutes for the root platform matrix. They are orchestration targets, not per-test wall-clock assertions.

| Target/profile | Windows budget | Enforcement and notes |
|---|---:|---|
| `quality-checks` | 10m | script child phases; cold module tidy/build headroom |
| `test-unit` | 15m | preserves `-timeout=10m`; outer child budget is not a test assertion |
| `test` prerequisites individually | 20m each | no Make-graph aggregate promise |
| `qa` prerequisites individually | 25m each | quality/tagged/lint/vuln/static phases individually bounded |
| `lint` | 10m | preferred/fallback tool discovery and run |
| `test-fuzz` short smoke | 10m | preserves `FUZZTIME=500ms` and every current fuzz target |
| each parity target | 15m | includes all current module selectors |
| `backend-plugin-module-checks` | 25m | per-phase cap 8m; discovery/build headroom |
| backend-plugin security checks | 20m | includes two fuzz smoke targets |
| backend-plugin cross-platform QA | 20m | includes package smoke and lifecycle tests |
| `backend-plugin-release-gates-static` | 15m | static report/traceability only |
| explicit `backend-plugin-release-gates` full mode | 120m | coordinator/profile only; Linux workflow remains authoritative |
| `package-plugin-smoke` | 15m | external/package prerequisites block clearly |
| `isolated-root-qa` / `installed-plugin-smoke` | 20m each | platform prerequisites block, not skip |
| `node-independence` | 90m | Linux-authoritative no-Node lane; replays the full host verification surface under namespace isolation |

### Complete `.PHONY` target table

The rows are the complete names from `Makefile` line 1. `Windows-supported bounded` means the target has a Windows route and bounded supported child work. `explicit unsupported SKIP` is only for the Go race capability. `opt-in/BLOCKED prerequisite` preserves external or credential-dependent semantics. `Linux-authoritative` means a Windows run may be a local check or blocker but cannot satisfy the named Linux evidence.

| Target | Classification | Semantics |
|---|---|---|
| `help` | Windows-supported bounded | output only |
| `test` | Windows-supported bounded | quality, unit, parity prerequisites; no graph aggregate claim |
| `test-cost` | Windows-supported bounded | explicit Windows-authoritative anchor/head resource certification |
| `test-fast` | Windows-supported bounded | complete root-graph cached test route |
| `precommit-full` | Windows-supported bounded | full precommit verification pipeline |
| `test-unit` | Windows-supported bounded | root Go tests with outer phase budget |
| `billing-convergence-certify` | Windows-supported bounded | fail-fast billing convergence suites with explicit PostgreSQL and race availability reporting |
| `test-precommit-extra` | Windows-supported bounded | precommit hygiene/executor matrix |
| `test-postgres-migrations` | opt-in/BLOCKED prerequisite | DSN required; external block/fail |
| `test-authority-postgres` | opt-in/BLOCKED prerequisite | direct + pooled aggregate; explicit DSNs/attestation required |
| `test-authority-postgres-direct` | opt-in/BLOCKED prerequisite | direct PostgreSQL proof only |
| `test-authority-postgres-pooled` | opt-in/BLOCKED prerequisite | pooler attestation required |
| `qa-tests` | Windows-supported bounded | tagged Go tests |
| `test-race` | explicit unsupported SKIP | Windows `SKIP`; Linux command is authoritative |
| `test-fuzz` | Windows-supported bounded | short smoke; full fuzz is Linux-authoritative/scheduled |
| `test-reasoning-e2e-soak` | opt-in/BLOCKED prerequisite | explicit soak/env and long profile |
| `parity-checks` | Windows-supported bounded | conformance selectors preserved; external backend prerequisites block |
| `parity-acp-plugin` | Windows-supported bounded | nested ACP modules, `GOWORK=off` |
| `parity-cursorcliacp-plugin` | Windows-supported bounded | nested Cursor CLI ACP modules |
| `parity-cli-acp-plugins` | Windows-supported bounded | Gemini/AGY/Cursor CLI modules |
| `parity-openrouter-plugin` | Windows-supported bounded | OpenRouter support/module checks; external config may block |
| `parity-hosted-compatible-plugins` | Windows-supported bounded | NVIDIA/Hugging Face modules |
| `parity-ollama-plugins` | Windows-supported bounded | Ollama module; live service is not assumed |
| `parity-opencode-plugins` | Windows-supported bounded | OpenCode module and arch tests |
| `parity-codex-plugins` | Windows-supported bounded | Codex module and arch/runtime tests |
| `parity-local-compatible-plugins` | Windows-supported bounded | local-compatible module checks |
| `test-local-compatible-plugin-modules` | Windows-supported bounded | three nested modules |
| `release-gates` | Linux-authoritative | full conformance/fuzz release evidence; Windows may run bounded supported pieces |
| `bench` | Windows-supported bounded | benchmark smoke; not release evidence |
| `pgo-profile` | opt-in/BLOCKED prerequisite | developer workload required; not a default gate |
| `profile-only-check` | opt-in/BLOCKED prerequisite | developer workload required; not a default gate |
| `pgo-build` | Windows-supported bounded | local build |
| `quality-checks` | Windows-supported bounded | PowerShell/runner quality phases |
| `quality-checks-fast` | Windows-supported bounded | inline Windows route; omits duplicate Go compile/vet/archtest before the following test target |
| `regex-hotpath-check` | Windows-supported bounded | native script |
| `arch-report` | Windows-supported bounded | `go run` child is parent/runner bounded |
| `qa` | Windows-supported bounded | required quality/tagged/lint/vuln/static gates; missing tools fail |
| `vet` | Windows-supported bounded | root vet |
| `lint` | Windows-supported bounded | `golangci-lint`, else `staticcheck`, else fail |
| `vuln` | opt-in/BLOCKED prerequisite | `govulncheck` required; missing tool fails |
| `run` | opt-in/BLOCKED prerequisite | interactive server; explicit lifecycle, not bounded verification |
| `hooks-install` | Windows-supported bounded | native hook install |
| `check-change-size` | Windows-supported bounded | native staged file-count gate |
| `backend-plugin-module-checks` | Windows-supported bounded | complete dynamic module contract |
| `backend-plugin-absence-checks` | Windows-supported bounded | native script |
| `backend-plugin-security-checks` | Linux-authoritative | Windows bounded local check; Linux scheduled security/fuzz evidence authoritative |
| `backend-plugin-cross-platform-qa` | Windows-supported bounded | native matrix and package checks; CI matrix authoritative for release claim |
| `backend-plugin-release-gates-static` | Windows-supported bounded | static report/wiring |
| `backend-plugin-release-gates` | Linux-authoritative | full release matrix; explicit profile may run it, Linux workflow is authoritative |
| `package-minimal` | opt-in/BLOCKED prerequisite | package tools/prerequisites required |
| `package-full` | opt-in/BLOCKED prerequisite | package tools/prerequisites required |
| `package-plugin-smoke` | opt-in/BLOCKED prerequisite | depends on both package profiles |
| `docs-check` | Windows-supported bounded | documentation tests |
| `knowledge-check` | Windows-supported bounded | steering/ADR tests |
| `example-config-check` | Windows-supported bounded | docs plus bootstrap inspect |
| `backend-plugin-example-check` | Windows-supported bounded | docs/example script |
| `kiro-spec-check` | Windows-supported bounded | explicit `SPEC` required |
| `node-independence` | Linux-authoritative | Node-absent host lane; requires an unprivileged user+mount namespace, so the Windows route reports the Linux requirement instead of degrading to a PATH-only proof |
| `isolated-root-qa` | opt-in/BLOCKED prerequisite | copy/build prerequisites required |
| `installed-plugin-smoke` | opt-in/BLOCKED prerequisite | installed artifact/binary prerequisites required |
| `test-cursor-sdk-live` | opt-in/BLOCKED prerequisite | Node, `CURSOR_SDK_LIVE=1`, and key required |
| `test-cursor-sdk-live-bridge` | opt-in/BLOCKED prerequisite | tagged live bridge and key required |
| `test-cursor-sdk-platform` | Windows-supported bounded | fake bridge/current-OS smoke |
| `test-cursor-sdk-comparison-report` | Windows-supported bounded | synthetic/blocked-offline report; no credentials |
| `tmp-clean` | Windows-supported bounded | native residue scan; dry-run by default, explicit `-Apply` delete; never invoked automatically |
| `test-openresponses-compliance` | Windows-supported bounded | full-path compliance suite; direct PS1 route, go tests carry their own `-timeout` |
| `test-openresponses-compliance-static` | Windows-supported bounded | qa static wiring/evidence gate; direct PS1 `-Static` route |

### Make invocation policy

Windows recipes call the existing scripts with `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/quality-checks.ps1` or `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/backend-plugin-module-checks.ps1`; those scripts call `go run ./tools/taskrunner/cmd/lip-taskrunner` with explicit `--cwd`, `--env`, `--timeout`, and `--label`. POSIX scripts use the same runner CLI where child-tree ownership is required and may retain shell syntax for shell-native helpers. Every command path in this design is specified.

## Direct Subprocess Inventory

Disposition meanings: `runner/context-bounded` must migrate to `taskrunner.Run`; `parent-bounded` already has a request context/owned lifecycle and must preserve a parent timeout plus output bound; `excluded with rationale` is a fixture, a required process-owning connector lifecycle, or a lookup API rather than a tooling child.

| Call site | Current operation | Disposition |
|---|---|---|
| `tools/backendplugin/release_gates/main.go:404` | module `go list/vet/test` | runner/context-bounded; label module+step, `GOWORK=off`, per-phase 8m |
| `tools/backendplugin/release_gates/main.go:466` | module command build | runner/context-bounded; label module+command, remove temp output after reap |
| `tools/backendplugin/release_gates/catalog.go:216` | root `go_test` gate | runner/context-bounded; capture bounded report output |
| `tools/backendplugin/release_gates/catalog.go:238` | nested Make gate | runner/context-bounded; no recursive aggregate claim |
| `tools/backendplugin/release_gates/conformance.go:28,89,113` | list/filter test discovery | runner/context-bounded; preserve selectors and `-timeout=15m` |
| `tools/backendplugin/release_gates/tidy_check.go:121` | `go mod tidy -diff` | runner/context-bounded; `GOWORK=off`, bounded capture |
| `tools/backendplugin/package_plugins/main.go:308` | plugin command build | runner/context-bounded; bounded capture and cleanup |
| `tools/backendplugin/crossplatform_qa/main.go:378,493,546` | build, Go checks, native/package steps | runner/context-bounded; phase labels and child cwd/env |
| `tools/backendplugin/isolated_root_qa/main.go:78,271` | isolated Go checks/gofmt | runner/context-bounded; temp root is child cwd |
| `tools/backendplugin/installed_plugin_smoke/main.go:72,112,156,304` | build/package/doctor/invoke | runner/context-bounded; `serve` below is parent-bounded |
| `tools/backendplugin/installed_plugin_smoke/main.go:400` | long-lived server | parent-bounded; retain the existing `CommandContext` plus explicit readiness/cleanup and add a bounded server-profile context; do not use capture mode because readiness is streaming |
| `scripts/arch-report.go:470,480` | Go list commands | parent-bounded; existing `CommandContext` must retain context and use bounded output; outside selected Make child runner because it is a standalone report utility |
| `tools/backendplugin/tools_test.go:480,524,580,995,1007` | test helper `go run` children | excluded with rationale: test-only fixtures; each must use `CommandContext` with a test deadline and bounded output, but not production runner API unless tests need process-tree proof |
| `connector-support/acp/lookpath.go:25,28` | `exec.LookPath` | excluded with rationale: lookup API, not a child process; injectable resolver is the test seam |
| `connector-support/acp/transport_stdio_os.go:26` | ACP child start | excluded with rationale: connector-owned interactive lifecycle; existing `ProcessStarter`/transport contract owns pipes and cancellation |
| `connector-support/acp/transport_stdio_os_windows.go:21` | `taskkill` tree fallback | excluded with rationale: existing connector lifecycle, not Make tooling; must remain separately tested and is not the selected runner mechanism |
| `connector-support/acp/transport_stdio_os_unix.go:19` | POSIX process group kill | excluded with rationale: existing connector lifecycle; separate ACP process-tree evidence |
| `connector-support/acp/process_tree_cleanup_windows_test.go:45,92` and `process_tree_cleanup_unix_test.go:50` | direct/shell descendant fixtures and process observation | excluded with rationale: test-only process-tree evidence; intentionally exercises platform cleanup rather than production tooling orchestration |
| `connectors/cursorcliacp/internal/product/models.go:100` | model discovery command | parent-bounded; existing `CommandContext`, must add bounded output and preserve actionable missing CLI errors |
| `connectors/agycliacp/internal/product/models.go:166` | model discovery command | parent-bounded; existing `CommandContext`, bounded output required |
| `connectors/codex/internal/catalog/discovery.go:24` | model discovery command | parent-bounded; existing derived context, bounded output required |
| `connectors/codex/internal/catalog/discovery.go:58,95` | executable lookup | excluded with rationale: lookup API |
| `connectors/cursorsdk/internal/product/bridge_proc.go:38` | interactive bridge start | excluded with rationale: owned bridge lifecycle; existing platform cleanup tests |
| `connectors/cursorsdk/internal/product/bridge_proc_os_windows.go:21` and `bridge_proc_os_unix.go:19` | bridge tree cleanup | excluded with rationale: owned interactive bridge lifecycle; platform-specific cleanup is tested separately and must not be confused with Make tooling runner cleanup |
| `connectors/cursorsdk/internal/product/platform_native.go:81,123` | Node capability probes | parent-bounded; existing contexts, bounded output |
| `connectors/cursorsdk/internal/product/fakebridge/*` | test helper build/process | excluded with rationale: test fixture; retain context and bounded output |
| `connectors/codex/cmd/fake-codex-cli/main.go:46,48` | descendant fixture | excluded with rationale: intentional direct/shell process-tree test fixture |
| `connectors/cursorsdk/internal/product/live_bridge_live_test.go:35` | opt-in npm build | excluded with rationale: credential/Node-gated test fixture; must retain test deadline and fail actionable, never silently skip a required opted-in run |
| `scripts/quality-checks.ps1/.sh` | gofmt/tidy/build/vet/guards/archtest | runner/context-bounded; every child labeled `quality-checks:<phase>` |
| `scripts/backend-plugin-module-checks.ps1/.sh` | root/discovery/module/synthetic/copy phases | runner/context-bounded; no parent `GOWORK` mutation, every child labeled module+phase |
| Make fuzz recipes lines 141-192 | all current fuzz commands | runner/context-bounded; preserve each fuzz name/path and `FUZZTIME`, nested modules use child cwd/env |
| Make parity recipes lines 228-270 | all current parity commands | runner/context-bounded; preserve selectors and `GOWORK=off` |

The audit is intentionally not a claim that every `os/exec` in the product must use this tooling runner. Only the listed tooling call sites are in scope; process-owning connector and test fixtures retain their own contracts.

## ACP Resolver Design

`ExecutableCache` gets `NewExecutableCache(resolver Resolver)` with a mutex-protected generation map. `Resolver` defaults to `exec.LookPath`; `LookPath` obtains the current generation, uses `sync.OnceValue` within that generation, and returns the resolver result. `Reset` swaps to a fresh map while holding the mutex. A resolver already running continues; post-reset lookups cannot observe the old map. This is the intended behavior, not a linearizability guarantee for an already-started lookup.

The deterministic test injects a resolver that signals entry and waits on channels, schedules: old lookup enters -> reset -> new lookup resolves -> old lookup releases, then repeats a small fixed schedule. It asserts both results are valid, the new lookup does not reuse an old entry, and two caches do not share entries. The real smoke uses `NewExecutableCache(nil)` and looks up `go`, then `lip-acp-reliability-known-missing-7f4d`; absent `go` fails with `install Go and ensure the go executable is on PATH`, not skip.

## SQLite Retry Design

`DurableStore.Append` wraps its current transaction body in `appendSQLiteWithRetry`. Before each retry the transaction is rolled back (deferred rollback remains harmless after commit), and conflict resolution that needs a fresh read remains inside the same whole-operation attempt semantics. The retry is enabled only for the SQLite Bun dialect and modernc base result codes 5/6. Use `modernc.org/sqlite.Error.Code()` through `errors.As`; `Code() & 0xff` handles extended codes. The fallback text is only `database is locked` on SQLite.

`DurableConfig` adds optional `SQLiteRetryNow`, `SQLiteRetrySleep`, and `SQLiteRetryObserver` seams. Defaults are `time.Now`, a timer selecting on context, and no-op observer. The observer is deterministic test evidence, not production policy. Twelve total attempts use a 2s budget with 5-10-20-40-80-160ms exponential backoff capped at 250ms. Whole transaction means all current validation/lookups/inserts/filter/supersession/commit work restarts; no blanket retry wraps tests or non-SQLite stores. Terminal errors are wrapped with `sqlite_retry_canceled` or `sqlite_busy_retry_exhausted`.

## Linux Evidence and Status

| Evidence | Authoritative existing workflow/job | Command/status |
|---|---|---|
| PR QA | `.github/workflows/qa.yml` / `qa` | architecture, vet, release-command tests; blocking status preserved |
| Root race/release | `.github/workflows/release.yml` / `verify` | `go test -race ./...`; blocking release verification |
| Nightly race/security/fuzz | `.github/workflows/race-fuzz-nightly.yml` / `race-fuzz` | focused `go test -race`, `make backend-plugin-security-checks`, `make test-fuzz`; scheduled status preserved |
| Full backend release | `.github/workflows/backend-plugin-release-gates.yml` / `release-gates` | `make backend-plugin-release-gates`; 120-minute job, scheduled/main status preserved |
| Native platform matrix | `.github/workflows/backend-plugin-cross-platform.yml` / `cross-platform-qa` | `make backend-plugin-cross-platform-qa`; PR matrix status preserved |
| Vulnerability evidence | `.github/workflows/security.yml` / `govulncheck` | `govulncheck ./...`; blocking/scheduled status preserved |

Static preservation tests must assert these workflow/job/command strings remain present and that Linux paths do not select the Windows `SKIP:` branch. Windows skips and `external_blocker` results are labels only; they never satisfy Linux race, security, conformance, fuzz, or release proof.

## Traceability

| Requirement | Design sections | Contract tests |
|---|---|---|
| 1 | Runner API/CLI, output policy | `tools/taskrunner/runner_contract_test.go` |
| 2 | Process lifecycle | `tools/taskrunner/process_tree_windows_test.go`, `process_tree_posix_test.go` |
| 3 | Target routes/budgets | `internal/qa/windows_task_reliability_contract_test.go` |
| 4 | Module/release inventory | `tools/backendplugin/bounded_orchestration_contract_test.go` |
| 5-6 | Target table, inventory, evidence | `internal/qa/windows_task_reliability_contract_test.go` |
| 7 | ACP resolver design | `connector-support/acp/lookpath_test.go` |
| 8 | SQLite retry design | `internal/infra/metering/journalstore/sqlite_retry_contract_test.go` |
| 9 | Diagnostics/state | all contract tests plus `git diff --check` |

## Exact Acceptance Commands

These commands are concrete:

```text
go test ./tools/taskrunner -run 'TestRunner_(Success|ChildFailure|DeadlineExceeded|WorkingDirectory|ChildEnvironment|InvalidRequest|StreamDoesNotCapture|CaptureHeadTailAndAggregateBounds|RedactsCapturedSecrets)' -count=1 -timeout=2m
go test ./tools/taskrunner -run 'TestProcessTree_(WindowsJobObjectDirect|WindowsJobObjectShell|POSIXProcessGroup)' -count=1 -timeout=2m
go test ./tools/backendplugin -run 'TestBoundedOrchestration_(ModulePhaseLabels|StopsDependentPhase|CleansDescendants|StaticAndFullProfiles|NoUnboundedProductionExec)' -count=1 -timeout=8m
go test ./tools/taskrunner/cmd/lip-taskrunner -run 'TestProfile_WindowsFullReleaseDeadlinePropagation' -count=1 -timeout=2m
cd connector-support/acp && GOWORK=off go test . -run 'TestExecutableCache_(ResetGeneration|DeterministicConcurrency|InstanceOwnership|RealLookupSmoke)' -count=1 -timeout=2m
go test ./internal/infra/metering/journalstore -run 'TestSQLiteRetry_(BusyEventuallySucceeds|WholeTransactionRestarts|CancellationStopsBeforeNextAttempt|ObserverUsesDeterministicClock|NonBusyDoesNotRetry|PostgresDoesNotRetry|LockReleased)' -count=1 -timeout=4m
go test ./internal/qa -run 'TestWindowsTaskReliability_(TargetTableComplete|WindowsRoutes|FuzzAndParitySelectors|RaceSkip|LinuxEvidence|PostgresAndExternalBlockers|CallSiteAudit)' -count=1 -timeout=4m
```

The ACP contract commands are executed inside the nested `connector-support/acp` module with `GOWORK=off`; they are not run as root-module packages.

Windows Make acceptance uses these exact existing targets (each has its own phase budget): `make quality-checks`, `make test-unit`, `make test`, `make lint`, `make test-race`, `make test-fuzz`, `make parity-checks`, `make parity-acp-plugin`, `make parity-cursorcliacp-plugin`, `make parity-cli-acp-plugins`, `make parity-openrouter-plugin`, `make parity-hosted-compatible-plugins`, `make parity-ollama-plugins`, `make parity-opencode-plugins`, `make parity-codex-plugins`, `make parity-local-compatible-plugins`, `make test-local-compatible-plugin-modules`, `make backend-plugin-module-checks`, `make backend-plugin-security-checks`, `make backend-plugin-cross-platform-qa`, `make backend-plugin-release-gates-static`, and `make qa`. Full aggregate evidence uses only `go run ./tools/taskrunner/cmd/lip-taskrunner profile --name windows-full-release --root .` and is not inferred from those separate Make runs.
