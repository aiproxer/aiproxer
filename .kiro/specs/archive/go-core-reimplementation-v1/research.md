# Research Notes: go-core-reimplementation-v1

## Purpose

Capture the reasoning inputs used to generate this spec, summarize what was learned from the current Python LIP repository, and record the external references that shaped the Go-first design.

This file is intentionally background-oriented. The implementation contract lives in `requirements.md`, `design.md`, and `tasks.md`.

## Research inputs

### 1. Kiro / cc-sdd workflow shape

Key observations from the current Kiro-inspired workflow and cc-sdd references:

- The spec workflow is explicitly phase-based: init -> requirements -> design -> tasks -> implementation.
- Specs are treated as **contracts between bounded parts of the system**, not as giant “write all code from this document” prompts.
- The newer cc-sdd design template explicitly emphasizes:
  - boundary-first design,
  - a **File Structure Plan**,
  - tasks that can carry `_Boundary:` and `_Depends:` annotations.
- The LIP repository’s Kiro guidance also emphasizes:
  - spec-first for complex architectural work,
  - TDD,
  - steering as project memory,
  - traceability from tasks back to requirements.

### 2. Current Python LIP product direction

The current repository still clearly values:

- multiple client-facing protocol surfaces,
- multiple backend families,
- cross-protocol flexibility,
- dynamic routing and failover,
- B2BUA-style session handling,
- tool-call-reactor seams,
- capability-driven plugin boundaries,
- and strong debugging / capture / observability posture.

That means the rewrite should preserve the product’s intent but replace the architectural shape.

### 3. Specific current-LIP behaviors worth preserving

#### Routing and failover

The current docs and README show a routing selector model with:

- `backend:model`
- ordered failover using `|`
- weighted routing using `^`
- and a session-aware `[first]` override for weighted selectors

The README also documents an important runtime property:

- if a weighted branch fails **before meaningful output starts**, the request can re-roll within the same logical request using remaining weighted leaves.

This is a distinctive execution behavior and should stay core-owned.

#### Failure handling semantics

The current user-facing failure-handling documentation is explicit about:

- waiting and retrying short pre-output errors,
- immediate failover when wait is too long or another backend is available,
- surfacing errors when no safe recovery is possible,
- **never** silently recovering after content has started,
- emitting streaming keepalive comments during silent wait periods.

These semantics are directly relevant to the Go execution engine.

#### B2BUA session lineage

The current Python codebase already models B2BUA as:

- a core-owned A-leg continuity identity,
- attempt-scoped B-leg session identifiers,
- an attempt record store,
- and session resolution logic.

The Go rewrite should keep the explicit lineage model while simplifying the implementation.

#### Tool call reactor seams

The current tool-call-reactor subsystem documents several good lessons:

- avoid global mutable stream state,
- inject collaborators instead of reading runtime globals,
- keep the reactor path typed and fail-open by default,
- and separate orchestration from specific policy handlers.

The Go design keeps those lessons, but reserves only the hook surfaces in v1.

### 4. Why the Go rewrite should not copy the Python architecture

The Python repository has already moved toward typed boundaries and capability declarations, but it still carries a lot of coupling pressure from historical growth. The rewrite should not port that structure. Instead it should:

- reduce the core to canonical model + execution engine + routing/B2BUA,
- move all protocol behavior into plugins,
- keep future advanced behaviors behind hook APIs,
- and use the current Python repo mainly as a **behavior oracle** and **fixture source**.

## Design conclusions

### Conclusion A: Three classes of ownership are required

To reconcile “small core” with “routing/B2BUA must stay distinctive,” the design uses three ownership classes:

1. **Core-owned semantics**
   - canonical call / event model
   - capability negotiation
   - routing and failover
   - B2BUA lineage
   - diagnostics

2. **Protocol plugins**
   - frontends
   - backends

3. **Feature-hook plugins**
   - submit hooks
   - request/response part altering
   - tool-call reactors

This prevents the most common failure mode: putting too much execution logic into feature plugins or too much feature logic into the core.

### Conclusion B: Streaming must be the single execution path

The rewrite should not have separate streaming and non-streaming semantics. Instead:

- backends emit canonical event streams,
- frontends either stream them directly or collect them,
- all retry/failover rules are expressed relative to “has client-visible content started yet?”

That keeps the model small and matches the current LIP failure-handling posture.

### Conclusion C: No pairwise translators

The current product requirement is “translate between all supported APIs.” The only scalable way to do that is:

- protocol -> canonical
- canonical -> protocol

Pairwise translation would explode in maintenance burden.

### Conclusion D: Do not use Go’s native `plugin` package

For v1, explicit in-process registration is the correct portability/simplicity choice. It avoids portability limits and race-detector drawbacks while preserving small-core boundaries.

### Conclusion E: ACP support should be subset-first

ACP is important, but it has richer concepts than a plain LLM request/response API. The v1 backend should therefore support the prompt-turn subset cleanly and reject unsupported ACP-only features explicitly rather than pretending they map perfectly.

## External reference notes

### Official / primary references used

- cc-sdd repository and spec-driven guide
- current LIP repo `.kiro`, README, AGENTS, feature docs, and recent dev commit history
- official or primary Go SDK references for:
  - OpenAI
  - Anthropic
  - Google Gen AI
  - AWS Bedrock
- official ACP protocol overview and transport guidance
- Go standard `plugin` package documentation

## Source references

### Kiro / cc-sdd

- https://github.com/gotalab/cc-sdd
- https://github.com/gotalab/cc-sdd/blob/main/README.md
- https://github.com/gotalab/cc-sdd/blob/main/docs/guides/spec-driven.md
- https://raw.githubusercontent.com/gotalab/cc-sdd/main/.kiro/settings/templates/specs/design.md
- https://raw.githubusercontent.com/gotalab/cc-sdd/main/.kiro/settings/templates/specs/tasks.md

### Current LIP repo

- https://github.com/matdev83/llm-interactive-proxy
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/README.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/AGENTS.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/.kiro/AGENTS.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/.kiro/steering/product.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/.kiro/steering/structure.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/.kiro/steering/tech.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/.kiro/steering/testing.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/docs/user_guide/features/failure-handling.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/docs/development_guide/routing-selectors.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/src/core/interfaces/b2bua_mapping_store_interface.py
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/src/core/services/b2bua_session_resolver_service.py
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/src/core/services/tool_call_reactor/README.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/src/core/interfaces/tool_call_reactor_interface.py
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/src/core/interfaces/tool_call_reactor_orchestrator_interface.py
- https://github.com/matdev83/llm-interactive-proxy/commits/dev

### Official API specification references (normative docs)

Primary vendor documentation for the **frontend** and **backend** protocol matrix. Use these for emulator cross-checks and connector compliance; SDK repos (below) are implementation aids, not substitutes for the HTTP/API contracts.

#### OpenAI Responses API (frontend + backend)

- Responses API reference: https://platform.openai.com/docs/api-reference/responses
- Create response: https://platform.openai.com/docs/api-reference/responses/create
- Streaming responses: https://platform.openai.com/docs/api-reference/responses-streaming
- Migration / conceptual guide (Responses vs Chat Completions): https://platform.openai.com/docs/guides/migrate-to-responses
- **Implementation cross-check (2026-04-20):** Go frontend `internal/plugins/frontends/openairesponses` decode/encode + SSE `response.completed` / `[DONE]` verified against `internal/refclient/openairesponses` (official `openai-go` client) in integration tests.
- **Frontend subset vs spec (2026-04-21 refresh):** `POST /v1/responses` only; `input` as a string or an array of **`message`-typed items** (other Responses input item types, e.g. `function_call_output`, are rejected at decode). `instructions` must be a **JSON string** (array form is rejected). Request `tools`, **`tool_choice`** (strings `auto` / `none` / `required`, or object `type: function` with a function name), and sampling fields (`temperature`, `top_p`, `max_output_tokens`, `parallel_tool_calls`) decode into `lipapi.Call`. Wire **`tool_choice: "required"`** maps to canonical **`ToolChoice` mode `any`** (force tool use without a single fixed name), matching `lipapi` validation. Streaming and non-stream responses emit **assistant text** plus **`usage`** when the canonical stream contains `EventUsageDelta`. Canonical **tool-call** events map to Responses **`function_call`** output items and SSE (`response.output_item.added`, `response.function_call_arguments.delta` / `.done`, etc.). Canonical **`EventReasoningDelta`** is **not** emitted on the Responses SSE/JSON wire in v1 (silently skipped). Contract tests: `internal/plugins/frontends/openairesponses/*_test.go`.
- **Additional frontend contract tests (2026-04-21):** Prior cases plus decode rejects (`function_call_output` items, unsupported content/tool types, bad roles), `instructions` + multi-turn `input`, `tool_choice` function object and `required` string, and encode `TestWriteStreamSSE_reasoningDeltaDoesNotBreakCompletion`; HTTP handler coverage unchanged (`TestIntegration_*`).
- **Backend emulator cross-check (2026-04-20):** Reference provider `internal/refbackend/openairesponses` (JSON + SSE for `responses.create`) verified round-trip with `internal/refclient/openairesponses` in `server_test.go`, including multimodal `input_image` / `input_file` request paths and custom JSON response bodies.
- **Backend connector cross-check (2026-04-20):** Go backend `internal/plugins/backends/openairesponses` builds `responses.ResponseNewParams` from `lipapi.Call` (instructions, reasoning effort, tools and tool_choice, multimodal parts, tool-turn `function_call_output` items) and maps Responses SSE to canonical events via `Responses.NewStreaming`. Tests: `invoke_test.go` (including ParamsForCall coverage above and `TestUpstreamError_returnsAPIError` for `*openai.Error`), `integration_test.go`, `map_events_internal_test.go`. See also `refbackend-spec-matrix.md` §10.0.1.
- **Backend connector cross-check (2026-04-20 follow-up):** `map_events_internal_test.go` covers stream `error` → `lipapi.EventError` (`TestHandleUnion_streamError_*`).
- **Backend connector cross-check (2026-04-21):** Responses SSE **`response.output_item.added` (function_call)**, **`response.function_call_arguments.delta` / `.done`**, and **`response.output_item.done`** map to canonical tool events (`map_events.go`); **`response.completed`** finalizes any `function_call` output items not already closed. **`TestHandleUnion_nonMappedEventTypes_emitNoTextOrToolDeltas`** now locks only **non-output** noise events (`response.in_progress`, `response.queued`). E2E: **`TestIntegration_refbackendToolCallStream`** + **`TestHandleUnion_toolCallStream_mapsToCanonicalToolEvents`**.

#### OpenAI Chat Completions — legacy OpenAI-compatible surface (frontend + backend)

- Chat Completions API reference: https://platform.openai.com/docs/api-reference/chat
- Create chat completion: https://platform.openai.com/docs/api-reference/chat/create
- **Frontend subset vs spec (2026-04-21 refresh):** `POST …/chat/completions`; `messages`, `tools`, `tool_choice`, `stream_options`, and sampling options decode into `lipapi.Call` (`stream_options` is preserved under `openailegacy.stream_options` in `Extensions`). **Assistant messages with `tool_calls` or legacy `function_call` are rejected** at decode. **`developer`** role messages decode as canonical **system**. Wire **`tool_choice: "required"`** decodes to canonical **`ToolChoice` mode `any`** (same rationale as Responses: OpenAI “must call a tool” vs `lipapi.ToolChoiceRequired` requiring a named tool). Encode maps canonical **tool-call** events to **`delta.tool_calls`** (streaming) and to **`message.tool_calls`** (non-stream), with **`finish_reason: tool_calls`** when tools ran; **`usage`** on non-stream and final stream chunk when the canonical stream reports token deltas. Integration + golden-backed tests: `internal/plugins/frontends/openailegacy/*_test.go`.
- **Additional frontend contract tests (2026-04-21):** `TestDecodeChat_toolChoiceRequiredString`, `TestDecodeChat_developerRoleMapsToSystem`, `TestDecodeChat_unsupportedToolType`; existing `TestDecodeChat_streamOptionsExtension` and HTTP handler `TestIntegration_*`.
- **Backend emulator cross-check (2026-04-20):** Reference provider `internal/refbackend/openaichat` (JSON + SSE for `chat/completions`) verified round-trip with `internal/refclient/openaichat` in `server_test.go`, including multimodal `image_url` / `file` content parts and custom JSON response bodies.
- **Backend connector cross-check (2026-04-20):** Go backend `internal/plugins/backends/openailegacy` maps canonical calls to Chat Completions (`ParamsForCall` covers tools, `tool_choice`, `stream_options` / usage on stream, generation options, and `role: tool` messages). Tests: `invoke_test.go` (including `TestParamsForCall_toolsAndToolChoiceWireJSON`, `TestParamsForCall_toolResultMessage`, `TestUpstreamError_returnsAPIError`), `integration_test.go`. The connector invokes **`Chat.Completions.NewStreaming` only**; there is no separate non-stream HTTP path in the plugin. See `refbackend-spec-matrix.md` §10.0.2.
- **Backend connector cross-check (2026-04-20 follow-up):** Streaming **`delta.tool_calls`** through **`finish_reason: tool_calls`** is covered by `map_events_internal_test.go` (`TestHandleChunk_toolCallsStreamingFromJSON`) and an end-to-end `httptest` path against `internal/refbackend/openaichat` with custom `StreamSSE` (`TestIntegration_refbackendToolCallsStream` in `integration_test.go`).
- **Backend connector cross-check (2026-04-21):** **`TestIntegration_refbackendMissingAPIKeyOpenFails`** — empty API key yields 401 from refbackend on collect.

#### Anthropic Messages API (frontend + backend)

- Messages API reference: https://docs.anthropic.com/en/api/messages
- **Frontend subset vs spec (2026-04-21 refresh):** `POST …/messages`; `system` (string or JSON array of supported blocks), `messages`, `tools`, `tool_choice`, `max_tokens` (must be **positive**), `temperature` / `top_p`, `stream`, and multimodal **`image` / `document`** (base64 sources only) decode into `lipapi.Call`. **`metadata` / `top_k`** are accepted on the wire but **not** mapped into canonical options (ignored). The **`anthropic-version` header is read but not validated or modeled** on the canonical call. Streaming SSE emits **`message_start`**, **`content_block_start` / `content_block_delta` / `content_block_stop`** for **text** (`text_delta`) and **`tool_use`** (`input_json_delta` on args), then **`message_delta`** / **`message_stop`**. Canonical **`EventReasoningDelta`** is not emitted on Messages SSE in v1. Tests: `internal/plugins/frontends/anthropic/*_test.go`.
- **Additional frontend contract tests (2026-04-21):** `TestDecodeMessage_maxTokensZeroRejected`, `TestDecodeMessage_invalidToolChoiceString`, `TestDecodeMessage_imageNonBase64SourceRejected`; plus existing system-array, tool_result, streaming tool SSE, and HTTP `TestIntegration_*`.
- **Backend emulator cross-check (2026-04-20):** Reference provider `internal/refbackend/anthropicmessages` (JSON + SSE for `messages` with `x-api-key`) verified round-trip with `internal/refclient/anthropicmessages` in `server_test.go`, including multimodal `image` / `document` content blocks and custom JSON response bodies.
- **Backend connector cross-check (2026-04-20):** Go backend `internal/plugins/backends/anthropic` (`ParamsForCall` + streaming `Messages.NewStreaming` → canonical events) verified via `integration_test.go` and `invoke_test.go` against `internal/refbackend/anthropicmessages` (`httptest`), including multimodal request bodies, tools / `disable_parallel_tool_use`, SDK-parseable SSE for text + usage, and `invoke_test.go` **`TestUpstreamError_returnsAPIError`** (typed `*anthropic.Error` from non-stream `Messages.New` against an error HTTP response). See `refbackend-spec-matrix.md` §10.0.3.
- **Backend connector cross-check (2026-04-20 follow-up):** Tool streaming (`tool_use` + `input_json_delta`) and **`thinking_delta` → `EventReasoningDelta`** are covered by `map_events_internal_test.go` (JSON-unmarshaled `MessageStreamEventUnion` fixtures) and by `TestIntegration_refbackendToolUseStream` in `integration_test.go` (custom refbackend `StreamSSE` + full connector `Open` path).
- **Backend connector cross-check (2026-04-21):** **`TestIntegration_refbackendMissingAPIKeyOpenFails`** — empty `x-api-key` yields 401 on collect.

#### Google Gemini `generateContent` / streaming (frontend + backend)

- Gemini API documentation hub: https://ai.google.dev/gemini-api/docs
- REST method catalog (includes `generateContent`, `streamGenerateContent`, batch, live): https://ai.google.dev/api/all-methods
- Text generation guide: https://ai.google.dev/gemini-api/docs/text-generation
- **Frontend subset vs spec (2026-04-21 refresh):** Google AI style paths `…/models/{model}:generateContent` and `…:streamGenerateContent` (see `internal/plugins/frontends/gemini/path.go`); request body `contents`, `systemInstruction`, `generationConfig`, `tools` / `toolConfig` decode into `lipapi.Call`. Parts support **`text`**, **`inlineData` / `inline_data`**, **`functionCall` / `function_call`** (model turn history), and **`functionResponse` / `function_response`** (user turn tool results). **`toolConfig.functionCallingConfig.allowedFunctionNames`** is accepted but **not** narrowed into canonical `ToolChoice.Name` in v1 (mode only). Stream encoding emits **per-chunk `candidates[].content.parts`** (text deltas as separate chunks; completed tool calls as **`functionCall`** parts) and a trailing **`usageMetadata`** object when token deltas were seen; **non-stream** JSON still **omits `usageMetadata`** even if usage events appeared on the canonical stream (subset contract). Tests: `internal/plugins/frontends/gemini/*_test.go`.
- **Additional frontend contract tests (2026-04-21):** `TestDecodeGenerateContent_emptyPartsRejected`, `TestDecodeGenerateContent_unsupportedPartRejected`, plus existing generationConfig, `functionResponse` paths, stream `functionCall`, and non-stream usage omission tests.
- **Backend emulator cross-check (2026-04-20):** Reference provider `internal/refbackend/gemini` (`generateContent` / `streamGenerateContent?alt=sse` with `x-goog-api-key`) verified round-trip with `internal/refclient/gemini` in `server_test.go`, including multimodal inline image/PDF request bodies.
- **Backend connector cross-check (2026-04-20):** Go backend `internal/plugins/backends/gemini` (`StreamParamsForCall` + `Models.GenerateContentStream` → canonical events via `map_events.go`) verified via `integration_test.go` and `invoke_test.go` against `internal/refbackend/gemini` (`httptest`), including streaming text, `usageMetadata` token counts, multimodal inline request bodies, tools / `toolConfig`, and system instruction wiring. See `refbackend-spec-matrix.md` §10.0.4.
- **Backend connector cross-check (2026-04-20 follow-up):** `map_events_internal_test.go` covers **`Part.Thought` → `EventReasoningDelta`** and **`FunctionCall` parts → canonical tool-call events** (args JSON delta aggregation) without relying on live Gemini HTTP.
- **Backend connector cross-check (2026-04-21):** **`TestIntegration_refbackendToolCallStream`** — scripted refbackend SSE with `functionCall` in `candidates[].content.parts` (full connector path). **`TestIntegration_refbackendMissingAPIKeyOpenFails`** — genai client rejects empty API key at `Open`.

#### AWS Bedrock — `Converse` / `ConverseStream` (backend)

- `Converse` (API reference): https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html
- `ConverseStream` (API reference): https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ConverseStream.html
- Conversation inference (user guide): https://docs.aws.amazon.com/bedrock/latest/userguide/conversation-inference.html
- **Backend emulator cross-check (2026-04-20):** Reference provider `internal/refbackend/bedrock` (`POST /model/{modelId}/converse` JSON + `POST /model/{modelId}/converse-stream` as `application/vnd.amazon.eventstream`) verified round-trip with the official `bedrockruntime` AWS SDK for Go v2 in `server_test.go`, including multimodal `image` + `document` (inline bytes) request paths.
- **Backend connector cross-check (2026-04-20):** Go backend `internal/plugins/backends/bedrock` (`ConverseStreamInputForCall` + `ConverseStream` → canonical events via `map_events.go`) verified via `integration_test.go` and `invoke_test.go` against `internal/refbackend/bedrock` (`httptest`), including streaming text, `metadata` usage token counts (custom eventstream body), multimodal request bodies, tools / `ToolChoice`, system blocks, and **`TestUpstreamError_returnsResponseError`** (`*smithyhttp.ResponseError` from `ConverseStream` against an error HTTP response). See `refbackend-spec-matrix.md` §10.0.5.
- **Backend connector cross-check (2026-04-21):** **`TestIntegration_refbackendToolUseStream`** — ConverseStream `application/vnd.amazon.eventstream` frames for **`toolUse`** (`contentBlockStart` / `contentBlockDelta` / `contentBlockStop`) + `messageStop` (`tool_use`), against `internal/refbackend/bedrock`.

#### Agent Client Protocol — ACP subset (backend)

- Protocol overview: https://agentclientprotocol.com/protocol/overview
- Schema: https://agentclientprotocol.com/protocol/schema
- Transports (e.g. stdio, HTTP drafts): https://agentclientprotocol.com/protocol/transports
- Specification source (GitHub): https://github.com/agentclientprotocol/agent-client-protocol
- Community libraries index: https://agentclientprotocol.com/libraries/community
- **Backend emulator cross-check (2026-04-20):** Reference provider `internal/refbackend/acp` implements JSON-RPC `initialize`, `authenticate`, `session/new`, streaming `session/prompt` (`application/x-ndjson` with `session/update` progress plus terminal result), `session/cancel` (HTTP 204), and rejects `session/load` when `loadSession` is false; `server_test.go` covers resource-style prompt content and cancellation. Wire shapes follow https://agentclientprotocol.com/protocol/prompt-turn and package doc (custom HTTP test transport, not normative stdio).
- **Backend connector cross-check (2026-04-20):** Go backend `internal/plugins/backends/acp` (`initialize` / `authenticate` / `session/new` or `acp.sessionId` reuse + `session/prompt` NDJSON → canonical events) verified via `integration_test.go`, `invoke_test.go`, and `map_events_internal_test.go` against `internal/refbackend/acp` (`httptest`), including streaming text, resource-style prompt blocks, session reuse via `Call.Extensions["acp.sessionId"]`, and client cancellation during prompt. Declared tools on the canonical call are rejected in the v1 subset. See `refbackend-spec-matrix.md` §10.0.6.
- **ACP architecture parity (2026-04-20):** The connector is layered for **Python `BaseAcpConnector` parity**: [`Transport`](internal/plugins/backends/acp/transport.go) (`httpTransport` today; stdio-ready), [`HandshakeProfile`](internal/plugins/backends/acp/handshake.go) + `Call.Extensions` for cwd/MCP/authenticate/skip-auth, [`session/prompt`](internal/plugins/backends/acp/prompt_msg.go) with `messageId`, [`SessionUpdateMapperOptions`](internal/plugins/backends/acp/session_update.go) for plan/thought/tool warnings, [`ServerRequestHandler`](internal/plugins/backends/acp/server_handler.go) for inbound agent JSON-RPC (HTTP replies via `SendJSONRPC`), [`CancelProfile`](internal/plugins/backends/acp/cancel.go) for multi-method cancel + correlated ids, and [`HistoryCoordinator`](internal/plugins/backends/acp/history.go) stub for B2BUA-owned transcript state. Subprocess pooling remains out of scope (`internal/acpruntime` per design).

### Official / primary Go and SDK repositories (non-normative aids)

- OpenAI Go SDK: https://github.com/openai/openai-go
- Anthropic Go SDK: https://github.com/anthropics/anthropic-sdk-go
- Google Gen AI Go SDK: https://github.com/googleapis/go-genai — https://pkg.go.dev/google.golang.org/genai
- Go `plugin` package (not used in v1 per design; listed for boundary clarity only): https://pkg.go.dev/plugin

## Open questions intentionally left for implementation discovery

1. Exact canonical field naming and package split inside `lipapi`
2. Whether the in-memory B2BUA store uses TTL sweeps or lazy expiration in v1
3. Whether to expose diagnostics via JSON only or add text/debug views
4. Whether the Gemini frontend should grow beyond the current subset (e.g. more part types, stream `usageMetadata` on non-stream, or honoring `allowedFunctionNames` in canonical `ToolChoice`)
5. Whether ACP session reuse will need a dedicated adapter cache layer in v1
6. Whether to add a tiny helper dependency for SSE framing or keep it entirely stdlib-based

These do not change the architecture contract and can be resolved during implementation as long as the spec boundaries remain intact.
