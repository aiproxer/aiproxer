# Design Document

## 1. Overview

Issue #369 is a narrow brownfield follow-up to the existing `reasoning-output-preservation` feature. It does not replace exact reasoning preservation, OpenAI Responses exact-item replay, Codex native continuity/compaction, or compaction-continuity preservation.

Existing authoritative lifecycle:

```text
final surfaced canonical stream
    -> reasoning-preservation StreamObserver
    -> authoritative TurnStore
    -> later candidate AttemptTransform
    -> historical reasoning restoration
```

The new optional lane starts only after the authoritative original artifact exists:

```text
final surfaced winner
        |
        v
existing reasoning observer
        |
        +-- exact/native/signed/opaque ---> append original only
        |
        +-- canonical semantic text
                |
                v
          append original first
                |
                v
      optional-state reservation
       (session + aggregate budgets)
                |
                v
        egress policy decision
       deny / redact / allow
                |
                v
    SubmitCollect(detached compressor)
                |
                v
       bind JobID to reservation
                |
          Finish returns
                |
          later matching turn
                |
                v
   optional BackgroundPoller.Poll
        | pending -> original
        | failed  -> original
        | complete
        v
 bounded raw text extraction
      BEFORE JSON decode
        |
        v
 schema + savings validation
        |
        v
 CAS attach bounded surrogate
        |
        +-- shadow -> original
        |
        +-- active + destination supports original dialect
                    -> surrogate textual placements only
```

### Non-negotiable invariant

`TurnArtifact.Reasoning` remains authoritative and retained under the existing reasoning-preservation lifetime/eviction semantics. Compression adds optional state. No compression failure, timeout, privacy denial, budget exhaustion, reload, stale result, parser failure, or active-mode decision may destroy the original merely to make the optimization work.

## 2. Spec Relationship and Ownership

This specification is a **follow-up**. It does not supersede completed specs for:

- reasoning output preservation;
- reasoning-preservation E2E validation;
- OpenAI Responses reasoning preservation;
- OpenAI Codex native compaction;
- compaction continuity preservation.

Ownership remains:

| Concern | Authority |
| --- | --- |
| surfaced-winner reasoning capture | `reasoningpreservation.StreamObserverFactory` |
| authoritative original reasoning state | reasoning-preservation `TurnStore` |
| historical reinjection | reasoning-preservation `AttemptTransform` / restore domain |
| canonical reasoning dialect/structure | `pkg/lipapi` reasoning contracts |
| detached auxiliary execution | `pkg/lipsdk/auxiliary` + `internal/core/auxreq` |
| background worker lifetime | process-owned auxiliary scheduler |
| routing/B2BUA | existing Executor/core authorities |
| credit/usage/billing | existing billing/metering authorities |
| provider-native exact continuity | provider/native owner, notably direct Codex |
| semantic compressor prompt/result contract | reasoning-preservation compression subdomain |

`compactioncontinuity` feature packages are not imported. The reusable dependency is generic auxiliary infrastructure only.

## 3. Brownfield Constraints

Current `main` provides:

- final-stream observer semantics where `success_released` is the only safe capture terminal;
- `TurnArtifact` containing exact placed reasoning plus anchor/backend/model/byte metadata;
- bounded in-memory `TurnStore` with TTL, turns/session, bytes/turn, and bytes/session;
- attempt-time `ReasoningReplaySupport` used to decide whether a destination can represent a historical reasoning dialect;
- exported `auxiliary.BackgroundClient` with `SubmitCollect`, `Await`, and `Forget`;
- a process-owned bounded background scheduler with queue/result limits, job timeout, generation pinning, coalescing, and detached execution support;
- auxiliary request envelope metadata for role, visibility, detached session mode, parent lineage, disabled plugins, and a canonical child `Call`;
- trusted principal/scope propagation in auxiliary execution context for accounting and authorization.

The design must therefore avoid:

- widening provider/backend contracts unless evidence requires it;
- making existing `BackgroundClient` implementations source-incompatible;
- adding a second worker runtime;
- adding a second reasoning store/transcript store;
- provider-name special cases in generic core;
- synchronous compressor waits in response release or ordinary replay.

## 4. Canonical Semantic Classification

### 4.1 Types

The reasoning-preservation feature owns a pure classifier over canonical artifacts, conceptually:

```go
type ReplaySemantics uint8

const (
    ReplayUnknown ReplaySemantics = iota
    ReplayExactRequired
    ReplaySemanticText
)

type SegmentSemantics struct {
    PlacementIndex int
    Dialect        lipapi.ReasoningDialect
    Semantics      ReplaySemantics
    SourceBytes    int
}
```

Inputs are canonical reasoning parts, dialects, and presence semantics. Provider/model string matching is forbidden.

### 4.2 Initial positive/negative scope

V1 positive case is intentionally narrow: ordinary plain-text historical reasoning whose canonical dialect/structure has no exact, opaque, signed, or native authority.

Fail closed to exact/original for:

- OpenAI Responses reasoning items / encrypted content;
- Codex exact/native continuity payloads;
- Anthropic signed thinking;
- redacted/opaque thinking;
- any reasoning part with non-empty signature or opaque authority incompatible with text replacement;
- unknown dialects;
- malformed/contradictory structures;
- mixed individual parts where a safe text-only subset cannot be identified without mutating exact authority.

A turn may contain both exact and semantic-text **parts**. The classifier returns per-placement semantics; only independently semantic-text placements may be compressed.

### 4.3 Destination use

Creation safety and destination representability are separate checks. Active substitution requires:

1. original placement still classifies `ReplaySemanticText`;
2. surrogate correlation matches the authoritative artifact/policy revision;
3. destination `ReasoningReplaySupport` can represent the original dialect;
4. normal restore rules do not classify client reasoning as already preserved.

No new semantic-permission field is added to backend replay ABI in v1. If real evidence shows a plain-text canonical dialect that nevertheless requires exact bytes for some provider, this design must be revised rather than patched with provider-name exceptions.

## 5. Configuration

Compression is nested under reasoning preservation and disabled by default.

Conceptual immutable configuration:

```go
type CompressionMode string
const (
    CompressionShadow CompressionMode = "shadow"
    CompressionActive CompressionMode = "active"
)

type CompressionConfig struct {
    Enabled bool
    Mode    CompressionMode
    Route   string // explicit; no inherit in v1

    Timeout time.Duration

    MaxInputTokens int
    MaxInputBytes  int

    MaxOutputTokens int // upstream generation request bound
    MaxOutputBytes  int // raw collected text bound BEFORE decoding

    MaxSurrogateBytes int // decoded semantic payload bound
    MinSourceBytes    int
    MinSavedBytes     int
    MinSavingsRatio   float64

    MaxPendingPerSession        int
    MaxSurrogateBytesPerSession int
    MaxPendingTotal             int
    MaxSurrogateBytesTotal      int

    Egress EgressConfig
}
```

### 5.1 Raw output vs surrogate bounds

`MaxOutputBytes` and `MaxSurrogateBytes` are deliberately different:

- `MaxOutputTokens` asks the selected model/provider to limit generation;
- `MaxOutputBytes` is a local allocation/parser guard over the **entire raw collected textual response**, including JSON syntax and fields;
- `MaxSurrogateBytes` bounds the decoded surrogate text actually retained.

`MaxOutputBytes` must be enforced before JSON unmarshalling and before constructing an unbounded combined string from canonical collected fragments. The generic scheduler's own `MaxResultBytes` remains a process-level outer ceiling but does not replace a stricter feature-specific raw limit.

### 5.2 Defaults and validation

- `enabled=false` means no new work/state/telemetry/cost.
- Enabled mode defaults to `shadow`.
- Enabled v1 requires a non-empty explicit `route`.
- All numeric bounds are positive and below hard ceilings.
- `MaxOutputBytes >= MaxSurrogateBytes` plus schema overhead margin.
- aggregate optional-state limits must be >= their corresponding per-session/per-turn limits.
- active mode uses the same capture/adoption path as shadow; it only changes final surrogate selection.
- data-egress approval is separate from route syntax and is mandatory under §10.

Standard companion/injected reasoning-preservation configuration does not enable compression automatically.

## 6. Artifact and Store Model

### 6.1 Authoritative original

Existing fields remain authoritative:

```text
TurnArtifact
  ID
  Anchor
  SourceBackend
  SourceModel
  Reasoning[]        <-- authoritative original
  CreatedAt
  ReasoningBytes
```

Optional additions are conceptually:

```go
type CompressionState struct {
    PolicyRevision string
    ReservationID  string
    Pending        *PendingCompression
    Surrogate      *ReasoningSurrogate
}

type PendingCompression struct {
    JobID            auxiliary.JobID
    OriginalDigest   [32]byte
    SemanticDigest   [32]byte
    EgressPolicyHash [32]byte
    CreatedAt        time.Time
}

type ReasoningSurrogate struct {
    OriginalDigest [32]byte
    PolicyRevision string
    Sanitization   string // none|redacted
    Segments       []SurrogateSegment
    Bytes          int
}
```

No raw parent/session/account identifier is required inside model-facing surrogate state.

### 6.2 Reservation-before-submit

After the original append succeeds, the feature first performs an **optional-state reservation** under the store's lock/CAS authority:

1. locate exact artifact ID + original digest;
2. verify no current pending/surrogate for the same policy revision;
3. check per-session pending count;
4. check feature-instance `MaxPendingTotal` across all sessions;
5. record bounded reservation token.

Only a successful reservation may proceed toward data-egress approval and provider submission. This prevents provider work from being started when the feature already knows no optional state can be retained.

If policy denies before submit, clear the reservation. If `SubmitCollect` fails, clear it. If the job is submitted but JobID binding loses a stale/CAS race, call `Forget` when safe and clear the reservation; provider work already incurred remains billable.

### 6.3 Separate optional budgets

Authoritative `ReasoningBytes` retains its existing FIFO/TTL limits. Optional state has independent accounting:

- pending count per session;
- pending count across the feature instance;
- surrogate bytes per turn;
- surrogate bytes per session;
- surrogate bytes across the feature instance.

Aggregate counts/bytes are maintained under the same store lock/atomic authority used for optional-state mutation. Expiry, delete, original eviction, surrogate replacement/clear, and stale cleanup decrement aggregate counters exactly once.

**Optional-state exhaustion never evicts an authoritative original.** The optimization is skipped instead.

This feature-instance bound prevents an attacker or high-volume user from bypassing per-session limits by opening many sessions. A later account-specific quota can be added independently if product policy requires it; v1 requires at least the process/feature-instance hard bound for memory safety.

### 6.4 Store port

The internal `TurnStore` may be extended because it is feature-internal, for example:

```go
type CompressionStore interface {
    ReserveCompression(ctx, partition, artifactID, originalDigest, policyRev) (Reservation, error)
    BindCompressionJob(ctx, partition, artifactID, reservationID, jobID) error
    AttachSurrogate(ctx, partition, artifactID, pending, surrogate) error
    ClearCompression(ctx, partition, artifactID, reservationOrJobID) error
}
```

Exact naming is implementation detail. Required semantics are CAS/stale protection, defensive copies, original-coupled lifetime, and separate optional budgets.

## 7. Source-Compatible Background Poll Capability

### 7.1 Existing interface remains unchanged

Current exported SDK contract remains:

```go
type BackgroundClient interface {
    SubmitCollect(context.Context, Request, SubmitOptions) (JobID, error)
    Await(context.Context, JobID) (lipapi.Collected, error)
    Forget(JobID)
}
```

Adding `Poll` directly would break any external implementation. Therefore v1 must not change that required method set.

### 7.2 Optional capability

Add a separate optional exported capability, conceptually:

```go
type PollState uint8
const (
    PollPending PollState = iota
    PollCompleted
    PollFailed
    PollNotFound
)

type PollResult struct {
    State     PollState
    Collected lipapi.Collected // defensive copy only when completed
    Err       error            // terminal job failure when state=failed
}

type BackgroundPoller interface {
    Poll(context.Context, JobID) (PollResult, error)
}
```

The process-owned scheduler implements both interfaces. Existing external implementations satisfying only historical `BackgroundClient` remain source-compatible.

Enabled reasoning compression requires composition to supply both:

- the existing `BackgroundClient` for submit/forget;
- an optional poll capability view for non-blocking adoption.

This may be represented as two typed fields in a feature-internal service struct or by a checked type assertion against the same scheduler-backed value. No service locator is introduced.

### 7.3 Poll semantics

`Poll` must:

- never wait for job completion;
- clone completed `Collected` state defensively;
- distinguish pending/completed/failed/not-found-expired;
- run existing scheduler cleanup before lookup as appropriate;
- preserve `Await` semantics unchanged;
- not consume/forget the result automatically;
- return no content for pending/not-found;
- be race-safe against `Finish`, `Forget`, expiry, and shutdown.

## 8. Compressor Domain

The feature owns a small compressor subpackage, not a generic summary framework.

### 8.1 Input

Input consists only of eligible semantic-text placements after egress policy/sanitization:

```json
{
  "schema_version": 1,
  "segments": [
    {"index": 0, "text": "..."},
    {"index": 2, "text": "..."}
  ]
}
```

Indexes are local placement identifiers, not session/trace/account/provider identifiers.

The model sees:

- fixed system instruction describing semantic compression;
- strict output schema;
- sanitized/bounded segment JSON as untrusted quoted data.

The model does **not** see tool output, ordinary assistant answer text, user transcript, files/media, signatures, opaque reasoning, native checkpoints, parent lineage, principal/account IDs, credentials, or raw artifact anchors/digests.

### 8.2 Auxiliary request envelope

Control-plane metadata is intentionally separate from model content:

```text
auxiliary.Request envelope (trusted, not model prompt)
  Role = reasoning_preservation_compressor
  Visibility = private
  SessionMode = detached
  ParentTraceID / ParentALegID / ParentBLegID / optional branch binding
  DisablePlugins = [reasoning-output-preservation]
  Call -> canonical no-tools child prompt

execution context
  cloned trusted principal/scope, marked internal by auxreq policy
```

Role/visibility/lineage/scope are required for authorization, routing, correlation, generation ownership, and billing. The design only excludes them from **child model input and content-bearing telemetry**, not from the entire auxiliary request/execution envelope.

### 8.3 Output schema

One artifact uses at most one compressor call. Multiple semantic placements may be compressed in one response:

```json
{
  "schema_version": 1,
  "segments": [
    {"index": 0, "text": "compressed reasoning"}
  ]
}
```

Strict decoder rejects unknown fields, duplicate indexes, missing expected indexes, unexpected indexes, empty required text, invalid UTF-8/controls, tool calls, and non-text channels.

## 9. Raw Result Bounding Before Decode

A key hardening rule is that decoded schema validation is **not** the first size check.

Processing order:

```text
Poll -> completed Collected
   -> reject tool calls/non-text channels
   -> iterate canonical text fragments with byte counter
   -> if bytes > MaxOutputBytes: raw_oversize; stop
   -> materialize <= MaxOutputBytes raw bytes
   -> strict JSON decode
   -> validate indexes/text/control characters
   -> compute decoded surrogate bytes
   -> enforce MaxSurrogateBytes + session/aggregate budgets
   -> enforce savings
```

Do not call an API that constructs the entire returned text string before checking `MaxOutputBytes` if that API can allocate beyond the feature limit. The generic scheduler's process `MaxResultBytes` is a defense-in-depth outer bound, not the feature parser contract.

This directly prevents a provider/model that ignores requested max output tokens from turning JSON decode into a large local allocation path.

## 10. Ordinary-Text Privacy and Data-Egress Policy

### 10.1 Semantic safety is not data sensitivity

`ReplaySemanticText` says the **representation** may be semantically transformed. It says nothing about whether the text contains:

- API keys/tokens/passwords;
- personal or regulated data;
- proprietary source/code/business logic;
- customer data;
- location/identity data;
- material subject to residency or retention constraints.

Therefore detached/private auxiliary execution is insufficient as a privacy control by itself.

### 10.2 Narrow feature-scoped egress policy

The compression composition receives a trusted policy seam, conceptually:

```go
type EgressAction uint8
const (
    EgressDeny EgressAction = iota
    EgressAllow
    EgressRedactThenAllow
)

type CompressionEgressInput struct {
    Route          string
    PrincipalScope scope.PrincipalScopeView // control plane only
    Purpose        string // reasoning_semantic_compression
    SourceClass    string // semantic_reasoning_text
}

type CompressionEgressDecision struct {
    Action        EgressAction
    PolicyVersion string
    Sanitizer     TrustedTextSanitizer // required for redact_then_allow
}
```

The actual API may be narrower. Required behavior:

- explicit route alone is not approval;
- trusted operator policy decides whether the selected route satisfies applicable retention/residency/consent/provider-processing rules;
- deny => clear optional reservation and keep original;
- redact => reuse existing trusted secret/redaction policy where available, sanitize locally before input budgeting/prompt construction, then send only sanitized text;
- if redaction is required but no trusted sanitizer can satisfy policy => deny;
- no second feature-local heuristic secret detector is introduced when the repository already has the relevant secret/redaction authority.

For a wholly local/same-trust-boundary compressor, the policy may allow without remote processing constraints according to operator configuration. The policy remains explicit so implementation does not infer locality from model names.

### 10.3 Policy metadata

Store only a bounded content-free policy version/hash and sanitization class with pending/surrogate state. Never persist policy secrets or full route credentials.

Tests use fake policy/sanitizer implementations; no real external credentials are needed.

## 11. Compressor Submission Lifecycle

Submission occurs after `success_released` and after original append:

1. derive canonical semantic placements;
2. skip exact/unknown/below-min-source cases;
3. reserve optional pending capacity under per-session + aggregate limits;
4. evaluate trusted egress policy;
5. redact locally if required;
6. enforce sanitized input byte/token bounds;
7. build detached no-tools auxiliary request with explicit route;
8. `SubmitCollect` through generation-bound background client;
9. bind returned JobID to the exact reservation/artifact digest;
10. return from observer without waiting.

If any step before provider submission fails, clear reservation and no provider cost occurs. If provider work has been accepted but subsequent binding fails, forget result when possible and preserve billing/accountability for incurred work.

Compression work is never submitted for unsuccessful/superseded B-legs because the observer only reaches this lane from the surfaced `success_released` lifecycle.

## 12. Result Adoption Lifecycle

The existing attempt transform owns adoption because it already knows:

- authoritative session partition;
- artifact match;
- destination candidate;
- negotiated replay support;
- exact/current client history.

For a matching artifact with pending compression:

1. inspect poll capability once; never wait;
2. pending => original for this attempt;
3. failed/not-found => clear optional pending state, original;
4. completed => apply §9 raw byte bound before decode;
5. strict decode/validate;
6. verify job/artifact/original digest/policy correlation;
7. enforce decoded surrogate + session + aggregate bytes and savings;
8. CAS attach surrogate and clear pending count;
9. `Forget(jobID)`;
10. shadow => original;
11. active => run destination checks, then use surrogate only for eligible placements.

Poll/parser/store errors are optimization-local fail-open conditions and must not trigger authoritative reasoning `on_state_error=reject` candidate exclusion.

## 13. Surrogate Selection and Placement

`ReasoningSurrogate` is a replacement view only for textual payloads, never a replacement artifact.

Active selection builds an ephemeral restoration artifact:

- copy original placements and non-compressible parts;
- for each validated surrogate segment, replace only `Reasoning.Text` of the exact semantic placement;
- preserve `BeforeNonReasoningPart` unchanged;
- preserve dialect needed for destination representation;
- never modify signature/opaque/native fields;
- never alter tool IDs, tool ordering, ordinary assistant text, images/files, or other canonical message structure.

If any placement correlation is ambiguous, use the original artifact for the whole affected restoration rather than guessing.

## 14. Billing and Economic Attribution

Compressor child execution uses normal economics:

```text
class = auxiliary
role  = reasoning_preservation_compressor
principal/account = originating trusted scope
child A-leg/B-leg/BillingCallID = ordinary detached auxiliary lineage
```

Required semantics:

- pre-submit credit/exposure rejection => no provider work;
- submitted work => actual usage/cost remains accountable even if output is invalid/stale/insufficient/private-policy-rejected for adoption;
- primary frontend protocol usage excludes compressor usage;
- account/operator totals include it;
- no feature-owned ledger, pricing engine, or cost accumulator.

## 15. Shadow and Active Modes

### Shadow

Shadow is default when compression is enabled.

It executes the entire path—classification, privacy decision, reservation, auxiliary inference, raw bound, parsing, validation, CAS storage, savings accounting—but always restores the original reasoning.

Shadow evidence measures:

- eligible/ineligible count;
- privacy allow/redact/deny;
- source bytes;
- raw output bytes;
- decoded surrogate bytes;
- hypothetical saved bytes/tokens;
- latency;
- queue/admission/provider/parser/stale/aggregate-budget outcomes;
- auxiliary usage/cost through existing billing surfaces.

### Active

Active changes only the final selection after all shadow-path contracts are proven. It remains explicit operator configuration and still falls back to original on every uncertainty.

## 16. Observability and Privacy

Content-free outcomes include:

- exact/ineligible/unknown;
- below source threshold;
- optional reservation/session-limit/aggregate-limit denied;
- egress allowed/redacted/denied/missing-policy;
- submitted/coalesced/queue saturated/admission denied;
- pending/completed/provider failed/not-found;
- raw_oversize;
- decode/schema/control invalid;
- insufficient_savings;
- stale/correlation conflict;
- surrogate attached;
- shadow_ready;
- active_used;
- original_fallback.

Safe numeric observations may include bounded source/raw/surrogate byte counts, savings, and duration.

Never log or emit as content-bearing telemetry:

- original reasoning text;
- surrogate text;
- redacted-before/after text;
- signatures/opaque JSON;
- prompts;
- credentials;
- raw session/account/principal/lineage identifiers;
- raw anchors/digests.

Control-plane metadata still exists where required by the auxiliary execution path; use hashes/classes/counts in diagnostics when existing policy allows.

## 17. Concurrency, Reload, and Shutdown

Invariants:

1. Original store mutation and optional-state counters are synchronized by the store authority.
2. Reservation prevents aggregate oversubscription across concurrent sessions.
3. A stale completion cannot overwrite a newer artifact/policy revision.
4. Poll is race-safe with scheduler completion, `Forget`, expiry, and shutdown.
5. Submitted work keeps existing `KindAsync` generation ownership until terminal completion/cleanup.
6. Generation reload changes future submissions only; a queued job uses captured generation/route/policy snapshot as existing auxiliary semantics require.
7. Compression does not hold store locks while making policy/provider calls.
8. No feature-owned goroutine polls results in the background.
9. Original expiry/delete/eviction invalidates pending/surrogate state and decrements aggregate counters exactly once.
10. Process restart loses process-local compression state consistently with existing reasoning-preservation v1 durability; no fake durable recovery is introduced.

## 18. Failure Semantics

| Failure | Required behavior |
| --- | --- |
| exact/unknown artifact | no compression; original |
| per-session/aggregate optional budget full | no submission; original |
| egress policy deny/missing required policy | no submission; original |
| required sanitizer unavailable/fails | no submission; original |
| input too large | no submission; original |
| queue full | clear reservation; original |
| billing/admission reject | clear reservation; original |
| provider failure/timeout | clear pending when observed; original |
| poll unavailable/pending | original, no wait |
| raw result exceeds `MaxOutputBytes` | reject before decode; original |
| malformed schema/tool calls | reject; original |
| decoded surrogate too large | reject; original |
| insufficient savings | reject; original |
| stale/correlation mismatch | reject/forget; original |
| destination cannot represent original dialect | original/unrepresentable policy as existing behavior |
| active substitution validation fails | original |

No compression failure becomes primary retry/failover authority.

## 19. Expected Change Surface

Minimal expected implementation areas:

- `internal/plugins/features/reasoningpreservation`
  - semantic classifier;
  - nested compression config;
  - optional artifact/store state + aggregate counters;
  - compressor request/parser/validation;
  - observer post-original shadow submission;
  - attempt-transform non-blocking adoption + final selection;
  - content-free telemetry.
- `pkg/lipsdk/auxiliary`
  - **new separate optional** poll capability/result types; historical `BackgroundClient` unchanged.
- `internal/core/auxreq`
  - scheduler implementation of optional poll capability.
- `internal/infra/runtimebundle` / standard feature composition
  - explicit generation-bound compression auxiliary/poller + egress policy/sanitizer binding without service locator.
- existing billing/report surfaces
  - no new accounting engine; only existing auxiliary workload role/class evidence if needed.
- docs/config examples/tests/architecture fixtures during implementation.

Provider/backend/frontends should not need semantic-compression-specific branches.

## 20. Testing Strategy and Implementation Order

TDD order is intentionally dependency-gated:

1. RED disabled/exact/signed/native/source-compatibility/privacy/aggregate-bound/raw-limit invariants.
2. GREEN canonical classifier, config, separate `BackgroundPoller`, and non-destructive optional-state store/reservation/counters.
3. RED/GREEN isolated compressor builder, egress-policy seam, sanitizer path, bounded raw extractor, strict decoder, and savings validator.
4. GREEN original-first **shadow-only** observer submission after `success_released`; no active replay.
5. GREEN non-blocking shadow adoption with poll/raw-decode/CAS/aggregate-counter cleanup; still replay originals.
6. GREEN active destination-gated textual substitution with placement/mixed exact-part invariants.
7. certify billing, privacy, reload, race/goleak/fuzz, multi-session aggregate bounds, oversized raw responses, shadow value, protocol lifecycle, architecture, and repository gates.

Backend-visible semantic substitution is forbidden before Phase 6.

## 21. Architecture Validation

The corrected design satisfies repository Kiro gates:

1. **Core vs plugin ownership:** feature owns semantic compression; core keeps routing/B2BUA/economics.
2. **Canonical model neutrality:** classification and restoration use `lipapi` reasoning contracts.
3. **Streaming first:** capture remains final canonical stream observation; auxiliary non-streaming is collection over normal canonical execution.
4. **No provider SDK leaks:** compressor uses auxiliary/Executor path only.
5. **No retry post-output:** compressor failure is post-release optimization state and never retry authority.
6. **SDK source compatibility:** exported `BackgroundClient` required method set remains unchanged; polling is optional additive interface.
7. **Bounded resources:** queue/result outer bounds plus feature raw bytes, per-session optional budgets, and feature-instance aggregate optional budgets.
8. **Privacy boundary:** representation eligibility is separate from data-egress approval; required redaction occurs before provider submission.
9. **Control-plane/model separation:** trusted auxiliary metadata remains available for authorization/billing but is excluded from model prompt/content telemetry.
10. **Minimality:** no new provider client, workflow engine, transcript DB, money ledger, provider Cartesian matrix, or backend semantic ABI in v1.

## 22. Open Questions Deferred From V1

- durable/distributed surrogate state;
- primary-route inheritance;
- account-specific optional memory quota beyond required feature-instance hard bound;
- callback-driven adoption;
- waits before replay;
- system/operator-funded compressor accounting;
- generic organization-wide data-processing registry beyond the narrow trusted egress seam;
- expansion of semantic classification to additional dialects based on measured evidence.

None is required to implement #369 safely.