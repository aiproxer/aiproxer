# Model-specific persistent system steering — #735

## V1 slice

An operator-configured producer selects persistent backend-only system overlays once at the first eligible inference of an authoritative A-leg. It reuses conversation-view projection, placement and final reassertion. Memory, SQLite and PostgreSQL are mandatory. The approved plan is the authority for activation policy and implementation order; this spec materializes that approval without broadening it.

## Requirement 1 — Operator configuration and pure matching

- **1.1** When `plugins.features` contains an enabled `model-system-prompt` registration, the feature shall decode only its `rules` subtree (`id`, `model_pattern`, `append`); outer `enabled` is authoritative and defaults off; unknown fields shall be rejected.
- **1.2** Before publishing an enabled generation, the feature shall reject missing/duplicate/non-ASCII/invalid derived overlay IDs, missing or invalid Go regexes, invalid UTF-8, empty/whitespace-only or NUL-containing append text, and text exceeding 64 KiB per overlay.
- **1.3** Before publication, the feature shall reject more than 64 rules or more than 256 KiB total configured append bytes; runtime capacity shall also include other active producers.
- **1.4** The feature shall compile regexes once during generation construction, retain immutable rules, evaluate all matches in configuration order and preserve append bytes without rendering/interpolation.
- **1.5** For each matching rule, the feature shall produce exactly one standalone canonical `RoleSystem` overlay with ID `model-system-prompt.<rule-id>`, `StablePrefix`, `StablePrefixFallback` and a bounded reason, preserving original system/developer instructions byte-for-byte.

## Requirement 2 — Eligible inference and initial intent

- **2.1** On an accepted inference turn, the runtime shall bootstrap after Secret Guard/submit and authoritative A-leg binding but before the one normal conversation-view snapshot, so the first eligible backend call sees the committed overlays.
- **2.2** The runtime shall derive initial intent from the accepted selector after default-route/model-alias resolution and the frozen authoritative A-leg override, using logical `Primary.Model`, never native/provider IDs or the eventual candidate/failover winner.
- **2.3** The runtime shall inspect all selector leaves without selecting candidates; one distinct logical model is eligible, while multiple distinct models commit an empty `ambiguous_skip` decision with bounded diagnostics.
- **2.4** The runtime shall evaluate local-turn Match once in existing handler order before bootstrap and retain the selection for later Handle; claimed local turns shall not bootstrap or allocate B-legs, and local-only A-legs remain eligible at their first inference. Existing failure, tagging, response release and authority cleanup semantics shall remain intact.
- **2.5** When an unmarked A-leg already has authoritative B-leg allocations, the store-authorized operation shall commit an empty `preexisting_skip` decision rather than retroactively inject; determination and commit shall serialize with `NextBLeg`, not use empty steering, completed attempt rows, client newness or a process cache. Detached children use their private A-leg, not parent lineage.

## Requirement 3 — Atomic authoritative persistence

- **3.1** Under A-leg authority, persistence shall check the generation-independent producer completion first, return committed evidence without invoking the decision callback on reuse, or atomically commit the full ordered overlay batch and completion, including zero-overlay no-match/skip decisions.
- **3.2** Concurrent first inference turns shall have one successful committed decision; losers shall reuse it without duplicate/reordered overlays, including independent SQLite/PostgreSQL handles.
- **3.3** On validation, collision, capacity, cancellation or storage failure, the request shall reject before backend Open and persistence shall leave no partial overlays, completion, allocated slots or revisions; preexisting feature-owned overlay IDs without completion shall be rejected, not overwritten.
- **3.4** Memory shall retain completion/overlays for the authoritative A-leg lifetime; SQLite and PostgreSQL shall preserve them across reopen/restart/resume. Retirement shall remove both through the established A-leg lifecycle.
- **3.5** Forward migrations, EnsureSchema and the existing `dbparity.DefaultCatalog()` contracts shall agree across SQLite/PostgreSQL; database coordination shall use transaction authority, with SQLite write reservation before reads and PostgreSQL A-leg row locking, never process-global durable locks.

## Requirement 4 — Model visibility, client truth and privacy

- **4.1** Later turns shall reconstruct overlays without client replay; retries, failover and parallel attempts shall reuse the turn's frozen snapshot, and final pre-Open reassertion shall restore steering removed/moved by late transforms.
- **4.2** Disabled/no-match sessions shall preserve backend-visible canonical input; matched overlays shall occur exactly once in configuration order and preserve a stable instruction prefix across at least three append-only turns.
- **4.3** The producer shall never add hidden steering to client ingress, CTP, continuation truth, structural client-turn records or frontend output; PTB/backend-effective input shall include it. A-legs shall not share session/user contents.
- **4.4** Existing canonical adapters shall convey steering into OpenAI-, Anthropic- and Gemini-family instruction representations without provider-specific feature branches or a frontend×backend Cartesian matrix.
- **4.5** Diagnostics shall explain attempted/matched/no-match/failed/ambiguous/preexisting/reused decisions with bounded outcomes/counts and bounded logical model evidence; normal logs/metrics shall contain no append text, raw steering digests or client prompt contents, and no unbounded model metric labels.

## Requirement 5 — Generation, fast-path and delivery

- **5.1** Reload shall leave completed selection (including empty decisions) and overlays unchanged; new eligible A-legs use new immutable rules; later model changes shall not rerun matching. Invalid candidates leave the previous generation usable.
- **5.2** Producer removal/generation retirement shall not deactivate persisted overlays or clear completion; generation resources follow existing pin/drain/close ownership, without new background work or a second cleanup authority.
- **5.3** An enabled producer shall occupy the existing conservative steering dependency before any overlay exists, forcing canonical fallback; persisted steering shall continue blocking the large-payload optimization after removal. Bootstrap shall execute once on that fallback.
- **5.4** Delivery shall include a config example and short guide explaining regex order, activation/ambiguity, frozen lifetime/reload, bounds, canonical fast-path fallback, provider visibility/model disclosure and explicit PTB capture exposure; implementation shall stay within the approved delivery budgets.

## Issue acceptance trace

| Issue AC | Spec references |
|---|---|
| 1 | 4.2 |
| 2 | 2.1 |
| 3 | 4.3 |
| 4 | 4.1 |
| 5 | 1.4, 1.5, 3.2, 4.2 |
| 6 | 3.1, 4.2 |
| 7 | 1.4, 3.1, 5.1 |
| 8 | 3.1, 3.2, 3.3 |
| 9 | 3.4 |
| 10 | 3.4, 3.5 |
| 11 | 5.1, 5.2 |
| 12 | 4.1 |
| 13 | 4.1 |
| 14 | 4.4 |
| 15 | 1.2, 1.3 |
| 16 | 4.5 |
| 17 | 5.3 |
| 18 | 4.4 |

## Deferred / out of scope

Dynamic per-turn model following; backend/route matching policy; raw provider JSON surgery; system-message substring editing/replacement; templates/interpolation; client-controlled policy; admin mutation; secrets injection; cache-policy changes; generic public planes/registries/session-opener widening; process caches for durable completion. No follow-up issues are created by task 1; proposing any deferred work requires separate authorization.
