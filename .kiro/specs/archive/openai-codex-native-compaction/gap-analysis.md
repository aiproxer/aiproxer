# Brownfield Gap Analysis

## Scope and Method

This review compares the revised native-context requirements against repository `main` after:

- PR #235, which added exact Codex reasoning/compaction output transport and backend-plugin replay capability;
- the existing `reasoning-output-preservation` feature and its full-stack validation;
- the current direct Codex HTTP/WebSocket continuation implementation; and
- current OpenAI Codex CLI source at commit `3016671bb077c43448b8fa88f3edfa9772e17058`.

The analysis treats exact reasoning continuity and native compaction as one quality-sensitive workflow while preserving their separate controls and evidence. The current approved scope is default-on for direct Codex, with explicit operator opt-out; this is not deferred promotion work.

## Current Assets

### Direct Codex connector

Reusable assets:

- exact Responses reasoning-item canonicalization and replay;
- completed compaction-item capture;
- `reasoning.encrypted_content` request support when a reasoning object is present;
- static and managed OAuth account execution;
- HTTP/SSE and experimental WebSocket transports;
- stable prompt-cache identity;
- WebSocket continuation store with exact input fingerprints and `previous_response_id`;
- model catalog with context windows;
- local token estimation and provider usage mapping.

### Reasoning-preservation feature

Reusable assets:

- surfaced-winner observation after hooks and completion gates;
- authoritative-session partitioning;
- exact assistant-anchor matching;
- ordered `PlacedReasoning` positions relative to non-reasoning parts;
- ambiguity/conflict/state policies;
- exact dialect capability checks;
- bounded TTL/turn/byte state;
- attempt-transform restoration before backend open;
- deterministic, random, and soak validation.

### Canonical and plugin contracts

Reusable assets:

- `PartReasoning` and `ReasoningPart`;
- exact dialect `openai.responses.reasoning_item.v1`;
- `EventReasoningPart`;
- backend-plugin ABI carriage added by PR #235;
- attempt metadata containing backend identity, model, authoritative session, and replay support.

## Requirement-to-Asset Map

| Requirement area | Existing asset | Gap | Classification |
|---|---|---|---|
| 1 Configuration | Codex typed YAML and feature config | No coordinated native-context mode; compaction defaults not implemented | Missing |
| 2 Always request encrypted reasoning | Payload include exists only when `Reasoning != nil` | Continuity-enabled requests without explicit effort may omit encrypted reasoning | Missing |
| 2 Exact item fidelity | PR #235 | Core codec/replay largely complete; compaction validation still needs hardening | Partial |
| 3 Surfaced reasoning continuity | reasoning-preservation observer/transform | Codex is not automatically eligible under current catalog; no continuity marker contract | Partial |
| 3 Winner ownership | final-stream observer | Connector-local capture would be unsafe; existing feature is correct owner | Constraint |
| 4 Action ordering | placed reasoning positions and Codex payload input expansion | Must prove reasoning around function calls/outputs and preserve exact compaction history before no-tools projection | Partial |
| 5 Model metadata | context windows parsed | `auto_compact_token_limit` and `comp_hash` are dropped | Missing |
| 5 Planning | WebSocket fingerprints/token estimator | No reasoning-aware compaction planner or safe trajectory split | Missing |
| 6 V2 compaction | connector Responses stream infrastructure | No trigger item, strict collector, retained predicate, or installation workflow | Missing |
| 7 Checkpoint store | WebSocket continuation TTL/LRU pattern | No separate account/model/static-shape-bound compaction store | Missing |
| 8 Response chain | WebSocket `previous_response_id` | Correct as optimization; must not be promoted to durable cross-turn authority | Constraint |
| 9 Usage/privacy | provider usage and diagnostics infrastructure | No internal compaction usage aggregation or continuity/compaction metrics | Partial |
| 10 Quality evidence | reasoning E2E harness and testkit | No four-mode Codex evaluation or coding-oriented quality measures | Missing |

## Brownfield Findings That Changed the Original Requirements

### Finding 1: Exact codec support is not automatic continuity

PR #235 allows exact reasoning items to cross the connector boundary, but a later request only replays them when they are already present in the canonical call. Many agent clients resend visible assistant/tool history without encrypted reasoning. Requirements were amended to make surfaced-response observation and automatic restoration explicit.

### Finding 2: Connector-local reasoning storage is the wrong owner

A connector does not know whether its B-leg response becomes the surfaced winner after failover, parallel races, response hooks, or completion gates. Capturing reasoning inside the connector could persist losing or swallowed reasoning. Requirements now mandate the existing final-stream reasoning-preservation feature as the authoritative cross-request store.

### Finding 3: Codex CLI does not rely on cross-turn response IDs

Current Codex CLI creates a fresh `ModelClientSession` per Codex turn. Its WebSocket `previous_response_id` reuse is mainly an incremental within-turn transport optimization. Durable cross-turn behavior comes from exact `ResponseItem` history. Requirements now make exact encrypted item replay the correctness baseline and explicitly exclude new automatic HTTP response-ID chaining.

### Finding 4: Compaction must consume restored native history

Running compaction directly on the client transcript can omit private reasoning even though restorable artifacts exist. Requirements now require attempt-transform restoration and an eligibility marker before required-continuity compaction.

### Finding 5: Ordinary no-tools projection is unsuitable for compaction

The direct connector intentionally converts historical tool records to display text when no current tool schema exists. Native compaction needs the exact structured trajectory. Requirements now separate the compaction history view from normal-request projection.

### Finding 6: Quality evidence must be binding

The original spec mentioned task-quality comparison only in research notes. The revised requirements make a four-mode quality evaluation and coding-oriented metrics release criteria.

## Implementation Options

### Option A: Connector-Local Reasoning and Compaction Store

**Approach**

- capture reasoning and compaction items in the connector;
- match later client history locally;
- compact and replay from connector-owned state.

**Advantages**

- one configuration block;
- minimal changes outside the connector.

**Disadvantages**

- cannot reliably distinguish surfaced winners from losing/swallowed B-legs;
- duplicates mature reasoning-preservation matching, policies, and bounds;
- risks divergence between reasoning-only and compaction history;
- weakens failover/parallel correctness.

**Disposition:** Rejected.

### Option B: Extend Only the Global Reasoning-Preservation Feature

**Approach**

- make Codex eligible;
- restore reasoning into canonical calls;
- leave compaction for a future generic spec.

**Advantages**

- correct surfaced-response ownership;
- low connector state complexity;
- immediate potential quality benefit.

**Disadvantages**

- does not deliver native compaction or checkpoint reuse;
- cannot test the two-setting combination described by OpenAI;
- leaves context-window benefits unresolved.

**Disposition:** Viable subset, insufficient for requested scope.

### Option C: Hybrid Feature/Connector Design

**Approach**

- reasoning-preservation owns surfaced exact reasoning observation and restoration;
- a small internal continuity marker proves eligible restoration ran;
- direct Codex always requests encrypted reasoning for marked attempts;
- connector builds an exact native history view after restoration;
- connector owns provider-specific V2 compaction and checkpoint state;
- WebSocket response IDs remain optional transport optimization.

**Advantages**

- preserves winner ownership and existing policy;
- closest match to Codex CLI durable item history;
- keeps provider-specific compaction at the adapter edge;
- independently configurable and testable;
- supports four-mode evaluation.

**Disadvantages**

- spans root feature and optional connector modules;
- requires a narrow shared extension-key contract;
- needs careful cross-module integration tests.

**Disposition:** Preferred.

## Required Design Decisions

1. **Continuity ownership:** `reasoning-output-preservation` is authoritative for automatic cross-request reasoning state.
2. **Compaction ownership:** direct `openai-codex` connector owns V2 trigger, collection, retained policy, checkpoint store, and request rewrite.
3. **Eligibility:** standard runtime composition installs a backend-specific direct-Codex reasoning-preservation rule by default; do not widen the global GPT model ceiling or use a broad GPT matcher.
4. **Continuity proof:** the attempt transform sets a bounded internal call-extension marker only for an eligible exact-replay candidate.
5. **Request shape:** continuity-marked Codex attempts always request encrypted reasoning and use model-supported/default reasoning controls.
6. **Planning order:** restore reasoning → build exact native history → rewrite existing checkpoint → decide compaction → normal transport.
7. **Response IDs:** keep existing WebSocket continuation as optimization; no new cross-turn HTTP chain.
8. **Safety:** full reasoning-complete history is authoritative fallback; required-continuity compaction skips without the marker.
9. **Quality:** four-mode evaluation remains required evidence and must distinguish observation from inference; it does not gate the already-approved default-on policy.
10. **Context budget:** use `CodexHarnessHeadroomV1`: exact catalog metadata first; Spark fallback 128K headline/96K usable/80K trigger; other GPT-5.x fallback 250K usable/220K trigger; validate explicit overrides below usable ceilings.

## Research Needed During Implementation

- Live ChatGPT Codex acceptance of a reasoning object with no explicit effort but encrypted-content inclusion.
- Live acceptance of structured historical function calls/outputs when the current request advertises no tools.
- Exact required Codex metadata/header parity for V2 compaction.
- Whether current backend compaction retained-agent predicate should be mirrored exactly or version-gated.
- Whether model-switch previous-model compaction is accepted through the direct endpoint.

Each unknown has deterministic fail-open behavior and an environment-gated live test.

## Complexity and Risk

- **Effort: XL** — multiple modules, stateful provider workflow, feature/connector integration, transport/account matrix, and quality harness.
- **Risk: High** — opaque provider-bound state, live endpoint behavior, concurrency, winner ownership, and quality claims.
- **Risk controls:** explicit operator opt-out, explicit reasoning rule, continuity marker, process-local bounded state, exact prefix checks, live gates, cooldown, full-history fallback, and conservative model-aware headroom.

## Requirements Gap Review Result

The requirements were corrected to cover:

- automatic surfaced reasoning retention;
- unconditional encrypted-reasoning request under eligible continuity;
- action-level reasoning/tool ordering;
- exact native compaction history;
- explicit response-ID scope;
- companion feature configuration;
- four-mode quality evaluation; and
- already-implemented PR #235 work as a brownfield baseline rather than future tasks.

No unresolved requirement gap remains. Proceed to design with the hybrid approach.
