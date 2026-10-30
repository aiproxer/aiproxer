# Wire-contract goldens

These fixtures anchor wire-contract parity checks for the frontends listed below.

| File | Provenance |
|------|------------|
| `openai_responses_http_streaming.json` | HTTP SSE contract fixture. Endpoint: OpenAI Responses streaming (SSE event payloads as JSON objects). Streaming mode: HTTP SSE contract fixture. |
| `openai_responses_http_nonstream.json` | Derived minimal completed Responses object matching the final `response.completed` payload of the streaming fixture. Endpoint: `POST /v1/responses` non-streaming JSON body. Streaming mode: **non-stream** (`stream:false`). |
| `anthropic_messages_nonstream.json` | Minimal Anthropic Messages API-style assistant JSON synthesized to mirror common Messages response shapes. Endpoint: `POST /v1/messages` non-streaming JSON body. Streaming mode: **non-stream**. |
