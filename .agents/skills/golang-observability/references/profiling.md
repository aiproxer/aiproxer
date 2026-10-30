# Profiling and Continuous Profiling

See the local `golang-troubleshooting` skill for on-demand debugging.

## What Profiling Is

Profiling analyzes the runtime behavior of your program — where CPU time is spent, how memory is allocated, which goroutines are blocked, and where lock contention occurs. While metrics tell you "the service is slow," profiling tells you "this specific function on line 42 is the bottleneck."

## On-Demand Profiling with `pprof`

Protect pprof endpoints with authentication/authorization or network isolation; basic auth is one option, not a universal requirement. They expose runtime details and can be abused for DoS.

See the local `golang-benchmark` skill's pprof reference for profile types, capture, and analysis commands.

## Continuous Profiling

On-demand profiling requires a capture during the problem. Continuous profiling retains samples for later analysis, with overhead that varies by collector, profile types, and workload. Startup environment variables can select whether a process starts profiling; changing the environment of an already-running process has no effect unless the application implements reload/control.

Prefer a pull model that scrapes the existing protected `net/http/pprof` endpoints: it needs no profiler SDK in the binary and reuses the same access controls. Use a push SDK only when the deployment environment requires it, and follow the pinned SDK's documentation for its API.

## Cost of Continuous Profiling

Continuous profiling adds CPU, memory, and network overhead to each participating instance. Measure the selected collector and profile types in the target workload; do not assume a fixed percentage.

**Cost factors:**

- **CPU overhead** — profiling itself consumes CPU cycles. In CPU-bound services, even 2-5% overhead matters.
- **Network/storage** — profile data is continuously shipped to the profiling backend. High-replica services multiply this.
- **All profile types enabled** — each additional profile type (mutex, block, goroutine) adds incremental overhead.

**Mitigation:**

- Select participating instances through startup configuration or an authenticated application control path; an environment variable alone does not toggle a running process
- Start with CPU + heap profiles only; add mutex/block/goroutine profiles when investigating specific issues
- In large deployments, enable continuous profiling on a fraction of replicas (e.g., 1 in 10) rather than all of them

## When to Profile

1. Metrics show high CPU/memory usage → look at CPU/heap profiles
2. P99 latency spikes → CPU profile + mutex profile to find contention
3. Goroutine count growing → goroutine profile to find leaks
4. Before and after an optimization → compare profiles to verify improvement
