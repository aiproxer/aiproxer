# Diagnostic Tools Quick Reference

Use these tools to validate the root cause of a slowdown before applying an optimization. Avoid auto-fix flags when the change needs review; inspect and explain any generated diff.

For detailed usage of each tool, see the dedicated reference files:

- [pprof Reference](./pprof.md) — profiling (CPU, heap, goroutine, mutex, block)
- [benchstat Reference](./benchstat.md) — statistical benchmark comparison
- [Trace Reference](./trace.md) — execution tracer
- [Compiler Analysis](./compiler-analysis.md) — escape analysis, inlining, SSA, assembly

## GC and Runtime Diagnostics

Configure via environment variables — no recompile needed.

| Command | Use for |
| --- | --- |
| `GODEBUG=gctrace=1 ./app` | GC frequency, pause times, heap sizes, CPU% — one line per GC cycle |
| `GODEBUG=gcpacertrace=1 ./app` | Why GC triggers when it does — pacer decisions (trigger ratio, heap goal) |
| `GODEBUG=schedtrace=1000 ./app` | Load balancing, goroutine distribution across Ps — prints every 1000ms |
| `GODEBUG=schedtrace=1000,scheddetail=1 ./app` | Per-goroutine state detail on top of schedtrace |
| `runtime/pprof` heap profiles with `alloc_objects` | Allocation sites and GC churn; capture a representative workload and compare samples |
| `GODEBUG=madvdontneed=1 ./app` | Force memory return to OS — useful for container RSS monitoring (default in Go 1.16+) |
| `GODEBUG=gccheckmark=1 ./app` | GC correctness debugging (rare — only when suspecting GC bugs) |
| `GOTRACEBACK=all ./app` | Full goroutine dumps on panic — all goroutines, not just the crashing one |

See the local `golang-troubleshooting` skill for detailed diagnostic interpretation.

### Programmatic APIs

- **`runtime.ReadMemStats`** — heap size, NumGC, pause durations (PauseNs circular buffer), TotalAlloc (cumulative). Use for dashboards, alerting on heap growth.
- **`debug.ReadGCStats`** — GC-specific statistics: pause percentiles, pause timeline, total pause duration. More focused than ReadMemStats.
- **`runtime/metrics` (Go 1.16+)** — stable API, safe for concurrent reads, lower overhead than ReadMemStats. Keys: `/gc/cycles/total:gc-cycles`, `/gc/heap/allocs:bytes`, `/gc/pauses:seconds`, `/sched/latencies:seconds`, `/memory/classes/heap/released:bytes`.
- **`debug.FreeOSMemory()`** — forces GC + returns memory to OS. One-off use after large temporary allocations (not for regular use — let the runtime manage this).
- **`expvar`** — stdlib metrics at `/debug/vars` as JSON. `import _ "expvar"` auto-registers. Lightweight, no dependencies. Integrates with Netdata, Telegraf, or custom dashboards.

## Static Analysis

| Command | Use for |
| --- | --- |
| `fieldalignment ./...` | Detect suboptimal struct field ordering (padding waste). Do NOT use `-fix` flag — let the coding agent apply changes manually with explanatory comments. |
| `unsafe.Sizeof` / `Alignof` / `Offsetof` | Inspect struct memory layout at compile time — compare before/after reordering to quantify savings. |
| `go vet ./...` | Suspicious constructs: printf format mismatches, unreachable code, unused results, suspicious shifts. |
| `staticcheck ./...` | Advanced linter: performance pitfalls (SA9003: empty branch, SA4006: unused value, SA1019: deprecated API). |
| `go test -race ./...` | Data race detection in exercised executions. It does not detect false sharing; investigate that with hardware counters and benchmarks. |

## Third-Party Profiling

| Tool | What it adds | When to use |
| --- | --- | --- |
| **fgprof** (`github.com/felixge/fgprof`) | Full goroutine profiler — captures both on-CPU and off-CPU (I/O wait) time in a single profile. Standard pprof CPU profiles only show on-CPU time. | pprof CPU profile shows low CPU% but latency is high. |
| **Continuous profiling collector** | Aggregate pprof profiles over time, compare deployments, and detect regressions. | Production monitoring and historical analysis; review `golang-observability` for privacy and endpoint controls. |
| **Linux perf** (`perf record -g ./app && perf report`) | Hardware performance counters: cache misses, branch mispredictions, TLB misses. Requires `perf_data_converter` for pprof format. | CPU microarchitecture-level analysis when pprof isn't granular enough. |
