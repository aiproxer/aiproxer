# Conversation View

Canonical A-leg/B-leg visibility for proxy-owned content. One authoritative A-leg snapshot determines the model-visible B-leg trajectory, with no client-writable visibility flag.

Spec: `.kiro/specs/non-forwardable-conversation-content/` (requirements, design, tasks, final-review). SDK: `pkg/aipsdk/nonforwardable`, `pkg/aipsdk/steering`, `pkg/aipsdk/localturn`. Kernel projection: `internal/core/conversationprojection` (pure identity/exclusion/projection/reassertion/anchors). Steering state and persistence: `internal/infra/conversationview` (+ `sdkadapter`). Memory/durable stores: `internal/core/b2bua` (Memory) and `internal/core/continuity/bunstore` (SQLite/PostgreSQL). Runtime seams: `internal/core/runtime` (early projection, final reassertion, local-turn), `internal/core/localstream` (canonical local streams), `internal/infra/metrics` (bounded observability). Standard composition of the state services is owned by `internal/standardplugins/featurehost`.

## Both directions

```
A-leg / client truth          B-leg / model truth
---------------------         -------------------

normal client message  ─────> normal client message
client-visible local   ──X    (removed by projection)
(never_backend)              \

(not present)          ─────> proxy steering (injected)
```

* `never_backend`: complete A-leg-visible messages (client-visible local replies, tagged input spans) whose **semantic identity** is persisted. Every later B-leg removes them deterministically, even when the client replays full history. Source and reply are tagged **before** release (`Tag-Before-Release`), no eventual persistence.

* `persistent steering`: complete proxy-owned messages persisted under the A-leg (role+text). The client never sees them and never returns them, so the proxy reinjects them on **every** later B-leg from durable state.

The opt-in [model-system-prompt producer](model-system-prompt.md) selects ordered system overlays once at the first eligible inference; its guide covers configuration, frozen empty decisions, reload/removal and PTB capture exposure. The opt-in [proxy-memory producer](proxy-memory.md) captures eligible Codex/Claude-Code sessions, consolidates Codex-parity project memory in the background, and reinjects the published summary the same way; its guide covers requirements, configuration, MCP client setup, REST controls and the forget/plaintext limits.

No client or data-plane field can mark its own content `never_backend` or create steering. Only trusted in-process producers via `pkg/aipsdk/nonforwardable`, `pkg/aipsdk/steering`, and `pkg/aipsdk/localturn` may mutate state. Frontend wire DTOs and canonical `aipapi.Call`/`Message`/`Item` carry no visibility flag.

## Whole-message granularity

V1 operates only on **complete canonical messages** (`aipapi.Message` in `Instructions`/`Messages` or `ItemKindMessage`). Partial substring or content-part surgery is not supported. Non-message items are rejected for identity/anchor, and dangling `item_reference` targeting a removed message is cleaned during projection. V1 does not classify individual content parts independently; collocated sub-object surgery would violate the replay/placement invariant.

## Semantic identity (v1)

All exclusion and anchor decisions use a **replay-stable semantic identity** for complete messages:

* v1 digest is **SHA-256 over a deterministic semantic projection** of ordered role + ordered semantic content.
* **Excluded** (transient/non-semantic): generated/transport item IDs, item status, assistant phase, positional indexes, proxy-only `Message.Metadata`, call/session/routing fields, response IDs, trace IDs, B-leg IDs, transport/cache wrapper metadata.
* **Normalized**: CRLF/CR → LF, otherwise whitespace/Unicode preserved; structured JSON content canonicalized deterministically before hashing.
* **Authority neutral**: legacy `Message` vs item authority with equivalent role/content produce the same identity; frontend encode→decode round-trip preserves it for every covered content form.
* **Occurrence aware**: identical role/content repeated in one call shares the base digest; a placement anchor pairs `MessageAnchor{Identity, Occurrence}` to disambiguate.
* **Telemetry safe**: logs/metrics carry only bounded reason codes and counts; plaintext and raw digests are not logged to enforce exclusion or resolve an anchor (`internal/core/conversationprojection/identity.go`).

Identity/anchor/tag APIs reject non-`ItemKindMessage` items rather than inferring partial semantics.

## Limits and bounds

All bounds are atomically enforced; exceeding a bound fails the mutation with no partial state.

| Dimension | Limit |
|---|---|
| Unique `never_backend` identities per A-leg | 4096 |
| Active steering overlays per A-leg | 64 |
| Per-overlay rendered steering message | 64 KiB |
| Total active steering payload per A-leg | 256 KiB |
| OverlayID / ReasonCode | ASCII, ≤128 / ≤64 bytes, bounded charset `[_-.A-Za-z0-9]` |
| Local-turn assistant text | ≤64 KiB |
| Overlay lifecycle | bounded `OverlayID`, immutable `SlotOrdinal`, monotonic overlay revision, active/inactive state, `AnchorMissingPolicy`, bounded timestamps for diagnostics |

Tagging is batch-atomic and idempotent (repeat tags consume no capacity). Steering `Put` with identical semantic content/placement/policy is idempotent; any content/placement/policy change creates a new revision.

## Trusted producer boundaries

* `nonforwardable.Registrar`: `TagMessages(aLeg, identities, reason)` over the authoritative `Tagger` port. Batch-atomic, idempotent, 4096 cap. Registrar is bound to an authoritative `ALegID` at construction via `sdkadapter.NewRegistrar`; no global locator.
* `steering.Writer`: `Put(overlayID, message, placement, anchorPolicy, reason)` and `Deactivate(overlayID, reason)`. Stores the **rendered** model-visible payload verbatim per revision; semantic no-op is idempotent. `Deactivate` stops future reinjection without rewriting completed PTB. Constructed via `sdkadapter.NewWriter` / `NewConversationViewServicesWithObserver` with authoritative `ALegID` and optional narrow `TrajectoryResolver` + `Observer`.
* `localturn.Handler`: `Match(ctx, call) (claim bool, sourceIndexes, reasons)` is pure/narrow and may only claim **complete normalized source message indexes**; `Handle` returns bounded assistant text from which core builds the canonical assistant message. Feature bundles contribute to the ordered `PlaneLocalTurnHandlers` extension plane (`[]localturn.Handler`) in `FeatureBundle.PlaneSet`; invalid own-handlers are ignored in deterministic order; stored visibility state remains enforceable even if producers are absent in the next generation.

All three are explicitly constructed trusted services (no global registry, no client-frontend exposure). Composition wiring lives in `internal/infra/runtimebundle` / `internal/infra/metrics` via `NewConversationViewServicesWithMetrics` / `NewSteeringWriterWithMetrics`.

## Local-turn causal tagging (Tag-Before-Release)

The generic two-phase local-turn seam runs after `secret_guard` on the accepted ingress view and before inference credit/billing/route work, against a preserved deep canonical ingress clone:

1. `Match` (pure, fail-open/closed per `FailureMode`) identifies zero or more normalized complete source messages.
2. On claim: validate indexes → **commit source `never_backend` tags** before invoking `Handle`. If source tagging fails, `Handle` does not run and no B-leg opens.
3. `Handle` returns bounded assistant text → core constructs canonical assistant `Message` → **commits reply tag** before stream release. If reply tagging fails, no event is released.
4. Construct the finite `aipapi.EventStream` from exactly that tagged content: one assistant response/message/text/terminal sequence, no provider usage/cost event, no B-leg/provider identity, no background goroutine, context-cancellation aware.
5. After claim, any handler error/panic/invalid reply/tag failure fails the request with **no inference fallback**; request-level concurrency authority is released deterministically and no inference billing is performed. Zero B-legs / zero provider calls on local success.

This preserves: local reply + claimed source remain client-visible in A-leg/continuation, yet both are filtered on the next backend turn; CTP/continuation represent client truth while PTB/backend represent the projected model truth.

## Placement and cache stability

Two producer-facing placements (persisted as `stable_prefix` or fixed `PlacementAfterMessage`):

* `stable_prefix`: after the deterministic static instruction prefix and before mutable history. Stable across turns.
* `after_ingress_tail`: at registration time resolved to a **fixed semantic anchor** (`MessageAnchor{Identity, Occurrence}`) immediately after the current terminal forwardable user message. Persisted as `PlacementAfterMessage`. Registration validates the anchor is not `never_backend`, is present in the current backend-effective trajectory, and is at a safe terminal user-message boundary.

Adjacency invariant: an `after_message` overlay whose anchor message carries tool calls is injected after the contiguous tool-result messages answering those calls — after the last present result when some are missing — so a backend never receives a call split from its result. Only the overlay moves; client message bytes and order are untouched. Candidate adaptation integrity checks the same result-side placement point after authority adaptation, mapping the anchor through its stable call IDs.

For a given overlay revision, role/text/anchor/order are byte-stable. No per-turn timestamp, trace ID, nonce, or counter may enter the model-visible payload.

Cache invariant: with append-only forwardable history and unchanged steering, `M(T)` is an exact prefix of `M(T+1)` through `T`'s final content. Activation `… U_N, STEERING` stays `… U_N, STEERING, A_N, U_N+1` later.

Anti-tail rule: unchanged steering is never appended to the moving tail to fake stability; that would relocate it relative to prior assistant/user history and break prefix equality across three append-only turns (covered by regression suites in `internal/core/conversationprojection` and `internal/core/runtime`).

## Explicit discontinuities, fallback, and fail-closed

Create, content-replace, placement-move, and deactivate are **explicit cache discontinuities**. `SteeringState.CacheDiscontinuityKind`/`Placement` is emitted via the narrow `conversationview.Observer` seam (`OnSteeringMutation`) with bounded `operation`/`placement` labels only; after the discontinuity, unchanged subsequent turns restore prefix stability.

If a fixed anchor disappears (history compacted/truncated):

* `stable_prefix_fallback`: deterministic prefix fallback + bounded `anchor_missing_fallback` diagnostic (`OnAnchorFallback(stage, policy)` with `stage=early|final`).
* `fail_closed`: reject before backend (`OnAnchorFailure(policy)` + `OnProjectionFailure(stage)` only when `errors.Is(err, ErrAnchorMissing/ErrAnchorNotFound)`).

Never silently relocate to the moving tail. Both early projection and the final candidate-open reassertion choke point apply the same fallback/fail-closed policy.

## Hidden steering is not a secret

Backend-only steering **is sent to the remote provider/model** and may be quoted or paraphrased in model output. The transport/session property hides it from the client, not from the model. **Do not place credentials, tokens, secrets, API keys, or other sensitive material in steering text.** Treat at-rest steering as sensitive application data under existing DB/access controls, but assume the model will see it. Diagnostics and metrics are content-free: counts, revisions, placement classes, bounded reason codes; no OverlayID, ALegID, digest, or plaintext ever becomes a metric label or log field.

## Provider cache policy is separate

Core preserves a **structural, provider-neutral** cache-friendly canonical sequence. It does not rewrite `PromptCacheKey`, synthesize provider cache keys, choose provider TTLs, or inject provider `cache_control` markers merely because steering exists. Existing provider/backend cache behavior and the prompt-cache residency contract remain provider/backend-owned. Cache stability is structural, not absolute provider-cache-hit assurance: unrelated model-visible changes, provider options, client compaction, explicit steering revisions, model changes, or cache expiry may still legitimately miss.

Backend adapters translate the final canonical steering order without silently dropping or repositioning it; unsupported required role/placement rejects explicitly via normal pre-open adaptation (`internal/plugins/backends/protocols/*` sentinels for OpenAI-family, Anthropic-family, Gemini-family).

## What this feature does not do

No core projection redesign is required for these future consumers; they are **separate producers** consuming the plumbing above:

* **Interactive commands** (`!/`, `set`/`unset`, command handlers, routing-setting mutations) — not implemented. `localturn` has no command grammar.
* **Quality Verifier** (verifier model calls, scheduling, prompts, recall policy) — not implemented; only generic fake writers/fixtures are used in steering tests.
* **Quota/budget notifications** (thresholds, scheduling, policy) — not implemented.
* **Generic async notification scheduler** — not implemented.
* **Interleaved-thinking memo migration** — intentionally not refocused onto persistent steering.

Tests for local-turn and steering use only generic fake producers (`pkg/aipsdk/localturn`, `pkg/aipsdk/steering`, `internal/infra/conversationview/sdkadapter`, `internal/featurebundle`); no concrete command/verifier/quota/provider-cache implementation belongs in this diff.

## Observability

Bounded Prometheus series `aip_conversation_view_*_total` with labels `stage` (`early`, `final`), `placement` (`stable_prefix`, `after_message`), `operation` (`create`, `replace`, `move`, `deactivate`), `policy` (`stable_prefix_fallback`, `fail_closed`). `conversation_view_anchor_fallback_total` is `stage`+`policy`. `OnProjection`/`OnProjectionFailure`/`OnAnchorFallback`/`OnSteeringMutation` are panic-isolated via `conversationview.SafeObserver` so no observer panic affects request/mutation. Injected via `conversationview.Observer` (nil is no-op) at early projection and final reassertion chokepoints. Production composition via `internal/infra/metrics.NewConversationViewServicesWithMetrics(store, aLegID, resolver, bundle)` which combines `Bundle.ConversationViewObserver()` with `sdkadapter.NewConversationViewServicesWithObserver`; `internal/infra/metrics.NewSteeringWriterWithMetrics` similarly. No global, no HTTP exposure. No high-cardinality labels. CTP = client truth; PTB = final model truth (excludes `never_backend`, includes active steering).

## Secret guard ordering

`runSecretGuardStage` runs before the generic two-phase local-turn seam (`runLocalTurnStage`) on the accepted ingress view. Local handlers see already-guarded content. Reason/source codes are bounded identifiers and must not carry command arguments, quota values, prompts, steering text, or raw payloads. No client-authoritative visibility/steering field is added to canonical or frontend wire DTOs.

## Lifecycle, snapshot, and persistence

* Snapshot: one coherent bounded `Snapshot` per logical turn (exclusions + active overlays + revisions + slot order + state revision). Linearizable per A-leg for Memory and Bun (SQLite/PostgreSQL). In-flight turn stays on snapshot N; concurrent mutation N+1 applies to the next turn.
* Stores: `internal/core/b2bua` (Memory, under existing A-leg lock) and `internal/core/continuity/bunstore` (Bun, A-leg-owned rows, A-leg deletion cascades). No widening of `b2bua.Store` or public `pkg/aipsdk/continuity.Store` beyond the optional capability. Unsupported continuity without conversation-view capability fails deterministically at composition.
* Durability: with durable continuity, process restart and generation reload preserve exclusions/active steering (no stale process-global cache; shared PostgreSQL per-logical-turn snapshot; `bunstore` handles delete/recreate/no-op/revision correctly).
* Runtime: early projection occurs after authoritative A-leg/secret/submit boundaries but before backend request/pre-request transforms, context estimation, billing/route/capability work; final reassertion occurs at the shared candidate-open choke point after interleaved/attempt transforms and before PTB capture/`Backend.Open`. Every attempt/race/TTFT/interleaved arm observes the same frozen snapshot; no durable re-read per B-leg and no per-candidate I/O.

## References

* Design invariants and sequence: `.kiro/specs/non-forwardable-conversation-content/design.md`
* Requirements: `.kiro/specs/non-forwardable-conversation-content/requirements.md` (Req 13.7–13.18 quality gates)
* Validation evidence: `.kiro/specs/non-forwardable-conversation-content/final-review.md` (skips, gate outputs, traceability)
* Runtime flow: `docs/runtime-flow.md` (executor flow) and `docs/architecture.md` (ownership map)
* SDK contracts: `pkg/aipsdk/nonforwardable`, `pkg/aipsdk/steering`, `pkg/aipsdk/localturn`, `pkg/aipsdk/feature` (`PlaneLocalTurnHandlers` in `FeatureBundle`)
