# CI Benchmark Regression Detection

Use comparable repeated measurements for regression decisions. A controlled local machine can be more reliable than a shared CI runner; record workload, toolchain, host, raw samples, variance, and operational budget. CI is authoritative only when the repository adopts a suitably controlled gate. Tools like `benchdiff` help compare revisions in either environment.

## benchdiff

Runs Go benchmarks on two git refs and uses `benchstat` to display deltas. Caches results for non-worktree refs so re-runs are fast. Prevents macOS sleep during benchmarks.

```bash
go install filippo.io/mostly-harmless/benchdiff@vX.Y.Z # replace with a reviewed release
```

```bash
# Compare current worktree against HEAD (default)
benchdiff -- -benchmem

# Compare two specific refs
benchdiff -base-ref main -head-ref feature-branch

# Compare against a specific commit or tag
benchdiff -base-ref v1.2.0

# Pass extra flags to go test — everything after -- goes to go test
benchdiff -- -benchmem -count=10 -benchtime=3s

# Filter to specific benchmarks
benchdiff -- -benchmem -count=10 -bench=BenchmarkParse

# Target a specific package
benchdiff -- -benchmem -count=10 ./pkg/parser/...

# Clear cached results (useful after rebasing or when cache is stale)
benchdiff -clear-cache

# Combine: compare main with 10 iterations, filtered to critical benchmarks
benchdiff -base-ref main -- -benchmem -count=10 -bench='BenchmarkParse|BenchmarkEncode'
```

Best for: quick PR-to-base comparisons in git-based workflows. Leverages `benchstat` for statistical rigor and caches non-worktree refs so re-runs only re-measure the worktree.

## cob

Compares benchmarks between HEAD and HEAD~1, failing the CI job if performance degrades beyond a configured threshold. Calibrate that threshold from repeated baseline variance and the user-visible budget; the tool's default is not a policy.

```bash
go install github.com/knqyf263/cob@vX.Y.Z # replace with a reviewed release
```

```bash
# Run with the tool's default only after reviewing it against measured variance
cob

# Configure a threshold only after measuring this benchmark and runner
cob -threshold 10

# Compare against a specific base commit
cob -base main

# Only report regressions (ignore improvements)
cob -only-degression

# Choose which metrics to compare (default: ns/op,B/op)
cob -compare "ns/op,B/op,allocs/op"

# Custom go test arguments
cob -bench-args "test -run '^$' -bench BenchmarkParse -benchmem ./pkg/parser/..."

# Increase benchmark duration for more stable results
cob -bench-args "test -run '^$' -bench . -benchmem -benchtime=3s ./..."

# Skip cob for a specific commit: include [skip cob] in commit message
```

**Caution:** `cob` may manipulate the checkout internally; run it in a disposable CI checkout and never point it at a worktree with uncommitted changes. It requires benchmarks to pass and its single-run comparison is more noise-sensitive than `benchstat`.

Best for: simple post-commit regression gating in CI where statistical rigor is less critical than fast feedback.

## gobenchdata

GitHub Action + CLI that collects benchmark results, publishes to gh-pages as JSON, and visualizes with an interactive web dashboard. Shows performance trends over time.

```bash
go install go.bobheadxi.dev/gobenchdata@vX.Y.Z # replace with a reviewed release
```

### CLI commands

```bash
# Parse go test -bench output to JSON
go test -bench=. -benchmem -count=5 ./... | gobenchdata --json bench.json

# Parse from a file
gobenchdata --json bench.json < bench.txt

# Add a tag to the benchmark run (e.g., git commit)
gobenchdata --json bench.json --tag "$(git rev-parse --short HEAD)" < bench.txt

# Evaluate regression checks against a checks config
gobenchdata checks eval bench.txt --checks-config .gobenchdata-checks.yml

# Generate the web dashboard app (static Vue.js site)
gobenchdata web generate ./dashboard-app

# Serve the dashboard locally for preview
gobenchdata web serve ./dashboard-app

# Merge multiple benchmark JSON files
gobenchdata merge old-bench.json new-bench.json > combined.json

# Prune old entries (keep last 30 runs)
gobenchdata prune --count 30 bench.json
```

### GitHub Action setup

```yaml
# .github/workflows/benchmark.yml
name: Benchmark
on: [push]
jobs:
  benchmark:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2
      - uses: actions/setup-go@d35c59abb061a4a6fb18e82ac0862c26744d6ab5 # v5.5.0
        with:
          go-version: stable
      - name: Run benchmarks
        run: go test -bench=. -benchmem -count=5 ./... | tee bench.txt
```

Add a history publisher only after pinning it to a reviewed full commit SHA and scoping its write permissions to that job.

### Regression gating on PRs

```yaml
- name: Check for regressions
  run: gobenchdata checks eval bench.txt --checks-config .gobenchdata-checks.yml
```

```yaml
# .gobenchdata-checks.yml
checks:
  - name: "No major regressions"
    package: ./...
    benchmarks: [".*"]
    thresholds:
      - metric: NsPerOp
        max: 1.2 # fail if >20% slower
      - metric: AllocedBytesPerOp
        max: 1.3 # fail if >30% more allocations
  - name: "Critical path stability"
    package: ./pkg/parser
    benchmarks: ["BenchmarkParse.*"]
    thresholds:
      - metric: NsPerOp
        max: 1.1 # stricter: fail if >10% slower
```

### Dashboard configuration

```yaml
# gobenchdata-web.yml — configure the Vue.js dashboard
title: "My Project Benchmarks"
description: "Performance tracking dashboard"
chartGroups:
  - name: Parser
    charts:
      - name: Parse Performance
        package: myapp/pkg/parser
        benchmarks: ["BenchmarkParse.*"]
        metrics: [NsPerOp, AllocedBytesPerOp, AllocsPerOp]
  - name: Encoding
    charts:
      - name: Encode/Decode
        package: myapp/pkg/encoding
        benchmarks: ["Benchmark(Encode|Decode).*"]
        metrics: [NsPerOp, MBPerS]
```

Best for: long-term trend tracking and visualization; complements benchdiff/cob for immediate gating.

## Tool Selection Guide

| Tool | Statistical rigor | Dashboard | Best for |
| --- | --- | --- | --- |
| **benchdiff** | High (uses benchstat) | No | Local dev + CI PR comparisons |
| **cob** | Low (single comparison) | No | Quick CI gate, simple setup |
| **gobenchdata** | Medium (configurable checks) | Yes (Vue.js on gh-pages) | Long-term trend tracking |
| **benchstat** (raw) | High | No (CSV export) | Maximum control, custom workflows |

## Noisy Neighbor Mitigation

Cloud CI environments share hardware with other jobs. Expect 5-10% variance even on quiet machines.

### Why CI benchmarks are noisy

- **Shared CPU/memory** — other CI jobs compete for resources
- **Thermal throttling** — sustained load reduces clock speed
- **Different hardware across runs** — CI runners may have different specs
- **Kernel scheduling** — context switches add unpredictable latency
- **Disk I/O contention** — shared storage affects I/O-bound benchmarks

### Strategies

**Statistical rigor** — run enough repetitions to estimate this workload's variance and compare with `benchstat`. A single run cannot estimate noise; a p-value does not remove all false positives or replace workload review.

**Relative comparison in same job** — run both base and head benchmarks in the same CI job on the same machine, rather than comparing against historical absolute values. This cancels out machine-to-machine variation. Tools like `benchdiff` do this automatically by checking out both git refs.

**Dedicated benchmark runners** — for critical path benchmarks, use self-hosted CI runners with no other workloads. This eliminates noisy neighbors entirely but costs more infrastructure.

**Calibrated thresholds** — shared runners usually need a wider gate than dedicated runners, but the value depends on the benchmark, runner fleet, and user-visible budget. Measure baseline variance, choose an explicit false-positive/false-negative trade-off, and review the gate when hardware or toolchains change.

**Never "retry until pass"** — rerunning benchmarks until they pass introduces selection bias. If a benchmark is flaky, fix the noise source (more iterations, dedicated runner, wider threshold) rather than retrying.

## System Tuning for Self-Hosted Runners

> **WARNING: These commands modify kernel and CPU settings. Apply them ONLY on dedicated CI runners, NEVER on developer machines or shared servers.**

When you control the CI hardware, these settings dramatically reduce benchmark variance by eliminating the main sources of non-determinism.

### Disable CPU frequency scaling

Variable CPU frequency makes benchmark times meaningless — the same code runs at different speeds depending on load and thermals:

```bash
# Set all CPUs to "performance" governor (fixed maximum frequency)
echo performance | sudo tee /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor
```

### Disable Turbo Boost

Turbo Boost temporarily increases clock speed but throttles under sustained load, creating variance between the start and end of a benchmark run:

```bash
# Intel
echo 1 | sudo tee /sys/devices/system/cpu/intel_pstate/no_turbo

# AMD
echo 0 | sudo tee /sys/devices/system/cpu/cpufreq/boost
```

### Pin benchmarks to specific CPU cores

Prevents the OS from migrating the benchmark process across cores, which causes cache thrashing (L1/L2 caches are per-core):

```bash
# Pin to cores 2 and 3 (leave cores 0-1 for OS and other processes)
taskset -c 2,3 go test -bench=. -count=10 ./...
```

### Disable SMT (Hyper-Threading)

SMT shares execution units between logical cores on the same physical core, causing unpredictable contention:

```bash
# Disable SMT system-wide
echo off | sudo tee /sys/devices/system/cpu/smt/control

# Or disable individual sibling cores (check /sys/devices/system/cpu/cpu*/topology/thread_siblings_list)
echo 0 | sudo tee /sys/devices/system/cpu/cpu1/online  # if cpu0 and cpu1 are siblings
```

### Combined CI setup script

```bash
#!/bin/bash
# benchmark-setup.sh — run on self-hosted CI runner before benchmarks
set -euo pipefail

echo "=== Configuring CPU for stable benchmarks ==="
echo performance | sudo tee /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor
echo 1 | sudo tee /sys/devices/system/cpu/intel_pstate/no_turbo 2>/dev/null || true
echo off | sudo tee /sys/devices/system/cpu/smt/control 2>/dev/null || true

echo "=== Running benchmarks on isolated cores ==="
taskset -c 2,3 go test -bench=. -benchmem -count=10 ./... | tee bench.txt
```
