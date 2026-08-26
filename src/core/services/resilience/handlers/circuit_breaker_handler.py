"""Circuit breaker error handler for transient upstream failures."""

from __future__ import annotations

import asyncio

from src.core.common.exceptions import (
    APIConnectionError,
    APITimeoutError,
    AuthenticationError,
    RateLimitExceededError,
)
from src.core.interfaces.resilience_interface import (
    ActionType,
    ErrorContext,
    IErrorHandler,
    ResilienceAction,
)
from src.core.services.resilience.circuit_breaker_state import (
    CircuitBreakerStateManager,
)

_TRANSIENT_STATUS_CODES = frozenset([500, 502, 503, 504])
_NON_TRANSIENT_STATUS_CODES = frozenset([400, 401, 403, 404, 422, 429])


class CircuitBreakerErrorHandler:
    """Records transient failures in circuit breaker state before delegation."""

    def __init__(
        self,
        circuit_breaker_state: CircuitBreakerStateManager,
        next_handler: IErrorHandler | None = None,
    ) -> None:
        self._circuit_breaker_state = circuit_breaker_state
        self._next = next_handler

    def set_next(self, handler: IErrorHandler) -> IErrorHandler:
        self._next = handler
        return handler

    def can_handle(self, error: Exception) -> bool:
        if isinstance(
            error,
            RateLimitExceededError
            | AuthenticationError
            | asyncio.CancelledError
            | GeneratorExit,
        ):
            return False

        if type(error).__name__ in ("SessionCancelledError", "CancelledError"):
            return False

        status_code = _extract_status_code(error)
        if status_code is not None:
            if 400 <= status_code < 500 or status_code in _NON_TRANSIENT_STATUS_CODES:
                return False
            return status_code in _TRANSIENT_STATUS_CODES

        return bool(_is_timeout_or_transport_error(error))

    def handle(self, context: ErrorContext) -> ResilienceAction:
        recorded = False
        if self.can_handle(context.error):
            self._circuit_breaker_state.record_failure(
                context.instance_id,
                reason=_build_failure_reason(context.error),
            )
            recorded = True

        if self._next is not None:
            action = self._next.handle(context)
            if action.type != ActionType.PROCEED:
                return action
            if action.reason and not action.reason.startswith("No handler for "):
                return action

        if recorded:
            return ResilienceAction(
                type=ActionType.PROCEED,
                reason="circuit_breaker_failure_recorded",
            )

        return ResilienceAction(
            type=ActionType.PROCEED,
            reason=f"No handler for {type(context.error).__name__}",
        )


def _extract_status_code(error: Exception) -> int | None:
    status_code = getattr(error, "status_code", None)
    if isinstance(status_code, int):
        return status_code

    response = getattr(error, "response", None)
    response_code = getattr(response, "status_code", None)
    if isinstance(response_code, int):
        return response_code

    details = getattr(error, "details", None)
    if isinstance(details, dict):
        details_status = details.get("status_code")
        if isinstance(details_status, int):
            return details_status

    return None


def _is_timeout_or_transport_error(error: Exception) -> bool:
    if isinstance(error, APITimeoutError | APIConnectionError):
        return True
    if isinstance(error, TimeoutError | ConnectionError):
        return True

    try:
        import httpx
    except ImportError:
        return False

    return isinstance(error, httpx.TimeoutException | httpx.TransportError)


def _build_failure_reason(error: Exception) -> str:
    status_code = _extract_status_code(error)
    if status_code in _TRANSIENT_STATUS_CODES:
        return f"http_{status_code}"
    if _is_timeout_or_transport_error(error):
        if isinstance(error, APITimeoutError | TimeoutError):
            return "timeout"
        return "transport_error"
    return type(error).__name__.lower()
