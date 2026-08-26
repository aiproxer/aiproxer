"""Usage accounting orchestration collaborator."""

from __future__ import annotations

import asyncio
import contextlib
import logging
import time
from typing import Any

from pydantic.types import JsonValue

from src.connectors.base import LLMBackend
from src.core.app.constants.logging_constants import TRACE_LEVEL
from src.core.common.exceptions import (
    APIConnectionError,
    APITimeoutError,
    AuthenticationError,
    BackendError,
    RateLimitExceededError,
)
from src.core.domain.b2bua_identity import B2buaIdentity
from src.core.domain.chat import CanonicalChatRequest, ChatRequest
from src.core.domain.request_context import RequestContext
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.domain.traffic_leg import TrafficLeg
from src.core.domain.translation_utils.processed_response_usage import (
    usage_summary_from_processed_response,
)
from src.core.domain.usage_canonical_record import UsageCompletionOutcome
from src.core.domain.usage_summary import UsageSummary
from src.core.interfaces.backend_completion_collaborators import (
    IUsageAccountingOrchestrator,
    IWireCaptureOrchestrator,
)
from src.core.interfaces.backend_factory_interface import IBackendFactory
from src.core.interfaces.backend_lifecycle_manager_interface import (
    IBackendLifecycleManager,
)
from src.core.interfaces.domain_entities_interface import ISession
from src.core.interfaces.planning_phase_manager_interface import IPlanningPhaseManager
from src.core.interfaces.resilience_interface import IResilienceCoordinator
from src.core.interfaces.stream_session_id_resolver_interface import (
    IStreamSessionIdResolver,
)
from src.core.interfaces.usage_normalization_service_interface import (
    IUsageNormalizationService,
)
from src.core.interfaces.usage_tracking_interface import IUsageTrackingService
from src.core.interfaces.usage_tracking_wrapper_interface import IUsageTrackingWrapper
from src.core.services.resilience.scope import build_resilience_instance_id
from src.core.services.streaming.chunk_normalizer import (
    normalize_to_processed_chunk_content,
)
from src.core.services.streaming.stream_recovery_budget import (
    get_or_init_stream_recovery_budget,
)
from src.core.utils.usage_recalculation import calculate_outbound_tokens

logger = logging.getLogger(__name__)


def _stream_emitted_meaningful_output(context: RequestContext | None) -> bool:
    """Return True when streaming already marked meaningful backend output."""
    if context is None:
        return False
    budget = get_or_init_stream_recovery_budget(context)
    return bool(budget is not None and budget.meaningful_output_emitted)


def _record_streaming_resilience_outcome(
    resilience: IResilienceCoordinator | None,
    *,
    instance_id: str | None,
    effective_model: str,
    completion_outcome: UsageCompletionOutcome | None,
    stream_error: BaseException | None,
    error_classification: str | None,
    context: RequestContext | None,
    backend_type: str,
) -> None:
    """Record circuit-breaker outcome for a finished stream (no awaits).

    Usage accounting may still classify client disconnect as incomplete; resilience
    treats disconnect-after-meaningful-output as success so half-open probes heal.
    """
    if resilience is None or not instance_id:
        return

    if completion_outcome == UsageCompletionOutcome.complete:
        resilience.record_success(instance_id, effective_model)
        return

    # Check if this was a cancellation or client disconnect
    if isinstance(stream_error, asyncio.CancelledError | GeneratorExit) or (
        stream_error is not None
        and type(stream_error).__name__ in ("SessionCancelledError", "CancelledError")
    ):
        resilience.release_circuit_breaker_probe(instance_id)
        return

    # Incomplete / disconnect with no explicit stream error: do not penalize.
    if stream_error is None and error_classification is None:
        if _stream_emitted_meaningful_output(context):
            resilience.record_success(instance_id, effective_model)
            return
        resilience.release_circuit_breaker_probe(instance_id)
        return

    failure_error: Exception
    if stream_error is None:
        failure_error = BackendError(
            message=(
                "Streaming terminated with error chunk"
                if error_classification
                else "Streaming terminated incompletely"
            ),
            backend_name=backend_type,
        )
    elif isinstance(stream_error, Exception):
        failure_error = stream_error
    else:
        # BaseException that is not an Exception (e.g. KeyboardInterrupt, SystemExit)
        return
    resilience.record_failure(instance_id, effective_model, failure_error)


def _to_usage_summary(usage: Any) -> UsageSummary | None:
    """Convert usage from various formats to UsageSummary.

    Args:
        usage: Usage data in various formats (dict, UsageSummary, StreamingUsage, etc.)

    Returns:
        UsageSummary instance or None
    """
    if usage is None:
        return None
    if isinstance(usage, UsageSummary):
        return usage
    if isinstance(usage, dict):
        return UsageSummary.from_dict(usage)  # type: ignore[reportUnknownArgumentType]
    # Try to extract dict from Pydantic models
    if hasattr(usage, "model_dump"):
        return UsageSummary.from_dict(usage.model_dump())
    if hasattr(usage, "to_dict"):
        return UsageSummary.from_dict(usage.to_dict())
    if hasattr(usage, "__dict__"):
        return UsageSummary.from_dict(usage.__dict__)
    return None


class UsageAccountingOrchestrator(IUsageAccountingOrchestrator):
    """Handles usage accounting, response wrapping, and lifecycle updates."""

    @staticmethod
    def _normalize_metadata(metadata: dict[str, Any] | None) -> dict[str, JsonValue]:
        """Normalize metadata to dict[str, JsonValue] for boundary safety.

        Args:
            metadata: Raw metadata dictionary or None

        Returns:
            Normalized metadata with JSON-serializable values only
        """
        from src.core.domain.translation_utils.json_utils import (
            sanitize_dict_for_json,
        )

        if metadata is None:
            return {}

        # Sanitize metadata to ensure all values are JSON-serializable
        sanitized = sanitize_dict_for_json(metadata)
        return sanitized

    @staticmethod
    def _extract_b2bua_usage_metadata(
        context: RequestContext | None,
    ) -> dict[str, JsonValue] | None:
        if context is None:
            return None
        identity = getattr(context, "b2bua_identity", None)
        if not isinstance(identity, B2buaIdentity):
            return None

        metadata: dict[str, JsonValue] = {"a_session_id": identity.a_session_id}
        if isinstance(identity.b_session_id, str) and identity.b_session_id.strip():
            metadata["b_session_id"] = identity.b_session_id
        if isinstance(identity.b_seq, int):
            metadata["b_seq"] = identity.b_seq
        return metadata

    @staticmethod
    def _should_force_usage_recalculation_for_backend(
        backend_type: str | None,
    ) -> bool:
        """Return True when backend usage is known to be estimator-derived.

        Gemini OAuth Code Assist streaming currently reports usage from a local
        estimator (not authoritative provider-side token accounting). For these
        backends we force transport-side usage reconciliation so outbound token
        hints are propagated to client-visible usage.
        """
        if not isinstance(backend_type, str):
            return False
        normalized = backend_type.strip().lower().replace("_", "-")
        return normalized.startswith("gemini-oauth")

    def __init__(
        self,
        usage_tracking_service: IUsageTrackingService | None,
        usage_tracking_wrapper: IUsageTrackingWrapper,
        stream_session_id_resolver: IStreamSessionIdResolver,
        planning_phase_manager: IPlanningPhaseManager,
        resilience_coordinator: IResilienceCoordinator | None,
        backend_factory: IBackendFactory | None = None,
        backend_lifecycle_manager: IBackendLifecycleManager | None = None,
        usage_normalization_service: IUsageNormalizationService | None = None,
        wire_capture_orchestrator: IWireCaptureOrchestrator | None = None,
    ):
        """Initialize the usage accounting orchestrator."""
        self._usage_tracking_service = usage_tracking_service
        self._usage_tracking_wrapper = usage_tracking_wrapper
        self._stream_session_id_resolver = stream_session_id_resolver
        self._planning_phase_manager = planning_phase_manager
        self._resilience = resilience_coordinator
        self._backend_factory = backend_factory
        self._backend_lifecycle_manager = backend_lifecycle_manager
        self._usage_normalization_service = usage_normalization_service
        self._wire_capture_orchestrator = wire_capture_orchestrator

    async def calculate_and_record_usage(
        self,
        domain_request: ChatRequest,
        request: ChatRequest,
        backend_type: str,
        effective_model: str,
        session: ISession | None,
        session_id_for_backend: str | None,
        context: RequestContext | None = None,
    ) -> tuple[int, str | None, str | None]:
        """Calculate tokens and record request usage."""
        ctp_record_id = None
        ptb_record_id = None
        outbound_tokens = 0

        try:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Calculating usage: domain_request_type=%s, request_type=%s",
                    type(domain_request).__name__,
                    type(request).__name__,
                )

            outbound_tokens = await asyncio.to_thread(
                calculate_outbound_tokens,
                domain_request,
                model=effective_model,
                label="outbound",
            )

            # Calculate verbatim tokens (from original request)
            # Always calculate for logging/debugging purposes
            verbatim_tokens = await asyncio.to_thread(
                calculate_outbound_tokens,
                request,
                model=effective_model,
                label="verbatim",
            )

            if logger.isEnabledFor(logging.DEBUG):
                # Log additional debug info if verbatim tokens are 0 (potential issue)
                if verbatim_tokens == 0:
                    # Extract message count for debugging
                    message_count = 0
                    if hasattr(request, "messages"):
                        message_count = len(request.messages) if request.messages else 0
                    if logger.isEnabledFor(TRACE_LEVEL):
                        logger.log(
                            TRACE_LEVEL,
                            f"Outbound tokens to {backend_type}/{effective_model}: {outbound_tokens} (verbatim: {verbatim_tokens}, "
                            f"verbatim_message_count: {message_count}, usage_tracking_enabled: {self._usage_tracking_service is not None})",
                        )
                else:
                    if logger.isEnabledFor(TRACE_LEVEL):
                        logger.log(
                            TRACE_LEVEL,
                            f"Outbound tokens to {backend_type}/{effective_model}: {outbound_tokens} (verbatim: {verbatim_tokens})",
                        )

            # Record request usage
            if self._usage_tracking_service:
                try:
                    # Use session.state.proxy_user if available
                    proxy_user = None
                    if (
                        session
                        and hasattr(session, "state")
                        and hasattr(session.state, "proxy_user")
                    ):
                        proxy_user = getattr(session.state, "proxy_user", None)  # type: ignore[attr-defined]

                    usage_session_id = session_id_for_backend
                    ptb_turn_number = 1
                    if context is not None:
                        context_session_id = getattr(context, "session_id", None)
                        if (
                            isinstance(context_session_id, str)
                            and context_session_id.strip()
                        ):
                            usage_session_id = context_session_id
                        b2bua_usage_metadata = self._extract_b2bua_usage_metadata(
                            context
                        )
                        if b2bua_usage_metadata is not None:
                            a_session_id = b2bua_usage_metadata.get("a_session_id")
                            if isinstance(a_session_id, str) and a_session_id.strip():
                                usage_session_id = a_session_id
                            b_seq = b2bua_usage_metadata.get("b_seq")
                            if isinstance(b_seq, int) and b_seq > 0:
                                ptb_turn_number = b_seq

                    sid = usage_session_id or "unknown"

                    # Extract call_purpose from context if available
                    call_purpose: str | None = None
                    if context is not None:
                        raw_purpose = context.extensions.get("call_purpose")
                        if isinstance(raw_purpose, str):
                            call_purpose = raw_purpose

                    ctp_record_id = await self._usage_tracking_service.record_request(
                        session_id=sid,
                        backend_type=backend_type,
                        model=effective_model,
                        frontend_type="openai",
                        leg=TrafficLeg.CLIENT_TO_PROXY,
                        prompt_tokens=verbatim_tokens,
                        proxy_user=proxy_user,
                        turn_number=1,
                        call_purpose=call_purpose,
                    )

                    ptb_record_id = await self._usage_tracking_service.record_request(
                        session_id=sid,
                        backend_type=backend_type,
                        model=effective_model,
                        frontend_type="openai",
                        leg=TrafficLeg.PROXY_TO_BACKEND,
                        prompt_tokens=outbound_tokens,
                        proxy_user=proxy_user,
                        turn_number=ptb_turn_number,
                        call_purpose=call_purpose,
                    )
                except Exception as e:
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            f"Failed to record request usage: {e}", exc_info=True
                        )

        except (ValueError, TypeError, AttributeError, KeyError) as exc:
            # Token calculation errors (calculate_outbound_tokens handles these internally,
            # but catch here as defensive guard for unexpected code paths)
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to calculate outbound tokens or record usage: %s",
                    type(exc).__name__,
                    exc_info=True,
                )
        except Exception as exc:
            # Defensive catch-all for truly unexpected errors on critical billing path
            # Fail-open: log but don't break request flow
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Unexpected error calculating outbound tokens or recording usage: %s",
                    type(exc).__name__,
                    exc_info=True,
                )

        return outbound_tokens, ctp_record_id, ptb_record_id

    async def wrap_response_for_usage(
        self,
        result: ResponseEnvelope | StreamingResponseEnvelope,
        outbound_tokens: int,
        ctp_record_id: str | None,
        ptb_record_id: str | None,
        start_time: float,
        context: RequestContext | None = None,
        backend_type: str | None = None,
        effective_model: str | None = None,
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        """Wrap response with usage tracking."""
        b2bua_usage_metadata = self._extract_b2bua_usage_metadata(context)

        # Store outbound tokens in result metadata for tracking
        if hasattr(result, "metadata") and result.metadata is None:
            result.metadata = {}
        if hasattr(result, "metadata") and isinstance(result.metadata, dict):
            result.metadata["outbound_tokens"] = outbound_tokens
            if self._should_force_usage_recalculation_for_backend(backend_type):
                result.metadata["allow_usage_recalculation"] = True
            if b2bua_usage_metadata is not None:
                result.metadata.setdefault("b2bua", dict(b2bua_usage_metadata))

        # Wrap result content for usage tracking
        if (
            isinstance(result, StreamingResponseEnvelope)
            and self._usage_tracking_service
            and (ctp_record_id or ptb_record_id)
        ):
            if result.content is not None:
                result.content = self._usage_tracking_wrapper.wrap_stream_for_usage(
                    result.content,
                    ctp_record_id,
                    ptb_record_id,
                    start_time,
                )
        elif (
            isinstance(result, ResponseEnvelope)
            and self._usage_tracking_service
            and (ctp_record_id or ptb_record_id)
        ):
            try:
                usage = getattr(result, "usage", None)
                if (
                    usage is None
                    and hasattr(result, "metadata")
                    and isinstance(result.metadata, dict)
                ):
                    usage = result.metadata.get("usage")

                if usage:
                    # Convert UsageSummary to dict if needed
                    if hasattr(usage, "to_dict") and not isinstance(
                        usage, dict | list | str | int | float | bool
                    ):
                        usage_dict = usage.to_dict()  # type: ignore[attr-defined]
                    elif hasattr(usage, "model_dump") and not isinstance(
                        usage, dict | list | str | int | float | bool
                    ):
                        usage_dict = usage.model_dump()  # type: ignore[attr-defined]
                    elif isinstance(usage, dict):
                        usage_dict = usage  # type: ignore[reportUnknownVariableType]
                    else:
                        usage_dict = {}
                    if b2bua_usage_metadata is not None:
                        usage_dict = dict(usage_dict)
                        usage_dict.setdefault("b2bua", dict(b2bua_usage_metadata))

                    completion_tokens_raw = usage_dict.get("completion_tokens", 0)  # type: ignore[reportUnknownMemberType]
                    completion_tokens = (
                        int(completion_tokens_raw)
                        if isinstance(completion_tokens_raw, int | float | str)
                        else 0
                    )
                    duration_ms = (time.time() - start_time) * 1000

                    if ptb_record_id:
                        await self._usage_tracking_service.record_response(
                            record_id=ptb_record_id,
                            completion_tokens=completion_tokens,
                            backend_reported_usage=usage_dict,  # type: ignore[reportUnknownArgumentType]
                            http_status_code=getattr(result, "status_code", 200),
                            total_duration_ms=duration_ms,
                        )

                    if ctp_record_id:
                        await self._usage_tracking_service.record_response(
                            record_id=ctp_record_id,
                            completion_tokens=completion_tokens,
                            backend_reported_usage=usage_dict,  # type: ignore[reportUnknownArgumentType]
                            http_status_code=getattr(result, "status_code", 200),
                            total_duration_ms=duration_ms,
                        )
            except Exception as e:
                logger.error(f"Failed to record response usage: {e}", exc_info=True)

        # Build canonical usage for non-streaming responses
        if (
            isinstance(result, ResponseEnvelope)
            and self._usage_normalization_service
            and context is not None
        ):
            try:
                from src.core.domain.usage_canonical_record import (
                    UsageCompletionOutcome,
                )
                from src.core.domain.usage_normalization_context import (
                    UsageNormalizationContext,
                )
                from src.core.domain.usage_payload import UsagePayload

                # Extract usage from envelope
                usage_summary = getattr(result, "usage", None)
                raw_usage = None
                if usage_summary is None and hasattr(result, "metadata"):
                    metadata = result.metadata
                    if isinstance(metadata, dict):
                        usage_data = metadata.get("usage")
                        if usage_data:
                            if isinstance(usage_data, dict):
                                raw_usage = UsagePayload(payload=usage_data)
                            else:
                                usage_summary = _to_usage_summary(usage_data)

                # Build normalization context
                norm_context = UsageNormalizationContext.from_request_context(
                    context,
                    is_streaming=False,
                    completion_outcome=UsageCompletionOutcome.complete,
                )

                # Override backend_type and model if provided
                if backend_type:
                    norm_context.backend_type = backend_type
                if effective_model:
                    norm_context.model = effective_model

                # Build canonical usage record
                canonical_usage = (
                    await self._usage_normalization_service.build_canonical_record(
                        context=norm_context,
                        usage=usage_summary,
                        raw_usage=raw_usage,
                    )
                )

                # Attach canonical usage to envelope
                result.canonical_usage = canonical_usage
            except Exception as e:
                logger.warning(
                    f"Failed to build canonical usage for non-streaming response: {e}",
                    exc_info=True,
                )

        return result

    async def handle_streaming_response(
        self,
        result: StreamingResponseEnvelope,
        backend_type: str,
        effective_model: str,
        context: RequestContext | None,
        request: CanonicalChatRequest,
        session_id_for_backend: str | None,
        key_name: str | None = None,
    ) -> StreamingResponseEnvelope:
        """Handle streaming response with session ID injection and phase updates."""
        # If the connector returns an error envelope for a streaming request
        # (status_code>=400), there is no stream lifecycle to observe.
        # Record a failure for resilience/cooldown purposes and return as-is.
        status_code = getattr(result, "status_code", 200)
        if isinstance(status_code, int) and status_code >= 400 and self._resilience:
            instance_id = build_resilience_instance_id(backend_type, context)
            # Best-effort: preserve upstream error info if available.
            message = f"Backend returned {status_code} error"
            details: dict[str, Any] = {}
            meta = getattr(result, "metadata", None)
            if isinstance(meta, dict):
                err_message = meta.get("error_message")
                if isinstance(err_message, str) and err_message.strip():
                    message = err_message
                err_details = meta.get("error_details")
                if isinstance(err_details, dict):
                    details = err_details

            self._resilience.record_failure(
                instance_id,
                effective_model,
                BackendError(
                    message=message,
                    status_code=status_code,
                    details=details,
                ),
            )
            return result

        # Get session_id from context for stream correlation
        session_id = getattr(context, "session_id", None)
        session_id = self._stream_session_id_resolver.resolve_stream_session_id(
            session_id, context, request
        )
        if context is not None and not getattr(context, "session_id", None):
            with contextlib.suppress(Exception):
                context.session_id = session_id

        # Wrap with session_id injection and canonical usage tracking
        original_content = result.content
        b2bua_usage_metadata = self._extract_b2bua_usage_metadata(context)

        resilience_instance_id: str | None = None
        if self._resilience:
            resilience_instance_id = build_resilience_instance_id(backend_type, context)

        async def _inject_session_id_and_track_usage() -> Any:
            from src.core.domain.usage_normalization_context import (
                UsageNormalizationContext,
            )
            from src.core.domain.usage_payload import UsagePayload
            from src.core.interfaces.response_processor_interface import (
                ProcessedResponse,
            )

            accumulated_usage = None
            completion_outcome: UsageCompletionOutcome | None = None
            error_classification: str | None = None
            stream_error: BaseException | None = None

            try:
                if original_content:
                    async for chunk in original_content:  # type: ignore
                        # chunk is ProcessedResponse from AsyncIterator[ProcessedResponse]
                        # Merge session_id into existing metadata
                        metadata = dict(chunk.metadata or {})
                        if session_id and "session_id" not in metadata:
                            metadata["session_id"] = session_id
                        if session_id and "stream_id" not in metadata:
                            metadata["stream_id"] = session_id
                        if b2bua_usage_metadata is not None and "b2bua" not in metadata:
                            metadata["b2bua"] = dict(b2bua_usage_metadata)

                        # Track usage from chunks (``chunk.usage`` or OpenAI-style ``content["usage"]``)
                        observed_usage = usage_summary_from_processed_response(chunk)
                        if observed_usage is not None:
                            accumulated_usage = observed_usage

                        # Check for error metadata (take precedence over exception-based classification)
                        error_info = metadata.get("error")
                        if error_info and isinstance(error_info, dict):
                            error_type = error_info.get("type", "")
                            if isinstance(error_type, str):
                                error_type_lower = error_type.lower()
                                if "timeout" in error_type_lower:
                                    error_classification = "timeout"
                                elif (
                                    "backenderror" in error_type_lower
                                    or "backend_error" in error_type_lower
                                ):
                                    error_classification = "backend_error"
                                elif (
                                    "connectionerror" in error_type_lower
                                    or "connection_error" in error_type_lower
                                ):
                                    error_classification = "connection_error"

                        # Normalize content and metadata to ensure boundary safety
                        normalized_content = normalize_to_processed_chunk_content(
                            chunk.content
                        )
                        normalized_metadata = self._normalize_metadata(metadata)
                        usage_for_yield = observed_usage
                        if usage_for_yield is None:
                            usage_for_yield = _to_usage_summary(chunk.usage)
                        yield ProcessedResponse(
                            content=normalized_content,
                            metadata=normalized_metadata,
                            usage=usage_for_yield,
                        )

                # Stream completed. If we observed an explicit error chunk, treat the
                # completion as incomplete for resilience/usage classification.
                completion_outcome = (
                    UsageCompletionOutcome.complete
                    if error_classification is None
                    else UsageCompletionOutcome.incomplete
                )
            except GeneratorExit:
                # Client disconnected - this is expected
                completion_outcome = UsageCompletionOutcome.incomplete
                if context and context.processing_context:
                    # processing_context.values is dict[str, Any], not None, so no need to check
                    context.processing_context.values["cancel_reason"] = (
                        "client_disconnect"
                    )
                raise
            except Exception as e:
                # Stream error occurred
                completion_outcome = UsageCompletionOutcome.incomplete
                stream_error = e
                # Classify error (only if not already classified from metadata)
                if error_classification is None:
                    if isinstance(e, APITimeoutError):
                        error_classification = "timeout"
                    elif isinstance(e, BackendError):
                        error_classification = "backend_error"
                    elif isinstance(e, APIConnectionError):
                        error_classification = "connection_error"
                    else:
                        error_classification = "unknown"
                raise
            finally:
                # Resilience first (no awaits): probe release/success must not be skipped
                # if usage normalization awaits during async-generator cleanup.
                _record_streaming_resilience_outcome(
                    self._resilience,
                    instance_id=resilience_instance_id,
                    effective_model=effective_model,
                    completion_outcome=completion_outcome,
                    stream_error=stream_error,
                    error_classification=error_classification,
                    context=context,
                    backend_type=backend_type,
                )

                # Build canonical usage when stream completes
                if (
                    self._usage_normalization_service
                    and context is not None
                    and completion_outcome is not None
                ):
                    try:
                        # Build normalization context (cancel_reason will be extracted from context)
                        norm_context = UsageNormalizationContext.from_request_context(
                            context,
                            is_streaming=True,
                            completion_outcome=completion_outcome,
                            error_classification=error_classification,
                        )

                        # Override backend_type and model
                        norm_context.backend_type = backend_type
                        norm_context.model = effective_model

                        # Extract raw usage if available
                        raw_usage = None
                        if accumulated_usage is None and hasattr(result, "metadata"):
                            result_metadata = result.metadata
                            if isinstance(result_metadata, dict):
                                usage_data = result_metadata.get("usage")
                                if usage_data and isinstance(usage_data, dict):
                                    raw_usage = UsagePayload(payload=usage_data)

                        # Build canonical usage record
                        canonical_usage = await self._usage_normalization_service.build_canonical_record(
                            context=norm_context,
                            usage=accumulated_usage,
                            raw_usage=raw_usage,
                        )

                        # Attach canonical usage to envelope
                        # Set canonical_usage directly on envelope (primary location)
                        result.canonical_usage = canonical_usage

                        # Capture canonical usage to wire capture (streaming completion)
                        if self._wire_capture_orchestrator is not None:
                            # Use create_task to avoid blocking the stream cleanup/closing for wire capture I/O
                            try:
                                capture_metadata: dict[str, JsonValue] = {}
                                for key in (
                                    "account_id",
                                    "retry_attempt",
                                    "is_retry",
                                ):
                                    if key in context.extensions:
                                        capture_metadata[key] = context.extensions[key]
                                capture_metadata_opt: dict[str, JsonValue] | None = (
                                    capture_metadata or None
                                )

                                loop = asyncio.get_running_loop()
                                capture_task = loop.create_task(
                                    self._wire_capture_orchestrator.capture_stream_completion(
                                        context=context,
                                        session_id=session_id,
                                        backend_type=backend_type,
                                        effective_model=effective_model,
                                        key_name=key_name,
                                        canonical_usage=canonical_usage,
                                        capture_metadata=capture_metadata_opt,
                                    )
                                )
                                # Ensure task is not garbage collected and handle potential exceptions
                                capture_task.add_done_callback(
                                    lambda t: (
                                        t.exception() if not t.cancelled() else None
                                    )
                                )
                            except RuntimeError:
                                # Fallback if no event loop (unlikely here)
                                pass
                    except Exception as e:
                        logger.warning(
                            f"Failed to build canonical usage for streaming response: {e}",
                            exc_info=True,
                        )

            if session_id and self._planning_phase_manager:
                await self._planning_phase_manager.update_counters(
                    session_id, ProcessedResponse(content="", metadata={})
                )

        # Modify the original result envelope's content and return it
        # This ensures canonical_usage set in the finally block is on the returned envelope
        # Note: canonical_usage will be None initially but set asynchronously when stream completes
        result.content = _inject_session_id_and_track_usage()  # type: ignore[assignment]
        return result

    async def handle_non_streaming_response(
        self,
        result: ResponseEnvelope,
        backend_type: str,
        effective_model: str,
        session_id_for_backend: str | None,
        context: RequestContext | None = None,
    ) -> ResponseEnvelope:
        """Handle non-streaming response with success recording and phase updates."""
        # Record success in resilience coordinator
        if self._resilience:
            instance_id = build_resilience_instance_id(backend_type, context)
            self._resilience.record_success(instance_id, effective_model)

        session_id_for_state = session_id_for_backend
        if context is not None and context.session_id:
            session_id_for_state = context.session_id

        if session_id_for_state and self._planning_phase_manager:
            await self._planning_phase_manager.update_counters(
                session_id_for_state, result
            )

        return result

    async def handle_auth_failure(
        self,
        exc: Exception,
        backend: LLMBackend,
        backend_type: str,
        session_id_for_backend: str | None,
    ) -> None:
        """Handle authentication failure with backend lifecycle side effects."""
        if not self._backend_lifecycle_manager or not self._backend_factory:
            return

        is_auth_error = False
        auth_message = ""

        if isinstance(exc, AuthenticationError):
            is_auth_error = True
            auth_message = str(exc)
        elif hasattr(exc, "status_code") and getattr(exc, "status_code", None) == 401:
            # Duck typing for transport exceptions or backend errors
            is_auth_error = True
            if hasattr(exc, "detail"):
                auth_message = str(getattr(exc, "detail", ""))  # type: ignore[attr-defined]
            elif hasattr(exc, "message"):
                auth_message = str(getattr(exc, "message", ""))  # type: ignore[attr-defined]
            else:
                auth_message = str(exc)
        elif isinstance(exc, BackendError) and getattr(exc, "status_code", None) == 401:
            is_auth_error = True
            auth_message = getattr(exc, "message", str(exc))

        if (
            is_auth_error
            and hasattr(backend, "has_static_credentials")
            and backend.has_static_credentials
            and not str(backend_type).lower().startswith("opencode-go")
        ):
            # Permanent auth failure for static backends (env vars)
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Authentication failed for static backend %s: %s",
                    backend_type,
                    exc,
                )
            if hasattr(backend, "mark_auth_invalid"):
                backend.mark_auth_invalid(auth_message)

            if hasattr(self._backend_factory, "unregister_backend"):
                self._backend_factory.unregister_backend(backend_type)  # type: ignore[attr-defined]

            self._backend_lifecycle_manager.discard(
                backend_type, session_id_for_backend, reason=auth_message
            )

    async def handle_backend_error(
        self,
        call_exc: Exception,
        backend_type: str,
        effective_model: str,
        context: RequestContext | None,
        request: CanonicalChatRequest,
        backend: LLMBackend,
        normalized_exc: Exception | None = None,
    ) -> None:
        """Handle backend error (resilience updates)."""
        # Store retry-after in backend instance if this is a rate limit error
        if isinstance(normalized_exc, RateLimitExceededError) and hasattr(
            backend, "set_retry_after"
        ):
            reset_at = getattr(normalized_exc, "reset_at", None)
            if reset_at is not None:
                retry_after_seconds = reset_at - time.time()
                if retry_after_seconds > 0:
                    backend.set_retry_after(retry_after_seconds)
                    if logger.isEnabledFor(logging.INFO):
                        logger.info(
                            "Backend %s rate limited, cached retry-after for %.1f seconds",
                            backend_type,
                            retry_after_seconds,
                        )
