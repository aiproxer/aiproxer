# BetterLeaks secret-guard benchmark report

## Scope and acceptance

This report records task 8.3 for requirements **8.8** and **10.6**, covering the approved design sections **BetterLeaks Scanner Builder** and **Performance and Scalability**. The only implementation file is `internal/plugins/features/secretguard/benchmark_test.go`; it measures the feature-private `scanCall` path with the scanner/generation and exact matcher built outside timed loops. This task changed benchmark coverage and validation only. It did not change production defaults, detection behavior, scan limits, finding limits, or worker policy.

The corpus covers exact-only, BetterLeaks-only, and hybrid modes; text and valid JSON; 1 KiB, 10 KiB, 100 KiB, 1 MiB, and 2 MiB; no-hit and positive cases; serial and `RunParallel` execution. It also includes successful generic-detector and AWS multipart positives. The 257-message generic-heavy fixture is an explicit **bounded-failure** bucket: it admits 129,280 bytes, projects the default 256-finding cap, and is excluded from scan-throughput interpretation.

The benchmark canary requires: `errors.Is(err, errBetterLeaksFindingCap)` for bounded failures; exactly 256 projected findings; admitted bytes equal to 129,280; no scan byte-limit hit; zero findings for every no-hit case; positive findings for every positive case; and no `ScanLimitHit` in throughput cases. `TestBetterLeaksBenchmarkNegativeControls` removes a positive detector and removes the cap stimulus, and confirms that both invalid corpus shapes are rejected. Benchmark loops ignore only the validated finding-cap sentinel, never arbitrary errors.

## Environment and policy

| Fact | Value |
| --- | --- |
| Platform | Windows/amd64 |
| CPU | AMD Ryzen 7 5800X 8-Core Processor |
| Go | go1.26.6 |
| Benchmark GOMAXPROCS | 16 (`-16` suffix) |
| BetterLeaks | `github.com/betterleaks/betterleaks/v2@v2.0.0-rc.1` |
| Confidence | medium (`DefaultBetterLeaksConfidence`) |
| Decode depth | 1 (`DefaultBetterLeaksDecodeDepth`) |
| Scanner workers | 4 (`min(4, GOMAXPROCS)`) |
| Finding cap | 256 (`DefaultBetterLeaksMaxFindings`) |
| Request scan cap | 2 MiB |

All credentials are repository synthetic fixtures. Neither benchmark output nor failure messages print raw payloads or finding values.

## Standard hot-path measurements

The exact commands were run with `-benchtime=3x`; every row is actual `ns/op / B/op / allocs/op`. The serial and full `RunParallel` tables contain the same 66 cases, including the cap bucket and the two successful realistic fixtures. `bounded-failure` is a cap/error measurement, not throughput.

| Bucket | Case | Serial | RunParallel |
| --- | --- | ---: | ---: |
| throughput | `exact-only/text/no-hit/1024/throughput-16` | 11367 / 3253 / 10 | 28933 / 6296 / 27 |
| throughput | `betterleaks-only/text/no-hit/1024/throughput-16` | 38367 / 21061 / 31 | 36833 / 21298 / 44 |
| throughput | `hybrid/text/no-hit/1024/throughput-16` | 37567 / 20848 / 30 | 23633 / 30429 / 51 |
| throughput | `exact-only/text/positive/1024/throughput-16` | 22300 / 4517 / 22 | 25467 / 6632 / 36 |
| throughput | `betterleaks-only/text/positive/1024/throughput-16` | 440967 / 26549 / 144 | 174033 / 109069 / 222 |
| throughput | `hybrid/text/positive/1024/throughput-16` | 406800 / 27573 / 151 | 157133 / 111253 / 232 |
| throughput | `exact-only/json/no-hit/1024/throughput-16` | 25100 / 6242 / 24 | 17633 / 6960 / 40 |
| throughput | `betterleaks-only/json/no-hit/1024/throughput-16` | 47967 / 16968 / 43 | 46433 / 17413 / 55 |
| throughput | `hybrid/json/no-hit/1024/throughput-16` | 51267 / 24322 / 48 | 46133 / 37696 / 64 |
| throughput | `exact-only/json/positive/1024/throughput-16` | 88967 / 44013 / 120 | 57567 / 44640 / 134 |
| throughput | `betterleaks-only/json/positive/1024/throughput-16` | 451500 / 57880 / 186 | 199367 / 113370 / 246 |
| throughput | `hybrid/json/positive/1024/throughput-16` | 840067 / 68010 / 249 | 188200 / 150656 / 331 |
| throughput | `exact-only/text/no-hit/10240/throughput-16` | 57333 / 21594 / 8 | 34767 / 23661 / 26 |
| throughput | `betterleaks-only/text/no-hit/10240/throughput-16` | 104900 / 48352 / 30 | 52067 / 62045 / 47 |
| throughput | `hybrid/text/no-hit/10240/throughput-16` | 118767 / 48890 / 32 | 692667 / 48941 / 42 |
| throughput | `exact-only/text/positive/10240/throughput-16` | 102433 / 22858 / 20 | 59000 / 25405 / 39 |
| throughput | `betterleaks-only/text/positive/10240/throughput-16` | 3056200 / 54773 / 144 | 1264833 / 118709 / 219 |
| throughput | `hybrid/text/positive/10240/throughput-16` | 5235833 / 55797 / 151 | 1431933 / 118418 / 212 |
| throughput | `exact-only/json/no-hit/10240/throughput-16` | 173633 / 53346 / 27 | 123367 / 55962 / 45 |
| throughput | `betterleaks-only/json/no-hit/10240/throughput-16` | 229133 / 79912 / 48 | 81567 / 94357 / 67 |
| throughput | `hybrid/json/no-hit/10240/throughput-16` | 266167 / 73624 / 47 | 114133 / 87354 / 64 |
| throughput | `exact-only/json/positive/10240/throughput-16` | 685467 / 488978 / 131 | 283633 / 489605 / 146 |
| throughput | `betterleaks-only/json/positive/10240/throughput-16` | 3614667 / 123992 / 189 | 1394133 / 166965 / 247 |
| throughput | `hybrid/json/positive/10240/throughput-16` | 4088500 / 559474 / 285 | 3715933 / 587325 / 320 |
| throughput | `exact-only/text/no-hit/102400/throughput-16` | 450700 / 214197 / 10 | 159067 / 214733 / 23 |
| throughput | `betterleaks-only/text/no-hit/102400/throughput-16` | 709800 / 337120 / 30 | 269433 / 350813 / 47 |
| throughput | `hybrid/text/no-hit/102400/throughput-16` | 1017033 / 330640 / 28 | 379067 / 350866 / 46 |
| throughput | `exact-only/text/positive/102400/throughput-16` | 884933 / 215461 / 22 | 307300 / 216088 / 36 |
| throughput | `betterleaks-only/text/positive/102400/throughput-16` | 37640200 / 374357 / 150 | 24366533 / 431930 / 221 |
| throughput | `hybrid/text/positive/102400/throughput-16` | 65408733 / 350453 / 151 | 17074400 / 433029 / 229 |
| throughput | `exact-only/json/no-hit/102400/throughput-16` | 1285967 / 475325 / 32 | 808267 / 475861 / 45 |
| throughput | `betterleaks-only/json/no-hit/102400/throughput-16` | 1919200 / 591432 / 49 | 668833 / 611749 / 68 |
| throughput | `hybrid/json/no-hit/102400/throughput-16` | 2882367 / 598786 / 54 | 782833 / 612085 / 69 |
| throughput | `exact-only/json/positive/102400/throughput-16` | 7509267 / 5269010 / 146 | 2699867 / 5269728 / 162 |
| throughput | `betterleaks-only/json/positive/102400/throughput-16` | 41363733 / 772122 / 217 | 13472667 / 799621 / 252 |
| throughput | `hybrid/json/positive/102400/throughput-16` | 40848867 / 5578485 / 329 | 14602900 / 5578184 / 338 |
| throughput | `exact-only/text/no-hit/1048576/throughput-16` | 4601867 / 2098357 / 10 | 3157067 / 2099021 / 25 |
| throughput | `betterleaks-only/text/no-hit/1048576/throughput-16` | 8570500 / 3169984 / 33 | 2510567 / 3177165 / 48 |
| throughput | `hybrid/text/no-hit/1048576/throughput-16` | 17754633 / 3163509 / 31 | 4024067 / 3177234 / 47 |
| throughput | `exact-only/text/positive/1048576/throughput-16` | 9456100 / 2099621 / 22 | 6393100 / 2100248 / 36 |
| throughput | `betterleaks-only/text/positive/1048576/throughput-16` | 406199100 / 3274477 / 169 | 122096933 / 3310816 / 222 |
| throughput | `hybrid/text/positive/1048576/throughput-16` | 340140367 / 3275498 / 176 | 131961300 / 3318464 / 231 |
| throughput | `exact-only/json/no-hit/1048576/throughput-16` | 17624700 / 6291645 / 36 | 7926433 / 6295333 / 54 |
| throughput | `betterleaks-only/json/no-hit/1048576/throughput-16` | 21759733 / 7356456 / 55 | 7603867 / 7372986 / 79 |
| throughput | `hybrid/json/no-hit/1048576/throughput-16` | 22778900 / 7356792 / 56 | 11082767 / 7364064 / 73 |
| throughput | `exact-only/json/positive/1048576/throughput-16` | 71831567 / 56364253 / 169 | 34852933 / 56363248 / 180 |
| throughput | `betterleaks-only/json/positive/1048576/throughput-16` | 374848067 / 8503773 / 196 | 124699267 / 8543986 / 233 |
| throughput | `hybrid/json/positive/1048576/throughput-16` | 449339567 / 58635661 / 370 | 180017300 / 58632349 / 381 |
| throughput | `exact-only/text/no-hit/2097152/throughput-16` | 8533933 / 4195509 / 10 | 4793200 / 4198536 / 29 |
| throughput | `betterleaks-only/text/no-hit/2097152/throughput-16` | 14647700 / 6309088 / 30 | 5043000 / 6323496 / 49 |
| throughput | `hybrid/text/no-hit/2097152/throughput-16` | 22912733 / 6309232 / 30 | 11086767 / 6323733 / 48 |
| throughput | `exact-only/text/positive/2097152/throughput-16` | 18193000 / 4196682 / 20 | 6829800 / 4197309 / 35 |
| throughput | `betterleaks-only/text/positive/2097152/throughput-16` | 690134400 / 6513221 / 192 | 247623667 / 6531000 / 226 |
| throughput | `hybrid/text/positive/2097152/throughput-16` | 689514167 / 6499554 / 179 | 283696733 / 6531976 / 232 |
| throughput | `exact-only/json/no-hit/2097152/throughput-16` | 40037800 / 12583962 / 39 | 17372367 / 12583728 / 51 |
| throughput | `betterleaks-only/json/no-hit/2097152/throughput-16` | 40739600 / 14698032 / 58 | 18315567 / 14710368 / 75 |
| throughput | `hybrid/json/no-hit/2097152/throughput-16` | 55602200 / 14705024 / 61 | 21593600 / 14710517 / 74 |
| throughput | `exact-only/json/positive/2097152/throughput-16` | 154678933 / 131976402 / 174 | 80271033 / 131975477 / 187 |
| throughput | `betterleaks-only/json/positive/2097152/throughput-16` | 760045333 / 17000930 / 226 | 284019133 / 17017112 / 263 |
| throughput | `hybrid/json/positive/2097152/throughput-16` | 864500333 / 136411069 / 380 | 362333733 / 136394874 / 386 |
| bounded-failure | `betterleaks-only-generic-heavy-cap-bounded/text/no-hit/129280/bounded-failure-16` | 112816667 / 18779882 / 118104 | 54685067 / 19100162 / 118515 |
| bounded-failure | `hybrid-generic-heavy-cap-bounded/text/no-hit/129280/bounded-failure-16` | 117771367 / 18896258 / 118220 | 48123300 / 19132952 / 118626 |
| realistic-hit | `betterleaks-only-generic-hit/text/positive/50/throughput-16` | 512600 / 172250 / 627 | 209167 / 253136 / 763 |
| realistic-hit | `hybrid-generic-hit/text/positive/50/throughput-16` | 481000 / 172394 / 627 | 222500 / 272442 / 770 |
| realistic-hit | `betterleaks-only-aws-multipart-hit/text/positive/101/throughput-16` | 170600 / 29698 / 223 | 99600 / 68277 / 245 |
| realistic-hit | `hybrid-aws-multipart-hit/text/positive/101/throughput-16` | 207633 / 48909 / 228 | 109967 / 87370 / 249 |

## Steady parallel measurement

`BenchmarkBetterLeaksHotPathParallelSteady` was run with `-benchtime=1s`. Each row below completed a calibrated steady load for at least one second; `N` is Go's completed operation count. The complete steady run covered 40 non-cap cases. These representative rows show every detector mode, text/JSON, no-hit/positive, 1 KiB/100 KiB/2 MiB, plus both realistic fixtures.

| Case | N | ns/op / B/op / allocs/op |
| --- | ---: | ---: |
| `betterleaks-only/text/no-hit/1024/throughput-16` | 157638 | 6394 / 12734 / 29 |
| `hybrid/text/no-hit/1024/throughput-16` | 180037 | 7460 / 12868 / 29 |
| `betterleaks-only/json/no-hit/1024/throughput-16` | 149145 | 8928 / 15568 / 43 |
| `hybrid/json/no-hit/1024/throughput-16` | 140666 | 9803 / 15897 / 44 |
| `betterleaks-only/text/no-hit/102400/throughput-16` | 5638 | 232783 / 330082 / 30 |
| `hybrid/text/no-hit/102400/throughput-16` | 4278 | 254318 / 330528 / 30 |
| `betterleaks-only/text/positive/102400/throughput-16` | 115 | 9027323 / 353558 / 150 |
| `hybrid/text/positive/102400/throughput-16` | 132 | 9345845 / 354219 / 157 |
| `betterleaks-only/json/no-hit/102400/throughput-16` | 3252 | 367900 / 592214 / 50 |
| `hybrid/json/no-hit/102400/throughput-16` | 2424 | 444523 / 592554 / 50 |
| `betterleaks-only/json/positive/102400/throughput-16` | 123 | 8994832 / 727481 / 177 |
| `hybrid/json/positive/102400/throughput-16` | 100 | 10794693 / 5547686 / 299 |
| `betterleaks-only/text/no-hit/2097152/throughput-16` | 273 | 3829158 / 6304168 / 32 |
| `hybrid/text/no-hit/2097152/throughput-16` | 292 | 4186067 / 6304012 / 32 |
| `betterleaks-only/text/positive/2097152/throughput-16` | 6 | 269229050 / 6507488 / 205 |
| `hybrid/text/positive/2097152/throughput-16` | 6 | 244609900 / 6501097 / 205 |
| `betterleaks-only/json/no-hit/2097152/throughput-16` | 175 | 5997384 / 14696711 / 58 |
| `hybrid/json/no-hit/2097152/throughput-16` | 162 | 7170165 / 14698504 / 60 |
| `betterleaks-only/json/positive/2097152/throughput-16` | 5 | 280398440 / 16993966 / 240 |
| `hybrid/json/positive/2097152/throughput-16` | 4 | 260901125 / 136407352 / 383 |
| `betterleaks-only-generic-hit/text/positive/50/throughput-16` | 6670 | 180929 / 122767 / 567 |
| `hybrid-generic-hit/text/positive/50/throughput-16` | 7406 | 181523 / 123601 / 568 |
| `betterleaks-only-aws-multipart-hit/text/positive/101/throughput-16` | 23260 | 50896 / 27973 / 224 |
| `hybrid-aws-multipart-hit/text/positive/101/throughput-16` | 22976 | 46041 / 28084 / 224 |

The full scheduler-sensitive `RunParallel -benchtime=3x` matrix remains useful for shape coverage, while the one-second run is the evidence used for steady-load interpretation. The concurrent goroutine monitor below samples during this same class of shared scans.

## Latency tails

The opt-in wall-clock harness ran `SECRETGUARD_BENCH_PERCENTILES=1 SECRETGUARD_BENCH_SAMPLES=100 go test -v -run '^TestBetterLeaksLatencyPercentiles$'`. Samples were collected for representative 1 KiB and 100 KiB cases, with 8 repetitions per 1 KiB sample and 2 per 100 KiB sample. Values use nearest-rank quantiles: sorted sample index `ceil(n*p)-1`, clamped to the sample range. These are bounded local timing observations, not an SLO claim.

| Detector | Kind | Size | Hit | Samples | Repetitions | p50 | p95 | p99 |
| --- | --- | ---: | --- | ---: | ---: | ---: | ---: | ---: |
| exact-only | text | 1 KiB | no | 100 | 8 | 0s | 64.95us | 260.25us |
| betterleaks-only | text | 1 KiB | no | 100 | 8 | 0s | 66.337us | 69.55us |
| hybrid | text | 1 KiB | no | 100 | 8 | 0s | 125.012us | 125.175us |
| exact-only | text | 1 KiB | yes | 100 | 8 | 0s | 125.012us | 125.2us |
| betterleaks-only | text | 1 KiB | yes | 100 | 8 | 396.1us | 707.987us | 1.124737ms |
| hybrid | text | 1 KiB | yes | 100 | 8 | 399.037us | 873.95us | 1.124925ms |
| exact-only | JSON | 1 KiB | no | 100 | 8 | 0s | 125.262us | 188.9us |
| betterleaks-only | JSON | 1 KiB | no | 100 | 8 | 0s | 125.712us | 140.637us |
| hybrid | JSON | 1 KiB | no | 100 | 8 | 0s | 125.237us | 315.012us |
| exact-only | JSON | 1 KiB | yes | 100 | 8 | 124.8us | 129.925us | 250us |
| betterleaks-only | JSON | 1 KiB | yes | 100 | 8 | 455.462us | 656.887us | 996.687us |
| hybrid | JSON | 1 KiB | yes | 100 | 8 | 500.137us | 907.1us | 1.125462ms |
| exact-only | text | 100 KiB | no | 100 | 2 | 500us | 1.04275ms | 2.50065ms |
| betterleaks-only | text | 100 KiB | no | 100 | 2 | 500.8us | 1.0017ms | 1.56405ms |
| hybrid | text | 100 KiB | no | 100 | 2 | 1.0406ms | 4.8651ms | 6.49925ms |
| exact-only | text | 100 KiB | yes | 100 | 2 | 999.85us | 3.14915ms | 6.1766ms |
| betterleaks-only | text | 100 KiB | yes | 100 | 2 | 35.258ms | 44.0497ms | 48.8461ms |
| hybrid | text | 100 KiB | yes | 100 | 2 | 34.7602ms | 38.43335ms | 40.8381ms |
| exact-only | JSON | 100 KiB | no | 100 | 2 | 1.6701ms | 2.63595ms | 3.00175ms |
| betterleaks-only | JSON | 100 KiB | no | 100 | 2 | 1.8865ms | 3.05395ms | 4.1172ms |
| hybrid | JSON | 100 KiB | no | 100 | 2 | 2.49895ms | 3.78615ms | 5.5683ms |
| exact-only | JSON | 100 KiB | yes | 100 | 2 | 7.57105ms | 12.00125ms | 12.56265ms |
| betterleaks-only | JSON | 100 KiB | yes | 100 | 2 | 35.4632ms | 41.47695ms | 46.5926ms |
| hybrid | JSON | 100 KiB | yes | 100 | 2 | 45.6729ms | 52.58375ms | 54.8877ms |

## Scanner construction and goroutine observations

`BenchmarkBetterLeaksScannerBuild -benchtime=3x` measured **166,497,533 ns/op, 49,353,810 B/op, 395,135 allocs/op**. This is scanner construction/precompile cost and is separate from hot-path scan rows.

`BenchmarkBetterLeaksGoroutineRetainedCount -benchtime=1s` reported **2 before, 2 after** while repeatedly scanning a representative 100 KiB BetterLeaks-only text no-hit call. `BenchmarkBetterLeaksGoroutineConcurrentBound -benchtime=1s` monitored `runtime.NumGoroutine()` during shared `RunParallel` scans and reported **2 before, 40 peak during**, with **247,894 ns/op, 330,591 B/op, 30 allocs/op**. The monitor owns its stop channel and waits for completion before reporting; the 40 count includes concurrent benchmark workers and the monitor, so it is a measured process peak rather than a claim about detector goroutines alone.

## Baseline and artifact comparison

The pre-BetterLeaks baseline is merge-base **`e719bb5f042e548296f9cf36950bb261d4354d83`**. The candidate benchmark commit at measurement time is **`b2084b41`**. Baseline source and overlay are reproducible from tracked [`benchmark-baseline.ps1`](benchmark-baseline.ps1) and [`benchmark-baseline_test.go.txt`](benchmark-baseline_test.go.txt).

The baseline script performs these exact steps: archive the baseline ref with `git -C <candidate-worktree> archive --format=tar e719bb5f042e548296f9cf36950bb261d4354d83`, extract into a new temporary directory, copy the tracked overlay to `internal/plugins/features/secretguard/baseline_bench_test.go`, run `go test -run '^$' -bench '^BenchmarkSecretGuardBaselineExact$' -benchmem -benchtime=1x ./internal/plugins/features/secretguard`, then run `go build -o <temp>\lipstd-baseline.exe ./cmd/lipstd`. The candidate used the matching plain `go build -o <temp>\lip-betterleaks-lipstd-after-20261003.exe ./cmd/lipstd`, with no special build flags and no benchmark environment overrides. The script run produced baseline `lipstd-baseline.exe` size **124,973,056 bytes**.

Both release builds used plain `go build` with no `-tags`, `-trimpath`, or `-ldflags`; `GOFLAGS` and build-tag environment overrides were unset, `CGO_ENABLED=1`, `GOOS=windows`, and `GOARCH=amd64`. The baseline exact-only overlay uses the same payload shapes and sizes; its recorded `B/op / allocs/op` values were:

| Kind | Size | No-hit | Positive |
| --- | ---: | ---: | ---: |
| text | 1 KiB | 2,816 / 9 | 2,824 / 10 |
| text | 10 KiB | 2,816 / 9 | 2,824 / 10 |
| text | 100 KiB | 2,816 / 9 | 3,080 / 13 |
| text | 1 MiB | 2,816 / 9 | 3,080 / 13 |
| text | 2 MiB | 2,816 / 9 | 2,824 / 10 |
| JSON | 1 KiB | 6,664 / 23 | 7,760 / 37 |
| JSON | 10 KiB | 44,552 / 26 | 45,648 / 40 |
| JSON | 100 KiB | 370,440 / 32 | 371,280 / 43 |
| JSON | 1 MiB | 5,244,712 / 37 | 5,245,520 / 47 |
| JSON | 2 MiB | 10,487,912 / 40 | 10,488,416 / 48 |

The candidate release binary was **134,790,144 bytes**, a delta of **+9,817,088 bytes (+7.85%)** against that baseline build. `go list -deps ./cmd/lipstd` reported **1,037** baseline packages and **1,166** candidate packages (**+129, +12.44%**); BetterLeaks contributed **25** candidate dependency entries and none in baseline. The binary/dependency comparison is a release-build observation, not a benchmark-test binary comparison.

## Allocation regression and performance review trigger

The exact-only 2 MiB text no-hit baseline was **2,816 B/op, 9 allocs/op**. Candidate measurements were **4,195,509 B/op, 10 allocs/op** for exact-only, **6,309,088 B/op, 30 allocs/op** for BetterLeaks-only, and **6,309,232 B/op, 30 allocs/op** for hybrid. This is an explicit no-secret-allocation regression signal alongside positive-hit latency: baseline exact-only 2 MiB text positive was **9.795 ms**, versus candidate serial **18.193 ms** exact-only, **690.134 ms** BetterLeaks-only, and **689.514 ms** hybrid (the corresponding candidate B/op values were 4,196,682; 6,513,221; and 6,499,554).

This evidence is a maintainer/design performance-review trigger. Bounded root-cause hypotheses are request-owned logical-fragment copies in `walkLogicalFragments`, JSON decode/canonical-merge buffers, and BetterLeaks report/projection allocations. The next investigation should use allocation profiles and ownership tracing to separate those sources, then evaluate zero-copy or admission-owned buffers only where the security ownership and redaction contracts remain intact. This task intentionally does not tune production defaults or weaken confidence, decode depth, worker count, scan limits, or finding limits to hide the regression.

## Task 8.6 remediation measurements

The remediation keeps text fragments as immutable Go strings through admission, exact scanning, and BetterLeaks source delivery. A byte slice is materialized lazily only when occurrence spans or a rewrite require byte mapping; the same admitted byte slice is shared by BetterLeaks projection and exact occurrence mapping for a positive hybrid scan. JSON fragments retain their admitted raw bytes because canonical JSON occurrence mapping and mutation require byte offsets. The walker, source, and redaction regressions remained green after this change.

The comparable Windows/amd64 run used Go 1.26.6, the unchanged medium-confidence/decode-depth-1/worker-4 policy, and `-benchtime=3x`. Values in this allocation table are `B/op / allocs/op`; the earlier candidate values are retained above for comparison.

| Case | Before task 8.6 | After task 8.6 |
| --- | ---: | ---: |
| exact-only, text, 2 MiB, no-hit | 4,195,509 / 10 | 1,221 / 8 |
| BetterLeaks-only, text, 2 MiB, no-hit | 6,309,088 / 30 | 17,906 / 28 |
| hybrid, text, 2 MiB, no-hit | 6,309,232 / 30 | 18,210 / 29 |
| exact-only, text, 2 MiB, positive | 4,196,682 / 10 | 2,099,637 / 21 |
| BetterLeaks-only, text, 2 MiB, positive | 6,513,221 / 30 | 2,307,245 / 188 |
| hybrid, text, 2 MiB, positive | 6,499,554 / 30 | 2,280,453 / 172 |

The text no-hit allocation regression is removed: exact-only is below the 2,816 B/op pre-BetterLeaks baseline, and BetterLeaks-only/hybrid no-hit scans no longer duplicate a multi-megabyte text payload. Positive text scans still materialize approximately one 2 MiB byte representation for occurrence mapping; the profile shows that remaining cost alongside BetterLeaks report and matching work. The hybrid positive path now reuses that representation rather than allocating a second copy. JSON remains dominated by its required decode/mapping buffers (for example, the 2 MiB no-hit rows remain approximately 12.58 MiB exact-only and 14.70 MiB BetterLeaks-only), so this remediation does not claim a broad JSON allocation reduction.

Fresh allocation profiles are retained at `betterleaks-cert-20261003/task-8-6-final-exact-nohit-2m.mem.pprof` and `betterleaks-cert-20261003/task-8-6-final2-hybrid-positive-2m.mem.pprof`. The exact no-hit profile is setup-dominated after the change; the hybrid positive profile attributes the remaining request-sized allocation to `betterLeaksLogicalFragmentSource.betterLeaksRaw`. Its `regexp/syntax` allocations arise during scanner/config construction in benchmark setup and do not measure steady-request matching allocation cost. Independently repeated 2 MiB positive scans measured approximately 311–316 ms for BetterLeaks/hybrid. Large positive text occurrence mapping and JSON decode buffers remain a performance-review item. This evidence supports the scoped copy-remediation change while retaining the recommendation against a feature GO claim until those material positive/JSON costs receive a separate design review.

## Reproduction and verification

Candidate commands:

```powershell
go test -run '^TestBetterLeaksBenchmarkCorpus$|^TestBetterLeaksBenchmarkNegativeControls$' ./internal/plugins/features/secretguard
go test -run '^$' -bench '^BenchmarkBetterLeaksHotPath$' -benchmem -benchtime=3x ./internal/plugins/features/secretguard
go test -run '^$' -bench '^BenchmarkBetterLeaksHotPathParallel$' -benchmem -benchtime=3x ./internal/plugins/features/secretguard
go test -run '^$' -bench '^BenchmarkBetterLeaksHotPathParallelSteady$' -benchmem -benchtime=1s ./internal/plugins/features/secretguard
go test -run '^$' -bench '^BenchmarkBetterLeaksScannerBuild$' -benchmem -benchtime=3x ./internal/plugins/features/secretguard
go test -run '^$' -bench '^BenchmarkBetterLeaksGoroutine(RetainedCount|ConcurrentBound)$' -benchmem -benchtime=1s ./internal/plugins/features/secretguard
$env:SECRETGUARD_BENCH_PERCENTILES='1'; $env:SECRETGUARD_BENCH_SAMPLES='100'; go test -v -run '^TestBetterLeaksLatencyPercentiles$' ./internal/plugins/features/secretguard
go test ./internal/plugins/features/secretguard
go vet ./internal/plugins/features/secretguard
go build -o lip-betterleaks-lipstd-after-20261003.exe ./cmd/lipstd
```

Focused verification after the remediation passed:

- `go test -run '^TestBetterLeaksBenchmarkCorpus$|^TestBetterLeaksBenchmarkNegativeControls$' ./internal/plugins/features/secretguard` — PASS.
- Full serial and full `RunParallel` hot-path matrices — PASS, 66 rows each.
- One-second steady parallel matrix — PASS, 40 non-cap rows.
- Scanner build and retained/concurrent goroutine benchmarks — PASS.
- 100-sample percentile test — PASS (24 representative cases, 36.765s).
- `go test ./internal/plugins/features/secretguard` — PASS (22.210s).
- `go vet ./internal/plugins/features/secretguard` — PASS.
- Windows `-race` was not retried because the known local cgo tool failure occurs before tests; Linux race evidence for task 8.2 is inherited from parent commit `b2084b41`, with no new race claim here.

The full test output and normalized measurements above contain no raw synthetic secret payloads.

## Newline-dense review regression

The historical matrix above omits the single-fragment, near-cap newline topology raised in PR 726 review. See review-remediation.md for index-inclusive projection and complete scanner measurements over one 2 MiB fragment with 256 near-tail findings. The default-policy full scanner measured 54,477,314 B/op over three iterations with zero cap failures; projection alone is 34,816 B/op and must not be presented as including its roughly 8 MiB line-start index. The prior generic-heavy fixture remains a separate-fragment bounded-failure bucket.
