# Requirements Document

## Project Description (Input)
Detect backend model calls to tools that do not exist and correct them with hidden steering before the invalid call reaches the agent client. Source: GitHub issue [#997](https://github.com/matdev83/go-llm-interactive-proxy/issues/997) and its research notes; owner decisions of 2026-10-12: the feature is opt-in, the correction budget gets a sane default, and V1 covers turns that are only tool calls.

- **Who has the problem:** operators who run agent harnesses (coding agents, tool-using assistants) through AIProxer, mostly against cheaper or weaker models that often invent tool names.
- **Current situation:** a call to a tool the client never offered goes straight to the agent. Tool-call repair fixes only near-miss spellings with exactly one normalized match. Every other unknown name reaches the client, wastes a full agent round trip, and leaves the invented name in the client's context, where it keeps misleading the model in later turns.
- **What should change:** when enabled, the proxy recognises a completed tool call whose name is not in the tool list the model was offered. It withholds the call, asks the same backend once more with a hidden correction message that lists the real tool names, and gives the client only the corrected output. It is a user-experience aid, not a policy: whenever correction is unsafe, impossible, or already tried, the original call goes through unchanged, so harnesses that load tools on demand after an unknown-tool error keep working.

## Introduction
This slice adds one opt-in feature plugin, one additive tool-call finalizer outcome in the public SDK, and the core glue that withholds the invalid call and reuses the existing hidden continuation path. Routing, failover, tool-call repair, Agent Loop Guard, frontends, and backends keep their current behaviour.

## V1 Slice
- **Delivers**: with the feature enabled, a backend attempt whose first client-visible output is a call to an unknown tool is replaced by one hidden corrected attempt. The client sees one response containing only the corrected output.
- **First consumer**: any agent harness that calls the proxy through a continuation-capable client protocol (OpenAI Chat Completions, OpenAI Responses, OpenResponses, Anthropic Messages, Gemini) with the `tool-name-correction` feature enabled.
- **Safe degradation**: every case the slice cannot handle safely (visible text already produced, budget spent, name already seen in the conversation, unsupported protocol, hidden steering unavailable, follow-up cannot start, too much later output to hold) delivers the original attempt's output unchanged and in order, exactly as today.

## Deferred
- Correcting turns where assistant text was already streamed before the invalid call; this needs a follow-up attempt that knows the already-delivered text. Follow-up: [feature backlog #997](https://github.com/matdev83/go-llm-interactive-proxy/issues/997)
- Cancelling the original attempt as soon as the invalid call completes, instead of letting it finish, to save output tokens and latency. Follow-up: [feature backlog #997](https://github.com/matdev83/go-llm-interactive-proxy/issues/997)
- Correcting a batch where a valid tool call was already delivered before the invalid one. Follow-up: [feature backlog #997](https://github.com/matdev83/go-llm-interactive-proxy/issues/997)
- Correcting calls to tools that exist but are excluded by the request's allowed-tools subset or forced tool choice. Follow-up: [feature backlog #997](https://github.com/matdev83/go-llm-interactive-proxy/issues/997)
- Correcting calls whose name is valid but whose arguments tool-call repair could not fix (same mechanism, different trigger). Follow-up: [feature backlog #997](https://github.com/matdev83/go-llm-interactive-proxy/issues/997)
- Ranking the closest tool names first in the correction message. Follow-up: [feature backlog #997](https://github.com/matdev83/go-llm-interactive-proxy/issues/997)
- Tool calls emitted by a model on a request that offered no tools. Follow-up: [feature backlog #997](https://github.com/matdev83/go-llm-interactive-proxy/issues/997)
- Parity on the large-body/wire execution path, metrics beyond the decision diagnostic, and enabling the feature by default in `lipstd`. Follow-up: [feature backlog #997](https://github.com/matdev83/go-llm-interactive-proxy/issues/997)

## Boundary Context
- **In scope**: detecting completed calls to unknown tool names; withholding the call and the rest of that backend attempt; one hidden follow-up request with a correction message; delivering the follow-up as the same client response; the pass-through rules; the correction budget; feature configuration and a decision diagnostic.
- **Out of scope**: changing how tool-call repair normalizes names or arguments; changing Agent Loop Guard decisions; changing routing, failover, or pre-output retry; changing frontend or backend wire behaviour; persisting anything across turns or restarts.
- **Adjacent expectations**: tool-call repair, when enabled, runs first, so a near-miss name it fixes is never treated as invalid. Agent Loop Guard, when enabled, still evaluates the finish of the corrected attempt as it would any other attempt. Harnesses that load tools on demand keep receiving the unknown-tool call once the correction budget is spent or the name already appears in their history.
- **Boundary ownership**: feature plugin (`tool-name-correction`) owns detection policy and the correction message; the SDK gains one additive finalizer outcome; core owns eligibility, withholding, budget, and the hidden continuation.
- **Revalidation triggers**: B2BUA continuation and output commitment (routing/streaming); the tool-call finalizer contract (`pkg/lipsdk/toolcall`); the conversation-view steering contract.

## Requirements

### Requirement 1: Opt-in activation and detection
**Objective:** As an operator, I want to enable invalid-tool detection explicitly, so that existing deployments keep their exact behaviour unless I opt in.

#### Acceptance Criteria
1. Where the tool name correction feature is not configured or is disabled, the proxy shall deliver every tool call exactly as it does without the feature.
2. While the feature is enabled, when a backend model completes a tool call whose name is not in the tool list offered to that model for the turn, the proxy shall classify the call as an invalid tool call.
3. While the feature is enabled, when a completed tool call names an offered tool (including a name that tool-call repair already corrected to an offered tool) or a name matching an operator-configured pass-through pattern, the proxy shall deliver the call unchanged.
4. If the client request offers no tools, then the proxy shall deliver tool calls unchanged.
5. If the feature configuration contains an out-of-range correction budget or a malformed pass-through name pattern, then the proxy shall reject the configuration at load time with an error naming the offending field.

### Requirement 2: Hidden correction
**Objective:** As an agent harness user, I want an invented tool call replaced by a corrected one before my agent sees it, so that my agent's context never contains the invented name.

#### Acceptance Criteria
1. When an invalid tool call is eligible for correction, the proxy shall withhold that call and all later output of the same backend attempt from the client until the follow-up decision is made.
2. When an invalid tool call is withheld, the proxy shall send one follow-up backend request made of the client's original request plus a correction message that the client never receives.
3. The proxy shall deliver the follow-up attempt's output as the continuation of the same client response, with one response start and one response finish, for both streaming and non-streaming client requests.
4. The proxy shall never deliver the withheld call's name, its arguments, or the correction message to the client.
5. When the client response ends, the proxy shall remove the correction message, so that it is absent from later turns of the conversation.

### Requirement 3: Pass-through safety rails
**Objective:** As an operator, I want correction to step aside whenever it could loop, duplicate output, or break a harness, so that the feature can never make a session worse than without it.

#### Acceptance Criteria
1. The proxy shall perform at most the configured number of corrections per client turn, with a default of one and an allowed range of one to three.
2. If the correction budget of the current client turn is spent, then the proxy shall deliver further invalid tool calls unchanged.
3. If the current backend attempt already produced assistant text, media, a decision result, or another tool call before the invalid call, whether or not that output has reached the client yet, then the proxy shall deliver the invalid tool call unchanged.
4. Where the operator disabled correction after reasoning, if the current backend attempt already produced reasoning output before the invalid call, then the proxy shall deliver the invalid tool call unchanged.
5. If the conversation history in the client request already contains a tool call with the same name, then the proxy shall deliver the invalid tool call unchanged.
6. If the request cannot carry a hidden continued response, because its client protocol does not support one, completion gates buffer the response, or hidden steering is unavailable, then the proxy shall deliver the invalid tool call unchanged.
7. If the follow-up backend request cannot be started, or the original attempt produces more output after the invalid call than the proxy can hold, then the proxy shall deliver the withheld call and the rest of the original attempt's output unchanged and in order, and end the response as the original attempt would have ended it.

### Requirement 4: Correction message content
**Objective:** As an operator, I want the hidden message to steer the model precisely and safely, so that correction works and cannot be abused through model-produced text.

#### Acceptance Criteria
1. The correction message shall state that the named tool does not exist and shall instruct the model to call one of the available tools or to answer without calling a tool.
2. The correction message shall list available tool names from the offered tool list, without descriptions or schemas, within 4096 bytes in total, ending with a count of omitted names when the list is truncated.
3. The correction message shall quote the invalid tool name only after reducing it to at most 64 printable characters.

### Requirement 5: Operator visibility
**Objective:** As an operator, I want to see what the feature decided, so that I can judge whether enabling it helps my models.

#### Acceptance Criteria
1. When the proxy decides on an invalid tool call, and again when an accepted correction later falls back to delivering the original output, the proxy shall record one diagnostic entry whose bounded reason code says whether the call was corrected or delivered unchanged, and why.
2. The diagnostic entry shall never contain tool arguments, the correction message, or the raw invalid tool name.
