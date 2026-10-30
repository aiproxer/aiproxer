# Model-specific persistent system steering

`model-system-prompt` adds operator-owned, backend-only system instructions once per authoritative A-leg, using [conversation-view steering](conversation-view.md). It is disabled by default. The outer registration's `enabled` controls activation; `config` accepts only `rules`, with `id`, `model_pattern` and `append` fields. Unknown fields are rejected; do not put `enabled` inside `config`.

## Configuration

Merge this row into your existing `plugins.features` list (the tracked [`config.yaml`](../config.yaml) includes a disabled example):

```yaml
plugins:
  features:
    - id: model-system-prompt
      enabled: true
      config:
        rules:
          - id: concise
            model_pattern: '^gpt-4o(-mini)?$'
            append: 'Keep explanations concise and state uncertainty explicitly.'
          - id: mini-checks
            model_pattern: '^gpt-4o-mini$'
            append: 'Check assumptions before proposing a change.'
```

Patterns use Go `regexp` syntax and compile once before generation publication. Matching is unanchored unless you supply anchors. **Every** matching rule contributes one standalone canonical system message, in configuration order; this is not first-match-wins. Append bytes are preserved without trimming, interpolation or per-turn rendering. Existing system/developer instructions are not edited. Overlays use `stable_prefix` placement and `stable_prefix_fallback` anchor policy; canonical OpenAI-, Anthropic- and Gemini-family adapters convey them into their instruction representations, with unsupported required semantics rejected explicitly.

## Selection and lifetime

Selection runs after accepted submit/Secret Guard and authoritative A-leg binding, before the first eligible inference's normal view snapshot/backend Open. The matching identity is the accepted logical model after default-route/model-alias resolution and the frozen authoritative A-leg routing override—not a provider-native ID, chosen candidate or failover winner. All selector leaves are inspected: one distinct logical model is eligible; multiple distinct models freeze an empty `ambiguous_skip` decision. An unmarked A-leg with any authoritative B-leg allocation freezes an empty `preexisting_skip` decision, even without a completed attempt record. Claimed local-only turns do not bootstrap or allocate B-legs; their A-leg remains eligible at its first inference. Detached children use their own private A-leg.

The ordered overlays and producer completion commit atomically. Validation, collision, shared-capacity, cancellation or storage errors reject before backend Open, without partial overlays, completion, slots or revisions. Concurrent first turns reuse the winning committed decision. No-match and skip decisions freeze too: later model changes, reloads, feature disable/removal and generation retirement do not rematch or erase completion/overlays. New eligible A-legs use the newly published rules; invalid reloads leave the previous generation usable. Later turns, retries, failover and parallel attempts use the frozen conversation-view snapshot, with final pre-Open reassertion.

Memory retains state until authoritative A-leg retirement (not across process restart). SQLite/PostgreSQL retain it across reopen/restart/resume using the existing continuity database; forward migrations, EnsureSchema and database parity contracts cover both dialects. Durable coordination uses database transactions, not process-global locks. Authoritative A-leg retirement removes completion and overlays; generation retirement does not.

## Bounds and fast path

| Dimension | Limit |
|---|---|
| Configured rules | 64 |
| Derived overlay ID `model-system-prompt.<id>` | Unique ASCII `[_-.A-Za-z0-9]`, at most 128 bytes **including the prefix** (rule ID at most 108 bytes) |
| Each append | Valid UTF-8, nonempty/non-whitespace, no NUL, at most 64 KiB |
| Total configured append bytes | 256 KiB |
| Runtime active steering per A-leg | 64 overlays / 256 KiB total, shared with other producers |

Enabling this feature opts into conservative canonical fallback for large-body requests even before any overlay exists. Persisted steering continues blocking the wire optimization after producer removal. This is not raw-provider-JSON injection or a provider cache policy; stable structure does not guarantee cache hits.

## Visibility and exposure

The producer does not add steering to client ingress, CTP, continuation truth, structural client-turn records or frontend responses. PTB/backend-effective input includes it. **The remote provider/model receives the text and may quote or paraphrase it in output: hidden steering is not a secret. Never put credentials or sensitive material in `append`.** Protect persisted text using existing database/access controls.

Normal feature diagnostics are content-free: bounded attempted/matched/no-match/failed/ambiguous/preexisting/reused outcomes, counts and bounded logical-model evidence; no append text, client prompt contents, raw steering digests or unbounded model metric labels. Explicit plaintext PTB captures are different: they intentionally record model-visible input, including steering. Restrict their access/retention or leave plaintext capture disabled; ordinary log privacy is not a promise to redact an explicitly enabled PTB capture.
