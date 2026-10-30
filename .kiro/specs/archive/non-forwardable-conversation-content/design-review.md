# Design Validation Review

## Review Method

The revised design was revalidated as a brownfield canonical/runtime/continuity/cache-sensitive architecture change against:

- root `AGENTS.md`, `.kiro/AGENTS.md`, structure/testing steering;
- Go-LIP `main` at `b54982384840ba85c0af2a019ccc35becdd63f10`;
- `pkg/lipapi.Call` legacy/item authority and validation;
- secure-session/A-leg authority;
- request/pre-request/routing/context/billing/capability ordering;
- candidate-open/PTB/`be.Open` boundary;
- B2BUA MemoryStore and Bun SQLite/PostgreSQL continuity;
- FeatureBundle extension patterns;
- OpenResponses continuation materialization;
- existing interleaved-thinking tail memo shaping;
- existing prompt-cache residency contract;
- Python non-forwardable registry/enforcer and Quality Verifier steering helper;
- current official OpenAI, Anthropic and Gemini prompt-cache guidance;
- all revised acceptance criteria and `gap-analysis.md`.

The revision was treated as NO-GO until it proved both directions of visibility and did not introduce systematic prompt-cache churn.

## Round 1: Does Hidden Steering Fit the Original `never_backend` Abstraction?

### Assessment

**Decision: NO-GO**

A digest-only deny registry cannot reconstruct a message that the client never saw.

### Critical Issue 1: Opposite visibility requires different authority

**Concern:** `never_backend` messages exist in client replay and only require durable recognition. Hidden steering does not exist in client replay and requires durable reconstruction.

**Resolution:** elevate the internal abstraction to `conversationview` with two narrow concepts:
- exclusion tags containing semantic identity only;
- steering overlays containing full model-visible message payload + placement.

**Traceability:** Requirements 2, 3, 9; Design D1, D4-D6.

### Critical Issue 2: Separate stores could yield incoherent per-turn views

**Concern:** reading exclusion and steering independently could observe different mutation moments and would tempt per-B-leg reads.

**Resolution:** standard stores expose one coherent `Snapshot` per logical turn while mutation ports remain segregated.

**Traceability:** Requirements 2.7-2.10, 5.1, 5.10, 13.1; Design D4.

### Critical Issue 3: Full steering payload has different security/bounds

**Concern:** the previous registry intentionally stored no message plaintext. Hidden steering cannot be reconstructed without content.

**Resolution:** persist only bounded versioned model-visible role/text for overlays, up to 64 KiB/message, 64 active overlays and 256 KiB active payload/A-leg; ordinary telemetry remains content-free.

**Traceability:** Requirements 9.2-9.3, 9.17-9.19, 12.9; Design D6/Data Model.

**Result after remediation: PASS.**

## Round 2: Cache Stability

### Assessment

**Decision: NO-GO**

The obvious reinjection algorithm—append the same steering message to the current tail—violates exact-prefix prompt caching across ordinary turns.

### Critical Issue 1: Moving-tail reinjection destroys append-only model history

Activation:

```text
S, U1, STEER
```

Next client request omits hidden steering:

```text
S, U1, A1, U2
```

Naive projection:

```text
S, U1, A1, U2, STEER
```

The previous model-visible request is not a prefix.

**Resolution:** persist placement. Mid-session `after_ingress_tail` resolves once to a durable semantic anchor after the activation user message; later projection re-inserts at that exact boundary:

```text
S, U1, STEER, A1, U2
```

**Traceability:** Requirements 9.5-9.8, 10.1-10.5; Design D8-D10.

### Critical Issue 2: Editing an early system prefix mid-session is also expensive

**Concern:** always putting steering in top-level/stable system content can invalidate the cached history before it.

**Resolution:** support two placements:
- `stable_prefix` for session-wide guidance;
- fixed activation boundary for mid-session guidance.

**Traceability:** Requirements 9.5, 10.4-10.6; Design D7-D8.

### Critical Issue 3: Per-turn rendering can secretly invalidate cache

**Concern:** a helper could add current time/turn/trace metadata while "reusing" one logical steering record.

**Resolution:** persist the already rendered canonical message per revision. No dynamic model-visible metadata is regenerated.

**Traceability:** Requirements 10.3, 13.12-13.13; Design D11.

### Critical Issue 4: Anchor loss could reintroduce moving-tail behavior

**Concern:** history compaction/truncation can remove the anchor.

**Resolution:** explicit `AnchorMissingPolicy`:
- deterministic `stable_prefix_fallback`;
- `fail_closed`.
Current-tail fallback is prohibited.

**Traceability:** Requirements 10.7-10.9; Design D9.

### Critical Issue 5: Overlay mutations cannot be falsely advertised as cache-neutral

**Concern:** create/update/deactivate necessarily changes model-visible context.

**Resolution:** model these as explicit cache discontinuities. The contract guarantees stability after the mutation, not impossible preservation across changed content.

**Traceability:** Requirement 10.6; Design D12.

**Result after remediation: PASS.**

## Round 3: Canonical and Provider-Portability Constraints

### Assessment

**Decision: NO-GO**

A fully arbitrary "insert any role anywhere" API would be more general than current canonical/provider constraints justify.

### Critical Issue 1: Arbitrary anchors can split dependent model events

**Concern:** insertion between tool-call/result or related reasoning items can produce an invalid trajectory.

**Resolution:** V1 activation-boundary registration resolves only to the current terminal forwardable user message. Arbitrary item/part insertion is out of scope.

**Traceability:** Requirements 9.4-9.7; Design D8.

### Critical Issue 2: Provider role grammar differs

**Concern:** some backends can represent mid-conversation system/developer messages differently.

**Resolution:** keep canonical payload/placement explicit and narrow. Normal candidate admission/adaptation must preserve required semantics or reject; it may not silently move/drop steering. Stable-prefix guidance can use instruction semantics; fixed-boundary V1 uses a safe ordinary text-message carrier.

**Traceability:** Requirements 6.5, 9.4-9.5, 10.13; Design D7-D8, Final Reassertion.

### Critical Issue 3: Existing interleaved tail memo is not the same lifecycle

**Concern:** migrating it under this spec would change established memo expiry/salience semantics and broaden scope.

**Resolution:** no migration. Persistent base projection composes with existing interleaved attempt shaping; final tests certify no duplication/loss.

**Traceability:** Out-of-scope, Requirement 13.11; Design Interaction With Existing Interleaved Thinking.

**Result after remediation: PASS.**

## Round 4: Runtime Ordering and Final Authority

### Assessment

**Decision: NO-GO**

Early injection alone is insufficient because attempt transforms and interleaved shaping occur later.

### Critical Issue 1: Late stages can undo hidden steering

**Concern:** an attempt transform can remove/duplicate/move a projection-owned message.

**Resolution:** final conversation-view reassertion at the common candidate-open path uses the frozen snapshot, then validation/adaptation/integrity checking precedes PTB/Open.

**Traceability:** Requirement 6; Design D14.

### Critical Issue 2: Re-reading the store at final open creates inconsistent race arms

**Concern:** one arm could use revision N and another N+1.

**Resolution:** final reassertion uses the same snapshot captured for the logical turn.

**Traceability:** Requirements 2.7-2.10, 5.10, 6.1, 9.15, 13.1.

### Critical Issue 3: Client and continuation evidence must remain steering-free

**Concern:** recording the projected call into frontend continuation would expose backend-only orchestration and could cause later duplicates.

**Resolution:** continuation/CTP record client truth; steering is injected only after materialization at core B-leg projection. PTB is the model-visible truth.

**Traceability:** Requirements 9.9-9.10, 11.4-11.7; Design Continuation Flow.

**Result after remediation: PASS.**

## Round 5: Prompt-Cache Boundary Ownership

### Assessment

**Decision: PASS**

The design correctly separates structural cache friendliness from provider cache policy.

Core guarantees:

- stable/append-only canonical model-visible placement when state is unchanged;
- no moving-tail reinjection;
- deterministic overlay bytes/order;
- explicit cache discontinuity on model-visible mutation.

Core does **not**:

- mutate `PromptCacheKey`;
- choose TTLs;
- inject provider `cache_control`;
- create explicit provider cache objects;
- infer cache hits/misses as correctness.

Provider adapters and the existing prompt-cache residency contract remain owners of actual provider cache semantics.

## Requirements Traceability Review

**Decision: PASS**

All revised requirements have design owners:

- identity and occurrence anchoring — D3;
- coherent A-leg state — D4-D6;
- client-visible exclusion — registry + base/final projection;
- tag-before-release — local-turn sequencing;
- early projected economics — base projection;
- final safety/completeness — D14;
- local success stream — local-turn/local stream;
- persistent hidden steering — D6-D9;
- cache invariants — D10-D13;
- continuation separation — dedicated flow;
- observability/security — metrics/data rules;
- TDD/bounds/race — testing plan.

## SOLID Review

### Single Responsibility — PASS

- identity does not store state;
- state store does not decide producer policy;
- projector does not implement verifier/commands;
- local-turn handler does application work;
- steering writer mutates steering only;
- runtime sequences;
- frontends/backends translate.

### Open/Closed — PASS

New client/frontends/backends inherit canonical projection. New trusted producers call narrow APIs instead of changing runtime branches.

### Liskov — PASS

Memory/Bun satisfy the same snapshot/mutation semantics; local EventStream remains substitutable for frontend consumers.

### Interface Segregation — PASS

Reader/Tagger/SteeringWriter/local-turn contracts are distinct. No base Store enlargement.

### Dependency Inversion — PASS

Runtime depends on ports; storage implements ports; feature producers use SDK contracts; core has no provider SDK.

## Hexagonal Boundary Review

**Decision: PASS**

- A-leg state/policy is core-owned.
- Producers are driving application extensions.
- Memory/Bun are driven adapters.
- Concrete writer construction is explicit.
- No DI container, global service locator or `map[string]any` service bag is required.
- Provider cache controls remain outside core.

## Security Review

**Decision: PASS**

- client cannot inject trusted hidden steering via a visibility flag;
- steering is absent from client response/continuation;
- steering is visible to remote model/provider and is therefore not a secret mechanism;
- content at rest is bounded and excluded from ordinary logs/metric labels;
- local-turn secret guard ordering remains intact;
- store failure is fail closed.

## Performance Review

**Decision: PASS**

- one coherent snapshot read per logical turn;
- no I/O per candidate arm;
- membership set is bounded;
- overlay count/payload is bounded;
- no background watcher/cleanup service;
- prefix-stability tests make cache regression a release gate.

The added persistence I/O is the necessary price of cross-restart/shared-process correctness. A process-local cache was deliberately rejected.

## Brownfield Compatibility Review

**Decision: PASS**

No-overlay/no-tag/no-claim execution changes no client/backend wire contracts and retains existing routing/streaming/accounting behavior except bounded snapshot/projection overhead.

No implementation requires:

- new `lipapi` wire visibility fields;
- backend connector ABI visibility flags;
- route grammar changes;
- billing schema changes;
- frontend continuation schema changes for steering;
- provider cache policy changes;
- command/verifier/quota implementation.

## Testing Review

**Decision: PASS**

The task plan requires RED tests before production code for:

- semantic identity and occurrence anchor;
- coherent state + memory/Bun persistence;
- client truth vs model truth;
- local-turn causal tagging;
- persistent hidden steering across replay/restart/reload;
- exact-prefix multi-turn cache invariant;
- anchor loss fallback/fail;
- late-transform corruption;
- initial/failover/parallel/TTFT/interleaved paths;
- bounded OpenAI/Anthropic/Gemini-family translation sentinels;
- race conditions and final architecture/quality gates.

## Final Assessment

**Decision: GO FOR DESIGN READINESS**

The steering requirement materially improves the abstraction. The system is no longer merely a deny-list for messages that the client replays. It is a durable **A-leg → B-leg conversation-view projection**.

The key cache design is intentionally strict:

> unchanged persistent steering is model-visible history, not a per-turn adornment.

For mid-session activation the proxy records the activation boundary once and reinserts there forever (until explicit mutation/deactivation/anchor-loss policy). This preserves append-only/exact-prefix history across ordinary turns rather than moving the same instruction behind every newly appended user/assistant turn.

No concrete producer is authorized by this spec.

## Implementation Gate

This remains a spec-only PR. `spec.json` keeps requirements/design/tasks approvals and `ready_for_implementation` false until maintainer review.
