"""
Unit tests for GeminiHealthCheckService.

Tests verify health check behavior including first-use checks, caching,
error propagation, and endpoint fallback.
"""

from unittest.mock import AsyncMock, Mock

import httpx
import pytest
from src.connectors.gemini_base.endpoints import StandardCodeAssistEndpoint
from src.connectors.gemini_base.health_check_service import GeminiHealthCheckService
from src.connectors.gemini_base.models import GeminiOAuthCredentials
from src.core.common.exceptions import AuthenticationError, BackendError


@pytest.fixture(autouse=True)
def clear_health_check_cache():
    """Clear GeminiHealthCheckService global cache before each test for isolation."""
    GeminiHealthCheckService._successfully_checked_backends.clear()


@pytest.fixture
def mock_credential_coordinator():
    """Create a mock ICredentialCoordinator."""
    coordinator = Mock()
    coordinator.refresh_if_needed = AsyncMock(return_value=True)
    credentials = GeminiOAuthCredentials(
        access_token="test_access_token",
        refresh_token="test_refresh_token",
        expiry_date=9999999999999,
        project_id="test-project",
    )
    coordinator.credentials = credentials
    return coordinator


@pytest.fixture
def mock_endpoint_config():
    """Create a mock IEndpointConfig."""
    return StandardCodeAssistEndpoint()


@pytest.fixture
def mock_http_client():
    """Create a mock httpx.AsyncClient."""
    client = Mock(spec=httpx.AsyncClient)
    return client


@pytest.fixture
def health_check_service(
    mock_credential_coordinator, mock_endpoint_config, mock_http_client
):
    """Create a GeminiHealthCheckService instance."""
    return GeminiHealthCheckService(
        credential_coordinator=mock_credential_coordinator,
        endpoint_config=mock_endpoint_config,
        http_client=mock_http_client,
        backend_name="test-backend",
    )


class TestEnsureHealthy:
    """Test ensure_healthy method."""

    @pytest.mark.asyncio
    async def test_first_use_performs_health_check(
        self, health_check_service, mock_credential_coordinator, mock_http_client
    ):
        """Verify health check is performed on first use."""
        # Setup mock response
        mock_response = Mock()
        mock_response.status_code = 200
        mock_http_client.post = AsyncMock(return_value=mock_response)

        # Execute
        await health_check_service.ensure_healthy()

        # Verify
        mock_credential_coordinator.refresh_if_needed.assert_called_once()
        mock_http_client.post.assert_called_once()
        assert health_check_service._health_checked is True

    @pytest.mark.asyncio
    async def test_subsequent_calls_are_noop(
        self, health_check_service, mock_credential_coordinator, mock_http_client
    ):
        """Verify subsequent calls are no-ops if already checked."""
        # Setup mock response
        mock_response = Mock()
        mock_response.status_code = 200
        mock_http_client.post = AsyncMock(return_value=mock_response)

        # Execute first call
        await health_check_service.ensure_healthy()
        first_call_count = mock_http_client.post.call_count

        # Execute second call
        await health_check_service.ensure_healthy()

        # Verify second call didn't make additional HTTP requests
        assert mock_http_client.post.call_count == first_call_count

    @pytest.mark.asyncio
    async def test_refresh_failure_raises_backend_error(
        self, health_check_service, mock_credential_coordinator
    ):
        """Verify BackendError is raised on refresh failure."""
        # Setup mock to fail refresh
        mock_credential_coordinator.refresh_if_needed = AsyncMock(return_value=False)

        # Execute and verify
        with pytest.raises(BackendError) as exc_info:
            await health_check_service.ensure_healthy()

        assert "Failed to refresh OAuth token" in exc_info.value.message
        assert exc_info.value.backend_name == "test-backend"

    @pytest.mark.asyncio
    async def test_health_check_failure_logs_warning_but_continues(
        self, health_check_service, mock_credential_coordinator, mock_http_client
    ):
        """Verify non-critical health check failures log warnings but don't raise."""
        # Setup mock to fail health check
        mock_response = Mock()
        mock_response.status_code = 500
        mock_http_client.post = AsyncMock(return_value=mock_response)

        # Execute (should not raise)
        await health_check_service.ensure_healthy()

        # Verify it was marked as checked despite failure
        assert health_check_service._health_checked is True

    @pytest.mark.asyncio
    async def test_disabled_health_checks_skip_check(
        self, mock_credential_coordinator, mock_endpoint_config, mock_http_client
    ):
        """Verify health checks are skipped when disabled."""
        service = GeminiHealthCheckService(
            credential_coordinator=mock_credential_coordinator,
            endpoint_config=mock_endpoint_config,
            http_client=mock_http_client,
            backend_name="test-backend",
            disable_health_checks=True,
        )

        # Execute
        await service.ensure_healthy()

        # Verify no HTTP calls were made
        mock_http_client.get.assert_not_called()
        mock_http_client.post.assert_not_called()
        assert service._health_checked is True


class TestPerformHealthCheck:
    """Test _perform_health_check method."""

    @pytest.mark.asyncio
    async def test_successful_check_via_load_code_assist(
        self, health_check_service, mock_http_client
    ):
        """Verify successful health check via loadCodeAssist endpoint."""
        # Setup mock response
        mock_response = Mock()
        mock_response.status_code = 200
        mock_http_client.post = AsyncMock(return_value=mock_response)

        # Execute
        result = await health_check_service._perform_health_check()

        # Verify
        assert result is True
        mock_http_client.post.assert_called_once()
        # Verify correct endpoint was called
        call_args = mock_http_client.post.call_args
        assert "loadCodeAssist" in call_args[0][0]

    @pytest.mark.asyncio
    async def test_timeout_handling(self, health_check_service, mock_http_client):
        """Verify timeout exceptions are handled gracefully."""
        # Setup mock to raise timeout
        mock_http_client.post = AsyncMock(side_effect=httpx.TimeoutException("Timeout"))

        # Execute
        result = await health_check_service._perform_health_check()

        # Verify
        assert result is False

    @pytest.mark.asyncio
    async def test_request_error_handling(self, health_check_service, mock_http_client):
        """Verify request errors are handled gracefully."""
        # Setup mock to raise request error
        mock_http_client.post = AsyncMock(
            side_effect=httpx.RequestError("Connection error")
        )

        # Execute
        result = await health_check_service._perform_health_check()

        # Verify
        assert result is False

    @pytest.mark.asyncio
    async def test_no_access_token_returns_false(
        self, health_check_service, mock_credential_coordinator
    ):
        """Verify False is returned when no access token is available."""
        # Setup mock with no credentials
        mock_credential_coordinator.credentials = None

        # Execute
        result = await health_check_service._perform_health_check()

        # Verify
        assert result is False

    @pytest.mark.asyncio
    async def test_authentication_error_handling(
        self, health_check_service, mock_credential_coordinator, mock_http_client
    ):
        """Verify AuthenticationError is handled gracefully."""
        # Setup mock to raise AuthenticationError
        mock_http_client.post = AsyncMock(
            side_effect=AuthenticationError("Auth failed")
        )

        # Execute
        result = await health_check_service._perform_health_check()

        # Verify
        assert result is False

    @pytest.mark.asyncio
    async def test_backend_error_handling(self, health_check_service, mock_http_client):
        """Verify BackendError is handled gracefully."""
        # Setup mock to raise BackendError
        mock_http_client.post = AsyncMock(side_effect=BackendError("Backend failed"))

        # Execute
        result = await health_check_service._perform_health_check()

        # Verify
        assert result is False

    @pytest.mark.asyncio
    async def test_unexpected_exception_handling(
        self, health_check_service, mock_http_client
    ):
        """Verify unexpected exceptions are handled gracefully."""
        # Setup mock to raise unexpected exception
        mock_http_client.post = AsyncMock(side_effect=ValueError("Unexpected error"))

        # Execute
        result = await health_check_service._perform_health_check()

        # Verify
        assert result is False
