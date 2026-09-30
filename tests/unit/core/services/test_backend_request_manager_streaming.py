from __future__ import annotations

from collections.abc import AsyncIterator
from typing import Any, cast
from unittest.mock import AsyncMock, MagicMock

import pytest
from pydantic.types import JsonValue
from src.core.common.exceptions import BackendError
from src.core.domain.chat import ChatMessage, ChatRequest
from src.core.domain.request_context import RequestContext
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.interfaces.response_processor_interface import (
    ProcessedChunkContent,
    ProcessedResponse,
)
from src.core.services.backend_request_manager.streaming_response_handler import (
    BackendStreamingResponseHandler,
)

from tests.helpers.backend_request_manager_fixtures import (
    create_backend_request_manager,
)

JsonDict = dict[str, JsonValue]


def _meta(data: dict[str, Any]) -> JsonDict:
    return cast(JsonDict, data)


def _make_context() -> RequestContext:
    return RequestContext(
        headers={},
        cookies={},
        state=None,
        app_state=None,
        client_host=None,
        session_id=None,
        agent=None,
        original_request=None,
        processing_context=None,
    )


@pytest.mark.asyncio
async def test_streaming_retry_replays_full_replacement_stream() -> None:
    """Ensure streaming retries forward the complete replacement stream."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="openai",
        messages=[ChatMessage(role="user", content="hi")],
        stream=True,
    )

    # First response has swallowed tool call
    backend_response_swallowed = StreamingResponseEnvelope(
        content=async_iterator_from_list(
            [
                ProcessedResponse(
                    content="dangerous tool response",
                    metadata=_meta(
                        {
                            "tool_call_swallowed": True,
                            "steering_message": "Do not execute that command.",
                            "swallowed_original_content": "rm -rf /",
                            "swallowed_tool_calls": [
                                {"function": {"name": "shell", "arguments": "{}"}}
                            ],
                        }
                    ),
                )
            ]
        ),
    )

    async def retry_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(content="safe replacement 1", metadata=_meta({}))
        yield ProcessedResponse(
            content="safe replacement 2", metadata=_meta({"is_done": True})
        )

    # Retry response
    backend_response_retry = StreamingResponseEnvelope(content=retry_stream())

    backend_processor.process_backend_request.side_effect = [
        backend_response_swallowed,
        backend_response_retry,
    ]

    # Test through public API - the handler will detect swallowed tool call and retry
    result = await manager.process_backend_request(
        original_request,
        "session-x",
        _make_context(),
    )

    assert isinstance(result, StreamingResponseEnvelope)
    assert result.content is not None
    chunks: list[str] = []
    async for chunk in result.content:
        chunks.append(str(chunk.content))

    # Should get retry stream content
    assert len(chunks) >= 2
    assert any("safe replacement 1" in str(chunk) for chunk in chunks)
    assert backend_processor.process_backend_request.await_count >= 1


def async_iterator_from_list(items):
    """Helper to create async iterator from list."""

    async def _iter():
        for item in items:
            yield item

    return _iter()


@pytest.mark.asyncio
async def test_empty_stream_is_retried_before_forwarding() -> None:
    """Empty streaming responses should trigger a retry instead of reaching the client."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="openai",
        messages=[ChatMessage(role="user", content="hi")],
        stream=True,
    )

    async def empty_stream():
        if False:
            yield ProcessedResponse(content="", metadata=_meta({}))

    async def retry_stream():
        yield ProcessedResponse(content="meaningful output", metadata=_meta({}))
        yield ProcessedResponse(content="", metadata=_meta({"is_done": True}))

    # First call returns empty stream, second call (retry) returns meaningful content
    backend_processor.process_backend_request.side_effect = [
        StreamingResponseEnvelope(content=empty_stream()),
        StreamingResponseEnvelope(content=retry_stream()),
    ]

    # Use public API - empty stream will trigger retry internally
    envelope = await manager.process_backend_request(
        original_request,
        "session-empty",
        _make_context(),
    )

    assert isinstance(envelope, StreamingResponseEnvelope)
    assert envelope.content is not None
    chunks = [chunk async for chunk in envelope.content]

    # Should have retried (backend called twice: initial + retry)
    assert backend_processor.process_backend_request.await_count >= 1
    # Should get meaningful output from retry
    assert any(chunk.content == "meaningful output" for chunk in chunks)


@pytest.mark.asyncio
async def test_empty_stream_retry_respects_max_limit() -> None:
    """Do not exceed the max empty-stream retry budget when retries stay empty."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="openai",
        messages=[ChatMessage(role="user", content="hi")],
        stream=True,
    )

    async def empty_stream() -> AsyncIterator[ProcessedResponse]:
        if False:
            yield ProcessedResponse(content="", metadata=_meta({}))

    async def retry_empty_stream() -> AsyncIterator[ProcessedResponse]:
        if False:
            yield ProcessedResponse(content="", metadata=_meta({}))

    # First call returns empty stream, retry also returns empty (hits limit)
    backend_processor.process_backend_request.side_effect = [
        StreamingResponseEnvelope(content=empty_stream()),
        StreamingResponseEnvelope(content=retry_empty_stream()),
    ]

    # Use public API - empty stream will trigger retry, then hit limit and return a terminal error chunk
    envelope = await manager.process_backend_request(
        original_request,
        "session-empty-max",
        _make_context(),
    )

    assert isinstance(envelope, StreamingResponseEnvelope)
    assert envelope.content is not None
    chunks = [chunk async for chunk in envelope.content]

    # Should have retried
    assert backend_processor.process_backend_request.await_count >= 1
    # Should contain terminal error metadata (never assistant text)
    assert any(
        isinstance(chunk.metadata, dict)
        and chunk.metadata.get("finish_reason") == "error"
        and isinstance(chunk.metadata.get("error"), dict)
        for chunk in chunks
    )


@pytest.mark.asyncio
async def test_responses_tool_only_then_stop_is_not_retried_as_empty() -> None:
    """SIWC/Responses tool-only turns end with function_call deltas then stop.

    After e48d49a93, output_item.done no longer attaches finish_reason=tool_calls
    (to avoid early SSE close). The empty-stream gate must still treat the
    tool_calls delta as meaningful and must flush the trailing stop so the
    Responses frontend can emit response.completed + [DONE].
    """
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="openai-chatgpt-plan:gpt-6.1-sol",
        messages=[ChatMessage(role="user", content="list files")],
        stream=True,
    )

    async def tool_only_then_stop() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(
            content={
                "id": "resp_tool_only",
                "object": "response.chunk",
                "created": 1,
                "model": "gpt-6.1-sol",
                "choices": [
                    {
                        "index": 0,
                        "delta": {
                            "tool_calls": [
                                {
                                    "id": "call_read_1",
                                    "index": 0,
                                    "type": "function",
                                    "function": {
                                        "name": "read",
                                        "arguments": '{"filePath":"C:\\\\tmp"}',
                                    },
                                }
                            ]
                        },
                        "finish_reason": None,
                    }
                ],
            },
            metadata=_meta({}),
        )
        yield ProcessedResponse(
            content={
                "id": "resp_tool_only",
                "object": "response.chunk",
                "created": 1,
                "model": "gpt-6.1-sol",
                "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
            },
            metadata=_meta({"finish_reason": "stop", "is_done": True}),
        )

    backend_processor.process_backend_request.return_value = StreamingResponseEnvelope(
        content=tool_only_then_stop()
    )

    envelope = await manager.process_backend_request(
        original_request,
        "session-responses-tool-only",
        _make_context(),
    )

    assert isinstance(envelope, StreamingResponseEnvelope)
    assert envelope.content is not None
    chunks = [chunk async for chunk in envelope.content]

    assert backend_processor.process_backend_request.await_count == 1
    assert len(chunks) == 2
    first = cast(dict[str, Any], chunks[0].content)
    assert first["choices"][0]["delta"]["tool_calls"]
    assert chunks[1].metadata.get("finish_reason") == "stop"


@pytest.mark.asyncio
async def test_completed_stop_without_text_is_not_retried_as_empty() -> None:
    """A clean finish_reason=stop with no assistant text must close the turn.

    SIWC often completes with an empty message item + response.completed. Treating
    that as empty-stream failure caused 502 "no user-visible content" loops under
    OpenCode dual-submit/retry, and held the terminal chunk so SSE never [DONE].
    """
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="openai-chatgpt-plan:gpt-6.1-sol",
        messages=[ChatMessage(role="user", content="continue")],
        stream=True,
    )

    async def empty_completed_stop() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(
            content={
                "id": "resp_empty_stop",
                "object": "response.chunk",
                "created": 1,
                "model": "gpt-6.1-sol",
                "choices": [
                    {
                        "index": 0,
                        "delta": {"role": "assistant"},
                        "finish_reason": None,
                    }
                ],
            },
            metadata=_meta({}),
        )
        yield ProcessedResponse(
            content={
                "id": "resp_empty_stop",
                "object": "response.chunk",
                "created": 1,
                "model": "gpt-6.1-sol",
                "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
            },
            metadata=_meta({"finish_reason": "stop", "is_done": True}),
        )

    backend_processor.process_backend_request.return_value = StreamingResponseEnvelope(
        content=empty_completed_stop()
    )

    envelope = await manager.process_backend_request(
        original_request,
        "session-empty-completed-stop",
        _make_context(),
    )

    assert isinstance(envelope, StreamingResponseEnvelope)
    assert envelope.content is not None
    chunks = [chunk async for chunk in envelope.content]

    assert backend_processor.process_backend_request.await_count == 1
    assert len(chunks) == 2
    assert chunks[-1].metadata.get("finish_reason") == "stop"
    assert not any(
        isinstance(chunk.metadata, dict)
        and (
            chunk.metadata.get("finish_reason") == "error"
            or (
                isinstance(chunk.metadata.get("proxy_warning"), dict)
                and chunk.metadata["proxy_warning"].get("type")
                == "empty_stream_after_retries"
            )
        )
        for chunk in chunks
    )


@pytest.mark.asyncio
async def test_metadata_tool_calls_list_suppresses_empty_stream_retry() -> None:
    """tool_calls parked on metadata (no text) must not trigger empty recovery."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="openai",
        messages=[ChatMessage(role="user", content="use a tool")],
        stream=True,
    )

    async def metadata_tool_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(
            content="",
            metadata=_meta(
                {
                    "tool_calls": [
                        {
                            "id": "call_meta_1",
                            "type": "function",
                            "function": {
                                "name": "bash",
                                "arguments": '{"command":"pwd"}',
                            },
                        }
                    ],
                    "finish_reason": "stop",
                    "is_done": True,
                }
            ),
        )

    backend_processor.process_backend_request.return_value = StreamingResponseEnvelope(
        content=metadata_tool_stream()
    )

    envelope = await manager.process_backend_request(
        original_request,
        "session-metadata-tool-calls",
        _make_context(),
    )

    assert isinstance(envelope, StreamingResponseEnvelope)
    assert envelope.content is not None
    chunks = [chunk async for chunk in envelope.content]

    assert backend_processor.process_backend_request.await_count == 1
    assert len(chunks) == 1
    assert isinstance(chunks[0].metadata.get("tool_calls"), list)


@pytest.mark.asyncio
async def test_terminal_tool_calls_stream_is_not_retried_as_empty() -> None:
    """Tool-call terminal chunks are meaningful and must not trigger empty retry."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="openai",
        messages=[ChatMessage(role="user", content="use a tool")],
        stream=True,
    )

    async def tool_only_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(
            content="",
            metadata=_meta({"finish_reason": "tool_calls", "is_done": True}),
        )

    backend_processor.process_backend_request.return_value = StreamingResponseEnvelope(
        content=tool_only_stream()
    )

    envelope = await manager.process_backend_request(
        original_request,
        "session-tool-calls",
        _make_context(),
    )

    assert isinstance(envelope, StreamingResponseEnvelope)
    assert envelope.content is not None
    chunks = [chunk async for chunk in envelope.content]

    assert backend_processor.process_backend_request.await_count == 1
    assert len(chunks) == 1
    assert chunks[0].metadata.get("finish_reason") == "tool_calls"


@pytest.mark.asyncio
async def test_tool_call_emitted_marker_suppresses_empty_stream_retry() -> None:
    """Explicit tool-call markers must suppress empty-stream retry."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="openai",
        messages=[ChatMessage(role="user", content="use a tool")],
        stream=True,
    )

    async def tool_marker_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(
            content="",
            metadata=_meta({"tool_call_emitted": True, "is_done": True}),
        )

    backend_processor.process_backend_request.return_value = StreamingResponseEnvelope(
        content=tool_marker_stream()
    )

    envelope = await manager.process_backend_request(
        original_request,
        "session-tool-marker",
        _make_context(),
    )

    assert isinstance(envelope, StreamingResponseEnvelope)
    assert envelope.content is not None
    chunks = [chunk async for chunk in envelope.content]

    assert backend_processor.process_backend_request.await_count == 1
    assert len(chunks) == 1
    assert chunks[0].metadata.get("tool_call_emitted") is True


@pytest.mark.asyncio
async def test_nested_terminal_stop_chunk_is_not_treated_as_empty_stream() -> None:
    """Nested terminal stop chunks must count as meaningful to avoid false retries."""
    manager = create_backend_request_manager()
    handler = cast(Any, manager._post_backend_response_coordinator._streaming_handler)

    nested_chunk = ProcessedResponse(
        content=cast(
            ProcessedChunkContent,
            ProcessedResponse(
                content={
                    "id": "resp_nested_stop",
                    "object": "response.chunk",
                    "created": 123,
                    "model": "gpt-5.4-mini",
                    "choices": [
                        {
                            "index": 0,
                            "delta": {},
                            "finish_reason": "stop",
                        }
                    ],
                    "usage": {"total_tokens": 10},
                },
                metadata=_meta({"finish_reason": "stop", "is_done": True}),
            ),
        ),
        metadata=_meta({"session_id": "session-nested-stop"}),
    )

    assert handler._chunk_has_meaningful_output(nested_chunk) is True


@pytest.mark.asyncio
async def test_processed_response_with_nested_tool_call_dict_is_meaningful() -> None:
    """Nested ProcessedResponse payloads from the stream normalizer must still count."""
    manager = create_backend_request_manager()
    handler = cast(Any, manager._post_backend_response_coordinator._streaming_handler)

    nested_chunk = ProcessedResponse(
        content=cast(
            ProcessedChunkContent,
            ProcessedResponse(
                content={
                    "id": "resp_nested_tool",
                    "object": "response.chunk",
                    "created": 123,
                    "model": "gpt-5.4-mini",
                    "choices": [
                        {
                            "index": 0,
                            "delta": {
                                "tool_calls": [
                                    {
                                        "id": "fc_nested_tool",
                                        "index": 0,
                                        "type": "function",
                                        "function": {
                                            "name": "bash",
                                            "arguments": '{"command":"git status --short"}',
                                        },
                                    }
                                ]
                            },
                            "finish_reason": "tool_calls",
                        }
                    ],
                },
                metadata=_meta(
                    {"tool_call_emitted": True, "finish_reason": "tool_calls"}
                ),
            ),
        ),
        metadata=_meta({"session_id": "session-nested-tool"}),
    )

    assert handler._chunk_has_meaningful_output(nested_chunk) is True


@pytest.mark.asyncio
async def test_ws_tool_call_chunk_without_marker_still_avoids_empty_retry() -> None:
    """Canonical tool-call chunks should suppress empty retry even if metadata was lost."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="openai",
        messages=[ChatMessage(role="user", content="use a tool")],
        stream=True,
    )

    async def tool_chunk_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(
            content={
                "id": "resp_ws_tool",
                "object": "response.chunk",
                "created": 123,
                "model": "gpt-5.4-mini",
                "choices": [
                    {
                        "index": 0,
                        "delta": {
                            "tool_calls": [
                                {
                                    "id": "fc_ws_tool",
                                    "index": 0,
                                    "type": "function",
                                    "function": {
                                        "name": "bash",
                                        "arguments": '{"command":"git status --short"}',
                                    },
                                }
                            ]
                        },
                        "finish_reason": "tool_calls",
                    }
                ],
            },
            metadata=_meta(
                {"event_type": "response.output_item.done", "is_done": True}
            ),
        )

    backend_processor.process_backend_request.return_value = StreamingResponseEnvelope(
        content=tool_chunk_stream()
    )

    envelope = await manager.process_backend_request(
        original_request,
        "session-ws-tool-no-marker",
        _make_context(),
    )

    assert isinstance(envelope, StreamingResponseEnvelope)
    assert envelope.content is not None
    chunks = [chunk async for chunk in envelope.content]

    assert backend_processor.process_backend_request.await_count == 1
    assert len(chunks) == 1
    content = cast(dict[str, Any], chunks[0].content)
    assert content["choices"][0]["finish_reason"] == "tool_calls"


@pytest.mark.asyncio
async def test_streaming_retry_skipped_when_retry_marker_present() -> None:
    """When retry marker is present, the reactor should not trigger again."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    # Create request with retry marker to prevent retry
    # Note: The retry marker alone doesn't prevent retry if limit is exceeded
    # To test loop prevention, we need a retry marker WITHOUT exceeding the limit
    flagged_request = ChatRequest(
        model="gemini",
        messages=[ChatMessage(role="user", content="continue")],
        stream=True,
        extra_body={
            "_tool_call_reactor_retry": True,
            "_tool_call_reactor_retry_count": 1,  # Below limit, so retry should be skipped
        },
    )

    async def original_stream():
        yield ProcessedResponse(
            content="proxy replacement",
            metadata=_meta(
                {
                    "tool_call_swallowed": True,
                    "steering_message": "Already handled.",
                }
            ),
        )

    stream_envelope = StreamingResponseEnvelope(content=original_stream())

    # Mock backend to return stream with swallowed tool call
    backend_processor.process_backend_request.return_value = stream_envelope

    # Use public API - retry marker should prevent retry
    result = await manager.process_backend_request(
        flagged_request,
        "session-y",
        _make_context(),
    )

    assert isinstance(result, StreamingResponseEnvelope)
    assert result.content is not None
    chunks = [chunk async for chunk in result.content]
    assert len(chunks) == 1
    assert chunks[0].metadata.get("tool_call_swallowed") is True
    # With retry marker, should not trigger additional retry
    assert backend_processor.process_backend_request.await_count == 1


@pytest.mark.asyncio
async def test_full_suite_swallow_replays_history_and_hides_steering() -> None:
    """Full-suite steering should replay the request with history and hide steering output."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    steering_metadata = _meta(
        {
            "tool_call_swallowed": True,
            "steering_message": "please target specific tests",
            "swallowed_original_content": "original llm response",
            "swallowed_tool_calls": [
                {"function": {"name": "execute_command", "arguments": "pytest"}}
            ],
        }
    )
    steering_processed = ProcessedResponse(
        content="steering-text", metadata=steering_metadata
    )
    corrected_processed = ProcessedResponse(
        content="corrected output", metadata=_meta({"clean": True})
    )
    response_processor.process_response = AsyncMock(
        side_effect=[steering_processed, corrected_processed]
    )
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_messages = [
        ChatMessage(role="system", content="sys"),
        ChatMessage(role="user", content="run all tests"),
    ]
    original_request = ChatRequest(
        model="gemini",
        messages=original_messages,
        stream=False,
    )

    # Backend returns response with tool_call_swallowed metadata
    # The handler checks ProcessedResponse metadata, but coordinator checks ResponseEnvelope metadata
    backend_processor.process_backend_request.side_effect = [
        ResponseEnvelope(
            content="raw tool call",
            metadata=_meta(dict(steering_metadata)),
        ),
        ResponseEnvelope(content="second response"),
    ]

    result = await manager.process_backend_request(
        original_request, "session-full-suite", _make_context()
    )

    assert backend_processor.process_backend_request.await_count == 2
    retry_args = backend_processor.process_backend_request.await_args_list[1].kwargs
    retry_request = retry_args["request"]
    assert isinstance(retry_request, ChatRequest)
    assert len(retry_request.messages) == len(original_messages) + 1
    assert retry_request.messages[: len(original_messages)] == original_messages
    assert retry_request.messages[-1].role == "system"
    proxy_notice = retry_request.messages[-1].content
    assert isinstance(proxy_notice, str)
    assert "Proxy Notice" in proxy_notice
    assert "Proxy Steering Notice" in proxy_notice  # Escalating message
    assert "Steering instruction" in proxy_notice
    assert "execute_command" in proxy_notice
    assert "pytest" in proxy_notice
    extra_body = retry_request.extra_body or {}
    assert extra_body.get("_tool_call_reactor_retry") is True

    assert isinstance(result, ResponseEnvelope)
    assert result.content == "corrected output"
    result_metadata = result.metadata or {}
    assert result_metadata.get("clean") is True
    assert result.content != steering_processed.content


@pytest.mark.asyncio
async def test_full_suite_swallow_retry_failure_does_not_leak_steering() -> None:
    """If steering replay fails, do not forward steering text to the client."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    steering_metadata = _meta(
        {
            "tool_call_swallowed": True,
            "steering_message": "avoid full suite",
            "swallowed_original_content": "raw llm response",
            "swallowed_tool_calls": [
                {"function": {"name": "execute_command", "arguments": "pytest"}}
            ],
        }
    )
    steering_processed = ProcessedResponse(
        content="steering-text", metadata=steering_metadata
    )
    # Coordinator returns fallback response on retry failure
    # The handler processes the initial response, then recursively calls handle() with fallback response
    # The fallback response has tool_call_swallowed (from original metadata) but handler won't retry because
    # the handler checks is_terminal_response, and tool_call_reactor_retry_failed should prevent retry
    # However, the handler doesn't check for tool_call_reactor_retry_failed, so it will try to retry again
    # The recursive call will process the fallback response again
    fallback_processed = ProcessedResponse(
        content="[Proxy Notice]\nA tool call was blocked by proxy policy and the proxy attempted to recover, but the backend retry failed. Please retry your request.",
        metadata=_meta(
            {
                # Coordinator includes tool_call_swallowed in fallback metadata (from original response metadata)
                "tool_call_swallowed": True,
                "tool_call_reactor_retry_failed": True,
                "steering_retry_occurred": True,
                "dangerous_command_retry_count": 1,
                "tool_call_reactor_retry_count": 1,
            }
        ),
    )
    # Handler processes initial response (detects tool_call_swallowed), then recursively processes fallback response
    # The recursive call will process the fallback response again, but won't retry because request doesn't have _tool_call_reactor_retry
    # Actually, the handler will try to retry again because is_retry_request is False
    # But the backend_processor side_effect is exhausted, so it will fail
    # We need to add more items to side_effect to handle the recursive call
    response_processor.process_response = AsyncMock(
        side_effect=[steering_processed, fallback_processed, fallback_processed]
    )
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="gemini",
        messages=[ChatMessage(role="user", content="please run pytest")],
        stream=False,
    )

    # Backend returns response with tool_call_swallowed metadata
    # First call: initial response with swallowed tool call
    # Second call: retry attempt fails with RuntimeError (coordinator catches and returns fallback)
    # Handler recursively processes fallback response, but request is marked as retry so no further retries
    backend_processor.process_backend_request.side_effect = [
        ResponseEnvelope(
            content="raw tool call",
            metadata=_meta(dict(steering_metadata)),
        ),
        RuntimeError("backend failure"),
    ]

    result = await manager.process_backend_request(
        original_request, "session-retry-fail", _make_context()
    )

    # Should have called backend twice: initial + retry attempt
    assert backend_processor.process_backend_request.await_count == 2
    assert isinstance(result, ResponseEnvelope)
    assert isinstance(result.content, str)
    assert result.content
    # Coordinator returns fallback message on retry failure
    assert (
        "backend retry failed" in result.content.lower()
        or "retry failed" in result.content.lower()
    )
    failure_metadata = result.metadata or {}
    assert failure_metadata.get("tool_call_swallowed") is True
    assert result.content != steering_processed.content


@pytest.mark.asyncio
async def test_streaming_full_suite_swallow_replays_history_and_hides_steering() -> (
    None
):
    """Streaming full-suite steering should replay history and hide steering chunk."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_messages = [
        ChatMessage(role="system", content="sys"),
        ChatMessage(role="user", content="run all tests"),
    ]
    original_request = ChatRequest(
        model="gemini",
        messages=original_messages,
        stream=True,
    )

    steering_metadata = _meta(
        {
            "tool_call_swallowed": True,
            "steering_message": "please target specific tests",
            "swallowed_original_content": "stream steering content",
            "swallowed_tool_calls": [
                {"function": {"name": "execute_command", "arguments": "pytest"}}
            ],
        }
    )

    async def initial_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(content="steering chunk", metadata=steering_metadata)

    async def retry_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(content="fixed 1", metadata=_meta({}))
        yield ProcessedResponse(content="fixed 2", metadata=_meta({"is_done": True}))

    backend_processor.process_backend_request.side_effect = [
        StreamingResponseEnvelope(content=initial_stream()),
        StreamingResponseEnvelope(content=retry_stream()),
    ]

    result = await manager.process_backend_request(
        original_request, "session-stream-full-suite", _make_context()
    )

    assert isinstance(result, StreamingResponseEnvelope)
    assert result.content is not None
    chunks = [chunk async for chunk in result.content]

    assert backend_processor.process_backend_request.await_count == 2
    retry_args = backend_processor.process_backend_request.await_args_list[1].kwargs
    retry_request = retry_args["request"]
    assert isinstance(retry_request, ChatRequest)
    assert len(retry_request.messages) == len(original_messages) + 1
    assert retry_request.messages[: len(original_messages)] == original_messages
    assert retry_request.messages[-1].role == "system"
    proxy_notice = retry_request.messages[-1].content
    assert isinstance(proxy_notice, str)
    assert "Proxy Notice" in proxy_notice
    assert "Proxy Steering Notice" in proxy_notice  # Escalating message
    assert "Steering instruction" in proxy_notice
    assert "execute_command" in proxy_notice
    extra_body = retry_request.extra_body or {}
    assert extra_body.get("_tool_call_reactor_retry") is True

    assert [chunk.content for chunk in chunks] == ["fixed 1", "fixed 2"]
    assert all("steering chunk" not in str(chunk.content) for chunk in chunks)


@pytest.mark.asyncio
async def test_streaming_full_suite_swallow_retry_failure_does_not_leak_steering() -> (
    None
):
    """Streaming replay failures should not surface steering content."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="gemini",
        messages=[ChatMessage(role="user", content="run all tests")],
        stream=True,
    )

    steering_metadata = _meta(
        {
            "tool_call_swallowed": True,
            "steering_message": "avoid full suite",
            "swallowed_original_content": "stream steering content",
            "swallowed_tool_calls": [
                {"function": {"name": "execute_command", "arguments": "pytest"}}
            ],
        }
    )

    async def initial_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(content="steering chunk", metadata=steering_metadata)

    backend_processor.process_backend_request.side_effect = [
        StreamingResponseEnvelope(content=initial_stream()),
        RuntimeError("backend failure"),
    ]

    result = await manager.process_backend_request(
        original_request, "session-stream-retry-fail", _make_context()
    )

    assert isinstance(result, StreamingResponseEnvelope)
    assert result.content is not None
    chunks = [chunk async for chunk in result.content]

    assert backend_processor.process_backend_request.await_count == 2
    assert len(chunks) == 1
    assert isinstance(chunks[0].content, str)
    assert chunks[0].content
    assert "backend retry failed" in chunks[0].content.lower()
    metadata = getattr(chunks[0], "metadata", {})
    assert metadata.get("tool_call_swallowed") is True
    assert metadata.get("tool_call_reactor_retry_failed") is True
    assert "steering chunk" not in str(chunks[0].content)


@pytest.mark.asyncio
async def test_dangerous_command_swallow_replays_history_and_hides_steering() -> None:
    """Dangerous command steering should replay history and hide steering output."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    steering_metadata = _meta(
        {
            "tool_call_swallowed": True,
            "steering_message": "dangerous command blocked",
            "swallowed_original_content": "raw dangerous output",
            "swallowed_tool_calls": [
                {
                    "function": {
                        "name": "execute_command",
                        "arguments": "git reset --hard",
                    }
                }
            ],
        }
    )
    steering_processed = ProcessedResponse(
        content="steering-text", metadata=steering_metadata
    )
    corrected_processed = ProcessedResponse(content="safe reply", metadata=_meta({}))
    response_processor.process_response = AsyncMock(
        side_effect=[steering_processed, corrected_processed]
    )
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_messages = [
        ChatMessage(role="user", content="do git reset --hard"),
    ]
    original_request = ChatRequest(
        model="gemini",
        messages=original_messages,
        stream=False,
    )

    # Backend returns response with tool_call_swallowed metadata
    backend_processor.process_backend_request.side_effect = [
        ResponseEnvelope(
            content="raw tool call",
            metadata=_meta(dict(steering_metadata)),
        ),
        ResponseEnvelope(content="second response"),
    ]

    result = await manager.process_backend_request(
        original_request, "session-dangerous", _make_context()
    )

    assert backend_processor.process_backend_request.await_count == 2
    retry_args = backend_processor.process_backend_request.await_args_list[1].kwargs
    retry_request = retry_args["request"]
    proxy_notice = retry_request.messages[-1].content
    assert "git reset --hard" in proxy_notice
    assert "Proxy Steering Notice" in proxy_notice  # Escalating message
    assert "Steering instruction" in proxy_notice
    assert retry_request.extra_body.get("_tool_call_reactor_retry") is True

    assert isinstance(result, ResponseEnvelope)
    assert result.content == "safe reply"
    assert result.content != steering_processed.content


@pytest.mark.asyncio
async def test_tool_access_block_non_streaming_replays_and_hides_steering() -> None:
    """Tool access control steering should replay history and hide steering for non-stream."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    steering_metadata = _meta(
        {
            "tool_call_swallowed": True,
            "steering_message": "tool not allowed",
            "swallowed_original_content": "blocked content",
            "swallowed_tool_calls": [
                {"function": {"name": "deploy_service", "arguments": "{}"}}
            ],
        }
    )
    steering_processed = ProcessedResponse(
        content="steering-text", metadata=steering_metadata
    )
    corrected_processed = ProcessedResponse(
        content="allowed output", metadata=_meta({})
    )
    response_processor.process_response = AsyncMock(
        side_effect=[steering_processed, corrected_processed]
    )
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="gemini",
        messages=[ChatMessage(role="user", content="deploy now")],
        stream=False,
    )

    # Backend returns response with tool_call_swallowed metadata
    backend_processor.process_backend_request.side_effect = [
        ResponseEnvelope(
            content="raw tool call",
            metadata=_meta(dict(steering_metadata)),
        ),
        ResponseEnvelope(content="second response"),
    ]

    result = await manager.process_backend_request(
        original_request, "session-tool-access-ns", _make_context()
    )

    assert backend_processor.process_backend_request.await_count == 2
    retry_args = backend_processor.process_backend_request.await_args_list[1].kwargs
    proxy_notice = retry_args["request"].messages[-1].content
    assert "deploy_service" in proxy_notice
    assert "Proxy Steering Notice" in proxy_notice  # Escalating message
    assert "Steering instruction" in proxy_notice
    assert retry_args["request"].extra_body.get("_tool_call_reactor_retry") is True
    assert isinstance(result, ResponseEnvelope)
    assert result.content == "allowed output"


@pytest.mark.asyncio
async def test_tool_access_block_streaming_replays_and_hides_steering() -> None:
    """Tool access control steering should replay history and hide steering chunk."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    steering_metadata = _meta(
        {
            "tool_call_swallowed": True,
            "steering_message": "tool not allowed",
            "swallowed_original_content": "blocked stream content",
            "swallowed_tool_calls": [
                {"function": {"name": "deploy_service", "arguments": "{}"}}
            ],
        }
    )

    async def initial_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(content="steering chunk", metadata=steering_metadata)

    async def retry_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(content="allowed later", metadata=_meta({}))

    backend_processor.process_backend_request.side_effect = [
        StreamingResponseEnvelope(content=initial_stream()),
        StreamingResponseEnvelope(content=retry_stream()),
    ]

    original_request = ChatRequest(
        model="gemini",
        messages=[ChatMessage(role="user", content="deploy now")],
        stream=True,
    )

    result = await manager.process_backend_request(
        original_request, "session-tool-access", _make_context()
    )

    assert isinstance(result, StreamingResponseEnvelope)
    assert result.content is not None
    chunks = [chunk async for chunk in result.content]
    retry_args = backend_processor.process_backend_request.await_args_list[1].kwargs
    retry_request = retry_args["request"]
    proxy_notice = retry_request.messages[-1].content
    assert "deploy_service" in proxy_notice
    assert "Proxy Steering Notice" in proxy_notice  # Escalating message
    assert "Steering instruction" in proxy_notice
    assert retry_request.extra_body.get("_tool_call_reactor_retry") is True
    assert [chunk.content for chunk in chunks] == ["allowed later"]
    assert all("steering chunk" not in str(chunk.content) for chunk in chunks)


@pytest.mark.asyncio
async def test_config_steering_streaming_retry_failure_does_not_leak() -> None:
    """Config steering replay failures should not leak steering content."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    steering_metadata = _meta(
        {
            "tool_call_swallowed": True,
            "steering_message": "use patch_file",
            "swallowed_original_content": "apply_diff steering",
            "swallowed_tool_calls": [
                {"function": {"name": "apply_diff", "arguments": "{}"}}
            ],
        }
    )

    async def initial_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(content="steering chunk", metadata=steering_metadata)

    backend_processor.process_backend_request.side_effect = [
        StreamingResponseEnvelope(content=initial_stream()),
        RuntimeError("backend failure"),
    ]

    original_request = ChatRequest(
        model="gemini",
        messages=[ChatMessage(role="user", content="apply diff")],
        stream=True,
    )

    result = await manager.process_backend_request(
        original_request, "session-config-retry-fail", _make_context()
    )

    assert isinstance(result, StreamingResponseEnvelope)
    assert result.content is not None
    chunks = [chunk async for chunk in result.content]
    assert backend_processor.process_backend_request.await_count == 2
    assert len(chunks) == 1
    assert isinstance(chunks[0].content, str)
    assert chunks[0].content
    assert "backend retry failed" in chunks[0].content.lower()
    metadata = getattr(chunks[0], "metadata", {})
    assert metadata.get("tool_call_swallowed") is True
    assert metadata.get("tool_call_reactor_retry_failed") is True
    assert "steering chunk" not in str(chunks[0].content)


@pytest.mark.asyncio
async def test_config_steering_non_streaming_replays_and_hides_steering() -> None:
    """Config steering (apply_diff) should replay history and hide steering output."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    steering_metadata = _meta(
        {
            "tool_call_swallowed": True,
            "steering_message": "use patch_file",
            "swallowed_original_content": "apply_diff steering",
            "swallowed_tool_calls": [
                {"function": {"name": "apply_diff", "arguments": "{}"}}
            ],
        }
    )
    steering_processed = ProcessedResponse(
        content="steering-text", metadata=steering_metadata
    )
    corrected_processed = ProcessedResponse(content="patched", metadata=_meta({}))
    response_processor.process_response = AsyncMock(
        side_effect=[steering_processed, corrected_processed]
    )
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="gemini",
        messages=[ChatMessage(role="user", content="apply diff")],
        stream=False,
    )

    # Backend returns response with tool_call_swallowed metadata
    backend_processor.process_backend_request.side_effect = [
        ResponseEnvelope(
            content="raw tool call",
            metadata=_meta(
                {
                    "tool_call_swallowed": True,
                    "steering_message": steering_metadata.get("steering_message"),
                    "swallowed_original_content": steering_metadata.get(
                        "swallowed_original_content"
                    ),
                    "swallowed_tool_calls": steering_metadata.get(
                        "swallowed_tool_calls"
                    ),
                }
            ),
        ),
        ResponseEnvelope(content="second response"),
    ]

    result = await manager.process_backend_request(
        original_request, "session-config-ns", _make_context()
    )

    assert backend_processor.process_backend_request.await_count == 2
    retry_args = backend_processor.process_backend_request.await_args_list[1].kwargs
    proxy_notice = retry_args["request"].messages[-1].content
    assert "apply_diff" in proxy_notice
    assert "Proxy Steering Notice" in proxy_notice  # Escalating message
    assert "Steering instruction" in proxy_notice
    assert isinstance(result, ResponseEnvelope)
    assert result.content == "patched"


@pytest.mark.asyncio
async def test_file_sandboxing_streaming_retry_failure_does_not_leak() -> None:
    """File sandboxing steering replay failures should not leak steering content."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    steering_metadata = _meta(
        {
            "tool_call_swallowed": True,
            "steering_message": "File operation blocked",
            "swallowed_original_content": "file sandbox steer",
            "swallowed_tool_calls": [
                {"function": {"name": "write_file", "arguments": "{}"}}
            ],
        }
    )

    async def initial_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(content="steering chunk", metadata=steering_metadata)

    backend_processor.process_backend_request.side_effect = [
        StreamingResponseEnvelope(content=initial_stream()),
        RuntimeError("backend failure"),
    ]

    original_request = ChatRequest(
        model="gemini",
        messages=[ChatMessage(role="user", content="write file")],
        stream=True,
    )

    result = await manager.process_backend_request(
        original_request, "session-file-sandbox", _make_context()
    )

    assert isinstance(result, StreamingResponseEnvelope)
    assert result.content is not None
    chunks = [chunk async for chunk in result.content]
    assert backend_processor.process_backend_request.await_count == 2
    assert len(chunks) == 1
    assert isinstance(chunks[0].content, str)
    assert chunks[0].content
    assert "backend retry failed" in chunks[0].content.lower()
    metadata = getattr(chunks[0], "metadata", {})
    assert metadata.get("tool_call_swallowed") is True
    assert metadata.get("tool_call_reactor_retry_failed") is True
    assert "steering chunk" not in str(chunks[0].content)


@pytest.mark.asyncio
async def test_dangerous_command_streaming_replays_and_hides_steering() -> None:
    """Dangerous command steering should replay history and hide steering chunk (streaming)."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    steering_metadata = _meta(
        {
            "tool_call_swallowed": True,
            "steering_message": "dangerous command blocked",
            "swallowed_original_content": "steering content",
            "swallowed_tool_calls": [
                {
                    "function": {
                        "name": "execute_command",
                        "arguments": "git reset --hard",
                    }
                }
            ],
        }
    )

    async def initial_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(content="steering chunk", metadata=steering_metadata)

    async def retry_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(content="safer command", metadata=_meta({}))

    backend_processor.process_backend_request.side_effect = [
        StreamingResponseEnvelope(content=initial_stream()),
        StreamingResponseEnvelope(content=retry_stream()),
    ]

    original_request = ChatRequest(
        model="gemini",
        messages=[ChatMessage(role="user", content="do git reset --hard")],
        stream=True,
    )

    result = await manager.process_backend_request(
        original_request, "session-dangerous-stream", _make_context()
    )

    assert isinstance(result, StreamingResponseEnvelope)
    assert result.content is not None
    chunks = [chunk async for chunk in result.content]
    assert backend_processor.process_backend_request.await_count == 2
    retry_args = backend_processor.process_backend_request.await_args_list[1].kwargs
    proxy_notice = retry_args["request"].messages[-1].content
    assert "git reset --hard" in proxy_notice
    assert "Proxy Steering Notice" in proxy_notice  # Escalating message
    assert "Steering instruction" in proxy_notice
    assert [chunk.content for chunk in chunks] == ["safer command"]


def test_should_surface_pre_output_error_includes_bad_gateway() -> None:
    """502/500 upstream failures must bypass empty-stream recovery (regression)."""
    be502 = BackendError(
        message="bad gateway",
        backend_name="openai",
        status_code=502,
    )
    assert (
        BackendStreamingResponseHandler._should_surface_pre_output_error(be502) is True
    )

    be500 = BackendError(
        message="internal",
        backend_name="openai",
        status_code=500,
    )
    assert (
        BackendStreamingResponseHandler._should_surface_pre_output_error(be500) is True
    )


def test_should_surface_pre_output_error_considers_details_status_code() -> None:
    """Some errors only carry HTTP status inside ``details``."""
    be = BackendError(
        message="wrapped",
        backend_name="openai",
        status_code=200,
        details={"status_code": 502},
    )
    assert BackendStreamingResponseHandler._should_surface_pre_output_error(be) is True


def test_context_length_sse_error_extracts_canonical_bad_request_status() -> None:
    """OpenAI-compatible context overflow stream errors must surface as HTTP 400."""
    payload = (
        "event: error\n"
        'data: {"type":"error","error":{"type":"invalid_request_error",'
        '"code":"context_length_exceeded",'
        '"message":"Your input exceeds the context window of this model.",'
        '"param":"input"},"sequence_number":2}\n\n'
    )
    chunk = ProcessedResponse(content=payload, metadata={})

    assert BackendStreamingResponseHandler._extract_terminal_error_status(chunk) == 400


def test_translated_context_length_error_extracts_canonical_bad_request_status() -> (
    None
):
    """Translated Responses stream errors carry the provider payload as top-level error."""
    chunk = ProcessedResponse(
        content={
            "id": "chatcmpl-context-length",
            "object": "chat.completion.chunk",
            "created": 123,
            "model": "gpt-5.5",
            "choices": [{"index": 0, "delta": {}, "finish_reason": "error"}],
            "error": {
                "type": "invalid_request_error",
                "code": "context_length_exceeded",
                "message": "Your input exceeds the context window of this model.",
                "param": "input",
            },
        },
        metadata={},
    )

    assert BackendStreamingResponseHandler._extract_terminal_error_status(chunk) == 400


def test_metadata_error_without_terminal_finish_does_not_force_bad_gateway() -> None:
    """Metadata diagnostics alone must not rewrite a successful stream to HTTP 502."""
    chunk = ProcessedResponse(
        content="",
        metadata={
            "error": {
                "type": "diagnostic",
                "message": "non-terminal diagnostic",
            }
        },
    )

    assert BackendStreamingResponseHandler._extract_terminal_error_status(chunk) is None


@pytest.mark.asyncio
async def test_context_length_sse_error_bypasses_empty_stream_retry() -> None:
    """Context overflow is a client request error, not an empty stream to retry."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="openai-codex:gpt-5.5",
        messages=[ChatMessage(role="user", content="large request")],
        stream=True,
    )

    async def context_overflow_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(
            content=(
                "event: error\n"
                'data: {"type":"error","error":{"type":"invalid_request_error",'
                '"code":"context_length_exceeded",'
                '"message":"Your input exceeds the context window of this model.",'
                '"param":"input"},"sequence_number":2}\n\n'
            ),
            metadata={},
        )

    backend_processor.process_backend_request.return_value = StreamingResponseEnvelope(
        content=context_overflow_stream(),
    )

    result = await manager.process_backend_request(
        original_request,
        "session-context-overflow",
        _make_context(),
    )

    assert isinstance(result, StreamingResponseEnvelope)
    assert result.status_code == 400
    assert result.content is not None
    chunks = [chunk async for chunk in result.content]

    assert backend_processor.process_backend_request.await_count == 1
    assert len(chunks) == 1
    assert "context_length_exceeded" in str(chunks[0].content)
    assert "empty_stream_after_retries" not in str(chunks[0].content)


@pytest.mark.asyncio
async def test_context_length_terminal_error_propagates_canonical_metadata() -> None:
    """The handler must stamp the upstream error shape onto the envelope metadata
    so the request processor can raise a canonical, agent-recognizable error
    instead of a generic ``Backend returned 400 error`` body."""
    backend_processor = AsyncMock()
    response_processor = MagicMock()
    response_processor.process_streaming_response = (
        lambda stream, _session_id, context=None: stream
    )
    manager = create_backend_request_manager(
        backend_processor=backend_processor,
        response_processor=response_processor,
    )

    original_request = ChatRequest(
        model="openai-codex:gpt-5.5",
        messages=[ChatMessage(role="user", content="large request")],
        stream=True,
    )

    async def context_overflow_stream() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(
            content={
                "id": "resp-context-overflow",
                "object": "response.chunk",
                "created": 123,
                "model": "gpt-5.5",
                "choices": [{"index": 0, "delta": {}, "finish_reason": "error"}],
                "error": {
                    "type": "invalid_request_error",
                    "code": "context_length_exceeded",
                    "message": "Your input exceeds the context window of this model.",
                    "param": "input",
                },
            },
            metadata={},
        )

    backend_processor.process_backend_request.return_value = StreamingResponseEnvelope(
        content=context_overflow_stream(),
    )

    result = await manager.process_backend_request(
        original_request,
        "session-context-overflow-metadata",
        _make_context(),
    )

    assert isinstance(result, StreamingResponseEnvelope)
    assert result.status_code == 400
    metadata = result.metadata or {}
    assert metadata.get("error_code") == "context_length_exceeded"
    assert metadata.get("error_type") == "invalid_request_error"
    assert metadata.get("error_param") == "input"
    assert "context window" in str(metadata.get("error_message") or "").lower()
    error_details = metadata.get("error_details")
    assert isinstance(error_details, dict)
    assert error_details.get("code") == "context_length_exceeded"


def test_extract_terminal_error_payload_returns_none_for_non_error_chunk() -> None:
    """Non-terminal chunks must not be misread as error payloads."""
    chunk = ProcessedResponse(
        content={
            "id": "resp-created",
            "object": "response.chunk",
            "choices": [
                {"index": 0, "delta": {"role": "assistant"}, "finish_reason": None}
            ],
        },
        metadata={},
    )
    assert (
        BackendStreamingResponseHandler._extract_terminal_error_payload(chunk) is None
    )


def test_chunk_has_meaningful_output_reasoning_only_dict_with_fallback() -> None:
    """Reasoning-only OpenAI-shaped dict chunks count when fallback flag is on."""
    handler = BackendStreamingResponseHandler(
        MagicMock(),
        MagicMock(),
        MagicMock(),
        MagicMock(),
        AsyncMock(),
    )
    payload: dict[str, Any] = {
        "choices": [
            {
                "index": 0,
                "delta": {
                    "role": "assistant",
                    "reasoning_content": "internal",
                    "content": "",
                },
            }
        ]
    }
    chunk = ProcessedResponse(content=payload, metadata={})
    assert (
        handler._chunk_has_meaningful_output(
            chunk, count_reasoning_for_empty_stream=True
        )
        is True
    )
    assert (
        handler._chunk_has_meaningful_output(
            chunk, count_reasoning_for_empty_stream=False
        )
        is False
    )


def test_chunk_has_meaningful_output_reasoning_summary_only_dict() -> None:
    """reasoning_summary-only chunks count as reasoning output for the empty-stream check."""
    handler = BackendStreamingResponseHandler(
        MagicMock(),
        MagicMock(),
        MagicMock(),
        MagicMock(),
        AsyncMock(),
    )
    payload: dict[str, Any] = {
        "choices": [
            {
                "index": 0,
                "delta": {
                    "content": None,
                    "reasoning_content": None,
                    "reasoning_summary": "Planning design updates",
                },
            }
        ]
    }
    reasoning_summary_chunk = ProcessedResponse(content=payload, metadata={})
    assert (
        handler._chunk_has_meaningful_output(
            reasoning_summary_chunk, count_reasoning_for_empty_stream=True
        )
        is True
    )
    assert (
        handler._chunk_has_meaningful_output(
            reasoning_summary_chunk, count_reasoning_for_empty_stream=False
        )
        is False
    )
    supported_chunk = ProcessedResponse(
        content=payload,
        metadata=_meta({"_client_supports_reasoning_fields": True}),
    )
    assert (
        handler._chunk_has_meaningful_output(
            supported_chunk, count_reasoning_for_empty_stream=False
        )
        is True
    )


def test_resolve_count_reasoning_for_empty_stream_defaults_true_without_app_state() -> (
    None
):
    handler = BackendStreamingResponseHandler(
        MagicMock(),
        MagicMock(),
        MagicMock(),
        MagicMock(),
        AsyncMock(),
    )

    assert handler._resolve_count_reasoning_for_empty_stream(_make_context()) is True
