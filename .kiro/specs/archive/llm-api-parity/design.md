# LLM API parity — design

## Traceability

- **Normative URLs:** [research.md § Official API specification references](../go-core-reimplementation-v1/research.md#official-api-specification-references-normative-docs)
- **FE×BE matrix code:** `internal/testkit/conformance/matrix.go`
- **Existing evidence tables:** [refclient-spec-matrix.md](../go-core-reimplementation-v1/refclient-spec-matrix.md), [refbackend-spec-matrix.md](../go-core-reimplementation-v1/refbackend-spec-matrix.md)
- **Handoff roadmap:** `.dev/llm_api_parity_plan.md`

## Row status vocabulary

| Status | Meaning |
|--------|---------|
| `implemented` | Proxy supports; matrix row green; tests listed in row |
| `planned` | In scope; not yet green |
| `out_of_scope` | Explicitly excluded for this roadmap |
| `vendor_extension_only` | Represented only via `Call.Extensions` / adapter-local contract |
| `wire_only` | Proven refclient↔refbackend; not yet full proxy path |

## Canonical stream extensions (Phase 2)

Assistant multimodal **references** (not inline megabyte payloads) are represented as first-class canonical events so frontends can encode vendor-specific output items without overloading `text_delta`:

- `assistant_image_ref` — `Event` uses `AssistantRef` + `AssistantMIME` (see `pkg/lipapi` godoc).
- `assistant_file_ref` — `Event` uses `AssistantRef` + `AssistantMIME` + optional `AssistantName`.

Optional **`FinishReason`** on `EventResponseFinished` carries adapter-normalized stop/finish semantics where the vendor documents it. Aggregated copy appears on `Collected.FinishReason`.

**P2.3 — Capability audit:** No new `lipapi.Capability` values were added for assistant multimodal **output** refs. `lipapi.RequiredCapabilities` is derived from **request** message parts only (e.g. user `PartImageRef` / `PartFileRef`), not from assistant ref events on the response stream. Evidence: `pkg/lipapi/required_capabilities_assistant_output_test.go`.

## Parity matrices (row IDs)

### OpenAI Responses (`OAR-*`)

| ID | Area | Normative anchor (research.md) | Owner paths | Status | Evidence |
|----|------|---------------------------------|-------------|--------|----------|
| OAR-REQ-ITEMS | Request `input` message items + tools + `tool_choice` | OpenAI Responses create | `internal/plugins/frontends/openairesponses`, `pkg/lipapi` | implemented | Frontend decode/encode tests |
| OAR-SSE | SSE sequence + terminal markers | Responses streaming | `internal/plugins/frontends/openairesponses`, `internal/plugins/backends/openairesponses` | implemented | Integration + `map_events` tests |
| OAR-TOOLS | Tool call lifecycle + continuation items | Migration + create | backends/openairesponses, `pkg/lipapi` | implemented | `invoke_test`, integration |
| OAR-USAGE | Usage propagation | Streaming + create | front/back openairesponses | implemented | Encode + map_events |
| OAR-MM-IN | Multimodal request (`input_image`, `input_file`) | Responses reference | front/back, refclient, refbackend | implemented | refclient matrix §9.0.1 |
| OAR-MM-OUT | Assistant image/file output items | Responses reference | `pkg/lipapi` events, front/back | implemented | `output_media.go` + `output_media_test.go`; `TestIntegration_refbackendStreamAssistantMediaCollected` in `integration_test.go`; refbackend `TestHandler_assistantOutput_imageAndFileInMessage_refclientParse` |

### OpenAI Chat Completions (`OAC-*`)

| ID | Area | Normative anchor | Owner paths | Status | Evidence |
|----|------|------------------|-------------|--------|----------|
| OAC-DECODE | messages, tools, `tool_choice`, stream_options | Chat create | `internal/plugins/frontends/openailegacy` | implemented | decode tests |
| OAC-STREAM | Stream chunks + finish_reason | Chat streaming | front/back openailegacy | implemented | map_events + integration |
| OAC-TOOLS | Tool deltas + `tool_calls` finish | Chat streaming | backends/openailegacy | implemented | internal tests |
| OAC-MM-IN | Vision / file parts | Chat reference | refclient, refbackend, plugins | implemented | refclient §9.0.2 |
| OAC-MM-OUT | Assistant multimodal refs | Chat reference | `pkg/lipapi`, plugins | implemented | Chat non-stream `content` JSON array; stream path unchanged for media-only deltas |

### Anthropic Messages (`ANT-*`)

| ID | Area | Normative anchor | Owner paths | Status | Evidence |
|----|------|------------------|-------------|--------|----------|
| ANT-BLOCKS | Content blocks + streaming order | Messages API | anthropic front/back | implemented | integration + map_events |
| ANT-TOOLS | tool_use + tool_result | Messages API | plugins | implemented | tests per research notes |
| ANT-USAGE | usage + stop | Messages API | backends | implemented | integration |
| ANT-MM-IN | image + document | Messages API | refclient, plugins | implemented | §9.0.3 |
| ANT-MM-OUT | Assistant image/document output | Messages API | lipapi + anthropic encode | implemented | Frontend encode tests; backend `assistant_output_stream.go` + `TestHandleEvent_assistantImageURLContentBlockStart`, `TestHandleEvent_assistantDocumentURLContentBlockStart` in `map_events_internal_test.go` |

### Gemini (`GEM-*`)

| ID | Area | Normative anchor | Owner paths | Status | Evidence |
|----|------|------------------|-------------|--------|----------|
| GEM-BODY | contents, systemInstruction, generationConfig, tools | Text generation / REST | gemini front/back | implemented | decode/encode tests |
| GEM-STREAM | stream chunk framing + usageMetadata | REST streaming | plugins, refbackend | implemented | integration |
| GEM-FN | functionCall / functionResponse turns | Gemini docs | plugins | implemented | tests |
| GEM-MM-IN | inline multimodal | Gemini docs | refclient, plugins | implemented | §9.0.4 |
| GEM-MM-OUT | Assistant multimodal parts on wire | Gemini docs | lipapi + gemini encode | implemented | Backend `map_events.go` (`Part.FileData` URI → assistant ref events) + `TestHandleResponse_fileDataURI_emitsAssistantImageRef` / `_nonImage_emitsAssistantFileRef` in `map_events_internal_test.go`; non-stream `usageMetadata` omission unchanged (documented subset) |

### Bedrock (`BRK-*`)

| ID | Area | Normative anchor | Owner paths | Status | Evidence |
|----|------|------------------|-------------|--------|----------|
| BRK-CONV | Converse + ConverseStream mapping | AWS Converse API | backends/bedrock, refbackend | implemented | integration + invoke tests |
| BRK-TOOLS | toolUse stream | API reference | bedrock plugin | implemented | TestIntegration_refbackendToolUseStream |
| BRK-USAGE | metadata usage | User guide | bedrock | implemented | map_events / integration |
| BRK-MM | Image + document inline | User guide | refbackend, plugin | implemented | server_test / integration |

### ACP subset (`ACP-*`)

| ID | Area | Normative anchor | Owner paths | Status | Evidence |
|----|------|------------------|-------------|--------|----------|
| ACP-HAND | initialize + authenticate | ACP overview | backends/acp, refbackend | implemented | server_test |
| ACP-SESS | session/new + reuse | Prompt-turn | acp plugin | implemented | integration |
| ACP-PROMPT | session/prompt NDJSON | Prompt-turn | acp plugin | implemented | invoke + integration |
| ACP-CANCEL | cancel | Transports / schema | acp | implemented | tests |
| ACP-RES | Resource/reference prompt content | Subset | acp | implemented | integration |
| ACP-TOOLS-REJ | Canonical tools rejected | Subset | validateACPCall | implemented | plugin tests |
| ACP-EXCL | Full agent / terminal / fs / slash | — | — | out_of_scope | Listed here only |

## Conflicts with stage-two architecture

When refactors under `go-core-reimplementation-stage-two` overlap parity work, **canonical representability and wire evidence take precedence** until matrix rows can be marked truthfully.

## Conformance layout

- **Shared:** existing `AllCells()` matrix + `harness.go` translation tests.
- **Protocol-specific:** `internal/testkit/conformance/parity_*.go` (see tasks) for rows that are invisible in FE×BE cells alone.
