from __future__ import annotations

import asyncio
import subprocess
from collections.abc import AsyncGenerator
from dataclasses import replace
from pathlib import Path
from typing import Any
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from src.connectors.acp_core.base_connector import BaseAcpConnector
from src.connectors.acp_core.types import (
    ACPError,
    ACPNotification,
    ACPProcessRuntime,
    HistoryState,
)
from src.connectors.acp_core.types import AcpStreamPiece as AcpStreamPiece
from src.connectors.acp_core.workspace_policy import ACP_MISSING_PROJECT_WORKSPACE_CODE
from src.connectors.contracts import (
    ConnectorChatCompletionsRequest,
    ConnectorRequestContext,
)
from src.core.common.exceptions import APITimeoutError, BackendError
from src.core.domain.chat import CanonicalChatRequest, ChatMessage
from src.core.domain.responses import ProcessedResponse, ResponseEnvelope


class DummyAcpConnector(BaseAcpConnector[ACPProcessRuntime]):
    backend_type = "dummy-acp"
    VENDOR_PREFIX = "dummy"

    async def _build_subprocess_command(self, runtime: ACPProcessRuntime) -> list[str]:
        return ["dummy", "acp"]

    def _create_runtime(
        self, project_dir: Path, model: str, client_session_id: str = "default"
    ) -> ACPProcessRuntime:
        return ACPProcessRuntime(
            project_dir=project_dir,
            model=model,
            client_session_id=client_session_id,
            process_lock=asyncio.Lock(),
            request_lock=asyncio.Lock(),
            cancellation_lock=asyncio.Lock(),
            cancellation_event=asyncio.Event(),
        )

    async def _perform_handshake(self, runtime: ACPProcessRuntime) -> None:
        runtime.session_id = "dummy-session"
        runtime.initialized = True

    async def _handle_server_request(
        self, runtime: ACPProcessRuntime, msg: ACPNotification
    ) -> None:
        pass

    def get_available_models(self) -> list[str]:
        return ["dummy/model"]

    async def initialize(self, **kwargs: Any) -> None:
        self._default_project_dir = Path("/tmp/dummy")
        self.is_functional = True


class StrictWorkspaceDummy(DummyAcpConnector):
    requires_explicit_workspace = True


def _make_request(
    stream: bool = False,
    with_history: bool = False,
    *,
    messages: list[ChatMessage] | None = None,
    session_id: str | None = None,
    extra_body: dict[str, Any] | None = None,
) -> ConnectorChatCompletionsRequest:
    resolved_messages = messages or (
        [
            ChatMessage(role="system", content="You are a helpful assistant."),
            ChatMessage(role="user", content="What is 2+2?"),
            ChatMessage(role="assistant", content="4"),
            ChatMessage(role="user", content="hello"),
        ]
        if with_history
        else [ChatMessage(role="user", content="hello")]
    )

    request = CanonicalChatRequest(
        model="dummy/model",
        stream=stream,
        messages=resolved_messages,
        extra_body=extra_body,
    )
    context: ConnectorRequestContext | None = None
    if session_id is not None:
        context = ConnectorRequestContext(
            request_id=None, session_id=session_id, client_host=None
        )
    return ConnectorChatCompletionsRequest(
        request=request,
        processed_messages=resolved_messages,
        effective_model="dummy/model",
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=context,
        options={},
    )


@pytest.fixture
def connector() -> DummyAcpConnector:
    return DummyAcpConnector(MagicMock(), MagicMock())


@pytest.mark.asyncio
async def test_acp_error_includes_structured_detail(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "dummy/model")
    runtime.session_id = "dummy-session"
    response = ACPNotification(
        id=7,
        error=ACPError(
            code=-32603,
            message="Internal error",
            data={"error": "Gemini quota exhausted for this account"},
        ),
    )

    with (
        patch.object(
            connector, "_read_jsonrpc_message", AsyncMock(return_value=response)
        ),
        pytest.raises(
            BackendError,
            match=(
                "ACP process error: Internal error: "
                "Gemini quota exhausted for this account"
            ),
        ) as exc_info,
    ):
        await connector._iter_acp_stream_pieces(runtime, 7, "dummy/model").__anext__()

    assert exc_info.value.details["code"] == -32603
    assert exc_info.value.details["data"] == {
        "error": "Gemini quota exhausted for this account"
    }


@pytest.mark.asyncio
async def test_acp_quota_error_code_is_forwarded(connector: DummyAcpConnector) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "dummy/model")
    runtime.session_id = "dummy-session"
    response = ACPNotification(
        id=7,
        error=ACPError(
            code=-32003,
            message="Model provider quota or rate limit exceeded",
            data={"error": "RESOURCE_EXHAUSTED (code 429): Individual quota reached"},
        ),
    )

    with (
        patch.object(
            connector, "_read_jsonrpc_message", AsyncMock(return_value=response)
        ),
        pytest.raises(
            BackendError,
            match=(
                "ACP process error: Model provider quota or rate limit exceeded: "
                "RESOURCE_EXHAUSTED"
            ),
        ) as exc_info,
    ):
        await connector._iter_acp_stream_pieces(runtime, 7, "dummy/model").__anext__()

    assert exc_info.value.details["code"] == -32003
    assert "RESOURCE_EXHAUSTED" in str(exc_info.value.details["data"]["error"])


@pytest.mark.asyncio
async def test_acp_error_after_partial_output_yields_notice_and_completes(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "dummy/model")
    runtime.session_id = "dummy-session"
    update = ACPNotification(
        method="session/update",
        params={
            "sessionId": "dummy-session",
            "update": {
                "sessionUpdate": "agent_message_chunk",
                "content": {"type": "text", "text": "partial output "},
            },
        },
    )
    error_response = ACPNotification(
        id=7,
        error=ACPError(
            code=-32603,
            message="Internal error",
            data={"error": "boom after partial"},
        ),
    )

    with patch.object(
        connector,
        "_read_jsonrpc_message",
        AsyncMock(side_effect=[update, error_response]),
    ):
        gen = connector._iter_acp_stream_pieces(runtime, 7, "dummy/model")
        first = await gen.__anext__()
        assert first.content == "partial output "
        second = await gen.__anext__()
        assert second.content is not None
        assert "ACP backend turn failed after partial output" in second.content
        assert "Internal error" in second.content
        with pytest.raises(StopAsyncIteration):
            await gen.__anext__()


@pytest.mark.asyncio
async def test_acp_error_after_partial_output_flushes_pending_tool_summary(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "dummy/model")
    runtime.session_id = "dummy-session"
    tool_update = ACPNotification(
        method="session/update",
        params={
            "sessionId": "dummy-session",
            "update": {
                "sessionUpdate": "tool_call",
                "toolCallId": "tool-1",
                "title": "read_file",
                "rawInput": {"path": "README.md"},
            },
        },
    )
    error_response = ACPNotification(
        id=7,
        error=ACPError(code=-32603, message="failed after tool output"),
    )

    with patch.object(
        connector,
        "_read_jsonrpc_message",
        AsyncMock(side_effect=[tool_update, error_response]),
    ):
        pieces = [
            piece
            async for piece in connector._iter_acp_stream_pieces(
                runtime, 7, "dummy/model"
            )
        ]

    content = "".join(piece.content or "" for piece in pieces)
    assert "read_file" in content
    assert "ACP backend turn failed after partial output" in content
    assert content.index("read_file") < content.index("ACP backend turn failed")


@pytest.mark.asyncio
async def test_windows_terminate_kills_tree_before_root_process(
    connector: DummyAcpConnector,
) -> None:
    process = MagicMock()
    process.pid = 1234
    process.poll.return_value = None
    process.wait.return_value = 0

    def fake_taskkill(*args: Any, **kwargs: Any) -> subprocess.CompletedProcess[bytes]:
        assert process.terminate.call_count == 0
        return subprocess.CompletedProcess(args=args, returncode=0)

    with (
        patch("src.connectors.acp_core.base_connector.os.name", "nt"),
        patch(
            "src.connectors.acp_core.base_connector.subprocess.run",
            side_effect=fake_taskkill,
        ),
    ):
        await connector._terminate_process(process)

    process.terminate.assert_not_called()


@pytest.mark.asyncio
async def test_stderr_drain_keeps_only_bounded_tail(
    connector: DummyAcpConnector,
) -> None:
    class FakeStderr:
        def __init__(self) -> None:
            self.chunks = [b"x" * 20_000, b""]

        def read1(self, _size: int) -> bytes:
            return self.chunks.pop(0)

    process = MagicMock()
    process.stderr = FakeStderr()
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")

    connector._drain_stderr_thread(process, runtime)

    assert len(runtime.stderr_tail) == 16 * 1024
    assert bytes(runtime.stderr_tail) == b"x" * (16 * 1024)


@pytest.mark.asyncio
async def test_spawn_clears_stderr_tail_before_reader_starts(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    runtime.stderr_tail.extend(b"stale diagnostics")

    class FakeStderr:
        def __init__(self) -> None:
            self.chunks = [b"startup diagnostics", b""]

        def read1(self, _size: int) -> bytes:
            return self.chunks.pop(0)

    class FakeProcess:
        pid = 1234
        stdin = MagicMock()
        stdout = MagicMock()

        def __init__(self) -> None:
            self.stderr = FakeStderr()

        def poll(self) -> None:
            return None

    process = FakeProcess()

    class FakeThread:
        def __init__(self, target: Any, args: tuple[Any, ...], **_: Any) -> None:
            self._target = target
            self._args = args

        def start(self) -> None:
            self._target(*self._args)

        def is_alive(self) -> bool:
            return False

        def join(self, timeout: float | None = None) -> None:
            del timeout

    with (
        patch(
            "src.connectors.acp_core.base_connector.subprocess.Popen",
            return_value=process,
        ),
        patch("src.connectors.acp_core.base_connector.threading.Thread", FakeThread),
        patch(
            "src.connectors.acp_core.base_connector.capture_acp_subprocess_identity",
            return_value=None,
        ),
    ):
        await connector._spawn_process(runtime)

    assert bytes(runtime.stderr_tail) == b"startup diagnostics"


@pytest.mark.asyncio
async def test_stale_stream_cancel_callback_does_not_touch_new_generation(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    assert runtime.request_lock is not None
    await runtime.request_lock.acquire()
    runtime.active_request_generation = 2

    cancelled = await connector._cancel_active_request(
        runtime, prompt_request_id=1, expected_generation=1
    )

    assert cancelled is False
    assert runtime.active_request_generation == 2
    assert runtime.request_lock.locked()


@pytest.mark.asyncio
async def test_cancel_active_request_cleans_up_already_exited_process(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    mock_process = MagicMock()
    mock_process.poll.return_value = 0
    mock_process.stdin = MagicMock()
    mock_process.stdout = MagicMock()
    mock_process.stderr = MagicMock()
    mock_process.pid = 12345
    runtime.process = mock_process
    runtime.initialized = True

    class JoinProbe:
        def is_alive(self) -> bool:
            return False

        def join(self, *, timeout: float | None = None) -> None:
            assert timeout == 0

    runtime.stderr_drain_thread = JoinProbe()
    assert runtime.request_lock is not None
    await runtime.request_lock.acquire()

    with patch.object(connector, "_kill_runtime", AsyncMock()) as kill_mock:
        cancelled = await connector._cancel_active_request(runtime, prompt_request_id=1)

    assert cancelled is True
    kill_mock.assert_not_called()
    assert runtime.process is None
    assert runtime.initialized is False
    assert runtime.stderr_drain_thread is None
    assert runtime.stderr_drain_stop_event.is_set()
    assert runtime.request_lock.locked() is False
    mock_process.stdin.close.assert_called_once()
    mock_process.stdout.close.assert_called_once()
    mock_process.stderr.close.assert_called_once()


@pytest.mark.asyncio
async def test_stderr_drain_thread_join_is_non_blocking(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")

    class JoinProbe:
        def join(self, *, timeout: float | None = None) -> None:
            assert timeout == 0

    runtime.stderr_drain_thread = JoinProbe()

    connector._join_stderr_drain_thread(runtime)

    assert runtime.stderr_drain_thread is None


@pytest.mark.asyncio
async def test_blocked_stdin_write_kills_runtime_on_timeout(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    runtime.process = MagicMock()
    runtime.process.stdin = MagicMock()
    connector._process_timeout = 0.01

    async def stuck_to_thread(*_: Any, **__: Any) -> None:
        await asyncio.sleep(3600)

    with (
        patch(
            "src.connectors.acp_core.base_connector.asyncio.to_thread",
            side_effect=stuck_to_thread,
        ),
        patch.object(connector, "_kill_runtime", AsyncMock()) as kill_mock,
        pytest.raises(APITimeoutError, match="Timeout writing to ACP process"),
    ):
        await connector._write_json_line(runtime, {"jsonrpc": "2.0"})

    kill_mock.assert_awaited_once_with(runtime)


def test_resolve_stream_keepalive_interval_default(
    connector: DummyAcpConnector,
) -> None:
    assert connector._resolve_stream_keepalive_interval() == 12.0


def test_session_update_thought_maps_to_reasoning_piece(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    msg = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "agent_thought_chunk",
                "content": {"type": "text", "text": "planning step"},
            },
        },
    )
    piece = connector._session_update_to_stream_piece(msg, runtime)
    assert piece == AcpStreamPiece(content="Thinking:\nplanning step")


def test_session_update_thought_prefers_text_delta_over_full_text(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    msg = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "agent_thought_chunk",
                "content": {
                    "type": "text",
                    "text": "cumulative snapshot",
                    "textDelta": "delta only",
                },
            },
        },
    )
    piece = connector._session_update_to_stream_piece(msg, runtime)
    assert piece == AcpStreamPiece(content="Thinking:\ndelta only")


def test_session_update_message_after_thought_closes_block(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    first = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "agent_thought_chunk",
                "content": {"type": "text", "text": "follow-up analysis"},
            },
        },
    )
    second = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "agent_message_chunk",
                "content": {"type": "text", "text": "visible answer"},
            },
        },
    )
    piece1 = connector._session_update_to_stream_piece(first, runtime)
    piece2 = connector._session_update_to_stream_piece(second, runtime)
    assert piece1 == AcpStreamPiece(content="Thinking:\nfollow-up analysis")
    assert piece2 == AcpStreamPiece(content="\n\nvisible answer")


def test_session_update_consecutive_thought_chunks_append_without_reopening(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    first = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "agent_thought_chunk",
                "content": {"type": "text", "text": "I now have a clear understanding"},
            },
        },
    )
    second = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "agent_thought_chunk",
                "content": {"type": "text", "text": " and can proceed"},
            },
        },
    )
    piece1 = connector._session_update_to_stream_piece(first, runtime)
    piece2 = connector._session_update_to_stream_piece(second, runtime)
    assert piece1 == AcpStreamPiece(
        content="Thinking:\nI now have a clear understanding"
    )
    assert piece2 == AcpStreamPiece(content=" and can proceed")


def test_session_update_flat_acp_tool_call_spec_shape(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    msg = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call",
                "toolCallId": "call_001",
                "title": "Reading configuration file",
                "kind": "read",
                "status": "pending",
                "rawInput": {"path": "/etc/app.json"},
            },
        },
    )
    piece = connector._session_update_to_stream_piece(msg, runtime)
    assert piece is not None
    assert piece.content is not None
    assert "Status: started" in piece.content
    assert "Tool: Reading configuration file" in piece.content
    flush = connector._flush_incomplete_acp_tool_streams(runtime)
    assert len(flush) == 1
    assert flush[0].content is not None
    assert flush[0].content.startswith("---\n```text\nTool: Reading configuration file")
    assert "Input size:" in flush[0].content
    assert "Output size: 0 bytes" in flush[0].content
    assert "Status:" not in flush[0].content


def test_session_update_tool_call_emits_summary_when_completed(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    msg = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call",
                "toolCall": {
                    "name": "read_file",
                    "arguments": '{"path": "/x"}',
                    "status": "completed",
                },
            },
        },
    )
    pieces = connector._session_update_to_stream_pieces(msg, runtime)
    joined = "".join(p.content or "" for p in pieces)
    assert "Status:" not in joined
    assert joined.startswith("---\n```text\nTool: read_file")
    assert "Tool: read_file" in joined
    assert 'Arguments: {"path":"/x"}' in joined
    assert "Input size:" in joined
    assert "Status: started" not in joined


def test_session_update_tool_call_update_emits_status_and_size_summary(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    call = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call",
                "toolCall": {
                    "toolCallId": "tc-1",
                    "name": "list_dir",
                    "arguments": '{"path": "."}',
                    "status": "in_progress",
                },
            },
        },
    )
    upd = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call_update",
                "toolCallUpdate": {
                    "toolCallId": "tc-1",
                    "name": "list_dir",
                    "result": ["a.txt", "b.txt"],
                    "status": "completed",
                },
            },
        },
    )
    first = connector._session_update_to_stream_pieces(call, runtime)
    second = connector._session_update_to_stream_pieces(upd, runtime)
    started = "".join(p.content or "" for p in first)
    assert "Status: started" in started
    assert "Tool: list_dir" in started
    assert 'Arguments: {"path":"."}' in started
    assert "Ended:" not in started
    joined = "".join(p.content or "" for p in second)
    assert "Status:" not in joined
    assert joined.startswith("---\n```text\nTool: list_dir")
    assert "Tool: list_dir" in joined
    assert "Output size:" in joined
    assert "a.txt" not in joined


def test_session_update_tool_call_list_emits_each_tool_separately(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    msg = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call",
                "toolCall": [
                    {"name": "read_file", "toolCallId": "x1", "status": "completed"},
                    {"name": "list_dir", "toolCallId": "x2", "status": "completed"},
                ],
            },
        },
    )
    pieces = connector._session_update_to_stream_pieces(msg, runtime)
    joined = "".join(p.content or "" for p in pieces)
    assert "read_file" in joined
    assert "list_dir" in joined
    assert len(runtime.acp_tool_stream_accum) == 2


def test_session_update_tool_summary_defers_until_output_observed(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    early = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call",
                "toolCall": {
                    "toolCallId": "tc-1",
                    "name": "list_dir",
                    "status": "completed",
                },
            },
        },
    )
    pieces1 = connector._session_update_to_stream_pieces(early, runtime)
    joined1 = "".join(p.content or "" for p in pieces1)
    assert "Status:" not in joined1
    assert "Tool: list_dir" not in joined1
    late = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call_update",
                "toolCallUpdate": {
                    "toolCallId": "tc-1",
                    "name": "list_dir",
                    "result": ["a.txt"],
                },
            },
        },
    )
    pieces2 = connector._session_update_to_stream_pieces(late, runtime)
    joined2 = "".join(p.content or "" for p in pieces2)
    assert joined2.startswith("---\n```text\nTool: list_dir")
    assert "Tool: list_dir" in joined2
    assert "Output size:" in joined2
    assert "a.txt" not in joined2


def test_session_update_deferred_tool_summary_flushes_at_stream_end(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    early = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call",
                "toolCall": {
                    "toolCallId": "tc-1",
                    "name": "list_dir",
                    "status": "completed",
                },
            },
        },
    )
    pieces = connector._session_update_to_stream_pieces(early, runtime)
    assert pieces == []
    flush = connector._flush_incomplete_acp_tool_streams(runtime)
    assert len(flush) == 1
    assert flush[0].content is not None
    assert flush[0].content.startswith("---\n```text\nTool: list_dir")
    assert "Input size: 0 bytes" in flush[0].content
    assert "Output size: 0 bytes" in flush[0].content


def test_session_update_multiple_tools_without_correlation_ids_emit_separately(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    first = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call",
                "toolCall": {"name": "read_file", "status": "completed"},
            },
        },
    )
    second = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call",
                "toolCall": {"name": "list_dir", "status": "completed"},
            },
        },
    )
    p1 = connector._session_update_to_stream_pieces(first, runtime)
    p2 = connector._session_update_to_stream_pieces(second, runtime)
    assert len(p1) >= 1 and any("read_file" in (x.content or "") for x in p1)
    assert len(p2) >= 1 and any("list_dir" in (x.content or "") for x in p2)
    assert len(runtime.acp_tool_stream_accum) == 2


def test_session_update_tool_call_update_seen_empty_tail_returns_none(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    call = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call",
                "toolCall": {
                    "toolCallId": "tc-1",
                    "name": "list_dir",
                    "arguments": '{"path": "."}',
                    "status": "pending",
                },
            },
        },
    )
    redundant = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call_update",
                "toolCallUpdate": {"toolCallId": "tc-1", "name": "list_dir"},
            },
        },
    )
    first = connector._session_update_to_stream_piece(call, runtime)
    second = connector._session_update_to_stream_piece(redundant, runtime)
    assert first is not None
    assert first.content is not None
    assert "Status: started" in first.content
    assert second is None


def test_resolve_stream_keepalive_interval_from_config(
    connector: DummyAcpConnector,
) -> None:
    fh = MagicMock()
    fh.keepalive_interval = 7.5
    connector.config = MagicMock()
    connector.config.failure_handling = fh
    assert connector._resolve_stream_keepalive_interval() == 7.5


def test_build_runtime_key_includes_client_session_id(
    connector: DummyAcpConnector,
) -> None:
    key = connector._build_runtime_key(Path("/tmp/ws"), "my-model", "client-42")
    assert key == (str(Path("/tmp/ws")), "my-model", "client-42")


def test_resolve_client_session_id_defaults(connector: DummyAcpConnector) -> None:
    req = _make_request()
    assert connector._resolve_client_session_id(req) == "default"


def test_resolve_client_session_id_from_context(connector: DummyAcpConnector) -> None:
    req = _make_request(session_id="  abc  ")
    assert connector._resolve_client_session_id(req) == "abc"


def test_resolve_client_session_id_from_options(connector: DummyAcpConnector) -> None:
    req = _make_request()
    req.options = {"session_id": "opt-sess-123"}
    assert connector._resolve_client_session_id(req) == "opt-sess-123"


def test_resolve_client_session_id_from_extra_body(
    connector: DummyAcpConnector,
) -> None:
    req = _make_request(extra_body={"session_id": "eb-sess-456"})
    assert connector._resolve_client_session_id(req) == "eb-sess-456"


def test_resolve_client_session_id_normalizes_b2bua_attempt_suffix(
    connector: DummyAcpConnector,
) -> None:
    req1 = _make_request(
        session_id="llm-b2bua-b-350a2b91-7ee4-4b99-8419-979b44c0dab5-1"
    )
    req2 = _make_request(
        session_id="llm-b2bua-b-350a2b91-7ee4-4b99-8419-979b44c0dab5-2"
    )
    assert (
        connector._resolve_client_session_id(req1)
        == "llm-b2bua-b-350a2b91-7ee4-4b99-8419-979b44c0dab5"
    )
    assert (
        connector._resolve_client_session_id(req2)
        == "llm-b2bua-b-350a2b91-7ee4-4b99-8419-979b44c0dab5"
    )
    assert connector._resolve_client_session_id(
        req1
    ) == connector._resolve_client_session_id(req2)


@pytest.mark.asyncio
async def test_acquire_runtime_isolates_per_client_session(
    connector: DummyAcpConnector,
) -> None:
    connector._default_project_dir = Path("/tmp/dummy")
    ra = await connector._acquire_runtime(_make_request(session_id="s-a"))
    rb = await connector._acquire_runtime(_make_request(session_id="s-b"))
    assert ra is not rb
    assert ra.client_session_id == "s-a"
    assert rb.client_session_id == "s-b"
    assert len(connector._runtimes) == 2


@pytest.mark.asyncio
async def test_stream_lock_released_before_terminal_chunk(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    assert runtime.request_lock is not None
    await runtime.request_lock.acquire()
    runtime.active_request_generation = 1

    async def source(*_: Any) -> AsyncGenerator[ProcessedResponse, None]:
        yield ProcessedResponse(content='data: {"ok":true}\n\n')
        yield ProcessedResponse(content="data: [DONE]\n\n")

    with patch.object(connector, "_stream_response", source):
        stream = connector._stream_response_with_lock(runtime, "m", 1, 1)
        first = await anext(stream)
        assert first.content != "data: [DONE]\n\n"
        assert runtime.request_lock.locked()

        done = await anext(stream)
        assert done.content == "data: [DONE]\n\n"
        assert not runtime.request_lock.locked()
        assert runtime.active_request_generation is None
        await stream.aclose()


@pytest.mark.asyncio
async def test_stream_lock_released_before_provider_terminal_json_chunk(
    connector: DummyAcpConnector,
) -> None:
    """Codex finish_reason chunks must release the lock before being yielded."""

    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    assert runtime.request_lock is not None
    await runtime.request_lock.acquire()
    runtime.active_request_generation = 1

    async def source(*_: Any) -> AsyncGenerator[ProcessedResponse, None]:
        yield ProcessedResponse(
            content=('data: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\n')
        )

    with patch.object(connector, "_stream_response", source):
        stream = connector._stream_response_with_lock(runtime, "m", 1, 1)
        terminal = await anext(stream)

    assert isinstance(terminal.content, str)
    assert terminal.content.startswith("data: {")
    assert not runtime.request_lock.locked()
    assert runtime.active_request_generation is None
    await stream.aclose()


@pytest.mark.asyncio
async def test_abandoned_stream_cancels_before_releasing_lock(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    assert runtime.request_lock is not None
    await runtime.request_lock.acquire()

    async def source(*_: Any) -> AsyncGenerator[ProcessedResponse, None]:
        yield ProcessedResponse(content='data: {"partial":true}\n\n')
        await asyncio.sleep(3600)

    with patch.object(connector, "_stream_response", source):
        stream = connector._stream_response_with_lock(runtime, "m", 1)
        await anext(stream)
        await stream.aclose()

    assert not runtime.request_lock.locked()
    assert runtime.cancellation_event is not None
    assert not runtime.cancellation_event.is_set()


@pytest.mark.asyncio
async def test_prepare_prompt_first_turn_serializes_full_transcript(
    connector: DummyAcpConnector,
) -> None:
    connector._default_project_dir = Path("/tmp/dummy")
    runtime = connector._create_runtime(Path("/tmp/dummy"), "model")
    runtime.process = MagicMock()
    runtime.process.stdin = MagicMock()
    runtime.process.stdout = MagicMock()

    assert runtime.history_state is None

    with (
        patch.object(connector, "_spawn_process", AsyncMock()),
        patch.object(
            connector, "_send_jsonrpc_message", AsyncMock(return_value=1)
        ) as send_mock,
    ):
        await connector._prepare_turn_request_locked(
            runtime, _make_request(with_history=True)
        )

    assert runtime.history_state is not None
    assert runtime.history_state.message_count == 4
    sent_params = send_mock.call_args[0][2]
    assert "System Note:" in sent_params["prompt"][0]["text"]


@pytest.mark.asyncio
async def test_prepare_prompt_incremental_tail_after_non_acp_turns(
    connector: DummyAcpConnector,
) -> None:
    connector._default_project_dir = Path("/tmp/dummy")
    runtime = connector._create_runtime(Path("/tmp/dummy"), "model")
    runtime.process = MagicMock()
    runtime.process.stdin = MagicMock()
    runtime.process.stdout = MagicMock()

    base = [
        ChatMessage(role="user", content="first"),
        ChatMessage(role="assistant", content="ack"),
    ]
    extended = [
        *base,
        ChatMessage(role="user", content="from other model"),
        ChatMessage(role="assistant", content="external reply"),
        ChatMessage(role="user", content="back to acp"),
    ]

    with (
        patch.object(connector, "_spawn_process", AsyncMock()),
        patch.object(
            connector, "_send_jsonrpc_message", AsyncMock(return_value=1)
        ) as send_mock,
    ):
        await connector._prepare_turn_request_locked(
            runtime, _make_request(messages=base)
        )
        assert runtime.history_state is not None
        assert runtime.history_state.message_count == 2

        await connector._prepare_turn_request_locked(
            runtime, _make_request(messages=extended)
        )

    assert runtime.history_state.message_count == 5
    last_text = send_mock.call_args[0][2]["prompt"][0]["text"]
    assert "Additional conversation occurred" in last_text
    assert "from other model" in last_text
    assert "back to acp" in last_text


@pytest.mark.asyncio
async def test_prepare_prompt_diverged_prefix_resets_and_reserializes_full(
    connector: DummyAcpConnector,
) -> None:
    connector._default_project_dir = Path("/tmp/dummy")
    runtime = connector._create_runtime(Path("/tmp/dummy"), "model")
    runtime.process = MagicMock()
    runtime.process.stdin = MagicMock()
    runtime.process.stdout = MagicMock()

    messages_v1 = [
        ChatMessage(role="user", content="q1"),
        ChatMessage(role="assistant", content="a1"),
        ChatMessage(role="user", content="q2"),
    ]
    messages_v2 = [
        ChatMessage(role="user", content="q1-edited"),
        ChatMessage(role="assistant", content="a1"),
        ChatMessage(role="user", content="q2"),
    ]

    with (
        patch.object(connector, "_spawn_process", AsyncMock()),
        patch.object(
            connector, "_send_jsonrpc_message", AsyncMock(return_value=1)
        ) as send_mock,
        patch.object(connector, "_kill_runtime", AsyncMock()) as kill_mock,
    ):
        await connector._prepare_turn_request_locked(
            runtime, _make_request(messages=messages_v1)
        )
        await connector._prepare_turn_request_locked(
            runtime, _make_request(messages=messages_v2)
        )

    kill_mock.assert_awaited_once()
    last_text = send_mock.call_args[0][2]["prompt"][0]["text"]
    assert "q1-edited" in last_text
    assert "System Note: The user is continuing a previous session" in last_text


@pytest.mark.asyncio
async def test_prepare_prompt_same_length_retry_uses_last_user_only(
    connector: DummyAcpConnector,
) -> None:
    connector._default_project_dir = Path("/tmp/dummy")
    runtime = connector._create_runtime(Path("/tmp/dummy"), "model")
    runtime.process = MagicMock()
    runtime.process.stdin = MagicMock()
    runtime.process.stdout = MagicMock()
    msgs = [ChatMessage(role="user", content="ping")]

    with (
        patch.object(connector, "_spawn_process", AsyncMock()),
        patch.object(
            connector, "_send_jsonrpc_message", AsyncMock(return_value=1)
        ) as send_mock,
    ):
        await connector._prepare_turn_request_locked(
            runtime, _make_request(messages=msgs)
        )
        hc = runtime.history_state.message_count if runtime.history_state else 0
        assert hc == 1
        await connector._prepare_turn_request_locked(
            runtime, _make_request(messages=msgs)
        )

    last_text = send_mock.call_args[0][2]["prompt"][0]["text"]
    assert last_text == "ping"


def test_hash_messages_prefix_detects_edit(connector: DummyAcpConnector) -> None:
    a = [
        ChatMessage(role="user", content="x"),
        ChatMessage(role="assistant", content="y"),
    ]
    b = [
        ChatMessage(role="user", content="x-changed"),
        ChatMessage(role="assistant", content="y"),
    ]
    h1 = connector._hash_messages_prefix(a, 1)
    h2 = connector._hash_messages_prefix(b, 1)
    assert h1 != h2


def test_hash_messages_prefix_stable_ignores_metadata_only_changes(
    connector: DummyAcpConnector,
) -> None:
    a = [ChatMessage(role="user", content="hi", metadata={"a": 1})]
    b = [ChatMessage(role="user", content="hi", metadata={"b": 2})]
    assert connector._hash_messages_prefix(a, 1) == connector._hash_messages_prefix(
        b, 1
    )


@pytest.mark.asyncio
async def test_parallel_acquire_after_idle_reap_uses_same_pool_runtime(
    connector: DummyAcpConnector,
) -> None:
    connector._default_project_dir = Path("/tmp/dummy")
    connector._idle_timeout = 5.0
    rid = "sess-par"
    key = connector._build_runtime_key(Path("/tmp/dummy"), "model", rid)
    stale = connector._create_runtime(Path("/tmp/dummy"), "model", rid)
    stale.process = MagicMock()
    stale.process.poll.return_value = None
    stale.last_activity = 1.0
    async with connector._runtime_pool_lock:
        connector._runtimes[key] = stale

    with (
        patch(
            "src.connectors.acp_core.base_connector.time.monotonic", return_value=100.0
        ),
        patch.object(connector, "_terminate_process", AsyncMock()),
    ):
        r1, r2 = await asyncio.gather(
            connector._acquire_runtime(_make_request(session_id=rid)),
            connector._acquire_runtime(_make_request(session_id=rid)),
        )

    assert r1 is r2
    assert r1 is not stale
    assert connector._runtimes[key] is r1


@pytest.mark.asyncio
async def test_kill_all_runtimes_next_acquire_creates_new_runtime_object(
    connector: DummyAcpConnector,
) -> None:
    connector._default_project_dir = Path("/tmp/dummy")
    r_before = await connector._acquire_runtime(_make_request(session_id="recycle"))
    await connector._kill_all_runtimes()
    assert len(connector._runtimes) == 0
    r_after = await connector._acquire_runtime(_make_request(session_id="recycle"))
    assert r_after is not r_before
    assert r_after.history_state is None


@pytest.mark.asyncio
async def test_non_streaming_chat_completions_include_visible_thinking_blocks(
    connector: DummyAcpConnector,
) -> None:
    connector.is_functional = True
    connector._default_project_dir = Path("/tmp/dummy")
    runtime = connector._create_runtime(Path("/tmp/dummy"), "dummy/model")

    async def _mock_iter(
        _: ACPProcessRuntime, __: int, ___: str
    ) -> AsyncGenerator[AcpStreamPiece, None]:
        yield AcpStreamPiece(content="Thinking:\nplan step\n")
        yield AcpStreamPiece(content="\n\n")
        yield AcpStreamPiece(content="Answer")

    with (
        patch.object(connector, "_acquire_runtime", AsyncMock(return_value=runtime)),
        patch.object(
            connector,
            "_prepare_turn_request_locked",
            AsyncMock(return_value=(5, "dummy/model")),
        ),
        patch.object(connector, "_iter_acp_stream_pieces", side_effect=_mock_iter),
    ):
        response = await connector.chat_completions(_make_request())

    assert isinstance(response, ResponseEnvelope)
    assert isinstance(response.content, dict)
    message = response.content["choices"][0]["message"]
    assert message["content"] == "Thinking:\nplan step\n\n\nAnswer"
    assert "reasoning_content" not in message


@pytest.mark.asyncio
async def test_failed_non_streaming_turn_retires_runtime_before_unlock(
    connector: DummyAcpConnector,
) -> None:
    connector.is_functional = True
    connector._default_project_dir = Path("/tmp/dummy")
    runtime = connector._create_runtime(Path("/tmp/dummy"), "dummy/model")
    runtime.process = MagicMock()
    runtime.process.poll.return_value = None
    runtime.process.pid = 1234
    runtime.history_state = HistoryState(message_count=1, prefix_hash="stale")
    runtime.last_prompt_params = {"sessionId": "stale-session"}

    async def _failing_iter(
        _: ACPProcessRuntime, __: int, ___: str
    ) -> AsyncGenerator[AcpStreamPiece, None]:
        raise BackendError(message="simulated ACP turn failure")
        if False:
            yield AcpStreamPiece(content="unreachable")

    with (
        patch.object(connector, "_acquire_runtime", AsyncMock(return_value=runtime)),
        patch.object(
            connector,
            "_prepare_turn_request_locked",
            AsyncMock(return_value=(5, "dummy/model")),
        ),
        patch.object(connector, "_iter_acp_stream_pieces", side_effect=_failing_iter),
        patch.object(connector, "_terminate_process", AsyncMock()) as terminate_mock,
        pytest.raises(BackendError, match="simulated ACP turn failure"),
    ):
        await connector.chat_completions(_make_request())

    terminate_mock.assert_awaited_once()
    assert runtime.process is None
    assert runtime.history_state is None
    assert runtime.last_prompt_params is None
    assert runtime.request_lock is not None
    assert not runtime.request_lock.locked()


@pytest.mark.asyncio
async def test_requires_explicit_workspace_raises_without_override() -> None:
    connector = StrictWorkspaceDummy(MagicMock(), MagicMock())
    await connector.initialize()
    with pytest.raises(BackendError) as exc_info:
        connector._resolve_project_dir_for_request(_make_request())
    assert exc_info.value.details.get("code") == ACP_MISSING_PROJECT_WORKSPACE_CODE


@pytest.mark.asyncio
async def test_requires_explicit_workspace_accepts_options_project_dir(
    tmp_path: Path,
) -> None:
    workspace = tmp_path / "ws"
    workspace.mkdir()
    connector = StrictWorkspaceDummy(MagicMock(), MagicMock())
    await connector.initialize()
    base = _make_request()
    req = replace(base, options={"project_dir": str(workspace)})
    resolved = connector._resolve_project_dir_for_request(req)
    assert resolved == workspace.resolve()


def test_in_progress_heartbeat_emits_after_interval(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    connector._acp_tool_heartbeat_seconds = 0.01
    call = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call",
                "toolCall": {
                    "toolCallId": "tc-hb",
                    "name": "run_command",
                    "status": "in_progress",
                    "rawInput": {"CommandLine": "npm test"},
                },
            },
        },
    )
    start_pieces = connector._session_update_to_stream_pieces(call, runtime)
    assert any("Status: started" in (p.content or "") for p in start_pieces)
    acc = next(iter(runtime.acp_tool_stream_accum.values()))
    acc.last_heartbeat_perf = acc.started_perf - 1.0
    acc.started_perf = acc.started_perf - 45.0
    heartbeats = connector._acp_in_progress_heartbeat_pieces(runtime)
    assert len(heartbeats) == 1
    assert heartbeats[0].content is not None
    assert heartbeats[0].content.startswith("Tool still running: run_command")
    assert connector._acp_in_progress_heartbeat_pieces(runtime) == []


@pytest.mark.asyncio
async def test_iter_acp_stream_emits_heartbeat_while_tool_in_progress(
    connector: DummyAcpConnector,
) -> None:
    runtime = connector._create_runtime(Path("/tmp/ws"), "m")
    runtime.session_id = "dummy-session"
    connector._acp_tool_heartbeat_seconds = 0.05
    connector._process_timeout = 2.0
    start = ACPNotification(
        method="session/update",
        params={
            "sessionId": "s1",
            "update": {
                "sessionUpdate": "tool_call",
                "toolCallId": "tc-wait",
                "title": "run_command",
                "status": "in_progress",
            },
        },
    )
    done = ACPNotification(id=7, result={"stopReason": "end_turn"})

    async def delayed_messages() -> Any:
        yield start
        await asyncio.sleep(0.16)
        yield done

    gen = delayed_messages()

    async def next_message(_runtime: Any) -> ACPNotification:
        return await anext(gen)

    with patch.object(connector, "_read_jsonrpc_message", side_effect=next_message):
        pieces = [
            piece
            async for piece in connector._iter_acp_stream_pieces(
                runtime, 7, "dummy/model"
            )
        ]

    contents = [p.content or "" for p in pieces]
    assert any("Status: started" in text for text in contents)
    assert any("Tool still running: run_command" in text for text in contents)
