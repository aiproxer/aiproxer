# Requirements: Windows Task Reliability

## Introduction

This feature makes developer-facing verification predictable on Windows 10 without weakening Linux evidence. It covers bounded child-process execution, Windows-safe Make routing, nested module and backend-plugin tooling, ACP lookup evidence, SQLite contention handling, and truthful platform classification. Supported work fails with actionable context when it cannot complete. Only a documented host capability limitation may be a successful skip.

The repository's current GNU Make prerequisite graph is not treated as one deadline. A direct recipe invocation receives a phase budget, and every process tree spawned by that invocation is bounded. An aggregate deadline is normative only for a separately invoked profile coordinator that owns the whole sequence.

## Boundary Context

- **In scope:** Make/PowerShell/POSIX routing for the targets listed below; `tools/taskrunner` and its `tools/taskrunner/cmd/lip-taskrunner` CLI; nested module and release orchestration; Windows descendant cleanup; deterministic ACP lookup tests; SQLite-only busy retries; race-skip classification; static preservation checks.
- **Out of scope:** application request behavior, public `pkg/lipapi` and `pkg/lipsdk` contracts, provider protocol semantics, a general production process supervisor, Windows race-detector support, and changing Linux workflow status.
- **Allowed future implementation files:** tooling under `tools/taskrunner*`, existing `tools/backendplugin/**`, the existing Windows/POSIX scripts, `connector-support/acp`, and `internal/infra/metering/journalstore`. No production application package or public contract is authorized.
- **Dependency policy:** use the standard library plus existing `golang.org/x/sys` (already required by `go.mod`) for Windows Job Objects. Do not add cgo or a new process-management dependency.

## Requirements

### Requirement 1: Exact bounded command contract

**Objective:** A tooling caller can execute one child command with explicit lifecycle ownership, without changing the parent process environment or working directory.

#### Acceptance Criteria

1. The reusable package shall be `tools/taskrunner`, and the executable command shall be `tools/taskrunner/cmd/lip-taskrunner`; existing `tools/backendplugin/**` commands and Windows scripts shall use this boundary. This follows the repository convention of reusable/tool commands below `tools/` rather than introducing a new top-level `cmd` hierarchy.
2. The package shall accept a request containing: non-empty `Argv []string` (executable at index 0), optional absolute or repository-resolved `Dir`, `Env []string` overrides, a required positive child `Timeout`, an optional caller context, `OutputMode` (`stream` or `capture`), numeric output limits, a diagnostic `Label`, and a redaction list of exact secret values.
3. Environment construction shall start from a copy of `os.Environ()` unless `ClearEnv` is explicitly requested, apply only the supplied `KEY=VALUE` overrides, and never call `os.Setenv`, `os.Unsetenv`, `os.Chdir`, or mutate the caller's slices. `Dir` shall be assigned to the child only.
4. The package result shall include `Kind` (`success`, `child_failure`, `deadline_exceeded`, `start_failure`, `cleanup_failure`, or `invalid_request`), the raw child exit status when available, `Label`, normalized child directory, duration category, bounded stdout/stderr excerpts when capture mode is used, and separate cleanup status/error. A cleanup error shall not replace the original child or timeout classification.
5. The CLI shall accept exactly these flags: repeatable `--env KEY=VALUE`, `--cwd DIR`, required `--timeout DURATION`, `--label TEXT`, `--output stream|capture` (default `stream`), `--stdout-limit` (default `65536`), `--stderr-limit` (default `65536`), `--aggregate-limit` (default `262144`), `--head-limit` (default `32768`), `--tail-limit` (default `32768`), and `--clear-env`. The command begins after `--`; for example:

   `go run ./tools/taskrunner/cmd/lip-taskrunner --label "module:phase" --cwd connector-support/acp --env GOWORK=off --timeout 8m --output stream -- go test -count=1 ./...`

6. The CLI shall return exit code `0` for success, `1` for a child failure (while retaining the child's raw status in its diagnostic), `2` for deadline exceeded, `3` for start/cleanup/infrastructure failure, and `4` for invalid request. It shall never return success for a supported command whose process or owned pipes were not reaped.
7. In `stream` mode stdout and stderr shall be copied directly to the caller's corresponding streams and not retained in memory; developer output shall not be truncated. In `capture` mode each stream shall retain at most 64 KiB, with at most 32 KiB from the head and 32 KiB from the tail, and the combined retained bytes shall not exceed 256 KiB. Truncation shall be marked. Captured text shall be redacted for exact configured secret values before diagnostics; labels, paths, and environment values shall not be logged by default.

### Requirement 2: Process-tree cancellation

**Objective:** A timeout cancels the child and descendants in a way that is feasible with the repository's existing Go toolchain and dependencies.

#### Acceptance Criteria

1. On Windows, the runner shall create a Job Object before spawning, set `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, assign the child process immediately after `Start`, and close the job only after the child and pipes have been drained and the process handle has been waited. Closing the job is the tree-cancellation operation; `taskkill` is not the runner implementation.
2. If assignment fails while the process is live, the runner shall terminate the direct process, wait for it, close the job, report `cleanup_failure` (or the original timeout/failure with cleanup failure attached), and never claim descendant ownership or success. If the process handle is already signaled before assignment, the runner may reap it and return its already-determined child result with `assignment_not_required` recorded; this is the only permitted assignment race exception.
3. On Windows, cleanup shall be idempotent: a normal exit before timeout does not kill a reused PID, a timeout cancels at most the owned Job Object once, `Wait` is called exactly once, and every pipe is closed even when setup or cleanup fails. Direct-child and shell-descendant fixtures shall prove that no descendant remains after timeout.
4. On POSIX, the runner shall set `Setpgid: true` before spawn, signal the negative process-group ID on timeout/cancellation, wait for the direct child, close pipes, and report the group-kill result separately. POSIX behavior shall not be inferred from Windows behavior.
5. A runner timeout shall be the earlier of the caller context deadline and the request child timeout. The runner shall not promise to cancel processes that were not spawned by it or that escaped an explicitly excluded interactive lifecycle.

### Requirement 3: Windows Make routing and profiles

**Objective:** Windows recipes use explicit child cwd/env handling while retaining current selectors and fail-fast semantics.

#### Acceptance Criteria

1. The Windows branch of every target in the `.PHONY` classification table in the design shall use either a native PowerShell script that invokes `tools/taskrunner/cmd/lip-taskrunner` or a direct runner invocation. It shall not use POSIX inline environment assignment, `command -v`, `/dev/null`, or `cd DIR && COMMAND` syntax.
2. The exact nested-module invocation shall be equivalent to:

   `go run ./tools/taskrunner/cmd/lip-taskrunner --label "parity-acp-plugin:connector-support/acp:test" --cwd connector-support/acp --env GOWORK=off --timeout 15m -- go test -parallel=8 -timeout=10m -run "KillProcessTree_|ProcessTree_CrossCompile|PID|Pool|Cancel|Open_|MapSession|Scripted" ./...`

   It shall return the child's failure through the runner and shall not alter the parent Make process.
3. PowerShell quality checks shall invoke the same CLI for `gofmt`, `go mod tidy`, `go build`, `go vet`, goroutine/regex checks, and architecture tests. The exact first invocation shape shall be:

   `go run ./tools/taskrunner/cmd/lip-taskrunner --label "quality-checks:gofmt" --cwd C:\path\to\repository --timeout 2m --output stream -- gofmt -l .`

    The script replaces `C:\path\to\repository` with the resolved repository root before invoking the command; the literal example path is not emitted or executed.
4. Existing Make selectors, fuzz names, module paths, `GOWORK=off`, `-parallel=8`, `-timeout=10m`, `-tags=precommit,integration`, and the preferred `golangci-lint`/fallback `staticcheck` order shall remain unchanged in meaning. Missing required Go, Make, PowerShell, linter, vulnerability, packaging, or release tools shall fail with install/PATH guidance.
5. Direct Make targets shall have the normative phase budgets in the design. Those budgets apply to each recipe child or to a target-local coordinator. They do not add up to a promised deadline for `test: quality-checks test-unit parity-checks` or any other GNU Make prerequisite graph.
6. The explicit `windows-full-release-profile` coordinator shall be implemented. Its exact invocation shall be `go run ./tools/taskrunner/cmd/lip-taskrunner profile --name windows-full-release --root .`; it shall own the sequential profile context, invoke the named Make phases through the runner, and enforce one maximum 120-minute profile deadline. A normal `make test`, `make qa`, or other existing target shall not claim this aggregate guarantee.

### Requirement 4: Module and backend-plugin orchestration

**Objective:** Dynamic module and release checks remain complete, diagnosable, and bounded.

#### Acceptance Criteria

1. `backend-plugin-module-checks` shall preserve root `go list ./...`, root `go build`, root module-graph isolation, root `GOWORK=off go test ./...`, structural discovery, every discovered module's list/test/command builds/import isolation, synthetic discovery, and root-without-connectors build.
2. Each discovered module phase shall have a label containing the module and phase, use a runner/context deadline, stop dependent phases on failure, and return non-zero. No discovered module may be omitted because it is slow or because a different phase is unsupported.
3. Backend-plugin security, cross-platform, package, installed-smoke, isolated-root, and release tools shall use the runner for every production child invocation that currently uses `os/exec`. Their Make-level phase budgets and the explicit full profile are separate from Go test's internal `-timeout`.
4. Static release mode shall remain the default QA path. Full release mode shall retain dynamic module/release coverage and be opt-in; missing external prerequisites shall be reported as `external_blocker`/failure according to the existing tool contract, not as a false pass.

### Requirement 5: Target semantics and evidence

**Objective:** Every `.PHONY` target has an honest platform classification and no unsupported result is mistaken for evidence.

#### Acceptance Criteria

1. The design shall contain one row for every `.PHONY` name currently present in `Makefile` line 1, with one of: `Windows-supported bounded`, `explicit unsupported SKIP`, `opt-in/BLOCKED prerequisite`, or `Linux-authoritative`.
2. PostgreSQL migration/direct/pooled targets shall remain explicit prerequisite operations. Without the required DSN or pooler attestation they shall fail/block clearly; they shall not claim that SQLite or a Windows skip proves PostgreSQL behavior.
3. Live Cursor SDK targets shall remain credential/Node-dependent opt-in operations. Offline comparison/platform checks may run with synthetic inputs, but an external dependency failure is not an unsupported-platform pass.
4. `test-race` on Windows shall print a line beginning `SKIP:` that states Go race evidence is unsupported on Windows and Linux CI remains mandatory, then exit `0`. On Linux and strict CI, the existing race command shall execute and fail on missing/failing evidence. The Windows skip shall never be emitted by Linux code paths.
5. Linux-authoritative evidence shall name its existing workflow/job and command: `.github/workflows/release.yml` `verify` runs `go test -race ./...`; `.github/workflows/qa.yml` `qa` owns the PR QA checks; `.github/workflows/race-fuzz-nightly.yml` `race-fuzz` owns focused race, backend security, and fuzz smoke; `.github/workflows/backend-plugin-release-gates.yml` `release-gates` runs the full backend-plugin gate with a 120-minute job limit; `.github/workflows/backend-plugin-cross-platform.yml` `cross-platform-qa` owns the native platform matrix. Windows `SKIP` or `external_blocker` results can never satisfy those Linux race, security, conformance, fuzz, or release proofs, and existing scheduled/blocking status shall not change.

### Requirement 6: Subprocess call-site audit

**Objective:** No direct child process used by the changed tooling surface is left architecturally ambiguous.

#### Acceptance Criteria

1. The design shall inventory every current direct `os/exec` call in `tools/backendplugin/**`, `scripts/arch-report.go`, ACP process lifecycle, connector model discovery, and test fixtures found by the review. Each row shall say `runner/context-bounded`, `parent-bounded`, or `excluded with rationale`.
2. The inventory shall include the Make fuzz recipes, all parity recipes, `scripts/quality-checks.ps1`, `scripts/quality-checks.sh`, and `scripts/backend-plugin-module-checks.ps1/.sh`, including their nested cwd/env behavior.
3. A future static test shall fail if a new production child call is added to the bounded tooling directories without either using `tools/taskrunner` or recording an explicit parent-owned lifecycle exemption.

### Requirement 7: ACP reset and lookup contract

**Objective:** Concurrency evidence tests the intended cache behavior rather than a real-PATH stress loop.

#### Acceptance Criteria

1. `connector-support/acp.ExecutableCache` shall expose `NewExecutableCache(resolver Resolver)` where `type Resolver func(string) (string, error)`; a zero-value cache shall use `exec.LookPath`. The resolver seam shall be injectable without changing connector behavior.
2. `Reset` shall start a new cache generation under a mutex. A lookup that acquired the old generation may finish using the pre-reset resolver/cache; a lookup acquiring the generation after `Reset` shall create/use a fresh entry. No linearizability claim is made about completion order, and reset shall not cancel an in-flight resolver call.
3. The deterministic test shall use a small fixed worker/operation schedule, a blocking fake resolver, and a test context/deadline to prove valid results, no panic, no deadlock, and instance ownership. It shall not use the current 64-goroutine by 400-iteration real `exec.LookPath` loop.
4. The real smoke test shall call the required `go` executable through the real resolver and a deterministic missing name such as `lip-acp-reliability-known-missing-7f4d`. Missing `go` shall fail with actionable PATH/install guidance; the smoke test shall not skip. Lookup completion shall be bounded by a test context/deadline without a fixed-duration success assertion.

### Requirement 8: SQLite busy retry

**Objective:** Only the SQLite durable journal retries transient lock contention, and it retries the whole append transaction.

#### Acceptance Criteria

1. The SQLite retry defaults shall be exactly 12 total attempts (initial attempt plus at most 11 retries), a 2s elapsed retry budget, and backoffs of 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, then 250ms (capped) before retries. The earlier of the caller context, 2s retry budget, and attempt limit wins.
2. Busy classification shall apply only when `s.db.Dialect().Name() == dialect.SQLite` and the wrapped error is a `*modernc.org/sqlite.Error` whose base code (`Code() & 0xff`) is `SQLITE_BUSY` (5) or `SQLITE_LOCKED` (6). A narrow compatibility fallback may recognize the modernc error text `database is locked` only when the dialect is SQLite; it shall not classify arbitrary PostgreSQL or constraint errors as busy.
3. Each attempt shall include `BeginTx`, supersession validation, source/identity lookup, fact insert, filter insert, supersession insert, conflict handling, and `Commit`. A busy result shall roll back the failed transaction, release its connection, check context/budget, wait with a context-aware sleeper, and start a fresh transaction. No transaction or statement handle crosses an attempt.
4. Cancellation, deadline, or exhausted busy budget shall return a classified wrapped error (`sqlite_retry_canceled` or `sqlite_busy_retry_exhausted`) and shall not start another attempt. Constraint, identity collision, malformed input, non-busy SQLite errors, PostgreSQL errors, and memory-store operations shall return without busy retry.
5. `DurableConfig` shall provide a deterministic retry seam: injected `SQLiteRetryNow`, `SQLiteRetrySleep`, and `SQLiteRetryObserver` with production defaults of `time.Now`, a context-aware timer, and no-op observation. The observer shall receive attempt number, classification, backoff, and terminal outcome; tests shall use it instead of sleeping or wrapping all appends in a retry helper.
6. Contention tests shall prove whole-transaction eventual success/idempotency, rollback/commit lock release with a subsequent write/close probe, cancellation with no extra attempt, and no retry for non-busy/identity/constraint/PostgreSQL cases.

### Requirement 9: Diagnostics and verification state

**Objective:** A bounded result remains actionable and the spec remains unapproved.

#### Acceptance Criteria

1. Failure, timeout, cleanup failure, block, and skip diagnostics shall include target, phase, module/package when applicable, platform, duration category, outcome, and next action without secrets or unbounded child output.
2. Focused tests shall use fake deadlines/clocks, channels, and observers for propagation. They shall not assert that a unit test completes within a brittle wall-clock duration.
3. The implementation tasks shall be TDD ordered with exact package/test paths and names for runner success/failure/timeout/output/env/cwd, profile budget if retained, process-tree cleanup, target/call-site audit, ACP reset/smoke, SQLite retry, and evidence semantics.
4. `spec.json` shall carry truthful lifecycle metadata that follows the current phase, approval, and completion state. No task authorizes staging or committing.
