"""Forced upstream streaming and terminal SIWC stream semantics (task 5.1)."""

from __future__ import annotations

import json
from collections.abc import AsyncIterator, Generator
from typing import Any
from unittest.mock import AsyncMock, MagicMock, Mock

import httpx
import pytest
from src.connectors.contracts import ConnectorResponsesRequest
from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector
from src.connectors.openai_chatgpt_plan.stream_accumulator import (
    ChatGPTPlanStreamError,
)
from src.core.config.app_config import AppConfig
from src.core.domain.chat import CanonicalChatRequest, ChatMessage
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.domain.responses_native_wiring import (
    RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY,
)
from src.core.domain.translators.responses.streaming import (
    reset_active_responses_stream_context,
    responses_to_domain_stream_chunk,
)
from src.core.services.translation_service import TranslationService

PROFILE_ID = "primary"
ACCESS_TOKEN = "siwc-access-token-placeholder-test"
PUBLIC_RESPONSES_URL = "https://api.openai.com/v1/responses"
RESP_ID = "resp_siwc_stream_1"


@pytest.fixture(autouse=True)
def _reset_responses_stream_context() -> Generator[None, None, None]:
    reset_active_responses_stream_context()
    yield
    reset_active_responses_stream_context()


class _StubTokenManager:
    def __init__(self, token: str) -> None:
        self.token = token

    async def get_access_token(self, profile_id: str) -> str:
        del profile_id
        return self.token

    async def force_refresh(self, profile_id: str) -> None:
        del profile_id

    async def mark_needs_reauth(self, profile_id: str, reason: str) -> None:
        del profile_id, reason


def _sse_bytes(events: list[tuple[str, dict[str, Any]]]) -> bytes:
    return "".join(
        f"event: {name}\ndata: {json.dumps(payload)}\n\n" for name, payload in events
    ).encode("utf-8")


def _completed_events(*, text: str = "hello") -> list[tuple[str, dict[str, Any]]]:
    return [
        (
            "response.created",
            {
                "type": "response.created",
                "response": {"id": RESP_ID, "model": "gpt-4o", "status": "in_progress"},
            },
        ),
        (
            "response.output_text.delta",
            {"type": "response.output_text.delta", "delta": text},
        ),
        (
            "response.completed",
            {
                "type": "response.completed",
                "response": {
                    "id": RESP_ID,
                    "object": "response",
                    "model": "gpt-4o",
                    "status": "completed",
                    "output": [
                        {
                            "type": "message",
                            "role": "assistant",
                            "content": [{"type": "output_text", "text": text}],
                        }
                    ],
                    "usage": {
                        "input_tokens": 3,
                        "output_tokens": 1,
                        "total_tokens": 4,
                    },
                },
            },
        ),
    ]


def _failed_events() -> list[tuple[str, dict[str, Any]]]:
    return [
        (
            "response.created",
            {
                "type": "response.created",
                "response": {"id": RESP_ID, "model": "gpt-4o"},
            },
        ),
        (
            "response.failed",
            {
                "type": "response.failed",
                "response": {
                    "id": RESP_ID,
                    "status": "failed",
                    "error": {"code": "server_error", "message": "upstream boom"},
                },
            },
        ),
    ]


def _incomplete_events() -> list[tuple[str, dict[str, Any]]]:
    return [
        (
            "response.created",
            {
                "type": "response.created",
                "response": {"id": RESP_ID, "model": "gpt-4o"},
            },
        ),
        (
            "response.output_text.delta",
            {"type": "response.output_text.delta", "delta": "partial"},
        ),
        (
            "response.incomplete",
            {
                "type": "response.incomplete",
                "response": {
                    "id": RESP_ID,
                    "status": "incomplete",
                    "incomplete_details": {"reason": "max_output_tokens"},
                },
            },
        ),
    ]


def _mock_stream_response(events: list[tuple[str, dict[str, Any]]]) -> Mock:
    body = _sse_bytes(events)
    mock_response = Mock()
    mock_response.status_code = 200
    mock_response.headers = {"content-type": "text/event-stream"}

    async def _aiter_bytes() -> AsyncIterator[bytes]:
        yield body

    mock_response.aiter_bytes = MagicMock(return_value=_aiter_bytes())
    mock_response.aread = AsyncMock(return_value=body)
    mock_response.aclose = AsyncMock()
    return mock_response


def _make_connector(mock_client: httpx.AsyncClient) -> OpenAIChatGPTPlanConnector:
    connector = OpenAIChatGPTPlanConnector(
        client=mock_client,
        config=AppConfig(),
        translation_service=TranslationService(),
    )
    connector.disable_health_check()
    connector.api_base_url = "https://example.invalid/v1"
    connector._use_websocket = False
    connector._chatgpt_plan_profile_id = PROFILE_ID
    connector._chatgpt_plan_token_manager = _StubTokenManager(ACCESS_TOKEN)
    return connector


def _make_request(*, stream: bool) -> ConnectorResponsesRequest:
    native = {
        "model": "gpt-4o",
        "instructions": "Be concise.",
        "input": [
            {
                "type": "message",
                "role": "user",
                "content": [{"type": "input_text", "text": "hi"}],
            }
        ],
    }
    request_data = CanonicalChatRequest(
        model="gpt-4o",
        messages=[ChatMessage(role="user", content="hi")],
        stream=stream,
        extra_body={RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY: native},
    )
    return ConnectorResponsesRequest(
        request=request_data,
        processed_messages=[],
        effective_model="gpt-4o",
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=None,
        options={},
    )


class TestResponsesIncompleteTranslation:
    def test_response_incomplete_is_terminal_error_chunk(self) -> None:
        out = responses_to_domain_stream_chunk(
            {
                "type": "response.incomplete",
                "response": {
                    "id": "resp_incomplete_1",
                    "status": "incomplete",
                    "incomplete_details": {"reason": "max_output_tokens"},
                },
            }
        )
        assert isinstance(out, dict)
        assert out.get("error")
        assert out["choices"][0].get("finish_reason") == "error"
        assert out["error"].get("code") == "response_incomplete"


class TestOpenAIChatGPTPlanForcedStreaming:
    @pytest.fixture
    def mock_client(self) -> Mock:
        client = AsyncMock(spec=httpx.AsyncClient)
        client.build_request = MagicMock(return_value=MagicMock())
        return client

    @pytest.mark.asyncio
    async def test_downstream_stream_true_returns_streaming_envelope(
        self, mock_client: Mock
    ) -> None:
        mock_client.send = AsyncMock(
            return_value=_mock_stream_response(_completed_events())
        )
        connector = _make_connector(mock_client)

        result = await connector.responses(_make_request(stream=True))

        assert isinstance(result, StreamingResponseEnvelope)
        payload = mock_client.build_request.call_args[1]["json"]
        assert payload["stream"] is True
        assert mock_client.build_request.call_args[0][1] == PUBLIC_RESPONSES_URL

        chunks: list[Any] = []
        assert result.content is not None
        async for chunk in result.content:
            chunks.append(chunk)
        assert chunks

    @pytest.mark.asyncio
    async def test_downstream_stream_false_accumulates_after_completed(
        self, mock_client: Mock
    ) -> None:
        mock_client.send = AsyncMock(
            return_value=_mock_stream_response(_completed_events(text="ok"))
        )
        connector = _make_connector(mock_client)

        result = await connector.responses(_make_request(stream=False))

        assert isinstance(result, ResponseEnvelope)
        payload = mock_client.build_request.call_args[1]["json"]
        assert payload["stream"] is True
        assert payload["store"] is False
        content = result.content
        assert isinstance(content, dict)
        blob = json.dumps(content)
        assert "ok" in blob
        assert "error" not in content or not content.get("error")

    @pytest.mark.asyncio
    async def test_response_failed_is_terminal_non_success(
        self, mock_client: Mock
    ) -> None:
        mock_client.send = AsyncMock(
            return_value=_mock_stream_response(_failed_events())
        )
        connector = _make_connector(mock_client)

        with pytest.raises(ChatGPTPlanStreamError) as exc_info:
            await connector.responses(_make_request(stream=False))

        details = exc_info.value.details or {}
        assert details.get("terminal_event") == "response.failed"
        assert "server_error" in str(details) or "boom" in str(exc_info.value).lower()

    @pytest.mark.asyncio
    async def test_response_incomplete_is_terminal_non_success(
        self, mock_client: Mock
    ) -> None:
        mock_client.send = AsyncMock(
            return_value=_mock_stream_response(_incomplete_events())
        )
        connector = _make_connector(mock_client)

        with pytest.raises(ChatGPTPlanStreamError) as exc_info:
            await connector.responses(_make_request(stream=False))

        details = exc_info.value.details or {}
        assert details.get("terminal_event") == "response.incomplete"
