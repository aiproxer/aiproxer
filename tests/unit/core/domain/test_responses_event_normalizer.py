"""Unit tests for ResponsesEventNormalizer and semantic event models."""

from __future__ import annotations

import asyncio
from collections.abc import AsyncGenerator, AsyncIterator
from typing import Any

import pytest
from src.core.domain.responses_event_normalizer import (
    ResponsesEventNormalizer,
    ResponsesStreamSource,
)
from src.core.domain.responses_semantic_events import (
    ResponsesSemanticEvent,
    ResponsesSemanticEventType,
)
from src.core.domain.responses_wire_renderer import ResponsesWireRenderer
from src.core.interfaces.response_processor_interface import ProcessedResponse
from src.core.services.in_memory_responses_session_store import (
    InMemoryResponsesSessionStore,
)


async def _collect(
    gen: AsyncIterator[ResponsesSemanticEvent],
) -> list[ResponsesSemanticEvent]:
    return [e async for e in gen]


def _chunks(*items: Any) -> AsyncGenerator[Any, None]:
    async def _gen() -> AsyncGenerator[Any, None]:
        for it in items:
            yield it

    return _gen()


@pytest.mark.asyncio
@pytest.mark.parametrize("finish", [True, False])
async def test_legacy_responses_lifecycle_has_sdk_required_metadata(
    finish: bool,
) -> None:
    normalizer = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS, response_id="resp_sdk"
    )
    chunks: list[dict[str, Any]] = [
        {
            "id": "resp_sdk",
            "model": "test-model",
            "created": 1790860000,
            "choices": [{"delta": {"content": "hello"}}],
        },
        {
            "choices": [],
            "usage": {
                "prompt_tokens": 17,
                "completion_tokens": 5,
                "total_tokens": 22,
                "prompt_tokens_details": {"cached_tokens": 7},
                "completion_tokens_details": {"reasoning_tokens": 2},
            },
        },
    ]
    if finish:
        chunks.append({"choices": [{"delta": {}, "finish_reason": "stop"}]})
    events = await _collect(normalizer.normalize(_chunks(*chunks)))
    created = next(
        e for e in events if e.type == ResponsesSemanticEventType.RESPONSE_CREATED
    )
    assert created.response is not None
    assert created.response["created_at"] == 1790860000
    terminal = events[-1]
    assert terminal.response is not None
    assert terminal.response["usage"] == {
        "input_tokens": 17,
        "output_tokens": 5,
        "total_tokens": 22,
        "input_tokens_details": {"cached_tokens": 7},
        "output_tokens_details": {"reasoning_tokens": 2},
    }


@pytest.mark.asyncio
async def test_legacy_responses_tool_terminal_has_numeric_usage_without_provider_usage() -> (
    None
):
    normalizer = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS,
        response_id="resp_tool_sdk",
    )
    events = await _collect(
        normalizer.normalize(
            _chunks(
                {
                    "choices": [
                        {
                            "delta": {
                                "tool_calls": [
                                    {
                                        "id": "call_read",
                                        "function": {"name": "read", "arguments": "{}"},
                                    }
                                ]
                            },
                            "finish_reason": "tool_calls",
                        }
                    ]
                }
            )
        )
    )
    assert events[0].response is not None
    assert isinstance(events[0].response["created_at"], int)
    assert isinstance(events[0].response["model"], str)
    assert events[-1].response is not None
    assert events[-1].response["usage"] == {
        "input_tokens": 0,
        "output_tokens": 0,
        "total_tokens": 0,
    }
    assert events[-1].response["output"][0]["call_id"] == "call_read"


@pytest.mark.asyncio
async def test_openai_maps_lifecycle_and_positions() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="resp_x"
    )
    chunks = _chunks(
        {
            "type": "response.created",
            "response": {"id": "resp_real", "model": "gpt-4"},
        },
        {"type": "response.in_progress", "response": {"id": "resp_real"}},
        {
            "type": "response.output_item.added",
            "output_index": 0,
            "item": {"id": "item_1", "type": "message", "role": "assistant"},
        },
        {
            "type": "response.content_part.added",
            "output_index": 0,
            "content_index": 0,
            "part": {"type": "output_text"},
        },
        {
            "type": "response.output_text.delta",
            "output_index": 0,
            "content_index": 0,
            "item_id": "item_1",
            "delta": "hi",
        },
        {
            "type": "response.output_text.done",
            "output_index": 0,
            "content_index": 0,
            "item_id": "item_1",
            "text": "hi",
        },
        {
            "type": "response.content_part.done",
            "output_index": 0,
            "content_index": 0,
            "item_id": "item_1",
        },
        {
            "type": "response.output_item.done",
            "output_index": 0,
            "item": {"id": "item_1", "type": "message", "status": "completed"},
        },
        {"type": "response.completed", "response": {"id": "resp_real"}},
    )
    events = await _collect(n.normalize(chunks))
    assert [e.type for e in events[:-1]] == [
        ResponsesSemanticEventType.RESPONSE_CREATED,
        ResponsesSemanticEventType.RESPONSE_IN_PROGRESS,
        ResponsesSemanticEventType.OUTPUT_ITEM_ADDED,
        ResponsesSemanticEventType.CONTENT_PART_ADDED,
        ResponsesSemanticEventType.TEXT_DELTA,
        ResponsesSemanticEventType.TEXT_DONE,
        ResponsesSemanticEventType.CONTENT_PART_DONE,
        ResponsesSemanticEventType.OUTPUT_ITEM_DONE,
    ]
    assert events[-1].type == ResponsesSemanticEventType.RESPONSE_COMPLETED
    assert list(range(len(events))) == [e.sequence_number for e in events]
    td = next(e for e in events if e.type == ResponsesSemanticEventType.TEXT_DELTA)
    assert td.output_index == 0
    assert td.content_index == 0
    assert td.item_id == "item_1"
    assert td.delta == "hi"
    assert td.response_id == "resp_real"
    cr = events[0]
    assert cr.response is not None
    assert cr.response.get("id") == "resp_real"


@pytest.mark.asyncio
async def test_openai_passthrough_unknown_response_family() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="resp_x"
    )
    raw = {
        "type": "response.file_search.in_progress",
        "output_index": 1,
        "content_index": 2,
        "item_id": "fs_1",
        "foo": {"bar": 1},
    }
    chunks = _chunks(raw, {"type": "response.completed", "response": {"id": "resp_x"}})
    events = await _collect(n.normalize(chunks))
    pt = events[0]
    assert pt.type == ResponsesSemanticEventType.PASSTHROUGH
    assert pt.raw == raw
    assert pt.output_index == 1
    assert pt.content_index == 2
    assert pt.item_id == "fs_1"
    assert events[-1].type == ResponsesSemanticEventType.RESPONSE_COMPLETED


@pytest.mark.asyncio
async def test_openai_tool_call_args_events() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="r1"
    )
    chunks = _chunks(
        {
            "type": "response.function_call_arguments.delta",
            "output_index": 0,
            "content_index": 0,
            "item_id": "call_1",
            "name": "bash",
            "delta": '{"c',
        },
        {
            "type": "response.function_call_arguments.done",
            "output_index": 0,
            "content_index": 0,
            "item_id": "call_1",
            "name": "bash",
            "arguments": '{"command":"ls"}',
        },
        {"type": "response.completed", "response": {"id": "r1"}},
    )
    events = await _collect(n.normalize(chunks))
    assert events[0].type == ResponsesSemanticEventType.TOOL_CALL_ARGS_DELTA
    assert events[0].delta == '{"c'
    assert events[1].type == ResponsesSemanticEventType.TOOL_CALL_ARGS_DONE
    assert events[1].text == '{"command":"ls"}'


@pytest.mark.asyncio
async def test_openai_failed_and_incomplete() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="r1"
    )
    fail_chunks = _chunks(
        {
            "type": "response.failed",
            "response": {"id": "r1", "error": {"message": "boom"}},
        }
    )
    fe = await _collect(n.normalize(fail_chunks))
    assert fe[-1].type == ResponsesSemanticEventType.RESPONSE_FAILED
    assert fe[-1].error is not None

    inc_chunks = _chunks(
        {
            "type": "response.incomplete",
            "response": {"id": "r2", "status": "incomplete"},
        }
    )
    ie = await _collect(
        ResponsesEventNormalizer(
            source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="r2"
        ).normalize(inc_chunks)
    )
    assert ie[-1].type == ResponsesSemanticEventType.RESPONSE_INCOMPLETE


@pytest.mark.asyncio
async def test_openai_response_done_maps_to_completed() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="r1"
    )
    events = await _collect(
        n.normalize(_chunks({"type": "response.done", "response": {"id": "r1"}}))
    )
    assert events[-1].type == ResponsesSemanticEventType.RESPONSE_COMPLETED


@pytest.mark.asyncio
async def test_legacy_chat_stream_end_closes_text_lifecycle_once() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS,
        response_id="resp_fallback",
    )
    events = await _collect(
        n.normalize(
            _chunks(
                {
                    "id": "chat_exact",
                    "model": "cursor/glm-5.2-max",
                    "choices": [{"delta": {"content": "OK"}, "finish_reason": None}],
                },
            )
        )
    )

    assert [event.type for event in events[-4:]] == [
        ResponsesSemanticEventType.TEXT_DONE,
        ResponsesSemanticEventType.CONTENT_PART_DONE,
        ResponsesSemanticEventType.OUTPUT_ITEM_DONE,
        ResponsesSemanticEventType.RESPONSE_COMPLETED,
    ]
    assert {event.response_id for event in events} == {"chat_exact"}
    assert (
        sum(
            event.type == ResponsesSemanticEventType.RESPONSE_COMPLETED
            for event in events
        )
        == 1
    )


@pytest.mark.asyncio
async def test_anthropic_sse_string_mapping() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.ANTHROPIC, response_id="resp_ant"
    )
    sse = (
        'event: message_start\ndata: {"type":"message_start","message":{"id":"msg_1"}}\n\n'
        'event: content_block_start\ndata: {"type":"content_block_start","index":0,'
        '"content_block":{"type":"text","text":""}}\n\n'
        'event: content_block_delta\ndata: {"type":"content_block_delta","index":0,'
        '"delta":{"type":"text_delta","text":"Hello"}}\n\n'
        'event: content_block_stop\ndata: {"type":"content_block_stop","index":0}\n\n'
        'event: message_stop\ndata: {"type":"message_stop"}\n\n'
    )
    events = await _collect(n.normalize(_chunks(sse)))
    types = [e.type for e in events]
    assert ResponsesSemanticEventType.RESPONSE_CREATED in types
    assert ResponsesSemanticEventType.RESPONSE_IN_PROGRESS in types
    assert ResponsesSemanticEventType.TEXT_DELTA in types
    assert types[-1] == ResponsesSemanticEventType.RESPONSE_COMPLETED
    td = next(e for e in events if e.type == ResponsesSemanticEventType.TEXT_DELTA)
    assert td.delta == "Hello"
    assert td.output_index == 0
    assert td.content_index == 0


@pytest.mark.asyncio
async def test_anthropic_message_stop_preserves_text_tool_text_order() -> None:
    """Interleaved text / tool_use / text blocks must appear in index order in output."""
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.ANTHROPIC, response_id="resp_ctx"
    )
    chunks = _chunks(
        {"type": "message_start", "message": {"id": "root-msg"}},
        {
            "type": "content_block_start",
            "index": 0,
            "content_block": {"type": "text", "text": ""},
        },
        {
            "type": "content_block_delta",
            "index": 0,
            "delta": {"type": "text_delta", "text": "First"},
        },
        {"type": "content_block_stop", "index": 0},
        {
            "type": "content_block_start",
            "index": 1,
            "content_block": {
                "type": "tool_use",
                "id": "toolu_01",
                "name": "get_x",
            },
        },
        {
            "type": "content_block_delta",
            "index": 1,
            "delta": {"type": "input_json_delta", "partial_json": "{}"},
        },
        {"type": "content_block_stop", "index": 1},
        {
            "type": "content_block_start",
            "index": 2,
            "content_block": {"type": "text", "text": ""},
        },
        {
            "type": "content_block_delta",
            "index": 2,
            "delta": {"type": "text_delta", "text": "Last"},
        },
        {"type": "content_block_stop", "index": 2},
        {"type": "message_stop"},
    )
    events = await _collect(n.normalize(chunks))
    done = events[-1]
    assert done.type == ResponsesSemanticEventType.RESPONSE_COMPLETED
    resp = done.response
    assert isinstance(resp, dict)
    out = resp.get("output")
    assert isinstance(out, list)
    kinds = [o.get("type") for o in out if isinstance(o, dict)]
    assert kinds == ["message", "function_call", "message"]
    assert out[0]["content"][0]["text"] == "First"
    assert out[1].get("name") == "get_x"
    assert out[2]["content"][0]["text"] == "Last"


@pytest.mark.asyncio
async def test_anthropic_processed_response_dict() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.ANTHROPIC, response_id="resp_ant"
    )
    chunks = _chunks(
        ProcessedResponse(
            content={
                "type": "content_block_delta",
                "index": 1,
                "delta": {"type": "input_json_delta", "partial_json": '{"a":'},
            }
        ),
        ProcessedResponse(content={"type": "message_stop"}),
    )
    events = await _collect(n.normalize(chunks))
    assert any(
        e.type == ResponsesSemanticEventType.TOOL_CALL_ARGS_DELTA for e in events
    )
    assert events[-1].type == ResponsesSemanticEventType.RESPONSE_COMPLETED


@pytest.mark.asyncio
async def test_gemini_dict_text_and_terminal() -> None:
    n = ResponsesEventNormalizer(source=ResponsesStreamSource.GEMINI, response_id="rg")
    chunks = _chunks(
        {
            "candidates": [
                {"content": {"parts": [{"text": "Hel"}]}, "index": 0},
            ]
        },
        {
            "candidates": [
                {
                    "content": {"parts": [{"text": "lo"}]},
                    "finishReason": "STOP",
                    "index": 0,
                }
            ]
        },
    )
    events = await _collect(n.normalize(chunks))
    assert events[0].type == ResponsesSemanticEventType.RESPONSE_CREATED
    assert events[1].type == ResponsesSemanticEventType.RESPONSE_IN_PROGRESS
    deltas = [e for e in events if e.type == ResponsesSemanticEventType.TEXT_DELTA]
    assert "".join((e.delta or "") for e in deltas) == "Hello"
    assert events[-1].type == ResponsesSemanticEventType.RESPONSE_COMPLETED


@pytest.mark.asyncio
async def test_anthropic_stream_round_trip_persists_previous_response_items() -> None:
    store = InMemoryResponsesSessionStore()
    normalizer = ResponsesEventNormalizer(
        source=ResponsesStreamSource.ANTHROPIC,
        response_id="resp_ant",
    )
    renderer = ResponsesWireRenderer(
        store,
        transport="websocket",
        realtime_websocket_terminal=True,
    )

    sse = (
        'event: message_start\ndata: {"type":"message_start","message":{"id":"msg_1"}}\n\n'
        'event: content_block_start\ndata: {"type":"content_block_start","index":0,'
        '"content_block":{"type":"text","text":""}}\n\n'
        'event: content_block_delta\ndata: {"type":"content_block_delta","index":0,'
        '"delta":{"type":"text_delta","text":"Hello"}}\n\n'
        'event: content_block_stop\ndata: {"type":"content_block_stop","index":0}\n\n'
        'event: message_stop\ndata: {"type":"message_stop"}\n\n'
    )

    _ = [
        frame
        async for frame in renderer.render(
            normalizer.normalize(_chunks(sse)), "resp_ant"
        )
    ]
    resolved = await store.resolve("resp_ant")
    assert resolved is not None
    assert len(resolved.output_items) == 1
    assert resolved.output_items[0].id == "msg_1"
    assert resolved.output_items[0].content is not None
    assert resolved.output_items[0].content[0].text == "Hello"


@pytest.mark.asyncio
async def test_gemini_stream_round_trip_persists_previous_response_items() -> None:
    store = InMemoryResponsesSessionStore()
    normalizer = ResponsesEventNormalizer(
        source=ResponsesStreamSource.GEMINI,
        response_id="resp_gem",
    )
    renderer = ResponsesWireRenderer(
        store,
        transport="websocket",
        realtime_websocket_terminal=True,
    )

    chunks = _chunks(
        {
            "candidates": [
                {"content": {"parts": [{"text": "Hel"}]}, "index": 0},
            ]
        },
        {
            "candidates": [
                {
                    "content": {"parts": [{"text": "lo"}]},
                    "finishReason": "STOP",
                    "index": 0,
                }
            ]
        },
    )

    _ = [
        frame
        async for frame in renderer.render(normalizer.normalize(chunks), "resp_gem")
    ]
    resolved = await store.resolve("resp_gem")
    assert resolved is not None
    assert len(resolved.output_items) == 1
    assert resolved.output_items[0].role == "assistant"
    assert resolved.output_items[0].content is not None
    assert resolved.output_items[0].content[0].text == "Hello"


@pytest.mark.asyncio
async def test_sequence_monotonic_from_zero_all_events() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="r"
    )
    events = await _collect(
        n.normalize(
            _chunks(
                {"type": "response.created", "response": {"id": "r"}},
                {"type": "response.completed", "response": {"id": "r"}},
            )
        )
    )
    assert [e.sequence_number for e in events] == [0, 1]


@pytest.mark.asyncio
async def test_terminal_completed_when_missing() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="r"
    )
    events = await _collect(
        n.normalize(_chunks({"type": "response.created", "response": {"id": "r"}}))
    )
    assert events[-1].type == ResponsesSemanticEventType.RESPONSE_COMPLETED


@pytest.mark.asyncio
async def test_mid_stream_exception_emits_failed_terminal() -> None:
    async def bad() -> AsyncGenerator[Any, None]:
        yield {"type": "response.created", "response": {"id": "r"}}
        raise RuntimeError("upstream exploded")

    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="r"
    )
    events = await _collect(n.normalize(bad()))
    assert events[0].type == ResponsesSemanticEventType.RESPONSE_CREATED
    assert events[-1].type == ResponsesSemanticEventType.RESPONSE_FAILED
    assert events[-1].error is not None


@pytest.mark.asyncio
async def test_stops_consuming_upstream_after_completed() -> None:
    """After response.completed, do not wait for upstream EOF before finishing.

    chatgpt-plan / OpenAI Responses HTTP SSE may keep the body open after the
    terminal event; hanging here leaves OpenCode waiting until manual close.
    """
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="r"
    )

    async def hanging_upstream() -> AsyncGenerator[Any, None]:
        yield {"type": "response.output_text.delta", "delta": "hi", "item_id": "i1"}
        yield {"type": "response.completed", "response": {"id": "r"}}
        # If the normalizer keeps reading, this Future never resolves and the
        # test times out — that is the production hang.
        await asyncio.Future()

    events = await asyncio.wait_for(
        _collect(n.normalize(hanging_upstream())),
        timeout=0.5,
    )
    assert events[-1].type == ResponsesSemanticEventType.RESPONSE_COMPLETED
    assert any(e.type == ResponsesSemanticEventType.RESPONSE_COMPLETED for e in events)


@pytest.mark.asyncio
async def test_stops_consuming_after_legacy_finish_reason() -> None:
    """Translated finish_reason=stop must also end upstream consumption."""
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="rid"
    )

    async def hanging_upstream() -> AsyncGenerator[Any, None]:
        yield {
            "id": "rid",
            "object": "response.chunk",
            "created": 1,
            "model": "gpt",
            "choices": [
                {"index": 0, "delta": {"content": "hi"}, "finish_reason": None}
            ],
        }
        yield {
            "id": "rid",
            "object": "response.chunk",
            "created": 1,
            "model": "gpt",
            "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
        }
        await asyncio.Future()

    events = await asyncio.wait_for(
        _collect(n.normalize(hanging_upstream())),
        timeout=0.5,
    )
    assert events[-1].type == ResponsesSemanticEventType.RESPONSE_COMPLETED


@pytest.mark.asyncio
async def test_no_duplicate_terminal_after_completed() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="r"
    )

    async def gen() -> AsyncGenerator[Any, None]:
        yield {"type": "response.completed", "response": {"id": "r"}}

    events = await _collect(n.normalize(gen()))
    assert (
        sum(
            1 for e in events if e.type == ResponsesSemanticEventType.RESPONSE_COMPLETED
        )
        == 1
    )


@pytest.mark.asyncio
async def test_openai_legacy_role_only_delta_does_not_emit_passthrough() -> None:
    """Role-only chat completion chunks must not leak as PASSTHROUGH on Responses wire."""
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="rid_role"
    )
    events = await _collect(
        n.normalize(
            _chunks(
                {
                    "id": "cc-1",
                    "object": "chat.completion.chunk",
                    "model": "gpt-4o",
                    "choices": [
                        {
                            "index": 0,
                            "delta": {"role": "assistant"},
                            "finish_reason": None,
                        }
                    ],
                }
            )
        )
    )
    assert not any(
        e.type == ResponsesSemanticEventType.PASSTHROUGH for e in events
    ), events


@pytest.mark.asyncio
async def test_openai_legacy_chat_chunk_maps_lifecycle_and_text_delta() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="rid_default"
    )
    events = await _collect(
        n.normalize(
            _chunks(
                {
                    "id": "resp-chunk-1",
                    "object": "response.chunk",
                    "model": "mock-model",
                    "choices": [
                        {
                            "index": 0,
                            "delta": {"content": "Hello world", "role": "assistant"},
                            "finish_reason": None,
                        }
                    ],
                }
            )
        )
    )
    assert events[0].type == ResponsesSemanticEventType.RESPONSE_CREATED
    assert events[1].type == ResponsesSemanticEventType.RESPONSE_IN_PROGRESS
    text_delta = next(
        event for event in events if event.type == ResponsesSemanticEventType.TEXT_DELTA
    )
    assert text_delta.delta == "Hello world"
    assert events[-1].type == ResponsesSemanticEventType.RESPONSE_COMPLETED


@pytest.mark.asyncio
async def test_acp_legacy_chat_stream_emits_complete_responses_lifecycle_once() -> None:
    normalizer = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS,
        response_id="resp_acp",
    )
    events = await _collect(
        normalizer.normalize(
            _chunks(
                'data: {"id":"chat-acp","object":"chat.completion.chunk",'
                '"model":"cursor/glm-5.2-max","choices":[{"index":0,'
                '"delta":{"content":"OK"},"finish_reason":null}]}\n\n',
                'data: {"id":"chat-acp","object":"chat.completion.chunk",'
                '"model":"cursor/glm-5.2-max","choices":[{"index":0,'
                '"delta":{},"finish_reason":"stop"}]}\n\n',
                "data: [DONE]\n\n",
            )
        )
    )

    assert [event.type for event in events] == [
        ResponsesSemanticEventType.RESPONSE_CREATED,
        ResponsesSemanticEventType.RESPONSE_IN_PROGRESS,
        ResponsesSemanticEventType.OUTPUT_ITEM_ADDED,
        ResponsesSemanticEventType.CONTENT_PART_ADDED,
        ResponsesSemanticEventType.TEXT_DELTA,
        ResponsesSemanticEventType.TEXT_DONE,
        ResponsesSemanticEventType.CONTENT_PART_DONE,
        ResponsesSemanticEventType.OUTPUT_ITEM_DONE,
        ResponsesSemanticEventType.RESPONSE_COMPLETED,
    ]
    assert [event.sequence_number for event in events] == list(range(len(events)))
    assert (
        sum(
            event.type
            in {
                ResponsesSemanticEventType.RESPONSE_COMPLETED,
                ResponsesSemanticEventType.RESPONSE_FAILED,
                ResponsesSemanticEventType.RESPONSE_INCOMPLETE,
            }
            for event in events
        )
        == 1
    )


@pytest.mark.asyncio
async def test_acp_legacy_chat_error_emits_failed_terminal_once() -> None:
    normalizer = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS,
        response_id="resp_acp_error",
    )
    events = await _collect(
        normalizer.normalize(
            _chunks(
                {
                    "object": "chat.completion.chunk",
                    "model": "cursor/glm-5.2-max",
                    "choices": [{"index": 0, "delta": {}, "finish_reason": "error"}],
                    "error": {
                        "message": "ACP process exited",
                        "code": "backend_error",
                    },
                },
                "data: [DONE]\n\n",
            )
        )
    )

    assert events[-1].type == ResponsesSemanticEventType.RESPONSE_FAILED
    assert events[-1].error == {
        "message": "ACP process exited",
        "code": "backend_error",
    }
    assert (
        sum(
            event.type
            in {
                ResponsesSemanticEventType.RESPONSE_COMPLETED,
                ResponsesSemanticEventType.RESPONSE_FAILED,
                ResponsesSemanticEventType.RESPONSE_INCOMPLETE,
            }
            for event in events
        )
        == 1
    )


@pytest.mark.asyncio
async def test_acp_empty_stream_emits_lifecycle_before_completed() -> None:
    normalizer = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS,
        response_id="resp_acp_empty",
    )

    events = await _collect(normalizer.normalize(_chunks()))

    assert [event.type for event in events] == [
        ResponsesSemanticEventType.RESPONSE_CREATED,
        ResponsesSemanticEventType.RESPONSE_IN_PROGRESS,
        ResponsesSemanticEventType.RESPONSE_COMPLETED,
    ]
    assert [event.sequence_number for event in events] == [0, 1, 2]
    assert [event.response_id for event in events] == [
        "resp_acp_empty",
        "resp_acp_empty",
        "resp_acp_empty",
    ]


@pytest.mark.asyncio
async def test_acp_role_only_chunk_emits_lifecycle_before_completed() -> None:
    normalizer = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS,
        response_id="resp_acp_role",
    )
    events = await _collect(
        normalizer.normalize(
            _chunks(
                {
                    "id": "chat-role",
                    "model": "cursor/glm-5.2-max",
                    "choices": [
                        {
                            "index": 0,
                            "delta": {"role": "assistant"},
                            "finish_reason": None,
                        }
                    ],
                }
            )
        )
    )

    assert [event.type for event in events] == [
        ResponsesSemanticEventType.RESPONSE_CREATED,
        ResponsesSemanticEventType.RESPONSE_IN_PROGRESS,
        ResponsesSemanticEventType.RESPONSE_COMPLETED,
    ]
    assert [event.sequence_number for event in events] == [0, 1, 2]
    assert events[0].response is not None
    assert events[0].response == {
        "id": "chat-role",
        "model": "cursor/glm-5.2-max",
        "object": "response",
        "created_at": events[0].response["created_at"],
    }
    assert isinstance(events[0].response["created_at"], int)


@pytest.mark.asyncio
async def test_acp_backend_error_starts_lifecycle_before_failed() -> None:
    async def bad() -> AsyncGenerator[Any, None]:
        raise RuntimeError("ACP process exited")
        yield  # pragma: no cover

    normalizer = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS,
        response_id="resp_acp_backend_error",
    )
    events = await _collect(normalizer.normalize(bad()))

    assert [event.type for event in events] == [
        ResponsesSemanticEventType.RESPONSE_CREATED,
        ResponsesSemanticEventType.RESPONSE_IN_PROGRESS,
        ResponsesSemanticEventType.RESPONSE_FAILED,
    ]
    assert [event.sequence_number for event in events] == [0, 1, 2]
    assert events[-1].error == {
        "message": "ACP process exited",
        "type": "RuntimeError",
    }


@pytest.mark.asyncio
async def test_processed_response_metadata_tool_calls_yield_semantic_tool_events() -> (
    None
):
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES, response_id="rid_default"
    )

    async def gen() -> AsyncGenerator[Any, None]:
        yield ProcessedResponse(
            content="",
            metadata={
                "tool_calls": [
                    {
                        "id": "call_abc",
                        "type": "function",
                        "function": {
                            "name": "fetch_data",
                            "arguments": '{"query": "status"}',
                        },
                    }
                ]
            },
        )
        yield ProcessedResponse(content="", metadata={"is_done": True})

    events = await _collect(n.normalize(gen()))
    types = [e.type for e in events]
    assert ResponsesSemanticEventType.TOOL_CALL_ARGS_DELTA in types
    assert ResponsesSemanticEventType.TOOL_CALL_ARGS_DONE in types
    assert ResponsesSemanticEventType.OUTPUT_ITEM_DONE in types
    assert types[-1] == ResponsesSemanticEventType.RESPONSE_COMPLETED
