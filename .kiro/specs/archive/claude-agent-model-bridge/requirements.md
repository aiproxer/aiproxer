# Requirements Document

## Project Description (Input)

Add an opt-in, local-only **Claude Agent SDK model connector** (`claudeagentmodel`) that serves inference from the operator's own native Claude Code login while arbitrary client harnesses keep their own prompts, tool definitions, permissions and tool-execution loops. The connector is distinct from the autonomous `claudeagentacp` agent runtime and from the `claude_code_hybrid` backend.

This spec covers the shippable consumer: real SDK text streaming (S1), the dynamic deferred tool-call bridge for the existing OpenAI Chat and Anthropic Messages frontends (S2), and cross-client continuation for OpenAI Responses ordered items and Gemini name-only `functionResponse` requests (S3). Release certification, durability across proxy restarts, and multi-user use remain deferred to follow-up slices of issue #822.

- **Who has the problem:** single-user operators who own a Claude subscription/login and want their existing coding harness to use it as a model backend without adopting a specific agent runtime or a static tool registry.
- **Current situation:** `claudeagentacp` exposes an autonomous ACP agent runtime; `claude_code_hybrid` is a CLI hybrid. Neither turns the client harness's own canonical tool inventory into a parked native SDK model turn.
- **What should change:** a disabled-by-default connector streams text from a pinned official Claude Agent SDK sidecar, then exposes exactly the admitted canonical tools through an ephemeral SDK-owned MCP server. The caller executes its own tool calls and returns the result in a subsequent HTTP request that releases the same parked native turn.

## V1 Slice

- **Delivers:** an experimental optional connector with strict local configuration, verified native login/runtime identity, runtime-discovered models, canonical text streaming, and a dynamic deferred tool bridge for OpenAI Chat, Anthropic Messages, OpenAI Responses ordered items, and Gemini name-only continuation (`tool_calls`/`tool_use`/`function_call`/`functionCall`, streaming and collected).
- **First consumer:** a single-user loopback AIProxer deployment whose operator enables the connector for a Chat-oriented or Anthropic-oriented harness; no client-specific mapping or configuration is added.
- **Safe degradation:** the connector stays inactive unless explicitly enabled; unsupported semantics produce named, bounded warnings or typed rejects before the affected output is committed. Missing login, stale runtime identity, ambiguous continuation, expired pending calls and discovery failures fail closed and never fabricate a model, tool call or history.

## Deferred

- Durable parked sessions across proxy restart, cross-node coordination, session migration/pooling, and warm parked processes — follow-up issue TBD.
- Multi-user subscription sharing/pooling, remote hosting, account rotation, and any non-local certification — follow-up issue TBD.
- Parallel native tool calls beyond one outstanding call per step, and general client/public MCP passthrough — follow-up issue TBD.
- Structured outputs, reasoning replay, vision/document inputs, native sampling controls, and coding of subscription economics — follow-up issue TBD.
- Broad public "subscription backend" claims and release certification of the exact installed artifact set — follow-up S4 after maintainer legal/policy sign-off; engineering enablement is not policy approval.
- Windows/macOS runtime certification; Linux/amd64 is the first certified profile.

## Boundary Context

- **In scope:** optional connector identity, strict configuration, credential isolation, runtime/model discovery, text streaming, dynamic per-step tool mapping, deferred MCP invocation, canonical tool-call events, exact-ID/preserved-ID continuation, OpenAI Responses ordered-item continuation with materialized previous-response proof, Gemini name-only continuation with unique-name/argument/prefix proof, parked-process lifecycle, disabled example and operator documentation.
- **Out of scope:** changes to `pkg/aipapi`, root module/provider SDK imports, new frontend×backend translators, autonomous agent subprocess semantics borrowed from `claudeagentacp`, native Claude builtin execution, and billing/money logic.
- **Adjacent expectations:** the host still owns routing, failover, canonical protocol decoding/encoding and B2BUA continuity. The connector is the sole owner of its sidecar process tree and can only answer the attempt it was given.
- **Boundary ownership:** backend plugin (`connectors/claudeagentmodel`), plus additive documentation/example and optional small helper under `connector-support/` only if direct sibling `internal` import duplication cannot be avoided.
- **Revalidation triggers:** canonical request/event/tool contracts, backend-plugin ABI, capability negotiation, frontend tool wire forms, process-tree isolation, or the pinned Claude Agent SDK/CLI behavior.

## Requirements

### Requirement 1: Opt-in posture, configuration and credential isolation

**Objective:** As a single-user operator, I want an explicitly enabled local connector that never exposes my Claude login, so that I can use my own subscription without widening the trust boundary.

#### Acceptance Criteria

1. While the connector factory is absent, unconfigured, or its example is disabled, the system shall not launch a sidecar, discover models, or reach a Claude endpoint.
2. When an operator configures the connector, the connector shall accept only the documented strict typed fields and defaults, and shall reject unknown keys, malformed values, non-absolute workspace paths, and out-of-range bounds before any process or network side effect.
3. If `admin_policy: honor` or a host-login credential source is selected, then the connector shall require explicit local single-user opt-in and shall reject any conflicting host policy before launch.
4. The connector shall source credentials only from the operator's existing Claude Code login or an explicitly configured secret, and shall never accept credential material from a client request, capture or forward subscription tokens, place secrets in argv/YAML/logs, or inherit a disallowed process environment.
5. Where the host runs in multi-user access mode, the connector shall fail closed unless a separately reviewed host-owned approval admits it.

### Requirement 2: Runtime-discovered models and honest text streaming

**Objective:** As a client harness, I want text responses from a real discovered Claude model with truthful identity and usage, so that my session stays continuous without invented compatibility.

#### Acceptance Criteria

1. When Configure succeeds, the connector shall resolve and list only exact runtime-discovered model IDs with honest capabilities, and shall report failed or stale discovery explicitly instead of inventing or silently substituting a model.
2. When a request names a model, the connector shall execute that exact discovered model or reject before upstream I/O, and shall surface any SDK fallback or `model_refusal_fallback` as a visible effective-model change rather than continue under the requested identity.
3. While serving an admitted plain-text request, the connector shall stream response start, message start, text deltas, usage presence, and a terminal response event, and a non-streaming response shall be the collection of the same events.
4. The connector shall preserve unknown-versus-zero usage and shall never invent cost, cache tokens, quota, or subscription entitlement.

### Requirement 3: Dynamic deferred tool bridge

**Objective:** As a client harness, I want my own tool definitions and execution loop preserved, so that Claude can request my tools while only my harness executes them.

#### Acceptance Criteria

1. When an admitted request includes tools, the connector shall validate each canonical definition (unique nonblank bounded names, bounded descriptions, structurally valid JSON schemas) and shall reject the whole request before upstream I/O on duplicates, oversized values, invalid schemas, or names it cannot represent unambiguously; it shall not repair schemas silently.
2. When a valid tool catalogue is admitted, the connector shall expose exactly those tools through a per-step ephemeral MCP server, preserve original names, descriptions and parameter schemas, use deterministic reversible native names, and reject any native-name collision.
3. While a step executes, the connector shall disable native SDK tools, agents, terminal and external MCP discovery, so only caller-admitted tools are invocable and a model attempt to run a native builtin has no filesystem or network effect.
4. If the SDK proposes more than one outstanding tool call in a step, then the connector shall fail explicitly and safely retire or roll back the parked turn rather than guess correlation.
5. When the SDK produces one complete, structurally valid tool call, the connector shall emit its native call ID with the original client name and exact JSON arguments, finish the tool-call event sequence, and end the HTTP response with a tool-use stop while keeping the native SDK turn parked.
6. When ToolChoice is none, the connector shall expose no tools; when a forced or required choice cannot be enforced, it shall reject under strict semantics or record an observable downgrade, never claim enforcement.
7. When the next request returns a result for the pending call, the connector shall release exactly one matching deferred invocation with the caller's exact result content where the canonical tool-result carrier represents it. The canonical carrier has no error-status field in this slice, so tool error status is neither propagated nor synthesized or inferred from content; the operator documentation states this explicitly. The connector shall never execute the tool itself, synthesize a result, or release the same pending call twice.

### Requirement 4: Continuation authority and bounded lifecycle

**Objective:** As an operator, I want parked turns bound to the right user and request, so that a tool result cannot resume another conversation or be replayed.

#### Acceptance Criteria

1. The connector shall bind a parked turn only to the configured instance, the host-authenticated local principal, and the host-validated proxy-owned session ID when present; client session headers, User-Agent and client IDs shall remain diagnostics only.
2. When a request continues a pending call without an authoritative session, the connector shall require the exact preserved opaque call ID, or for a name-only frontend exactly one outstanding pending call with that name and the exact argument digest, a unique pending candidate, and a matching ordered fingerprint of the visible prior transcript before releasing it.
3. If continuation evidence is missing, ambiguous, reordered, conflicting, expired or already consumed, then the connector shall refuse with a typed continuation error (`continuation_mismatch`, `ambiguous_continuation`, `continuation_expired`, `continuation_consumed`, or `history_unavailable` for an unresolved item reference or unavailable materialized parent) and shall not release or re-run the pending invocation.
4. While a turn is parked, the connector shall bind the request's model, route, generation and tool snapshot; a change shall produce an explicit rejection or retirement with history-replay disclosure, never a silent rebind to a different model or catalogue.
5. When an HTTP response stream closes after a committed tool-call boundary, the connector shall keep the parked native turn alive; explicit cancellation, client failure, idle timeout, pending TTL, shutdown and process death shall each retire and drain the owned process exactly once.

### Requirement 5: Honest degradation, isolation and operator surface

**Objective:** As a maintainer, I want unsupported behavior named and the optional connector isolated, so that V1 ships without hidden compatibility or core coupling.

#### Acceptance Criteria

1. The connector shall surface every unsupported semantic (instruction-role loss, forced tool choice, history replay, multimodal tool result, unsupported parallel call) as an explicit warning or typed error, and strict semantics shall reject instead.
2. The connector shall keep errors, diagnostics, stderr and process arguments bounded and secret-free, and shall not echo raw upstream error blobs, prompts, request payloads or credentials.
3. The connector shall remain an optional isolated module: the root module and essential backends shall not depend on it or the Node SDK, and existing `claudeagentacp` and `claude_code_hybrid` behavior shall be unchanged.
4. The connector's operator documentation and disabled example shall document opt-in, local single-user trust and credential posture, the supported Chat/Anthropic tool envelope, bounds, rollback, experimental status, and the maintainer legal/policy sign-off required before any broad subscription claim.

## Out-of-Scope Assumptions Worth Naming

- The connector runs as the local OS user and is not a sandbox. Native Claude builtins are disabled by settings, but the sidecar itself is trusted local code.
- Subscription use is personal and experimental; this spec does not authorize sharing accounts, publishing a broad subscription claim, or representing subscription usage as provider cost.
- `claudeagentacp` and `claude_code_hybrid` keep their own specifications, permission policies and release gates.
