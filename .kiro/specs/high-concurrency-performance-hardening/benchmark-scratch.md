# Performance Benchmark Scratchpad

> **Implementation evidence file.** This file intentionally starts as a template. Implementing agents MUST append measured baseline and post-change results here as tasks execute. Do not replace measurements with estimates or expected improvements.
>
> **Not measured = not proved.** A performance/concurrency optimization may be called successful only when an equivalent before/after experiment demonstrates the claimed effect. An optimization that is not measurably beneficial, is statistically inconclusive, or shifts cost to another critical metric must be reverted or explicitly retained only for a separately proven correctness/architectural reason.

## 1. Evidence Rules

1. Establish the reusable traffic/load harness and freeze named scenarios before optimizing production code.
2. For every production optimization attempt, create an entry in this file **before** making the production change. Record the hypothesis, affected path, baseline commit, scenario, host, configuration, and baseline measurements.
3. After the change, rerun the same scenario on the same class of host with the same Go version, `GOMAXPROCS`, process configuration, database mode, feature flags, payload shape, **logical-stream target**, transport topology/connection plan, ramp, stream cadence, duration, profiler settings, and certification gate profile unless the variable being changed is itself the experiment. Record every intentional difference.
4. Keep failed, neutral, invalid, unsupported, and reverted experiments. Do not delete evidence because a proposed optimization did not work.
5. Microbenchmarks must use repeated samples and a statistical comparison (prefer `benchstat` or an equivalent checked-in/reportable procedure). Do not compare single `go test -bench` numbers.
6. End-to-end/load scenarios must be repeated enough to expose variance. Record all repetitions or machine-readable result paths plus aggregate p50/p95/p99 where applicable.
7. For concurrency changes, capture mutex/block profiles when contention is part of the hypothesis. For allocation/memory changes, capture heap/alloc evidence. For CPU changes, capture CPU profiles. For scheduler/goroutine hypotheses, capture runtime metrics and Go trace when profiles are insufficient.
8. A throughput win that causes unacceptable latency, memory, error rate, durability, ordering, correctness, cleanup, or tail behavior is not a successful optimization.
9. Correctness-required fixes such as streaming timeout semantics still require before/after evidence demonstrating the failure and its removal, even when shipping the fix is not conditional on a throughput improvement.
10. Never use production provider APIs to establish scalability. Use deterministic local reference backends/fakes for proxy-capacity measurements; measure external database variants separately when persistence is the subject.
11. Raw prompts, credentials, bearer tokens, secrets, or unbounded user/model/session identifiers must not be written into this file, result JSON, benchmark labels, logs, metrics, pprof labels, trace task/region/log metadata, or artifact filenames. Binary profiles/traces remain access-controlled diagnostic artifacts and are not public evidence files.
12. If the environment cannot run a requested scale tier, record the host/resource/OS/generator limit that stopped the test and do not attribute that limit to the proxy without evidence.
13. A run whose semantic correctness assertions fail is **invalid for performance certification**, regardless of throughput. Record it as `invalid-run`, preserve the failure evidence, and fix/understand correctness before using performance numbers from that run.
14. Capacity tiers refer to **simultaneous logical LLM streams**, not physical TCP connections. Always record actual/target transport connection counts separately for each transport leg/topology.
15. A certification gate profile must be frozen before the first result it evaluates. Do not choose or relax thresholds after seeing candidate/certification measurements.
16. The implementation baseline is post-#446 `main`, beginning at `f70201d037268508931ceab599b12ee4d3b40aad`. Record the exact run commit; pre-#446 HOLD/DELTA/DISCONNECT results are historical context only and cannot satisfy Phase 2.
17. Fingerprint built-in/standard and negotiated executable-plugin backend execution separately. For executable plugins, record per-stream goroutines/stack, bounded event-buffer capacity/occupancy where observable, scheduler/trace evidence, cancellation acknowledgement/forced-close latency, and owner reclamation.
18. Treat #446's launch-permit linearization and parallel sibling cancellation as correctness/performance regression controls. A run with an unjoinable cancellation worker, duplicate/missing terminal/usage/billing evidence, or unreclaimed stream owner is invalid for certification.
19. Record a phase-aware cost envelope: fixed resources per active stream plus request-start, per-event, terminal-burst, and cleanup costs. For executable plugins, record AIProxer, managed connector child processes, and aggregate process-tree resources separately; moving cost across IPC is not an aggregate improvement. Derive aggregate counter deltas by summing each member once over the same interval and aggregate gauge peaks from the maximum aligned per-timestamp sum, never by adding independent process peaks. Prefer cgroup/process-tree memory or aligned PSS/USS for unique physical memory; label summed RSS as conservative accounting that may count shared pages more than once.
20. Classify every material request/event/terminal/cleanup database operation as exactly one canonical class before testing a cache, batch, or async path: authoritative synchronous work (including mandatory stream/event records), reusable/versioned read work, best-effort projection, or idempotent terminal work. Never use stale cache or volatile enqueue acknowledgement to erase a required correctness/durability boundary.
21. Allocation/reuse experiments must compare simpler ownership/copy elimination, preallocation, or incremental processing where applicable. A retained buffer/object reuse candidate records capacity classes/drop threshold, oversized-buffer rejection, reset/zeroing/privacy behavior, post-GC retention, and cross-session tests.
22. Goroutine experiments report owner and join contract for each per-stream goroutine/channel. A shared worker candidate is eligible only for bounded short independent jobs with explicit queue/full/fairness/cancel/shutdown behavior; never pool blocking stream receive, ordered send, cancellation, or close merely to lower the count.

## 2. Environment Record

Create one subsection per benchmark host/runtime combination and reference its ID from experiment entries.

### ENV-<id>

- Date/time:
- Commit / branch:
- OS / kernel / build:
- CPU model / logical CPUs:
- RAM / swap:
- Storage / filesystem:
- Go version:
- `GOMAXPROCS` / `GOMEMLIMIT` / relevant `GODEBUG`:
- Proxy build flags:
- Proxy configuration checksum / notable values:
- Database mode and version, if any:
- Database location/latency class, if any:
- Traffic generator host/process placement:
- Generator CPU/RAM/socket headroom during run:
- Open-file/socket/backlog limits:
- Reverse proxy / TLS / HTTP version topology:
- Backend execution path: built-in/standard | negotiated executable plugin
- Backend-plugin protocol/features and executable identity, if applicable:
- Managed connector child process/PID identity and resource sampling method, if applicable:
- Resource accounting scope: proxy | connector child | aggregate process tree:
- Client -> front-door connection topology:
- Front-door/terminator -> AIProxer connection topology, if applicable:
- AIProxer -> reference-backend transport topology:
- Other competing workload:

## 3. Canonical Scenario Registry

Task 1 establishes stable scenario IDs here before production optimization begins. At minimum include:

- `HOLD-BUILTIN-*` / `HOLD-PLUGIN-*` — long-held idle/low-rate streaming logical streams through built-in/standard versus negotiated executable-plugin execution.
- `DELTA-BUILTIN-*` / `DELTA-PLUGIN-*` — actively emitting streams with controlled event cadence and payload size through each backend execution form.
- `START-*` — high request/session-start churn.
- `BODY-*` — concurrent maximum/near-maximum request-body materialization pressure, including chunked bodies.
- `OUTPUT-*` — long cumulative output / many-event streams for retained-memory analysis.
- `COMPLETE-*` — synchronized/compressed completion bursts for terminal persistence and billing spool pressure.
- `RETRY-*` — large prompts/tool schemas with controlled pre-output retry/failover counts.
- `OBSERVE-*` — traffic/audit disabled, enabled-fast, and deliberately slow-sink variants.
- `AUTHZ-*` — concurrency-authority same-dimension and many-dimension contention.
- `B2BUA-*` — independent A-leg, hot-A-leg, and expiration/churn variants.
- `DISCONNECT-LAUNCH-*` — cancellation while a B-leg launch permit is outstanding.
- `DISCONNECT-SIBLING-{1,N}-*` — one versus multiple sibling B-leg cancellation/close, preserving parallel teardown.
- `DISCONNECT-ACK-*` / `DISCONNECT-FORCE-*` — graceful cancellation acknowledgement versus deadline/forced-close fallback.
- `DISCONNECT-RACE-*` — cancellation receipt racing upstream success/failure/terminal evidence.
- `DISCONNECT-JOIN-CONTRACT-*` — deterministic fake whose `Cancel` becomes joinable only after `Close`, plus a bounded negative case that fails validity rather than hanging.
- `SOAK-*` — steady-state long-duration run at the highest sustainable tier.
- `COST-*` — phase-aware overlays on HOLD/START/DELTA/COMPLETE/DISCONNECT that derive fixed-stream, request-start, per-event, terminal-burst, cleanup, database-operation, allocation/GC, shared-state, and goroutine/scheduler costs rather than introducing a second synthetic protocol path.

For each scenario record: protocol/client driver, stable reference-backend script ID plus immutable version/content digest, **logical stream target**, transport topology, target client/front-door transport connections, target terminator/proxy connections if applicable, max/multiplexed streams per connection where controlled, backend execution path, required process resource scopes and aggregation method, negotiated plugin features when applicable, ramp, request bytes, TTFT, stream duration, event count, event size, event cadence, launch-in-flight flag, sibling B-leg count, cancellation mode/deadline, explicit cancellation-versus-upstream-terminal schedule, upstream terminal outcome (`success`/`failure` where raced), completion synchronization, secure-session mode, store mode, traffic observer mode, authority mode, expected terminal/usage/billing/joinability/cleanup assertions, phase-cost dimensions, and certification gate profile ID when used for a capacity verdict.

A scenario fingerprint MUST include logical-stream and transport-connection fields independently. It MUST also include the reference-backend script ID/version or digest and the complete cancellation plan, so upstream-success and upstream-failure races or different race schedules cannot compare as equivalent. `HOLD-10000` over HTTP/1.1 and `HOLD-10000` multiplexed over HTTP/2 are not equivalent resource scenarios even though both carry 10,000 logical streams.

## 4. Certification Gate Registry

Certification is deterministic but does **not** invent universal latency/SLO numbers. Before certification, create a gate profile for every scenario/tier class. Numeric thresholds must have a recorded source and cannot be selected after seeing the run.

### GATE-<id>

- Scenario family / applicable scenario IDs:
- Target logical streams:
- Transport topology / connection plan:
- Minimum warm-up duration:
- Minimum steady-state duration:
- Minimum valid repetition count:
- Cleanup observation window:
- **Correctness gate:** all expected event/order/count/terminalization assertions pass; unexpected semantic failures allowed: `0` unless an explicit scenario contract says otherwise.
- **Unexpected error/rejection gate:** value/rate and source. Expected QoS/policy rejections for a rejection scenario are classified separately and do not consume the unexpected-error budget.
- **Start latency gate:** threshold + statistic + source, or `not-a-release-gate` with rationale.
- **TTFT gate:** threshold + statistic + source, or `not-a-release-gate` with rationale.
- **Event-forward latency gate:** threshold + statistic + source, or `not-a-release-gate` with rationale.
- **Terminal latency gate:** threshold + statistic + source, or `not-a-release-gate` with rationale.
- **CPU/resource-headroom gate:** threshold + source when required for the capacity claim.
- **Fixed active-stream cost gate:** heap/RSS contribution, goroutines, stack bytes, channels/reserved buffers, sockets/owners and source, or `not-a-release-gate` with rationale.
- **Request-start cost gate:** allocations/bytes, DB/lock/scheduler work, start latency and source, or `not-a-release-gate` with rationale.
- **Per-event cost gate:** allocations/bytes, GC/scheduler, DB/channel blocking, event-forward latency and source, or `not-a-release-gate` with rationale.
- **Terminal-burst cost gate:** peak allocations, durable DB/spool/queue work, goroutine/scheduler burst, terminal latency and source, or `not-a-release-gate` with rationale.
- **Database-operation gate:** classified logical operations and physical queries/transactions/pool-wait by lifecycle phase plus source/budget, or `not-a-release-gate` with rationale.
- **Process-tree attribution gate:** proxy and connector-child measurements present separately and aggregate headroom passes when executable plugins are in scope.
- **Steady-state memory-growth gate:** method/threshold + source; must reject a supported positive monotonic growth trend attributable to live-history leakage.
- **Cleanup cost gate:** cleanup deadline plus expected return band for heap/goroutines/sockets/session/lease/cache/queue state + source/method.
- **Durability gate:** missing mandatory records `0`; duplicate mandatory records `0`; ordering/exactly-once rules per existing contract.
- **Timeout gate:** healthy progressing stream must not fail solely because total duration crosses the historical blanket timeout.
- Threshold source(s): product SLO | configured/protocol bound | explicit operator capacity objective | predeclared baseline-regression budget | frozen lower-tier scaling contract | other documented source.
- Profile frozen at commit/time:

Rules:

1. Non-negotiable correctness, mandatory durability, timeout, and bounded-cleanup semantics do not become optional because no product latency SLO exists.
2. A latency/resource threshold without a defensible recorded source cannot be invented merely to obtain `GO`; mark the performance gate unresolved and the tier **NO-GO for certification** until the release objective is defined.
3. Gate profiles may differ by scenario (for example `HOLD`, `DELTA`, `BODY`, `COMPLETE`) because their useful latency/resource metrics differ, but they must be frozen before the evaluated run and versioned when intentionally changed.
4. `GO` requires a valid run reaching the target logical-stream tier for the required steady-state duration and passing every required gate. A required-gate failure yields `NO-GO`. A generator/OS/host inability yields `NO-GO (environment-limited)`/`unsupported-by-host` for that tier, not a proxy-failure label.
5. The scenario declares applicable fixed, request-start, per-event, terminal-burst, and cleanup phases. Each applicable phase maps to its same-named gate. A missing required gate or threshold source yields `NO-GO (gate-definition-incomplete)`; unavailable required evidence makes the run `invalid-incomplete`; a measured gate failure yields `NO-GO` with the typed measured limiter.

## 5. Baseline Matrix

Do not edit production performance behavior until the harness exists and at least the P0-relevant baseline matrix has been captured.

The 1k/5k/10k columns are **logical-stream tiers**. Every evidence entry also records actual transport connection counts.

| Scenario | 1k streams | 5k streams | 10k streams | Store / Feature Mode | Evidence entry |
|---|---:|---:|---:|---|---|
| HOLD-BUILTIN | pending | pending | pending | minimal / built-in-standard | pending |
| HOLD-PLUGIN | pending | pending | pending | minimal / negotiated executable plugin | pending |
| DELTA-BUILTIN | pending | pending | pending | secure-session memory / built-in-standard | pending |
| DELTA-PLUGIN | pending | pending | pending | secure-session memory / negotiated executable plugin | pending |
| DELTA-BUILTIN | pending | pending | pending | secure-session durable / built-in-standard | pending |
| DELTA-PLUGIN | pending | pending | pending | secure-session durable / negotiated executable plugin | pending |
| START | pending | pending | pending | normal | pending |
| BODY | pending | pending | pending | normal | pending |
| OUTPUT | pending | pending | pending | normal | pending |
| COMPLETE | pending | pending | pending | durable billing spool | pending |
| DISCONNECT-LAUNCH | pending | pending | pending | negotiated executable plugin / launch in flight | pending |
| DISCONNECT-SIBLING-1 | pending | pending | pending | built-in + plugin sentinels | pending |
| DISCONNECT-SIBLING-N | pending | pending | pending | built-in + plugin sentinels / parallel teardown | pending |
| DISCONNECT-ACK/FORCE/RACE | pending | pending | pending | negotiated executable plugin | pending |
| DISCONNECT-JOIN-CONTRACT | pending | bounded sentinel | bounded sentinel | plugin contract fake | pending |
| COST-ENVELOPE | pending | pending | pending | fixed + start/event/terminal/cleanup; proxy + connector + aggregate | pending |

Use `unsupported-by-host` rather than `fail` when the generator/OS/host cannot supply the requested tier. Record the limiting evidence.

## 6. Experiment Entry Template

Copy this section for **every** optimization attempt, including reverted attempts.

### EXP-<nnn> — <short title>

**Status:** planned | baseline-recorded | changed | proved-improvement | correctness-only | inconclusive | no-material-effect | unsupported-by-host | invalid-run | regressed | reverted

**Task / requirements:**

**Hypothesis:**

**Why this is being attempted:**

**Production path under test:**

**Baseline commit:**

**Candidate commit:**

**Environment ID:**

**Scenario ID(s):**

**Certification Gate ID(s), if applicable:**

**Target logical streams:**

**Achieved steady logical streams:**

**Target transport connections by leg:**

**Observed transport connections by leg:**

**Backend execution path / negotiated plugin features:**

**Reference-backend script ID / immutable version or content digest:**

**Required process resource scopes / child connector identities / aggregation method:**

**Cancellation schedule (launch state / siblings / mode / deadline / cancel and upstream-terminal offsets / terminal outcome):**

**Controlled variables / intentional differences:**

#### Run validity / limiter classification

- Result status: valid | invalid-correctness | invalid-incomplete | unsupported-by-host | aborted-safety
- Correctness assertion summary / failed assertion IDs:
- Generator limiter:
- Host/OS limiter:
- Proxy-derived limiter:
- Database/backend/external limiter:
- Metrics unavailable and why:

#### Baseline measurements

- Repetition count:
- Warm-up / steady-state duration:
- Throughput / completions / events:
- Request-start latency p50/p95/p99:
- TTFT p50/p95/p99:
- Client-facing event forwarding latency p50/p95/p99:
- Terminal latency p50/p95/p99:
- Expected vs unexpected error/rejection/cancellation counts:
- Process CPU / system CPU:
- Proxy process CPU/RSS/heap/stack/goroutines/alloc/GC:
- Managed connector child CPU/RSS/heap/stack/goroutines/alloc/GC:
- Aggregate process-tree CPU/RSS/heap/stack/goroutines plus aligned-sample/counter-delta method and member completeness:
- RSS / live heap / heap objects:
- Total allocation bytes / malloc/free counts:
- Alloc bytes/op and allocs/op where applicable:
- GC CPU / pause / cycles:
- Goroutine count / stack memory:
- Goroutines and stack bytes per logical stream by backend execution path:
- Fixed channels/buffers/owners per logical stream by process; reserved capacity versus peak occupancy:
- Bounded stream/event channel capacity, peak occupancy, and blocked-send/receive evidence where observable:
- Cancellation acknowledgement / forced-close / join latency p50/p95/p99:
- Launch/attempt/control/upstream/closer/cancellation owner count before load, at steady state, and after cleanup:
- File descriptors / sockets by transport leg where observable:
- Logical streams target / achieved peak / achieved steady:
- Mutex/block profile summary:
- DB query count/QPS/p50/p95/p99/pool wait, if applicable:
- DB operations by request-start / stream-event / terminal / cleanup phase and correctness class:
- Allocations/bytes and GC/scheduler/blocking cost per request-start / event / terminal:
- Process-shared state/lock owner and time-in-critical-section profile:
- Cache/reuse/worker candidate configuration, bounds, freshness/privacy/lifecycle contract, if applicable:
- Durable spool append latency/backlog, if applicable:
- Outbound connections/TLS handshakes/HTTP2 streams, if applicable:
- Queue depth/drop/coalesce/backpressure metrics, if applicable:
- Correctness/durability assertion results:
- Profile/result artifact paths:

#### Candidate measurements

Record the same fields as the baseline.

#### Statistical / causal comparison

- Tool/procedure:
- Primary metric delta and confidence:
- Tail-latency delta:
- Memory/allocation delta:
- Fixed-stream and request/event/terminal/cleanup cost-envelope delta:
- Proxy/connector/aggregate process-tree delta:
- Database operation-count/classification delta:
- Contention delta:
- Secondary regressions:
- Correctness/durability checks:
- Gate profile results, if applicable:

#### Conclusion

- **Measured conclusion:**
- **Decision:** keep | revise-and-rerun | revert | no production change required
- **Claim allowed by evidence:**
- **Remaining limiter / next experiment:**

## 7. Final Certification Record

Complete after the implementation program has executed the final certification gates. **Record NO-GO outcomes when gates fail; do not wait for every gate to pass before writing the verdict.** Independent remaining gates should still run where safe so the evidence package is complete. If a safety/resource stop prevents a remaining gate, mark it `not-run-after-safety-stop` with the cause.

- Certification commit:
- Environment IDs:
- Gate profile IDs / frozen timestamps:
- Highest successfully held logical-stream tier:
- Highest successfully active-delta logical-stream tier:
- Client/front-door transport connections at highest tier:
- Terminator/AIProxer ingress connections at highest tier, if applicable:
- AIProxer/reference-backend connections at highest tier:
- 1k logical-stream verdict + failing/unsupported gates:
- 5k logical-stream verdict + failing/unsupported gates:
- 10k logical-stream verdict + failing/unsupported gates:
- Multi-hour soak duration and gate result:
- Secure-session memory-store result:
- Secure-session durable-store result:
- Traffic observation result:
- Concurrency-authority result:
- Billing completion-burst result:
- Long-stream (> legacy 120 s) result:
- Body-flood admission result:
- Disconnect cleanup result:
- Built-in/standard versus executable-plugin HOLD/DELTA result:
- Proxy/connector/aggregate process-tree cost result:
- Fixed per-stream resource envelope result:
- Request-start/per-event/terminal/cleanup cost-envelope result:
- Classified database-operation budget/result by lifecycle phase:
- Allocation/GC and scheduler budget/result:
- Retained/rejected cache, async-write, buffer/object reuse, shared-state, and worker/goroutine-topology decisions:
- Post-#446 launch-in-flight / sibling-1 / sibling-N / acknowledgement / forced-close / upstream-race results:
- Forced-close joinability and executable-plugin owner/buffer reclamation result:
- Correctness assertions all passed? yes/no + failures:
- Mandatory durability missing/duplicates/order failures:
- Unexpected error/rejection gate result:
- Tail-latency gate results and sources:
- Steady-state memory-growth gate result:
- Cleanup cost gate result:
- Monotonic heap/session/history growth observed? yes/no + evidence:
- Dominant remaining CPU profile nodes:
- Dominant remaining mutex/block profile nodes:
- Dominant remaining DB operations:
- Proxy-derived capacity limiter:
- Host/OS/generator/external-service limiter:
- Findings intentionally left unchanged because benchmarks showed no material impact:
- Overall certification verdict: GO | NO-GO (proxy-limited) | NO-GO (environment-limited) | NO-GO (gate-definition-incomplete)
- Exact scope/rationale of verdict:
