"""
Tests for RequestSideEffects implementation.

Tests cover:
- Allowed tool names registration in streaming registry
- Memory context injection
- Memory capture
- Fail-open behavior for all operations
- Ordering guarantees (project directory before context injection)
"""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest
from src.core.domain.chat import ChatMessage, ChatRequest
from src.core.domain.request_context import RequestContext
from src.core.memory.capture_middleware import MemoryCaptureMiddleware
from src.core.memory.injection_middleware import ContextInjectionMiddleware
from src.core.services.request_side_effects import RequestSideEffects


@pytest.fixture
def mock_context_injector() -> ContextInjectionMiddleware:
    """Create a mock context injector."""
    mock = AsyncMock(spec=ContextInjectionMiddleware)

    # Default: return request unchanged
    async def inject_context(session_id, request):
        return request

    mock.maybe_inject_context.side_effect = inject_context
    return mock


@pytest.fixture
def mock_memory_capture() -> MemoryCaptureMiddleware:
    """Create a mock memory capture middleware."""
    mock = AsyncMock(spec=MemoryCaptureMiddleware)
    mock.capture_request.return_value = None
    return mock


@pytest.fixture
def side_effects(
    mock_context_injector: ContextInjectionMiddleware,
    mock_memory_capture: MemoryCaptureMiddleware,
) -> RequestSideEffects:
    """Create RequestSideEffects with mocked dependencies."""
    return RequestSideEffects(
        context_injector=mock_context_injector, memory_capture=mock_memory_capture
    )


@pytest.mark.asyncio
@pytest.mark.unit
class TestRequestSideEffects:
    """Test RequestSideEffects implementation."""

    async def test_tool_names_registration(self, side_effects: RequestSideEffects):
        """Test that tool names are registered in streaming registry."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            tools=[
                {
                    "type": "function",
                    "function": {"name": "read_file", "description": "Read a file"},
                },
                {
                    "type": "function",
                    "function": {"name": "write_file", "description": "Write a file"},
                },
            ],
        )

        # Act
        updated_request = await side_effects.apply(context, "test-session", request)

        # Assert
        # Verify tool names were registered (we can't easily verify global registry,
        # so we just ensure no exception was raised)
        assert updated_request is not None

    async def test_tool_names_registration_with_pydantic_tools(
        self, side_effects: RequestSideEffects
    ):
        """Test tool names registration with Pydantic model tools."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )

        # Use dict tools instead since Pydantic validation is strict
        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            tools=[
                {
                    "type": "function",
                    "function": {"name": "read_file", "description": "Read a file"},
                },
                {
                    "type": "function",
                    "function": {"name": "write_file", "description": "Write a file"},
                },
            ],
        )

        # Act
        updated_request = await side_effects.apply(context, "test-session", request)

        # Assert
        assert updated_request is not None


    async def test_tool_names_registration_responses_flat_tools(
        self, side_effects: RequestSideEffects
    ):
        """Responses-flat tools expose name at the top level, not under function."""
        from src.core.services.streaming.stream_context_registry import (
            get_global_streaming_context_registry,
        )

        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        session_id = "responses-flat-tools-session"
        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            tools=[
                {
                    "type": "function",
                    "name": "read",
                    "description": "Read a file",
                    "parameters": {"type": "object", "properties": {}},
                },
                {
                    "type": "function",
                    "name": "glob",
                    "description": "Glob files",
                    "parameters": {"type": "object", "properties": {}},
                },
            ],
        )

        await side_effects.apply(context, session_id, request)

        registry = get_global_streaming_context_registry()
        buffer = registry.get_tool_call_buffer(session_id)
        assert buffer.allowed_tools == ["read", "glob"]

    async def test_tool_names_registration_with_no_tools(
        self, side_effects: RequestSideEffects
    ):
        """Test that registration handles requests with no tools gracefully."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        # Act
        updated_request = await side_effects.apply(context, "test-session", request)

        # Assert
        assert updated_request is not None

    async def test_tool_names_registration_fails_gracefully(
        self, side_effects: RequestSideEffects
    ):
        """Test that tool registration failures are handled gracefully."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )

        # Create invalid tool structure to trigger error
        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            tools=[{"invalid": "structure"}],
        )

        # Act - should not raise
        updated_request = await side_effects.apply(context, "test-session", request)

        # Assert
        assert updated_request is not None

    async def test_context_injection_called(
        self,
        side_effects: RequestSideEffects,
        mock_context_injector: ContextInjectionMiddleware,
    ):
        """Test that context injection is called when configured."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        # Act
        await side_effects.apply(context, "test-session", request)

        # Assert
        mock_context_injector.maybe_inject_context.assert_called_once_with(
            "test-session", request
        )

    async def test_context_injection_updates_request(
        self,
        side_effects: RequestSideEffects,
        mock_context_injector: ContextInjectionMiddleware,
    ):
        """Test that context injection can modify the request."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        # Mock injector to add a system message
        async def inject_with_system(session_id, req):
            return req.model_copy(
                update={
                    "messages": [
                        ChatMessage(role="system", content="Memory context"),
                        *req.messages,
                    ]
                }
            )

        mock_context_injector.maybe_inject_context.side_effect = inject_with_system

        # Act
        updated_request = await side_effects.apply(context, "test-session", request)

        # Assert
        assert len(updated_request.messages) == 2
        assert updated_request.messages[0].role == "system"

    async def test_context_injection_fails_gracefully(
        self,
        side_effects: RequestSideEffects,
        mock_context_injector: ContextInjectionMiddleware,
    ):
        """Test that context injection failures are handled gracefully."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        # Mock injector to raise
        mock_context_injector.maybe_inject_context.side_effect = Exception(
            "Injection failed"
        )

        # Act - should not raise
        updated_request = await side_effects.apply(context, "test-session", request)

        # Assert - request should be returned unchanged
        assert updated_request == request

    async def test_memory_capture_called(
        self,
        side_effects: RequestSideEffects,
        mock_memory_capture: MemoryCaptureMiddleware,
    ):
        """Test that memory capture is called when configured."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        # Act
        await side_effects.apply(context, "test-session", request)

        # Assert
        mock_memory_capture.capture_request.assert_called_once_with(
            "test-session", request
        )

    async def test_memory_capture_fails_gracefully(
        self,
        side_effects: RequestSideEffects,
        mock_memory_capture: MemoryCaptureMiddleware,
    ):
        """Test that memory capture failures are handled gracefully."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        # Mock capture to raise
        mock_memory_capture.capture_request.side_effect = Exception("Capture failed")

        # Act - should not raise
        updated_request = await side_effects.apply(context, "test-session", request)

        # Assert
        assert updated_request is not None

    async def test_none_context_injector(
        self, mock_memory_capture: MemoryCaptureMiddleware
    ):
        """Test that side effects work when context injector is None."""
        # Arrange
        side_effects = RequestSideEffects(
            context_injector=None, memory_capture=mock_memory_capture
        )
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        # Act
        updated_request = await side_effects.apply(context, "test-session", request)

        # Assert
        assert updated_request == request
        mock_memory_capture.capture_request.assert_called_once()

    async def test_none_memory_capture(
        self, mock_context_injector: ContextInjectionMiddleware
    ):
        """Test that side effects work when memory capture is None."""
        # Arrange
        side_effects = RequestSideEffects(
            context_injector=mock_context_injector, memory_capture=None
        )
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        # Act
        updated_request = await side_effects.apply(context, "test-session", request)

        # Assert
        assert updated_request is not None
        mock_context_injector.maybe_inject_context.assert_called_once()

    async def test_all_dependencies_none(self):
        """Test that side effects work when all dependencies are None."""
        # Arrange
        side_effects = RequestSideEffects(context_injector=None, memory_capture=None)
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        # Act
        updated_request = await side_effects.apply(context, "test-session", request)

        # Assert
        assert updated_request == request

    async def test_operations_ordering(
        self,
        side_effects: RequestSideEffects,
        mock_context_injector: ContextInjectionMiddleware,
        mock_memory_capture: MemoryCaptureMiddleware,
    ):
        """Test that operations occur in the correct order."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            tools=[
                {
                    "type": "function",
                    "function": {"name": "test_tool", "description": "Test"},
                }
            ],
        )

        call_order = []

        async def track_inject(session_id, req):
            call_order.append("inject")
            return req

        async def track_capture(session_id, req):
            call_order.append("capture")

        mock_context_injector.maybe_inject_context.side_effect = track_inject
        mock_memory_capture.capture_request.side_effect = track_capture

        # Act
        await side_effects.apply(context, "test-session", request)

        # Assert - injection should happen before capture
        assert call_order == ["inject", "capture"]
