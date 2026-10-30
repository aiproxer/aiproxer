# Design Document

## Overview

#503 adds an **optional, proof-gated large-request execution lane** for already-supported JSON create operations. #532 is the implementation work order. An eligible request is captured into a bounded-memory replay source, receives the same shared JSON protections, is semantically proven under the existing byte-weighted decode admission, is assessed against the complete frozen runtime composition, and only then is replayed directly to an explicitly same-wire backend without building the full canonical `lipapi.Call` graph or re-marshalling the provider request.

The existing canonical path remains the behavioral oracle and fallback. “Streaming” does **not** mean client bytes are forwarded upstream before EOF: Go-LIP still needs complete request validity and eligibility before provider commitment. The first-order benefit is lower retained Go heap/GC and avoided canonical decode/re-encode/clone work for multi-MiB bodies, not earlier provider TTFT.

This revision was re-baselined against `main` at `b08c60846a2a0119eeefef135cd6bbe06162a894` on 2026-09-09. The original architecture survives, but all implementation references below use the current post-slimming/ownership-closure runtime.

## Rebaseline Findings

The material changes since the previous `40168ce...` review are:

1. The generated plane architecture is now a closed set of **26** standard planes. The large-body classification must extend `feature.Plane[T]` / `plane_manifest.go` / `FrozenPlaneSet`; no parallel plane registry is allowed.
2. Three current planes require explicit treatment:
   - `PlaneLocalTurnHandlers` receives a full `lipapi.Call` in both match and handle and may short-circuit the request: V1 blocker when occupied.
   - `PlaneSecretGuardExecution` drives content-aware secret guards over the full Call plus quarantine/audit semantics: V1 blocker when active until a separately exact streaming guard contract exists.
   - `PlaneTerminalDecisionProvider` has a bounded SDK input, but `DecisionContinue` can rely on request trajectory/continuation state: initial V1 blocker unless Task 12 closes both evidence and continuation-source dependencies.
3. Core feature ownership has been slimmed into feature-host composition and narrow runtime ports. The dependency census must classify current ports rather than historical feature-specific structs.
4. `preparedRequest.call` is no longer a reliable textual search token. Current code uses `prep.call`, identity ingress/baseline Calls, terminal/receive facts, and many `lipapi.CloneCall` sites. Architecture ratchets must follow type/dataflow/package ownership, not stale symbol spelling.
5. Standard bundled HTTP frontends are generation-scoped and receive `*runtime.Executor`; the outer `GenerationDispatcher` already pins the request generation. The optional internal two-phase executor seam remains viable, but tests must prove assessment and execution cannot cross a generation reload boundary.
6. `frontendpipe.Config.execute` currently wraps **streaming** execution in `holdalive.Wait`, while `StreamKeepaliveInterval` is injected into request context before body processing. Wire execution must preserve both behaviors.
7. OpenAI Responses and legacy Chat currently use `RouteFromBodyModel=true` and no legacy `ResolveRouteSelector`; OpenResponses performs auth and JSON media-type checks in its outer handler before entering `frontendpipe`. Candidate gating must preserve these frontend-specific orderings.
8. Current `main` already contains focused hot-path improvements, including no-op traffic/interleaved savings. Benchmarks must start from current main and must not attribute already-landed savings to #503.
9. A meaningful missed optimization in the older spec is visible now: a generation whose frozen authority summary makes wire execution **impossible for every request** should not spool/scan a multi-MiB request merely to rediscover that blocker. The design therefore adds a constant-time static pre-capture disposition.
10. Performance completion needs a stronger invariant than “B/op improved”: an accepted spill-backed wire request must not retain or allocate payload-sized Go objects after capture. Heap growth across 1 MiB → 5 MiB → 20 MiB should be bounded rather than O(payload) or clone-amplified.

---

## 1. Canonical Flow: Behavioral Oracle

Current shared frontend behavior is the oracle. The exact outer ordering differs by frontend, but the shared `frontendpipe` flow is approximately:

```text
frontend-specific outer handler
  ├─ method/path/auth/media-type checks where that frontend owns them
  └─ frontendpipe.ServeHTTP
       ├─ request context stream-keepalive interval
       ├─ method / AltServe / path
       ├─ reqbody.ReadAll under MaxRequestBodyBytes
       ├─ executor availability
       ├─ header route selector
       ├─ optional ResolveRouteSelector(r, whole []byte, path)   <-- legacy full-body authority
       ├─ jsonguard.PreflightWithContext(whole []byte)
       ├─ decodeqos.TryAdmit(decodedBodyBytes)
       ├─ RouteFromBodyModel/default selector under permit
       ├─ Spec.Decode
       ├─ release decode permit
       ├─ call.Validate / AfterDecode / traffic
       ├─ Exec.Execute (streaming wrapped in holdalive.Wait)
       ├─ WrapStream
       └─ BuildEncodeOpts + stream/non-stream writer
```

The large-body lane may refactor shared helpers but shall not silently change this oracle's observable ordering or error ownership.

---

## 2. Target Request Flow

```text
frontend-specific outer checks
  ↓
cheap candidate gates
  ├─ feature disabled / no profile / no two-phase executor → canonical
  ├─ identity body + known request length below threshold → canonical
  ├─ gzip wave 1 → canonical
  ├─ legacy full-body ResolveRouteSelector present → canonical
  └─ FrozenStaticDisposition == definitely-canonical → canonical   [O(1), no spool]
  ↓
CaptureSource
  ├─ fixed RAM prefix/window
  ├─ secure spill after memory threshold
  ├─ exact body ceiling
  ├─ shared streaming JSON scanner
  └─ lossless continuation state if capture must decline
  ↓ EOF / final decoded bytes
below threshold → canonical from source
  ↓
one existing DecodeAdmission permit
  ↓
frontend profile proof from replay
  ├─ canonical route selector/model/default facts
  ├─ exact protocol semantics for certified subset
  ├─ normalized client-turn shape
  ├─ canonical semantic identity digest
  ├─ body mode/rewrite semantics
  └─ rewrite spans
  ↓
core AssessLargeBody (same permit still held)
  ├─ frozen authority summary
  ├─ exact route candidate set
  ├─ late-route compatibility envelope
  ├─ backend exact/domain wire proofs
  ├─ secure/metering/accounting/billing capabilities
  └─ NO side effects / I/O / stores / provider calls
  ├─ decline → Spec.Decode from replay under SAME permit → normal canonical path
  └─ accept  → release permit → ONE-WAY WIRE COMMIT
                                ↓
                         ExecuteLargeBody
                                ├─ BeginTurn/A-leg exactly once
                                ├─ normal late route override inside certified domain
                                ├─ existing authority/economic admission
                                ├─ existing attempt/failover/race owner
                                ├─ per-attempt replay + bounded model splice
                                ├─ backend OpenWire using existing client/security policy
                                └─ canonical EventStream + ResponseFacts
                                      ↓
                         frontend Wrap/Encode/keepalive/session carriers
```

### Commit rule

Before the wire commit, **canonical fallback is expected and cheap enough to be normal control flow**. After the wire commit, expected canonical fallback is forbidden. Any unexpected need for prompt content after commit is an invariant failure that cleans up the one logical turn; it never starts a second canonical execution.

---

## 3. Configuration

Illustrative internal configuration:

```yaml
server:
  max_request_body_bytes: 8388608   # existing authority; unchanged
  large_payload_fast_path:
    enabled: false
    threshold_bytes: 1048576
    memory_spool_bytes: 65536
    max_inflight_spool_bytes: 268435456
    max_semantic_fact_bytes: 262144
    spool_dir: ""
```

Rules:

- default off;
- `threshold_bytes > 0` and must not change the existing max-body policy;
- `memory_spool_bytes` bounds retained request bytes in Go heap;
- `max_inflight_spool_bytes` is an optimization budget, not a new request-admission error source;
- `max_semantic_fact_bytes` bounds profile-derived metadata such as normalized part shapes;
- `spool_dir` is validated at generation build/reload; invalid configuration rejects the candidate generation and preserves last-good publication;
- defaults are evidence-adjustable after Task 19 measurements, not assumed optimal.

The feature configuration belongs to base server/runtime configuration because it controls an internal transport optimization, not provider business semantics.

---

## 4. Static Pre-Capture Disposition

### Purpose

The dynamic assessor is necessarily request-specific: route selector/model, delivery mode, rewrite facts, protocol requirements, and some economic/counting facts are unknown until the body is proven. But many blockers are generation-static. Spooling a 5–20 MiB request when `LocalTurnHandlers` or an unclassified content authority makes **every** request canonical would waste disk/CPU and add latency without any chance of benefit.

### Contract

Composition derives a compact immutable summary and a hot projection equivalent to:

```go
type StaticWireDisposition uint8

const (
    StaticWireUnknown StaticWireDisposition = iota
    StaticWireDefinitelyCanonical
    StaticWireNeedsRequestAssessment
)

type StaticWireReason uint16 // bounded enum, no plugin/provider/user strings

// Illustrative internal method; exact package/name may differ.
func (e *Executor) LargeBodyStaticDisposition(frontendProfileID string) (StaticWireDisposition, StaticWireReason)
```

This surface is **not authorization**. `NeedsRequestAssessment` means only “do not reject before capture”; the request still needs complete proof and `AssessLargeBody`.

### Must be static blockers initially

At minimum, when relevant to the candidate profile:

- unclassified typed plane;
- occupied Local Turn plane;
- active Secret Guard execution requiring Call content;
- terminal decision provider without separately certified continuation-source support;
- active canonical-only request-mutating hook/plane;
- canonical-only traffic/raw-capture/redaction path;
- mandatory secure-session/metering/accounting capability that has no wire view at that implementation stage;
- external/manual executor with no internal two-phase capability.

A static route/backend blocker may be used only when composition can prove it for the entire frontend profile without request/model-dependent callbacks. Otherwise leave it to dynamic assessment.

### Performance rules

- no reflection;
- no map walk proportional to plugins/backends;
- no plugin/backend/store callback;
- no allocation on the common path;
- no temp file/replay/scanner construction when definitely canonical.

---

## 5. Replay Source and Lossless Canonical Continuation

### Source abstraction

Illustrative shape:

```go
type Source interface {
    Size() int64
    Open() (io.ReadCloser, error) // independent offset-zero reader
    Close() error                 // idempotent root close
}
```

Implementation may use a memory segment followed by an append-only file or a single spill file after a memory threshold. It must not construct a second payload-sized buffer to join pieces.

### Capture guarantees

- consume client body once;
- enforce existing decoded body limit;
- reserve logical spool bytes with checked arithmetic;
- keep current input chunk/unwritten suffix until write success;
- on recoverable spool decline/write failure, expose exactly:

```text
retained prefix + current unwritten suffix + still-unread request body
```

as one canonical continuation reader;
- never restart the socket read;
- preserve cancellation;
- root close does not block forever on leaked child readers;
- Windows open-file deletion semantics are explicitly tested;
- spill paths use unpredictable names and restrictive permissions where supported;
- prompt bytes/path never enter telemetry.

### Shared streaming JSON scanner

Implement a provider-neutral incremental equivalent of current `jsonguard`/JSON-shape preflight. It owns syntax/shape/limit semantics only. Protocol profiles subscribe to token/span events or a similarly bounded stream; the shared scanner must not know OpenAI/OpenResponses fields.

The scanner must handle chunk boundaries inside UTF-8, escapes, surrogate pairs, numbers, object names, and giant strings without retaining giant values.

---

## 6. Protocol Proof and Canonical Semantic Identity

Each certified frontend profile owns a bounded proof compiler over the validated replay stream.

Illustrative output:

```go
type LargeBodyProof struct {
    ProfileID          string
    Operation          lipapi.Operation
    Delivery           lipapi.DeliveryMode
    RouteSelector      string
    ClientModel        string
    MaxOutputTokens    int64
    ProtocolFacts      ProtocolFacts
    BodyMode           BodyMode
    Rewrite            RewriteSemantics
    ModelSpan          Span
    CanonicalDigest    [32]byte
    ClientTurnShape    ClientTurnShape
    SessionInput       SessionInput
    SourceDigest       [32]byte
    BodyBytes          int64
}
```

Do **not** turn this into a shadow `lipapi.Call`. Every field must have a named downstream consumer and bounded size.

### Exact identity

The wire path must derive the same stable semantic sum as the canonical post-frontend-decode/pre-core Call for the supported subset. The safest implementation is to factor `diag` so canonical and wire paths can both derive existing IDs/tokens/timestamps from the same already-computed sum.

The profile's hash writer must reproduce canonical serialization semantics exactly for its certified subset:

- field order used by the stable hash;
- zero/omitted behavior;
- normalized messages/items/tools/options;
- JSON string escaping and Unicode;
- route selector/model precedence;
- session/header precedence;
- `Call.ID` clearing semantics used by the current stable hash.

If exact equivalence is difficult for a field, that shape is canonical-only. A raw request hash is useful as source integrity evidence but is **not** a substitute for canonical economic/request identity.

---

## 7. Generation-Frozen Authority Classification

### Typed planes

Add request-access metadata to the existing generated plane descriptor. Equivalent names are allowed, but semantics are:

```go
type RequestBodyAccess uint8

const (
    RequestBodyAccessUnclassified RequestBodyAccess = iota
    RequestBodyCanonicalRequired
    RequestBodyMetadataOnly
    RequestBodyResponseOnly
    RequestBodyWireContract
)
```

The generator/frozen set remains the only typed-plane census. New plane without classification = generation/CI failure.

### Current explicit initial classifications

The implementation agent must audit all 26 planes, not copy this small list blindly. These are non-negotiable starting points:

| Plane | Initial V1 posture | Reason |
| --- | --- | --- |
| `SecretGuardExecution` + guards | canonical required when active | guard stage receives full Call; quarantine/audit/denial semantics |
| `LocalTurnHandlers` | canonical required when occupied | `Match`/`Handle` receive full Call; can claim/short-circuit |
| `TerminalDecisionProvider` | canonical required unless continuation-source parity implemented | bounded provider input is insufficient when decision can continue trajectory |
| request/attempt transforms and request-part hooks | canonical required unless explicit wire contract | mutate/inspect canonical request |
| response-only observers | potentially response-only (SUPERSEDED: Blocker 2 conservative fail-safe blocks when occupied; response pipeline reuse deferred; follow-up to PR #629 review) | operate on canonical output events, subject to characterization |
| bounded metadata-only session/workspace authorities | potentially metadata-only | only after exact bounded input parity |

### Separate hook bus

`hooks.Bus` is not a typed plane. Freeze occupancy/chain categories into the same summary. Call-mutating/request-content chains are blockers unless wire-certified.

### Current narrow/non-plane ports

Task 1 must classify at least current executor/config dependencies:

- prompt cache maintenance;
- conversation view reader/tagger/observer and steering writer factory;
- terminal policy reader;
- interleaved processor;
- compaction detector/background auxiliary;
- request token estimator;
- catalog/capability/eligibility and backend-execution resolvers;
- route override reader;
- secure-session manager/recorder and denial policy;
- metering recorder;
- accounting preflight/stream reconstruction/usage authority/concurrency coordinators/terminal work;
- billing credit gate/exposure admission/leg observer/terminal sink;
- `BillingIdentity` Call-shaped callbacks;
- traffic raw/observer/redactor facilities;
- any custom Call-shaped callback added by composition.

Unknown is canonical, never “probably metadata-only.”

---

## 8. Side-Effect-Free Assessment

Illustrative internal interface:

```go
type LargeBodyExecutorView interface {
    LargeBodyStaticDisposition(profileID string) (StaticWireDisposition, StaticWireReason)
    AssessLargeBody(context.Context, AssessmentRequest) (Assessment, error)
    ExecuteLargeBody(context.Context, ExecuteRequest) (ExecutionResult, error)
}
```

Public `lipsdk.ExecutorView` is unchanged in V1. Standard bundled frontends may type-assert the internal concrete executor/interface; external/manual frontends remain canonical-only.

### Assessment input

Contains proof + immutable generation facts only. It must not carry a full request body reader because assessment is not allowed to inspect prompt content; protocol proof already produced the bounded facts.

### Assessment work

1. verify frozen eligibility summary is not statically incompatible;
2. resolve exact selector/default aliases using current pure generation data;
3. derive the same candidate set/order/composition semantics as canonical routing;
4. compute the finite/universal late route-override domain without reading the live route-override store;
5. resolve pure backend exact/domain wire support for every possible candidate/domain member;
6. verify body/rewrite/model-span compatibility;
7. verify secure/metering/accounting/billing/counting capabilities;
8. verify current non-plane/custom Call callbacks are either bounded wire facts or blockers;
9. issue an opaque generation/proof-bound acceptance stamp.

### Forbidden during assessment

Tests must fail if assessment:

- calls `SecureSession.BeginTurn`;
- creates/fetches an A-leg;
- reads or mutates DB/store/continuation/route-override state;
- reserves billing/accounting/usage authority;
- opens provider/network connections;
- reads replay/spill bytes;
- waits on client body;
- invokes arbitrary plugin callbacks whose bounded/pure contract is not declared.

Assessment happens under the decode permit, so bounded CPU matters. Precompute route-domain/backend compatibility facts at composition time where possible, but do not create a second provider-specific core registry.

---

## 9. Backend Wire Contract

Extend the internal backend abstraction additively with provider-neutral capability functions, equivalent to:

```go
type WireRequestSupport struct {
    Compatible        bool
    NeedsModelRewrite bool
    // bounded reason enum for diagnostics
}

type WireDomainSupport struct {
    Compatible       bool
    AnyAcceptedModel bool
}

// Pure, no I/O.
ResolveWireRequest(ctx, WireRequestFacts, AttemptCandidate) WireRequestSupport
ResolveWireDomain(ctx, WireDomainFacts) WireDomainSupport

// Post-commit only.
OpenWire(ctx, WireOpenRequest) (lipapi.ManagedEventStream, error)
```

Proof receives immutable `BodyMode` and `RewriteSemantics`; `NeedsModelRewrite` cannot magically authorize a transformation that the frontend profile did not certify.

### OpenWire implementation rule

For OpenAI-compatible backends, reuse current:

- base URL/endpoint resolution;
- credential pool/acquire/cooldown/auth-invalid handling;
- shared HTTP client and client options;
- SDK retry policy choice;
- first-recv/stream-peek semantics;
- provider response parsing and error classification.

The new path should replace only request-body construction/serialization, not create a parallel transport/security stack.

### HTTP framing/security

Build outbound method/path/query/headers from backend-owned canonical logic. Never forward client headers wholesale. Rewritten body length must control outbound `Content-Length` if a known length is sent. Do not leak stale `Transfer-Encoding`, `Content-Encoding`, `Expect`, trailers, client auth, or LIP session/control headers.

Test HTTP/1.1 and HTTP/2 with the same shared client policy, cancellation, connection reuse, and redirect behavior.

---

## 10. Secure Session, Metering, Accounting, and Billing

### Secure session

Protocol proof emits bounded `SessionInput` and `ClientTurnShape`. Wire execution refactors existing secure-session fact construction so both canonical and wire paths share authority rules. Only after one-way commit may it call `BeginTurn`.

`BeginTurn` results required by the frontend return through a sensitive carrier:

```go
type SessionResponseCarrier struct {
    AuthoritativeSessionID string
    ALegID                 string
    ResumeToken            secretString // illustrative non-loggable wrapper
}
```

The token is frontend-only and not available to provider/metrics/general diagnostics.

### Metering

Current Call-cloning checkpoints cannot be used on the accepted wire lane. Introduce wire-native checkpoints from bounded facts and source/rewrite digests. Preserve public checkpoint identity and exact-once semantics.

### Token accounting

If configured admission requires input tokens and only `CountCall` is available, assessment declines. A future/implemented `WireCounter` must count exactly from the certified source/profile semantics; bytes are never tokens.

### Billing

Refactor stock Call-shaped callbacks only where semantics can be expressed exactly as bounded facts. Custom `BillingIdentity` Call callbacks are blockers by default. Reservation/settlement/idempotency begins only after wire commit and stays under existing lifecycle ownership.

---

## 11. Post-Commit Runtime Facts: No Shadow Call

Create only DTOs required by downstream consumers, for example:

- request/trace/economic identity;
- operation/delivery/protocol requirements;
- selector/client model/effective model;
- max-output bound;
- session/A-leg/workspace/principal scope;
- source digest/body bytes;
- rewrite semantics and per-attempt rewrite digest;
- bounded conversation/terminal facts only where proven independent of prompt content.

Do not add a struct that gradually mirrors every field of `lipapi.Call`.

### Audit targets on current main

The implementation inventory must trace current `lipapi.CloneCall` and Call retention through:

- preparation and accepted ingress/backend baselines;
- conversation projection/filter baselines;
- `recvTurnFacts`;
- terminal evidence/call closure;
- attempt derivation and clamp preview;
- continuation/interleaved paths;
- metering/accounting checkpoints;
- billing callbacks;
- local turn / secret guard / terminal decision;
- frontend response helpers.

Architecture tests should define the accepted wire package/function boundary and reject `lipapi.Call`/`CloneCall` ingress there except for explicit response-only canonical APIs. Do not grep only `preparedRequest.call`.

---

## 12. ExecuteLargeBody and Existing Attempt Ownership

After stamp validation and source ownership transfer:

1. perform wire secure-session preparation and exactly one `BeginTurn`;
2. apply normal live route-override snapshot/barrier inside the already certified route domain;
3. perform normal request authority/economic admission;
4. use the existing attempt/recovery owner for sequential/fallback/race behavior;
5. allocate B-legs/metrics/TTFT/failure history as today;
6. for each attempt, open a fresh replay reader at offset zero;
7. apply only assessment-approved model token splice for that candidate;
8. call the candidate backend's `OpenWire`;
9. preserve first-visible-event commitment and no-failover-after-output behavior;
10. preserve terminal accounting/session/attempt cleanup.

If attempt 1 fails pre-output, attempt 2 must see a complete replay. Parallel race attempts receive independent readers.

---

## 13. Frontend Response and Keepalive Bridge

`ExecutionResult` must include canonical stream plus bounded response facts:

```go
type ExecutionResult struct {
    Stream        lipapi.EventStream
    ResponseFacts ResponseFacts
    Session       SessionResponseCarrier
}
```

Frontend-owned profile state plus those facts must be sufficient for current `BuildEncodeOpts`, `WrapStream`, response writers, cancellation IDs, and session headers.

### Keepalive parity

Current shared pipe has two relevant mechanisms:

- `StreamKeepaliveInterval` is placed into request context before body processing;
- streaming `Execute` is wrapped in `holdalive.Wait` using `PreRequestKeepalive`.

The candidate flow must preserve both. Implement the wire invoke under the same holdalive wrapper after proof/assessment/commit. Do not send keepalive before complete validation/commit merely to hide a slow upload or assessment.

OpenAI Responses must preserve the A-leg/session cancellation carrier encoded in response ID. Chat/Responses deterministic IDs/timestamps continue to derive from canonical semantic identity where current behavior does so.

---

## 14. Protocol Certification Order

Certification is sequential. A later lane may reuse infrastructure only after the earlier lane proves it.

### Lane 1 — OpenAI Responses frontend → OpenAI-compatible Responses backend

Current frontend facts to freeze in tests:

- no legacy `ResolveRouteSelector` in the create spec;
- `RouteFromBodyModel=true`;
- header selector precedence over body model/default;
- response ID/cancellation/session carrier behavior;
- stable created-at/identity behavior;
- stream/non-stream writers.

Initial profile is intentionally narrow. Unknown/normalization-sensitive/body-LIP-metadata shapes are canonical.

### Lane 2 — OpenAI Chat Completions → OpenAI-compatible Chat

Same architecture, with Chat message/tool/function/reasoning normalization and completion response identity characterized independently.

### Lane 3 — OpenResponses HTTP create → OpenResponses-compatible backend

Initial subset only:

- HTTP create;
- explicit `store:false`;
- no `previous_response_id`;
- no compaction;
- no WebSocket.

Outer auth + JSON media-type checks stay before `frontendpipe`. `store:true`/continuation requires a later explicit certification because current `prepareCreateState` performs reservation/lineage/recorder work and may materialize parent continuation state.

### Gzip

Wave 1 remains canonical. Decoded-stream support is a later wave only after exact decoded limits/framing/body-mode semantics are proven.

---

## 15. Performance Model and Hard Gates

### What should disappear on accepted wire requests

The accepted path should avoid:

- whole-body `[]byte` retention after spill;
- full protocol DTO decode of prompt-sized content;
- `lipapi.Call` message/item/tool tree construction proportional to prompt size;
- multiple `CloneCall` copies of that tree;
- provider request re-serialization proportional to the canonical tree;
- Call-retaining metering checkpoints.

### What still exists

- one full client upload to local replay storage;
- JSON validation/protocol proof CPU;
- source file I/O above memory threshold;
- bounded semantic metadata;
- provider upload from replay;
- output/event processing.

### Heap invariant

For an accepted spill-backed request, request-attributable Go heap is bounded by:

```text
memory_spool_bytes
+ max_semantic_fact_bytes
+ O(fixed scanner/copy/http buffers)
+ bounded runtime/response metadata
```

It must not contain a payload-sized body slice/string, full Call tree, or payload-scale clone. Compare 1 MiB, 5 MiB, and test-only 20 MiB profiles; accepted wire heap should remain approximately flat/bounded while canonical remains proportional to request materialization.

Do not impose a brittle single nanosecond threshold across platforms. Gate on architecture + heap scaling + material multi-MiB benefit and report CPU/file-I/O tradeoffs separately.

### Static-blocker benchmark

A generation that is definitely canonical (for example occupied Local Turn without wire contract) must show essentially canonical baseline overhead: no spool file, no replay source, no streaming scanner/profile, no decode-permit behavior change. This benchmark prevents the optimization infrastructure itself from taxing common incompatible deployments.

---

## 16. Testing and Ratchets

Minimum suites:

1. canonical characterization before refactor;
2. streaming JSON differential/fuzz vs current preflight;
3. replay short-write/failure/cancel/Windows cleanup state tests;
4. canonical semantic identity differential/fuzz;
5. 26-plane + hook + non-plane authority census ratchet;
6. static disposition no-allocation/no-spool tests;
7. same-permit fallback under admission saturation;
8. side-effect sentinel tests for `AssessLargeBody`;
9. secure-session first-turn → resume-next-turn with wire first turn;
10. metering/accounting/billing exact-once tests;
11. route-domain tests for homogeneous/heterogeneous, sequential/fallback/race;
12. provider request semantic/body/model rewrite tests;
13. HTTP/1.1 + HTTP/2 framing/header/client-policy parity tests;
14. retry/replay/race/cancel tests;
15. response identity/cancellation/keepalive/session-header parity tests;
16. no-Call/no-Clone post-commit architecture tests;
17. multi-size heap/GC/allocation/load benchmarks;
18. full repo QA/race/static checks.

No production lane advertises support until its own differential certification and realistic secure-session + metering composition are green.

---

## 17. Failure Ownership Summary

| Failure point | Result |
| --- | --- |
| feature/profile/static disposition unavailable | canonical, before capture |
| known identity request length below threshold | canonical, before capture |
| legacy full-body route resolver present | canonical, before capture |
| spool reservation/create/write recoverably unavailable | lossless canonical continuation |
| malformed/over-limit JSON | same canonical frontend error; no provider open |
| decode admission rejects | same 429/503 behavior; no provider open |
| profile proof uncertain | canonical decode under same permit |
| assessment blocker/incompatible route domain | canonical decode under same permit |
| assessment accept | one-way wire commit |
| post-commit unexpected content need | internal invariant failure; clean one turn; no canonical re-execute |
| provider pre-output failure | existing retry/failover using fresh replay reader |
| provider failure after visible output | existing committed-stream behavior; no new failover |

This table is the implementation agent's fallback oracle.
