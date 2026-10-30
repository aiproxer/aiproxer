# Research & Design Decisions

## Summary

- **Feature tracker**: #503
- **Implementation work order**: #532
- **Original spec PR**: #531
- **Previous review baseline**: `40168ce1f3890a1c86c22e898be9d264d63ccd72`
- **2026-09-09 rebaseline**: `b08c60846a2a0119eeefef135cd6bbe06162a894`
- **Delta**: current main is 69 commits ahead of the previous review baseline.
- **Verdict**: **scope and core architecture remain valid; implementation should proceed only from the refreshed artifacts.** The ownership-closure work changed the authority census and several seams, and the older spec missed a valuable static pre-capture rejection optimization plus some transport/keepalive and heap-scaling acceptance details.

The retained V1 design is a **two-phase, same-decode-permit assessment followed by a one-way wire commit**. All expected declines happen before `BeginTurn`; canonical fallback uses the already-held decode permit; after commit the execution lane contains no expected canonical fallback.

The major 2026-09-09 additions are:

1. current 26-plane authority census, including explicit Secret Guard Execution / Local Turn / Terminal Decision classifications;
2. current post-slimming narrow runtime-port inventory;
3. O(1) generation-frozen **definitely-ineligible pre-capture disposition** so impossible requests are not spooled/scanned;
4. frontend-specific outer-order and `holdalive`/stream-keepalive parity;
5. stronger backend HTTP framing/client-policy parity requirements;
6. current-dataflow/`CloneCall` ratchets instead of stale `preparedRequest.call` text checks;
7. a hard accepted-lane heap-scaling invariant rather than only aggregate benchmark improvement;
8. benchmarking against current main, including already-landed #592/#602 improvements.

---

## 1. Current Ingress Path Is Still the Correct Optimization Boundary

### Sources audited

- `internal/plugins/frontends/frontendpipe/pipe.go`
- `internal/plugins/frontends/reqbody/*`
- `internal/plugins/frontends/decodeqos/*`
- `internal/plugins/frontends/jsonguard/*`
- `internal/core/jsonshape/*`
- target frontend handlers/decoders

### Current behavior

The shared create path still materializes the complete body before JSON preflight and protocol decode. `frontendpipe.ServeHTTP`:

1. applies method / alt-serve / path;
2. reads the complete request through `reqbody.ReadAll` under the configured body ceiling;
3. resolves the header route selector;
4. if configured, invokes `Spec.ResolveRouteSelector(r, body, pm)` on the complete `[]byte`;
5. runs shared JSON preflight;
6. obtains byte-weighted decode admission from the complete body length;
7. performs `RouteFromBodyModel` defaulting and protocol decode under that permit;
8. releases the permit after decode;
9. validates / runs `AfterDecode` / traffic / executor;
10. wraps streaming executor startup in pre-request holdalive and later response handling.

### Decision

The existing canonical path remains the exact oracle. The optimization must intercept **before the full body allocation**, but it cannot move provider commitment before EOF/validation.

The original two-phase assessment remains necessary because releasing the decode permit after protocol proof and then discovering a runtime blocker would create a second admission decision and could turn an already-admitted valid request into a new 429/503.

---

## 2. Frontend Outer Ordering Is Not Uniform

### Sources audited

- `internal/plugins/frontends/openairesponses/handler.go`
- `internal/plugins/frontends/openailegacy/handler.go`
- `internal/plugins/frontends/openresponses/handler.go`
- `internal/plugins/frontends/openresponses/handler_pipe.go`

### Findings

- OpenAI Responses create currently uses `RouteFromBodyModel=true` and no legacy full-body `ResolveRouteSelector` in its pipe spec.
- OpenAI Chat Completions likewise uses `RouteFromBodyModel=true` and no legacy full-body resolver.
- OpenResponses performs authentication and strict `application/json` media-type validation in its **outer handler before** calling `frontendpipe`.
- OpenResponses `AfterDecode` can run continuation/materialization and response-storage preparation. Missing `store` defaults to storage-enabled behavior; `store:false` is the correct initial stateless subset.

### Decision

Do not encode a fictional universal “auth/path/content-type” ordering inside the new common fast path. Each frontend keeps its current outer authority; candidate capture begins only after whatever checks currently precede `frontendpipe` for that frontend.

The older `tasks.md` phrase “Handler auth/path/content-type → ...” was too generic and is replaced with explicit “preserve each frontend's current outer ordering.”

---

## 3. Keepalive Is Part of Frontend Parity

### Source audited

- `internal/plugins/frontends/frontendpipe/pipe.go`

### Findings

Current shared behavior has two distinct mechanisms:

1. `StreamKeepaliveInterval` is stored in request context before body processing.
2. For streaming requests, `Config.execute` invokes `Exec.Execute` through `holdalive.Wait` using `PreRequestKeepalive`.

The previous spec focused on response wrapping/state but did not name these as certification dependencies.

### Decision

`ExecuteLargeBody` must run through equivalent holdalive ownership for streaming requests and preserve the stream-keepalive context. No keepalive/provider bytes may be emitted before full validation and the one-way wire commit.

This is a semantic/operational parity requirement, not a performance optional.

---

## 4. Extension Architecture Now Has 26 Standard Planes

### Sources audited

- `pkg/lipsdk/feature/plane.go`
- `pkg/lipsdk/feature/plane_manifest.go`
- `internal/core/extensions/snapshot.go`
- `internal/testkit/planeparity/*`
- archived ownership/plane closure evidence

### Findings

The current generated manifest includes 26 production planes. The notable post-spec additions/current authorities include:

- `PlaneSecretGuardExecution`
- `PlaneLocalTurnHandlers`
- `PlaneTerminalDecisionProvider`

`hooks.Bus` remains a separate production mechanism and cannot be assumed to be represented by the plane manifest.

### Decisions

1. Add request-body access metadata to the existing generated `feature.Plane[T]` descriptor.
2. Keep one generated/frozen plane census only.
3. Freeze hook occupancy/categories separately into the same final wire eligibility summary.
4. Unknown/new plane or non-plane authority fails closed and fails CI until classified.

### Initial V1 classification evidence

#### Local Turn

`pkg/lipsdk/localturn` exposes handlers whose `Match` and `Handle` receive a full `lipapi.Call`, and the runtime invokes them before ordinary backend execution. They can claim/short-circuit a request.

**Decision**: occupied Local Turn plane is `CanonicalRequired` in V1.

#### Secret Guard Execution

`internal/core/runtime/executor_secret_guard.go` passes the full Call to `extensions.RunSecretGuardStage`; a block can trigger quarantine, A-leg cancellation, decision auditing, and fail-closed storage behavior.

**Decision**: active Secret Guard execution is `CanonicalRequired` until a separate streaming/wire guard contract proves exact matching + audit + quarantine semantics. Do not treat the binder plane as mere metadata because its runtime effect is content-sensitive.

#### Terminal Decision

The public terminal decision SDK input is bounded, but `DecisionContinue` is not just a response observer: continuation semantics can require preserved request/trajectory state.

**Decision**: initial `PlaneTerminalDecisionProvider` is `CanonicalRequired` unless Task 12 implements and certifies both bounded evidence and source-backed continuation semantics. A bounded provider DTO alone is insufficient eligibility proof.

---

## 5. Post-Slimming Core Uses Narrow Ports, Which Must Be Classified Explicitly

### Sources audited

- `internal/core/runtime/executor_config.go`
- archived `core-feature-ownership-full-closure/final-ownership-census.md`
- `internal/standardplugins/featurehost/*`
- `internal/infra/runtimebundle/*`

### Findings

The slimming/ownership work successfully removed broad feature knowledge from generic core, but this does **not** mean the fast path can look only at extension planes. Current runtime still consumes narrow ports that can affect request semantics or lifecycle:

- prompt-cache maintenance;
- conversation projection reader/tagger/observer and steering writer factory;
- terminal policy reader;
- interleaved processor;
- compaction detector/background auxiliary;
- route/capability/catalog/eligibility/request-token-estimator seams;
- route override reader;
- secure-session manager and recorder;
- accounting preflight, stream usage, usage authority, concurrency, metering recorder, terminal work;
- billing credit/exposure/leg/terminal sinks;
- `BillingIdentity` callbacks taking `lipapi.Call`;
- traffic ports;
- hook bus and other Call-shaped custom callbacks.

### Decision

Task 1 must classify this **current** set. `WireEligibilitySummary` is the generation-frozen union of:

```text
generated typed-plane access
+ hook-chain occupancy/access
+ current non-plane/narrow-port wire capabilities
+ frontend/profile static facts
```

This summary is not a second feature framework; it is a compiled eligibility projection over the existing architecture.

---

## 6. Static Pre-Capture Rejection Is a Material Missing Optimization

### Observation

The original design captures/spools before dynamic core assessment. That is necessary when eligibility depends on the request's model/route/options. It is wasteful when generation-static state already proves the wire lane impossible—for example occupied Local Turn or Secret Guard execution without a wire contract.

For a 5–20 MiB request, paying disk writes + streaming JSON scan + profile setup only to discover a generation-static blocker materially reduces ROI and can make enabling the feature harmful in feature-rich deployments.

### Decision

Compile an O(1) hot disposition from the frozen summary:

```text
DefinitelyCanonical
NeedsRequestAssessment
```

It is intentionally asymmetric:

- `DefinitelyCanonical` authorizes only **skipping the optimization**;
- `NeedsRequestAssessment` does not authorize wire execution and does not imply likely eligibility.

### Constraints

- no request-path reflection;
- no map/plugin/backend walk;
- no I/O/store access;
- no allocation in the common path;
- no replay source, temp file, streaming scanner, or profile state when definitely canonical.

### Why this is safe

A conservative early reject cannot weaken semantics. The only risk would be a false “wire eligible” static result, which the design forbids: static disposition never returns “eligible,” only “not worth considering” or “continue to dynamic proof.”

---

## 7. Generation Pinning Does Not Require a New Public Executor Design

### Sources audited

- `internal/infra/runtimehost/generation_dispatcher.go`
- `internal/infra/runtimehost/generation_executor.go`
- `internal/stdhttp/contract/http_input.go`
- `internal/infra/runtimebundle/*`

### Findings

There are two relevant executor surfaces:

- the public/process `GenerationExecutor`, which acquires the current generation per public `Execute` and pins the returned stream;
- standard bundled HTTP frontends, which are built inside a generation-scoped handler graph and receive that generation's concrete `*runtime.Executor`. The `GenerationDispatcher` already acquires one request lease before delegating to the generation handler.

### Decision

The original optional internal type-assertion design remains valid for standard bundled HTTP. Do **not** widen public `lipsdk.ExecutorView` in V1.

Add explicit tests that static disposition → proof → assessment → canonical fallback/wire execution remain within the same request generation even when reload publishes a new generation concurrently.

External/manual executor/frontends without the optional internal capability remain canonical-only.

---

## 8. Full-Call Dependency Audit Must Follow Current Dataflow, Not Old Names

### Sources audited

- `internal/core/runtime/executor_prepare_request.go`
- `internal/core/runtime/executor_prepare_secure.go`
- `internal/core/runtime/recv_turn_facts.go`
- `internal/core/runtime/terminal_evidence.go`
- `internal/core/runtime/conversation_view_seam.go`
- `internal/core/runtime/attempt_clamp_preview.go`
- `internal/core/runtime/interleaved_open.go`
- `pkg/lipapi/call_clone.go`
- continuation/materialization paths

### Findings

The old tasks used `preparedRequest.call` as a convenient ratchet phrase. Current code no longer has that exact reference shape, while full Calls and clones remain semantically important in many places.

Current canonical execution still deep-clones prompt-bearing Calls for distinct authorities/immutable baselines and attempt derivation. Examples include accepted ingress/backend baselines, conversation-filtered baselines, receive-turn facts, terminal evidence, clamp previews, continuation/interleaved paths, and per-attempt derivation.

This is not itself a bug: these copies protect canonical semantics. It is exactly why a separate wire lane can produce meaningful multi-MiB heap/GC savings.

### Decision

The wire path must not weaken canonical clone semantics. Instead:

- accepted wire execution bypasses prompt-scale Call creation entirely;
- downstream dependencies receive exact bounded DTOs/source digests/source readers or block assessment;
- architecture tests target the wire boundary and reject `lipapi.Call`/`lipapi.CloneCall` ingress there except explicitly characterized response-only canonical APIs;
- no “minimal/fake Call” bridge is permitted.

---

## 9. Backend Wire Support Still Fits the Internal Backend Abstraction

### Sources audited

- `internal/core/execbackend/backend.go`
- `internal/plugins/backends/openaicompat/backend.go`
- OpenAI-compatible request/open/stream files

### Findings

Current `execbackend.Backend` is already an internal provider-neutral value with pure capability resolvers plus `Open`. OpenAI-compatible backends centralize credential pool/cooldown behavior, shared client construction, flavor/model resolution, and first-recv error classification.

### Decision

Add internal optional pure `ResolveWireRequest`/`ResolveWireDomain` capability plus post-commit `OpenWire` (names may differ). Reuse the existing backend's transport/security/client/parser infrastructure. Do not introduce provider-name switches in generic core and do not create a second credential/retry stack.

`OpenWire` only substitutes the request serialization/body source step.

---

## 10. HTTP Transport Parity Needed More Explicit Acceptance

### Gap in old artifacts

The prior spec correctly required established endpoint/auth/client ownership and correct rewritten length, but implementation agents could still accidentally create a subtly different HTTP path: stale inbound framing headers, client auth leakage, trailer propagation, different redirect/proxy/TLS/HTTP2 behavior, or an SDK retry that replays outside core attempt accounting.

### Decision

Certification now explicitly covers:

- provider method/path/query parity;
- backend-owned auth/content headers;
- no blind forwarding of client Authorization/session/control headers;
- no stale `Transfer-Encoding`, `Content-Length`, `Content-Encoding`, `Expect`, or request trailers;
- exact rewritten length when known;
- same shared `http.Client`/Transport, TLS, proxy, HTTP2 and redirect policy;
- cancellation while request body is being sent;
- connection reuse;
- hidden SDK retry ownership.

Test HTTP/1.1 and HTTP/2 where supported by the existing backend client.

---

## 11. Performance Baseline Must Start From Current Main

### Current context

Issue #532 already notes that current main includes focused hot-path optimizations from #592 and #602. In particular, no-op feature/traffic paths have been reduced since the original #531 review.

### Decision

Task 1 records a fresh baseline at `b08c608...` or the actual implementation-start SHA. Do not compare primarily against the older pre-slimming numbers.

The fast path must earn incremental value beyond the current canonical path.

### Required metrics

- allocs/op and B/op;
- CPU/ns/op by stage;
- GC cycles/pause/live heap;
- peak request-attributable Go heap;
- capture/spill bytes and file I/O;
- protocol proof + assessment latency;
- provider-open latency;
- concurrent session behavior and decode-permit occupancy.

---

## 12. Stronger Heap-Scaling Completion Gate

### Problem with aggregate B/op only

A path can improve B/op materially yet still retain one payload-sized body or one full Call clone, leaving GC risk proportional to body size. That would undershoot the purpose of #503.

### Decision

For an accepted spill-backed wire request, after capture the request's prompt-size-dependent bytes live in the replay source, not Go heap. Retained Go memory is bounded by:

```text
memory_spool_bytes
+ max_semantic_fact_bytes
+ fixed scanner/copy/http buffers
+ bounded runtime/response metadata
```

No payload-sized `[]byte`/`string`, canonical item/message tree, or `CloneCall` may be allocated/retained on the accepted wire lane.

Benchmark 1 MiB, 5 MiB, and a test-only 20 MiB request. The wire lane should show approximately flat/bounded Go-heap growth. A body-proportional slope must be explained and either removed or treated as a failed optimization gate.

This requirement is deliberately architectural rather than a fragile machine-specific nanosecond threshold.

---

## 13. Practical Eligibility Matrix Must Match the Current Runtime

The final matrix must include, at minimum:

| Dimension | Cases |
| --- | --- |
| typed planes | empty + every occupied access class; explicitly Local Turn, Secret Guard Execution, Terminal Decision |
| legacy hooks | empty, response-only, request-mutating |
| frontend traffic | no-op, observer, raw capture/redaction |
| secure session | off, standard manager+recorder, new session + resume |
| metering | off, standard wire-native |
| accounting | off, exact wire counter available, CountCall-only blocker |
| billing | off, stock bounded-fact path, custom Call callback blocker |
| conversation/steering | absent/present and classified |
| route override | absent, homogeneous same-wire domain, heterogeneous incompatible domain |
| routing | sequential, fallback, weighted, race where supported |
| frontend route resolver | absent, legacy full-body callback present |
| protocols | each certified lane independently |
| static disposition | definitely canonical, needs dynamic assessment |

At least one normal secure-session + metering production-like composition must **actually hit the wire path**. A design where the stock distribution is permanently blocked is incomplete.

---

## 14. Protocol Rollout Remains Valid

The previous lane ordering remains sound:

1. OpenAI Responses → OpenAI-compatible Responses;
2. OpenAI Chat Completions → OpenAI-compatible Chat;
3. OpenResponses HTTP create → OpenResponses-compatible backend, initially explicit `store:false`, no previous response, no compaction, no WebSocket;
4. gzip decoded-body support later.

There is no evidence from current main that warrants broadening this order before Lane 1 proves the shared architecture.

---

## 15. Rebaseline Verdict

### Scope retained

Keep:

- bounded replay/spill;
- shared streaming JSON validation;
- same decode permit for proof + assessment + canonical fallback;
- exact canonical semantic identity;
- pure exact/domain backend wire proof;
- route-domain pre-certification;
- secure-session and metering wire facts;
- one-way commit;
- existing attempt/failover/race ownership;
- sequential protocol certification;
- default-off rollout.

### Scope strengthened

Add/clarify:

- current 26-plane + narrow-port census;
- static pre-capture rejection;
- current generation-pinning characterization;
- frontend-specific outer ordering;
- pre-request/stream keepalive parity;
- HTTP framing/client-policy parity;
- current Call/CloneCall dataflow ratchets;
- accepted-lane near-flat heap scaling;
- current-main baseline and static-blocker overhead benchmark.

### Scope deliberately not expanded

Do not add in V1 merely because the codebase is now slimmer:

- arbitrary Secret Guard streaming inspection;
- Local Turn support;
- terminal-decision continuation support;
- OpenResponses `store:true`/continuations;
- gzip passthrough;
- public SDK two-phase executor ABI;
- provider-native passthrough;
- route/failover simplification for eligibility.

**Implementation readiness after this refresh: GO**, provided Task 1 re-runs the census against the exact implementation-start SHA and treats any new drift as a spec stop condition rather than guessing.
