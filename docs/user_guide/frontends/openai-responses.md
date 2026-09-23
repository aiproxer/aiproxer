# OpenAI Responses API Frontend

The OpenAI Responses API frontend (`/v1/responses`) provides full compatibility with the OpenAI Responses API specification. This newer API is designed for structured output generation with JSON schema validation, multi-turn conversations, and enhanced reasoning model support.

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/v1/responses` | Create a structured response (HTTP) |
| WebSocket | `/v1/responses` | WebSocket endpoint for low-latency connections |

**WebSocket Support**: The Responses API supports WebSocket transport for persistent, low-latency connections. See [WebSocket Transport Guide](../features/websocket-transport.md) for details.

## Supported Request Parameters

### Core Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `model` | string | Yes | Model identifier |
| `input` | string/array | No | Text, image, or file inputs to the model |
| `messages` | array | No | Conversation messages (alternative to `input`) |
| `instructions` | string | No | System/developer message for model context |
| `response_format` | object | No | Structured response format specification |

### Output Control

| Parameter | Type | Description |
|-----------|------|-------------|
| `max_tokens` | integer | Maximum tokens to generate (deprecated) |
| `max_output_tokens` | integer | Upper bound for output tokens including reasoning |
| `temperature` | number | Sampling temperature (0.0-2.0) |
| `top_p` | number | Nucleus sampling parameter (0.0-1.0) |
| `top_logprobs` | integer | Number of most likely tokens to return (0-20) |
| `n` | integer | Number of completions to generate |
| `stop` | string/array | Stop sequences |
| `presence_penalty` | number | Presence penalty (-2.0 to 2.0) |
| `frequency_penalty` | number | Frequency penalty (-2.0 to 2.0) |
| `logit_bias` | object | Logit bias adjustments |

### Tool Calling

| Parameter | Type | Description |
|-----------|------|-------------|
| `tools` | array | Array of tool definitions |
| `tool_choice` | string/object | Tool selection: `none`, `auto`, `required`, or specific tool |
| `parallel_tool_calls` | boolean | Allow model to run tool calls in parallel |
| `max_tool_calls` | integer | Maximum number of built-in tool calls per response |

### Reasoning Models (o1, o3, gpt-5)

| Parameter | Type | Description |
|-----------|------|-------------|
| `reasoning` | object | Configuration for reasoning models |
| `reasoning.effort` | string | Reasoning effort: `minimal`, `low`, `medium`, `high` |
| `reasoning.summary` | string | Summary mode: `auto`, `concise`, `detailed` |

### Multi-turn Conversation

| Parameter | Type | Description |
|-----------|------|-------------|
| `conversation` | string/object | Conversation this response belongs to |
| `previous_response_id` | string | Link to previous response for context |
| `truncation` | string | Truncation strategy for long contexts |

### Text/Format Configuration

| Parameter | Type | Description |
|-----------|------|-------------|
| `text` | object | Text response configuration |
| `text.format` | object | Format specification (type: text, json_schema, etc.) |
| `text.verbosity` | string | Verbosity level: `low`, `medium`, `high` (also settable via URI `?verbosity=...` or backend `extra.verbosity`; the proxy injects/merges into `text.verbosity` on the outbound Responses payload) |

### Streaming & Processing

| Parameter | Type | Description |
|-----------|------|-------------|
| `stream` | boolean | Whether to stream the response |
| `stream_options` | object | Options for streaming responses |
| `background` | boolean | Run model response in background |

### Caching & Optimization

| Parameter | Type | Description |
|-----------|------|-------------|
| `prompt_cache_key` | string | Cache key for prompt caching optimization |
| `prompt_cache_retention` | string | Cache retention policy (e.g., `24h`) |
| `prompt` | object | Reference to a prompt template |
| `seed` | integer | Random seed for reproducibility |

### Metadata & Tracking

| Parameter | Type | Description |
|-----------|------|-------------|
| `metadata` | object | Key-value pairs attached to the request (max 16) |
| `safety_identifier` | string | Stable identifier for safety tracking |
| `user` | string | User identifier (deprecated, use `safety_identifier`) |
| `include` | array | Additional output data to include in response |
| `store` | boolean | Store response for later retrieval/distillation |
| `service_tier` | string | Service tier: `auto`, `default`, `flex` |

### Proxy-Specific Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `session_id` | string | Session identifier for proxy session management |
| `agent` | string | Agent identifier |
| `extra_body` | object | Additional parameters passed through to backend |

`extra_body` can also carry proxy-only routing hints. When your model selector
uses composite routing with `[max_context=N]`, set
`extra_body.request_context_tokens` to tell the proxy the exact token count for
this request. That override is used when deciding which branches remain eligible.

Example:

```json
{
  "model": "[max_context=8192]openai:gpt-4o|anthropic:claude-3-5-sonnet",
  "messages": [{"role": "user", "content": "Hello"}],
  "extra_body": {
    "request_context_tokens": 9000
  }
}
```

## Response Format

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Unique response identifier |
| `object` | string | Always `"response"` |
| `created` | integer | Unix timestamp of creation |
| `model` | string | Model used for generation |
| `choices` | array | Array of completion choices (legacy format) |
| `output` | array | Responses API formatted output items |
| `output_text` | array | Aggregated text output (if available) |
| `usage` | object | Token usage statistics |
| `service_tier` | string | Actual service tier used |
| `system_fingerprint` | string | System fingerprint for reproducibility |

### Output Item Format

```json
{
  "id": "msg-resp-123-0",
  "type": "message",
  "role": "assistant",
  "status": "completed",
  "content": [
    {"type": "output_text", "text": "Response text..."}
  ]
}
```

## JSON Schema Validation

The frontend enforces JSON schema limits for safety and performance:

| Limit | Value |
|-------|-------|
| Maximum schema depth | 64 levels |
| Maximum schema nodes | 4096 nodes |
| Maximum collection items | 256 per array/object |
| Maximum string length | 65536 characters |

Invalid schemas return HTTP 400 with detailed error information.

## Example Usage

### Basic Structured Output

```bash
curl -X POST http://localhost:8000/v1/responses \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_PROXY_KEY" \
  -d '{
    "model": "gpt-4o",
    "input": "Generate a person profile for a software engineer",
    "instructions": "You are a helpful assistant that generates structured data.",
    "response_format": {
      "type": "json_schema",
      "json_schema": {
        "name": "person_profile",
        "description": "A person profile",
        "schema": {
          "type": "object",
          "properties": {
            "name": {"type": "string"},
            "age": {"type": "integer"},
            "occupation": {"type": "string"},
            "skills": {
              "type": "array",
              "items": {"type": "string"}
            }
          },
          "required": ["name", "age", "occupation"]
        },
        "strict": true
      }
    },
    "max_output_tokens": 500,
    "temperature": 0.7
  }'
```

### Streaming Response

```bash
curl -X POST http://localhost:8000/v1/responses \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_PROXY_KEY" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Write a story"}],
    "stream": true
  }'
```

### With Reasoning Configuration

```bash
curl -X POST http://localhost:8000/v1/responses \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_PROXY_KEY" \
  -d '{
    "model": "o1-preview",
    "input": "Solve this complex mathematical problem...",
    "reasoning": {
      "effort": "high",
      "summary": "detailed"
    },
    "max_output_tokens": 4000
  }'
```

### Using Input Field (Alternative to Messages)

```bash
curl -X POST http://localhost:8000/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "input": "What is 2+2?",
    "instructions": "Respond with just the number"
  }'
```

## Backend Translation

Requests to `/v1/responses` are automatically translated to the appropriate format for any configured backend:

| Target Backend | Translation |
|----------------|-------------|
| OpenAI | Native passthrough or Chat Completions translation |
| Anthropic | Full translation with tool support |
| Gemini | Full translation with multimodal support |
| Cursor CLI ACP | Chat Completions projection; client tools are rejected; each request/session workspace gets its own ACP child; Cursor-owned agent tools are not surfaced to Codex |
| Other backends | Explicit `provider_limitation` until a dedicated projection path exists |

### Cursor CLI ACP limitations

Cursor CLI ACP can be used as a Responses-wire-compatible Codex custom provider only with an explicit safety limitation. It emits native Responses streaming events and non-streaming `object: "response"` payloads, but it does not expose Cursor-owned tool requests as Codex function calls. Responses tool definitions are rejected and ACP permission requests are denied. However, live Cursor CLI testing proves that `--mode ask` can still execute built-in tools (including file edits) without sending an ACP permission request. Therefore this path is not mechanically text-only, is not governed by the Codex child-agent tool loop, and must not be configured as an Executor. Limit it to Planner/Advisor roles only if the configured workspace is dedicated and disposable or independently constrained at the OS/container boundary.

Each Cursor ACP request uses the workspace from session `project_dir` or request `project_dir` / `workspace_path` / `cwd` / `project`. Distinct directories spawn distinct ACP children without restarting the proxy. Optional `workspace_path` or `CURSOR_CLI_WORKSPACE` is only a default when the request/session does not name a directory. Prompt text is never inspected by the connector to select a workspace. `GET /v1/models` exposes instance-pinned selectors such as `cursor-cli-acp.default:cursor/glm-5.2-max` from live Cursor CLI discovery after the lazy backend has been activated. Multiple Cursor instances remain separate catalog entries, and a configured `cursor_api_endpoint` is used for both model discovery and ACP execution. Unavailable selectors fail without fallback. Embedded model effort suffixes are authoritative; contradictory `reasoning.effort` values are rejected. Every Cursor turn uses a fresh ACP process; `previous_response_id` replays the complete visible transcript from the in-process session store. Use the [Cursor CLI ACP backend guide](../backends/overview.md#cursor-cli-acp-as-a-codex-custom-provider) for the exact standard and fast model slug format; do not append `[fast=false]` to the API slug.

The `input` field is converted to `messages` format, and `instructions` becomes a system message when routing to backends that don't natively support the Responses API.

## Differences from Chat Completions

| Feature | Chat Completions | Responses API |
|---------|------------------|---------------|
| Input format | `messages` array only | `input` field or `messages` |
| System message | In `messages` array | `instructions` field |
| Output format | `choices[].message` | `output[]` with typed content |
| Structured output | `response_format` | Enhanced `response_format` with strict validation |
| Reasoning config | `reasoning_effort` only | Full `reasoning` object |
| Conversation state | Manual | `conversation`, `previous_response_id` |

## Related Documentation

- [OpenAI Chat Completions Frontend](openai-chat-completions.md) - Legacy Chat Completions API
- [OpenAI Backend](../backends/openai.md) - Configure OpenAI as a backend
- [Frontend Overview](overview.md) - All available frontends

