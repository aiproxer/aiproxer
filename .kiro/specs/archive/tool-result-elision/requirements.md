# Requirements Document

## Project Description (Input)
Coding agents replay their whole conversation on every turn, and tool output dominates that history: the same file read, test log or command output is often returned several times, and every copy is resent to the model on every later turn. Operators pay for those bytes in context window, uncached input, and prompt-cache writes. Issue #468 asks for conservative, backend-only hygiene of historical tool results; issue #821 is the umbrella for proxy-level compression. This spec delivers the first slice: an opt-in proxy feature that replaces later, byte-identical copies of an earlier tool result with a short marker in the request sent to the model, without changing what the client sees and without destabilizing the provider prompt cache.

## Introduction
The client-visible conversation (A-leg) stays authoritative and untouched. Only the model-bound request (B-leg) changes, and only for content that carries no new information: a tool result identical to an earlier result of the same tool that is still present in the same request. The replacement is lossless, because the marker names the earlier call whose result the model can still read.

Every elision decision is a deterministic function of the conversation history itself, so the same history always produces the same model-bound bytes. Once a result is elided on an append-only conversation it stays elided with identical marker text on every later turn, without stored state. New elisions are applied in batches so the provider prompt cache is invalidated rarely, not on every turn.

## V1 Slice
- **Delivers**: Fewer model-bound bytes for long coding sessions with repeated tool output, with lossless markers, unchanged client transcripts, and at most one prompt-cache discontinuity per applied batch.
- **First consumer**: Agent harnesses replaying full history through any frontend whose canonical request carries tool results (Chat Completions, Anthropic Messages, Responses with full input), with the feature row enabled.
- **Safe degradation**: Whenever the feature cannot decide safely (unresolvable tool call, non-text result, exact-replay item, internal error), the affected result, or the whole request on error, is forwarded exactly as it would be without the feature.

## Deferred
- Uneventful-result elision (empty or "no match" output) — follow-up: needs trustworthy per-tool classifiers; empty results save almost no bytes, and recognising non-empty "no match" output from free text is guesswork.
- Superseded reads (older read of a resource made stale by a later read) — follow-up: needs a resource-identity and intervening-write contract.
- Truncation or summarization of old oversized results, and a per-pass maximum-coverage cap — follow-up: only meaningful for lossy classes; lossless duplicate elision removes no information.
- Lossless retrieve tool for elided content — follow-up: blocked by the exclusive control-tool plane held by Agent Loop Guard; needs its own SDK spec.
- Publishing a `tool-result-elision` structural cause and digest feed to the prompt-cache collapse diagnostic — follow-up: the diagnostic's seam-digest and structural-cause inputs have no production publisher yet; wire them first.
- Prometheus dashboards, alerts, or per-session savings in admin read models — follow-up: after V1 counters show real usage.
- Large-payload wire fast-path parity — follow-up: with the feature enabled, large requests take the canonical path.
- Image and multimodal result downscaling — follow-up: separate measurement first.

## Boundary Context
- **In scope**: Byte-identical duplicate tool results in the model-bound request; protected recent rounds; batch hysteresis; deterministic marker text; opt-in configuration; bounded counters.
- **Out of scope**: The client-visible transcript, continuation records, persisted session state, tool calls and their arguments, reasoning, compaction or extension items, user/system/developer/assistant text, routing, billing rules, and any provider-specific branching.
- **Adjacent expectations**: Conversation-view steering and exclusions keep their placement (late content rewrites are carried by the existing structural lineage proof). Context-window preflight and billing estimates see the reduced request because the feature runs before them. The prompt-cache collapse diagnostic already attributes a configuration change. Retiring a delayed-result barrier can also admit batches; specific structural-cause publication remains deferred as listed above.
- **Boundary ownership**: Feature plugin owns detection, planning, markers and counters; core owns stage order, request validation and conversation-view reassertion; composition owns registration and counter export.
- **Revalidation triggers**: Conversation-view projection or reassertion changes; request-transform stage order; canonical tool-result representation in `pkg/lipapi`; large-payload eligibility rules.

## Requirements

### Requirement 1: Opt-in, backend-only operation
**Objective:** As an operator, I want tool-result elision to be off unless I enable it and to affect only what the model receives, so that enabling it can never change what my users see.

#### Acceptance Criteria
1. The proxy shall keep tool-result elision disabled unless its feature row is explicitly enabled.
2. While tool-result elision is disabled, the proxy shall send model-bound requests that are byte-identical to the requests it sends without the feature installed.
3. When tool-result elision changes a model-bound request, the proxy shall leave the client-visible conversation, the client response, and stored continuation records unchanged.
4. If the feature configuration is invalid, then the proxy shall refuse the configuration at load or reload time with a validation error naming the invalid setting.

### Requirement 2: Lossless elision of duplicate tool results
**Objective:** As an operator, I want later identical copies of a tool result replaced by a reference to the earlier copy, so that the model receives the same information in fewer bytes.

#### Acceptance Criteria
1. When a tool result in the model-bound request is byte-identical in payload to an earlier result of the same tool in the same request, is at least the configured minimum size, and is eligible for elision, the proxy shall replace that later result's payload with a marker that names the earlier tool call and the original payload size.
2. The proxy shall keep the earliest occurrence of each identical result verbatim.
3. When the proxy replaces a result payload, the proxy shall keep the result's position, its tool-call association, its tool name, its identifiers, and every other item in the request unchanged.
4. If a tool result's originating tool call cannot be identified, its payload contains non-text content, it carries a provider item identifier, or another item references it, then the proxy shall forward that result unchanged.
5. The proxy shall produce marker text that depends only on the earlier call identifier and the original payload size, and shall forward a result unchanged when that identifier exceeds the marker's bounded length.

### Requirement 3: Protected recency and batched application
Eligibility clarification (unchanged numbered criteria): batching uses the stable result prefix strictly before the first protected-origin-round result. Every result, including unique/first/ineligible results, establishes that barrier. The suffix is safety-ineligible until the barrier advances; its savings are not pending. Within the prefix all otherwise eligible duplicates participate in immediate, whole threshold batches. This conservative eligibility preserves 4.1 for delayed/parallel results without state; it is not permission to drop arbitrary pending candidates.

**Objective:** As an operator, I want recent tool output left intact and elisions applied in batches, so that the model sees fresh results verbatim and the provider prompt cache is invalidated rarely.

#### Acceptance Criteria
1. The proxy shall forward verbatim every tool result produced by the most recent configured number of assistant tool-call rounds.
2. While the total size saved by eligible duplicates not yet elided is below the configured minimum batch size, the proxy shall forward those duplicates verbatim.
3. When eligible duplicates reach the configured minimum batch size, the proxy shall elide them together in the same request.
4. Where steering is placed after a message whose tool-result payload is elided, the proxy shall keep that steering at the same conversation position.

### Requirement 4: Prompt-cache-stable decisions
**Objective:** As an operator, I want elision decisions to be stable across turns, restarts and instances, so that elision never causes repeated prompt-cache churn.

#### Acceptance Criteria
1. When a later turn extends a conversation by appending messages, the proxy shall forward every previously elided result with the same marker bytes and every previously verbatim earlier content with the same bytes, except for results newly elided by a batch.
2. The proxy shall make the same elision decisions for the same conversation history and configuration regardless of process restarts, the serving proxy instance, or the selected backend.
3. When the client rewrites or truncates earlier conversation history, the proxy shall derive elision decisions from the rewritten history alone.
4. When the operator changes the feature configuration, the proxy shall apply the new configuration only to requests admitted under the new configuration generation.

### Requirement 5: Observability and safe failure
**Objective:** As an operator, I want to see what elision does and be sure it fails safe, so that I can measure savings without risking request failures.

#### Acceptance Criteria
1. When tool-result elision processes a request, the proxy shall count results considered, results elided, results skipped as exact-replay, and estimated tokens saved.
2. The proxy shall record elision counters and diagnostics without tool output, tool names, call identifiers, session identifiers, or other request content.
3. If tool-result elision fails while processing a request, then the proxy shall forward that request without any elision and shall not fail the request.
4. When tool-result elision reduces a request, the proxy shall apply context-window and billing pre-dispatch checks to the reduced request.
