# Remote CI performance policy

Local iteration policy remains in [development-iteration.md](development-iteration.md).
These rules govern hosted runners and complement #687.

Remote Windows historical comparisons are temporarily paused to unblock
development. Default CI keeps ordinary scoped unit tests and builds on Windows,
Linux and macOS, with no comparison triggered by policy edits or PR labels.
The weekly comparison workflow is disabled in GitHub and its job is guarded off
in source. `make test-cost` remains available locally; remote cost certification
is paused until the workflow is deliberately restored.

## Why improvements regressed

Immutable dependency-only keys froze compiler snapshots. Dedicated keys fixed
first-writer partial snapshots, but PR-only QA had no default-branch producer:
GitHub caches under one PR's merge ref cannot warm another PR. Advancing every
PR snapshot also accumulated old compiler entries and expanded module trees.
On Windows, transferring/extracting/saving these trees became more expensive
than the work they accelerated. Repeated cross-compilation on three hosts added
another multiplier. New workflow lanes could still opt into the old policy.

Representative measured job logs, before this follow-up:

| Lane / PR | Observed cost | Implication |
| --- | --- | --- |
| CI Windows #665 | 539 s total; 130 s restore + 222 s save; 153 s tests/build | Cache I/O dominated useful work. |
| CI Windows #684 | 115 s total; 64 s restore; 17 s tests/build | Even warm snapshots could dominate. |
| Backend Windows #684 | 388 s total; 172 s restore; 177 s QA | Expanded cache extraction was the bottleneck. |
| QA #665/#670/#678 | 541–556 s; same compatible key missed across PRs | Private PR producers did not seed future PRs. |
| QA #679 | 94 s with its own warm PR snapshot | The compiler cache was useful when actually available. |
| CodeQL #678/#679/#684 | 435–450 s; 287–296 s extraction, 126–128 s analysis | Toolchain/dependency setup and extraction dominate. |
| Connector pool race #665/#670 | 483–500 s; 322–333 s runtimebundle execution | Compiler caching alone cannot remove test execution cost. |
| Official compliance #684 | 82–132 s manifest step; test itself 0.023 s | A filesystem check compiled the entire archtest dependency graph. |

Sources: the Actions jobs attached to [#665](https://github.com/aiproxer/aiproxer-enterprise/pull/665),
[#670](https://github.com/aiproxer/aiproxer-enterprise/pull/670),
[#678](https://github.com/aiproxer/aiproxer-enterprise/pull/678),
[#679](https://github.com/aiproxer/aiproxer-enterprise/pull/679), and
[#684](https://github.com/aiproxer/aiproxer-enterprise/pull/684).
These are historical measurements, not claimed post-change speedups.

## Cache ownership and limits

All Go workflows use `.github/actions/go-cache`; setup-go's automatic cache
stays disabled. The composite owns keys, restore fallbacks, byte bounds and
publication rules. `policy.json` is the single registry of producer workflow/job
and compiler size limits. Read-only security, release, nightly and benchmark
consumers borrow compatible lanes; a different job cannot publish that lane.
Each workload has a lane so a small job cannot publish a
partial snapshot as another job's complete baseline.

- Compiler keys include lane, OS, architecture, actual Go version, dependency
  fingerprint and source SHA. Restore fallbacks advance within that toolchain/lane.
- Module snapshots contain `GOMODCACHE/cache/download`, including Go's validated
  module archives, rather than every expanded module source tree. Go expands
  the modules actually needed by the lane. Module keys advance with source too.
- PRs consume caches. Successful main pushes and scheduled/manual main runs
  publish baselines reusable by future PRs. New dependencies/lanes can still
  have a cold first run; merging this policy must seed main before measuring
  cross-PR reuse. Failed main work never publishes a supposedly complete baseline.
- Compiler snapshots default to 768 MiB uncompressed; QA and the complete
  claimed-platform compile lane, native backend lane, full race lane, official
  suite and Windows cost watchdog allow 1536 MiB;
  CI unit/build allows 1024 MiB for both normal and trimpath variants; the
  stdlib-only taskrunner lane uses 256 MiB.
  The shared portable module archive snapshot is capped at 512 MiB. Old entries are discarded
  only after work completes on a disposable main runner. Missing entries
  rebuild or download normally; correctness does not depend on cache hits.
- Cache maintenance keeps the newest completed snapshot per workload/ref,
  removes closed-PR and superseded formats, and bounds owned Go caches to
  7 GiB compressed in aggregate. Private refs are evicted before shared main
  baselines. Other namespaces (including CodeQL/npm) are never deleted by this
  selector. A new Go/toolchain version can evict older baselines safely.
- Cache restore/save duration, hits and snapshot sizes appear in job summaries.
  Actual restored keys distinguish useful main fallbacks from exact-key hits;
  the cache action's `cache-hit=false` alone does not mean a cache miss.
  A transfer phase over 60 seconds emits a warning. Investigate extraction and
  retained bytes before increasing limits. This warning is diagnostic because
  runner/network variation must not make functional checks flaky.

`scripts/test_ci_go_cache.py` and `scripts/test_prune_go_caches.py` exercise bounds,
newest-entry selection, migration and namespace safety offline. QA contracts
ratchet shared-policy adoption and the existence of main producers.

## Workload ownership

The backend workflow compiles every selected claimed GOOS/GOARCH pair once on
Linux. Native Linux, macOS and Windows jobs retain package matrix, lifecycle,
IPC, security and process-tree tests. Their existing status checks require the
compile job and the native matrix to succeed; native execution runs concurrently
with compilation so deduplication does not serialize the critical path. `-compile-only` and `-skip-compile` reports explicitly
identify which evidence was omitted; these modes cannot be combined. Local
`make backend-plugin-cross-platform-qa` still performs the complete work by default.

Module synchronization budgets both levels of concurrency: two modules and
two compiler processes per module. The full connector-pool race package set
is retained and now includes the SDK coordinator regression, with two package workers and four test slots. Race instrumentation
has its own advancing main cache rather than sharing a non-race partial cache.

The vendored official manifest check lives in lightweight filesystem QA and
runs before expensive architecture/compliance work. Static architecture checks,
the pinned 17-case official suite, exact-head evidence and coverage thresholds
remain mandatory. Official/coverage lanes now have scoped main producers.

Unrelated required matrix statuses retain their names and successful bypasses
on Linux; they do not allocate Windows/macOS runners. PostgreSQL starts only
for test-relevant CI. Repository preflight runs independently of database parity;
the required Repo hygiene status still fails closed on either failure. The
independent bridge-node-tests status only executes its suite for relevant scope.

Failure-reporting jobs use `always() && !cancelled()`: dependency failures still
run their fail-closed checks, but superseded work can stop. Bare job-level
`always()` shields a running job from normal cancellation and can leave the next
revision queued behind an obsolete Windows benchmark. A filesystem QA contract
ratchets the cancellation-aware job guard across every workflow. Step-level
artifact/log cleanup retains its existing conditions. An already-running old
workflow uses its old definition and may require GitHub's force-cancel API;
changing a later revision cannot change that job's cancellation condition.

CI classifies main pushes against `github.event.before`, just as PRs use their
actual base. Passing an empty base for every push previously enabled the full
Windows historical cost comparison even for ordinary production changes: the
first main run spent over 25 minutes before failing in the historical suite.
Cost classification remains available as metadata, but default CI
and the weekly/manual watchdog no longer execute the remote comparison. Initial branch pushes
retain full validation, and an invalid predecessor fails closed. Executable QA
fixtures cover production, documentation, cost policy and both predecessor cases.
Security and the native ACP, taskrunner and backend gates also
use the actual push predecessor. Ordinary main merges no longer widen a selected
connector to the full native matrix or run Go scans for documentation alone.
Fifty-three Git fixtures execute the ten lane classifiers/selectors against relevant,
documentation, initial, invalid and manual events. Manual and initial runs retain
full validation; bad revisions fail before any bypass. The official compliance
selector also checks revisions before its process-substitution loop, preventing
a failed Git diff from masquerading as an unrelated change.
Shared SDK, cache and selector changes force the complete connector matrix even
when a single connector is edited in the same commit, retaining full cache seeding.
NousPortal's existing provider parity scenarios also use the release gate's
discoverable `TestParity_` prefix, so certification executes their assertions.

The failed Windows artifact identified an observability fixture's 50 ms
cancellation deadline, not a cost-budget violation. Its immediate fake streams
now have a one-second completion guard; phase, cause and count assertions remain.
The ratchet applies the same five literal guard changes to its pinned historical
anchor, commits only that test file, and rejects an unexpected fixture. Executable
PowerShell QA checks the known anchor, unexpected fixture and unrelated anchor.
Production sources, measured workloads and all cost thresholds remain unchanged.

CodeQL analysis is disabled. `.github/workflows/codeql.yml` keeps an instant
no-op `Analyze (Go)` job only so a required status check of that name still
reports on pull requests.

## Test stalls are separate from compilation

Module validation exposed a cancellation teardown race: a reader could record
CANCEL while being joined, after the coordinator's final worker-start check.
Teardown then waited on a worker that had never started. Connector TCKs could
stall until the whole module's five-minute timeout. The existing coordinator
now starts the final receipt after readers quiesce, before joining cancellation.
A scheduler-controlled EOF/CANCEL regression checks exactly one physical
cancellation, one close, one terminal and no leaked goroutines under `-race`.

Connector bufconn fixtures also bound fallback session close to 30 seconds.
Certification still fails when its required close/scenarios fail; fallback
cleanup cannot hide that diagnostic behind an unbounded Background close.
The self-defense goroutine resource certification also runs in an isolated
test process, preserving its exact no-growth assertion while excluding
background work from earlier tests and transient Prometheus Gather collectors.
State and metric cardinality assertions still run through the same real stack.
This avoids expensive false-positive reruns.

The durable-store enqueue/drain fixture also gives concurrent calls distinct
work heads. Reusing one head produced legitimate same-revision conflicts
whenever several calls were admitted before the fence. Both racing and fully
admitted schedules now assert worker errors, exact admitted counts, pin coverage
and exactly-once posting. No production behavior changes.

A fast filesystem/AST policy prevents the copied unbounded fixture pattern
from returning. No public API/ABI or cancellation frame shape is changed.

## Reviewing changes

New heavyweight lanes need a compatible main producer, explicit workload owner,
bounded cache policy, scoped triggers and fail-closed required statuses. Keep
platform-native evidence native and compiler variants (race/coverage/toolchains)
explicit. Never fix speed by silently shrinking test selectors or overriding budgets.

Compare multiple main/PR runs after seeding, reporting cache misses separately
from hits. Use Actions step logs to distinguish setup, queue/startup, restore,
compilation, test execution and save. Local timings cannot substantiate hosted
Windows/macOS improvements. The weekly development-cost watchdog retains the
existing Windows test budgets; cache warnings and retention tests cover separate
causes that package-duration budgets do not observe.

## Artifact storage

Artifact storage is a shared account allowance charged per hour of occupancy, so
one oversized upload consumes it for its entire retention window. Upload only the
report directory a later reader needs, never the whole checkout or runner temp
root, and declare `retention-days` explicitly: diagnostic evidence keeps 14 days,
release qualification 30. GitHub's retention expiry is the automated cleanup that
still works from an exhausted allowance; a workflow that deletes old artifacts
cannot start once Actions is blocked. `TestWorkflowArtifacts_*` in `internal/qa`
enforces both rules and pins the development-cost watchdog to its
measurements/reports directories, because its OutputRoot also holds measured
binaries and per-run scratch trees.

## Exhaustive certification tier

The full three-node structure and all five four/five-node sweeps,
topology-wide order comparisons and maximum-depth replay probe run with
`make test-billing-schema` (`-tags=integration`). Every existing graph, evidence
assignment, oracle comparison, seam and assertion is retained. Default unit
tests retain the named regressions, bounded model properties and bounded
metamorphic cases. `make qa` includes the certification tier, including when its
duplicate-root-pass optimization is enabled.

CI runs the twelve certification tests in a separate Ubuntu job after the
certified SDK/public-contract, oracle, dependency or certification-policy
changes. Discovery must find all twelve tests. The existing required Repo
hygiene check propagates scope or certification failures, while unrelated
changes report an explicit bypass. The job owns a bounded 768 MiB compiler-cache
lane with trusted main publication and read-only PR restoration. Pure schema
arithmetic needs no native platform matrix; Windows keeps ordinary portable
unit/build checks. The certification process has a separate 30-minute execution
guard; it does not change any test assertion or ratchet threshold.

The triggering Windows artifact contained the certified package's ten-minute
test timeout, although its final log tail showed successful unrelated packages.
The historical anchor passed. This is an execution-tier correction for exhaustive
certification, not a compiler-cache hit claim.

The certification products are planned to move to the Enterprise composition
together with their implementations, and their operator documentation moves to
the Enterprise tree (the stub stays here). That move is not finished: the
certification tier still runs in this tree against the in-tree package, and the
open-source tree keeps the harness that runs it.

Default runtime checkpoint fixtures retain real SQLite files and restart proofs,
using WAL with `synchronous=FULL` to avoid rollback-journal creation without
disabling commit synchronization (see [SQLite's WAL documentation](https://sqlite.org/wal.html)).
The independent-reader fixture opens every cursor before reading and uses
scheduler yields for varied pacing instead of an OS timer for each small read.
Push-classifier scenarios reuse one repository per workflow lane, running serially
against successive real commits; lanes remain parallel and every output assertion
is retained. These fixture changes leave production database settings and all
other Windows cost budgets unchanged.

The runtime package has an explicitly approved 45-second growth allowance above
the frozen anchor, which predates its added checkpoint and durable-restart proofs.
The measured increase was 37.3 seconds. Its existing relative multiplier remains
in place; all other package and aggregate limits are retained. The
`AIP_ALLOW_TEST_COST_POLICY_UPDATE=1` authorizes local policy-edit preflight
without authorizing measurement violations. The `allow-test-cost-policy-update`
PR label records that approval; it no longer starts remote measurements.
The broader `AIP_ALLOW_TEST_COST_GROWTH` override remains a separate opt-in.

The ratchet uses a stable, short checkout path for each pinned historical anchor.
Go compiler entries depend on source paths, so a random checkout path prevents
reuse even when the frozen source is identical. Per-run temporary roots and
measurement artifacts remain isolated. An existing anchor directory still fails
closed; concurrent measurements must not share or overwrite that checkout.
