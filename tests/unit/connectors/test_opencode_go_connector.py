"""Tests for the opencode-go backend connector."""

from __future__ import annotations

import json
from dataclasses import replace
from typing import Any, cast
from unittest.mock import MagicMock

import httpx
import pytest

opencode_go_module = pytest.importorskip("src.connectors.opencode_go")

from src.connectors.contracts import (
    ConnectorChatCompletionsRequest,
    ConnectorRequestContext,
)
from src.core.config.app_config import AppConfig
from src.core.domain.chat import CanonicalChatRequest, ChatMessage
from src.core.domain.models_listing import ModelsListingResponse
from src.core.services.translation_service import TranslationService

OPENCODE_GO_BASE_URL = "https://opencode.ai/zen/go/v1"
CURATED_OPENAI_MODELS = [
    "glm-5",
    "glm-5.1",
    "glm-5.2",
    "kimi-k2.5",
    "kimi-k2.6",
    "kimi-k2.7-code",
    "deepseek-v4-pro",
    "deepseek-v4-flash",
    "mimo-v2.5",
    "mimo-v2.5-pro",
    "mimo-v2-pro",
    "mimo-v2-omni",
]
CURATED_ANTHROPIC_MODELS = [
    "minimax-m3",
    "minimax-m2.5",
    "minimax-m2.7",
    "qwen3.7-max",
    "qwen3.7-plus",
    "qwen3.6-plus",
    "qwen3.5-plus",
]
CURATED_MODELS = CURATED_OPENAI_MODELS + CURATED_ANTHROPIC_MODELS


class RequestRecorder:
    def __init__(self, models_payload: dict[str, Any] | None = None) -> None:
        self.requests: list[httpx.Request] = []
        self.models_payload = models_payload or {
            "data": [{"id": model} for model in CURATED_MODELS]
        }

    def __call__(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        path = request.url.path.rstrip("/")

        if path.endswith("/models"):
            return httpx.Response(
                200,
                json=self.models_payload,
            )

        if path.endswith("/chat/completions"):
            return httpx.Response(
                200,
                json={
                    "id": "chatcmpl-opencode-go",
                    "object": "chat.completion",
                    "choices": [
                        {
                            "index": 0,
                            "message": {"role": "assistant", "content": "ok"},
                            "finish_reason": "stop",
                        }
                    ],
                },
            )

        if path.endswith("/messages"):
            return httpx.Response(
                200,
                json={
                    "id": "msg-opencode-go",
                    "type": "message",
                    "role": "assistant",
                    "content": [{"type": "text", "text": "ok"}],
                    "stop_reason": "end_turn",
                },
            )

        raise AssertionError(f"Unexpected request URL: {request.method} {request.url}")


async def _make_backend(
    client: httpx.AsyncClient,
    *,
    overrides: dict[str, str] | None = None,
) -> Any:
    config = MagicMock(spec=AppConfig)
    config.streaming_yield_interval = 0.0
    config.backends = MagicMock()

    backend = opencode_go_module.OpencodeGoBackend(
        client=client,
        config=config,
        translation_service=TranslationService(),
    )

    await backend.initialize(
        api_key="test-api-key",
        api_base_url=OPENCODE_GO_BASE_URL,
        openai_api_base_url=OPENCODE_GO_BASE_URL,
        anthropic_api_base_url=OPENCODE_GO_BASE_URL,
        key_name="opencode-go",
        model_protocol_overrides=dict(overrides or {}),
    )

    disable_health_check = getattr(backend, "disable_health_check", None)
    if callable(disable_health_check):
        disable_health_check()
    elif hasattr(backend, "_health_check_enabled"):
        backend._health_check_enabled = False

    return backend


def _make_request(
    model: str,
    *,
    stream: bool = False,
    extra_body: dict[str, Any] | None = None,
    messages: list[ChatMessage] | None = None,
    processed_messages: list[ChatMessage] | None = None,
    reasoning_effort: str | None = None,
) -> ConnectorChatCompletionsRequest:
    request_messages = messages or [ChatMessage(role="user", content="hello")]
    request_processed_messages = processed_messages or request_messages
    canonical_request = CanonicalChatRequest(
        model=model,
        messages=request_messages,
        max_tokens=16,
        stream=stream,
        extra_body=extra_body,
        reasoning_effort=reasoning_effort,
    )
    return ConnectorChatCompletionsRequest(
        request=canonical_request,
        processed_messages=request_processed_messages,
        effective_model=model,
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=ConnectorRequestContext(
            request_id="test-request-id",
            session_id="test-session-id",
            client_host="127.0.0.1",
            extensions={},
        ),
        options={},
    )


def _posted_json(requests: list[httpx.Request], path_suffix: str) -> dict[str, Any]:
    for request in requests:
        if request.method == "POST" and request.url.path.endswith(path_suffix):
            return cast(dict[str, Any], json.loads(request.content.decode("utf-8")))
    raise AssertionError(f"No POST request found for suffix {path_suffix!r}")


def _matching_request(requests: list[httpx.Request], path_suffix: str) -> httpx.Request:
    for request in requests:
        if request.method == "POST" and request.url.path.endswith(path_suffix):
            return request
    raise AssertionError(f"No POST request found for suffix {path_suffix!r}")


@pytest.mark.asyncio
async def test_openai_path_routes_curated_openai_models_to_chat_completions() -> None:
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)

        await backend.chat_completions(_make_request("opencode-go:glm-5.1"))

    assert any(
        request.method == "POST" and request.url.path.endswith("/chat/completions")
        for request in recorder.requests
    )
    assert not any(
        request.method == "POST" and request.url.path.endswith("/messages")
        for request in recorder.requests
    )

    payload = _posted_json(recorder.requests, "/chat/completions")
    assert payload["model"] == "glm-5.1"


@pytest.mark.asyncio
async def test_anthropic_path_routes_curated_anthropic_models_to_messages() -> None:
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)

        await backend.chat_completions(_make_request("opencode-go:minimax-m2.7"))

    assert any(
        request.method == "POST" and request.url.path.endswith("/messages")
        for request in recorder.requests
    )
    assert not any(
        request.method == "POST" and request.url.path.endswith("/chat/completions")
        for request in recorder.requests
    )

    payload = _posted_json(recorder.requests, "/messages")
    assert payload["model"] == "minimax-m2.7"


@pytest.mark.asyncio
@pytest.mark.parametrize("model", ["qwen3.7-max", "qwen3.6-plus", "qwen3.5-plus"])
async def test_qwen_models_route_to_anthropic_messages(model: str) -> None:
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)

        await backend.chat_completions(_make_request(f"opencode-go:{model}"))

    assert not any(
        request.method == "POST" and request.url.path.endswith("/chat/completions")
        for request in recorder.requests
    )

    request = _matching_request(recorder.requests, "/messages")
    payload = cast(dict[str, Any], json.loads(request.content.decode("utf-8")))
    assert payload["model"] == model
    assert request.headers["x-api-key"] == "test-api-key"
    assert "authorization" not in request.headers


@pytest.mark.asyncio
async def test_model_protocol_overrides_can_redirect_unknown_models() -> None:
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(
            client,
            overrides={"custom-openai-model": "openai"},
        )

        await backend.chat_completions(_make_request("opencode-go:custom-openai-model"))

    payload = _posted_json(recorder.requests, "/chat/completions")
    assert payload["model"] == "custom-openai-model"


@pytest.mark.asyncio
async def test_model_protocol_overrides_can_redirect_to_anthropic() -> None:
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(
            client,
            overrides={"custom-anthropic-model": "anthropic"},
        )

        await backend.chat_completions(
            _make_request("opencode-go:custom-anthropic-model")
        )

    payload = _posted_json(recorder.requests, "/messages")
    assert payload["model"] == "custom-anthropic-model"


@pytest.mark.asyncio
async def test_api_key_strips_leading_bearer_prefix() -> None:
    """Env/config sometimes includes ``Bearer ``; OpenAI stack re-adds it for /chat/completions."""
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)
    config = MagicMock(spec=AppConfig)
    config.streaming_yield_interval = 0.0
    config.backends = MagicMock()

    async with httpx.AsyncClient(transport=transport) as client:
        backend = opencode_go_module.OpencodeGoBackend(
            client=client,
            config=config,
            translation_service=TranslationService(),
        )
        await backend.initialize(
            api_key="Bearer  secret-token",
            api_base_url=OPENCODE_GO_BASE_URL,
            openai_api_base_url=OPENCODE_GO_BASE_URL,
            anthropic_api_base_url=OPENCODE_GO_BASE_URL,
            key_name="opencode-go",
            model_protocol_overrides={},
        )
        disable_health_check = getattr(backend, "disable_health_check", None)
        if callable(disable_health_check):
            disable_health_check()
        elif hasattr(backend, "_health_check_enabled"):
            backend._health_check_enabled = False

        await backend.chat_completions(_make_request("opencode-go:kimi-k2.5"))
        await backend.chat_completions(_make_request("opencode-go:minimax-m2.7"))

    openai_req = _matching_request(recorder.requests, "/chat/completions")
    anthropic_req = _matching_request(recorder.requests, "/messages")
    assert openai_req.headers["authorization"] == "Bearer secret-token"
    assert anthropic_req.headers["x-api-key"] == "secret-token"


@pytest.mark.asyncio
async def test_anthropic_path_uses_x_api_key_header() -> None:
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)

        await backend.chat_completions(_make_request("opencode-go:minimax-m2.7"))

    request = _matching_request(recorder.requests, "/messages")
    assert request.headers["x-api-key"] == "test-api-key"
    assert "authorization" not in request.headers


@pytest.mark.asyncio
async def test_openai_streaming_path_uses_raw_model_and_bearer_auth() -> None:
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)
        await backend.chat_completions(
            _make_request("opencode-go:opencode-go/kimi-k2.5", stream=True)
        )

    request = _matching_request(recorder.requests, "/chat/completions")
    payload = cast(dict[str, Any], json.loads(request.content.decode("utf-8")))
    assert payload["model"] == "kimi-k2.5"
    assert request.headers["authorization"] == "Bearer test-api-key"


@pytest.mark.asyncio
async def test_anthropic_streaming_path_uses_raw_model_and_x_api_key() -> None:
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)
        await backend.chat_completions(
            _make_request("opencode-go:minimax-m2.7", stream=True)
        )

    request = _matching_request(recorder.requests, "/messages")
    payload = cast(dict[str, Any], json.loads(request.content.decode("utf-8")))
    assert payload["model"] == "minimax-m2.7"
    assert request.headers["x-api-key"] == "test-api-key"
    assert "authorization" not in request.headers


@pytest.mark.asyncio
async def test_openai_endpoint_style_base_url_is_normalized() -> None:
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)
    config = MagicMock(spec=AppConfig)
    config.streaming_yield_interval = 0.0
    config.backends = MagicMock()

    async with httpx.AsyncClient(transport=transport) as client:
        backend = opencode_go_module.OpencodeGoBackend(
            client=client,
            config=config,
            translation_service=TranslationService(),
        )
        await backend.initialize(
            api_key="test-api-key",
            openai_api_base_url="https://opencode.ai/zen/go/v1/chat/completions",
            anthropic_api_base_url="https://opencode.ai/zen/go/v1/messages",
            key_name="opencode-go",
            model_protocol_overrides={},
        )
        backend.disable_health_check()

        await backend.chat_completions(_make_request("opencode-go:glm-5.1"))
        await backend.chat_completions(_make_request("opencode-go:minimax-m2.7"))

    openai_request = _matching_request(recorder.requests, "/chat/completions")
    anthropic_request = _matching_request(recorder.requests, "/messages")
    assert str(openai_request.url) == "https://opencode.ai/zen/go/v1/chat/completions"
    assert str(anthropic_request.url) == "https://opencode.ai/zen/go/v1/messages"


def test_provider_name_reports_openai_for_outer_connector() -> None:
    config = MagicMock(spec=AppConfig)
    config.streaming_yield_interval = 0.0
    config.backends = MagicMock()
    client = MagicMock(spec=httpx.AsyncClient)
    backend = opencode_go_module.OpencodeGoBackend(
        client=client,
        config=config,
        translation_service=TranslationService(),
    )

    assert backend.get_provider_name() == "openai"


@pytest.mark.asyncio
async def test_unknown_model_is_routed_to_openai_by_default() -> None:
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)

        await backend.chat_completions(_make_request("opencode-go:does-not-exist"))

    assert any(
        request.method == "POST" and request.url.path.endswith("/chat/completions")
        for request in recorder.requests
    )
    assert not any(
        request.method == "POST" and request.url.path.endswith("/messages")
        for request in recorder.requests
    )

    payload = _posted_json(recorder.requests, "/chat/completions")
    assert payload["model"] == "does-not-exist"


@pytest.mark.asyncio
async def test_available_models_are_canonically_prefixed() -> None:
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(
            client,
            overrides={"custom-openai-model": "openai"},
        )

        models = backend.get_available_models()
        async_models = await backend.get_available_models_async()

    expected = [f"opencode-go/{model}" for model in CURATED_MODELS]
    assert models[: len(expected)] == expected
    assert async_models[: len(expected)] == expected
    assert "opencode-go/custom-openai-model" in models
    assert "opencode-go/custom-openai-model" in async_models


@pytest.mark.asyncio
async def test_available_models_are_loaded_from_upstream_catalog() -> None:
    recorder = RequestRecorder(
        models_payload={
            "data": [
                {
                    "id": "kimi-k2.7-code",
                    "endpoint": "https://opencode.ai/zen/go/v1/chat/completions",
                    "ai_sdk_package": "@ai-sdk/openai-compatible",
                },
                {
                    "id": "minimax-m3",
                    "endpoint": "https://opencode.ai/zen/go/v1/messages",
                    "ai_sdk_package": "@ai-sdk/anthropic",
                },
            ]
        }
    )
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)
        models = backend.get_available_models()

        await backend.chat_completions(_make_request("opencode-go:kimi-k2.7-code"))
        await backend.chat_completions(_make_request("opencode-go:minimax-m3"))

    assert models == ["opencode-go/kimi-k2.7-code", "opencode-go/minimax-m3"]
    assert (
        _posted_json(recorder.requests, "/chat/completions")["model"]
        == "kimi-k2.7-code"
    )
    assert _posted_json(recorder.requests, "/messages")["model"] == "minimax-m3"


@pytest.mark.asyncio
async def test_legacy_kimi_k2_7_alias_is_remapped_to_wire_id() -> None:
    """User-supplied ``kimi-k2.7`` must hit the gateway as ``kimi-k2.7-code``."""
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)
        await backend.chat_completions(_make_request("opencode-go:kimi-k2.7"))
        await backend.chat_completions(
            _make_request("opencode-go:opencode-go/kimi-k2.7")
        )

    payloads = [
        cast(dict[str, Any], json.loads(r.content))
        for r in recorder.requests
        if r.method == "POST" and r.url.path.endswith("/chat/completions")
    ]
    assert len(payloads) == 2
    assert payloads[0]["model"] == "kimi-k2.7-code"
    assert payloads[1]["model"] == "kimi-k2.7-code"


@pytest.mark.asyncio
async def test_openai_payload_has_vendor_prefix_when_user_omits_it() -> None:
    """When user sends opencode-go:mimo-v2-pro, backend receives raw mimo-v2-pro."""
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)
        await backend.chat_completions(_make_request("opencode-go:mimo-v2-pro"))

    payload = _posted_json(recorder.requests, "/chat/completions")
    assert payload["model"] == "mimo-v2-pro"


@pytest.mark.asyncio
async def test_openai_payload_strips_extra_body_vendor_prefixed_model() -> None:
    """extra_body can repeat OpenCode config-style model ids; wire must stay raw."""
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)
        await backend.chat_completions(
            _make_request(
                "opencode-go:kimi-k2.5",
                extra_body={"model": "opencode-go/kimi-k2.5"},
            )
        )

    payload = _posted_json(recorder.requests, "/chat/completions")
    assert payload["model"] == "kimi-k2.5"


@pytest.mark.asyncio
async def test_openai_payload_strips_glm_rejected_metadata_and_reasoning() -> None:
    """OpenCode Go GLM rejects message metadata and top-level reasoning fields."""
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)
    messages = [
        ChatMessage(role="user", content="hello"),
        ChatMessage(
            role="assistant",
            content="memo",
            metadata={"source": "interleaved_thinking"},
        ),
    ]

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)
        await backend.chat_completions(
            _make_request(
                "opencode-go:glm-5.2",
                messages=messages,
                processed_messages=messages,
                reasoning_effort="high",
            )
        )

    payload = _posted_json(recorder.requests, "/chat/completions")
    assert "reasoning" not in payload
    assert "reasoning_effort" not in payload
    assert all("metadata" not in message for message in payload["messages"])


@pytest.mark.asyncio
async def test_anthropic_payload_strips_extra_body_vendor_prefixed_model() -> None:
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)
        await backend.chat_completions(
            _make_request(
                "opencode-go:minimax-m2.7",
                extra_body={"model": "opencode-go/minimax-m2.7"},
            )
        )

    payload = _posted_json(recorder.requests, "/messages")
    assert payload["model"] == "minimax-m2.7"


@pytest.mark.asyncio
async def test_anthropic_path_strips_thinking_and_beta_extra_body() -> None:
    """OpenCode Go /messages rejects interleaved-thinking / beta header shapes (HTTP 400)."""
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    base = _make_request(
        "opencode-go:minimax-m2.7",
        extra_body={
            "thinking": {"type": "enabled", "budget_tokens": 1024},
            "anthropic_beta": ["some-beta-flag"],
        },
    )
    connector_req = replace(
        base, request=base.request.model_copy(update={"reasoning_effort": "high"})
    )

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)
        await backend.chat_completions(connector_req)

    anthropic_req = _matching_request(recorder.requests, "/messages")
    assert anthropic_req.headers.get("anthropic-beta") is None

    payload = cast(dict[str, Any], json.loads(anthropic_req.content.decode("utf-8")))
    assert "thinking" not in payload
    assert "reasoning_effort" not in payload


@pytest.mark.asyncio
async def test_anthropic_path_converts_openai_tools_to_flat_messages_api_shape() -> (
    None
):
    """OpenCode Go rejects OpenAI-style tool wrappers (HTTP 400); use Anthropic flat tools."""
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    tools = [
        {
            "type": "function",
            "function": {
                "name": "do_thing",
                "description": "desc",
                "parameters": {
                    "type": "object",
                    "properties": {"x": {"type": "string"}},
                    "required": ["x"],
                },
            },
        }
    ]

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)
        await backend.chat_completions(
            _make_request("opencode-go:minimax-m2.7", extra_body={"tools": tools})
        )

    payload = _posted_json(recorder.requests, "/messages")
    assert "tools" in payload
    wire_tools = payload["tools"]
    assert len(wire_tools) == 1
    assert wire_tools[0] == {
        "name": "do_thing",
        "description": "desc",
        "input_schema": {
            "type": "object",
            "properties": {"x": {"type": "string"}},
            "required": ["x"],
        },
    }


@pytest.mark.asyncio
async def test_openai_payload_does_not_duplicate_vendor_prefix() -> None:
    """When user sends canonical opencode-go path, backend strips the vendor prefix."""
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)
        await backend.chat_completions(
            _make_request("opencode-go:opencode-go/mimo-v2-pro")
        )

    payload = _posted_json(recorder.requests, "/chat/completions")
    assert payload["model"] == "mimo-v2-pro"
    assert "opencode-go/" not in payload["model"]


@pytest.mark.asyncio
async def test_anthropic_payload_has_vendor_prefix_when_user_omits_it() -> None:
    """When user sends opencode-go:minimax-m2.7, Anthropic backend receives raw minimax-m2.7."""
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)
        await backend.chat_completions(_make_request("opencode-go:minimax-m2.7"))

    payload = _posted_json(recorder.requests, "/messages")
    assert payload["model"] == "minimax-m2.7"


@pytest.mark.asyncio
async def test_anthropic_payload_does_not_duplicate_vendor_prefix() -> None:
    """When user sends canonical opencode-go Anthropic model, backend strips the vendor prefix."""
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)
        await backend.chat_completions(
            _make_request("opencode-go:opencode-go/minimax-m2.7")
        )

    payload = _posted_json(recorder.requests, "/messages")
    assert payload["model"] == "minimax-m2.7"
    assert "opencode-go/" not in payload["model"]


@pytest.mark.asyncio
async def test_normalize_opencode_go_api_key_strips_bearer_prefix() -> None:
    norm = opencode_go_module._normalize_opencode_go_api_key
    assert norm("Bearer secret") == "secret"
    assert norm("  bearer  token  ") == "token"
    assert norm("plain-key") == "plain-key"


def test_normalize_model_name_strips_both_prefix_forms() -> None:
    """_normalize_model_name should strip opencode-go/ and opencode-go: forms
    back to the raw model id."""
    strip = opencode_go_module._normalize_model_name

    assert strip("mimo-v2-pro") == "mimo-v2-pro"
    assert strip("opencode-go/mimo-v2-pro") == "mimo-v2-pro"
    assert strip("opencode-go:mimo-v2-pro") == "mimo-v2-pro"
    assert strip("opencode-go:opencode-go/mimo-v2-pro") == "mimo-v2-pro"
    assert strip("") == ""
    assert strip("  glm-5.1  ") == "glm-5.1"


@pytest.mark.asyncio
async def test_list_models_returns_models_listing_response() -> None:
    """list_models fetches from the API and caches subsequent calls."""
    recorder = RequestRecorder()
    transport = httpx.MockTransport(recorder)

    async with httpx.AsyncClient(transport=transport) as client:
        backend = await _make_backend(client)

        result1 = await backend.list_models()
        result2 = await backend.list_models()

    models_get_count = sum(
        1
        for r in recorder.requests
        if r.method == "GET" and r.url.path.rstrip("/").endswith("/models")
    )
    assert models_get_count == 1

    assert isinstance(result1, ModelsListingResponse)
    assert isinstance(result2, ModelsListingResponse)
    assert result1 == result2

    expected_ids = CURATED_MODELS
    assert [m.id for m in result1.data] == expected_ids
    assert result1.object == "list"


@pytest.mark.asyncio
async def test_enumerator_returns_configured_models_when_specified() -> None:
    enumerator = opencode_go_module.OpencodeGoConfiguredModelEnumerator()
    config = opencode_go_module.BackendConfig(
        connector="opencode-go",
        api_key="test-key",
        models=["glm-5.1", "opencode-go/custom-model"],
    )

    result = await enumerator.enumerate("opencode-go", config)

    assert result.status == "available"
    assert result.source == "opencode_go_configured"
    assert result.models == ("opencode-go/glm-5.1", "opencode-go/custom-model")
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_enumerator_falls_back_to_curated_models_on_live_probe_failure(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    enumerator = opencode_go_module.OpencodeGoConfiguredModelEnumerator()
    config = opencode_go_module.BackendConfig(
        connector="opencode-go",
        api_key="test-key",
        api_url="http://127.0.0.1:1/invalid",
        extra={"model_discovery_timeout_seconds": 0.1},
    )

    result = await enumerator.enumerate("opencode-go", config)

    assert result.status == "available"
    assert result.source == "opencode_go_curated"
    assert "opencode-go/glm-5.1" in result.models
    assert "opencode-go/minimax-m2.7" in result.models
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_enumerator_returns_live_upstream_models_when_probe_succeeds(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    enumerator = opencode_go_module.OpencodeGoConfiguredModelEnumerator()
    config = opencode_go_module.BackendConfig(
        connector="opencode-go",
        api_key="test-key",
        extra={"model_protocol_overrides": {"custom-extra": "anthropic"}},
    )

    recorder = RequestRecorder(
        models_payload={"data": [{"id": "glm-5.1"}, {"id": "upstream-new-model"}]}
    )
    transport = httpx.MockTransport(recorder)

    real_async_client = httpx.AsyncClient

    def _mock_client(*args: Any, **kwargs: Any) -> httpx.AsyncClient:
        kwargs["transport"] = transport
        return real_async_client(*args, **kwargs)

    monkeypatch.setattr(httpx, "AsyncClient", _mock_client)

    result = await enumerator.enumerate("opencode-go", config)

    assert result.status == "available"
    assert result.source == "opencode_go_upstream"
    assert result.models == (
        "opencode-go/glm-5.1",
        "opencode-go/upstream-new-model",
        "opencode-go/custom-extra",
    )
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_enumerator_returns_unavailable_when_api_key_missing(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("OPENCODE_GO_API_KEY", raising=False)
    monkeypatch.delenv("OPENCODE_GO_API_KEY_1", raising=False)
    enumerator = opencode_go_module.OpencodeGoConfiguredModelEnumerator()
    config = opencode_go_module.BackendConfig(connector="opencode-go")

    result = await enumerator.enumerate("opencode-go", config)

    assert result.status == "unavailable"
    assert result.error_code == "missing_api_key"
    assert result.models == ()
