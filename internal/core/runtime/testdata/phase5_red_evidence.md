# Phase 5 RED Evidence: Runtime Attempt Publication Ownership Convergence

This document records TDD RED -> GREEN evidence for tasks 5.1, 5.2, and 5.3.
Each section captures the failing test output before the ownership change and the passing output after.

## 5.1 — Make Each Parallel Arm Return an Immutable Outcome

**Invariant:** Workers must not mutate shared `recoveryController` state (`excluded`, `failures`, `budget`, `TTFT`, `[first]`, `interleaved`, `affinity`, `slot`). Each arm receives immutable copies (`frozenReqFacts`, `frozenRouteFacts`, `frozenInterleaved`, `sharedFailCopy`, `ttftCopy`) and returns `parallelArmOutcome`; the reducer owns all shared-progress mutation.

**RED command (before isolation):**
```powershell
go test ./internal/archtest -run shared_recovery_mutation -count=1 -v
```

**RED output (before `parallel_race.go` worker isolation):**
```
=== RUN   TestPhase1_AttemptBoundaryRatchets/shared_recovery_mutation_detected_red
    phase1_attempt_boundary_ratchets_test.go:368: Phase5.1: workers isolated: expected no shared recovery mutations in parallel worker closures but found:
        parallel_race.go:184: parallel worker goroutine mutates recovery state: req.progress.excluded[cand.Key] = struct{}{}
        parallel_race.go:195: parallel worker goroutine mutates recovery state: req.progress.failures.CapabilityReject = ...
--- FAIL: TestPhase1_AttemptBoundaryRatchets/shared_recovery_mutation_detected_red (0.02s)
FAIL
```

**GREEN command (after isolation):**
```powershell
go test ./internal/archtest -run shared_recovery_mutation -count=1 -v
```

**GREEN output (after):**
```
=== RUN   TestPhase1_AttemptBoundaryRatchets/shared_recovery_mutation_detected_red
    phase1_attempt_boundary_ratchets_test.go:369: Phase5.1: workers isolated: no shared recovery mutations found inside parallel worker goroutines
--- PASS: TestPhase1_AttemptBoundaryRatchets/shared_recovery_mutation_detected_red (0.02s)
PASS
ok      github.com/aiproxer/aiproxer/internal/archtest  1.48s
```

**Supporting runtime reducer proof:** `go test ./internal/core/runtime -run TestPhase5 -count=1` now PASS (see 5.2).

Implementation: `internal/core/runtime/parallel_race.go:138-158` snapshots `frozenReqFacts`, `frozenRouteFacts`, `frozenInterleaved`, `sharedFailCopy`, `ttftCopy` before the `go func(entry legEntry)` loop; each leg builds `legReq` / `legProgress` local copies and returns `parallelArmOutcome` with `hist` delta only. Mutation of `r.req.progress.excluded` / `sh` is confined to `parallelRoundReducer.Reduce` (stable entries order).

---

## 5.2 — Introduce One Parallel-Round Reducer for Shared Progress and Publication

**Invariant:** The coordinator (`parallelRoundReducer`) owns arm starts, handicap progression, attempt/TTFT budget application, failure merge, winner selection, and ready-attempt publication. Winner selection is deterministic on `arrival` sequence; all-failure deltas are merged in stable `entries` order; existing `FinalError` precedence (`CapabilityReject > TransportReject > AdmissionErr > ContextLimit > ParallelFailure`) is preserved. Every loser/late `readyAttempt` is terminalized exactly once via `Dispose`/`releaseLosers` through the common terminal operation.

**RED command (before reducer extraction):**
```powershell
go test ./internal/core/runtime -run TestPhase5_ParallelRoundReducer_StableFailureMergeOrder -count=1 -v
```

**RED output (before `parallelRoundReducer` owned the merge):**
```
=== RUN   TestPhase5_ParallelRoundReducer_StableFailureMergeOrder
    phase5_parallel_reducer_test.go:145: expected CapabilityReject to be recorded in failure history
    phase5_parallel_reducer_test.go:158: expected FinalError to prioritize CapabilityReject (*aipapi.RejectError), got parallel race arm failed: candidate "cand-2" failed before winner: stream err 2
--- FAIL: TestPhase5_ParallelRoundReducer_StableFailureMergeOrder (0.01s)
```

Cause: failure-history merging was interleaved with worker goroutines; arrival order `[3,1,2]` produced nondeterministic overwrite of `CapabilityReject` vs `ParallelFailure`, violating stable-entries-order and `FinalError` precedence.

**GREEN command (after):**
```powershell
go test ./internal/core/runtime -run TestPhase5_ParallelRoundReducer_StableFailureMergeOrder -count=1 -v
go test ./internal/core/runtime -run TestPhase5_ParallelRoundReducer -count=1 -v
```

**GREEN output (after):**
```
=== RUN   TestPhase5_ParallelRoundReducer_StableFailureMergeOrder
--- PASS: TestPhase5_ParallelRoundReducer_StableFailureMergeOrder (0.00s)
=== RUN   TestPhase5_ParallelRoundReducer_DeterministicWinnerSelection
--- PASS: TestPhase5_ParallelRoundReducer_DeterministicWinnerSelection (0.00s)
=== RUN   TestPhase5_ParallelRoundReducer_ContextCancellation_DisposesAll
--- PASS: TestPhase5_ParallelRoundReducer_ContextCancellation_DisposesAll (0.00s)
=== RUN   TestPhase5_ParallelRoundReducer_FatalError_WinnerSurvives
--- PASS: TestPhase5_ParallelRoundReducer_FatalError_WinnerSurvives (0.00s)
=== RUN   TestPhase5_ParallelRoundReducer_FatalError_NoWinner_AbortsRound
--- PASS: TestPhase5_ParallelRoundReducer_FatalError_NoWinner_AbortsRound (0.00s)
=== RUN   TestPhase5_ParallelRoundReducer_SingleAttemptRecordPerOpenedLeg
--- PASS: TestPhase5_ParallelRoundReducer_SingleAttemptRecordPerOpenedLeg (0.00s)
PASS
```

Implementation: `internal/core/runtime/parallel_race.go:419-718` — `newParallelRoundReducer` + `Reduce` steps 1-7 (winner selection by earliest `arrival`, stable `legs` construction, context/fatal handling, stable `entries` merge, winner `Consume` -> `commitMemoInjection` -> `persistInterleavedState`, loser terminalization). Verified by `phase5_parallel_reducer_test.go` including `SingleAttemptRecordPerOpenedLeg` (exactly-once `RecordAttempt` per opened B-leg).

---

## 5.3 — Commit Winner-Only State Only After Accepted Publication

**Invariant:** `pendingSelectionEffects` (interleaved memo `PendingMemoUpdate` + cycle) remain uncommitted data on `readyAttempt` until `Reduce` calls `ready.Consume()` successfully. Only the winner's `Consume` is followed by `commitMemoInjection`/`persistInterleavedState`; losing arms are `Dispose`-d without committing; all-failure, fatal, and `ctx.Done` paths never persist winner state. Denied publication (slot closed) disposes the ready without side effects.

**RED command (before Consume ordering):**
```powershell
go test ./internal/core/runtime -run TestPhase5_WinnerOnlyCommit_FatalErrorNeverPersists -count=1 -v
```

**RED output (before `Consume` before commit):**
```
=== RUN   TestPhase5_WinnerOnlyCommit_FatalErrorNeverPersists
    phase5_winner_only_commit_test.go:336: memo was mutated despite fatal error: got "failed memo", want "original memo"
--- FAIL: TestPhase5_WinnerOnlyCommit_FatalErrorNeverPersists (0.01s)
=== RUN   TestPhase5_WinnerOnlyCommit_AcceptedWinnerPersistsState
    phase5_winner_only_commit_test.go:149: persisted memo = "loser memo from arm B", want "winner memo from arm A"
--- FAIL: TestPhase5_WinnerOnlyCommit_AcceptedWinnerPersistsState (0.01s)
```

Cause: memo/cycle effects were committed before `Consume` validated single-use publication, so a fatal `ErrTTFTTimeout` with no winner and a loser that arrived earlier could still persist its `PendingMemoUpdate`; winning vs losing memo ordering was not guarded by publication acceptance.

**GREEN command (after):**
```powershell
go test ./internal/core/runtime -run TestPhase5_WinnerOnlyCommit -count=1 -v
```

**GREEN output (after):**
```
=== RUN   TestPhase5_WinnerOnlyCommit_AcceptedWinnerPersistsState
--- PASS: TestPhase5_WinnerOnlyCommit_AcceptedWinnerPersistsState (0.01s)
=== RUN   TestPhase5_WinnerOnlyCommit_AllFailureNeverPersists
--- PASS: TestPhase5_WinnerOnlyCommit_AllFailureNeverPersists (0.00s)
=== RUN   TestPhase5_WinnerOnlyCommit_FatalErrorNeverPersists
--- PASS: TestPhase5_WinnerOnlyCommit_FatalErrorNeverPersists (0.00s)
=== RUN   TestPhase5_WinnerOnlyCommit_ContextCanceledNeverPersists
--- PASS: TestPhase5_WinnerOnlyCommit_ContextCanceledNeverPersists (0.00s)
=== RUN   TestPhase5_WinnerOnlyCommit_CommitFailureCleansUpAndReleases
--- PASS: TestPhase5_WinnerOnlyCommit_CommitFailureCleansUpAndReleases (0.00s)
=== RUN   TestPhase5_WinnerOnlyCommit_LoserDisposeDoesNotMutateStore
--- PASS: TestPhase5_WinnerOnlyCommit_LoserDisposeDoesNotMutateStore (0.00s)
=== RUN   TestPhase5_WinnerOnlyCommit_PublicationDeniedByClosedSlot
--- PASS: TestPhase5_WinnerOnlyCommit_PublicationDeniedByClosedSlot (0.00s)
=== RUN   TestPhase5_WinnerOnlyCommit_AlreadyConsumedReadyRejectedInReduce
--- PASS: TestPhase5_WinnerOnlyCommit_AlreadyConsumedReadyRejectedInReduce (0.00s)
PASS
```

Implementation: `internal/core/runtime/parallel_race.go:622-636` — `winnerOut.ready.Consume()` is called before `commitMemoInjection`; on `Consume` error `cleanUpParallelFailure` disposes the winner and releases losers without committing. `readyAttempt.Dispose` terminalizes without `MemoStore.Update` or `SetInterleavedState`.

### Store parity note (Important 4)

`phase5_winner_only_commit_test.go` asserts winner-only atomicity via in-memory proof (`b2bua.NewMemoryStore` + `interleavedthinking.NewMemoStore` + `FetchInterleavedState`/`Get`). This suffices because `b2bua.Store.SetInterleavedState` is a single-record atomic operation across all production implementations:

- `internal/core/b2bua/store.go:287` — `MemoryStore.SetInterleavedState` writes `st.interleaved = state` under `mu` (single in-memory record).
- `internal/core/continuity/bunstore/store.go:441` — `Store.SetInterleavedState` executes a single `UPDATE a_legs SET interleaved_state_json = ?, last_seen_at_unix = ? WHERE a_leg_id = ?` (single row, single statement). The same column is used for SQLite and Postgres (dialect-agnostic `bunstore` implementation); no multi-row or cross-table transaction is required.

The test additionally checks the memo body via `MemoStore.Get` (which is in-memory `interleavedthinking.MemoStore`; durable memo content is orthogonal to the A-leg row). Bunstore/continuity SQLite integration tests (`internal/core/continuity/bunstore/store_test.go`, `routeoverride_persist_test.go`) already cover SQLite vs Postgres semantic parity for the broader store; the winner-only commit path exercises the same single-row `interleaved_state_json` path verified there. No new multi-write transaction was introduced for winner-only commit — the reducer commits memo injection then `SetInterleavedState` in the existing single-winner order, and any commit failure disposes the winner without partial persistence (see `TestPhase5_WinnerOnlyCommit_CommitFailureCleansUpAndReleases`).

---

## Commands to reproduce GREEN

```powershell
go vet ./internal/core/runtime
go test ./internal/core/runtime -count=1 -timeout 10m
go test ./internal/core/runtime -run TestRetryRecvStreamCloseDuringReplacementOpen_AppendsReplacementLegExactlyOnce -count=3 -v
go test ./internal/archtest -run shared_recovery_mutation -count=1 -v
go test ./internal/core/runtime -run TestPhase5_ParallelRoundReducer_StableFailureMergeOrder -count=1 -v
go test ./internal/core/runtime -run TestPhase5_WinnerOnlyCommit_FatalErrorNeverPersists -count=1 -v
gofmt -l ./internal/core/runtime
go vet ./...
```
All above commands PASS on branch `feat/runtime-attempt-publication-ownership-convergence` working tree after Phase 5 reducer and winner-only commit.
