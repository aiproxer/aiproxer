"""Regression: Responses function_call name must survive multi-event streams.

OpenAI puts ``name`` on ``response.output_item.added`` while later
``function_call_arguments.*`` events often only carry ``item_id``. ``item.id``
and ``item.call_id`` frequently differ (``fc_…`` vs ``call_…``); names must be
cached under both so domain mapping and outbound SSE keep the tool name.
"""

from __future__ import annotations

from collections.abc import AsyncIterator, Generator

import pytest
from src.core.domain.responses_event_normalizer import (
    ResponsesEventNormalizer,
    ResponsesStreamSource,
)
from src.core.domain.responses_semantic_events import ResponsesSemanticEventType
from src.core.domain.responses_wire_renderer import _wire_payload_for_semantic
from src.core.domain.translation_utils.tool_call_state import (
    codex_function_name_cache,
    get_cached_function_name,
)
from src.core.domain.translators.responses.streaming import (
    reset_active_responses_stream_context,
    responses_to_domain_stream_chunk,
)


@pytest.fixture(autouse=True)
def _reset_tool_name_state() -> Generator[None, None, None]:
    reset_active_responses_stream_context()
    codex_function_name_cache.clear()
    yield
    reset_active_responses_stream_context()
    codex_function_name_cache.clear()


def test_function_call_name_cached_under_item_id_and_call_id() -> None:
    """Name from output_item.added must resolve via item_id on args.done."""
    responses_to_domain_stream_chunk(
        {"type": "response.created", "response": {"id": "resp_name_carry_1"}}
    )
    responses_to_domain_stream_chunk(
        {
            "type": "response.output_item.added",
            "output_index": 0,
            "item": {
                "id": "fc_item_abc",
                "call_id": "call_xyz_abc",
                "type": "function_call",
                "name": "read",
                "arguments": "",
            },
        }
    )

    assert get_cached_function_name("call_xyz_abc") == "read"
    assert get_cached_function_name("fc_item_abc") == "read"

    # Mirror upstream: args.done has item_id only (no name).
    responses_to_domain_stream_chunk(
        {
            "type": "response.function_call_arguments.done",
            "item_id": "fc_item_abc",
            "output_index": 0,
            "arguments": '{"filePath":"C:\\\\Users\\\\tmp"}',
        }
    )
    assert get_cached_function_name("fc_item_abc") == "read"

    done = responses_to_domain_stream_chunk(
        {
            "type": "response.output_item.done",
            "output_index": 0,
            "item": {
                "id": "fc_item_abc",
                "call_id": "call_xyz_abc",
                "type": "function_call",
                # Upstream sometimes omits name on done; carry from cache.
                "arguments": '{"filePath":"C:\\\\Users\\\\tmp"}',
            },
        }
    )
    tool_calls = done["choices"][0]["delta"]["tool_calls"]
    assert tool_calls[0]["function"]["name"] == "read"
    assert "filePath" in tool_calls[0]["function"]["arguments"]


@pytest.mark.asyncio
async def test_normalizer_carries_name_onto_args_done_wire() -> None:
    """Semantic normalizer + wire renderer must emit name on args.done."""

    async def _chunks() -> AsyncIterator[dict]:
        yield {
            "type": "response.created",
            "response": {"id": "resp_wire_name_1"},
        }
        yield {
            "type": "response.output_item.added",
            "output_index": 0,
            "item": {
                "id": "fc_wire_1",
                "call_id": "call_wire_1",
                "type": "function_call",
                "name": "glob",
                "arguments": "",
            },
        }
        yield {
            "type": "response.function_call_arguments.delta",
            "item_id": "fc_wire_1",
            "output_index": 0,
            "delta": '{"pattern":"*"}',
        }
        yield {
            "type": "response.function_call_arguments.done",
            "item_id": "fc_wire_1",
            "output_index": 0,
            "arguments": '{"pattern":"*"}',
        }
        yield {
            "type": "response.output_item.done",
            "output_index": 0,
            "item": {
                "id": "fc_wire_1",
                "call_id": "call_wire_1",
                "type": "function_call",
                "name": "glob",
                "arguments": '{"pattern":"*"}',
            },
        }
        yield {
            "type": "response.completed",
            "response": {"id": "resp_wire_name_1", "output": []},
        }

    normalizer = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES,
        response_id="resp_wire_name_1",
    )
    events = [event async for event in normalizer.normalize(_chunks())]
    args_done = [
        e for e in events if e.type == ResponsesSemanticEventType.TOOL_CALL_ARGS_DONE
    ]
    assert args_done, "expected function_call_arguments.done semantic event"
    assert args_done[0].name == "glob"

    wire = _wire_payload_for_semantic(args_done[0], realtime_ws_terminal=False)
    assert wire["type"] == "response.function_call_arguments.done"
    assert wire["name"] == "glob"
    assert wire["arguments"] == '{"pattern":"*"}'
    assert wire["item_id"] == "fc_wire_1"
