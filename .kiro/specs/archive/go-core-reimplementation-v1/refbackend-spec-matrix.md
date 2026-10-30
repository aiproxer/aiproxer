# Reference backend emulator — specification cross-check matrix

**Parity roadmap:** [`../llm-api-parity/design.md`](../llm-api-parity/design.md) (tasks: [`../llm-api-parity/tasks.md`](../llm-api-parity/tasks.md)).

Normative URLs are listed in `research.md` under **Official API specification references (normative docs)**. This matrix ties each backend emulator to exercised surfaces and tests.

## Shared multimodal fixtures

Same as reference clients: `testdata/refclient/tiny.png`, `testdata/refclient/minimal.pdf`.

## 10.0.1 — OpenAI Responses API (`internal/refbackend/openairesponses`)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| Create response (non-streaming) | Responses create | JSON `application/json` + SDK parse | `server_test.go` |
| Create response (streaming) | Responses streaming | SSE `response.completed` + `[DONE]` | `server_test.go` |
| Auth | API key bearer | `Authorization: Bearer` required | `server_test.go` |
| Routing | POST path suffix `/responses` | wrong path → 404 | `server_test.go` |
| Multimodal — inbound | Image / file inputs | `input_image` + `input_file` in request JSON | `server_test.go` |
| Multimodal — outbound | Assistant message may include `input_image` / `input_file` output items | `NonStreamJSON` / scripted SSE + SDK `RawJSON` on content blocks; `TestHandler_assistantOutput_imageAndFileInMessage_refclientParse` | `server_test.go` |

### Connector stream mapping (OpenAI Responses backend plugin)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| Stream `error` events | Responses streaming | `TestHandleUnion_streamError_emitsEventError`, `TestHandleUnion_streamError_emptyMessage_defaults` | `internal/plugins/backends/openairesponses/map_events_internal_test.go` |
| v1 subset — ignored stream types | Responses streaming (`response.in_progress`, `response.queued`, …) | `TestHandleUnion_nonMappedEventTypes_emitNoTextOrToolDeltas` | `internal/plugins/backends/openairesponses/map_events_internal_test.go` |
| Streaming — function tool calls | `response.output_item.added` + `response.function_call_arguments.delta` / `.done` | `TestHandleUnion_toolCallStream_mapsToCanonicalToolEvents` | `internal/plugins/backends/openairesponses/map_events_internal_test.go` |
| E2E — tool stream vs refbackend | Scripted SSE + `httptest` | `TestIntegration_refbackendToolCallStream` | `internal/plugins/backends/openairesponses/integration_test.go` |
| E2E — assistant media on `response.completed` stream | Completed response `output` message with `input_image` + `input_file` | `TestIntegration_refbackendStreamAssistantMediaCollected` | `internal/plugins/backends/openairesponses/integration_test.go` |

## 10.0.2 — Legacy OpenAI Chat Completions (`internal/refbackend/openaichat`)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| Create chat completion (non-streaming) | Chat create | JSON `application/json` + SDK parse | `server_test.go` |
| Create chat completion (streaming) | Chat streaming | SSE `data:` chunks + delta text | `server_test.go` |
| Auth | API key bearer | `Authorization: Bearer` required | `server_test.go` |
| Routing | POST `…/chat/completions` | wrong path → 404 | `server_test.go` |
| Multimodal — inbound | Image URL + file parts | `image_url` + `"type":"file"` in request JSON | `server_test.go` |
| Multimodal — outbound | Non-stream assistant `content` may include image/file parts (proxy encoders); streaming deltas remain text-centric on the wire | `design.md` row OAC-MM-OUT; emulator `NonStreamJSON` is the hook for scripted assistant payloads | `server_test.go` |
| Streaming — `delta.tool_calls` | Chat streaming (tool_calls + `finish_reason`) | custom `Config.StreamSSE` consumed by `openai-go` stream + `internal/plugins/backends/openailegacy` `TestIntegration_refbackendToolCallsStream` | `openailegacy/integration_test.go` |
| Auth — missing key | Bearer required | `TestIntegration_refbackendMissingAPIKeyOpenFails` (401 on collect) | `openailegacy/integration_test.go` |

### Connector stream mapping (legacy Chat backend plugin)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| `delta.tool_calls` sequence | Chat streaming | JSON-unmarshaled `ChatCompletionChunk` fixtures | `internal/plugins/backends/openailegacy/map_events_internal_test.go` |

## 10.0.3 — Anthropic Messages API (`internal/refbackend/anthropicmessages`)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| Create message (non-streaming) | Messages create | JSON `application/json` + SDK parse | `server_test.go` |
| Create message (streaming) | Messages streaming | SSE `event:` lines + SDK stream iterator | `server_test.go` |
| Auth | API key | `x-api-key` required | `server_test.go` |
| Routing | POST `…/v1/messages` | wrong path → 404 | `server_test.go` |
| Multimodal — inbound | Image + document blocks | `"type":"image"` + `"type":"document"` in request JSON | `server_test.go` |
| Multimodal — outbound | Default emulator JSON is assistant text; custom `NonStreamJSON` can carry richer blocks | Same pattern as OpenAI Responses row; connector maps streaming assistant `image` / `document` starts | `server_test.go`, `../llm-api-parity/design.md` ANT-MM-OUT |
| Streaming — `tool_use` / `input_json_delta` | Messages streaming (tool block) | custom `Config.StreamSSE` + `internal/plugins/backends/anthropic` `TestIntegration_refbackendToolUseStream` | `anthropic/integration_test.go` |
| Auth — missing key | `x-api-key` required | `TestIntegration_refbackendMissingAPIKeyOpenFails` (401 on collect) | `anthropic/integration_test.go` |

### Connector stream mapping (Anthropic Messages backend plugin)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| `input_json_delta` / `tool_use` | Messages streaming | JSON-unmarshaled `MessageStreamEventUnion` fixtures | `internal/plugins/backends/anthropic/map_events_internal_test.go` |
| `thinking_delta` | Extended thinking stream | JSON-unmarshaled delta fixture | `internal/plugins/backends/anthropic/map_events_internal_test.go` |
| Assistant `image` / `document` `content_block_start` | Messages streaming (URL / base64 sources) | `TestHandleEvent_assistantImageURLContentBlockStart`, `TestHandleEvent_assistantDocumentURLContentBlockStart` | `internal/plugins/backends/anthropic/map_events_internal_test.go` |

## 10.0.4 — Gemini generateContent (`internal/refbackend/gemini`)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| generateContent (non-streaming) | Gemini REST `generateContent` | JSON `application/json` + `google.golang.org/genai` parse | `server_test.go` |
| streamGenerateContent (`?alt=sse`) | Gemini streaming | SSE `data:` JSON chunks (`\n\n` delimited) consumed by genai stream iterator | `server_test.go` |
| Auth | Google AI API key | `x-goog-api-key` required (direct `http.Post` when SDK cannot omit key) | `server_test.go` |
| Routing | POST path contains `:generateContent` or `streamGenerateContent` | non-POST / wrong path → 404 | `server_test.go` |
| Multimodal — inbound | Image + PDF inline parts | request JSON contains `inlineData` / `inline_data` | `server_test.go` |
| Multimodal — outbound | Model inline image + PDF parts | configurable `NonStreamJSON` with `inlineData` image and PDF parts parsed by SDK | `server_test.go` |
| Streaming — `functionCall` in candidate | Tool invocation in SSE chunk | `TestIntegration_refbackendToolCallStream` | `gemini/integration_test.go` |
| Auth — missing key | Google AI client requires API key | `TestIntegration_refbackendMissingAPIKeyOpenFails` | `gemini/integration_test.go` |

## 10.0.5 — Amazon Bedrock Converse / ConverseStream (`internal/refbackend/bedrock`)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| Converse (non-streaming) | Converse API | JSON `application/json` + SDK parse | `server_test.go` |
| ConverseStream | ConverseStream API | `application/vnd.amazon.eventstream` + SDK stream iterator | `server_test.go` |
| Auth | SigV4 | `Authorization` required unless `AllowMissingAuthorization` | `server_test.go` |
| Routing | POST `/model/{modelId}/converse` and `/converse-stream` | wrong path → 404 | `server_test.go` |
| Multimodal — inbound | Image + document blocks | request JSON contains image bytes + document bytes | `server_test.go` |
| Multimodal — outbound | Assistant text in default stream frames; image/document assistant-output → canonical ref events not mapped in Bedrock connector v1 | `design.md` row BRK-MM covers **inbound** user multimodal; emulator + connector tests focus text + `toolUse` stream | `server_test.go`, `bedrock/integration_test.go` |
| Backend connector | Same wire vs emulator | `internal/plugins/backends/bedrock` `integration_test.go` + `invoke_test.go` | connector package |
| ConverseStream — `toolUse` block | `contentBlockStart` / `contentBlockDelta` (`toolUse.input`) / `contentBlockStop` | `TestIntegration_refbackendToolUseStream` | `bedrock/integration_test.go` |

## 10.0.6 — ACP prompt-turn subset (`internal/refbackend/acp`)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| initialize / authenticate / session/new | JSON-RPC 2.0 | single JSON response per `POST /v1/acp` | `server_test.go` |
| session/prompt | NDJSON stream + terminal JSON-RPC result | `application/x-ndjson` lines | `server_test.go` |
| session/cancel | JSON-RPC notification | HTTP 204 | `server_test.go` |
| Resource prompt content | prompt-turn resource blocks | `type":"resource"` echo path | `server_test.go` |
| Backend connector | Same wire vs emulator | `internal/plugins/backends/acp` `integration_test.go` + `invoke_test.go` | connector package |

### Connector stream mapping (ACP backend plugin)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| NDJSON `session/update` | plan vs `agent_message_chunk` | plan → `EventReasoningDelta` (or `EventWarning` if disabled); chunk → `EventTextDelta` (`text` / `textDelta`) | `internal/plugins/backends/acp/map_events_internal_test.go` |
| Terminal `result.stopReason` | end_turn / cancelled | `EventResponseFinished` after deltas | `internal/plugins/backends/acp/integration_test.go` |
| Inbound server JSON-RPC | agent → client requests | `ServerRequestHandler` + HTTP `SendJSONRPC` reply path | `internal/plugins/backends/acp/server_request_test.go` (unit); full stdio interleave TBD |
| Future emulator | cancel variants / inbound RPC | extend `internal/refbackend/acp` when conformance needs them | — |

### Connector stream mapping (Gemini backend plugin)

| Area | Normative reference | Exercised in tests | Test file |
|------|---------------------|--------------------|-----------|
| Candidate `Part` — `FileData` URI | Model file / image refs on the wire | `TestHandleResponse_fileDataURI_emitsAssistantImageRef`, `TestHandleResponse_fileDataURI_nonImage_emitsAssistantFileRef` | `internal/plugins/backends/gemini/map_events_internal_test.go` |
| Candidate `Part` — `thought` | Reasoning / thought summaries (API-dependent) | `Part{Thought: true}` → `EventReasoningDelta` | `internal/plugins/backends/gemini/map_events_internal_test.go` |
| Candidate `Part` — `functionCall` | Tool invocation | `FunctionCall` with `Args` → tool canonical events | `internal/plugins/backends/gemini/map_events_internal_test.go` |
