"""Cancellation, usage, diagnostics, and capture redaction (task 5.3)."""

from __future__ import annotations

import json
from collections.abc import AsyncIterator, Generator
from typing import Any
from unittest.mock import AsyncMock, MagicMock, Mock

import httpx
import pytest
from src.connectors.contracts import ConnectorResponsesRequest
from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector
from src.connectors.openai_chatgpt_plan.catalog import profile_identity_fingerprint
from src.connectors.openai_chatgpt_plan.models import (
    ChatGPTPlanProfile,
    redact_chatgpt_plan_mapping,
)
from src.core.config.app_config import AppConfig
from src.core.domain.chat import CanonicalChatRequest, ChatMessage
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.domain.responses_native_wiring import (
    RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY,
)
from src.core.domain.translators.responses.streaming import (
    reset_active_responses_stream_context,
)
from src.core.services.translation_service import TranslationService

PROFILE_ID = "primary"
ACCESS_TOKEN = "siwc-access-token-placeholder-test"
REFRESH_TOKEN = "siwc-refresh-token-placeholder-test"
ID_TOKEN = "siwc-id-token-placeholder-test"
RESP_ID = "resp_siwc_obs_1"


@pytest.fixture(autouse=True)
def _reset_responses_stream_context() -> Generator[None, None, None]:
    reset_active_responses_stream_context()
    yield
    reset_active_responses_stream_context()


class _StubTokenManager:
    def __init__(self, token: str = ACCESS_TOKEN) -> None:
        self.token = token

    async def get_access_token(self, profile_id: str) -> str:
        del profile_id
        return self.token

    async def force_refresh(self, profile_id: str) -> None:
        del profile_id

    async def mark_needs_reauth(self, profile_id: str, reason: str) -> None:
        del profile_id, reason


def _completed_sse(text: str = "hello") -> Mock:
    events = [
        (
            "response.created",
            {
                "type": "response.created",
                "response": {"id": RESP_ID, "model": "gpt-4o"},
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
                        "input_tokens": 11,
                        "output_tokens": 3,
                        "total_tokens": 14,
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


def _make_connector(mock_client: httpx.AsyncClient) -> OpenAIChatGPTPlanConnector:
    connector = OpenAIChatGPTPlanConnector(
        client=mock_client,
        config=AppConfig(),
        translation_service=TranslationService(),
    )
    connector.disable_health_check()
    connector._chatgpt_plan_profile_id = PROFILE_ID
    connector._chatgpt_plan_token_manager = _StubTokenManager()
    connector._chatgpt_plan_identity_fingerprint = "abc123fingerprint"
    return connector


def _make_request(*, stream: bool) -> ConnectorResponsesRequest:
    return ConnectorResponsesRequest(
        request=CanonicalChatRequest(
            model="gpt-4o",
            messages=[ChatMessage(role="user", content="hi")],
            stream=stream,
            extra_body={
                RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY: {
                    "model": "gpt-4o",
                    "input": [
                        {
                            "type": "message",
                            "role": "user",
                            "content": [{"type": "input_text", "text": "hi"}],
                        }
                    ],
                }
            },
        ),
        processed_messages=[],
        effective_model="gpt-4o",
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=None,
        options={},
    )


class TestChatGPTPlanObservability:
    @pytest.fixture
    def mock_client(self) -> Mock:
        client = AsyncMock(spec=httpx.AsyncClient)
        client.build_request = MagicMock(return_value=MagicMock())
        client.send = AsyncMock(return_value=_completed_sse())
        return client

    def test_log_extra_includes_safe_profile_diagnostics(
        self, mock_client: Mock
    ) -> None:
        connector = _make_connector(mock_client)
        extra = connector._get_log_extra(None)
        assert extra["profile_id"] == PROFILE_ID
        assert extra["profile_identity_fingerprint"] == "abc123fingerprint"
        blob = json.dumps(extra)
        assert ACCESS_TOKEN not in blob
        assert "siwc-access" not in blob

    @pytest.mark.asyncio
    async def test_streaming_response_exposes_cancel_callback(
        self, mock_client: Mock
    ) -> None:
        connector = _make_connector(mock_client)
        result = await connector.responses(_make_request(stream=True))
        assert isinstance(result, StreamingResponseEnvelope)
        assert result.cancel_callback is not None
        await result.cancel_callback()
        mock_client.send.return_value.aclose.assert_awaited()

    @pytest.mark.asyncio
    async def test_non_streaming_accumulation_preserves_usage(
        self, mock_client: Mock
    ) -> None:
        connector = _make_connector(mock_client)
        result = await connector.responses(_make_request(stream=False))
        assert isinstance(result, ResponseEnvelope)
        assert result.usage is not None
        usage = result.usage
        if isinstance(usage, dict):
            total = usage.get("total_tokens") or usage.get("total")
        else:
            total = getattr(usage, "total_tokens", None)
        assert total == 14


class TestChatGPTPlanCaptureRedaction:
    def test_profile_secret_fields_redacted_from_capture_payload(self) -> None:
        payload = {
            "profile_id": PROFILE_ID,
            "access_token": ACCESS_TOKEN,
            "refresh_token": REFRESH_TOKEN,
            "id_token": ID_TOKEN,
            "Authorization": f"Bearer {ACCESS_TOKEN}",
            "authorization": f"Bearer {ACCESS_TOKEN}",
            "pkce_verifier": "siwc-pkce-verifier-placeholder",
            "code": "siwc-auth-code-placeholder",
        }
        redacted = redact_chatgpt_plan_mapping(payload)
        blob = json.dumps(redacted)
        for secret in (ACCESS_TOKEN, REFRESH_TOKEN, ID_TOKEN):
            assert secret not in blob
        assert redacted["profile_id"] == PROFILE_ID
        assert "siwc-access-token-placeholder-test" not in blob

    def test_fingerprint_excludes_token_material(self) -> None:
        from datetime import datetime, timezone

        now = datetime.now(timezone.utc)
        profile = ChatGPTPlanProfile(
            profile_id=PROFILE_ID,
            issued_client_id="client-issued-1",
            issuer="https://auth.openai.com",
            subject="sub-123",
            access_token=ACCESS_TOKEN,
            refresh_token=REFRESH_TOKEN,
            id_token=ID_TOKEN,
            granted_scopes=("openid", "chatgpt.tokens.use.direct"),
            status="ready",
            created_at=now,
            updated_at=now,
        )
        fingerprint = profile_identity_fingerprint(profile)
        assert ACCESS_TOKEN not in fingerprint
        assert REFRESH_TOKEN not in fingerprint
        assert ID_TOKEN not in fingerprint
        assert len(fingerprint) == 64
