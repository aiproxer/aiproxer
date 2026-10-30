# Brownfield Requirements Gap Analysis

## Scope

This analysis compares the hardened `requirements.md` against current `main` as of 2026-08-19. The repository already implements most of the difficult lifecycle and infrastructure around issue #369. The remaining work is not a new generic compression subsystem; it is a narrow bridge between the existing reasoning-preservation artifact lifecycle and generic auxiliary/background execution, with five additional safety constraints discovered during PR review.

## Existing Authorities That Must Be Reused

### 1. Reasoning preservation already owns the correct surfaced-winner lifecycle

Current package: `internal/plugins/features/reasoningpreservation`.

Landed behavior:

- `StreamObserverFactory.Open` resolves capture eligibility for the surfaced candidate.
- Observer consumes defensive canonical stream events and distinguishes text reasoning, signatures, opaque reasoning, exact reasoning parts, tools, text, images, and files.
- `Finish` commits only for `response.OutcomeSuccessReleased`.
- Original reasoning is stored as `TurnArtifact.Reasoning []PlacedReasoning` with exact placement relative to non-reasoning parts.
- Artifact is scoped by authoritative session partition and later matched by existing `AttemptTransform`.
- Authoritative state is bounded by TTL, turns/session, bytes/turn, and bytes/session.
- Telemetry avoids reasoning contents.

**Conclusion:** do not create another observer, reasoning transcript, matching engine, or replay owner. Compression extends this feature only after original artifact commit.

### 2. Exact/native replay is already first-class

Completed OpenAI Responses/direct Codex work requires exact item/opaque/encrypted continuity. Direct Codex owns native `/responses/compact` checkpoint behavior. Anthropic signed/redacted/opaque thinking also demonstrates structures where readable text is not sufficient proof of mutability.

**Conclusion:** semantic compression is not native compaction replacement. Exactness must be explicit and override readable text.

### 3. Generic auxiliary/background infrastructure already exists

Current packages:

- `pkg/lipsdk/auxiliary`;
- `internal/core/auxreq`;
- runtime composition that binds generation-local auxiliary clients.

Landed capabilities:

- normal Executor/routing/B2BUA execution;
- detached private child calls;
- originating principal/scope propagation;
- ordinary admission/billing/metering;
- bounded worker pool/queue/results;
- coalescing;
- generation-pin retention;
- bounded job timeout/result TTL/result bytes;
- explicit `Forget`;
- feature self-disable patterns.

Historical exported `BackgroundClient` contains `SubmitCollect`, `Await`, and `Forget` only.

**Conclusion:** do not add provider client, compressor worker pool, goroutine-per-artifact scheduler, second billing seam, or dependency on `compactioncontinuity` feature semantics.

### 4. Auxiliary model content and control-plane metadata are already separate

`auxiliary.Request` envelope carries role, visibility, detached session mode, parent lineage, disabled plugins and canonical child `Call`. Core auxiliary execution also propagates cloned trusted principal/scope for authorization/accounting.

**Conclusion:** the model-visible privacy boundary is `Call.Messages`/prompt content, not the entire auxiliary request. Required control-plane metadata must remain while staying out of the model prompt/content telemetry.

## Requirement-by-Requirement Gap Map

| Requirement | Current state | Gap / action |
|---|---|---|
| R1 exact/native baseline | Strong existing exact replay/native compaction | Add explicit non-compressible semantic classification and egress architecture guards |
| R2 replay semantics | Replay dialect support exists; no lossy/semantic class | Add one canonical typed classifier reused by source and active destination selection |
| R3 opt-in bounded config | No `compression` block | Add strict nested config including raw output bytes and aggregate optional-state limits |
| R4 original-before-compression | Correct `success_released` append point exists | Hook only after successful original append; reserve optional capacity before provider work |
| R5 non-destructive store | Original-only artifact | Add CAS reservation/pending/surrogate state plus per-session and feature-instance aggregate limits |
| R6 generic auxiliary/source compatibility | Background infrastructure exists; exported `BackgroundClient` has three methods | Add **separate optional poll capability**, not a required new method on `BackgroundClient` |
| R7 ordinary-text privacy | Semantic structure is observable; no feature-specific compressor egress policy | Add narrow trusted allow/redact/deny egress decision and reuse existing sanitizer authority where available |
| R8 metadata separation | Aux envelope/context already carries trusted identity/lineage | Make model-content vs control-plane contract explicit and test it |
| R9 billing | Aux execution traverses ordinary economic path | Add workload role and originating principal/admission/settlement tests |
| R10 result validation/allocation | No compressor exists | Add feature raw `max_output_bytes` before decode, strict schema/decoded bounds/savings |
| R11 non-blocking adoption | `Await` blocks | Add source-compatible optional non-blocking poll capability; no callbacks/poll workers |
| R12 target revalidation | AttemptTransform already validates replay dialects | Add surrogate selection using canonical class + existing destination replay support |
| R13 shadow/release evidence | No compression telemetry/evidence | Add content-free shadow metrics, race/fuzz/security/performance/repository gates |

## Critical Gap 1: No Semantic Compression Classification

Current reasoning preservation knows candidate eligibility and representable dialects, but not whether a retained artifact may be lossily rewritten.

Unsafe shortcut: `ReasoningPart.Text != ""`.

Why unsafe:

- exact Responses items may contain readable text while exact item replay remains authority;
- Anthropic thinking may carry signatures coupled to content;
- provider-native continuity may expose inspectable text yet remain exact;
- future providers may add mixed signed/opaque structures.

### Required remediation

Add one pure canonical semantic authority with unknown default failing closed, conceptually:

```go
type ReplaySemantics uint8
const (
    ReplayUnknown ReplaySemantics = iota
    ReplayExactRequired
    ReplaySemanticText
)
```

Source submission and destination surrogate selection consult the same authority. No provider-name conditionals.

## Critical Gap 2: Artifact Storage Cannot Represent Safe Async Compression

Current artifact has ID, anchor, source identity, original placements, creation time, and original byte count. Missing:

- optional reservation/pending job reference;
- source/policy digest for late-result validation;
- validated surrogate;
- optional byte/count accounting;
- aggregate optional-state totals.

### Required remediation

Keep `Reasoning` unchanged and authoritative. Add bounded optional state and internal atomic/CAS operations:

```text
TurnArtifact
  original placements          authoritative
  optional reservation/pending
  optional validated surrogate
```

Reserve optional pending capacity **before** provider submission. Optional state must never evict an otherwise-retained original.

## Critical Gap 3: Per-Session Optional Bounds Alone Are Insufficient

The initial spec bounded pending/surrogate state per session/turn only. An attacker/high-volume user could create many sessions and grow feature/process memory proportionally.

### Required remediation

Add feature-instance hard limits:

- total pending references across sessions;
- total surrogate bytes across sessions.

Maintain totals atomically with reservation, attach, delete, expiry, original eviction and stale cleanup. Multi-session tests prove aggregate exhaustion rejects optional state and originals remain intact.

A future account-specific quota is product policy; the feature-instance hard bound is the minimum memory-safety requirement.

## Critical Gap 4: Non-Blocking Adoption Must Preserve Exported SDK Source Compatibility

Current exported interface:

```go
type BackgroundClient interface {
    SubmitCollect(...)
    Await(...)
    Forget(...)
}
```

The initial design proposed adding `Poll` directly. That is source-incompatible for external implementations.

`Await` with zero/tiny deadline is timing-dependent; waiting in observer/AttemptTransform adds latency; callbacks/maintenance goroutines add a new lifecycle owner.

### Required remediation

Keep `BackgroundClient` unchanged and add a separate optional feature-neutral capability, e.g.:

```go
type BackgroundPoller interface {
    Poll(context.Context, JobID) (PollResult, error)
}
```

Properties:

- no wait/sleep;
- pending/completed/failed/not-found states;
- defensive completed-result copy;
- no feature-specific fields;
- bounded by existing scheduler retention;
- `Forget` remains explicit;
- standard scheduler implements both;
- external historical `BackgroundClient` implementations still compile.

Reasoning `AttemptTransform` polls once during later matching replay; pending means immediate original fallback.

## Critical Gap 5: Raw Result Must Be Bounded Before JSON Decode

The initial spec had model `MaxOutputTokens` and decoded `MaxSurrogateBytes` but no feature-level raw collected-response byte bound.

Why this matters:

- provider/model can ignore token guidance;
- JSON/schema overhead is not counted by decoded surrogate limit;
- converting an entire oversized collected response to one string before validation can allocate well beyond intended feature bounds.

### Required remediation

Add `max_output_bytes` and enforce:

```text
completed Collected
  -> reject tools/non-text
  -> iterate text fragments with byte counter
  -> exceed max_output_bytes => reject before decode
  -> materialize bounded raw bytes
  -> strict JSON decode
  -> decoded surrogate/savings validation
```

Generic scheduler `MaxResultBytes` remains an outer defense-in-depth ceiling, not a substitute for the feature-specific limit. Add oversized raw-response tests that prove JSON decode is not reached.

## Critical Gap 6: Ordinary Semantic Text Can Still Be Sensitive

Canonical `SemanticText` only says the representation can be transformed. It can still contain secrets, PII, proprietary code, regulated/customer data, or material constrained by retention/residency/consent/provider policy.

Detached/private execution does not establish permission to process that text through another route/provider.

### Required remediation

Before out-of-trust-boundary submission, require a narrow trusted compressor-egress decision over explicit route/purpose/trusted principal policy context:

- allow;
- redact then allow;
- deny.

Policy covers applicable retention, residency, consent/legal-basis and provider-processing constraints. Reuse existing trusted secret/redaction authority when available. If required redaction cannot be satisfied, deny compression/fail-open to original.

Sanitize **before** input-size accounting and provider submission. Explicit route string alone is not consent.

This is feature-scoped; do not create a general compliance platform merely for #369.

## Critical Gap 7: Control-Plane Metadata Must Not Be Confused With Model Input

The auxiliary path legitimately needs role, visibility, parent lineage, and cloned principal/scope for authorization, routing, correlation and billing. Removing them would break existing auxiliary semantics.

### Required remediation

Keep those values in trusted auxiliary envelope/execution context while prohibiting their copy into:

- compressor `Call.Messages`;
- local segment JSON;
- content-bearing telemetry/logs.

Tests inspect both envelope and model prompt to prove this separation.

## Critical Gap 8: Final-Stream `response.Services` Is Deliberately Empty

Injecting generic Aux/state into `response.Services` merely for #369 would broaden a clean SDK contract.

### Required remediation

Prefer construction-time injection:

```text
runtime composition
    -> generation-bound BackgroundClient
    -> optional BackgroundPoller
    -> trusted egress/sanitizer policy
    -> reasoning-preservation InstanceParts
    -> observer + AttemptTransform + TurnStore
```

Compression-disabled path should require none of these. Widen `response.Services` only if later implementation proof demands it.

## Critical Gap 9: Compaction-Continuity Extractor Is a Pattern, Not a Dependency

It demonstrates fixed policy, untrusted delimited input, detached no-tools child, self-disable, bounded input/output, background submission and strict result validation. But its capsule/source/branch semantics are wrong for #369.

### Required remediation

Build a much smaller reasoning-compressor subdomain under reasoning preservation. Reuse `auxiliary`, not compaction feature packages.

## Critical Gap 10: Source and Destination Safety Are Separate

A source may be semantically compressible while a later destination cannot represent that dialect.

### Required remediation

Two gates:

1. source canonical semantic classification + egress policy before compression;
2. destination reclassification + existing `ReasoningReplaySupport` before active substitution.

Stored surrogate is not destination permission.

## Critical Gap 11: Shadow Mode Must Be a Real Behavioral State

Internal mode flag is insufficient if backend-visible substitution can occur accidentally.

### Required remediation

- disabled: no job/state/compression telemetry;
- shadow: full compression/adoption/evidence path, **original always replayed**;
- active: surrogate selection only after source/result/destination checks.

Tests assert actual backend-visible historical reasoning.

## Interaction With Concurrent Active Specs

Current active Kiro specs include request/terminal ownership simplifications. Implementation must rebase/revalidate if they materially change:

- final stream observer lifecycle;
- attempt-transform ownership/order;
- runtime feature composition;
- auxiliary scheduler ownership;
- generation retirement.

Freeze semantic ownership/order, not incidental function names.

## Requirements Corrections Applied

Initial brownfield analysis corrected requirements by:

1. making non-blocking result inspection explicit;
2. prohibiting callback/maintenance polling in v1;
3. making optional state unable to evict authoritative originals;
4. preferring constructor/composition injection over response-service widening;
5. separating source compressibility from destination representability;
6. excluding `compactioncontinuity` feature semantics from dependency surface;
7. making shadow mode backend-visible/testable;
8. requiring revalidation after pipeline simplifications.

Post-PR CodeRabbit review added five further corrections:

9. raw `max_output_bytes` before full materialization/JSON decode;
10. separate optional `BackgroundPoller` to preserve exported `BackgroundClient` source compatibility;
11. feature-instance aggregate pending/surrogate bounds across sessions;
12. explicit model-visible vs control-plane metadata separation;
13. trusted ordinary-text egress allow/redact/deny policy with redaction before provider submission.

## Gap Analysis Verdict

**GO to implementation design/tasks after all remediations above.**

The brownfield repository still owns the hardest machinery. The implementation remains relatively narrow if it reuses existing authorities. The principal risks are safe asynchronous result adoption, strict exact/native exclusion, ordinary-text egress protection, raw response allocation safety, and bounded optional state across many sessions—not compressor prompting itself.