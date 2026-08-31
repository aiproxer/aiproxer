"""Tests for Runinfra OpenAI-compatible connector."""

from __future__ import annotations

from typing import Any
from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest
from src.connectors.contracts import ConnectorChatCompletionsRequest
from src.connectors.runinfra import (
    RUNINFRA_BACKEND_TYPE,
    RUNINFRA_DEFAULT_BASE_URL,
    RuninfraConfiguredModelEnumerator,
    RuninfraConnector,
)
from src.core.config.app_config import AppConfig, BackendConfig
from src.core.domain.chat import CanonicalChatRequest, ChatMessage
from src.core.domain.responses import ResponseEnvelope
from src.core.services.backend_registry import backend_registry
from src.core.services.translation_service import TranslationService


def _backend(client: httpx.AsyncClient | None = None) -> RuninfraConnector:
    return RuninfraConnector(
        client=client or httpx.AsyncClient(),
        config=AppConfig(),
        translation_service=TranslationService(),
    )


def _make_request(
    request_data: CanonicalChatRequest, effective_model: str
) -> ConnectorChatCompletionsRequest:
    return ConnectorChatCompletionsRequest(
        request=request_data,
        processed_messages=list(request_data.messages),
        effective_model=effective_model,
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=None,
    )


def test_registered_in_backend_registry() -> None:
    """Connector should be registered in backend_registry under 'runinfra'."""
    factory = backend_registry.get_backend_factory("runinfra")
    assert factory is RuninfraConnector


@pytest.mark.asyncio
async def test_initialize_uses_explicit_api_key() -> None:
    """Connector should use provided api_key and default base URL."""
    client = AsyncMock(spec=httpx.AsyncClient)
    response = MagicMock()
    response.status_code = 200
    response.json.return_value = {"data": [{"id": "meta-llama/Llama-3.3-70B-Instruct"}]}
    client.get.return_value = response

    connector = _backend(client)
    await connector.initialize(api_key="provided-runinfra-key")

    assert connector.api_key == "provided-runinfra-key"
    assert connector.api_base_url == RUNINFRA_DEFAULT_BASE_URL
    assert connector.backend_type == RUNINFRA_BACKEND_TYPE
    assert connector.available_models == ["meta-llama/Llama-3.3-70B-Instruct"]

    await_args = client.get.await_args
    assert await_args.args[0] == f"{RUNINFRA_DEFAULT_BASE_URL}/models"
    headers = await_args.kwargs["headers"]
    assert headers["Authorization"] == "Bearer provided-runinfra-key"
    assert "x-llmproxy-loop-guard" in headers


@pytest.mark.asyncio
async def test_initialize_strips_whitespace_and_bearer() -> None:
    """Connector should normalize whitespace and leading 'Bearer ' prefix."""
    client = AsyncMock(spec=httpx.AsyncClient)
    response = MagicMock()
    response.status_code = 200
    response.json.return_value = {"data": []}
    client.get.return_value = response

    connector = _backend(client)
    await connector.initialize(api_key="  Bearer   my-runinfra-key  ")

    assert connector.api_key == "my-runinfra-key"
    headers = client.get.await_args.kwargs["headers"]
    assert headers["Authorization"] == "Bearer my-runinfra-key"


@pytest.mark.asyncio
async def test_initialize_uses_env_api_key(monkeypatch: pytest.MonkeyPatch) -> None:
    """Connector should fall back to RUNINFRA_API_KEY when no key is provided."""
    monkeypatch.setenv("RUNINFRA_API_KEY", "env-runinfra-key")

    client = AsyncMock(spec=httpx.AsyncClient)
    response = MagicMock()
    response.status_code = 200
    response.json.return_value = {"data": [{"id": "deepseek-ai/DeepSeek-V3"}]}
    client.get.return_value = response

    connector = _backend(client)
    await connector.initialize()

    assert connector.api_key == "env-runinfra-key"
    assert connector.available_models == ["deepseek-ai/DeepSeek-V3"]


@pytest.mark.asyncio
async def test_initialize_allows_base_url_override() -> None:
    """Connector should support custom api_base_url."""
    client = AsyncMock(spec=httpx.AsyncClient)
    response = MagicMock()
    response.status_code = 200
    response.json.return_value = {"data": []}
    client.get.return_value = response

    connector = _backend(client)
    await connector.initialize(
        api_key="test-key", api_base_url="https://custom.runinfra.endpoint/v1"
    )

    assert connector.api_base_url == "https://custom.runinfra.endpoint/v1"
    await_args = client.get.await_args
    assert await_args.args[0] == "https://custom.runinfra.endpoint/v1/models"


@pytest.mark.asyncio
async def test_initialize_empty_models_when_no_key(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Without API key, model discovery should not run and models list remains empty."""
    monkeypatch.delenv("RUNINFRA_API_KEY", raising=False)
    client = AsyncMock(spec=httpx.AsyncClient)
    connector = _backend(client)
    await connector.initialize()

    assert connector.api_key is None
    assert connector.available_models == []
    client.get.assert_not_awaited()


def test_get_headers_bearer_shape() -> None:
    """Authorization header should use standard Bearer token shape."""
    connector = _backend()
    connector.api_key = "secret-runinfra-key"

    headers = connector.get_headers(identity=None)
    assert headers["Authorization"] == "Bearer secret-runinfra-key"
    assert "x-llmproxy-loop-guard" in headers


@pytest.mark.asyncio
async def test_prepare_payload_strips_vendor_prefix() -> None:
    """Payload preparation should strip 'runinfra/' vendor prefix from model name."""
    connector = _backend()
    connector.api_key = "test-key"

    request_data = CanonicalChatRequest(
        model="runinfra/meta-llama/Llama-3.3-70B-Instruct",
        messages=[ChatMessage(role="user", content="Hello")],
        stream=False,
    )
    processed_messages = [{"role": "user", "content": "Hello"}]

    payload = await connector._prepare_payload(
        request_data=request_data,
        processed_messages=processed_messages,
        effective_model="runinfra/meta-llama/Llama-3.3-70B-Instruct",
    )

    assert payload["model"] == "meta-llama/Llama-3.3-70B-Instruct"
    assert payload["messages"] == [{"role": "user", "content": "Hello"}]
    assert payload["stream"] is False


def test_get_available_models_adds_vendor_prefix() -> None:
    """get_available_models should return models with 'runinfra/' prefix."""
    connector = _backend()
    connector.available_models = [
        "meta-llama/Llama-3.3-70B-Instruct",
        "deepseek-ai/DeepSeek-V3",
    ]

    models = connector.get_available_models()
    assert models == [
        "runinfra/meta-llama/Llama-3.3-70B-Instruct",
        "runinfra/deepseek-ai/DeepSeek-V3",
    ]


@pytest.mark.asyncio
async def test_non_streaming_chat_completions() -> None:
    """Non-streaming request should produce a valid ResponseEnvelope."""
    response = httpx.Response(
        status_code=200,
        headers={"content-type": "application/json"},
        json={
            "id": "chatcmpl-runinfra-123",
            "object": "chat.completion",
            "model": "meta-llama/Llama-3.3-70B-Instruct",
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": "Hello from Runinfra!"},
                    "finish_reason": "stop",
                }
            ],
            "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
        },
    )
    client = AsyncMock(spec=httpx.AsyncClient)
    client.send = AsyncMock(return_value=response)
    client.post = AsyncMock(return_value=response)

    connector = _backend(client)
    connector.api_key = "test-key"

    request_data = CanonicalChatRequest(
        model="runinfra/meta-llama/Llama-3.3-70B-Instruct",
        messages=[ChatMessage(role="user", content="Hello")],
        stream=False,
    )
    req = _make_request(request_data, "runinfra/meta-llama/Llama-3.3-70B-Instruct")

    result = await connector._chat_completions_canonical(req)
    assert isinstance(result, ResponseEnvelope)
    assert isinstance(result.content, dict)
    assert result.content["choices"][0]["message"]["content"] == "Hello from Runinfra!"


@pytest.mark.asyncio
async def test_runinfra_enumerator_uses_explicit_models() -> None:
    """Explicit config.models should be returned with runinfra/ prefix."""
    enumerator = RuninfraConfiguredModelEnumerator()
    config = BackendConfig(
        connector="runinfra",
        models=["meta-llama/Llama-3.3-70B-Instruct", "runinfra/custom-model"],
    )

    result = await enumerator.enumerate("runinfra", config)

    assert result.status == "available"
    assert result.source == "runinfra_configured"
    assert result.models == (
        "runinfra/meta-llama/Llama-3.3-70B-Instruct",
        "runinfra/custom-model",
    )
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_runinfra_enumerator_returns_live_upstream_models(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Enumerator should query upstream /models and return models with runinfra/ prefix."""
    enumerator = RuninfraConfiguredModelEnumerator()
    config = BackendConfig(
        connector="runinfra",
        api_key="test-key",
    )

    class MockTransport(httpx.AsyncBaseTransport):
        async def handle_async_request(self, request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                200,
                json={
                    "data": [
                        {"id": "meta-llama/Llama-3.3-70B-Instruct"},
                        {"id": "deepseek-ai/DeepSeek-V3"},
                    ]
                },
            )

    real_async_client = httpx.AsyncClient

    def _mock_client(*args: Any, **kwargs: Any) -> httpx.AsyncClient:
        kwargs["transport"] = MockTransport()
        return real_async_client(*args, **kwargs)

    monkeypatch.setattr(httpx, "AsyncClient", _mock_client)

    result = await enumerator.enumerate("runinfra", config)

    assert result.status == "available"
    assert result.source == "runinfra_upstream"
    assert result.models == (
        "runinfra/meta-llama/Llama-3.3-70B-Instruct",
        "runinfra/deepseek-ai/DeepSeek-V3",
    )
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_runinfra_enumerator_falls_back_to_curated_on_failure() -> None:
    """Enumerator should fall back to curated models on upstream failure."""
    enumerator = RuninfraConfiguredModelEnumerator()
    config = BackendConfig(
        connector="runinfra",
        api_key="test-key",
        api_url="http://127.0.0.1:1/invalid",
        extra={"model_discovery_timeout_seconds": 0.1},
    )

    result = await enumerator.enumerate("runinfra", config)

    assert result.status == "available"
    assert result.source == "runinfra_curated"
    assert "runinfra/meta-llama/Llama-3.3-70B-Instruct" in result.models
    assert "runinfra/deepseek-ai/DeepSeek-V3" in result.models
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_runinfra_enumerator_returns_unavailable_when_missing_api_key(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Enumerator should return unavailable when no API key is found."""
    monkeypatch.delenv("RUNINFRA_API_KEY", raising=False)
    monkeypatch.delenv("RUNINFRA_API_KEY_1", raising=False)

    enumerator = RuninfraConfiguredModelEnumerator()
    config = BackendConfig(connector="runinfra")

    result = await enumerator.enumerate("runinfra", config)

    assert result.status == "unavailable"
    assert result.error_code == "missing_api_key"
    assert result.models == ()
