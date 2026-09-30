"""Unit tests for ResponsesWireRenderer (SSE and WebSocket wire surfaces)."""

from __future__ import annotations

import json
from collections.abc import AsyncGenerator
from typing import Any

import pytest
from src.core.domain.responses_domain import ResponsesOutputItem
from src.core.domain.responses_semantic_events import (
    ResponsesSemanticEvent,
    ResponsesSemanticEventType,
)
from src.core.domain.responses_wire_renderer import ResponsesWireRenderer
from src.core.services.in_memory_responses_session_store import (
    InMemoryResponsesSessionStore,
)


def _ev(
    *,
    etype: ResponsesSemanticEventType,
    response_id: str = "resp_1",
    sequence_number: int = 0,
    **kwargs: Any,
) -> ResponsesSemanticEvent:
    base: dict[str, Any] = {
        "type": etype,
        "response_id": response_id,
        "sequence_number": sequence_number,
    }
    base.update(kwargs)
    return ResponsesSemanticEvent.model_validate(base)


async def _collect_wire(
    gen: AsyncGenerator[str | dict[str, Any], None],
) -> list[str | dict[str, Any]]:
    return [x async for x in gen]


def _parse_sse_frames(sse: str) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    for block in sse.split("\n\n"):
        block = block.strip()
        if not block:
            continue
        for line in block.splitlines():
            line = line.strip()
            if line.startswith("data:"):
                payload = line[5:].lstrip()
                if payload == "[DONE]":
                    out.append({"__sse__": "[DONE]"})
                else:
                    out.append(json.loads(payload))
    return out


class _OrderingSpyStore(InMemoryResponsesSessionStore):
    """Records when store() completes relative to consumer-visible SSE frames."""

    def __init__(self) -> None:
        super().__init__()
        self.timeline: list[str] = []

    async def store(self, *args: Any, **kwargs: Any) -> None:
        self.timeline.append("store_enter")
        await super().store(*args, **kwargs)
        self.timeline.append("store_leave")


@pytest.mark.asyncio
async def test_sse_done_is_not_blocked_by_session_store() -> None:
    """[DONE] must reach the client even if session store awaits/hangs.

    OpenCode finalizes on response.completed and may abort the SSE reader.
    If [DONE] is only yielded after store(), the client retries the turn and
    loops (assistant-only follow-ups that claim tool output was "shown above").
    """
    store = _OrderingSpyStore()
    renderer = ResponsesWireRenderer(store, transport="sse")

    async def events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_COMPLETED,
            sequence_number=0,
            response={"id": "resp_chain", "output": []},
        )

    async for frame in renderer.render(events(), "resp_chain"):
        if isinstance(frame, str) and "[DONE]" in frame:
            store.timeline.append("yield_sse_done")
        elif isinstance(frame, str):
            store.timeline.append("yield_sse_data")

    assert "yield_sse_done" in store.timeline
    assert "store_leave" in store.timeline
    # DONE must not wait on store completion.
    assert store.timeline.index("yield_sse_done") < store.timeline.index("store_enter")


@pytest.mark.asyncio
async def test_sse_coalesces_completed_and_done_in_one_frame() -> None:
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(store, transport="sse")

    async def events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_COMPLETED,
            sequence_number=0,
            response={"id": "resp_coalesce", "output": []},
        )

    frames = [frame async for frame in renderer.render(events(), "resp_coalesce")]
    assert len(frames) == 1
    assert isinstance(frames[0], str)
    assert '"type": "response.completed"' in frames[0] or '"type":"response.completed"' in frames[0].replace(" ", "")
    assert "data: [DONE]" in frames[0]


@pytest.mark.asyncio
async def test_sse_ordering_ends_with_typed_terminal_and_done() -> None:
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(store, transport="sse")

    async def events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_CREATED,
            sequence_number=0,
            response={"id": "resp_1", "model": "gpt-4o"},
        )
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_IN_PROGRESS, sequence_number=1
        )
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_COMPLETED,
            sequence_number=2,
            response={"id": "resp_1", "output": []},
        )

    chunks: list[str] = []
    async for frame in renderer.render(events(), "resp_1"):
        assert isinstance(frame, str)
        chunks.append(frame)

    merged = "".join(chunks)
    parsed = _parse_sse_frames(merged)
    types = [p.get("type") for p in parsed if "__sse__" not in p]
    assert types == ["response.created", "response.in_progress", "response.completed"]
    assert parsed[-1] == {"__sse__": "[DONE]"}


@pytest.mark.asyncio
async def test_sse_can_end_at_typed_terminal_without_done_sentinel() -> None:
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(store, transport="sse")

    async def events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_COMPLETED,
            sequence_number=0,
            response={"id": "resp_no_sentinel", "output": []},
        )

    frames = await _collect_wire(
        renderer.render(
            events(),
            "resp_no_sentinel",
            emit_done_sentinel=False,
        )
    )

    assert all(frame != "data: [DONE]\n\n" for frame in frames)


@pytest.mark.asyncio
async def test_sse_never_emits_response_done() -> None:
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(
        store, transport="sse", realtime_websocket_terminal=True
    )

    async def events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_COMPLETED,
            sequence_number=0,
            response={"id": "r", "output": []},
        )

    sse_parts: list[str] = []
    async for f in renderer.render(events(), "r"):
        assert isinstance(f, str)
        sse_parts.append(f)
    merged = "".join(sse_parts)
    for block in merged.split("\n\n"):
        if "data:" in block and "[DONE]" not in block:
            obj = json.loads(block.split("data:", 1)[1].strip())
            assert obj.get("type") != "response.done"


@pytest.mark.asyncio
async def test_ws_responses_surface_uses_completed_not_done() -> None:
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(
        store,
        transport="websocket",
        realtime_websocket_terminal=False,
    )

    async def events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_COMPLETED,
            sequence_number=0,
            response={"id": "r", "output": []},
        )

    frames = await _collect_wire(renderer.render(events(), "r"))
    assert len(frames) == 1
    first = frames[0]
    assert isinstance(first, dict)
    assert first["type"] == "response.completed"
    assert first.get("type") != "response.done"


@pytest.mark.asyncio
async def test_ws_realtime_terminal_maps_completed_to_response_done() -> None:
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(
        store,
        transport="websocket",
        realtime_websocket_terminal=True,
    )

    async def events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_COMPLETED,
            sequence_number=0,
            response={"id": "r", "output": []},
        )

    frames = await _collect_wire(renderer.render(events(), "r"))
    first_rt = frames[0]
    assert isinstance(first_rt, dict)
    assert first_rt["type"] == "response.done"


@pytest.mark.asyncio
async def test_sequence_number_progression_and_official_fields() -> None:
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(store, transport="sse")

    async def events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.TEXT_DELTA,
            sequence_number=5,
            output_index=0,
            content_index=1,
            item_id="it_1",
            delta="x",
        )
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_COMPLETED,
            sequence_number=6,
            response={"id": "resp_1"},
        )

    seq_parts: list[str] = []
    async for f in renderer.render(events(), "resp_1"):
        assert isinstance(f, str)
        seq_parts.append(f)
    merged = "".join(seq_parts)
    parsed = [p for p in _parse_sse_frames(merged) if "__sse__" not in p]
    assert parsed[0]["sequence_number"] == 5
    assert parsed[0]["output_index"] == 0
    assert parsed[0]["content_index"] == 1
    assert parsed[0]["item_id"] == "it_1"
    assert parsed[0]["type"] == "response.output_text.delta"


@pytest.mark.asyncio
async def test_passthrough_preserves_payload_with_sequence() -> None:
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(store, transport="sse")

    raw = {
        "type": "response.file_search.in_progress",
        "foo": {"bar": 1},
        "item_id": "fs",
    }

    async def events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.PASSTHROUGH,
            sequence_number=9,
            raw=raw,
        )
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_COMPLETED,
            sequence_number=10,
            response={"id": "resp_1"},
        )

    pt_parts: list[str] = []
    async for f in renderer.render(events(), "resp_1"):
        assert isinstance(f, str)
        pt_parts.append(f)
    merged = "".join(pt_parts)
    parsed = [p for p in _parse_sse_frames(merged) if "__sse__" not in p]
    assert parsed[0]["type"] == "response.file_search.in_progress"
    assert parsed[0]["sequence_number"] == 9
    assert parsed[0]["foo"] == {"bar": 1}


@pytest.mark.asyncio
async def test_session_store_called_after_terminal_with_output_items() -> None:
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(store, transport="websocket")

    item_done = {
        "id": "msg_1",
        "type": "message",
        "role": "assistant",
        "status": "completed",
    }

    async def events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.OUTPUT_ITEM_DONE,
            sequence_number=0,
            output_index=0,
            item=item_done,
        )
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_COMPLETED,
            sequence_number=1,
            response={"id": "resp_x", "output": [item_done]},
        )

    await _collect_wire(
        renderer.render(
            events(),
            "resp_x",
            instructions="sys",
            ttl_seconds=120,
        )
    )

    resolved = await store.resolve("resp_x")
    assert resolved is not None
    assert resolved.instructions == "sys"
    assert resolved.history_items == [ResponsesOutputItem.model_validate(item_done)]
    assert len(resolved.output_items) == 1
    assert resolved.output_items[0].id == "msg_1"


@pytest.mark.asyncio
async def test_failed_and_incomplete_sse_terminal() -> None:
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(store, transport="sse")

    async def events_failed() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_FAILED,
            sequence_number=0,
            error={"message": "bad", "type": "invalid_request"},
            response={"id": "resp_1"},
        )

    fail_parts: list[str] = []
    async for f in renderer.render(events_failed(), "resp_1"):
        assert isinstance(f, str)
        fail_parts.append(f)
    merged_f = "".join(fail_parts)
    parsed_f = _parse_sse_frames(merged_f)
    assert parsed_f[-2]["type"] == "response.failed"
    assert parsed_f[-1] == {"__sse__": "[DONE]"}

    async def events_inc() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_INCOMPLETE,
            sequence_number=0,
            response={"id": "resp_2"},
        )

    inc_parts: list[str] = []
    async for f in renderer.render(events_inc(), "resp_2"):
        assert isinstance(f, str)
        inc_parts.append(f)
    merged_i = "".join(inc_parts)
    parsed_i = _parse_sse_frames(merged_i)
    assert parsed_i[-2]["type"] == "response.incomplete"
    assert parsed_i[-1] == {"__sse__": "[DONE]"}


@pytest.mark.asyncio
async def test_output_item_from_domain_round_trip() -> None:
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(store, transport="websocket")
    item = ResponsesOutputItem(
        id="m1",
        type="message",
        role="assistant",
        status="completed",
        content=None,
    )

    async def events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.OUTPUT_ITEM_DONE,
            sequence_number=0,
            item=item.model_dump(mode="python"),
        )
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_COMPLETED,
            sequence_number=1,
            response={"id": "rid"},
        )

    await _collect_wire(renderer.render(events(), "rid"))
    resolved = await store.resolve("rid")
    assert resolved is not None
    assert resolved.output_items[0].id == "m1"


@pytest.mark.asyncio
async def test_session_store_persists_anthropic_streamed_output_item_body() -> None:
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(
        store,
        transport="websocket",
        realtime_websocket_terminal=True,
    )

    item = {
        "id": "msg_ant_1",
        "type": "message",
        "role": "assistant",
        "status": "completed",
        "content": [{"type": "output_text", "text": "Hello"}],
    }

    async def events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.OUTPUT_ITEM_ADDED,
            sequence_number=0,
            output_index=0,
            content_index=0,
            item_id="msg_ant_1",
            item={"id": "msg_ant_1", "type": "message", "role": "assistant"},
        )
        yield _ev(
            etype=ResponsesSemanticEventType.TEXT_DELTA,
            sequence_number=1,
            output_index=0,
            content_index=0,
            item_id="msg_ant_1",
            delta="Hello",
        )
        yield _ev(
            etype=ResponsesSemanticEventType.OUTPUT_ITEM_DONE,
            sequence_number=2,
            output_index=0,
            content_index=0,
            item_id="msg_ant_1",
        )
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_COMPLETED,
            sequence_number=3,
            response={"id": "resp_ant_1", "output": [item]},
        )

    await _collect_wire(renderer.render(events(), "resp_ant_1"))

    resolved = await store.resolve("resp_ant_1")
    assert resolved is not None
    assert len(resolved.output_items) == 1
    assert resolved.output_items[0].id == "msg_ant_1"
    assert resolved.output_items[0].content is not None
    assert resolved.output_items[0].content[0].text == "Hello"


@pytest.mark.asyncio
async def test_session_store_persists_gemini_streamed_text_from_terminal_output() -> (
    None
):
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(
        store,
        transport="websocket",
        realtime_websocket_terminal=True,
    )

    terminal_item = {
        "id": "msg_gem_1",
        "type": "message",
        "role": "assistant",
        "status": "completed",
        "content": [{"type": "output_text", "text": "Hello"}],
    }

    async def events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_CREATED,
            sequence_number=0,
            response={"id": "resp_gem_1"},
        )
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_IN_PROGRESS,
            sequence_number=1,
        )
        yield _ev(
            etype=ResponsesSemanticEventType.TEXT_DELTA,
            sequence_number=2,
            output_index=0,
            content_index=0,
            item_id="msg_gem_1",
            delta="Hel",
        )
        yield _ev(
            etype=ResponsesSemanticEventType.TEXT_DELTA,
            sequence_number=3,
            output_index=0,
            content_index=0,
            item_id="msg_gem_1",
            delta="lo",
        )
        yield _ev(
            etype=ResponsesSemanticEventType.RESPONSE_COMPLETED,
            sequence_number=4,
            response={"id": "resp_gem_1", "output": [terminal_item]},
        )

    await _collect_wire(renderer.render(events(), "resp_gem_1"))

    resolved = await store.resolve("resp_gem_1")
    assert resolved is not None
    assert len(resolved.output_items) == 1
    assert resolved.output_items[0].id == "msg_gem_1"
    assert resolved.output_items[0].content is not None
    assert resolved.output_items[0].content[0].text == "Hello"


@pytest.mark.asyncio
async def test_sse_yields_done_when_no_terminal_event() -> None:
    """HTTP SDK decoders expect [DONE] even if upstream omitted a terminal frame."""

    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(store, transport="sse")

    async def no_events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        if False:
            yield _ev(
                etype=ResponsesSemanticEventType.RESPONSE_CREATED,
                sequence_number=0,
            )

    chunks = await _collect_wire(
        renderer.render(no_events(), "rid-empty", instructions=None)
    )
    assert chunks
    assert chunks[-1] == "data: [DONE]\n\n"
    assert await store.resolve("rid-empty") is None


@pytest.mark.asyncio
async def test_websocket_yields_incomplete_when_no_semantic_terminal_event() -> None:
    """WebSocket clients need a terminal frame; SSE uses [DONE] for the same case."""
    store = InMemoryResponsesSessionStore()
    renderer = ResponsesWireRenderer(
        store,
        transport="websocket",
        realtime_websocket_terminal=False,
    )

    async def no_events() -> AsyncGenerator[ResponsesSemanticEvent, None]:
        if False:
            yield _ev(
                etype=ResponsesSemanticEventType.RESPONSE_CREATED,
                sequence_number=0,
            )

    frames = await _collect_wire(
        renderer.render(no_events(), "rid-ws-no-terminal", instructions=None)
    )
    assert len(frames) >= 1
    last = frames[-1]
    assert isinstance(last, dict)
    assert last["type"] == "response.incomplete"
    assert isinstance(last.get("response"), dict)
    assert last["response"].get("id") == "rid-ws-no-terminal"
