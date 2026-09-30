from __future__ import annotations

import asyncio
import json
from collections.abc import AsyncIterator
from types import SimpleNamespace
from typing import cast
from unittest.mock import MagicMock

import pytest
from fastapi import Request
from src.core.app.controllers.responses_controller import ResponsesController
from src.core.common.exceptions import ResponsesProtocolError
from src.core.domain.chat import ChatMessage, ChatRequest
from src.core.domain.responses import StreamingResponseEnvelope
from src.core.interfaces.response_processor_interface import ProcessedResponse
from tests.utils.responses_controller_test_deps import (
    build_responses_controller_backend_kwargs,
)


class _FakeRequest:
    """Minimal request stub for testing streaming cancellation handling."""

    def __init__(self, disconnect_sequence: list[bool]) -> None:
        self._disconnect_iter = iter(disconnect_sequence)
        self.state = SimpleNamespace()

    async def is_disconnected(self) -> bool:
        try:
            return next(self._disconnect_iter)
        except StopIteration:
            return False


async def _make_stream() -> AsyncIterator[ProcessedResponse]:
    yield ProcessedResponse(
        content={
            "id": "resp_123",
            "object": "response.chunk",
            "created": 123,
            "model": "gpt-4o",
            "choices": [
                {
                    "index": 0,
                    "delta": {"content": "hello"},
                    "finish_reason": None,
                }
            ],
        },
    )
    yield ProcessedResponse(
        content={
            "id": "resp_123",
            "object": "response.chunk",
            "created": 123,
            "model": "gpt-4o",
            "choices": [
                {
                    "index": 0,
                    "delta": {"content": "world"},
                    "finish_reason": "stop",
                }
            ],
        },
    )


async def _make_tool_stream() -> AsyncIterator[ProcessedResponse]:
    yield ProcessedResponse(
        content="",
        metadata={
            "id": "resp_tool",
            "tool_calls": [
                {
                    "id": "call_1",
                    "type": "function",
                    "function": {
                        "name": "fetch_data",
                        "arguments": '{"query":"status"}',
                    },
                }
            ],
        },
    )
    yield ProcessedResponse(content="", metadata={"id": "resp_tool", "is_done": True})


def _decode_sse_payloads(blob: str) -> list[dict]:
    payloads: list[dict] = []
    for line in blob.splitlines():
        if not line.startswith("data: "):
            continue
        raw = line[len("data: ") :].strip()
        if raw == "[DONE]":
            continue
        payloads.append(json.loads(raw))
    return payloads


@pytest.mark.asyncio
async def test_streaming_disconnect_triggers_backend_cancel() -> None:
    controller = ResponsesController(
        request_processor=MagicMock(),
        translation_service=MagicMock(),
        **build_responses_controller_backend_kwargs(),
    )

    cancel_called = asyncio.Event()

    async def _cancel_callback() -> None:
        cancel_called.set()

    envelope = StreamingResponseEnvelope(
        content=_make_stream(),
        cancel_callback=_cancel_callback,
    )

    request = _FakeRequest(disconnect_sequence=[False, True])
    domain_request = ChatRequest(
        model="gpt-4o",
        messages=[ChatMessage(role="user", content="hi")],
        stream=True,
    )

    stream = controller._stream_response_envelope(
        request=cast(Request, request),
        domain_request=domain_request,
        response=envelope,
        request_id="req-test",
    )

    parts: list[str] = []
    while True:
        try:
            parts.append(await stream.__anext__())
        except StopAsyncIteration:
            break
    blob = "".join(parts)
    assert "hello" in blob
    assert "response.output_text.delta" in blob
    assert "response.chunk" not in blob

    await asyncio.wait_for(cancel_called.wait(), timeout=0.1)


@pytest.mark.asyncio
async def test_streaming_tool_calls_emit_wire_events_only() -> None:
    controller = ResponsesController(
        request_processor=MagicMock(),
        translation_service=MagicMock(),
        **build_responses_controller_backend_kwargs(),
    )

    envelope = StreamingResponseEnvelope(content=_make_tool_stream())
    request = _FakeRequest(disconnect_sequence=[False, False, False])
    domain_request = ChatRequest(
        model="gpt-4o",
        messages=[ChatMessage(role="user", content="tool")],
        stream=True,
    )

    stream = controller._stream_response_envelope(
        request=cast(Request, request),
        domain_request=domain_request,
        response=envelope,
        request_id="req-tool",
    )

    parts: list[str] = []
    while True:
        try:
            parts.append(await stream.__anext__())
        except StopAsyncIteration:
            break
    payloads = _decode_sse_payloads("".join(parts))
    assert payloads
    assert all(payload.get("object") != "response.chunk" for payload in payloads)
    types = [payload.get("type") for payload in payloads]
    assert "response.function_call_arguments.delta" in types
    assert "response.function_call_arguments.done" in types
    assert "response.output_item.done" in types
    assert types[-1] == "response.completed"


async def _empty_processed_stream() -> AsyncIterator[ProcessedResponse]:
    """Async generator that yields nothing (valid empty stream iterator)."""
    if False:
        yield ProcessedResponse(content={}, metadata={})


@pytest.mark.asyncio
async def test_semantic_sse_missing_responses_stream_source_raises() -> None:
    controller = ResponsesController(
        request_processor=MagicMock(),
        translation_service=MagicMock(),
        **build_responses_controller_backend_kwargs(),
    )
    ctx = SimpleNamespace(
        extensions={
            "responses_semantic_pipeline": True,
        },
    )
    request = _FakeRequest(disconnect_sequence=[False])
    domain_request = SimpleNamespace(model="gpt-4o", stream=True, extra_body={})
    envelope = StreamingResponseEnvelope(content=_empty_processed_stream())

    stream = controller._stream_response_envelope(
        request=cast(Request, request),
        domain_request=domain_request,
        response=envelope,
        request_id="req-missing-stream-source",
        context=ctx,
    )

    with pytest.raises(ResponsesProtocolError) as exc_info:
        await stream.__anext__()

    assert exc_info.value.code == "missing_stream_source"
    assert exc_info.value.status_code == 500


@pytest.mark.asyncio
async def test_semantic_sse_invalid_responses_stream_source_raises() -> None:
    controller = ResponsesController(
        request_processor=MagicMock(),
        translation_service=MagicMock(),
        **build_responses_controller_backend_kwargs(),
    )
    ctx = SimpleNamespace(
        extensions={
            "responses_semantic_pipeline": True,
            "responses_stream_source": "not_a_valid_enum_member",
        },
    )
    request = _FakeRequest(disconnect_sequence=[False])
    domain_request = SimpleNamespace(model="gpt-4o", stream=True, extra_body={})
    envelope = StreamingResponseEnvelope(content=_empty_processed_stream())

    stream = controller._stream_response_envelope(
        request=cast(Request, request),
        domain_request=domain_request,
        response=envelope,
        request_id="req-invalid-stream-source",
        context=ctx,
    )

    with pytest.raises(ResponsesProtocolError) as exc_info:
        await stream.__anext__()

    assert exc_info.value.code == "invalid_stream_source"
    assert exc_info.value.status_code == 500


async def _hanging_native_completed_stream() -> AsyncIterator[ProcessedResponse]:
    yield ProcessedResponse(
        content={
            "type": "response.created",
            "response": {"id": "resp_hang", "object": "response"},
        }
    )
    yield ProcessedResponse(
        content={
            "type": "response.output_text.delta",
            "item_id": "msg_1",
            "output_index": 0,
            "content_index": 0,
            "delta": "hello",
        }
    )
    yield ProcessedResponse(
        content={
            "type": "response.completed",
            "response": {
                "id": "resp_hang",
                "object": "response",
                "status": "completed",
                "output": [],
            },
        }
    )
    await asyncio.Future()


async def _hanging_legacy_finish_stream() -> AsyncIterator[ProcessedResponse]:
    yield ProcessedResponse(
        content={
            "id": "resp_legacy",
            "object": "response.chunk",
            "created": 1,
            "model": "gpt-4o",
            "choices": [
                {"index": 0, "delta": {"content": "hi"}, "finish_reason": None}
            ],
        }
    )
    yield ProcessedResponse(
        content={
            "id": "resp_legacy",
            "object": "response.chunk",
            "created": 1,
            "model": "gpt-4o",
            "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
        }
    )
    await asyncio.Future()


@pytest.mark.asyncio
async def test_semantic_sse_emits_done_without_waiting_for_upstream_eof() -> None:
    """Semantic Responses SSE must end after completed even if upstream hangs."""
    controller = ResponsesController(
        request_processor=MagicMock(),
        translation_service=MagicMock(),
        **build_responses_controller_backend_kwargs(),
    )
    ctx = SimpleNamespace(
        extensions={
            "responses_semantic_pipeline": True,
            "responses_stream_source": "openai_responses",
        },
    )
    request = _FakeRequest(disconnect_sequence=[False] * 20)
    domain_request = SimpleNamespace(model="gpt-4o", stream=True, extra_body={})
    envelope = StreamingResponseEnvelope(content=_hanging_legacy_finish_stream())

    stream = controller._stream_response_envelope(
        request=cast(Request, request),
        domain_request=domain_request,
        response=envelope,
        request_id="req-eos-hang",
        context=ctx,
    )

    parts: list[str] = []

    async def _drain() -> None:
        async for part in stream:
            parts.append(part)

    await asyncio.wait_for(_drain(), timeout=1.0)
    blob = "".join(parts)
    assert "response.completed" in blob
    assert "data: [DONE]" in blob


@pytest.mark.asyncio
async def test_native_passthrough_emits_done_without_waiting_for_upstream_eof() -> None:
    """Non-semantic native wire passthrough must [DONE] after response.completed."""
    controller = ResponsesController(
        request_processor=MagicMock(),
        translation_service=MagicMock(),
        **build_responses_controller_backend_kwargs(),
    )
    request = _FakeRequest(disconnect_sequence=[False] * 20)
    domain_request = ChatRequest(
        model="gpt-4o",
        messages=[ChatMessage(role="user", content="hi")],
        stream=True,
    )
    envelope = StreamingResponseEnvelope(content=_hanging_native_completed_stream())

    stream = controller._stream_response_envelope(
        request=cast(Request, request),
        domain_request=domain_request,
        response=envelope,
        request_id="req-native-eos",
    )

    parts: list[str] = []

    async def _drain() -> None:
        async for part in stream:
            parts.append(part)

    await asyncio.wait_for(_drain(), timeout=1.0)
    blob = "".join(parts)
    assert "response.completed" in blob
    assert "data: [DONE]" in blob


@pytest.mark.asyncio
async def test_generator_exit_schedules_cancel_without_ignored_exit() -> None:
    """aclose/GeneratorExit must not raise 'async generator ignored GeneratorExit'."""
    controller = ResponsesController(
        request_processor=MagicMock(),
        translation_service=MagicMock(),
        **build_responses_controller_backend_kwargs(),
    )
    cancel_called = asyncio.Event()

    async def _cancel_callback() -> None:
        cancel_called.set()

    async def _slow_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(
            content={
                "id": "resp_slow",
                "object": "response.chunk",
                "created": 1,
                "model": "gpt-4o",
                "choices": [
                    {"index": 0, "delta": {"content": "x"}, "finish_reason": None}
                ],
            }
        )
        await asyncio.sleep(60)

    envelope = StreamingResponseEnvelope(
        content=_slow_stream(),
        cancel_callback=_cancel_callback,
    )
    request = _FakeRequest(disconnect_sequence=[False] * 20)
    domain_request = ChatRequest(
        model="gpt-4o",
        messages=[ChatMessage(role="user", content="hi")],
        stream=True,
    )
    stream = controller._stream_response_envelope(
        request=cast(Request, request),
        domain_request=domain_request,
        response=envelope,
        request_id="req-ge",
    )
    assert await stream.__anext__()
    # Closing the async generator must complete cleanly.
    await asyncio.wait_for(stream.aclose(), timeout=1.0)
    await asyncio.wait_for(cancel_called.wait(), timeout=1.0)

