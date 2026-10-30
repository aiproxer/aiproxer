# Requirements Document

## Introduction

Go-LIP needs one canonical mechanism for **proxy-owned conversation visibility** across the A-leg/B-leg boundary. Two opposite cases must be supported without teaching frontends or backends about individual features:

1. **Client-visible, backend-hidden content** — complete messages that remain part of the client/agent-visible A-leg transcript but must never be sent to an inference backend, even when the client later replays the complete transcript.
2. **Backend-visible, client-hidden steering** — complete proxy-owned steering messages that are never exposed to the client/agent, but remain model-visible on every applicable B-leg because the proxy persists and deterministically reinjects them.

The second case is not ordinary request mutation. Because the client never sees backend-only steering, the client cannot return it on the next turn; therefore the proxy owns its complete lifecycle, persistence, placement, and reinjection. The placement contract is also a prompt-cache contract: unchanged steering must stay at a stable model-visible position across turns rather than following the moving tail of the conversation.

This specification implements the reusable infrastructure only. It deliberately does **not** implement interactive command syntax/handlers, routing-setting commands, Quality Verifier policy, quota-notification policy, or any other concrete producer. Those later features consume the canonical plumbing defined here.

The first supported unit for both visibility directions is a **complete canonical conversation message**. Partial substring/content-part surgery is not part of this feature.

## Boundary Context

### In scope

- Versioned replay-stable semantic identity for complete canonical message units.
- Authoritative A-leg-scoped `never_backend` classification for client-visible/backend-hidden messages.
- Authoritative A-leg-scoped persistent backend-only steering overlays with complete proxy-owned message payloads.
- Bounded MemoryStore and SQLite/PostgreSQL durability following A-leg lifecycle.
- One coherent per-turn conversation-view snapshot containing exclusion and steering state.
- Early derivation of a backend-effective call: remove client-visible local-only content, then inject persistent steering.
- Stable-prefix and fixed activation-boundary steering placement.
- Cache-friendly reinjection with explicit prefix-stability invariants.
- A final backend projection guard before PTB traffic capture and backend `Open`.
- A generic ordered local-turn extension seam for successful proxy-local responses with no B-leg.
- Tag-before-release semantics for proxy-generated client-visible local replies.
- A trusted steering writer/controller for future proxy features.
- Canonical local text response streams encoded by existing frontends.
- Full-history and OpenResponses continuation replay/materialization compatibility.
- Metrics/diagnostics, failure behavior, race/cache regression coverage, SDK/plugin documentation, and architecture tests required for production readiness.

### Out of scope

- `!/` or any other interactive command grammar.
- Interactive command parsing, dispatch, command handlers, setting/routing mutations, or concrete command state.
- Quality Verifier decision logic, verifier calls, scheduling, prompts, or recall policy.
- Concrete quota/budget/usage notifications or their scheduling/policy.
- A generic asynchronous notification scheduler.
- Automatic migration of the existing interleaved-thinking memo mechanism to the new persistent steering facility.
- Partial text/content-part deletion or injection inside a mixed message.
- Client/data-plane APIs allowing callers to mark their own content hidden or create steering.
- Provider-specific steering logic or pairwise frontend/backend translation.
- Provider cache TTL policy, `cache_control` placement, explicit provider cache object management, or modification of `PromptCacheKey`.
- Guaranteeing that a model will never quote/paraphrase hidden steering in its output; backend-hidden-to-client is a transport/session property, not a secrecy boundary against the model.

### Boundary ownership

- **Canonical message semantics:** existing `pkg/lipapi`; no client-authoritative visibility flag is added to `Call`, `Message`, or `Item`.
- **Conversation-view domain/application policy:** focused `internal/core/conversationview` capability: identity, A-leg state value objects, placement resolution, projection, and final integrity checks.
- **Trusted producer contracts:** focused `pkg/lipsdk/nonforwardable`, `pkg/lipsdk/localturn`, and `pkg/lipsdk/steering` contracts.
- **Extension composition:** existing FeatureBundle/runtime snapshot machinery for local-turn handlers; steering writer is an explicitly constructed trusted service, not a global locator.
- **A-leg persistence:** standard `internal/core/b2bua` memory store and `internal/core/continuity/bunstore` through a focused optional capability.
- **Runtime enforcement:** `internal/core/runtime` after authoritative A-leg resolution and at the shared candidate-open boundary.
- **Frontend/backend adapters:** remain protocol/provider translation edges; they do not own visibility policy.
- **Provider prompt-cache semantics:** remain provider/backend-owned; this feature only preserves a cache-friendly canonical model-visible sequence.

### Hexagonal lens

- Driving adapters/producers: existing client frontends and future trusted proxy features.
- Application orchestration: runtime local-turn stage, per-turn conversation-view snapshot, and backend-call preparation.
- Core policy/domain: semantic message identity, exclusion, persistent steering state, anchor resolution, deterministic projection.
- Driven adapters: memory/Bun A-leg conversation-view stores.
- Composition root: process services + immutable runtime generation/FeatureBundle snapshot.

### Revalidation triggers

Any implementation that introduces a client/wire-visible `non_forwardable`/steering flag, moves policy into frontends/providers, widens the base `b2bua.Store`/public continuity contracts, adds regex/substring rewriting, re-reads mutable state per B-leg, silently relocates steering to the current tail, mutates provider cache keys from core, or allows client-visible local output before durable classification MUST return to design validation.

## Requirement 1: Canonical Replay-Stable Message Identity

**Objective:** As the proxy, I want one deterministic semantic identity for a complete message so that client-visible local-only content and persistent placement anchors survive reconstruction and replay.

### Acceptance Criteria

1.1. THE identity service SHALL support complete canonical message units represented by legacy `lipapi.Message` in `Instructions`/`Messages` and by `lipapi.Item` where `Kind == ItemKindMessage`.

1.2. WHEN two supported messages have equivalent canonical role and ordered semantic content, THE service SHALL produce the same versioned identity regardless of legacy-message versus item authority.

1.3. THE v1 identity SHALL use SHA-256 over a deterministic semantic projection containing role and ordered semantic content.

1.4. THE identity projection SHALL exclude transient/non-semantic carriers including generated/transport item IDs, item status, assistant phase, positional indexes, proxy-only `Message.Metadata`, call/session/routing fields, response IDs, trace IDs, B-leg IDs, and transport/cache wrapper metadata.

1.5. THE identity projection SHALL normalize CRLF/CR to LF while otherwise preserving text whitespace and Unicode, and SHALL canonicalize structured JSON content deterministically before hashing.

1.6. WHEN a supported message is encoded through an official frontend and later decoded from equivalent replay, THE decoded identity SHALL remain equal for every covered content form.

1.7. Normal registry logs/metrics SHALL contain only version/digest and bounded metadata; plaintext SHALL NOT be logged merely to enforce exclusion or resolve an anchor.

1.8. IF an item is not a complete `ItemKindMessage`, THEN identity/tag/anchor APIs SHALL reject it rather than infer partial-item semantics.

1.9. V1 SHALL NOT classify or anchor an individual substring/content part independently from its containing message.

1.10. WHEN role/content-identical messages occur more than once, THE base semantic digest MAY repeat; any placement anchor that must distinguish occurrences SHALL pair the digest with an explicit occurrence ordinal.

## Requirement 2: Authoritative A-Leg Conversation-View State

**Objective:** As the proxy, I want visibility state to follow authoritative session continuity so exclusions and steering survive turns, reloads, durable restart, and shared stores.

### Acceptance Criteria

2.1. ALL state SHALL be keyed by proxy-authoritative `ALegID`; client session hints, response IDs, B-leg IDs, or unvalidated A-leg strings SHALL NOT authorize lookup/mutation.

2.2. THE standard in-memory B2BUA store SHALL implement a focused optional conversation-view capability under the existing A-leg lock/lifecycle without widening base `b2bua.Store`.

2.3. THE standard Bun continuity store SHALL persist equivalent semantics for SQLite and PostgreSQL using A-leg-owned rows/tables and SHALL remove dependent state with A-leg deletion.

2.4. WHEN durable continuity is configured and the process restarts, a resumed A-leg SHALL observe previously committed exclusions and active steering.

2.5. WHEN runtime generations reload, conversation-view state SHALL remain process/continuity-owned and SHALL NOT be copied into or reset with a generation.

2.6. WHEN an A-leg is deleted/recreated, the new A-leg SHALL NOT inherit prior visibility state.

2.7. THE store SHALL provide one coherent bounded snapshot containing both `never_backend` tags and active persistent steering overlays for one logical turn.

2.8. THE store SHALL provide narrow mutation capabilities for exclusion tagging and steering put/replace/deactivate; consumers SHALL depend only on the narrow port they require.

2.9. Snapshot/mutations SHALL be linearizable per A-leg for the standard memory and Bun implementations.

2.10. Shared PostgreSQL deployments SHALL read authoritative state per logical turn; no indefinitely stale process-global cache SHALL be authoritative.

2.11. THE base public `pkg/lipsdk/continuity.Store` and unrelated continuity implementations SHALL remain source-compatible.

2.12. THE state representation SHALL be explicitly bounded and reject mutations atomically when any configured v1 count/byte limit would be exceeded.

## Requirement 3: Client-Visible / Backend-Hidden (`never_backend`) Registry

**Objective:** As the proxy, I want A-leg-visible local messages to stay off every inference B-leg after the client replays them.

### Acceptance Criteria

3.1. A `never_backend` tag SHALL contain message identity, bounded non-secret reason code, and bounded creation diagnostics; it SHALL NOT require message plaintext persistence.

3.2. Tagging the same identity repeatedly on one A-leg SHALL be idempotent and consume no additional unique-tag capacity.

3.3. Tagging multiple identities in one batch SHALL commit all new identities or none.

3.4. THE v1 unique-tag limit SHALL be 4096 identities per A-leg.

3.5. IF a tag operation exceeds capacity or persistence fails, the operation SHALL fail without partial mutation.

3.6. THE feature SHALL expose no untrusted client/data-plane tag mutation API.

3.7. Successfully committed current-turn tags SHALL be merged into the request-local snapshot immediately without another store read.

## Requirement 4: Tag-Before-Release / Tag-Before-Local-Side-Effect Safety

**Objective:** As the proxy, I want client-visible local content classified before it can create an unprotected replay.

### Acceptance Criteria

4.1. BEFORE any proxy-generated client-visible message designated `never_backend` is released through a frontend, its tag SHALL commit successfully.

4.2. IF reply tagging fails, the proxy SHALL release no event containing that local-only reply.

4.3. WHEN a local-turn handler claims existing client input as local-only, core SHALL persist the claimed source-message tags before invoking the handler's side-effecting/response-producing phase.

4.4. IF claimed-input tagging fails, the handler's execution phase SHALL NOT run and no B-leg SHALL be opened as fallback.

4.5. IF local-turn execution fails after source tagging, the source tags SHALL remain authoritative and the request SHALL fail without inference fallback.

4.6. No asynchronous/eventual persistence after client release SHALL satisfy this safety contract.

## Requirement 5: Early Backend-Effective Conversation Projection

**Objective:** As routing/context/billing/runtime policy, I want the exact model-visible conversation derived before backend-oriented work so economics and capability decisions use the same semantic context that will be sent.

### Acceptance Criteria

5.1. AFTER authoritative A-leg resolution and client/A-leg evidence handling, THE runtime SHALL load one conversation-view snapshot for the logical turn.

5.2. BEFORE backend-oriented request/pre-request transforms, context-size estimation, billing authorization, route planning, capability negotiation, or B-leg creation, THE runtime SHALL derive a backend-effective call from a deep clone of accepted client/A-leg input.

5.3. Projection SHALL first remove all complete concrete messages matching `never_backend`, including dependent in-call `item_reference` values that would otherwise dangle.

5.4. Projection SHALL then inject every active backend-only steering overlay exactly once using the deterministic placement/order contract in Requirements 9 and 10.

5.5. CTP/client evidence and continuation/A-leg truth SHALL NOT be rewritten merely to produce the B-leg view.

5.6. AFTER removal/injection/dependency cleanup, THE backend-effective call SHALL pass canonical validation.

5.7. IF projection cannot resolve required state/dependencies/steering placement safely, the request SHALL fail closed before backend-oriented work, except for a steering overlay whose explicit anchor-missing policy permits deterministic stable-prefix fallback.

5.8. Backend-oriented transforms, request-size/context estimation, billing/credit calculations, route/capability checks, and baseline freeze SHALL operate on the projected call.

5.9. Projection SHALL be protocol/provider-neutral and SHALL import no frontend DTO or provider SDK.

5.10. The logical turn SHALL carry the frozen snapshot through every B-leg attempt; mutable continuity state SHALL NOT be reinterpreted independently per failover/race/retry arm.

## Requirement 6: Final Backend Projection Guard

**Objective:** As the proxy safety boundary, I want the final backend-facing call to reassert both exclusion and persistent steering invariants after late attempt shaping.

### Acceptance Criteria

6.1. AFTER per-candidate shaping/attempt transforms and BEFORE PTB capture/backend `Open`, THE shared candidate-open path SHALL reassert the frozen conversation-view snapshot.

6.2. Reassertion SHALL remove any `never_backend` message reintroduced by late transforms.

6.3. Reassertion SHALL ensure each active persistent steering overlay is present exactly once at its frozen placement; it SHALL NOT append another copy to the moving tail.

6.4. IF a mutable attempt transform changes the relevant history around a steering anchor, reassertion SHALL deterministically rebuild the overlay placement from the frozen snapshot or reject the candidate/request according to the overlay's anchor-missing policy.

6.5. THE candidate-facing canonical call SHALL validate after reassertion and normal candidate adaptation SHALL preserve the projected semantic trajectory; unsupported representation SHALL reject explicitly rather than silently move/drop steering.

6.6. PTB capture SHALL be produced from the already-enforced backend-facing call: it SHALL exclude `never_backend` messages and SHALL include active backend-only steering.

6.7. THE same guard SHALL cover initial opens, failover/retry-before-output, parallel/race arms, TTFT replacement, and interleaved thinker/executor B-legs through the shared choke point.

6.8. Backend plugins/connectors SHALL not implement visibility filtering/steering persistence; they receive an ordinary canonical backend-facing call.

6.9. Existing no-retry-after-client-output behavior SHALL remain unchanged.

## Requirement 7: Generic Local-Turn Extension Seam

**Objective:** As a future proxy-local feature, I want a canonical way to claim and answer a client turn locally without faking a backend error or modifying each frontend.

### Acceptance Criteria

7.1. FeatureBundle SHALL accept an ordered optional list of generic local-turn handlers; this spec SHALL add no concrete command handler.

7.2. The local-turn stage SHALL run after authentication/workspace/secure-session/A-leg authority, ingress secret guarding and accepted submit policy, and before inference credit/billing, route planning/provider work, or B-leg creation.

7.3. Runtime SHALL retain a deep canonical ingress view so local matching/source tagging uses what the client actually submitted.

7.4. EACH handler SHALL expose a pure `Match` phase that either passes or claims the turn and identifies zero or more normalized complete source-message indexes plus bounded reason codes.

7.5. On claim, runtime SHALL validate indexes and commit source tags before invoking `Handle`.

7.6. The first successfully claimed handler in deterministic order SHALL own the turn.

7.7. AFTER claim, handler error/panic/invalid reply/tag failure SHALL fail the request and SHALL NOT fall through to inference.

7.8. BEFORE claim, Match errors MAY obey existing fail-open/fail-closed extension semantics.

7.9. `Handle` SHALL return one bounded assistant text reply; core SHALL construct the canonical assistant message, tag it, then construct/release the local stream from the same semantic content.

7.10. Successful local turns SHALL open zero B-legs, perform zero model/provider calls, perform no inference billing authorization, and emit no provider usage.

7.11. Local turns SHALL preserve normal A-leg/session/trace correlation, CTP evidence, frontend encoding and continuation recording.

7.12. Any request-level concurrency authority acquired before matching SHALL be released deterministically with no fabricated B-leg usage.

7.13. No `!/`, `set`, `unset`, command registry, routing mutation, or command-owned state is implemented by this spec.

## Requirement 8: Canonical Proxy-Local Response Stream

**Objective:** As a frontend, I want proxy-local success to use the same canonical response abstraction as inference so all official frontends encode it uniformly.

### Acceptance Criteria

8.1. A successful local turn SHALL return a normal finite `lipapi.EventStream` with exactly one assistant text response.

8.2. The stream SHALL produce a valid response/message/text/terminal sequence for streaming and non-streaming collection.

8.3. It SHALL emit no provider usage/cost event and fabricate no backend/B-leg identity.

8.4. Existing shared frontend encoders SHALL encode it without provider-specific branching.

8.5. The message tagged before release SHALL be semantically identical to the encoded assistant content so future replay identity matches.

8.6. The finite local stream SHALL obey context cancellation/Close and SHALL require no background goroutine.

8.7. Frontend contract tests SHALL prove local output replay decodes to identity-equivalent canonical assistant messages.

## Requirement 9: Persistent Backend-Only Steering Overlays

**Objective:** As a trusted proxy feature, I want model-visible steering that remains invisible to the client and is automatically reconstructed on every later B-leg.

### Acceptance Criteria

9.1. THE SDK SHALL define a trusted backend-only steering writer/controller; no client/data-plane protocol SHALL expose equivalent mutation authority.

9.2. EACH overlay SHALL have a bounded stable `OverlayID`, immutable `SlotOrdinal`, monotonic overlay revision, active/inactive state, bounded source/reason code, complete canonical message payload, placement, anchor-missing policy, and timestamps needed for diagnostics.

9.3. Because the client cannot replay hidden steering, THE store SHALL persist the complete model-visible canonical message payload for active overlays; proxy-only metadata SHALL NOT be part of persisted/model-visible content.

9.4. THE v1 persisted steering message SHALL be exactly one complete text-bearing canonical message, not arbitrary tool/reasoning/extension items and not a substring injection.

9.5. V1 SHALL support two producer-facing placement modes:
- `stable_prefix`: persistently placed at the deterministic end of the stable proxy/client instruction prefix and before mutable conversation history;
- `after_ingress_tail`: resolved at registration time to a durable anchor immediately after the current terminal forwardable user message, then stored as a fixed semantic message anchor.

9.6. A resolved after-message anchor SHALL use replay-stable message identity plus occurrence ordinal, not transient item IDs or current absolute request indexes.

9.7. Runtime SHALL reject an after-message registration whose anchor is itself `never_backend`, absent from the current backend-effective trajectory, or not a safe terminal user-message boundary.

9.8. Active overlays SHALL be reinserted on every subsequent applicable backend turn even though no corresponding client message exists.

9.9. Client-visible frontend responses, CTP client payloads, secure-session client transcript surfaces, and continuation records SHALL NOT be augmented with backend-only steering.

9.10. PTB/backend-facing calls SHALL contain active steering after projection.

9.11. Multiple overlays at one placement SHALL use immutable `SlotOrdinal` order; map/DB iteration order SHALL never determine model-visible ordering.

9.12. Creating a new overlay SHALL allocate a new slot after existing overlays for the same resolved placement; replacing content for an `OverlayID` SHALL retain its slot and placement unless the producer explicitly performs a placement-changing replacement.

9.13. A semantic no-op `Put` SHALL be idempotent; a content/placement/policy change SHALL create a new revision.

9.14. `Deactivate` SHALL stop the overlay from all future snapshots but SHALL NOT rewrite already completed B-leg evidence.

9.15. Mutations committed after a logical turn has taken its snapshot SHALL apply only to later logical turns; all attempts of the in-flight turn SHALL use the frozen snapshot.

9.16. Memory and Bun implementations SHALL persist/restore overlay content, placement, slot, revision and active state across A-leg lifetime/restart/reload.

9.17. V1 SHALL cap active overlays at 64 per A-leg, each rendered steering message at 64 KiB, and total active steering payload at 256 KiB per A-leg; mutations exceeding limits SHALL fail atomically.

9.18. Normal logs/metrics SHALL NOT contain steering plaintext. Protected debugging MAY expose only bounded IDs/revisions/digests unless a separate existing secure transcript surface explicitly permits content.

9.19. Hidden steering SHALL NOT be treated as a secret channel: producers SHALL NOT place credentials/tokens/secrets in it, because the remote model/backend receives it and model output may reveal its substance.

9.20. This spec SHALL NOT implement Quality Verifier, interactive-command, quota-notification, or other producer policy; it only makes those producers possible.

## Requirement 10: Cache-Stable Steering Placement

**Objective:** As a proxy operator, I want persistent steering to preserve provider prompt-cache locality across ordinary turns so hidden orchestration does not create avoidable latency/cost regressions.

### Acceptance Criteria

10.1. THE canonical cache invariant SHALL be: if the client/backend-relevant history changes only by appending forwardable conversation content and the active steering snapshot/revisions are unchanged, the normalized model-visible sequence used on turn N SHALL be an exact prefix of the normalized model-visible sequence on turn N+1 through turn N's final input content.

10.2. THE implementation SHALL NOT satisfy reinjection by appending an unchanged persistent steering message to the current moving tail on every request, because that relocates it relative to prior assistant/user history.

10.3. For a given overlay revision, model-visible role/text, placement anchor, and relative overlay ordering SHALL be deterministic and semantically byte-stable across turns; per-turn timestamps, trace IDs, random nonces, request IDs, counters, or fresh rendering SHALL NOT enter its model-visible payload.

10.4. An `after_ingress_tail` overlay created for turn N SHALL produce activation ordering `... U_N, STEERING`; when later history is replayed/appended, reinjection SHALL produce `... U_N, STEERING, A_N, U_N+1 ...`, so the activation request remains a prefix of later requests subject to unrelated model-visible changes.

10.5. A `stable_prefix` overlay SHALL remain at the same deterministic prefix slot relative to all unchanged static instructions and other stable-prefix overlays on every turn.

10.6. Overlay create/content-replace/placement-change/deactivate is an **explicit cache discontinuity**. The runtime SHALL record a bounded revision/discontinuity diagnostic; after that mutation, unchanged subsequent turns SHALL again satisfy the stable-prefix invariant.

10.7. If a fixed after-message anchor disappears because client history was compacted/truncated/replaced, runtime SHALL never silently relocate the overlay to the current tail. The overlay SHALL obey its stored `AnchorMissingPolicy`: `stable_prefix_fallback` or `fail_closed`.

10.8. `stable_prefix_fallback` SHALL use one deterministic prefix location and emit bounded `anchor_missing_fallback` diagnostics. The history rewrite that removed the anchor is already a prefix discontinuity; fallback SHALL not introduce turn-to-turn wandering.

10.9. `fail_closed` SHALL prevent the backend request when a required anchor cannot be resolved.

10.10. Core SHALL NOT rewrite `PromptCacheKey`, synthesize provider cache keys, choose provider TTLs, or inject provider `cache_control` markers merely because steering exists. Existing provider/adaptor cache behavior and the prompt-cache residency contract remain separate.

10.11. The cache-stability guarantee is structural, not absolute provider-cache-hit assurance: unrelated model-visible changes such as tool/schema/system changes, provider options, client compaction, explicit steering revisions, model changes, or cache expiry MAY legitimately cause misses.

10.12. Tests SHALL compare consecutive projected canonical trajectories across at least three append-only turns and SHALL fail if unchanged steering moves, duplicates, changes payload, or destroys prefix equality.

10.13. A bounded backend-family sentinel set SHALL prove the stable canonical ordering survives representative OpenAI-family, Anthropic-family, and Gemini-family translation without restoring a frontend×backend Cartesian matrix.

10.14. Where provider prompt-cache usage evidence is available, integration tests MAY assert expected cache-read continuity, but correctness SHALL NOT depend on external network/cache timing.

## Requirement 11: Replay, Continuation, and A-Leg/B-Leg Visibility

**Objective:** As a client and runtime, I want each visibility class reconstructed on the correct leg after history replay/materialization.

### Acceptance Criteria

11.1. WHEN a client resends full legacy history containing tagged local messages, the next backend-effective call SHALL remove all matching `never_backend` messages.

11.2. WHEN OpenResponses `previous_response_id` materialization reconstructs tagged local-turn input/reply messages, projection SHALL remove those concrete message items before backend work.

11.3. THE feature SHALL NOT require frontends to delete client-visible local turns from their A-leg/continuation history for correctness.

11.4. Backend-only steering SHALL never rely on client replay/continuation storage; it SHALL be reconstructed exclusively from authoritative A-leg conversation-view state.

11.5. WHEN the same A-leg survives generation reload or process restart with durable continuity, previously tagged messages SHALL remain excluded and active steering SHALL remain injected even if the original producer is absent from the new generation.

11.6. An opaque out-of-call item reference containing no concrete local message remains governed by existing continuation/reference semantics; projection guarantees concrete tagged message removal and cleanup of references to items removed from the same call.

11.7. CTP and client-facing continuation evidence SHALL represent client-visible truth; PTB SHALL represent the final model-visible truth including hidden steering and excluding local-only messages.

## Requirement 12: Observability and Security

**Objective:** As an operator, I want visibility projection auditable without leaking local control/steering payloads or confusing client and backend evidence.

### Acceptance Criteria

12.1. CTP evidence SHALL describe what the client actually submitted, including client-visible local-only messages when present, under existing redaction/capture policy.

12.2. PTB evidence SHALL contain the final projected backend call: no `never_backend` messages and all required active steering.

12.3. Runtime SHALL emit bounded counters/diagnostics for filtered count, injected overlay count, overlay revisions, placement class, anchor fallback/failure and explicit cache discontinuity; message/steering plaintext and raw digests SHALL NOT become high-cardinality metric labels.

12.4. Conversation-view store lookup failure on a backend turn SHALL fail closed rather than assume empty exclusions/steering.

12.5. Existing secret-guard boundaries SHALL run before local-turn handlers inspect/handle accepted client input.

12.6. Reason/source codes SHALL be bounded identifiers and SHALL NOT contain command arguments, quota values, prompts, steering text, or arbitrary payload.

12.7. No client-authoritative visibility/steering field SHALL be added to canonical or frontend wire DTOs.

12.8. Core visibility packages SHALL import no provider SDK.

12.9. Backend-only steering content SHALL be considered sensitive application data at rest and in PTB capture according to existing access/redaction policy, even though it is not a secret from the remote model.

## Requirement 13: Performance, Lifecycle, Compatibility, and TDD Quality Gates

**Objective:** As a maintainer, I want complete infrastructure with bounded hot-path cost and executable regression proof before concrete producers depend on it.

### Acceptance Criteria

13.1. A logical backend turn SHALL perform no more than one authoritative conversation-view snapshot read; every B-leg attempt SHALL reuse request-local state.

13.2. No polling loop, watcher, background cleanup goroutine, or global mutable service locator SHALL be introduced for projection/steering state.

13.3. In-memory state SHALL use existing A-leg store synchronization/eviction; durable state SHALL follow A-leg lifecycle.

13.4. If configured local-turn/steering producer plumbing requires conversation-view capabilities that the continuity implementation does not provide, composition SHALL fail deterministically rather than run without safety/persistence.

13.5. Local-turn handler lists SHALL be frozen with immutable runtime snapshots; stored visibility state remains enforceable independently of producer presence.

13.6. Existing routing, failover, no-retry-after-output, billing, prompt-cache residency, frontend encoding, and provider adapter ownership SHALL remain intact outside the projected call contents.

13.7. BEFORE production implementation, RED tests SHALL freeze identity, storage, projection, local-turn ordering, steering persistence/placement, anchor failure, cache-prefix invariants, and final guard behavior.

13.8. Memory, SQLite, and PostgreSQL contract tests SHALL cover atomicity, bounds, A-leg deletion/recreation, restart/load, concurrent snapshot/mutation, overlay revision/slot ordering, and shared-store behavior.

13.9. Runtime tests SHALL prove tagged client-visible history cannot affect route/context/billing or reach PTB/backend and that hidden steering does affect the projected/PTB/backend call while remaining absent from client output/continuation.

13.10. A late attempt transform SHALL deliberately remove/move/duplicate steering and reintroduce a tagged message in RED tests; final reassertion SHALL restore the frozen backend view or reject safely before `Open`.

13.11. Runtime path coverage SHALL include initial, failover/retry-before-output, parallel/race, TTFT, and interleaved paths through shared projection code.

13.12. Local-turn tests SHALL use only generic fake handlers and prove source-tag-before-handle, reply-tag-before-release, zero B-legs/inference usage and post-claim no-fallback.

13.13. Steering tests SHALL use only generic fake writers/fixtures and SHALL NOT implement verifier/command/quota logic.

13.14. Frontend/continuation tests SHALL cover full-history replay and OpenResponses materialization while proving backend-only steering never enters client continuation.

13.15. Race tests SHALL cover concurrent tag/steering mutations versus snapshots and generation reload/producer removal.

13.16. Architecture/docs SHALL explain both visibility directions, whole-message granularity, trusted producer boundaries, fixed-anchor cache behavior, explicit cache discontinuities, and the prohibition on using hidden steering as a credential/secrecy mechanism.

13.17. Final validation SHALL run repository formatting/vet/arch checks, deterministic unit suites, focused SQLite/PostgreSQL tests, backend-family sentinel translation tests, and targeted `go test -race` for the new core/runtime/store packages.

13.18. Final diff review SHALL remove unnecessary generic framework/configuration and SHALL confirm no concrete interactive command, verifier, quota-notification, or provider cache policy implementation slipped into scope.
