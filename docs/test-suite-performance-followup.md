# Test suite performance

This analysis targets test execution and test fixtures. It does not change
production code, CI selection, compiler caches, or remote QA caching.

## Measured default execution

The comparison uses Go 1.26.6 on Windows/amd64, warm compiler/module caches,
and sequential `go test -json -count=1 -timeout=10m ./...` invocations. The
baseline is source from `e8d52689`, supplied through a Go source overlay in the
same worktree. Both baseline and final candidate passed all 356 root-module
packages. Nested connector modules are outside this measurement.

These are single observations under parallel package execution, not statistical
benchmarks or promises about GitHub runner time. Package durations overlap and
include contention. The execution window is measured between the first and last
test JSON events; it excludes compilation before the first event.

| Measurement | Original | Candidate |
| --- | ---: | ---: |
| Test execution window | 105.1 s | 85.5 s |
| Sum of package durations | 708.7 s | 473.3 s |
| `internal/plugins/frontends/frontendpipe` | 61.8 s | 9.2 s |
| `internal/infra/billingstore` | 83.5 s | 50.0 s |
| `internal/archtest` | 47.9 s | 25.6 s |
| `internal/infra/runtimebundle` | 83.3 s | 67.8 s |
| `internal/plugins/frontends/openresponses` | 21.4 s | 11.1 s |

The observed execution-window reduction is 18.7%. Improvements in unchanged
packages can result from reduced contention and run-to-run variance; they must
not be attributed to direct changes in those packages.

## Expensive certification kept outside default execution

The following tests remain available with `-tags=precommit`, with their original
assertions, payload sizes, budgets, and cross-platform contexts:

| Certification | Why it belongs in the explicit suite | Default protection |
| --- | --- | --- |
| `TestLargePayloadProof_TransientAllocBounded` | Repeated embedded benchmark calibration at 1, 5, and 20 MiB | Functional payload tests and the bounded 1 MiB proof allocation test |
| `TestFindingB1_RealPipeline_TransientAllocBounded` | Another full pipeline benchmark matrix at 1, 5, and 20 MiB | Real pipeline correctness tests and capture helpers |
| `TestCompaction_MemoryBudget_FlatAllocationsAcross1_5_20MiB` | Explicit payload-scaling certification | Compaction Unicode, whitespace, multipart, and sentinel tests |
| `TestGOWORKOff_RootListBuildModuleGraph` | Recursively lists the whole module graph, builds the CLI, and compiles public packages inside an already compiling suite | Command-plan safety and cancellation tests; normal root compilation |
| Full host ownership caller graphs and injected rogue callers | Typed loading for both Linux/amd64 and Windows/amd64 in every ordinary run | The same ownership invariants and independent mutation checks on the native platform |

Native architecture checks verify that all production files selected for the
native build are included in analysis. Explicit certification retains the
original cross-platform file inventory and Windows-only overlay detection.

Run the retained certifications with:

```sh
go test -tags=precommit -count=1 ./internal/archtest
go test -tags=precommit -count=1 \
  -run '^(TestFindingB1_RealPipeline_TransientAllocBounded|TestLargePayloadProof_TransientAllocBounded|TestCompaction_MemoryBudget_FlatAllocationsAcross1_5_20MiB)$' \
  ./internal/plugins/frontends/frontendpipe \
  ./internal/plugins/frontends/openailegacy
```

## Long fixture delays removed

- A durable-store rollback fixture previously repeated a permanent primary-key
  collision through the entire production contention backoff. A test-only query
  hook now observes the actual failing insert and then cancels the child
  context. The production write path still runs, the underlying unique violation
  is asserted, and the original no-partial-row checks remain. Retry, exhaustion,
  and cancellation tests remain unchanged. A focused rollback/contract run fell
  from 16.033 s to 0.313 s.
- The WebSocket shutdown fixture used `net.Pipe` without reading the peer's
  close frame, causing a 10-second production write deadline. It now owns and
  joins a peer reader, and additionally asserts normal close framing. The
  isolated test fell from 10.018 s to 0.019 s.
- Runtime host staging copied entire connector executables with `os.ReadFile`.
  The allocation profile attributed about 1.8 GB of cumulative allocation to
  this fixture helper. Copies now stream into independent destination files.
  A 32 MiB regression fixture fell from 33,565,976 allocated bytes to 36,008,
  while checking content, executable permissions, and mutation isolation.

## Remaining large costs

`internal/infra/runtimebundle` was the longest package in the complete run,
led by billing and session scenarios that took 12–17 seconds under package
contention. The open-core billing split removed those scenarios from the
open-source tree, so re-profile the package before choosing its next target.

The host CPU profile attributes about 30% of aggregate samples to SQLite
`FlushFileBuffers` paths. Its allocation profile also shows substantial full
configuration freezing/YAML serialization. These are cumulative sample and
allocation measurements, not wall-clock percentages. The concurrent store
fixture already uses WAL plus `synchronous=NORMAL`; its composed metering
journal follows the real host opening path. Replacing it with an unrelated
in-memory store would lose the composition/restart evidence these tests prove.
The next substantial reduction should target repeated full host construction
and full configuration fixtures while retaining representative real-stack
sentinels and all restart/ownership assertions.

`internal/infra/billingstore` is the second-largest package. Its isolated CPU
profile attributes about 44% of aggregate samples to `seedTestSchemaIfEmpty`.
More than 600 tests call the ordinary SQLite fixture constructor. The fixture
already avoids replaying the migration chain: it recreates the captured schema
and migration history on each private database. Schema materialization is
therefore the next measurable target, rather than individual short assertions.
Any further change must retain private mutable databases, schema equivalence,
and the real migration path in upgrade tests.

## Verification and limits

The final default root suite passed. The full precommit architecture suite and
the three allocation certifications passed. Linux race checks passed for 100
WebSocket shutdown repetitions and ten repetitions of billing rollback and
account-transaction tests. SQLite contract/parity tests passed; an external
PostgreSQL service was not exercised.

One intermediate complete run failed two unchanged host convergence tests under
load; focused unchanged-main repetitions and the final complete candidate run
passed. Those intermediate failures are not evidence of a corrected convergence
defect. CPU profiling also made the host goroutine-leak check correctly detect
the profiler's `runtime/pprof.profileWriter`; ordinary host tests passed without
profiling. Leak assertions and convergence timeouts were not weakened.
