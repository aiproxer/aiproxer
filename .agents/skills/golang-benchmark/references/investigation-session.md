# Investigation Session Setup

Tools and techniques for **temporary deep-dive performance investigation** — not everyday monitoring. These are things you enable for hours or days while debugging a specific issue, then disable.

## Setting Up a Session

Before diving into profiles, set up the environment to collect high-resolution data:

1. **Reduce Prometheus scrape interval** to <=10s on the target instance (normally 15-30s). More data points during a short investigation window reveal patterns that 30s intervals miss. Revert after investigation.

2. **Enable pprof** through startup configuration or an authenticated runtime control path; an environment variable read only at startup requires a restart:

   ```bash
   kubectl set env deployment/my-service PPROF_ENABLED=true
   kubectl rollout restart deployment/my-service
   ```

3. **Enable continuous profiling** on the target instance only — not fleet-wide. Profiling a single instance is manageable; profiling every replica can overwhelm the backend.

   ```bash
   kubectl set env deployment/my-service CONTINUOUS_PROFILING_ENABLED=true
   kubectl rollout restart deployment/my-service
   ```

4. **Enable debug logging** via env var if needed — but only on the target instance. Debug logging has significant throughput impact:

   ```bash
   kubectl set env deployment/my-service LOG_LEVEL=debug
   kubectl rollout restart deployment/my-service
   ```

**Key principle:** costly debug features need an explicit, authenticated control policy. Startup environment configuration is simple; instant runtime toggles require application reload/control logic.

## Prometheus Go Runtime Collector

The `prometheus/client_golang` library automatically registers collectors that expose Go runtime metrics. These are invaluable during investigation sessions — they provide a time-series view of memory, GC, goroutines, and CPU that complements point-in-time profiles.

When using `prometheus/client_golang`, refer to the library's official documentation to verify collector setup and available options.

### Key Series

→ See [prometheus-go-metrics.md](./prometheus-go-metrics.md) for the **exhaustive reference** of all Go runtime metrics (verified from official sources). **Note:** runtime/metrics list varies by Go version — use `metrics.All()` at runtime for your specific Go version.

**Performance note:** Since Go 1.17, `prometheus/client_golang` populates the `go_memstats_*` metrics from `runtime/metrics` rather than `runtime.ReadMemStats()`, so scraping them avoids the `ReadMemStats()` stop-the-world pause. The default `collectors.NewGoCollector()` already uses `runtime/metrics`; opt in only if you want the additional runtime/metrics series (GC, memory classes, scheduler, CPU):

```go
import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// The default collector already uses runtime/metrics for go_memstats_*.
// Opt in to the full set of additional runtime/metrics series.
reg := prometheus.NewRegistry()
reg.MustRegister(collectors.NewGoCollector(
	collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsAll),
))
reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
```

## PromQL Deep-Dive Queries

Use these during investigation sessions with the reduced scrape interval. Each query includes what to look for and what the result means.

### GC pressure

| PromQL | What to look for |
| --- | --- |
| `rate(go_gc_duration_seconds_count[5m])` | GC cycles/s; compare with this service's baseline and allocation rate rather than applying a universal threshold. |
| `rate(go_gc_duration_seconds_sum[5m]) / rate(go_gc_duration_seconds_count[5m])` | Average GC pause. Increasing trend = heap growing or too many pointers to scan. |
| `go_gc_duration_seconds{quantile="1"}` | Worst-case GC pause. Spikes here cause tail latency (P99). |

### Memory growth investigation

| PromQL | What to look for |
| --- | --- |
| `go_memstats_alloc_bytes` | Cumulative allocation; growth is expected and is not a live-memory/leak proof. |
| `rate(go_memstats_alloc_bytes_total[5m])` | Allocation rate (bytes/s). Compare before/after deploy — significant increase = new allocation pattern. |
| `process_resident_memory_bytes - go_memstats_sys_bytes` | Rough non-Go/resident-memory gap; growth is an investigation signal. |

### Goroutine growth investigation

| PromQL | What to look for |
| --- | --- |
| `go_goroutines` | Compare with load and lifecycle; sustained independent growth is a leak suspect. |
| `delta(go_goroutines[1h])` | Net change over 1h; combine with stack snapshots and controlled load before concluding. |

### CPU saturation

| PromQL | What to look for |
| --- | --- |
| `rate(process_cpu_seconds_total[5m])` | CPU cores consumed. Compare to GOMAXPROCS. |
| `rate(process_cpu_seconds_total[5m]) / <GOMAXPROCS>` | CPU utilization ratio. >0.8 sustained = CPU-saturated. |

### Post-deploy regression detection

| PromQL | What to look for |
| --- | --- |
| `rate(go_memstats_alloc_bytes_total[5m])` | Compare before/after deploy window. Significant increase = new allocation pattern introduced. |
| `histogram_quantile(0.99, rate(http_request_duration_seconds_bucket[5m]))` | P99 latency increase after deploy = performance regression. Requires app-level histogram. |

### Example alerting rules

```yaml
# GC taking too much time
- alert: HighGCPauseTime
  expr: rate(go_gc_duration_seconds_sum[5m]) / rate(go_gc_duration_seconds_count[5m]) > 0.01
  for: 10m
  annotations:
    summary: "Average GC pause >10ms — reduce allocations or tune GOGC"

# Goroutine leak
- alert: GoroutineLeak
  expr: go_goroutines > 10000
  for: 5m
  annotations:
    summary: "Goroutine count >10K — check for leaked goroutines"

# Memory approaching container limit
- alert: MemoryNearLimit
  expr: predict_linear(process_resident_memory_bytes[1h], 3600) > <container_limit_bytes>
  for: 15m
  annotations:
    summary: "RSS projected to exceed container limit within 1h"
```

Adjust thresholds to your application — a data pipeline has different baselines than an API server.

## Host-Level Correlation

Go runtime metrics alone don't show the full picture. Host-level metrics reveal whether the problem is in your application or the infrastructure.

- **`node_exporter`** — host CPU, memory, disk I/O, network. Correlate with Go app metrics: high `node_cpu_seconds_total` with low `process_cpu_seconds_total` = noisy neighbor, not your app.
- **`process-exporter`** — per-process metrics on Linux. Useful when multiple Go services share a host.

## Cost Warnings

**Profiles and traces are expensive to collect.** Keep them short-term and localized:

- **pprof CPU profiling** — CPU-intensive during the capture window. Don't run 30s profiles back-to-back in production. Space them out.
- **Continuous profiling** — overhead, sampling, retention, and exporter cost vary by implementation and workload. Pilot on a bounded subset, measure overhead, and use an explicit application reload/control path if runtime changes are supported; an environment variable alone does not change a running process. See `golang-observability` for privacy and endpoint controls.
- **Execution traces** — generate large files quickly (MB/s). Capture 5-10s max. Longer traces are unwieldy and slow to analyze.
- **Debug log level** — significant throughput impact due to allocation and I/O overhead. Never leave on permanently.
- **All costly features** need an explicit enable/disable path. Treat environment variables as startup configuration unless the application implements reload.
