# Large-Payload Streaming Fast Path

Operator guide for configuring, observing, and rolling back the large-payload streaming fast path optimization in `cmd/aipstd` and `pkg/aipruntime`.

---

## 1. Overview and Architecture

The large-payload streaming fast path (`server.large_payload_fast_path`) is an optional memory-optimization subsystem designed for multi-megabyte requests (e.g., large documents, massive tool definitions, extensive system instructions).

In standard canonical execution, the proxy decodes the full request into an in-memory `aipapi.Call` struct, clones it multiple times for attempt derivations and checkpoints, and re-encodes it to upstream JSON. For requests larger than 1 MiB, this produces substantial Go heap allocation and garbage collection overhead.

The fast path captures the request into a bounded memory/disk spool, scans structural JSON boundaries, verifies protocol constraints, and streams the original bytes (with candidate model name rewriting if necessary) directly to upstream backends without allocating or retaining a full `aipapi.Call` object.

---

## 2. Full-Body Prevalidation

The fast path is **not** an unvalidated raw pipe:

1. **Full Receipt Before Provider Open:**
   Per Requirement 21.8, proxy validation receives and validates the **complete request body before opening the upstream provider connection**. The primary benefit is Go heap/GC reduction and elimination of repeated AST allocations—**not early Time To First Token (TTFT)**.
2. **Outer Handler Checks First:**
   Authentication, TLS, and media-type validation (`Content-Type: application/json`) execute in the outer HTTP handlers before any fast-path capture logic.
3. **Five Pre-Capture Gates (`EvaluatePreCaptureGates`):**
   Requests undergo five cheap pre-capture gates before any spooling:
    - Gate 1, feature/profile/two-phase executor: `enabled: true`, a profile, and a two-phase executor must all be present.
    - Gate 2, known-length threshold: parsed known identity/uncompressed request length below `threshold_bytes` stays canonical (compressed `Content-Length` is never trusted as decoded length).
    - Gate 3, gzip wave 1: gzip-encoded requests bypass capture to canonical decoding.
    - Gate 4, frozen static disposition: `DefinitelyCanonical` generations stay canonical with zero spool/scanner work.
    - Gate 5, custom route hook: a configured `ResolvePreAdmissionRoute` or `ResolveBodyRoute` without a candidate proof contract stays canonical.
   (`POST`-method and route matching are enforced one level out, in shared `ServeHTTP`, before gate evaluation.)
4. **Streaming Token Scanning & Protocol Proof:**
   During capture, an incremental JSON scanner extracts structural tokens (e.g., model name, stream flag, store parameter) into bounded semantic facts. A protocol proof verifies that the request belongs to an approved wire subset.
5. **One-Way Wire Commit:**
   Core assessment verifies route override domains, economic admission, and metering bounds before committing to wire execution. If any check fails, the request gracefully falls back to the canonical path under the same decode permit.
6. **Route Origin And Model Identity:**
   The protocol proof carries the route's origin: whether the client named the route, the proxy chose its default route, or a continuation parent's lineage restored it. The wire lane resolves a route without the request-bound model registry, so the only model identity it adds is the one the generation's own default route needs: a request on the derived default route keeps the generation's frozen canonical-to-native binding, and every other route keeps the model identity the lane resolved on its own. The assessment and the wire execution use the same identity, so the model the backend receives is the one the assessment decided on. A request that declines to the canonical lane replays the same route, with the same origin.
   The origin is final at the last authority that can replace the route: an administrator's route override, or a request transform that rewrites the selector, replaces the origin too. An overridden route belongs to the overriding authority, so it never keeps the default route's frozen binding — the current request-bound registry stays authoritative for it, even when the override selects exactly the expression the frontend would have selected.

---

## 3. Spool Confidentiality and Security

Spool files contain **plaintext client prompt data**. Operators must configure host environments accordingly:

- **Volume Protection:**
  When `spool_dir` is configured, place it on a dedicated volume or filesystem with restricted access permissions and encryption at rest (e.g., LUKS, BitLocker, or platform volume encryption).
- **Owner-Only Permissions:**
  Spool files are created with mode `0600` (read/write by proxy owner process only) where supported by the operating system.
- **Unpredictable Filenames:**
  Spool files use cryptographically random, unpredictable filenames. Names never contain user IDs, session tokens, model identifiers, or request metadata.
- **Lifetime and Cleanup:**
  Spool files are ephemeral. The underlying file is unlinked immediately upon completion, error, or client cancellation. On Windows systems where open files cannot be immediately unlinked, deletion is marked pending and executed when the last file descriptor closes.
- **Telemetry Redaction:**
  Spool directory paths, full filenames, prompt text prefixes, and sensitive session resume tokens are strictly excluded from logs, Prometheus metrics, and traces.

---

## 4. Memory vs. Disk I/O Tradeoff

### 4.1 Configuration Knobs

Configured under `server.large_payload_fast_path`:

| Key | Type | Default | Description |
|---|---|---|---|
| `enabled` | `bool` | `false` | Master toggle for the optimization. Default is disabled. |
| `threshold_bytes` | `int64` | `1048576` (1 MiB) | Decoded-size consideration threshold. Requests smaller than this use canonical processing. |
| `memory_spool_bytes` | `int64` | `65536` (64 KiB) | In-memory buffer size per captured request. Exceeding bytes spill to disk. |
| `max_inflight_spool_bytes` | `int64` | `268435456` (256 MiB) | Global logical spool reservation ceiling across concurrent captures. |
| `max_semantic_fact_bytes` | `int64` | `262144` (256 KiB) | Maximum memory allowed for profile-derived metadata facts. |
| `spool_dir` | `string` | `""` | Directory for temporary spill files. Empty string uses OS temp directory. |

### 4.2 Optimization Budget vs. OOM Admission

`max_inflight_spool_bytes` is an **optimization budget, not a global request admission gate**:

- If total in-flight spool allocations exceed `max_inflight_spool_bytes`, the proxy **declines fast-path spooling** and routes the request to canonical processing.
- Exhaustion produces **no client-visible error and no HTTP 413**.
- Because canonical fallback can still allocate heap according to pre-existing limits, this budget must not be treated as a global OOM prevention mechanism.

### 4.3 Measured Tradeoffs (Production Evidence)

From empirical benchmarks (Tasks 19.2–19.7 and Remediation Phases 3/6):

- **Retained Go Heap:**
  - *Pre-Remediation Baseline (Task 19.3):* On accepted wire requests, post-commit retained Go heap during provider streaming was approximately **35.6 KiB (35,656 B/op, 12 allocs/op) with a flat 0.000% slope** across 1 MiB, 5 MiB, and 20 MiB payloads (compared to multi-megabyte retained heaps and 7–9 Call clones on the canonical path).
  - *Post-Remediation (Streaming Proof, Phase 3/6):* Confirmed flat at **35,622–35,632 B/op (11 allocs/op) with a 0.000% slope** (35,627 B/op @1 MiB, 35,632 B/op @5 MiB, 35,622 B/op @20 MiB; `evidence/19.2-19.5-benchmarks.md` §5.1.2).
- **True End-to-End Pipeline Transient Heap (Capture-Through-Open, `frontendpipe.ServeHTTP`):**
  - *Historical Defect (Finding B1, `candidate.go:387`):* While isolated `CompileProof` was flat (~77–89 KiB), candidate capture previously called `readCompletedSource(compSrc)` to materialize the whole spooled body into a memory `[]byte`. This caused real pipeline transient allocations to scale linearly with payload size: **~2.5 MB @1 MiB**, **~11.3 MB @5 MiB**, and **~50.4 MB @20 MiB**, violating the true end-to-end memory invariant despite isolated proof passes.
  - *Post-Remediation (Source-Only Accepted Path, Phase 1–4):* `candidate.go` returns `res, nil, nil` directly, avoiding in-memory slice materialization. Measured capture-through-open transient allocations collapsed to a flat **~220–235 KiB across 1 MiB, 5 MiB, and 20 MiB** (ceiling: 458,752 B/op; `TestFindingB1_RealPipeline_TransientAllocBounded`):
    - OpenAI Responses: 221,596 B/op (306 allocs/op) @1 MiB, 220,971 B/op (298 allocs/op) @5 MiB, 236,676 B/op (602 allocs/op) @20 MiB (**PASS**).
    - OpenAI Chat: 222,568 B/op (361 allocs/op) @1 MiB, 222,584 B/op (363 allocs/op) @5 MiB, 234,565 B/op (595 allocs/op) @20 MiB (**PASS**).
    - OpenResponses: 220,255 B/op (297 allocs/op) @1 MiB, 219,415 B/op (290 allocs/op) @5 MiB, 231,710 B/op (588 allocs/op) @20 MiB (**PASS**).
    True capture-through-open transient heap is strictly bounded, consuming ~48–52% of the 458,752 B/op ceiling with flat $O(1)$ slope.
- **Isolated Proof-Time Transient Heap (`CompileProof`):**
  - *Pre-Remediation Baseline (`io.ReadAll`):* Allocated **6,877,983 B/op (252 allocs/op) @1 MiB**, **34,141,596 B/op (185 allocs/op) @5 MiB**, and **165,527,723 B/op (362 allocs/op) @20 MiB** (~6.5x–7.9x body size slope; strict 19.3 invariant failed).
  - *Post-Remediation (Streaming Proof, Phase 3/6):* Transient proof allocations collapsed to **82,524 B/op (269 allocs/op) @1 MiB (-98.80%)**, **78,152 B/op (209 allocs/op) @5 MiB (-99.77%)**, and **88,225 B/op (452 allocs/op) @20 MiB (-99.95%)** (`evidence/19.2-19.5-benchmarks.md` §5.1.2). Across individual certified protocol lanes (`TestLargePayloadProof_TransientAllocBounded`, target ceiling 458,752 B/op; §5.1.3):
    - OpenAI Responses: 78,569 B/op (1 MiB), 78,219 B/op (5 MiB), 89,408 B/op (20 MiB) (**PASS**).
    - OpenAI Chat: 79,592 B/op (1 MiB), 79,702 B/op (5 MiB), 88,721 B/op (20 MiB) (**PASS**).
    - OpenResponses: 77,423 B/op (1 MiB), 77,229 B/op (5 MiB), 87,791 B/op (20 MiB) (**PASS**).
    Transient allocations are flat across 1→5→20 MiB, flipping Task 19.3 strict heap gate to **PASS**.
- **Latency Impact:**
  - *Pre-Remediation Baseline:* Fast-path end-to-end latency was approximately **1.4x to 1.6x higher** than canonical transport (~38.9 ms [38.88 ms] vs 26.9 ms at 1 MiB; ~184.4 ms [184.39 ms] vs 116.0 ms at 5 MiB; 1,078.60 ms vs ~450.0 ms at 20 MiB). This overhead stemmed from disk spill I/O, streaming SHA-256 calculation, and proof compilation via whole-body `io.ReadAll` before upstream connection establishment.
  - *Post-Remediation (Streaming Proof, Phase 3/6):* End-to-end latency reduced to **30.43 ms at 1 MiB (+13% vs canonical ~26.9 ms)**, **142.00 ms at 5 MiB (+22% vs canonical ~116.0 ms)**, and **561.95 ms at 20 MiB (+25% vs canonical ~450.0 ms)** (`evidence/19.2-19.5-benchmarks.md` §5.1.2; `evidence/19.6-19.7-eligibility-roi.md` §5.1). The single-pass streaming scanner extracts tokens concurrently during capture without whole-body buffering.
- **Disk I/O:** Requests exceeding `memory_spool_bytes` (64 KiB) incur sequential disk write and read operations. High-throughput deployments should ensure `spool_dir` resides on fast NVMe or ramdisk storage.

---

## 5. Observability and Diagnostics

Diagnostics use closed-enum metrics with bounded label cardinality (Requirement 22.2). No model names, user IDs, session tokens, or payload paths appear in metric labels.

### 5.1 Prometheus Metrics

Namespace: `aip_large_payload_*`

| Metric Name | Type | Labels | Description |
|---|---|---|---|
| `aip_large_payload_pipeline_stages_total` | Counter | `stage` | Request count reaching each pipeline stage (`considered`, `static_canonical`, `captured`, `profile_proven`, `assessment_eligible`, `wire`, `canonical`). |
| `aip_large_payload_declines_total` | Counter | `reason` | Total requests declined from fast-path to canonical execution, partitioned by decline reason. |
| `aip_large_payload_captured_total` | Counter | `size_bucket`, `storage` | Captures by size bucket (`lt_32k`, `32k_256k`, `256k_1m`, `1m_5m`, `5m_20m`, `gte_20m`) and storage tier (`memory`, `file`). |
| `aip_large_payload_replays_total` | Counter | None | Total times a captured replay source was executed against an upstream provider. |
| `aip_large_payload_rewrites_total` | Counter | None | Total candidate model token rewrites spliced into outbound streams. |
| `aip_large_payload_stage_duration_seconds` | Histogram | `stage` | Latency histogram for pipeline stages (`pre_capture_gates`, `capture`, `proof`, `assessment`, `execution`). |
| `aip_large_payload_active_spool_bytes` | Gauge | None | Current active logical spool reservation in bytes across all in-flight captures. |

### 5.2 Decline Reasons

When a request falls back to canonical processing, `aip_large_payload_declines_total` records one of the following reasons:

- **Static Configuration Blockers:**
  - `feature_disabled`: Feature is toggled off (`enabled: false`).
  - `below_threshold`: Payload `Content-Length` is smaller than `threshold_bytes`.
  - `gzip_compressed`: Inbound payload is gzip-compressed (bypasses capture in V1).
  - `static_blocker`: Active extension planes require full `aipapi.Call` materialization.
  - `local_turn`: Local turn handler plane is occupied.
  - `secret_guard`: Secrets guard inspection is active.
  - `terminal_decision`: Terminal decision provider is configured.
  - `frontend_route_resolver`: Custom route hook is active.
  - `traffic`: Traffic capture or payload redaction plane is active.
  - `accounting_counting`: Token accounting requires full Call inspection.
  - `custom_call_callback`: Custom Call callback is registered.
  - `backend_domain`: Backend does not belong to a certified wire profile.
- **Dynamic Runtime Declines:**
  - `spool_budget_exhausted`: Active spool bytes exceed `max_inflight_spool_bytes`.
  - `proof_uncertain`: JSON scanner or profile proof encountered unsupported fields or malformed syntax.
  - `authority_blocker`: An extension plane declined the request during pre-commit assessment.
  - `route_incompatible`: Outbound candidates have incompatible wire rewrite domains.
  - `backend_incompatible`: Selected backend does not support raw wire execution.
  - `rewrite_unsupported`: Model name rewrite cannot be applied cleanly to the payload.
  - `session_unsupported`: Session metadata exceeds semantic fact limits.
  - `metering_unsupported`: Metering configuration requires full Call cloning.
  - `counting_unsupported`: Exact wire token counter unavailable for this provider.
  - `generation_mismatch`: Active generation changed during assessment.
  - `canceled`: Client aborted connection during capture or assessment.
  - `limit_exceeded`: Request body exceeded maximum allowable request size.
  - `read_error`: Network I/O failure while reading from client.

---

## 6. Configuration and Rollback

### 6.1 Sample Production Configuration

```yaml
server:
  address: "0.0.0.0:8080"
  large_payload_fast_path:
    enabled: false                   # Keep false until production assessor is wired
    threshold_bytes: 1048576         # 1 MiB
    memory_spool_bytes: 65536        # 64 KiB
    max_inflight_spool_bytes: 268435456 # 256 MiB
    max_semantic_fact_bytes: 262144  # 256 KiB
    spool_dir: "/var/lib/aip/spool"  # Dedicated protected volume
```

### 6.2 Transactional Reload and Last-Good Protection

The configuration parser strictly validates `large_payload_fast_path` on process start and during transactional configuration reloads (`POST /admin/config/reload` or `SIGHUP`):

- Setting negative or zero values for budgets when `enabled: true` fails validation.
- Setting `memory_spool_bytes > max_inflight_spool_bytes` fails validation.
- Blank or whitespace-only `spool_dir` strings, or paths containing NUL characters, fail validation.
- **Last-Good Preservation:** Any invalid reload candidate is rejected (`HTTP 422 invalid`), and the proxy continues operating on the previous valid generation without disruption.

### 6.3 Emergency Rollback

To immediately disable the fast path across all traffic:

1. Update the configuration file:
   ```yaml
   server:
     large_payload_fast_path:
       enabled: false
   ```
2. Trigger an atomic reload via the opt-in management listener (configured with
   `AIP_RELOAD_MANAGEMENT_ADDRESS`, plus `AIP_RELOAD_MANAGEMENT_TOKEN` for
   non-loopback/multi-user exposure):
    ```bash
    curl -X POST http://127.0.0.1:9090/admin/config/reload
    ```
    Or send `SIGHUP` to the process (Unix builds only; non-Unix builds expose reload via the API above).
3. In-flight wire streams finish normally; all new requests immediately revert to canonical processing with zero downtime.

---

## 7. Current Rollout Status and Canonical-Only Default

### 7.1 Historical Pre-Remediation Baseline (Tasks 19.7 and 20.3)

Prior to remediation, the fast path was **default-off and canonical-only in production**:
- `server.large_payload_fast_path.enabled` defaulted to `false`.
- No protocol lane advertised wire support in stock distributions (`aipstd`).
- All certified protocol lanes (Lane 1: OpenAI Responses, Lane 2: OpenAI Chat, Lane 3: OpenResponses No-Store) declined to canonical execution in production builds due to the proof-time transient heap spike (Task 19.3 strict gate failure) and uncomposed production assessor.

### 7.2 Post-Remediation and Execution Equivalence Status (Phases 0–4 Recertification)

Following execution of the remediation plan (`evidence/remediation-19.3-proof-transient-and-production-assessor-plan.md`) and the execution equivalence remediation (Phases 0–4):

- **Per-Lane Advertisement Status:**
  All three certified protocol lanes remain **ADVERTISE-CAPABLE** (`evidence/19.6-19.7-eligibility-roi.md` §5.3):
  - **Lane 1 (OpenAI Responses → OpenAI Responses):** **ADVERTISE-CAPABLE** (Default-Off). Condition (a) PASS (220,971–236,676 B/op real pipeline transient heap); Condition (b) PASS (production assessor composed in `runtimebundle.BuildHost`); Condition (c) PASS (streaming-only policy enforced in production; non-streaming declines to canonical).
  - **Lane 2 (OpenAI Chat → OpenAI Chat):** **ADVERTISE-CAPABLE** (Default-Off). Condition (a) PASS (222,568–234,565 B/op real pipeline transient heap); Condition (b) PASS (production assessor composed); Condition (c) PASS (streaming-only policy enforced).
  - **Lane 3 (OpenResponses → OpenResponses):** **ADVERTISE-CAPABLE** (Scoped to `store: false` no-continuation subset; Default-Off). Condition (a) PASS (219,415–231,710 B/op real pipeline transient heap); Condition (b) PASS (production assessor composed); Condition (c) PASS (streaming-only policy enforced; `store: true` or continuations decline to canonical).
- **Execution Equivalence Gaps Closed (Findings B1, B2, H3, M4, M5):**
  - **Finding B1 (Capture Body Materialization):** `frontendpipe/candidate.go` returns `res, nil, nil` directly for accepted candidates without whole-body `[]byte` slice materialization, ensuring flat ~220–235 KiB capture-through-open transient heap.
  - **Finding B2 (Race Engine Equivalence):** Wire execution runs on canonical attempt machinery (`internal/core/runtime`, net -217 lines): route-plan/shared-race delegation through `tryOpenParallelGroup` and `openNext` under existing attempt ownership. Restores canonical winner criteria (delta required, not message started), honors handicap delays, loser cancellation isolation, and backend panic isolation.
  - **Finding H3 (Routing State & Fail-Closed):** Two-turn weighted-first routing state advances correctly on parallel winner and store update failures abort cleanly.
  - **Finding M4 (Spill Lifecycle Refcounting):** Shared spill lifecycle tracking across readers and completed sources (`internal/core/largebody`), eliminating spill file leaks on pre-existing reader closure.
  - **Finding M5 (Truthful Terminal Capture Errors):** Added `CaptureOutcomeTerminalError` with truthful terminal error propagation on capture reader failures, preventing bogus canonical fallbacks on corrupt or unreadable spools.
- **Default Configuration Remains Disabled:**
  `server.large_payload_fast_path.enabled` strictly defaults to `false` (Requirement 22.1).
- **Non-Advertisement in Stock Binaries:**
  Wire support is **NOT advertised** in default stock distribution responses (`TestLane1E2E_WireSupportNotAdvertised`, `TestLane2E2E_WireSupportNotAdvertised`, `TestLane3E2E_WireSupportNotAdvertised` all pass).
- **Enablement Gate and Issue #532 Policy:**
  Per governance rules, **NO enablement in production until Task 19.7 per-lane gates pass**. All per-lane gates pass and equivalence gaps are closed. Production configuration remains default-off in stock binaries, and GitHub issue **#532 stays open** until formal downstream PR delivery is completed.

### 7.3 Activation Prerequisites and Follow-Up Status

Status of prerequisite workstreams and intentional V1 boundaries:

1. **Streaming `CompileProof` Rework:** **COMPLETED** (Remediation Phases 1–3). Replaced `io.ReadAll` with streaming token scanning and `CallIdentityWriter`. Proof-time transient allocation collapsed to ~78–89 KiB flat across 1, 5, and 20 MiB; Task 19.3 strict heap gate is PASS.
2. **Production Assessor Composition:** **COMPLETED** (Remediation Phase 4). Unified `AuthorityAssessmentGate`, `BackendWireProofGate`, `runtime.StandardLaneDomainPolicies()`, and `runtime.Executor` into `ProductionLargeBodyAssessor` composed in `runtimebundle.BuildHost` via `build_large_body_assessor.go`.
3. **Enablement Wiring:** **COMPLETED** (Remediation Phase 5). Formally linked `server.large_payload_fast_path` configuration to `runtimebundle` and frontend specifications while preserving `enabled: false` default and invalid-reload last-good semantics.
4. **Execution Equivalence Remediation:** **COMPLETED** (Fix Phases 0–4). Resolved findings B1, B2, H3, M4, M5 across `frontendpipe`, `runtime`, and `largebody`, verified with 10 dedicated finding tests and repo-wide test suites.
5. **Detached-Session Execution (Req 19.8):** **Intentionally Canonical-Only in V1.** Detached execution stays canonical-only until its Call/lifecycle dependencies are separately represented and parity-tested.
6. **Custom route hook contract (Req 13.2):** **Intentionally Canonical-Only in V1.** A configured `ResolvePreAdmissionRoute` or `ResolveBodyRoute` stays canonical-only until a future bounded route-resolution contract preserves the same ordering, precedence, and selector semantics.

---

## 8. Specification and Evidence References

For full verification artifacts and design details, consult:

- **Specification Requirements:** `.kiro/specs/large-payload-streaming-fast-path/requirements.md` (Requirements 20, 21, 22)
- **Technical Design:** `.kiro/specs/large-payload-streaming-fast-path/design.md` (Sections 3, 4, 7, 8, 15)
- **Remediation Plan:** `.kiro/specs/large-payload-streaming-fast-path/evidence/remediation-19.3-proof-transient-and-production-assessor-plan.md`
- **Call and Authority Census:** `.kiro/specs/large-payload-streaming-fast-path/evidence/1.8-call-census.md`
- **Plane Census:** `.kiro/specs/large-payload-streaming-fast-path/evidence/1.9-plane-census.md`
- **Benchmarks and Heap Analysis:** `.kiro/specs/large-payload-streaming-fast-path/evidence/19.2-19.5-benchmarks.md`
- **Eligibility Matrix and Lane ROI:** `.kiro/specs/large-payload-streaming-fast-path/evidence/19.6-19.7-eligibility-roi.md`
- **Initial Rollout Evidence:** `.kiro/specs/large-payload-streaming-fast-path/evidence/20.3-20.4-rollout.md`
- **Remediation Closeout Delta:** `.kiro/specs/large-payload-streaming-fast-path/evidence/20.5-remediation-closeout.md`
- **Execution Equivalence Closeout Delta:** `.kiro/specs/large-payload-streaming-fast-path/evidence/20.6-execution-equivalence-closeout.md`
