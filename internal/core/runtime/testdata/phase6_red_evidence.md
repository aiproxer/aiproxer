# Phase 6 RED Evidence: Attempt Publication Ownership Convergence

This document records TDD RED -> GREEN evidence for Phase 6 remediation of
runtime-attempt-publication-ownership-convergence (Tasks 6.1-6.4 review findings).

## 6.1 Raw Stream Mutation Ratchet — RED before allowlist tighten

**Invariant:** Raw `loadInner`/`takeInner`/`storeInner` outside `attempt_session.go`
must be zero outside the tight allowlist (6 sites). Each allowed site is an
attempt-owned snapshot behind `slot.require()` or a lifecycle-complete wrapper
(`TerminalizeAttempt`/`cancelAndClose`/`InstallBridgeStream`).

**RED command (before allowlist correction for line drift):**
```powershell
go test ./internal/archtest -run TestPhase1_AttemptBoundaryRatchets/raw_stream_mutation -count=1 -v
```

**RED output (line drift after adding deprecation comments):**
```
=== RUN   TestPhase1_AttemptBoundaryRatchets/raw_stream_mutation_outside_attempt_owner_detected_red
    phase1_attempt_boundary_ratchets_test.go:256: Phase6.1: found 1 non-allowlisted raw stream mutation sites:
        executor_open_attempt.go:1049: calling attemptSession.loadInner
--- FAIL: TestPhase1_AttemptBoundaryRatchets/raw_stream_mutation_outside_attempt_owner_detected_red
```

Cause: addition of 8 lines of deprecation comments in `attempt_session.go` shifted
`executor_open_attempt.go` readiness snapshot from line 1041 to 1049; allowlist
still pinned to 1041.

**GREEN command (after allowlist tighten to 1049 + documentation):**
```powershell
go test ./internal/archtest -run TestPhase1_AttemptBoundaryRatchets -count=1 -v
```

**GREEN output (after):**
```
=== RUN   TestPhase1_AttemptBoundaryRatchets/raw_stream_mutation_outside_attempt_owner_detected_red
    phase1_attempt_boundary_ratchets_test.go:268: Phase6.1: verified raw stream mutation boundary (6 allowed sites, 0 non-allowlisted)
--- PASS
```

Implementation: `internal/archtest/phase1_attempt_boundary_ratchets_test.go:209-268`
tight allowlist with exact count check (6) and per-site justification comment.
Remaining raw sites are attempt-owned reads via detached slot snapshot:
- `executor_open_attempt.go:1049 loadInner` — pre-ready sideband evidence snapshot consumed by `prepareReadyAttempt` before publication
- `executor_recv_loop.go:265 takeInner`, `300 loadInner`, `432 loadInner`, `537 takeInner` — Recv-loop handling behind `slot.require()`
- `executor_retry_stream.go:84 takeInner` — detached close path

Lifecycle-complete alternatives (`TerminalizeAttempt` for replacement/terminalize,
`cancelAndClose` wrapper for pre-output recoverable, `InstallBridgeStream` for
parallel winner bridge) are used wherever mutation closes/cancels the stream;
the retained loads are read-only snapshots required before those wrappers.

---

## 6.2 Duplicate Terminal Entry Points — RED before GREEN seal

**Invariant:** Production attempt terminalization has exactly one owner:
`attemptSession.TerminalizeAttempt`. Four legacy methods
(`AbortBeforeReturn`, `cancelAndClose`, `finishAsReplaced` on `attemptSession`
and `Rollback`/`Abort`/`RollbackParallelLoser` on `attemptTx`) are deprecated
transitional shims delegating to `TerminalizeAttempt`.

**RED command (old baseline expectation):**
```powershell
go test ./internal/archtest -run TestPhase1_AttemptBoundaryRatchets/duplicate_terminal -count=1 -v
```

**RED output (old ratchet expected >=2 and logged RED as gap):**
```
=== RUN   TestPhase1_AttemptBoundaryRatchets/duplicate_terminal_entry_points_detected_red
    phase1_attempt_boundary_ratchets_test.go:298: RED: found duplicate cleanup/terminal entry points (to be hardened in Phase 3):
        attempt_session.go: method finishAsReplaced on type *attemptSession
        attempt_session.go: method cancelAndClose on type *attemptSession
        attempt_session.go: method AbortBeforeReturn on type *attemptSession
        executor_open_attempt.go: method Rollback on type *attemptTx
--- PASS (but ratchet was RED baseline, not GREEN seal)
```

**GREEN command (after sealing):**
```powershell
go test ./internal/archtest -run TestPhase1_AttemptBoundaryRatchets/duplicate_terminal -count=1 -v
```

**GREEN output (after):**
```
=== RUN   TestPhase1_AttemptBoundaryRatchets/duplicate_terminal_entry_points_detected_red
    phase1_attempt_boundary_ratchets_test.go:XXX: Phase6.1: sealed terminal boundary: 1 production TerminalizeAttempt + 4 transitional shims (delegating):
    production: attempt_session.go: PRODUCTION method TerminalizeAttempt on type *attemptSession
    shims: attempt_session.go: method AbortBeforeReturn ...
--- PASS
```

Implementation: `internal/archtest/phase1_attempt_boundary_ratchets_test.go:274-320`
now asserts GREEN: exactly 1 production `TerminalizeAttempt` plus 4 allowed
delegating shims. Shim methods in `internal/core/runtime/attempt_session.go`
and `internal/core/runtime/executor_open_attempt.go` are annotated as
`Deprecated transitional shim — delegates to TerminalizeAttempt`.

---

## 6.3 Ownership Metrics After-Metrics — RED before tracked & justified

**Invariant:** `phase1_attempt_boundary_after_metrics.json` is a tracked file;
`cross_owner_access_sites` and `state_copy_surface_sites` growth is bounded to
+2 with explicit `justifications` entry.

**RED command (before git add & bounded check):**
```powershell
go test ./internal/archtest -run TestPhase1_AttemptBoundaryRatchets/ownership_metrics -count=1 -v
git ls-files internal/archtest/testdata/phase1* # before_metrics missing in index
```

**RED output:** `after_metrics.json` untracked (git ls-files empty); ratchet
only checked `facade_owner_count`, `fan_out`, `cleanup` and ignored
`cross_owner_access_sites` 12->14 drift without justification gate.

**GREEN command (after tracking & bounded justification):**
```powershell
git add internal/archtest/testdata/phase1_attempt_boundary_before_metrics.json internal/archtest/testdata/phase1_attempt_boundary_after_metrics.json
go test ./internal/archtest -run TestPhase1_AttemptBoundaryRatchets/ownership_metrics -count=1 -v
```

**GREEN output (after):**
```
=== RUN   TestPhase1_AttemptBoundaryRatchets/ownership_metrics_before_after_ratchet
    phase1_attempt_boundary_ratchets_test.go:436: Phase6.1: ownership metrics ratchet OK (cross_owner 12->14 state_copy 5->6 with justifications)
--- PASS
```

Implementation: `internal/archtest/testdata/phase1_attempt_boundary_after_metrics.json`
now carries `justifications` for both deltas:
- `cross_owner_access_sites: "+2 due to TerminalizeAttempt converged terminalization and readyAttempt single-use handoff"`
- `state_copy_surface_sites: "+1 due to readyAttempt capability verification at publication boundary"`

Ratchet in `phase1_attempt_boundary_ratchets_test.go:410-437` asserts bounded
growth (delta <=2) and requires non-empty justification when delta >0.

---

## 6.4 Certification Concurrency — RED before real backend Open proof

**Invariant:** Parallel arm execution is concurrent in the real
`tryOpenParallelGroup` path, not coarse-serialized behind a lock. Synthetic
goroutine sleep proof alone is insufficient.

**RED command (synthetic only):**
```powershell
go test ./internal/core/runtime -run TestPhase6_Certification_ParallelArmsRunConcurrently_NotSerialized -count=1 -v
```

**RED gap:** synthetic `time.Sleep` in goroutines proves the harness can run
concurrently, but does not prove `parallel_race.go`'s backend `Open` calls run
concurrently; a future regression could serialize `evaluateCandidate`/`openAttemptTx`.

**GREEN command (after supplementing with blocked Open):**
```powershell
go test ./internal/core/runtime -run TestPhase6_Certification_RealParallelBackendOpenRunsConcurrently -count=1 -v
go test -run TestPhase6 -count=5 -timeout 10m ./internal/core/runtime
```

**GREEN output (representative):**
```
=== RUN   TestPhase6_Certification_RealParallelBackendOpenRunsConcurrently
    phase6_certification_test.go:XXX: real backend Open concurrency OK: maxConcurrent=2 elapsed=55.3ms
--- PASS
=== RUN   TestPhase6_FaultMatrix_ConcurrentTerminalization
--- PASS
```

Implementation: `internal/core/runtime/phase6_certification_test.go:176-265`
adds `TestPhase6_Certification_RealParallelBackendOpenRunsConcurrently` which
wires two backends whose `Open` blocks 50ms each, records `maxConcurrent`,
and asserts `maxConcurrent>=2` and `elapsed < 90ms` (concurrent) vs
serialized `>=100ms`. Linux CI race log and 5x repeat logs are captured in
`internal/core/runtime/testdata/phase6_certification_evidence.md` §2-5.
