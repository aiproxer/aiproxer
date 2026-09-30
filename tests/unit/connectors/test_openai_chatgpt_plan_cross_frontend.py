"""Cross-frontend and coexistence tests for openai-chatgpt-plan (task 6.3)."""

from __future__ import annotations

import json
from collections.abc import AsyncIterator, Generator
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, Mock

import httpx
import pytest
from src.connectors.contracts import (
    ConnectorChatCompletionsRequest,
    ConnectorResponsesRequest,
)
from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector
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

ACCESS = "siwc-access-token-integration-placeholder"
PROFILE = "primary"


@pytest.fixture(autouse=True)
def _reset_ctx() -> Generator[None, None, None]:
    reset_active_responses_stream_context()
    yield
    reset_active_responses_stream_context()


class _Tokens:
    async def get_access_token(self, profile_id: str) -> str:
        del profile_id
        return ACCESS

    async def force_refresh(self, profile_id: str) -> None:
        del profile_id

    async def mark_needs_reauth(self, profile_id: str, reason: str) -> None:
        del profile_id, reason


def _sse_response(text: str = "ok") -> Mock:
    events = [
        (
            "response.created",
            {
                "type": "response.created",
                "response": {"id": "resp_int_1", "model": "gpt-4o"},
            },
        ),
        (
            "response.output_text.delta",
            {"type": "response.output_text.delta", "delta": text},
        ),
        (
            "response.output_item.done",
            {
                "type": "response.output_item.done",
                "output_index": 0,
                "item": {
                    "type": "function_call",
                    "id": "call_1",
                    "call_id": "call_1",
                    "name": "lookup",
                    "arguments": '{"q":"x"}',
                },
            },
        ),
        (
            "response.completed",
            {
                "type": "response.completed",
                "response": {
                    "id": "resp_int_1",
                    "status": "completed",
                    "model": "gpt-4o",
                    "output": [
                        {
                            "type": "function_call",
                            "id": "call_1",
                            "call_id": "call_1",
                            "name": "lookup",
                            "arguments": '{"q":"x"}',
                        }
                    ],
                    "usage": {
                        "input_tokens": 2,
                        "output_tokens": 2,
                        "total_tokens": 4,
                    },
                },
            },
        ),
    ]
    body = "".join(f"event: {n}\ndata: {json.dumps(p)}\n\n" for n, p in events).encode(
        "utf-8"
    )
    resp = Mock()
    resp.status_code = 200
    resp.headers = {"content-type": "text/event-stream"}

    async def _aiter() -> AsyncIterator[bytes]:
        yield body

    resp.aiter_bytes = MagicMock(return_value=_aiter())
    resp.aread = AsyncMock(return_value=body)
    resp.aclose = AsyncMock()
    return resp


def _connector(client: httpx.AsyncClient) -> OpenAIChatGPTPlanConnector:
    connector = OpenAIChatGPTPlanConnector(
        client=client,
        config=AppConfig(),
        translation_service=TranslationService(),
    )
    connector.disable_health_check()
    connector._chatgpt_plan_profile_id = PROFILE
    connector._chatgpt_plan_token_manager = _Tokens()  # type: ignore[assignment]
    return connector


@pytest.mark.asyncio
async def test_native_responses_tool_roundtrip_and_forced_upstream_stream() -> None:
    client = AsyncMock(spec=httpx.AsyncClient)
    client.build_request = MagicMock(return_value=MagicMock())
    client.send = AsyncMock(return_value=_sse_response())
    connector = _connector(client)
    native = {
        "model": "gpt-4o",
        "instructions": "Harness instructions must survive.",
        "tools": [
            {
                "type": "function",
                "name": "lookup",
                "parameters": {
                    "type": "object",
                    "properties": {"q": {"type": "string"}},
                },
            }
        ],
        "input": [
            {
                "type": "message",
                "role": "user",
                "content": [{"type": "input_text", "text": "call lookup"}],
            }
        ],
    }
    request = ConnectorResponsesRequest(
        request=CanonicalChatRequest(
            model="gpt-4o",
            messages=[ChatMessage(role="user", content="call lookup")],
            stream=True,
            tools=native["tools"],
            extra_body={RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY: native},
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
    assert isinstance(result, StreamingResponseEnvelope)
    payload = client.build_request.call_args[1]["json"]
    assert payload["stream"] is True
    assert payload["store"] is False
    assert payload["instructions"] == "Harness instructions must survive."
    assert payload["tools"][0]["name"] == "lookup"
    assert "chatgpt.com/backend-api" not in str(client.build_request.call_args[0][1])


@pytest.mark.asyncio
async def test_chat_completions_projects_system_and_forces_upstream_stream() -> None:
    client = AsyncMock(spec=httpx.AsyncClient)
    client.build_request = MagicMock(return_value=MagicMock())
    client.send = AsyncMock(return_value=_sse_response("done"))
    connector = _connector(client)
    chat_req = ConnectorChatCompletionsRequest(
        request=CanonicalChatRequest(
            model="gpt-4o",
            messages=[
                ChatMessage(role="system", content="High priority system rule"),
                ChatMessage(role="user", content="hi"),
            ],
            stream=False,
            system_prompt="High priority system rule",
        ),
        processed_messages=[],
        effective_model="gpt-4o",
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=None,
        options={},
    )
    result = await connector.chat_completions(chat_req)
    assert isinstance(result, ResponseEnvelope)
    payload = client.build_request.call_args[1]["json"]
    assert payload["stream"] is True
    assert payload.get("instructions") == "High priority system rule" or any(
        isinstance(item, dict) and item.get("role") == "developer"
        for key in ("input", "messages")
        for item in (payload.get(key) or [])
    )


@pytest.mark.asyncio
async def test_coexistence_does_not_import_codex_from_new_package() -> None:
    import src.connectors.openai_chatgpt_plan as plan_pkg
    import src.connectors.openai_codex  # noqa: F401

    package_dir = (
        Path(__file__).resolve().parents[3]
        / "src"
        / "connectors"
        / "openai_chatgpt_plan"
    )
    for path in package_dir.rglob("*.py"):
        text = path.read_text(encoding="utf-8")
        assert "src.connectors.openai_codex" not in text
        assert "_openai_codex_connector" not in text
    assert plan_pkg.OpenAIChatGPTPlanConnector.backend_type == "openai-chatgpt-plan"


@pytest.mark.asyncio
async def test_anthropic_translated_canonical_path_needs_no_client_family_adapter() -> (
    None
):
    """Anthropic frontend -> canonical -> SIWC; no client-family branching required."""
    client = AsyncMock(spec=httpx.AsyncClient)
    client.build_request = MagicMock(return_value=MagicMock())
    client.send = AsyncMock(return_value=_sse_response("anthropic-ok"))
    connector = _connector(client)
    translation = TranslationService()
    domain = translation.to_domain_request(
        {
            "model": "gpt-4o",
            "system": "Be terse",
            "messages": [{"role": "user", "content": "ping"}],
            "stream": False,
        },
        "anthropic",
    )
    # Route the translated domain request through the Chat Completions connector path
    chat_req = ConnectorChatCompletionsRequest(
        request=domain.model_copy(update={"model": "gpt-4o", "max_tokens": None}),
        processed_messages=[],
        effective_model="gpt-4o",
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=None,
        options={},
    )
    result = await connector.chat_completions(chat_req)
    assert isinstance(result, ResponseEnvelope)
    payload = client.build_request.call_args[1]["json"]
    assert payload["stream"] is True
    assert "opencode" not in str(payload).casefold()
    assert "codex" not in str(payload).casefold()


@pytest.mark.asyncio
async def test_coexistence_profile_state_does_not_cross_backends() -> None:
    plan_client = AsyncMock(spec=httpx.AsyncClient)
    plan_client.build_request = MagicMock(return_value=MagicMock())
    plan_client.send = AsyncMock(return_value=_sse_response("plan"))
    plan = _connector(plan_client)
    plan._chatgpt_plan_profile_id = "plan-profile-a"
    plan.set_chatgpt_plan_identity_fingerprint("fp-plan-a")

    from src.connectors.openai_responses import OpenAIResponsesConnector

    responses = OpenAIResponsesConnector(
        client=AsyncMock(spec=httpx.AsyncClient),
        config=AppConfig(),
        translation_service=TranslationService(),
    )
    # Plan connector keeps its own profile binding; sibling Responses connector has none
    assert plan._chatgpt_plan_profile_id == "plan-profile-a"
    assert getattr(responses, "_chatgpt_plan_profile_id", None) in (None, "")
    assert plan._chatgpt_plan_identity_fingerprint == "fp-plan-a"
    assert not hasattr(responses, "_chatgpt_plan_identity_fingerprint") or (
        getattr(responses, "_chatgpt_plan_identity_fingerprint", None) is None
    )
