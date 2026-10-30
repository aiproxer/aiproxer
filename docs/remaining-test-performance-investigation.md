# Remaining test performance investigation

Investigated architecture gates, remaining metering journal fixtures, and
durable-store host tests at merged PR #700, commit
`44eea98bb6f429ea6a352fa0a06e29063ae25507`. The investigation below records the
baseline and diagnostic probes. The implementation section records the selected
follow-ups. Default gates and CI policy are unchanged.

## Implemented follow-ups

Durable work claims now read their marker through the existing transaction.
The marker reader retains its validation, missing-marker classification and
pooled behavior. A bounded regression covers one- and two-connection SQLite
pools; the same assertion is included in the mandatory PostgreSQL parity entry
point.

Six slow stock host scenarios now execute inside `testing/synctest` bubbles.
Production workers retain their original one-second cadence, constructors and
lifecycle ownership. Real database writes, host construction, close/reopen and
evidence assertions remain unchanged. Bubble cleanup also requires owned
goroutines to terminate. Test bodies remain separate named functions to avoid
large indentation-only diffs.

The 1,000-tick progress regression uses the same isolated clock and additionally
asserts exactly five seconds of simulated elapsed time. Its iteration count,
stall window, tick duration and parent deadline remain unchanged.

| Fresh isolated binary | Baseline wall | Implementation wall, three processes |
| --- | ---: | ---: |
| Resume scenario | 4.16–4.18 s | 1.07 / 0.66 / 0.67 s |
| 1,000-tick progress | 5.02 s | 0.027 / 0.022 / 0.026 s |
| Complete billingstore | 11.79 / 10.30 s | 7.77 / 9.69 / 7.78 s |
| Complete runtimebundle | 21.16 / 20.00 / 29.56 s | 31.96 / 18.36 / 23.90 s |

The complete runtimebundle observations do not demonstrate a package-wide gain;
they have substantial variance and the isolated gains must not be substituted
for an aggregate speedup. In the first root-suite pair, the six changed host
scenarios fell from 12.52–16.58 s to 4.55–7.41 s under load, while other tests
still determined package completion.

Two sequential matched root comparisons used warm compiler/module caches,
native 16/16 concurrency, fresh `-count=1` execution and a source overlay restoring
the baseline implementations. The second comparison reversed execution order.
The baseline overlay was passed through `GOFLAGS` to nested Go commands too.
All 360 package results passed in every run.

| Root comparison | Baseline wall / CPU | Implementation wall / CPU |
| --- | ---: | ---: |
| Baseline first | 101.50 / 761.06 s | 108.39 / 756.34 s |
| Implementation first | 89.34 / 732.52 s | 89.81 / 765.64 s |

There is no demonstrated aggregate root-suite improvement. The implementation
removes a transaction/pool correctness defect and makes the selected inner-loop
scenarios substantially faster; it should not be described as a full-suite
speedup. The large variation in unchanged packages also precludes attributing
the first pair's wall regression to a specific mechanism from these samples.

Verification completed: all 360 default root packages passed on both sides of
the initial matched comparison; mandatory quality checks passed; focused store
race checks passed; changed host scenarios and polling regressions passed two
race-instrumented repetitions; SQLite catalog parity passed; the integration-tagged
store package compiled with the new PostgreSQL regression.

The configured remote PostgreSQL parity run failed during authentication reads
with network timeouts. It does not certify the repair's PostgreSQL behavior.
The hosted `db-parity` job must pass against its ephemeral PostgreSQL service
before merge. Architecture and remaining journal maintenance remain deferred.

## Priorities supported by measurements

| Priority | Finding | Fresh Windows evidence | Follow-up |
| --- | --- | --- | --- |
| 1 | Work claims request another pooled connection while holding a transaction | Original concurrent claim: 0.21 / 5.16 / 5.18 s; transaction-read probe: 0.12–0.18 s, five passing processes | Correct the transaction boundary, with dedicated correctness and dialect verification |
| 2 | Real host scenarios wait for one-second background worker ticks | Resume scenario: 4.16–4.18 s original, 0.70–0.79 s with diagnostic 20 ms worker ticks | Introduce controlled test scheduling while retaining real worker execution and production cadence |
| 3 | Architecture gates consume substantial aggregate CPU, but existing sharing already avoids many repeated queries | Complete architecture package: 12.86 / 13.47 s, approximately 100 / 102 CPU seconds, 129 / 136 processes including the test process | Profile child/toolchain work before a broader refactor; no demonstrated large test-body saving yet |
| Defer | Remaining journal WAL/NORMAL conversion | Complete journal package: 2.72 / 2.95 s original, 2.12 / 2.25 s scoped probe | Small maintenance change, outside the requested large-gain priority |

Times overlap inside a package. Package improvements cannot be added together to
predict full-suite savings. These measurements do not establish a new root-suite
or GitHub runner timing.

## Method and evidence

Native Windows/amd64, Go 1.26.6, Ryzen 7 5800X, 16 logical processors. Measurements
used stable compiler/module caches and sequential child executions. Test binaries
were compiled before their measured runs; their wall times exclude compilation.
Each measured process used `-test.count=1 -test.parallel=16 -test.timeout=10m`.
Source overlays supplied diagnostics and prototypes without editing repository
sources. Windows Job Object accounting collected descendant-inclusive CPU,
process count and process I/O. Process I/O is not physical-disk traffic.

Raw logs, metrics, profiles, overlay sources and runners are retained locally in
`C:/Users/Mateusz/tmp/aip-perf-investigation-700`. See
`remaining-test-performance-evidence.json` for the selected measurements and
artifact names. Runners are `run_baseline.py`, `run_variants.py`, `run_confirm.py`
and `run_final.py`; `probe.go` wraps the repository's existing `tools/taskrunner`.
They invoke binaries from their package source directories, matching `go test`.
Nested native gates used Git's Bash on PATH rather than the WSL shim.

The initial new-worktree run included cache warm-up and is excluded. One host
confirmation overlapped a diagnostic compilation and is also excluded. Profiled
runs are identified separately. Failed exploratory journal runs are retained,
but excluded from successful timing comparisons.

A separate warm `go test -json -count=1 -parallel=16 -timeout=10m` of the four
investigated packages passed in 29.72 s, with 221.14 descendant CPU seconds and
283 processes. Its package durations were architecture 15.76 s, journal 3.07 s,
billingstore 14.27 s and runtimebundle 25.68 s. This loaded execution explains why
isolated test timings differ from package and full-root timings.

## Durable-store work claim: a correctness defect exposed by a slow test

The work-claim path opens a transaction, then calls a claim gate. The gate
receives that transaction but reads its marker through the pool handle instead.
That read therefore needs a second pooled connection.

The concurrent claim test starts eight contenders against an eight-connection
SQLite pool with a five-second busy timeout. In a stalled run, the block profile
attributes approximately 5.03 s to the pooled marker query and 5.05 s to
`database/sql.(*DB).conn`. Other claim transactions can occupy the pool while
waiting for SQLite's writer. A busy timeout then releases a slot. The observed
timing is bimodal; this is not a uniformly slow query.

A bounded diagnostic regression prepared authentic work, then attempted the
claim with a 250 ms context:

- Pool size one: unchanged implementation failed with a marker-read context
  deadline exceeded after approximately 250 ms.
- Pool size two: unchanged control passed.
- A probe reading the existing marker row through the supplied transaction:
  both sizes passed, and the unchanged eight-contender test passed in five fresh
  processes at 0.12–0.18 s. Winner, fencing, foreign completion, duplicate
  completion and reclaim assertions remained intact.

The full default durable-store binary passed on the probe three times in
7.77 / 8.20 / 7.89 s, compared with unchanged 11.79 / 10.30 s. All 939 existing
top-level tests passed, plus the diagnostic regression. This is a production
transaction/pool issue, not merely fixture overhead. Increasing the pool or
shortening the busy timeout would mask the demonstrated dependency.

The eventual repair must preserve missing-marker errors and ownership
semantics. Review transaction visibility and activation serialization, test
single-connection and pool-saturation paths, and run applicable SQLite/PostgreSQL
parity plus Linux race checks. The prototype is not a certified repair;
external PostgreSQL and race verification were not performed here.

## Host tests: scheduling dominates representative scenarios

The resume test's instrumentation measured roughly 3.6 s in waits for terminal
delivery and background worker heads. Its initial durable-store setup was
76 ms, with reopen setup 3 ms. This confirms that repeating the already
optimized schema work is not the principal remaining bottleneck.

The background post-usage and revision workers use one-second intervals. A
diagnostic overlay changed only those constructor intervals to 20 ms; the
observation relay retained its 100 ms cadence. The original resume assertions
passed three times in 0.70 / 0.79 / 0.78 s, versus approximately 4.16 s
unchanged.

All 979 top-level runtimebundle tests passed on the interval probe in three
complete runs. Unchanged package observations were 21.16 / 20.00 / 29.56 s;
probe observations were 16.21 / 18.59 / 19.34 s. Their medians differ by about
12%, with substantial variance. Descendant CPU medians increased from 67.14 to
71.78 s. Faster polling is therefore evidence of removable waits, but a blanket
interval reduction is not a sound production recommendation.

There is a second avoidable wall-clock cost:
`TestOutboxDrainPollCompletesThroughSlowProgress` executes 1,000 iterations with
5 ms ticks, taking 5.02 s isolated. A diagnostic timing-scale change to 5 us ticks
and a 200 us stall window retained all 1,000 iterations and passed in
0.23 / 0.24 / 0.41 s. This establishes sensitivity to elapsed time, not a robust
replacement test. A deterministic clock/tick source should preserve progress,
stall, blocking-read and cancellation contracts without Windows timer precision
dependence. Its savings cannot be added to the worker experiment: both tests run
in parallel with other package work.

Follow-up acceptance: production cadence unchanged; real worker ownership,
posting, close/reopen and exact economic assertions preserved; explicit tick or
work-availability control in tests; cancellation/race coverage; fresh package and
root-suite comparisons demonstrating the resulting wall and CPU effects.

## Architecture: large aggregate cost, narrower evidence for further savings

Isolated native caller check: 0.87–0.89 s, approximately 3.3–3.9 CPU seconds.
Overlay negative caller check: 1.97–1.99 s, approximately 11–12 CPU seconds.
External billing module gate: 3.29–3.42 s, approximately 6.4–7.0 CPU seconds.
The complete package remains much heavier because these and other checks overlap.

Instrumentation measured native type loading at 0.86 s and overlay loading at
1.77 s. The separate runtimebundle ownership guard took approximately 2.05 s:
each of its Windows and Linux metadata scans discovered 387 packages in about
0.8 s, followed by about 0.9 s type loading for five selected owner roots. The
two platform paths execute concurrently and prove distinct build contexts.

Existing code already shares native non-overlay type loads, ownership loads by
platform, and a single module dependency inventory for many boundary assertions.
Overlay loads must retain independent mutated-source evidence. Removing a load
or platform without preserving these invariants would reduce coverage.

A direct fresh JSON run of the external billing fixture took 3.12 s overall,
but its actual test package took only 0.075 s. Most isolated gate time is Go
command/build/startup work, not lengthy test bodies. This gate verifies the current
public billing API and lifecycle; no evidence identified it as an obsolete release
gate that should simply disappear from defaults.

The architecture CPU profile on Windows attributed extensive samples to pipe
reads and process waits, and exceeded Job Object CPU accounting. It is unsuitable
for interpreting those sample percentages as actual CPU utilization. Keep the
approximately 100 CPU seconds from Job Object accounting as the aggregate result;
obtain child-command attribution before claiming a particular loader redesign
will remove a large fraction of it.

## Journal: measurable, but low absolute return

The three remaining restart/reconstruction tests together took 0.67–0.97 s
isolated. WAL/NORMAL probes reduced them to 0.11–0.20 s while retaining file
close/reopen and real migrations. Full-package medians fell from 2.84 to 2.19 s,
about 0.65 s. All 134 existing top-level tests passed in both scoped probe runs.

An initial overbroad DSN rewrite failed memory-backed callers of the shared
helper. The corrected diagnostic leaves existing `file:` memory DSNs untouched
and changes plain file paths only. That failure is evidence that any eventual
maintenance change must preserve shared helper semantics. No migration, retry or
durability assertion was removed. This work is a lower priority than the
transaction defect and host scheduling given the requested focus on large gains.
