"""SIWC error mapping for ChatGPT-plan inference failures (task 5.2)."""

from __future__ import annotations

import pytest
from src.connectors.openai_chatgpt_plan.errors import (
    ChatGPTPlanAuthError,
    ChatGPTPlanCapabilityError,
    ChatGPTPlanErrorMapper,
    ChatGPTPlanQuotaError,
    ChatGPTPlanSubscriptionUnavailableError,
)
from src.core.common.exceptions import (
    BackendError,
    ResponsesProviderLimitationError,
)


class TestChatGPTPlanErrorMapper:
    def test_maps_subscription_sharing_usage_limit_exceeded(self) -> None:
        mapped = ChatGPTPlanErrorMapper().map_upstream_error(
            status_code=429,
            payload={
                "error": {
                    "code": "subscription_sharing_usage_limit_exceeded",
                    "message": "Plan allowance exhausted",
                }
            },
            profile_id="primary",
        )
        assert isinstance(mapped, ChatGPTPlanQuotaError)
        assert mapped.details.get("upstream_code") == (
            "subscription_sharing_usage_limit_exceeded"
        )
        assert mapped.details.get("profile_id") == "primary"
        assert mapped.details.get("profile_rotation") is False

    def test_maps_subscription_sharing_usage_unavailable(self) -> None:
        mapped = ChatGPTPlanErrorMapper().map_upstream_error(
            status_code=403,
            payload={
                "error": {
                    "code": "subscription_sharing_usage_unavailable",
                    "message": "Plan temporarily unavailable",
                }
            },
            profile_id="primary",
        )
        assert isinstance(mapped, ChatGPTPlanSubscriptionUnavailableError)
        assert mapped.details.get("upstream_code") == (
            "subscription_sharing_usage_unavailable"
        )

    def test_maps_unsupported_capability_codes(self) -> None:
        mapped = ChatGPTPlanErrorMapper().map_upstream_error(
            status_code=400,
            payload={
                "error": {
                    "code": "subscription_sharing_unsupported_capability",
                    "message": "Capability not supported",
                    "param": "temperature",
                }
            },
            profile_id="primary",
        )
        assert isinstance(mapped, ChatGPTPlanCapabilityError)
        assert mapped.details.get("retryable") is False

    def test_maps_route_not_supported(self) -> None:
        mapped = ChatGPTPlanErrorMapper().map_upstream_error(
            status_code=400,
            payload={
                "error": {
                    "code": "subscription_sharing_route_not_supported",
                    "message": "Route not supported",
                }
            },
            profile_id="primary",
        )
        assert isinstance(mapped, ChatGPTPlanCapabilityError)

    def test_maps_invalid_user_authorization_context(self) -> None:
        mapped = ChatGPTPlanErrorMapper().map_upstream_error(
            status_code=401,
            payload={
                "error": {
                    "code": "subscription_sharing_invalid_user",
                    "message": "Invalid user context",
                }
            },
            profile_id="primary",
        )
        assert isinstance(mapped, ChatGPTPlanAuthError)
        assert mapped.details.get("upstream_code") == (
            "subscription_sharing_invalid_user"
        )

    def test_auth_status_without_siwc_code_is_refresh_eligible(self) -> None:
        mapped = ChatGPTPlanErrorMapper().map_upstream_error(
            status_code=401,
            payload={
                "error": {"message": "Unauthorized", "type": "invalid_request_error"}
            },
            profile_id="primary",
        )
        assert isinstance(mapped, ChatGPTPlanAuthError)
        assert mapped.details.get("refresh_eligible") is True

    def test_does_not_retry_capability_rejection(self) -> None:
        mapper = ChatGPTPlanErrorMapper()
        err = ResponsesProviderLimitationError("temperature", "openai-chatgpt-plan")
        assert mapper.should_retry_same_body(err) is False
        mapped = mapper.map_upstream_error(
            status_code=400,
            payload={
                "error": {
                    "code": "subscription_sharing_unsupported_capability",
                    "message": "no",
                }
            },
            profile_id="p",
        )
        assert mapper.should_retry_same_body(mapped) is False

    def test_preserves_safe_backend_error_without_token_leak(self) -> None:
        mapped = ChatGPTPlanErrorMapper().map_exception(
            BackendError(
                message="token=secret-access-token-value boom",
                status_code=500,
                details={"error_payload": {"token": "secret-access-token-value"}},
            ),
            profile_id="primary",
        )
        text = str(mapped)
        assert "secret-access-token-value" not in text
        assert "secret-access-token-value" not in str(mapped.details)


class TestChatGPTPlanAuthRetryPolicy:
    def test_auth_error_is_refresh_eligible_once(self) -> None:
        err = ChatGPTPlanAuthError(
            "Access token rejected",
            details={"profile_id": "primary", "refresh_eligible": True},
        )
        mapper = ChatGPTPlanErrorMapper()
        assert mapper.should_refresh_and_retry(err, already_retried=False) is True
        assert mapper.should_refresh_and_retry(err, already_retried=True) is False

    def test_quota_error_never_rotates_or_retries_as_auth(self) -> None:
        err = ChatGPTPlanQuotaError(
            "Plan exhausted",
            details={
                "profile_id": "primary",
                "upstream_code": "subscription_sharing_usage_limit_exceeded",
                "profile_rotation": False,
            },
        )
        mapper = ChatGPTPlanErrorMapper()
        assert mapper.should_refresh_and_retry(err, already_retried=False) is False
        assert mapper.should_rotate_profile(err) is False


class _RefreshTokenManager:
    def __init__(self) -> None:
        self.tokens = ["siwc-access-stale", "siwc-access-fresh"]
        self.force_refresh_calls = 0
        self.mark_calls: list[str] = []
        self.get_calls = 0

    async def get_access_token(self, profile_id: str) -> str:
        del profile_id
        idx = min(self.get_calls, len(self.tokens) - 1)
        self.get_calls += 1
        return self.tokens[idx]

    async def force_refresh(self, profile_id: str) -> None:
        del profile_id
        self.force_refresh_calls += 1

    async def mark_needs_reauth(self, profile_id: str, reason: str) -> None:
        del profile_id
        self.mark_calls.append(reason)


@pytest.mark.asyncio
async def test_connector_refreshes_once_on_401_then_succeeds() -> None:
    from collections.abc import AsyncIterator
    from unittest.mock import AsyncMock, MagicMock, Mock

    import httpx
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
    from src.core.services.translation_service import TranslationService

    reset_active_responses_stream_context()
    manager = _RefreshTokenManager()

    def _sse(ok: bool) -> Mock:
        if ok:
            events = [
                (
                    "response.created",
                    {"type": "response.created", "response": {"id": "resp_ok"}},
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
                            "id": "resp_ok",
                            "status": "completed",
                            "model": "gpt-4o",
                            "output": [
                                {
                                    "type": "message",
                                    "role": "assistant",
                                    "content": [{"type": "output_text", "text": "ok"}],
                                }
                            ],
                            "usage": {
                                "input_tokens": 1,
                                "output_tokens": 1,
                                "total_tokens": 2,
                            },
                        },
                    },
                ),
            ]
            body = "".join(
                f"event: {n}\ndata: {__import__('json').dumps(p)}\n\n"
                for n, p in events
            ).encode()
            status = 200
        else:
            body = (
                b'{"error":{"message":"Unauthorized","type":"invalid_request_error"}}'
            )
            status = 401
        resp = Mock()
        resp.status_code = status
        resp.headers = {
            "content-type": "text/event-stream" if ok else "application/json"
        }
        resp.text = body.decode("utf-8", errors="replace")
        resp.json = Mock(return_value={"error": {"message": "Unauthorized"}})

        async def _aiter_bytes() -> AsyncIterator[bytes]:
            yield body

        async def _aread() -> bytes:
            return body

        resp.aiter_bytes = MagicMock(return_value=_aiter_bytes())
        resp.aread = AsyncMock(side_effect=_aread)
        resp.aclose = AsyncMock()
        return resp

    client = AsyncMock(spec=httpx.AsyncClient)
    client.build_request = MagicMock(return_value=MagicMock())
    client.send = AsyncMock(side_effect=[_sse(False), _sse(True)])

    connector = OpenAIChatGPTPlanConnector(
        client=client,
        config=AppConfig(),
        translation_service=TranslationService(),
    )
    connector.disable_health_check()
    connector._chatgpt_plan_profile_id = "primary"
    connector._chatgpt_plan_token_manager = manager  # type: ignore[assignment]

    request = ConnectorResponsesRequest(
        request=CanonicalChatRequest(
            model="gpt-4o",
            messages=[ChatMessage(role="user", content="hi")],
            stream=False,
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

    result = await connector.responses(request)
    assert isinstance(result, ResponseEnvelope)
    assert manager.force_refresh_calls == 1
    assert client.send.await_count == 2
    reset_active_responses_stream_context()
