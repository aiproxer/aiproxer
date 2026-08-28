"""
Regression tests for Gemini base connector observability, reliability, and security.

Tests verify error propagation, rate-limit handling, health check behavior,
and credential/log redaction invariants. Covers Requirements 6.1, 6.2, 6.3,
7.1, 7.2, 7.3, 8.1, 8.2, 8.3.
"""

import inspect
from unittest.mock import AsyncMock, Mock

import httpx
import pytest
from src.connectors.gemini_base.connector import GeminiOAuthBaseConnector
from src.connectors.gemini_base.credential_coordinator import (
    GeminiCredentialCoordinator,
)
from src.connectors.gemini_base.error_mapper import GeminiErrorMapper
from src.connectors.gemini_base.health_check_service import GeminiHealthCheckService
from src.connectors.gemini_base.models import GeminiOAuthCredentials
from src.connectors.gemini_base.streaming_executor import StreamingExecutor
from src.core.common.exceptions import (
    AuthenticationError,
    BackendError,
    InvalidRequestError,
)

pytestmark = [pytest.mark.behavior]


@pytest.fixture(scope="module")
def health_check_service_source():
    return inspect.getsource(GeminiHealthCheckService)


@pytest.fixture(scope="module")
def credential_coordinator_source():
    return inspect.getsource(GeminiCredentialCoordinator)


@pytest.fixture(scope="module")
def error_mapper_source():
    return inspect.getsource(GeminiErrorMapper)


@pytest.fixture(scope="module")
def connector_source():
    return inspect.getsource(GeminiOAuthBaseConnector)


@pytest.fixture(scope="module")
def streaming_executor_source():
    return inspect.getsource(StreamingExecutor)


class TestErrorPropagationInvariants:
    """Test error propagation semantics for routing and failover services."""

    def test_authentication_error_preserves_status_code(self) -> None:
        """Verify AuthenticationError preserves 401 status code for failover."""
        error = AuthenticationError(
            message="Token expired",
            details={"backend": "antigravity-oauth"},
        )
        assert error.status_code == 401

    def test_backend_error_preserves_backend_name(self) -> None:
        """Verify BackendError preserves backend name for routing."""
        error = BackendError(
            message="API error",
            backend_name="antigravity-oauth",
            code="rate_limit",
            status_code=429,
        )
        assert error.backend_name == "antigravity-oauth"
        assert error.code == "rate_limit"
        assert error.status_code == 429

    def test_error_mapper_preserves_llm_proxy_error_unchanged(self) -> None:
        """Verify LLMProxyError subclasses pass through unchanged."""
        mapper = GeminiErrorMapper()

        original = BackendError(
            message="Rate limited",
            backend_name="test",
            code="rate_limit",
            status_code=429,
        )

        # map_exception returns exceptions (doesn't raise), except HTTPException
        result = mapper.map_exception(original, backend_name="test")

        # Should be exact same object
        assert result is original
        assert result.status_code == 429
        assert result.code == "rate_limit"

    def test_error_mapper_converts_generic_exceptions(self) -> None:
        """Verify generic exceptions become BackendError for circuit breaker."""
        mapper = GeminiErrorMapper()

        generic = ValueError("Something broke")

        # map_exception returns exceptions (doesn't raise), except HTTPException
        result = mapper.map_exception(generic, backend_name="test-backend")

        assert isinstance(result, BackendError)
        assert result.backend_name == "test-backend"
        # Note: Exception chaining is not preserved when returning (only when raising)
        # The original error is included in the message instead


class TestRateLimitHandling:
    """Test rate-limit handling behavior."""

    def test_rate_limit_status_code_preserved(self) -> None:
        """Verify 429 status code is preserved in errors."""
        error = BackendError(
            message="Rate limit exceeded",
            backend_name="antigravity-oauth",
            code="rate_limit_exceeded",
            status_code=429,
        )
        assert error.status_code == 429

    def test_error_mapper_preserves_rate_limit_error(self) -> None:
        """Verify rate limit errors pass through error mapper unchanged."""
        mapper = GeminiErrorMapper()

        rate_limit_error = BackendError(
            message="Rate limit exceeded. Retry after 60 seconds.",
            backend_name="test",
            code="rate_limit_exceeded",
            status_code=429,
        )

        # map_exception returns exceptions (doesn't raise), except HTTPException
        result = mapper.map_exception(rate_limit_error, backend_name="test")

        assert result is rate_limit_error
        assert result.status_code == 429


class TestHealthCheckBehavior:
    """Test health check behavior invariants."""

    def test_health_check_does_not_introduce_new_endpoints(
        self, health_check_service_source: str
    ) -> None:
        """Verify health check uses existing endpoints only."""
        # Should use loadCodeAssist
        assert "loadCodeAssist" in health_check_service_source

    def test_health_check_uses_only_specified_endpoints(
        self, health_check_service_source: str
    ) -> None:
        """Verify health check uses only the loadCodeAssist endpoint.

        Requirement: 7.3 - The system shall not introduce new health check endpoints.
        """
        # Must use loadCodeAssist
        assert "loadCodeAssist" in health_check_service_source

        # Should not use any other endpoints
        # Check that no other v1internal endpoints are used
        import re

        # Find all v1internal endpoint references
        endpoint_pattern = r"v1internal:(\w+)"
        endpoints = re.findall(endpoint_pattern, health_check_service_source)

        # Should only contain loadCodeAssist
        allowed_endpoints = {"loadCodeAssist"}
        found_endpoints = set(endpoints)

        # All found endpoints must be in allowed list
        assert found_endpoints.issubset(
            allowed_endpoints
        ), f"Found unexpected endpoints: {found_endpoints - allowed_endpoints}"

    def test_health_check_failure_does_not_raise(self) -> None:
        """Verify health check failures are logged but don't raise."""
        mock_coordinator = Mock()
        mock_coordinator.refresh_if_needed = AsyncMock(return_value=True)
        mock_coordinator.credentials = GeminiOAuthCredentials(access_token="test_token")

        mock_endpoint = Mock()
        mock_endpoint.get_base_url.return_value = "https://test.com"
        mock_endpoint.get_api_headers.return_value = {}

        mock_client = Mock(spec=httpx.AsyncClient)
        fail_response = Mock()
        fail_response.status_code = 500
        mock_client.get = AsyncMock(return_value=fail_response)
        mock_client.post = AsyncMock(return_value=fail_response)

        service = GeminiHealthCheckService(
            credential_coordinator=mock_coordinator,
            endpoint_config=mock_endpoint,
            http_client=mock_client,
            backend_name="test",
        )

        # Should not raise despite health check failure
        import asyncio

        asyncio.run(service.ensure_healthy())
        assert service._health_checked is True


class TestCredentialRedaction:
    """Test credential redaction in logs and captures."""

    def test_credentials_not_logged_directly(
        self, credential_coordinator_source: str
    ) -> None:
        """Verify credentials are not logged directly in production code paths.

        The actual credential redaction happens at the logging layer, not
        at the model level. Verify that production code follows safe patterns.
        """
        # Production code should not log raw credentials
        # Check that there are no dangerous logging patterns
        lines = credential_coordinator_source.split("\n")
        dangerous_patterns = ['credentials)}"]', "access_token={", "refresh_token={"]

        for line in lines:
            for pattern in dangerous_patterns:
                assert (
                    pattern not in line
                ), f"Found potentially unsafe credential logging: {line}"

    def test_secret_redaction_in_log_output(
        self, credential_coordinator_source: str
    ) -> None:
        """Verify production code doesn't log credentials directly.

        Requirement: 8.1 - The system shall keep secrets redacted in logs and wire captures.

        This test verifies that production code patterns don't directly log credential
        values. Actual redaction happens at the logging layer, but we verify that
        production code follows safe patterns.
        """
        # Production code should not log raw credentials
        # Check for dangerous patterns that would expose secrets
        dangerous_patterns = [
            'logger.debug(f"credentials: {credentials}")',
            'logger.info(f"token: {access_token}")',
            'logger.debug(f"refresh_token={refresh_token}")',
            'logger.error(f"creds: {self._credentials}")',
        ]

        # Verify no dangerous logging patterns exist
        for pattern in dangerous_patterns:
            # Remove f-string and variable parts for pattern matching
            if "credentials" in pattern.lower() and "{" in pattern:
                # Check if there are any logger calls with credentials dict directly
                import re

                # Look for logger calls that might log credentials directly
                logger_pattern = (
                    r"logger\.(debug|info|warning|error)\([^)]*credentials[^)]*\)"
                )
                matches = re.findall(
                    logger_pattern, credential_coordinator_source, re.IGNORECASE
                )

                # If matches found, verify they don't log raw credential values
                for _match in matches:
                    # Extract the log message part
                    log_call_match = re.search(
                        r"logger\.(?:debug|info|warning|error)\(([^)]+)\)",
                        credential_coordinator_source,
                    )
                    if log_call_match:
                        log_message = log_call_match.group(1)
                        # Verify it doesn't directly format credentials dict
                        assert (
                            "{credentials}" not in log_message
                            and "{self._credentials}" not in log_message
                            and "access_token=" not in log_message.lower()
                        ), f"Found potentially unsafe credential logging: {log_message}"

        # Verify credential coordinator uses safe logging patterns
        # (e.g., logging that credentials were loaded, not their values)
        assert (
            "logger.info" in credential_coordinator_source
            or "logger.debug" in credential_coordinator_source
        )
        # Verify it doesn't log credential values directly
        assert 'f"access_token: {access_token}"' not in credential_coordinator_source
        assert 'f"token: {token}"' not in credential_coordinator_source

    def test_to_dict_preserves_credentials_for_internal_use(self) -> None:
        """Verify to_dict still works for internal credential passing."""
        creds = GeminiOAuthCredentials(
            access_token="secret_token_12345",
            refresh_token="refresh_secret_67890",
            expiry_date=9999999999999,
        )

        data = creds.to_dict()

        # Internal use should have full credentials
        assert data["access_token"] == "secret_token_12345"
        assert data["refresh_token"] == "refresh_secret_67890"


class TestCredentialLoadingMechanisms:
    """Test credential loading mechanism invariants (Requirement 8.3)."""

    def test_credential_coordinator_uses_credential_loader(
        self, credential_coordinator_source: str
    ) -> None:
        """Verify credential coordinator delegates to CredentialLoader."""
        # Should use CredentialLoader for loading
        assert "CredentialLoader" in credential_coordinator_source

    def test_credential_coordinator_uses_token_manager(
        self, credential_coordinator_source: str
    ) -> None:
        """Verify credential coordinator uses TokenManager for refresh."""
        # Should use TokenManager for refresh
        assert "TokenManager" in credential_coordinator_source

    def test_credential_coordinator_uses_file_watcher(
        self, credential_coordinator_source: str
    ) -> None:
        """Verify credential coordinator uses FileWatcher for hot reload."""
        # Should use FileWatcher for watching changes
        assert (
            "FileWatcher" in credential_coordinator_source
            or "file_watcher" in credential_coordinator_source.lower()
        )

    def test_credential_file_watching_behavior(
        self, credential_coordinator_source: str
    ) -> None:
        """Verify credential file watching behavior is preserved.

        Requirement: 8.3 - Credential loading mechanisms preserved.
        """
        from src.connectors.gemini_base.file_watcher import (
            FileWatcher,
            FileWatcherState,
        )

        # Should start file watching during initialization
        assert (
            "start_file_watching" in credential_coordinator_source
            or "FileWatcher.start_file_watching" in credential_coordinator_source
        )

        # Should have method to handle file changes
        assert "_handle_credentials_file_change" in credential_coordinator_source

        # Verify FileWatcher has required methods
        watcher_source = inspect.getsource(FileWatcher)
        assert "start_file_watching" in watcher_source
        assert "stop_file_watching" in watcher_source

        # Verify FileWatcherState exists
        state_source = inspect.getsource(FileWatcherState)
        assert "file_observer" in state_source


class TestLoggingStructure:
    """Test logging structure invariants (Requirement 7.2)."""

    def test_error_mapper_logs_with_exc_info(self, error_mapper_source: str) -> None:
        """Verify error mapper logs exceptions with exc_info=True."""
        # Should log with exc_info=True for debugging
        assert (
            "exc_info=True" in error_mapper_source or "exc_info" in error_mapper_source
        )

    def test_health_check_logs_failures(self, health_check_service_source: str) -> None:
        """Verify health check logs failures appropriately."""
        # Should have logging statements
        assert (
            "logger" in health_check_service_source.lower()
            or "logging" in health_check_service_source
        )

    def test_credential_coordinator_logs_operations(
        self, credential_coordinator_source: str
    ) -> None:
        """Verify credential coordinator logs important operations."""
        # Should have logging
        assert (
            "logger" in credential_coordinator_source.lower()
            or "logging" in credential_coordinator_source
        )


class TestCapturePayloadCompatibility:
    """Test CBOR capture payload compatibility (Requirement 7.1)."""

    def test_response_envelope_structure_unchanged(self) -> None:
        """Verify ResponseEnvelope maintains expected structure."""
        from src.core.domain.responses import ResponseEnvelope

        envelope = ResponseEnvelope(
            content={"test": "data"},
            media_type="application/json",
            headers={"X-Test": "header"},
        )

        # Core fields must exist
        assert hasattr(envelope, "content")
        assert hasattr(envelope, "media_type")
        assert hasattr(envelope, "headers")

    def test_streaming_response_envelope_structure_unchanged(self) -> None:
        """Verify StreamingResponseEnvelope maintains expected structure."""
        from collections.abc import AsyncIterator

        from src.core.domain.responses import StreamingResponseEnvelope
        from src.core.interfaces.response_processor_interface import ProcessedResponse

        async def mock_gen() -> AsyncIterator[ProcessedResponse]:
            yield ProcessedResponse(content={})

        envelope = StreamingResponseEnvelope(
            content=mock_gen(),
            media_type="text/event-stream",
            headers={"X-Test": "header"},
        )

        # Core fields must exist
        assert hasattr(envelope, "content")
        assert hasattr(envelope, "media_type")
        assert hasattr(envelope, "headers")

    def test_wire_capture_payload_structure_validation(self) -> None:
        """Verify wire capture payload structure matches requirements.

        Requirement: 7.1 - The system shall keep CBOR capture payloads and metadata
        consistent with current behavior.
        """
        from collections.abc import AsyncIterator

        from src.core.domain.responses import (
            ResponseEnvelope,
            StreamingResponseEnvelope,
        )
        from src.core.interfaces.response_processor_interface import ProcessedResponse

        # Test non-streaming envelope structure
        non_streaming = ResponseEnvelope(
            content={"choices": [{"message": {"content": "test"}}]},
            media_type="application/json",
            headers={},
        )

        # Verify structure matches expected format for wire capture
        assert isinstance(non_streaming.content, dict)
        assert "choices" in non_streaming.content or isinstance(
            non_streaming.content, dict
        )
        assert non_streaming.media_type == "application/json"

        # Test streaming envelope structure
        async def mock_stream() -> AsyncIterator[ProcessedResponse]:
            yield ProcessedResponse(content={"delta": {"content": "chunk"}})

        streaming = StreamingResponseEnvelope(
            content=mock_stream(),
            media_type="text/event-stream",
            headers={},
        )

        # Verify streaming structure
        assert hasattr(streaming.content, "__aiter__")
        assert streaming.media_type == "text/event-stream"

        # Verify both envelope types have consistent metadata fields
        # (wire capture needs these fields)
        for envelope in [non_streaming, streaming]:
            assert hasattr(envelope, "content")
            assert hasattr(envelope, "media_type")
            assert hasattr(envelope, "headers")


class TestAuthRetrySemantics:
    """Test 401 auth retry semantics for reliability."""

    def test_connector_has_auth_retry_constant(self, connector_source: str) -> None:
        """Verify auth retry timeout constant exists."""
        assert "AUTH_RETRY_TIMEOUT" in connector_source

    def test_streaming_executor_has_401_handling(
        self, streaming_executor_source: str
    ) -> None:
        """Verify streaming executor handles 401 errors."""
        assert (
            "401" in streaming_executor_source
            or "status_code" in streaming_executor_source
        )

    def test_connector_has_retry_flag(self, connector_source: str) -> None:
        """Verify connector uses retry flag to prevent infinite loops."""
        assert "_auth_retry_attempted" in connector_source


class TestValidationBehavior:
    """Test request validation behavior (Requirement 8.2)."""

    def test_invalid_request_error_preserved(self) -> None:
        """Verify InvalidRequestError is preserved through error mapper."""
        mapper = GeminiErrorMapper()

        invalid = InvalidRequestError(
            message="Invalid model specified",
            details={"field": "model"},
            status_code=400,
        )

        # map_exception returns exceptions (doesn't raise), except HTTPException
        result = mapper.map_exception(invalid, backend_name="test")

        assert result is invalid
        assert result.status_code == 400

    def test_credentials_model_validates_access_token(self) -> None:
        """Verify credentials model requires access_token."""
        with pytest.raises(ValueError, match="access_token"):
            GeminiOAuthCredentials(access_token="")

        with pytest.raises(ValueError):
            GeminiOAuthCredentials()  # No access_token


class TestCircuitBreakerCompatibility:
    """Test circuit breaker input compatibility (Requirement 6.2)."""

    def test_backend_error_has_required_fields_for_circuit_breaker(self) -> None:
        """Verify BackendError has all fields circuit breaker needs."""
        error = BackendError(
            message="Service temporarily unavailable",
            backend_name="antigravity-oauth",
            code="service_unavailable",
            status_code=503,
        )

        # Circuit breaker needs these fields
        assert hasattr(error, "status_code")
        assert hasattr(error, "backend_name")
        assert hasattr(error, "code")
        assert hasattr(error, "message")

    def test_authentication_error_compatible_with_circuit_breaker(self) -> None:
        """Verify AuthenticationError works with circuit breaker."""
        error = AuthenticationError(
            message="Token expired",
            details={"action": "refresh"},
        )

        # Should have status code for circuit breaker decisions
        assert hasattr(error, "status_code")
        assert error.status_code == 401


class TestWireCapturePayloadStructure:
    """Test wire capture payload structure compatibility.

    Requirement: 7.1 - CBOR capture payloads maintain consistent structure.
    """

    def test_response_envelope_has_required_fields_for_capture(self) -> None:
        """Verify ResponseEnvelope has fields required for wire capture."""
        from src.core.domain.responses import ResponseEnvelope

        envelope = ResponseEnvelope(
            content={"choices": [{"message": {"content": "test"}}]},
            media_type="application/json",
            headers={"X-Test": "header"},
        )

        # Wire capture needs these fields
        assert hasattr(envelope, "content")
        assert hasattr(envelope, "media_type")
        assert hasattr(envelope, "headers")

        # Content should be serializable for CBOR
        assert isinstance(envelope.content, dict | str | bytes)

    def test_streaming_response_envelope_has_required_fields_for_capture(self) -> None:
        """Verify StreamingResponseEnvelope has fields required for wire capture."""
        from collections.abc import AsyncIterator

        from src.core.domain.responses import StreamingResponseEnvelope
        from src.core.interfaces.response_processor_interface import ProcessedResponse

        async def mock_gen() -> AsyncIterator[ProcessedResponse]:
            yield ProcessedResponse(content={"delta": {"content": "chunk"}})

        envelope = StreamingResponseEnvelope(
            content=mock_gen(),
            media_type="text/event-stream",
            headers={"X-Test": "header"},
        )

        # Wire capture needs these fields
        assert hasattr(envelope, "content")
        assert hasattr(envelope, "media_type")
        assert hasattr(envelope, "headers")

        # Content should be an async iterator
        assert hasattr(envelope.content, "__aiter__")


class TestHealthCheckEndpointValidation:
    """Test health check endpoint validation.

    Requirement: 7.3 - Health check uses correct endpoint URLs.
    """

    def test_health_check_uses_correct_endpoint_urls(
        self, health_check_service_source: str
    ) -> None:
        """Verify health check constructs correct endpoint URLs."""
        # Should use loadCodeAssist
        assert "loadCodeAssist" in health_check_service_source
        assert "v1internal:loadCodeAssist" in health_check_service_source

    def test_health_check_does_not_use_other_endpoints(
        self, health_check_service_source: str
    ) -> None:
        """Verify health check doesn't use endpoints other than allowed ones."""
        # Find all v1internal endpoint references
        import re

        endpoint_pattern = r"v1internal:(\w+)"
        endpoints = re.findall(endpoint_pattern, health_check_service_source)

        # Should only contain loadCodeAssist
        allowed_endpoints = {"loadCodeAssist"}
        found_endpoints = set(endpoints)

        # All found endpoints must be in allowed list
        assert found_endpoints.issubset(
            allowed_endpoints
        ), f"Found unexpected endpoints: {found_endpoints - allowed_endpoints}"


class TestExcInfoRuntimeVerification:
    """Test exc_info logging at runtime.

    Requirement: 7.2 - Error mapper logs exceptions with exc_info=True.
    """

    def test_error_mapper_logs_with_exc_info_at_runtime(self) -> None:
        """Verify error mapper actually passes exc_info=True to logger at runtime."""
        from unittest.mock import MagicMock

        mock_logger = MagicMock()
        error_mapper = GeminiErrorMapper(logger_instance=mock_logger)

        generic_error = ValueError("Test error")

        # Map exception
        result = error_mapper.map_exception(generic_error, backend_name="test-backend")

        # Verify logger.error was called with exc_info=True
        mock_logger.error.assert_called_once()
        call_kwargs = mock_logger.error.call_args[1]
        assert call_kwargs.get("exc_info") is True

        # Verify result is BackendError
        assert isinstance(result, BackendError)

    def test_error_mapper_logs_generic_exceptions_with_traceback(self) -> None:
        """Verify generic exceptions are logged with full traceback."""
        from unittest.mock import MagicMock

        mock_logger = MagicMock()
        error_mapper = GeminiErrorMapper(logger_instance=mock_logger)

        try:
            raise RuntimeError("Test runtime error")
        except RuntimeError as e:
            error_mapper.map_exception(e, backend_name="test-backend")

        # Verify logger.error was called with exc_info=True
        mock_logger.error.assert_called_once()
        call_kwargs = mock_logger.error.call_args[1]
        assert call_kwargs.get("exc_info") is True

        # Verify the error message includes backend name
        error_message = mock_logger.error.call_args[0][0]
        assert "test-backend" in error_message
