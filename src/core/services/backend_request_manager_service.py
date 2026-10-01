"""
Backend request manager implementation.

This module provides the implementation of the backend request manager interface.
"""

from __future__ import annotations

import asyncio
import json
import logging
import math
from collections.abc import AsyncIterator, Mapping
from typing import Any, cast

from src.core.common.exceptions import (
    BackendError,
    DuplicateRequestError,
)
from src.core.domain.backend_request_manager.canonical_post_backend_response import (
    select_post_backend_processing_mode,
)
from src.core.domain.backend_request_manager.context_models import (
    ResponseProcessingContext,
    StructuredOutputContext,
)
from src.core.domain.chat import ChatRequest
from src.core.domain.processed_result import ProcessedResult
from src.core.domain.request_context import RequestContext
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.interfaces.backend_processor_interface import IBackendProcessor
from src.core.interfaces.backend_request_manager_components import (
    IBackendRequestPreparation,
)
from src.core.interfaces.backend_request_manager_interface import IBackendRequestManager
from src.core.interfaces.configuration_interface import IConfig
from src.core.interfaces.quality_verifier_service_interface import (
    IQualityVerifierServiceFactory,
)
from src.core.interfaces.request_deduplication_interface import (
    IRequestDeduplicationService,
)
from src.core.interfaces.response_processor_interface import (
    IResponseProcessor,
)
from src.core.services.envelope_compatibility_adapter import (
    EnvelopeCompatibilityAdapter,
)
from src.core.services.history_compaction_service import HistoryCompactionService
from src.core.services.post_backend_response_coordinator import (
    PostBackendResponseCoordinator,
)

logger = logging.getLogger(__name__)


class BackendRequestManager(IBackendRequestManager):
    """Implementation of the backend request manager."""

    def __init__(
        self,
        backend_processor: IBackendProcessor,
        response_processor: IResponseProcessor,
        quality_verifier_service_factory: IQualityVerifierServiceFactory | None,
        request_preparation: IBackendRequestPreparation,
        post_backend_response_coordinator: PostBackendResponseCoordinator,
        history_compaction_service: HistoryCompactionService | None = None,
        config: IConfig | None = None,
        dedup_service: IRequestDeduplicationService | None = None,
        envelope_compatibility_adapter: EnvelopeCompatibilityAdapter | None = None,
    ) -> None:
        """Initialize the backend request manager.

        Args:
            backend_processor: The backend processor
            response_processor: The response processor
            quality_verifier_service_factory: Factory for modifying schemas
            request_preparation: Service for preparing backend requests
            post_backend_response_coordinator: Canonical post-backend pipeline
            history_compaction_service: Optional service for compacting history (kept for backward compatibility)
            config: Optional application configuration (kept for backward compatibility)
            dedup_service: Optional request deduplication service
            envelope_compatibility_adapter: Optional canonical-handle envelope adapter
        """
        self._backend_processor = backend_processor
        if quality_verifier_service_factory is None:
            raise ValueError("quality_verifier_service_factory is required")
        self._response_processor = response_processor
        self._quality_verifier_service_factory = quality_verifier_service_factory
        self._request_preparation = request_preparation
        self._history_compaction_service = history_compaction_service
        self._config = config
        self._dedup_service = dedup_service
        self._post_backend_response_coordinator = post_backend_response_coordinator
        self._envelope_compatibility_adapter = (
            envelope_compatibility_adapter or EnvelopeCompatibilityAdapter()
        )

    def _preflight_tool_call_retry_limit(
        self, request: ChatRequest, session_id: str
    ) -> ResponseEnvelope | StreamingResponseEnvelope | None:
        """Return a terminal response without calling the backend when already at limit.

        Some callers/tests expect that when the request already carries a retry counter at
        the maximum allowed value, the proxy should terminate the session immediately.

        This is a lightweight preflight guard; the full retry logic is implemented in
        ToolCallRetryCoordinator and the response handlers.
        """
        try:
            from src.core.services.tool_call_retry_coordinator import (
                ToolCallRetryCoordinator,
            )

            extra_body = request.extra_body or {}
            dangerous_retry_key = getattr(
                ToolCallRetryCoordinator, "_DANGEROUS_RETRY_KEY", None
            )
            legacy_retry_key = getattr(
                ToolCallRetryCoordinator, "_LEGACY_DANGEROUS_RETRY_KEY", None
            )
            if not isinstance(dangerous_retry_key, str):
                dangerous_retry_key = "dangerous_retry_count"
            if not isinstance(legacy_retry_key, str):
                legacy_retry_key = "_dangerous_command_retry_count"
            retry_count = extra_body.get(dangerous_retry_key, 0)
            if not isinstance(retry_count, int):
                retry_count = 0
            legacy_retry_count = extra_body.get(legacy_retry_key, 0)
            if isinstance(legacy_retry_count, int) and legacy_retry_count > retry_count:
                retry_count = legacy_retry_count

            # If already at max, terminate immediately (no backend call)
            max_retries = getattr(
                ToolCallRetryCoordinator, "_MAX_DANGEROUS_COMMAND_RETRIES", 0
            )
            if retry_count >= max_retries:
                coordinator = ToolCallRetryCoordinator(
                    backend_processor=self._backend_processor
                )
                create_terminal_response = getattr(
                    coordinator, "_create_terminal_response", None
                )
                if callable(create_terminal_response):
                    response = create_terminal_response(
                        retry_count=retry_count + 1,
                        session_id=session_id,
                        is_streaming=bool(request.stream),
                    )
                    if isinstance(
                        response, ResponseEnvelope | StreamingResponseEnvelope
                    ):
                        return response
        except (AttributeError, ImportError, KeyError) as e:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Preflight tool call retry limit check failed: %s",
                    e,
                    exc_info=True,
                )
            return None

        return None

    def _build_processing_context(
        self,
        request: ChatRequest,
        session_id: str,
        context: RequestContext | dict[str, Any],
    ) -> ResponseProcessingContext:
        """Build ResponseProcessingContext from request and context.

        Args:
            request: The backend request
            session_id: Session identifier
            context: Request context with processing_context

        Returns:
            Typed processing context with all required fields
        """
        # Extract backend_name.
        # Prefer the routed backend from RequestContext (set by routing/registry).
        backend_name: str | None = None
        if isinstance(context, dict):
            b_raw = context.get("backend")
            if isinstance(b_raw, str) and b_raw:
                backend_name = b_raw
        elif isinstance(context.backend, str) and context.backend:
            backend_name = context.backend
        extra_body = getattr(request, "extra_body", None)
        if backend_name is None and isinstance(extra_body, dict):
            raw_backend_type = extra_body.get("backend_type")
            if isinstance(raw_backend_type, str):
                backend_name = raw_backend_type

        # Extract model_name.
        model_name: str | None = None
        if isinstance(context, dict):
            m_raw = context.get("effective_model")
            if isinstance(m_raw, str) and m_raw:
                model_name = m_raw
        elif isinstance(context.effective_model, str) and context.effective_model:
            model_name = context.effective_model
        if model_name is None:
            raw_model = getattr(request, "model", None)
            if isinstance(raw_model, str):
                model_name = raw_model

        # Extract client_os from processing_context if available
        client_os: str | None = None
        proc_ctx = (
            None
            if isinstance(context, dict)
            else getattr(context, "processing_context", None)
        )
        if proc_ctx is not None:
            processing_values = proc_ctx.values
            raw_client_os = processing_values.get("client_os")
            if isinstance(raw_client_os, str):
                client_os = raw_client_os

        # Build structured output context if schema is present
        structured_output: StructuredOutputContext | None = None
        if proc_ctx is not None:
            processing_values = proc_ctx.values
            response_schema = processing_values.get("response_schema")
            if response_schema is not None:
                schema_name = processing_values.get("schema_name", "unnamed")
                request_id = processing_values.get("request_id", session_id)
                structured_output = StructuredOutputContext(
                    response_schema=response_schema,
                    schema_name=str(schema_name),
                    request_id=str(request_id),
                )

        return ResponseProcessingContext(
            session_id=session_id,
            backend_name=backend_name,
            model_name=model_name,
            client_os=client_os,
            original_request=request,
            structured_output=structured_output,
        )

    def _should_bypass_dedup(
        self, request: ChatRequest, context: RequestContext
    ) -> bool:
        """Determine whether request deduplication should be bypassed.

        Deduplication is now enabled for both streaming and non-streaming requests
        with status-aware tracking that allows legitimate retries after 429/503 errors.

        Bypass is only allowed via explicit header.
        """
        # InternLM and Kimi streaming are handled by connectors where clients may replay
        # identical requests (e.g. reconnects or immediate retry after upstream validation
        # failures). The generic dedup "done-only" response can look like a silent empty
        # completion in these flows, so bypass dedup for these streaming backends.
        model = getattr(request, "model", None)
        if (
            bool(getattr(request, "stream", False))
            and isinstance(model, str)
            and model.strip()
            .lower()
            .startswith(("internlm:", "internlm/", "kimi-code:", "kimi/"))
        ):
            return True

        # OpenCode (and other Responses clients) abort mid-tool then retry the same
        # body. A done-only / empty response.completed (output:[]) clears the UI.
        # Bypass streaming Responses semantic pipeline so retries re-hit upstream.
        extensions = getattr(context, "extensions", None)
        if (
            bool(getattr(request, "stream", False))
            and isinstance(extensions, Mapping)
            and extensions.get("responses_semantic_pipeline") is True
        ):
            return True

        headers = getattr(context, "headers", {})
        if isinstance(headers, Mapping):
            dedup_override = headers.get("x-llmproxy-no-dedup")
            if isinstance(dedup_override, str) and dedup_override.strip().lower() in {
                "1",
                "true",
                "yes",
            }:
                return True

        return False

    async def prepare_backend_request(
        self,
        request_data: ChatRequest,
        command_result: ProcessedResult,
        *,
        history_compaction_session_allowed: bool = True,
    ) -> ChatRequest | None:
        """Prepare backend request based on command processing results."""
        return await self._request_preparation.prepare(
            request_data,
            command_result,
            history_compaction_session_allowed=history_compaction_session_allowed,
        )

    async def process_backend_request(
        self,
        backend_request: ChatRequest,
        session_id: str,
        context: RequestContext | dict[str, Any] | None,
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        """Process backend request with retry handling."""
        if context is None:
            context = RequestContext(headers={}, cookies={}, state=None, app_state=None)
        elif isinstance(context, dict):
            context = RequestContext(
                headers=context.get("headers", {}),
                cookies=context.get("cookies", {}),
                state=context.get("state"),
                app_state=context.get("app_state"),
                client_host=context.get("client_host"),
                session_id=context.get("session_id"),
                request_id=context.get("request_id"),
                agent=context.get("agent"),
                original_request=context.get("original_request"),
                processing_context=context.get("processing_context"),
                domain_request=context.get("domain_request"),
                raw_body=context.get("raw_body"),
                backend=context.get("backend"),
                effective_model=context.get("effective_model"),
                extensions=context.get("extensions", {}),
            )
        content_hash: str | None = None
        preflight = self._preflight_tool_call_retry_limit(backend_request, session_id)
        if preflight is not None:
            return preflight

        # Deduplication check FIRST (before any processing)
        if self._dedup_service:
            if self._should_bypass_dedup(backend_request, context):
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Request deduplication bypassed (x-llmproxy-no-dedup header) "
                        "session=%s model=%s",
                        session_id,
                        backend_request.model,
                    )
            else:
                dedup_result = await self._dedup_service.check_and_register(
                    backend_request, session_id
                )
                retry_after_seconds: float | None = None
                try:
                    is_duplicate, content_hash, retry_after_seconds = dedup_result
                except ValueError:
                    is_duplicate, content_hash = cast(tuple[bool, str], dedup_result)
                if is_duplicate:
                    # A cached upstream failure must not be converted into an empty successful
                    # stream. That makes clients appear hung and prevents useful retries.
                    if content_hash and self._dedup_service is not None:
                        outcome = await self._dedup_service.get_request_outcome(
                            content_hash, session_id
                        )
                        if isinstance(outcome, tuple) and len(outcome) == 2:
                            outcome_status, outcome_code = outcome
                            if (
                                outcome_status == "success"
                                and isinstance(outcome_code, int)
                                and outcome_code >= 400
                            ):
                                logger.warning(
                                    "Replaying cached backend error for duplicate: "
                                    "hash=%s session=%s status=%s",
                                    content_hash[:8],
                                    session_id,
                                    outcome_code,
                                )
                                raise BackendError(
                                    message=(
                                        "Previous identical request failed upstream "
                                        f"with HTTP {outcome_code}"
                                    ),
                                    status_code=outcome_code,
                                    code=str(outcome_code),
                                )

                    # Use debug level to avoid log spam during tight retry loops
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Duplicate request swallowed: hash=%s session=%s model=%s",
                            content_hash[:8],
                            session_id,
                            backend_request.model,
                        )
                    # For streaming requests, return a benign "no-op" SSE completion
                    # instead of a 429 error. Some clients issue accidental parallel
                    # duplicates; returning a non-2xx aborts the whole run even if
                    # the original request is still streaming successfully.
                    if getattr(backend_request, "stream", False):
                        headers: dict[str, str] = {
                            "x-llmproxy-duplicate-request": "true"
                        }
                        if (
                            isinstance(retry_after_seconds, int | float)
                            and retry_after_seconds > 0
                        ):
                            headers["Retry-After"] = str(
                                max(0, math.ceil(float(retry_after_seconds)))
                            )

                        extensions = getattr(context, "extensions", None)
                        responses_path = (
                            isinstance(extensions, Mapping)
                            and extensions.get("responses_semantic_pipeline") is True
                        )

                        async def _done_only_stream() -> AsyncIterator[Any]:
                            from src.core.interfaces.response_processor_interface import (
                                ProcessedResponse,
                            )

                            if responses_path:
                                # Never emit chat.completion.chunk on /v1/responses.
                                # Empty response.completed (output:[]) clears OpenCode UI;
                                # mark incomplete so clients retry instead of accepting empty.
                                import json as _json
                                import time as _time

                                resp_id = f"resp_dup_{int(_time.time())}"
                                events = [
                                    {
                                        "type": "response.created",
                                        "sequence_number": 0,
                                        "response": {
                                            "id": resp_id,
                                            "object": "response",
                                            "status": "in_progress",
                                        },
                                    },
                                    {
                                        "type": "response.incomplete",
                                        "sequence_number": 1,
                                        "response": {
                                            "id": resp_id,
                                            "object": "response",
                                            "status": "incomplete",
                                            "output": [],
                                            "incomplete_details": {
                                                "reason": "duplicate_request"
                                            },
                                        },
                                    },
                                ]
                                for evt in events:
                                    payload = _json.dumps(
                                        evt, separators=(",", ":")
                                    ).encode("utf-8")
                                    yield ProcessedResponse(
                                        content=b"data: " + payload + b"\n\n"
                                    )
                                yield ProcessedResponse(content=b"data: [DONE]\n\n")
                                return

                            # Chat Completions: minimal terminal chunk + [DONE].
                            yield ProcessedResponse(
                                content=b'data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}\n\n'
                            )
                            yield ProcessedResponse(content=b"data: [DONE]\n\n")

                        return StreamingResponseEnvelope(
                            content=_done_only_stream(),
                            headers=headers,
                            status_code=200,
                        )

                    raise DuplicateRequestError(
                        content_hash,
                        session_id,
                        retry_after_seconds=retry_after_seconds,
                    )

        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Submitting backend request: hash=%s session=%s model=%s stream=%s",
                content_hash[:8] if content_hash else "n/a",
                session_id,
                backend_request.model,
                getattr(backend_request, "stream", False),
            )

        # Build processing context once per request
        processing_context = self._build_processing_context(
            backend_request, session_id, context
        )

        try:
            # Execute backend request
            backend_response = await self._backend_processor.process_backend_request(
                request=backend_request,
                session_id=session_id,
                context=context,
            )

            post_backend_mode = select_post_backend_processing_mode(
                bool(backend_request.stream),
                backend_response,
            )
            canonical_handle = (
                await self._post_backend_response_coordinator.from_backend_response(
                    backend_response,
                    request=backend_request,
                    context=context,
                    processing_context=processing_context,
                    processing_mode=post_backend_mode,
                )
            )
            canonical_converted: ResponseEnvelope | StreamingResponseEnvelope
            if backend_request.stream:
                canonical_converted = (
                    await self._envelope_compatibility_adapter.to_streaming(
                        canonical_handle, context
                    )
                )
            else:
                canonical_converted = (
                    await self._envelope_compatibility_adapter.to_non_streaming(
                        canonical_handle, context
                    )
                )

            if isinstance(canonical_converted, StreamingResponseEnvelope):
                streaming_result = canonical_converted
                if (
                    self._dedup_service
                    and content_hash
                    and streaming_result.status_code >= 400
                ):
                    await self._dedup_service.mark_request_complete(
                        content_hash,
                        session_id,
                        status_code=streaming_result.status_code,
                    )
                if self._dedup_service and content_hash:
                    dedup_service = self._dedup_service
                    assert dedup_service is not None
                    original_iter = streaming_result.content

                    async def _wrapped_stream() -> AsyncIterator[Any]:
                        client_disconnected = False
                        last_status_code: int | None = (
                            streaming_result.status_code
                            if streaming_result.status_code >= 400
                            else None
                        )
                        saw_done_sentinel = False
                        saw_terminal_finish = False
                        saw_terminal_error = False
                        terminal_status_code: int | None = None

                        def _item_contains_done_sentinel(item: Any) -> bool:
                            payload = getattr(item, "content", None)
                            if isinstance(payload, bytes):
                                return b"data: [DONE]" in payload
                            if isinstance(payload, str):
                                return payload.strip() == "data: [DONE]"
                            return False

                        def _try_extract_terminal_status(item: Any) -> None:
                            nonlocal saw_terminal_finish, saw_terminal_error
                            nonlocal terminal_status_code

                            if saw_terminal_finish:
                                return

                            payload = getattr(item, "content", None)
                            if not isinstance(payload, bytes):
                                return

                            if b'"finish_reason"' not in payload:
                                return

                            # Best-effort parse of an OpenAI-style SSE payload:
                            # `data: {json}\n\n`
                            try:
                                text = payload.decode("utf-8", errors="ignore")
                            except Exception:
                                return

                            # Handle potentially batched events.
                            for block in text.replace("\r\n", "\n").split("\n\n"):
                                stripped = block.strip()
                                if not stripped.startswith("data:"):
                                    continue
                                data_part = stripped[5:].strip()
                                if not data_part or data_part == "[DONE]":
                                    continue
                                try:
                                    obj = json.loads(data_part)
                                except Exception:
                                    continue
                                if not isinstance(obj, dict):
                                    continue
                                choices = obj.get("choices")
                                if not isinstance(choices, list) or not choices:
                                    continue
                                first = choices[0]
                                if not isinstance(first, dict):
                                    continue
                                finish = first.get("finish_reason")
                                if isinstance(finish, str) and finish:
                                    saw_terminal_finish = True
                                    if finish == "error":
                                        saw_terminal_error = True
                                        err = obj.get("error")
                                        if isinstance(err, dict):
                                            status = err.get("status_code")
                                            if isinstance(status, int):
                                                terminal_status_code = status
                                            elif (
                                                isinstance(status, float)
                                                and status.is_integer()
                                            ):
                                                terminal_status_code = int(status)
                                        if terminal_status_code is None:
                                            terminal_status_code = 500
                                    return

                        try:
                            if original_iter is None:
                                return
                            async for item in original_iter:
                                if _item_contains_done_sentinel(item):
                                    saw_done_sentinel = True
                                _try_extract_terminal_status(item)
                                yield item
                            last_status_code = 200
                        except BackendError as e:
                            last_status_code = e.status_code
                            raise
                        except (GeneratorExit, asyncio.CancelledError):
                            client_disconnected = True
                            raise
                        except Exception:
                            last_status_code = 500
                            raise
                        finally:
                            # If the client closes the connection immediately after receiving the
                            # terminal [DONE] sentinel, the downstream iterator may be cancelled
                            # before the stream naturally exhausts. Treat this as a success to
                            # avoid misclassifying completions as disconnects.
                            if client_disconnected and (
                                saw_done_sentinel or saw_terminal_finish
                            ):
                                client_disconnected = False
                                if saw_terminal_error:
                                    last_status_code = terminal_status_code or 500
                                else:
                                    last_status_code = 200

                            if saw_terminal_error:
                                last_status_code = (
                                    terminal_status_code or last_status_code or 500
                                )
                            try:
                                await dedup_service.mark_request_complete(
                                    content_hash,
                                    session_id,
                                    status_code=last_status_code,
                                    client_disconnected=client_disconnected,
                                )
                            except Exception:
                                # Fail-open: never break streaming cleanup because of dedup tracking.
                                if logger.isEnabledFor(logging.DEBUG):
                                    logger.debug(
                                        "Failed to mark streaming request completion for dedup tracking",
                                        exc_info=True,
                                    )

                    streaming_result.content = _wrapped_stream()

                return streaming_result
            if self._dedup_service and content_hash:
                await self._dedup_service.mark_request_complete(
                    content_hash, session_id, status_code=200
                )
            return canonical_converted

        except asyncio.CancelledError:
            # Client disconnected before completion - mark as zombie pattern
            if self._dedup_service and content_hash:
                await self._dedup_service.mark_request_complete(
                    content_hash, session_id, client_disconnected=True
                )
            raise
        except Exception as e:
            # Unexpected error - mark based on exception type
            status_code = getattr(e, "status_code", None)
            if status_code is None:
                status_code = 500
            if self._dedup_service and content_hash:
                await self._dedup_service.mark_request_complete(
                    content_hash, session_id, status_code=status_code
                )
            raise
