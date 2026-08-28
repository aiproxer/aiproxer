"""Tests for CommandCode Anthropic Messages connector."""

from __future__ import annotations

import os
from typing import Any
from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest
from src.connectors.commandcode_anthropic import (
    COMMANDCODE_ANTHROPIC_BACKEND_TYPE,
    COMMANDCODE_ANTHROPIC_DEFAULT_BASE_URL,
    CommandCodeAnthropicConfiguredModelEnumerator,
    CommandCodeAnthropicConnector,
)
from src.connectors.contracts import ConnectorChatCompletionsRequest
from src.core.common.exceptions import ConfigurationError
from src.core.config.app_config import AppConfig, BackendConfig
from src.core.domain.chat import CanonicalChatRequest, ChatMessage
from src.core.domain.responses import ResponseEnvelope
from src.core.services.backend_registry import backend_registry
from src.core.services.translation_service import TranslationService


def _backend(client: httpx.AsyncClient | None = None) -> CommandCodeAnthropicConnector:
    return CommandCodeAnthropicConnector(
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
    """Connector should be registered for both commandcode-anthropic and commandcode_anthropic."""
    cls1 = backend_registry.get_backend_factory("commandcode-anthropic")
    cls2 = backend_registry.get_backend_factory("commandcode_anthropic")
    assert cls1 is CommandCodeAnthropicConnector
    assert cls2 is CommandCodeAnthropicConnector


@pytest.mark.asyncio
async def test_initialize_uses_explicit_api_key() -> None:
    """Connector should use provided api_key and default base URL."""
    client = AsyncMock(spec=httpx.AsyncClient)
    response = MagicMock()
    response.status_code = 200
    response.json.return_value = {"data": [{"id": "claude-3-5-sonnet-20241022"}]}
    client.get.return_value = response

    connector = _backend(client)
    await connector.initialize(api_key="provided-test-key")

    assert connector.api_key == "provided-test-key"
    assert connector.anthropic_api_base_url == COMMANDCODE_ANTHROPIC_DEFAULT_BASE_URL
    assert connector.backend_type == COMMANDCODE_ANTHROPIC_BACKEND_TYPE
    assert connector.auth_header_name == "x-api-key"
    assert connector.available_models == ["claude-3-5-sonnet-20241022"]
    assert connector.get_available_models() == ["claude-3-5-sonnet-20241022"]

    await_args = client.get.await_args
    assert await_args.args[0] == f"{COMMANDCODE_ANTHROPIC_DEFAULT_BASE_URL}/models"
    headers = await_args.kwargs["headers"]
    assert headers["x-api-key"] == "provided-test-key"


@pytest.mark.asyncio
async def test_initialize_uses_env_api_key(monkeypatch: pytest.MonkeyPatch) -> None:
    """Connector should fall back to COMMANDCODE_API_KEY when no key is provided."""
    monkeypatch.setenv("COMMANDCODE_API_KEY", "env-commandcode-key")

    client = AsyncMock(spec=httpx.AsyncClient)
    response = MagicMock()
    response.status_code = 200
    response.json.return_value = {"data": [{"id": "claude-haiku-4-5-20251001"}]}
    client.get.return_value = response

    connector = _backend(client)
    await connector.initialize()

    assert connector.api_key == "env-commandcode-key"
    assert connector.available_models == ["claude-haiku-4-5-20251001"]


@pytest.mark.asyncio
async def test_initialize_allows_base_url_override() -> None:
    """Connector should support custom api_base_url or anthropic_api_base_url."""
    client = AsyncMock(spec=httpx.AsyncClient)
    response = MagicMock()
    response.status_code = 200
    response.json.return_value = {"data": []}
    client.get.return_value = response

    connector = _backend(client)
    await connector.initialize(
        api_key="test-key", api_base_url="https://custom.endpoint/provider/v1"
    )

    assert connector.anthropic_api_base_url == "https://custom.endpoint/provider/v1"
    await_args = client.get.await_args
    assert await_args.args[0] == "https://custom.endpoint/provider/v1/models"


@pytest.mark.asyncio
async def test_initialize_raises_when_no_api_key(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Connector should raise ConfigurationError when no API key is provided."""
    monkeypatch.delenv("COMMANDCODE_API_KEY", raising=False)

    connector = _backend()
    with pytest.raises(ConfigurationError, match="COMMANDCODE_API_KEY"):
        await connector.initialize()


@pytest.mark.asyncio
async def test_prepare_anthropic_payload_formats_messages() -> None:
    """Payload preparation should format Anthropic Messages correctly."""
    connector = _backend()
    connector.api_key = "test-key"

    request_data = CanonicalChatRequest(
        model="claude-3-5-sonnet-20241022",
        messages=[
            ChatMessage(role="system", content="Be helpful"),
            ChatMessage(role="user", content="Hello"),
        ],
        stream=True,
    )
    processed_messages = [
        {"role": "system", "content": "Be helpful"},
        {"role": "user", "content": "Hello"},
    ]

    payload = connector._prepare_anthropic_payload(
        request_data=request_data,
        processed_messages=processed_messages,
        effective_model="claude-3-5-sonnet-20241022",
        project=None,
    )

    assert payload["model"] == "claude-3-5-sonnet-20241022"
    assert payload["system"] == "Be helpful"
    assert payload["messages"] == [{"role": "user", "content": "Hello"}]


@pytest.mark.asyncio
async def test_non_streaming_anthropic_messages() -> None:
    """Non-streaming request should produce a valid ResponseEnvelope."""
    req_http = httpx.Request("POST", "https://api.commandcode.ai/provider/v1/messages")
    response = httpx.Response(
        status_code=200,
        headers={"content-type": "application/json"},
        json={
            "id": "msg_123",
            "type": "message",
            "role": "assistant",
            "model": "claude-3-5-sonnet-20241022",
            "content": [{"type": "text", "text": "Hello from Claude!"}],
            "stop_reason": "end_turn",
            "usage": {"input_tokens": 10, "output_tokens": 5},
        },
        request=req_http,
    )
    client = AsyncMock(spec=httpx.AsyncClient)
    client.send = AsyncMock(return_value=response)
    client.post = AsyncMock(return_value=response)

    connector = _backend(client)
    connector.api_key = "test-key"

    request_data = CanonicalChatRequest(
        model="claude-3-5-sonnet-20241022",
        messages=[ChatMessage(role="user", content="Hello")],
        stream=False,
    )
    req = _make_request(request_data, "claude-3-5-sonnet-20241022")

    result = await connector._chat_completions_canonical(req)
    assert isinstance(result, ResponseEnvelope)
    assert isinstance(result.content, dict)
    assert result.content["choices"][0]["message"]["content"] == "Hello from Claude!"


@pytest.mark.asyncio
async def test_live_commandcode_anthropic_model_listing() -> None:
    """Live model listing verification against CommandCode API when key is set."""
    api_key = os.getenv("COMMANDCODE_API_KEY")
    if not api_key:
        pytest.skip("COMMANDCODE_API_KEY not set in environment")

    async with httpx.AsyncClient(timeout=30.0) as client:
        connector = CommandCodeAnthropicConnector(
            client=client,
            config=AppConfig(),
            translation_service=TranslationService(),
        )
        await connector.initialize(api_key=api_key)

        assert len(connector.available_models) > 0
        claude_models = [m for m in connector.available_models if "claude" in m]
        assert len(claude_models) > 0


@pytest.mark.asyncio
async def test_enumerator_returns_configured_models() -> None:
    enumerator = CommandCodeAnthropicConfiguredModelEnumerator()
    config = BackendConfig(
        connector="commandcode-anthropic",
        api_key="test-key",
        models=[
            "claude-3-5-sonnet-20241022",
            "commandcode-anthropic/claude-haiku-4-5-20251001",
        ],
    )

    result = await enumerator.enumerate("commandcode-anthropic", config)

    assert result.status == "available"
    assert result.source == "commandcode_anthropic_configured"
    assert result.models == (
        "commandcode-anthropic/claude-3-5-sonnet-20241022",
        "commandcode-anthropic/claude-haiku-4-5-20251001",
    )
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_enumerator_returns_live_upstream_models(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    enumerator = CommandCodeAnthropicConfiguredModelEnumerator()
    config = BackendConfig(
        connector="commandcode-anthropic",
        api_key="test-key",
    )

    class MockTransport(httpx.AsyncBaseTransport):
        async def handle_async_request(self, request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                200,
                json={
                    "data": [
                        {"id": "claude-3-5-sonnet-20241022"},
                        {"id": "custom-anthropic-model"},
                    ]
                },
            )

    real_async_client = httpx.AsyncClient

    def _mock_client(*args: Any, **kwargs: Any) -> httpx.AsyncClient:
        kwargs["transport"] = MockTransport()
        return real_async_client(*args, **kwargs)

    monkeypatch.setattr(httpx, "AsyncClient", _mock_client)

    result = await enumerator.enumerate("commandcode-anthropic", config)

    assert result.status == "available"
    assert result.source == "commandcode_anthropic_upstream"
    assert result.models == (
        "commandcode-anthropic/claude-3-5-sonnet-20241022",
        "commandcode-anthropic/custom-anthropic-model",
    )
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_enumerator_falls_back_to_curated_on_failure() -> None:
    enumerator = CommandCodeAnthropicConfiguredModelEnumerator()
    config = BackendConfig(
        connector="commandcode-anthropic",
        api_key="test-key",
        api_url="http://127.0.0.1:1/invalid",
        extra={"model_discovery_timeout_seconds": 0.1},
    )

    result = await enumerator.enumerate("commandcode-anthropic", config)

    assert result.status == "available"
    assert result.source == "commandcode_anthropic_curated"
    assert "commandcode-anthropic/claude-3-5-sonnet-20241022" in result.models
    assert "commandcode-anthropic/Qwen/Qwen3.7-Flash" in result.models
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_enumerator_returns_unavailable_when_missing_api_key(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("COMMANDCODE_API_KEY", raising=False)
    monkeypatch.delenv("COMMANDCODE_API_KEY_1", raising=False)

    enumerator = CommandCodeAnthropicConfiguredModelEnumerator()
    config = BackendConfig(connector="commandcode-anthropic")

    result = await enumerator.enumerate("commandcode-anthropic", config)

    assert result.status == "unavailable"
    assert result.error_code == "missing_api_key"
    assert result.models == ()
