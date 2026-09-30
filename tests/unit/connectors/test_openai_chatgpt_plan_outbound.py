"""Outbound public Responses wire tests for ChatGPT-plan (task 4.4).

Proves POST https://api.openai.com/v1/responses with a SIWC bearer token,
policy-projected JSON, and no Codex private URL/header identity.
Token placeholders must not use the sk- prefix.
"""

from __future__ import annotations

import json
from collections.abc import AsyncIterator, Generator
from pathlib import Path
from typing import Any
from unittest.mock import AsyncMock, MagicMock, Mock

import httpx
import pytest
from src.connectors.contracts import ConnectorResponsesRequest
from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector
from src.core.config.app_config import AppConfig
from src.core.domain.chat import CanonicalChatRequest, ChatMessage
from src.core.domain.responses import ResponseEnvelope
from src.core.domain.responses_native_wiring import (
    RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY,
)
from src.core.domain.translators.responses.streaming import (
    reset_active_responses_stream_context,
)
from src.core.interfaces.configuration import IAppIdentityConfig
from src.core.services.translation_service import TranslationService

PACKAGE_DIR = (
    Path(__file__).resolve().parents[3] / "src" / "connectors" / "openai_chatgpt_plan"
)
CONNECTOR_PATH = PACKAGE_DIR / "connector.py"

PROFILE_ID = "primary"
ACCESS_TOKEN = "siwc-access-token-placeholder-test"
PUBLIC_RESPONSES_URL = "https://api.openai.com/v1/responses"
DECOY_API_BASE = "https://example.invalid/v1"

FORBIDDEN_HEADER_NAMES = frozenset(
    {
        "originator",
        "codex-task-type",
        "chatgpt-account-id",
        "openai-beta",
        "conversation_id",
        "session_id",
        "version",
    }
)
FORBIDDEN_SOURCE_LITERALS = (
    "chatgpt.com/backend-api/codex",
    "chatgpt.com/backend-api",
    "backend-api/codex",
    "originator=codex_cli_rs",
)
FORBIDDEN_URL_SUBSTRINGS = (
    "chatgpt.com/backend-api",
    "backend-api/codex",
    "codex_cli_rs",
)

NATIVE_PAYLOAD: dict[str, Any] = {
    "model": "gpt-4o",
    "store": True,
    "previous_response_id": "resp_should_not_be_sent",
    "instructions": "Be concise.",
    "input": [
        {
            "type": "message",
            "role": "system",
            "content": [{"type": "input_text", "text": "High-priority harness rule."}],
        },
        {
            "type": "message",
            "role": "user",
            "content": [{"type": "input_text", "text": "What is the status?"}],
        },
    ],
}


@pytest.fixture(autouse=True)
def _reset_responses_stream_context() -> Generator[None, None, None]:
    reset_active_responses_stream_context()
    yield
    reset_active_responses_stream_context()


class _StubTokenManager:
    def __init__(self, token: str) -> None:
        self.token = token
        self.profile_ids: list[str] = []

    async def get_access_token(self, profile_id: str) -> str:
        self.profile_ids.append(profile_id)
        return self.token


class _InjectedIdentity(IAppIdentityConfig):
    def __init__(self, headers: dict[str, str]) -> None:
        self._headers = headers

    def get_resolved_headers(
        self, incoming_headers: dict[str, Any] | None
    ) -> dict[str, str]:
        del incoming_headers
        return dict(self._headers)


def _codex_like_identity() -> _InjectedIdentity:
    return _InjectedIdentity(
        {
            "originator": "codex_cli_rs",
            "version": "0.42.0",
            "Codex-Task-Type": "agent",
            "chatgpt-account-id": "acct_should_not_be_sent",
            "OpenAI-Beta": "responses=experimental",
            "conversation_id": "conv_should_not_be_sent",
            "session_id": "sess_should_not_be_sent",
            "User-Agent": "codex_cli_rs/0.42.0",
            "X-Title": "llm-interactive-proxy",
        }
    )


def _completed_sse_response() -> Mock:
    events = [
        (
            "response.created",
            {
                "type": "response.created",
                "response": {"id": "resp-123", "model": "gpt-4o"},
            },
        ),
        (
            "response.output_text.delta",
            {"type": "response.output_text.delta", "delta": "ok"},
        ),
        (
            "response.completed",
            {
                "type": "response.completed",
                "response": {
                    "id": "resp-123",
                    "object": "response",
                    "model": "gpt-4o",
                    "status": "completed",
                    "output": [
                        {
                            "type": "message",
                            "role": "assistant",
                            "content": [{"type": "output_text", "text": "ok"}],
                        }
                    ],
                    "usage": {
                        "input_tokens": 4,
                        "output_tokens": 1,
                        "total_tokens": 5,
                    },
                },
            },
        ),
    ]
    body = "".join(
        f"event: {name}\ndata: {json.dumps(payload)}\n\n" for name, payload in events
    ).encode("utf-8")
    mock_response = Mock()
    mock_response.status_code = 200
    mock_response.headers = {"content-type": "text/event-stream"}

    async def _aiter_bytes() -> AsyncIterator[bytes]:
        yield body

    mock_response.aiter_bytes = MagicMock(return_value=_aiter_bytes())
    mock_response.aread = AsyncMock(return_value=body)
    mock_response.aclose = AsyncMock()
    return mock_response


def _make_connector(
    mock_client: httpx.AsyncClient,
    *,
    token_manager: _StubTokenManager | None = None,
) -> tuple[OpenAIChatGPTPlanConnector, _StubTokenManager]:
    manager = token_manager or _StubTokenManager(ACCESS_TOKEN)
    connector = OpenAIChatGPTPlanConnector(
        client=mock_client,
        config=AppConfig(),
        translation_service=TranslationService(),
    )
    connector.disable_health_check()
    connector.api_base_url = DECOY_API_BASE
    connector._use_websocket = True
    connector._chatgpt_plan_profile_id = PROFILE_ID
    connector._chatgpt_plan_token_manager = manager
    return connector, manager


def _make_responses_request(
    connector: OpenAIChatGPTPlanConnector,
    extra_body: dict[str, Any],
    *,
    identity: _InjectedIdentity | None = None,
    options: dict[str, Any] | None = None,
) -> ConnectorResponsesRequest:
    request_data = CanonicalChatRequest(
        model="gpt-4o",
        messages=[ChatMessage(role="user", content="What is the status?")],
        stream=False,
        extra_body=extra_body,
    )
    return ConnectorResponsesRequest(
        request=request_data,
        processed_messages=[],
        effective_model="gpt-4o",
        identity=identity,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=None,
        options=options
        or {
            "openai_url": DECOY_API_BASE,
            "use_websocket": True,
        },
    )


def _captured_call(mock_client: Mock) -> Any:
    mock_client.build_request.assert_called()
    return mock_client.build_request.call_args


def _iter_connector_helper_sources() -> list[Path]:
    files = [CONNECTOR_PATH]
    for path in sorted(PACKAGE_DIR.glob("*.py")):
        if path.name.startswith("outbound") or path.name.endswith("_headers.py"):
            files.append(path)
    unique: list[Path] = []
    seen: set[Path] = set()
    for path in files:
        if path not in seen:
            unique.append(path)
            seen.add(path)
    return unique


class TestOpenAIChatGPTPlanOutboundWire:
    @pytest.fixture
    def mock_client(self) -> Mock:
        client = AsyncMock(spec=httpx.AsyncClient)
        client.build_request = MagicMock(return_value=MagicMock())
        client.send = AsyncMock(return_value=_completed_sse_response())
        return client

    @pytest.mark.asyncio
    async def test_responses_posts_exactly_public_v1_responses_url(
        self, mock_client: Mock
    ) -> None:
        connector, _manager = _make_connector(mock_client)
        extra_body = {
            RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY: dict(NATIVE_PAYLOAD),
        }
        result = await connector.responses(
            _make_responses_request(connector, extra_body)
        )

        assert isinstance(result, ResponseEnvelope)
        call_args = _captured_call(mock_client)
        assert call_args[0][0] == "POST"
        assert call_args[0][1] == PUBLIC_RESPONSES_URL
        url = str(call_args[0][1])
        for marker in FORBIDDEN_URL_SUBSTRINGS:
            assert marker not in url

    @pytest.mark.asyncio
    async def test_authorization_uses_siwc_bearer_without_sk_prefix(
        self, mock_client: Mock
    ) -> None:
        connector, manager = _make_connector(mock_client)
        extra_body = {
            RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY: dict(NATIVE_PAYLOAD),
        }
        await connector.responses(_make_responses_request(connector, extra_body))

        assert ACCESS_TOKEN.startswith("siwc-")
        assert "sk-" not in ACCESS_TOKEN
        assert manager.profile_ids == [PROFILE_ID]
        headers = _captured_call(mock_client)[1]["headers"]
        assert headers["Authorization"] == f"Bearer {ACCESS_TOKEN}"
        assert "sk-" not in headers["Authorization"]

    @pytest.mark.asyncio
    async def test_captured_headers_omit_codex_identity(
        self, mock_client: Mock
    ) -> None:
        connector, _manager = _make_connector(mock_client)
        extra_body = {
            RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY: dict(NATIVE_PAYLOAD),
        }
        await connector.responses(
            _make_responses_request(
                connector,
                extra_body,
                identity=_codex_like_identity(),
            )
        )

        headers = _captured_call(mock_client)[1]["headers"]
        lowered = {str(name).lower(): str(value) for name, value in headers.items()}
        for name in FORBIDDEN_HEADER_NAMES:
            assert name not in lowered
        user_agent = lowered.get("user-agent", "")
        for marker in ("codex_cli_rs", "originator"):
            assert marker not in user_agent.casefold()
        for value in lowered.values():
            folded = value.casefold()
            assert "codex_cli_rs" not in folded
            assert "responses=experimental" not in folded

    @pytest.mark.asyncio
    async def test_captured_json_is_policy_projected(self, mock_client: Mock) -> None:
        connector, _manager = _make_connector(mock_client)
        native = dict(NATIVE_PAYLOAD)
        extra_body = {RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY: native}
        request = _make_responses_request(connector, extra_body)
        original_extra = request.request.extra_body
        assert original_extra is not None
        original_native = original_extra[RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY]

        await connector.responses(request)

        payload = _captured_call(mock_client)[1]["json"]
        assert payload["store"] is False
        assert payload["stream"] is True
        assert "previous_response_id" not in payload
        for key in ("input", "messages"):
            items = payload.get(key)
            if not isinstance(items, list):
                continue
            for item in items:
                if isinstance(item, dict):
                    assert item.get("role") != "system"
        blob = str(payload)
        assert "<user_instructions>" not in blob
        assert request.request.extra_body is original_extra
        assert original_extra[RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY] is original_native
        assert original_native["store"] is True
        assert original_native["previous_response_id"] == "resp_should_not_be_sent"


class TestOpenAIChatGPTPlanOutboundSourceBoundary:
    def test_connector_source_forbids_codex_private_url_and_originator_literals(
        self,
    ) -> None:
        hits: list[str] = []
        for path in _iter_connector_helper_sources():
            assert path.is_file(), f"missing {path}"
            source = path.read_text(encoding="utf-8")
            for literal in FORBIDDEN_SOURCE_LITERALS:
                if literal in source:
                    hits.append(f"{path.as_posix()}: {literal}")
        assert hits == []
