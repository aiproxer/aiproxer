"""Response executor for OpenAI Codex connector.

This module implements the ResponseExecutor service that handles:
- Streaming execution with authentication retry and error mapping
- Connector-level non-stream accumulation via the canonical streaming path
- Credential refresh integration for streaming retries
"""

# ruff: noqa: C901

from __future__ import annotations

import contextlib
import copy
import inspect
import json
import logging
import time
from collections.abc import AsyncIterator, Awaitable, Callable, Mapping
from typing import TYPE_CHECKING, Any, cast

from fastapi import HTTPException
from pydantic.types import JsonValue

from src.connectors._openai_codex_capabilities import CodexClientCapabilities
from src.connectors.contracts import ConnectorRequestContext
from src.connectors.contracts.wire_capture_context import (
    WIRE_CAPTURE_ACCOUNT_ID_KEY,
    WIRE_CAPTURE_IS_RETRY_KEY,
    WIRE_CAPTURE_RETRY_ATTEMPT_KEY,
)
from src.connectors.openai_codex.catalog.config import DEFAULT_CLIENT_VERSION
from src.connectors.openai_codex.continuation import (
    CodexContinuationSnapshot,
    InMemoryCodexContinuationCoordinator,
)
from src.connectors.openai_codex.contracts import (
    CodexPayload,
    CodexRequestContext,
    CompatibilityState,
    ProviderStreamChunk,
)
from src.connectors.openai_codex.gpt55_account_compatibility import (
    DEFAULT_GPT55_DOWNGRADE,
    Gpt55FreePlanDowngradeConfig,
    is_gpt55_downgrade_target_advertised,
    is_upstream_gpt55_chatgpt_rejection,
    maybe_reactive_gpt55_downgrade,
    plan_hint_is_free,
    should_downgrade_source_model,
)
from src.connectors.openai_codex.interfaces import (
    ICodexContinuationCoordinator,
    ICodexTransport,
    ICompatibilityLayer,
    ICredentialManager,
    IResponseExecutor,
)
from src.connectors.openai_codex.utils import (
    build_codex_user_agent,
    fingerprint_component,
    fingerprint_input_items,
    json_default,
)
from src.core.app.constants.logging_constants import TRACE_LEVEL
from src.core.common.exceptions import (
    AuthenticationError,
    InvalidRequestError,
    LLMProxyError,
    RateLimitExceededError,
)
from src.core.common.resilience_retry import AsyncRetryExecutor, RetryPolicy
from src.core.domain.responses import (
    StreamingResponseEnvelope,
    StreamingResponseHandle,
)
from src.core.interfaces.response_processor_interface import ProcessedResponse
from src.core.services.tool_text_renderer import OverrideRenderer
from src.core.transport.fastapi.exception_adapters import (
    map_domain_exception_to_http_exception,
)

if TYPE_CHECKING:
    from src.connectors.openai import OpenAIConnector


def _map_codex_instruction_error(status_code: int, detail: Any) -> Any:
    """Map Codex instruction validation failures to actionable proxy errors."""

    if status_code != 400 or not isinstance(detail, dict):
        return detail
    if detail.get("detail") != "Instructions are not valid":
        return detail
    return {
        "error": "codex_instructions_invalid",
        "message": (
            "Codex backend rejected the instructions field as invalid. "
            "This usually happens when custom prompt modifications are incompatible with Codex's validation rules."
        ),
        "detail": detail.get("detail"),
        "suggestion": (
            "Set prompt_mode to 'codex_default' in your request capabilities "
            "(or in config via backends.openai_codex.extra.codex.default_capabilities) "
            "to use Codex's default instructions. System prompts are automatically "
            "converted to <user_instructions> blocks and do not need to be in the instructions field."
        ),
        "original_error": detail,
    }


def _codex_initiate_streaming_error_view(
    exc: HTTPException | LLMProxyError,
) -> tuple[int, Any]:
    """Normalize handshake errors from OpenAIConnector for Codex retry logic."""

    if isinstance(exc, HTTPException):
        return exc.status_code, _map_codex_instruction_error(
            exc.status_code, exc.detail
        )
    status_code = getattr(exc, "status_code", 500)
    if not isinstance(status_code, int):
        status_code = 500
    det = getattr(exc, "details", None)
    if isinstance(det, dict) and det:
        return status_code, _map_codex_instruction_error(status_code, det)
    return status_code, {"message": getattr(exc, "message", str(exc))}


class _CodexTransportAdapter:
    """Adapter that wraps OpenAIConnector to implement ICodexTransport protocol.

    Supports both HTTP/SSE and WebSocket transport modes based on connector configuration.
    """

    def __init__(
        self,
        connector: OpenAIConnector,
        use_websocket: bool = False,
        *,
        responses_websocket_mode: str = "v1",
        transport_backend: str = "openai-codex",
    ) -> None:
        self._connector = connector
        self._use_websocket = use_websocket
        self._transport_backend = transport_backend
        self._responses_websocket_mode = (
            responses_websocket_mode
            if responses_websocket_mode in ("v1", "v2")
            else "v1"
        )
        self._websocket_client: Any = None  # OpenAIWebSocketClient | None
        self._websocket_api_key: str | None = None

    async def initiate_streaming_request(
        self,
        url: str,
        payload: dict[str, Any],
        headers: dict[str, str],
        session_id: str,
        *,
        context: ConnectorRequestContext | None = None,
        backend: str | None = None,
        model: str = "unknown",
        key_name: str | None = None,
    ) -> StreamingResponseHandle:
        wire_backend = backend or self._transport_backend
        # Opportunistically use WebSocket if enabled
        if self._use_websocket:
            return await self._initiate_websocket_streaming(
                url,
                payload,
                headers,
                session_id,
                context=context,
                backend=wire_backend,
                model=model,
                key_name=key_name,
            )

        # Default to HTTP/SSE
        return await self._connector._handle_streaming_response(  # type: ignore[misc]
            url,
            payload,
            headers,
            session_id,
            "responses",
            context=context,
        )

    async def _initiate_websocket_streaming(
        self,
        url: str,
        payload: dict[str, Any],
        headers: dict[str, str],
        session_id: str,
        *,
        context: ConnectorRequestContext | None = None,
        backend: str | None = None,
        model: str = "unknown",
        key_name: str | None = None,
    ) -> StreamingResponseHandle:
        """Initiate WebSocket streaming request for Codex.

        Args:
            url: Codex API endpoint URL (HTTP)
            payload: Request payload
            headers: Request headers
            session_id: Session identifier

        Returns:
            StreamingResponseHandle with WebSocket stream
        """
        wire_backend = backend or self._transport_backend
        # Convert HTTP URL to WebSocket URL
        ws_url = url.replace("https://", "wss://").replace("http://", "ws://")

        # Extract API key from headers
        auth_header = headers.get("Authorization", "")
        api_key = auth_header.replace("Bearer ", "") if auth_header else None
        if not api_key:
            raise AuthenticationError(message="No API key in authorization header")

        # Recreate the WebSocket client when auth is refreshed so retries do not
        # continue with a stale bearer token.
        if (
            self._websocket_client is not None
            and self._websocket_api_key is not None
            and self._websocket_api_key != api_key
        ):
            with contextlib.suppress(Exception):
                await self._websocket_client.disconnect()
            self._websocket_client = None

        # Initialize WebSocket client if needed
        if self._websocket_client is None:
            from src.connectors.openai_websocket_client import OpenAIWebSocketClient

            # Extract base URL (everything except /responses)
            if "/responses" in ws_url:
                ws_base = ws_url.rsplit("/responses", 1)[0]
            else:
                ws_base = ws_url.rsplit("/", 1)[0]

            self._websocket_client = OpenAIWebSocketClient(
                api_key=api_key,
                api_base=ws_base,
                responses_websocket_mode=self._responses_websocket_mode,
            )
            self._websocket_api_key = api_key

        # Create async generator for streaming
        async def _websocket_stream() -> AsyncIterator[ProcessedResponse]:
            try:
                async for response_chunk in self._websocket_client.send_response_create(
                    payload=payload,
                    previous_response_id=payload.get("previous_response_id"),
                    context=context,
                    backend=wire_backend,
                    model=model,
                    key_name=key_name,
                ):
                    # Pass through ProcessedResponse from WebSocket client
                    yield response_chunk
            except Exception as e:
                if _is_expected_recoverable_codex_ws_error(e):
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            "Handled Codex WebSocket recovery condition: %s",
                            e,
                        )
                elif logger.isEnabledFor(logging.ERROR):
                    logger.error(
                        "Error in Codex WebSocket stream: %s",
                        e,
                        exc_info=True,
                    )
                raise

        # Create cancel callback
        async def _cancel_callback() -> None:
            if self._websocket_client:
                await self._websocket_client.disconnect()

        return StreamingResponseHandle(
            iterator=_websocket_stream(),
            headers=headers,
            cancel_callback=_cancel_callback,
        )

    async def cleanup(self) -> None:
        """Clean up WebSocket connections."""
        if self._websocket_client is not None:
            try:
                await self._websocket_client.disconnect()
            except Exception as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Error disconnecting Codex WebSocket client: %s",
                        e,
                        exc_info=True,
                    )
            finally:
                self._websocket_client = None
                self._websocket_api_key = None


logger = logging.getLogger(__name__)


class _CodexRetryDelayError(Exception):
    """Internal signal used to delegate auth retry waits to stamina."""

    def __init__(self, delay_seconds: float) -> None:
        super().__init__(f"codex_auth_retry_delay:{delay_seconds}")
        self.delay_seconds = delay_seconds


def _is_expected_recoverable_codex_ws_error(exc: Exception) -> bool:
    if isinstance(exc, RateLimitExceededError):
        return True
    if isinstance(exc, InvalidRequestError):
        details = exc.details
        return details.get("code") == "previous_response_not_found"
    return False


class ResponseExecutor(IResponseExecutor):
    """Service for executing Codex API requests on the canonical streaming path.

    This service handles:
    - Streaming execution with authentication retry and error mapping
    - Connector-level non-stream accumulation via the canonical streaming path
    - Credential refresh integration for streaming retries
    """

    # INFO logging calls ``json.dumps`` on the full Codex ``input`` list; skip that
    # work for very large histories to avoid multi-second event-loop stalls before
    # each ``http_full_replay`` upstream request.
    _LOG_JSON_MEASURE_MAX_INPUT_ITEMS: int = 120

    def __init__(
        self,
        base_connector: OpenAIConnector,
        credential_manager: ICredentialManager,
        max_retries: int = 2,
        retry_backoff_seconds: tuple[float, ...] = (0.5, 1.5, 3.0),
        codex_url: str = "https://chatgpt.com/backend-api/codex/responses",
        compatibility_layer: ICompatibilityLayer | None = None,
        transport: ICodexTransport | None = None,
        continuation_coordinator: ICodexContinuationCoordinator | None = None,
        use_websocket: bool = False,
        *,
        websocket_beta_mode: str = "v1",
        connector_transport_backend: str | None = None,
        continuation_backend_label: str | None = None,
        codex_ws_lineage: Any | None = None,
        preserve_tools_on_managed_ws_continuation: bool = False,
        gpt55_free_plan_downgrade: Gpt55FreePlanDowngradeConfig | None = None,
        codex_client_version: str = DEFAULT_CLIENT_VERSION,
    ) -> None:
        """Initialize the response executor.

        Args:
            base_connector: Base OpenAI connector for HTTP operations
            credential_manager: Credential manager for token refresh
            max_retries: Maximum retry attempts for streaming auth failures
            retry_backoff_seconds: Backoff sequence for retries
            codex_url: Codex API endpoint URL
            compatibility_layer: Optional compatibility layer for stream chunk translation
            transport: Optional transport interface for streaming HTTP requests
            use_websocket: Whether to use WebSocket transport (default: False)
        """
        self._base_connector = base_connector
        self._credential_manager = credential_manager
        self._max_retries = max_retries
        self._retry_backoff_seconds = retry_backoff_seconds
        self._codex_url = codex_url
        self._compatibility_layer = compatibility_layer
        self._use_websocket = use_websocket
        ws_norm = str(websocket_beta_mode).strip().lower()
        self._websocket_beta_mode = ws_norm if ws_norm in ("v1", "v2") else "v1"
        self._connector_transport_backend = (
            connector_transport_backend or "openai-codex"
        )
        self._continuation_backend_label = (
            continuation_backend_label or self._connector_transport_backend
        )
        self._codex_ws_lineage = codex_ws_lineage
        self._preserve_tools_on_managed_ws_continuation = (
            preserve_tools_on_managed_ws_continuation
        )
        self._gpt55_free_plan_downgrade: Gpt55FreePlanDowngradeConfig = (
            gpt55_free_plan_downgrade
            if gpt55_free_plan_downgrade is not None
            else DEFAULT_GPT55_DOWNGRADE
        )
        self._codex_client_version = codex_client_version or DEFAULT_CLIENT_VERSION
        self._max_incompatible_tool_retries = 2
        self._continuation_coordinator = (
            continuation_coordinator
            if continuation_coordinator is not None
            else InMemoryCodexContinuationCoordinator()
        )
        self._transport = (
            transport
            if transport is not None
            else _CodexTransportAdapter(
                base_connector,
                use_websocket=use_websocket,
                responses_websocket_mode=self._websocket_beta_mode,
                transport_backend=self._connector_transport_backend,
            )
        )
        self._auth_retry_delay_executor = AsyncRetryExecutor(
            RetryPolicy(
                attempts=2,
                timeout_seconds=None,
                wait_initial=0.0,
                wait_max=0.0,
                wait_jitter=0.0,
                wait_exp_base=1.0,
            )
        )

    def _maybe_apply_proactive_gpt55_free_plan_downgrade(
        self,
        payload: dict[str, Any],
        context: CodexRequestContext,
    ) -> None:
        """Replace gpt-5.5 with target model when plan telemetry says free tier."""
        cfg = self._gpt55_free_plan_downgrade
        raw_model = payload.get("model")
        if not isinstance(raw_model, str) or not should_downgrade_source_model(
            current_model=raw_model, config=cfg
        ):
            return
        hint_getter = getattr(
            self._credential_manager, "get_codex_plan_type_hint", None
        )
        if not callable(hint_getter):
            return
        try:
            plan_hint = hint_getter()
        except Exception as e:
            if logger.isEnabledFor(TRACE_LEVEL):
                logger.log(
                    TRACE_LEVEL,
                    "get_codex_plan_type_hint failed: %s",
                    e,
                    exc_info=True,
                    extra={
                        "backend": self._connector_transport_backend,
                        "session_id": context.session_id,
                    },
                )
            return
        if not plan_hint_is_free(plan_hint, cfg.free_plan_types):
            return
        self._warn_when_downgrade_target_not_advertised(cfg)
        payload["model"] = cfg.target_model
        if logger.isEnabledFor(logging.INFO):
            logger.info(
                "Codex proactive model downgrade: %s -> %s (plan_hint=%r, reason=%s)",
                raw_model,
                cfg.target_model,
                plan_hint,
                "codex_gpt55_unsupported_proactive_free_plan",
                extra={
                    "backend": self._connector_transport_backend,
                    "session_id": context.session_id,
                },
            )

    def _warn_when_downgrade_target_not_advertised(
        self, cfg: Gpt55FreePlanDowngradeConfig
    ) -> None:
        """Log when the downgrade target is absent from the resolved catalog.

        Absence is observability only: the target may still be legacy-accepted
        by the backend, so the configured target is still attempted.
        """
        catalog = getattr(self._base_connector, "_catalog", None)
        is_supported = getattr(catalog, "is_supported", None)
        try:
            advertised = is_gpt55_downgrade_target_advertised(cfg, is_supported)
        except Exception:
            return
        if advertised is False:
            logger.warning(
                "Codex gpt-5.5 downgrade target %r is not in the resolved model "
                "catalog; attempting it anyway as a legacy-accepted slug.",
                str(cfg.target_model).strip(),
            )

    async def execute(
        self, payload: CodexPayload, context: CodexRequestContext
    ) -> StreamingResponseEnvelope:
        """Execute Codex request with retry and compatibility handling.

        Codex always executes against the upstream streaming transport. Callers that
        need a non-streaming result must accumulate the returned stream at the
        connector boundary.

        Args:
            payload: Codex API payload
            context: Request context

        Returns:
            Streaming response envelope
        """
        # Resolve renderer key from capabilities
        renderer_key = self._select_renderer_key(context.capabilities)

        return await self._execute_streaming(payload, context, renderer_key)

    async def _execute_streaming(
        self, payload: CodexPayload, context: CodexRequestContext, renderer_key: str
    ) -> StreamingResponseEnvelope:
        """Execute streaming Codex request with authentication retry.

        Args:
            payload: Codex API payload
            context: Request context
            renderer_key: Renderer key for tool text rendering

        Returns:
            Streaming response envelope with retry handling
        """
        url = self._codex_url
        # Derive conversation_id from prompt_cache_key, fallback to session_id
        conversation_id = payload.prompt_cache_key or context.session_id
        headers = self._build_headers(conversation_id, context.session_id)
        continuation_context = self._build_continuation_context(
            context,
            prompt_cache_key=payload.prompt_cache_key,
        )
        payload_dict = payload.model_dump(exclude_none=True)
        # ChatGPT Codex ``/responses`` rejects ``temperature`` (HTTP 400
        # "Unsupported parameter: temperature"). Keep it off the wire even when
        # early-session bump or clients set it on the canonical request.
        payload_dict.pop("temperature", None)
        full_payload_dict = dict(payload_dict)
        continuation_snapshot = await self._get_continuation_snapshot(
            continuation_context
        )
        proxy_managed_previous_response_id = False
        use_websocket_transport = self._use_websocket

        if use_websocket_transport:
            continuation_reason = "client_previous_response_id_present"
            ws_v2_handled = False
            if self._codex_ws_lineage is not None:
                handled, payload_dict, ws_reason, ws_proxy = (
                    await self._codex_ws_lineage.try_prepare_websocket_continuation(
                        continuation_context=continuation_context,
                        payload_dict=payload_dict,
                        full_payload_dict=full_payload_dict,
                    )
                )
                if handled:
                    ws_v2_handled = True
                    continuation_reason = ws_reason
                    proxy_managed_previous_response_id = ws_proxy
            if not ws_v2_handled and "previous_response_id" not in payload_dict:
                continuation_reason = "no_previous_response_id_available"
                previous_response_id = (
                    await self._continuation_coordinator.resolve_previous_response_id(
                        continuation_context
                    )
                )
                if previous_response_id:
                    if continuation_snapshot is None:
                        payload_dict["previous_response_id"] = previous_response_id
                        proxy_managed_previous_response_id = True
                        continuation_reason = "continuation_no_snapshot"
                    elif self._is_compatible_continuation_snapshot(
                        continuation_snapshot, payload_dict
                    ):
                        sliced_input = self._slice_input_for_continuation(
                            continuation_snapshot, payload_dict
                        )
                        if sliced_input is not None:
                            payload_dict["previous_response_id"] = previous_response_id
                            payload_dict["input"] = sliced_input
                            proxy_managed_previous_response_id = True
                            continuation_reason = "continuation_delta_applied"
                        else:
                            await self._continuation_coordinator.invalidate(
                                continuation_context,
                                reason="continuation_input_drift",
                            )
                            continuation_snapshot = None
                            continuation_reason = "continuation_input_drift"
                    else:
                        await self._continuation_coordinator.invalidate(
                            continuation_context,
                            reason="continuation_static_fingerprint_changed",
                        )
                        continuation_snapshot = None
                        continuation_reason = "continuation_static_fingerprint_changed"
        else:
            # HTTP /responses rejects ``previous_response_id`` with HTTP 400.
            had_client_previous = "previous_response_id" in payload_dict
            payload_dict.pop("previous_response_id", None)
            if had_client_previous:
                continuation_reason = "http_stripped_client_previous_response_id"
            elif continuation_snapshot is not None:
                continuation_reason = "http_full_replay_active"
            else:
                continuation_reason = "http_bootstrap"
        replay_payload_dict = dict(full_payload_dict)
        initial_payload_dict = (
            self._prune_continuation_bootstrap_fields(dict(payload_dict))
            if proxy_managed_previous_response_id
            else dict(payload_dict)
        )
        initial_request_mode = self._resolve_request_mode(
            transport="websocket" if use_websocket_transport else "http",
            proxy_managed_previous_response_id=proxy_managed_previous_response_id,
            has_previous_response_id="previous_response_id" in initial_payload_dict,
            has_continuation_snapshot=continuation_snapshot is not None,
        )

        headers_holder: dict[str, str] = {}
        current_cancel: list[Callable[[], Awaitable[None]] | None] = [None]
        request_context = self._extract_connector_request_context(context)
        capture_key_name = self._resolve_capture_key_name(context)

        async def cancel_active_stream() -> None:
            cancel_cb = current_cancel[0]
            if cancel_cb is not None:
                await cancel_cb()

        async def _streaming_iterator() -> AsyncIterator[ProcessedResponse]:
            """Streaming iterator with authentication retry logic."""
            attempts_used = 0
            max_retries = await self._effective_rate_limit_max_retries()
            incompatible_tool_retries = 0
            previous_response_retry_used = False
            current_headers = dict(headers)
            current_payload_dict = dict(initial_payload_dict)
            current_request_mode = initial_request_mode
            self._maybe_apply_proactive_gpt55_free_plan_downgrade(
                current_payload_dict, context
            )
            gpt55_reactive_recovery_used = False
            handshake_dispatch_index = -1
            handshake_managed_oauth_account_id: str | None = None
            handshake_managed_oauth_account_email: str | None = None

            # Get compatibility state from context metadata if available
            # State should always be provided by the facade via CodexRequestContext.metadata
            # when compatibility layer is enabled and successfully applied (see design.md).
            compatibility_state: CompatibilityState | None = None
            if context.metadata and "compatibility_state" in context.metadata:
                state_value = context.metadata["compatibility_state"]
                if isinstance(state_value, CompatibilityState):
                    compatibility_state = state_value

            stream_handle = None
            try:
                while True:
                    handshake_dispatch_index += 1
                    ws_output_items: list[Any] = []
                    self._refresh_headers_auth(
                        current_headers,
                        conversation_id,
                        context.session_id,
                    )
                    snapshot_fn = getattr(
                        self._credential_manager,
                        "snapshot_managed_oauth_storage_account_id",
                        None,
                    )
                    _raw_snapshot = snapshot_fn() if callable(snapshot_fn) else None
                    handshake_managed_oauth_account_id = (
                        str(_raw_snapshot)
                        if isinstance(_raw_snapshot, str) and _raw_snapshot.strip()
                        else None
                    )
                    email_fn = getattr(
                        self._credential_manager,
                        "snapshot_managed_oauth_account_email",
                        None,
                    )
                    _raw_email = email_fn() if callable(email_fn) else None
                    handshake_managed_oauth_account_email = (
                        str(_raw_email)
                        if isinstance(_raw_email, str) and _raw_email.strip()
                        else None
                    )
                    self._log_request_attempt(
                        context,
                        current_payload_dict,
                        mode=current_request_mode,
                        attempt=attempts_used + 1,
                        reason=continuation_reason,
                    )
                    self._stamp_wire_capture_context(
                        request_context,
                        handshake_attempt_index=handshake_dispatch_index,
                    )
                    self._stamp_codex_websocket_handshake_headers(
                        request_context, current_headers
                    )
                    try:
                        stream_handle = (
                            await self._transport.initiate_streaming_request(
                                url,
                                current_payload_dict,
                                current_headers,
                                context.session_id,
                                context=request_context,
                                backend=self._connector_transport_backend,
                                model=context.effective_model,
                                key_name=capture_key_name,
                            )
                        )
                        # Fall through to consume the stream iterator below
                    except (HTTPException, LLMProxyError) as exc:
                        status_code, detail = _codex_initiate_streaming_error_view(exc)
                        if status_code == 403 and attempts_used < max_retries:
                            rotated = await self._handle_forbidden_rotation(
                                session_id=context.session_id
                            )
                            if rotated:
                                await self._invalidate_continuation_on_rotation(
                                    continuation_context,
                                    reason="auth_rotation",
                                )
                                await self._wait_for_auth_retry_delay(attempts_used)
                                attempts_used += 1
                                continue

                        if status_code == 401 or status_code == 403:
                            _acct_label = (
                                f"{handshake_managed_oauth_account_id}"
                                f" ({handshake_managed_oauth_account_email})"
                                if handshake_managed_oauth_account_id
                                and handshake_managed_oauth_account_email
                                else (
                                    handshake_managed_oauth_account_id
                                    or "unknown account"
                                )
                            )

                            _upstream_err = detail if isinstance(detail, dict) else {}
                            _upstream_code = None
                            if isinstance(_upstream_err.get("error"), dict):
                                _upstream_code = _upstream_err["error"].get("code")
                            elif isinstance(_upstream_err.get("code"), str):
                                _upstream_code = _upstream_err["code"]

                            if _upstream_code == "token_invalidated":
                                notify_fn = getattr(
                                    self._credential_manager,
                                    "maybe_notify_token_invalidated",
                                    None,
                                )
                                if callable(notify_fn) and inspect.iscoroutinefunction(
                                    notify_fn
                                ):
                                    await notify_fn(
                                        account_id=handshake_managed_oauth_account_id,
                                        email=handshake_managed_oauth_account_email,
                                    )

                            if attempts_used >= max_retries:
                                # Notify connector of authentication failure for degradation
                                degrade_method = getattr(
                                    self._base_connector, "_degrade", None
                                )
                                if degrade_method is not None:
                                    degrade_method(
                                        [
                                            f"Codex streaming request failed authentication during handshake after {attempts_used} attempts (account: {_acct_label})"
                                        ]
                                    )
                                raise HTTPException(
                                    status_code=401,
                                    detail={
                                        "error": "openai_codex_stream_auth_failed",
                                        "message": "Codex streaming request failed authentication during handshake and could not be recovered.",
                                        "details": {
                                            "backend": self._connector_transport_backend,
                                            "attempts": attempts_used,
                                            "max_retries": max_retries,
                                            "account_id": handshake_managed_oauth_account_id,
                                            "account_email": handshake_managed_oauth_account_email,
                                        },
                                    },
                                )
                            # Use connector's refresh method to ensure consistency and test compatibility
                            refresh_method = getattr(
                                self._base_connector, "_refresh_access_token", None
                            )
                            try:
                                if (
                                    refresh_method
                                    and callable(refresh_method)
                                    and inspect.iscoroutinefunction(refresh_method)
                                ):
                                    refreshed = await refresh_method()
                                else:
                                    refreshed = (
                                        await self._credential_manager.refresh_access_token()
                                    )
                                    # Update connector's api_key so get_headers() returns the new token
                                    new_token = (
                                        self._credential_manager.get_access_token()
                                    )
                                    if new_token and hasattr(
                                        self._base_connector, "api_key"
                                    ):
                                        self._base_connector.api_key = new_token
                            except Exception as refresh_exc:
                                logger.warning(
                                    "Codex streaming auth refresh raised for %s: %s",
                                    _acct_label,
                                    refresh_exc,
                                )
                                if logger.isEnabledFor(logging.DEBUG):
                                    logger.debug(
                                        "Codex streaming auth refresh exception details",
                                        exc_info=True,
                                    )
                                refreshed = False
                            if not refreshed:
                                raise HTTPException(
                                    status_code=401,
                                    detail={
                                        "error": "openai_codex_stream_auth_failed",
                                        "message": "Codex streaming request failed authentication during handshake and could not be recovered.",
                                        "details": {
                                            "backend": self._connector_transport_backend,
                                            "attempts": attempts_used,
                                            "max_retries": max_retries,
                                            "account_id": handshake_managed_oauth_account_id,
                                            "account_email": handshake_managed_oauth_account_email,
                                        },
                                    },
                                )
                            await self._wait_for_auth_retry_delay(attempts_used)
                            attempts_used += 1
                            continue
                        if status_code == 429:
                            rd = detail if isinstance(detail, dict) else {}
                            retry_after_seconds = (
                                self._extract_retry_after_from_payload(rd)
                            )
                            # Quota failover must get at least one handshake attempt even when
                            # streaming.max_retries is 0 or managed effective budget is 0 (n<=1),
                            # without changing 401/403 retry limits (same loop uses ``max_retries``).
                            rl_rotation_cap = max(1, max_retries)
                            rate_limit_rotation_attempted = (
                                attempts_used < rl_rotation_cap
                            )
                            rotated = False
                            if rate_limit_rotation_attempted:
                                rotated = await self._handle_rate_limit_rotation(
                                    retry_after_seconds=retry_after_seconds,
                                    session_id=context.session_id,
                                    upstream_codex_error=rd or None,
                                    response_headers=None,
                                    managed_oauth_account_id=handshake_managed_oauth_account_id,
                                )
                                if rotated:
                                    await self._invalidate_continuation_on_rotation(
                                        continuation_context,
                                        reason="rate_limit_rotation",
                                    )
                                    await self._wait_for_auth_retry_delay(attempts_used)
                                    attempts_used += 1
                                    continue
                            await self._notify_codex_quota_unrecovered(
                                upstream_detail=rd or {},
                                retry_after_seconds=retry_after_seconds,
                                pool_exhaustion_confirmed=(
                                    rate_limit_rotation_attempted and not rotated
                                ),
                            )
                        if status_code == 400:
                            cfg_55 = self._gpt55_free_plan_downgrade
                            cur_mod = current_payload_dict.get("model")
                            if isinstance(
                                cur_mod, str
                            ) and is_upstream_gpt55_chatgpt_rejection(detail):
                                nxt = maybe_reactive_gpt55_downgrade(
                                    current_model=cur_mod,
                                    config=cfg_55,
                                    recovery_already_used=gpt55_reactive_recovery_used,
                                )
                                if nxt is not None:
                                    gpt55_reactive_recovery_used = True
                                    self._warn_when_downgrade_target_not_advertised(
                                        cfg_55
                                    )
                                    if logger.isEnabledFor(logging.INFO):
                                        logger.info(
                                            "Codex reactive model downgrade: %s -> %s "
                                            "(reason=codex_gpt55_unsupported_downgrade)",
                                            cur_mod,
                                            nxt,
                                            extra={
                                                "backend": self._connector_transport_backend,
                                                "session_id": context.session_id,
                                            },
                                        )
                                    # New dict so prior initiate_streaming_request call_args keep
                                    # the model sent on the failed attempt (same object was passed).
                                    current_payload_dict = {
                                        **current_payload_dict,
                                        "model": nxt,
                                    }
                                    continue
                        if isinstance(exc, LLMProxyError):
                            raise map_domain_exception_to_http_exception(exc) from exc
                        raise HTTPException(status_code=status_code, detail=detail)

                    current_cancel[0] = stream_handle.cancel_callback
                    headers_holder.clear()
                    try:
                        headers_holder.update(dict(stream_handle.headers or {}))
                    except (TypeError, AttributeError, ValueError) as e:
                        if logger.isEnabledFor(TRACE_LEVEL):
                            logger.log(
                                TRACE_LEVEL,
                                "Failed to extract response headers from stream: %s",
                                e,
                                exc_info=True,
                                extra={
                                    "backend": self._connector_transport_backend,
                                    "session_id": context.session_id,
                                    "model": context.effective_model,
                                },
                            )
                        headers_holder.clear()

                    restart_stream = False
                    retry_for_incompatible_tools = False
                    retry_for_missing_previous_response = False
                    visible_output_emitted = False
                    observed_response_id: str | None = None
                    observed_response_id_persisted = False
                    terminal_response_id: str | None = None
                    http_midstream_fingerprints_deferred = False
                    last_stream_chunk_mono: float | None = None
                    try:
                        with OverrideRenderer(renderer_key):
                            stream_dbg_idx = 0
                            async for processed_chunk in stream_handle.iterator:
                                stream_dbg_idx += 1
                                _iter_enter = time.monotonic()
                                _gap_ms = (
                                    int((_iter_enter - last_stream_chunk_mono) * 1000)
                                    if last_stream_chunk_mono is not None
                                    else -1
                                )
                                _norm_ms = 0
                                _det_ms = 0
                                _cmp_ms = 0
                                _vis_ms = 0
                                candidate_observed_response_id = (
                                    self._extract_response_id(processed_chunk)
                                )
                                if (
                                    observed_response_id is None
                                    and candidate_observed_response_id is not None
                                ):
                                    observed_response_id = (
                                        candidate_observed_response_id
                                    )
                                    if not use_websocket_transport:
                                        # HTTP Codex strips ``previous_response_id``; full input
                                        # fingerprints are only needed for websocket delta slicing.
                                        # Skip ``record_turn`` here so huge ``http_full_replay`` payloads
                                        # do not block the session mid-stream (fingerprints are flushed
                                        # at stream completion or on the next persist call).
                                        await self._persist_observed_continuation(
                                            continuation_context,
                                            response_id=observed_response_id,
                                            payload_dict=replay_payload_dict,
                                            include_fingerprint_snapshot=False,
                                        )
                                        http_midstream_fingerprints_deferred = True
                                        observed_response_id_persisted = True
                                    elif self._codex_ws_lineage is not None:
                                        await self._persist_observed_ws_lineage(
                                            continuation_context,
                                            response_id=observed_response_id,
                                            payload_dict=replay_payload_dict,
                                            items_added=ws_output_items,
                                        )
                                    logger.info(
                                        "Observed Codex response id mid-stream and persisted provisional continuation state (response_id=%s, mode=%s).",
                                        observed_response_id,
                                        current_request_mode,
                                        extra={
                                            "backend": self._connector_transport_backend,
                                            "session_id": context.session_id,
                                            "model": context.effective_model,
                                            "continuation_mode": current_request_mode,
                                            "continuation_reason": continuation_reason,
                                            "response_id": observed_response_id,
                                        },
                                    )
                                candidate_response_id = (
                                    self._extract_terminal_response_id(processed_chunk)
                                )
                                if candidate_response_id:
                                    terminal_response_id = candidate_response_id
                                if (
                                    self._codex_ws_lineage is not None
                                    and use_websocket_transport
                                ):
                                    meta = processed_chunk.metadata
                                    if (
                                        meta.get("event_type")
                                        == "response.output_item.done"
                                    ):
                                        raw = self._coerce_stream_chunk_content(
                                            processed_chunk.content
                                        )
                                        if isinstance(raw, dict):
                                            item = raw.get("item")
                                            if item is not None:
                                                ws_output_items.append(
                                                    copy.deepcopy(item)
                                                )
                                                if observed_response_id is not None:
                                                    await self._persist_observed_ws_lineage(
                                                        continuation_context,
                                                        response_id=observed_response_id,
                                                        payload_dict=replay_payload_dict,
                                                        items_added=ws_output_items,
                                                    )
                                _t_norm = time.monotonic()
                                processed_chunk = (
                                    self._normalize_processed_stream_chunk(
                                        processed_chunk
                                    )
                                )
                                _norm_ms = int((time.monotonic() - _t_norm) * 1000)
                                _t_det = time.monotonic()
                                incompatible_tools = (
                                    self._detect_incompatible_tool_calls(
                                        processed_chunk.content,
                                        context,
                                    )
                                )
                                _det_ms = int((time.monotonic() - _t_det) * 1000)
                                if incompatible_tools:
                                    if (
                                        incompatible_tool_retries
                                        < self._max_incompatible_tool_retries
                                    ):
                                        retry_for_incompatible_tools = True
                                        restart_stream = True
                                        incompatible_tool_retries += 1
                                        current_payload_dict = self._append_incompatible_tool_retry_steering(
                                            current_payload_dict,
                                            incompatible_tools,
                                            context,
                                        )
                                        logger.info(
                                            "Retrying streaming Codex request after incompatible tool calls: %s",
                                            ", ".join(incompatible_tools),
                                            extra={
                                                "backend": self._connector_transport_backend,
                                                "session_id": context.session_id,
                                                "model": context.effective_model,
                                                "retry_count": incompatible_tool_retries,
                                            },
                                        )
                                        break
                                    logger.warning(
                                        "Incompatible streaming tool calls persisted after retries; forwarding final stream.",
                                        extra={
                                            "backend": self._connector_transport_backend,
                                            "session_id": context.session_id,
                                            "model": context.effective_model,
                                            "tool_names": incompatible_tools,
                                        },
                                    )

                                if self._should_retry_for_auth_error(processed_chunk):
                                    if visible_output_emitted:
                                        logger.warning(
                                            "Skipping stream restart for auth error because visible output was already emitted.",
                                            extra={
                                                "backend": self._connector_transport_backend,
                                                "session_id": context.session_id,
                                                "model": context.effective_model,
                                            },
                                        )
                                        yield processed_chunk
                                        last_stream_chunk_mono = time.monotonic()
                                        continue
                                    restart_stream = True
                                    logger.info(
                                        "Codex streaming chunk reported authentication failure; attempting token refresh.",
                                        extra={
                                            "backend": self._connector_transport_backend,
                                            "session_id": context.session_id,
                                            "model": context.effective_model,
                                            "attempts_used": attempts_used,
                                        },
                                    )
                                    # If max_retries is 0, raise immediately without yielding the error chunk
                                    if max_retries == 0:
                                        if stream_handle.cancel_callback is not None:
                                            with contextlib.suppress(Exception):
                                                await stream_handle.cancel_callback()
                                        # Notify connector of authentication failure for degradation
                                        degrade_method = getattr(
                                            self._base_connector, "_degrade", None
                                        )
                                        if degrade_method is not None:
                                            degrade_method(
                                                [
                                                    f"Codex streaming request failed authentication after {attempts_used} attempts"
                                                ]
                                            )
                                        raise HTTPException(
                                            status_code=401,
                                            detail={
                                                "error": "openai_codex_stream_auth_failed",
                                                "message": "Codex streaming request failed authentication and could not be recovered.",
                                                "details": {
                                                    "backend": self._connector_transport_backend,
                                                    "attempts": attempts_used,
                                                    "max_retries": max_retries,
                                                },
                                            },
                                        )
                                    break

                                # Apply compatibility layer translation if available
                                if self._compatibility_layer and compatibility_state:
                                    _t_cmp = time.monotonic()
                                    try:
                                        pre_translation_metadata = dict(
                                            processed_chunk.metadata or {}
                                        )
                                        chunk_wrapper = ProviderStreamChunk(
                                            raw=processed_chunk
                                        )
                                        translated_chunk = await self._compatibility_layer.translate_stream_chunk(
                                            chunk_wrapper, compatibility_state
                                        )
                                        # Extract the translated content
                                        if hasattr(translated_chunk, "raw"):
                                            processed_chunk = cast(
                                                ProcessedResponse, translated_chunk.raw
                                            )
                                        else:
                                            processed_chunk = cast(
                                                ProcessedResponse, translated_chunk
                                            )
                                        if pre_translation_metadata:
                                            merged_metadata = dict(
                                                pre_translation_metadata
                                            )
                                            merged_metadata.update(
                                                processed_chunk.metadata or {}
                                            )
                                            processed_chunk = ProcessedResponse(
                                                content=processed_chunk.content,
                                                usage=processed_chunk.usage,
                                                metadata=merged_metadata,
                                            )
                                        _cmp_ms = int(
                                            (time.monotonic() - _t_cmp) * 1000
                                        )
                                    except Exception as e:
                                        _cmp_ms = int(
                                            (time.monotonic() - _t_cmp) * 1000
                                        )
                                        if logger.isEnabledFor(TRACE_LEVEL):
                                            logger.log(
                                                TRACE_LEVEL,
                                                "Compatibility layer translation failed: %s",
                                                e,
                                                exc_info=True,
                                                extra={
                                                    "backend": self._connector_transport_backend,
                                                    "session_id": context.session_id,
                                                    "model": context.effective_model,
                                                },
                                            )
                                        # Continue with original chunk on translation failure
                                else:
                                    _cmp_ms = 0

                                _t_vis = time.monotonic()
                                if self._chunk_has_client_visible_output(
                                    processed_chunk
                                ):
                                    visible_output_emitted = True
                                _vis_ms = int((time.monotonic() - _t_vis) * 1000)
                                yield processed_chunk
                                last_stream_chunk_mono = time.monotonic()
                    except Exception as exc:
                        if self._is_previous_response_not_found_error(exc):
                            await self._continuation_coordinator.invalidate(
                                continuation_context,
                                reason="previous_response_not_found",
                            )
                            await self._invalidate_ws_lineage(
                                continuation_context,
                                reason="previous_response_not_found",
                            )
                            if (
                                proxy_managed_previous_response_id
                                and not previous_response_retry_used
                                and not visible_output_emitted
                                and "previous_response_id" in current_payload_dict
                            ):
                                retry_for_missing_previous_response = True
                                restart_stream = True
                                previous_response_retry_used = True
                                current_payload_dict = dict(replay_payload_dict)
                                current_payload_dict.pop("previous_response_id", None)
                                current_request_mode = "fallback_replay"
                                logger.info(
                                    "Retrying Codex request with full replay after continuation miss.",
                                    extra={
                                        "backend": self._connector_transport_backend,
                                        "session_id": context.session_id,
                                        "model": context.effective_model,
                                        "continuation_mode": current_request_mode,
                                    },
                                )
                            else:
                                raise
                        elif (
                            isinstance(exc, HTTPException | LLMProxyError)
                            and not visible_output_emitted
                        ):
                            status_code, detail = _codex_initiate_streaming_error_view(
                                exc
                            )
                            if status_code == 429:
                                rd = detail if isinstance(detail, dict) else {}
                                retry_after_seconds = (
                                    self._extract_retry_after_from_payload(rd)
                                )
                                rl_rotation_cap = max(1, max_retries)
                                rate_limit_rotation_attempted = (
                                    attempts_used < rl_rotation_cap
                                )
                                rotated = False
                                if rate_limit_rotation_attempted:
                                    rotated = await self._handle_rate_limit_rotation(
                                        retry_after_seconds=retry_after_seconds,
                                        session_id=context.session_id,
                                        upstream_codex_error=rd or None,
                                        response_headers=None,
                                        managed_oauth_account_id=handshake_managed_oauth_account_id,
                                    )
                                    if rotated:
                                        await self._invalidate_continuation_on_rotation(
                                            continuation_context,
                                            reason="rate_limit_rotation",
                                        )
                                        await self._wait_for_auth_retry_delay(
                                            attempts_used
                                        )
                                        attempts_used += 1
                                        restart_stream = True
                                if restart_stream:
                                    continue
                                await self._notify_codex_quota_unrecovered(
                                    upstream_detail=rd or {},
                                    retry_after_seconds=retry_after_seconds,
                                    pool_exhaustion_confirmed=(
                                        rate_limit_rotation_attempted and not rotated
                                    ),
                                )
                                if isinstance(exc, LLMProxyError):
                                    raise map_domain_exception_to_http_exception(
                                        exc
                                    ) from exc
                                raise HTTPException(
                                    status_code=status_code,
                                    detail=detail,
                                ) from exc
                            if isinstance(exc, LLMProxyError):
                                raise map_domain_exception_to_http_exception(
                                    exc
                                ) from exc
                            raise HTTPException(
                                status_code=status_code,
                                detail=detail,
                            ) from exc
                        else:
                            if (
                                observed_response_id_persisted
                                or observed_response_id is not None
                            ):
                                await self._invalidate_continuation_on_rotation(
                                    continuation_context,
                                    reason=f"streaming_error:{type(exc).__name__}",
                                )
                            raise

                    # If stream completed successfully without auth errors, exit retry loop
                    if not restart_stream:
                        await self._mark_account_used()
                        if terminal_response_id:
                            if not (
                                observed_response_id_persisted
                                and terminal_response_id == observed_response_id
                            ):
                                await self._persist_observed_continuation(
                                    continuation_context,
                                    response_id=terminal_response_id,
                                    payload_dict=replay_payload_dict,
                                    include_fingerprint_snapshot=True,
                                )
                            elif (
                                not use_websocket_transport
                                and http_midstream_fingerprints_deferred
                            ):
                                await self._record_continuation_turn(
                                    continuation_context,
                                    terminal_response_id,
                                    replay_payload_dict,
                                )
                        elif observed_response_id:
                            if not observed_response_id_persisted:
                                await self._persist_observed_continuation(
                                    continuation_context,
                                    response_id=observed_response_id,
                                    payload_dict=replay_payload_dict,
                                    include_fingerprint_snapshot=True,
                                )
                            elif (
                                not use_websocket_transport
                                and http_midstream_fingerprints_deferred
                            ):
                                await self._record_continuation_turn(
                                    continuation_context,
                                    observed_response_id,
                                    replay_payload_dict,
                                )
                            logger.info(
                                "Codex stream ended before terminal completion; observed response id remains available for continuation (response_id=%s, mode=%s, reason=%s).",
                                observed_response_id,
                                current_request_mode,
                                continuation_reason,
                                extra={
                                    "backend": self._connector_transport_backend,
                                    "session_id": context.session_id,
                                    "model": context.effective_model,
                                    "continuation_mode": current_request_mode,
                                    "continuation_reason": continuation_reason,
                                    "response_id": observed_response_id,
                                },
                            )
                        else:
                            logger.warning(
                                "Codex stream completed without a terminal response id; continuation state was not updated.",
                                extra={
                                    "backend": self._connector_transport_backend,
                                    "session_id": context.session_id,
                                    "model": context.effective_model,
                                    "continuation_mode": current_request_mode,
                                    "continuation_reason": continuation_reason,
                                },
                            )
                        final_response_id = terminal_response_id or observed_response_id
                        final_rid = (
                            final_response_id.strip()
                            if isinstance(final_response_id, str)
                            else ""
                        )
                        if (
                            use_websocket_transport
                            and self._codex_ws_lineage is not None
                            and final_rid
                        ):
                            await self._codex_ws_lineage.record_completed_websocket_turn(
                                continuation_context,
                                sent_payload=copy.deepcopy(replay_payload_dict),
                                response_id=final_rid,
                                items_added=ws_output_items,
                            )
                        break

                    if restart_stream:
                        if stream_handle.cancel_callback is not None:
                            retry_reason = (
                                "incompatible_tools"
                                if retry_for_incompatible_tools
                                else (
                                    "missing_previous_response"
                                    if retry_for_missing_previous_response
                                    else "auth_retry"
                                )
                            )
                            logger.info(
                                "Cancelling active Codex stream for retry (reason=%s, response_id=%s, mode=%s).",
                                retry_reason,
                                observed_response_id or "unknown",
                                current_request_mode,
                                extra={
                                    "backend": self._connector_transport_backend,
                                    "session_id": context.session_id,
                                    "model": context.effective_model,
                                    "continuation_mode": current_request_mode,
                                    "continuation_reason": continuation_reason,
                                    "response_id": observed_response_id,
                                    "retry_reason": retry_reason,
                                },
                            )
                            with contextlib.suppress(Exception):
                                await stream_handle.cancel_callback()

                        if retry_for_incompatible_tools:
                            current_cancel[0] = None
                            continue

                        if retry_for_missing_previous_response:
                            current_cancel[0] = None
                            continue

                        # Check retry limit before attempting refresh
                        # If we've already used all retries, raise exception
                        _midstream_acct_label = (
                            f"{handshake_managed_oauth_account_id}"
                            f" ({handshake_managed_oauth_account_email})"
                            if handshake_managed_oauth_account_id
                            and handshake_managed_oauth_account_email
                            else (
                                handshake_managed_oauth_account_id or "unknown account"
                            )
                        )
                        if attempts_used >= max_retries:
                            # Notify connector of authentication failure for degradation
                            degrade_method = getattr(
                                self._base_connector, "_degrade", None
                            )
                            if degrade_method is not None:
                                degrade_method(
                                    [
                                        f"Codex streaming request failed authentication after {attempts_used} attempts (account: {_midstream_acct_label})"
                                    ]
                                )
                            raise HTTPException(
                                status_code=401,
                                detail={
                                    "error": "openai_codex_stream_auth_failed",
                                    "message": "Codex streaming request failed authentication and could not be recovered.",
                                    "details": {
                                        "backend": self._connector_transport_backend,
                                        "attempts": attempts_used,
                                        "max_retries": max_retries,
                                        "account_id": handshake_managed_oauth_account_id,
                                        "account_email": handshake_managed_oauth_account_email,
                                    },
                                },
                            )

                        # Use connector's refresh method to ensure consistency and test compatibility
                        refresh_method = getattr(
                            self._base_connector, "_refresh_access_token", None
                        )
                        try:
                            if (
                                refresh_method
                                and callable(refresh_method)
                                and inspect.iscoroutinefunction(refresh_method)
                            ):
                                refreshed = await refresh_method()
                            else:
                                refreshed = (
                                    await self._credential_manager.refresh_access_token()
                                )
                                # Update connector's api_key so get_headers() returns the new token
                                new_token = self._credential_manager.get_access_token()
                                if new_token and hasattr(
                                    self._base_connector, "api_key"
                                ):
                                    self._base_connector.api_key = new_token
                        except Exception as refresh_exc:
                            logger.warning(
                                "Codex streaming auth refresh raised for %s: %s",
                                _midstream_acct_label,
                                refresh_exc,
                            )
                            if logger.isEnabledFor(logging.DEBUG):
                                logger.debug(
                                    "Codex streaming auth refresh exception details",
                                    exc_info=True,
                                )
                            refreshed = False
                        if not refreshed:
                            # Notify connector of authentication failure for degradation
                            degrade_method = getattr(
                                self._base_connector, "_degrade", None
                            )
                            if degrade_method is not None:
                                degrade_method(
                                    [
                                        f"Codex streaming token refresh failed after {attempts_used} attempts (account: {_midstream_acct_label})"
                                    ]
                                )
                            raise HTTPException(
                                status_code=401,
                                detail={
                                    "error": "openai_codex_stream_auth_failed",
                                    "message": "Codex streaming request failed authentication and could not be recovered.",
                                    "details": {
                                        "backend": self._connector_transport_backend,
                                        "attempts": attempts_used,
                                        "max_retries": max_retries,
                                        "account_id": handshake_managed_oauth_account_id,
                                        "account_email": handshake_managed_oauth_account_email,
                                    },
                                },
                            )

                        await self._wait_for_auth_retry_delay(attempts_used)
                        attempts_used += 1
                        # Restart the stream by continuing the outer while loop
                        continue

                    current_cancel[0] = None
                    return
            finally:
                # Cleanup compatibility state after streaming completes
                if self._compatibility_layer and compatibility_state:
                    try:
                        await self._compatibility_layer.cleanup_state(
                            compatibility_state
                        )
                    except Exception as e:
                        if logger.isEnabledFor(TRACE_LEVEL):
                            logger.log(
                                TRACE_LEVEL,
                                "Compatibility state cleanup failed: %s",
                                e,
                                exc_info=True,
                                extra={
                                    "backend": self._connector_transport_backend,
                                    "session_id": context.session_id,
                                    "model": context.effective_model,
                                },
                            )

        return StreamingResponseEnvelope(
            content=_streaming_iterator(),
            media_type="text/event-stream",
            headers=headers_holder,
            cancel_callback=cancel_active_stream,
            metadata={
                "backend": self._connector_transport_backend,
                "model": context.effective_model,
                "session_id": context.session_id,
            },
        )

    def _build_headers(self, conversation_id: str, session_id: str) -> dict[str, str]:
        """Build Codex-specific HTTP headers.

        Args:
            conversation_id: Conversation identifier (from prompt_cache_key)
            session_id: Session identifier (proxy correlation ID)

        Returns:
            Headers dictionary
        """
        # Get base headers from connector (includes Authorization)
        base_headers = self._base_connector.get_headers() or {}

        headers = dict(base_headers)
        headers["OpenAI-Beta"] = "responses=experimental"
        headers["Accept"] = "text/event-stream"
        headers["version"] = self._codex_client_version  # CODEX_VERSION_HEADER
        headers["originator"] = "codex_cli_rs"  # CODEX_ORIGINATOR

        headers["User-Agent"] = build_codex_user_agent(
            version=self._codex_client_version
        )

        headers["conversation_id"] = conversation_id
        # Codex CLI sends both conversation_id and session_id as the same stable id.
        # Using the proxy correlation id here breaks parity and can lead to backend
        # rejecting cached/session-scoped state. Keep them aligned.
        headers["session_id"] = conversation_id
        headers["Codex-Task-Type"] = "standard"

        # Add account ID if available
        account_id = self._get_account_id()
        if account_id:
            headers["chatgpt-account-id"] = account_id

        return headers

    def _build_continuation_context(
        self,
        context: CodexRequestContext,
        *,
        prompt_cache_key: str,
    ) -> CodexRequestContext:
        metadata = dict(context.metadata) if isinstance(context.metadata, dict) else {}
        metadata["continuation_backend"] = self._continuation_backend_label
        metadata["continuation_prompt_cache_key"] = prompt_cache_key
        account_id = self._get_account_id()
        if account_id:
            metadata["continuation_account_id"] = account_id
        return context.model_copy(update={"metadata": metadata})

    def _stamp_wire_capture_context(
        self,
        connector_context: ConnectorRequestContext | None,
        *,
        handshake_attempt_index: int,
    ) -> None:
        """Tag connector context for CBOR HTTP boundary capture (account + attempt)."""
        if connector_context is None:
            return
        account_id = self._get_account_id()
        if isinstance(account_id, str) and account_id.strip():
            connector_context.extensions[WIRE_CAPTURE_ACCOUNT_ID_KEY] = (
                account_id.strip()
            )
        connector_context.extensions[WIRE_CAPTURE_RETRY_ATTEMPT_KEY] = int(
            handshake_attempt_index
        )
        connector_context.extensions[WIRE_CAPTURE_IS_RETRY_KEY] = (
            handshake_attempt_index > 0
        )

    @staticmethod
    def _extract_connector_request_context(
        context: CodexRequestContext,
    ) -> ConnectorRequestContext | None:
        metadata = context.metadata if isinstance(context.metadata, dict) else {}
        candidate = metadata.get("connector_request_context")
        if isinstance(candidate, ConnectorRequestContext):
            return candidate
        request = context.request
        extra_body = getattr(request, "extra_body", None)
        if isinstance(extra_body, dict):
            request_id = extra_body.get("_llm_proxy_request_id")
            if isinstance(request_id, str) and request_id.strip():
                proxy_session_id = extra_body.get("_llm_proxy_session_id")
                proxy_client_host = extra_body.get("_llm_proxy_client_host")
                return ConnectorRequestContext(
                    request_id=request_id.strip(),
                    session_id=(
                        proxy_session_id.strip()
                        if isinstance(proxy_session_id, str)
                        and proxy_session_id.strip()
                        else context.session_id
                    ),
                    client_host=(
                        proxy_client_host.strip()
                        if isinstance(proxy_client_host, str)
                        and proxy_client_host.strip()
                        else None
                    ),
                    extensions={},
                )
        return ConnectorRequestContext(
            request_id=context.session_id,
            session_id=context.session_id,
            client_host=None,
            extensions={},
        )

    def _resolve_capture_key_name(self, context: CodexRequestContext) -> str:
        metadata = context.metadata if isinstance(context.metadata, dict) else {}
        candidate = metadata.get("capture_key_name")
        if isinstance(candidate, str) and candidate.strip():
            return candidate.strip()
        return self._connector_transport_backend

    async def _get_continuation_snapshot(
        self, context: CodexRequestContext
    ) -> CodexContinuationSnapshot | None:
        getter = getattr(self._continuation_coordinator, "get_snapshot", None)
        if not callable(getter):
            return None
        # Support both sync and async getters for backward compatibility during migration
        if inspect.iscoroutinefunction(getter):
            snapshot = await getter(context)
        else:
            snapshot = getter(context)
        return snapshot if isinstance(snapshot, CodexContinuationSnapshot) else None

    async def _record_continuation_turn(
        self,
        context: CodexRequestContext,
        response_id: str,
        payload_dict: dict[str, Any],
    ) -> None:
        recorder = getattr(self._continuation_coordinator, "record_turn", None)
        if callable(recorder):
            if inspect.iscoroutinefunction(recorder):
                await recorder(
                    context,
                    response_id=response_id,
                    payload_dict=payload_dict,
                )
            else:
                recorder(
                    context,
                    response_id=response_id,
                    payload_dict=payload_dict,
                )

    async def _persist_observed_continuation(
        self,
        context: CodexRequestContext,
        *,
        response_id: str,
        payload_dict: dict[str, Any],
        include_fingerprint_snapshot: bool = True,
    ) -> None:
        await self._continuation_coordinator.record_response_id(
            context,
            response_id,
        )
        if include_fingerprint_snapshot:
            await self._record_continuation_turn(
                context,
                response_id,
                payload_dict,
            )

    async def _persist_observed_ws_lineage(
        self,
        context: CodexRequestContext,
        *,
        response_id: str,
        payload_dict: dict[str, Any],
        items_added: list[Any],
    ) -> None:
        if self._codex_ws_lineage is None:
            return
        await self._codex_ws_lineage.record_completed_websocket_turn(
            context,
            sent_payload=copy.deepcopy(payload_dict),
            response_id=response_id,
            items_added=items_added,
        )

    async def _invalidate_continuation_on_rotation(
        self, context: CodexRequestContext, *, reason: str
    ) -> None:
        await self._continuation_coordinator.invalidate(context, reason=reason)
        await self._invalidate_ws_lineage(context, reason=reason)

    async def _invalidate_ws_lineage(
        self, context: CodexRequestContext, *, reason: str
    ) -> None:
        if self._codex_ws_lineage is None:
            return
        await self._codex_ws_lineage.invalidate(context, reason=reason)

    _WS_HANDSHAKE_HEADER_ALLOWLIST = frozenset(
        {
            "User-Agent",
            "originator",
            "version",
            "conversation_id",
            "session_id",
            "Codex-Task-Type",
            "chatgpt-account-id",
        }
    )

    def _stamp_codex_websocket_handshake_headers(
        self,
        request_context: ConnectorRequestContext | None,
        handshake_headers: dict[str, str],
    ) -> None:
        if not self._use_websocket or request_context is None:
            return
        extra = {
            k: v
            for k, v in handshake_headers.items()
            if k in self._WS_HANDSHAKE_HEADER_ALLOWLIST
        }
        request_context.extensions["codex_ws_extra_headers"] = cast(JsonValue, extra)

    @staticmethod
    def _is_compatible_continuation_snapshot(
        snapshot: CodexContinuationSnapshot,
        payload_dict: dict[str, Any],
    ) -> bool:
        # M1: Uses shared fingerprinting utilities instead of concrete class method
        return snapshot.instructions_fingerprint == fingerprint_component(
            payload_dict.get("instructions")
        ) and snapshot.tools_fingerprint == fingerprint_component(
            payload_dict.get("tools")
        )

    @staticmethod
    def _slice_input_for_continuation(
        snapshot: CodexContinuationSnapshot,
        payload_dict: dict[str, Any],
    ) -> list[Any] | None:
        current_input = payload_dict.get("input")
        if not isinstance(current_input, list) or not current_input:
            return None
        # M1: Uses shared fingerprinting utilities
        current_fingerprints = fingerprint_input_items(current_input)
        prior_fingerprints = snapshot.input_fingerprints
        if not prior_fingerprints:
            return None

        common_prefix_len = 0
        for prior, current in zip(
            prior_fingerprints, current_fingerprints, strict=False
        ):
            if prior != current:
                break
            common_prefix_len += 1

        if common_prefix_len < len(prior_fingerprints):
            return None
        if common_prefix_len <= 0 or common_prefix_len >= len(current_input):
            return None
        return list(current_input[common_prefix_len:])

    def _prune_continuation_bootstrap_fields(
        self, payload_dict: dict[str, Any]
    ) -> dict[str, Any]:
        """Remove continued-turn fields that Codex does not require."""
        pruned = dict(payload_dict)
        pruned.pop("stream", None)
        pruned.pop("background", None)
        if not self._preserve_tools_on_managed_ws_continuation:
            pruned.pop("tools", None)
        return pruned

    @staticmethod
    def _resolve_request_mode(
        *,
        transport: str,
        proxy_managed_previous_response_id: bool,
        has_previous_response_id: bool,
        has_continuation_snapshot: bool,
    ) -> str:
        if transport == "websocket":
            if proxy_managed_previous_response_id:
                return "ws_continuation_delta"
            if has_previous_response_id:
                return "ws_client_continuation"
            return "ws_bootstrap"
        if has_continuation_snapshot:
            return "http_full_replay"
        return "http_bootstrap"

    def _log_request_attempt(
        self,
        context: CodexRequestContext,
        payload_dict: dict[str, Any],
        *,
        mode: str,
        attempt: int,
        reason: str,
    ) -> None:
        if not logger.isEnabledFor(logging.INFO):
            return
        input_items = payload_dict.get("input")
        tools = payload_dict.get("tools")
        instructions = payload_dict.get("instructions")
        logger.info(
            "Submitting Codex request (mode=%s, reason=%s, attempt=%s).",
            mode,
            reason,
            attempt,
            extra={
                "backend": self._connector_transport_backend,
                "session_id": context.session_id,
                "model": context.effective_model,
                "attempt": attempt,
                "codex_transport": ("websocket" if self._use_websocket else "http_sse"),
                "continuation_mode": mode,
                "continuation_reason": reason,
                "has_previous_response_id": "previous_response_id" in payload_dict,
                "input_item_count": (
                    len(input_items) if isinstance(input_items, list) else 0
                ),
                "input_bytes": self._measure_json_bytes_for_log(input_items),
                "tools_count": len(tools) if isinstance(tools, list) else 0,
                "tools_bytes": self._measure_json_bytes_for_log(tools),
                "instructions_bytes": (
                    len(instructions.encode("utf-8"))
                    if isinstance(instructions, str)
                    else 0
                ),
            },
        )

    def _measure_json_bytes_for_log(self, value: Any) -> int | None:
        """Like ``_measure_json_bytes`` but skips huge lists used only for diagnostics."""
        if (
            isinstance(value, list)
            and len(value) > self._LOG_JSON_MEASURE_MAX_INPUT_ITEMS
        ):
            return None
        return self._measure_json_bytes(value)

    @staticmethod
    def _measure_json_bytes(value: Any) -> int:
        if value is None:
            return 0
        try:
            return len(
                json.dumps(
                    value,
                    sort_keys=True,
                    separators=(",", ":"),
                    default=json_default,
                ).encode("utf-8")
            )
        except (TypeError, ValueError):
            return 0

    @staticmethod
    def _json_default(value: Any) -> Any:
        # L1: Delegate to shared utility
        return json_default(value)

    def _detect_incompatible_tool_calls(
        self,
        response_like: object,
        context: CodexRequestContext,
    ) -> list[str]:
        if not self._compatibility_layer:
            return []
        tool_calls = self._extract_tool_calls(response_like)
        if not tool_calls:
            return []
        return self._compatibility_layer.detect_incompatible_tool_calls(
            tool_calls,
            context,
        )

    def _append_incompatible_tool_retry_steering(
        self,
        payload_dict: dict[str, Any],
        incompatible_tools: list[str],
        context: CodexRequestContext,
    ) -> dict[str, Any]:
        if not self._compatibility_layer or not incompatible_tools:
            return payload_dict
        adapted = self._compatibility_layer.append_incompatible_tool_steering(
            dict(payload_dict),
            incompatible_tools,
            context,
        )
        return dict(adapted)

    @staticmethod
    def _coerce_stream_chunk_content(content: object) -> dict[str, Any] | None:
        model_dump = getattr(content, "model_dump", None)
        if callable(model_dump):
            dumped = model_dump(exclude_none=True)
            if isinstance(dumped, dict):
                return cast(dict[str, Any], dumped)
        if isinstance(content, Mapping):
            return dict(content)
        return None

    def _normalize_processed_stream_chunk(
        self, chunk: ProcessedResponse
    ) -> ProcessedResponse:
        metadata = dict(chunk.metadata)
        event_type = metadata.get("event_type")
        if not isinstance(event_type, str):
            return chunk

        content_dict = self._coerce_stream_chunk_content(chunk.content)
        if not content_dict:
            return chunk

        if event_type in ("response.done", "response.completed"):
            content_dict = {"type": "response.completed", "response": content_dict}
        elif event_type == "error":
            content_dict = {"type": "error", **content_dict}
        elif "choices" in content_dict or not str(
            content_dict.get("type") or ""
        ).startswith("response."):
            return chunk

        translation_service = getattr(self._base_connector, "translation_service", None)
        if translation_service is None:
            return chunk

        translated = translation_service.to_domain_stream_chunk(
            content_dict, "responses"
        )
        translated_content = self._coerce_stream_chunk_content(translated)
        if translated_content is None:
            return chunk

        if self._translated_chunk_contains_tool_call(
            translated_content
        ) or self._raw_responses_event_completes_tool_call(event_type, content_dict):
            metadata["tool_call_emitted"] = True
            if (
                not isinstance(metadata.get("finish_reason"), str)
                or not str(metadata.get("finish_reason")).strip()
            ):
                metadata["finish_reason"] = "tool_calls"

        return ProcessedResponse(
            content=translated_content,
            usage=chunk.usage,
            metadata=metadata,
        )

    @staticmethod
    def _raw_responses_event_completes_tool_call(
        event_type: str, content_dict: dict[str, Any]
    ) -> bool:
        if event_type == "response.function_call_arguments.done":
            call_id = content_dict.get("item_id") or content_dict.get("call_id")
            arguments = content_dict.get("arguments")
            return bool(call_id) and arguments is not None

        if event_type != "response.output_item.done":
            return False

        item = content_dict.get("item")
        if not isinstance(item, Mapping):
            return False

        item_type = item.get("type")
        return item_type in {"function_call", "custom_tool_call", "local_shell_call"}

    @staticmethod
    def _translated_chunk_contains_tool_call(content_dict: dict[str, Any]) -> bool:
        choices = content_dict.get("choices")
        if not isinstance(choices, list):
            return False
        for choice in choices:
            if not isinstance(choice, Mapping):
                continue
            finish_reason = choice.get("finish_reason")
            if finish_reason == "tool_calls":
                return True
            delta = choice.get("delta") or choice.get("message")
            if isinstance(delta, Mapping) and delta.get("tool_calls"):
                return True
        return False

    def _extract_terminal_response_id(self, chunk: ProcessedResponse) -> str | None:
        metadata = chunk.metadata
        event_type = metadata.get("event_type")
        is_terminal = bool(metadata.get("done")) or event_type in {
            "response.done",
            "response.completed",
        }
        response_id = self._extract_response_id(chunk)
        content = self._coerce_stream_chunk_content(chunk.content)
        if not content:
            return None

        if not is_terminal:
            finish_reason = None
            choices = content.get("choices")
            if isinstance(choices, list) and choices:
                first_choice = choices[0]
                if isinstance(first_choice, Mapping):
                    candidate_finish_reason = first_choice.get("finish_reason")
                    if isinstance(candidate_finish_reason, str):
                        finish_reason = candidate_finish_reason
            is_terminal = finish_reason == "stop" and response_id is not None
            if not is_terminal:
                return None

        return response_id

    def _extract_response_id(self, chunk: ProcessedResponse) -> str | None:
        content = self._coerce_stream_chunk_content(chunk.content)
        if not content:
            return None

        response_id = content.get("response_id")
        if isinstance(response_id, str) and response_id.strip():
            return response_id.strip()

        response_id = content.get("id")
        if isinstance(response_id, str) and response_id.strip():
            return response_id.strip()

        response = content.get("response")
        if isinstance(response, Mapping):
            nested_id = response.get("id")
            if isinstance(nested_id, str) and nested_id.strip():
                return nested_id.strip()

        return None

    def _is_previous_response_not_found_error(self, exc: Exception) -> bool:
        if isinstance(exc, HTTPException):
            return self._contains_previous_response_not_found(exc.detail)
        if isinstance(exc, LLMProxyError):
            return self._contains_previous_response_not_found(exc.details) or (
                "previous_response_not_found" in exc.message
            )
        return "previous_response_not_found" in str(exc)

    def _contains_previous_response_not_found(self, payload: Any) -> bool:
        if isinstance(payload, Mapping):
            code = payload.get("code")
            if code == "previous_response_not_found":
                return True
            for nested_key in ("error", "details", "detail", "metadata"):
                if (
                    nested_key in payload
                    and self._contains_previous_response_not_found(
                        payload.get(nested_key)
                    )
                ):
                    return True
            return False
        if isinstance(payload, str):
            return "previous_response_not_found" in payload
        return False

    @staticmethod
    def _extract_tool_calls(response_like: object) -> list[dict[str, object]]:
        if isinstance(response_like, Mapping):
            item = response_like.get("item")
            if isinstance(item, Mapping):
                return ResponseExecutor._extract_tool_calls(item)

            response_type = response_like.get("type")
            name = response_like.get("name")
            if (
                isinstance(response_type, str)
                and isinstance(name, str)
                and response_type.lower()
                in {"function_call", "custom_tool_call", "local_shell_call"}
                and name.strip()
            ):
                return [{"function": {"name": name.strip()}}]

            direct_calls = response_like.get("tool_calls")
            if isinstance(direct_calls, list):
                return [item for item in direct_calls if isinstance(item, dict)]

            output = response_like.get("output")
            if isinstance(output, list):
                output_tool_calls: list[dict[str, object]] = []
                for output_item in output:
                    output_tool_calls.extend(
                        ResponseExecutor._extract_tool_calls(output_item)
                    )
                if output_tool_calls:
                    return output_tool_calls

            choices = response_like.get("choices")
            if isinstance(choices, list):
                extracted: list[dict[str, object]] = []
                for choice in choices:
                    if not isinstance(choice, Mapping):
                        continue
                    for container_key in ("message", "delta"):
                        container = choice.get(container_key)
                        if not isinstance(container, Mapping):
                            continue
                        tool_calls = container.get("tool_calls")
                        if isinstance(tool_calls, list):
                            extracted.extend(
                                item for item in tool_calls if isinstance(item, dict)
                            )
                return extracted

        metadata = getattr(response_like, "metadata", None)
        if isinstance(metadata, Mapping):
            tool_calls = metadata.get("tool_calls")
            if isinstance(tool_calls, list):
                return [item for item in tool_calls if isinstance(item, dict)]
        content = getattr(response_like, "content", None)
        if content is not None and content is not response_like:
            return ResponseExecutor._extract_tool_calls(content)
        return []

    @staticmethod
    def _chunk_has_client_visible_output(chunk: ProcessedResponse) -> bool:
        if ResponseExecutor._extract_tool_calls(chunk):
            return True

        content = chunk.content
        if isinstance(content, str):
            return bool(content.strip())
        if isinstance(content, bytes):
            return bool(content.strip())
        if not isinstance(content, Mapping):
            return False
        output = content.get("output")
        if isinstance(output, list) and any(
            isinstance(item, Mapping) for item in output
        ):
            return True
        choices = content.get("choices")
        if not isinstance(choices, list):
            return False
        for choice in choices:
            if not isinstance(choice, Mapping):
                continue
            for container_key in ("delta", "message"):
                container = choice.get(container_key)
                if not isinstance(container, Mapping):
                    continue
                text = container.get("content")
                if isinstance(text, str) and text.strip():
                    return True
        return False

    def _refresh_headers_auth(
        self, headers: dict[str, str], conversation_id: str, session_id: str
    ) -> None:
        """Update headers in place with latest auth token.

        Args:
            headers: Headers dictionary to update
            conversation_id: Conversation identifier (from prompt_cache_key)
            session_id: Session identifier (proxy correlation ID)
        """
        fresh_headers = self._base_connector.get_headers() or {}
        for key, value in fresh_headers.items():
            headers[key] = value
        # Ensure conversation metadata stays aligned
        headers["conversation_id"] = conversation_id
        headers["session_id"] = conversation_id

    async def _handle_rate_limit_rotation(
        self,
        *,
        retry_after_seconds: float | None,
        session_id: str | None,
        upstream_codex_error: Mapping[str, Any] | None = None,
        response_headers: Mapping[str, Any] | None = None,
        managed_oauth_account_id: str | None = None,
    ) -> bool:
        rotate_method = getattr(
            self._base_connector,
            "_handle_rate_limit_rotation",
            None,
        )
        if callable(rotate_method):
            rl_kw: dict[str, Any] = {
                "session_id": session_id,
                "upstream_codex_error": upstream_codex_error,
                "response_headers": response_headers,
            }
            try:
                if (
                    "managed_oauth_account_id"
                    in inspect.signature(rotate_method).parameters
                ):
                    rl_kw["managed_oauth_account_id"] = managed_oauth_account_id
            except (TypeError, ValueError):
                pass
            result = rotate_method(retry_after_seconds, **rl_kw)
            rotated = await result if inspect.isawaitable(result) else bool(result)
            return bool(rotated)

        fallback_rotate = getattr(self._credential_manager, "handle_rate_limit", None)
        if not callable(fallback_rotate):
            return False

        fb_kw: dict[str, Any] = {
            "session_id": session_id,
            "upstream_codex_error": upstream_codex_error,
            "response_headers": response_headers,
        }
        try:
            if (
                "managed_oauth_account_id"
                in inspect.signature(fallback_rotate).parameters
            ):
                fb_kw["managed_oauth_account_id"] = managed_oauth_account_id
        except (TypeError, ValueError):
            pass
        result = fallback_rotate(retry_after_seconds, **fb_kw)
        rotated = await result if inspect.isawaitable(result) else bool(result)
        if rotated:
            new_token = self._credential_manager.get_access_token()
            if new_token and hasattr(self._base_connector, "api_key"):
                self._base_connector.api_key = new_token
            return True
        return False

    async def _handle_auth_failure_rotation(self, *, session_id: str | None) -> bool:
        rotate_method = getattr(
            self._base_connector,
            "_handle_auth_failure_rotation",
            None,
        )
        if callable(rotate_method):
            result = rotate_method(session_id=session_id)
            rotated = await result if inspect.isawaitable(result) else bool(result)
            return bool(rotated)

        fallback_rotate = getattr(self._credential_manager, "handle_auth_failure", None)
        if not callable(fallback_rotate):
            return False

        result = fallback_rotate(session_id=session_id)
        rotated = await result if inspect.isawaitable(result) else bool(result)
        if rotated:
            new_token = self._credential_manager.get_access_token()
            if new_token and hasattr(self._base_connector, "api_key"):
                self._base_connector.api_key = new_token
            return True
        return False

    async def _handle_forbidden_rotation(self, *, session_id: str | None) -> bool:
        rotate_method = getattr(
            self._base_connector,
            "_handle_forbidden_rotation",
            None,
        )
        if callable(rotate_method):
            result = rotate_method(session_id=session_id)
            rotated = await result if inspect.isawaitable(result) else bool(result)
            return bool(rotated)

        fallback_rotate = getattr(
            self._credential_manager,
            "handle_forbidden_rotation",
            None,
        )
        if not callable(fallback_rotate):
            return False

        result = fallback_rotate(session_id=session_id)
        rotated = await result if inspect.isawaitable(result) else bool(result)
        if rotated:
            new_token = self._credential_manager.get_access_token()
            if new_token and hasattr(self._base_connector, "api_key"):
                self._base_connector.api_key = new_token
            return True
        return False

    async def _mark_account_used(self) -> None:
        mark_used_method = getattr(self._credential_manager, "mark_account_used", None)
        if not callable(mark_used_method):
            return
        result = mark_used_method()
        if inspect.isawaitable(result):
            await result

    async def _effective_rate_limit_max_retries(self) -> int:
        fn = getattr(self._credential_manager, "effective_max_rate_limit_retries", None)
        if not callable(fn):
            return max(0, self._max_retries)
        out = fn(self._max_retries)
        if inspect.isawaitable(out):
            out = await out
        try:
            return max(0, int(cast(Any, out)))
        except (TypeError, ValueError):
            return max(0, self._max_retries)

    async def _notify_codex_quota_unrecovered(
        self,
        *,
        upstream_detail: Any,
        retry_after_seconds: float | None,
        pool_exhaustion_confirmed: bool = True,
    ) -> None:
        fn = getattr(
            self._credential_manager, "notify_codex_usage_limit_unrecovered", None
        )
        if not callable(fn):
            return
        res = fn(
            upstream_detail=upstream_detail,
            retry_after_seconds=retry_after_seconds,
            pool_exhaustion_confirmed=pool_exhaustion_confirmed,
        )
        if inspect.isawaitable(res):
            await res

    @staticmethod
    def _extract_retry_after_seconds(headers: Mapping[str, Any] | None) -> float | None:
        if not headers:
            return None
        retry_after: Any | None = None
        for key in ("Retry-After", "retry-after", "RETRY-AFTER"):
            if key in headers:
                retry_after = headers.get(key)
                break
        if retry_after is None:
            return None
        if isinstance(retry_after, int | float):
            return float(retry_after)
        if isinstance(retry_after, str):
            stripped = retry_after.strip()
            if stripped:
                try:
                    return float(stripped)
                except ValueError:
                    return None
        return None

    @staticmethod
    def _extract_retry_after_from_payload(payload: Any) -> float | None:
        if not isinstance(payload, Mapping):
            return None

        key_aliases = (
            "retry_after",
            "retry_after_seconds",
            "retryAfter",
            "retryAfterSeconds",
            "retry_after_ms",
            "retryAfterMs",
            "resets_in_seconds",
            "resetsInSeconds",
        )

        for key in key_aliases:
            if key not in payload:
                continue
            value = payload.get(key)
            parsed = None
            if isinstance(value, int | float):
                parsed = float(value)
            elif isinstance(value, str):
                stripped = value.strip()
                if stripped:
                    with contextlib.suppress(ValueError):
                        parsed = float(stripped)
            if parsed is None:
                continue
            if key.endswith(("_ms", "Ms")):
                parsed = parsed / 1000.0
            if parsed > 0:
                return parsed

        resets_at = payload.get("resets_at")
        if isinstance(resets_at, int | float) and float(resets_at) > 1_000_000_000:
            delta = float(resets_at) - time.time()
            if delta > 0:
                return delta

        for nested_key in ("details", "detail", "error", "metadata"):
            nested = payload.get(nested_key)
            if isinstance(nested, Mapping):
                nested_value = ResponseExecutor._extract_retry_after_from_payload(
                    nested
                )
                if nested_value is not None:
                    return nested_value

        return None

    def _should_retry_for_auth_error(self, chunk: ProcessedResponse | Any) -> bool:
        """Check if a streaming chunk indicates an authentication failure.

        This method preserves the original connector's heuristic-based detection
        to ensure streaming retry parity (Requirement 1.2, 7.2).

        Args:
            chunk: Processed response chunk

        Returns:
            True if authentication error detected
        """
        content = getattr(chunk, "content", None)
        if content is None:
            content = chunk

        if not isinstance(content, Mapping):
            return False

        # Primary signal: explicit error payload from translation layer
        error_flag = content.get("error")
        details = content.get("details")

        status = self._extract_status_code(
            details if isinstance(details, Mapping) else None
        )
        if status in {401, 403}:
            return True

        # Some payloads stash status inside nested metadata objects
        if isinstance(details, Mapping):
            metadata = details.get("metadata")
            if isinstance(metadata, Mapping):
                status = self._extract_status_code(metadata)
                if status in {401, 403}:
                    return True

        # Fall back to heuristics based on codes/messages
        code = None
        if details is not None:
            code = details.get("code")
        if code is None:
            code = content.get("code")

        def _is_auth_code(value: Any) -> bool:
            if not isinstance(value, str):
                return False
            lowered = value.lower()
            return any(
                token in lowered
                for token in (
                    "auth",
                    "unauthorized",
                    "invalid_token",
                    "invalid_api_key",
                    "token_expired",
                    "access_denied",
                )
            )

        if _is_auth_code(code):
            return True

        # Check error flag and message for auth-related keywords
        for candidate in (error_flag, content.get("message")):
            if isinstance(candidate, str):
                lowered = candidate.lower()
                if "401" in lowered or "403" in lowered or "unauthorized" in lowered:
                    return True
                if "token" in lowered and "expired" in lowered:
                    return True

        return False

    @staticmethod
    def _extract_status_code(payload: Mapping[str, Any] | None) -> int | None:
        """Extract HTTP status code from error payload.

        Args:
            payload: Error payload dictionary

        Returns:
            Status code or None
        """
        if not isinstance(payload, Mapping):
            return None
        for key in ("status", "status_code", "http_status", "code"):
            if key not in payload:
                continue
            value = payload.get(key)
            if isinstance(value, int):
                if 100 <= value <= 599:
                    return value
            elif isinstance(value, str):
                stripped = value.strip()
                if stripped.isdigit():
                    numeric = int(stripped)
                    if 100 <= numeric <= 599:
                        return numeric
        return None

    async def _wait_for_auth_retry_delay(self, attempt_index: int) -> None:
        """Sleep using the shared retry executor instead of direct asyncio.sleep."""
        delay_seconds = self._get_retry_delay(attempt_index)
        if delay_seconds <= 0:
            return

        state = {"scheduled": False}

        async def _wait_once() -> None:
            if not state["scheduled"]:
                state["scheduled"] = True
                raise _CodexRetryDelayError(delay_seconds)
            return None

        def _should_retry(error: Exception) -> bool:
            return isinstance(error, _CodexRetryDelayError)

        def _extract_delay(error: Exception) -> float | None:
            if isinstance(error, _CodexRetryDelayError):
                return error.delay_seconds
            return None

        await self._auth_retry_delay_executor.execute(
            _wait_once,
            should_retry=_should_retry,
            retry_after_extractor=_extract_delay,
        )

    def _get_retry_delay(self, attempt_index: int) -> float:
        """Get delay for retry attempt.

        Args:
            attempt_index: Zero-based attempt index

        Returns:
            Delay in seconds
        """
        if attempt_index < 0:
            return 0.0
        if not self._retry_backoff_seconds:
            return 0.0
        if attempt_index < len(self._retry_backoff_seconds):
            return self._retry_backoff_seconds[attempt_index]
        return self._retry_backoff_seconds[-1]

    def _get_account_id(self) -> str | None:
        """Get ChatGPT account ID from credentials.

        Returns:
            Account ID or None
        """
        # Try to get account ID from credential manager (preferred)
        account_id = self._credential_manager.get_account_id()
        return account_id

    def _select_renderer_key(self, capabilities: CodexClientCapabilities) -> str:
        """Select renderer key from capabilities.

        This matches the original connector's _select_renderer_key behavior.
        The capabilities should already have tool_text_format set from renderer_default
        during settings loading, so we use "none" as the fallback.

        Args:
            capabilities: Client capabilities with tool_text_format

        Returns:
            Renderer key string
        """
        # Get tool_text_format from capabilities
        # Note: capabilities.tool_text_format should already be set from renderer_default
        # during settings loading, so if it's None/empty, we fall back to "none"
        preferred = capabilities.tool_text_format or "none"
        preferred = preferred.strip()

        if not preferred:
            return "none"

        # Handle "default" and "inherit" as special values that mean "use default"
        # Since we don't have access to connector's _renderer_default here,
        # and capabilities should already have the correct value, we treat these as "none"
        if preferred.lower() in {"default", "inherit"}:
            return "none"

        return preferred
