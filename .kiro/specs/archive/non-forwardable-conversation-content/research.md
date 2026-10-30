# Research and Architecture Notes

## Research Question

How should Go-LIP support proxy-owned conversation messages that are visible on only one leg, including persistent hidden steering that the proxy must reinsert across turns without destroying prompt-cache locality?

This research updates the original non-forwardable-message analysis after a newly identified requirement: some proxy-generated content is **B-leg-only**, not A-leg-only.

## Scope Guardrail

The research is infrastructure-only. It does not design or implement interactive command syntax/handlers, Quality Verifier policy, quota notification policy, or another concrete steering producer.

## Repository Baseline

Research baseline: `main` at `b54982384840ba85c0af2a019ccc35becdd63f10`.

Relevant current assets:

- `pkg/lipapi.Call` is the protocol-neutral canonical request. Legacy authority uses `Instructions` + `Messages`; item authority uses `Items`.
- `lipapi.Message.Metadata` is proxy-owned `json:"-"`, which is useful for current-request provenance but cannot be replay authority because clients do not round-trip it.
- `lipapi.NormalizedItems`/walkers provide protocol-neutral traversal.
- `runtime.Executor` establishes authoritative A-leg state before route planning and ultimately sends every normal attempt through the shared candidate-open path.
- The candidate-open path produces a final `wireCall`, emits PTB capture from it, then invokes `be.Open(...)`. This is the authoritative no-leak/no-omission boundary.
- `frontendpipe` already maps canonical `EventStream` back into each wire frontend, so proxy-local successful text does not require per-frontend synthetic response code.
- OpenResponses materializes `previous_response_id` history before Executor; a central projection therefore sees concrete replayed message items.
- B2BUA MemoryStore and Bun SQLite/PostgreSQL continuity already implement optional capability patterns (`routeoverride`, interleaved state) without widening base/public continuity contracts.
- FeatureBundle already provides ordered extension chains with immutable-generation composition.

## Python Lineage

The Python LIP evolved from command-string stripping toward server-side non-forwardable identity/registry/enforcement. That later design is the useful precedent for **A-leg-visible / B-leg-hidden** messages.

The Python Quality Verifier also exposes the opposite visibility direction: `quality_verifier_steering_messages.py` creates a private steering system message and appends it to the backend request for an inline recall. This proves a concrete future consumer for **B-leg-visible / A-leg-hidden** infrastructure. The Go spec must not port the verifier itself.

Important difference: the old helper appends steering to the current tail. That is appropriate for a one-off recall, but it is not sufficient for a persistent hidden steering message that must be present on later turns. Because the agent never sees that message, the next client request omits it. Re-appending it at the new tail relocates it after newly added assistant/user history.

Example:

```text
activation request:
S, U1, STEER

later client submission:
S, U1, A1, U2

naive reinjection at current tail:
S, U1, A1, U2, STEER
```

The activation request is no longer a prefix of the later request. The same unchanged steering instruction moved relative to the model-visible history.

## Existing Go Tail Injection

`internal/core/interleavedthinking/shape.go` intentionally injects a memo at the current tail while preserving the preceding prefix. That behavior serves a different feature with a bounded turn budget and tail-salience semantics.

It is **not** the persistence model for this specification:

- the interleaved memo is not hidden client/session state with a durable fixed placement contract;
- its current-tail location changes as the conversation grows;
- changing its position across turns cannot provide the exact-prefix invariant required for persistent hidden steering.

The new mechanism therefore does not modify interleaved thinking in this spec. It must coexist deterministically with it. A later behavior-specific migration would need its own semantic review.

## Prompt-Cache Ground Truth

Prompt caching is now economically and latency significant enough that the placement semantics must be normative.

### OpenAI

Official OpenAI material states that cache hits depend on exact prompt-prefix matches and recommends static content first/variable content later. OpenAI's Codex agent-loop documentation specifically describes model-visible history as append-only to preserve exact prefixes.

References:
- https://openai.com/index/api-prompt-caching/
- https://openai.com/index/unrolling-the-codex-agent-loop/
- https://openai.com/index/gpt-5-6-frontier-intelligence-efficiency/

Architectural implication: a persistent hidden message must not be removed and re-added at a moving location. Once active, it should behave like an append-only history element or a stable prefix element.

### Anthropic

Anthropic documents a cache-prefix hierarchy of tools → system → messages and states that changes before a breakpoint invalidate later cached content. Current Claude documentation explicitly supports mid-conversation system messages as a cache-friendly way to add instructions without rewriting the top-level system prefix and warns against editing/removing an earlier mid-conversation system message.

References:
- https://platform.claude.com/docs/en/build-with-claude/prompt-caching
- https://platform.claude.com/docs/en/build-with-claude/mid-conversation-system-messages
- https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-use-with-prompt-caching

Architectural implication: a steering instruction activated mid-session should become a stable historical element at its activation boundary; later turns append around/after it rather than moving it.

### Gemini

Gemini's official caching guidance recommends large/common content at the beginning and requests with similar prefixes. Explicit cached content is a prompt prefix.

Reference:
- https://ai.google.dev/gemini-api/docs/caching

Architectural implication: stable prefix steering is valuable for session-wide guidance, while mid-session guidance needs stable historical placement to avoid repeated divergence.

## Cache-Friendly Placement Options

### Option A: Reinject at current tail every turn

**Rejected.**

Pros:
- simple;
- maximum recency/salience.

Cons:
- position moves every turn;
- activation request is not a prefix of the next request;
- unchanged steering creates avoidable prefix divergence;
- silently conflates "persistent state" with "per-turn append."

### Option B: Rewrite top-level/system prefix on every turn

**Rejected as the only mechanism.**

Pros:
- stable after activation if never changed.

Cons:
- activating mid-session rewrites a very early prefix and can invalidate a large cached history;
- producer may semantically want steering to begin at a particular point in the conversation.

### Option C: Persist one fixed activation boundary

**Preferred for mid-session steering.**

Activation:

```text
S ... U_N, STEER
```

Later:

```text
S ... U_N, STEER, A_N, U_N+1
```

The original activation prompt remains an exact prefix of the later prompt, assuming the rest of the client/model-visible history is append-only and unrelated request wrappers are unchanged.

A durable anchor cannot use request index or transient item ID. It uses the semantic identity of the terminal forwardable user message plus an occurrence ordinal.

### Option D: Stable prefix placement

**Preferred for session-wide/background steering.**

The overlay is placed at one deterministic prefix slot after the stable instruction region and before mutable conversation history. It remains there for its entire unchanged revision.

Activation/update/deactivation can cause one intentional cache discontinuity. Unchanged subsequent turns are stable.

## Selected Visibility Model

The umbrella domain is **conversation view**, not commands.

Two independent visibility directions exist:

| Class | Stored payload | A-leg/client | B-leg/backend | Reconstruction authority |
|---|---|---|---|---|
| `never_backend` | semantic identity only | visible | hidden | client replay + proxy exclusion registry |
| persistent steering overlay | full canonical message + placement | hidden | visible | proxy A-leg state + deterministic injection |

The opposite storage requirements are fundamental:

- client-visible content returns from the client, so the proxy only needs a durable semantic deny identity;
- backend-only content never returns from the client, so the proxy must durably own the actual message content and its placement.

A single vague "non-forwardable tag" abstraction cannot represent both correctly.

## Selected Package/Port Shape

### `internal/core/conversationview`

Focused domain/application policy:

- semantic message identity;
- `never_backend` tag value objects;
- steering overlay value objects;
- immutable per-turn `Snapshot`;
- stable placement/anchor resolution;
- pure projection/reassertion helpers;
- narrow `Reader`, `Tagger`, and `SteeringStore`/application ports.

This gives the runtime one coherent per-turn state read while preserving Interface Segregation for mutation consumers.

### `pkg/lipsdk/nonforwardable`

Trusted registrar types for future proxy features that create client-visible/local-only messages. No client transport surface.

### `pkg/lipsdk/localturn`

Generic Match → protect source → Handle → protect reply seam. No command implementation.

### `pkg/lipsdk/steering`

Trusted producer contract:

- bounded `OverlayID`;
- text-bearing canonical steering message;
- placement request;
- anchor-missing policy;
- result/revision metadata;
- writer/controller methods to put/replace/deactivate.

The standard distribution injects the concrete writer explicitly into trusted feature construction/application code. It is not a global service locator and is not exposed to client frontends.

## Steering Placement Contract

### Stable prefix

Purpose: guidance that should apply session-wide or whose producer can accept one prefix reset on activation.

V1 role constraint:
- use a complete system/developer-style instruction representation at the stable instruction boundary when canonical authority permits it;
- if a candidate cannot preserve required semantics, normal admission/adaptation must reject rather than silently relocate/drop.

### Fixed activation boundary

Purpose: steering generated in response to a live turn (for example a future verifier recall).

Producer-facing `after_ingress_tail` is resolved **when registered** to:

```text
MessageIdentity + OccurrenceOrdinal
```

The source must be the current terminal forwardable user message. The stored anchor never becomes "current tail."

V1 deliberately does not offer arbitrary anchoring between tool-use/result fragments; that would create invalid provider trajectories and unnecessary complexity.

For broad compatibility, the persistent mid-conversation steering message is a normal canonical text message. Provider-specific stronger system-message forms remain adapter/capability concerns and must not cause silent movement.

## Anchor Loss

An anchor can disappear because the client/agent compacted or truncated history. At that point the previous exact prompt prefix is already broken independently of steering.

Two explicit policies:

- `stable_prefix_fallback`: continue applying the steering at one deterministic prefix slot and emit bounded diagnostics;
- `fail_closed`: do not send an unsteered/misplaced backend request.

Never move the overlay to whatever happens to be the new tail.

## Multiple Overlays

Ordering must be stable:

- each new overlay gets an immutable `SlotOrdinal` at store linearization;
- same placement: order by slot, never map/SQL row order;
- semantic no-op Put does not create a new revision/slot;
- replacement retains slot;
- placement-changing replacement is explicit and is a cache discontinuity;
- deactivation removes the overlay from future snapshots but cannot rewrite completed calls.

## State and Persistence

Unlike exclusion tags, steering requires full payload persistence.

Logical state per A-leg:

- state revision;
- up to 4096 exclusion identities;
- up to 64 active steering overlays;
- overlay ID, overlay revision, active flag;
- slot ordinal;
- versioned canonical role/text payload;
- placement + durable anchor;
- anchor-missing policy;
- bounded source/reason code;
- creation/update timestamps.

Initial payload bounds:
- one steering message: 64 KiB;
- total active steering payload: 256 KiB/A-leg.

SQLite/PostgreSQL storage is A-leg-owned. Mutations and snapshots are transactional/linearizable. Shared PostgreSQL readers load a current snapshot per logical turn.

## Runtime Ordering

Selected order:

```text
frontend decode / continuation materialization
        ↓
accepted client/A-leg ingress + CTP evidence
        ↓
secure authoritative A-leg
        ↓
one ConversationView snapshot
        ↓
local-turn Match / source protection / optional local response
        ↓
normal backend path:
  clone accepted client call
  remove never_backend
  clean dependent references
  inject persistent steering
  validate
        ↓
backend request/pre-request transforms
        ↓
routing / context / billing / capabilities
        ↓
baseline
        ↓
per-candidate/interleaved shaping + attempt transforms
        ↓
ConversationView final reassertion
        ↓
candidate adaptation/integrity check
        ↓
PTB capture
        ↓
backend Open
```

Important properties:

- CTP never contains backend-only steering.
- continuation recording never receives backend-only steering.
- PTB does contain backend-only steering.
- one logical turn uses one immutable view snapshot for all attempts.
- a concurrent writer mutation affects later turns, not an already admitted turn.
- the final reassertion protects against late transforms that duplicate/remove/move state.

## Interaction With Existing Prompt-Cache Residency Contract

The existing prompt-cache residency work deliberately keeps provider cache semantics in backend adapters. This feature should compose with it, not absorb it.

Therefore core does **not**:

- choose provider cache TTL;
- modify `PromptCacheKey` based on an overlay;
- add/remove vendor `cache_control`;
- invent explicit provider cache resources;
- inspect provider cached-token accounting to decide steering placement.

The visibility layer's job is structural: keep unchanged model-visible history as prefix-stable as possible. Provider adapters continue to observe/operate actual provider caching.

## Interaction With Existing Interleaved Thinking

Existing interleaved memo shaping can occur after the persistent base projection. The final reassertion distinguishes conversation-view-owned overlay instances from unrelated per-attempt injections.

This spec does not migrate or change interleaved memo expiry/tail behavior. A regression test should prove the two mechanisms compose without duplicate persistent steering or loss of the interleaved memo.

## Security

Backend-only does not mean secret:

- provider/model receives steering;
- PTB capture can contain it under existing protected/redaction policy;
- model may reveal/paraphrase it.

Trusted producers must not put credentials/tokens in steering.

Normal logs and metrics use IDs/revisions/counts only.

## Brownfield Risks

1. **Two histories accidentally conflated.** Mitigation: explicit A-leg/client evidence vs derived B-leg view.
2. **Late transform undoes projection.** Mitigation: final reassertion at shared attempt-open choke point.
3. **Moving hidden message destroys cache.** Mitigation: immutable placement anchor + prefix tests.
4. **Anchor uses transient IDs.** Mitigation: semantic identity + occurrence ordinal.
5. **Anchor removed by compaction.** Mitigation: explicit fallback/fail policy; never tail-follow.
6. **State read per race arm.** Mitigation: one immutable snapshot/turn.
7. **Steering content stored unbounded.** Mitigation: per-message/aggregate limits.
8. **Provider adapter silently relocates unsupported role.** Mitigation: explicit admission/adaptation failure.
9. **Generic SDK turns into service locator.** Mitigation: narrow writer and explicit constructor/composition injection.
10. **Scope drifts into verifier/commands.** Mitigation: fake producers only in this spec's tests.

## Effort Assessment

This is larger than the original exclusion-only infrastructure because the proxy must persist actual hidden content and stable placement state. It is still a focused architecture change: one domain capability, standard continuity adapters, one runtime projection pipeline, two small SDK producer seams, and tests.

Estimated implementation effort: **L**, driven by dual-dialect persistence, runtime ordering, anchor/cache conformance, and cross-protocol sentinel tests rather than algorithmic complexity.

## Research Conclusion

The correct abstraction is a **durable A-leg conversation-view projection**, not a command sanitizer and not a generic request-mutating hook.

Client-only content is projected out. Backend-only steering is projected in. Both directions are controlled by one immutable per-turn snapshot and one final shared backend boundary.

For persistent steering, **placement is state**. The proxy must store where the message became model-visible and recreate that same position every later turn. This is what converts hidden steering from a cache-hostile per-turn mutation into append-only model-visible history.
