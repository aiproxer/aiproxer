"""Backend completion flow orchestration service."""

from __future__ import annotations

import asyncio
import contextlib
import logging
import threading
import time
from collections.abc import AsyncIterator, Awaitable, Callable, Coroutine
from typing import Any, cast
from uuid import uuid4

from pydantic.types import JsonValue

from src.core.common.exceptions import (
    AuthenticationError,
    BackendError,
    LLMProxyError,
    NoForwardableContentError,
    NonForwardableEnforcementError,
    RateLimitExceededError,
    RoutingError,
    SessionCancelledError,
)
from src.core.common.session_key_resolver import (
    resolve_session_key_from_request_context,
)
from src.core.domain.b2bua_identity import B2buaIdentity
from src.core.domain.chat import CanonicalChatRequest, ChatMessage, ChatRequest
from src.core.domain.composite_routing import CompositeRoutePlan
from src.core.domain.request_context import RequestContext
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.domain.usage_canonical_record import CanonicalUsageRecord
from src.core.interfaces.backend_completion_collaborators import (
    IBackendAvailabilityChecker,
    IBackendInvoker,
    IBackendRequestPreparer,
    ICompletionSessionResolver,
    IFailureRecoveryExecutor,
    IUsageAccountingOrchestrator,
    IWireCaptureOrchestrator,
)
from src.core.interfaces.backend_completion_flow_interface import (
    IBackendCompletionFlow,
)
from src.core.interfaces.backend_work_guard_interface import IBackendWorkGuard
from src.core.interfaces.domain_entities_interface import ISession
from src.core.interfaces.exception_normalizer_interface import IExceptionNormalizer
from src.core.interfaces.non_forwardable_interface import (
    INonForwardableMessageEnforcer,
)
from src.core.interfaces.resilience_interface import IResilienceCoordinator
from src.core.interfaces.session_cancellation_coordinator_interface import (
    ISessionCancellationCoordinator,
)
from src.core.interfaces.stream_formatting_interface import IStreamFormattingService
from src.core.services.auxiliary_identity import (
    build_auxiliary_effective_session_id,
    derive_auxiliary_operation_key,
)
from src.core.services.b2bua_bleg_allocator_service import B2buaBlegAllocator
from src.core.services.boundary_validation import (
    log_boundary_validation_failure,
)
from src.core.services.composite_routing_state import (
    COMPOSITE_ROUTING_STATE_KEY,
    COMPOSITE_ROUTING_SURFACE_KEY,
    COMPOSITE_SELECTED_LEAF_IS_THINKER_KEY,
    COMPOSITE_SELECTED_LEAF_SELECTOR_KEY,
    INTERLEAVED_THINKING_SUPPRESS_THINKER_SELECTION_KEY,
    INTERLEAVED_THINKING_WEIGHTED_CYCLE_STATE_KEY,
    PARALLEL_COMPLETION_ACTIVE_KEY,
    contains_top_level_operator,
    resolve_composite_routing_surface,
)
from src.core.services.connector_invoker import ConnectorInvoker
from src.core.services.interleaved_thinking.output_recorder import (
    InterleavedThinkingOutputRecorder,
)
from src.core.services.interleaved_thinking.state_utils import request_id, session_id
from src.core.services.interleaved_thinking.transformer import (
    INTERLEAVED_THINKING_ACTIVE_KEY,
    INTERLEAVED_THINKING_DIAGNOSTIC_KEY,
    INTERLEAVED_THINKING_SUPPRESS_MEMO_INJECTION_KEY,
    InterleavedThinkingRequestTransformer,
)
from src.core.services.parallel_completion_orchestrator import (
    CallCompletionFn,
    ParallelCompletionOrchestrator,
    ParallelCompletionResult,
    try_parse_parallel_plan,
)
from src.core.services.resilience.scope import (
    build_resilience_error_context,
    build_resilience_instance_id,
)
from src.core.services.streaming.chunk_normalizer import (
    normalize_to_processed_chunk_content,
)
from src.core.services.streaming.stream_recovery_budget import (
    get_or_init_stream_recovery_budget,
)

# Import EoS adapter (optional dependency)
try:
    from src.core.services.backend_completion_flow.eos_adapter import (
        BackendCompletionFlowEosAdapter,
    )
except ImportError:
    BackendCompletionFlowEosAdapter = None  # type: ignore[assignment, misc]

logger = logging.getLogger(__name__)


class BackendCompletionFlow(IBackendCompletionFlow):
    """Orchestrates backend completion requests with failover, retry, and observability.

    This coordinator delegates substantial logic to focused collaborators:
    - BackendAvailabilityChecker: Availability gating (disabled backends, resilience checks)
    - CompletionSessionResolver: Session resolution and per-session backend selection
    - BackendRequestPreparer: Request preparation, config application, and target synchronization
    - BackendManager: Backend instance acquisition and lifecycle management
    - WireCaptureOrchestrator: Wire capture orchestration (outbound/inbound/errors)
    - UsageAccountingOrchestrator: Usage tracking, response wrapping, and accounting
    - FailureRecoveryExecutor: Failure recovery, retry, and failover execution

    The orchestrator owns flow ordering and shared context only. All substantial logic
    is delegated to collaborators to maintain clear boundaries and improve testability.

    Exception Handling Flow:
        Errors are handled in two layers:
        1. Inner exception handler: Catches exceptions from backend calls. When allow_failover=False,
           calls record_failure() and marks the exception to prevent double-processing.
        2. Outer exception handler: Catches exceptions that escape the inner handler (e.g., from
           apply_failure_recovery failures, auth failures that raise immediately). Calls record_failure()
           unless the exception is already marked as handled.

        The marker pattern (__handled_by_inner_handler__) prevents double-calling record_failure()
        when an exception propagates from inner to outer handler. This is necessary because:
        - When allow_failover=False, we call record_failure() in the inner handler before raising
        - The raised exception is caught by the outer handler
        - Without the marker, record_failure() would be called twice for the same error

    Raises:
        BackendError: If backend call fails and recovery is not possible
        RateLimitExceededError: If backend is rate limited
        AuthenticationError: If authentication fails
    """

    def __init__(
        self,
        availability_checker: IBackendAvailabilityChecker,
        request_preparer: IBackendRequestPreparer,
        session_resolver: ICompletionSessionResolver,
        backend_invoker: IBackendInvoker,
        failover_executor: IFailureRecoveryExecutor,
        wire_capture_orchestrator: IWireCaptureOrchestrator,
        usage_accounting_orchestrator: IUsageAccountingOrchestrator,
        exception_normalizer: IExceptionNormalizer,
        stream_formatting_service: IStreamFormattingService,
        connector_invoker: ConnectorInvoker,
        resilience_coordinator: IResilienceCoordinator | None = None,
        eos_adapter: BackendCompletionFlowEosAdapter | None = None,  # type: ignore[invalid-type-form]
        cancellation_coordinator: ISessionCancellationCoordinator | None = None,
        backend_work_guard: IBackendWorkGuard | None = None,
        non_forwardable_enforcer: INonForwardableMessageEnforcer | None = None,
        b2bua_bleg_allocator: B2buaBlegAllocator | None = None,
        interleaved_thinking_transformer: (
            InterleavedThinkingRequestTransformer | None
        ) = None,
        interleaved_thinking_output_recorder: (
            InterleavedThinkingOutputRecorder | None
        ) = None,
    ) -> None:
        """Initialize the completion flow orchestrator."""
        self._availability_checker = availability_checker
        self._request_preparer = request_preparer
        self._session_resolver = session_resolver
        self._backend_invoker = backend_invoker
        self._failover_executor = failover_executor
        self._wire_capture_orchestrator = wire_capture_orchestrator
        self._usage_accounting = usage_accounting_orchestrator

        # Store dependencies needed for local logic
        self._resilience = resilience_coordinator
        self._exception_normalizer = exception_normalizer
        self._stream_formatting_service = stream_formatting_service
        self._eos_adapter = eos_adapter  # type: ignore[reportUnknownVariableType]
        self._cancellation_coordinator = cancellation_coordinator
        self._backend_work_guard = backend_work_guard
        self._non_forwardable_enforcer = non_forwardable_enforcer
        self._connector_invoker = connector_invoker
        self._b2bua_bleg_allocator = b2bua_bleg_allocator
        self._interleaved_thinking_transformer = interleaved_thinking_transformer
        self._interleaved_thinking_output_recorder = (
            interleaved_thinking_output_recorder
        )
        self._parallel_orchestrator = ParallelCompletionOrchestrator()
        # Track cancellation tasks to prevent resource leaks
        self._cancellation_tasks: set[asyncio.Task[None]] = set()
        self._cancellation_tasks_lock = threading.Lock()

    def _attach_resilience_context(
        self,
        error: Exception,
        backend_type: str,
        context: RequestContext | None,
    ) -> None:
        """Attach resilience metadata to the error for handler decisions."""
        if getattr(error, "__resilience_context__", None) is not None:
            return
        error.__resilience_context__ = build_resilience_error_context(  # type: ignore[attr-defined]
            backend_type, context
        )

    def _record_failure(
        self,
        backend_type: str,
        effective_model: str,
        error: Exception,
        context: RequestContext | None,
    ) -> None:
        """Record failure with scoped instance id and attached context."""
        if not self._resilience:
            return
        instance_id = build_resilience_instance_id(backend_type, context)
        self._attach_resilience_context(error, backend_type, context)
        self._resilience.record_failure(instance_id, effective_model, error)

    def _cancellation_tasks_lock_sync_discard(self, task: asyncio.Task[None]) -> None:
        """Synchronous discard for task done callback.

        Thread-safe: Uses the cancellation tasks lock to prevent races with
        cleanup operations and other threads.
        """
        with self._cancellation_tasks_lock:
            self._cancellation_tasks.discard(task)

    def _normalize_backend_exception(
        self, exc: Exception, backend_type: str
    ) -> Exception:
        candidate = self._exception_normalizer.normalize(exc, backend_type)

        if isinstance(candidate, LLMProxyError):
            # Preserve status_code from original exception if candidate doesn't have one
            if (
                not hasattr(candidate, "status_code")
                or getattr(candidate, "status_code", None) is None
            ):
                original_status_code = getattr(exc, "status_code", None)
                if isinstance(original_status_code, int):
                    candidate.status_code = original_status_code
            return candidate

        if isinstance(getattr(candidate, "status_code", None), int) and not isinstance(
            candidate, LLMProxyError
        ):
            # Fallback: ensure framework/transport exceptions (e.g. HTTPException) are
            # translated into domain errors even if an injected normalizer is mocked or
            # otherwise fails to translate them.
            from src.core.services.exception_normalizer import ExceptionNormalizer

            fallback_candidate = ExceptionNormalizer().normalize(
                candidate, backend_type
            )
            if isinstance(fallback_candidate, LLMProxyError):
                return fallback_candidate

        return candidate

    @staticmethod
    def _exception_from_streaming_error_envelope(
        result: StreamingResponseEnvelope,
    ) -> Exception | None:
        """Convert terminal streaming error envelopes back into domain errors."""
        status_code = getattr(result, "status_code", None)
        if not isinstance(status_code, int) or status_code < 400:
            return None

        metadata = getattr(result, "metadata", None)
        error_message = f"Backend returned {status_code} error"
        error_type = ""
        error_code = ""
        error_details: dict[str, Any] = {}

        if isinstance(metadata, dict):
            raw_message = metadata.get("error_message")
            if isinstance(raw_message, str) and raw_message.strip():
                error_message = raw_message

            raw_type = metadata.get("error_type")
            if isinstance(raw_type, str):
                error_type = raw_type

            raw_code = metadata.get("error_code")
            if isinstance(raw_code, str):
                error_code = raw_code

            raw_details = metadata.get("error_details")
            if isinstance(raw_details, dict):
                error_details = dict(raw_details)

        if not error_code and status_code >= 400:
            # Preserve the HTTP status in the OpenAI-compatible error payload
            # when the upstream stream supplied no provider-specific code.
            error_code = str(status_code)

        if status_code == 401:
            return AuthenticationError(error_message)

        if status_code == 404 or error_type == "RoutingError":
            return RoutingError(
                message=error_message,
                details=error_details,
                code=error_details.get("code") or error_code or "unknown_model",
            )

        return BackendError(
            message=error_message,
            status_code=status_code,
            details=error_details,
            code=error_code or None,
        )

    async def _enforce_non_forwardable_content(
        self,
        session_id: str,
        canonical_request: CanonicalChatRequest,
        domain_request: CanonicalChatRequest,
        context: RequestContext | None,
        backend_type: str,
    ) -> tuple[CanonicalChatRequest, CanonicalChatRequest]:
        """Apply non-forwardable message filtering."""
        if self._non_forwardable_enforcer is None:
            return canonical_request, domain_request

        try:
            filtered_messages, filtered_count = (
                await self._non_forwardable_enforcer.filter_messages(
                    session_id=session_id,
                    messages=domain_request.messages,
                    context=context,
                )
            )

            # Update both canonical_request and domain_request with filtered messages
            canonical_request = canonical_request.model_copy(
                update={"messages": filtered_messages}
            )
            domain_request = domain_request.model_copy(
                update={"messages": filtered_messages}
            )

            # Log filtering decision if messages were filtered (requirement 6.1, 6.2)
            if filtered_count > 0 and logger.isEnabledFor(logging.INFO):
                log_extra = {
                    "session_id": session_id,
                    "filtered_count": filtered_count,
                    "remaining_count": len(filtered_messages),
                }
                # Include request correlation identifier if available (requirement 6.1)
                if context and context.request_id:
                    log_extra["request_id"] = context.request_id
                logger.info(
                    "Filtered non-forwardable messages from backend request",
                    extra=log_extra,
                )

            return canonical_request, domain_request

        except (NoForwardableContentError, NonForwardableEnforcementError) as e:
            # Fail closed: do not proceed with backend call
            raise BackendError(
                message=f"Non-forwardable message enforcement failed: {e!s}",
                backend_name=backend_type,
            ) from e

    async def _handle_streaming_response(
        self,
        result: StreamingResponseEnvelope,
        backend_type: str,
        effective_model: str,
        context: RequestContext | None,
        domain_request: CanonicalChatRequest,
        session_id_for_backend: str | None,
        session_key: Any | None,
    ) -> StreamingResponseEnvelope:
        """Handle streaming response with wire capture and session ID injection."""
        # Wire-capture: capture inbound stream
        key_name = self._wire_capture_orchestrator.detect_key_name(backend_type)
        session_id = getattr(context, "session_id", None)

        if result.content is not None:
            capture_metadata = self._build_capture_metadata(
                context=context,
                status_code=result.status_code,
                headers=result.headers,
                response_metadata=result.metadata,
            )

            # Adapt domain stream to bytes for capture
            byte_stream = self._stream_formatting_service.stream_as_sse_bytes(
                result.content
            )
            wrapped_stream = self._wire_capture_orchestrator.wrap_inbound_stream(
                context=context,
                session_id=session_id,
                backend_type=backend_type,
                effective_model=effective_model,
                key_name=key_name,
                stream=byte_stream,
                capture_metadata=capture_metadata,
            )

            # Convert back to ProcessedResponse stream for adapters
            async def _to_processed_with_capture() -> Any:
                import json as _json_mod

                from src.core.interfaces.response_processor_interface import (
                    ProcessedResponse,
                )

                def _split_sse_frames(payload: bytes) -> list[bytes]:
                    """Split possibly coalesced SSE payload into frame-sized chunks.

                    Some transports/coordinators may coalesce multiple SSE events into a
                    single bytes chunk. Downstream decoders in the response adapter
                    expect one SSE frame at a time; passing coalesced payloads can cause
                    JSON parsing to fail and produce synthetic empty assistant chunks.
                    """
                    if not payload:
                        return []

                    normalized = payload.replace(b"\r\n", b"\n")
                    if b"\n\n" not in normalized:
                        return [payload]

                    frames: list[bytes] = []
                    parts = normalized.split(b"\n\n")
                    for part in parts:
                        if not part.strip():
                            continue
                        frames.append(part + b"\n\n")

                    return frames or [payload]

                async for b in wrapped_stream:
                    for frame in _split_sse_frames(b):
                        # Normalize bytes to ProcessedChunkContent before wrapping
                        normalized_content = normalize_to_processed_chunk_content(frame)

                        # Extract metadata from SSE bytes to preserve model info
                        extracted_metadata: dict[str, Any] = {}
                        if session_id:
                            extracted_metadata["session_id"] = session_id
                            extracted_metadata["stream_id"] = session_id

                        # Try to parse SSE data and extract metadata
                        try:
                            text = frame.decode("utf-8", errors="replace")
                            stripped = text.strip()
                            if stripped == "data:" or stripped == "data: ":
                                extracted_metadata["_keepalive"] = True
                                extracted_metadata["model"] = effective_model
                            elif stripped.startswith("data: "):
                                json_part = stripped[6:].strip()
                                if json_part and json_part != "[DONE]":
                                    try:
                                        parsed = _json_mod.loads(json_part)
                                        if isinstance(parsed, dict):
                                            for key in (
                                                "id",
                                                "model",
                                                "created",
                                            ):
                                                if key in parsed:
                                                    extracted_metadata[key] = parsed[
                                                        key
                                                    ]
                                    except _json_mod.JSONDecodeError:
                                        pass
                        except (UnicodeDecodeError, AttributeError):
                            pass

                        yield ProcessedResponse(
                            content=normalized_content,
                            metadata=extracted_metadata,
                        )

            result.content = _to_processed_with_capture()  # type: ignore[assignment]

        streaming_result = await self._usage_accounting.handle_streaming_response(
            result=result,
            backend_type=backend_type,
            effective_model=effective_model,
            context=context,
            request=domain_request,
            session_id_for_backend=session_id_for_backend,
            key_name=key_name,
        )

        # Check cancellation status before returning
        if (
            self._cancellation_coordinator is not None
            and session_key is not None
            and self._cancellation_coordinator.is_cancelled(session_key)
        ):
            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    "Backend call completed but session was cancelled - "
                    "treating streaming result as non-deliverable",
                    extra={"session_key": session_key.primary_id},
                )
            raise SessionCancelledError(session_key=session_key)

        return streaming_result

    async def _handle_non_streaming_response(
        self,
        result: ResponseEnvelope,
        backend_type: str,
        effective_model: str,
        context: RequestContext | None,
        session_id_for_backend: str | None,
        session_key: Any | None,
    ) -> ResponseEnvelope:
        """Handle non-streaming response with wire capture and usage recording."""
        # Wire-capture: capture inbound response
        key_name = self._wire_capture_orchestrator.detect_key_name(backend_type)
        # Serialize content for capture (best effort)
        response_content: Any = result
        if hasattr(result, "model_dump") and not isinstance(result, dict):
            response_content = result.model_dump()  # type: ignore[attr-defined]
        elif hasattr(result, "__dict__"):
            response_content = result.__dict__

        # Extract canonical usage from envelope if present
        canonical_usage_for_capture: CanonicalUsageRecord | None = None
        if hasattr(result, "canonical_usage") and result.canonical_usage is not None:
            canonical_usage_for_capture = result.canonical_usage

        capture_metadata = self._build_capture_metadata(
            context=context,
            status_code=result.status_code,
            headers=result.headers,
            response_metadata=result.metadata,
        )

        await self._wire_capture_orchestrator.capture_inbound_response(
            context=context,
            session_id=getattr(context, "session_id", None),
            backend_type=backend_type,
            effective_model=effective_model,
            key_name=key_name,
            response_content=response_content,  # type: ignore[reportUnknownArgumentType]
            canonical_usage=canonical_usage_for_capture,
            capture_metadata=capture_metadata,
        )

        # Check cancellation status before returning
        if (
            self._cancellation_coordinator is not None
            and session_key is not None
            and self._cancellation_coordinator.is_cancelled(session_key)
        ):
            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    "Backend call completed but session was cancelled - "
                    "treating non-streaming result as non-deliverable",
                    extra={"session_key": session_key.primary_id},
                )
            raise SessionCancelledError(session_key=session_key)

        return await self._usage_accounting.handle_non_streaming_response(
            result=result,
            backend_type=backend_type,
            effective_model=effective_model,
            session_id_for_backend=session_id_for_backend,
            context=context,
        )

    @classmethod
    def _strip_client_reasoning_content_from_request(
        cls,
        request: CanonicalChatRequest,
        context: RequestContext | None,
    ) -> CanonicalChatRequest:
        if (
            not cls._is_interleaved_composite_attempt(context)
            or context is None
            or not bool(context.extensions.get(COMPOSITE_SELECTED_LEAF_IS_THINKER_KEY))
        ):
            return request

        messages: list[ChatMessage] = []
        stripped_count = 0
        dropped_count = 0
        for message in request.messages:
            if message.reasoning_content is None:
                messages.append(message)
                continue

            stripped_count += 1
            stripped_message = message.model_copy(update={"reasoning_content": None})
            if cls._is_empty_reasoning_only_message(stripped_message):
                dropped_count += 1
                continue
            messages.append(stripped_message)

        if stripped_count <= 0:
            return request
        if logger.isEnabledFor(logging.INFO):
            logger.info(
                "Stripped client-carried reasoning_content from interleaved "
                "backend request: request_id=%s session_id=%s stripped_messages=%d "
                "dropped_empty_messages=%d",
                request_id(context),
                session_id(context, None),
                stripped_count,
                dropped_count,
            )
        return request.model_copy(update={"messages": messages})

    @staticmethod
    def _is_empty_reasoning_only_message(message: ChatMessage) -> bool:
        content = message.content
        has_content = False
        if isinstance(content, str):
            has_content = bool(content.strip())
        elif content is not None:
            has_content = True
        return (
            not has_content
            and not message.tool_calls
            and not message.tool_call_id
            and not message.name
        )

    @staticmethod
    def _is_interleaved_composite_attempt(context: RequestContext | None) -> bool:
        if context is None:
            return False
        return COMPOSITE_SELECTED_LEAF_IS_THINKER_KEY in context.extensions

    @staticmethod
    def _extract_retry_after(headers: dict[str, str] | None) -> float | None:
        if not headers:
            return None
        value = headers.get("Retry-After") or headers.get("retry-after")
        if not value:
            return None
        try:
            return float(value)
        except (TypeError, ValueError):
            return None

    @staticmethod
    def _initialize_retry_metadata(context: RequestContext) -> None:
        extensions = context.extensions
        if "retry_attempt" not in extensions:
            extensions["retry_attempt"] = 0
        if "is_retry" not in extensions:
            extensions["is_retry"] = False

    @staticmethod
    def _set_composite_routing_surface(context: RequestContext) -> None:
        surface = resolve_composite_routing_surface(context)
        context.extensions[COMPOSITE_ROUTING_SURFACE_KEY] = surface.value

    def _should_record_interleaved_thinking(
        self,
        context: RequestContext | None,
    ) -> bool:
        if self._interleaved_thinking_output_recorder is None or context is None:
            return False
        return bool(context.extensions.get(INTERLEAVED_THINKING_ACTIVE_KEY))

    def _should_stream_interleaved_thinking_to_client(
        self,
        context: RequestContext | None,
    ) -> bool:
        if not self._should_record_interleaved_thinking(context):
            return False
        assert self._interleaved_thinking_output_recorder is not None
        return self._interleaved_thinking_output_recorder.stream_to_client

    def _record_interleaved_thinking_streaming_response(
        self,
        *,
        result: StreamingResponseEnvelope,
        session: ISession | None,
        context: RequestContext | None,
        backend_type: str,
        effective_model: str,
    ) -> StreamingResponseEnvelope:
        if not self._should_record_interleaved_thinking(context):
            return result
        assert self._interleaved_thinking_output_recorder is not None
        return self._interleaved_thinking_output_recorder.wrap_streaming(
            response=result,
            session=session,
            context=context,
            backend_type=backend_type,
            effective_model=effective_model,
        )

    def _record_interleaved_thinking_non_streaming_response(
        self,
        *,
        result: ResponseEnvelope,
        session: ISession | None,
        context: RequestContext | None,
        backend_type: str,
        effective_model: str,
    ) -> None:
        if not self._should_record_interleaved_thinking(context):
            return
        assert self._interleaved_thinking_output_recorder is not None
        self._interleaved_thinking_output_recorder.capture_non_streaming(
            response=result,
            session=session,
            context=context,
            backend_type=backend_type,
            effective_model=effective_model,
        )

    @staticmethod
    async def _drain_streaming_response(result: StreamingResponseEnvelope) -> None:
        if result.content is None:
            return
        async for _item in result.content:
            pass

    def _drain_interleaved_thinking_stream_in_background(
        self,
        result: StreamingResponseEnvelope,
        context: RequestContext | None,
    ) -> None:
        async def _drain() -> None:
            await self._drain_streaming_response(result)

        task = asyncio.create_task(_drain())

        def _observe_completion(done: asyncio.Task[None]) -> None:
            if done.cancelled():
                return
            exception = done.exception()
            if exception is None:
                return
            logger.warning(
                "Background interleaved thinking stream drain failed: "
                "request_id=%s session_id=%s",
                request_id(context),
                session_id(context, None),
                exc_info=(type(exception), exception, exception.__traceback__),
            )

        task.add_done_callback(_observe_completion)

    @staticmethod
    def _build_interleaved_thinking_continuation_context(
        context: RequestContext | None,
        *,
        suppress_memo_injection: bool = False,
    ) -> RequestContext | None:
        if context is None:
            return None
        continuation = context.with_processing_context()
        continuation.extensions.pop(INTERLEAVED_THINKING_ACTIVE_KEY, None)
        continuation.extensions.pop(INTERLEAVED_THINKING_DIAGNOSTIC_KEY, None)
        continuation.extensions.pop(COMPOSITE_SELECTED_LEAF_SELECTOR_KEY, None)
        continuation.extensions[COMPOSITE_SELECTED_LEAF_IS_THINKER_KEY] = False
        continuation.extensions[INTERLEAVED_THINKING_SUPPRESS_THINKER_SELECTION_KEY] = (
            True
        )
        continuation.extensions.pop("resolved_uri_params", None)
        continuation.extensions.pop(COMPOSITE_ROUTING_STATE_KEY, None)
        if suppress_memo_injection:
            continuation.extensions[
                INTERLEAVED_THINKING_SUPPRESS_MEMO_INJECTION_KEY
            ] = True
        return continuation

    async def _continue_after_interleaved_thinking(
        self,
        *,
        request: CanonicalChatRequest,
        stream: bool,
        allow_failover: bool,
        context: RequestContext | None,
        suppress_memo_injection: bool = False,
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        return await self.call_completion(
            request=request,
            stream=stream,
            allow_failover=allow_failover,
            context=self._build_interleaved_thinking_continuation_context(
                context,
                suppress_memo_injection=suppress_memo_injection,
            ),
        )

    @staticmethod
    def _append_visible_interleaved_thinking_output(
        request: CanonicalChatRequest,
        memo: str,
    ) -> CanonicalChatRequest:
        normalized_memo = memo.strip()
        if not normalized_memo:
            return request
        messages = [
            *request.messages,
            ChatMessage(
                role="assistant",
                content=normalized_memo,
                metadata={
                    "source": "interleaved_thinking",
                    "kind": "visible_thinker_output",
                },
            ),
        ]
        return request.model_copy(update={"messages": messages})

    async def _build_visible_interleaved_thinking_stream(
        self,
        *,
        thinker_response: StreamingResponseEnvelope,
        request: CanonicalChatRequest,
        stream: bool,
        allow_failover: bool,
        context: RequestContext | None,
    ) -> StreamingResponseEnvelope:
        assert self._interleaved_thinking_output_recorder is not None
        output_recorder = self._interleaved_thinking_output_recorder
        queue: asyncio.Queue[Any] = asyncio.Queue()
        stream_finished = object()

        async def _produce_stream() -> None:
            visible_memo_parts: list[str] = []
            try:
                if thinker_response.content is not None:
                    async for item in output_recorder.sanitize_visible_stream(
                        thinker_response.content,
                        as_reasoning_content=True,
                    ):
                        extracted_text = output_recorder.extract_memo_text(item.content)
                        if extracted_text:
                            current_memo = "".join(visible_memo_parts)
                            if extracted_text != current_memo:
                                visible_memo_parts.append(extracted_text)
                        await queue.put(item)

                continuation_request = self._append_visible_interleaved_thinking_output(
                    request,
                    "".join(visible_memo_parts),
                )
                continuation = await self._continue_after_interleaved_thinking(
                    request=continuation_request,
                    stream=stream,
                    allow_failover=allow_failover,
                    context=context,
                    suppress_memo_injection=True,
                )
                if isinstance(continuation, StreamingResponseEnvelope):
                    if continuation.content is not None:
                        async for item in continuation.content:
                            await queue.put(item)
                    return

                from src.core.interfaces.response_processor_interface import (
                    ProcessedResponse,
                )

                await queue.put(
                    ProcessedResponse(
                        content=continuation.content,
                        usage=continuation.usage,
                        metadata=dict(continuation.metadata or {}),
                    )
                )
            except Exception as exc:
                await queue.put(exc)
            finally:
                await queue.put(stream_finished)

        producer_task = asyncio.create_task(_produce_stream())

        def _observe_producer_completion(task: asyncio.Task[None]) -> None:
            if task.cancelled():
                return
            exception = task.exception()
            if exception is not None:
                logger.warning(
                    "Visible interleaved thinking producer failed unexpectedly",
                    exc_info=(type(exception), exception, exception.__traceback__),
                )

        producer_task.add_done_callback(_observe_producer_completion)

        async def _combined_stream() -> AsyncIterator[Any]:
            try:
                while True:
                    item = await queue.get()
                    if item is stream_finished:
                        return
                    if isinstance(item, Exception):
                        raise item
                    yield item
            finally:
                if not producer_task.done():
                    producer_task.cancel()
                    with contextlib.suppress(asyncio.CancelledError, Exception):
                        await producer_task

        return StreamingResponseEnvelope(
            content=_combined_stream(),
            media_type=thinker_response.media_type,
            headers=thinker_response.headers,
            status_code=thinker_response.status_code,
            metadata=dict(thinker_response.metadata or {}),
        )

    async def _build_hidden_interleaved_thinking_stream(
        self,
        *,
        thinker_response: StreamingResponseEnvelope,
        request: CanonicalChatRequest,
        stream: bool,
        allow_failover: bool,
        context: RequestContext | None,
    ) -> StreamingResponseEnvelope:
        queue: asyncio.Queue[Any] = asyncio.Queue()
        stream_finished = object()

        async def _produce_stream() -> None:
            try:
                await self._drain_streaming_response(thinker_response)
                continuation = await self._continue_after_interleaved_thinking(
                    request=request,
                    stream=stream,
                    allow_failover=allow_failover,
                    context=context,
                )
                if isinstance(continuation, StreamingResponseEnvelope):
                    if continuation.content is not None:
                        async for item in continuation.content:
                            await queue.put(item)
                    return

                from src.core.interfaces.response_processor_interface import (
                    ProcessedResponse,
                )

                await queue.put(
                    ProcessedResponse(
                        content=continuation.content,
                        usage=continuation.usage,
                        metadata=dict(continuation.metadata or {}),
                    )
                )
            except Exception as exc:
                await queue.put(exc)
            finally:
                await queue.put(stream_finished)

        producer_task = asyncio.create_task(_produce_stream())

        def _observe_producer_completion(task: asyncio.Task[None]) -> None:
            if task.cancelled():
                return
            exception = task.exception()
            if exception is not None:
                logger.warning(
                    "Hidden interleaved thinking producer failed unexpectedly",
                    exc_info=(type(exception), exception, exception.__traceback__),
                )

        producer_task.add_done_callback(_observe_producer_completion)

        async def _combined_stream() -> AsyncIterator[Any]:
            try:
                while True:
                    item = await queue.get()
                    if item is stream_finished:
                        return
                    if isinstance(item, Exception):
                        raise item
                    yield item
            finally:
                if not producer_task.done():
                    producer_task.cancel()
                    with contextlib.suppress(asyncio.CancelledError, Exception):
                        await producer_task

        return StreamingResponseEnvelope(
            content=_combined_stream(),
            media_type=thinker_response.media_type,
            headers=thinker_response.headers,
            status_code=thinker_response.status_code,
            metadata=dict(thinker_response.metadata or {}),
        )

    async def _maybe_continue_after_interleaved_thinking_stream(
        self,
        *,
        handled_streaming_response: StreamingResponseEnvelope,
        attempt_context: RequestContext | None,
        original_client_request: CanonicalChatRequest,
        stream: bool,
        allow_failover: bool,
        context: RequestContext | None,
    ) -> StreamingResponseEnvelope | None:
        if not self._should_record_interleaved_thinking(attempt_context):
            return None
        if self._should_stream_interleaved_thinking_to_client(attempt_context):
            return await self._build_visible_interleaved_thinking_stream(
                thinker_response=handled_streaming_response,
                request=original_client_request,
                stream=stream,
                allow_failover=allow_failover,
                context=context,
            )
        return await self._build_hidden_interleaved_thinking_stream(
            thinker_response=handled_streaming_response,
            request=original_client_request,
            stream=stream,
            allow_failover=allow_failover,
            context=context,
        )

    @staticmethod
    async def _single_non_streaming_response_as_stream(
        response: ResponseEnvelope,
    ) -> AsyncIterator[Any]:
        from src.core.interfaces.response_processor_interface import ProcessedResponse

        yield ProcessedResponse(
            content=response.content,
            usage=response.usage,
            metadata=dict(response.metadata or {}),
        )

    @staticmethod
    def _coerce_canonical_request(
        request: ChatRequest,
        context: RequestContext | None,
    ) -> CanonicalChatRequest:
        # BOUNDARY HARDENING: Reject dict input - coercion must happen at adapter boundaries
        if isinstance(request, dict):
            from src.core.common.exceptions import InvalidRequestError

            log_boundary_validation_failure(
                logger=logger,
                message="BackendCompletionFlow received dict input. "
                "Dict-to-domain coercion is centralized at adapter boundaries (transport adapters). "
                "Expected ChatRequest or CanonicalChatRequest.",
                context=context,
                service="BackendCompletionFlow",
                violation_type="dict_input",
                details={
                    "received_type": "dict",
                    "expected_type": "ChatRequest | CanonicalChatRequest",
                },
            )

            raise InvalidRequestError(
                message="BackendCompletionFlow received dict input. "
                "Dict-to-domain coercion is centralized at adapter boundaries (transport adapters). "
                "Expected ChatRequest or CanonicalChatRequest.",
                details={
                    "received_type": "dict",
                    "service": "BackendCompletionFlow",
                },
            )

        return (
            request
            if isinstance(request, CanonicalChatRequest)
            else CanonicalChatRequest.model_validate(request.model_dump())
        )

    def _build_capture_metadata(
        self,
        *,
        context: RequestContext | None,
        status_code: int,
        headers: dict[str, str] | None,
        response_metadata: dict[str, JsonValue] | None = None,
    ) -> dict[str, JsonValue]:
        capture_metadata: dict[str, JsonValue] = {"status_code": status_code}
        retry_after = self._extract_retry_after(headers)
        if retry_after is not None:
            capture_metadata["retry_after_seconds"] = retry_after

        if context is not None:
            for key in (
                "account_id",
                "retry_attempt",
                "is_retry",
                "compression_correlation_id",
                "compression_records_count",
            ):
                if key in context.extensions:
                    capture_metadata[key] = context.extensions[key]

        if response_metadata is not None:
            for key in ("account_id", "retry_attempt", "is_retry"):
                if key in response_metadata:
                    capture_metadata[key] = response_metadata[key]

        return capture_metadata

    @staticmethod
    def _resolve_b2bua_attempt_reason(context: RequestContext | None) -> str:
        if context is None:
            return "initial"

        explicit_reason = context.extensions.get("b2bua_attempt_reason")
        if isinstance(explicit_reason, str) and explicit_reason.strip():
            return explicit_reason.strip()

        is_retry = context.extensions.get("is_retry")
        if isinstance(is_retry, bool) and is_retry:
            return "retry"

        return "initial"

    @staticmethod
    def _is_auxiliary_request(context: RequestContext | None) -> bool:
        if context is None:
            return False
        extensions = getattr(context, "extensions", None)
        if not isinstance(extensions, dict):
            return False
        return bool(extensions.get("auxiliary_request"))

    @classmethod
    def _refresh_auxiliary_effective_session_identity(
        cls,
        *,
        context: RequestContext | None,
        backend_type: str,
        effective_model: str,
    ) -> None:
        if context is None:
            return
        extensions = getattr(context, "extensions", None)
        if not isinstance(extensions, dict):
            return
        if not bool(extensions.get("auxiliary_request")):
            return

        root_session_id = extensions.get("auxiliary_root_session_id")
        if not isinstance(root_session_id, str) or not root_session_id.strip():
            context_session_id = getattr(context, "session_id", None)
            if isinstance(context_session_id, str) and context_session_id.strip():
                root_session_id = context_session_id
            else:
                return
        normalized_root_session_id = root_session_id.strip()

        purpose = extensions.get("auxiliary_purpose")
        if not isinstance(purpose, str) or not purpose.strip():
            purpose = f"{backend_type}:{effective_model}"
        normalized_purpose = purpose.strip()

        operation_key = extensions.get("auxiliary_operation_key")
        if not isinstance(operation_key, str) or not operation_key.strip():
            operation_key = derive_auxiliary_operation_key(
                context=context,
                request_data=None,
                purpose=normalized_purpose,
            )
        normalized_operation_key = operation_key.strip()

        retry_attempt_raw = extensions.get("retry_attempt")
        retry_attempt = 0
        if isinstance(retry_attempt_raw, int):
            retry_attempt = max(0, retry_attempt_raw)
        elif isinstance(retry_attempt_raw, str) and retry_attempt_raw.strip():
            try:
                retry_attempt = max(0, int(retry_attempt_raw.strip()))
            except ValueError:
                retry_attempt = 0
        auxiliary_attempt_ordinal = retry_attempt + 1

        extensions["auxiliary_root_session_id"] = normalized_root_session_id
        extensions["auxiliary_purpose"] = normalized_purpose
        extensions["auxiliary_operation_key"] = normalized_operation_key
        extensions["auxiliary_attempt_ordinal"] = auxiliary_attempt_ordinal
        extensions["auxiliary_effective_session_id"] = (
            build_auxiliary_effective_session_id(
                root_session_id=normalized_root_session_id,
                purpose=normalized_purpose,
                operation_key=normalized_operation_key,
                attempt_ordinal=auxiliary_attempt_ordinal,
            )
        )

    async def _allocate_b2bua_attempt_context(
        self,
        *,
        context: RequestContext | None,
        a_session_id: str | None,
        backend_type: str,
        effective_model: str,
    ) -> tuple[RequestContext | None, str | None]:
        identity = getattr(context, "b2bua_identity", None) if context else None
        if (
            context is None
            or not isinstance(identity, B2buaIdentity)
            or self._b2bua_bleg_allocator is None
        ):
            return context, None
        if self._is_auxiliary_request(context):
            return context, None

        resolved_a_session_id = a_session_id or identity.a_session_id
        if not resolved_a_session_id:
            return context, None

        try:
            reason = self._resolve_b2bua_attempt_reason(context)
            allocation = await self._b2bua_bleg_allocator.allocate(
                a_session_id=resolved_a_session_id,
                backend_type=backend_type,
                effective_model=effective_model,
                reason=reason,
            )
            attempt_context = context.with_b2bua_attempt_identity(
                b_session_id=allocation.b_session_id,
                b_seq=allocation.seq,
            )
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Allocated B2BUA backend attempt identity",
                    extra={
                        "a_session_id": resolved_a_session_id,
                        "b_session_id": allocation.b_session_id,
                        "b_seq": allocation.seq,
                        "backend_type": backend_type,
                        "effective_model": effective_model,
                        "reason": reason,
                    },
                )
            return attempt_context, allocation.b_session_id
        except Exception:
            # Fail-open: retain A-leg processing and avoid leaking A-leg id outbound.
            logger.warning(
                "Failed to allocate B2BUA attempt identity",
                exc_info=True,
                extra={
                    "a_session_id": resolved_a_session_id,
                    "backend_type": backend_type,
                    "effective_model": effective_model,
                },
            )
            return context, None

    async def _begin_call_completion(
        self,
        *,
        request: ChatRequest,
        context: RequestContext | None,
        stream: bool,
    ) -> tuple[CanonicalChatRequest, ParallelCompletionResult | None]:
        canonical_request = self._coerce_canonical_request(request, context)
        if context is not None:
            self._initialize_retry_metadata(context)
            self._set_composite_routing_surface(context)
        parallel_result = await self._maybe_execute_parallel_completion(
            request=canonical_request,
            context=context,
            stream=stream,
        )
        return canonical_request, parallel_result

    async def _maybe_execute_parallel_completion(
        self,
        *,
        request: CanonicalChatRequest,
        context: RequestContext | None,
        stream: bool,
    ) -> ParallelCompletionResult | None:
        if (
            context is not None
            and context.extensions.get(PARALLEL_COMPLETION_ACTIVE_KEY) is True
        ):
            return None

        special_route_session = await self._load_special_parallel_cycle_state(
            request=request,
            context=context,
        )
        parse_context = context
        if (
            context is not None
            and special_route_session is None
            and self._is_thinker_parallel_selector(request.model)
        ):
            # Keep special-route cycle mutations ephemeral when no durable session
            # is available to persist them.
            parse_context = context.with_processing_context()

        parallel_plan = try_parse_parallel_plan(request, parse_context)
        if parallel_plan is None:
            return None

        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Executing parallel composite completion: stream=%s selector=%s",
                stream,
                parallel_plan.normalized_selector,
            )

        try:
            return await self._execute_parallel_streaming_completion(
                plan=parallel_plan,
                request=request,
                context=context,
                stream=stream,
            )
        finally:
            self._persist_special_parallel_cycle_state(
                session=special_route_session,
                context=context,
            )

    async def _load_special_parallel_cycle_state(
        self,
        *,
        request: CanonicalChatRequest,
        context: RequestContext | None,
    ) -> ISession | None:
        if context is None or not self._is_thinker_parallel_selector(request.model):
            return None

        session, _session_id_for_backend = await self._session_resolver.resolve_session(
            context,
            request,
        )
        if session is None:
            return None

        cycle_state = getattr(
            session.state,
            "interleaved_thinking_weighted_cycle_state",
            None,
        )
        if isinstance(cycle_state, dict):
            context.extensions[INTERLEAVED_THINKING_WEIGHTED_CYCLE_STATE_KEY] = cast(
                JsonValue,
                cycle_state,
            )
        return session

    @staticmethod
    def _is_thinker_parallel_selector(model: str) -> bool:
        selector = model.strip()
        return (
            "[thinker]" in selector
            and contains_top_level_operator(selector, "^")
            and contains_top_level_operator(selector, "!")
        )

    @staticmethod
    def _persist_special_parallel_cycle_state(
        *,
        session: ISession | None,
        context: RequestContext | None,
    ) -> None:
        if session is None or context is None:
            return
        raw_cycle_state = context.extensions.get(
            INTERLEAVED_THINKING_WEIGHTED_CYCLE_STATE_KEY
        )
        if not isinstance(raw_cycle_state, dict):
            return
        next_state = session.state.with_interleaved_thinking_weighted_cycle_state(
            cast(dict[str, Any], raw_cycle_state)
        )
        session.update_state(next_state)

    async def _execute_parallel_streaming_completion(
        self,
        *,
        plan: CompositeRoutePlan,
        request: CanonicalChatRequest,
        context: RequestContext | None,
        stream: bool,
    ) -> ParallelCompletionResult:
        return await self._parallel_orchestrator.execute(
            plan=plan,
            request=request,
            context=context,
            stream=stream,
            call_completion=cast(CallCompletionFn, self.call_completion),
        )

    async def call_completion(
        self,
        request: ChatRequest,
        stream: bool = False,
        allow_failover: bool = True,
        context: RequestContext | None = None,
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        """Execute completion orchestration with failover, retry, and observability.

        Boundary Hardening (Requirement 5.2):
            This method enforces typed contract boundaries by rejecting dict inputs.
            Dict-to-contract coercion must occur at explicit adapter boundaries (transport
            adapters) before reaching this core orchestration service. This ensures a
            single canonical representation per concept throughout the core pipeline
            (Requirement 5.3).

        Args:
            request: Chat request contract (ChatRequest or CanonicalChatRequest).
                Dict inputs are rejected with InvalidRequestError.
            stream: Whether to stream the response
            allow_failover: Whether to allow failover to alternative backends
            context: Optional request context for correlation and metadata

        Returns:
            Response envelope (streaming or non-streaming) with completion result

        Raises:
            InvalidRequestError: If request is a dict. Dict-to-domain coercion is
                centralized at adapter boundaries (transport adapters). Expected
                ChatRequest or CanonicalChatRequest.
            BackendError: If backend call fails and recovery is not possible
            RateLimitExceededError: If backend is rate limited
            AuthenticationError: If authentication fails
        """
        canonical_request, parallel_result = await self._begin_call_completion(
            request=request,
            context=context,
            stream=stream,
        )
        if parallel_result is not None:
            return parallel_result
        original_client_request = canonical_request.model_copy(deep=True)

        # Step 1: Prepare request (resolve target + synchronize)
        target = await self._request_preparer.prepare_request(
            canonical_request, context
        )
        canonical_request = self._request_preparer.synchronize_request_with_target(
            canonical_request, target
        )
        canonical_request = self._strip_client_reasoning_content_from_request(
            canonical_request,
            context,
        )
        recovery_canonical_request = canonical_request.model_copy(deep=True)
        backend_type = target.backend
        effective_model = target.model
        uri_params = target.uri_params
        self._refresh_auxiliary_effective_session_identity(
            context=context,
            backend_type=backend_type,
            effective_model=effective_model,
        )

        # Step 2: Check if complex failover applies
        if allow_failover and await self._failover_executor.check_complex_failover(
            canonical_request, effective_model, backend_type, stream, context
        ):
            # Complex failover handled, return result
            return await self._failover_executor.execute_complex_failover(
                canonical_request,
                effective_model,
                backend_type,
                stream,
                self.call_completion,
                context,
            )

        # Step 3: Check backend availability (disabled + resilience)
        try:
            await self._availability_checker.check_backend_availability(
                backend_type, effective_model, allow_failover, context
            )
        except Exception as exc:
            # For streaming requests, prefer returning a terminal SSE error chunk
            # rather than raising and forcing the transport layer to emit a JSON error.
            # This matches OpenAI streaming behavior more closely and gives clients
            # actionable feedback instead of a generic "connection error".
            if stream:
                return await self._build_terminal_error_stream_envelope(
                    error=exc, provider=backend_type
                )
            raise

        # Step 4: Initialize failure strategy tracking
        attempt_start_time = time.time()
        recovery_budget = get_or_init_stream_recovery_budget(context)
        budget_start_time = (
            recovery_budget.budget_start_time
            if recovery_budget is not None
            else attempt_start_time
        )
        attempted_backends: list[str] = (
            recovery_budget.attempted_backends if recovery_budget is not None else []
        )
        current_backend = backend_type
        content_started = (
            bool(context.extensions.get("meaningful_output_emitted"))
            if context is not None
            else False
        )
        session_id_for_backend: str | None = None
        outbound_session_id: str | None = None
        attempt_context: RequestContext | None = context

        try:

            # Step 5: Resolve session
            (
                session,
                session_id_for_backend,
            ) = await self._session_resolver.resolve_session(context, canonical_request)

            # Ensure session_id is non-empty (Requirement 8.1)
            if not session_id_for_backend:
                session_id_for_backend = str(uuid4())
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Generated new session ID for backend call: %s",
                        session_id_for_backend,
                    )

            # Synchronize session_id back to context if it's missing (Requirement 8.1)
            if context and not context.session_id:
                context.session_id = session_id_for_backend

            # In B2BUA mode allocate per-attempt B-leg identity for outbound correlation
            (
                attempt_context,
                outbound_session_id,
            ) = await self._allocate_b2bua_attempt_context(
                context=context,
                a_session_id=session_id_for_backend,
                backend_type=backend_type,
                effective_model=effective_model,
            )
            if self._is_auxiliary_request(context):
                outbound_session_id = session_id_for_backend
            elif not isinstance(
                getattr(context, "b2bua_identity", None), B2buaIdentity
            ):
                # Legacy mode keeps the same outbound correlation semantics.
                outbound_session_id = session_id_for_backend

            attempt_identity = (
                getattr(attempt_context, "b2bua_identity", None)
                if attempt_context is not None
                else None
            )
            if isinstance(attempt_identity, B2buaIdentity) and logger.isEnabledFor(
                logging.INFO
            ):
                logger.info(
                    "Dispatching backend attempt",
                    extra={
                        "request_id": getattr(attempt_context, "request_id", None),
                        "backend_type": backend_type,
                        "effective_model": effective_model,
                        "a_session_id": attempt_identity.a_session_id,
                        "b_session_id": attempt_identity.b_session_id,
                        "b_seq": attempt_identity.b_seq,
                    },
                )

            # Step 6: Acquire backend instance
            backend = await self._backend_invoker.acquire_backend(
                backend_type, session_id_for_backend
            )

            # Step 7: Prepare backend request (configs + URI params)
            domain_request = await self._request_preparer.prepare_backend_request(
                canonical_request, backend_type, session, uri_params
            )

            if self._interleaved_thinking_transformer is not None:
                domain_request = self._interleaved_thinking_transformer.transform(
                    request=domain_request,
                    target=target,
                    session=session,
                    context=attempt_context,
                )
                canonical_request = domain_request

            # Preserve original canonical_request for verbatim token calculation
            # (before non-forwardable filtering modifies it)
            # Make an explicit copy to ensure it's not accidentally modified
            original_canonical_request = canonical_request.model_copy()

            # Step 7.5: Apply non-forwardable message filtering
            # This happens before wire capture
            # to ensure filtered messages are used for both capture and backend calls
            (
                canonical_request,
                domain_request,
            ) = await self._enforce_non_forwardable_content(
                session_id=session_id_for_backend,
                canonical_request=canonical_request,
                domain_request=domain_request,
                context=attempt_context,
                backend_type=backend_type,
            )

            # Step 8: Prepare wire capture context (identity + backend config)
            identity = (
                await self._wire_capture_orchestrator.prepare_wire_capture_context(
                    backend_type, session
                )
            )

            # Step 9: Execute backend call (with wire capture + usage tracking)
            result: ResponseEnvelope | StreamingResponseEnvelope | None = None
            try:
                # Wire-capture: capture outbound payload pre-call (best-effort)
                await self._wire_capture_orchestrator.capture_wire_outbound(
                    backend_type=backend_type,
                    effective_model=effective_model,
                    domain_request=domain_request,
                    context=attempt_context,
                )

                # Resolve SessionKey and enforce cancellation before backend call.
                if self._backend_work_guard is not None:
                    session_key = self._backend_work_guard.ensure_session_active(
                        context=attempt_context,
                        purpose="primary_completion",
                        require_scope=False,
                    )
                else:
                    session_key = resolve_session_key_from_request_context(
                        attempt_context
                    )
                    if (
                        self._cancellation_coordinator is not None
                        and session_key is not None
                    ):
                        self._cancellation_coordinator.ensure_not_cancelled(session_key)

                # Prepare backend call kwargs
                backend_call_kwargs = self._request_preparer.prepare_backend_kwargs(
                    session_id_for_backend=outbound_session_id,
                    session=session,
                    context=attempt_context,
                    backend_type=backend_type,
                )

                # Calculate outbound tokens and record usage
                # Use original_canonical_request for verbatim token calculation
                # (before non-forwardable filtering was applied)
                (
                    outbound_tokens,
                    ctp_record_id,
                    ptb_record_id,
                ) = await self._usage_accounting.calculate_and_record_usage(
                    domain_request=domain_request,
                    request=original_canonical_request,
                    backend_type=backend_type,
                    effective_model=effective_model,
                    session=session,
                    session_id_for_backend=session_id_for_backend,
                    context=attempt_context,
                )

                # Execute the backend call through ConnectorInvoker
                result = await self._connector_invoker.invoke(
                    backend=backend,
                    domain_request=domain_request,
                    canonical_request=canonical_request,
                    effective_model=effective_model,
                    identity=identity,
                    cancellation_token=session_key,
                    cancellation_coordinator=self._cancellation_coordinator,
                    context=attempt_context,
                    options=backend_call_kwargs,
                )

                if (
                    self._backend_work_guard is not None
                    and isinstance(result, StreamingResponseEnvelope)
                    and result.content is not None
                ):
                    result.content = (
                        self._backend_work_guard.wrap_stream_with_cancellation(
                            stream=result.content,
                            session_key=session_key,
                            purpose="primary_completion",
                        )
                    )

                # Register cancellable work if coordinator and session_key are available
                if (
                    self._cancellation_coordinator is not None
                    and session_key is not None
                ):
                    from src.core.interfaces.session_cancellation_coordinator_interface import (
                        ICancellable,
                    )

                    # Create cancellable wrapper for streaming responses
                    if (
                        isinstance(result, StreamingResponseEnvelope)
                        and result.cancel_callback is not None
                    ):

                        outer = self

                        class StreamingCancellable:
                            """Cancellable wrapper for streaming backend work."""

                            def __init__(
                                self,
                                cancel_callback: Callable[[], Awaitable[None]],
                            ):
                                self._cancel_callback = cancel_callback
                                self._cancelled: bool = False

                            def cancel(self) -> None:
                                """Cancel streaming backend work."""
                                if not self._cancelled:
                                    self._cancelled = True
                                    # Schedule cancellation callback execution
                                    import contextlib

                                    # Suppress RuntimeError when no event loop exists (intentionally silent control flow)
                                    with contextlib.suppress(RuntimeError):
                                        loop = asyncio.get_running_loop()
                                        # Call cancel_callback to get coroutine, then create task and track it
                                        coro = self._cancel_callback()
                                        # Type cast: cancel_callback returns Awaitable[None] but create_task expects Coroutine
                                        # In practice, create_task accepts coroutines which are a subtype of Awaitable
                                        coroutine: Coroutine[Any, Any, None] = cast(
                                            Coroutine[Any, Any, None], coro
                                        )
                                        task: asyncio.Task[None] = loop.create_task(
                                            coroutine
                                        )
                                        # Track task to prevent resource leaks
                                        with outer._cancellation_tasks_lock:
                                            outer._cancellation_tasks.add(task)
                                        # Remove task from set when done to prevent unbounded growth
                                        task.add_done_callback(
                                            outer._cancellation_tasks_lock_sync_discard
                                        )

                        cancellable: ICancellable = StreamingCancellable(
                            result.cancel_callback,
                        )
                        self._cancellation_coordinator.register_cancellable(
                            session_key, cancellable
                        )
                    # For non-streaming responses, HTTP calls are typically fast
                    # and gating prevents new calls, so we skip registration

                # Wrap result for usage tracking
                assert result is not None, "Backend call must return a response"
                result = await self._usage_accounting.wrap_response_for_usage(
                    result=result,
                    outbound_tokens=outbound_tokens,
                    ctp_record_id=ctp_record_id,
                    ptb_record_id=ptb_record_id,
                    start_time=attempt_start_time,
                    context=attempt_context,
                    backend_type=backend_type,
                    effective_model=effective_model,
                )

                # Step 10: Handle streaming response (wire capture + session ID injection)
                if isinstance(result, StreamingResponseEnvelope):
                    if allow_failover:
                        streaming_error = self._exception_from_streaming_error_envelope(
                            result
                        )
                        if streaming_error is not None:
                            raise streaming_error
                    result = self._record_interleaved_thinking_streaming_response(
                        result=result,
                        session=session,
                        context=attempt_context,
                        backend_type=backend_type,
                        effective_model=effective_model,
                    )
                    handled_streaming_response = await self._handle_streaming_response(
                        result=result,
                        backend_type=backend_type,
                        effective_model=effective_model,
                        context=attempt_context,
                        domain_request=domain_request,
                        session_id_for_backend=session_id_for_backend,
                        session_key=session_key,
                    )
                    interleaved_continuation = (
                        await self._maybe_continue_after_interleaved_thinking_stream(
                            handled_streaming_response=handled_streaming_response,
                            attempt_context=attempt_context,
                            original_client_request=original_client_request,
                            stream=stream,
                            allow_failover=allow_failover,
                            context=context,
                        )
                    )
                    if interleaved_continuation is not None:
                        return interleaved_continuation
                    return handled_streaming_response

                # Step 11: Handle non-streaming response
                # Wire-capture: capture inbound response
                self._record_interleaved_thinking_non_streaming_response(
                    result=result,
                    session=session,
                    context=attempt_context,
                    backend_type=backend_type,
                    effective_model=effective_model,
                )
                handled_non_streaming_response = (
                    await self._handle_non_streaming_response(
                        result=result,
                        backend_type=backend_type,
                        effective_model=effective_model,
                        context=attempt_context,
                        session_id_for_backend=session_id_for_backend,
                        session_key=session_key,
                    )
                )
                if self._should_record_interleaved_thinking(attempt_context):
                    return await self._continue_after_interleaved_thinking(
                        request=original_client_request,
                        stream=stream,
                        allow_failover=allow_failover,
                        context=context,
                    )
                return handled_non_streaming_response

            except asyncio.CancelledError:
                raise
            except SessionCancelledError:
                # Preserve SessionCancelledError - do not normalize
                raise
            except Exception as call_exc:
                # Normalize the exception immediately for consistent handling
                normalized_exc = self._normalize_backend_exception(
                    call_exc, backend_type
                )

                # Check if this is an authentication failure first
                is_auth_failure = False
                if isinstance(normalized_exc, AuthenticationError) or (
                    hasattr(normalized_exc, "status_code")
                    and getattr(normalized_exc, "status_code", None) == 401
                ):
                    is_auth_failure = True

                if is_auth_failure:
                    # Handle authentication failures with backend lifecycle side effects
                    await self._usage_accounting.handle_auth_failure(
                        normalized_exc,
                        backend,
                        backend_type,
                        session_id_for_backend,
                    )
                    raise normalized_exc

                # Handle backend error (wire capture + usage/resilience updates)
                # 1. Wire capture error
                key_name = self._wire_capture_orchestrator.detect_key_name(backend_type)
                error_payload: dict[str, Any]
                if isinstance(normalized_exc, LLMProxyError):
                    error_payload = normalized_exc.to_dict()
                    # Ensure status code is present if available
                    import contextlib

                    with contextlib.suppress(Exception):
                        if (
                            isinstance(error_payload.get("error"), dict)
                            and "status_code" not in error_payload["error"]
                        ):
                            error_payload["error"]["status_code"] = getattr(
                                normalized_exc, "status_code", None
                            )
                else:
                    error_payload = {
                        "error": {
                            "message": str(normalized_exc),
                            "type": type(normalized_exc).__name__,
                        }
                    }

                # Extract canonical usage from envelope if present (may be None for errors)
                canonical_usage_for_error: CanonicalUsageRecord | None = None
                if (
                    result is not None
                    and isinstance(result, ResponseEnvelope)
                    and result.canonical_usage is not None
                ):
                    canonical_usage_for_error = result.canonical_usage
                # Note: For error cases, canonical_usage may not be available

                await self._wire_capture_orchestrator.capture_inbound_response(
                    context=attempt_context,
                    session_id=getattr(attempt_context, "session_id", None),
                    backend_type=backend_type,
                    effective_model=effective_model,
                    key_name=key_name,
                    response_content=error_payload,
                    canonical_usage=canonical_usage_for_error,
                )

                # 2. Update resilience/usage via accounting collaborator
                await self._usage_accounting.handle_backend_error(
                    call_exc=call_exc,
                    backend_type=current_backend,
                    effective_model=effective_model,
                    context=context,
                    request=canonical_request,
                    backend=backend,
                    normalized_exc=normalized_exc,
                )

                # Record each failed attempt so routing/availability state is updated
                # even when failover succeeds and no error escapes this call.
                if self._resilience and not getattr(
                    normalized_exc, "__handled_by_inner_handler__", False
                ):
                    self._record_failure(
                        current_backend, effective_model, normalized_exc, context
                    )
                    normalized_exc.__handled_by_inner_handler__ = True  # type: ignore[attr-defined]

                # 3. Record EoS error termination signal (fail-open)
                if self._eos_adapter is not None:  # type: ignore[reportUnknownVariableType]
                    try:
                        await self._eos_adapter.record_error_termination(  # type: ignore[reportUnknownMemberType]
                            error=normalized_exc,
                            session_id=session_id_for_backend,
                            backend_type=current_backend,
                            context=context,
                        )
                        # Mark as handled to prevent outer handler from calling record_error_termination again.
                        # This marker is necessary because when allow_failover=False, we call
                        # record_error_termination here and then raise, which will be caught by the outer
                        # handler. Without this marker, record_error_termination would be called twice for
                        # the same error.
                        normalized_exc.__eos_recorded_by_inner_handler__ = True  # type: ignore[attr-defined]
                    except Exception as eos_error:
                        # Fail-open: log but don't interfere with error handling
                        logger.warning(
                            "Failed to record EoS error termination: %s",
                            eos_error,
                            exc_info=True,
                        )

                # Step 13: Apply failure recovery (retry/failover)
                if allow_failover:
                    return await self._failover_executor.apply_failure_recovery(
                        error=normalized_exc,
                        model=effective_model,
                        backend_type=current_backend,
                        attempted_backends=attempted_backends,
                        start_time=budget_start_time,
                        is_streaming=stream,
                        content_started=content_started,
                        request=recovery_canonical_request,
                        context=context,
                        call_completion_callback=self.call_completion,
                    )

                # No failover allowed - record failure and raise the normalized error
                if stream:
                    return await self._build_terminal_error_stream_envelope(
                        error=normalized_exc,
                        provider=current_backend,
                    )
                raise normalized_exc

        except asyncio.CancelledError:
            raise
        except SessionCancelledError:
            # Preserve SessionCancelledError - do not normalize
            raise
        except (
            BackendError,
            RateLimitExceededError,
            LLMProxyError,
            AuthenticationError,
        ) as exc:
            # Record failure in resilience coordinator (handles cooldown/backoff).
            # This outer handler catches exceptions that escape the inner handler:
            # 1. Exceptions from apply_failure_recovery when allow_failover=True
            # 2. Exceptions from auth failure handling (which raises immediately)
            # 3. Unexpected exceptions that bypass the inner handler
            # We skip if already handled in inner handler (when allow_failover=False)
            # to avoid double-calling record_failure for the same error.
            if self._resilience and not getattr(
                exc, "__handled_by_inner_handler__", False
            ):
                self._record_failure(backend_type, effective_model, exc, context)

            # Record EoS error termination signal (fail-open)
            # Skip if already recorded in inner handler (when allow_failover=False)
            # to avoid double-calling record_error_termination for the same error.
            if (
                self._eos_adapter is not None
                and not getattr(  # type: ignore[reportUnknownVariableType]
                    exc, "__eos_recorded_by_inner_handler__", False
                )
            ):
                try:
                    await self._eos_adapter.record_error_termination(  # type: ignore[reportUnknownMemberType]
                        error=exc,
                        session_id=session_id_for_backend,
                        backend_type=backend_type,
                        context=context,
                    )

                except Exception as eos_error:
                    # Fail-open: log but don't interfere with error handling
                    logger.warning(
                        "Failed to record EoS error termination: %s",
                        eos_error,
                        exc_info=True,
                    )

            if stream:
                return await self._build_terminal_error_stream_envelope(
                    error=exc,
                    provider=backend_type,
                )

            # Propagate expected exceptions as-is
            raise
        except Exception as exc:
            # Normalize any remaining "foreign" exception into a domain error to keep
            # transport/framework types out of the service boundary.
            normalized_exc = self._normalize_backend_exception(exc, backend_type)

            # Record EoS error termination signal (fail-open)
            # Skip if already recorded in inner handler (when allow_failover=False)
            # to avoid double-calling record_error_termination for the same error.
            if (
                self._eos_adapter is not None
                and not getattr(  # type: ignore[reportUnknownVariableType]
                    normalized_exc, "__eos_recorded_by_inner_handler__", False
                )
            ):
                try:
                    await self._eos_adapter.record_error_termination(  # type: ignore[reportUnknownMemberType]
                        error=normalized_exc,
                        session_id=session_id_for_backend,
                        backend_type=backend_type,
                        context=context,
                    )

                except Exception as eos_error:
                    # Fail-open: log but don't interfere with error handling
                    logger.warning(
                        "Failed to record EoS error termination: %s",
                        eos_error,
                        exc_info=True,
                    )

            if stream:
                return await self._build_terminal_error_stream_envelope(
                    error=normalized_exc,
                    provider=backend_type,
                )

            if isinstance(normalized_exc, LLMProxyError):
                raise normalized_exc from exc

            raise BackendError(
                message=f"Backend call failed: {exc!s}",
                backend_name=backend_type,
            ) from exc

    async def _build_terminal_error_stream_envelope(
        self, *, error: Exception, provider: str
    ) -> StreamingResponseEnvelope:
        """Return a streaming envelope that emits a single terminal error chunk."""
        import time

        from src.core.domain.responses import StreamingResponseEnvelope
        from src.core.interfaces.response_processor_interface import ProcessedResponse
        from src.core.services.streaming.error_mapping import handle_streaming_error

        normalized_error = self._normalize_backend_exception(error, provider)
        routing_details: dict[str, Any] = {}
        if isinstance(normalized_error, RoutingError):
            maybe_details = getattr(normalized_error, "details", None)
            if isinstance(maybe_details, dict):
                routing_details = maybe_details

        status_code = getattr(normalized_error, "status_code", None)
        if not isinstance(status_code, int):
            status_code = getattr(error, "status_code", None)
        if not isinstance(status_code, int):
            status_code = 500

        error_code = getattr(normalized_error, "code", None)
        if not error_code and routing_details:
            details_code = routing_details.get("code")
            if isinstance(details_code, str):
                error_code = details_code

        # Keep streaming and non-streaming HTTP mappings aligned for routing errors.
        if isinstance(normalized_error, RoutingError):
            details_code = routing_details.get("code")
            if details_code == "unknown_model":
                status_code = 404
            elif details_code == "unsupported_on_instance":
                status_code = 400
            elif details_code == "temporarily_unavailable":
                status_code = 503
            elif details_code == "policy_rejected":
                status_code = 403

        logger.warning(
            "Backend %s produced terminal HTTP %s: %s",
            provider,
            status_code,
            normalized_error,
        )

        async def _iterator():
            terminal_chunk = await handle_streaming_error(
                normalized_error,
                stream_id=None,
                provider=provider,
            )

            metadata = dict(getattr(terminal_chunk, "metadata", {}) or {})
            metadata.setdefault("finish_reason", "error")

            error_payload = metadata.get("error")
            if isinstance(error_payload, dict):
                if isinstance(error_code, str) and error_payload.get("code") in (
                    None,
                    "",
                    "unknown",
                ):
                    error_payload["code"] = error_code
                if "status_code" not in error_payload:
                    error_payload["status_code"] = status_code
                metadata["error"] = error_payload
            else:
                error_payload = {
                    "type": type(normalized_error).__name__,
                    "message": str(normalized_error),
                    "code": str(error_code or status_code),
                    "status_code": status_code,
                }
                metadata["error"] = error_payload

            content = getattr(terminal_chunk, "content", "")
            if not content:
                content = {
                    "id": f"chatcmpl-error-{int(time.time())}",
                    "object": "chat.completion.chunk",
                    "created": int(time.time()),
                    "model": provider,
                    "choices": [{"index": 0, "delta": {}, "finish_reason": "error"}],
                    "error": {
                        "type": error_payload.get("type")
                        or type(normalized_error).__name__,
                        "message": error_payload.get("message")
                        or str(normalized_error),
                        "code": error_payload.get("code")
                        or str(error_code or status_code),
                        "status_code": error_payload.get("status_code") or status_code,
                    },
                }

            yield ProcessedResponse(
                content=normalize_to_processed_chunk_content(content),
                metadata=metadata,
                usage=getattr(terminal_chunk, "usage", None),
            )

        return StreamingResponseEnvelope(
            content=_iterator(),
            status_code=status_code,
            metadata={
                "error_type": type(normalized_error).__name__,
                "error_message": str(normalized_error),
                "error_code": error_code,
                "error_details": routing_details,
            },
        )

    async def cleanup(self) -> None:
        """Clean up pending cancellation tasks to prevent resource leaks.

        This method cancels and awaits all pending cancellation tasks.
        Should be called during application shutdown to ensure all resources
        are properly released.
        """
        # Take snapshot of pending tasks
        with self._cancellation_tasks_lock:
            pending_tasks = [t for t in self._cancellation_tasks if not t.done()]
        if pending_tasks:
            try:
                # Cancel all pending tasks
                for task in pending_tasks:
                    if not task.done():
                        task.cancel()
                # Await cancelled tasks to ensure they complete
                # Note: gather() with return_exceptions=True shouldn't raise,
                # but RuntimeError can occur if event loop is closed
                await asyncio.gather(*pending_tasks, return_exceptions=True)
            except RuntimeError as err:
                # RuntimeError: event loop closed or other infrastructure issues
                logger.warning(
                    "Error during cancellation task cleanup (event loop issue): %s; suppressing to allow cleanup to continue",
                    err,
                    exc_info=True,
                    extra={"pending_tasks_count": len(pending_tasks)},
                )
            except Exception as err:
                # Defensive catch for any other unexpected errors during cleanup
                logger.warning(
                    "Unexpected error during cancellation task cleanup: %s (%s); suppressing to allow cleanup to continue",
                    err,
                    type(err).__name__,
                    exc_info=True,
                    extra={"pending_tasks_count": len(pending_tasks)},
                )
            finally:
                # Clear set to prevent memory leaks
                with self._cancellation_tasks_lock:
                    self._cancellation_tasks.clear()
