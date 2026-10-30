# CommandCode Inference Backends

AIProxer provides two distinct backend connector plugins for the [CommandCode](https://commandcode.ai) Inference API:

1. **`commandcode-openai`**: Connects via CommandCode's OpenAI Chat Completions-compatible endpoint (`/provider/v1/chat/completions`) for OSS and general reasoning/chat models (such as `Qwen/Qwen3.7-Flash`, `deepseek-ai/*`, `meta-llama/*`).
2. **`commandcode-anthropic`**: Connects via CommandCode's Anthropic Messages-compatible endpoint (`/provider/v1/messages`) for Anthropic/Claude models (such as `claude-3-5-sonnet-20241022`, `claude-haiku-4-5-20251001`).

Both connectors are standalone executable backend plugins implementing the `aiproxer.backendplugin.manifest/v1` contract and communicate with AIProxer core over gRPC / anonymous local IPC.

---

## Configuration

### 1. `commandcode-openai` Connector

```yaml
plugins:
  backend_discovery:
    enabled: true
    paths:
      - /opt/AIProxer/plugins
  backends:
    - kind: commandcode-openai
      id: commandcode-openai
      enabled: true
      config:
        base_url: "https://api.commandcode.ai/provider/v1"
        timeout: 30s
```

### 2. `commandcode-anthropic` Connector

```yaml
plugins:
  backend_discovery:
    enabled: true
    paths:
      - /opt/AIProxer/plugins
  backends:
    - kind: commandcode-anthropic
      id: commandcode-anthropic
      enabled: true
      config:
        base_url: "https://api.commandcode.ai/provider/v1"
        timeout: 30s
```

Credentials are environment-only: the host reads `COMMANDCODE_OPENAI_API_KEY`
(`COMMANDCODE_OPENAI_API_KEY_2`, …) for `commandcode-openai` and
`COMMANDCODE_ANTHROPIC_API_KEY` (`COMMANDCODE_ANTHROPIC_API_KEY_2`, …) for
`commandcode-anthropic`. Never put `api_key` values or `${...}` interpolation
in connector YAML — literal secret fields fail closed at `check-config` with
the field path and the required environment root. The two exports use separate
roots so independent deployments never share one credential identity.

---

## Routing & Model Selection

- Routing is prefixed by the backend instance ID, e.g.:
  - `commandcode-openai:Qwen/Qwen3.7-Flash`
  - `commandcode-anthropic:claude-3-5-sonnet-20241022`
- Both connectors support **dynamic model inventory**, querying `GET /provider/v1/models` to discover supported model IDs and metadata.

---

## Capabilities & Cross-API Conversion

| Feature | `commandcode-openai` | `commandcode-anthropic` |
|---|---|---|
| **Upstream API** | `/provider/v1/chat/completions` | `/provider/v1/messages` |
| **Streaming (SSE)** | Yes (`EventTextDelta`, `EventToolCall*`, etc.) | Yes (SSE event protocol conversion) |
| **Non-Streaming** | Yes (Canonical collector) | Yes (JSON parsing + collection) |
| **Tool Calling** | Yes (OpenAI function tools) | Yes (Anthropic tool definitions & blocks) |
| **Reasoning / Thinking** | Yes (reasoning deltas forwarded) | Yes (thinking content blocks forwarded) |
| **Cross-API Conversion** | Anthropic / Gemini / OpenAI frontends -> `commandcode-openai` | OpenAI / Gemini / Anthropic frontends -> `commandcode-anthropic` |

---

## Verification & Testing

Both connectors include complete test suites:
- **Unit & service tests**: Configuration decoding, default URL fallback, and model catalog mapping.
- **Parity test suites**: Streaming and non-streaming request translation, tool execution deltas, reasoning blocks, error classification (401, 403, 429), and extra parameters.
- **Contract TCK**: Official `contracttest` certification over in-memory gRPC `bufconn`.
- **Live end-to-end tests**: Verified against `https://api.commandcode.ai/provider/v1`.
