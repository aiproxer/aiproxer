"""Regression: OpenCode Responses tool loop via chatgpt-plan native path.

Root causes covered:
1. output_item.added must NOT emit empty-arg domain tool_calls (OpenCode
   disconnects when the semantic legacy path synthesizes completed items).
2. Prefer call_id (call_...) over item_id (fc_...) for client-facing ids.
3. Legacy semantic OUTPUT_ITEM_DONE must expose call_id; RESPONSE_COMPLETED
   output must include function_call items.
4. CanonicalChatRequest for native Responses must carry tools so
   RequestSideEffects registers allowed tools (not []).
"""

from __future__ import annotations

from collections.abc import AsyncIterator, Generator
from typing import Any
from unittest.mock import MagicMock

import pytest

from src.core.domain.chat import ChatMessage, ChatRequest
from src.core.domain.request_context import RequestContext
from src.core.domain.responses_domain import ResponsesDomainRequest
from src.core.domain.responses_event_normalizer import (
    ResponsesEventNormalizer,
    ResponsesStreamSource,
)
from src.core.domain.responses_semantic_events import ResponsesSemanticEventType
from src.core.domain.responses_wire_renderer import _wire_payload_for_semantic
from src.core.domain.translation_utils.tool_call_state import (
    codex_function_name_cache,
)
from src.core.domain.translators.responses.streaming import (
    reset_active_responses_stream_context,
    responses_to_domain_stream_chunk,
)
from src.core.services.request_side_effects import RequestSideEffects
from src.core.services.streaming.stream_context_registry import (
    get_global_streaming_context_registry,
)


@pytest.fixture(autouse=True)
def _reset_tool_state() -> Generator[None, None, None]:
    reset_active_responses_stream_context()
    codex_function_name_cache.clear()
    yield
    reset_active_responses_stream_context()
    codex_function_name_cache.clear()


def test_output_item_added_does_not_emit_empty_arg_tool_calls() -> None:
    """OpenCode disconnects if an empty-arg function_call is marked completed."""
    responses_to_domain_stream_chunk(
        {"type": "response.created", "response": {"id": "resp_empty_added_1"}}
    )
    added = responses_to_domain_stream_chunk(
        {
            "type": "response.output_item.added",
            "output_index": 0,
            "item": {
                "id": "fc_empty_1",
                "call_id": "call_empty_1",
                "type": "function_call",
                "name": "read",
                "arguments": "",
            },
        }
    )
    assert added["choices"][0]["delta"] == {}
    assert "tool_calls" not in added["choices"][0]["delta"]


def test_domain_tool_call_prefers_call_id_over_item_id() -> None:
    responses_to_domain_stream_chunk(
        {"type": "response.created", "response": {"id": "resp_call_pref_1"}}
    )
    responses_to_domain_stream_chunk(
        {
            "type": "response.output_item.added",
            "output_index": 0,
            "item": {
                "id": "fc_pref_1",
                "call_id": "call_pref_1",
                "type": "function_call",
                "name": "glob",
                "arguments": "",
            },
        }
    )
    # Accumulate under item_id (SIWC args events often key by fc_...).
    responses_to_domain_stream_chunk(
        {
            "type": "response.function_call_arguments.delta",
            "item_id": "fc_pref_1",
            "output_index": 0,
            "delta": '{"pattern":"*"}',
        }
    )
    done = responses_to_domain_stream_chunk(
        {
            "type": "response.output_item.done",
            "output_index": 0,
            "item": {
                "id": "fc_pref_1",
                "call_id": "call_pref_1",
                "type": "function_call",
                "name": "glob",
                "arguments": "{}",
            },
        }
    )
    tool = done["choices"][0]["delta"]["tool_calls"][0]
    assert tool["id"] == "call_pref_1"
    assert "pattern" in tool["function"]["arguments"]


@pytest.mark.asyncio
async def test_legacy_semantic_tool_events_expose_call_id_and_completed_output() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES,
        response_id="resp_legacy_tools_1",
    )

    async def gen() -> AsyncIterator[dict[str, Any]]:
        yield {
            "id": "resp_legacy_tools_1",
            "object": "chat.completion.chunk",
            "choices": [
                {
                    "index": 0,
                    "delta": {
                        "tool_calls": [
                            {
                                "id": "call_legacy_1",
                                "type": "function",
                                "function": {
                                    "name": "bash",
                                    "arguments": '{"command":"dir"}',
                                },
                            }
                        ]
                    },
                    "finish_reason": None,
                }
            ],
        }
        yield {
            "id": "resp_legacy_tools_1",
            "object": "chat.completion.chunk",
            "choices": [
                {"index": 0, "delta": {}, "finish_reason": "stop"},
            ],
        }

    events = []
    async for event in n.normalize(gen()):
        events.append(event)

    done_items = [
        e
        for e in events
        if e.type == ResponsesSemanticEventType.OUTPUT_ITEM_DONE and e.item
    ]
    assert done_items
    item = done_items[0].item
    assert item is not None
    assert item.get("call_id") == "call_legacy_1"
    assert item.get("name") == "bash"
    assert "command" in str(item.get("arguments"))

    completed = [
        e for e in events if e.type == ResponsesSemanticEventType.RESPONSE_COMPLETED
    ]
    assert completed
    output = (completed[0].response or {}).get("output") or []
    assert any(
        isinstance(el, dict)
        and el.get("type") == "function_call"
        and el.get("call_id") == "call_legacy_1"
        for el in output
    )

    # Wire payload must keep call_id on the done item.
    wire = _wire_payload_for_semantic(done_items[0], realtime_ws_terminal=False)
    assert wire["item"]["call_id"] == "call_legacy_1"


@pytest.mark.asyncio
async def test_legacy_skips_empty_arg_tool_chunks() -> None:
    n = ResponsesEventNormalizer(
        source=ResponsesStreamSource.OPENAI_RESPONSES,
        response_id="resp_skip_empty_1",
    )

    async def gen() -> AsyncIterator[dict[str, Any]]:
        yield {
            "id": "resp_skip_empty_1",
            "choices": [
                {
                    "index": 0,
                    "delta": {
                        "tool_calls": [
                            {
                                "id": "call_empty",
                                "type": "function",
                                "function": {"name": "read", "arguments": ""},
                            }
                        ]
                    },
                    "finish_reason": None,
                }
            ],
        }
        yield {
            "id": "resp_skip_empty_1",
            "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
        }

    events = []
    async for event in n.normalize(gen()):
        events.append(event)

    assert not any(
        e.type == ResponsesSemanticEventType.OUTPUT_ITEM_DONE
        and isinstance(e.item, dict)
        and e.item.get("type") == "function_call"
        for e in events
    )


@pytest.mark.asyncio
async def test_request_side_effects_registers_tools_from_canonical_and_native() -> None:
    registry = get_global_streaming_context_registry()
    session_id = "sess-opencode-tools-reg-1"
    buffer = registry.get_tool_call_buffer(session_id)
    buffer.allowed_tools = None

    side = RequestSideEffects()
    ctx = MagicMock(spec=RequestContext)

    # Canonical tools path (after controller fix).
    req = ChatRequest(
        model="openai-chatgpt-plan:gpt-6.1-sol",
        messages=[ChatMessage(role="user", content="list tmp")],
        tools=[
            {
                "type": "function",
                "name": "read",
                "description": "Read a file",
                "parameters": {"type": "object", "properties": {}},
            },
            {
                "type": "function",
                "name": "bash",
                "description": "Run a shell command",
                "parameters": {"type": "object", "properties": {}},
            },
        ],
    )
    await side.apply(ctx, session_id, req)
    assert buffer.allowed_tools is not None
    assert "read" in buffer.allowed_tools
    assert "bash" in buffer.allowed_tools

    # Native-payload fallback when canonical.tools is empty.
    buffer.allowed_tools = None
    req2 = ChatRequest(
        model="openai-chatgpt-plan:gpt-6.1-sol",
        messages=[ChatMessage(role="user", content=".")],
        tools=None,
        extra_body={
            "responses_native_projected_payload": {
                "model": "gpt-6.1-sol",
                "tools": [
                    {"type": "function", "name": "glob", "parameters": {"type": "object"}},
                ],
            }
        },
    )
    await side.apply(ctx, session_id, req2)
    assert buffer.allowed_tools == ["glob"]


def test_responses_domain_tools_project_onto_canonical_shape() -> None:
    """Sanity: ResponsesDomainRequest carries tools for controller copy."""
    domain = ResponsesDomainRequest.model_validate(
        {
            "model": "openai-chatgpt-plan:gpt-6.1-sol",
            "input": [{"type": "message", "role": "user", "content": "hi"}],
            "stream": True,
            "tools": [
                {"type": "function", "name": "read", "parameters": {"type": "object"}},
            ],
            "tool_choice": "auto",
            "parallel_tool_calls": False,
        }
    )
    assert domain.tools is not None
    assert domain.tools[0]["name"] == "read"
