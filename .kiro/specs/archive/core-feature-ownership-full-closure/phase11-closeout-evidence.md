# Phase 11 Closeout Evidence

## 11.3 Budget reset: baseline and final deltas

Measurement method: recursive `CountNonTestGoLines` (non-test `.go` physical
lines), identical to the `internal/archtest` budget tests. Baselines from
`implementation-baseline.md` §9.

| Tree | Baseline (Task 1.3) | Final measured | Delta | Reset budget (+25 headroom) |
| --- | ---: | ---: | ---: | ---: |
| `internal/core` | 89,936 | 82,565 | -7,371 | 82,590 |
| `internal/infra/runtimebundle` | 12,541 | 12,316 | -225 | 12,341 |
| `internal/standardplugins` | 3,302 | 6,595 | +3,293 | (no tree ceiling; featurehost budgeted separately) |
| `internal/standardplugins/featurehost` | 0 | 2,979 | +2,979 | 3,004 (new recursive ceiling) |

Deleted feature LOC was not retained as growth allowance: the core ceiling
drops from 89,961 to 82,590 and the runtimebundle ceiling from 12,566 to
12,341. Migration scaffolding removal accounts for the runtimebundle
reduction, so no increase was taken.

New recursive ceiling: `internal/standardplugins/featurehost` at 3,004.
New critical-file caps (measured + 25) on the facade composition surface:
`runtime.go` 184, `process.go` 187, `generation.go` 249, `inputs.go` 121,
`bindings.go` 218.

## 11.1 Core ownership manifest

`internal/archtest/core_ownership.go` covers all 52 top-level
`internal/core/*` production packages: 50 `kernel invariant`, 2 `generic
extension mechanism` (`extensions`, `hooks`, each with recorded independent
consumers). `TestCoreOwnershipManifestCoversEveryTopLevelPackage` fails on any
new top-level core package without an entry.

## 11.2 Dependency and resurrection ratchets

New closure deny-list in `internal/archtest/closure_import_rules.go`,
evaluated together with `ForbiddenImports` by the shared scanner:

- `pkg/lipruntime` -> `internal/plugins/features/*`
- `internal/plugins/features` -> `internal/core`
- `internal/plugins/features` -> `internal/infra/runtimebundle`
- `internal/plugins/features` -> `internal/standardplugins/featurehost`

Previously existing and retained: `internal/core` -> features,
`runtimebundle` -> features, runtimebundle -> featurehost children,
feature-tree boundaries for the five migrated trees, request-path resolver
and registration-collection bans, terminal-decision admission-only lookup and
exclusive-provider chokepoint ratchets, and resurrection bans for all 13
retired package trees (8 core + 5 compose/support remnants).

New request-time guards: no `Resolver`/`Registry`/`Binder`/`Register`/`Bind`
methods on `featurehost.Runtime`, no string-keyed binding/registration map
storage in featurehost production code, and `reflect` confined to `isNil*`
startup typed-nil guards. The redundant absence test list now covers all 8
retired core dirs (`compactioncontinuity` and `conversationview` added).

## 11.4 Change-surface probes (disposable, reverted)

### Probe A: ordinary feature on existing planes

Temporary package `internal/plugins/features/probeordinary/` (noop
`RequestPartHook` on `PlaneRequestPartHooks`, SDK-only imports) registered via
a `featureProbeOrdinary` factory in `internal/standardplugins/features_install.go`
plus one `StandardBundle` table row in `internal/standardplugins/standard_table.go`.

Touched production files (4, all new or standard-distribution only):

- `internal/plugins/features/probeordinary/doc.go` (new)
- `internal/plugins/features/probeordinary/plugin.go` (new)
- `internal/standardplugins/features_install.go` (factory only)
- `internal/standardplugins/standard_table.go` (table row only)

Zero `internal/core`, `internal/infra/runtimebundle`, or public SDK
production edits. `go build ./...` clean; `go test ./internal/standardplugins/`
passed except the pinned `TestSpecBundle_standardBundleIDInventory`, which
fails by design because it pins the exact feature ID list and the probe ID
`probe-ordinary` appears in it — proving end-to-end registration through
standard composition. All probe code reverted (`git status` clean of probe
paths afterward).

### Probe B: host-bound feature on modeled registrations

Temporary package `internal/plugins/features/probehostbound/` (policy built
only from the already-modeled `secretguard.MatcherResolver` host fact) plus a
temporary package-level adapter `probeBindHostBoundPolicy` in
`internal/standardplugins/featurehost/` reading the already-bound startup
`boundHostFeatures.reasoning` value.

Touched production files (4, all new, zero tracked-file modifications):

- `internal/plugins/features/probehostbound/doc.go` (new)
- `internal/plugins/features/probehostbound/policy.go` (new)
- `internal/standardplugins/featurehost/probehostbound.go` (new)
- `internal/standardplugins/featurehost/probehostbound_internal_test.go` (new)

No new `ProcessServices`, `ExecutorConfig`, or `pkg/lipruntime.Options`
field; no `internal/core` or `internal/infra/runtimebundle` production edit
(`git grep` over those trees finds no probe reference; `git status` showed no
tracked modifications). `go build ./...` clean; temporary
`TestProbeHostBoundPolicy_BindsModeledFact` passed, including the absent-fact
failure path. All probe code reverted afterward.

## 11.5 Performance and concurrency certification

Host CPU differs from the Task 1.3 baseline machine (then AMD Ryzen 9 7950X,
now AMD Ryzen 7 5800X), so wall-clock ns/op is not comparable; B/op and
allocs/op are the regression signals.

### Extension plane / request-snapshot allocations (no regression)

`go test -bench 'BenchmarkCompletionGates_Populated|BenchmarkCompletionGates_Empty' -benchmem ./internal/core/extensions/`

| Benchmark | Baseline (1.3) | Final | Verdict |
| --- | --- | --- | --- |
| Populated | 32 B/op, 1 allocs/op | 32 B/op, 1 allocs/op | identical |
| Empty | 0 B/op, 0 allocs/op | 0 B/op, 0 allocs/op | identical |

### Conversation projection (structural move adds no request-time cost)

`go test -bench 'BenchmarkProject_NoStateFastPath|BenchmarkReassert_NoState|BenchmarkProject_4096Tags_20Messages' -benchmem ./internal/core/conversationprojection/`
(the Task 1.3 `conversationview` benchmarks now live in `conversationprojection`)

| Benchmark | Baseline (1.3) | Final | Verdict |
| --- | --- | --- | --- |
| Project_NoStateFastPath | 21248 B/op, 170 allocs/op | 21257 B/op, 170 allocs/op | same allocs (+9 B machine variance) |
| Reassert_NoState | 544 B/op, 21 allocs/op | 544 B/op, 21 allocs/op | identical |
| Project_4096Tags_20Messages | 240640 B/op, 187 allocs/op | 240099 B/op, 187 allocs/op | same allocs |

### Concurrency (non-race locally; -race via CI)

Full suites pass non-race on Windows: `keepwarm`, `compactioncontinuity/...`,
`featurehost/sessionpolicy`, `featurehost`. Focused concurrent tests pass:
`TestManagerQuiesceRejectsConcurrentArmWhileShutdownIsInProgress`,
`TestPolicyConcurrentActorWritesAreSerializedAndSnapshotsAtomic`,
`TestPolicyCloseRejectsWritesCompletesConcurrentOperationAndIsIdempotent`,
`TestReloadConcurrencyCertification_*`,
`TestBranchCoordinator_SerializesConcurrentInjectionUpdates`.

Windows local `-race` skipped: cgo-based race detector fails on Windows
hosts. Linux race evidence obtained from CI run 34160765866
(workflow "Race and fuzz (nightly)", workflow_dispatch on branch
`feat/core-feature-ownership-full-closure`, tested SHA `90c88d7a`,
command `bash scripts/race-check.sh --strict` =
`go test -race -tags=precommit,integration -count=1 <full module list>`).

Required concurrency suites — all `ok` under `-race` on Linux:
`internal/infra/auxiliary`, `internal/infra/conversationview` (+`sdkadapter`),
`internal/infra/runtimebundle` (284s), `internal/integration/conversationview`,
`internal/plugins/features/interleavedthinking` (+`state`),
`internal/plugins/features/keepwarm`, `internal/plugins/features/reasoningpreservation`
(+`reasoningreplay`), `internal/plugins/features/secretguard` (+`engine`),
`internal/standardplugins/featurehost` (+`compaction`, `reasoning`,
`secretguard`, `sessionpolicy`), `internal/stdhttp/admin/keepwarm`,
`pkg/lipsdk/featurehost`, `pkg/lipsdk/reasoninghost`, `pkg/lipsdk/secretguardhost`,
plus `internal/plugins/features/compactioncontinuity` (+`carriers`,
`extractor`, `injection`, `observability`, `policy`, `resultmerge`,
`source`, `state`).

Out-of-scope failures in the same run (billingstore journal concurrency,
openresponses/frontend bridge, `compactioncontinuity/capsule` merge,
`internal/qa` cross-platform selection, billing-convergence baselines,
speccheck inventory) reproduce identically on `main` scheduled runs
(34105540179, 34022492580) and are pre-existing, unrelated to this spec's
ownership paths. No `DATA RACE` warning was reported in any spec-owned
package.
