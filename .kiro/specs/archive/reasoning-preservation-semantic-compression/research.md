# Research and Architecture Decisions

## Purpose

This document records the brownfield research behind issue #369 and the decisions that prevent the feature from duplicating existing infrastructure or weakening exact reasoning continuity. It also records the post-PR review hardening that corrected five substantive gaps: raw-result bounds, exported SDK compatibility, aggregate optional-state bounds, model/control-plane metadata separation, and ordinary-text data-egress policy.

## Current-State Inventory

### Reasoning artifact capture and replay

`internal/plugins/features/reasoningpreservation` owns config/catalog eligibility, final canonical stream observation, authoritative-session partitioning, bounded in-memory turn artifacts, matching/classification, per-candidate `AttemptTransform` restoration, and privacy-safe telemetry.

Current artifact:

```go
type TurnArtifact struct {
    ID             string
    Anchor         [32]byte
    SourceBackend  string
    SourceModel    string
    Reasoning      []PlacedReasoning
    CreatedAt      time.Time
    ReasoningBytes int
}
```

`PlacedReasoning.BeforeNonReasoningPart` is structural. Any surrogate must preserve placement rather than replace an entire assistant message with free-form prose.

The current `TurnStore` exposes `Append`, `Snapshot`, and `Delete`; it defensively clones/clears artifacts and applies TTL/FIFO/session-byte bounds to authoritative reasoning.

### Exact/native continuity

The repository already represents exact-state forms including OpenAI Responses reasoning items, direct Codex encrypted reasoning/native compaction checkpoints, Anthropic signed thinking, and Anthropic redacted/opaque thinking. These are not interchangeable with textual summaries.

### Generic auxiliary/background infrastructure

Historical exported `pkg/lipsdk/auxiliary.BackgroundClient` exposes:

```go
SubmitCollect(...)
Await(...)
Forget(...)
```

`internal/core/auxreq.BackgroundScheduler` already owns bounded workers/queue/results, coalescing, job timeout, result TTL/bytes, generation pinning, shutdown and originating scope attribution.

The completed compaction-continuity implementation demonstrates the correct child-call pattern—detached/private, no-tools, independent route, plugin self-disable, bounded untrusted input, normal routing/billing and validated result—but its capsule/extractor semantics are feature-specific and must not be imported by reasoning preservation.

### Auxiliary metadata boundary

`auxiliary.Request` carries trusted control-plane fields such as role, visibility, detached session mode, parent trace/A-leg/B-leg/branch lineage and disabled plugins, while the canonical child `Call` carries model-visible messages/tools/options. `internal/core/auxreq` also clones trusted principal/scope into execution context for authorization/accounting. Therefore privacy requirements must distinguish control-plane metadata from model-visible prompt data.

## Decision 1: This Is a Follow-Up, Not a Superseding Spec

The active spec is `reasoning-preservation-semantic-compression`.

It does **not** supersede completed reasoning-preservation, OpenAI Responses preservation, Codex native compaction, or compaction-continuity specs. Those remain historical authorities. There was no previous dedicated #369 Kiro implementation spec to supersede.

## Decision 2: Original Artifact Is Always the Source of Truth

Compression never replaces `TurnArtifact.Reasoning`. It attaches bounded optional state to the same artifact.

```text
TurnArtifact
  OriginalReasoning[]       authoritative
  Compression?              optional
      reservation/pending ref
      immutable source/policy digest
      validated surrogate segments
      size/savings metadata
```

Rejected alternatives:

- storing only compressed text;
- deleting original after successful compression;
- using compressor success as a prerequisite for preservation.

All would make a lossy optimization continuity authority and prevent exact fallback.

## Decision 3: V1 Uses a Canonical Artifact/Dialect Semantic Profile

The first design draft proposed a new semantic-permission field in `ReasoningReplaySupport` and new final-stream metadata. Brownfield review rejected that as broader than necessary.

V1 uses one conservative canonical artifact/dialect classifier:

```go
type ReplaySemantics uint8

const (
    ReplaySemanticsUnknown ReplaySemantics = iota
    ReplaySemanticsExact
    ReplaySemanticsSemanticText
)
```

Initial policy:

- ordinary plain historical reasoning text without exact authority -> `SemanticText`;
- OpenAI Responses exact reasoning item -> `Exact`;
- Anthropic signed thinking -> `Exact`;
- Anthropic redacted/opaque thinking -> `Exact`;
- unknown/native/mixed/contradictory structure -> unknown/exact, never semantic.

Exact/native structural properties override readable text. No provider-name matching is introduced.

Destination replay still uses existing `AttemptMeta.ReplaySupport` to prove the candidate can represent the original dialect. If evidence later shows a canonical plain-text dialect that requires exact-byte replay for some provider, revise the semantic contract rather than add provider-name exceptions.

## Decision 4: Source and Destination Safety Are Separate Gates

**Source gate:** after original commit, classify each placed reasoning part. Only `SemanticText` segments may proceed toward compression.

**Destination gate:** during later `AttemptTransform`, classify the original again and require existing `ReasoningReplaySupport` for its dialect before active substitution.

A stored surrogate is never proof of destination permission.

## Decision 5: Compression Configuration Belongs to Reasoning Preservation

Reasoning preservation already owns artifact state and reinjection, so compression is a nested configuration rather than another feature.

V1 requires an **explicit compressor route**; no primary-route inheritance.

Illustrative shape:

```yaml
compression:
  enabled: true
  mode: shadow
  route: "openai-responses:small-model"
  timeout: 8s
  max_input_tokens: 12000
  max_input_bytes: 1048576
  max_output_tokens: 1500
  max_output_bytes: 262144
  max_surrogate_bytes: 131072
  min_source_bytes: 4096
  min_saved_bytes: 1024
  min_savings_ratio: 0.30
  max_pending_per_session: 8
  max_surrogate_bytes_per_session: 524288
  max_pending_total: 256
  max_surrogate_bytes_total: 16777216
  egress: <trusted policy/config reference>
```

`enabled` defaults false and enabled mode defaults `shadow`.

Three output limits are intentionally distinct:

- model/provider `max_output_tokens` request bound;
- local `max_output_bytes` raw collected-response allocation/parser bound **before decode**;
- decoded/retained `max_surrogate_bytes`.

Explicit route selection is not itself data-processing approval.

## Decision 6: Do Not Widen Final-Stream `response.Services` Unless Proven Necessary

`response.Services` is deliberately empty/read-only today. Prefer composition-time constructor injection:

```text
runtime/standard composition
  -> generation-bound BackgroundClient
  -> optional BackgroundPoller
  -> trusted compression egress/sanitizer policy
  -> reasoning-preservation runtime-aware constructor
  -> shared observer + AttemptTransform + TurnStore
```

Disabled compression uses the existing behavior and requires none of these additional capabilities.

## Decision 7: Preserve `BackgroundClient` Source Compatibility With a Separate Optional Poller

Using `Await` with zero/tiny deadlines is timing-dependent; waiting in `Finish` or `AttemptTransform` adds hidden latency; callbacks/feature polling goroutines add another lifecycle owner.

The initial spec proposed adding `Poll` to `BackgroundClient`. CodeRabbit correctly identified that the interface is exported and such a required method would break existing external implementations.

Final decision:

- historical `BackgroundClient` remains unchanged;
- add a separate optional, feature-neutral `BackgroundPoller`/equivalent capability;
- standard process scheduler implements both;
- enabled compression validates poll capability is available;
- external three-method implementations remain source-compatible.

Required poll states are pending, completed, failed, and not-found/expired. Completed data is defensively copied. Poll does not consume/forget; `Forget` remains explicit.

## Decision 8: Reserve Optional Capacity Before Provider Submission, Then Adopt Later

Safe sequence:

```text
successful surfaced response
  -> append original artifact
  -> source semantic gate + threshold
  -> reserve pending capacity
       per-session + feature-instance aggregate
  -> trusted egress allow/redact/deny
  -> sanitize if required
  -> bounded input construction
  -> SubmitCollect
  -> CAS bind JobID to reservation
  -> Finish returns

later matching request
  -> AttemptTransform snapshots artifact
  -> optional BackgroundPoller.Poll once
      pending -> original now
      failed/not found -> clear pending; original
      complete -> raw byte guard -> decode/validate
                 -> CAS surrogate under byte budgets -> Forget
  -> shadow/active + destination gate
  -> original or surrogate
```

The first immediate follow-up may replay original if compression is not ready. V1 deliberately chooses this over a synchronization barrier.

Reservation before submission prevents known optional-state exhaustion from creating avoidable provider work. If egress policy denies or submit fails, clear reservation. If provider work is accepted but JobID binding later loses a stale/CAS race, Forget when possible while retaining billing accountability.

## Decision 9: Pending and Surrogate Updates Are CAS-Like

Store mutations require authoritative partition plus artifact ID, original source/anchor digest, policy revision, and expected reservation/job ID where relevant.

Typed outcomes distinguish attached, stale/not-found, budget-rejected, already-present/conflict.

This prevents cross-session adoption, double adoption, stale results after eviction/reload and old-policy results overwriting newer optional state.

## Decision 10: Optional Compression State Has Per-Session and Feature-Instance Aggregate Budgets

Current `state.max_session_bytes` governs authoritative originals. Optional compression must not use that path in a way that evicts originals merely to make room.

V1 independent optional limits include:

- `max_pending_per_session`;
- `max_pending_total` across feature instance;
- `max_surrogate_bytes` per artifact;
- `max_surrogate_bytes_per_session`;
- `max_surrogate_bytes_total` across feature instance.

Pending/surrogate exhaustion rejects optional state and leaves originals retained. Aggregate counters are maintained atomically through reservation, attachment, delete, expiry, original eviction and stale cleanup. Multi-session tests prove sessions cannot bypass total limits by partition fan-out.

A future account-specific optional quota may be useful for product fairness, but the minimum v1 memory-safety authority is a hard feature-instance total.

## Decision 11: Semantic-Text Eligibility Is Not a Privacy Classification

A plain reasoning string can contain secrets, personal data, proprietary code, customer data or information subject to retention/residency/consent constraints.

Therefore the source gate has two independent dimensions:

1. **representation safety** — canonical artifact may be semantically transformed;
2. **data-egress policy** — selected route is approved for the actual purpose/policy context and source is allowed/redacted/denied.

A narrow trusted compression-egress policy returns allow, redact-then-allow, or deny. It covers applicable operator retention, residency, consent/legal-basis and provider-processing constraints. Where existing trusted secret/redaction policy can sanitize the source, reuse it. If redaction is required but cannot be satisfied, deny compression and keep original.

Sanitization occurs before input-size accounting/prompt construction/provider submission. Explicit route alone is never interpreted as consent.

This is feature-scoped and does not require a general compliance platform.

## Decision 12: Model-Visible Content and Control-Plane Metadata Are Separate

The compressor model receives only fixed instructions plus sanitized, bounded, locally indexed semantic reasoning segments.

Trusted auxiliary envelope/execution metadata may still include:

```text
role = reasoning_preservation_compressor
visibility = private
session mode = detached
parent trace/A-leg/B-leg/branch lineage
cloned originating principal/scope in execution context
```

Those values are required for authorization, routing, correlation, generation ownership and billing, but are **not copied into canonical child messages or content-bearing telemetry**.

Thus the correct claim is not “session/account identity is absent from the auxiliary request”; it is “trusted identity stays in control-plane metadata and out of model-visible content.”

## Decision 13: One Auxiliary Call per Artifact, Preserving Segment Placement

One artifact may contain multiple semantic-text reasoning placements. V1 sends all eligible local segments in one child call and requires strict result keyed by local placement index:

```json
{
  "schema_version": 1,
  "segments": [
    {"index": 0, "text": "..."},
    {"index": 2, "text": "..."}
  ]
}
```

Exact/signed/opaque placements are absent. Parser requires exactly expected indexes, no duplicates/unknown indexes and bounded text.

## Decision 14: Raw Compressor Result Is Bounded Before JSON Decode

`MaxOutputTokens` cannot be trusted as a local allocation guard, and `MaxSurrogateBytes` only applies after decode.

Required order:

```text
completed Collected
  -> reject tool calls/non-text channels
  -> iterate canonical text fragments with byte counter
  -> exceed MaxOutputBytes => stop/reject raw_oversize
  -> materialize bounded raw bytes
  -> strict JSON decode
  -> validate decoded surrogate bytes/schema/savings
```

Do not create the complete raw string first if that can exceed the feature limit. Existing scheduler `MaxResultBytes` remains an outer defense-in-depth bound, not a substitute.

## Decision 15: Active Replay Is Explicitly Lossy

An LLM compressor cannot mathematically prove identical hidden reasoning semantics. Therefore:

- `shadow` is default when compression is enabled;
- `active` is explicit operator acceptance of bounded semantic replay for canonical semantic-text artifacts;
- original remains available as fallback;
- docs/telemetry do not claim semantic equivalence or quality improvement without separate evaluation evidence.

## Decision 16: Feature-Local Compression Failures Are Fail-Open

Existing `AttemptTransform` can be fail-closed for **authoritative reasoning state** according to `on_state_error`. Compression-specific failures—privacy denial, optional budget rejection, poll failure, raw oversize, invalid output, stale job, optional CAS error—fall back to original and must not be mapped to `on_state_error=reject`.

Only failure of original TurnStore/matching retains existing reasoning-preservation policy.

## Decision 17: Content-Free Observability Reuses Existing Privacy Posture

Safe observations may contain outcome enums, counts, eligible source bytes, raw result bytes, decoded surrogate bytes, hypothetical saved bytes/tokens, latency, mode/profile classes and aggregate-budget outcomes.

Forbidden content includes raw reasoning, surrogate, redacted-before/after text, child prompt/result, signatures/opaque data, raw anchors/digests and raw session/account/principal/lineage identifiers.

Auxiliary scheduler/billing remain authoritative for queue/provider/token/cost truth rather than duplicating feature-local economic calculations.

## Decision 18: Workload Identity and Recursion Prevention

Child classification:

```text
class: auxiliary
role: reasoning_preservation_compressor
visibility: private
session mode: detached
disable plugin: reasoning-output-preservation
```

Parent lineage and principal correlation stay in trusted auxiliary metadata/context, not model-facing text.

## Decision 19: Coalescing Key Is Content-Free and Artifact-Specific

Use a versioned deterministic key derived from committed artifact/source/policy identity, preferably hashed for diagnostics:

```text
reasoning-compress:v1:<artifact/source/policy digest>
```

Duplicate submission paths coalesce rather than double-bill.

## Decision 20: Failure Matrix

| Failure | Primary/original behavior | Optional action |
|---|---|---|
| exact/unknown source | unchanged | no reservation/job |
| below source threshold | unchanged | no reservation/job |
| per-session/aggregate pending budget full | original retained | no provider submission |
| egress policy deny/missing required policy | original retained | clear reservation; no provider submission |
| required sanitizer unavailable/fails | original retained | clear reservation; no provider submission |
| aux unavailable / queue full | unchanged | clear reservation |
| admission denied | unchanged | no provider work |
| provider timeout/failure | unchanged | clear on later Poll; Forget |
| raw response > `MaxOutputBytes` | unchanged | reject before decode; clear; Forget |
| malformed/tool result | unchanged | reject; clear; Forget |
| decoded surrogate budget full | original retained | reject surrogate; Forget |
| insufficient savings | unchanged | reject; clear; Forget |
| artifact expired/evicted | normal store behavior | cannot attach; counters cleanup |
| destination unsupported | original replay | surrogate may remain for other destinations |
| shadow | original replay | surrogate may remain for measurement |
| active + valid destination | original retained | eligible segments may use surrogate |

## Decision 21: No Provider Cartesian Matrix

Verification uses canonical semantic/exact fixtures and routing lifecycle scenarios, not provider-by-provider combinations. New backends prove canonical dialect/replay-support contracts rather than adding pairwise compression cells.

## Decision 22: Active Pipeline Refactors Are Revalidation Triggers

Current active terminal/request-pipeline simplification specs may move function/package structure. Implementation must re-read current `main` and preserve semantic owners:

- final surfaced winner before original commit;
- original commit before optional reservation/compressor submission;
- `AttemptTransform` owns later reinjection/selection;
- auxiliary scheduler remains process-owned;
- no retry after output.

## Resolved Questions

- **Supersede old specs?** No; follow-up only.
- **Replace Codex native compaction?** No; exact/native stays authoritative.
- **Reuse compaction-continuity extractor?** No; reuse generic auxiliary infrastructure/pattern only.
- **Inline compression in observer Finish?** No; background submit after original commit and optional reservation.
- **Completion callback/maintenance poller?** No in v1; optional non-blocking poll on later replay.
- **Delete original after successful compression?** Never.
- **Inherit primary route?** No in v1; explicit compressor route.
- **New backend semantic-permission ABI?** Not in v1.
- **Add Poll directly to `BackgroundClient`?** No; separate optional capability preserves source compatibility.
- **Is plain semantic text automatically safe to send remotely?** No; trusted data-egress policy is separate and mandatory.
- **Does private/detached mean no session/account metadata exists?** No; control-plane metadata remains, model prompt stays clean.

## Implementation Readiness

The repository provides the difficult foundations. The hardened design can proceed without a new provider client, transcript store, generic scheduler, compaction detector dependency, backend semantic-permission ABI expansion, route-inheritance metadata, new billing ledger, or generic compliance platform. The remaining implementation work is a bounded bridge: canonical classification, optional state, source-compatible poll inspection, trusted egress/sanitization, raw-result guard, strict surrogate validation, shadow adoption, and finally active destination-gated substitution.