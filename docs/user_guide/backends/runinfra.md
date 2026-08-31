# Runinfra Backend

The Runinfra backend provides access to [runinfra.ai](https://runinfra.ai)'s OpenAI-compatible Chat Completions API.

## Overview

`runinfra.ai` hosts open-source and specialized models via an OpenAI-compatible interface. The proxy supports the `runinfra` backend for seamless model routing, automatic model discovery, streaming, and failover.

## Key Features

- **OpenAI-Compatible API**: Implements standard `/chat/completions` and `/models` protocols.
- **Unified Model Routing**: Routes models using the `runinfra/<model-name>` prefix (e.g. `runinfra/meta-llama/Llama-3.3-70B-Instruct`, `runinfra/deepseek-ai/DeepSeek-V3`).
- **Automatic Prefix Stripping**: Automatically strips the `runinfra/` vendor prefix from model identifiers before sending outbound requests to `https://api.runinfra.ai/v1`.
- **Dynamic Model Discovery**: Queries `GET /v1/models` on startup to discover available models when credentials are provided.

## Configuration

### Environment Variables

Set the API key in your environment:

```bash
export RUNINFRA_API_KEY="your-runinfra-api-key"
```

Optional configuration variables:
- `RUNINFRA_API_BASE_URL`: Override base URL (default: `https://api.runinfra.ai/v1`).
- `RUNINFRA_TIMEOUT`: Override request timeout in seconds (default: 120).

### CLI Arguments

Start the proxy with `runinfra` as the default backend:

```bash
python -m src.core.cli --default-backend runinfra
```

### YAML Configuration

```yaml
# config.yaml
backends:
  runinfra:
    # api_url: "https://api.runinfra.ai/v1" # Optional override
    timeout: 120
    # models: [] # Optional static allowlist

default_backend: runinfra
```

## Usage Examples

### Native OpenAI Chat Completions

```bash
curl -X POST http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_PROXY_KEY" \
  -d '{
    "model": "runinfra/meta-llama/Llama-3.3-70B-Instruct",
    "messages": [
      {"role": "user", "content": "Hello world!"}
    ]
  }'
```

### Models Discovery

```bash
curl http://localhost:8000/v1/models \
  -H "Authorization: Bearer YOUR_PROXY_KEY"
```

## Related Documentation

- [Backend Overview](overview.md)
- [NVIDIA Backend](nvidia.md)
- [CommandCode Backend](commandcode.md)
