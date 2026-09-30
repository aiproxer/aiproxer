"""Documented SIWC error normalization for the ChatGPT-plan connector."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any

from src.connectors.openai_chatgpt_plan.stream_accumulator import (
    ChatGPTPlanStreamError,
)
from src.core.common.exceptions import (
    AuthenticationError,
    BackendError,
    LLMProxyError,
    RateLimitExceededError,
    ResponsesProviderLimitationError,
    ValidationError,
)
from src.core.common.logging_utils import DEFAULT_REDACTED_FIELDS, redact_dict

SIWC_PROVIDER = "openai-chatgpt-plan"

_QUOTA_CODES = frozenset({"subscription_sharing_usage_limit_exceeded"})
_UNAVAILABLE_CODES = frozenset({"subscription_sharing_usage_unavailable"})
_CAPABILITY_CODES = frozenset(
    {
        "subscription_sharing_unsupported_capability",
        "subscription_sharing_route_not_supported",
    }
)
_NON_REFRESH_AUTH_CODES = frozenset(
    {
        "subscription_sharing_invalid_user",
        "invalid_user",
        "unauthorized",
        "invalid_api_key",
    }
)
_REFRESH_AUTH_CODES = frozenset(
    {
        "token_expired",
        "access_token_expired",
        "expired_token",
    }
)

_SECRET_FIELDS = set(DEFAULT_REDACTED_FIELDS) | {
    "access_token",
    "refresh_token",
    "id_token",
    "authorization",
    "token",
}


def _safe_details(details: Mapping[str, Any] | None) -> dict[str, Any]:
    if not details:
        return {}
    return redact_dict(dict(details), redacted_fields=_SECRET_FIELDS)


def _extract_error_mapping(payload: Any) -> dict[str, Any]:
    if not isinstance(payload, Mapping):
        return {}
    nested = payload.get("error")
    if isinstance(nested, Mapping):
        return dict(nested)
    return dict(payload)


def _extract_upstream_code(payload: Any) -> str | None:
    err = _extract_error_mapping(payload)
    for key in ("code", "type"):
        value = err.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip()
    return None


def _extract_message(payload: Any, fallback: str) -> str:
    err = _extract_error_mapping(payload)
    message = err.get("message")
    if isinstance(message, str) and message.strip():
        return message.strip()
    if isinstance(payload, str) and payload.strip():
        return payload.strip()
    return fallback


def _auth_refresh_eligible(*, status_code: int, code: str | None) -> bool:
    if code in _NON_REFRESH_AUTH_CODES:
        return False
    if code in _REFRESH_AUTH_CODES:
        return True
    return status_code == 401


class ChatGPTPlanAuthError(AuthenticationError):
    """SIWC authentication/authorization failure for a selected profile."""


class ChatGPTPlanQuotaError(RateLimitExceededError):
    """Selected-profile ChatGPT plan allowance exhausted (no profile rotation)."""


class ChatGPTPlanSubscriptionUnavailableError(BackendError):
    """Selected-profile ChatGPT plan usage temporarily unavailable."""


class ChatGPTPlanCapabilityError(ValidationError):
    """Upstream rejected the request body for SIWC capability/route limits."""


class ChatGPTPlanErrorMapper:
    """Map SIWC/public Responses failures to actionable typed AIProxer errors."""

    def map_upstream_error(
        self,
        *,
        status_code: int,
        payload: Any,
        profile_id: str | None,
    ) -> LLMProxyError:
        code = _extract_upstream_code(payload)
        message = _extract_message(payload, fallback="ChatGPT-plan upstream error")
        base_details: dict[str, Any] = {
            "profile_id": profile_id,
            "upstream_code": code,
            "status_code": status_code,
            "profile_rotation": False,
            "provider": SIWC_PROVIDER,
        }
        if code in _QUOTA_CODES:
            return ChatGPTPlanQuotaError(
                message or "ChatGPT plan allowance exhausted for the selected profile.",
                details=_safe_details(base_details),
            )
        if code in _UNAVAILABLE_CODES:
            return ChatGPTPlanSubscriptionUnavailableError(
                message or "ChatGPT plan usage is temporarily unavailable.",
                backend_name=SIWC_PROVIDER,
                details=_safe_details(base_details),
                status_code=503,
                code=code,
            )
        if code in _CAPABILITY_CODES:
            details = dict(base_details)
            details["retryable"] = False
            param = _extract_error_mapping(payload).get("param")
            if isinstance(param, str) and param:
                details["param"] = param
            return ChatGPTPlanCapabilityError(
                message or "Request uses a capability unsupported by ChatGPT-plan.",
                details=_safe_details(details),
            )
        if code in _NON_REFRESH_AUTH_CODES | _REFRESH_AUTH_CODES or status_code in {
            401,
            403,
        }:
            details = dict(base_details)
            details["refresh_eligible"] = _auth_refresh_eligible(
                status_code=status_code, code=code
            )
            return ChatGPTPlanAuthError(
                message or "ChatGPT-plan authentication failed.",
                details=_safe_details(details),
            )
        return BackendError(
            message=message,
            backend_name=SIWC_PROVIDER,
            details=_safe_details(base_details),
            status_code=status_code,
            code=code,
        )

    def map_exception(self, exc: Exception, *, profile_id: str | None) -> LLMProxyError:
        if isinstance(
            exc,
            ChatGPTPlanAuthError
            | ChatGPTPlanQuotaError
            | ChatGPTPlanSubscriptionUnavailableError
            | ChatGPTPlanCapabilityError
            | ChatGPTPlanStreamError,
        ):
            return exc
        if isinstance(exc, ResponsesProviderLimitationError):
            return ChatGPTPlanCapabilityError(
                exc.message,
                details=_safe_details(
                    {
                        "profile_id": profile_id,
                        "feature": getattr(exc, "feature", None),
                        "provider": getattr(exc, "provider", SIWC_PROVIDER),
                        "retryable": False,
                        "profile_rotation": False,
                    }
                ),
            )
        if isinstance(exc, AuthenticationError):
            # Local binding/selection failures are not refresh-eligible.
            details = getattr(exc, "details", None)
            payload = details
            has_upstream = isinstance(details, Mapping) and bool(details)
            if has_upstream:
                status_code = int(getattr(exc, "status_code", 401) or 401)
                if isinstance(details, Mapping):
                    payload = details.get("error_payload", details)
                return self.map_upstream_error(
                    status_code=status_code,
                    payload=(
                        payload if payload is not None else {"message": exc.message}
                    ),
                    profile_id=profile_id,
                )
            return ChatGPTPlanAuthError(
                exc.message,
                details=_safe_details(
                    {
                        "profile_id": profile_id,
                        "refresh_eligible": False,
                        "profile_rotation": False,
                        "provider": SIWC_PROVIDER,
                    }
                ),
            )
        if isinstance(exc, LLMProxyError):
            status_code = int(getattr(exc, "status_code", 500) or 500)
            details = getattr(exc, "details", None)
            payload = details
            if isinstance(details, Mapping):
                payload = details.get("error_payload", details)
            if status_code >= 400:
                return self.map_upstream_error(
                    status_code=status_code,
                    payload=(
                        payload if payload is not None else {"message": exc.message}
                    ),
                    profile_id=profile_id,
                )
            return exc
        return BackendError(
            message="ChatGPT-plan request failed.",
            backend_name=SIWC_PROVIDER,
            details=_safe_details({"profile_id": profile_id}),
            status_code=500,
        )

    def should_retry_same_body(self, exc: Exception) -> bool:
        if isinstance(
            exc, ResponsesProviderLimitationError | ChatGPTPlanCapabilityError
        ):
            return False
        details = getattr(exc, "details", None)
        if isinstance(details, Mapping) and details.get("retryable") is False:
            return False
        return False

    def should_refresh_and_retry(
        self, exc: Exception, *, already_retried: bool
    ) -> bool:
        if already_retried:
            return False
        if isinstance(exc, ChatGPTPlanAuthError):
            details = getattr(exc, "details", None)
            if isinstance(details, Mapping):
                return bool(details.get("refresh_eligible", False))
            return False
        return False

    def should_rotate_profile(self, exc: Exception) -> bool:
        del exc
        return False
