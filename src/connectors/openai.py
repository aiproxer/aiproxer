from __future__ import annotations

import asyncio
import contextlib
import inspect
import json
import logging
import time

logger = logging.getLogger(__name__)

from collections.abc import AsyncGenerator, Awaitable, Callable, Mapping
from dataclasses import dataclass
from json import JSONDecodeError
from typing import Any, NoReturn, cast

import httpx

from src.connectors.contracts import (
    ConnectorChatCompletionsRequest,
    ConnectorRequestContext,
    ConnectorResponsesRequest,
)
from src.connectors.contracts.wire_capture_context import (
    WIRE_CAPTURE_IS_RETRY_KEY,
    WIRE_CAPTURE_RETRY_ATTEMPT_KEY,
)
from src.core.app.constants.logging_constants import TRACE_LEVEL
from src.core.common.capture_aware_httpx import (
    CaptureAwareAsyncClient,
    HttpxBoundaryCaptureContext,
)
from src.core.common.exceptions import (
    AuthenticationError,
    BackendError,
    InvalidRequestError,
    RateLimitExceededError,
    ServiceUnavailableError,
)
from src.core.config.app_config import AppConfig
from src.core.domain.chat import CanonicalChatRequest
from src.core.domain.model_utils import RESOLVED_URI_PARAMS_EXTRA_BODY_KEY
from src.core.domain.models_listing import ModelsListingResponse
from src.core.domain.responses import (
    ResponseEnvelope,
    StreamingResponseEnvelope,
    StreamingResponseHandle,
)
from src.core.domain.responses_native_wiring import (
    RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY,
    NativeResponsesContext,
)
from src.core.domain.translation_utils.processed_response_usage import (
    usage_summary_from_processed_response,
)
from src.core.interfaces.configuration_interface import IAppIdentityConfig
from src.core.interfaces.response_processor_interface import (
    IResponseProcessor,
    ProcessedResponse,
)
from src.core.security.loop_prevention import ensure_loop_guard_header
from src.core.services.backend_registry import backend_registry
from src.core.services.translation_service import TranslationService

from .base import LLMBackend, add_vendor_prefix

# Maximum SSE buffer size to prevent DoS attacks. Reasoning-heavy models can emit
# large single ``data:`` JSON lines; a tiny cap truncates incomplete frames and
# stalls or truncates streams (see NVIDIA / NIM long-reasoning traffic).
MAX_SSE_BUFFER_SIZE = 262_144

# Internal-only keys passed via CanonicalChatRequest.extra_body so streaming uses the
# same resolved URL and headers as the canonical chat_completions path. Stripped from
# outbound JSON in _clean_openai_payload.
_LLM_PROXY_STREAM_URL_KEY = "_llm_proxy_stream_url"
_LLM_PROXY_STREAM_HEADERS_KEY = "_llm_proxy_stream_headers"
_LLM_PROXY_REQUEST_ID_KEY = "_llm_proxy_request_id"
_LLM_PROXY_SESSION_ID_KEY = "_llm_proxy_session_id"
_LLM_PROXY_CLIENT_HOST_KEY = "_llm_proxy_client_host"


def _parse_retry_after_header(headers: Any) -> int | None:
    try:
        value = headers.get("retry-after") if hasattr(headers, "get") else None
        if value is None:
            for key, v in headers.items():
                if str(key).lower() == "retry-after":
                    value = str(v).strip()
                    break
        if value is not None:
            return int(value)
    except (ValueError, TypeError, Exception):
        pass
    return None


def _error_details_from_http_response(response: httpx.Response) -> dict[str, Any]:
    """Extract provider hints from an HTTP error response (e.g. Retry-After for 429).

    Populates ``details['headers']`` in the shape expected by
    ``RateLimitErrorHandler._extract_retry_after`` so upstream rate-limit windows
    are respected instead of always falling back to the proxy default cooldown.
    """
    details: dict[str, Any] = {}
    retry_after: str | None = None
    try:
        # httpx.Headers is case-insensitive on .get; plain dict fixtures need a scan.
        hdrs = response.headers
        if hasattr(hdrs, "get"):
            got = hdrs.get("retry-after")
            if got is not None:
                retry_after = str(got).strip()
        if not retry_after:
            for key, value in hdrs.items():
                if str(key).lower() == "retry-after":
                    retry_after = str(value).strip()
                    break
    except Exception:
        return details
    if retry_after:
        details["headers"] = {"retry-after": retry_after}
    return details


def _extract_insufficient_quota_message(body: str) -> str | None:
    """Detect provider quota exhaustion responses."""

    if not body:
        return None

    normalized_body = body.strip()
    if not normalized_body:
        return None

    parsed_error: Any = None
    with contextlib.suppress(json.JSONDecodeError, ValueError, TypeError):
        parsed_error = json.loads(normalized_body)

    candidates: list[Any] = [parsed_error] if parsed_error is not None else []
    if isinstance(parsed_error, dict):
        nested_error = parsed_error.get("error")
        if nested_error is not None:
            candidates.append(nested_error)

    for candidate in candidates:
        if not isinstance(candidate, dict):
            continue

        code = candidate.get("code")
        error_type = candidate.get("type")
        message = candidate.get("message")
        normalized_code = str(code).strip().lower() if code is not None else ""
        normalized_type = (
            str(error_type).strip().lower() if error_type is not None else ""
        )
        normalized_message = (
            str(message).strip().lower() if isinstance(message, str) else ""
        )

        if (
            normalized_code == "insufficient_quota"
            or normalized_type == "insufficient_quota"
        ):
            return (
                str(message).strip()
                if isinstance(message, str) and message.strip()
                else normalized_body
            )

        if (
            "exceeded your current quota" in normalized_message
            or "token-limit" in normalized_message
            or "quota" in normalized_message
            and "exceeded" in normalized_message
        ):
            return (
                str(message).strip()
                if isinstance(message, str) and message.strip()
                else normalized_body
            )

    if (
        "insufficient_quota" in normalized_body.lower()
        or "exceeded your current quota" in normalized_body.lower()
    ):
        return normalized_body

    return None


def _build_quota_exhaustion_stream_chunk(
    *, body: str, error_details: dict[str, Any], model: str
) -> bytes:
    """Build a terminal OpenAI-compatible stream chunk for quota exhaustion."""

    import time

    message = _extract_insufficient_quota_message(body) or (
        "Upstream quota was exhausted."
    )
    error_payload: dict[str, Any] = {
        "id": f"chatcmpl-error-{int(time.time())}",
        "object": "chat.completion.chunk",
        "created": int(time.time()),
        "model": model,
        "choices": [{"index": 0, "delta": {}, "finish_reason": "error"}],
        "error": cast(
            dict[str, Any],
            {
                "message": message,
                "type": "quota_exceeded",
                "code": 503,
                "status_code": 503,
            },
        ),
    }
    error_body = error_payload["error"]
    if error_details and isinstance(error_body, dict):
        error_body["details"] = error_details
    return f"data: {json.dumps(error_payload, ensure_ascii=True)}\n\n".encode()


def _attach_http_error_details(
    error_detail: dict[str, Any] | str, response: httpx.Response
) -> dict[str, Any] | str:
    """Attach provider response metadata to error detail when available."""

    response_details = _error_details_from_http_response(response)
    if not response_details:
        return error_detail

    if isinstance(error_detail, dict):
        merged_detail = dict(error_detail)
        headers = response_details.get("headers")
        if isinstance(headers, dict):
            existing_headers = merged_detail.get("headers")
            if isinstance(existing_headers, dict):
                merged_detail["headers"] = {**existing_headers, **headers}
            else:
                merged_detail["headers"] = dict(headers)
        return merged_detail

    return {"message": str(error_detail), **response_details}


def _message_from_merged_detail(merged: dict[str, Any]) -> str:
    """Best-effort human message for LLMProxyError subclasses."""
    msg = merged.get("message")
    if isinstance(msg, str) and msg.strip():
        return msg.strip()
    err = merged.get("error")
    if isinstance(err, dict):
        nested = err.get("message")
        if isinstance(nested, str) and nested.strip():
            return nested.strip()
    detail = merged.get("detail")
    if isinstance(detail, str) and detail.strip():
        return detail.strip()
    return str(merged.get("message", merged))


def _is_quota_exceeded_detail(merged: dict[str, Any]) -> bool:
    if merged.get("type") == "quota_exceeded":
        return True
    err = merged.get("error")
    if isinstance(err, dict) and err.get("type") == "quota_exceeded":  # noqa: SIM103
        return True
    return False


def _raise_upstream_http_error(
    *,
    status_code: int,
    error_detail: dict[str, Any] | str,
    response: httpx.Response,
    url: str,
) -> NoReturn:
    """Map upstream HTTP failures to :class:`LLMProxyError` (never framework HTTP types)."""

    if isinstance(error_detail, dict):
        wrapped: dict[str, Any] = dict(error_detail)
    else:
        wrapped = {
            "message": str(error_detail),
            "type": ("openrouter_error" if "openrouter" in url else "openai_error"),
            "code": status_code,
        }

    merged_any = _attach_http_error_details(wrapped, response)
    merged: dict[str, Any]
    if isinstance(merged_any, dict):
        merged = merged_any
    else:
        merged = {
            "message": str(merged_any),
            "type": ("openrouter_error" if "openrouter" in url else "openai_error"),
            "code": status_code,
        }

    message = _message_from_merged_detail(merged)

    if status_code == 429:
        retry_raw: str | None = None
        hdrs = merged.get("headers")
        if isinstance(hdrs, dict):
            retry_raw = hdrs.get("retry-after") or hdrs.get("Retry-After")
            if retry_raw is not None:
                retry_raw = str(retry_raw).strip()
        reset_at: float | None = None
        if retry_raw:
            with contextlib.suppress(ValueError, TypeError):
                reset_at = time.time() + float(retry_raw)
        details = dict(merged)
        raise RateLimitExceededError(
            message=message,
            details=details,
            reset_at=int(reset_at) if reset_at is not None else None,
        )

    if status_code == 503 and _is_quota_exceeded_detail(merged):
        raise BackendError(
            message=message, backend_name="openai", status_code=503, details=merged
        )

    if status_code == 401:
        raise AuthenticationError(message=message, details=merged)

    if 400 <= status_code < 500:
        raise InvalidRequestError(
            message=message, details=merged, status_code=status_code
        )

    raise BackendError(
        message=message, backend_name="openai", status_code=status_code, details=merged
    )


@dataclass(frozen=True, slots=True)
class ConnectorChatInvocationContext:
    """Fields extracted from :class:`ConnectorChatCompletionsRequest` or duck-typed equivalents."""

    domain_request: Any
    processed_messages: list[Any]
    effective_model: str
    identity: IAppIdentityConfig | None
    cancellation_coordinator: Any
    cancellation_token: Any
    context: ConnectorRequestContext | None
    options: dict[str, Any]


def _extract_connector_chat_request(
    request: ConnectorChatCompletionsRequest | Any,
) -> ConnectorChatInvocationContext:
    """Extract connector fields; supports Pydantic models and SimpleNamespace-like wrappers."""

    domain_request = getattr(request, "request", None)
    if domain_request is None:
        raise TypeError(
            "Connector chat completions request missing required 'request' field."
        )

    processed_messages_source = getattr(request, "processed_messages", None)
    processed_messages = (
        list(processed_messages_source) if processed_messages_source is not None else []
    )
    effective_model = str(getattr(request, "effective_model", "") or "")
    identity = getattr(request, "identity", None)
    cancellation_coordinator = getattr(request, "cancellation_coordinator", None)
    cancellation_token = getattr(request, "cancellation_token", None)
    context = getattr(request, "context", None)

    options_obj = getattr(request, "options", None)
    options = options_obj if isinstance(options_obj, dict) else {}

    return ConnectorChatInvocationContext(
        domain_request=domain_request,
        processed_messages=processed_messages,
        effective_model=effective_model,
        identity=identity,
        cancellation_coordinator=cancellation_coordinator,
        cancellation_token=cancellation_token,
        context=context,
        options=options,
    )


def _raise_for_httpx_request_error(
    exc: httpx.RequestError, *, url: str, log_extra: dict[str, str] | None
) -> NoReturn:
    """Map httpx transport errors to domain errors (read timeout vs connect vs other)."""

    if isinstance(exc, httpx.ReadTimeout):
        if logger.isEnabledFor(logging.WARNING):
            logger.warning(
                "Upstream read timeout waiting for response headers or body: %s",
                url,
                extra=log_extra,
            )
        raise BackendError(
            message=(
                "Upstream timed out before sending a complete response. "
                "Increase the HTTP client read timeout or use streaming for long generations."
            ),
            details={"url": url, "reason": "read_timeout"},
            status_code=504,
        ) from exc

    if isinstance(exc, httpx.ConnectTimeout):
        if logger.isEnabledFor(logging.WARNING):
            logger.warning("Connect timeout to %s: %s", url, exc, extra=log_extra)
        raise ServiceUnavailableError(
            message=f"Could not connect to backend (connect timeout: {exc!s})",
            details={"url": url, "reason": "connect_timeout"},
        ) from exc

    if isinstance(exc, httpx.WriteTimeout):
        if logger.isEnabledFor(logging.WARNING):
            logger.warning("Write timeout to %s: %s", url, exc, extra=log_extra)
        raise BackendError(
            message="Request body upload timed out.",
            details={"url": url, "reason": "write_timeout"},
            status_code=504,
        ) from exc

    if isinstance(exc, httpx.ReadError):
        if logger.isEnabledFor(logging.WARNING):
            logger.warning(
                "Upstream read error (connection lost mid-stream): %s: %s",
                url,
                exc,
                extra=log_extra,
            )
        raise BackendError(
            message=f"Upstream read error: connection lost during read ({exc!s})",
            details={"url": url, "reason": "read_error"},
            status_code=502,
        ) from exc

    if isinstance(exc, httpx.RemoteProtocolError):
        if logger.isEnabledFor(logging.WARNING):
            logger.warning(
                "Upstream protocol error (remote disconnect): %s: %s",
                url,
                exc,
                extra=log_extra,
            )
        raise BackendError(
            message=f"Upstream protocol error: remote server disconnected ({exc!s})",
            details={"url": url, "reason": "remote_protocol_error"},
            status_code=502,
        ) from exc

    logger.error(
        "Request failed to %s: %s",
        url,
        exc,
        exc_info=True,
        extra=log_extra if log_extra else None,
    )
    raise ServiceUnavailableError(
        message=f"Could not connect to backend ({exc!s})", details={"url": url}
    ) from exc


def _is_retryable_http2_stream_termination(exc: httpx.RequestError) -> bool:
    if not isinstance(exc, httpx.RemoteProtocolError):
        return False
    message = str(exc)
    lowered = message.lower()
    if "ConnectionTerminated" in message and "ErrorCodes.NO_ERROR" in message:
        return True
    return "server disconnected" in lowered


class OpenAIConnector(LLMBackend):
    """Minimal OpenAI-compatible connector used by OpenRouterBackend in tests.

    It supports an optional `headers_override` kwarg and treats streaming
    responses that expose `aiter_bytes()` as streamable even if returned by
    test doubles.

    Implements StreamProducer protocol for streaming pipeline integration.
    """

    backend_type: str = "openai"

    # Vendor prefix for model names in unified model routing.
    # Subclasses should override this to use their vendor name.
    # Set to None for multi-vendor backends (like OpenRouter) that receive
    # models already prefixed from upstream.
    VENDOR_PREFIX: str | None = "openai"

    def __init__(
        self,
        client: httpx.AsyncClient,
        config: AppConfig,
        translation_service: TranslationService | None = None,
        response_processor: IResponseProcessor | None = None,
    ) -> None:
        super().__init__(config, response_processor)
        self.client = client
        self._capture_http_client = CaptureAwareAsyncClient(client)
        # Allow callers/tests to omit TranslationService; resolve through DI for consistency
        self.translation_service = (
            translation_service
            if translation_service is not None
            else self._resolve_translation_service()
        )
        self.config = config  # Stored config
        self.available_models: list[str] = []
        self.api_key: str | None = None
        self._api_base_url: str = "https://api.openai.com/v1"

        # WebSocket client for Responses API (lazy initialized)
        self._websocket_client: Any = None  # OpenAIWebSocketClient | None
        self._use_websocket: bool = False

        # Health check attributes
        self._health_checked: bool = False
        import os

        disable_health_checks_env = os.getenv(
            "DISABLE_HEALTH_CHECKS", "false"
        ).lower() in ("true", "1", "yes")

        disable_health_checks_config = bool(
            getattr(self.config, "disable_health_checks", False)
        )

        # Enable health checks only when neither config nor env disable them
        self._health_check_enabled = not (
            disable_health_checks_env or disable_health_checks_config
        )

    @staticmethod
    def _bump_wire_capture_http_transport_resend(
        capture: HttpxBoundaryCaptureContext,
    ) -> None:
        """Increment CBOR ``retry_attempt`` for a second physical send (HTTP/2 resend)."""
        ctx = capture.context
        if ctx is None:
            return
        cur = ctx.extensions.get(WIRE_CAPTURE_RETRY_ATTEMPT_KEY)
        base = int(cur) if isinstance(cur, int) else 0
        ctx.extensions[WIRE_CAPTURE_RETRY_ATTEMPT_KEY] = base + 1
        ctx.extensions[WIRE_CAPTURE_IS_RETRY_KEY] = True

    async def _send_request_with_retry(
        self,
        *,
        build_request: Callable[[], httpx.Request],
        stream: bool,
        capture: HttpxBoundaryCaptureContext,
        url: str,
        log_extra: dict[str, str] | None,
    ) -> httpx.Response:
        request = build_request()
        try:
            return await self._capture_http_client.send(
                request, stream=stream, capture=capture
            )
        except httpx.RequestError as exc:
            if _is_retryable_http2_stream_termination(exc):
                logger.warning(
                    "Transient upstream HTTP/2 termination for %s; retrying once",
                    url,
                    extra=log_extra if log_extra else None,
                )
                retry_request = build_request()
                self._bump_wire_capture_http_transport_resend(capture)
                try:
                    return await self._capture_http_client.send(
                        retry_request, stream=stream, capture=capture
                    )
                except httpx.RequestError as retry_exc:
                    _raise_for_httpx_request_error(
                        retry_exc, url=url, log_extra=log_extra
                    )
            _raise_for_httpx_request_error(exc, url=url, log_extra=log_extra)

    @property
    def api_base_url(self) -> str:
        """Return the API base URL."""
        return self._api_base_url

    @api_base_url.setter
    def api_base_url(self, value: str) -> None:
        """Set the API base URL."""
        self._api_base_url = value

    def enable_websocket(self, enabled: bool = True) -> None:
        """Enable or disable WebSocket transport for Responses API.

        Args:
            enabled: Whether to use WebSocket transport (default: True)
        """
        self._use_websocket = enabled
        if logger.isEnabledFor(logging.INFO):
            logger.info(
                "WebSocket transport %s for OpenAI Responses API",
                "enabled" if enabled else "disabled",
            )

    async def close(self) -> None:
        """Clean up resources including WebSocket connections."""
        if self._websocket_client is not None:
            try:
                await self._websocket_client.disconnect()
            except Exception as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Error closing WebSocket client: %s", e, exc_info=True
                    )
            finally:
                self._websocket_client = None

    @staticmethod
    def _resolve_translation_service() -> TranslationService:
        """Resolve TranslationService from the DI container."""

        from src.core.di.services import (
            get_or_build_service_provider,
            get_service_collection,
            register_core_services,
            set_service_provider,
        )

        provider = get_or_build_service_provider()
        service = provider.get_service(TranslationService)
        if service is None:
            # Rebuild provider to ensure TranslationService registration in isolated contexts
            services = get_service_collection()
            register_core_services(services)
            provider = services.build_service_provider()
            set_service_provider(provider)
            service = provider.get_required_service(TranslationService)
        return service

    def get_headers(self, identity: IAppIdentityConfig | None = None) -> dict[str, str]:
        """Return request headers including API key and optional request identity."""

        headers: dict[str, str] = {}

        if self.api_key:
            headers["Authorization"] = f"Bearer {self.api_key}"

        if identity is not None:
            try:
                identity_headers = identity.get_resolved_headers(None)
            except (KeyError, TypeError, AttributeError) as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Failed to get identity headers, using empty headers: %s",
                        e,
                        exc_info=True,
                    )
                identity_headers = {}
            else:
                identity_headers = dict(identity_headers)
            if identity_headers:
                headers.update(identity_headers)

        return self._apply_loop_guard_to_outbound_headers(headers)

    def _apply_loop_guard_to_outbound_headers(
        self, headers: Mapping[str, str] | None
    ) -> dict[str, str]:
        """Attach loop-guard for upstream calls. Subclasses may omit it for strict gateways."""
        return ensure_loop_guard_header(headers)

    async def initialize(self, **kwargs: Any) -> None:
        self.api_key = kwargs.get("api_key")
        if logger.isEnabledFor(logging.INFO):
            logger.info(
                "OpenAIConnector initialize called. api_key_provided=%s",
                "yes" if self.api_key else "no",
            )
        if "api_base_url" in kwargs:
            self.api_base_url = kwargs["api_base_url"]

        # Proceed to fetch models only when we have credentials; failures are non-fatal
        if not self.api_key:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Skipping OpenAI model listing during init; no API key configured"
                )
        else:
            try:
                headers = self.get_headers()
                response = await self.client.get(
                    f"{self.api_base_url}/models", headers=headers
                )
                self.update_quota_headers(response.headers)
                data = self._decode_json_payload(response)
                if isinstance(data, dict):
                    self.available_models = [
                        model["id"]
                        for model in data.get("data", [])
                        if isinstance(model, Mapping) and "id" in model
                    ]
                else:
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Unexpected models payload type from OpenAI: %s",
                            type(data).__name__,
                        )
                    self.available_models = []
            except Exception as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning("Failed to fetch models: %s", e, exc_info=True)
                # Log the error but don't fail initialization

    async def _perform_health_check(self) -> bool:
        """Perform a health check by testing API connectivity.

        This method tests actual API connectivity by making a simple request to verify
        the API key works and the service is accessible.

        Returns:
            bool: True if health check passes, False otherwise
        """
        try:
            # Test API connectivity with a simple models endpoint request
            if not self.api_key:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning("Health check failed - no API key available")
                return False

            headers = self.get_headers()
            if not headers.get("Authorization"):
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning("Health check failed - no authorization header")
                return False

            url = f"{self.api_base_url}/models"
            response = await self.client.get(url, headers=headers)

            if response.status_code == 200:
                if logger.isEnabledFor(logging.INFO):
                    logger.info("Health check passed - API connectivity verified")
                self._health_checked = True
                return True
            else:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        f"Health check failed - API returned status {response.status_code}"
                    )
                return False

        except httpx.HTTPError as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning("Health check failed - transport error: %s", e)
            return False
        except Exception as e:
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Health check failed - unexpected error: %s", e, exc_info=True
                )
            return False

    async def _ensure_healthy(self) -> None:
        """Ensure the backend is healthy before use.

        This method performs health checks on first use, similar to how
        models are loaded lazily in the parent class.
        """
        # Some wrapper connectors call OpenAIConnector canonical methods without
        # inheriting (or initializing) the health-check feature flags. Treat
        # missing flags as "disabled" to preserve backward compatibility.
        if not getattr(self, "_health_check_enabled", False):
            # Health check is disabled, skip
            return

        if not hasattr(self, "_health_checked") or not self._health_checked:
            logger.info(
                f"Performing first-use health check for {self.backend_type} backend"
            )

            healthy = await self._perform_health_check()
            if not healthy:
                logger.warning(
                    "Health check did not pass; continuing with lazy verification on first request"
                )
            else:
                logger.info("Health check passed - backend is ready for use")

            self._health_checked = True

    def enable_health_check(self) -> None:
        """Enable health check functionality for this connector instance."""
        self._health_check_enabled = True
        self._health_checked = False  # Reset so it will check on next use
        logger.info("Health check enabled for %s backend", self.backend_type)

    def disable_health_check(self) -> None:
        """Disable health check functionality for this connector instance."""
        self._health_check_enabled = False
        logger.info("Health check disabled for %s backend", self.backend_type)

    _XSSI_PREFIXES = (")]}',\n", ")]}',", ")]}'", "while(1);", "while (1);")

    def _decode_json_payload(self, response: httpx.Response) -> Any:
        """Safely decode JSON payloads that may include XSSI guards or trailing data."""
        try:
            return response.json()
        except JSONDecodeError:
            text = response.text or ""
            sanitized = self._strip_xssi_prefix(text)
            if sanitized != text:
                try:
                    return json.loads(sanitized)
                except JSONDecodeError:
                    pass
            candidate = self._extract_first_json_value(sanitized)
            if candidate:
                try:
                    return json.loads(candidate)
                except JSONDecodeError:
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Failed to decode sanitized JSON payload; candidate snippet=%s",
                            candidate[:200],
                        )
            logger.warning(
                "Unable to decode JSON payload from OpenAI response (status=%s, preview=%r)",
                getattr(response, "status_code", "unknown"),
                (sanitized or text)[:200],
                exc_info=True,
            )
            return None

    def _strip_xssi_prefix(self, payload: str) -> str:
        stripped = payload.lstrip()
        for prefix in self._XSSI_PREFIXES:
            if stripped.startswith(prefix):
                return stripped[len(prefix) :]
        return stripped

    def _extract_first_json_value(self, payload: str) -> str | None:
        candidate = payload.strip()
        if not candidate:
            return None

        opening = candidate[0]
        if opening not in ("{", "["):
            # Attempt to locate the first JSON object within the payload
            for idx, ch in enumerate(candidate):
                if ch in ("{", "["):
                    candidate = candidate[idx:]
                    opening = candidate[0]
                    break
            else:
                return None

        stack = []
        in_string = False
        escape = False
        for idx, ch in enumerate(candidate):
            if in_string:
                if escape:
                    escape = False
                elif ch == "\\":
                    escape = True
                elif ch == '"':
                    in_string = False
                continue

            if ch == '"':
                in_string = True
                continue

            if ch in ("{", "["):
                stack.append("]" if ch == "[" else "}")
            elif ch in ("}", "]"):
                if not stack or stack.pop() != ch:
                    return None
                if not stack:
                    return candidate[: idx + 1]

        return None

    def _get_log_extra(self, context: ConnectorRequestContext | None) -> dict[str, str]:
        """Extract correlation identifiers from context for logging.

        Args:
            context: Connector request context, may be None

        Returns:
            Dictionary with request_id, session_id, client_host if available
        """
        log_extra: dict[str, str] = {}
        if context:
            if context.request_id:
                log_extra["request_id"] = context.request_id
            if context.session_id:
                log_extra["session_id"] = context.session_id
            if context.client_host:
                log_extra["client_host"] = context.client_host
        return log_extra

    def _http_boundary_capture(
        self,
        *,
        model: str,
        context: ConnectorRequestContext | None,
        key_name: str | None = None,
    ) -> HttpxBoundaryCaptureContext:
        return HttpxBoundaryCaptureContext(
            backend=self.backend_type,
            model=model,
            key_name=self.backend_type if key_name is None else key_name,
            context=context,
        )

    async def _chat_completions_canonical(
        self, request: ConnectorChatCompletionsRequest
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        """Canonical connector API implementation.

        Extracts fields from ConnectorChatCompletionsRequest and delegates
        to the existing implementation logic.

        Uses request.context for logging correlation identifiers (request_id,
        session_id, client_host) when available.
        """
        # Some OAuth wrapper connectors still pass SimpleNamespace-like request
        # objects that omit optional canonical fields (identity/context/options).
        ctx = _extract_connector_chat_request(request)
        domain_request = ctx.domain_request
        processed_messages = ctx.processed_messages
        effective_model = ctx.effective_model
        identity = ctx.identity
        cancellation_coordinator = ctx.cancellation_coordinator
        cancellation_token = ctx.cancellation_token
        context = ctx.context
        options = ctx.options

        # Structural enforcement: check cancellation immediately if coordinator and token provided
        if cancellation_coordinator is not None and cancellation_token is not None:
            cancellation_coordinator.ensure_not_cancelled(cancellation_token)

        extra_body_for_responses = getattr(domain_request, "extra_body", None) or {}
        native_for_responses = extra_body_for_responses.get(
            RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY
        )
        if isinstance(native_for_responses, dict):
            return await self.responses(
                ConnectorResponsesRequest.from_chat_completions(request)
            )

        # Prepare context for logging correlation
        log_extra = self._get_log_extra(context)
        openai_url = options.get("openai_url")
        if not isinstance(openai_url, str):
            openai_url = None

        headers_override = options.get("headers_override")
        if isinstance(headers_override, dict):
            headers_override = {str(k): str(v) for k, v in headers_override.items()}
        else:
            headers_override = None

        # Perform health check if enabled (for subclasses that support it)
        await self._ensure_healthy()

        # request.request is a CanonicalChatRequest from ConnectorChatCompletionsRequest.

        # Prepare the payload using a helper so subclasses and tests can
        # override or patch payload construction logic easily.
        payload = await self._prepare_payload(
            domain_request, processed_messages, effective_model, context
        )
        headers: dict[str, str] | None = None

        base_headers: dict[str, str] | None
        try:
            base_headers = self.get_headers(identity=identity)
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to get base headers for chat_completions request: %s",
                    e,
                    exc_info=True,
                    extra=log_extra if log_extra else None,
                )
            base_headers = None

        if headers_override is not None:
            # Provider auth from base_headers must win on conflicts (e.g. a mistaken
            # Authorization value in headers_override must not replace the backend key).
            merged: dict[str, str] = {
                str(k): str(v) for k, v in headers_override.items()
            }
            if base_headers:
                merged = {**merged, **base_headers}
            headers = merged
        else:
            headers = base_headers

        api_base = openai_url or self.api_base_url
        url = f"{api_base.rstrip('/')}/chat/completions"

        if domain_request.stream:
            # Use the new streaming pipeline orchestrator
            # This integrates: Backend → Normalizer → Processors → Assembler
            stream_extra = dict(domain_request.extra_body or {})
            stream_extra[_LLM_PROXY_STREAM_URL_KEY] = url
            if headers:
                stream_extra[_LLM_PROXY_STREAM_HEADERS_KEY] = dict(headers)
            # Propagate correlation identifiers so stream_completion's raw request
            # path (an opaque StreamProducer protocol) can attribute boundary
            # captures (PROXY_TO_BACKEND) with the real request/session ids
            # instead of falling back to the session id as request id.
            if context:
                if context.request_id:
                    stream_extra[_LLM_PROXY_REQUEST_ID_KEY] = context.request_id
                if context.session_id:
                    stream_extra[_LLM_PROXY_SESSION_ID_KEY] = context.session_id
                if context.client_host:
                    stream_extra[_LLM_PROXY_CLIENT_HOST_KEY] = context.client_host
            streaming_domain_request = domain_request.model_copy(
                update={"extra_body": stream_extra, "model": effective_model}
            )
            # Get raw stream from backend via StreamProducer protocol
            raw_stream = self.stream_completion(streaming_domain_request)

            # Calculate prompt tokens for usage tracking
            prompt_tokens = 0
            try:
                from src.core.utils.token_count import count_tokens, extract_prompt_text

                prompt_text = extract_prompt_text(processed_messages)
                prompt_tokens = count_tokens(prompt_text, model=effective_model)
            except (ImportError, AttributeError, TypeError, KeyError, ValueError):
                logger.warning(
                    "Failed to calculate prompt tokens",
                    exc_info=True,
                    extra=log_extra if log_extra else None,
                )

            # Integrate with streaming pipeline
            from src.core.ports.streaming_integration import (
                integrate_streaming_pipeline,
            )

            return await integrate_streaming_pipeline(
                raw_stream=raw_stream,
                provider=self.get_provider_name(),
                stream_id=domain_request.session_id,
                enable_tool_call_repair=True,
                enable_think_tags=True,
                prompt_tokens=prompt_tokens,
                model_name=effective_model,
                vtc_enabled=getattr(domain_request, "vtc_enabled", False) or False,
                yield_interval=self.config.streaming_yield_interval,
                headers=headers,
                domain_request=domain_request,
            )
        else:
            # Return a domain ResponseEnvelope for non-streaming
            return await self._handle_non_streaming_response(
                url, payload, headers, domain_request.session_id or "", context
            )

    async def chat_completions(  # type: ignore[override]
        self, request: ConnectorChatCompletionsRequest
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        """Invoke OpenAI chat completions using ``ConnectorChatCompletionsRequest`` only.

        Implements :class:`ICanonicalChatCompletionsBackend`. The proxy invokes backends
        through :class:`ConnectorChatCompletionsRequest`; legacy positional call shapes
        are not supported at this boundary.
        """
        return await self._chat_completions_canonical(request)

    async def _prepare_payload(
        self,
        request_data: CanonicalChatRequest,
        processed_messages: list[Any],
        effective_model: str,
        context: ConnectorRequestContext | None = None,
    ) -> dict[str, Any]:
        """
        Default payload preparation for OpenAI-compatible backends.

        Subclasses or tests may patch/override this method to customize the
        final payload sent to the provider.
        """
        # request_data is expected to be a CanonicalChatRequest already
        # (the caller creates it via TranslationService.to_domain_request).
        payload = self.translation_service.from_domain_request(request_data, "openai")
        if inspect.isawaitable(payload):
            payload = await payload
        # Ensure the outbound payload uses the resolved model name without backend prefixes.
        if effective_model:
            payload["model"] = effective_model
        payload["stream"] = bool(getattr(request_data, "stream", False))
        if payload.get("stream"):
            stream_options = payload.get("stream_options") or {}
            # Explicitly request usage data in streaming responses when supported
            stream_options.setdefault("include_usage", True)
            payload["stream_options"] = stream_options

        # Prefer processed_messages (these are the canonical, post-processed
        # messages ready to send). Convert them to plain dicts to ensure JSON
        # serializability without mutating the original Pydantic models.
        if processed_messages:
            try:
                normalized_messages: list[dict[str, Any]] = []

                def _get_value(message: Any, key: str) -> Any:
                    if isinstance(message, Mapping):
                        return message.get(key)
                    return getattr(message, key, None)

                def _normalize_content(value: Any) -> Any:
                    if isinstance(value, list | tuple):
                        normalized_parts: list[Any] = []
                        for part in value:
                            if hasattr(part, "model_dump") and callable(
                                part.model_dump
                            ):
                                normalized_parts.append(
                                    part.model_dump(exclude_none=True)
                                )
                            elif isinstance(part, Mapping):
                                normalized_parts.append(dict(part))
                            else:
                                normalized_parts.append(part)
                        return normalized_parts
                    return value

                for message in processed_messages:
                    # ChatMessage.to_dict() is the provider-boundary serializer. In
                    # particular, it excludes proxy-only metadata such as the
                    # non-forwardable interleaved-thinking marker.
                    to_dict = getattr(message, "to_dict", None)
                    if callable(to_dict):
                        dumped = to_dict()
                        if isinstance(dumped, dict):
                            normalized_messages.append(dumped)
                        continue
                    if hasattr(message, "model_dump") and callable(message.model_dump):
                        dumped = message.model_dump(exclude_none=True)
                        if isinstance(dumped, dict):
                            normalized_messages.append(dumped)
                        continue

                    msg: dict[str, Any]
                    if isinstance(message, Mapping):
                        msg = dict(message)
                    else:
                        msg = {}

                    role = _get_value(message, "role") or msg.get("role") or "user"
                    msg["role"] = role

                    content = _get_value(message, "content")
                    if content is None and "content" in msg:
                        content = msg["content"]
                    msg["content"] = _normalize_content(content)

                    reasoning_content = _get_value(message, "reasoning_content")
                    if reasoning_content is not None:
                        msg["reasoning"] = reasoning_content

                    name = _get_value(message, "name")
                    if name is not None:
                        msg["name"] = name

                    tool_calls = _get_value(message, "tool_calls")
                    if tool_calls is None and isinstance(message, Mapping):
                        tool_calls = msg.get("tool_calls")
                    if tool_calls:
                        normalized_tool_calls: list[Any] = []
                        for tool_call in tool_calls:
                            if hasattr(tool_call, "model_dump") and callable(
                                tool_call.model_dump
                            ):
                                normalized_tool_calls.append(
                                    tool_call.model_dump(exclude_none=True)
                                )
                            elif isinstance(tool_call, Mapping):
                                normalized_tool_calls.append(dict(tool_call))
                            else:
                                normalized_tool_calls.append(tool_call)
                        msg["tool_calls"] = normalized_tool_calls

                    tool_call_id = _get_value(message, "tool_call_id")
                    if tool_call_id is not None:
                        msg["tool_call_id"] = tool_call_id

                    msg = {k: v for k, v in msg.items() if v is not None}
                    normalized_messages.append(msg)

                payload["messages"] = normalized_messages
            except (KeyError, TypeError, AttributeError):
                # Fallback - leave whatever the converter produced
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Message normalization failed, using converter output as fallback",
                        exc_info=True,
                    )

        # The caller may supply an "effective_model" which should override
        # the model value coming from the domain request. Many tests expect
        # the provider payload to use the effective_model.
        if effective_model:
            current_model = payload.get("model")
            if current_model != effective_model:
                log_extra_payload = self._get_log_extra(context) if context else None
                logger.debug(
                    "Overriding model in payload from '%s' to '%s'",
                    current_model,
                    effective_model,
                    extra=log_extra_payload if log_extra_payload else None,
                )
                if effective_model:
                    payload["model"] = effective_model

        # Convert reasoning_effort to reasoning: {'effort': ...} format for OpenAI/OpenRouter
        reasoning_effort = getattr(request_data, "reasoning_effort", None)
        if reasoning_effort is not None:
            # OpenAI/OpenRouter expects reasoning as a nested object with effort field
            payload["reasoning"] = {"effort": reasoning_effort}

        verbosity = getattr(request_data, "verbosity", None)
        if isinstance(verbosity, str) and verbosity.strip():
            payload["verbosity"] = verbosity.strip()

        # Allow request.extra_body to override or augment the final payload.
        extra = getattr(request_data, "extra_body", None)
        if isinstance(extra, dict):
            payload.update(extra)
        self._sanitize_deepseek_thinking_continuation_payload(
            payload, effective_model, context
        )
        self._enforce_reasoning_model_min_tokens(payload, effective_model, context)
        # Remove internal-only keys and any None-valued entries (recursively)
        # so providers receive a clean OpenAI-compatible payload.
        payload = self._clean_openai_payload(payload)

        return payload  # type: ignore[no-any-return]

    @staticmethod
    def _normalize_model_for_token_floor(model_name: Any) -> str:
        if not isinstance(model_name, str):
            return ""

        normalized = model_name.strip().lower()
        colon_idx = normalized.find(":")
        slash_idx = normalized.find("/")
        if colon_idx != -1 and (slash_idx == -1 or colon_idx < slash_idx):
            normalized = normalized[colon_idx + 1 :]
        return normalized

    @staticmethod
    def _coerce_positive_int(value: Any) -> int | None:
        if isinstance(value, bool):
            return None
        if isinstance(value, int):
            return value if value > 0 else None
        if isinstance(value, str):
            try:
                parsed = int(value)
            except ValueError:
                return None
            return parsed if parsed > 0 else None
        return None

    def _enforce_reasoning_model_min_tokens(
        self,
        payload: dict[str, Any],
        effective_model: str,
        context: ConnectorRequestContext | None = None,
    ) -> None:
        cfg = getattr(self.config, "reasoning_model_token_floor", None)
        enabled = True
        model_floors: dict[str, int] = {
            "stepfun/step-3.5-flash:free": 512,
            "kimi/kimi-for-coding": 512,
            "kimi-for-coding": 512,
        }
        if cfg is not None:
            enabled = bool(getattr(cfg, "enabled", True))
            configured_floors = getattr(cfg, "models", None)
            if isinstance(configured_floors, dict):
                model_floors = configured_floors

        if not enabled:
            return
        model_key = self._normalize_model_for_token_floor(
            effective_model or payload.get("model")
        )
        min_tokens = model_floors.get(model_key)
        if min_tokens is None:
            return

        log_extra_payload = self._get_log_extra(context) if context else None
        for token_key in ("max_completion_tokens", "max_tokens"):
            current_tokens = self._coerce_positive_int(payload.get(token_key))
            if current_tokens is None or current_tokens >= min_tokens:
                continue
            payload[token_key] = min_tokens
            logger.debug(
                "Raised %s from %d to %d for reasoning-first model '%s'",
                token_key,
                current_tokens,
                min_tokens,
                model_key,
                extra=log_extra_payload if log_extra_payload else None,
            )

    def _sanitize_deepseek_thinking_continuation_payload(
        self,
        payload: dict[str, Any],
        effective_model: str,
        context: ConnectorRequestContext | None = None,
    ) -> None:
        """Avoid DeepSeek 400s for unsupported roles and mixed thinking history."""

        model = str(payload.get("model") or effective_model or "").lower()
        if "deepseek" not in model:
            return

        removed_controls = []
        for key in ("reasoning", "thinking", "reasoning_effort"):
            if key in payload:
                removed_controls.append(key)
                payload.pop(key, None)

        messages = payload.get("messages")
        if not isinstance(messages, list):
            return

        remapped_developer_roles = 0
        reasoning_message_count = 0
        for message in messages:
            if not isinstance(message, dict):
                continue
            role = message.get("role")
            if isinstance(role, str) and role.strip().casefold() == "developer":
                message["role"] = "system"
                remapped_developer_roles += 1
            if message.get("reasoning_content") is not None:
                reasoning_message_count += 1
            elif message.get("reasoning") is not None:
                reasoning_message_count += 1
                message["reasoning_content"] = message.get("reasoning")
                message.pop("reasoning", None)
            elif message.get("reasoning_details") is not None:
                reasoning_message_count += 1
                message["reasoning_content"] = message.get("reasoning_details")
                message.pop("reasoning_details", None)

        if (
            reasoning_message_count <= 0
            and not removed_controls
            and remapped_developer_roles <= 0
        ):
            return

        log_extra_payload = self._get_log_extra(context) if context else None
        if remapped_developer_roles > 0:
            logger.warning(
                "Remapped DeepSeek unsupported message role developer->system "
                "in outbound payload: model=%s remapped_messages=%d",
                model,
                remapped_developer_roles,
                extra=log_extra_payload if log_extra_payload else None,
            )
        if reasoning_message_count > 0 or removed_controls:
            logger.warning(
                "Removed DeepSeek native thinking fields from outbound payload "
                "to avoid invalid mixed thinking-mode history: model=%s "
                "reasoning_messages=%d removed_controls=%s",
                model,
                reasoning_message_count,
                removed_controls,
                extra=log_extra_payload if log_extra_payload else None,
            )

    def _clean_openai_payload(self, payload: Any) -> dict[str, Any]:
        """Strip None values and internal-only top-level keys from an OpenAI payload."""
        disallowed_top_level_keys = {
            "extra_body",
            "backend_type",
            "agent",
            "session_id",
            "reasoning_effort",
            "request_context_tokens",
            "_edit_precision_mode",
            "_edit_precision_meta",
            RESOLVED_URI_PARAMS_EXTRA_BODY_KEY,
            _LLM_PROXY_STREAM_URL_KEY,
            _LLM_PROXY_STREAM_HEADERS_KEY,
            _LLM_PROXY_REQUEST_ID_KEY,
            _LLM_PROXY_SESSION_ID_KEY,
            _LLM_PROXY_CLIENT_HOST_KEY,
        }

        def _strip_none(value: Any) -> Any:
            if isinstance(value, list):
                cleaned_list = [
                    item for item in (_strip_none(v) for v in value) if item is not None
                ]
                return cleaned_list
            if isinstance(value, dict):
                cleaned_dict: dict[str, Any] = {}
                for key, val in value.items():
                    cleaned_val = _strip_none(val)
                    if cleaned_val is not None:
                        cleaned_dict[key] = cleaned_val
                return cleaned_dict
            return value

        if not isinstance(payload, dict):
            return {}

        cleaned_payload: dict[str, Any] = {}
        for key, val in payload.items():
            if key in disallowed_top_level_keys:
                continue
            cleaned_val = _strip_none(val)
            if cleaned_val is not None:
                cleaned_payload[key] = cleaned_val
        messages = cleaned_payload.get("messages")
        if isinstance(messages, list):
            for message in messages:
                if isinstance(message, dict):
                    message.pop("metadata", None)
            from src.core.domain.translation_utils.tool_utils import (
                sanitize_chat_messages_for_empty_tool_names,
            )

            sanitized_messages, removed_empty_tools = (
                sanitize_chat_messages_for_empty_tool_names(messages)
            )
            cleaned_payload["messages"] = sanitized_messages
            if removed_empty_tools > 0:
                logger.warning(
                    "Removed %d empty-name tool call/result field(s) from outbound "
                    "chat payload to avoid upstream HTTP 400s",
                    removed_empty_tools,
                )

        input_items = cleaned_payload.get("input")
        if isinstance(input_items, list):
            from src.core.domain.translation_utils.tool_utils import (
                sanitize_responses_input_for_empty_names,
            )

            sanitized_input, removed_empty_input = (
                sanitize_responses_input_for_empty_names(input_items)
            )
            cleaned_payload["input"] = sanitized_input
            if removed_empty_input > 0:
                logger.warning(
                    "Removed %d empty-name Responses input item(s) from outbound "
                    "payload to avoid upstream HTTP 400s",
                    removed_empty_input,
                )
        return cleaned_payload

    async def _handle_non_streaming_response(
        self,
        url: str,
        payload: dict[str, Any],
        headers: dict[str, str] | None,
        session_id: str,
        context: ConnectorRequestContext | None = None,
    ) -> ResponseEnvelope:
        if not headers or not headers.get("Authorization"):
            raise AuthenticationError(message="No auth credentials found")

        guarded_headers = self._apply_loop_guard_to_outbound_headers(headers)
        log_extra = self._get_log_extra(context)
        response = await self._send_request_with_retry(
            build_request=lambda: self.client.build_request(
                "POST", url, json=payload, headers=guarded_headers
            ),
            stream=False,
            capture=self._http_boundary_capture(
                model=str(payload.get("model") or "unknown"), context=context
            ),
            url=url,
            log_extra=log_extra if log_extra else None,
        )
        self.update_quota_headers(response.headers)

        if int(response.status_code) >= 400:
            try:
                err = response.json()
            except JSONDecodeError as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Failed to parse error response as JSON, using raw text: %s",
                        e,
                        exc_info=True,
                        extra=log_extra if log_extra else None,
                    )
                err = response.text
            except Exception as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Unexpected error parsing error response, using raw text: %s",
                        e,
                        exc_info=True,
                        extra=log_extra if log_extra else None,
                    )
                err = response.text
            _raise_upstream_http_error(
                status_code=int(response.status_code),
                error_detail=err,
                response=response,
                url=url,
            )

        decoded_json = self._decode_json_payload(response)
        if not isinstance(decoded_json, dict):
            raise BackendError(
                message="Invalid JSON payload returned by backend",
                details={"url": url},
                status_code=502,
            )
        response_json = decoded_json
        # Debug log raw response for non-streaming requests to help diagnose
        # translation issues (e.g., Claude Code via Anthropic frontend)
        if logger.isEnabledFor(logging.DEBUG):
            choices_count = len(response_json.get("choices", []))
            response_id = response_json.get("id", "unknown")
            response_model = response_json.get("model", "unknown")
            logger.debug(
                "Non-streaming response from backend: id=%s model=%s choices_count=%d",
                response_id,
                response_model,
                choices_count,
                extra=log_extra if log_extra else None,
            )
            if choices_count == 0:
                logger.debug(
                    "Empty choices in non-streaming response - raw response: %s",
                    str(response_json)[:500],
                    extra=log_extra if log_extra else None,
                )

        domain_response = self.translation_service.to_domain_response(
            response_json, "openai"
        )
        # Some tests use mocks that set response.headers to AsyncMock or
        # other non-dict types; defensively coerce to a dict and fall back
        # to an empty dict on error so tests don't raise during header
        # extraction.
        try:
            response_headers = dict(response.headers)
        except Exception as e:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Failed to extract response.headers, trying fallback: %s",
                    e,
                    exc_info=True,
                    extra=log_extra if log_extra else None,
                )
            try:
                response_headers = dict(getattr(response, "headers", {}) or {})
            except Exception as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Failed to extract response headers from fallback: %s",
                        e,
                        exc_info=True,
                        extra=log_extra if log_extra else None,
                    )
                response_headers = {}

        return ResponseEnvelope(
            content=domain_response.model_dump(),
            status_code=response.status_code,
            headers=response_headers,
            usage=domain_response.usage,
        )

    async def _handle_streaming_response(
        self,
        url: str,
        payload: dict[str, Any],
        headers: dict[str, str] | None,
        session_id: str,
        stream_format: str,
        context: ConnectorRequestContext | None = None,
    ) -> StreamingResponseHandle:
        """Return a streaming handle with iterator and cancellation callback."""

        log_extra = self._get_log_extra(context)

        if not headers or not headers.get("Authorization"):
            raise AuthenticationError(message="No auth credentials found")

        guarded_headers = self._apply_loop_guard_to_outbound_headers(headers)

        response = await self._send_request_with_retry(
            build_request=lambda: self.client.build_request(
                "POST", url, json=payload, headers=guarded_headers
            ),
            stream=True,
            capture=self._http_boundary_capture(
                model=str(payload.get("model") or "unknown"), context=context
            ),
            url=url,
            log_extra=log_extra if log_extra else None,
        )
        self.update_quota_headers(response.headers)

        status_code = (
            int(response.status_code) if hasattr(response, "status_code") else 200
        )
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "HTTP response status=%s content-type=%s content-length=%s url=%s",
                status_code,
                response.headers.get("content-type"),
                response.headers.get("content-length"),
                url,
                extra=log_extra if log_extra else None,
            )
        if status_code >= 400:
            body: str = ""
            try:
                body_bytes = await response.aread()
            except Exception as e:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Failed to read error response body: %s",
                        e,
                        exc_info=True,
                        extra=log_extra if log_extra else None,
                    )
                fallback: str = str(getattr(response, "text", ""))
                body = fallback() if callable(fallback) else fallback
            else:
                try:
                    body = body_bytes.decode("utf-8")
                except Exception as e:
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Failed to decode error response body: %s",
                            e,
                            exc_info=True,
                            extra=log_extra if log_extra else None,
                        )
                    fallback_text: str = str(getattr(response, "text", ""))
                    body = fallback_text() if callable(fallback_text) else fallback_text
            finally:
                try:
                    await response.aclose()
                except Exception as e:
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Failed to close error response: %s",
                            e,
                            exc_info=True,
                            extra=log_extra if log_extra else None,
                        )

            if not isinstance(body, str):
                body = str(body)
            logger.warning(
                "Backend %s returned HTTP %s with body: %s",
                url,
                status_code,
                body,
                extra=log_extra if log_extra else None,
            )

            # Attempt to parse error body as JSON and map Codex-specific errors
            error_detail: dict[str, Any] | str = body
            with contextlib.suppress(json.JSONDecodeError, ValueError):
                # Parse JSON; on failure, body remains as string (backward compatible)
                parsed_error = json.loads(body)
                if (
                    status_code == 400
                    and isinstance(parsed_error, dict)
                    and parsed_error.get("detail") == "Instructions are not valid"
                ):
                    # Map "Instructions are not valid" errors to actionable messages
                    error_detail = {
                        "error": "codex_instructions_invalid",
                        "message": (
                            "Codex backend rejected the instructions field as invalid. "
                            "This usually happens when custom prompt modifications are incompatible with Codex's validation rules."
                        ),
                        "detail": parsed_error.get("detail"),
                        "suggestion": (
                            "Set prompt_mode to 'codex_default' in your request capabilities "
                            "(or in config via backends.openai_codex.extra.codex.default_capabilities) "
                            "to use Codex's default instructions. System prompts are automatically "
                            "converted to <user_instructions> blocks and do not need to be in the instructions field."
                        ),
                        "original_error": parsed_error,
                    }
                elif isinstance(parsed_error, dict):
                    # Use parsed JSON if it's a dict
                    error_detail = parsed_error

            if status_code == 429:
                quota_message = _extract_insufficient_quota_message(body)
                if quota_message is not None:
                    status_code = 503
                    if isinstance(error_detail, dict):
                        error_detail = dict(error_detail)
                        error_detail["type"] = "quota_exceeded"
                        error_detail["code"] = 503
                        error_detail["message"] = quota_message
                        if "error" not in error_detail:
                            error_detail["error"] = {}
                        if isinstance(error_detail["error"], dict):
                            error_detail["error"] = dict(error_detail["error"])
                            error_detail["error"].setdefault("type", "quota_exceeded")
                            error_detail["error"].setdefault("code", 503)
                            error_detail["error"].setdefault("message", quota_message)
                    else:
                        error_detail = {
                            "message": quota_message,
                            "type": "quota_exceeded",
                            "code": 503,
                        }

            payload_detail: dict[str, Any] | str = (
                error_detail
                if isinstance(error_detail, dict)
                else {
                    "message": str(error_detail),
                    "type": (
                        "openrouter_error" if "openrouter" in url else "openai_error"
                    ),
                    "code": status_code,
                }
            )
            _raise_upstream_http_error(
                status_code=status_code,
                error_detail=payload_detail,
                response=response,
                url=url,
            )

        loop = asyncio.get_running_loop()
        response_id_future: asyncio.Future[str] = loop.create_future()
        cancel_lock = asyncio.Lock()
        cancel_state = {"called": False}
        supports_protocol_cancel = stream_format in {"responses", "openai-responses"}
        cancel_headers = dict(guarded_headers)
        cancel_headers.setdefault("Content-Type", "application/json")
        cancel_base_url = url.rstrip("/")
        cancel_model = str(payload.get("model") or "unknown")

        async def cancel_stream() -> None:
            async with cancel_lock:
                if cancel_state["called"]:
                    return
                cancel_state["called"] = True

            logger.debug(
                "upstream_stream_cancel_requested backend=%s model=%s method=%s session_id=%s",
                self.backend_type,
                cancel_model,
                (
                    "protocol_cancel_then_close"
                    if supports_protocol_cancel
                    else "close_response"
                ),
                session_id,
                extra=log_extra if log_extra else None,
            )

            target_id: str | None = None
            if supports_protocol_cancel:
                if response_id_future.done():
                    target_id = response_id_future.result()
                else:
                    try:
                        target_id = await asyncio.wait_for(response_id_future, 0.5)
                    except asyncio.TimeoutError:
                        target_id = None

            if target_id:
                logger.debug(
                    "upstream_protocol_cancel_requested backend=%s model=%s response_id=%s session_id=%s",
                    self.backend_type,
                    cancel_model,
                    target_id,
                    session_id,
                    extra=log_extra if log_extra else None,
                )
                cancel_sent = await self._send_openai_responses_cancel(
                    base_url=cancel_base_url,
                    headers=cancel_headers,
                    response_id=target_id,
                    session_id=session_id,
                    context=context,
                )
                if cancel_sent:
                    logger.debug(
                        "upstream_protocol_cancel_completed backend=%s model=%s response_id=%s session_id=%s",
                        self.backend_type,
                        cancel_model,
                        target_id,
                        session_id,
                        extra=log_extra if log_extra else None,
                    )
                else:
                    logger.warning(
                        "upstream_protocol_cancel_failed backend=%s model=%s response_id=%s session_id=%s",
                        self.backend_type,
                        cancel_model,
                        target_id,
                        session_id,
                        extra=log_extra if log_extra else None,
                    )
            elif supports_protocol_cancel:
                logger.debug(
                    "upstream_protocol_cancel_skipped backend=%s model=%s reason=response_id_unavailable session_id=%s",
                    self.backend_type,
                    cancel_model,
                    session_id,
                    extra=log_extra if log_extra else None,
                )

            try:
                await response.aclose()
            except Exception as e:
                logger.debug(
                    "upstream_stream_close_failed backend=%s model=%s session_id=%s error=%s",
                    self.backend_type,
                    cancel_model,
                    session_id,
                    e,
                    extra=log_extra if log_extra else None,
                )
            else:
                logger.debug(
                    "upstream_stream_close_completed backend=%s model=%s session_id=%s",
                    self.backend_type,
                    cancel_model,
                    session_id,
                    extra=log_extra if log_extra else None,
                )

        async def gen() -> AsyncGenerator[ProcessedResponse, None]:
            def _extract_chunk_id(chunk: Any) -> str | None:
                """Best-effort extraction of the response id from stream chunks."""
                if isinstance(chunk, dict):
                    chunk_id = chunk.get("id")
                    if isinstance(chunk_id, str) and chunk_id:
                        return chunk_id

                chunk_id = getattr(chunk, "id", None)
                if isinstance(chunk_id, str) and chunk_id:
                    return chunk_id

                if hasattr(chunk, "model_dump") and not isinstance(chunk, dict):
                    try:
                        chunk_dict = chunk.model_dump()  # type: ignore[attr-defined]
                        if isinstance(chunk_dict, dict):
                            chunk_id = chunk_dict.get("id")
                        else:
                            chunk_id = None
                    except (AttributeError, TypeError, ValueError) as e:
                        # Catch expected exceptions from model_dump() and attribute access
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                "Failed to extract chunk ID from model_dump (%s): %s",
                                type(e).__name__,
                                e,
                                exc_info=True,
                                extra=log_extra if log_extra else None,
                            )
                        return None
                    except Exception as e:
                        # Catch-all for unexpected errors - log with full context
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Unexpected error extracting chunk ID from model_dump: %s",
                                e,
                                exc_info=True,
                                extra=log_extra if log_extra else None,
                            )
                        return None
                    if isinstance(chunk_id, str) and chunk_id:
                        return chunk_id

                return None

            async def text_generator() -> AsyncGenerator[dict[Any, Any] | Any, None]:
                async def iter_sse_messages() -> AsyncGenerator[str, None]:
                    buffer = ""
                    separator = "\n\n"
                    alt_separator = "\r\n\r\n"
                    _first_byte_logged = False
                    try:
                        async for chunk_bytes in response.aiter_bytes():
                            if not _first_byte_logged and logger.isEnabledFor(
                                logging.DEBUG
                            ):
                                _first_byte_logged = True
                                preview = chunk_bytes[:300].decode(
                                    "utf-8", errors="replace"
                                )
                                logger.debug(
                                    "First streaming chunk (%d bytes): %s",
                                    len(chunk_bytes),
                                    preview,
                                    extra=log_extra if log_extra else None,
                                )
                            chunk_text = chunk_bytes.decode("utf-8", errors="replace")
                            # DoS protection: Limit buffer size to prevent memory exhaustion
                            if len(buffer) + len(chunk_text) > MAX_SSE_BUFFER_SIZE:
                                logger.warning(
                                    "SSE buffer overflow: truncating to prevent DoS",
                                    extra=log_extra if log_extra else None,
                                )
                                buffer = buffer[-MAX_SSE_BUFFER_SIZE:] if buffer else ""
                            buffer += chunk_text
                            while True:
                                if alt_separator in buffer:
                                    event, buffer = buffer.split(alt_separator, 1)
                                    separator_used = alt_separator
                                elif separator in buffer:
                                    event, buffer = buffer.split(separator, 1)
                                    separator_used = separator
                                else:
                                    break
                                if event:
                                    yield event + separator_used
                        if buffer:
                            yield buffer
                            buffer = ""
                    except httpx.ReadTimeout as exc:
                        if buffer:
                            yield buffer
                            buffer = ""
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Streaming read timeout during SSE for %s",
                                url,
                                extra=log_extra if log_extra else None,
                            )
                        raise BackendError(
                            message=(
                                "Upstream timed out while streaming. "
                                "Increase the HTTP client read timeout."
                            ),
                            details={"url": url, "reason": "read_timeout"},
                            status_code=504,
                        ) from exc
                    except httpx.ReadError as exc:
                        if buffer:
                            yield buffer
                            buffer = ""
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Streaming read error during SSE for %s",
                                url,
                                extra=log_extra if log_extra else None,
                            )
                        raise BackendError(
                            message=(
                                f"Upstream read error: connection lost during streaming ({exc!s})"
                            ),
                            details={"url": url, "reason": "read_error"},
                            status_code=502,
                        ) from exc
                    except httpx.RequestError as exc:
                        if buffer:
                            yield buffer
                            buffer = ""
                        raise ServiceUnavailableError(
                            message=f"Streaming connection interrupted ({exc!s})"
                        ) from exc

                try:
                    if stream_format in {"openai", "responses", "openai-responses"}:
                        async for message in iter_sse_messages():
                            domain_chunk = (
                                self.translation_service.to_domain_stream_chunk(
                                    message, stream_format
                                )
                            )
                            if (
                                isinstance(domain_chunk, dict)
                                and domain_chunk.get("error")
                                and logger.isEnabledFor(logging.DEBUG)
                            ):
                                try:
                                    if logger.isEnabledFor(logging.DEBUG):
                                        logger.debug(
                                            "Streaming chunk translation returned error=%s raw=%s",
                                            domain_chunk.get("error"),
                                            message[:500],
                                            extra=log_extra if log_extra else None,
                                        )
                                except (
                                    TypeError,
                                    ValueError,
                                    UnicodeDecodeError,
                                    UnicodeEncodeError,
                                    IndexError,
                                ):  # Exception types listed for documentation
                                    # Expected exceptions during debug logging:
                                    # - TypeError: message/obj not subscriptable or str() failed
                                    # - ValueError: slice indices invalid
                                    # - UnicodeError: encoding/decoding failed
                                    # - IndexError: string slice out of bounds
                                    if logger.isEnabledFor(logging.DEBUG):
                                        logger.debug(
                                            "Streaming chunk translation returned error but raw chunk not serializable",
                                            exc_info=True,
                                            extra=log_extra if log_extra else None,
                                        )
                            yield domain_chunk
                    else:
                        async for chunk in response.aiter_text():
                            domain_chunk = (
                                self.translation_service.to_domain_stream_chunk(
                                    chunk, stream_format
                                )
                            )
                            if (
                                isinstance(domain_chunk, dict)
                                and domain_chunk.get("error")
                                and logger.isEnabledFor(logging.DEBUG)
                            ):
                                try:
                                    logger.debug(
                                        "Streaming chunk translation returned error=%s raw=%s",
                                        domain_chunk.get("error"),
                                        chunk[:500],
                                        extra=log_extra if log_extra else None,
                                    )
                                except (
                                    TypeError,
                                    ValueError,
                                    UnicodeDecodeError,
                                    UnicodeEncodeError,
                                    IndexError,
                                ):  # Exception types listed for documentation
                                    # Expected exceptions during debug logging:
                                    # - TypeError: chunk not subscriptable or str() failed
                                    # - ValueError: slice indices invalid
                                    # - UnicodeError: encoding/decoding failed
                                    # - IndexError: string slice out of bounds
                                    if logger.isEnabledFor(logging.DEBUG):
                                        logger.debug(
                                            "Streaming chunk translation returned error but raw chunk not serializable",
                                            exc_info=True,
                                            extra=log_extra if log_extra else None,
                                        )
                            yield domain_chunk
                except httpx.RequestError as exc:
                    raise ServiceUnavailableError(
                        message=f"Streaming connection interrupted ({exc})"
                    ) from exc

            pending_error: Exception | None = None
            try:
                async for chunk in text_generator():
                    if supports_protocol_cancel and not response_id_future.done():
                        chunk_id = _extract_chunk_id(chunk)
                        if chunk_id:
                            response_id_future.set_result(chunk_id)
                    if stream_format in {
                        "responses",
                        "openai-responses",
                    } and isinstance(chunk, dict):
                        pr = ProcessedResponse(content=chunk)
                        yield ProcessedResponse(
                            content=chunk,
                            usage=usage_summary_from_processed_response(pr),
                        )
                    else:
                        yield ProcessedResponse(content=chunk)
            except ServiceUnavailableError as exc:
                pending_error = exc
            except httpx.HTTPError as exc:
                raise ServiceUnavailableError(
                    message=f"Streaming connection interrupted ({exc})"
                ) from exc
            finally:
                try:
                    await response.aclose()
                except (
                    httpx.HTTPStatusError,
                    httpx.HTTPError,
                    httpx.StreamError,
                    OSError,
                    RuntimeError,
                ):
                    # Expected exceptions during cleanup:
                    # - httpx.*: HTTP connection closing errors
                    # - OSError: socket/stream closure errors
                    # - RuntimeError: event loop or stream state issues
                    # Log cleanup errors but don't let them mask the original error
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Error closing streaming response during cleanup",
                            exc_info=True,
                            extra=log_extra if log_extra else None,
                        )
            if pending_error:
                raise pending_error

        try:
            response_headers = dict(response.headers)
        except (TypeError, AttributeError):
            # Catch specific exceptions from dict() conversion or missing headers attribute
            # TypeError: headers object doesn't support iteration/conversion
            # AttributeError: response object doesn't have headers attribute
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Failed to convert response headers to dict, using empty dict",
                    exc_info=True,
                    extra=log_extra if log_extra else None,
                )
            response_headers = {}

        return StreamingResponseHandle(
            iterator=gen(), cancel_callback=cancel_stream, headers=response_headers
        )

    async def _send_openai_responses_cancel(
        self,
        base_url: str,
        headers: Mapping[str, str],
        response_id: str,
        session_id: str,
        context: ConnectorRequestContext | None = None,
    ) -> bool:
        log_extra = self._get_log_extra(context)
        cancel_url = f"{base_url}/{response_id}/cancel"
        try:
            request = self.client.build_request("POST", cancel_url, headers=headers)
        except Exception as exc:
            logger.debug(
                "upstream_protocol_cancel_failed backend=%s response_id=%s session_id=%s error=%s",
                self.backend_type,
                response_id,
                session_id,
                exc,
                extra=log_extra if log_extra else None,
            )
            return False

        try:
            cancel_response = await self._capture_http_client.send(
                request,
                stream=False,
                capture=self._http_boundary_capture(
                    model="responses-cancel", context=context
                ),
            )
        except Exception as exc:
            logger.warning(
                "upstream_protocol_cancel_failed backend=%s response_id=%s session_id=%s error=%s",
                self.backend_type,
                response_id,
                session_id,
                exc,
                extra=log_extra if log_extra else None,
            )
            return False

        status_code = cancel_response.status_code
        with contextlib.suppress(Exception):
            await cancel_response.aclose()
        if status_code < 200 or status_code >= 300:
            logger.warning(
                "upstream_protocol_cancel_failed backend=%s response_id=%s session_id=%s status_code=%s",
                self.backend_type,
                response_id,
                session_id,
                status_code,
                extra=log_extra if log_extra else None,
            )
            return False
        return True

    @staticmethod
    def _append_interleaved_synthetic_messages_to_responses_input(
        payload: dict[str, Any],
        processed_messages: list[Any],
    ) -> None:
        """Append proxy-only synthetic user turns to a native Responses input.

        Native Responses requests carry their provider-shaped ``input`` in an
        internal extra-body field, so they bypass the normal chat-message
        projection. Interleaved-thinking steering still has to reach the executor
        and must be rendered as a plain user message without proxy metadata.
        """
        synthetic_items: list[dict[str, Any]] = []
        for message in processed_messages:
            metadata = getattr(message, "metadata", None)
            if not isinstance(metadata, dict):
                continue
            if (
                metadata.get("source") != "interleaved_thinking"
                or metadata.get("kind") != "thinker_memo_synthetic_user"
            ):
                continue
            content = getattr(message, "content", None)
            if isinstance(content, str):
                text = content
            elif isinstance(content, list):
                text_parts: list[str] = []
                for part in content:
                    if isinstance(part, str):
                        text_parts.append(part)
                    elif isinstance(part, dict) and isinstance(part.get("text"), str):
                        text_parts.append(part["text"])
                    else:
                        part_text = getattr(part, "text", None)
                        if isinstance(part_text, str):
                            text_parts.append(part_text)
                text = "".join(text_parts)
            else:
                text = "" if content is None else str(content)
            synthetic_items.append(
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": text}],
                }
            )

        if not synthetic_items:
            return
        raw_input = payload.get("input")
        if isinstance(raw_input, list):
            payload["input"] = [*raw_input, *synthetic_items]
        elif raw_input is None:
            payload["input"] = synthetic_items
        else:
            payload["input"] = [raw_input, *synthetic_items]

    async def responses(
        self, request: ConnectorResponsesRequest
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        """Handle OpenAI Responses API calls.

        This method handles requests to the /v1/responses endpoint, which provides
        structured output generation with JSON schema validation.
        """
        if (
            request.cancellation_coordinator is not None
            and request.cancellation_token is not None
        ):
            request.cancellation_coordinator.ensure_not_cancelled(
                request.cancellation_token
            )

        request_data = request.request
        processed_messages = list(request.processed_messages)
        effective_model = request.effective_model
        identity = request.identity
        kwargs = dict(request.options) if request.options else {}

        # Perform health check if enabled
        await self._ensure_healthy()

        extra_body = getattr(request_data, "extra_body", None) or {}
        native_payload_raw = extra_body.get(RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY)
        native_payload: dict[str, Any] | None = (
            dict(native_payload_raw) if isinstance(native_payload_raw, dict) else None
        )
        domain_request: CanonicalChatRequest | NativeResponsesContext
        if native_payload is not None:
            payload = native_payload
            if effective_model:
                payload["model"] = effective_model
            self._append_interleaved_synthetic_messages_to_responses_input(
                payload,
                processed_messages,
            )
            domain_request = NativeResponsesContext(
                stream=bool(getattr(request_data, "stream", False)),
                session_id=payload.get("session_id")
                or getattr(request_data, "session_id", None),
            )
        else:
            dr = self.translation_service.to_domain_request(request_data, "responses")
            domain_request = dr

            payload = self.translation_service.from_domain_to_responses_request(dr)

            if effective_model:
                payload["model"] = effective_model

        if processed_messages and native_payload is None:
            with contextlib.suppress(KeyError, TypeError, AttributeError):
                # Normalize messages; on failure, leave whatever the converter produced (fallback)
                normalized_messages: list[dict[str, Any]] = []
                for m in processed_messages:
                    # ChatMessage.to_dict() is the provider-boundary serializer and
                    # excludes proxy-only metadata. Preserve the explicit null content
                    # expected by some Responses-compatible gateways.
                    to_dict = getattr(m, "to_dict", None)
                    if callable(to_dict):
                        dumped = to_dict()
                        if isinstance(dumped, dict):
                            if "content" not in dumped:
                                dumped["content"] = None
                            normalized_messages.append(dumped)
                            continue
                    if hasattr(m, "model_dump") and callable(m.model_dump):
                        dumped = m.model_dump(exclude_none=False)
                        normalized_messages.append(dumped)
                        continue

                    # Fallback: build a minimal dict
                    msg: dict[str, Any] = {"role": getattr(m, "role", "user")}
                    content = getattr(m, "content", None)
                    msg["content"] = content

                    # Add other message fields if present
                    name = getattr(m, "name", None)
                    if name:
                        msg["name"] = name
                    tool_calls = getattr(m, "tool_calls", None)
                    if tool_calls:
                        msg["tool_calls"] = tool_calls
                    tool_call_id = getattr(m, "tool_call_id", None)
                    if tool_call_id:
                        msg["tool_call_id"] = tool_call_id
                    normalized_messages.append(msg)

                payload["messages"] = normalized_messages

        headers_override = kwargs.pop("headers_override", None)
        resolved_headers: dict[str, str] | None = None

        if isinstance(headers_override, Mapping):
            resolved_headers = {str(k): str(v) for k, v in headers_override.items()}

        base_headers: dict[str, str] | None
        try:
            base_headers = self.get_headers(identity=identity)
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to get base headers for responses request: %s",
                    e,
                    exc_info=True,
                )
            base_headers = None

        headers: dict[str, str] | None = None
        if resolved_headers:
            merged_resp = dict(resolved_headers)
            if base_headers is not None:
                merged_resp = {**merged_resp, **base_headers}
            headers = merged_resp
        else:
            headers = base_headers

        api_base_candidate = kwargs.get("openai_url")
        api_base = (
            api_base_candidate
            if isinstance(api_base_candidate, str) and api_base_candidate
            else self.api_base_url
        )
        url = f"{api_base.rstrip('/')}/responses"

        guarded_headers = self._apply_loop_guard_to_outbound_headers(headers)

        # Check if WebSocket transport is enabled and requested
        use_websocket_raw = kwargs.get("use_websocket")
        use_websocket = (
            use_websocket_raw
            if isinstance(use_websocket_raw, bool)
            else self._use_websocket
        )
        # Start from request.context (canonical path), then let options override
        connector_context: ConnectorRequestContext | None = None
        if isinstance(request.context, ConnectorRequestContext):
            connector_context = request.context
        options_context = kwargs.get("context")
        if isinstance(options_context, ConnectorRequestContext):
            connector_context = options_context
        if use_websocket:
            return await self._handle_websocket_response(
                payload,
                guarded_headers,
                domain_request,
                context=connector_context,
                effective_model=effective_model,
            )

        if domain_request.stream:
            # Return a domain-level streaming envelope
            stream_handle = await self._handle_streaming_response(
                url,
                payload,
                guarded_headers,
                domain_request.session_id or "",
                "openai-responses",
                context=connector_context,
            )
            return StreamingResponseEnvelope(
                content=stream_handle.iterator,
                media_type="text/event-stream",
                headers=stream_handle.headers or {},
                cancel_callback=stream_handle.cancel_callback,
            )
        else:
            # Return a domain ResponseEnvelope for non-streaming
            return await self._handle_responses_non_streaming_response(
                url,
                payload,
                guarded_headers,
                domain_request.session_id or "",
                context=connector_context,
            )

    async def _handle_websocket_response(
        self,
        payload: dict[str, Any],
        headers: dict[str, str] | None,
        domain_request: Any,
        context: ConnectorRequestContext | None = None,
        effective_model: str | None = None,
    ) -> StreamingResponseEnvelope:
        """Handle Responses API request via WebSocket transport.

        Args:
            payload: Response API request payload
            headers: Request headers
            domain_request: Domain request object

        Returns:
            StreamingResponseEnvelope with WebSocket stream

        Raises:
            AuthenticationError: If authentication fails
            ServiceUnavailableError: If WebSocket connection fails
        """
        if not headers or not headers.get("Authorization"):
            raise AuthenticationError(message="No auth credentials found")

        # Extract API key from headers
        auth_header = headers.get("Authorization", "")
        api_key = auth_header.replace("Bearer ", "") if auth_header else None
        if not api_key:
            raise AuthenticationError(message="No API key in authorization header")

        # Initialize WebSocket client if needed
        if self._websocket_client is None:
            from src.connectors.openai_websocket_client import OpenAIWebSocketClient

            # Convert HTTP URL to WebSocket URL
            ws_base = self.api_base_url.replace("https://", "wss://").replace(
                "http://", "ws://"
            )
            self._websocket_client = OpenAIWebSocketClient(
                api_key=api_key, api_base=ws_base
            )

        # Extract previous_response_id if present
        previous_response_id = payload.get("previous_response_id")

        # Create async generator for streaming
        async def _websocket_stream_generator():
            try:
                async for response_chunk in self._websocket_client.send_response_create(
                    payload=payload,
                    previous_response_id=previous_response_id,
                    context=context,
                    backend=self.backend_type,
                    model=str(effective_model or payload.get("model") or "unknown"),
                    key_name=self.backend_type,
                ):
                    yield response_chunk
            except Exception as e:
                if logger.isEnabledFor(logging.ERROR):
                    logger.error("Error in WebSocket stream: %s", e, exc_info=True)
                raise

        # Create cancel callback
        async def _cancel_callback() -> None:
            if self._websocket_client:
                await self._websocket_client.disconnect()

        return StreamingResponseEnvelope(
            content=_websocket_stream_generator(),
            media_type="text/event-stream",
            headers=headers or {},
            cancel_callback=_cancel_callback,
        )

    async def _handle_responses_non_streaming_response(
        self,
        url: str,
        payload: dict[str, Any],
        headers: dict[str, str] | None,
        session_id: str,
        context: ConnectorRequestContext | None = None,
    ) -> ResponseEnvelope:
        """Handle non-streaming Responses API responses with proper format conversion.

        Note: This method is called from responses() API endpoint, which is separate
        from the canonical chat completions API (Task 2.4 scope). The responses()
        method doesn't have access to ConnectorRequestContext, so context correlation
        is not available here. If this method is ever called from the canonical path,
        it should be updated to accept context parameter.
        """
        if not headers or not headers.get("Authorization"):
            raise AuthenticationError(message="No auth credentials found")

        guarded_headers = self._apply_loop_guard_to_outbound_headers(headers)

        response = await self._send_request_with_retry(
            build_request=lambda: self.client.build_request(
                "POST", url, json=payload, headers=guarded_headers
            ),
            stream=False,
            capture=self._http_boundary_capture(
                model=str(payload.get("model") or "unknown"), context=context
            ),
            url=url,
            log_extra=None,
        )
        self.update_quota_headers(response.headers)

        if int(response.status_code) >= 400:
            try:
                err = response.json()
            except JSONDecodeError as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Failed to parse error response as JSON, using raw text: %s",
                        e,
                        exc_info=True,
                    )
                err = response.text
            except Exception as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Unexpected error parsing error response, using raw text: %s",
                        e,
                        exc_info=True,
                    )
                err = response.text
            raise BackendError(
                message=str(err),
                status_code=response.status_code,
                code=str(response.status_code),
                details={"error_payload": err} if isinstance(err, dict) else {},
            )

        # For Responses API, we need to handle the response differently
        # The response should already be in Responses API format from OpenAI
        response_data = response.json()

        # Convert to domain response first, then back to ensure consistency
        # We'll treat the Responses API response as a special case of OpenAI response
        domain_response = self.translation_service.to_domain_response(
            response_data, "openai-responses"
        )

        # Convert back to Responses API format for the final response
        responses_content = self.translation_service.from_domain_to_responses_response(
            domain_response
        )

        try:
            response_headers = dict(response.headers)
        except AttributeError as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to access response.headers, trying getattr: %s",
                    e,
                    exc_info=True,
                )
            try:
                response_headers = dict(getattr(response, "headers", {}) or {})
            except Exception as e2:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Failed to get response headers via getattr, using empty dict: %s",
                        e2,
                        exc_info=True,
                    )
                response_headers = {}
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Unexpected error accessing response headers, using empty dict: %s",
                    e,
                    exc_info=True,
                )
            response_headers = {}

        return ResponseEnvelope(
            content=responses_content,
            status_code=response.status_code,
            headers=response_headers,
            usage=domain_response.usage,
        )

    async def list_models(
        self, api_base_url: str | None = None
    ) -> ModelsListingResponse:
        headers = self.get_headers()
        base = api_base_url or self.api_base_url
        logger.info("OpenAIConnector list_models - base URL: %s", base)
        response = await self.client.get(f"{base.rstrip('/')}/models", headers=headers)
        self.update_quota_headers(response.headers)
        response.raise_for_status()
        result = response.json()
        return ModelsListingResponse.model_validate(result)

    def get_available_models(self) -> list[str]:
        """Return available models with vendor prefix for unified model routing.

        Uses the class-level VENDOR_PREFIX to prefix model names. Subclasses
        can override VENDOR_PREFIX to use their vendor name, or set it to None
        for multi-vendor backends that receive pre-prefixed models from upstream.

        Returns:
            List of model names with vendor prefix (e.g., ['openai/gpt-4']).
        """
        models = self.available_models or []
        if self.VENDOR_PREFIX is None:
            # Multi-vendor backend: models are already prefixed from upstream
            return list(models)
        return [add_vendor_prefix(m, self.VENDOR_PREFIX) for m in models]

    # StreamProducer protocol implementation
    async def stream_completion(
        self, request: CanonicalChatRequest
    ) -> AsyncGenerator[object, None]:
        """Yield raw streaming chunks from the backend.

        This method implements the StreamProducer protocol for integration
        with the streaming pipeline refactor.

        Args:
            request: The chat completion request

        Yields:
            Raw streaming chunks from the backend (opaque provider-specific data)

        Note: This protocol method doesn't have access to ConnectorRequestContext.
        Error logs from this method cannot include context correlation identifiers.
        Adding context would require a protocol change, which is beyond Task 2.4 scope.
        """
        extra_body = getattr(request, "extra_body", None) or {}
        override_url: str | None = None
        override_headers: dict[str, str] | None = None
        connector_context: ConnectorRequestContext | None = None
        if isinstance(extra_body, dict):
            raw_u = extra_body.get(_LLM_PROXY_STREAM_URL_KEY)
            if isinstance(raw_u, str) and raw_u.strip():
                override_url = raw_u.strip()
            raw_h = extra_body.get(_LLM_PROXY_STREAM_HEADERS_KEY)
            if isinstance(raw_h, dict) and raw_h.get("Authorization"):
                override_headers = {str(k): str(v) for k, v in raw_h.items()}
            proxy_request_id = extra_body.get(_LLM_PROXY_REQUEST_ID_KEY)
            if isinstance(proxy_request_id, str) and proxy_request_id:
                proxy_session_id = extra_body.get(_LLM_PROXY_SESSION_ID_KEY)
                proxy_client_host = extra_body.get(_LLM_PROXY_CLIENT_HOST_KEY)
                connector_context = ConnectorRequestContext(
                    request_id=proxy_request_id,
                    session_id=(
                        proxy_session_id
                        if isinstance(proxy_session_id, str) and proxy_session_id
                        else None
                    ),
                    client_host=(
                        proxy_client_host
                        if isinstance(proxy_client_host, str) and proxy_client_host
                        else None
                    ),
                    extensions={},
                )
        if connector_context is None:
            fallback_session = getattr(request, "session_id", None)
            if isinstance(fallback_session, str) and fallback_session:
                connector_context = ConnectorRequestContext(
                    request_id=fallback_session,
                    session_id=fallback_session,
                    client_host=None,
                    extensions={},
                )

        # Build the request URL and payload
        if override_url is not None:
            url = override_url
        else:
            api_base = getattr(request, "api_base", None) or self.api_base_url
            url = f"{api_base.rstrip('/')}/chat/completions"

        # Get headers (prefer canonical path when injected via extra_body)
        if override_headers is not None:
            headers = override_headers
        else:
            identity = getattr(request, "identity", None)
            headers = self.get_headers(identity=identity)

        if not headers or not headers.get("Authorization"):
            raise AuthenticationError(message="No auth credentials found")

        guarded_headers = self._apply_loop_guard_to_outbound_headers(headers)

        # Prepare payload
        processed_messages = request.messages
        effective_model = request.model

        # Note: stream_completion is a protocol method and doesn't have context access
        # Context correlation would require protocol change
        payload = await self._prepare_payload(
            request, processed_messages, effective_model, context=None
        )

        # Ensure streaming is enabled
        payload["stream"] = True

        # Build and send request
        response = await self._send_request_with_retry(
            build_request=lambda: self.client.build_request(
                "POST", url, json=payload, headers=guarded_headers
            ),
            stream=True,
            capture=self._http_boundary_capture(
                model=str(effective_model), context=connector_context
            ),
            url=url,
            log_extra=None,
        )
        self.update_quota_headers(response.headers)

        status_code = (
            int(response.status_code) if hasattr(response, "status_code") else 200
        )
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "[stream_completion] HTTP status=%s content-type=%s content-length=%s url=%s",
                status_code,
                response.headers.get("content-type"),
                response.headers.get("content-length"),
                url,
            )
        if status_code >= 400:
            body = ""
            try:
                # Read only first 1MB of error body to prevent DoS
                aiter_bytes = getattr(response, "aiter_bytes", None)
                if callable(aiter_bytes):
                    aiter_bytes_fn = cast(
                        Callable[[], AsyncGenerator[bytes, None]], aiter_bytes
                    )
                    chunks: list[bytes] = []
                    total_size = 0
                    async for chunk in aiter_bytes_fn():
                        chunks.append(chunk)
                        total_size += len(chunk)
                        if total_size > 10 * 1024 * 1024:
                            break
                    body_bytes = b"".join(chunks)
                else:
                    aread = getattr(response, "aread", None)
                    if callable(aread):
                        aread_fn = cast(Callable[[], Awaitable[bytes]], aread)
                        body_bytes = await aread_fn()
                    else:
                        body_bytes = b""
                body = body_bytes.decode("utf-8")
            except (OSError, UnicodeDecodeError, httpx.RequestError, httpx.HTTPError):
                # Catch specific exceptions from reading/decoding error response body
                # UnicodeDecodeError: decode("utf-8") failed
                # IOError: I/O error during aread() or aiter_bytes()
                # httpx.RequestError, httpx.HTTPError: HTTP client errors
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Failed to read error response body, using fallback",
                        exc_info=True,
                    )
                try:
                    # Safely access text, ignoring ResponseNotRead on streams
                    body = str(getattr(response, "text", ""))
                except BaseException:
                    body = "Error response body could not be read"
            finally:
                with contextlib.suppress(BaseException):
                    await response.aclose()

            if not isinstance(body, str):
                body = str(body)
            logger.warning(
                "[stream_completion] Backend %s returned HTTP %s with body: %s",
                url,
                status_code,
                body,
            )

            error_details = _error_details_from_http_response(response)
            quota_message = _extract_insufficient_quota_message(body)
            if status_code == 429 and quota_message is not None:
                with contextlib.suppress(Exception):
                    await response.aclose()

                yield _build_quota_exhaustion_stream_chunk(
                    body=body, error_details=error_details, model=str(effective_model)
                )
                yield b"data: [DONE]\n\n"
                return

            if status_code == 429:
                reset_at = _parse_retry_after_header(response.headers)
                raise RateLimitExceededError(
                    message=body or "Upstream rate limit exceeded",
                    details=error_details,
                    reset_at=reset_at,
                )

            raise BackendError(
                message=body,
                status_code=status_code,
                code=str(status_code),
                details=error_details,
            )

        # Stream SSE messages
        try:
            buffer = ""
            separator = "\n\n"
            alt_separator = "\r\n\r\n"
            _sc_first_byte_logged = False

            async for chunk_bytes in response.aiter_bytes():
                if not _sc_first_byte_logged and logger.isEnabledFor(TRACE_LEVEL):
                    _sc_first_byte_logged = True
                    preview = chunk_bytes[:300].decode("utf-8", errors="replace")
                    logger.log(
                        TRACE_LEVEL,
                        "[stream_completion] First chunk (%d bytes): %s",
                        len(chunk_bytes),
                        preview,
                    )
                chunk_text = chunk_bytes.decode("utf-8", errors="replace")
                # DoS protection: Limit buffer size to prevent memory exhaustion
                if len(buffer) + len(chunk_text) > MAX_SSE_BUFFER_SIZE:
                    logger.warning("SSE buffer overflow: truncating to prevent DoS")
                    buffer = buffer[-MAX_SSE_BUFFER_SIZE:] if buffer else ""
                buffer += chunk_text

                while True:
                    if alt_separator in buffer:
                        event, buffer = buffer.split(alt_separator, 1)
                        separator_used = alt_separator
                    elif separator in buffer:
                        event, buffer = buffer.split(separator, 1)
                        separator_used = separator
                    else:
                        break

                    if event:
                        # Yield the raw SSE message (including separator)
                        yield event + separator_used

            # Yield any remaining buffer
            if buffer:
                yield buffer

        except httpx.ReadTimeout as exc:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning("Streaming read timeout for %s", url)
            raise BackendError(
                message=(
                    "Upstream timed out while streaming. "
                    "Increase the HTTP client read timeout."
                ),
                details={"url": url, "reason": "read_timeout"},
                status_code=504,
            ) from exc
        except httpx.ReadError as exc:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning("Streaming read error for %s", url)
            raise BackendError(
                message=(
                    f"Upstream read error: connection lost during streaming ({exc!s})"
                ),
                details={"url": url, "reason": "read_error"},
                status_code=502,
            ) from exc
        except httpx.RequestError as exc:
            raise ServiceUnavailableError(
                message=f"Streaming connection interrupted ({exc!s})"
            ) from exc
        finally:
            with contextlib.suppress(BaseException):
                await response.aclose()

    def get_provider_name(self) -> str:
        """Return the provider name for logging/metrics.

        Returns:
            Provider name ("openai")
        """
        return "openai"


backend_registry.register_backend("openai", OpenAIConnector)
