from __future__ import annotations

import json
from collections.abc import AsyncGenerator
from typing import Any

import pytest
from src.core.ports.openai_normalizer import OpenAIStreamNormalizer
from src.core.ports.streaming_orchestrator import StreamingPipeline
from src.core.services.streaming.tool_call_repair_processor import (
    ToolCallRepairProcessor,
)
from src.core.services.tool_call_repair_service import ToolCallRepairService


def _to_sse_bytes(payload: dict[str, Any]) -> bytes:
    return f"data: {json.dumps(payload)}\n\n".encode()


def _extract_json_sse_events(chunks: list[bytes]) -> list[dict[str, Any]]:
    events: list[dict[str, Any]] = []
    combined = b"".join(chunks).decode("utf-8", errors="replace")
    for raw_event in combined.split("\n\n"):
        event = raw_event.strip()
        if not event or not event.startswith("data: "):
            continue
        payload = event[6:]
        if payload == "[DONE]":
            continue
        events.append(json.loads(payload))
    return events


def _accumulate_tool_call_arguments(
    events: list[dict[str, Any]],
) -> dict[int | str, str]:
    result: dict[int | str, str] = {}
    for event in events:
        choices = event.get("choices")
        if not isinstance(choices, list) or not choices:
            continue

        first_choice = choices[0]
        if not isinstance(first_choice, dict):
            continue

        delta = first_choice.get("delta")
        if not isinstance(delta, dict):
            continue

        tool_calls = delta.get("tool_calls")
        if not isinstance(tool_calls, list):
            continue

        for tool_call in tool_calls:
            if not isinstance(tool_call, dict):
                continue

            function = tool_call.get("function")
            if not isinstance(function, dict):
                continue

            arguments = function.get("arguments")
            if not isinstance(arguments, str):
                continue

            key: int | str
            index = tool_call.get("index")
            if isinstance(index, int):
                key = index
            else:
                tool_call_id = tool_call.get("id")
                key = tool_call_id if isinstance(tool_call_id, str) else "unknown"

            result[key] = f"{result.get(key, '')}{arguments}"

    return result


@pytest.mark.asyncio
async def test_openai_pipeline_repairs_fragmented_tool_call_arguments_on_terminal_chunk() -> (
    None
):
    """End-to-end regression for fragmented tool-call argument JSON repair.

    Simulates a StepFun/OpenRouter-style stream where the model emits an
    unterminated `function.arguments` fragment and then ends with
    `finish_reason=tool_calls`.

    The pipeline should emit a final suffix fragment so client-side argument
    concatenation becomes valid JSON.
    """

    first_payload = {
        "id": "chatcmpl-stepfun-1",
        "object": "chat.completion.chunk",
        "created": 1730000000,
        "model": "stepfun/stepfun-3.5-flash",
        "choices": [
            {
                "index": 0,
                "delta": {
                    "tool_calls": [
                        {
                            "index": 0,
                            "id": "call_stepfun_1",
                            "type": "function",
                            "function": {
                                "name": "read_file",
                                "arguments": '{"path": "README.md"',
                            },
                        }
                    ]
                },
            }
        ],
    }

    terminal_payload = {
        "id": "chatcmpl-stepfun-1",
        "object": "chat.completion.chunk",
        "created": 1730000000,
        "model": "stepfun/stepfun-3.5-flash",
        "choices": [
            {
                "index": 0,
                "delta": {},
                "finish_reason": "tool_calls",
            }
        ],
    }

    async def raw_stream() -> AsyncGenerator[object, None]:
        yield _to_sse_bytes(first_payload)
        yield _to_sse_bytes(terminal_payload)
        yield b"data: [DONE]\n\n"

    pipeline = StreamingPipeline(
        normalizer=OpenAIStreamNormalizer(),
        processors=[
            ToolCallRepairProcessor(
                tool_call_repair_service=ToolCallRepairService(),
                max_buffer_bytes=4096,
            )
        ],
    )

    emitted: list[bytes] = []
    async for chunk in pipeline.process_stream(
        raw_stream(),
        provider="openai",
        stream_id="stepfun-repair-e2e",
        output_format="sse",
    ):
        emitted.append(chunk)

    combined = b"".join(emitted).decode("utf-8", errors="replace")
    assert "data: [DONE]" in combined

    json_events = _extract_json_sse_events(emitted)
    accumulated_arguments = _accumulate_tool_call_arguments(json_events)

    assert 0 in accumulated_arguments
    parsed_arguments = json.loads(accumulated_arguments[0])
    assert parsed_arguments == {"path": "README.md"}
