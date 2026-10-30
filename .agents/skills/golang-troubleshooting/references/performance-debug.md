# Performance debugging

Define the slow operation and workload first: throughput, p50/p95/p99 latency, CPU, allocations, memory, lock wait, or I/O. Establish a baseline under the same input size, concurrency, warm-up, and runtime settings.

Capture CPU and heap profiles with `runtime/pprof` or a protected HTTP endpoint. Use mutex/block profiles for contention, goroutine profiles for blocked work, and trace for scheduler/timer/network sequencing. `inuse_space` or rising RSS is a leak suspect, not proof; compare retained objects after a stable workload.

Change one bottleneck at a time and verify with benchmarks (`ReportAllocs`) and `benchstat`. Avoid fixed allocation or receiver-size rules; compiler escape analysis, architecture, and workload decide whether a change helps.
