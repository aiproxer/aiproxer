# Task 12.3 Full Repository Certification (Req 13.6)

Scope: branch `feat/core-feature-ownership-full-closure`, HEAD `35ad8e67`
(12.2 approved) plus the uncommitted 12.3 changes in this worktree
(lint-closure fixes, this file, tasks.md 12.3 checkbox).
Baseline for pre-existing comparison: merge-base `fff67a0b` (== `main`
worktree HEAD) and main scheduled CI runs 34105540179 / 34022492580.

## Gate table

| # | Gate (command) | Exit | Disposition |
| --- | --- | ---: | --- |
| 1 | `go test -count=1 ./...` (run 1, before lint fixes) | 1 | 2 failures, both triaged non-introduced (see §1). Post-fix re-run: exit 0, full pass |
| 2 | `go vet ./...` | 0 | clean |
| 3 | `go mod verify` | 0 | all modules verified |
| 4 | `govulncheck ./...` (govulncheck@v1.2.0, go1.26.6) | 0 | 0 vulnerabilities in code; 3 in required modules not called by this code |
| 5 | `make quality-checks` | 2 (make boundary; lint child exit 1) | red SOLELY on 4 pre-existing findings (see §2); all 7 other guardrail groups OK |
| 6 | `make arch-report` | 0 | clean (re-run after all edits) |
| 7 | `make docs-check` | 0 | clean |
| 8 | `go run ./scripts/generate-feature-planes.go -check` | 0 | generated file up to date |
| 9 | External SDK contracts: `go test -count=1 .` + `go run .` in `testdata/external_feature_sdk`; `go run .` in `testdata/external_connector`; `go test -run 'TestExternalFeatureSDKModulePublicOnlyCompileGate\|TestExternalConnectorModulePublicHostCompileGate' ./internal/archtest/`; `go test ./pkg/lipsdk/featurehost/... ./pkg/lipruntime/...` | 0 | all clean (`external_feature_sdk: ok`, `external_connector: ok`) |
| 10 | Fuzz seed corpus `go test -count=1 ./internal/core/config/` | 0 | clean (covers `FuzzReloadConfigSource`, `FuzzEffectiveCanonicalization` seed corpus) |
| 11 | `make test-db-parity-sqlite` | 0 | clean |
| 12 | `make test-db-parity-postgres-direct` | 1 | environmental, not a code failure (see §4) |
| 13 | Fixed-cost benchmarks from 11.5 (`-benchmem`) | 0 | alloc counts identical (see §3) |
| 14 | `git diff --check` | 0 | clean |

### §1 `go test ./...` failure list with disposition

Run 1 (exit 1) reported exactly two failing packages; run 2 after the
lint-closure edits (exit 0) has zero failures.

1. `internal/core/runtime` —
   `TestPhase6_Observability_ActualModesAndCauses_TableDriven/non_probe_stream_fallback_none`:
   `cancellation_observability_phase6_test.go:526: CancelALeg failed:
   leglifecycle: cancel and close b-legs: cancel b-leg: context deadline
   exceeded`.
   Disposition: timing flake under full-suite load, NOT introduced.
   Passes on isolated re-run in this worktree and passes in the clean
   `main` worktree (`fff67a0b`). Passes in run 2.
2. `tools/testcost` — `TestWindowsCostProbeHasProcessAccounting`:
   `measure_windows_test.go:27: probe accounting error = process not assigned
   to job object`.
   Disposition: environmental/load-sensitive, NOT introduced. Passes
   isolated in this worktree and passes in the clean `main` worktree.
   Passes in run 2.

Neither failure matches the main-run pre-existing list, and neither
reproduces outside full-suite load, so no production fix applies. No
introduced test failure remains.

3. `internal/infra/runtimebundle` —
`TestCompatibleMultiInstance_routingPolicyIndependence/parallel`
(observed once in an independent reviewer full-suite run, not in the
worker's runs): `parallel winner text="iso-slow-A" want iso-B`.
The `parallel` subtest races a 250 ms-delayed instance A against fast
instance B and asserts B wins — timing-sensitive by construction.
Disposition: UNCLASSIFIED pending investigation (execution-guardrails
§3 suspected-flake rule — see below). NOT cleared as pre-existing,
NOT attributed as introduced.
- The test file is byte-identical to `main` (`git diff fff67a0b HEAD`
  empty; `git log fff67a0b..HEAD` empty for it) — this spec never touched it.
- `internal/core/routing/` is untouched by this spec. Near-path spec
  changes: `internal/core/runtime/parallel_race.go` replaces the removed
  `memoUpdate *interleavedthinking.PendingMemoUpdate` field with the
  `turn InterleavedTurn` port and drops the `commitMemoInjection` call
  from the winner path (winner selection, timing, failover and race
  arbitration logic untouched); `build_executor.go`/`compile_generation.go`
  carry featurehost wiring; `openaicaps/compatible_replay.go` a 2-line
  import move.
- Reproduction attempts (all recorded, none reproduced the signature):
  `go test -count=15` passes 15/15 on this branch AND 15/15 on the clean
  `main` worktree (`fff67a0b`); 10/10 on this branch under artificial CPU
  load; 25/15-rep and `-cpu=2` contention runs on `main` all green;
  two subsequent full `go test -count=1 ./...` runs on this branch fully
  green; full `go test -count=1 ./...` on clean `main` green for
  `runtimebundle` (one unrelated `tools/taskrunner`
  `TestRunner_TimeoutPath` timing failure there, passing isolated —
  confirming full-suite load induces timing flakes on this host).
- Per execution-guardrails §3, the same-signature reproduction on the
  starting SHA was NOT achieved, so this failure MUST NOT be labeled
  pre-existing and MUST NOT be labeled introduced either: it remains
  explicitly UNCLASSIFIED. It does not block Task 12.3 because (a) the
  exercised test file and routing arbitration are outside this SDD's
  modified behavior, (b) all focused gates for the moved ownership paths
  are green including exact Linux race evidence, and (c) the failure
  signature (slow-leg win under full-suite load) is consistent with
  load-induced timing, but that consistency is hypothesis, not proof.
  The required follow-up (12.4/merge window): if this signature recurs,
  investigate before using any run containing it as certification evidence.

### §2 `make quality-checks` triage

Sub-gates all OK: [1/8] generated planes, [2/8] gofmt, [3/8] modules,
[4/8] build, [5/8] vet, [6-8/8] goroutine allowlist, regex hot-path,
archtest. The only red guardrail is `lint`, reporting exactly 4 findings,
all in `tools/kiro/speccheck/residual_ownership_inventory_test.go`
(`:401`, `:510` stringsseq; `:604`, `:688` slicescontains).
That file is byte-identical to `main` (`git diff fff67a0b HEAD` empty for
it); the same 4 findings reproduce with the same golangci-lint binary in
the clean `main` worktree. Cross-ref: speccheck-inventory is in the
main-run pre-existing failure list. Left untouched per smallest-diff;
no ratchet weakened.

Introduced lint debt found and fixed in this change: 105 findings across
38 branch files (91 + 10 + 4 over three `quality-checks` runs; per-run
visibility is capped by golangci-lint `max-same-issues`). Categories:
errcheck 10 (`defer x.Close()` → `defer func() { _ = x.Close() }()`,
the repo's established test pattern); forcetypeassert 9 (two-value form
with `t.Fatal` / `require.True`); gofumpt 5 (`gofumpt -w` on the exact
flagged files, incl. two production formatting-only spots);
paralleltest 46 (`t.Parallel()` for pure/local-state tests,
`//nolint:paralleltest // <reason>` for the 4 package-seam-swapping
tests, matching repo convention); modernize 21 (`reflect.TypeFor`,
`range over int`, `Type.Methods()`, `maps.Copy`, tagged switch,
`strings.Builder`, `slices.Contains`, `forvar` removals);
staticcheck 13 (`//nolint:staticcheck // <ID>: <reason>` for intentional
compile-time assertions and single-dir AST scans, matching repo
convention; one dead empty branch deleted); ineffassign 1.
Production edits are behavior-preserving (see §5).

Self-caught regression during the fix: adding `t.Parallel()` to
`TestDBParity_SQLite` panicked because the wrapper delegates to
`TestConversationView_BunContract_SQLite`, which parallelizes itself
(double `Parallel` on one `T`). Reverted to a `//nolint:paralleltest`
delegation comment; package re-run green.

### §3 Benchmark deltas (11.5 fixed-cost, same host CPU as 11.5 final)

| Benchmark (package) | Baseline (11.5 final) | This change | Verdict |
| --- | --- | --- | --- |
| CompletionGates_Populated (`core/extensions`) | 32 B/op, 1 allocs/op | 32 B/op, 1 allocs/op | identical |
| CompletionGates_Empty (`core/extensions`) | 0 B/op, 0 allocs/op | 0 B/op, 0 allocs/op | identical |
| Project_NoStateFastPath (`core/conversationprojection`) | 21257 B/op, 170 allocs/op | 21257 B/op, 170 allocs/op | identical |
| Reassert_NoState (`core/conversationprojection`) | 544 B/op, 21 allocs/op | 544 B/op, 21 allocs/op | identical |
| Project_4096Tags_20Messages (`core/conversationprojection`) | 240099 B/op, 187 allocs/op | 240111 B/op, 187 allocs/op | same allocs (+12 B machine variance) |

No allocation regression: every allocs/op count is identical to the 11.5 final.

### §4 Skipped with reason

- Linux `-race`: not run locally (Windows `-race` cgo toolchain fails —
  environmental). Held by orchestrator CI run 34160765866 (strict
  race-check, tested SHA `90c88d7a`) with all spec-owned suites ok and no
  `DATA RACE` in spec-owned packages. The 12.2-remediation commits
  (`23fdfbf2`, `35ad8e67`) are tests+docs only except the
  keepwarm/interleaved/hostenv production deltas. Follow-up: orchestrator
  dispatches a fresh strict race run on the final merge SHA (12.4 gate).
- Short `-fuzz` on moved parsers: skipped — `grep -rn "func Fuzz"` over
  `internal/plugins/features/interleavedthinking`,
  `internal/plugins/features/keepwarm`,
  `internal/standardplugins/legacyfeatureconfig` returns nothing; no fuzz
  target exists for the touched parsers.
- `test-db-parity-postgres-direct`: attempted; fails at TCP connect
  (`ping: pgdriver: SASL: read tcp ... i/o timeout` to `51.102.93.217:5432`)
  — no PostgreSQL server in this environment, not a code failure.

### §5 Production files touched (all behavior-preserving)

- `internal/infra/conversationview/reference_store.go`: C-loop →
  `for i := range excess` (identical deletes).
- `internal/infra/runtimebundle/build_executor.go`: gofumpt blank lines
  between adapter methods.
- `internal/standardplugins/featurehost/reasoning/options.go`: manual
  map-merge loops → two `maps.Copy` calls (prod-over-test precedence kept).
- `internal/standardplugins/featurehost/secretguard.go`: gofumpt type
  block for re-exported aliases.
- `internal/standardplugins/legacyfeatureconfig/normalize.go`: nested
  `if/else if` on `item.Content[j+1].Value` → tagged `switch` (identical
  branches).
- `internal/standardplugins/featurehost/interleaved_test.go` (test):
  bare-string context keys → test-only typed key (SA1029).
- `internal/standardplugins/featurehost/bindings_test.go` (test):
  assertion-less empty `if` deleted; now-unused `out` → `_`.

No map[string]any introduced; no exported ForTest identifiers; `git diff
--check` clean; all edited files stay in their existing size class (no
new files except this evidence file).

## Certification statement

Task 12.3 is PASS with listed pre-existing failures and one explicitly
UNCLASSIFIED timing-sensitive observation: the full correctness suite
(`go test -count=1 ./...`) is green, vet / module / vulnerability /
generated-code / docs / arch-report / external-SDK / SQLite-parity /
fixed-cost-benchmark gates are green, and every failure introduced by
this SDD (105 lint findings, all in branch-owned files) is fixed in
this change. Remaining red is pre-existing only: `make quality-checks`
lint reports the 4 `tools/kiro/speccheck` modernize findings verified
identical on `main` (speccheck-inventory is a known main-CI failure);
`postgres-direct` needs a server; Linux race evidence is held at
orchestrator CI run 34160765866 with a fresh strict run gated on 12.4.
Separately, one reviewer-observed `TestCompatibleMultiInstance`
parallel-subtest mismatch is recorded as UNCLASSIFIED per
execution-guardrails §3 (same-signature starting-SHA reproduction not
achieved; neither pre-existing nor introduced may be claimed) with full
reproduction evidence and a 12.4 recurrence watch.

### §6 Final-SHA strict race triage (run 34228868033, tested SHA `e1f3bd21`)

Every spec-owned concurrency suite is `ok` under `-race` on the final
SHA: `core/runtime` (+`failclosed`), `infra/auxiliary`,
`infra/billingcompose`, `infra/conversationview` (+`sdkadapter`),
`infra/runtimebundle` (264s), `plugins/features/compactioncontinuity`
(+`carriers`, `extractor`, `injection`, `observability`, `policy`,
`resultmerge`, `source`, `state`), `plugins/features/interleavedthinking`
(+`state`), `plugins/features/keepwarm`,
`plugins/features/reasoningpreservation` (+`reasoningreplay`),
`plugins/features/secretguard` (+`engine`), `standardplugins/featurehost`
(+`compaction`, `reasoning`, `secretguard`, `sessionpolicy`),
`stdhttp/admin/keepwarm`, `lipsdk/featurehost`, `lipsdk/reasoninghost`,
`lipsdk/secretguardhost`. Remaining run failures (billingstore journal,
openresponses bridge, capsule merge, qa cross-platform, backendplugin
bridge, speccheck inventory, billing-convergence) match the `main`
scheduled-run failure set.

Four `WARNING: DATA RACE` instances, all outside spec-owned production
code: (1) gRPC `pkg/lipsdk/backendplugin/host/session.go`
`SendMsg`/`CloseSend` — backend-connector infra, zero spec diff
(`git diff fff67a0b HEAD` empty for `pkg/lipsdk/backendplugin/` and
`internal/infra/backendplugins/`); (2)+(3) `capsule_test.go:247-248`
loop-variable capture across parallel subtests — test-only, file
untouched by this spec (`git log fff67a0b..HEAD` empty for it);
(4) `backendplugin` cancel-handshake test channel close — test-only,
same untouched trees. No `DATA RACE` in any spec-owned package.
