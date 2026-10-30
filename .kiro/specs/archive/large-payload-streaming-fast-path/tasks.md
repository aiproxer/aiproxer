# Implementation Plan

This plan is staged for a regression-sensitive brownfield codebase and for execution by agents that should **follow the plan rather than redesign it**. The canonical request path is the behavioral oracle. Do not improvise a simpler fast path, fabricate a partial `lipapi.Call`, or weaken an authority to keep wire mode.

Rebaseline used for this revision: `main` at `b08c60846a2a0119eeefef135cd6bbe06162a894` (2026-09-09). **Task 1 must still re-check the exact implementation-start SHA before production changes.**

Final V1 control flow:

```text
frontend-specific outer auth/media/path checks
  → cheap feature/profile/executor/known-length gates
  → O(1) frozen static disposition
       definitely canonical → unchanged canonical body read (NO spool/scanner)
       needs request facts  → continue
  → legacy full-body ResolveRouteSelector configured?
       yes → unchanged canonical path before capture
       no  → bounded capture + streaming shared JSON preflight
             → one existing byte-weighted DecodeAdmission permit
             → protocol semantic proof + RouteFromBodyModel parity
             → exact canonical semantic identity + recorder/session facts
             → AssessLargeBody (pure/frozen; SAME permit still held)
                  decline → canonical Spec.Decode under SAME permit → existing path
                  accept  → release permit → ONE-WAY WIRE COMMIT
             → ExecuteLargeBody
                  BeginTurn/A-leg exactly once
                  → live late route authority inside pre-certified domain
                  → existing authority/economic admission
                  → existing attempt/retry/recovery owner
                  → backend OpenWire from replay
                  → canonical EventStream + bounded ResponseFacts
                  → existing frontend wrap/write + keepalive/session carriers
```

There is no core-owned canonicalization callback, no second decode-admission decision, no provider-name switch in generic core, and no expected canonical fallback after wire commit.

## Execution Rules

- Characterization/TDD before each brownfield refactor.
- Keep implementation PRs chronological and focused. Do not implement Tasks 1–20 in one giant PR.
- Never fabricate a partial/minimal/shadow `lipapi.Call`.
- Never skip/reorder or post-hoc invoke a configured full-body `frontendpipe.Spec.ResolveRouteSelector`.
- Never run protocol semantic proof outside the existing byte-weighted decode admission.
- Never release/reacquire decode admission because proof/assessment declines.
- `AssessLargeBody` is bounded and side-effect-free: no `BeginTurn`, A-leg/store/DB read/write, billing/accounting reservation, provider I/O, client-body wait, spill read, or arbitrary unbounded plugin work.
- Static disposition is only a **fast reject**. It may say `definitely canonical` or `needs dynamic assessment`; it must never authorize wire execution.
- After assessment accepts and the permit is released, expected fallback is forbidden.
- Do not drop/reorder route candidates, disable races/fallbacks, or weaken authorities to retain wire mode.
- Backend exact/domain proof receives immutable profile body mode + rewrite semantics; `NeedsModelRewrite` cannot grant a rewrite the profile did not certify.
- Standard secure-session recording and metering need wire-native equivalents; do not make normal stock deployment permanently ineligible.
- Active Local Turn and Secret Guard execution are initial V1 blockers. Terminal Decision is also a blocker unless Task 12 explicitly closes continuation-source semantics.
- Preserve existing pre-request holdalive and stream keepalive semantics.
- First release remains default-off.
- If current code contradicts a task assumption, **stop that workstream, update the spec artifact first, then continue**. Do not guess.

---

## 1. Rebaseline Current Main and Freeze Canonical Oracles

- [x] 1. Revalidate the exact implementation-start architecture before production changes

- [x] 1.1 Record implementation-start SHA and compare it with this revision
  - Record `git rev-parse HEAD` in implementation evidence.
  - Re-check frontend ingress/decode/admission, generated planes, request-generation binding, secure-session lifecycle, metering/accounting, routing/route override, backend contracts, keepalive/response ownership.
  - If any material seam differs from the assumptions below, update `requirements.md`, `design.md`, and this plan before production code.
  - _Validation: `git diff --check`; targeted architecture tests_
  - _Requirements: 22_

- [x] 1.2 Freeze frontend-specific outer ordering and shared pipe ordering
  - Characterize OpenAI Responses, OpenAI Chat, and OpenResponses separately.
  - Confirm current shared ordering: body read → header selector → optional whole-body resolver → shared preflight → `TryAdmit` → guarded `RouteFromBodyModel`/Decode → post-decode/traffic → execute.
  - Confirm OpenResponses auth + JSON media-type check stays in the outer handler before `frontendpipe`.
  - Characterize method/path/auth/content-type error/status precedence so the candidate lane cannot reorder errors.
  - _Validation: `go test -race ./internal/plugins/frontends/frontendpipe/... ./internal/plugins/frontends/openairesponses/... ./internal/plugins/frontends/openailegacy/... ./internal/plugins/frontends/openresponses/...`_
  - _Requirements: 1, 2, 17_

- [x] 1.3 Freeze request-body limits, gzip, decode admission, and route-selector precedence
  - Exact limit and limit+1; chunked/known length; cancellation; gzip canonical behavior.
  - Header selector wins as today; `RouteFromBodyModel` runs only when selector remains empty and while decode permit is held.
  - Full-body resolver, when configured, runs before shared JSON preflight.
  - Characterize decode admission weight/saturation/overweight/cancel/panic-release and `Retry-After` mapping.
  - Add fixture proving the current canonical path applies at most one `TryAdmit` decision per considered request, including terminal `Spec.Decode` failure; true proof/assessment-decline same-permit fallback is owned by Tasks 7.6/11.9, not this task.
  - _Validation: `go test -race ./internal/plugins/frontends/decodeqos/... ./internal/plugins/frontends/reqbody/... ./internal/plugins/frontends/frontendpipe/...`_
  - _Requirements: 1, 2, 3, 4, 6, 13_

- [x] 1.4 Freeze request-generation binding
  - Characterize `GenerationDispatcher` request lease and generation-scoped frontend executor wiring.
  - Add reload-race fixture proving one HTTP request cannot assess against generation N and execute/fallback against generation N+1.
  - Do not redesign public `GenerationExecutor`; this is a characterization/ratchet task.
  - _Validation: `go test -race ./internal/infra/runtimehost/... ./internal/infra/runtimebundle/... ./internal/stdhttp/...`_
  - _Requirements: 1, 5, 6_

- [x] 1.5 Freeze secure-session/A-leg/route-override lifecycle
  - Count principal/scope/session-open/workspace stages, `BeginTurn`, A-leg create/fetch, route-override snapshot/barrier, secure client-turn recorder, B-legs, terminal/finalization, new-session resume-token return, resume/denial/cancel/error paths.
  - Characterize standard memory and Bun continuity stores as route-override-capable compositions.
  - Detached execution stays canonical-only.
  - _Validation: `go test -race ./internal/core/runtime/... ./internal/core/securesession/... ./internal/core/routeoverride/...`_
  - _Requirements: 6, 7, 14, 19_

- [x] 1.6 Freeze frontend response + keepalive behavior
  - OpenAI Responses: response ID/cancellation carrier/timestamp/model/session+resume headers, stream/non-stream, debug helpers.
  - OpenAI Chat: completion ID/timestamp/model/session headers.
  - OpenResponses: `AfterDecode`, `prepareCreateState`, store/continuation, wrappers/options/recorder.
  - Characterize `PreRequestKeepalive`/`holdalive.Wait` and `StreamKeepaliveInterval` context behavior for enabled/disabled/slow-open cases.
  - _Requirements: 17, 18_

- [x] 1.7 Freeze deterministic request/economic identity
  - Characterize `diag.StableCallID`, `StableCallToken`, `StableUnix`, explicit Call.ID precedence, metering checkpoint/fact/source IDs, billing call IDs, trace IDs, response IDs/timestamps.
  - Fixtures: huge strings, escaped Unicode/HTML-sensitive strings, tools/messages/items, model/selector, session-header precedence, optional fields.
  - _Requirements: 15, 16, 18_

- [x] 1.8 Build current Call/authority dependency census
  - Do **not** search only historical `preparedRequest.call`.
  - Trace all production `lipapi.Call` reads/retention and `lipapi.CloneCall` sites reachable from accepted request execution: `prep.call`, identity ingress/backend/conversation baselines, receive-turn facts, terminal evidence, attempt derivation/clamp preview, continuation/interleaved, metering/accounting, billing callbacks, local turn, secret guard, terminal decision, traffic, prompt-cache/compaction, response helpers.
  - Inventory current narrow ports from `executor_config.go`: prompt-cache maintenance; conversation reader/tagger/observer/steering; terminal policy; interleaved; compaction; request token estimator; routing/capability/eligibility; route override; secure-session recorder; accounting; billing; traffic; custom Call callbacks.
  - Classify every entry: `bounded wire fact/view`, `source/digest contract`, `response-only`, or `pre-assessment blocker`.
  - Produce a checked-in evidence table used by Tasks 3, 11, 12, and 19.
  - _Requirements: 5, 13, 14, 15, 19, 22_

- [x] 1.9 Freeze the 26-plane + hook census
  - Enumerate from `feature.StandardPlanes()` / generated manifest, not a manually copied list.
  - Explicitly verify `PlaneSecretGuardExecution`, `PlaneLocalTurnHandlers`, `PlaneTerminalDecisionProvider`.
  - Inventory `hooks.Bus` separately.
  - Characterize occupied Local Turn and Secret Guard as canonical blockers; Terminal Decision blocker unless later source/continuation contract is implemented.
  - _Validation: generator check + plane parity/arch tests_
  - _Requirements: 5, 13, 22_

- [x] 1.10 Capture current-main performance baseline
  - Bodies: 32 KiB, 256 KiB, 1 MiB, 5 MiB, test-only 20 MiB raised limit.
  - Record allocs/op, B/op, ns/op, GC cycles/pause/live+peak heap, decode/encode, provider-open fixture latency, Call clone amplification, production-like composition.
  - Include current #592/#602 optimizations; do not use stale #531-era numbers as the primary baseline.
  - _Requirements: 21_

---

## 2. Configuration and Internal Provider-Neutral Contracts

- [x] 2. Add zero-behavior-change plumbing only

- [x] 2.1 Add `server.large_payload_fast_path` configuration
  - Fields: `enabled`, `threshold_bytes`, `memory_spool_bytes`, `max_inflight_spool_bytes`, `max_semantic_fact_bytes`, `spool_dir`.
  - Default off. Validate positive/overflow relationships and spool directory during candidate generation/reload.
  - Invalid reload preserves last-good generation.
  - Do not change `MaxRequestBodyBytes` defaults.
  - Document plaintext spool and optimization-budget semantics.
  - _Requirements: 1, 2, 20, 22_

- [x] 2.2 Add internal provider-neutral large-body DTOs
  - Define bounded `Source`, `Span`, `BodyMode`, immutable `RewriteSemantics`, protocol `Proof`, `SessionInput`, `ClientTurnShape`, canonical `IdentityDigest`, source digest, assessment request/result/stamp, wire request/domain facts, rewrite plan, `ExecutionResult`, bounded `ResponseFacts`, sensitive session-response carrier.
  - No provider SDK/frontend-specific type, raw arbitrary header bag, prompt text, temp path, or unbounded map.
  - Do not create a DTO mirroring `lipapi.Call`.
  - _Requirements: 4, 6, 7, 8, 9, 14, 16, 18, 22_

- [x] 2.3 Keep public SDK compatibility
  - Do not add mandatory methods to `lipsdk.ExecutorView`.
  - Standard frontend path type-asserts an internal optional large-body capability; absence => canonical.
  - External/manual frontends/executors remain source-compatible/canonical-only.
  - _Requirements: 1, 22_

- [x] 2.4 Add configuration/DTO architecture tests before behavior
  - Feature disabled produces no new request-path object allocation beyond a trivial branch.
  - Core large-body package cannot import provider/frontend packages.
  - Sensitive carrier cannot be accidentally formatted into normal telemetry.
  - _Requirements: 1, 18, 22_

---

## 3. Compile Current Authorities Into Frozen Eligibility + O(1) Static Reject

- [x] 3. Make obviously impossible generations skip spool/scanner work

- [x] 3.1 Extend the existing generated plane descriptor with request access class
  - Add zero `Unclassified` plus `CanonicalRequired`, `MetadataOnly`, `ResponseOnly`, `WireContract` (equivalent names allowed).
  - Annotate **all 26 current production planes** from actual semantics.
  - Do not create a second named plane list.
  - New/unclassified plane fails generation/CI.
  - _Requirements: 5, 13, 22_

- [x] 3.2 Apply non-negotiable initial classifications
  - Occupied `PlaneLocalTurnHandlers` => canonical required.
  - Active Secret Guard execution/guards => canonical required until separately certified streaming guard contract.
  - `PlaneTerminalDecisionProvider` => canonical required unless Task 12 later implements bounded terminal evidence + continuation-source parity; do not classify it response-only merely because SDK input is bounded.
  - Request-mutating hooks/transforms => canonical unless explicit wire contract.
  - Response-only planes remain eligible only after characterization.
  - _Requirements: 5, 13, 19_

- [x] 3.3 Freeze separate hook-bus occupancy/access classes
  - Do not assume hooks are planes.
  - Submit/request-part/tool/request-mutating chains are blockers unless explicit wire contract.
  - Response-only chains require tests proving no request content dependency.
  - _Requirements: 5, 13_

- [x] 3.4 Compile current non-plane/narrow-port capabilities
  - Use Task 1.8 inventory.
  - Represent stock no-op vs blocker vs wire-capable state for traffic, secure recorder, metering/accounting/billing, conversation/steering, route override, counting, etc.
  - Custom `BillingIdentity`/other Call callbacks are blockers unless an explicit bounded fact contract exists.
  - No runtime reflection or arbitrary callback invocation.
  - _Requirements: 5, 14, 15, 19_

- [x] 3.5 Publish bounded generation-frozen `WireEligibilitySummary`
  - Composition-time only, deterministic, generation-pinned.
  - Summary may contain fixed bitsets/enums/small immutable slices; no request-sized data.
  - Unknown fails closed.
  - _Requirements: 5, 6, 22_

- [x] 3.6 Add constant-time static pre-capture disposition
  - Expose only `DefinitelyCanonical` vs `NeedsRequestAssessment` + bounded reason enum.
  - Static disposition **never** says “wire eligible.”
  - `DefinitelyCanonical` performs zero spool/scanner/profile construction and continues through the unchanged canonical body-read path.
  - No map/backend/plugin/store walk; no I/O; target allocation-free hot path.
  - _Requirements: 1, 5, 21_

- [x] 3.7 Add static-disposition ratchets/benchmarks now
  - Tests for Local Turn, Secret Guard, unclassified plane, canonical-only traffic, missing two-phase executor, and a normal potentially eligible generation.
  - Benchmark definitely-ineligible candidate against feature-disabled canonical baseline: no temp file/replay/scanner and negligible overhead.
  - _Validation: `go test ./pkg/lipsdk/feature/... ./internal/archtest/... ./internal/core/runtime/... ./internal/infra/runtimebundle/...`_
  - _Requirements: 5, 21, 22_

---

## 4. Replay Capture, Reservation, and Resource Safety

- [x] 4. Build replay independently of protocol/routing logic

- [x] 4.1 Implement bounded logical spool reservation
  - Known identity length may reserve early; unknown/chunked reserve incrementally with checked `int64` math.
  - Release exactly once on fallback/success/cancel/error.
  - Exhaustion => canonical optimization decline, not new 413.
  - _Requirements: 1, 20, 21_

- [x] 4.2 Implement bounded RAM + secure spill
  - Fixed/reusable copy buffer; no payload-growing `bytes.Buffer`.
  - Private unpredictable file names; restrictive permissions where supported.
  - Preserve current chunk/unwritten suffix until write succeeds.
  - _Requirements: 20_

- [x] 4.3 Implement lossless mid-capture canonical continuation
  - Reader = retained prefix + current unwritten suffix + still-unread request body.
  - Never reread/restart client socket.
  - Preserve same body ceiling/status semantics.
  - Random chunk/fault tests compare byte-for-byte with direct canonical read.
  - _Requirements: 1, 2, 20_

- [x] 4.4 Implement immutable completed source + independent readers
  - Offset-zero fresh reader each open; parallel readers independent.
  - Root close idempotent/nonblocking; pending deletion after root close until readers zero.
  - Windows file deletion covered; no cleanup goroutine required.
  - _Requirements: 10, 20_

- [x] 4.5 Compute source integrity digest during capture
  - Source digest is for replay/attempt evidence only; never substitute for canonical semantic identity.
  - _Requirements: 15, 16, 20_

- [x] 4.6 Fault-injection/privacy/leak tests
  - Reservation/create/short-write/read/remove failures, cancellation, timeout, exact limit/+1, leaked reader.
  - Assert no prompt bytes/spool path/session secret in logs/metrics/errors.
  - _Validation: `go test -race` for new replay package + reqbody/frontend fixtures_
  - _Requirements: 20, 22_

---

## 5. Shared Incremental JSON Safety Scanner

- [x] 5. Match current shared JSON protections without retaining large scalar content

- [x] 5.1 Implement incremental lexer/state machine
  - UTF-8, escapes/surrogates, numbers, delimiters/root/trailing/incomplete, depth/token/object/array/key/string/number/byte limits, cancellation.
  - Fixed buffers; giant string contents not retained.
  - _Requirements: 3_

- [x] 5.2 Expose bounded token/path/span events
  - Exact raw spans for selected top-level values; nested-key discrimination.
  - Provider-neutral scanner; no protocol field names in shared core.
  - _Requirements: 4, 9_

- [x] 5.3 Differential/fuzz against current slice preflight
  - Random buffer splits around UTF-8/escapes/numbers, deep/wide JSON, giant strings, duplicates, malformed/trailing data, exact limits, cancellation.
  - Compare stable error class/aggregate limits, not incidental text.
  - _Validation: `go test ./internal/core/jsonshape/...` + fuzz targets_
  - _Requirements: 3_

---

## 6. Exact Canonical Semantic Identity Without Full Call

- [x] 6. Preserve one request/economic identity namespace

- [x] 6.1 Factor `diag` helpers around an already-computed canonical sum
  - Preserve canonical `StableCallID`, token, Unix outputs byte-for-byte.
  - Add internal `...FromSum`/equivalent helpers; canonical path continues to derive sum from full Call.
  - No behavior change in this subtask.
  - _Requirements: 16_

- [x] 6.2 Define profile hash-writer contract
  - Emit/hash exact canonical stable representation for supported subset with `Call.ID` handling identical to current code.
  - Explicit field order, zero/omitted semantics, normalization, JSON escaping, arrays/maps/options, route/session precedence.
  - Large string contents streamed into hash without retention.
  - _Requirements: 4, 16, 17_

- [x] 6.3 Differential identity corpus/fuzz
  - Decode same body canonically and compare sum + ID/token/Unix + downstream deterministic IDs.
  - Huge Unicode/escaped strings, tools/items/messages, optional controls, route/model/session headers.
  - Any shape that cannot match exactly is removed from profile eligibility.
  - _Requirements: 16, 17, 18_

- [x] 6.4 Prove economic/checkpoint identity parity
  - Same logical request canonical vs wire gets same request/trace and deterministic metering/source/checkpoint identities.
  - Explicit caller IDs retain precedence.
  - _Requirements: 15, 16_

---

## 7. Frontend Candidate Capture and Same-Permit Protocol Proof

- [x] 7. Add candidate ingress without certifying a provider lane yet

- [x] 7.1 Add optional profile plumbing and bounded frontend wire state
  - Profile owns protocol proof, canonical identity digest, recorder shape, session precedence facts, body mode/rewrite semantics, model span, response-state seeds.
  - No backend selection/network inside profile.
  - Nil profile/capability => canonical with no spool.
  - _Requirements: 4, 8, 9, 14, 16, 18_

- [x] 7.2 Preserve each frontend's current outer ordering before candidate logic
  - Do not force a universal auth/content-type sequence.
  - OpenResponses outer auth/media check remains where it is.
  - Shared pipe candidate gates occur only after the frontend's current outer checks.
  - _Requirements: 1, 17_

- [x] 7.3 Apply cheap pre-capture gates in this order
  - feature/profile/two-phase executor available;
  - parsed known identity/uncompressed request length below threshold => canonical;
  - gzip wave 1 => canonical;
  - frozen static disposition `DefinitelyCanonical` => canonical;
  - configured legacy full-body `ResolveRouteSelector` without bounded contract => canonical;
  - only then allocate capture/scanner state.
  - Do not trust compressed Content-Length as decoded length.
  - _Requirements: 1, 2, 5, 11, 13, 21_

- [x] 7.4 Capture to EOF while running shared scanner
  - Preserve body limit/error parity and Task 4 continuation on recoverable decline.
  - Unknown/chunked final size below threshold => canonical from source.
  - _Requirements: 1, 2, 3, 20_

- [x] 7.5 Acquire exactly one decode-admission permit after EOF
  - Weight = exact final decoded bytes.
  - Never hold permit while waiting for client upload/spill writes.
  - Under permit, replay source through protocol proof: selector/default, semantic subset validation, `ClientTurnShape`, `SessionInput`, body/rewrite facts, canonical semantic identity.
  - Legacy full-body route resolver is **not** invoked here; it was a pre-capture canonical gate.
  - _Requirements: 4, 6, 13, 14, 16, 17_

- [x] 7.6 Canonical proof decline under SAME permit
  - Proof decline owns same-permit fallback: materialize/decode from replay with existing `Spec.Decode` while the original admission permit remains held, with no release/reacquire and no second `TryAdmit`/429/503 decision.
  - Release only at today's post-decode boundary and continue normal Validate/AfterDecode/traffic/Execute.
  - Add decode-admission saturation race test proving no second 429/503 decision.
  - _Requirements: 1, 6_

---

## 8. Internal Backend Exact/Domain Wire Capability + HTTP Construction

- [x] 8. Backend support must be pure before commit and transport-owned after commit

- [x] 8.1 Extend internal `execbackend.Backend` additively
  - Optional pure exact `ResolveWireRequest` and late-domain `ResolveWireDomain` (equivalent names allowed).
  - Inputs: profile/operation/delivery/protocol/body mode/rewrite semantics + candidate/model/domain facts.
  - Output can declare rewrite need only if supplied rewrite semantics support it.
  - Nil/unknown => canonical.
  - No external plugin ABI change in V1.
  - _Requirements: 7, 8, 9, 22_

- [x] 8.2 Implement streaming top-level model token splice
  - Exact scanner span + JSON encoded replacement + checked rewritten length.
  - Same/shorter/longer/escaped model, late model, nested misleading text, duplicate/invalid spans.
  - No second whole body.
  - _Requirements: 9_

- [x] 8.3 Prove exact/domain resolver purity
  - No provider I/O, stores, mutable session reads, unbounded plugin work.
  - Domain proof covers exact execution/model domain and same body/rewrite contract.
  - `NeedsModelRewrite=true` without certified span/semantics => incompatible.
  - _Requirements: 6, 8, 9_

- [x] 8.4 Build shared HTTP wire-open primitives by refactoring existing backend logic
  - Reuse endpoint/base URL, credential pool/cooldown, shared client/TLS/proxy/HTTP2/redirect policy, first-recv/stream parser/error classification.
  - Core remains retry owner; prevent hidden SDK retry from creating different attempt economics.
  - _Requirements: 10, 12_

- [x] 8.5 Enforce outbound framing/header security
  - Build provider headers from backend-owned canonical logic; never forward client headers wholesale.
  - No client Authorization/session/control leakage.
  - No stale `Transfer-Encoding`, `Content-Length`, `Content-Encoding`, `Expect`, request Trailer.
  - Exact rewritten length when known.
  - HTTP/1.1 + HTTP/2/cancel/reuse/redirect tests with shared client.
  - _Requirements: 12_

---

## 9. Secure-Session Wire Views and Sensitive Response Carrier

- [x] 9. Keep stock secure-session behavior eligible without prompt materialization

- [x] 9.1 Build exact bounded `SessionInput`
  - Preserve current header/body/session/resume/client-session precedence.
  - Initial profiles may reject body-carried LIP metadata and support authoritative headers only.
  - Resume token never enters backend facts/telemetry.
  - _Requirements: 14, 17_

- [x] 9.2 Refactor fact-based secure-session preparation shared by canonical/wire paths
  - Preserve principal/scope/session opener/workspace/new/resume/denial semantics.
  - Do not split/reimplement the entire executor.
  - `BeginTurn` still happens only after wire commit.
  - _Requirements: 6, 14, 19_

- [x] 9.3 Add bounded recorder input from `ClientTurnShape`
  - Match canonical normalized item/part role/ordinal/kind semantics without prompt text.
  - Semantic-fact budget overflow => pre-commit canonical.
  - Differential tests canonical vs wire recorder input.
  - _Requirements: 14, 21_

- [x] 9.4 Return sensitive session response carrier
  - Authoritative session ID, A-leg ID, raw new-session resume token.
  - Frontend emits exact current session/resume headers.
  - E2E: wire first turn → next canonical/wire request resumes successfully.
  - Assert token absent from logs/metrics/debug output.
  - _Requirements: 14, 18, 22_

---

## 10. Wire-Native Metering, Counting, Accounting, and Billing

- [x] 10. Economic correctness must not re-materialize the request

- [x] 10.1 Add wire-native frontend-ingress checkpoint
  - Same request identity, scope/frontend, count, max-output, timestamp, post-BeginTurn A-leg/session correlation as canonical path.
  - No hidden full Call clone/retention.
  - _Requirements: 15, 16, 19_

- [x] 10.2 Add wire-native backend-attempt checkpoint
  - Attempt/B-leg/backend/effective-model correlation + source/rewrite/attempt digest.
  - Refactor widening/integrity checks to bounded evidence where exact.
  - No hidden Call retained for retry/rerate.
  - _Requirements: 10, 15, 19_

- [x] 10.3 Prove no-accounting + standard metering path first
  - With token accounting/preflight disabled, normal secure-session + metering composition must reach wire mode.
  - Do this before adding optional wire token counter complexity.
  - _Requirements: 15, 21_

- [x] 10.4 Add exact wire token counting only where support is real
  - If accounting/context preflight requires tokens and only `CountCall` exists, dynamic assessment declines under same permit.
  - `WireCounter`/equivalent may scan replay only when exact profile/tokenizer semantics exist **before commit**; do not substitute bytes.
  - Keep permit-hold CPU bounded and measured; if exact counting is expensive/unbounded, leave that composition canonical.
  - _Requirements: 6, 15, 21_

- [x] 10.5 Refactor stock billing/exposure bounded facts
  - Principal/account/pricing/charge/max-output/exposure/terminal identity.
  - Share exact fact helpers with canonical path.
  - Current/custom `BillingIdentity` Call callbacks remain blockers unless explicitly refactored/contracted.
  - Reservations/settlement/idempotency exactly once post-commit.
  - _Requirements: 15, 19_

---

## 11. Implement Pure `AssessLargeBody` and Route Compatibility Envelopes

- [x] 11. This is the last expected fallback point

- [x] 11.1 Implement optional internal assessor/executor interface
  - `AssessLargeBody(ctx, proof) -> Assessment` and `ExecuteLargeBody(ctx, accepted, source) -> ExecutionResult`.
  - Assessment contains opaque generation/proof-bound stamp and bounded facts only.
  - Frontend cannot synthesize route/backend internals.
  - _Requirements: 6, 22_

- [x] 11.2 Add side-effect sentinels before real logic
  - Panic/fail test doubles if assessment touches `BeginTurn`, A-leg, DB/store/route-override read, billing/accounting reservation, provider/network, replay bytes, client wait, or unbounded callback.
  - Measure assessment duration under held decode permit.
  - _Requirements: 6, 21, 22_

- [x] 11.3 Consume frozen authority summary + current dependency census
  - Verify all typed planes/hooks/non-plane ports/callbacks are wire-safe or blockers.
  - Unknown => decline.
  - Re-check static summary defensively; do not redo hot-path reflection/census.
  - _Requirements: 5, 13, 14, 15, 19_

- [x] 11.4 Prove exact initial route candidate set
  - Reuse current alias/default backend/execution composition/native model rules.
  - Preserve sequential/fallback/weighted/race candidate order and membership exactly.
  - Do not prune incompatible candidates: any possible incompatible candidate declines the whole wire request.
  - _Requirements: 7, 8, 10_

- [x] 11.5 Build late route-override compatibility envelope
  - Do not block merely because `RouteOverrideReader` exists.
  - Use the same generation validator/known backend/execution policy to derive all legal outcomes **without reading the live store**.
  - Unbounded override model domain needs backend universal proof such as `AnyAcceptedModel`; otherwise decline.
  - _Requirements: 7, 8_

- [x] 11.6 Handle other late selector authorities conservatively
  - Full-Call route hints/selector mutators are blockers unless explicit bounded route-domain contract.
  - Separate from frontend legacy full-body resolver, already gated before capture.
  - _Requirements: 5, 7, 13, 19_

- [x] 11.7 Prove exact + domain backend wire support
  - Pass immutable body/rewrite facts to every resolver.
  - Any candidate/domain member incompatibility => decline.
  - Test homogeneous same-wire vs heterogeneous incompatible domains and actual post-BeginTurn override changes inside accepted domain.
  - _Requirements: 7, 8, 9, 21_

- [x] 11.8 Bind and validate assessment stamp
  - Stamp binds generation identity, profile/proof identity, source digest/size, body mode/rewrite contract, candidate/domain proof generation.
  - Execute disagreement => invariant failure, never canonical fallback.
  - _Requirements: 6, 8_

- [x] 11.9 Call assessment while SAME decode permit remains held
  - Assessment decline owns same-permit fallback: canonical `Spec.Decode` from replay under the original permit still held, with no release/reacquire and no second `TryAdmit`/429/503 decision (proof-decline fallback is owned by Task 7.6 under the same rule).
  - Accept => release once then commit.
  - Saturation/concurrency tests prove no fallback-induced second admission decision.
  - _Requirements: 1, 6_

---

## 12. Close All Post-Commit Full-Call Dependencies

- [x] 12. Accepted wire execution cannot enter prompt-scale Call/CloneCall machinery

- [x] 12.1 Convert Task 1.8 inventory into explicit bounded runtime wire facts
  - Only facts with named consumers; no shadow Call schema.
  - Route/protocol/max-output/identity/session/source/rewrite/economic facts as required.
  - _Requirements: 19_

- [x] 12.2 Refactor routing/capability/request-size helpers only where exact metadata facts suffice
  - Share helper logic with canonical path to avoid drift.
  - Content-dependent estimator/requirement without exact source contract => blocker.
  - _Requirements: 7, 8, 19_

- [x] 12.3 Refactor receive/terminal/conversation/continuation/interleaved/compaction dependencies conservatively
  - Metadata-only uses => bounded view.
  - Content/trajectory uses => assessment blocker unless an explicit source-backed contract is implemented.
  - Response-only uses remain on canonical events.
  - _Requirements: 13, 19_

- [x] 12.4 Keep Local Turn and Secret Guard canonical in V1
  - Do not attempt to make them wire-safe incidentally while closing generic dependencies.
  - Their occupied planes remain static blockers.
  - _Requirements: 5, 13, 19_

- [x] 12.5 Terminal Decision: either block or implement complete source/continuation parity
  - Default/simple implementation: occupied plane remains canonical blocker.
  - If implementation chooses to support it, it must prove bounded terminal evidence **and** continuation reconstruction from approved source/bounded facts, with full differential tests. Do not support only `DecisionStop` while silently changing potential `DecisionContinue` semantics unless provider capability is statically constrained to stop-only and certified.
  - _Requirements: 5, 13, 19_

- [x] 12.6 Replace stale textual ratchet with real architecture boundary
  - Wire post-commit packages/functions must not accept/dereference `lipapi.Call`, `*lipapi.Call`, or invoke `lipapi.CloneCall` except explicitly whitelisted response-only adapters.
  - Catch `prep.call`, ingress/baseline/terminal clones and future renames through type/import/dataflow-oriented tests, not grep for `preparedRequest.call`.
  - Frontend candidate code cannot materialize a second whole body solely for legacy route resolver.
  - _Validation: `go test ./internal/archtest/... ./internal/core/runtime/... ./internal/plugins/frontends/frontendpipe/...`_
  - _Requirements: 19, 22_

---

## 13. Implement `ExecuteLargeBody` Inside Existing Lifecycle/Attempt Machinery

- [x] 13. No custom miniature executor

- [x] 13.1 Cross one-way commit and begin one logical turn
  - Validate assessment stamp/source ownership.
  - Perform wire secure-session preparation and exactly one `BeginTurn`/A-leg lifecycle.
  - Read live route override only now, constrained to assessed domain.
  - Apply existing request authority/economic admission.
  - Build authoritative response/session facts.
  - No ordinary `Execute` fallback branch.
  - _Requirements: 6, 7, 14, 15, 18, 19_

- [x] 13.2 Reuse existing attempt/recovery ownership
  - Same B-leg allocation, attempt budgets/order, affinity/weighted/interleaved/race, credential retry, TTFT, failure history, first-event commitment, terminal cleanup.
  - Each attempt opens source at zero and applies only approved candidate model splice.
  - Backend parser returns canonical EventStream.
  - _Requirements: 8, 9, 10, 12, 15_

- [x] 13.3 Retry/race/cancel/invariant tests
  - Attempt1 pre-output failure → attempt2 gets complete exact bytes.
  - Parallel readers independent.
  - No failover after first visible event.
  - Cancellation closes readers/source and preserves lifecycle/economic cleanup.
  - Unexpected post-commit content need finalizes one turn and never invokes ordinary `Execute`.
  - Actual route override inside domain executes; outside-domain mismatch is invariant failure (should be unreachable after stamp/domain proof).
  - _Requirements: 6, 7, 10, 20_

---

## 14. Frontend Response-State and Keepalive Bridge

- [x] 14. Preserve protocol response behavior without a fake Call

- [x] 14.1 Refactor bounded shared frontend response context
  - Frontend-owned proof state + `ExecutionResult.ResponseFacts` + sensitive session carrier supply wrapping/writers.
  - Canonical path remains source-compatible.
  - Core does not import frontend response-state types.
  - _Requirements: 18_

- [x] 14.2 Preserve deterministic IDs/timestamps/cancellation
  - Use canonical semantic digest helpers where current canonical behavior uses stable Call hash.
  - OpenAI Responses cancellation remains bound to authoritative A-leg/session carrier.
  - _Requirements: 16, 18_

- [x] 14.3 Preserve session response headers/resume
  - New session returns same session/resume headers; next request resumes.
  - Sensitive token never reaches general logging/metrics/debug.
  - _Requirements: 14, 18, 22_

- [x] 14.4 Preserve `PreRequestKeepalive` and `StreamKeepaliveInterval`
  - Streaming `ExecuteLargeBody` is invoked through same holdalive semantics as current streaming `Execute`.
  - Stream keepalive context remains effective downstream.
  - No holdalive/provider bytes before validation + assessment + one-way commit.
  - Differential tests: enabled/disabled, long assessment, long provider-open, cancellation.
  - _Requirements: 1, 18_

---

## 15. Certify Lane 1: OpenAI Responses → OpenAI-Compatible Responses

- [x] 15. First production lane proves the complete shared architecture

- [x] 15.1 Implement conservative OpenAI Responses profile
  - Confirm at implementation time: no legacy full-body resolver; `RouteFromBodyModel=true`.
  - Exact endpoint, header/body-model selector precedence, stream/max-output/protocol requirements, recorder/session facts, body/rewrite semantics, model span, exact canonical identity.
  - Initial canonical-only: body LIP metadata, duplicate/unknown/normalization-sensitive fields, repair-sensitive histories/aliases, unsupported controls, semantic-fact overflow.
  - _Requirements: 4, 13, 14, 16, 17_

- [x] 15.2 Implement OpenAI-compatible Responses wire proof + `OpenWire`
  - Reuse Task 8 transport/credential/client/parser primitives.
  - Exact/domain proof supports route override only when genuinely universal for declared model/execution domain.
  - _Requirements: 7, 8, 9, 12_

- [x] 15.3 Provider-effective JSON differential suite
  - Canonical vs wire provider method/path/query/relevant headers + parsed JSON semantics after candidate model rewrite.
  - Include escaped/late model and no stale framing headers.
  - _Requirements: 9, 12, 17_

- [x] 15.4 Full E2E conformance
  - Selector precedence; stream mode; errors; canonical response events; stable request/response identity; cancellation; session/resume; secure recorder; metering; retry/failover/race; keepalive; decode-admission saturation fallback.
  - Run HTTP/1.1 + HTTP/2 transport fixtures where supported.
  - Only after green may this lane advertise wire support.
  - _Requirements: 1, 6, 7, 10, 12, 14, 15, 16, 17, 18_

---

## 16. Certify Lane 2: OpenAI Chat Completions → OpenAI-Compatible Chat

- [x] 16. Reuse infrastructure only after Lane 1 is green

- [x] 16.1 Implement conservative Chat profile
  - Confirm no legacy resolver / `RouteFromBodyModel=true` at implementation time.
  - Preserve message/tool/function/reasoning normalization, selector precedence, recorder/session facts, identity, exact model span.
  - Malformed/alias/unknown/duplicate shapes normalized by canonical encoder remain canonical.
  - _Requirements: 4, 13, 14, 16, 17_

- [x] 16.2 Add Chat backend wire support + response bridge
  - Reuse Task 8 transport; preserve completion ID/timestamp/model/session, retry/failover/errors/keepalive.
  - _Requirements: 8, 10, 12, 18_

- [x] 16.3 Differential/E2E certification
  - Same economic/secure-session/route/transport/response criteria as Lane 1.
  - Do not enable until its own corpus is green.
  - _Requirements: 17, 18, 21_

---

## 17. Certify Lane 3: OpenResponses HTTP Create, Explicit No-Store Only

- [x] 17. Do not treat default OpenResponses create as stateless

- [x] 17.1 Characterize/refactor only bounded no-store frontend state
  - Initial subset: HTTP create, **explicit `store:false`**, no `previous_response_id`, no compaction, no WebSocket.
  - Missing `store` stays canonical because current decode defaults true.
  - Preserve outer auth + JSON content-type ordering.
  - Prove no `AfterDecode` side effect/error moved after commit.
  - _Requirements: 1, 17, 18_

- [x] 17.2 Implement no-store OpenResponses proof + compatible backend wire support
  - Strict duplicate/field limits, selector precedence, body/rewrite, identity, endpoint/client/parser/error behavior.
  - `store:true`/continuation/unknown controls canonical.
  - _Requirements: 4, 8, 9, 12, 16, 17_

- [x] 17.3 Differential/E2E certification
  - Provider-effective JSON, response state/options/IDs, secure-session/metering, retry/failover/cancel/keepalive.
  - Assert missing/true store never reaches wire backend.
  - _Requirements: 17, 18_

- [x] 17.4 Leave storage/continuation as separate future certification
  - Requires exact reservation/response-ID/recorder/cleanup/lineage/trajectory parity.
  - Do not expand incidentally.
  - _Requirements: 17, 19_

---

## 18. Gzip Follow-Up Wave

- [x] 18. Compression remains canonical until separately proven

- [x] 18.1 Prove wave-1 gzip always bypasses candidate capture/profile
  - Existing decoded-limit/error behavior unchanged.
  - Compressed Content-Length never used as decoded threshold/reservation fact.
  - _Requirements: 2, 11_

- [x] 18.2 Optional later decoded-gzip replay source
  - Reuse exact current bounded decompression semantics.
  - Threshold/reservation in decoded bytes; remove stale outbound encoding/framing.
  - Represent decoded body mode explicitly; rerun scanner/profile/identity/backend/transport differential suites.
  - Do not implement in initial rollout unless earlier lanes are already certified and evidence justifies it.
  - _Requirements: 11, 12_

---

## 19. Performance, Practical Eligibility, and Observability Evidence

- [x] 19. Prove material value on current main, not just correctness

- [x] 19.1 Add bounded diagnostics
  - considered / static-canonical / captured / profile-proven / assessment-eligible / wire / canonical counts.
  - Static decline enum including local_turn, secret_guard, terminal_decision, frontend_route_resolver, traffic, accounting/counting, custom_call_callback, backend_domain, etc.
  - Size bucket, memory/file spill, replay/rewrite counts, stage latencies, active spool bytes.
  - No backend/model/user/session IDs, body/path/spool path/resume token in labels/logs.
  - _Requirements: 20, 22_

- [x] 19.2 Benchmark all required sizes/stages
  - 32 KiB, 256 KiB, 1 MiB, 5 MiB, test-only 20 MiB.
  - Giant string, late model, tools, malformed JSON, canonical fallback, replay/failover.
  - allocs/op, B/op, CPU, GC cycles/pause/live+peak heap, capture/proof/assessment/provider-open, file I/O.
  - Decode permit never held during upload/spill I/O.
  - Compare to Task 1.10 current-main baseline.
  - _Requirements: 6, 21_

- [x] 19.3 Enforce accepted-lane no-payload-heap invariant
  - Heap/profile evidence must show no payload-sized `[]byte`/`string`, full Call/item/message tree, or payload-scale `CloneCall` on accepted spill-backed wire path.
  - Retained request heap bounded by memory spool + semantic fact budget + fixed buffers/metadata.
  - Size-scaling 1 MiB → 5 MiB → 20 MiB must be approximately flat/bounded; material body-proportional slope = failed optimization gate unless removed.
  - _Requirements: 19, 21, 22_

- [x] 19.4 Benchmark static blocker overhead
  - Feature enabled but `DefinitelyCanonical` generation/profile must be near disabled/current canonical baseline.
  - Assert no temp file/replay/scanner/profile construction.
  - Include Local Turn/Secret Guard canonical blocker examples.
  - _Requirements: 5, 21_

- [x] 19.5 Concurrent load + spool saturation
  - Realistic sessions, slow uploads, concurrent accepted requests, races/fallback, budget saturation, cancellation.
  - Compare GC/heap/latency with canonical baseline.
  - Spool budget is optimization budget, not global OOM admission.
  - _Requirements: 20, 21_

- [x] 19.6 Publish current-runtime eligibility matrix
  - All 26 planes; hook categories; Local Turn/Secret Guard/Terminal Decision; traffic; secure recorder; metering; accounting; billing; conversation/steering; route override homogeneous/heterogeneous; sequential/fallback/race; each protocol lane; legacy resolver; static disposition.
  - At least one normal secure-session + metering production-like configuration must actually execute wire mode.
  - Quantify blockers rather than hiding them.
  - _Requirements: 5, 7, 13, 14, 15, 21_

- [x] 19.7 ROI decision per lane
  - Report CPU/file-I/O tradeoff separately from heap savings.
  - If a lane cannot show worthwhile multi-MiB benefit under realistic concurrency, leave it canonical-only; do not weaken correctness or enable for benchmark optics.
  - _Requirements: 21_

---

## 20. Final Architecture/Regression Gate and Default-Off Rollout

- [x] 20. No lane ships before complete evidence

- [x] 20.1 Final architecture ratchets
  - No unclassified production plane/hook/non-plane request authority.
  - No provider-name switch/provider SDK type in generic core large-body code.
  - No second plane classification registry.
  - No fake/shadow Call.
  - No `lipapi.Call`/`CloneCall` ingress into accepted post-commit wire boundary except explicit whitelisted response-only adapter.
  - No expected canonical fallback after commit.
  - No protocol proof outside decode admission.
  - No bypass of static `DefinitelyCanonical` disposition or configured legacy full-body resolver.
  - No public SDK widening without separate review.
  - _Requirements: 5, 6, 13, 19, 22_

- [x] 20.2 Full regression/quality gates
  - Targeted characterization/differential/fuzz/identity/route/session/economic/transport/keepalive suites.
  - Full repository unit/integration tests, race suites required by repo policy, static/arch checks, formatter/linter, Kiro spec checker.
  - Do not dismiss unrelated existing failure as caused by this feature without evidence; record baseline-vs-branch distinction.
  - _Requirements: 22_

- [x] 20.3 Default-off production rollout
  - Config default remains disabled.
  - Only lanes whose Task 15/16/17 certification and Task 19 ROI gate pass advertise support.
  - Unknown/new runtime authority => canonical.
  - Documentation explains full-body prevalidation, spool confidentiality, memory-vs-I/O tradeoff, metrics/decline reasons, and rollback toggle.
  - _Requirements: 20, 21, 22_

- [x] 20.4 Completion evidence for #532/#503
  - Checked-in current-start SHA + canonical baselines.
  - Current authority/Call dependency census.
  - Plane/non-plane eligibility matrix.
  - Differential protocol/transport/identity/secure/economic evidence.
  - Retry/race/cancel/resource evidence.
  - Accepted-lane heap-scaling and current-main ROI evidence.
  - Static-blocker overhead evidence.
  - Full QA/race/static results.
  - Do not close #532 until all applicable workstreams are complete or explicitly documented as intentionally canonical-only follow-ups under the requirements.
  - _Requirements: 21, 22_

## Implementation Notes

- Task 1.1 at `3da34d7875443355d65cb9d7df649555dfad3edb` has unchanged runtime seams vs `b08c608` baseline but full archtest and focused billing docs test failed at that SHA due to upstream `product.md`/`structure.md` marker removal; evidence `evidence/1.1-rebaseline.md`; no downstream workaround or production changes. Repaired by `caa38dc9` (cherry-pick of upstream fix `a640123c` restoring billing-exposure contract markers); `go test -count=1 -timeout=10m ./internal/archtest` now passes on the feature worktree.
- Task 1.2 test-only scope VERIFIED (independent reviewer APPROVED): fresh `go test -count=1` PASS exit 0 for 4 frontend packages (`frontendpipe`, `openairesponses`, `openailegacy`, `openresponses`); gofmt and diff check clean. Windows `go test -race` for same packages failed on `cgo.exe` exit 2 (Windows race/cgo toolchain limitation, not a test failure); future race certification needs working toolchain.
- Task 1.3 approved correction applied: 1.3 characterizes current canonical one-`TryAdmit` decision including terminal decode failure; Task 7.6 owns proof-decline same-permit fallback and Task 11.9 owns assessment-decline same-permit fallback with the original permit held and no second decision; Requirement 6.3 preserved.
- Task 1.4 test-only scope VERIFIED (fresh reviewer APPROVED): required suites ALL PASS on feature branch (`runtimehost`, `runtimebundle` incl. repaired candidate test, `stdhttp`); repair attribution `3054bc43`/`dc5f42af` retained; gofmt/diff-check clean; `-race` skipped per Windows cgo limitation.
- Task 1.5 test-only scope VERIFIED (fresh reviewer APPROVED): 3 lifecycle freeze test files; required 3-package suites PASS (`internal/core/runtime`, `internal/core/securesession`, `internal/core/routeoverride`); gofmt/diff-check clean; no production diff; Bun continuity via existing suites; future seams disclaimed; `-race` skipped per Windows cgo limitation.
- Task 1.6 test-only scope VERIFIED (fresh reviewer APPROVED): 4 response/keepalive freeze files, 26 new tests, per-lane coverage; touched suites PASS; vet/gofmt/diff-check clean; no production diff; future bridge seams disclaimed; `-race` skipped per Windows cgo limitation.
- Task 1.7 test-only scope VERIFIED (fresh reviewer APPROVED): 2 identity freeze files, 21 tests, per-requirement coverage (Req 15, 16, 18); diag + checkpoint suites PASS; vet/gofmt/diff-check clean; no production diff; future digest seams disclaimed; `-race` skipped per Windows cgo limitation.
- Task 1.8 test-only scope VERIFIED (fresh reviewer APPROVED): evidence `evidence/1.8-call-census.md` with CloneCall register + narrow-port inventory + classifications, handoff for Tasks 3/11/12/19; archtest PASS, diff-check clean; no production diff.
- Task 1.9 test-only scope VERIFIED (fresh reviewer APPROVED): 26-plane census test + evidence from `feature.StandardPlanes()`/generated manifest, explicitly naming `PlaneSecretGuardExecution`, `PlaneLocalTurnHandlers`, `PlaneTerminalDecisionProvider`; `hooks.Bus` inventoried separately; Local Turn/Secret Guard canonical blockers and fail-closed ratchet for unclassified/new planes; handoff for Task 3; archtest + feature suites PASS, vet/gofmt/diff-check clean; no production diff; `-race` skipped per Windows cgo limitation.
- Task 1.10 test-only scope VERIFIED (fresh reviewer APPROVED): baseline harness + evidence covering 32 KiB, 256 KiB, 1 MiB, 5 MiB, test-only gated 20 MiB with allocs/B/ns, GC, decode/encode, provider-open, clone amplification metrics including current #592/#602; package tests + benchmark slices PASS, gofmt/diff-check clean; 20 MiB properly gated; no production diff; handoff for Task 19; `-race` skipped per Windows cgo limitation.
- Task 2.1 VERIFIED (fresh reviewer APPROVED): six-field default-off `server.large_payload_fast_path` config with validation + invalid-reload last-good preservation; `MaxRequestBodyBytes` untouched; config package + archtest PASS, vet/gofmt/diff-check clean; budgets.go bump justified per procedure; `-race` skipped Windows cgo limitation.
- Task 2.2 VERIFIED (fresh reviewer APPROVED): `internal/core/largebody` provider-neutral DTO seam (bounded/immutable/redacted, no Call mirror, no SDK/frontend imports, no prompt/path/header-bag/unbounded maps); core ownership + budget ratchets PASS; largebody 17/17 + focused archtest PASS, vet/gofmt/diff-check clean; full archtest 18.8s implementer-claimed, focused subsets re-verified; no consumers yet; `-race` skipped Windows cgo limitation.
- Task 2.3 VERIFIED (fresh reviewer APPROVED): internal LargeBodyExecutor + AsLargeBodyExecutor helper, ExecutorView unchanged, absent=>canonical, budgets bump; largebody 21/21 + line-budget PASS, vet/gofmt/diff-check clean, pkg/lipsdk untouched; `-race` skipped Windows cgo limitation.
- Task 2.4 VERIFIED (fresh reviewer APPROVED): disabled-gate 0-alloc ratchet, core import boundary, sensitive-carrier redaction; SessionInput IDs spec-compliant clear-by-contract, Task 19.1 diagnostics must not use IDs as labels (follow-up); largebody 26/26 + boundary test PASS, vet/gofmt/diff-check clean; `-race` skipped Windows cgo limitation.
- Task 3.1 VERIFIED (fresh reviewer APPROVED): RequestBodyAccess on sole plane descriptor, 26 annotations 19/3/4/0/0, fail-closed generation+CI, no eligibility consumption yet; feature 149 tests + archtest PASS, generator -check + vet/gofmt/diff-check clean; `-race` skipped Windows cgo limitation.
- Task 3.2 VERIFIED (fresh reviewer APPROVED): non-negotiable blocker ratchets (Local Turn/Secret Guard/Terminal Decision canonical, request-mutating hooks canonical unless explicit wire contract, response-only only after characterization); 3.1 values already truthful; feature suite PASS, vet/gofmt/diff-check clean; zero production diff; test-only hardening scope, `-race` skipped Windows cgo limitation.
- Task 3.3 VERIFIED (fresh reviewer APPROVED): Bus 4-chain freeze (submit/request-part/tool/request-mutating blockers unless explicit wire contract, response-only proof with no request content dependency, no plane conflation; note: response-only wire-eligibility SUPERSEDED by Blocker 2 conservative fail-safe: occupied response-part hook chains block wire eligibility until deferred response pipeline reuse is implemented; follow-up to PR #629 review); hooks suite PASS, gofmt/diff-check clean; test-only scope, no production diff; `-race` skipped Windows cgo limitation.
- Task 3.4 VERIFIED (fresh reviewer APPROVED): 46-row narrow-port freeze from Task 1.8 inventory (stock no-op vs blocker vs wire-capable states, custom Call callbacks blockers unless explicit bounded fact contract, no reflection/arbitrary invocation); runtime suite PASS, vet/gofmt/diff-check clean; test-only scope, no production diff; `-race` skipped Windows cgo limitation.
- Task 3.5 VERIFIED (fresh reviewer APPROVED): WireEligibilitySummary composition-time/deterministic/pinned/bounded/fail-closed, fixed bitsets/enums, no request-sized data, budgets bump; largebody + feature + archtest PASS, vet/gofmt/diff-check clean; `-race` skipped Windows cgo limitation.
- Task 3.6 VERIFIED (fresh reviewer APPROVED): StaticDisposition two-state (DefinitelyCanonical vs NeedsRequestAssessment) + bounded reason, allocation-free, generation-pinned, budgets bump; largebody + archtest PASS, gofmt/diff-check clean; `-race` skipped Windows cgo limitation.
- Task 3.7 VERIFIED (fresh reviewer APPROVED): static-disposition ratchets (Local Turn, Secret Guard, unclassified plane, canonical-only traffic, missing two-phase executor, normal eligible generation) + benchmarks (0-alloc ~11-14ns vs 4ns baseline); largebody + feature/archtest/runtime/runtimebundle suites PASS, vet/gofmt/diff-check clean; zero production diff; `-race` skipped Windows cgo limitation.
- Task 4.1 VERIFIED (fresh reviewer APPROVED): SpoolLedger/SpoolReservation ledger with checked int64, exact-once release, exhaustion => decline not 413, no filesystem yet; largebody + budget gate PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation.
- Task 4.2 VERIFIED (fresh reviewer APPROVED): SpillBuffer bounded RAM + 0600 unpredictable spill, suffix preservation, reservation integration, budgets bump; largebody + archtest PASS, budget recount verified, diff-check/gofmt/vet clean; -race unavailable cgo limitation.
- Task 4.3 VERIFIED (fresh reviewer APPROVED): CaptureReader/CaptureRequestBody lossless continuation, forward-only socket, same ceiling, suffix guard, budgets bump; largebody + budget gate PASS, diff-check/gofmt/vet clean; -race unavailable cgo limitation.
- Task 4.4 VERIFIED (fresh reviewer APPROVED): CompletedSource offset-zero readers, parallel independence, idempotent nonblocking close, pending deletion, budgets bump; follow-up: pre-completion reader tracking gap noted for 4.6; largebody suite + archtest budget PASS, vet/diff-check clean; -race unavailable cgo limitation.
- Task 4.5 VERIFIED (fresh reviewer APPROVED): incremental SHA-256 source digest, evidence-only distinct from IdentityDigest, unwritten suffix never hashed, budgets bump; largebody suite + budget gate PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation.
- Task 4.6 VERIFIED (fresh reviewer APPROVED): fault-injection/privacy/leak test suite (reservation/create/short-write/read/remove failures, cancellation, timeout, exact limit/+1, leaked reader, no prompt/spool/secret leaks); test-only, Task 4.4 gap characterized without production change; largebody + archtest PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation.
- Task 5.1 VERIFIED (fresh reviewer APPROVED): incremental Scanner in jsonshape, chunked feeds, UTF-8/escape/number/limits/cancel, no giant-string retention, differential parity vs preflight oracle, budgets bump; jsonshape + archtest PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation.
- Task 5.2 VERIFIED (fresh reviewer APPROVED): path-tracked token events with exact spans, nested-key discrimination, provider-neutral caller-selected keys, budgets bump; jsonshape + budget gates PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation.
- Task 5.3 VERIFIED (fresh reviewer APPROVED): differential corpus + fuzz harness (191 differential subtests, 15s fuzz 1.2M execs 0 failures), Kind-only parity decision vs slice preflight, surrogate-split scanner panic fix independently verified, no request-path wiring; jsonshape suite PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation.
- Task 6.1 VERIFIED (review subagent APPROVED): FromSum factoring, byte-for-byte parity, Call.ID precedence, budgets bump; diag + archtest PASS, diff-check/gofmt clean; follow-ups noted (explicit-ID early-return optimization + dead wrapper removal); -race unavailable cgo limitation.
- Task 6.2 VERIFIED (review subagent APPROVED): CallIdentityWriter hash-writer, streaming escape parity, Call.ID identical, no retention, budgets bump; largebody + diag + archtest PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation.
- Task 6.3 VERIFIED (review subagent APPROVED): differential corpus + fuzz cover Items path incl. empty-text fix, marshal-faithful F1 resolution, oracle untouched; suites + fuzz PASS, diff-check/vet/gofmt clean; -race unavailable cgo limitation.
- Task 6.4 VERIFIED (review subagent APPROVED): economic/checkpoint parity on digest seam, caller-ID precedence, Task 10 obligation documented, zero production diff; 41 parity tests PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation.
- Task 7.1 VERIFIED (review subagent APPROVED): FrontendProfile + wire state/seeds plumbing, nil=>canonical no-spool, zero ServeHTTP change, budgets catch-up bump; frontendpipe + archtest PASS, vet/diff-check clean; -race unavailable cgo limitation.
- Task 7.2 VERIFIED (review subagent APPROVED): per-frontend outer ordering freeze characterized across 4 frontends (no universal sequence, OpenResponses auth/media intact); reviewer suggestion/obligation noted for Task 7.3 to land production ServeHTTP candidate-gate proof; 4 frontend suites PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation.
- Task 7.3 VERIFIED (review subagent APPROVED): five cheap gates in order wired into ServeHTTP after outer checks, zero spool on decline, off/nil unchanged; frontendpipe suite PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation.
- Task 7.4 VERIFIED (review subagent APPROVED): capture-to-EOF with scanner feed, parity, lossless continuation, below-threshold canonical-from-source; frontendpipe + largebody suites PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation.
- Task 7.5 VERIFIED (review subagent APPROVED): single exact-weight permit post-EOF, proof replay under permit, same-permit decline fallback, legacy bypass, 429 parity; frontendpipe + largebody + decodeqos + archtest PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation; reviewer flags noted: capture-time materialization to revisit in 8+, partial 7.6 overlap, unreachable defensive branch.
- Task 7.6 VERIFIED (re-review subagent APPROVED): proof-decline same-permit fallback saturation race proof, single TryAdmit, no second decision, vet fix; saturation-race suite PASS, vet/diff-check clean; -race unavailable cgo limitation.
- Task 8.1 VERIFIED (review subagent APPROVED): additive pure ResolveWireRequest/ResolveWireDomain, fail-closed rewrite rule, nil=>canonical, no ABI change, budget + hexagonal baseline updates; execbackend + largebody + archtest PASS, vet/build/gofmt/diff-check clean; follow-ups: configured budget to replace 1024 magic, protocol binding proof in 8.3; -race unavailable cgo limitation.
- Task 8.2 VERIFIED (review subagent APPROVED): streaming model splice, exact spans, checked length, duplicate rejection, budgets bump; largebody + jsonshape + budget gates PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation.
- Task 8.3 VERIFIED (review subagent APPROVED): resolver purity proofs, configured semantic-fact budget replacing 1024 magic, protocol binding, budgets bump; execbackend + largebody + archtest PASS, vet/gofmt/diff-check clean; -race unavailable cgo limitation.
- Task 8.4 VERIFIED (review subagent APPROVED): shared credential/streampeek/wire-open primitives via refactor, behavior preserved, core retry ownership intact; 21 backend packages PASS, build/vet/gofmt/diff-check clean; follow-ups: DefaultSDKMaxRetries wiring, 8.5+ must consume helpers; -race unavailable cgo limitation.
- Task 8.5 VERIFIED (review subagent APPROVED): backend-owned outbound headers, auth/session/framing stripping with Connection-token awareness, exact rewritten Content-Length, cleared trailers, shared-client HTTP/1.1+HTTP/2/cancel/reuse/redirect conformance; openaicompat suite PASS, vet/gofmt clean; dead redirect probe variable removed; -race unavailable cgo limitation.
- Task 9.1 VERIFIED (review subagent APPROVED): exact bounded SessionInput with header-over-body precedence, fail-closed body-metadata rejection, SensitiveString resume wrapping with String/GoString/Format/LogValue/JSON redaction, no session/token fields in backend facts; largebody + sessionwire suites PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 9.2 VERIFIED (review subagent APPROVED): shared fact-based PrepareSecureSession/PreparedSecureSession seam, BeginTurn strictly post-commit via ExecuteBeginTurn, canonical parity + no-early-turn + resume/denial + workspace fail-closed proofs, dead helper removed; runtime suite PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 9.3 VERIFIED (review subagent APPROVED): bounded ClientTurnShape recorder input with canonical NormalizedItems parity, no prompt-text retention, budget overflow => ErrSemanticFactBudgetExceeded pre-commit canonical; largebody + runtime suites PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 9.4 VERIFIED (review subagent APPROVED): sensitive SessionResponseCarrier with IsNew-only raw token, canonical-delegated exact session/resume/A-leg headers, wire→canonical→wire E2E resume + denial, token absent from logs/metrics/body; runtime + sessionwire + openairesponses suites PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 10.1 VERIFIED (review subagent APPROVED): wire-native frontend-ingress checkpoint with shared exact identity/quantity builders, canonical field-by-field parity, zero Call clone/retention, post-BeginTurn A-leg/session correlation, unused quantity field removed; checkpoint + runtime suites PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 10.2 VERIFIED (review subagent APPROVED): wire-native backend-attempt checkpoint with shared public builder, attempt/B-leg/backend/model correlation + source/rewrite/attempt digests, widening refactored to shared exact helper + bounded evidence assert, zero Call retention, dead snapshot method + stale comment removed; checkpoint + runtime suites PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 10.3 VERIFIED (review subagent APPROVED): no-accounting + standard metering composition reaches wire mode with FE/BE fact persistence, accounting-enabled correctly declines wire pre-Counter, wire snapshots skip deferred counting, ctx-holder fallback, zero Call retention; runtime + checkpoint suites PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 10.4 VERIFIED (review subagent APPROVED): exact WireCounter gate with pre-commit tokenizer semantics, CountCall-only/inexact/unbounded/timeout decline under same permit, 5MiB/250ms bounded measured counting, CheckWire limit parity, zero Call retention; largebody + runtime + preflight suites PASS, vet/gofmt clean; -race unavailable cgo limitation; archtest docs-gate failure pre-existing at HEAD, unrelated.
- Task 10.5 VERIFIED (review subagent APPROVED): stock billing/exposure on bounded facts with shared exact credit/identity/closure helpers, WireBounded identity contract with custom-callback blockers, adapter bounded fallbacks, exactly-once post-commit reservation/settlement, shared account-fallback helper + comment fix; runtime + billing + compose + admission suites PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 11.1 VERIFIED (review subagent APPROVED): split LargeBodyAssessor/WireExecutor ports with bare-Proof assessment, opaque generation-bound stamp + bounded facts Assessment with strict Validate + constructors, generation binding moved assessor-side, public ExecutorView untouched; largebody + frontendpipe + archtest suites PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 11.2 VERIFIED (review subagent APPROVED): fail-closed panic sentinel harness for all 8 side-effect surfaces + held-permit duration measurement with ceiling and single-decision enforcement, test-only, no production change; largebody suite PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 11.3 VERIFIED (re-review subagent APPROVED after empty-generation fail-open fix): authority assessment gate over frozen summary + census with unknown=>decline, defensive sealed/pinned/blocker recheck, manifest round-trip parity, no hot-path reflection, sentinel-untouched; empty-generation regression test added; largebody suite PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 11.4 VERIFIED (re-review subagent APPROVED after thinker-role fix): exact initial candidate set via canonical composition reuse with planner-identical thinker/executor role+key tagging, whole-request decline on any incompatibility without pruning, DeclineReasonCanceled taxonomy addition; routing + largebody suites PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 11.5 VERIFIED (re-review subagent APPROVED after nil-validator fail-open fix): late route-override envelope from shared generation validator without live-store reads, reader presence never blocks alone, unbounded domain requires AnyAcceptedModel, nil validator fails closed with regression test; routing + largebody + runtimebundle suites PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 11.6 VERIFIED (re-review subagent APPROVED after envelope-composition fixes): late selector authorities fail closed without bounded contracts, per-backend universal proof for mixed envelopes, union (never replace) envelope composition across assessors, legacy resolver out of scope; largebody suite PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 11.7 VERIFIED (review subagent APPROVED): exact+domain backend proof gate with immutable body/rewrite facts to every resolver, whole-request decline without pruning, homogeneous accept vs heterogeneous decline, post-BeginTurn inside-domain override proof, strict rewrite-mismatch union rule; largebody + routing + execbackend suites PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 11.8 VERIFIED (review subagent APPROVED): 7-point stamp binding with execute-time revalidation as terminal invariant failure (never fallback), backward-compatible constructor, no sensitive leakage; largebody suite PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 11.9 VERIFIED (review subagent APPROVED): same-permit assessment with release-once-then-commit on accept and same-permit canonical fallback on decline, saturation proofs of no second admission, execute-panic close safety; frontendpipe suite PASS, vet/gofmt clean; -race unavailable cgo limitation; archtest core-ratchet failures pre-existing, untouched by this task.
- Task 12.1 VERIFIED (re-review subagent APPROVED after real-assertion fixes): explicit 8-domain bounded wire facts with mechanical consumer-coverage ratchet and structural no-shadow-Call assertion, census evidence doc; largebody suite PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 12.2 VERIFIED (review subagent APPROVED): shared PrepareSelector across canonical/wire (no drift), exact size-constraint blocker via canonical predicate, metadata-only failover-set seam, TurnFacts evaluation; routing + capabilities + runtime + largebody suites PASS, vet/gofmt clean; -race unavailable cgo limitation; TestFault_CancellationAndTimeout flake + archtest/qa failures proven pre-existing at base.
- Task 12.3 VERIFIED (review subagent APPROVED): metadata-only bounded views, content/trajectory blockers, response-only on canonical events (note: response-only wire-eligibility SUPERSEDED by Blocker 2 conservative fail-safe: occupied response-only planes block wire eligibility because wire streaming bypasses response machinery; deferred response pipeline reuse; follow-up to PR #629 review), census additions, wire response-evidence helper (production wiring deferred; secure-turn/identity-fallback gaps recorded for 12.4/14); largebody + runtime suites PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 12.4 VERIFIED (review subagent APPROVED): Local Turn + Secret Guard non-negotiable canonical blockers with compile-time static bits, assessment-time census decline, anti-tamper weakened-access proofs, SDK declaration pinning; largebody suite PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 12.5 VERIFIED (review subagent APPROVED): terminal-decision default-block via exact-ID + census + substring triple coverage with anti-tamper proofs, no DecisionContinue semantic change; largebody suite PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 12.6 VERIFIED (review subagent APPROVED): type/dataflow archtest boundary (go/packages+go/types, rename-resistant) with explicit whitelists wired into all scans, RED mutations proven, no body-double for legacy resolver; archtest + runtime + frontendpipe suites PASS, vet/gofmt clean; -race unavailable cgo limitation; full-suite archtest failures pre-existing at base.
- Task 13.1 VERIFIED (review subagent APPROVED): one-way wire commit with stamp/source validation, shared prep + single BeginTurn/A-leg, domain-constrained live override, shared authority/economic admission with release discipline, authoritative response/session facts, no Execute fallback; runtime + largebody suites PASS, vet/gofmt clean; -race unavailable cgo limitation; carry to 13.2/13.3: propagate admitted BillingCallID, body-size request exposure, mandatory per-request WireIdentity, consume BindSession view.
- Task 13.2 VERIFIED (re-review subagent APPROVED after 4-findings remediation): real attempt execution reusing canonical ownership (B-leg/affinity/budgets/TTFT/failure-history/terminal legs), offset-zero bodies + approved splice only, winner-ctx survival, recoverability classification, per-leg BE checkpoints, terminal cleanup on all paths; runtime + largebody + execbackend suites PASS, vet/gofmt clean; -race unavailable cgo limitation; carry: winner-leg outcome-by-cause vs Failed-on-success, sequential-exhaustion last-cause in FinalError.
- Task 13.3 VERIFIED (re-review subagent APPROVED after early-close + race fixes): full recovery matrix (exact-bytes failover, reader independence, first-event commitment, cancel cleanup with Canceled mapping, post-commit finalization without Execute, in/out-of-domain override), mutex-guarded stream error state, Cancel-path regression test; runtime suite PASS, vet/gofmt clean; -race unavailable cgo limitation.
- Task 14.1 VERIFIED (inline controller review APPROVED; review subagent spawn failed on provider `astra-model` field, checklist run manually): frontend-owned ResponseContext coupling proof state + ResponseFacts + session carrier + stream, NewResponseContext/Validate/WriteSessionHeaders accessors, optional Spec WireWrapStream/WireWriteStream/WireWriteNonStream driving wire path with session headers + OnWireCommit exposure, trivial ExecutionResult.ResponseFacts accessor, core-import isolation test; frontendpipe suite PASS, TestResponseContext 6/6 PASS, vet/gofmt clean, no TBD/secrets; largebody full-suite 150s timeout proven unrelated (single TestFault_CancellationAndTimeout re-run PASS 0.017s); -race unavailable cgo limitation.
- Task 14.2 VERIFIED (review subagent APPROVED after 1 remediation round: genuine mutation RED evidence in `evidence/14.2-red-phase.md`, six unused IdentityDigest helpers deleted with identity.go restored to HEAD): ResponseContext deterministic token/time/ResponseID/OpenAIResponseID/OpenAIMessageID/ChatCompletionID/AnthropicMessageID parity with diag.Stable* (Req 16.4/16.6/18.7), carrier-bound CancellationID/OpenAIResponseID with Format/Parse round-trip + openairesponses Handler cancel E2E incl. 400-on-unbound-ID (Req 18.6), DeterministicToken seed field + validation; frontendpipe + largebody + openairesponses suites PASS, vet/gofmt clean, no TBD/secrets; -race unavailable cgo limitation.
- Task 14.3 VERIFIED (review subagent APPROVED after 1 remediation round: discriminating exact-shape String/LogValue assertions + compile-time interface conformance vars, genuine method-deletion RED evidence in `evidence/14.3-red-phase.md`): wire/canonical session+resume header parity incl. DeepEqual full-map + empty cases, turn-2 resume via ApplyAuthoritativeHeaders/BuildSessionInput, SensitiveString/carrier redaction across %v/%+v/%#v/%s/%q/GoString/slog/ServeHTTP body+logs (Req 14.6/14.7, 18.2, 22.2-22.3); frontendpipe suite PASS, vet/gofmt/diff-check clean, no TBD/secrets; -race unavailable cgo limitation.
- Task 14.4 VERIFIED (review subagent APPROVED first round, genuine RED evidence in `evidence/14.4-red-phase.md`): Config.executeLargeBody mirrors canonical Config.execute holdalive shape exactly (stream wraps wire ExecuteLargeBody in holdalive.Wait, non-stream bypasses), pre-commit silence structurally guaranteed, keepalive ctx + cancellation preserved; 8 differential tests (102 emit on/off, non-stream bypass, long-assessment silence incl. decline, cancel, interval effective/default); frontendpipe suite PASS, vet/gofmt/diff-check clean, no TBD/secrets; -race unavailable cgo limitation.
- Task 15.1 VERIFIED (review subagent APPROVED; no RED evidence file, implicit RED only): conservative OpenAI Responses profile (no legacy resolver, RouteFromBodyModel=true, header-to-body selector parity, scanner duplicate/unknown/controls declines, body-LIP-metadata + malformed-history + fact-overflow canonical-only, exact digest parity via 6 tests + 9-case corpus, scanner model span + rewrite, session/recorder facts); openairesponses + frontendpipe suites PASS, vet/gofmt/diff-check clean; CARRY: transient O(payload) proof-time allocation must be reworked/justified before Req 21 gates, pre-existing archtest budget failures need repair before PR, follow-up differential cases (large-string corpus, carrier-format test) recommended.
- Task 15.2 VERIFIED (review subagent APPROVED after 1 remediation round: genuine RED evidence in `evidence/15.2-red-phase.md` + no-auth nil-pool OpenWire test): exact/domain Responses wire proof (universal-only override, static-inventory gating, rewrite-semantics checks) + OpenWire reusing Task 8 primitives (ResolveURL/credpool/NewOutboundRequest/ParseAndPeekStream, RecoverablePreOutputError); openaicompat + execbackend suites PASS, vet/gofmt/diff-check clean; CARRY: single-acquire vs canonical credential-rotation economics to confirm in 15.4.
- Task 15.3 VERIFIED (review subagent APPROVED; reviewer-reproduced RED + controller-captured evidence in `evidence/15.3-red-phase.md`): two-leg canonical-vs-wire provider-effective differential (parsed-JSON semantics, escaped/late model, stale-framing + exact-length) exposing 2 genuine canonical bugs fixed in legitimate direction (tool descriptions, double-encoded tool outputs; consistent with openailegacy/adapters) + object-shaped tool-output canonical-only profile decline with discriminating case; openaicompat + openairesponses suites PASS, vet/gofmt/diff-check clean.
- Task 15.4 VERIFIED (review subagent APPROVED; test-only, no production diff, no RED file by design): 14 lane-1 E2E tests green driving real Handler/frontendpipe/OpenWire with executor double at production seam (selector, stream modes, errors, event parity, identity, cancel, resume, recorder, metering, retry/race/credential economics incl. 15.2 carry, keepalive, saturation fallback, HTTP/1.1+HTTP/2, not-advertised); openairesponses suite PASS, vet/gofmt clean; CARRY to rollout gate: streaming-only lane policy must be re-encoded in production assessor, metering-egress/saturation-test naming hardening, harness retry loop replaced by real runtime executor before advertising.
- Task 16.1 VERIFIED (review subagent APPROVED, 15.1-mirror precedent, no per-task evidence file): conservative Chat profile (no legacy resolver, RouteFromBodyModel=true, canonical-parser normalization parity, wide canonical-only declines incl. non-string tool content/arguments, exact span + digest parity x3, session facts, chatcmpl_ carrier); openailegacy + frontendpipe suites PASS, vet/gofmt/diff-check clean; CARRY to 16.3: session-hint identity parity vs decline, OpenRouter-headers identity corpus.
- Task 16.2 VERIFIED (review subagent APPROVED; reviewer-generated RED, no evidence file): Chat exact/domain wire proof mirroring 15.2 (operation/profile/streaming/model/inventory/rewrite gates), attachWireProof flavor dispatch with byte-identical Responses behavior, Flavor de-hardcoded in prims (chat maps to /chat/completions + ChatCompletionChunk), Chat CancellationID fallback gated on operation; openaicompat + frontendpipe + execbackend suites PASS, vet/gofmt/diff-check clean; CARRY: streaming-only assessor re-encode still open (rollout gate).
- Task 16.3 VERIFIED (review subagent APPROVED): 7 Chat differential + 14 lane-2 E2E green (21/21), both 16.1 carries closed (pre-commit session-hint decline + E2E fallback proof, two-leg OpenRouter-header identity parity); lane-1 session-hint decline remediated in same commit (reviewer carry, mutation-proven); suites PASS (openaicompat/openailegacy/openairesponses/frontendpipe), vet/gofmt/diff-check clean.
- Task 17.1 VERIFIED (review subagent APPROVED; old-vs-new field proof): bounded no-store characterization (explicit store:false + no prev_id; missing-store/store:true/prev_id/compaction/WebSocket stay canonical), behavior-preserving short-circuit (only unreachable store/scope fields differ), zero store I/O proven via failing store, outer auth/media ordering pinned; openresponses + frontendpipe suites PASS, vet/gofmt/diff-check clean; minor gap: no dedicated cancelled-context test.
- Task 17.2 VERIFIED (review subagent APPROVED after 1 remediation round: RouteFromBodyModel=true in pipe buildPipe + handler-sourced SelectorDifferential with identity parity, RED-proven): no-store OpenResponses profile (store gate, duplicate/unknown/controls declines, selector precedence, identity parity) + compatible backend exact/universal-only-domain proof reusing Task 8 transport; openresponses + openaicompat + frontendpipe suites PASS, vet/gofmt/diff-check clean.
- Task 17.3 VERIFIED (review subagent APPROVED; test-only, no production diff): 6 differential + 15 E2E green (21/21) with wire-boundary hard gate (missing/true store, prev_id, compaction never reach OpenWire/ExecuteLargeBody via dual counters), parsed-JSON differential + full criteria parity with lanes 1-2; suites PASS, vet/gofmt/diff-check clean.
- Task 17.4 VERIFIED (controller scope audit, no code change): storage/continuation left as separate future certification — 17.1-17.3 diffs contain zero store reservations, response-ID persistence, recorder writes, cleanup, lineage, or trajectory machinery on the wire path (only reference is the failing-store fixture asserting zero store I/O); store:true/missing-store/prev_id/compaction all decline to canonical; future certification requires exact reservation/response-ID/recorder/cleanup/lineage/trajectory parity per Req 17/19.
- Task 18.1 VERIFIED (review subagent APPROVED, reviewer-proven RED via old-logic replication): Header.Get-to-Values multi-line gzip detection fix in isRequestGzip/isRequestCompressed/isGzipEncoded (single-value identical, degenerate case more correct), gzip bypasses capture/profile/reservation with compressed-Length never a threshold fact, decoded limits/errors unchanged, no remaining request-path holes; frontendpipe + reqbody suites PASS, vet/gofmt/diff-check clean.
- Task 18.2 DEFERRED by its own gate (controller decision, no code change): lanes certified test-only but not advertised and no Req 21 ROI evidence justifies decoded-gzip replay cost; gzip stays canonical via 18.1 bypass. Future work requires decoded-bytes threshold/reservation, explicit decoded body mode, stale-framing removal, and rerun of scanner/profile/identity/backend/transport differentials.
- Task 19.1 VERIFIED (review subagent APPROVED after 1 remediation round: seq-ordered spool observer gauge fix + genuine RED evidence in `evidence/19.1-red-phase.md`): bounded stage/decline/size/spill/latency/spool diagnostics via closed-enum observer + prom sink, zero production wiring (default-off consistent); largebody + metrics + frontendpipe suites PASS, vet/gofmt/diff-check clean; single unexplained frontendpipe FAIL not attributable (7 consecutive green runs).
- Tasks 19.2-19.5 VERIFIED (review subagent APPROVED, adversarial re-runs match to <1%): full stage/shape benchmarks + honest MIXED 19.3 verdict (strict proof-time invariant FAILED ~6.5-7.9x transient via CompileProof io.ReadAll; post-commit retained heap PASS flat 35,656 B/op), negligible blocker overhead with zero temp files, saturation all-200 with cancel cleanup; evidence/19.2-19.5-benchmarks.md; bench + evidence files only, no production diff; follow-ups: deterministic cancel-trigger proof, Phase-B relabel, preflight/capture pairing footnote.
- Tasks 19.6-19.7 VERIFIED (review subagent APPROVED after controller citation fixes: SupportsWire naming, 2 test-path citations, 1.10 latency/heap numbers with corrected ~1.4-1.6x ratio): full 26-plane/46-port eligibility matrix with quantified blockers, honest wire-mode statement (candidate-path proven, zero production-runtime wire execution — group 20 gap), all-lanes-canonical-only ROI with activation conditions; evidence/19.6-19.7-eligibility-roi.md only, no code diff.
- Tasks 20.1-20.2 VERIFIED (review subagent APPROVED): all 10 ratchets PASS; budget overruns properly attributed to feature code (merge-base archtests green) and repaired per procedure (pipe.go 455->480, internal/core 97324->97349, boundary-test compaction, AST/hexagonal baseline regens, lifecycle-handle adapter + goroutine allowlist); full repo suite green except 2 upstream doc-marker artifacts failing identically at merge-base (fixed on main in 96803666); pkg/ surface purely additive from approved 3.1 (report zero-change wording corrected); fuzz smoke 703,992 execs clean; race skipped per Windows policy.
- Tasks 20.3-20.4 VERIFIED (review subagent APPROVED after docs-only remediation: complete 7-item follow-up list, exact 5-gate enumeration, management-listener prerequisites + Unix-only SIGHUP, fixed citations): operator guide docs/large-payload-fast-path.md (config/rollback/metrics/declines/spool confidentiality) + evidence/20.3-20.4-rollout.md (default-off, non-advertisement, fail-closed, full artifact audit, #532 readiness with canonical-only follow-ups); targeted suites PASS; DELIVERY NOTE: main moved (1022e47f steering anchor) — merge/rebase main before PR or qa marker test blocks CI.
- Remediation Phases 0–7 VERIFIED: full remediation executed per remediation plan (`evidence/remediation-19.3-proof-transient-and-production-assessor-plan.md`); Phase 0-6 commits `21804c80`..`059eb291`; Task 19.3 strict heap gate flipped from FAIL to PASS via streaming proof (~78–89 KiB flat transient heap across 1/5/20 MiB; evidence `evidence/19.2-19.5-benchmarks.md` §5.1.2/§5.1.3); dormant assessor resolved via production assessor composition in `runtimebundle.BuildHost` (`build_large_body_assessor.go`) with streaming-only policy enforced; all 3 lanes certified ADVERTISE-CAPABLE (Task 19.7 gates pass per Phase 6 recertification; `evidence/19.6-19.7-eligibility-roi.md`); Phase 7 docs-only rollout updated `docs/large-payload-fast-path.md` §4.3/§7 and added delta evidence `evidence/20.5-remediation-closeout.md`; config default remains disabled (`enabled: false`); wire support not advertised by default; NO enablement until 19.7 per-lane gates pass (they now do per Phase 6 — recorded); issue #532 stays open until downstream PR merge; zero `*.go` changes in Phase 7.
- Execution Equivalence Remediation (Phases 0–4) VERIFIED: execution equivalence remediation executed per fix plan on branch `fix/large-payload-execution-equivalence`; Phase 0-3 commits `18b5be87`..`6771e21f`; Finding B1 (capture memory slice eliminated; source-only accepted lane `return res, nil, nil` in `candidate.go`; true pipeline transient flat ~220–235 KiB across 1/5/20 MiB vs 458,752 B ceiling; `TestFindingB1_RealPipeline_TransientAllocBounded`); Finding B2 (wire race engine collapsed onto canonical attempt machinery in `internal/core/runtime`, net -217 lines; winner requires text/reasoning delta, handicap delays honored, cancellation independence, panic isolation); Finding H3 (two-turn weighted-first routing state tracking and fail-closed store abort); Finding M4 (shared spill lifecycle refcounting across readers and completed sources in `largebody`); Finding M5 (truthful `CaptureOutcomeTerminalError` on capture reader failure); Phase 4 full recertification: all 10 finding tests GREEN; 3-lane E2E and differentials GREEN; `frontendpipe` full + `TestLargePayloadProof_TransientAllocBounded` 9/9 GREEN; `backends/openaicompat`, `runtimebundle`, `runtime`, `largebody`, `jsonshape`, `archtest` FULL (zero budget adjustments), `internal/qa` full, `WireSupportNotAdvertised` x3 all GREEN; repo-wide `go test -count=1 ./...` 0 failures; fuzz smoke clean (60s total, 0 crashes; no spill/capture/race fuzz targets exist); docs and evidence refreshed (`docs/large-payload-fast-path.md`, `evidence/19.2-19.5-benchmarks.md`, `evidence/19.6-19.7-eligibility-roi.md`, `evidence/20.6-execution-equivalence-closeout.md`); zero enablement in production (default remains `enabled: false`); wire support not advertised by default; issue #532 stays open; zero production `*.go` changes in Phase 4.
