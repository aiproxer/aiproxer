"""Shared pytest fixtures for OpenAI Codex ResponseExecutor unit tests."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest
from src.connectors._openai_codex_capabilities import CodexClientCapabilities
from src.connectors.openai_codex.contracts import (
    CodexPayload,
    CodexRequestContext,
    ProcessedMessage,
)
from src.connectors.openai_codex.executor import ResponseExecutor
from src.connectors.openai_codex.interfaces import ICredentialManager
from src.core.domain.chat import CanonicalChatRequest, ChatMessage


@pytest.fixture
def mock_base_connector():
    """Create a mock base OpenAI connector."""
    connector = MagicMock()
    connector.client = MagicMock()
    connector.translation_service = MagicMock()
    connector.get_headers = MagicMock(return_value={"Authorization": "Bearer token"})
    connector._handle_streaming_response = AsyncMock()
    connector._handle_rate_limit_rotation = AsyncMock(return_value=False)
    connector._handle_auth_failure_rotation = AsyncMock(return_value=False)
    connector._handle_forbidden_rotation = AsyncMock(return_value=False)
    # Mock methods that might be called during header building
    connector._codex_user_agent = MagicMock(return_value="test-user-agent")
    connector._codex_account_id = MagicMock(return_value=None)
    connector.codex_client_version = ""
    return connector


@pytest.fixture
def mock_credential_manager():
    """Create a mock credential manager."""
    manager = MagicMock(spec=ICredentialManager)
    manager.refresh_access_token = AsyncMock(return_value=True)
    manager.get_access_token = MagicMock(return_value="test_token")
    manager.handle_forbidden_rotation = AsyncMock(return_value=False)
    return manager


@pytest.fixture
def executor(mock_base_connector, mock_credential_manager):
    """Create a ResponseExecutor instance for testing."""
    return ResponseExecutor(
        mock_base_connector,
        mock_credential_manager,
        max_retries=2,
        retry_backoff_seconds=(0.1, 0.2),
        codex_client_version="9.9.9",
    )


@pytest.fixture
def sample_context():
    """Create a sample CodexRequestContext."""
    request = CanonicalChatRequest(
        model="gpt-5.1-codex",
        messages=[ChatMessage(role="user", content="Test message")],
        stream=False,
    )
    return CodexRequestContext(
        request=request,
        processed_messages=[
            ProcessedMessage(
                role="user",
                content="Test message",
                tool_calls=None,
            )
        ],
        effective_model="gpt-5.1-codex",
        capabilities=CodexClientCapabilities(),
        session_id="test-session-123",
    )


@pytest.fixture
def non_streaming_payload():
    """Create a non-streaming payload."""
    return CodexPayload(
        model="gpt-5.1-codex",
        input=[],
        tools=[],
        tool_choice="auto",
        parallel_tool_calls=False,
        store=False,
        stream=False,
        include=[],
        prompt_cache_key="test-key",
    )


@pytest.fixture
def streaming_payload():
    """Create a streaming payload."""
    return CodexPayload(
        model="gpt-5.1-codex",
        input=[],
        tools=[],
        tool_choice="auto",
        parallel_tool_calls=False,
        store=False,
        stream=True,
        include=[],
        prompt_cache_key="test-key",
    )
