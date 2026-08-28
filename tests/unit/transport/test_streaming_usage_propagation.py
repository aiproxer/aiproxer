import json

import pytest
from src.core.domain.responses import StreamingResponseEnvelope
from src.core.interfaces.response_processor_interface import ProcessedResponse
from src.core.transport.fastapi.response_adapters import (
    to_fastapi_streaming_response,
)


async def _collect_streaming_body(response) -> str:
    """Consume a Starlette StreamingResponse and return the full body as text."""
    chunks: list[bytes] = []
    async for chunk in response.body_iterator:
        chunks.append(chunk)
    return b"".join(chunks).decode("utf-8", errors="ignore")


@pytest.mark.asyncio
async def test_usage_chunk_is_emitted_to_client_stream() -> None:
    """Ensure usage-only streaming chunks are not dropped before reaching the client."""

    async def usage_stream():
        # Usage-only chunk that previously looked "empty" to the pipeline
        yield ProcessedResponse(
            content={
                "id": "chatcmpl-usage-1",
                "usage": {
                    "prompt_tokens": 10,
                    "completion_tokens": 5,
                    "total_tokens": 15,
                },
            },
            metadata={"model": "test-model"},
        )
        # Terminal chunk
        yield ProcessedResponse(content=None, metadata={"finish_reason": "stop"})

    envelope = StreamingResponseEnvelope(content=usage_stream())

    response = to_fastapi_streaming_response(envelope)
    body = await _collect_streaming_body(response)

    # Assert that the usage payload and fields are present in the emitted SSE stream
    assert '"usage"' in body
    assert '"prompt_tokens": 10' in body
    assert '"completion_tokens": 5' in body
    assert '"total_tokens": 15' in body
    # Sanity check that the stream still terminates
    assert "[DONE]" in body


@pytest.mark.asyncio
async def test_streaming_usage_recalculated_from_accumulated_content() -> None:
    """Ensure usage is recalculated for streaming when content is transformed."""

    async def stream():
        yield ProcessedResponse(
            content={
                "choices": [{"delta": {"content": "Short reply"}}],
                "model": "gpt-4o",
            },
            metadata={"stream_id": "stream-1"},
        )
        yield ProcessedResponse(
            content={
                "choices": [{"delta": {}, "finish_reason": "stop"}],
                "usage": {
                    "prompt_tokens": 120,
                    "completion_tokens": 500,
                    "total_tokens": 620,
                },
            },
            metadata={
                "stream_id": "stream-1",
                "accumulated_content": "Short reply",
                "model": "gpt-4o",
            },
        )

    envelope = StreamingResponseEnvelope(
        content=stream(),
        metadata={"allow_usage_recalculation": True, "outbound_tokens": 120},
    )

    response = to_fastapi_streaming_response(envelope)
    body = await _collect_streaming_body(response)

    data_lines = [
        line[len("data: ") :]
        for line in body.splitlines()
        if line.startswith("data: ")
        and line.strip() not in {"data: [DONE]", 'data: ["DONE"]'}
    ]
    payloads = [
        json.loads(line)
        for line in data_lines
        if line.strip() not in {"[DONE]", '["DONE"]'}
    ]
    final_payload = payloads[-1]

    assert "usage" in final_payload
    usage = final_payload["usage"]
    assert usage["prompt_tokens"] == 120  # preserved from backend usage
    assert usage["completion_tokens"] < 500  # recalculated from accumulated content
    assert usage["total_tokens"] == usage["prompt_tokens"] + usage["completion_tokens"]


@pytest.mark.asyncio
async def test_pre_serialized_stop_chunk_with_usage_is_not_dropped() -> None:
    """Ensure a pre-serialized StopChunkWithUsage payload (data + DONE) is preserved."""

    stop_chunk = {
        "id": "chatcmpl-stop-test",
        "object": "chat.completion.chunk",
        "created": 1700000000,
        "model": "gemini-3-pro-preview",
        "choices": [
            {
                "index": 0,
                "delta": {"content": "final text"},
                "finish_reason": "stop",
            }
        ],
        "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
    }

    async def stream():
        payload = f"data: {json.dumps(stop_chunk)}\n\ndata: [DONE]\n\n".encode()
        yield ProcessedResponse(content=payload)

    envelope = StreamingResponseEnvelope(content=stream())
    response = to_fastapi_streaming_response(envelope)

    body = await _collect_streaming_body(response)

    assert '"chatcmpl-stop-test"' in body
    assert '"finish_reason": "stop"' in body
    assert '"final text"' in body
    assert '"usage"' in body
    assert '"total_tokens": 15' in body
    assert body.count("[DONE]") >= 1


@pytest.mark.asyncio
async def test_streaming_usage_respects_outbound_token_hint_for_tool_calls() -> None:
    """Ensure prompt tokens are populated from outbound_tokens when backend omits usage."""

    async def stream():
        # Simulate a tool-call chunk with no backend usage
        yield ProcessedResponse(
            content={
                "id": "chatcmpl-test",
                "object": "chat.completion.chunk",
                "created": 123,
                "model": "gpt-4o",
                "choices": [
                    {
                        "index": 0,
                        "delta": {
                            "role": "assistant",
                            "content": "<tool_call/>",
                            "tool_calls": [],
                        },
                        "finish_reason": "stop",
                    }
                ],
            },
            metadata={"stream_id": "stream-tool"},
        )

    envelope = StreamingResponseEnvelope(
        content=stream(),
        metadata={"outbound_tokens": 4321},
    )

    response = to_fastapi_streaming_response(envelope)
    body = await _collect_streaming_body(response)

    data_lines = [
        line[len("data: ") :]
        for line in body.splitlines()
        if line.startswith("data: ")
        and line.strip() not in {"data: [DONE]", 'data: ["DONE"]'}
    ]
    payloads = [
        json.loads(line)
        for line in data_lines
        if line.strip() not in {"[DONE]", '["DONE"]'}
    ]
    final_payload = payloads[-1]

    assert "usage" in final_payload
    usage = final_payload["usage"]
    assert usage["prompt_tokens"] == 4321
    assert usage["total_tokens"] >= usage["prompt_tokens"]


@pytest.mark.asyncio
async def test_outbound_tokens_overrides_connector_estimate_for_recalc_backends() -> (
    None
):
    """When allow_usage_recalculation is set (gemini-oauth), outbound_tokens
    must replace the connector's lower prompt_tokens estimate."""

    async def stream():
        yield ProcessedResponse(
            content={
                "id": "chatcmpl-gemini-recalc",
                "object": "chat.completion.chunk",
                "created": 123,
                "model": "gemini-3-flash-preview",
                "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                "usage": {
                    "prompt_tokens": 34631,
                    "completion_tokens": 200,
                    "total_tokens": 34831,
                },
            },
            metadata={"stream_id": "stream-gemini"},
        )

    envelope = StreamingResponseEnvelope(
        content=stream(),
        metadata={
            "outbound_tokens": 42861,
            "allow_usage_recalculation": True,
        },
    )

    response = to_fastapi_streaming_response(envelope)
    body = await _collect_streaming_body(response)

    data_lines = [
        line[len("data: ") :]
        for line in body.splitlines()
        if line.startswith("data: ")
        and line.strip() not in {"data: [DONE]", 'data: ["DONE"]'}
    ]
    payloads = [
        json.loads(line)
        for line in data_lines
        if line.strip() not in {"[DONE]", '["DONE"]'}
    ]
    final_payload = payloads[-1]

    assert "usage" in final_payload
    usage = final_payload["usage"]
    assert usage["prompt_tokens"] == 42861
    assert usage["total_tokens"] == usage["prompt_tokens"] + usage["completion_tokens"]


@pytest.mark.asyncio
async def test_streaming_backend_usage_not_overridden_by_prompt_hint() -> None:
    """When backend usage exists, outbound hint must not replace provider values."""

    async def stream():
        yield ProcessedResponse(
            content={
                "id": "chatcmpl-provider-usage",
                "object": "chat.completion.chunk",
                "created": 123,
                "model": "gpt-4o",
                "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                "usage": {
                    "prompt_tokens": 50,
                    "completion_tokens": 10,
                    "total_tokens": 60,
                },
            },
            metadata={"stream_id": "stream-provider"},
        )

    envelope = StreamingResponseEnvelope(
        content=stream(),
        metadata={"outbound_tokens": 4321},
    )

    response = to_fastapi_streaming_response(envelope)
    body = await _collect_streaming_body(response)

    data_lines = [
        line[len("data: ") :]
        for line in body.splitlines()
        if line.startswith("data: ")
        and line.strip() not in {"data: [DONE]", 'data: ["DONE"]'}
    ]
    payloads = [
        json.loads(line)
        for line in data_lines
        if line.strip() not in {"[DONE]", '["DONE"]'}
    ]
    final_payload = payloads[-1]

    assert "usage" in final_payload
    usage = final_payload["usage"]
    assert usage["prompt_tokens"] == 50
    assert usage["completion_tokens"] == 10
    assert usage["total_tokens"] == 60
