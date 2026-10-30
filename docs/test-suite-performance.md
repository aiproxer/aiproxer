# Test-Suite Performance Investigation

Branch: `perf/test-suite-speedup` (sibling worktree `aiproxer-perf`),
based on `main` @ `a7714fd1`.

All timings are on this 16-core Windows box, Go 1.26.6, warm Go build/test cache.

## Executive summary

The suite was slow primarily because hundreds of file-backed SQLite tests
competed for the same disk, each committing with a full fsync. Under the default
`go test ./...` package fan-out (up to 16 packages × 16 parallel tests), that
turned ~55s of isolated work per store package into 200s+, and starved unrelated
packages too.

Removing the per-commit fsync from test databases (WAL + `synchronous=NORMAL`)
plus de-serializing `archtest`'s Go-toolchain subprocesses cut the whole
`go test ./...` run from **~361s to ~86s (≈4.2x)** with **zero test failures** and
no test removed or weakened.

| Metric | Before | After | Factor |
| --- | --- | --- | --- |
| Whole `go test ./...` invocation (incl. build) | ~361s | **86.2s** | 4.2x |
| Test-phase span (JSON event window) | 305.3s | **81.0s** | 3.8x |
| Sum of per-package test time | 3,820s | **504s** | 7.6x |
| Link/startup-only (`-run=^$`) | 55.7s | 55.7s | 1.0x |

## Root causes

1. **File-backed SQLite with the default rollback journal + `synchronous=FULL`.**
   Isolated `billingstore` was 55s but 223s under the full suite; `journalstore`
   6.8s → 49s; `runtimebundle` 81s → 245s. The inflation is fsync/disk
   serialization across dozens of concurrent test binaries.
2. **`archtest` shells out to `go list` / `go list -m all` / `go build` /
   `go test -run=^$`.** Isolated 27s, but 159s under load because those
   subprocesses contend with the rest of the suite for CPU and the Go build
   cache.
3. **Mega test packages.** `runtimebundle` (942 tests) and `core/runtime`
   (1,839) were single binaries, but this was secondary to the I/O contention.
4. **Redundant re-tag passes.** `make test` = quality-checks-fast + untagged
   `./...` + parity re-tags; `make qa` adds a third tagged `./...`. Each fresh
   pass pays ~56–62s of link/startup.

## Changes landed

### A. Fast test SQLite DSNs (WAL + `synchronous=NORMAL`) — 52 test files

Upgraded every **file-backed** test DSN (skips `mode=memory`) to add
`_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)`, preserving
`foreign_keys`, `busy_timeout`, and `_txlock` ordering. WAL+NORMAL keeps
close/reopen and process-crash durability for committed transactions while
avoiding a full fsync per commit. This was already the accepted pattern in
`leasestore`, `workstore`, `authoritystore`, and `journalstore` (WAL) tests; no
test asserts rollback-journal or power-loss semantics.

Isolated effect (tests all pass):

| Package | Before | After |
| --- | --- | --- |
| `infra/billingstore` | 55.4s | 33.0s |
| `infra/metering/journalstore` | 6.8s | 3.3s |
| `infra/runtimebundle` | 81.1s | 32.9s |
| `infra/usageauthority/authoritystore` | — | 3.2s |

Standalone microbenchmark (outside the repo), 300 committed transactions to a
file DB: **1.214s default vs 0.010s WAL+NORMAL (~120x)**, 300/300 rows durable
after reopen.

### C. Concurrent GOWORK=off plan — 1 file

`internal/archtest/backend_plugin_gowork_test.go` now runs the plan's four
independent `go` commands concurrently (first failure cancels the rest).
`archtest` loaded time dropped **115.5s → 52.8s**; isolated 27.2s → 23.7s.

### E. `make test-quick` — Makefile

Documented single-pass inner-loop target (`go test ./...` with the standard
flags), distinct from the comprehensive `make test`. Kept out of `.PHONY` to
match the `lint-advisory` precedent and the frozen
windows-task-reliability classification table; help text added.

### F. Guidance — `.kiro/steering/testing.md`

Recorded the fast file-backed SQLite DSN convention in the Test-Cost policy and
added `make test-quick` to the canonical command intents.

## Evaluated but not landed (with evidence)

### B. Injectable relay/worker intervals — **no gain after A**

Time-boxed experiment: temporarily shrank the observation-relay interval
(100ms→1ms) and the background worker intervals (1s→1ms), then ran
`runtimebundle`: **33.5s vs 32.9s baseline — no improvement.** Once the fsync
contention was removed, poll cadences stopped being the bottleneck. Landing it
would require a production constructor API change (318 `BuildOptions` callers)
for no measured benefit, so it was reverted.

### D. Prebuild/cache in-test stub binaries — **already mitigated**

The connector staging cache (`staging_cache_test.go`) already builds each stub
binary once per test-binary run. Isolated, the stub-build test is 4.4s vs 3.6s
for a no-op invocation (~1s attributable to the build). Cross-package sharing
would need a content-addressed binary cache; not justified.

## Verification

- `go test -json -count=1 -parallel=16 -timeout=20m ./...` → **exit 0**, 350
  packages reported, **0 test-level failures**.
- `gofmt -l` on all changed Go files → clean.
- `go test ./internal/qa -run TestWindowsTaskReliability_TargetTableComplete`
  → pass.
- Changed surface: **53 `*.go` files** (under the 100-file gate) + `Makefile` +
  `.kiro/steering/testing.md`.

## Trade-offs and follow-ups

- The DSN change deliberately trades rollback-journal/full-fsync semantics for
  speed in tests. Any future test that asserts journal mode or power-loss
  durability must keep its own DSN; this is noted in steering.
- `archtest` remains the second-longest package (~53s loaded) because it still
  invokes the Go toolchain; the remaining cost is one shared
  `go list -deps -json ./...` plus nested-module compile gates. Moving the
  nested-module gates to a tagged/CI tier is a candidate follow-up but would
  shift coverage out of `make test`, so it was not done unilaterally.
- `make test`/`make qa` still re-run overlapping passes; consolidating them is a
  gate-composition change that needs maintainer sign-off (E only adds a
  documented fast path).
