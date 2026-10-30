# Brownfield Requirements Gap Analysis

## Scope and Method

This analysis compares the revised `non-forwardable-conversation-content` requirements against Go-LIP `main` at `b54982384840ba85c0af2a019ccc35becdd63f10`.

The original gap analysis covered replay-stable client-visible/backend-hidden messages. This revision reopens the brownfield review because persistent **backend-visible/client-hidden steering** adds materially different state, projection, and prompt-cache requirements.

Reviewed areas:

- `pkg/lipapi.Call`, `Message`, `Item`, validation, cloning/walkers;
- shared frontend `frontendpipe`;
- OpenResponses continuation materialization/recording;
- secure-session/A-leg request preparation;
- request/pre-request/route/context/billing/capability ordering;
- candidate-open and PTB/`be.Open` choke point;
- interleaved-thinking tail shaping;
- B2BUA MemoryStore and Bun continuity;
- FeatureBundle extension patterns;
- prompt-cache residency contracts;
- Python non-forwardable services and Quality Verifier steering helper;
- current official OpenAI/Anthropic/Gemini prompt-cache guidance.

Classifications:

- **Missing** — no current capability satisfies it.
- **Partial** — reusable seam exists but requires focused integration.
- **Constraint** — existing contracts narrow the valid solution.
- **Risk** — current behavior can violate the new invariant unless explicitly handled.

## Existing Assets Worth Preserving

### Canonical request authority is already centralized

`lipapi.Call` provides both legacy message and item-authoritative forms. The visibility mechanism can remain protocol-neutral and operate on canonical complete messages.

### A-leg is the correct durable ownership anchor

Secure-session/runtime resolves authoritative A-leg continuity before route planning. Visibility state can therefore be server-owned and cannot be forged by a client session hint.

### Backend opening has a real common choke point

All normal attempts converge on a final canonical `wireCall`, PTB capture and `be.Open`. This allows one final visibility reassertion rather than backend-specific enforcement.

### Frontend encoding already accepts local canonical streams

A successful backend-free local turn can reuse `lipapi.EventStream` and existing frontend encoders.

### Optional continuity capabilities are an established pattern

`routeoverride.Store` and interleaved-state storage prove standard memory/Bun stores can add feature-specific A-leg state without expanding the base/public continuity interface.

### Prompt-cache residency is already a separate provider-owned concern

The existing prompt-cache residency contract deliberately avoids moving provider TTL/cache controls into core. Persistent steering should preserve canonical prefix stability but leave provider cache operations in their existing owner.

## Gap Register

| ID | Severity | Class | Effort | Finding | Required disposition |
|---|---:|---|---:|---|---|
| G-01 | P0 | Missing | M | No replay-stable semantic identity/store exists for client-visible messages that must never reach a backend. | Add versioned identity + A-leg exclusion registry. |
| G-02 | P0 | Partial | M | Runtime has canonical preparation but no early visibility projection. | Derive backend-effective call after A-leg resolution and before backend-oriented policy. |
| G-03 | P0 | Partial | S/M | Final `wireCall` choke point exists but does not reassert conversation visibility. | Add mandatory final projection guard before PTB/Open. |
| G-04 | P0 | Missing | M | Existing extension stages cannot represent successful local handling with protected source/reply ordering. | Add generic two-phase local-turn seam. |
| G-05 | P0 | Missing | M | No A-leg state stores complete backend-only steering content because previous feature scope only stored message digests. | Extend focused conversation-view state with bounded persistent overlays. |
| G-06 | P0 | Missing | M | No trusted producer API can Put/Replace/Deactivate persistent hidden steering. | Add narrow `pkg/lipsdk/steering` writer/controller. |
| G-07 | P0 | Risk | M | A hidden message is absent from all later client requests by definition. Ordinary request transforms cannot recreate it unless proxy state owns the payload. | Make full steering payload authoritative A-leg state and inject from snapshot each turn. |
| G-08 | P0 | Risk | M | Naive "append steering to current tail" relocates unchanged content on every turn. | Persist placement as state; forbid tail-following reinjection. |
| G-09 | P0 | Missing | M | No durable semantic anchor exists for a mid-session injection boundary. Absolute index and item ID are unstable across replay. | Resolve registration to semantic message identity + occurrence ordinal. |
| G-10 | P0 | Constraint | S/M | Arbitrary insertion can break tool-call/result ordering or provider message grammar. | V1 anchor only at safe terminal forwardable user-message boundaries; validate after projection. |
| G-11 | P0 | Risk | M | Activating mid-session steering in top-level system rewrites a very early cache prefix. | Support fixed activation-boundary placement in addition to stable prefix. |
| G-12 | P0 | Missing | S/M | There is no normative prefix-stability/cache regression gate. | Add exact-prefix canonical invariants and multi-turn tests. |
| G-13 | P0 | Risk | M | Late attempt/interleaved transforms can duplicate/remove/move steering after early injection. | Final reassertion rebuilds frozen view or rejects before PTB/Open. |
| G-14 | P0 | Constraint | M | Candidate/backend representation may not preserve arbitrary mid-history roles. | Keep v1 payload/placement bounded; unsupported semantics reject explicitly, never silently relocate/drop. |
| G-15 | P0 | Missing | M | MemoryStore has no steering payload/slot/revision state. | Extend focused optional A-leg capability under existing lock/lifecycle. |
| G-16 | P0 | Missing | M | Bun SQLite/Postgres has no steering rows/payload/anchor/slot/revision schema. | Add A-leg-owned migration and transactional implementation. |
| G-17 | P0 | Risk | M | Shared PostgreSQL processes could serve stale hidden steering if process-cached. | Snapshot authoritative state once per logical turn; no indefinite cache. |
| G-18 | P0 | Risk | S/M | Generation reload can remove producer while its prior steering must remain active. | State is continuity-owned; runtime enforcement independent of producer presence. |
| G-19 | P0 | Missing | S/M | No anchor-missing behavior exists for client compaction/truncation. | Persist explicit `stable_prefix_fallback` or `fail_closed`; never current-tail fallback. |
| G-20 | P1 | Constraint | S | Existing interleaved-thinking tail injection has distinct semantics. | Do not migrate it in this feature; define deterministic composition and regression coverage. |
| G-21 | P1 | Risk | S/M | Multiple overlays could reorder nondeterministically through map/SQL iteration. | Allocate immutable `SlotOrdinal`; deterministic ordering. |
| G-22 | P1 | Risk | S | Per-turn rendering can accidentally add timestamps/nonces and bust cache. | Persist rendered canonical payload once per revision; no dynamic model-visible metadata. |
| G-23 | P1 | Constraint | S | Backend-only steering is model-visible and can be echoed. | Document transport invisibility, not secrecy; prohibit credentials/secrets. |
| G-24 | P1 | Risk | M | Full hidden steering content now exists at rest. | Bound payload and keep plaintext out of ordinary telemetry; use existing data-access controls. |
| G-25 | P1 | Constraint | S | Provider cache keys/TTL/breakpoints differ. | Do not change provider cache policy/`PromptCacheKey`; structural prefix stability only. |
| G-26 | P0 | Missing | M | No tests prove A-leg/continuation excludes steering while PTB includes it. | Add dual-view integration tests including previous-response materialization. |
| G-27 | P0 | Missing | M | No tests prove cache-stable placement across ≥3 turns or explicit overlay revisions. | Add canonical prefix and backend-family sentinel tests. |
| G-28 | P0 | Risk | M | Re-reading mutable state on failover/race arms could give different steering within one turn. | Freeze one coherent snapshot per logical turn. |
| G-29 | P1 | Constraint | S | Base `b2bua.Store` is mirrored publicly. | Keep optional focused capability; do not widen base/public store. |
| G-30 | P1 | Constraint | S | Concrete verifier/command/quota work is already/scheduled elsewhere. | Use generic fake producers only; keep all producer policy out of tasks. |

## Requirements Review Round 1: Original Exclusion Design

### Finding R1-A: Metadata alone cannot survive client replay

`Message.Metadata` is non-wire. The design was remediated with deterministic semantic message identity and an A-leg registry.

### Finding R1-B: One filter at final backend open is too late

Context sizing, route planning and billing would count bytes never sent. The design added early backend-effective projection.

### Finding R1-C: One early filter is not a no-leak boundary

Late attempt shaping can reintroduce content. The design added final enforcement at the shared PTB/Open choke point.

### Finding R1-D: A future interactive command still needed a local-success path

Existing request hooks reject/mutate but do not return a successful assistant response. The design added a generic two-phase local-turn seam while explicitly excluding command handlers.

## Requirements Review Round 2: Hidden Steering Reopens the Model

### Finding R2-A: Backend-only steering cannot use the exclusion registry

**Problem:** client-visible content can be reconstructed from replay, but hidden steering is never returned by the client.

**Remediation:** store the complete canonical steering payload plus placement under A-leg continuity.

### Finding R2-B: "Non-forwardable" is directional, not a single flag

**Problem:** one class is forbidden on B-leg; the other is forbidden on A-leg but required on B-leg.

**Remediation:** make the umbrella domain `conversationview` with separate narrow concepts: `never_backend` tags and persistent steering overlays.

### Finding R2-C: Runtime needs one coherent state snapshot

**Problem:** separate mutable reads can observe an exclusion revision and steering revision from different moments, and per-B-leg reads can diverge across races.

**Remediation:** standard stores expose one coherent per-turn snapshot while mutations remain segregated by narrow ports.

## Requirements Review Round 3: Cache Stability

### Finding R3-A: Current-tail reinjection is cache-hostile

**Problem:** if the client omits hidden steering and the proxy re-appends it to each new current tail, the same message moves after newly appended history.

**Remediation:** placement is persisted state. Mid-session steering anchors after the activation user message; session-wide steering uses stable prefix.

### Finding R3-B: Top-level-prefix rewrite is too destructive for mid-session activation

**Problem:** editing top-level/system prefix after a long session can invalidate a large cached prefix.

**Remediation:** fixed activation-boundary placement provides append-only model-visible history for mid-session guidance.

### Finding R3-C: Per-turn identity/index is not stable enough

**Problem:** absolute array indexes move after client materialization and transient item IDs can be regenerated.

**Remediation:** durable anchor = semantic message identity + occurrence ordinal.

### Finding R3-D: Missing anchor after compaction needs explicit semantics

**Problem:** silently choosing the new tail would both alter steering semantics and add avoidable cache churn.

**Remediation:** explicit `stable_prefix_fallback` or `fail_closed`; no tail fallback.

### Finding R3-E: Updating an instruction cannot be "cache transparent"

**Problem:** a changed model-visible message necessarily changes the prompt.

**Remediation:** classify Put/replace/placement change/deactivate as explicit cache discontinuities. The contract guarantees stability after the revision, not impossible zero-churn mutation.

## Requirements Review Round 4: Brownfield Integration

### Finding R4-A: Existing interleaved thinking already injects at tail

**Problem:** treating it as the new persistent overlay could change its established memo expiry/salience behavior and broaden scope.

**Remediation:** no migration. Persistent base projection happens before feature-specific attempt shaping; final tests prove coexistence.

### Finding R4-B: Provider cache control must remain backend-owned

**Problem:** a generic core steering facility could be tempted to manipulate `PromptCacheKey`/TTL/breakpoints.

**Remediation:** requirements explicitly prohibit this. Prefix-stability is canonical structure; provider cache policy remains in prompt-cache/provider owners.

### Finding R4-C: Strong mid-history system roles are not uniformly portable

**Problem:** backend protocols have differing constraints.

**Remediation:** v1 placement/message contracts are bounded and normal candidate adaptation must reject unsupported semantics rather than silently relocate. Stable-prefix instructions remain appropriate for system/developer guidance; activation-boundary steering uses a normal safe text-message carrier.

## Implementation Approach Options

### Option A: Extend generic request transforms with hidden state

Rejected:
- transform has no durable A-leg payload/anchor ownership;
- easy to run too late/too differently per attempt;
- does not establish client invisibility/continuation semantics;
- no cache-stability contract.

### Option B: Store backend-only messages in frontend continuation history

Rejected:
- leaks server-private orchestration into client/A-leg state;
- not all frontends use proxy continuation;
- mixes frontend storage with core policy.

### Option C: Maintain a process-local steering map

Rejected:
- lost on restart;
- incoherent across shared PostgreSQL processes;
- creates a second session lifecycle.

### Option D: A-leg conversation-view capability with deterministic projection

Preferred:
- one authoritative A-leg lifecycle;
- one snapshot/turn;
- store only digests for client-replayable exclusions and full payload only where proxy reconstruction requires it;
- stable placement can be certified independently of provider cache implementation;
- frontends/backends remain unchanged.

## Complexity and Risk

**Effort: L.**

Highest implementation risk is not parsing; it is preserving ordering and cache stability through:

- legacy vs item authority;
- continuation materialization;
- late attempt transforms;
- shared durable state;
- multiple overlays;
- anchor disappearance after compaction;
- provider-family translation constraints.

## Design Recommendations

1. Rename the internal policy concept from "deny-list only" to `conversationview`; keep public producer APIs narrow and directional.
2. Read one coherent A-leg state snapshot per logical turn.
3. Project client truth to backend truth before backend-oriented policy.
4. Reassert the same frozen view immediately before PTB/Open.
5. Persist complete backend-only steering payload and placement; never expect the client to echo it.
6. Provide `stable_prefix` and fixed `after_ingress_tail` activation placement.
7. Treat placement and slot order as immutable state for an unchanged overlay revision.
8. Make overlay mutation an explicit cache discontinuity, then restore stable prefix behavior.
9. Never use current-tail relocation as an anchor-loss fallback.
10. Keep provider cache keys/TTL/breakpoints out of core.
11. Do not implement any concrete command/verifier/quota producer in this spec.
