"""Tests for BackendRequestManager deduplication integration."""

from __future__ import annotations

import asyncio
import contextlib
from typing import Any, cast
from unittest.mock import AsyncMock, MagicMock

import pytest
from src.core.common.exceptions import BackendError, DuplicateRequestError
from src.core.domain.chat import ChatMessage, ChatRequest
from src.core.domain.request_context import RequestContext
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.interfaces.backend_processor_interface import IBackendProcessor
from src.core.interfaces.configuration_interface import IConfig
from src.core.interfaces.quality_verifier_service_interface import (
    IQualityVerifierServiceFactory,
)
from src.core.interfaces.request_deduplication_interface import (
    IRequestDeduplicationService,
)
from src.core.interfaces.response_processor_interface import (
    IResponseProcessor,
    ProcessedResponse,
)
from src.core.services.backend_request_manager_service import BackendRequestManager

from tests.helpers.backend_request_manager_fixtures import (
    create_backend_request_manager,
)


class TestBackendRequestManagerDeduplication:
    @pytest.fixture
    def mock_backend_processor(self) -> MagicMock:
        return MagicMock(spec=IBackendProcessor)

    @pytest.fixture
    def mock_response_processor(self) -> MagicMock:
        return MagicMock(spec=IResponseProcessor)

    @pytest.fixture
    def mock_quality_verifier_service_factory(self) -> MagicMock:
        return MagicMock(spec=IQualityVerifierServiceFactory)

    @pytest.fixture
    def mock_dedup_service(self) -> AsyncMock:
        return AsyncMock(spec=IRequestDeduplicationService)

    @pytest.fixture
    def mock_config(self) -> MagicMock:
        return MagicMock(spec=IConfig)

    @pytest.fixture
    def backend_request_manager(
        self,
        mock_backend_processor: MagicMock,
        mock_response_processor: MagicMock,
        mock_quality_verifier_service_factory: MagicMock,
        mock_dedup_service: AsyncMock,
        mock_config: MagicMock,
    ) -> BackendRequestManager:
        # Use helper to create manager with all required components
        manager = create_backend_request_manager(
            backend_processor=mock_backend_processor,
            response_processor=mock_response_processor,
        )
        # Set the dedup service
        manager._dedup_service = mock_dedup_service
        return manager

    @pytest.mark.asyncio
    async def test_process_backend_request_calls_dedup_service(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
    ) -> None:
        """Verify that the dedup service is called before processing."""
        # Setup
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )
        session_id = "test-session"
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )

        # Mock dedup service to return "not a duplicate"
        mock_dedup_service.check_and_register.return_value = (False, "hash123")

        # Mock backend processing (canonical path requires a real envelope type)
        mock_backend_processor.process_backend_request = AsyncMock(
            return_value=ResponseEnvelope(content={"ok": True}, status_code=200)
        )

        # Execute
        await backend_request_manager.process_backend_request(
            request, session_id, context
        )

        # Verify
        mock_dedup_service.check_and_register.assert_awaited_once_with(
            request, session_id
        )
        mock_backend_processor.process_backend_request.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_process_backend_request_raises_on_duplicate(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
    ) -> None:
        """Verify that duplicate requests raise DuplicateRequestError and do not reach backend."""
        # Setup
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )
        session_id = "test-session"
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )

        # Mock dedup service to return "IS a duplicate"
        mock_dedup_service.check_and_register.return_value = (True, "hash123")

        # Mock backend processing
        mock_backend_processor.process_backend_request = AsyncMock()

        # Execute & verify
        with pytest.raises(DuplicateRequestError):
            await backend_request_manager.process_backend_request(
                request, session_id, context
            )

        # Backend should not be called on duplicate
        mock_backend_processor.process_backend_request.assert_not_called()

    @pytest.mark.asyncio
    async def test_duplicate_replays_cached_upstream_error(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
    ) -> None:
        """A cached HTTP error is not reported as a successful duplicate."""
        request = ChatRequest(
            model="deepseek-v4-flash",
            messages=[ChatMessage(role="user", content="test")],
            stream=True,
        )
        mock_dedup_service.check_and_register.return_value = (True, "hash403")
        mock_dedup_service.get_request_outcome.return_value = ("success", 403)

        with pytest.raises(BackendError) as exc_info:
            await backend_request_manager.process_backend_request(
                request,
                "test-session",
                RequestContext(
                    headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
                ),
            )

        assert exc_info.value.status_code == 403
        mock_backend_processor.process_backend_request.assert_not_called()

    @pytest.mark.asyncio
    async def test_streaming_duplicate_returns_done_stream(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
    ) -> None:
        """Streaming duplicates should not surface as HTTP 429 errors."""

        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            stream=True,
        )
        session_id = "test-session"
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )

        mock_dedup_service.check_and_register.return_value = (
            True,
            "hash123",
            10.5,
        )

        result = await backend_request_manager.process_backend_request(
            request, session_id, context
        )
        assert isinstance(result, StreamingResponseEnvelope)
        assert result.status_code == 200
        assert result.headers is not None
        assert result.headers.get("x-llmproxy-duplicate-request") == "true"
        assert result.headers.get("Retry-After") == "11"

        mock_backend_processor.process_backend_request.assert_not_called()
        assert result.content is not None
        out: list[bytes] = []
        async for chunk in result.content:
            assert isinstance(chunk.content, bytes)
            out.append(chunk.content)
        rendered = b"".join(out)
        assert b"data: [DONE]" in rendered

    @pytest.mark.asyncio
    async def test_streaming_http_error_marks_dedup_complete_before_iteration(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        """Streaming HTTP errors must immediately allow legitimate client retries."""

        monkeypatch.setenv("LLM_PROXY_DISABLE_EMPTY_STREAM_RECOVERY", "1")
        request = ChatRequest(
            model="openai-codex:gpt-5.5",
            messages=[ChatMessage(role="user", content="test")],
            stream=True,
        )
        session_id = "test-session"
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )

        async def _error_stream():
            yield ProcessedResponse(content=b"upstream connect error")

        mock_dedup_service.check_and_register.return_value = (False, "hash123")
        mock_backend_processor.process_backend_request = AsyncMock(
            return_value=StreamingResponseEnvelope(
                content=_error_stream(),
                status_code=503,
                media_type="text/plain",
            )
        )

        result = await backend_request_manager.process_backend_request(
            request, session_id, context
        )

        assert isinstance(result, StreamingResponseEnvelope)
        assert result.status_code == 503
        mock_dedup_service.mark_request_complete.assert_awaited_once_with(
            "hash123",
            session_id,
            status_code=503,
        )

    @pytest.mark.asyncio
    async def test_streaming_dedup_enabled_for_streaming_requests(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
    ) -> None:
        """Verify streaming dedup is enabled for streaming requests.

        This was changed from bypass to enabled to prevent zombie request
        patterns where clients continue retrying after being stopped.

        Status-aware tracking ensures legitimate retries after 429/503
        are still allowed.
        """
        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            stream=True,
        )
        session_id = "test-session"
        context = RequestContext(
            headers={"user-agent": "generic-client/1.0"},
            cookies={},
            state=MagicMock(),
            app_state=MagicMock(),
            agent="generic-client/1.0",
        )

        # Mock dedup service to return duplicate
        mock_dedup_service.check_and_register.return_value = (True, "hash123")

        # Execute & verify - streaming duplicate returns a benign done-only stream
        result = await backend_request_manager.process_backend_request(
            request, session_id, context
        )
        assert isinstance(result, StreamingResponseEnvelope)
        assert result.status_code == 200
        assert result.headers is not None
        assert result.headers.get("x-llmproxy-duplicate-request") == "true"

        # Dedup service should have been called
        mock_dedup_service.check_and_register.assert_awaited_once_with(
            request, session_id
        )
        # Backend should not be called on duplicate
        mock_backend_processor.process_backend_request.assert_not_called()

    @pytest.mark.asyncio
    async def test_streaming_dedup_bypass_via_header(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        """Verify dedup can still be bypassed via x-llmproxy-no-dedup header."""
        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            stream=True,
        )
        session_id = "test-session"
        monkeypatch.setenv("LLM_PROXY_DISABLE_EMPTY_STREAM_RECOVERY", "1")
        context = RequestContext(
            headers={
                "user-agent": "generic-client/1.0",
                "x-llmproxy-no-dedup": "true",
            },
            cookies={},
            state=MagicMock(),
            app_state=MagicMock(),
            agent="generic-client/1.0",
        )

        async def _minimal_stream():
            # OpenAI-shaped SSE so empty-stream gate counts output as meaningful.
            yield ProcessedResponse(
                content='data: {"choices":[{"index":0,"delta":{"content":"."}}]}\n\n',
            )

        mock_backend_processor.process_backend_request = AsyncMock(
            return_value=StreamingResponseEnvelope(content=_minimal_stream())
        )
        mock_dedup_service.check_and_register.return_value = (True, "hash123")

        await backend_request_manager.process_backend_request(
            request, session_id, context
        )

        # Dedup should be bypassed via header
        mock_dedup_service.check_and_register.assert_not_called()
        mock_backend_processor.process_backend_request.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_streaming_dedup_bypassed_for_internlm_streaming_requests(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        """InternLM streaming requests bypass dedup to avoid silent empty duplicates.

        Real-world clients may replay identical streaming requests (e.g. after reconnects).
        The generic streaming-dedup behavior returns a done-only stream, which can look
        like a successful but empty completion. For InternLM we bypass dedup so the
        request reaches the backend.
        """
        monkeypatch.setenv("LLM_PROXY_DISABLE_EMPTY_STREAM_RECOVERY", "1")
        request = ChatRequest(
            model="internlm:internlm/intern-s1-pro",
            messages=[ChatMessage(role="user", content="test")],
            stream=True,
        )
        session_id = "test-session"
        context = RequestContext(
            headers={"user-agent": "generic-client/1.0"},
            cookies={},
            state=MagicMock(),
            app_state=MagicMock(),
            agent="generic-client/1.0",
        )

        async def _minimal_stream():
            yield ProcessedResponse(
                content='data: {"choices":[{"index":0,"delta":{"content":"."}}]}\n\n',
            )

        mock_backend_processor.process_backend_request = AsyncMock(
            return_value=StreamingResponseEnvelope(content=_minimal_stream())
        )
        mock_dedup_service.check_and_register.return_value = (True, "hash123", 10.0)

        await backend_request_manager.process_backend_request(
            request, session_id, context
        )

        mock_dedup_service.check_and_register.assert_not_called()
        mock_backend_processor.process_backend_request.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_streaming_dedup_bypassed_for_kimi_streaming_requests(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        """Kimi streaming requests bypass dedup to avoid silent done-only duplicates."""
        monkeypatch.setenv("LLM_PROXY_DISABLE_EMPTY_STREAM_RECOVERY", "1")
        request = ChatRequest(
            model="kimi-code:kimi/kimi-for-coding",
            messages=[ChatMessage(role="user", content="test")],
            stream=True,
        )
        session_id = "test-session"
        context = RequestContext(
            headers={"user-agent": "generic-client/1.0"},
            cookies={},
            state=MagicMock(),
            app_state=MagicMock(),
            agent="generic-client/1.0",
        )

        async def _minimal_stream():
            yield ProcessedResponse(
                content='data: {"choices":[{"index":0,"delta":{"content":"."}}]}\n\n',
            )

        mock_backend_processor.process_backend_request = AsyncMock(
            return_value=StreamingResponseEnvelope(content=_minimal_stream())
        )
        mock_dedup_service.check_and_register.return_value = (True, "hash123", 10.0)

        await backend_request_manager.process_backend_request(
            request, session_id, context
        )

        mock_dedup_service.check_and_register.assert_not_called()
        mock_backend_processor.process_backend_request.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_streaming_dedup_marks_complete_only_after_stream_consumed(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
    ) -> None:
        """Regression: do not mark streaming request complete before the stream ends."""

        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            stream=True,
        )
        session_id = "test-session"
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )

        mock_dedup_service.check_and_register.return_value = (False, "hash123")

        async def _two_chunk_stream():
            yield ProcessedResponse(content=b"data: chunk1\n\n")
            yield ProcessedResponse(content=b"data: chunk2\n\n")

        envelope = StreamingResponseEnvelope(content=_two_chunk_stream())
        mock_backend_processor.process_backend_request = AsyncMock(
            return_value=envelope
        )

        async def _passthrough_handle(
            *, stream: StreamingResponseEnvelope, **_: Any
        ) -> StreamingResponseEnvelope:
            return stream

        cast(Any, backend_request_manager._post_backend_response_coordinator)._streaming_handler.handle = AsyncMock(side_effect=_passthrough_handle)  # type: ignore[assignment]

        result = await backend_request_manager.process_backend_request(
            request, session_id, context
        )
        assert isinstance(result, StreamingResponseEnvelope)
        assert result.content is not None

        # Not complete until the stream is actually consumed.
        mock_dedup_service.mark_request_complete.assert_not_awaited()

        _ = await result.content.__anext__()
        mock_dedup_service.mark_request_complete.assert_not_awaited()

        # Exhaust the stream
        with contextlib.suppress(StopAsyncIteration):
            while True:
                _ = await result.content.__anext__()

        mock_dedup_service.mark_request_complete.assert_awaited_once_with(
            "hash123",
            session_id,
            status_code=200,
            client_disconnected=False,
        )

    @pytest.mark.asyncio
    async def test_streaming_dedup_marks_client_disconnect_on_stream_close(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
    ) -> None:
        """Regression: a client disconnect should mark request completion as disconnect."""

        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            stream=True,
        )
        session_id = "test-session"
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )

        mock_dedup_service.check_and_register.return_value = (False, "hash123")

        hold_open = asyncio.Event()

        async def _hanging_stream():
            try:
                yield ProcessedResponse(content=b"data: chunk1\n\n")
                await hold_open.wait()
            except GeneratorExit:
                return

        envelope = StreamingResponseEnvelope(content=_hanging_stream())
        mock_backend_processor.process_backend_request = AsyncMock(
            return_value=envelope
        )

        async def _passthrough_handle(
            *, stream: StreamingResponseEnvelope, **_: Any
        ) -> StreamingResponseEnvelope:
            return stream

        cast(Any, backend_request_manager._post_backend_response_coordinator)._streaming_handler.handle = AsyncMock(side_effect=_passthrough_handle)  # type: ignore[assignment]

        result = await backend_request_manager.process_backend_request(
            request, session_id, context
        )
        assert isinstance(result, StreamingResponseEnvelope)
        assert result.content is not None

        _ = await result.content.__anext__()

        # Close early to simulate a client disconnect.
        aclose = getattr(result.content, "aclose", None)
        assert aclose is not None
        with contextlib.suppress(GeneratorExit):
            await aclose()

        mock_dedup_service.mark_request_complete.assert_awaited_once_with(
            "hash123",
            session_id,
            status_code=None,
            client_disconnected=True,
        )

    @pytest.mark.asyncio
    async def test_streaming_dedup_treats_disconnect_after_done_as_success(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
    ) -> None:
        """Regression: disconnect after terminal [DONE] should be marked as success."""

        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            stream=True,
        )
        session_id = "test-session"
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )

        mock_dedup_service.check_and_register.return_value = (False, "hash123")

        hold_open = asyncio.Event()

        async def _done_then_hang():
            try:
                yield ProcessedResponse(content=b"data: chunk1\n\n")
                yield ProcessedResponse(content=b"data: [DONE]\n\n")
                await hold_open.wait()
            except GeneratorExit:
                return

        envelope = StreamingResponseEnvelope(content=_done_then_hang())
        mock_backend_processor.process_backend_request = AsyncMock(
            return_value=envelope
        )

        async def _passthrough_handle(
            *, stream: StreamingResponseEnvelope, **_: Any
        ) -> StreamingResponseEnvelope:
            return stream

        cast(Any, backend_request_manager._post_backend_response_coordinator)._streaming_handler.handle = AsyncMock(side_effect=_passthrough_handle)  # type: ignore[assignment]

        result = await backend_request_manager.process_backend_request(
            request, session_id, context
        )
        assert isinstance(result, StreamingResponseEnvelope)
        assert result.content is not None

        # Consume until DONE is observed by downstream.
        _ = await result.content.__anext__()
        _ = await result.content.__anext__()

        # Close early to simulate a client disconnect right after DONE.
        aclose = getattr(result.content, "aclose", None)
        assert aclose is not None
        with contextlib.suppress(GeneratorExit):
            await aclose()

        mock_dedup_service.mark_request_complete.assert_awaited_once_with(
            "hash123",
            session_id,
            status_code=200,
            client_disconnected=False,
        )

    @pytest.mark.asyncio
    async def test_streaming_dedup_parses_finish_reason_stop_and_marks_success(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
    ) -> None:
        """Regression: finish_reason parsing should not crash, and disconnect after stop should be success."""

        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            stream=True,
        )
        session_id = "test-session"
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )

        mock_dedup_service.check_and_register.return_value = (False, "hash123")

        hold_open = asyncio.Event()

        async def _stop_then_hang():
            try:
                yield ProcessedResponse(
                    content=b'data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}\n\n'
                )
                await hold_open.wait()
            except GeneratorExit:
                return

        envelope = StreamingResponseEnvelope(content=_stop_then_hang())
        mock_backend_processor.process_backend_request = AsyncMock(
            return_value=envelope
        )

        async def _passthrough_handle(
            *, stream: StreamingResponseEnvelope, **_: Any
        ) -> StreamingResponseEnvelope:
            return stream

        cast(Any, backend_request_manager._post_backend_response_coordinator)._streaming_handler.handle = AsyncMock(side_effect=_passthrough_handle)  # type: ignore[assignment]

        result = await backend_request_manager.process_backend_request(
            request, session_id, context
        )
        assert isinstance(result, StreamingResponseEnvelope)
        assert result.content is not None

        _ = await result.content.__anext__()

        aclose = getattr(result.content, "aclose", None)
        assert aclose is not None
        with contextlib.suppress(GeneratorExit):
            await aclose()

        mock_dedup_service.mark_request_complete.assert_awaited_once_with(
            "hash123",
            session_id,
            status_code=200,
            client_disconnected=False,
        )

    @pytest.mark.asyncio
    async def test_streaming_dedup_parses_finish_reason_error_and_marks_error_code(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
    ) -> None:
        """Regression: finish_reason=error should not be misclassified as client disconnect."""

        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            stream=True,
        )
        session_id = "test-session"
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )

        mock_dedup_service.check_and_register.return_value = (False, "hash123")

        hold_open = asyncio.Event()

        async def _error_then_hang():
            try:
                yield ProcessedResponse(
                    content=b'data: {"choices":[{"index":0,"delta":{},"finish_reason":"error"}],"error":{"status_code":503,"message":"Service Unavailable"}}\n\n'
                )
                await hold_open.wait()
            except GeneratorExit:
                return

        envelope = StreamingResponseEnvelope(content=_error_then_hang())
        mock_backend_processor.process_backend_request = AsyncMock(
            return_value=envelope
        )

        async def _passthrough_handle(
            *, stream: StreamingResponseEnvelope, **_: Any
        ) -> StreamingResponseEnvelope:
            return stream

        cast(Any, backend_request_manager._post_backend_response_coordinator)._streaming_handler.handle = AsyncMock(side_effect=_passthrough_handle)  # type: ignore[assignment]

        result = await backend_request_manager.process_backend_request(
            request, session_id, context
        )
        assert isinstance(result, StreamingResponseEnvelope)
        assert result.content is not None

        _ = await result.content.__anext__()

        aclose = getattr(result.content, "aclose", None)
        assert aclose is not None
        with contextlib.suppress(GeneratorExit):
            await aclose()

        mock_dedup_service.mark_request_complete.assert_awaited_once_with(
            "hash123",
            session_id,
            status_code=503,
            client_disconnected=False,
        )

    @pytest.mark.asyncio
    async def test_process_backend_request_routing_error_temporarily_unavailable_records_503(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
    ) -> None:
        """RoutingError with temporarily_unavailable should record status_code=503 (retriable)."""
        from src.core.common.exceptions import RoutingError

        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )
        session_id = "test-session"
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )

        mock_dedup_service.check_and_register.return_value = (False, "hash_route_err")
        mock_backend_processor.process_backend_request = AsyncMock(
            side_effect=RoutingError(
                message="Backend temporarily unavailable",
                details={
                    "code": "temporarily_unavailable",
                    "category": "availability",
                    "retryable": True,
                },
            )
        )

        with pytest.raises(RoutingError) as exc_info:
            await backend_request_manager.process_backend_request(
                request, session_id, context
            )

        assert exc_info.value.status_code == 503
        mock_dedup_service.mark_request_complete.assert_awaited_once_with(
            "hash_route_err", session_id, status_code=503
        )

    @pytest.mark.asyncio
    async def test_process_backend_request_unhandled_exception_records_500(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
    ) -> None:
        """Unhandled exceptions without status_code attribute should default to status_code=500."""
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )
        session_id = "test-session"
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )

        mock_dedup_service.check_and_register.return_value = (False, "hash_unhandled")
        mock_backend_processor.process_backend_request = AsyncMock(
            side_effect=RuntimeError("unexpected failure")
        )

        with pytest.raises(RuntimeError):
            await backend_request_manager.process_backend_request(
                request, session_id, context
            )

        mock_dedup_service.mark_request_complete.assert_awaited_once_with(
            "hash_unhandled", session_id, status_code=500
        )


    def test_should_bypass_dedup_for_responses_semantic_pipeline_stream(
        self, backend_request_manager: BackendRequestManager
    ) -> None:
        request = ChatRequest(
            model="openai-chatgpt-plan:gpt-6.1-sol",
            messages=[ChatMessage(role="user", content="x")],
            stream=True,
        )
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )
        context.extensions["responses_semantic_pipeline"] = True
        assert backend_request_manager._should_bypass_dedup(request, context) is True

    def test_should_not_bypass_dedup_for_plain_chat_stream(
        self, backend_request_manager: BackendRequestManager
    ) -> None:
        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="x")],
            stream=True,
        )
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )
        assert backend_request_manager._should_bypass_dedup(request, context) is False

    @pytest.mark.asyncio
    async def test_done_only_stream_responses_path_emits_incomplete_not_chat_chunk(
        self,
        backend_request_manager: BackendRequestManager,
        mock_dedup_service: AsyncMock,
        mock_backend_processor: MagicMock,
    ) -> None:
        """Regression: Responses dedup must not emit chat.completion.chunk / empty completed."""
        request = ChatRequest(
            model="openai-chatgpt-plan:gpt-6.1-sol",
            messages=[ChatMessage(role="user", content="Call read once")],
            stream=True,
        )
        context = RequestContext(
            headers={}, cookies={}, state=MagicMock(), app_state=MagicMock()
        )
        # Force the done-only path (bypass off) to validate shape.
        context.extensions["responses_semantic_pipeline"] = True
        # Temporarily disable bypass by clearing the flag after we want shape test:
        # Instead call _done_only indirectly: mark as duplicate AND clear bypass flag.
        context.extensions.pop("responses_semantic_pipeline", None)
        context.extensions["responses_semantic_pipeline"] = True

        # Monkeypatch bypass to False so done-only runs while responses_path is True.
        backend_request_manager._should_bypass_dedup = lambda req, ctx: False  # type: ignore[method-assign]
        mock_dedup_service.check_and_register.return_value = (True, "hashdup", 1.0)
        mock_backend_processor.process_backend_request = AsyncMock()

        result = await backend_request_manager.process_backend_request(
            request, "sess", context
        )
        assert isinstance(result, StreamingResponseEnvelope)
        assert result.headers.get("x-llmproxy-duplicate-request") == "true"
        chunks: list[bytes] = []
        assert result.content is not None
        async for item in result.content:
            content = getattr(item, "content", item)
            if isinstance(content, bytes):
                chunks.append(content)
            elif isinstance(content, str):
                chunks.append(content.encode())
        blob = b"".join(chunks)
        assert b"chat.completion.chunk" not in blob
        assert b"response.incomplete" in blob
        assert b"duplicate_request" in blob
        assert b"[DONE]" in blob

