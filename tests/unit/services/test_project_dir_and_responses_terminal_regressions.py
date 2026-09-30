"""Regression tests for Responses stream terminal detection and project-dir races."""

from __future__ import annotations

import asyncio
from unittest.mock import AsyncMock

import pytest
from src.connectors.openai import is_responses_stream_terminal_chunk
from src.core.domain.translators.responses.streaming import (
    responses_to_domain_stream_chunk,
)
from src.core.domain.chat import ChatMessage, ChatRequest
from src.core.domain.responses import ResponseEnvelope
from src.core.domain.session import Session, SessionState
from src.core.services.project_directory_resolution_service import (
    ProjectDirectoryResolutionService,
    _LLM_RESOLUTION_TIMEOUT_SECONDS,
    _MIN_LLM_PROMPT_CHARS,
)
from tests.unit.services.test_project_directory_resolution_service import (
    create_app_config,
)


def test_function_call_output_item_done_has_no_finish_reason() -> None:
    responses_to_domain_stream_chunk(
        {
            "type": "response.created",
            "response": {"id": "resp_multi_tool", "model": "gpt-6.1-sol"},
        }
    )
    done = responses_to_domain_stream_chunk(
        {
            "type": "response.output_item.done",
            "output_index": 0,
            "item": {
                "id": "call_a",
                "call_id": "call_a",
                "type": "function_call",
                "name": "read",
                "arguments": '{"filePath":"C:\\\\tmp"}',
            },
        }
    )
    assert done["choices"][0]["delta"]["tool_calls"][0]["function"]["name"] == "read"
    assert done["choices"][0].get("finish_reason") is None


def test_terminal_predicate_ignores_tool_calls_and_null_error() -> None:
    assert is_responses_stream_terminal_chunk({"choices": [{"finish_reason": "tool_calls", "delta": {}}]}) is False
    assert is_responses_stream_terminal_chunk({"error": None}) is False
    assert is_responses_stream_terminal_chunk({"error": {}}) is False
    assert is_responses_stream_terminal_chunk({"type": "response.created"}) is False
    assert is_responses_stream_terminal_chunk({"choices": [{"finish_reason": "stop", "delta": {}}]}) is True
    assert is_responses_stream_terminal_chunk({"type": "response.completed"}) is True
    assert is_responses_stream_terminal_chunk({"error": {"message": "boom"}}) is True


@pytest.mark.asyncio
async def test_short_prompt_skips_llm_project_dir_call() -> None:
    mock_backend = AsyncMock()
    mock_session = AsyncMock()
    session = Session(session_id="short-prompt", state=SessionState())
    config = create_app_config("hybrid")
    service = ProjectDirectoryResolutionService(config, mock_backend, mock_session)
    request = ChatRequest(
        model="openai-chatgpt-plan:gpt-6.1-sol",
        messages=[ChatMessage(role="user", content="x")],
    )
    assert len("x") < _MIN_LLM_PROMPT_CHARS
    await service.maybe_resolve_project_directory(session, request)
    mock_backend.call_completion.assert_not_called()
    assert session.state.project_dir_resolution_attempted is True


@pytest.mark.asyncio
async def test_concurrent_project_dir_llm_is_single_flight() -> None:
    mock_backend = AsyncMock()
    started = asyncio.Event()
    release = asyncio.Event()

    async def slow_completion(*_args, **_kwargs):
        started.set()
        await release.wait()
        return ResponseEnvelope(
            content=(
                "<directory-resolution-response>"
                "<project-absolute-directory>/home/user/proj"
                "</project-absolute-directory>"
                "</directory-resolution-response>"
            )
        )

    mock_backend.call_completion.side_effect = slow_completion
    mock_session = AsyncMock()
    session = Session(session_id="dual-opencode", state=SessionState())
    config = create_app_config("hybrid")
    service = ProjectDirectoryResolutionService(config, mock_backend, mock_session)
    prompt = "please open my project on the desktop folder somehow"
    request = ChatRequest(
        model="test-model",
        messages=[ChatMessage(role="user", content=prompt)],
    )

    async def run_one() -> None:
        await service.maybe_resolve_project_directory(session, request)

    t1 = asyncio.create_task(run_one())
    await asyncio.wait_for(started.wait(), timeout=1.0)
    # Second concurrent call must return immediately without stacking another LLM call.
    await asyncio.wait_for(run_one(), timeout=0.2)
    assert mock_backend.call_completion.await_count == 1
    release.set()
    await t1
    assert mock_backend.call_completion.await_count == 1
    assert session.state.project_dir == "/home/user/proj"


@pytest.mark.asyncio
async def test_project_dir_llm_timeout_fails_open() -> None:
    mock_backend = AsyncMock()

    async def hang(*_args, **_kwargs):
        await asyncio.sleep(_LLM_RESOLUTION_TIMEOUT_SECONDS + 5)
        return ResponseEnvelope(content="should-not-matter")

    mock_backend.call_completion.side_effect = hang
    mock_session = AsyncMock()
    session = Session(session_id="timeout-session", state=SessionState())
    config = create_app_config("hybrid")
    service = ProjectDirectoryResolutionService(config, mock_backend, mock_session)
    request = ChatRequest(
        model="test-model",
        messages=[
            ChatMessage(role="user", content="please find my project on the desktop")
        ],
    )
    # Use a tiny timeout for the test without waiting 20s.
    import src.core.services.project_directory_resolution_service as mod

    old = mod._LLM_RESOLUTION_TIMEOUT_SECONDS
    mod._LLM_RESOLUTION_TIMEOUT_SECONDS = 0.05
    try:
        await service.maybe_resolve_project_directory(session, request)
    finally:
        mod._LLM_RESOLUTION_TIMEOUT_SECONDS = old
    assert session.state.project_dir is None
    assert session.state.project_dir_resolution_attempted is True
