"""Regression coverage for Responses API routing compatibility."""

from __future__ import annotations

import json
from types import SimpleNamespace
from typing import cast
from unittest.mock import AsyncMock

import pytest
from fastapi import Request
from src.core.app.controllers.responses_controller import ResponsesController
from src.core.common.exceptions import ResponsesProviderLimitationError
from src.core.domain.backend_target import BackendTarget
from src.core.domain.chat import CanonicalChatRequest, ChatMessage
from src.core.domain.request_context import RequestContext
from src.core.domain.responses import (
    ProcessedResponse,
    ResponseEnvelope,
    StreamingResponseEnvelope,
)
from src.core.domain.responses_api import ResponsesRequest
from src.core.domain.responses_domain import (
    ResponsesContentPart,
    ResponsesInputItem,
    ResponsesOutputItem,
)
from src.core.domain.responses_event_normalizer import ResponsesStreamSource
from src.core.domain.responses_native_wiring import (
    ACP_RESPONSES_STANDALONE_MODE_KEY,
    RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY,
)

from tests.utils.responses_controller_test_deps import (
    build_responses_controller_backend_kwargs,
)


class _StubTranslationService:
    def __init__(self) -> None:
        self._domain_request = CanonicalChatRequest(
            model="gpt-test",
            messages=[ChatMessage(role="user", content="stub")],
            stream=False,
        )

    def to_domain_request(
        self, request: object, source_format: str
    ) -> CanonicalChatRequest:
        return self._domain_request

    def from_domain_request(
        self, request: CanonicalChatRequest, target_format: str
    ) -> dict[str, object]:
        return {"model": request.model, "target_format": target_format}

    def to_domain_response(self, response: object, source_format: str) -> object:
        return response

    def from_domain_response(self, response: object, target_format: str) -> object:
        return response


def _make_request() -> Request:
    scope = {
        "type": "http",
        "method": "POST",
        "path": "/v1/responses",
        "headers": [],
        "client": ("127.0.0.1", 12345),
        "app": SimpleNamespace(state=SimpleNamespace()),
    }

    async def receive() -> dict[str, object]:
        return {"type": "http.request", "body": b"", "more_body": False}

    request = Request(scope, receive=receive)
    request.state.request_id = "req-regression-responses-routing"
    return request


def _responses_request(**kwargs: object) -> ResponsesRequest:
    payload: dict[str, object] = {"model": "alias:minimax", "input": "hello"}
    payload.update(kwargs)
    return ResponsesRequest.model_validate(payload)


@pytest.mark.asyncio
async def test_prepare_responses_execution_accepts_legacy_openai_style_backend_targets() -> (
    None
):
    """Responses routing should treat OpenAI-compatible backends like `opencode-go` as supported.

    Regression evidence: the 2026-04-20 10:34:13 log/capture shows `/v1/responses`
    requests for `alias:minimax` failing after the first composite leaf resolves to `ollama`,
    even though later leaves include OpenAI-compatible backends (`opencode-go`, `opencode-zen`).
    """

    kwargs = build_responses_controller_backend_kwargs()
    kwargs["backend_model_resolver"].resolve_target = AsyncMock(
        return_value=BackendTarget(
            backend="opencode-go",
            model="minimax-m2.7",
            uri_params={},
        )
    )

    controller = ResponsesController(
        AsyncMock(),
        translation_service=_StubTranslationService(),
        **kwargs,
    )

    _, canonical, stream_source, _ = await controller._prepare_responses_execution(
        responses_request=_responses_request(),
    )

    assert stream_source is ResponsesStreamSource.OPENAI_RESPONSES
    assert canonical.model == "opencode-go:minimax-m2.7"
    assert canonical.extra_body is not None
    assert RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY in canonical.extra_body


@pytest.mark.asyncio
async def test_handle_responses_request_succeeds_for_alias_that_can_fall_through_to_legacy_openai_backend() -> (
    None
):
    """Composite aliases used from `/v1/responses` should remain usable when a later leaf is compatible.

    This is the user-visible regression from the 2026-04-20 10:34:13 failure: the
    client asked for `alias:minimax`, whose configured replacement includes
    `opencode-go:minimax-m2.7`, but the request died with `responses_api.routing`
    before a Responses-compatible branch could be used.
    """

    kwargs = build_responses_controller_backend_kwargs()
    kwargs["backend_model_resolver"].resolve_target = AsyncMock(
        return_value=BackendTarget(
            backend="opencode-go",
            model="minimax-m2.7",
            uri_params={},
        )
    )

    processor = AsyncMock()
    processor.process_request.return_value = ResponseEnvelope(
        content={
            "id": "resp-minimax-ok",
            "object": "response",
            "output": [],
        }
    )

    controller = ResponsesController(
        processor,
        translation_service=_StubTranslationService(),
        **kwargs,
    )

    response = await controller.handle_responses_request(
        _make_request(),
        _responses_request(model="alias:minimax", input="hello"),
    )

    assert response.status_code == 200
    processor.process_request.assert_awaited_once()
    domain_request = cast(
        CanonicalChatRequest, processor.process_request.await_args.args[1]
    )
    assert domain_request.model == "opencode-go:minimax-m2.7"
    assert domain_request.extra_body is not None
    assert RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY in domain_request.extra_body


@pytest.mark.asyncio
async def test_prepare_responses_execution_projects_cursor_acp_to_canonical_chat() -> (
    None
):
    kwargs = build_responses_controller_backend_kwargs()
    kwargs["backend_model_resolver"].resolve_target = AsyncMock(
        return_value=BackendTarget(
            backend="cursor-cli-acp.default",
            model="cursor/glm-5.2-max",
            uri_params={},
        )
    )
    controller = ResponsesController(
        AsyncMock(),
        translation_service=_StubTranslationService(),
        **kwargs,
    )

    _, canonical, stream_source, _ = await controller._prepare_responses_execution(
        responses_request=_responses_request(
            model="cursor-cli-acp:cursor/glm-5.2-max",
            input=[
                {"type": "message", "role": "system", "content": "system"},
                {"type": "message", "role": "developer", "content": "developer"},
                {"type": "message", "role": "user", "content": "question"},
                {"type": "message", "role": "assistant", "content": "answer"},
            ],
            stream=True,
        ),
    )

    assert stream_source is ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS
    assert canonical.model == "cursor-cli-acp.default:cursor/glm-5.2-max"
    assert canonical.stream is True
    assert [message.role for message in canonical.messages] == [
        "system",
        "developer",
        "user",
        "assistant",
    ]
    assert [
        message.to_dict()["content"][0]["text"] for message in canonical.messages
    ] == [
        "system",
        "developer",
        "question",
        "answer",
    ]
    assert RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY not in (canonical.extra_body or {})


@pytest.mark.asyncio
async def test_cursor_responses_conversation_ids_isolate_and_follow_previous_response() -> (
    None
):
    kwargs = build_responses_controller_backend_kwargs()
    kwargs["backend_model_resolver"].resolve_target = AsyncMock(
        return_value=BackendTarget(
            backend="cursor-cli-acp.default",
            model="cursor/glm-5.2-max",
            uri_params={},
        )
    )
    processor = AsyncMock()
    processor.process_request.side_effect = [
        ResponseEnvelope(
            content={
                "id": "resp-conversation-a",
                "object": "response",
                "output": [],
            }
        ),
        ResponseEnvelope(
            content={
                "id": "resp-conversation-b",
                "object": "response",
                "output": [],
            }
        ),
        ResponseEnvelope(
            content={
                "id": "resp-conversation-a-next",
                "object": "response",
                "output": [],
            }
        ),
    ]
    controller = ResponsesController(
        processor,
        translation_service=_StubTranslationService(),
        **kwargs,
    )

    await controller.handle_responses_request(
        _make_request(),
        _responses_request(
            model="cursor-cli-acp.default:cursor/glm-5.2-max",
            session_id="shared-client-session",
        ),
    )
    await controller.handle_responses_request(
        _make_request(),
        _responses_request(
            model="cursor-cli-acp.default:cursor/glm-5.2-max",
            session_id="shared-client-session",
        ),
    )
    await controller.handle_responses_request(
        _make_request(),
        _responses_request(
            model="cursor-cli-acp.default:cursor/glm-5.2-max",
            previous_response_id="resp-conversation-a",
        ),
    )

    first = cast(
        CanonicalChatRequest, processor.process_request.await_args_list[0].args[1]
    )
    second = cast(
        CanonicalChatRequest, processor.process_request.await_args_list[1].args[1]
    )
    chained = cast(
        CanonicalChatRequest, processor.process_request.await_args_list[2].args[1]
    )
    assert first.session_id
    assert second.session_id
    assert first.session_id != second.session_id
    assert chained.session_id
    assert chained.session_id not in {first.session_id, second.session_id}
    assert (first.extra_body or {}).get(ACP_RESPONSES_STANDALONE_MODE_KEY) is True
    assert (chained.extra_body or {}).get(ACP_RESPONSES_STANDALONE_MODE_KEY) is True


@pytest.mark.asyncio
async def test_cursor_chained_turn_replays_complete_history_after_runtime_reap() -> (
    None
):
    kwargs = build_responses_controller_backend_kwargs()
    kwargs["backend_model_resolver"].resolve_target = AsyncMock(
        return_value=BackendTarget(
            backend="cursor-cli-acp.default",
            model="cursor/glm-5.2-max",
            uri_params={},
        )
    )
    store = kwargs["responses_session_store"]
    await store.store(
        "resp_previous",
        [
            ResponsesOutputItem(
                id="assistant_1",
                type="message",
                role="assistant",
                status="completed",
                content=[ResponsesContentPart(type="output_text", text="first answer")],
            )
        ],
        history_items=[
            ResponsesInputItem(
                type="message",
                role="user",
                content="original prompt",
            ),
            ResponsesOutputItem(
                id="assistant_1",
                type="message",
                role="assistant",
                status="completed",
                content=[ResponsesContentPart(type="output_text", text="first answer")],
            ),
        ],
    )

    controller = ResponsesController(
        AsyncMock(),
        translation_service=_StubTranslationService(),
        **kwargs,
    )
    _, canonical, _, _ = await controller._prepare_responses_execution(
        responses_request=_responses_request(
            model="cursor-cli-acp:cursor/glm-5.2-max",
            previous_response_id="resp_previous",
            input="follow up",
        )
    )

    assert [message.role for message in canonical.messages] == [
        "user",
        "assistant",
        "user",
    ]
    assert [
        message.to_dict()["content"][0]["text"] for message in canonical.messages
    ] == ["original prompt", "first answer", "follow up"]


@pytest.mark.asyncio
async def test_prepare_responses_execution_rejects_cursor_acp_tools() -> None:
    kwargs = build_responses_controller_backend_kwargs()
    kwargs["backend_model_resolver"].resolve_target = AsyncMock(
        return_value=BackendTarget(
            backend="cursor-cli-acp.default",
            model="cursor/glm-5.2-max",
            uri_params={},
        )
    )
    controller = ResponsesController(
        AsyncMock(),
        translation_service=_StubTranslationService(),
        **kwargs,
    )

    with pytest.raises(ResponsesProviderLimitationError) as exc_info:
        await controller._prepare_responses_execution(
            responses_request=_responses_request(
                model="cursor-cli-acp:cursor/glm-5.2-max",
                tools=[
                    {
                        "type": "function",
                        "name": "read_file",
                        "description": "Read a file",
                        "parameters": {"type": "object", "properties": {}},
                    }
                ],
            ),
        )

    assert exc_info.value.feature == "tools"
    assert exc_info.value.provider == "cursor-cli-acp"


@pytest.mark.asyncio
async def test_prepare_responses_execution_rejects_conflicting_cursor_model_effort() -> (
    None
):
    kwargs = build_responses_controller_backend_kwargs()
    kwargs["backend_model_resolver"].resolve_target = AsyncMock(
        return_value=BackendTarget(
            backend="cursor-cli-acp.default",
            model="cursor/glm-5.2-max",
            uri_params={},
        )
    )
    controller = ResponsesController(
        AsyncMock(),
        translation_service=_StubTranslationService(),
        **kwargs,
    )

    with pytest.raises(ResponsesProviderLimitationError) as exc_info:
        await controller._prepare_responses_execution(
            responses_request=_responses_request(
                model="cursor-cli-acp:cursor/glm-5.2-max",
                reasoning={"effort": "high"},
            ),
        )

    assert exc_info.value.feature == "reasoning.effort=high"


@pytest.mark.asyncio
async def test_cursor_acp_non_streaming_chat_response_becomes_responses_object() -> (
    None
):
    kwargs = build_responses_controller_backend_kwargs()
    kwargs["backend_model_resolver"].resolve_target = AsyncMock(
        return_value=BackendTarget(
            backend="cursor-cli-acp.default",
            model="cursor/glm-5.2-max",
            uri_params={},
        )
    )
    processor = AsyncMock()
    processor.process_request.return_value = ResponseEnvelope(
        content={
            "id": "chat-acp-1",
            "object": "chat.completion",
            "created": 123,
            "model": "cursor/glm-5.2-max",
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": "PROXY_ROUTE_OK"},
                    "finish_reason": "stop",
                }
            ],
            "usage": {
                "prompt_tokens": 3,
                "completion_tokens": 2,
                "total_tokens": 5,
                "input_tokens_details": {"cached_tokens": 1},
                "output_tokens_details": {"reasoning_tokens": 1},
                "cost": 0.0123,
            },
        }
    )
    controller = ResponsesController(
        processor,
        translation_service=_StubTranslationService(),
        **kwargs,
    )

    response = await controller.handle_responses_request(
        _make_request(),
        _responses_request(
            model="cursor-cli-acp:cursor/glm-5.2-max",
            input="Reply exactly PROXY_ROUTE_OK",
            stream=False,
        ),
    )

    payload = json.loads(bytes(response.body))
    assert payload["object"] == "response"
    assert payload["status"] == "completed"
    assert payload["model"] == "cursor/glm-5.2-max"
    assert payload["output"][0]["type"] == "message"
    assert payload["output"][0]["content"] == [
        {"type": "output_text", "text": "PROXY_ROUTE_OK"}
    ]
    assert payload["usage"] == {
        "input_tokens": 3,
        "output_tokens": 2,
        "total_tokens": 5,
        "input_tokens_details": {"cached_tokens": 1},
        "output_tokens_details": {"reasoning_tokens": 1},
        "cost": 0.0123,
    }


@pytest.mark.asyncio
async def test_cursor_acp_non_streaming_json_envelope_becomes_responses_object() -> (
    None
):
    kwargs = build_responses_controller_backend_kwargs()
    kwargs["backend_model_resolver"].resolve_target = AsyncMock(
        return_value=BackendTarget(
            backend="cursor-cli-acp.default",
            model="cursor/glm-5.2-max",
            uri_params={},
        )
    )
    processor = AsyncMock()
    processor.process_request.return_value = ResponseEnvelope(
        content=json.dumps(
            {
                "id": "chat-acp-json-1",
                "object": "chat.completion",
                "model": "cursor/glm-5.2-max",
                "choices": [
                    {
                        "index": 0,
                        "message": {"role": "assistant", "content": "OK"},
                        "finish_reason": "stop",
                    }
                ],
            }
        )
    )
    controller = ResponsesController(
        processor,
        translation_service=_StubTranslationService(),
        **kwargs,
    )

    response = await controller.handle_responses_request(
        _make_request(),
        _responses_request(
            model="cursor-cli-acp:cursor/glm-5.2-max",
            input="Reply exactly OK",
            stream=False,
        ),
    )

    payload = json.loads(bytes(response.body))
    assert payload["object"] == "response"
    assert payload["output"][0]["content"][0]["text"] == "OK"


@pytest.mark.asyncio
async def test_cursor_acp_non_streaming_plain_text_envelope_becomes_responses_object() -> (
    None
):
    kwargs = build_responses_controller_backend_kwargs()
    kwargs["backend_model_resolver"].resolve_target = AsyncMock(
        return_value=BackendTarget(
            backend="cursor-cli-acp.default",
            model="cursor/glm-5.2-max",
            uri_params={},
        )
    )
    processor = AsyncMock()
    processor.process_request.return_value = ResponseEnvelope(
        content="PROXY_ROUTE_OK",
        metadata={"id": "chat-acp-live-shape", "model": "cursor/glm-5.2-max"},
    )
    controller = ResponsesController(
        processor,
        translation_service=_StubTranslationService(),
        **kwargs,
    )

    response = await controller.handle_responses_request(
        _make_request(),
        _responses_request(
            model="cursor-cli-acp:cursor/glm-5.2-max",
            input="Reply exactly PROXY_ROUTE_OK",
            stream=False,
        ),
    )

    payload = json.loads(bytes(response.body))
    assert payload["id"] == "chat-acp-live-shape"
    assert payload["model"] == "cursor/glm-5.2-max"
    assert payload["output"][0]["content"][0]["text"] == "PROXY_ROUTE_OK"


@pytest.mark.asyncio
async def test_cursor_acp_semantic_stream_disconnect_calls_backend_cancel() -> None:
    kwargs = build_responses_controller_backend_kwargs()
    store = AsyncMock()
    kwargs["responses_session_store"] = store
    controller = ResponsesController(
        AsyncMock(),
        translation_service=_StubTranslationService(),
        **kwargs,
    )
    request = _make_request()
    request.is_disconnected = AsyncMock(return_value=True)  # type: ignore[method-assign]
    cancel = AsyncMock()

    async def chunks():
        yield ProcessedResponse(
            content=(
                'data: {"object":"chat.completion.chunk","choices":['
                '{"delta":{"content":"late"},"finish_reason":null}]}\n\n'
            )
        )

    envelope = StreamingResponseEnvelope(content=chunks(), cancel_callback=cancel)
    context = RequestContext(
        request_id="req-disconnect",
        headers={},
        cookies={},
        client_host=None,
        original_request=None,
        state={},
        app_state={},
    )
    context.extensions["responses_semantic_pipeline"] = True
    context.extensions["responses_stream_source"] = (
        ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS.value
    )

    stream = controller._stream_response_envelope(
        request=request,
        domain_request=CanonicalChatRequest(
            model="cursor-cli-acp:cursor/glm-5.2-max",
            messages=[ChatMessage(role="user", content="hello")],
            stream=True,
        ),
        response=envelope,
        request_id="req-disconnect",
        context=context,
    )
    frames = [frame async for frame in stream]

    cancel.assert_awaited_once()
    assert not any('"type": "response.completed"' in frame for frame in frames)
    assert not any('"type": "response.incomplete"' in frame for frame in frames)
    assert not any("data: [DONE]" in frame for frame in frames)
    store.store.assert_not_awaited()


@pytest.mark.asyncio
async def test_prepare_responses_execution_accepts_openai_chatgpt_plan_backend() -> None:
    """Native /v1/responses must route openai-chatgpt-plan via OpenAI Responses wire.

    Phase 7.2 proof: without this allowlist entry the frontend raises
    ResponsesProviderLimitationError(feature='responses_api.routing') before the
    SIWC connector can run a client tool round-trip.
    """

    kwargs = build_responses_controller_backend_kwargs()
    kwargs["backend_model_resolver"].resolve_target = AsyncMock(
        return_value=BackendTarget(
            backend="openai-chatgpt-plan",
            model="openai/gpt-5.5",
            uri_params={},
        )
    )

    controller = ResponsesController(
        AsyncMock(),
        translation_service=_StubTranslationService(),
        **kwargs,
    )

    _, canonical, stream_source, _ = await controller._prepare_responses_execution(
        responses_request=_responses_request(
            model="openai-chatgpt-plan:openai/gpt-5.5",
            input="call get_weather",
            stream=True,
        ),
    )

    assert stream_source is ResponsesStreamSource.OPENAI_RESPONSES
    assert canonical.model == "openai-chatgpt-plan:openai/gpt-5.5"
    assert canonical.extra_body is not None
    assert RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY in canonical.extra_body


@pytest.mark.asyncio
async def test_prepare_responses_execution_accepts_openai_chatgpt_plan_instance_backend() -> None:
    """Instance backends (openai-chatgpt-plan.home) must share the same wire path."""

    kwargs = build_responses_controller_backend_kwargs()
    kwargs["backend_model_resolver"].resolve_target = AsyncMock(
        return_value=BackendTarget(
            backend="openai-chatgpt-plan.home",
            model="openai/gpt-5.5",
            uri_params={},
        )
    )

    controller = ResponsesController(
        AsyncMock(),
        translation_service=_StubTranslationService(),
        **kwargs,
    )

    _, canonical, stream_source, _ = await controller._prepare_responses_execution(
        responses_request=_responses_request(
            model="openai-chatgpt-plan.home:openai/gpt-5.5",
            input="hello",
            stream=True,
        ),
    )

    assert stream_source is ResponsesStreamSource.OPENAI_RESPONSES
    assert canonical.model == "openai-chatgpt-plan.home:openai/gpt-5.5"
    assert canonical.extra_body is not None
    assert RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY in canonical.extra_body
