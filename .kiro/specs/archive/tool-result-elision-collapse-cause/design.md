# Design Document

## 1. Problem and current state

The prompt-cache collapse diagnostic (`internal/infra/metrics/prompt_cache_collapse.go`) watches usage per
`(session, backend, model)` key. When a key that was healthy sees a run of cold legs, it records one episode and logs a
bounded `cause` label. Today the only label that can ever be produced is `config-generation` (a reloaded config
generation, published by `runtimebundle`), plus the time-derived `keep-warm-expiry`; everything else is `unexplained`.

The diagnostic already owns the two inputs a richer attribution needs:

- `NoteStructuralCause(session, backend, model, cause)` — a note anchored to the key's healthy epoch. It **drops notes
  for unknown keys** (documented, asserted by `prompt_cache_collapse_test.go`), because a key without healthy history
  can never fire an episode.
- `ObserveSeamDigests(session, backend, model, bLegID, digests)` — content-free per-message digests, used to report the
  first divergent message index between the two most recent legs.

Neither has a production publisher. Tool-result elision (shipped in #1229, extended by #1276/#1277/#1278) changes the
model-bound prompt on purpose, so it invalidates the provider's cached prefix and makes the next legs cold. When that
happens the operator currently sees `unexplained`, which is the worst possible answer: the proxy knows it did it.

## 2. Design decision

**A proxy-applied change records a bounded, content-free cause label on the request itself, and the label rides the usage
sample of the leg that request produced.**

Two properties of that decision carry the whole design:

- **The commit boundary is the boundary.** The stage runner clones the live request before it runs a transformation and
  commits the clone only when the transformation returns in time (`internal/core/extensions/decision_runner.go:44-72`).
  A label written on the request therefore reaches the live request exactly when the change does. A transformation that
  timed out mutates a discarded clone: neither the change nor the label reaches the model.
- **The leg is the identity.** The usage sample belongs to exactly one leg of exactly one request. A label that rides
  that sample cannot be claimed by another request — not by a resumed turn that reuses the A-leg, not by an identical
  request, not by a request that overlapped it.

## 3. Components

### 3.1 The canonical request carries the cause (`pkg/lipapi`)

`lipapi.Call` gains one proxy-owned field:

```go
// StructuralCause is the proxy-owned, content-free label of a structural
// change the proxy applied to this request ... never client input, never
// serialized to a provider payload.
StructuralCause string `json:"-"`
```

It follows the existing precedent of `MaxPendingWireEvents`: a proxy-owned, non-wire field on the canonical call. The
label is bounded (`MaxStructuralCauseBytes`) and validated with the other envelope fields. `CloneCall` copies it, so
every projection and per-attempt derivation carries it.

The first label wins: the earliest change to the cached prefix is the one a later discontinuity is downstream of.

### 3.2 The feature records the cause (`internal/plugins/features/toolresultelision`)

The transform writes the label onto the call it publishes, that is, onto the copy it hands back to the stage runner. The
runner's commit is what makes it live. A no-op pass, a fail-open pass and a panic-guarded pass record nothing, because in
those cases no elision reached the model.

The feature owns only the label it writes. It does not publish, does not call a diagnostic, and needs no publisher
dependency: the composition root no longer wires anything for it.

### 3.3 Core carries the label onto the leg's usage sample (`internal/core/runtime`)

The cause is captured at the final backend-open boundary: after the request-part hooks, the control-tool projection, the
conversation-view reassertion, the attempt clamps and the backend adaptation, from the exact call the backend receives.
The attempt session carries it, and the usage emitter copies it into the usage event of that attempt's leg. Core does not
interpret the label: it carries whatever the request recorded, and the diagnostic validates it.

Capturing at the final boundary matters twice over:

- A transformation that undoes the change the cause names clears the label, and the capture then reads the cleared value:
  the diagnostic never attributes a change the provider-bound request no longer carries. The proxy cannot verify the
  change's presence without inspecting content, which it deliberately does not do, so the contract is explicit: a
  transformation that reverses a proxy-owned change clears the label.
- The candidate call is rebuilt from the conversation view's frozen baseline, so a label read before that rebuild is not
  the label the provider receives.

The cause is published only when the change reached the provider-bound request. The wire path (large bodies) carries no
canonical call, so an elision that never reached the wire payload publishes no cause.

The label is per request, but the usage sample is per leg, so the attempt session — the only per-leg state the response
pipeline reads — carries it. That is one field on the attempt state, which the request-attempt state ratchet counts (§9).

#### 3.3.1 The request publishes a cause once per cache key

A structural cause names one change to the cached prefix. The request that made the change is the only request that may
publish it, and it publishes it at most once per affected cache key: a later leg of the same request, or a retry of the
same candidate, carries the same change and must not re-arm a cause that a healthy sample already proved stale.

The publication owner is the request's recovery controller. It is the one request-lifetime object that every attempt of
the request already reaches: the open loop, parallel arms, replacements, continuations and interleaved turns all open
through it. The controller holds a small claim set — one entry per cache key the request published for — and each
attempt's request facts reference that set. Parallel arms inherit the reference by the value copy they already make, and
a replacement receives it from the recovery snapshot it is opened from, so a chain of replacements of any length shares
one set.

The claim is taken once the attempt is usable: after the backend returned a stream, the mandatory binding succeeded, and
every fallible step of the open has completed. Exactly one attempt per key therefore carries the cause into its usage
samples, whatever order the attempts finish in. A request that shares the session, the backend and the model with an
earlier request has its own controller and publishes its own cause.

The request execution is the identity, so no identifier takes part in the decision. The A-leg identifier is reused by
resumed secure sessions, the trace identifier is derived from the call content and can be reused by the caller, and a
billing or continuation identifier is not the request either. The claim set lives exactly as long as the request, so it
needs no retention or eviction policy of its own, and it is the only new per-request state in the path.

### 3.4 The diagnostic applies the cause (`internal/infra/metrics`)

The cause vocabulary gains `tool-result-elision`, and one `publishableCollapseCause` whitelist is shared by both entry
points. The usage path applies the label of the sample it is processing to that sample's key, before the healthy/cold
evaluation, so a healthy sample still clears it as stale exactly like any other published cause.

Two rules keep the attribution honest:

- **The deduplication boundary is authoritative for state mutation.** The cause is applied only after the sample is
  accepted, that is after the duplicate-B-leg checks and the history rotation. A repeated usage delta for a leg the
  detector already processed never mutates the state, so it can neither apply a cause nor revive one that a healthy
  sample already cleared.
- **Every candidate key of the request is attributed.** Each candidate leg carries the cause on its own sample, and each
  key applies it once, with no per-request candidate budget.

The once-per-request-and-key rule lives on the publication side (§3.3.1), not here: the detector applies every cause it
receives, and the request publishes one. The detector therefore keeps no per-request state at all — no pending store, no
eviction bookkeeping, and no request identity. The label travels with the sample that carries it, so the detector keeps
only the per-key note it already had.

## 4. Flow

```text
request-wide transform (elision, order 900)
  └─ batch applied?  ── no ──▶ (no label)
        │ yes
        ▼
  call.StructuralCause = "tool-result-elision"   (on the transform's copy)
        │
        ▼
stage runner: commit the copy ── timed out / failed?  ──▶ discard copy: no label, no change
        │ committed
        ▼
attempt opens: capture the cause at the final boundary, claim the request's cache key
        │ (already claimed by this request?  → this attempt carries no cause)
        ▼
attempt session → usage sample of that leg: usage.Event.StructuralCause = "tool-result-elision"
        │
        ▼
diagnostic: apply the cause to the sample's key, then evaluate healthy/cold
        └─ healthy?  → note cleared (healthy epoch)
           cold run  → episode trips with the note as the cause
```

## 5. Attribution rules (Requirement 3)

| Situation | Attributed cause |
| --- | --- |
| The leg's sample carries a cause, no later healthy sample | the carried cause |
| A later healthy sample on that key | the note is cleared; the episode falls back to configuration change, keep-warm expiry, or unexplained |
| A later leg of the same request after a healthy sample | the cause is not re-applied: the request published it once, so that leg carries no cause |
| A repeated usage delta for an already-processed leg | no state change at all: the sample is not accepted |
| The leg's sample carries no cause | unchanged from today |
| Another request's leg | never affected: the cause rides its own sample |

## 6. Failure and degradation

- The label is advisory. An unknown label is ignored by the diagnostic; an absent label leaves today's behaviour.
- The elision feature's existing fail-open guarantee is unchanged: a failed pass forwards the original request and
  records no cause.
- No request, stream, route, cache decision, bill or transcript is touched by the label.

## 7. Privacy and safety

- The label is a fixed diagnostic token. No tool content, no tool-call identifier, no payload, no session content and
  no credential material crosses the seam.
- The label is never serialized to a provider payload, and it is never client input: the frontends do not populate it.
- The set of labels that can reach a log line is closed and bounded.

## 8. Alternatives considered and rejected

| Alternative | Why rejected |
| --- | --- |
| Publish the cause from the transform into the diagnostic | The transform runs inside the stage runner's bounded worker. A worker that keeps running after the deadline would publish a cause for a change that was discarded, so the publication would have to be conditional on a commit the transform cannot observe. |
| Publish the cause at the attempt stage | The attempt stage has no per-leg identity before the B-leg is allocated, and the fact that an elision happened would still have to travel on the request. |
| Key the publication by the A-leg identifier | A resumed secure session keeps the A-leg of the record it resumes (`internal/core/securesession/app/manager.go:275-340`), so two different turns can share it. |
| Key the publication by the trace identifier | Production derives the trace id from the call content when the request carries none (`internal/core/runtime/executor_prepare_secure.go:435-439`), so two identical requests share it. |
| Key the publication by the session | The session identity on the usage event is derived from the canonical call, while the publication side sees a session view, and the authoritative session id can be bound later in the request. |
| Carry the claim set on the request facts and copy it through the terminal-facts conversion | The replacement path rebuilds the facts from the terminal facts by value, so a conversion that omits the owner allocates a second set without any error. That is the defect the fourth review found. The copy is also a counted field copy, and the attempt-state ceiling is already at its limit. The controller is reached by every attempt through the snapshot each replacement is opened from, so the facts need no bridge at all. |
| Reach the claim set through the attempt's failure history | The parallel arms start their transactions with a local failure history that has no reference back to the controller (`internal/core/runtime/parallel_race.go:266`, `:328`), so they would publish against no set. |
| Detect the request identity inside the diagnostic, with a bounded per-key set of request identifiers | It needs an identifier that is unique per request execution, and none of the available ones is; and it would put a growing per-key set with its own retention and eviction policy into a diagnostic that today holds one note per key. The request execution already owns that identity. |
| Let the diagnostic apply every cause it receives and rely on the observation order to clear stale ones | A later leg of the same request re-arms the cause after a healthy sample cleared it, so a collapse is attributed to a change the cache had already proven irrelevant. |
| A pending store in the diagnostic, resolved at usage time | It needs a join key that is unique per request, and none of the identifiers available at publication time is; and it needs its own eviction, expiry and re-insertion correctness. |
| Write the cause into `call.Extensions` | `pkg/lipsdk/backendplugin/invocation_meta.go:141-152` forwards every extension key to the backend as `ext.<key>`, so a proxy-internal marker would become wire-visible. |
| Write the cause into `call.SemanticExtensions` | Semantic extensions participate in protocol requirements (`pkg/lipapi/requirements.go:329-334`), so a carrier there would change backend admission. |

## 9. Design validation checklist

1. **Core vs plugin ownership**: the feature plugin owns when an elision happened and writes the label; the diagnostic
   (infrastructure adapter) owns the vocabulary and the attribution rules; core only carries the label. Core does not
   import the feature and does not branch on it.
2. **Canonical model neutrality**: the label is a proxy-owned, non-wire field; it never enters a provider payload, and it
   carries no content.
3. **Streaming-first compatibility**: the label is set on the request path, before the backend call, and does not touch
   the stream.
4. **Zero provider SDK leaks into core**: no provider, protocol or SDK is involved.
5. **Post-output retry prevention**: unaffected; no retry or failover behaviour changes.
6. **Proportionality**: no new stage, plane, store or publisher contract. The new surface is one field on the canonical
   request, one field on the usage evidence, the copy between them, and one request-scoped claim set that keeps the
   publication once per cache key. The claim set is referenced by the request's recovery controller and by the request
   facts of each attempt; it is not carried through the facts conversions.

   The request-attempt state ratchet (`internal/archtest/request_attempt_state_baseline.json`) counts the field copies
   that reach the attempt state. Carrying the cause on the attempt session costs exactly one of them, so the checked-in
   ceiling moves from 370 to 371. The publication reference adds no counted copy: it is set where each attempt's request
   facts are built, not in the facts conversion the ratchet counts. The alternative — a core port that publishes the
   cause per leg, with a pending store on the diagnostic side — adds a port, an adapter and a store to avoid that single
   unit, which is the more expensive change.

## 10. Risks

- **Label loss**: a projection that rebuilds the request without the label loses the cause. The cause is then simply not
  attributed, which is the diagnostic's ordinary `unexplained` outcome, never a wrong attribution.
- **Observation-order semantics**: the detector decides staleness from observation time, not from dispatch order, so a
  healthy request that completes after an eliding request can clear that request's cause. This is the detector's existing
  state machine, unchanged by this slice.
- **A lost publication**: an attempt that claimed its key and then produced no usage sample (its stream failed before the
  first usage delta) consumed the request's single publication for that key without a sample to carry it, so a later
  attempt on the same key publishes no cause. The collapse is then attributed as `unexplained`, never to the wrong change.
  A nil stream, a failed binding and a failed open publish nothing: the claim is taken only after the stream exists and
  every fallible open step has completed.
- **First label wins**: when two changes shape one request, the earlier one keeps the label. The alternative is a
  last-writer rule, which is equally arbitrary; the earliest change to the cached prefix is the more likely cause of a
  later discontinuity.
