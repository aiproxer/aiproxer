"""Unit tests for resilience error handlers."""

from unittest.mock import patch

from src.core.common.exceptions import (
    APIConnectionError,
    APITimeoutError,
    AuthenticationError,
    BackendError,
    RateLimitExceededError,
    SessionCancelledError,
)
from src.core.interfaces.resilience_interface import ActionType, ErrorContext
from src.core.services.resilience.circuit_breaker_state import (
    CircuitBreakerStateManager,
)
from src.core.services.resilience.handlers import (
    AuthErrorHandler,
    CircuitBreakerErrorHandler,
    RateLimitErrorHandler,
)
from src.core.services.resilience.rate_limit_state import (
    InstanceStatus,
    RateLimitStateManager,
)


class TestRateLimitErrorHandler:
    """Tests for RateLimitErrorHandler."""

    def test_can_handle_rate_limit_exceeded_error(self) -> None:
        """Should handle RateLimitExceededError."""
        manager = RateLimitStateManager()
        handler = RateLimitErrorHandler(manager)

        error = RateLimitExceededError("Rate limited")
        assert handler.can_handle(error) is True

    def test_can_handle_http_429(self) -> None:
        """Should handle errors with status_code 429."""
        manager = RateLimitStateManager()
        handler = RateLimitErrorHandler(manager)

        class MockError(Exception):
            status_code = 429

        assert handler.can_handle(MockError()) is True

    def test_cannot_handle_other_errors(self) -> None:
        """Should not handle non-rate-limit errors."""
        manager = RateLimitStateManager()
        handler = RateLimitErrorHandler(manager)

        assert handler.can_handle(ValueError("test")) is False
        assert handler.can_handle(AuthenticationError("test")) is False

    def test_extracts_retry_after_from_reset_at(self) -> None:
        """Should extract retry-after from reset_at timestamp."""
        manager = RateLimitStateManager()
        handler = RateLimitErrorHandler(manager)

        with patch("time.time", return_value=1000.0):
            error = RateLimitExceededError("Rate limited", reset_at=1060.0)
            context = ErrorContext(instance_id="backend.1", model="gpt-4", error=error)

            action = handler.handle(context)

        assert action.type == ActionType.COOLDOWN
        assert 59.0 <= action.duration <= 61.0

    def test_extracts_retry_after_from_details(self) -> None:
        """Should extract retry-after from details dict."""
        manager = RateLimitStateManager()
        handler = RateLimitErrorHandler(manager)

        error = RateLimitExceededError(
            "Rate limited", details={"retry_after_seconds": 120}
        )
        context = ErrorContext(instance_id="backend.1", model="gpt-4", error=error)

        action = handler.handle(context)

        assert action.type == ActionType.COOLDOWN
        assert action.duration == 120.0

    def test_extracts_retry_after_from_headers(self) -> None:
        """Should extract retry-after from headers."""
        manager = RateLimitStateManager()
        handler = RateLimitErrorHandler(manager)

        error = RateLimitExceededError(
            "Rate limited", details={"headers": {"retry-after": "300"}}
        )
        context = ErrorContext(instance_id="backend.1", model="gpt-4", error=error)

        action = handler.handle(context)

        assert action.type == ActionType.COOLDOWN
        assert action.duration == 300.0

    def test_extracts_retry_after_from_google_rpc_error(self) -> None:
        """Should extract retry-after from Google RPC error details."""
        manager = RateLimitStateManager()
        handler = RateLimitErrorHandler(manager)

        # Structure matches Google's ErrorInfo metadata
        error = RateLimitExceededError(
            "Rate limited",
            details={
                "error": {
                    "code": 429,
                    "details": [
                        {
                            "@type": "type.googleapis.com/google.rpc.ErrorInfo",
                            "metadata": {"quotaResetDelay": "4.15s"},
                        }
                    ],
                }
            },
        )
        context = ErrorContext(instance_id="backend.1", model="gemini-pro", error=error)

        action = handler.handle(context)

        assert action.type == ActionType.COOLDOWN
        # Should be parsed as 4.15s
        assert abs(action.duration - 4.15) < 0.001

    def test_default_cooldown_when_no_retry_after(self) -> None:
        """Should use default cooldown when retry-after not available."""
        manager = RateLimitStateManager()
        handler = RateLimitErrorHandler(manager, default_cooldown=45.0)

        error = RateLimitExceededError("Rate limited")
        context = ErrorContext(instance_id="backend.1", model="gpt-4", error=error)

        action = handler.handle(context)

        assert action.type == ActionType.COOLDOWN
        assert action.duration == 45.0

    def test_sets_model_cooldown_for_model_specific_limit(self) -> None:
        """Should set model cooldown for model-specific rate limits."""
        manager = RateLimitStateManager()
        handler = RateLimitErrorHandler(manager)

        error = RateLimitExceededError(
            "Rate limit exceeded for model gpt-4",
            details={"retry_after_seconds": 60},
        )
        context = ErrorContext(instance_id="backend.1", model="gpt-4", error=error)

        handler.handle(context)

        # Model should be in cooldown, but instance should be available
        assert manager.is_instance_available("backend.1") is True
        assert manager.is_model_available("backend.1", "gpt-4") is False
        assert manager.is_model_available("backend.1", "gpt-3.5") is True

    def test_sets_instance_cooldown_for_account_limit(self) -> None:
        """Should set instance cooldown for account-level rate limits."""
        manager = RateLimitStateManager()
        handler = RateLimitErrorHandler(manager)

        error = RateLimitExceededError(
            "Rate limit exceeded for your organization",
            details={"retry_after_seconds": 600},
        )
        context = ErrorContext(instance_id="backend.1", model="gpt-4", error=error)

        handler.handle(context)

        # Instance should be in cooldown (affects all models)
        assert manager.is_instance_available("backend.1") is False
        assert manager.is_model_available("backend.1", "gpt-4") is False
        assert manager.is_model_available("backend.1", "gpt-3.5") is False

    def test_detects_account_indicator_in_message(self) -> None:
        """Should detect account/org indicators in error message."""
        manager = RateLimitStateManager()
        handler = RateLimitErrorHandler(manager)

        test_cases = [
            "Your account has exceeded the rate limit",
            "Organization quota exceeded",
            "API key rate limit reached",
            "Billing limit exceeded",
        ]

        for message in test_cases:
            manager = RateLimitStateManager()  # Fresh manager for each test
            handler = RateLimitErrorHandler(manager)
            error = RateLimitExceededError(message, details={"retry_after_seconds": 60})
            context = ErrorContext(instance_id="backend.1", model="gpt-4", error=error)

            handler.handle(context)

            assert (
                manager.is_instance_available("backend.1") is False
            ), f"Expected instance-wide limit for: {message}"


class TestAuthErrorHandler:
    """Tests for AuthErrorHandler."""

    def test_can_handle_authentication_error(self) -> None:
        """Should handle AuthenticationError."""
        manager = RateLimitStateManager()
        handler = AuthErrorHandler(manager)

        error = AuthenticationError("Invalid API key")
        assert handler.can_handle(error) is True

    def test_can_handle_http_401(self) -> None:
        """Should handle errors with status_code 401."""
        manager = RateLimitStateManager()
        handler = AuthErrorHandler(manager)

        class MockError(Exception):
            status_code = 401

        assert handler.can_handle(MockError()) is True

    def test_can_handle_http_403(self) -> None:
        """Should NOT treat generic 403 as authentication failure."""
        manager = RateLimitStateManager()
        handler = AuthErrorHandler(manager)

        class MockError(Exception):
            status_code = 403

        assert handler.can_handle(MockError()) is False

    def test_cannot_handle_other_errors(self) -> None:
        """Should not handle non-auth errors."""
        manager = RateLimitStateManager()
        handler = AuthErrorHandler(manager)

        assert handler.can_handle(ValueError("test")) is False
        assert handler.can_handle(RateLimitExceededError("test")) is False

    def test_disables_instance_on_auth_error(self) -> None:
        """Should disable instance on authentication error."""
        manager = RateLimitStateManager()
        handler = AuthErrorHandler(manager)

        error = AuthenticationError("Invalid API key")
        context = ErrorContext(instance_id="backend.1", model="gpt-4", error=error)

        action = handler.handle(context)

        assert action.type == ActionType.DISABLE_INSTANCE
        assert action.permanent is True
        assert manager.get_instance_status("backend.1") == InstanceStatus.DISABLED

    def test_does_not_disable_when_instance_id_looks_oauth(self) -> None:
        """OAuth-like instance ids should not be permanently disabled even if extra context is missing."""
        manager = RateLimitStateManager()
        handler = AuthErrorHandler(manager)

        error = AuthenticationError("Token expired")
        context = ErrorContext(
            instance_id="qwen-oauth:session-123",
            model="qwen/coder-model",
            error=error,
            extra={},
        )

        action = handler.handle(context)

        assert action.type == ActionType.PROCEED
        assert (
            manager.get_instance_status("qwen-oauth:session-123")
            == InstanceStatus.ACTIVE
        )

    def test_does_not_disable_openai_codex_scoped_instance_without_extra(self) -> None:
        """Codex scoped ids include 'codex' and must not brick routing if extra is incomplete."""
        manager = RateLimitStateManager()
        handler = AuthErrorHandler(manager)

        error = AuthenticationError("HTTP 401")
        context = ErrorContext(
            instance_id="openai-codex:llm-b2bua-test",
            model="gpt-5",
            error=error,
            extra={},
        )

        action = handler.handle(context)

        assert action.type == ActionType.PROCEED
        assert (
            manager.get_instance_status("openai-codex:llm-b2bua-test")
            == InstanceStatus.ACTIVE
        )

    def test_does_not_disable_opencode_go_on_auth_error(self) -> None:
        """OpenCode Go shares one key across protocol shapes; 401 must not brick routing."""
        manager = RateLimitStateManager()
        handler = AuthErrorHandler(manager)

        error = AuthenticationError("HTTP 401")
        context = ErrorContext(
            instance_id="opencode-go",
            model="kimi-k2.5",
            error=error,
            extra={"backend_type": "opencode-go"},
        )

        action = handler.handle(context)

        assert action.type == ActionType.PROCEED
        assert manager.get_instance_status("opencode-go") == InstanceStatus.ACTIVE

    def test_skips_disable_for_unscoped_personal_backend(self) -> None:
        """Should not disable unscoped instances for personal OAuth backends."""
        manager = RateLimitStateManager()
        handler = AuthErrorHandler(manager)

        error = AuthenticationError("Invalid API key")
        context = ErrorContext(
            instance_id="backend.1",
            model="gpt-4",
            error=error,
            extra={"is_personal_backend": True},
        )

        action = handler.handle(context)

        assert action.type == ActionType.PROCEED
        assert manager.get_instance_status("backend.1") == InstanceStatus.ACTIVE

    def test_disables_scoped_personal_backend(self) -> None:
        """Should not disable scoped instances for personal OAuth backends."""
        manager = RateLimitStateManager()
        handler = AuthErrorHandler(manager)

        error = AuthenticationError("Invalid API key")
        context = ErrorContext(
            # Scoped ID typical for personal backends (backend:session_id)
            instance_id="backend.1:session-123",
            model="gpt-4",
            error=error,
            extra={"is_personal_backend": True},
        )

        action = handler.handle(context)

        assert action.type == ActionType.PROCEED
        assert (
            manager.get_instance_status("backend.1:session-123")
            == InstanceStatus.ACTIVE
        )

    def test_skips_disable_for_oauth_auto_backend(self) -> None:
        """Should not disable oauth-auto instances on auth errors."""
        manager = RateLimitStateManager()
        handler = AuthErrorHandler(manager)

        error = AuthenticationError("Account verification required")
        context = ErrorContext(
            instance_id="gemini-oauth-auto:session-123",
            model="gpt-4",
            error=error,
            extra={"backend_type": "gemini-oauth-auto"},
        )

        action = handler.handle(context)

        assert action.type == ActionType.PROCEED
        assert (
            manager.get_instance_status("gemini-oauth-auto:session-123")
            == InstanceStatus.ACTIVE
        )

    def test_builds_reason_from_error(self) -> None:
        """Should build reason from error message."""
        manager = RateLimitStateManager()
        handler = AuthErrorHandler(manager)

        error = AuthenticationError("API key expired")
        context = ErrorContext(instance_id="backend.1", model="gpt-4", error=error)

        action = handler.handle(context)

        assert "API key expired" in action.reason


class TestHandlerChaining:
    """Tests for handler chain behavior."""

    def test_chain_delegates_to_next_handler(self) -> None:
        """Should delegate to next handler if can't handle."""
        manager = RateLimitStateManager()
        auth_handler = AuthErrorHandler(manager)
        rate_limit_handler = RateLimitErrorHandler(manager, next_handler=auth_handler)

        # Auth error should be handled by auth_handler
        auth_error = AuthenticationError("Invalid key")
        context = ErrorContext(instance_id="backend.1", model="gpt-4", error=auth_error)

        action = rate_limit_handler.handle(context)

        assert action.type == ActionType.DISABLE_INSTANCE

    def test_chain_handles_own_error_type(self) -> None:
        """Should handle its own error type without delegating."""
        manager = RateLimitStateManager()
        auth_handler = AuthErrorHandler(manager)
        rate_limit_handler = RateLimitErrorHandler(manager, next_handler=auth_handler)

        # Rate limit error should be handled by rate_limit_handler
        rate_error = RateLimitExceededError(
            "Rate limited", details={"retry_after_seconds": 60}
        )
        context = ErrorContext(instance_id="backend.1", model="gpt-4", error=rate_error)

        action = rate_limit_handler.handle(context)

        assert action.type == ActionType.COOLDOWN
        assert manager.is_instance_available("backend.1") is True  # Not disabled


class TestCircuitBreakerErrorHandler:
    """Tests for CircuitBreakerErrorHandler."""

    def test_can_handle_transient_5xx_errors(self) -> None:
        manager = CircuitBreakerStateManager()
        handler = CircuitBreakerErrorHandler(manager)

        assert handler.can_handle(BackendError("Server error", status_code=500)) is True
        assert handler.can_handle(BackendError("Bad Gateway", status_code=502)) is True
        assert handler.can_handle(BackendError("Unavailable", status_code=503)) is True
        assert (
            handler.can_handle(BackendError("Gateway Timeout", status_code=504)) is True
        )

    def test_cannot_handle_client_4xx_errors(self) -> None:
        manager = CircuitBreakerStateManager()
        handler = CircuitBreakerErrorHandler(manager)

        assert handler.can_handle(BackendError("Bad Request", status_code=400)) is False
        assert handler.can_handle(BackendError("Not Found", status_code=404)) is False
        assert (
            handler.can_handle(BackendError("Unprocessable", status_code=422)) is False
        )
        assert handler.can_handle(RateLimitExceededError("Rate limit")) is False
        assert handler.can_handle(AuthenticationError("Auth failed")) is False

    def test_cannot_handle_cancellations(self) -> None:
        import asyncio
        from typing import cast

        manager = CircuitBreakerStateManager()
        handler = CircuitBreakerErrorHandler(manager)

        assert handler.can_handle(cast(Exception, asyncio.CancelledError())) is False
        assert handler.can_handle(cast(Exception, GeneratorExit())) is False
        assert handler.can_handle(SessionCancelledError(message="Cancelled")) is False

    def test_can_handle_timeout_and_connection_errors(self) -> None:
        manager = CircuitBreakerStateManager()
        handler = CircuitBreakerErrorHandler(manager)

        assert handler.can_handle(APITimeoutError("Timeout")) is True
        assert handler.can_handle(APIConnectionError("Conn error")) is True
        assert handler.can_handle(TimeoutError()) is True
        assert handler.can_handle(ConnectionError()) is True
