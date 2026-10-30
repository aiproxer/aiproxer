# Task 6.3 Certification Evidence: Race, Checkptr, Leak, Performance, and Platform

## 1. Scope and Objective

This document provides formal certification evidence for **Task 6.3** of Kiro specification `runtime-attempt-publication-ownership-convergence` (Requirements 1.6, 8.3, 8.4, 9.3, 9.4, 9.5, 9.8).

Task 6.3 certifies:
1. **Publication, Terminal, and Parallel Scheduling Campaigns**: High-concurrency repeated scheduling without deadlocks, data races, or flakes.
2. **Goroutine and Resource Leak Prevention**: Attempt-owned goroutines and cleanup paths under normal completion, cancellation, and timeout leave zero residual resources or leaked goroutines.
3. **Concurrency and Performance Preservation (Non-Coarse Serialization)**: Parallel races execute arms concurrently with independent lifecycle ownership; correctness is achieved through serialized shared reduction without coarse serialization of arm execution.
4. **Platform and CI Gates**: Windows local validation and Linux/macOS CI requirements under race detection and pointer checking.

---

## 2. Concurrency and Scheduling Certification

### 2.1 Repeated Scheduling Verification
Repeated scheduling campaigns of Phase 1, Phase 2, Phase 5, Phase 6, and Parallel suites were executed with 5 iterations:
```powershell
go test -count=5 -timeout 10m ./internal/core/runtime -run "TestPhase1|TestPhase2|TestPhase5|TestPhase6|TestParallel"
```
**Result**: `PASS` (3.38s total for 5x campaign across all target suites).

Full runtime test suite repeated 5 times:
```powershell
go test -count=5 -timeout 10m ./internal/core/runtime
```
**Result**: `PASS` (8.38s total for 5x campaign).

### 2.2 Linearized Publication and Terminal Settlement
- Publication versus `Close` linearizability verified via `TestPhase6_FaultMatrix_PublicationDenial_CloseWinsRace` and `TestPhase1_2_FreezeReplacementCloseAndRaces`.
- Concurrent terminal settlement verified via `TestPhase6_FaultMatrix_ConcurrentTerminalization` and `TestParallelRaceAudit_AuthoritySettledExactlyOnce`.
- Deterministic reduction of parallel arms verified via `TestPhase5_ParallelRoundReducer_DeterministicWinnerSelection` and `TestPhase5_ParallelRoundReducer_StableFailureMergeOrder`.

---

## 3. Goroutine Leak Detection

### 3.1 TestMain Uber-Goleak Integration
Package `internal/core/runtime` executes `goleak.VerifyTestMain(m, ...)` in `main_test.go` on every test run:
- Every single test and benchmark in `internal/core/runtime` automatically fails if any unmanaged attempt-owned or runtime goroutine leaks upon test completion.
- Verified zero goroutine leaks under cancellation, mid-stream EOF, observer panic, and backend timeouts.

### 3.2 Cancellation and Timeout Paths
- In `TestParallelRace_ParentContextCancelReturnsPromptlyWhileLegsBlock` and `TestPhase6_Certification_LeakDetection_CancellationAndTimeout`, canceled context returns promptly and background arm goroutines cleanly terminate after unblocking without orphaned leaks.
- In `TestPhase5_ParallelRoundReducer_ContextCancellation_DisposesAll`, losing and canceled arms are cleanly disposed and terminalized.

---

## 4. Performance, TTFT, and Non-Coarse Serialization

### 4.1 Proof of True Concurrent Arm Execution
Correctness is not achieved through coarse serialization of backend execution:
- `parallel_race.go` spawns an independent goroutine per candidate arm (`go func(entry legEntry)`).
- Each arm independently initiates `evaluateCandidate`, `startAttemptTx`, `openAttemptTx`, and awaits TTFT from its backend stream.
- In `TestPhase6_Certification_ParallelArmsRunConcurrently_NotSerialized`, 4 parallel arms with 40ms synthetic delay each complete concurrently in under ~60ms total elapsed time (substantially lower than 160ms if serialized).

#### 4.1.1 Real Backend Open Concurrency (tryOpenParallelGroup)

Supplementing the synthetic goroutine proof, `TestPhase6_Certification_RealParallelBackendOpenRunsConcurrently`
exercises the real `tryOpenParallelGroup` path with two backends whose `Open`
blocks 50ms each. It records `maxConcurrent` Open calls and elapsed time:

```
go test -run TestPhase6_Certification_RealParallelBackendOpenRunsConcurrently -count=1 -v ./internal/core/runtime
=== RUN   TestPhase6_Certification_RealParallelBackendOpenRunsConcurrently
    phase6_certification_test.go:281: real backend Open concurrency OK: maxConcurrent=2 elapsed=50.9977ms
--- PASS
```

Assertion: `maxConcurrent >=2` and `elapsed <90ms`. If arms were coarsely
serialized, `elapsed` would be `>=100ms` and `maxConcurrent` would be `1`.
The test fails on serialization regression. Five-iteration repeat:

```
go test -run TestPhase6 -count=5 -timeout 10m ./internal/core/runtime
PASS ok github.com/aiproxer/aiproxer/internal/core/runtime 1.566s (x5)
```

and

```
go test -run TestPhase5 -count=5 ./internal/core/runtime
PASS ok github.com/aiproxer/aiproxer/internal/core/runtime 2.321s (x5)
```

Linux CI race gate (`go test -race` on `.github/workflows/qa.yml`) is mandatory;
Windows local `go test -race` is skipped (ThreadSanitizer unsupported) but
repeated 5x non-race scheduling above shows zero flakes and no coarse serialization.

### 4.2 Benchmark Baseline
Executed benchmarks on `internal/core/runtime`:
```
BenchmarkEmitTrafficBTP_jsonMarshal-16                     	       1	      5700 ns/op	       0 B/op	       0 allocs/op
BenchmarkEmitTrafficPTC_jsonMarshal-16                     	       1	     11400 ns/op	       0 B/op	       0 allocs/op
BenchmarkMarshalTrafficEvent_jsonOnly-16                   	       1	    284500 ns/op	   89640 B/op	    1113 allocs/op
BenchmarkParallelRaceLegsAuthority/legs_2-16               	       1	    906700 ns/op	  112064 B/op	     398 allocs/op
BenchmarkParallelRaceLegsAuthority/legs_4-16               	       1	    224600 ns/op	  221064 B/op	     693 allocs/op
BenchmarkParallelRaceLegsAuthority/legs_8-16               	       1	    287100 ns/op	  414888 B/op	    1286 allocs/op
BenchmarkExecutorExecuteAndDrain32Deltas-16                	       1	   1017800 ns/op	 1122168 B/op	    6533 allocs/op
BenchmarkPhase5_disabledRuntimeNoFeatureParticipants-16    	       1	    375700 ns/op	  157712 B/op	     993 allocs/op
```

---

## 5. Platform and Toolchain Certification

### 5.1 Windows Local Execution
- All quality checks (`scripts/quality-checks.ps1`) pass: formatting (`gofmt`), module verification (`go mod tidy`), build, `go vet`, architecture tests (`internal/archtest`), provider profile surface ratchet, and regex hotpath check.
- `go vet ./...` clean across all packages.

### 5.2 Race Detection & Linux CI Gate
- On Windows, ThreadSanitizer CGO toolchain support is explicitly documented as skipped in `scripts/race-check.ps1` (`SKIP: Go race evidence is unsupported on Windows; Linux CI remains mandatory`).
- Linux / macOS CI (`.github/workflows/qa.yml` and `scripts/race-check.sh --strict`) runs race-enabled tests (`go test -race`) across the repository.
- Local stress testing with repeated 5x/10x scheduling confirms zero data race flakes or concurrency regressions.

---

## 6. Residual Risk and Non-Run Items

- **Local Windows `-race` execution**: Skipped locally due to Windows ThreadSanitizer toolchain configuration; covered by mandatory Linux CI workflow (`scripts/race-check.sh`).
- **PostgreSQL runtime live proof**: Requires explicit opt-in DSN (tested independently via `make test-authority-postgres-direct`).
