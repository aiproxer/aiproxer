# Requirements Document

## Project Description (Input)
Coding agents replay their whole conversation every turn, and the proxy now elides byte-identical tool results from the model-bound request. Each applied elision batch changes the prompt prefix the provider caches, so the cache is invalidated on purpose and the next turns run cold. AIProxer already diagnoses prompt-cache collapse episodes (`.kiro/specs` history: #1079, #1071, #1074) and logs a bounded `cause` label with the episode, but the diagnostic's structural-cause input has no production publisher: only a configuration change can ever be attributed today. Issue #1280 asks for the generic structural-cause input to be wired, with tool-result elision as its first publisher, so that a collapse caused by an applied elision batch is attributed to `tool-result-elision` instead of appearing unexplained. The diagnostic must keep proving *why* a cache collapsed, and must never learn tool content to do it.

## V1 Slice
- **Delivers**: One bounded, content-free publication path from a proxy-applied request change to the prompt-cache collapse diagnostic, and the tool-result-elision feature as its first publisher. An applied elision batch is then reported as the `tool-result-elision` cause on the collapse episode it caused.
- **First consumer**: The tool-result-elision feature plugin, which knows when a batch was applied to a request and records it on the request itself.
- **Safe degradation**: With no cause recorded, the feature behaves exactly as today and the diagnostic keeps its current attribution. A cause is advisory: it never changes a request, stream, route, cache decision, or bill, and it never fails a request.

## Deferred
- The seam-digest feed (`ObserveSeamDigests`) that reports the divergent message index — follow-up: it needs a per-message digest of every model-bound request, which is an unconditional per-request hashing cost for a rarely-firing diagnostic; it needs its own cost decision and configuration gate before it can ship. The cause path does not depend on it.
- The remaining structural causes (`keep-warm-expiry`, `affinity-switch`, `credential-switch`, `compaction`, `system-prompt`) — follow-up: the vocabulary and the publication path exist after this slice; each cause needs its own authority that observes the event it describes.
- A cause label on the collapse Prometheus counter — follow-up: the counter's cardinality is deliberately bounded to backend and model; a cause label is only worth adding once real episodes show which causes dominate.
- Operator documentation for the new cause — follow-up: the cause is a log label on an existing diagnostic, and the operator guide should follow real usage.
- Per-session or per-cause saving estimates — follow-up: owned by the dashboards and alerts issue (#1281).

## Boundary Context
- **In scope**: The bounded cause vocabulary of the collapse diagnostic; one proxy-owned, content-free cause label on the canonical request; its propagation onto the usage sample of the leg that request produced; the attribution rules that decide when a cause wins; the tool-result-elision feature recording the cause of an applied batch.
- **Out of scope**: The collapse detector's thresholds, episode state machine, keep-warm expiry attribution, configuration-generation attribution, the Prometheus counter, the seam-digest feed, the elision planner and marker format, the canonical transcript, routing, billing, and provider protocols.
- **Adjacent expectations**: The diagnostic keeps attributing a configuration change and a keep-warm expiry when no cause was recorded. The elision feature keeps its existing counters and its fail-open behaviour. A recorded cause never survives a later healthy sample, matching the detector's existing healthy-epoch rule.
- **Boundary ownership**: The diagnostic owns the cause vocabulary and the attribution rules. The feature plugin owns when an elision happened and records it on the request. Core owns the propagation of the recorded label onto the leg's own usage sample. Composition owns nothing new: the label rides existing request and usage contracts.
- **Revalidation triggers**: Collapse diagnostic state machine or attribution changes; request-transform stage order and its timeout/commit semantics; the canonical request representation in `pkg/lipapi`; the usage evidence contract in `pkg/lipsdk/usage`.

## Requirements

### Requirement 1: A bounded structural cause on the request
**Objective:** As an operator, I want a proxy-applied request change to be recorded on the request itself, so that the change can be attributed later without a side channel.

#### Acceptance Criteria
1. The canonical request shall carry at most one proxy-owned structural cause label; the label is never client input and is never serialized to a provider payload.
2. The label shall be bounded in length and shall carry no content: no message, tool result, tool-call identifier, or payload.
3. A transformation that changes a request shall record its cause only when its changes are committed to the live request; a transformation that timed out, was canceled, was discarded, or failed shall record no cause.
4. When a request already carries a cause, a later transformation shall not replace it: the earliest change to the cached prefix keeps the label.
5. Recording a cause shall not change the request's messages, items, tools, options, identity, or any other field, and shall never fail a request.
6. The cause shall be captured from the request the provider actually receives, after every request mutation, reassertion, clamp and adaptation. A transformation that undoes the change the cause names shall clear the label, so the diagnostic never attributes a change the provider-bound request no longer carries.

### Requirement 2: The cause reaches the usage sample of its own leg
**Objective:** As an operator, I want the recorded cause to reach the diagnostic with the leg that carried it, so that a request is never attributed another request's cause.

#### Acceptance Criteria
1. The usage sample of a leg shall carry the structural cause of the request that produced it, and no other cause.
2. A request that recorded no cause shall produce usage samples that carry no cause.
3. A request that never produced a usage sample shall leave no cause behind for a later request.
4. Two requests that share a session, a backend, a model, or even a resumed A-leg shall never exchange causes.

### Requirement 3: Attribution semantics
**Objective:** As an operator, I want a recorded cause to explain a collapse episode accurately, so that the label I read is evidence and not a guess.

#### Acceptance Criteria
1. A collapse episode that follows a recorded cause, with no later healthy sample in between, shall be attributed to that cause.
2. A later healthy usage sample shall clear a recorded cause, so a cause never survives a proven-healthy epoch.
3. A repeated or already-recorded usage sample shall not change the diagnostic state, so a duplicate delta can neither apply a cause nor revive one a healthy sample proved stale.
4. A request shall publish a structural cause at most once per cache key: a request that changed the cached prefix did so once, so a later leg of that request, or a retry of the same candidate, shall carry no cause and shall not re-arm a stale cause.
5. The request execution shall be the identity of that publication: no A-leg identifier (reused by resumed secure sessions), no content-derived trace identifier (shared by identical requests), and no billing or continuation identifier shall take part in it.
6. Every candidate key of the request that carried the cause shall be attributed, with no per-request candidate budget.
7. An episode with no recorded cause shall keep its current attribution: a configuration change, a keep-warm expiry, or unexplained.
8. The diagnostic's episode log line shall carry the attributed cause label and shall carry no tool content, no tool-call identifier, and no request payload.
9. Only the known cause labels shall be accepted; an empty, unknown, or derived label shall be ignored without changing diagnostic state.

### Requirement 4: Composition
**Objective:** As a maintainer, I want the recorded cause to travel on existing contracts, so that the path cannot drift apart.

#### Acceptance Criteria
1. The cause shall travel on the canonical request and the usage evidence that already exist; no new stage, plane, store, or publisher contract shall be added.
2. The core shall not branch on a concrete feature: it shall carry whatever label the request recorded, and the diagnostic shall validate it.
3. The path shall be covered end to end: an applied elision batch in a request shall be reported as the `tool-result-elision` cause on the collapse episode it caused, with no tool content in the diagnostic.
