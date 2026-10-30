# Research Notes

## Research Objective

Determine how the current Codex CLI preserves private reasoning and performs native compaction, then map those mechanisms onto the direct `openai-codex` connector without duplicating core orchestration or weakening surfaced-output ownership.

## Sources Reviewed

### OpenAI public material

- OpenAI, “How two settings tripled our ARC-AGI-3 scores.”
- OpenAI Responses API reasoning persistence guidance.
- OpenAI Responses compaction guidance.

### OpenAI Codex source

Reviewed current `openai/codex` source at commit:

`3016671bb077c43448b8fa88f3edfa9772e17058`

Primary files:

- `codex-rs/core/src/client.rs`
- `codex-rs/core/src/context_manager/history.rs`
- `codex-rs/core/src/session/turn.rs`
- `codex-rs/core/src/stream_events_utils.rs`
- `codex-rs/core/src/compact_remote_v2.rs`
- `codex-rs/core/src/compact_remote_v2_attempt.rs`

### Go-LIP source

Reviewed repository `main` after PR #235, including:

- `connectors/codex/internal/codex/payload.go`
- `connectors/codex/internal/codex/payload_input.go`
- `connectors/codex/internal/codex/stream.go`
- `connectors/codex/internal/codex/continuation.go`
- `internal/plugins/features/reasoningpreservation/*`
- `internal/reasoningreplay/eligible.go`
- `docs/reasoning-output-preservation.md`
- backend-plugin exact reasoning support introduced by PR #235.

## Finding 1: The ARC Result Is About Retained Reasoning Plus Compaction

OpenAI reports that retaining private reasoning and replacing rolling truncation with compaction increased GPT-5.6 Sol's ARC-AGI-3 score from 13.3% to 38.3% while materially reducing token use. The article does not provide a clean ablation isolating each setting.

Engineering implication:

- quality gains from the combination are plausible for long-horizon coding agents;
- the magnitude cannot be transferred directly to coding;
- reasoning continuity and compaction must be independently configurable and evaluated.

## Finding 2: Codex Always Requests Encrypted Reasoning

`ModelClient` builds every normal Responses request with:

- `reasoning: Some(...)`; and
- `include: ["reasoning.encrypted_content"]`.

This remains true when no explicit reasoning effort override is supplied; model/default reasoning controls still form a valid request.

Go-LIP contrast:

- the direct connector currently requests encrypted reasoning only when its `Reasoning` field is non-nil;
- that field is created only from explicit/default effort configuration;
- a continuity-enabled request with no explicit effort may therefore fail to capture exact state.

Design consequence:

- eligible continuity must drive the include/reasoning request shape;
- explicit user/route effort still wins;
- live endpoint compatibility must be tested.

## Finding 3: Codex Persists Exact Native Response Items

For each completed output item, Codex records the original `ResponseItem` into session history and rollout persistence. `ContextManager::record_items` accepts reasoning, messages, calls, outputs, web/search items, and compaction items.

Reasoning is not reduced to displayed summary text before persistence.

Design consequence:

- exact encrypted item replay is the durable semantic model;
- canonical exact reasoning parts are the correct Go-LIP carrier;
- visible assistant text alone is insufficient.

## Finding 4: Later Prompts Are Rebuilt From Native Item History

Before each sampling request, Codex clones history and calls `for_prompt`. Tool continuations and retries also rebuild from current history.

The effective loop is:

```text
completed reasoning item
  -> exact history record
  -> tool call and output record
  -> later prompt from exact history
  -> new reasoning item
```

Design consequence:

- the Go-LIP workflow must restore reasoning before connector payload construction;
- action-level ordering must be covered by tests;
- compaction planning must see the same restored exact history.

## Finding 5: `previous_response_id` Is Not Codex's Durable Cross-Turn Foundation

A `ModelClientSession` is created per Codex turn. It holds:

- a lazily reused WebSocket connection;
- the last request/response for incremental delta calculation;
- a turn-scoped sticky-routing token.

`previous_response_id` is used when a later request inside that session is an incremental extension. A fresh client session is created for the next Codex turn.

Design consequence:

- do not add automatic cross-turn HTTP response-ID chaining as the primary design;
- keep the existing Go-LIP WebSocket mechanism as an optimization;
- exact reasoning/compaction items plus full history remain the correctness baseline.

## Finding 6: Codex Compacts Reasoning-Complete History

Remote Compaction V2:

1. clones current native history;
2. trims tool outputs only as needed to fit;
3. normalizes the history for the model;
4. appends one `CompactionTrigger`;
5. streams a normal Responses request;
6. accepts one completed compaction checkpoint in the legacy streamed path; the current dedicated unary response returns the full replacement list;
7. builds replacement history;
8. installs it and recomputes token usage.

Because current history contains exact reasoning items, the compaction request sees private reasoning state.

Design consequence:

- Go-LIP must restore reasoning before compaction planning;
- a compaction request built from an unaugmented client transcript is semantically incomplete.

## Finding 7: Codex Retains More Than User Messages

Current V2 replacement retains, within a 64,000-token budget:

- user/developer/system messages;
- bounded non-final `AgentMessage` items;
- the new compaction item.

It excludes final-answer-style agent messages and does not redundantly retain reasoning/tool items represented by the opaque checkpoint.

Design consequence:

- revise the original user-only retained policy;
- implement a versioned/tested Codex-aligned predicate;
- cap retained agent messages individually.

## Finding 8: Codex Treats Encrypted Length as Model-Visible State, Not Base64 Text

Codex estimates reasoning and compaction item cost from encrypted payload length using a model-visible estimate rather than tokenizing the ciphertext string as ordinary prompt text.

Design consequence:

- Go-LIP estimator must not run ciphertext through the normal tokenizer;
- use provider usage when available, otherwise a conservative opaque-state estimate;
- keep estimated and provider-reported authority distinct.

## Finding 9: PR #235 Provides the Exact-Item Baseline

Merged PR #235 already:

- captures completed Codex reasoning items;
- canonicalizes exact OpenAI Responses reasoning envelopes;
- emits `EventReasoningPart`;
- transports exact reasoning through the backend-plugin ABI;
- replays compatible reasoning parts;
- retains completed compaction items;
- advertises exact Responses reasoning replay support for the direct HTTP connector.

Tasks must not reimplement this. They should characterize it, extend request shape, and integrate it with automatic continuity and compaction.

## Finding 10: Existing Reasoning Preservation Has Correct Winner Ownership

The `reasoning-output-preservation` feature observes final surfaced output after hooks/gates. Its attempt transform:

- resolves backend/model eligibility;
- partitions by authoritative session;
- snapshots bounded artifacts;
- classifies assistant turns;
- restores exact missing reasoning;
- checks backend replay support;
- excludes or skips according to policy.

This is superior to connector-local reasoning storage because the connector cannot know whether its attempt wins a race or is swallowed.

## Finding 11: Existing Placement Model Supports Action-Level Ordering

`PlacedReasoning` records reasoning before a given count of non-reasoning assistant parts. Classification compares exact placements. Restore can therefore preserve reasoning relative to text and structured call parts if the canonical assistant trajectory represents them together.

Remaining proof needed:

- reasoning before function call;
- multiple reasoning items;
- function output between assistant trajectories;
- reasoning after tool output;
- rollback/fork ambiguity.

## Finding 12: Codex Is Not Automatically Eligible Today

The standard reasoning-replay catalog:

- does not list `openai-codex` among automatic backend prefixes;
- caps automatic GPT matching at GPT 5.5;
- supports explicit backend-only rules without model keywords.

Preferred approach:

- use an explicit backend-only rule for the configured Codex instance;
- do not broaden the global GPT ceiling;
- use backend replay support to enforce exact dialect compatibility.

## Finding 13: A Continuity Marker Is Needed for Safe Compaction

The connector cannot infer whether the attempt transform ran or whether required reasoning restoration was eligible. A small internal call-extension marker can indicate:

- exact Codex continuity policy was eligible;
- restore/preserve classification completed according to policy;
- the connector may request encrypted reasoning and, in required mode, plan compaction.

Constraints:

- marker contains no session or payload data;
- marker is candidate-local;
- marker is consumed internally and never serialized upstream;
- absence causes compaction skip in required mode.

## Finding 14: Native Compaction Needs a Pre-Projection History View

The connector's normal no-tools path may convert prior structured calls/outputs to text to avoid protocol stalls. That projection is not suitable for compaction because it loses native action structure.

Design consequence:

- build an exact native history view from the post-restoration canonical call;
- validate call/output pairing independently;
- apply normal no-tools projection only to the normal request when needed;
- live-test structured history with empty current tool definitions.

## Quality Evaluation Design

Use a deterministic set of long-horizon agentic coding/repository tasks with fixed environment snapshots and tool implementations.

Modes:

| Mode | Reasoning continuity | Native compaction |
|---|---:|---:|
| Baseline | Off | Off |
| Reasoning only | On | Off |
| Compaction only | Off | On, evaluation-only best effort |
| Full native context | On | On, required |

Primary quality measures:

- task success and test pass rate;
- repeated or contradictory tool actions;
- rediscovery of established facts;
- invalidated earlier decisions;
- turns and tool calls to completion.

Efficiency measures:

- provider input/output/reasoning tokens;
- cache read/write;
- compaction cost;
- wall time and TTFT;
- serialized bytes;
- active context before/after;
- checkpoint reuse count;
- break-even turn.

Safety measures:

- restore ambiguity/conflict;
- checkpoint mismatch;
- fail-open/hard failure;
- auth/account rotation;
- ciphertext leakage scan.

## Finding 16: Default Context Budget Must Reserve Harness Headroom

The human-approved planning decision is an effective usable budget, not a
provider-limit claim. GPT-5.* planning must reserve headroom for the original
Codex harness system prompt/tooling, other agent harness tools, cross-harness
glue, and output/cancellation margin. The named policy is
`CodexHarnessHeadroomV1`.

- Exact catalog/model metadata wins for headline hard context and
  `auto_compact_token_limit` when present, but still passes the reserve and safe
  trigger checks.
- The exact `gpt-5.3-codex-spark` fallback is headline 128K, usable 96K, safe
  trigger 80K, with 32K reserved headroom. It must not be treated as 128K of
  conversation budget.
- Other GPT-5.x models without exact metadata use a conservative usable ceiling
  of 250K and a safe trigger of 220K, with 30K named planning headroom. This is a
  fallback usable ceiling, not a guessed headline context limit.
- Operator trigger overrides remain supported but must be validated below the
  usable ceiling and below hard context after retained-window/headroom checks.

These rules are deliberately conservative until exact catalog metadata is
available. They are planning behavior, not quality evidence.

## Resolved Design Questions

1. **Primary durable mechanism:** exact encrypted item replay.
2. **Response IDs:** optional WebSocket optimization only.
3. **Reasoning store owner:** existing surfaced-output feature.
4. **Compaction store owner:** direct Codex connector.
5. **Model eligibility:** explicit backend-only rule, no GPT ceiling change.
6. **Compaction prerequisite:** continuity marker required by default.
7. **Request include:** encrypted reasoning requested for every eligible attempt.
8. **Compaction history:** post-restoration exact native view.
9. **Retained predicate:** Codex-aligned and bounded.
10. **Rollout:** direct-Codex native context default-on with explicit backend,
    compaction, and reasoning-feature opt-outs; quality evidence remains
    reported without an unapproved claim of improvement.

## Open External Questions

## Finding 15: Current Codex Uses the Dedicated Unary Compact Endpoint

Authoritative current Codex source evidence is commit
`3aae5d885bac39c1262491aa3fd100dfd8b3919f`, especially
`codex-rs/core/src/client.rs` (`RESPONSES_COMPACT_ENDPOINT`, `CompactClient`, and
`compact_conversation_history`). The client posts to `/responses/compact`, sends
the compact input (`model`, `input`, `instructions`, and supported compact
controls), and treats the response as unary JSON rather than a streamed normal
`/responses` request. The OpenResponses contract at the local snapshot
`C:\Users\Mateusz\AppData\Local\Temp\opencode\or-upstream\openresponses-92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c\public\openapi\openapi.json`
defines the same `POST /responses/compact` response as `object=response.compaction`
with an output list containing retained `message` items followed by one compact
summary item. The live dedicated endpoint confirmed that summary item uses
`type=compaction_summary`, while the OpenResponses snapshot's item schema still
names the related provider envelope `type=compaction`; the live wire shape is the
authority for this connector. The response is unary JSON with top-level `id`,
`object`, `created_at`, `output`, and `usage`; it has no response status field.
The entire output list is the replacement history, not an opaque item to wrap in
the old `ReplacementBuilder`. SSE remains a compatibility parser only; the
connector does not send a compaction trigger to the dedicated endpoint.

Compatibility adaptation: accepted requirements continue to describe the
provider checkpoint as opaque and private, but the implementation now stores the
authoritative output list directly and replays its `compaction_summary` item
unchanged. The legacy streamed `compaction` trigger/result parser remains
isolated for compatibility and is not used for the dedicated unary result.

1. Does the ChatGPT Codex endpoint accept encrypted reasoning inclusion with no explicit effort in all supported models?
2. Which Codex turn metadata fields/headers are mandatory for direct V2 compaction?
3. Does structured historical tool state remain accepted when current tools are empty?
4. Is previous-model compaction supported on model switch through this direct connector?
5. How stable are retained-item constants across backend revisions?

All external questions are guarded by live tests, pre-output fallback behavior, explicit operator opt-out, and cooldown.
