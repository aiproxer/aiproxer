# Windows Task Reliability Research

## Status and Baseline

- Repository: `go-llm-interactive-proxy`
- Baseline: branch `fix/windows-task-reliability` at `70d500d9`
- Date: 2026-07-31
- Scope: specification artifacts only; no implementation change is included
- Target: Windows 10 developer execution, with Linux CI/release/security/race evidence preserved
- Spec state: requirements, design, and tasks generated; approvals intentionally false; not ready for implementation

## Repository Guidance Reviewed

The review read `AGENTS.md`, `.kiro/AGENTS.md`, all six files in `.kiro/steering/`, `Makefile`, both quality scripts, both backend-plugin module-check scripts, `scripts/race-check.ps1`, the current ACP lookup/process lifecycle code and tests, the SQLite durable journal and contention tests, all selected backend-plugin subprocess tools, the relevant root and nested `go.mod` files, and the CI workflows for QA, release, security, race/fuzz, and backend-plugin gates.

The repository supports the selected tooling layout: reusable tooling packages and commands live under `tools/`, `tools/backendplugin/**` already owns release/module orchestration, and `golang.org/x/sys` is already a direct dependency. No new dependency or cgo is needed for Windows Job Objects.

## Findings

### 1. Nested module checks and backend-plugin tools lack one child ownership contract

`backend-plugin-module-checks.ps1/.sh` runs root checks, dynamically discovers modules, runs module list/test/build/import checks, creates a synthetic module, and builds a connector-free copy. The current child operations have no common per-phase deadline or process-tree cleanup. The release, packaging, cross-platform, isolated-root, and installed-smoke Go tools similarly contain direct `os/exec` calls, many with `CombinedOutput` and no parent context.

The selected correction is a narrow package at `tools/taskrunner` and exact CLI at `tools/taskrunner/cmd/lip-taskrunner`. The runner owns one child request, copied env, child cwd, bounded capture or direct streaming, process-tree cleanup, and structured result. The inventory in `design.md` identifies every selected direct call and explicit exclusions.

### 2. GNU Make prerequisites cannot honestly receive one aggregate deadline

The Makefile expresses `test`, `qa`, `package-plugin-smoke`, and other sequences through prerequisite graphs. A recipe runner cannot safely infer or enforce one deadline over all GNU Make scheduling and child activity without becoming a top-level Make coordinator. Therefore normal targets promise independent phase/child budgets only. The explicit `windows-full-release` profile coordinator is the sole aggregate-budget owner and has an exact 120-minute context.

### 3. Windows and POSIX process cleanup need separate concrete designs

Existing ACP Windows cleanup uses `taskkill`, while the tooling runner must not depend on that shell utility. Windows uses `x/sys/windows` Job Objects with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, immediate process assignment, idempotent close, and wait/reap ordering. POSIX uses a new process group and negative-PID signal. Direct and shell descendant fixtures are required for both platform families. Assignment-before-exit races are handled explicitly; an already-signaled process is reaped without killing a reused PID.

### 4. Current Make recipes mix command languages

Parity and fuzz recipes contain POSIX `cd ... && GOWORK=off ...` forms. `lint` uses `command -v`; Windows scripts use parent-scoped environment/location mutation. The design preserves all current package selectors, fuzz names, flags, and `GOWORK=off`, but routes Windows child cwd/env through the exact CLI. It does not change Linux shell semantics except where the same runner is needed for descendant ownership.

### 5. Target semantics include real external prerequisites

PostgreSQL migration/direct/pooled targets, vulnerability/release tools, packaging, live Cursor SDK, Node, and service-backed operations do not become magically runnable on Windows. The complete `.PHONY` table classifies them as `opt-in/BLOCKED prerequisite` or `Linux-authoritative` where appropriate. Missing DSNs, pooler attestation, credentials, or tools remain actionable failures/blocks, not unsupported passes. Only Windows Go race capability is an explicit successful skip.

### 6. Linux evidence is owned by existing workflows

The existing authoritative owners are `.github/workflows/release.yml` `verify` for `go test -race ./...`, `.github/workflows/qa.yml` `qa` for PR QA, `.github/workflows/race-fuzz-nightly.yml` `race-fuzz` for focused race/security/fuzz, `.github/workflows/backend-plugin-release-gates.yml` `release-gates` for the 120-minute full backend gate, `.github/workflows/backend-plugin-cross-platform.yml` `cross-platform-qa` for the native matrix, and `.github/workflows/security.yml` `govulncheck` for vulnerability evidence. Windows `SKIP` and `external_blocker` outcomes cannot satisfy those jobs or alter their current blocking/scheduled status.

### 7. ACP test stress is not a concurrency contract

`connector-support/acp/lookpath_test.go` currently uses 64 goroutines and 400 iterations of real `exec.LookPath`, with an observed Windows run of about 53 seconds. `ExecutableCache` remains instance-owned, but the spec adds `NewExecutableCache(Resolver)` and a mutex-protected generation reset. In-flight old-generation lookups may finish; post-reset lookups create fresh entries. A required smoke resolves the real `go` executable and a deterministic missing name, and missing `go` fails with PATH/install guidance.

### 8. SQLite retry belongs in production at the transaction boundary

`DurableStore.Append` currently has one transaction body and `append_race_test.go` wraps calls in a test-only retry helper. The production policy is now SQLite dialect only, modernc base code 5/6 only, twelve total attempts, 2s retry budget, and exact 5/10/20/40/80/160ms backoffs capped at 250ms. Every attempt restarts validation/lookups/inserts/filter/supersession/commit and rolls back before retry. Injected clock/sleeper/observer seams provide deterministic evidence; no blanket test retry remains.

## Decisions and Rejected Suggestions

1. **Selected:** `tools/taskrunner` plus `tools/taskrunner/cmd/lip-taskrunner`; it matches the existing `tools/` convention and avoids a production/public API.
2. **Rejected:** a single aggregate deadline for ordinary Make targets; GNU Make prerequisite semantics do not provide a reliable owner. An explicit profile coordinator is used only where an owner exists.
3. **Rejected:** `taskkill` as the runner's Windows implementation; Job Objects provide process-tree ownership without another shell child and are feasible with existing `x/sys`.
4. **Rejected:** broad output truncation in streaming developer mode; streaming has bounded memory because it is not captured. Numeric head/tail/aggregate limits apply only to captured subprocess diagnostics.
5. **Rejected:** blanket database/test retries; only recognized modernc SQLite busy/locked results retry, and the whole transaction restarts.
6. **Rejected:** making missing external services or tools unsupported skips; those are explicit blocks/failures, preserving the repository's existing semantics.

## Evidence Required During Implementation

- Runner tests prove success/failure/timeout, exact exit mapping, env/cwd isolation, stream versus capture memory bounds, redaction, and invalid requests.
- Windows Job Object and POSIX process-group tests prove direct and shell descendant cleanup, assignment/wait/cleanup races, and separate cleanup outcomes.
- Static tests prove every `.PHONY` target row, all current fuzz/parity recipes, PowerShell quality/module routing, direct subprocess inventory, and Linux workflow/job/command preservation.
- Backend-plugin tests prove dynamic module coverage, phase labels, failure stop, static/full distinction, and no unowned selected `os/exec` waits.
- ACP tests prove generation reset semantics, deterministic interleaving, instance ownership, and required real `go` plus missing-name smoke.
- SQLite tests prove exact attempts/backoff/budget, whole-transaction restarts, observer events, cancellation, lock release, and non-busy/non-SQLite no-retry behavior.
- Final verification runs `git diff --check`, reviews changed paths, confirms `spec.json` remains unapproved/tasks-generated, and confirms `.release-files` remains sorted.
