# Requirements Document

## Project Description (Input)
Agent clients (Codex-style) send the model's earlier `reasoning` items back in Responses `input`. The frontend decodes each one into a canonical `PartReasoning`, which makes `reasoning_replay` a required hard capability — and no catalog provider profile can satisfy it, so any session whose history contains a reasoning item is rejected on every profile backend. Amend the archived reasoning-preservation requirement so that a candidate which cannot replay the dialect of client-sent reasoning applies a configured per-attempt policy — `log_skip` (strip those parts for that attempt) by default, `reject` (today's behaviour) for strict operators — reusing the existing downgrade seam and the existing `reject` | `log_skip` vocabulary instead of a second knob.

## Introduction
This spec amends `.kiro/specs/archive/openai-responses-reasoning-preservation/requirements.md`. That document's line 34 states that hard `reasoning_replay` capability semantics "shall remain non-downgradeable", while its sections 3.7 and 7.5 already approve an operator-configured `reject` / `log_skip` choice for unrepresentable reasoning dialects. That choice is currently enforced only for reasoning the proxy restores, not for reasoning the client sends. This spec extends the choice to client-sent reasoning and reverses line 34 for client-sent reasoning only: the standard distribution defaults to `log_skip`, and operators who need strict fidelity may set `reject`. The reversal is recorded here explicitly and must be carried into operator docs.

Reasoning items are hidden model state, not transcript. Omitting one asserts nothing false about what happened or in what order; the model simply receives less private context, and foreign reasoning (for example OpenAI `encrypted_content` sent to a DeepSeek backend) is unusable by the target backend anyway. Reordering, by contrast, tells the model a false sequence of events. This spec therefore permits omission under policy and never permits reorder.

## V1 Slice
- **Delivers**: Codex-style sessions with reasoning history stop failing on profile backends: a per-attempt `log_skip` strips unrepresentable client-sent reasoning parts for that candidate only, while lossless candidates still receive the exact items.
- **First consumer**: The core executor attempt path (`internal/core/runtime`), negotiating each candidate through the existing `lipapi.Negotiate` seam; no new execution path.
- **Safe degradation**: When the policy is `reject`, or when stripping cannot preserve order and identity of everything else, the attempt fails exactly as today (candidate refusal with existing failover semantics).

## Deferred
- Native per-profile reasoning replay for OpenAI-compatible backends (Track 2 of the handoff; blocked on the owner's live DeepSeek probe) — follow-up: per-profile opt-in `reasoning_replay` plus declared dialect, honoured by replay support instead of the prefix list.
- Adjacency refusal reclassification (Track 3; small and independent) — follow-up: refusal error satisfies `ErrResponsesAdjacencyRefused`, `ErrCapabilityReject`, and recoverable-pre-output; docs fix for `docs/provider-profiles.md:99`.
- Per-dialect strip granularity (strip only the unrepresentable dialect, keep representable ones) — follow-up: only if a real route mixes dialects per attempt.
- Metrics beyond the single skip counter and decision log — follow-up: dashboards or alerts on lossy routes.

## Boundary Context
- **In scope**: Policy vocabulary and default; per-attempt strip mechanics on the executor clone; unified rule with proxy-restored reasoning; skip observability; post-strip adjacency ordering for the `callA, reasoning, callB, outA, outB` shape.
- **Out of scope**: Changing what any backend can replay; pairwise translators or reasoning conversion; touching the shared call; reordering model-authored items; making `reasoning_replay` globally soft; model-ID heuristics.
- **Adjacent expectations**: The reasoning-preservation feature keeps owning proxy-restored reasoning under its existing `on_unrepresentable`; this slice owns client-sent reasoning under the new attempt policy, and both apply the same per-candidate rule. The adjacency quirk keeps owning post-strip sequence legality.
- **Boundary ownership**: Core owns the per-attempt policy decision and strip; `pkg/lipapi` owns the downgrade seam; frontend/backend adapters own unchanged encode/decode; docs own the line-34 reversal notice.
- **Revalidation triggers**: Capability negotiation, routing/executor attempt derivation, streaming/failover-after-output rules, secure-session or diagnostics posture if the decision log changes shape.

## Requirements

### Requirement 1: Single per-attempt policy for unrepresentable client reasoning
**Objective:** As an operator, I want one configured policy that decides what happens when an attempt candidate cannot replay client-sent reasoning, so that lossy routes work by default and strict routes stay strict.

#### Acceptance Criteria
1. When an attempt candidate cannot replay the dialect of client-sent reasoning items and the policy is `log_skip`, the proxy shall proceed with that candidate without those reasoning parts and shall record a downgrade decision for the attempt. **[1.1]**
2. When an attempt candidate cannot replay the dialect of client-sent reasoning items and the policy is `reject`, the proxy shall refuse that candidate exactly as it does today, preserving existing failover to the next candidate. **[1.2]**
3. The proxy shall default the policy to `log_skip` in the standard distribution, shall allow operators to set `reject`, and shall document this default as an explicit reversal of archived line 34 for client-sent reasoning. **[1.3]**
4. The proxy shall express the policy with the existing `reject` | `log_skip` vocabulary and shall not introduce a second, differently named knob for the same choice. **[1.4]**
5. When an attempt candidate can replay the dialect of client-sent reasoning items, the proxy shall deliver the exact items to that candidate losslessly regardless of the policy. **[1.5]**

### Requirement 2: Attempt isolation with byte-identical remainder
**Objective:** As a client on a multi-candidate route, I want the strip to affect only the incapable attempt, so that a later lossless candidate still sees my exact history.

#### Acceptance Criteria
1. When the policy strips reasoning for an attempt, the proxy shall apply the strip to the per-attempt clone only and shall never mutate the shared call, so a later candidate still negotiates against the exact items. **[2.1]**
2. When the policy strips reasoning for an attempt, the proxy shall leave every other item and its order byte-identical, removing only the unrepresentable reasoning parts. **[2.2]**
3. The proxy shall never reorder model-authored items as part of a strip or to satisfy sequence legality after a strip. **[2.3]**

### Requirement 3: One rule for restored and client-sent reasoning
**Objective:** As an operator, I want proxy-restored reasoning and client-sent reasoning to follow the same per-candidate rule, so that loss accounting is single and restores never fight strips.

#### Acceptance Criteria
1. When a candidate cannot replay a reasoning dialect under `log_skip`, the proxy shall apply the same strip-or-withhold outcome to proxy-restored reasoning and to client-sent reasoning for that attempt. **[3.1]**
2. When both restored and client-sent reasoning are unrepresentable for one attempt, the proxy shall record the downgrade once and shall not strip the same attempt twice. **[3.2]**
3. When the policy is `log_skip`, the proxy shall not restore reasoning into a candidate that cannot replay its dialect. **[3.3]**
4. When the policy is `reject`, the proxy shall preserve the existing restore-path `on_unrepresentable` behaviour for proxy-restored reasoning. **[3.4]**

### Requirement 4: Observable loss
**Objective:** As an operator, I want to see how lossy my routes are, so that silent degradation is impossible.

#### Acceptance Criteria
1. When the policy strips reasoning for an attempt, the proxy shall increment a bounded `reasoning_replay_skipped` counter labelled by backend. **[4.1]**
2. When the policy strips reasoning for an attempt, the proxy shall emit a content-safe decision log entry naming the candidate and the policy, without reasoning bodies or opaque envelopes. **[4.2]**
3. The proxy shall keep skip telemetry content-safe: no reasoning bodies, opaque envelopes, signatures, or session partitions in logs, metrics, or errors. **[4.3]**

### Requirement 5: Post-strip adjacency ordering
**Objective:** As a client sending tool-loop histories, I want the stripped sequence to pass the adjacency quirk when it is legal, so that the reasoning shapes that used to be rejected before negotiation now flow.

#### Acceptance Criteria
1. When a stripped attempt carries `callA, callB, outA, outB` order to a `deepseek-responses` candidate, the proxy shall emit it without adjacency refusal. **[5.1]**
2. When a stripped attempt still traps a user or assistant message between a later call and an earlier output, the proxy shall apply the existing adjacency refusal path unchanged. **[5.2]**
3. If a stripped sequence cannot satisfy adjacency legality without reordering, the proxy shall refuse the attempt rather than reorder it. **[5.3]**

## Requirements Review
Five areas and 18 criteria cover the slice: one policy knob with a recorded default reversal, attempt-isolated byte-identical strips, a unified restored/client-sent rule, observable loss, and post-strip adjacency behaviour. A pre-write review confirmed the handoff's six suggested points fold into these five areas (tests are task evidence, not requirements), the single-knob constraint forbids a second vocabulary, and omission-without-reorder is the governing invariant. Native replay, refusal reclassification, per-dialect granularity, and extra metrics are deferred.
