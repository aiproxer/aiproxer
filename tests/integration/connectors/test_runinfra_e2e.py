"""End-to-end integration tests for runinfra.ai backend through the proxy."""

from __future__ import annotations

import json
from typing import Any

import httpx
import pytest

pytest.importorskip("respx")

from respx import MockRouter
from src.core.app.stages import (
    BackendStage,
    CommandStage,
    ControllerStage,
    CoreServicesStage,
    InfrastructureStage,
    ProcessorStage,
)
from src.core.app.test_builder import ApplicationTestBuilder
from src.core.config.app_config import (
    AppConfig,
    AuthConfig,
    BackendConfig,
    BackendSettings,
)
from starlette.testclient import TestClient

pytestmark = [pytest.mark.no_global_mock]

_BASE = "https://api.runinfra.ai/v1"
_MODEL = "meta-llama/Llama-3.3-70B-Instruct"
_PROXY_MODEL = f"runinfra/{_MODEL}"


def _models_payload() -> dict[str, Any]:
    return {
        "object": "list",
        "data": [
            {
                "id": _MODEL,
                "object": "model",
                "created": 1700000000,
                "owned_by": "meta",
            },
            {
                "id": "deepseek-ai/DeepSeek-V3",
                "object": "model",
                "created": 1700000000,
                "owned_by": "deepseek",
            },
        ],
    }


def _chat_payload() -> dict[str, Any]:
    return {
        "id": "chatcmpl-runinfra-proof",
        "object": "chat.completion",
        "created": 1700000000,
        "model": _MODEL,
        "choices": [
            {
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": "Hello from runinfra.ai backend!",
                },
                "finish_reason": "stop",
            }
        ],
        "usage": {"prompt_tokens": 8, "completion_tokens": 6, "total_tokens": 14},
    }


def _chat_sse_stream() -> bytes:
    chunks = [
        {
            "id": "chatcmpl-runinfra-stream",
            "object": "chat.completion.chunk",
            "created": 1700000000,
            "model": _MODEL,
            "choices": [
                {
                    "index": 0,
                    "delta": {"role": "assistant", "content": "Hello"},
                    "finish_reason": None,
                }
            ],
        },
        {
            "id": "chatcmpl-runinfra-stream",
            "object": "chat.completion.chunk",
            "created": 1700000000,
            "model": _MODEL,
            "choices": [
                {
                    "index": 0,
                    "delta": {"content": " from runinfra!"},
                    "finish_reason": None,
                }
            ],
        },
        {
            "id": "chatcmpl-runinfra-stream",
            "object": "chat.completion.chunk",
            "created": 1700000000,
            "model": _MODEL,
            "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 8, "completion_tokens": 5, "total_tokens": 13},
        },
    ]
    lines = []
    for c in chunks:
        lines.append(f"data: {json.dumps(c)}\n\n")
    lines.append("data: [DONE]\n\n")
    return "".join(lines).encode("utf-8")


@pytest.fixture
async def proxy_app(respx_mock: MockRouter) -> Any:
    """Build and initialize in-process proxy app configured with runinfra backend."""
    respx_mock.get(f"{_BASE}/models").mock(
        return_value=httpx.Response(200, json=_models_payload())
    )
    respx_mock.post(f"{_BASE}/chat/completions").mock(
        return_value=httpx.Response(200, json=_chat_payload())
    )

    backends_dict = {
        "default_backend": "runinfra",
        "runinfra": BackendConfig(
            api_key="test-runinfra-key-12345",
            api_url=_BASE,
        ),
    }
    backends = BackendSettings.model_validate(backends_dict)
    config = AppConfig(backends=backends, auth=AuthConfig(disable_auth=True))

    builder = ApplicationTestBuilder()
    builder.add_stage(CoreServicesStage())
    builder.add_stage(InfrastructureStage())
    builder.add_stage(BackendStage())
    builder.add_stage(CommandStage())
    builder.add_stage(ProcessorStage())
    builder.add_stage(ControllerStage())

    return await builder.build(config)


def test_runinfra_models_discovery_endpoint(proxy_app: Any) -> None:
    """Proxy /models endpoint should list runinfra models."""
    with TestClient(proxy_app) as client:
        response = client.get("/v1/models")
        assert response.status_code == 200
        data = response.json()
        model_ids = [m["id"] for m in data.get("data", [])]
        assert _PROXY_MODEL in model_ids
        assert "runinfra/deepseek-ai/DeepSeek-V3" in model_ids


def test_runinfra_openai_chat_completions_non_streaming(
    proxy_app: Any, respx_mock: MockRouter
) -> None:
    """Test OpenAI chat completions non-streaming through proxy to runinfra."""
    respx_mock.post(f"{_BASE}/chat/completions").mock(
        return_value=httpx.Response(200, json=_chat_payload())
    )
    with TestClient(proxy_app) as client:
        response = client.post(
            "/v1/chat/completions",
            json={
                "model": _PROXY_MODEL,
                "messages": [{"role": "user", "content": "Hello"}],
                "stream": False,
            },
        )
        assert response.status_code == 200, response.text
        data = response.json()
        assert "choices" in data
        assert len(data["choices"]) > 0
        content = data["choices"][0]["message"]["content"]
        assert "runinfra.ai" in content

        # Check outbound upstream request received stripped model name and Bearer key
        upstream_call = respx_mock.calls.last
        assert upstream_call is not None
        req_json = json.loads(upstream_call.request.content.decode("utf-8"))
        assert req_json["model"] == _MODEL  # 'runinfra/' prefix is stripped
        assert (
            upstream_call.request.headers["authorization"]
            == "Bearer test-runinfra-key-12345"
        )
        assert "x-llmproxy-loop-guard" in upstream_call.request.headers


def test_runinfra_openai_chat_completions_streaming(
    proxy_app: Any, respx_mock: MockRouter
) -> None:
    """Test OpenAI chat completions streaming through proxy to runinfra."""
    respx_mock.post(f"{_BASE}/chat/completions").mock(
        return_value=httpx.Response(
            200,
            headers={"content-type": "text/event-stream"},
            content=_chat_sse_stream(),
        )
    )

    with TestClient(proxy_app) as client:
        response = client.post(
            "/v1/chat/completions",
            json={
                "model": _PROXY_MODEL,
                "messages": [{"role": "user", "content": "Hello"}],
                "stream": True,
            },
        )
        assert response.status_code == 200
        assert "text/event-stream" in response.headers.get("content-type", "")

        chunks: list[str] = []
        for line in response.text.splitlines():
            if line.startswith("data: ") and line != "data: [DONE]":
                chunk = json.loads(line[6:])
                choices = chunk.get("choices", [])
                if choices:
                    content = choices[0].get("delta", {}).get("content")
                    if content:
                        chunks.append(content)

        full_content = "".join(chunks)
        assert "Hello from runinfra!" in full_content


def test_runinfra_anthropic_messages_frontend_non_streaming(
    proxy_app: Any, respx_mock: MockRouter
) -> None:
    """Test Anthropic Messages frontend cross-API translation to runinfra backend."""
    respx_mock.post(f"{_BASE}/chat/completions").mock(
        return_value=httpx.Response(200, json=_chat_payload())
    )
    with TestClient(proxy_app) as client:
        response = client.post(
            "/anthropic/v1/messages",
            json={
                "model": _PROXY_MODEL,
                "messages": [{"role": "user", "content": "Hello Anthropic format"}],
                "max_tokens": 64,
                "stream": False,
            },
            headers={"anthropic-version": "2023-06-01"},
        )
        assert response.status_code == 200, response.text
        data = response.json()
        assert data.get("type") == "message"
        assert "content" in data
        assert len(data["content"]) > 0
        assert "runinfra.ai" in data["content"][0]["text"]


def test_runinfra_anthropic_messages_frontend_streaming(
    proxy_app: Any, respx_mock: MockRouter
) -> None:
    """Test Anthropic Messages frontend streaming translation to runinfra backend."""
    respx_mock.post(f"{_BASE}/chat/completions").mock(
        return_value=httpx.Response(
            200,
            headers={"content-type": "text/event-stream"},
            content=_chat_sse_stream(),
        )
    )

    with TestClient(proxy_app) as client:
        response = client.post(
            "/anthropic/v1/messages",
            json={
                "model": _PROXY_MODEL,
                "messages": [{"role": "user", "content": "Hello Anthropic streaming"}],
                "max_tokens": 64,
                "stream": True,
            },
            headers={"anthropic-version": "2023-06-01"},
        )
        assert response.status_code == 200
        event_types: list[str] = []
        for line in response.text.splitlines():
            if line.startswith("event: "):
                event_types.append(line.split(": ", 1)[1].strip())

        assert "message_start" in event_types
        assert "content_block_delta" in event_types
        assert "message_stop" in event_types


def test_runinfra_gemini_generate_content_frontend(
    proxy_app: Any, respx_mock: MockRouter
) -> None:
    """Test Gemini generateContent frontend cross-API translation to runinfra backend."""
    respx_mock.post(f"{_BASE}/chat/completions").mock(
        return_value=httpx.Response(200, json=_chat_payload())
    )
    with TestClient(proxy_app) as client:
        response = client.post(
            "/v1beta/models/test-model:generateContent",
            json={
                "model": _PROXY_MODEL,
                "contents": [
                    {
                        "role": "user",
                        "parts": [{"text": "Hello Gemini format"}],
                    }
                ],
            },
        )
        assert response.status_code == 200, response.text
        data = response.json()
        assert "candidates" in data, str(data)
        assert len(data["candidates"]) > 0, str(data)
        candidate = data["candidates"][0]
        text_parts = candidate["content"]["parts"]
        assert any(len(str(p.get("text", ""))) > 0 for p in text_parts), f"text_parts: {text_parts}"
