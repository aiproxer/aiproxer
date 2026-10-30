# Exact-head baseline-versus-final performance certification

## Verdict

**PASS — performance gate closed.**

The repeated isolated comparison covers the complete required matrix. Candidate compilation is comfortably within the 10% ceiling (it improves in all three metrics). Manager Acquire/Release and generation dispatch are statistically unchanged. BuildHost, successful reload, and no-op reload improve. The only regression is Manager publication; the maintainer explicitly approved its measured absolute cost and operational rationale on 2026-07-26.

## Revisions

- Reviewed baseline: `efe4624909cea318c7211d5cb3734059d3210802`
- Certified implementation head: `a5a2d375c767b3dad8225de0879f5a6c6f4b1ee5`
- Final evidence commit: evidence-only descendant of `a5a2d375`; no production or benchmark source changed after capture.
- Baseline fixture blob: `2ea6f79e83c1355156d9e423d5db16a4394c57f2`
- Final fixture blob: `2ea6f79e83c1355156d9e423d5db16a4394c57f2`
- `internal/infra/runtimehost/reload_bench_test.go` blob at both revisions: `c9a8ea62abfb1c83b255bfbeac91c8b5ddf13fb7`

## Results

Medians and significance are from `benchstat`, 10 samples per revision.

| Surface | Time vs baseline | Bytes vs baseline | Allocations vs baseline | Verdict |
| --- | ---: | ---: | ---: | --- |
| Candidate compilation | **-19.68%** | **-15.44%** | **-12.56%** | PASS; all required metrics improve |
| BuildHost | **-10.07%** | **-11.80%** | **-10.56%** | PASS |
| Successful reload | **-2.09%** | **-11.51%** | **-9.97%** | PASS |
| No-op reload | **-1.82%** | +0.96% | +1.52% | PASS |
| Manager Acquire/Release | statistically unchanged | unchanged | unchanged | PASS |
| Generation dispatch | statistically unchanged | unchanged | unchanged | PASS |
| Manager publication | +321.32% | +55.05% | +125.00% | **APPROVED EXCEPTION** |

### Approved publication exception

Publication moved from `561.5 ns/op`, `480.5 B/op`, 4 allocs/op to `2365.5 ns/op`, `745.0 B/op`, 9 allocs/op.

Absolute impact: **+1.804 microseconds and +5 allocations per successful configuration publication**. This is paid once per successful runtime configuration reload, not per request. The cost comes from launching the required manager-owned asynchronous retirement of the replaced generation, preserving non-blocking publication and independent retirement of unrelated generations. Acquire/Release and generation dispatch remain statistically unchanged.

The maintainer selected: **“Approve the +1.804µs / +5 allocations per reload publication and document it in PR #205.”**

## Isolation protocol

- Host: Linux `6.17.0-1018-oracle`, AMD EPYC 9J14, two available CPUs (`0,1`), Go `1.26.5 linux/amd64`.
- Equal-length detached worktrees: `/tmp/lipbench-A` and `/tmp/lipbench-B`.
- Prebuilt package test binaries; module download and `100ms` warmups completed before capture.
- Controlled environment: `GOMAXPROCS=2`, `-test.cpu=2`, `taskset -c 0,1`, `LANG=C`, `LC_ALL=C`, `TZ=UTC`, common HOME/TMPDIR.
- Host immediately before capture: load average `0.10, 0.16, 0.22`; no competing CPU-heavy process.
- Schedule: `ABBA` repeated five times (20 package-matrix runs; 10 samples per revision).
- Each benchmark: `-test.benchtime=2s -test.count=1 -test.benchmem -test.run=^$`.
- Every raw file contains exactly 10 samples for each of the seven required benchmark names.

## Baseline-only benchmark overlay

Baseline `efe46249` predates the BuildHost/successful-reload/no-op-reload benchmark functions. `benchmark-baseline-overlay.patch` adds only equivalent test harnesses:

- BuildHost uses baseline `BuildBootstrap(BootstrapServe)` plus `AttachReloadHost` with the canonical standard HTTP composer.
- Successful and no-op reloads invoke the real baseline coordinator and assert `ResultPublished` / `ResultNoop`.
- The successful-reload harness retires `PreviousGeneration` through the baseline canonical `LifecycleWorker` outside the timed region, because baseline publication did not schedule retirement itself.
- Production files are unchanged.

Overlay SHA-256: `668690728a28e4cd0524824f9d425c1c15e282327a911e16022c2db0f91fa63b`.

## Artifacts and checksums

| File | Role | SHA-256 |
| --- | --- | --- |
| `bench-final-runA.txt` | Raw baseline samples (`efe46249`) | `cde1425618914b7c9a2a1ebf0a17c72e28a6394c3b512ebbe55e95450e654a25` |
| `bench-final-runB.txt` | Raw final samples (`a5a2d375`) | `b49ca628c32d539dc850a31838ea3e856269faa6421ad1dae89607d761a6e685` |
| `benchstat-final.txt` | Baseline-versus-final `benchstat` output | `f926d0ac96e68825dbd9c7905e2866186c4ff4fd27b552cdae78d09a6c3e139d` |
| `benchmark-baseline-overlay.patch` | Reviewed baseline-only harness | `668690728a28e4cd0524824f9d425c1c15e282327a911e16022c2db0f91fa63b` |

## Covered benchmark names

- `BenchmarkCandidateCompilation`
- `BenchmarkBuildHost`
- `BenchmarkSuccessfulReload`
- `BenchmarkNoopReload`
- `BenchmarkManager_AcquireRelease`
- `BenchmarkManager_Publish`
- `BenchmarkGenerationDispatcher_AcquireLease`
