---
name: golang-benchmark
description: "Write and review Go benchmarks and profiling experiments: timed-path validity, b.Loop, benchstat, pprof, trace, compiler diagnostics, and regression evidence."
---

# Go benchmarking and profiling

Use this skill when a performance claim needs evidence. Establish a representative workload, measure a baseline and a candidate under comparable conditions, and record enough repetitions to quantify noise. A profile identifies where time or memory goes; it does not by itself prove a fix. Apply `golang-performance` after a bottleneck is established.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Trace the timed loop: does it execute the intended operation, check errors, consume representative inputs, and avoid measuring an exhausted iterator or reused already-mutated fixture?
- Check setup exclusion, compiler elimination, parallel benchmark accounting, bytes/op, and allocation reporting. A benchmark of a failure fast path cannot support a success-path claim.
- Compare identical toolchains, flags, hardware, input distributions, and concurrency; retain raw repeated samples. Separate statistical evidence from production relevance.
- Follow profile samples to application callers and distinguish cumulative allocations from retained memory. Require correctness evidence alongside a claimed speedup.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## A small, valid benchmark

For Go 1.24 and newer, prefer `b.Loop()`:

```go
func BenchmarkParse(b *testing.B) {
	data := loadFixture("large.json")
	for b.Loop() {
		result := Parse(data)
		benchmarkSink = result // only needed when the result could be eliminated
	}
}
```

`b.Loop` manages the benchmark loop and excludes setup before the first iteration. Existing `b.N` loops remain valid; use a package-level sink or another observable result when the compiler could remove the work, and use `b.ResetTimer` only when setup must be excluded from a legacy loop. Keep fixtures, parsing options, and allocation behavior identical between variants.

Report allocations when they matter:

```go
func BenchmarkEncode(b *testing.B) {
	value := fixture()
	b.ReportAllocs()
	for b.Loop() { _ = Encode(value) }
}
```

Use sub-benchmarks for meaningful dimensions (`size=64`, `size=4096`), not for arbitrary cases. `ReportMetric` is useful for domain units such as bytes/sec; explain how the metric is calculated.

## Run and compare

```sh
go test -run='^$' -bench='BenchmarkEncode$' -benchmem -count=10 ./path/to/pkg > new.txt
benchstat old.txt new.txt
```

Useful controls:

| Option | Why |
| --- | --- |
| `-run='^$'` | Skip ordinary tests while collecting benchmarks. |
| `-benchmem` | Include bytes and allocations per operation. |
| `-count=N` | Repeats the benchmark; choose N from observed noise, commonly 5–20. |
| `-benchtime=3s` | Gives a noisy benchmark more samples per repetition. |
| `-cpu=1,2,4` | Tests scaling, but compare the same CPU list in both versions. |
| `-timeout` | Bounds a hung benchmark or fixture. |

Compile each version once when comparing command-level wall time or when build latency is part of the experiment. `go test`'s reported `ns/op` excludes compilation; precompilation is not needed to make benchmark timing valid. Run precompiled binaries with matching `-test.bench` and `-test.benchmem` flags if the experiment measures invocation latency.

Treat `benchstat` output as an estimate, not a verdict from a lower point estimate. `~` means no statistically significant difference was detected under the test; check p-values and intervals, then repeat with a larger deliberate sample or reduce noise. Do not rerun until a favorable result appears. Interleave old/new runs when thermal state, CPU frequency, background work, or scheduling can drift, and keep the machine and compiler constant. A statistically significant change may still be irrelevant to the production workload.

## Profiles

Capture only the profile needed to answer a question:

```sh
go test -run='^$' -bench='BenchmarkParse$' -cpuprofile=cpu.pprof ./parser
go tool pprof -top -cum cpu.pprof
go test -run='^$' -bench='BenchmarkParse$' -memprofile=heap.pprof ./parser
go tool pprof -alloc_objects heap.pprof
go test -run='^$' -bench='BenchmarkParse$' -trace=trace.out ./parser
go tool trace trace.out
```

`alloc_objects` helps find allocation rate and GC churn; `alloc_space` measures cumulative bytes allocated. `inuse_space` and `inuse_objects` describe live data at the snapshot and are useful for leak suspects, not proof of a leak. Establish comparable snapshots over time and verify retention with a controlled workload before calling something a leak. Use `top -cum`, `list`, and `peek` to move from runtime symptoms to application callers.

For a service, protect and authenticate pprof/trace endpoints, capture briefly, and avoid putting secrets or customer data in profile labels. See [pprof](references/pprof.md), [trace](references/trace.md), and [benchstat](references/benchstat.md).

## Compiler and runtime diagnostics

Use diagnostics to test a profile-backed hypothesis:

```sh
go test -run='^$' -bench='BenchmarkParse$' -gcflags='-m=2' ./parser
go tool compile -S file.go
```

Escape output is a compile-time explanation, not a promise that every escape is worth removing. Receiver choice, inlining, and layout are compiler/workload dependent; validate changes with the benchmark. Use `runtime/trace`, `runtime/metrics`, pprof, and the race detector for their respective questions. Check the installed Go release's documented `GODEBUG` settings before using a flag; undocumented or removed settings are not a diagnostic plan.

## Regression gates

For CI, keep benchmark jobs separate from correctness tests when their timing noise would make PRs unreliable. Pin the tool version, use a stable runner where possible, save raw results, and compare a fixed baseline. Set a threshold only after measuring normal variance, and report a suspected regression for review rather than silently accepting or rejecting based on one run. See [ci-regression](references/ci-regression.md) and [investigation-session](references/investigation-session.md).

## Navigation

- [benchstat](references/benchstat.md): comparison output, significance, and experiment design.
- [pprof](references/pprof.md): CPU, heap, goroutine, block, and mutex analysis.
- [trace](references/trace.md): scheduler, blocking, GC, and flight-recorder timelines.
- [compiler analysis](references/compiler-analysis.md): escape, inlining, SSA, and assembly.
- [diagnostic tools](references/tools.md): runtime and profiling utilities.
- [CI regressions](references/ci-regression.md): reproducible benchmark gates.
- [investigation sessions](references/investigation-session.md): production evidence and cost controls.
- [Prometheus Go metrics](references/prometheus-go-metrics.md): runtime metrics and query examples.

Related local skills: `golang-performance`, `golang-observability`, `golang-troubleshooting`, and `golang-testing`.
