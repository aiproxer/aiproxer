# Requirements Document

## Introduction

Issue #503 asks Go-LIP to reduce heap growth, allocation pressure, GC pressure, and redundant JSON/canonical-object work for large request bodies by forwarding a verified same-wire request without constructing the full canonical request tree. Issue #532 is the implementation work order. This is a brownfield optimization of already-supported request flows, not a new proxy mode and not native provider passthrough (#490).

Correctness has priority over optimization. The existing materialize → body-based route resolution → shared JSON preflight → decode admission → frontend decode/validation → canonical core → backend encode path is the behavioral oracle. A request may use the large-payload lane only when Go-LIP can prove that all enabled authorities and relevant frontend/core/backend semantics are preserved. Unknown or unproven behavior always selects the existing canonical path.

This revision was re-baselined against `main` at `b08c60846a2a0119eeefef135cd6bbe06162a894` on 2026-09-09, 69 commits after the prior `40168ce1f3890a1c86c22e898be9d264d63ccd72` review baseline. The completed core-slimming/ownership-closure work does **not** invalidate the architecture, but it changes the production authority census and the exact implementation seams. In particular, the current generated extension surface has 26 standard planes, including `PlaneSecretGuardExecution`, `PlaneLocalTurnHandlers`, and `PlaneTerminalDecisionProvider`; runtime now consumes feature behavior through narrow ports/feature-host composition; and current hot-path optimizations must be the performance baseline rather than pre-slimming measurements.

V1 therefore keeps the original **two-phase executor seam with a one-way wire commit**, with one additional performance rule: a generation/profile combination that is statically and unconditionally incompatible with wire execution must be rejected by an O(1), generation-frozen pre-capture disposition before spool/scanner work begins.

The request flow is:

1. preserve the frontend's existing outer method/auth/media-type/path/error ordering;
2. apply cheap feature/profile/executor/known-length gates and a generation-frozen static wire disposition;
3. if a configured legacy full-body route resolver has no bounded wire contract, continue canonically without capture;
4. capture/replay with bounded RAM + spill while enforcing the existing body ceiling and shared JSON safety semantics;
5. acquire the existing byte-weighted decode-admission permit after EOF/final decoded size is known;
6. run low-allocation protocol semantic proof, canonical route/model derivation, normalized recorder facts, and exact canonical semantic identity derivation while holding that permit;
7. while the **same** permit is still held, run a bounded side-effect-free core wire-eligibility assessment;
8. if proof/assessment declines, run the ordinary canonical protocol decoder under the same permit and continue on the unchanged canonical path;
9. if assessment succeeds, release the permit and cross the one-way wire commit; from that point no expected canonical fallback is allowed.

This keeps expected optimization declines before `SecureSession.BeginTurn`, avoids duplicate lifecycle/accounting effects, preserves route-selection authority, avoids a fallback-induced second decode-admission decision, and prevents obviously ineligible deployments from paying spool/scanner cost.

## Boundary Context

- **In scope**: optional large-payload capture; bounded-memory replay/spill; lossless mid-capture canonical continuation; streaming shared JSON validation; bounded protocol proof and exact canonical semantic identity digest; pre-preflight body-route authority preservation; decode-QoS parity; O(1) static pre-capture disposition; side-effect-free pre-turn eligibility; typed extension/hook/non-plane authority classification; late-bound route-authority compatibility envelopes; exact/domain same-wire backend proof; bounded model rewrite; secure-session/metering wire views; retry/failover replay; frontend response/session-carrier and keepalive parity; backend HTTP transport parity; gzip follow-up; cancellation/resource cleanup; metrics; differential conformance; allocation/load/heap-scaling benchmarks; conservative rollout.
- **Out of scope**: native provider passthrough (#490); cross-protocol raw forwarding; weakening canonical validation; changing default request-size limits; changing route/failover semantics to manufacture eligibility; bypassing secret/DLP/guardrail/accounting/traffic authorities; arbitrary new content encodings; provider SDK types in core; broad capability-profile work owned by #495; expected post-commit canonical fallback in V1; incidental public stabilization of the low-level replay/assessment API; continuation-capable local/terminal feature support unless separately wire-certified.
- **Boundary ownership**: frontend ingress owns cheap gates, preservation of frontend-specific outer ordering and pre-preflight route authority, capture, shared JSON validation, decode admission, protocol proof, exact canonical fallback decode, and frontend-only response state; core/runtime owns static/dynamic eligibility facts, secure-session/B2BUA lifecycle, route authority, metering/accounting, attempts, and terminal ownership; backend plugins own exact/domain wire compatibility and HTTP construction; composition freezes generation-scoped eligibility summaries.

## Requirements

### Requirement 1: Compatibility-First Activation and Exact Fallback
**Objective:** Enabling the optimization must not silently change normal request behavior.

#### Acceptance Criteria
1. Disabled configuration shall use the existing canonical path with no spool/scanner/wire allocation beyond trivial configuration checks.
2. Any unknown, unsupported, unclassified, or false eligibility fact shall select canonical processing.
3. No provider request-body byte shall be committed before complete body validation and successful side-effect-free wire assessment.
4. If wire consideration declines after client bytes were consumed but before wire commit, the retained/captured input shall feed the canonical path exactly once and in original order.
5. Optimization spool-budget exhaustion, missing wire support, profile uncertainty, recoverable spill failure, or static incompatibility shall not turn an otherwise valid canonical request into a new client error.
6. Existing frontend-specific method/auth/content-type/path/error ordering and existing HTTP/error mappings for malformed JSON, request-too-large, content type/encoding, authentication, decode admission, policy denial, and protocol decode errors shall be preserved.
7. Existing `lipsdk.ExecutorView.Execute(ctx, *lipapi.Call)` remains supported and unchanged.
8. Expected fallback is a bounded internal disposition/metric, not error-level logging.
9. Standard generation-scoped HTTP serving shall keep the request pinned to the same published generation across static disposition, proof, assessment, canonical fallback or wire execution. Reload shall not mix eligibility facts from one generation with execution from another.

### Requirement 2: Existing Body Limits and Threshold Semantics
**Objective:** The feature must not create a second request-admission policy.

#### Acceptance Criteria
1. A configurable decoded-size threshold controls consideration only; it never proves eligibility.
2. A known positive request `Content-Length` below threshold may take the canonical path before spooling **only for identity/uncompressed bodies where that length is the same byte domain used by the existing body limit**. The implementation shall use Go's parsed request length/framing semantics rather than reparsing an untrusted raw header string.
3. Unknown/chunked requests may be captured; if final decoded size is below threshold they shall materialize and continue canonically.
4. Existing frontend `MaxRequestBodyBytes` and protocol limits remain authoritative and defaults remain unchanged.
5. Configured limits above/below defaults shall be honored exactly.
6. Existing request-too-large status/error behavior shall remain and shall open no provider request.
7. Compressed `Content-Length` shall never be treated as decoded length.
8. The threshold fast reject shall not bypass any frontend-specific outer auth/media-type/path checks that currently happen before body read.

### Requirement 3: Streaming Shared JSON Safety Parity
**Objective:** Large bodies receive the same shared JSON-shape protections without proportional heap retention.

#### Acceptance Criteria
1. The incremental scanner shall enforce the same normalized byte/depth/token/root/object/array/key/string/number limits as current shared preflight.
2. It shall validate UTF-8, escapes/surrogate pairs, number grammar, delimiters, incomplete/trailing values, and cancellation across buffer boundaries.
3. Ordinary large scalar content shall not be retained solely for validation.
4. Existing slice preflight remains the differential oracle; fuzz/random corpora compare pass/fail class and aggregate counts.
5. Shared scanning code remains provider-neutral; protocol field semantics live at frontend profiles.
6. Scanner/profile uncertainty selects canonical decode rather than an approximate new frontend error.

### Requirement 4: Bounded Protocol Proof With Arbitrary Field Ordering
**Objective:** The wire lane must prove canonical semantics without assuming metadata is early in the body.

#### Acceptance Criteria
1. Profiles shall find required top-level fields irrespective of order and body size.
2. Profiles may retain only explicitly bounded semantic facts such as model, stream mode, exact output controls, session/control carriers, protocol requirements, item-shape metadata, and raw rewrite spans.
3. Exact raw offsets shall be recorded for rewrites such as the top-level model token.
4. Nested keys or matching text inside user content shall never be mistaken for protocol metadata.
5. Exceeding a semantic-fact bound shall select canonical processing, never truncation.
6. Duplicate protocol-owned names that canonical decode/re-encode would collapse or normalize are canonical-only unless exact duplicate behavior is separately certified; OpenResponses retains its stricter duplicate rejection behavior.
7. Profile proof shall include the certified-subset `Call.Validate`/frontend normalization semantics required before executor entry.
8. For frontends using `RouteFromBodyModel`, protocol proof shall derive the same selector/default behavior as the canonical guarded decode path before canonical identity derivation and core assessment.

### Requirement 5: One Generation-Pinned Wire-Eligibility Summary
**Objective:** Every production authority that can invalidate raw forwarding must be represented explicitly without creating a second extension architecture.

#### Acceptance Criteria
1. For typed extension planes, `pkg/lipsdk/feature/plane_manifest.go` / `feature.Plane[T]` / `FrozenPlaneSet` is the sole source of plane declarations; request-body access metadata shall extend that architecture rather than recreating named plane mirrors.
2. The implementation baseline contains **26 standard planes**. Every current and future production plane shall declare an explicit access class such as `Unclassified`, `CanonicalRequired`, `MetadataOnly`, `ResponseOnly`, or `WireContract`.
3. `Unclassified` shall fail declaration/codegen/architecture tests and shall fail closed at runtime.
4. The re-baselined initial classification shall explicitly cover the newly relevant planes:
   - occupied `PlaneLocalTurnHandlers` is `CanonicalRequired` in V1 because `Match` and `Handle` receive a full `lipapi.Call` and may short-circuit the request;
   - occupied `PlaneSecretGuardExecution`/secret guards are `CanonicalRequired` until a separate exact streaming guard contract preserves matching, quarantine, audit, and denial semantics;
   - occupied `PlaneTerminalDecisionProvider` is `CanonicalRequired` initially unless Task 12 proves bounded terminal evidence **and** continuation/source semantics without retaining/reconstructing the request Call. A bounded SDK `Input` alone is not sufficient proof because `DecisionContinue` can require trajectory state.
5. The frozen generation shall derive a bounded `WireEligibilitySummary` from typed plane access plus **non-plane authorities that remain production architecture**, including hook-bus occupancy, frontend/core traffic facilities, secure-session recorder capability, metering/accounting mode, route-override capability, and Call-shaped/narrow runtime ports discovered by the dependency inventory.
6. The current non-plane/narrow-port inventory must explicitly classify at least: `PromptCacheMaintenance`; conversation view reader/tagger/observer and steering writer factory; `TerminalPolicyReader`; interleaved processor; compaction detector/background auxiliary; request token estimator; route/capability/eligibility resolvers; secure-session recorder; billing credit/exposure/leg/terminal sinks; `BillingIdentity` Call callbacks; accounting preflight/stream usage/usage authority/concurrency/metering/terminal work; and custom Call-shaped callbacks.
7. Legacy `hooks.Bus` shall not be assumed covered by the plane manifest. Occupied submit/request-part/tool chains that can inspect or mutate canonical requests are canonical-required unless given an explicit typed wire contract; response-only chains may remain active when proven response-only (SUPERSEDED: under Blocker 2 conservative fail-safe, occupied response-part hook chains statically block wire eligibility because wire execution bypasses response hooks; response pipeline reuse deferred; follow-up to PR #629 review).
8. The summary shall be built at composition time without hot-path reflection, request-path map walks, or arbitrary plugin invocation.
9. From that summary, expose a **constant-time static pre-capture disposition** that can answer only `definitely canonical` versus `needs request-specific assessment`. It may conservatively reject wire consideration but may never authorize wire execution by itself.
10. A definitely canonical disposition must bypass spool/scanner/profile allocation and flow straight into the unchanged canonical read path. The hot check must be allocation-free or effectively zero-allocation and must not call stores, plugins, backends, or route expansion.
11. Config reload shall pin summary/disposition facts for request lifetime.
12. Ratchet tests shall fail when a new plane/hook/non-plane request authority is added without a wire classification.

### Requirement 6: Two-Phase Assessment and One-Way Wire Commit
**Objective:** Expected fallback must happen before turn side effects and without a second decode-admission decision.

#### Acceptance Criteria
1. After whole-body shared preflight, the frontend shall acquire the current byte-weighted `DecodeAdmission` permit and retain it through protocol semantic proof **and** side-effect-free core assessment.
2. `AssessLargeBody` (or equivalent) shall perform no `BeginTurn`, A-leg creation/fetch, DB/store read or mutation, billing reservation, provider/network I/O, client-body wait, spill I/O, or arbitrary unbounded plugin work.
3. If profile proof or core assessment declines, the existing canonical `Spec.Decode` shall execute while the same admission permit is still held; no release/reacquire cycle is allowed.
4. After canonical decode, the permit releases at the same logical boundary as today and the normal post-decode/`AfterDecode`/traffic/`Execute` path continues.
5. If assessment succeeds, the permit releases and the request crosses a one-way wire commit. `ExecuteLargeBody` may then begin the normal secure-session/A-leg lifecycle.
6. No **expected** post-commit condition may require canonical fallback. A newly discovered content dependency after commit is an internal invariant failure: finalize/abort the one turn once; never start a second ordinary `Execute`.
7. Assessment and execution use the same immutable generation/executor composition; execution revalidates an assessment stamp or equivalent immutable facts and treats disagreement as an invariant bug, not fallback.
8. The additional permit hold introduced by assessment shall be measured and bounded; assessment may iterate only generation-bounded data structures and pure backend declarations.
9. A future late-fallback mechanism requires a separate design; it is not required for #503 V1.

### Requirement 7: Late-Bound Route Authorities Need a Pre-Certified Domain
**Objective:** Standard route-override support must not make the fast path dead, while post-A-leg routing must not create a late fallback.

#### Acceptance Criteria
1. Mere presence of `RouteOverrideReader` shall **not** automatically block V1: standard continuity stores expose this capability and runtime composition wires it when available.
2. Before wire commit, core shall prove a generation-pinned **late-route compatibility envelope** covering every selector/backend/execution-mode/model domain that a post-commit route authority can legally choose under the current generation.
3. For session route overrides, the envelope shall derive from the same generation validator/known-backend/execution-composition policy that accepts override selectors. Because override model text is not necessarily a finite catalog, a backend whose wire compatibility is model-dependent may need to prove an `AnyAcceptedModel`/equivalent universal model contract or make the envelope ineligible.
4. After `BeginTurn`, the normal authoritative route-override snapshot/barrier and affinity/weighted-first/interleaved selection may run unchanged because every possible outcome is already inside the proven envelope.
5. If the complete late-route domain cannot be proven wire-compatible, assessment shall decline before `BeginTurn`.
6. Route-hint or future selector-mutating authorities that can produce outcomes outside the exact initial selector set require their own bounded route-domain contract; otherwise they are canonical-required.
7. Eligibility/load evidence shall include homogeneous and heterogeneous backend generations so the cost of conservative route-domain proof is visible.

### Requirement 8: Explicit Same-Wire Backend Compatibility
**Objective:** Protocol/backend naming alone must never authorize raw forwarding.

#### Acceptance Criteria
1. Backends expose optional provider-neutral wire proof for the exact frontend profile, operation, delivery mode, protocol requirements, body mode, rewrite semantics, and candidate model.
2. Backends additionally expose a universal/domain proof when needed by late-bound route authorities; nil/unknown/partial proof means canonical-only.
3. Exact initial-route candidates and every member of any required late-route domain shall be proven before wire commit.
4. Incompatible candidates shall cause assessment decline; core shall not prune/reorder candidates, disable fallback/race, or otherwise change routing semantics to keep wire mode.
5. Compatibility resolution performs no provider network I/O and is generation-stable.
6. External backend plugin ABI remains unchanged unless a separately versioned capability extension is deliberately introduced.
7. V1 may extend the internal `execbackend.Backend` contract additively; generic core must not switch on provider names.

### Requirement 9: Safe Model Rewrite
**Objective:** Native-model binding must remain correct without a second full request body.

#### Acceptance Criteria
1. Rewrite uses the scanner-recorded exact top-level model token span, never regex/prefix search.
2. Replacement is a complete JSON token encoded with normal JSON escaping.
3. A splice reader emits prefix + replacement + suffix without constructing a second full body.
4. Span/size arithmetic uses checked `int64` math before provider open.
5. Duplicate/ambiguous/repaired model forms are canonical-only unless separately certified.
6. Each candidate may receive its own native-model replacement while all other request bytes remain semantically unchanged.

### Requirement 10: Replay, Retry, Failover, and Race Semantics
**Objective:** Raw transport must stay inside the existing attempt/recovery authority.

#### Acceptance Criteria
1. Captured source is immutable after EOF and provides independent offset-zero readers.
2. Each provider/credential retry obtains and closes a fresh reader.
3. Parallel/race attempts require independent readers and pre-certified backend/body compatibility for every possible candidate.
4. Existing attempt budgets, affinity/interleaved state, weighted/race ordering, failure classification, B-leg lifecycle, first-event commitment, and pre-output recovery remain authoritative.
5. No new retry/failover occurs after client-visible output.
6. Replay tests prove complete second-attempt bytes and independent concurrent cursors.

### Requirement 11: Compression Is Staged
**Objective:** Compression support must not weaken decoded-byte protections.

#### Acceptance Criteria
1. Wave 1 routes gzip through the existing canonical `reqbody.ReadAll` path.
2. A later wave may capture the decoded identity JSON stream only after matching current compressed/decoded limit behavior.
3. Decoded-body wire requests remove stale outbound content-encoding state.
4. Brotli/zstd/deflate are not introduced by #503.
5. Compressed `Content-Length` is never used for decoded threshold/reservation proof.

### Requirement 12: Backend HTTP and Credential Semantics
**Objective:** Wire transport reuses established backend security/transport ownership instead of introducing a subtly different HTTP client path.

#### Acceptance Criteria
1. Wire opens reuse endpoint/base-URL resolution, credential selection/cooldown, shared `http.Client`/Transport, TLS/proxy/HTTP2/redirect policy, response limits/parsers, and failure classification.
2. Client Authorization, hop-by-hop headers, stale `Transfer-Encoding`, stale `Content-Length`, stale `Content-Encoding`, `Expect`, request `Trailer`, and frontend-only/session/control headers are never blindly forwarded to the provider.
3. Core owns retry; hidden SDK/client retries that independently replay request bodies are disabled/avoided or proven to use the same replay contract without changing attempt economics.
4. Outbound `Content-Length` reflects the exact rewritten body length when the backend/client path currently sends a known length; otherwise valid streaming HTTP semantics are used. A model rewrite may not leave stale framing metadata.
5. Provider responses still become the same canonical `lipapi.Event` streams.
6. Conformance includes HTTP/1.1 and HTTP/2 where the existing backend transport supports them, request cancellation while opening/sending, connection reuse, redirect behavior if enabled by the shared client, and no accidental request-trailer propagation.
7. Provider method/path/query and backend-owned auth/content headers match the canonical encoder for the certified lane; only explicitly documented protocol-opaque differences are allowed.

### Requirement 13: Content, Policy, Hook, and Traffic Authorities Remain Authoritative
**Objective:** Large bodies shall not bypass safety, feature, routing, or observation stages.

#### Acceptance Criteria
1. Frontend ingress traffic features requiring a full body shall select canonical handling before spooling unless a streaming/wire contract exists. A provably no-op traffic configuration should be represented in the generation-frozen static disposition so it does not add per-request work.
2. A configured `frontendpipe.Spec.ResolveRouteSelector` callback is canonical-only in V1 because the current contract receives the complete `[]byte` body and runs before shared preflight. A future bounded route-resolution contract may make such a frontend eligible only if it preserves the same ordering, precedence, and selector semantics; the wire lane shall neither skip the legacy callback nor materialize a second full body merely to invoke it.
3. Active secret guards, raw capture/redaction, submit hooks, request/request-part/pre-request/attempt transforms, conversation projection/steering, tool-catalog request changes, local-turn content logic, compaction preservers, or similar Call/content stages block unless explicitly wire-certified.
4. `PlaneLocalTurnHandlers` and `PlaneSecretGuardExecution` are explicit V1 blockers when occupied, not generic “feature” assumptions.
5. `PlaneTerminalDecisionProvider` is a V1 blocker unless bounded terminal/continuation source parity is specifically implemented and certified.
6. Response-only observers/gates may remain active if canonical provider events preserve their inputs/semantics (SUPERSEDED: under Blocker 2 conservative fail-safe, occupied response-only planes statically block wire eligibility because wire streaming bypasses response pipeline machinery; response pipeline reuse deferred; follow-up to PR #629 review).
7. Metadata-only session/workspace stages may remain active only with exact bounded inputs.
8. No authority may be disabled or skipped merely to make a request eligible.

### Requirement 14: Secure-Session Parity Without a Canonical Call
**Objective:** The standard secure-session lifecycle must remain usable on the wire path.

#### Acceptance Criteria
1. Wire facts shall carry the exact bounded client/session authority inputs that canonical `BeginTurn` receives, sourced through normal frontend/header/body precedence. Sensitive resume tokens never reach backends or telemetry.
2. Initial profiles may conservatively reject body-carried LIP session metadata while still supporting authoritative LIP session/resume headers; any supported body metadata must match current precedence exactly.
3. Standard secure-session recording shall not be blocked merely because the recorder is normally composed. Protocol proof shall produce a bounded **normalized client-turn shape** equivalent to `lipapi.NormalizedItems` for the certified subset (role/ordinal/content-part kinds and other recorder-required non-content facts).
4. If normalized item/part metadata exceeds configured semantic-fact bounds, the request shall be canonical.
5. Wire execution shall call the existing recorder through an equivalent bounded `ClientTurnRecordInput` path; prompt text shall not be materialized solely for this recorder.
6. New-session `BeginTurn` response carriers—including authoritative session ID, A-leg ID, and the raw resume token returned for a new session—shall return to the frontend through a **sensitive response-carrier contract** and preserve current session-header semantics.
7. Sensitive response carrier values are never metrics labels/log fields and are released with request lifetime.

### Requirement 15: Metering, Accounting, and Billing Need Wire-Native Evidence
**Objective:** Standard economic checkpoints must not silently retain a full Call and erase the memory benefit.

#### Acceptance Criteria
1. The wire path shall not call current Call-cloning frontend/backend metering checkpoint helpers merely to satisfy existing APIs.
2. Add wire-native frontend/backend ingress checkpoint capture from exact bounded facts: request/economic identity, scope, frontend/backend/attempt/A-leg/session correlations, model, request count, and exact max-output quantity.
3. Wire checkpoint storage shall not retain a hidden full canonical Call. Widening/retry integrity shall instead use immutable source/proof digests plus bounded rewrite/attempt evidence.
4. When token accounting/preflight is disabled, wire checkpointing shall remain fully usable without tokenization.
5. When configured accounting/context/billing requires input-token counting, eligibility requires an exact wire counting contract for the profile/backend/source. Raw body byte count is never substituted for tokens.
6. Stock billing/exposure/price/max-output/account identity paths shall be explicitly characterized. Exact bounded inputs should share helpers with canonical execution; arbitrary custom Call callbacks—especially current `BillingIdentity` callbacks receiving `lipapi.Call`—remain canonical blockers unless refactored to exact bounded facts or an explicit wire contract.
7. Economic/metering facts, reservations, settlement, terminal usage, and retry idempotency occur exactly once and preserve current failure policy.
8. Eligibility evidence shall report accounting/billing on/off separately rather than hiding a production blocker.

### Requirement 16: Deterministic Request and Economic Identity Parity
**Objective:** Wire execution must not create a second identity namespace for the same logical request.

#### Acceptance Criteria
1. Inventory all production uses of `diag.StableCallID`, `StableCallToken`, `StableUnix`, Call.ID-derived checkpoint IDs, billing call IDs, response IDs/timestamps, trace IDs, and dedupe/source IDs.
2. A certified profile shall produce an **exact canonical semantic identity digest** equivalent to the current post-frontend-decode/pre-core canonical Call identity for its supported subset, without retaining proportional prompt content.
3. The canonical-digest implementation shall be differential-tested against the current `diag` hash over fully decoded canonical Calls for large strings, escapes/Unicode, tool/message shapes, session/header precedence, route selector/model, and all certified optional fields.
4. Helpers may derive current stable Call ID/token/timestamp from that digest so wire trace/economic identity and deterministic frontend fields remain path-stable.
5. A raw-body-only hash that differs from canonical stable identity is not sufficient for economic identity.
6. Caller/provider-supplied explicit IDs retain current precedence.
7. If exact canonical identity cannot be generated for a request shape, the profile shall decline to canonical processing.

### Requirement 17: Conservative Protocol Certification Matrix
**Objective:** Only request subsets proven equivalent to canonical decode→encode may use raw forwarding.

#### Acceptance Criteria
1. Each profile enumerates supported fields/types, duplicates, normalization/repair triggers, requirement derivation, session precedence, normalized recorder shape, identity digest rules, rewrite rules, frontend outer/pre/post-decode side effects, keepalive behavior, and response dependencies.
2. Unknown/extra fields that canonical encoding discards or normalizes are canonical-only unless explicitly proven safe for the exact backend.
3. Malformed histories/aliases that current decoders repair/drop/normalize are canonical-only.
4. OpenResponses create is not eligible merely because `previous_response_id` is absent: current decoder defaults `store=true`. First certification requires **explicit `store:false`**, absent `previous_response_id`, no compaction, and no WebSocket unless continuation/storage parity is separately implemented.
5. OpenAI Responses/Chat initial profiles conservatively reject proxy/session body metadata, normalization-sensitive aliases/histories, and unresolved frontend response dependencies.
6. Current OpenAI Responses and legacy Chat handlers use `RouteFromBodyModel=true` and no legacy full-body resolver; their first profiles must preserve header-selector precedence and body-model/default behavior exactly. If this changes later, the lane fails closed until re-certified.
7. OpenResponses auth and JSON `Content-Type` checks occur in the outer handler before `frontendpipe`; the fast path shall preserve that ordering rather than forcing a generic frontend gate order.
8. Profile broadening requires differential corpus in the same change.
9. Canonical-vs-wire tests compare provider-effective semantics and normalize only fields explicitly documented as protocol-opaque/non-deterministic.

### Requirement 18: Preserve Frontend Response, Keepalive, and Session-Carrier State
**Objective:** Skipping the canonical Call must not break response IDs, cancellation, timestamps, wrappers, keepalive behavior, or session continuity.

#### Acceptance Criteria
1. Wire execution returns an `ExecutionResult` containing canonical events plus bounded provider-neutral response facts; EventStream-only is insufficient.
2. Response facts shall include authoritative request/trace identity, A-leg/session facts, operation/delivery, and a separately marked sensitive session-response carrier when required.
3. The wire path shall not fabricate a partial `lipapi.Call` to satisfy existing response encoders/loggers.
4. Each certified frontend inventories `BuildEncodeOpts`, `WrapStream`, response writers, cancellation endpoints, stream/debug helpers, session response carriers, `AfterDecode`/Extra state, `PreRequestKeepalive`, and `StreamKeepaliveInterval`; each dependency is refactored to bounded facts/frontend-owned state or blocks the profile.
5. Streaming `ExecuteLargeBody` shall preserve the same pre-request `holdalive` behavior that currently wraps `Exec.Execute`; the stream keepalive interval already carried through request context shall remain effective. No holdalive/provider byte is emitted before validation and one-way wire commit.
6. OpenAI Responses cancellation IDs remain bound to authoritative A-leg/session semantics; wire requests must remain cancellable by the returned ID.
7. Deterministic response IDs/timestamps derived from canonical Call hashes use Requirement 16's equivalent digest unless the protocol explicitly treats the field as opaque and conformance documents a deliberate difference.
8. Frontend-specific state stays at the frontend boundary; core contracts do not import provider/frontend state.

### Requirement 19: Close Every Post-Commit Full-Call Dependency
**Objective:** No runtime component may accidentally force canonical materialization after wire commit.

#### Acceptance Criteria
1. Before wire execution implementation, create a production inventory of every post-commit read/clone/retention of the prepared/current/ingress/baseline `lipapi.Call`, every Call-retaining checkpoint, every `lipapi.Call`-typed callback, and every `diag` helper that would require the full request. The inventory shall follow current dataflow/field ownership, not stale textual symbol names.
2. Current known dependency families include routing/request-size/capability requirements, secure-session recorder input, frontend/backend metering checkpoints, accounting/token estimators, billing admission/identity/policy, `recvTurnFacts`, conversation view/steering, continuation support, interleaved-thinking, terminal decision/evidence, terminal usage/session fields, traffic snapshots, prompt-cache/compaction seams, local-turn handling, and response helpers.
3. Current `lipapi.CloneCall` sites in request preparation, conversation baselines, terminal evidence, receive-turn facts, clamp previews, continuation/interleaved paths, and attempt derivation are part of the audit: wire execution must not accidentally reintroduce payload-scale clones through a helper.
4. Every dependency is classified as exact bounded wire fact/view, wire source/digest contract, response-only behavior, or pre-assessment canonical blocker.
5. Standard normally composed components such as secure-session recorder and metering checkpoint require an explicit wire-native path rather than being silently classified as permanent blockers.
6. A ratchet/AST/architecture test shall prevent new post-commit full-Call dependencies from being treated as wire-safe implicitly. Prefer type/dataflow/package-boundary assertions over grepping a historical field spelling such as `preparedRequest.call`.
7. No consumer receives a fake/partial canonical Call. If exact semantics cannot be represented without materialization, assessment declines before wire commit.
8. Detached-session execution remains canonical-only until its Call/lifecycle dependencies are separately represented and parity-tested.

### Requirement 20: Replay-Spool Resource Safety, Lossless Recovery, and Confidentiality
**Objective:** Pre-commit replay stays bounded, deterministic, recoverable, and safe for prompt data.

#### Acceptance Criteria
1. Memory retained by capture is bounded by configured memory-spool bytes plus fixed scanner/copy buffers and bounded semantic facts.
2. Spill files use unpredictable names without user/session/model data and owner-only permissions where supported.
3. Spool paths/body prefixes do not appear in normal logs/metrics/traces.
4. Global logical spool reservation is bounded and released exactly once on success/fallback/cancel/error.
5. Reservation exhaustion is an optimization decline, not a new 413. Documentation states canonical fallback may still allocate according to the pre-existing path, so this budget is not global OOM admission.
6. Capture shall retain ownership of the **current input chunk/unwritten suffix until the corresponding memory/file write fully succeeds**. A short/partial write shall never discard bytes already read from the client.
7. On mid-capture reservation decline or recoverable create/write failure, a dedicated canonical-continuation reader shall expose: successfully retained memory/file prefix + current unwritten suffix + still-unread client body, exactly once and under the existing request-size ceiling.
8. Mid-capture canonical continuation shall never restart or reread the client socket; randomized chunk/fault tests shall compare its resulting bytes with a direct canonical read.
9. `Source.Close` is idempotent and shall not deadlock waiting for leaked readers; root close marks deletion pending and final removal occurs when tracked readers close. No cleanup goroutine is required.
10. File create/write/read/remove failures, cancellation, timeout, short writes, and Windows open-file deletion behavior receive injected/state tests.
11. Operator docs state that spool files can contain plaintext prompt data and describe `spool_dir`, volume/filesystem protection, and lifetime.

### Requirement 21: Evidence-Based Performance and Practical Eligibility
**Objective:** The feature must measurably reduce heap/GC and actually execute in realistic compositions.

#### Acceptance Criteria
1. Benchmarks cover 32 KiB, 256 KiB, 1 MiB, 5 MiB, and a test-only raised-limit 20 MiB request.
2. Record allocs/op, B/op, CPU, capture/shared-preflight/protocol-proof/assessment/provider-open latency, GC cycles/pause/live heap, peak request-attributable Go heap, and temp-file I/O.
3. Include concurrent realistic session counts, slow uploads, spool-budget saturation, malformed/late-field/giant-string cases, and replay/failover.
4. Verify decode permits are never held while waiting for client upload; measure the additional bounded hold across side-effect-free assessment.
5. Publish an eligibility matrix for all 26 typed extension planes (explicitly including secret-guard execution, local turn, and terminal decision), hook chains, frontend/core traffic, standard secure-session recorder, metering, accounting/billing, conversation/steering ports, route override with homogeneous vs heterogeneous backend generations, sequential/fallback/race selectors, and each certified lane.
6. At least one normal secure-session + metering production-like composition shall reach wire execution. A design that compiles but is blocked by standard normally-on components is incomplete.
7. Measure local spool/device I/O and provider-open latency separately from heap savings; default spool thresholds/budgets must remain evidence-adjustable rather than assumed optimal.
8. Performance claims shall state that V1 still receives/validates the full request before provider open; the principal gain is heap/GC and avoided canonical object/marshal/clone work, not early TTFT.
9. Benchmark against the **current rebaseline commit**, including already-landed hot-path improvements (#592/#602), so the feature receives credit only for incremental savings.
10. On an accepted spill-backed wire request, prompt-size-dependent Go heap retention/allocation after capture shall be eliminated: the implementation may retain at most configured `memory_spool_bytes` + `max_semantic_fact_bytes` + fixed scanner/copy/transport buffers, plus bounded output/runtime metadata. It shall not allocate or retain a payload-sized `[]byte`/`string`, full `lipapi.Call`, item/message tree, or payload-scale Call clone.
11. Heap/GC evidence shall include a size-scaling check across 1 MiB → 5 MiB → 20 MiB. The accepted wire lane must show approximately flat/bounded Go-heap growth rather than canonical O(payload) or clone-amplified growth; any material body-proportional heap slope must be explained and either removed or treated as a failed optimization gate.
12. A **definitely ineligible** generation/profile benchmark shall prove the static pre-capture disposition adds negligible overhead versus the disabled/current canonical baseline and creates no temp file, streaming scanner, semantic profile, or replay source.
13. Completion requires a material improvement on multi-MiB accepted requests. If a lane's total CPU/I/O tradeoff is not worthwhile under realistic concurrency, leave that lane canonical-only rather than enabling it for benchmark optics.

### Requirement 22: Conservative Rollout, Diagnostics, API Boundary, and Architecture Ratchets
**Objective:** Operators can enable, inspect, and revert the optimization safely without accidentally stabilizing a premature public API.

#### Acceptance Criteria
1. Feature is default-off in the first release.
2. Metrics use bounded static labels only and never backend/model/session/user IDs.
3. Logs/traces may record body size, spill/profile, rewrite/replay, assessment result, and fallback reason but never body content, spool path, or resume token.
4. Architecture tests prevent provider-name switches in core, provider types in large-body contracts, duplicate typed-plane classification systems, unclassified request authorities, fake Calls, payload-scale Call clones on wire execution, expected fallback after wire commit, protocol proof outside decode admission, bypass of the O(1) static disposition when it says definitely canonical, and skipping a configured pre-preflight full-body route resolver.
5. The V1 replay/proof/assessment/rewrite seam shall remain in an internal provider-neutral package unless a concrete supported external plugin consumer requires an explicit API/ABI/versioning review; existing public `lipsdk.ExecutorView` remains unchanged.
6. External backend/frontend/manual executor contracts remain source-compatible and canonical-only unless separately extended through a deliberate versioned capability.
7. Full QA/race/static checks, canonical characterization suites, identity-digest differential tests, transport/keepalive parity tests, heap-scaling evidence, and protocol differential tests pass before any production profile advertises wire support.
8. The spec/design shall be revalidated if main materially changes in frontend route selection/decode/admission, generation request binding, secure-session lifecycle, metering/accounting, extension/hook composition, routing authority, backend contracts, transport/keepalive ownership, or response encoding before implementation begins.
9. Implementation PRs shall follow the chronological workstreams in `tasks.md`; do not merge the complete feature in one giant change. Each PR must leave the canonical path green and the feature default-off until certification gates are met.
