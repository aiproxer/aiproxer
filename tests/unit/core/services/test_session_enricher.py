"""
Tests for SessionEnricher implementation.

Tests cover:
- Session ID resolution and session loading
- Agent normalization (incoming agent vs session agent)
- Client OS detection and propagation
- VTC detection and enablement
- Project directory auto-resolution
- Fail-open behavior for best-effort operations
"""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, Mock

import pytest
from src.core.domain.chat import ChatMessage, ChatRequest
from src.core.domain.request_context import RequestContext
from src.core.domain.responses_native_wiring import ACP_RESPONSES_TEXT_ONLY_MODE_KEY
from src.core.domain.session import Session, SessionState
from src.core.interfaces.application_state_interface import IApplicationState
from src.core.interfaces.session_manager_interface import ISessionManager
from src.core.services.session_enricher import SessionEnricher


@pytest.fixture
def mock_session_manager() -> ISessionManager:
    """Create a mock session manager."""
    mock = AsyncMock(spec=ISessionManager)
    mock.resolve_session_id.return_value = "test-session-123"

    # Mock session with state
    session = MagicMock(spec=Session)
    session.agent = None
    session.state = MagicMock(spec=SessionState)
    session.state.client_os = None
    session.state.vtc_enabled = False
    session.state.project_dir_resolution_attempted = False
    session.update_state = MagicMock()

    mock.get_session.return_value = session
    mock.update_session_agent.return_value = session

    return mock


@pytest.fixture
def mock_app_state() -> IApplicationState:
    """Create a mock application state."""
    mock = Mock(spec=IApplicationState)

    # Mock app config
    app_config = MagicMock()
    app_config.vtc_client_patterns = ["cursor", "windsurf"]

    mock.get_setting.return_value = app_config
    mock.get_service.return_value = None

    return mock


@pytest.fixture
def enricher(
    mock_session_manager: ISessionManager, mock_app_state: IApplicationState
) -> SessionEnricher:
    """Create a SessionEnricher with mocked dependencies."""
    return SessionEnricher(
        session_manager=mock_session_manager, app_state=mock_app_state
    )


@pytest.mark.asyncio
@pytest.mark.unit
class TestSessionEnricher:
    """Test SessionEnricher implementation."""

    async def test_basic_session_resolution(
        self, enricher: SessionEnricher, mock_session_manager: ISessionManager
    ):
        """Test basic session resolution and loading."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        # Act
        session, updated_request = await enricher.enrich(context, request)

        # Assert
        assert session is not None
        mock_session_manager.resolve_session_id.assert_called_once_with(context)
        mock_session_manager.get_session.assert_called_once_with("test-session-123")

    async def test_domain_request_attached_to_context(self, enricher: SessionEnricher):
        """Test that request is attached to context as domain_request."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        # Act
        await enricher.enrich(context, request)

        # Assert
        assert hasattr(context, "domain_request")
        assert context.domain_request is request  # type: ignore

    async def test_agent_normalization_from_request(
        self, enricher: SessionEnricher, mock_session_manager: ISessionManager
    ):
        """Test agent normalization when agent comes from request."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            agent="cursor",
        )

        # Session has different agent
        session = MagicMock(spec=Session)
        session.agent = "windsurf"
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = False
        session.state.project_dir_resolution_attempted = False

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Act
        _, updated_request = await enricher.enrich(context, request)

        # Assert
        mock_session_manager.update_session_agent.assert_called_once_with(
            session, "cursor"
        )
        # Request should be updated with session agent
        assert updated_request.agent == "windsurf"

    async def test_agent_normalization_from_context(
        self, enricher: SessionEnricher, mock_session_manager: ISessionManager
    ):
        """Test agent normalization when agent comes from context."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock(), agent="cursor"
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        # Session has different agent
        session = MagicMock(spec=Session)
        session.agent = "windsurf"
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = False
        session.state.project_dir_resolution_attempted = False

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Act
        _, updated_request = await enricher.enrich(context, request)

        # Assert
        mock_session_manager.update_session_agent.assert_called_once_with(
            session, "cursor"
        )
        assert updated_request.agent == "windsurf"

    async def test_client_os_detection_windows(
        self, enricher: SessionEnricher, mock_session_manager: ISessionManager
    ):
        """Test client OS detection for Windows."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4",
            messages=[
                ChatMessage(role="user", content="User system info (win32 10.0.19045)")
            ],
        )

        session = MagicMock(spec=Session)
        session.agent = None
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = False
        session.state.project_dir_resolution_attempted = False

        # Make with_client_os return a properly configured new state
        def make_new_state_with_os(os_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = os_value
            new_state.vtc_enabled = session.state.vtc_enabled
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_client_os = make_new_state_with_os

        # Make with_vtc_enabled return a properly configured new state
        def make_new_state_with_vtc(vtc_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = session.state.client_os
            new_state.vtc_enabled = vtc_value
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_vtc_enabled = make_new_state_with_vtc

        # Make update_state actually update session.state
        def update_state_impl(new_state):
            session.state = new_state

        session.update_state = MagicMock(side_effect=update_state_impl)

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Act
        await enricher.enrich(context, request)

        # Assert
        session.update_state.assert_called_once()
        # Verify client_os was set in context
        assert context.ensure_processing_context().values.get("client_os") == "windows"

    async def test_client_os_detection_macos(
        self, enricher: SessionEnricher, mock_session_manager: ISessionManager
    ):
        """Test client OS detection for macOS."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4",
            messages=[
                ChatMessage(role="user", content="User system info (darwin 22.0.0)")
            ],
        )

        session = MagicMock(spec=Session)
        session.agent = None
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = False
        session.state.project_dir_resolution_attempted = False

        # Make with_client_os return a properly configured new state
        def make_new_state_with_os(os_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = os_value
            new_state.vtc_enabled = session.state.vtc_enabled
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_client_os = make_new_state_with_os

        # Make with_vtc_enabled return a properly configured new state
        def make_new_state_with_vtc(vtc_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = session.state.client_os
            new_state.vtc_enabled = vtc_value
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_vtc_enabled = make_new_state_with_vtc

        # Make update_state actually update session.state
        def update_state_impl(new_state):
            session.state = new_state

        session.update_state = MagicMock(side_effect=update_state_impl)

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Act
        await enricher.enrich(context, request)

        # Assert
        session.update_state.assert_called_once()
        assert context.ensure_processing_context().values.get("client_os") == "macos"

    async def test_client_os_detection_linux(
        self, enricher: SessionEnricher, mock_session_manager: ISessionManager
    ):
        """Test client OS detection for Linux."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="User system info (linux)")],
        )

        session = MagicMock(spec=Session)
        session.agent = None
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = False
        session.state.project_dir_resolution_attempted = False

        # Make with_client_os return a properly configured new state
        def make_new_state_with_os(os_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = os_value
            new_state.vtc_enabled = session.state.vtc_enabled
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_client_os = make_new_state_with_os

        # Make with_vtc_enabled return a properly configured new state
        def make_new_state_with_vtc(vtc_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = session.state.client_os
            new_state.vtc_enabled = vtc_value
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_vtc_enabled = make_new_state_with_vtc

        # Make update_state actually update session.state
        def update_state_impl(new_state):
            session.state = new_state

        session.update_state = MagicMock(side_effect=update_state_impl)

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Act
        await enricher.enrich(context, request)

        # Assert
        session.update_state.assert_called_once()
        assert context.ensure_processing_context().values.get("client_os") == "linux"

    async def test_client_os_detection_from_windows_path(
        self, enricher: SessionEnricher, mock_session_manager: ISessionManager
    ):
        """Test client OS detection from Windows path pattern."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4",
            messages=[
                ChatMessage(
                    role="user", content="File located at C:\\Users\\test\\file.txt"
                )
            ],
        )

        session = MagicMock(spec=Session)
        session.agent = None
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = False
        session.state.project_dir_resolution_attempted = False

        # Make with_client_os return a properly configured new state
        def make_new_state_with_os(os_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = os_value
            new_state.vtc_enabled = session.state.vtc_enabled
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_client_os = make_new_state_with_os

        # Make with_vtc_enabled return a properly configured new state
        def make_new_state_with_vtc(vtc_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = session.state.client_os
            new_state.vtc_enabled = vtc_value
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_vtc_enabled = make_new_state_with_vtc

        # Make update_state actually update session.state
        def update_state_impl(new_state):
            session.state = new_state

        session.update_state = MagicMock(side_effect=update_state_impl)

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Act
        await enricher.enrich(context, request)

        # Assert
        session.update_state.assert_called_once()
        assert context.ensure_processing_context().values.get("client_os") == "windows"

    async def test_client_os_not_detected_when_already_set(
        self, enricher: SessionEnricher, mock_session_manager: ISessionManager
    ):
        """Test that OS detection is skipped when client_os is already set."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4",
            messages=[
                ChatMessage(role="user", content="User system info (win32 10.0.19045)")
            ],
        )

        session = MagicMock(spec=Session)
        session.agent = None
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = "macos"  # Already set
        session.state.vtc_enabled = False
        session.state.project_dir_resolution_attempted = False

        # Make with_client_os return a properly configured new state
        def make_new_state_with_os(os_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = os_value
            new_state.vtc_enabled = session.state.vtc_enabled
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_client_os = make_new_state_with_os

        # Make with_vtc_enabled return a properly configured new state
        def make_new_state_with_vtc(vtc_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = session.state.client_os
            new_state.vtc_enabled = vtc_value
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_vtc_enabled = make_new_state_with_vtc

        # Make update_state actually update session.state
        def update_state_impl(new_state):
            session.state = new_state

        session.update_state = MagicMock(side_effect=update_state_impl)

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Act
        await enricher.enrich(context, request)

        # Assert
        # update_state should not be called since OS was already detected
        session.update_state.assert_not_called()
        # But client_os should still be propagated to context
        assert context.ensure_processing_context().values.get("client_os") == "macos"

    async def test_vtc_detection_enabled(
        self,
        enricher: SessionEnricher,
        mock_session_manager: ISessionManager,
        mock_app_state: IApplicationState,
    ):
        """Test VTC detection and enablement."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            agent="cursor",
        )

        session = MagicMock(spec=Session)
        session.agent = None
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = False  # Not yet enabled
        session.state.project_dir_resolution_attempted = False

        # Make with_client_os return a properly configured new state
        def make_new_state_with_os(os_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = os_value
            new_state.vtc_enabled = session.state.vtc_enabled
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_client_os = make_new_state_with_os

        # Make with_vtc_enabled return a properly configured new state
        def make_new_state_with_vtc(vtc_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = session.state.client_os
            new_state.vtc_enabled = vtc_value
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_vtc_enabled = make_new_state_with_vtc

        # Make update_state actually update session.state
        def update_state_impl(new_state):
            session.state = new_state

        session.update_state = MagicMock(side_effect=update_state_impl)

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Mock VTC patterns
        app_config = MagicMock()
        app_config.vtc_client_patterns = ["cursor", "windsurf"]
        mock_app_state.get_setting.return_value = app_config

        # Act
        _, updated_request = await enricher.enrich(context, request)

        # Assert
        session.update_state.assert_called()
        # VTC flag should be propagated to request
        assert updated_request.vtc_enabled is True

    async def test_vtc_not_enabled_for_non_matching_agent(
        self,
        enricher: SessionEnricher,
        mock_session_manager: ISessionManager,
        mock_app_state: IApplicationState,
    ):
        """Test that VTC is not enabled for non-matching agents."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="test")],
            agent="other-agent",
        )

        session = MagicMock(spec=Session)
        session.agent = None
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = False
        session.state.project_dir_resolution_attempted = False

        # Make with_client_os return a properly configured new state
        def make_new_state_with_os(os_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = os_value
            new_state.vtc_enabled = session.state.vtc_enabled
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_client_os = make_new_state_with_os

        # Make with_vtc_enabled return a properly configured new state
        def make_new_state_with_vtc(vtc_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = session.state.client_os
            new_state.vtc_enabled = vtc_value
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_vtc_enabled = make_new_state_with_vtc

        # Make update_state actually update session.state
        def update_state_impl(new_state):
            session.state = new_state

        session.update_state = MagicMock(side_effect=update_state_impl)

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Mock VTC patterns
        app_config = MagicMock()
        app_config.vtc_client_patterns = ["cursor", "windsurf"]
        mock_app_state.get_setting.return_value = app_config

        # Act
        _, updated_request = await enricher.enrich(context, request)

        # Assert
        # VTC should not be enabled
        assert (
            not hasattr(updated_request, "vtc_enabled")
            or updated_request.vtc_enabled is None
        )

    async def test_vtc_already_enabled(
        self, enricher: SessionEnricher, mock_session_manager: ISessionManager
    ):
        """Test that VTC flag is propagated when already enabled in session."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        session = MagicMock(spec=Session)
        session.agent = "cursor"
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = True  # Already enabled
        session.state.project_dir_resolution_attempted = False

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Act
        _, updated_request = await enricher.enrich(context, request)

        # Assert
        assert updated_request.vtc_enabled is True

    async def test_project_directory_resolution(
        self,
        enricher: SessionEnricher,
        mock_session_manager: ISessionManager,
        mock_app_state: IApplicationState,
    ):
        """Test project directory auto-resolution."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        session = MagicMock(spec=Session)
        session.agent = None
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = False
        session.state.project_dir_resolution_attempted = False

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Mock project directory service
        project_dir_service = AsyncMock()
        mock_app_state.get_service.return_value = project_dir_service

        # Act
        await enricher.enrich(context, request)

        # Assert
        project_dir_service.maybe_resolve_project_directory.assert_called_once_with(
            session, request
        )

    async def test_project_directory_resolution_fails_gracefully(
        self,
        enricher: SessionEnricher,
        mock_session_manager: ISessionManager,
        mock_app_state: IApplicationState,
    ):
        """Test that project directory resolution failures are handled gracefully."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        session = MagicMock(spec=Session)
        session.agent = None
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = False
        session.state.project_dir_resolution_attempted = False

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Mock project directory service that raises
        project_dir_service = AsyncMock()
        project_dir_service.maybe_resolve_project_directory.side_effect = Exception(
            "Failed to resolve"
        )
        mock_app_state.get_service.return_value = project_dir_service

        # Act - should not raise
        await enricher.enrich(context, request)

        # Assert - call completed successfully despite error
        project_dir_service.maybe_resolve_project_directory.assert_called_once()

    async def test_project_directory_skipped_when_already_attempted(
        self,
        enricher: SessionEnricher,
        mock_session_manager: ISessionManager,
        mock_app_state: IApplicationState,
    ):
        """Test that project directory resolution is skipped when already attempted."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4", messages=[ChatMessage(role="user", content="test")]
        )

        session = MagicMock(spec=Session)
        session.agent = None
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = False
        session.state.project_dir_resolution_attempted = True  # Already attempted
        session.state.project_dir = r"C:\already\resolved"

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Mock project directory service
        project_dir_service = AsyncMock()
        mock_app_state.get_service.return_value = project_dir_service

        # Act
        await enricher.enrich(context, request)

        # Assert
        project_dir_service.maybe_resolve_project_directory.assert_not_called()

    async def test_project_directory_retried_when_first_attempt_left_dir_empty(
        self,
        enricher: SessionEnricher,
        mock_session_manager: ISessionManager,
        mock_app_state: IApplicationState,
    ):
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="Working directory: C:\\repo")],
        )

        session = MagicMock(spec=Session)
        session.agent = None
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = False
        session.state.project_dir_resolution_attempted = True
        session.state.project_dir = None

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        project_dir_service = AsyncMock()
        mock_app_state.get_service.return_value = project_dir_service

        await enricher.enrich(context, request)

        project_dir_service.maybe_resolve_project_directory.assert_called_once()

    async def test_project_directory_skipped_for_acp_responses_text_only_mode(
        self,
        enricher: SessionEnricher,
        mock_app_state: IApplicationState,
    ):
        """The static ACP workspace contract must not inspect prompt text for paths."""
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="cursor-cli-acp.default:cursor/glm-5.2-max",
            messages=[ChatMessage(role="user", content="work in another repository")],
            extra_body={ACP_RESPONSES_TEXT_ONLY_MODE_KEY: True},
        )
        project_dir_service = AsyncMock()
        mock_app_state.get_service.return_value = project_dir_service

        await enricher.enrich(context, request)

        project_dir_service.maybe_resolve_project_directory.assert_not_called()

    async def test_multimodal_content_os_detection(
        self, enricher: SessionEnricher, mock_session_manager: ISessionManager
    ):
        """Test OS detection from multimodal content (list of parts)."""
        # Arrange
        context = RequestContext(
            headers={}, cookies={}, state={}, app_state=MagicMock()
        )
        request = ChatRequest(
            model="gpt-4",
            messages=[
                ChatMessage(
                    role="user",
                    content=[
                        {"type": "text", "text": "User system info (win32 10.0.19045)"},
                        {
                            "type": "image_url",
                            "image_url": {"url": "https://example.com/image.png"},
                        },
                    ],
                )
            ],
        )

        session = MagicMock(spec=Session)
        session.agent = None
        session.state = MagicMock(spec=SessionState)
        session.state.client_os = None
        session.state.vtc_enabled = False
        session.state.project_dir_resolution_attempted = False

        # Make with_client_os return a properly configured new state
        def make_new_state_with_os(os_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = os_value
            new_state.vtc_enabled = session.state.vtc_enabled
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_client_os = make_new_state_with_os

        # Make with_vtc_enabled return a properly configured new state
        def make_new_state_with_vtc(vtc_value):
            new_state = MagicMock(spec=SessionState)
            new_state.client_os = session.state.client_os
            new_state.vtc_enabled = vtc_value
            new_state.project_dir_resolution_attempted = (
                session.state.project_dir_resolution_attempted
            )
            return new_state

        session.state.with_vtc_enabled = make_new_state_with_vtc

        # Make update_state actually update session.state
        def update_state_impl(new_state):
            session.state = new_state

        session.update_state = MagicMock(side_effect=update_state_impl)

        mock_session_manager.get_session.return_value = session
        mock_session_manager.update_session_agent.return_value = session

        # Act
        await enricher.enrich(context, request)

        # Assert
        session.update_state.assert_called_once()
        assert context.ensure_processing_context().values.get("client_os") == "windows"
