from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from src.connectors.contracts import ConnectorChatCompletionsRequest
from src.connectors.openai_codex import OpenAICodexConnector
from src.core.common.exceptions import InvalidRequestError
from src.core.config.app_config import AppConfig
from src.core.domain.chat import CanonicalChatRequest, ChatMessage
from src.core.services.translation_service import TranslationService


def _make_connector() -> OpenAICodexConnector:
    client = AsyncMock()
    config = AppConfig()
    mock_translation_service = AsyncMock(spec=TranslationService)
    connector = OpenAICodexConnector(
        client=client, config=config, translation_service=mock_translation_service
    )
    connector.is_functional = True
    connector.api_key = "token"
    connector._auth_credentials = {"tokens": {"access_token": "token"}}
    return connector


async def _fake_validate_runtime_credentials(self: OpenAICodexConnector) -> bool:
    return True


async def _fake_load_auth(self: OpenAICodexConnector) -> bool:
    return True


@pytest.mark.asyncio
async def test_openai_codex_degrades_on_http_auth_error(monkeypatch):
    connector = _make_connector()

    mock_codex_call = AsyncMock(
        side_effect=InvalidRequestError(
            message="invalid token", status_code=401, details={}
        )
    )

    monkeypatch.setattr(
        OpenAICodexConnector,
        "_validate_runtime_credentials",
        _fake_validate_runtime_credentials,
    )
    monkeypatch.setattr(OpenAICodexConnector, "_load_auth", _fake_load_auth)
    monkeypatch.setattr(
        OpenAICodexConnector, "_call_codex_responses_api", mock_codex_call
    )

    with pytest.raises(InvalidRequestError):
        request = CanonicalChatRequest(
            model="gpt-5.5",
            messages=[ChatMessage(role="user", content="test")],
        )
        connector_req = ConnectorChatCompletionsRequest(
            request=request,
            processed_messages=list(request.messages),
            effective_model="gpt-5.5",
            identity=None,
            cancellation_token=None,
            cancellation_coordinator=None,
            context=None,
            options={},
        )
        await connector.chat_completions(connector_req)

    assert connector.is_functional is False


@pytest.mark.asyncio
async def test_connector_reads_pre_resolved_uri_params_from_extra_body(monkeypatch):
    connector = _make_connector()

    captured_request = None

    async def capturing_codex_call(
        self, request_data, processed_messages, effective_model, domain_request, **kw
    ):
        nonlocal captured_request
        captured_request = request_data

        async def mock_stream():
            yield MagicMock()

        return mock_stream()

    monkeypatch.setattr(
        OpenAICodexConnector,
        "_validate_runtime_credentials",
        _fake_validate_runtime_credentials,
    )
    monkeypatch.setattr(OpenAICodexConnector, "_load_auth", _fake_load_auth)
    monkeypatch.setattr(
        OpenAICodexConnector, "_call_codex_responses_api", capturing_codex_call
    )
    monkeypatch.setattr(OpenAICodexConnector, "_is_codex_model", lambda s, m: True)

    with patch(
        "src.connectors._openai_codex_connector.parse_model_with_params"
    ) as mock_parse:
        request = CanonicalChatRequest(
            model="gpt-5.4",
            messages=[ChatMessage(role="user", content="test")],
            extra_body={"_resolved_uri_params": {"reasoning_effort": "high"}},
        )
        connector_req = ConnectorChatCompletionsRequest(
            request=request,
            processed_messages=list(request.messages),
            effective_model="gpt-5.4",
            identity=None,
            cancellation_token=None,
            cancellation_coordinator=None,
            context=None,
            options={},
        )
        await connector.chat_completions(connector_req)

        mock_parse.assert_not_called()

    assert captured_request is not None
    assert captured_request._codex_resolved_reasoning_effort == "high"


@pytest.mark.asyncio
async def test_connector_falls_back_to_request_field_when_no_pre_resolved_params(
    monkeypatch,
):
    connector = _make_connector()

    captured_request = None

    async def capturing_codex_call(
        self, request_data, processed_messages, effective_model, domain_request, **kw
    ):
        nonlocal captured_request
        captured_request = request_data

        async def mock_stream():
            yield MagicMock()

        return mock_stream()

    monkeypatch.setattr(
        OpenAICodexConnector,
        "_validate_runtime_credentials",
        _fake_validate_runtime_credentials,
    )
    monkeypatch.setattr(OpenAICodexConnector, "_load_auth", _fake_load_auth)
    monkeypatch.setattr(
        OpenAICodexConnector, "_call_codex_responses_api", capturing_codex_call
    )
    monkeypatch.setattr(OpenAICodexConnector, "_is_codex_model", lambda s, m: True)

    with patch(
        "src.connectors._openai_codex_connector.parse_model_with_params"
    ) as mock_parse:
        request = CanonicalChatRequest(
            model="gpt-5.4",
            messages=[ChatMessage(role="user", content="test")],
            reasoning_effort="high",
            extra_body={},
        )
        connector_req = ConnectorChatCompletionsRequest(
            request=request,
            processed_messages=list(request.messages),
            effective_model="gpt-5.4",
            identity=None,
            cancellation_token=None,
            cancellation_coordinator=None,
            context=None,
            options={},
        )
        await connector.chat_completions(connector_req)

        mock_parse.assert_not_called()

    assert captured_request is not None
    assert captured_request._codex_resolved_reasoning_effort == "high"
