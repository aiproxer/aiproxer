"""
Streaming response handler service.

This service processes streaming backend responses including:
- Response processor middleware wrapping
- Empty-stream recovery with retry prompts
- Loop detection and cancellation
- Tool-call retry coordination
- Quality Verifier
- Metadata attachment

Requirements: 1.3, 1.4, 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 6.1, 6.2, 6.3, 7.1, 7.2, 8.1, 8.2, 9.1, 9.2, 10.1
"""

from __future__ import annotations

import asyncio
import contextlib
import json
import logging
import os
from collections.abc import AsyncIterator, Callable, Mapping
from dataclasses import dataclass
from typing import Any, cast

from pydantic.types import JsonValue

from src.core.common.exceptions import (
    APIConnectionError,
    APITimeoutError,
    BackendError,
    LLMProxyError,
    ParsingError,
    RateLimitExceededError,
    ServiceUnavailableError,
    SessionCancelledError,
    TranslationError,
)
from src.core.common.openai_stream_reasoning import openai_dict_has_reasoning_output
from src.core.common.reasoning_request_signals import (
    chat_request_indicates_reasoning_output,
)
from src.core.common.session_key_resolver import (
    resolve_session_key_from_request_context,
)
from src.core.domain.backend_request_manager.context_models import (
    ResponseProcessingContext,
    StreamingContext,
)
from src.core.domain.chat import ChatMessage, ChatRequest
from src.core.domain.request_context import RequestContext
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.interfaces.backend_processor_interface import IBackendProcessor
from src.core.interfaces.backend_request_manager_components import (
    ILoopDetectorFactory,
    IQualityVerifierStreamVerifier,
    IStructuredOutputEnforcer,
    IToolCallRetryCoordinator,
)
from src.core.interfaces.backend_work_guard_interface import IBackendWorkGuard
from src.core.interfaces.loop_detector_interface import ILoopDetector
from src.core.interfaces.response_processor_interface import (
    IResponseProcessor,
    ProcessedResponse,
)
from src.core.interfaces.session_cancellation_coordinator_interface import (
    ISessionCancellationCoordinator,
)
from src.core.interfaces.state_provider_interface import StateAccessViolationError
from src.core.services.empty_response_middleware import EmptyResponseRetryError
from src.core.services.post_backend_response_coordinator import (
    response_envelope_as_single_chunk_stream,
)
from src.core.services.quality_verifier_service import QualityVerifierService
from src.core.services.streaming.chunk_normalizer import (
    normalize_to_processed_chunk_content,
)
from src.core.services.streaming.error_mapping import handle_streaming_error
from src.core.services.streaming.stream_recovery_budget import (
    mark_stream_meaningful_output,
)

logger = logging.getLogger(__name__)

# Default constants (used when config is not provided for backward compatibility)
_DEFAULT_STREAM_RECOVERY_PROMPT = "The previous response was empty, please try again."
_DEFAULT_MAX_EMPTY_STREAM_RETRIES = 1


_MEANINGFUL_FINISH_REASONS: frozenset[str] = frozenset(
    {
        "error",
        "cancelled",
        "security_limit",
        "tool_calls",
        # A clean model completion (including tool-only Responses turns that end
        # with finish_reason=stop after function_call items, and SIWC empty
        # assistant messages) is not an empty-stream failure. Holding these
        # terminal chunks triggered empty recovery -> 502 -> OpenCode retry loops
        # and prevented response.completed / [DONE] from closing the SSE turn.
        "stop",
        "length",
    }
)


@dataclass
class RetryState:
    """Retry state extracted from request."""

    current_retry_count: int
    reactor_retry_active: bool


@dataclass
class QualityVerifierConfig:
    """Quality Verifier configuration extracted from request context."""

    model_spec: str | None
    frequency: int
    max_history: int | None
    max_consecutive_failures: int
    cooldown_seconds: int
    ttft_timeout_seconds: float
    eligible_turn_count: int | None
    skip_verification: bool


class BackendStreamingResponseHandler:
    """Service for handling streaming backend responses."""

    def __init__(
        self,
        response_processor: IResponseProcessor,
        loop_detector_factory: ILoopDetectorFactory,
        quality_verifier_stream_verifier: IQualityVerifierStreamVerifier,
        tool_call_retry_coordinator: IToolCallRetryCoordinator,
        backend_processor: IBackendProcessor,
        cancellation_coordinator: ISessionCancellationCoordinator | None = None,
        backend_work_guard: IBackendWorkGuard | None = None,
        structured_output_enforcer: IStructuredOutputEnforcer | None = None,
        empty_stream_recovery_prompt: str | None = None,
        max_empty_stream_retries: int | None = None,
    ) -> None:
        """Initialize the streaming response handler.

        Args:
            response_processor: Response processor for middleware wrapping
            loop_detector_factory: Factory for creating loop detectors
            quality_verifier_stream_verifier: Service for Quality Verifier
            tool_call_retry_coordinator: Coordinator for tool-call retries
            backend_processor: Backend processor for empty-stream retries
            cancellation_coordinator: Coordinator for session cancellation checks
            backend_work_guard: Guard for backend work scope tracking
            structured_output_enforcer: Enforcer for structured output validation
            empty_stream_recovery_prompt: Recovery prompt for empty stream retries
            max_empty_stream_retries: Maximum number of empty stream retry attempts
        """
        self._response_processor = response_processor
        self._loop_detector_factory = loop_detector_factory
        self._quality_verifier_stream_verifier = quality_verifier_stream_verifier
        self._tool_call_retry_coordinator = tool_call_retry_coordinator
        self._backend_processor = backend_processor
        self._cancellation_coordinator = cancellation_coordinator
        self._backend_work_guard = backend_work_guard
        self._structured_output_enforcer = structured_output_enforcer

        # Configurable empty stream recovery settings (with backward-compatible defaults)
        self._empty_stream_recovery_prompt = (
            empty_stream_recovery_prompt
            if empty_stream_recovery_prompt is not None
            else _DEFAULT_STREAM_RECOVERY_PROMPT
        )
        self._max_empty_stream_retries = (
            max_empty_stream_retries
            if max_empty_stream_retries is not None
            else _DEFAULT_MAX_EMPTY_STREAM_RETRIES
        )

    @staticmethod
    def _coerce_processed_chunk(raw_chunk: Any) -> ProcessedResponse:
        """Normalize raw stream chunks into ProcessedResponse objects."""
        if isinstance(raw_chunk, ProcessedResponse):
            return raw_chunk
        return ProcessedResponse(
            content=normalize_to_processed_chunk_content(raw_chunk),
            metadata={},
        )

    def _extract_text_from_chunk(self, chunk: Any) -> str:
        """Extract textual content from a streaming chunk."""
        content = getattr(chunk, "content", chunk)
        if isinstance(content, str):
            return content
        if isinstance(content, bytes):
            try:
                return content.decode("utf-8")
            except UnicodeDecodeError:
                return content.decode("utf-8", errors="ignore")
        if isinstance(content, dict):
            # OPTIMIZATION: Extract from standard OpenAI format directly to avoid expensive json.dumps
            # This is on the hot path for every token (loop detection + meaning check)
            if "choices" in content and isinstance(content["choices"], list):
                choices = content["choices"]
                if choices and isinstance(choices[0], dict):
                    # Try delta (stream) or message (non-stream)
                    delta = choices[0].get("delta") or choices[0].get("message")
                    if isinstance(delta, dict) and "content" in delta:
                        val = delta["content"]
                        if val is not None:
                            return str(val)

            # Fallback: Use dict() to safely handle StopChunkWithUsage which is a dict subclass
            return json.dumps(dict(content))
        return str(content) if content is not None else ""

    @staticmethod
    def _status_from_stream_error_payload(
        error_payload: dict[str, Any],
        *,
        default_unknown_error_status: int | None,
    ) -> int | None:
        status_code = error_payload.get("status_code")
        if isinstance(status_code, int) and status_code >= 400:
            return status_code

        # OpenAI-compatible context overflow errors are client request errors.
        # Coding agents key compaction/retry decisions on this canonical 400.
        if error_payload.get("code") == "context_length_exceeded":
            return 400

        return default_unknown_error_status

    @staticmethod
    def _extract_terminal_error_status(chunk: Any) -> int | None:
        metadata = getattr(chunk, "metadata", {}) or {}
        finish_reason = metadata.get("finish_reason")
        error_payload = metadata.get("error")
        if isinstance(error_payload, dict):
            return BackendStreamingResponseHandler._status_from_stream_error_payload(
                error_payload,
                default_unknown_error_status=502 if finish_reason == "error" else None,
            )

        if finish_reason == "error":
            return 502

        content = getattr(chunk, "content", None)
        if isinstance(content, dict):
            payload_error = content.get("error")
            if isinstance(payload_error, dict):
                return (
                    BackendStreamingResponseHandler._status_from_stream_error_payload(
                        payload_error,
                        default_unknown_error_status=502,
                    )
                )
            payload_details = content.get("details")
            if payload_error and isinstance(payload_details, dict):
                return (
                    BackendStreamingResponseHandler._status_from_stream_error_payload(
                        payload_details,
                        default_unknown_error_status=502,
                    )
                )

            choices = content.get("choices")
            if isinstance(choices, list):
                for choice in choices:
                    if (
                        isinstance(choice, dict)
                        and choice.get("finish_reason") == "error"
                    ):
                        return 502

        if isinstance(content, str | bytes):
            text = (
                content.decode("utf-8", errors="replace")
                if isinstance(content, bytes)
                else content
            )
            for (
                payload
            ) in BackendStreamingResponseHandler._try_parse_openai_sse_payloads(text):
                payload_error = payload.get("error")
                if isinstance(payload_error, dict):
                    return BackendStreamingResponseHandler._status_from_stream_error_payload(
                        payload_error,
                        default_unknown_error_status=502,
                    )
                choices = payload.get("choices")
                if isinstance(choices, list):
                    for choice in choices:
                        if (
                            isinstance(choice, dict)
                            and choice.get("finish_reason") == "error"
                        ):
                            return 502

        return None

    @staticmethod
    def _extract_terminal_error_payload(chunk: Any) -> dict[str, Any] | None:
        """Return the structured ``error`` object carried by a terminal stream chunk.

        Mirrors :meth:`_extract_terminal_error_status` but yields the full error
        payload (``code``/``type``/``message``/``param``) so the envelope metadata
        can preserve the upstream error shape. Coding agents key automated
        compaction on canonical markers such as OpenAI's
        ``context_length_exceeded``; without this, a streamed backend error
        collapses to a generic ``BackendError`` body that agents cannot recognize.
        """
        metadata = getattr(chunk, "metadata", {}) or {}
        error_payload = metadata.get("error")
        if isinstance(error_payload, dict):
            return error_payload

        content = getattr(chunk, "content", None)
        if isinstance(content, dict):
            payload_error = content.get("error")
            if isinstance(payload_error, dict):
                return payload_error
            payload_details = content.get("details")
            if payload_error and isinstance(payload_details, dict):
                return payload_details

        if isinstance(content, str | bytes):
            text = (
                content.decode("utf-8", errors="replace")
                if isinstance(content, bytes)
                else content
            )
            for (
                payload
            ) in BackendStreamingResponseHandler._try_parse_openai_sse_payloads(text):
                payload_error = payload.get("error")
                if isinstance(payload_error, dict):
                    return payload_error

        return None

    @staticmethod
    def _is_sse_done_only(text: str) -> bool:
        """Return True if the payload is only an SSE done marker."""
        if not text:
            return False

        normalized = text.replace("\r\n", "\n")
        stripped = normalized.strip()
        if stripped in {"[DONE]", "data: [DONE]", "data:[DONE]", 'data: ["DONE"]'}:
            return True

        if "data:" not in normalized:
            return False

        data_lines: list[str] = []
        for line in normalized.splitlines():
            line_s = line.strip()
            if not line_s.startswith("data:"):
                continue
            val = line_s[5:].lstrip()
            if val:
                data_lines.append(val)

        return bool(data_lines) and all(
            v.strip() in {"[DONE]", '["DONE"]'} for v in data_lines
        )

    @staticmethod
    def _is_sse_comment_only(text: str) -> bool:
        """Return True for SSE comment/keepalive payloads (": ...")."""
        stripped = text.strip()
        return bool(stripped) and stripped.startswith(":")

    @staticmethod
    def _try_parse_openai_sse_payloads(text: str) -> list[dict[str, Any]]:
        """Best-effort parse of SSE `data: {json}` into OpenAI chunk dict(s)."""
        if not text:
            return []
        normalized = text.replace("\r\n", "\n")

        payloads: list[dict[str, Any]] = []
        if "data:" in normalized:
            for block in normalized.split("\n\n"):
                for line in block.splitlines():
                    line = line.strip()
                    if not line.startswith("data:"):
                        continue
                    data_part = line[5:].lstrip()
                    if not data_part or data_part == "[DONE]":
                        continue
                    try:
                        obj = json.loads(data_part)
                    except json.JSONDecodeError:
                        continue
                    if isinstance(obj, dict):
                        payloads.append(obj)
            return payloads

        stripped = normalized.strip()
        if not stripped:
            return []
        if stripped.startswith("{"):
            try:
                obj = json.loads(stripped)
            except json.JSONDecodeError:
                return []
            if isinstance(obj, dict):
                return [obj]
        return []

    @staticmethod
    def _openai_dict_has_user_visible_output(payload: dict[str, Any]) -> bool:
        # Top-level error payload
        if payload.get("error"):
            return True

        # Anthropic streaming events (SSE `data:` JSON, not OpenAI-shaped)
        event_type = payload.get("type")
        if event_type == "content_block_delta":
            delta = payload.get("delta")
            if isinstance(delta, dict):
                text = delta.get("text")
                if isinstance(text, str) and text.strip():
                    return True
        if event_type in {"content_block_start", "message_start", "message_delta"}:
            return True

        choices = payload.get("choices")
        if not isinstance(choices, list) or not choices:
            return False

        for choice in choices:
            if not isinstance(choice, dict):
                continue
            finish = choice.get("finish_reason")
            if isinstance(finish, str) and finish in _MEANINGFUL_FINISH_REASONS:
                return True

            delta = choice.get("delta") or choice.get("message")
            if not isinstance(delta, dict):
                continue
            if delta.get("tool_calls"):
                return True
            content_val = delta.get("content")
            if content_val is not None and str(content_val).strip():
                return True

        return False

    def _metadata_has_meaningful_output(self, metadata: dict[str, Any]) -> bool:
        if metadata.get("error"):
            return True
        if metadata.get("tool_call_emitted") is True:
            return True

        # ContentAccumulationProcessor may park tool_calls on metadata (and strip
        # them from interim chunks). A non-empty tool_calls list is still
        # user-visible / client-actionable output for empty-stream gating.
        tool_calls = metadata.get("tool_calls")
        if isinstance(tool_calls, list) and tool_calls:
            return True

        accumulated_content = metadata.get("accumulated_content")
        if isinstance(accumulated_content, str) and accumulated_content.strip():
            return True

        accumulated_reasoning = metadata.get("accumulated_reasoning")
        if (
            metadata.get("reasoning_is_output")
            and isinstance(accumulated_reasoning, str)
            and accumulated_reasoning.strip()
        ):
            return True

        # IMPORTANT: reasoning-only output is not considered user-visible by default.
        if metadata.get("reasoning_is_output"):
            metadata_reasoning = (
                metadata.get("reasoning_content")
                or metadata.get("reasoning")
                or metadata.get("thinking")
                or metadata.get("thought")
            )
            if isinstance(metadata_reasoning, str) and metadata_reasoning.strip():
                return True

        finish_reason = metadata.get("finish_reason")
        if (
            isinstance(finish_reason, str)
            and finish_reason in _MEANINGFUL_FINISH_REASONS
        ):
            return True

        if metadata.get("is_cancellation") is True:
            return True
        if metadata.get("loop_detected") is True:
            return True

        return bool(
            metadata.get("tool_call_swallowed")
            or metadata.get("tool_call_reactor_retry_failed")
        )

    def _text_payload_has_meaningful_output(self, text: str) -> bool:
        if self._is_sse_done_only(text) or self._is_sse_comment_only(text):
            return False
        parsed = self._try_parse_openai_sse_payloads(text)
        if parsed:
            if any(self._openai_dict_has_user_visible_output(p) for p in parsed):
                return True
            # Parsed OpenAI-shaped `data:` JSON with no visible text (e.g. reasoning-only).
            return any(self._non_openai_sse_json_is_meaningful(p) for p in parsed)
        return bool(text.strip())

    @staticmethod
    def _non_openai_sse_json_is_meaningful(payload: dict[str, Any]) -> bool:
        """Detect provider-native JSON objects embedded in SSE text (non-OpenAI)."""
        ctype = payload.get("type")
        return bool(payload.get("candidates")) or (
            isinstance(ctype, str)
            and (
                ctype.startswith("content_block")
                or ctype in {"message_start", "message_delta", "message_stop"}
            )
        )

    def _content_has_meaningful_output(self, content: Any) -> bool:
        if isinstance(content, dict):
            if content.get("error"):
                return True

            # A dict without "choices" is meaningful unless it's just usage/metadata.
            if content and "choices" not in content:
                return not set(content.keys()) <= {
                    "usage",
                    "model",
                    "id",
                    "object",
                    "created",
                }

            return self._openai_dict_has_user_visible_output(content)

        if isinstance(content, str):
            if '"error"' in content:
                return True
            return self._text_payload_has_meaningful_output(content)

        if isinstance(content, bytes | bytearray):
            try:
                decoded = content.decode("utf-8")
            except UnicodeDecodeError:
                decoded = content.decode("utf-8", errors="ignore")
            return self._text_payload_has_meaningful_output(decoded)

        return False

    def _chunk_has_meaningful_output(
        self,
        chunk: ProcessedResponse,
        *,
        count_reasoning_for_empty_stream: bool = True,
    ) -> bool:
        """Check whether a streamed chunk carries user-visible output."""
        metadata = getattr(chunk, "metadata", {}) or {}
        content = getattr(chunk, "content", None)

        # Reasoning-only streams are meaningful only when the client can render them.
        if metadata.get("_client_supports_reasoning_fields"):
            if isinstance(content, dict):
                if openai_dict_has_reasoning_output(content):
                    return True
            elif isinstance(content, str):
                parsed = self._try_parse_openai_sse_payloads(content)
                if parsed and any(openai_dict_has_reasoning_output(p) for p in parsed):
                    return True
            elif isinstance(content, bytes | bytearray):
                try:
                    decoded = content.decode("utf-8")
                except UnicodeDecodeError:
                    decoded = content.decode("utf-8", errors="ignore")
                parsed = self._try_parse_openai_sse_payloads(decoded)
                if parsed and any(openai_dict_has_reasoning_output(p) for p in parsed):
                    return True

        if self._metadata_has_meaningful_output(metadata):
            return True

        if self._content_has_meaningful_output(content):
            return True

        if count_reasoning_for_empty_stream:
            if isinstance(content, dict) and openai_dict_has_reasoning_output(content):
                return True
            if isinstance(content, str | bytes | bytearray):
                if isinstance(content, bytes | bytearray):
                    try:
                        decoded_reason = content.decode("utf-8")
                    except UnicodeDecodeError:
                        decoded_reason = content.decode("utf-8", errors="ignore")
                else:
                    decoded_reason = content
                parsed_reason = self._try_parse_openai_sse_payloads(decoded_reason)
                if parsed_reason and any(
                    openai_dict_has_reasoning_output(p) for p in parsed_reason
                ):
                    return True

        # Avoid treating OpenAI-shaped dict chunks as meaningful via JSON serialization.
        if isinstance(content, dict) and "choices" in content:
            return False

        # For textual/SSE payloads, the content check above is authoritative.
        # Do not fall back to generic extraction (which would treat any non-empty
        # SSE envelope as "meaningful" even when it's reasoning-only or [DONE]-only).
        if isinstance(content, str | bytes | bytearray):
            return False

        text = self._extract_text_from_chunk(chunk)
        return bool(text and text.strip())

    async def _create_retry_request(
        self, original_request: ChatRequest, recovery_prompt: str
    ) -> ChatRequest:
        """Create a retry request with the recovery prompt appended."""
        retry_messages = list(original_request.messages)
        recovery_message = ChatMessage(role="user", content=recovery_prompt)
        retry_messages.append(recovery_message)
        return original_request.model_copy(update={"messages": retry_messages})

    @staticmethod
    def _should_surface_pre_output_error(stream_error: Exception) -> bool:
        """Return True when a transient pre-output error must bypass empty retry."""
        if isinstance(
            stream_error,
            RateLimitExceededError
            | ServiceUnavailableError
            | APIConnectionError
            | APITimeoutError,
        ):
            return True

        status_code = getattr(stream_error, "status_code", None)
        if isinstance(status_code, int):
            # Surface all 4xx client errors immediately, retrying them is useless
            if 400 <= status_code < 500:
                return True
            # Common upstream / gateway failures: must not fall through to
            # "empty model output" recovery (misleading user prompt append).
            if status_code in {500, 502, 503, 504}:
                return True

        details = getattr(stream_error, "details", None)
        if isinstance(details, dict):
            details_status = details.get("status_code")
            if isinstance(details_status, int):
                if 400 <= details_status < 500:
                    return True
                if details_status in {500, 502, 503, 504}:
                    return True

        return False

    def _raise_empty_stream_error(self, session_id: str, reason: str) -> None:
        """Raise a backend error when no content is produced after retries."""
        raise BackendError(
            message="Upstream model returned no content after retries",
            backend_name=None,
            status_code=204,  # Use 204 No Content to trigger longer dedup window
            details={
                "session_id": session_id,
                "reason": reason,
                "error_type": "empty_stream_after_retries",
            },
        )

    def _extract_retry_state(self, request: ChatRequest) -> RetryState:
        """Extract retry count and active flag from request.

        Returns:
            RetryState with current_retry_count and reactor_retry_active fields.
        """
        extra_body = getattr(request, "extra_body", None)
        current_retry_count = 0
        if isinstance(extra_body, dict):
            current_retry_count = extra_body.get("_tool_call_reactor_retry_count", 0)
            legacy_count = extra_body.get("_dangerous_command_retry_count", 0)
            if isinstance(legacy_count, int) and legacy_count > current_retry_count:
                current_retry_count = legacy_count

        reactor_retry_active = bool(
            isinstance(extra_body, dict) and extra_body.get("_tool_call_reactor_retry")
        )
        return RetryState(
            current_retry_count=current_retry_count,
            reactor_retry_active=reactor_retry_active,
        )

    def _extract_quality_verifier_config(
        self, context: RequestContext
    ) -> QualityVerifierConfig:
        """Extract Quality Verifier configuration from context.

        Returns:
            QualityVerifierConfig containing model_spec and frequency
        """
        # Extract from RequestContext extensions if available
        # This follows the architectural pattern of using typed fields instead of direct app_state access
        quality_verifier_model_spec: str | None = None
        quality_verifier_frequency: int = 10
        quality_verifier_max_history: int | None = None
        quality_verifier_max_consecutive_failures: int = 5
        quality_verifier_cooldown_seconds: int = 300
        quality_verifier_ttft_timeout_seconds: float = 30.0
        eligible_turn_count: int | None = None
        skip_verification = False

        if hasattr(context, "extensions") and context.extensions:
            quality_verifier_model_spec_value = context.extensions.get(
                "quality_verifier_model", None
            )
            quality_verifier_model_spec = (
                str(quality_verifier_model_spec_value)
                if quality_verifier_model_spec_value is not None
                else None
            )
            quality_verifier_frequency_value = context.extensions.get(
                "quality_verifier_frequency", 10
            )
            # Convert JsonValue to int safely
            if quality_verifier_frequency_value is not None:
                if isinstance(quality_verifier_frequency_value, int | float):
                    quality_verifier_frequency = int(quality_verifier_frequency_value)
                elif isinstance(quality_verifier_frequency_value, str):
                    try:
                        quality_verifier_frequency = int(
                            quality_verifier_frequency_value
                        )
                    except (ValueError, TypeError):
                        quality_verifier_frequency = 10  # default value
                else:
                    quality_verifier_frequency = 10  # default value
            else:
                quality_verifier_frequency = 10  # default value

            quality_verifier_max_history_value = context.extensions.get(
                "quality_verifier_max_history", None
            )
            if quality_verifier_max_history_value is not None:
                if isinstance(quality_verifier_max_history_value, int | float):
                    quality_verifier_max_history = int(
                        quality_verifier_max_history_value
                    )
                elif isinstance(quality_verifier_max_history_value, str):
                    try:
                        quality_verifier_max_history = int(
                            quality_verifier_max_history_value
                        )
                    except (ValueError, TypeError):
                        quality_verifier_max_history = None
                else:
                    quality_verifier_max_history = None
            else:
                quality_verifier_max_history = None

            # Extract circuit breaker settings
            failures_value = context.extensions.get(
                "quality_verifier_max_consecutive_failures", 5
            )
            if isinstance(failures_value, int | float | str):
                with contextlib.suppress(ValueError, TypeError):
                    quality_verifier_max_consecutive_failures = int(failures_value)

            cooldown_value = context.extensions.get(
                "quality_verifier_cooldown_seconds", 300
            )
            if isinstance(cooldown_value, int | float | str):
                with contextlib.suppress(ValueError, TypeError):
                    quality_verifier_cooldown_seconds = int(cooldown_value)

            ttft_timeout_value = context.extensions.get(
                "quality_verifier_ttft_timeout_seconds", 30.0
            )
            if isinstance(ttft_timeout_value, int | float | str):
                with contextlib.suppress(ValueError, TypeError):
                    quality_verifier_ttft_timeout_seconds = float(ttft_timeout_value)
            if quality_verifier_ttft_timeout_seconds <= 0:
                quality_verifier_ttft_timeout_seconds = 30.0

            # Optional per-request eligible turn counter and skip flag
            eligible_turn_value = context.extensions.get(
                "quality_verifier_eligible_turn_count", None
            )
            eligible_turn_count = QualityVerifierService.coerce_eligible_turn_floor(
                eligible_turn_value
            )

            skip_value = context.extensions.get(
                "quality_verifier_skip_verification", None
            )
            if isinstance(skip_value, bool):
                skip_verification = skip_value
            elif isinstance(skip_value, str):
                skip_verification = skip_value.strip().lower() in {
                    "1",
                    "true",
                    "yes",
                    "on",
                }

        return QualityVerifierConfig(
            model_spec=quality_verifier_model_spec,
            frequency=quality_verifier_frequency,
            max_history=quality_verifier_max_history,
            max_consecutive_failures=quality_verifier_max_consecutive_failures,
            cooldown_seconds=quality_verifier_cooldown_seconds,
            ttft_timeout_seconds=quality_verifier_ttft_timeout_seconds,
            eligible_turn_count=eligible_turn_count,
            skip_verification=skip_verification,
        )

    def _try_resolve_icfg_from_context(self, context: RequestContext) -> Any:
        """Return IConfig from request context or global provider, or None."""
        from src.core.interfaces.configuration_interface import IConfig

        cfg: Any = None
        app_state = getattr(context, "app_state", None)
        if app_state is not None:
            try:
                service_provider = getattr(app_state, "service_provider", None)
                if service_provider is not None and hasattr(
                    service_provider, "get_service"
                ):
                    cfg_any = service_provider.get_service(cast(type[Any], IConfig))
                    if cfg_any is not None and hasattr(cfg_any, "session"):
                        cfg = cast(IConfig, cfg_any)
            except Exception:
                cfg = None

            for attr in ("config", "app_config"):
                try:
                    candidate = getattr(app_state, attr, None)
                except StateAccessViolationError:
                    continue
                if candidate is not None and hasattr(candidate, "session"):
                    cfg = cast(IConfig, candidate)
                    break

        if cfg is None and app_state is not None:
            try:
                from src.core.di.services import get_service_provider

                provider = get_service_provider()
                cfg_any = provider.get_service(cast(type[Any], IConfig))
                cfg = cast(IConfig | None, cfg_any)
            except Exception:
                cfg = None
        return cfg

    def _resolve_count_reasoning_for_empty_stream(
        self, context: RequestContext
    ) -> bool:
        cfg = self._try_resolve_icfg_from_context(context)
        if cfg is None:
            return True
        er = getattr(cfg, "empty_response", None)
        if er is None:
            return True
        return bool(getattr(er, "count_reasoning_for_empty_stream", True))

    def _resolve_client_reasoning_policy(
        self, context: RequestContext, request: ChatRequest
    ) -> tuple[bool, str]:
        """Resolve client reasoning policy once per stream (not per chunk)."""
        try:
            from src.core.common.client_compatibility import (
                resolve_client_reasoning_policy,
            )

            cfg = self._try_resolve_icfg_from_context(context)

            headers = getattr(context, "headers", None)
            ua_val = None
            if isinstance(headers, Mapping):
                ua_val = headers.get("user-agent") or headers.get("User-Agent")
            ua = ua_val if isinstance(ua_val, str) else None

            client_cfg = (
                getattr(getattr(cfg, "session", None), "client_compatibility", None)
                if cfg is not None
                else None
            )
            count_reasoning = self._resolve_count_reasoning_for_empty_stream(context)
            request_reasoning_signal = (
                chat_request_indicates_reasoning_output(request)
                if count_reasoning
                else False
            )
            policy = resolve_client_reasoning_policy(
                headers=headers,
                client_config=client_cfg,
                user_agent=ua,
                request_indicates_reasoning_output=request_reasoning_signal,
            )
            return bool(policy.reasoning_counts_as_meaningful), str(
                policy.reasoning_mode
            )
        except Exception:
            return False, "passthrough"

    def _wrap_with_middleware(
        self,
        original_stream: AsyncIterator[ProcessedResponse],
        processing_context: ResponseProcessingContext,
        request_context: RequestContext,
    ) -> AsyncIterator[ProcessedResponse]:
        """Wrap stream with response processor middleware with fail-open behavior."""
        backend_name = processing_context.backend_name or ""
        if "gemini" in backend_name.lower():
            return original_stream

        try:
            # Create a new RequestContext with backend and model info from processing_context
            # This matches the non-streaming handler pattern of cloning instead of mutating
            original_request = (
                processing_context.original_request or request_context.original_request
            )
            enriched_context = RequestContext(
                headers=request_context.headers,
                cookies=request_context.cookies,
                state=request_context.state,
                app_state=request_context.app_state,
                client_host=request_context.client_host,
                session_id=processing_context.session_id or request_context.session_id,
                request_id=request_context.request_id,
                agent=request_context.agent,
                original_request=original_request,
                processing_context=request_context.processing_context,
                domain_request=request_context.domain_request,
                raw_body=request_context.raw_body,
                backend=processing_context.backend_name,
                effective_model=processing_context.model_name,
                extensions=request_context.extensions,
                original_domain_request=request_context.original_domain_request,
            )
            processed_stream = self._response_processor.process_streaming_response(
                original_stream,
                processing_context.session_id,
                enriched_context,
            )
            if not hasattr(processed_stream, "__aiter__"):
                close = getattr(processed_stream, "close", None)
                if callable(close):
                    with contextlib.suppress(Exception):
                        close()
                raise TypeError("Streaming middleware returned non-async-iterator")
            return processed_stream
        except (KeyboardInterrupt, SystemExit):
            # Re-raise system exceptions to allow proper cleanup
            raise
        except asyncio.CancelledError:
            # Re-raise cancellation to allow proper cleanup
            raise
        except (
            LLMProxyError,
            ParsingError,
            TranslationError,
            TypeError,
            ValueError,
            AttributeError,
            KeyError,
        ) as e:
            # Catch domain exceptions and common data processing errors for fail-open behavior
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Streaming middleware failed for session %s: %s",
                    processing_context.session_id,
                    e,
                    exc_info=True,
                )
            return original_stream
        except Exception as e:
            # Catch-all for unexpected application-level errors - log with full context but still fail-open
            # System exceptions (KeyboardInterrupt, SystemExit, CancelledError) are excluded above
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Unexpected error in streaming middleware for session %s: %s",
                    processing_context.session_id,
                    e,
                    exc_info=True,
                )
            return original_stream

    async def _preprocess_synthetic_blocking_stream(
        self,
        original_stream: AsyncIterator[ProcessedResponse],
        processing_context: ResponseProcessingContext,
        request_context: RequestContext,
    ) -> AsyncIterator[ProcessedResponse]:
        """Apply non-streaming ``process_response`` semantics to a single synthetic chunk.

        Coordinator wraps blocking ``ResponseEnvelope`` values as one-chunk streams so
        all business logic runs through this handler; that must still invoke the same
        ``IResponseProcessor.process_response`` entrypoint as the retired non-streaming
        handler so middleware (tool-call steering, etc.) observes identical inputs.
        """

        chunks: list[ProcessedResponse] = []
        async for raw in original_stream:
            chunks.append(self._coerce_processed_chunk(raw))

        async def _replay_many() -> AsyncIterator[ProcessedResponse]:
            for item in chunks:
                yield item

        if len(chunks) != 1:
            return _replay_many()

        chunk = chunks[0]
        if chunk.metadata.get("_parallel_completion_aggregated") is True:

            async def _preserve_parallel_aggregate() -> (
                AsyncIterator[ProcessedResponse]
            ):
                yield chunk

            return _preserve_parallel_aggregate()

        original_request = (
            processing_context.original_request or request_context.original_request
        )
        enriched_context = RequestContext(
            headers=request_context.headers,
            cookies=request_context.cookies,
            state=request_context.state,
            app_state=request_context.app_state,
            client_host=request_context.client_host,
            session_id=processing_context.session_id or request_context.session_id,
            request_id=request_context.request_id,
            agent=request_context.agent,
            original_request=original_request,
            processing_context=request_context.processing_context,
            domain_request=request_context.domain_request,
            raw_body=request_context.raw_body,
            backend=processing_context.backend_name,
            effective_model=processing_context.model_name,
            extensions=request_context.extensions,
            original_domain_request=request_context.original_domain_request,
        )

        processed_any = await self._response_processor.process_response(
            chunk.content,
            processing_context.session_id or "",
            enriched_context,
        )
        processed = (
            processed_any
            if isinstance(processed_any, ProcessedResponse)
            else self._coerce_processed_chunk(processed_any)
        )

        retry_metadata_keys = (
            "dangerous_command_retry_count",
            "tool_call_reactor_retry_count",
            "steering_retry_occurred",
            "tool_call_reactor_retry_failed",
        )
        for key, value in (chunk.metadata or {}).items():
            if key in retry_metadata_keys or key not in processed.metadata:
                processed.metadata[key] = value

        metadata = processed.metadata or {}
        if (
            self._structured_output_enforcer is not None
            and processing_context.structured_output is not None
            and not metadata.get("structured_output_validated", False)
            and not metadata.get("schema_validation_attempted", False)
        ):
            processed = await self._structured_output_enforcer.enforce(
                response=processed,
                context=processing_context.structured_output,
            )

        async def _one() -> AsyncIterator[ProcessedResponse]:
            yield processed

        return _one()

    def _create_loop_detector(self, session_id: str) -> ILoopDetector | None:
        """Create loop detector with fail-open behavior."""
        try:
            return self._loop_detector_factory.create()
        except Exception as err:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to create loop detector for session %s: %s",
                    session_id,
                    err,
                    exc_info=True,
                )
            return None

    async def _apply_quality_verifier_verification(
        self,
        request: ChatRequest,
        processed_stream: AsyncIterator[ProcessedResponse],
        processing_context: ResponseProcessingContext,
        request_context: RequestContext,
        quality_verifier_model_spec: str | None,
        quality_verifier_frequency: int,
        quality_verifier_max_history: int | None,
        quality_verifier_max_consecutive_failures: int,
        quality_verifier_cooldown_seconds: int,
        quality_verifier_ttft_timeout_seconds: float,
        quality_verifier_eligible_turn_count: int | None,
        quality_verifier_skip_verification: bool,
    ) -> AsyncIterator[ProcessedResponse]:
        """Apply Quality Verifier with fail-open behavior."""
        # Extract stream_id from request_context
        stream_id = processing_context.session_id
        if request_context.request_id is not None:
            stream_id = request_context.request_id
        elif request_context.processing_context is not None:
            processing_values = request_context.processing_context.values
            # ProcessingContext.values is dict[str, Any], no isinstance check needed
            stream_id = (
                processing_values.get("stream_id", processing_context.session_id)
                or processing_context.session_id
            )

        qv_eligible_raw: Any = None
        try:
            qv_eligible_raw = request_context.extensions.get(
                "quality_verifier_eligible_turn_count"
            )
        except Exception:
            qv_eligible_raw = None

        streaming_context: StreamingContext = {
            "session_id": processing_context.session_id,
            "stream_id": stream_id,
            "quality_verifier_model_spec": quality_verifier_model_spec,
            "quality_verifier_frequency": quality_verifier_frequency,
            "quality_verifier_max_history": quality_verifier_max_history,
            "quality_verifier_max_consecutive_failures": quality_verifier_max_consecutive_failures,
            "quality_verifier_cooldown_seconds": quality_verifier_cooldown_seconds,
            "quality_verifier_ttft_timeout_seconds": quality_verifier_ttft_timeout_seconds,
            "quality_verifier_eligible_turn_count": quality_verifier_eligible_turn_count,
            "quality_verifier_eligible_turn_raw": qv_eligible_raw,
            "quality_verifier_skip_verification": quality_verifier_skip_verification,
        }

        try:
            # Use RequestContext directly for cancellation gate

            # verify_or_passthrough is an async generator, returns AsyncIterator directly
            verified_stream = (
                self._quality_verifier_stream_verifier.verify_or_passthrough(
                    request=request,
                    stream=processed_stream,
                    context=streaming_context,
                    request_context=request_context,
                )
            )
            if not hasattr(verified_stream, "__aiter__"):
                close = getattr(verified_stream, "close", None)
                if callable(close):
                    with contextlib.suppress(Exception):
                        close()
                raise TypeError("Quality Verifier returned non-async-iterator stream")
            return verified_stream
        except Exception as err:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Quality Verifier failed for session %s, using original stream: %s",
                    processing_context.session_id,
                    err,
                    exc_info=True,
                )
            return processed_stream

    async def _handle_tool_call_swallowed_stream(
        self,
        chunk: ProcessedResponse,
        request: ChatRequest,
        context: RequestContext,
        processing_context: ResponseProcessingContext,
    ) -> AsyncIterator[ProcessedResponse] | None:
        """Handle tool-call swallowed detection and retry coordination.

        Returns:
            AsyncIterator of retried chunks if retry occurred, None otherwise
        """
        metadata = getattr(chunk, "metadata", {}) or {}
        retry_state = self._extract_retry_state(request)

        # Skip retry if marker is present and limit not exceeded (prevents infinite loops)
        if (
            request.stream
            and retry_state.reactor_retry_active
            and retry_state.current_retry_count < 3
        ):
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Streaming: Skipping tool-call retry (marker present, count=%d) for session %s",
                    retry_state.current_retry_count,
                    processing_context.session_id,
                )
            return None

        # Check if limit exceeded
        if (
            request.stream
            and retry_state.reactor_retry_active
            and retry_state.current_retry_count >= 3
        ):
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Streaming: Tool call retry limit exceeded for session %s",
                    processing_context.session_id,
                    exc_info=True,
                )
            terminal_metadata: dict[str, JsonValue] = {
                "dangerous_command_limit_exceeded": True,
                "dangerous_command_retry_count": retry_state.current_retry_count + 1,
                "tool_call_reactor_retry_count": retry_state.current_retry_count + 1,
                "session_terminated": True,
                "is_done": True,
                "finish_reason": "security_limit",
                "_steering_replacement": True,
                "session_id": processing_context.session_id,
            }

            async def terminal_chunk() -> AsyncIterator[ProcessedResponse]:
                yield ProcessedResponse(
                    content="[Proxy Steering - Session Terminated]\n\n"
                    "This session has been terminated due to repeated attempts "
                    "to perform blocked tool calls.",
                    metadata=terminal_metadata,
                )

            return terminal_chunk()

        # Delegate to coordinator
        try:
            from src.core.domain.backend_request_manager.context_models import (
                ToolCallRetryState,
            )

            response_envelope = ResponseEnvelope(
                content=chunk.content,
                metadata=metadata,
            )

            tool_call_retry_state = ToolCallRetryState(
                retry_count=retry_state.current_retry_count,
                max_retries=3,
                steering_message=None,
                is_streaming=bool(request.stream),
            )

            retry_result: ResponseEnvelope | StreamingResponseEnvelope | None
            if request.stream:
                retry_result = await self._tool_call_retry_coordinator.handle_streaming(
                    request=request,
                    response=response_envelope,
                    context=context,
                    retry_state=tool_call_retry_state,
                )
            else:
                retry_result = (
                    await self._tool_call_retry_coordinator.handle_non_streaming(
                        request=request,
                        response=response_envelope,
                        context=context,
                        retry_state=tool_call_retry_state,
                    )
                )

            if retry_result is not None:
                if isinstance(retry_result, ResponseEnvelope):
                    retry_stream_env = response_envelope_as_single_chunk_stream(
                        retry_result
                    )
                    retry_stream_body = retry_stream_env.content
                    if retry_stream_body is None:
                        return None

                    async def retry_blocking_chunks() -> (
                        AsyncIterator[ProcessedResponse]
                    ):
                        restream = await self._preprocess_synthetic_blocking_stream(
                            retry_stream_body,
                            processing_context,
                            context,
                        )
                        async for retry_chunk in restream:
                            retry_meta = dict(retry_chunk.metadata or {})
                            retry_meta["_steering_replacement"] = True
                            yield ProcessedResponse(
                                content=retry_chunk.content,
                                metadata=retry_meta,
                                usage=retry_chunk.usage,
                            )

                    return retry_blocking_chunks()

                if retry_result.content is not None:

                    async def retry_chunks() -> AsyncIterator[ProcessedResponse]:
                        if retry_result.content is None:
                            return
                        async for retry_chunk in retry_result.content:
                            retry_meta = dict(retry_chunk.metadata or {})
                            retry_meta["_steering_replacement"] = True
                            yield ProcessedResponse(
                                content=retry_chunk.content,
                                metadata=retry_meta,
                                usage=retry_chunk.usage,
                            )

                    return retry_chunks()
        except (TypeError, AttributeError, RuntimeError, asyncio.CancelledError) as err:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Tool-call retry coordination failed for session %s: %s",
                    processing_context.session_id,
                    err,
                    exc_info=True,
                )
        return None

    async def handle(  # noqa: C901
        self,
        stream: StreamingResponseEnvelope,
        request: ChatRequest,
        context: RequestContext,
        processing_context: ResponseProcessingContext,
        retry_depth: int = 0,
    ) -> StreamingResponseEnvelope:
        """Return a processed streaming response envelope.

        Args:
            stream: The streaming response envelope
            request: The original backend request
            context: Request context
            processing_context: Typed processing context
            retry_depth: Internal retry depth counter to prevent infinite recursion (default: 0)

        Returns:
            A processed streaming response envelope with middleware applied
        """
        original_stream = stream.content
        if original_stream is None:
            # Empty stream - trigger retry
            if processing_context.session_id:
                self._raise_empty_stream_error(
                    session_id=processing_context.session_id,
                    reason="streaming_response_had_no_content_iterator",
                )
            return stream

        # Use original context to avoid direct access to app_state in service layer
        # Extract Quality Verifier config from context if available
        quality_verifier_config = self._extract_quality_verifier_config(context)
        quality_verifier_model_spec = quality_verifier_config.model_spec
        quality_verifier_frequency = quality_verifier_config.frequency
        quality_verifier_max_history = quality_verifier_config.max_history

        stream_meta = stream.metadata or {}
        synthetic_blocking = bool(stream_meta.get("_synthetic_blocking_envelope"))
        if synthetic_blocking:
            try:
                original_stream = await self._preprocess_synthetic_blocking_stream(
                    original_stream, processing_context, context
                )
            except EmptyResponseRetryError as exc:
                # Preserve historical non-streaming retry contract: middleware may
                # signal empty blocking bodies via ``EmptyResponseRetryError`` before any
                # streaming empty-recovery runs.
                session_key = resolve_session_key_from_request_context(context)
                if (
                    self._cancellation_coordinator is not None
                    and session_key is not None
                ):
                    self._cancellation_coordinator.ensure_not_cancelled(session_key)

                if logger.isEnabledFor(logging.INFO):
                    logger.info(
                        "Empty blocking response for session %s, retrying with recovery prompt",
                        exc.session_id,
                        exc_info=True,
                    )

                base_request = exc.original_request or request
                if exc.recovery_prompt:
                    retry_request = await self._create_retry_request(
                        base_request, exc.recovery_prompt
                    )
                else:
                    retry_request = base_request

                try:
                    retry_response = (
                        await self._backend_processor.process_backend_request(
                            request=retry_request,
                            session_id=processing_context.session_id,
                            context=context,
                        )
                    )
                except SessionCancelledError:
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Retry backend request cancelled for session %s",
                            processing_context.session_id,
                        )
                    raise

                if isinstance(retry_response, ResponseEnvelope):
                    return await self.handle(
                        stream=response_envelope_as_single_chunk_stream(retry_response),
                        request=retry_request,
                        context=context,
                        processing_context=processing_context,
                        retry_depth=retry_depth,
                    )

                return await self.handle(
                    stream=retry_response,
                    request=retry_request,
                    context=context,
                    processing_context=processing_context,
                    retry_depth=retry_depth,
                )

        if synthetic_blocking:
            # Synthetic blocking payloads already went through process_response() in
            # _preprocess_synthetic_blocking_stream(); avoid re-running stream wrappers
            # (which can turn canonical single payloads into terminal-only markers).
            processed_stream = original_stream
            loop_detector = None
            verified_stream = processed_stream
        else:
            # Wrap stream with response processor middleware
            processed_stream = self._wrap_with_middleware(
                original_stream, processing_context, context
            )

            # Create loop detector
            loop_detector = self._create_loop_detector(processing_context.session_id)

            # Wrap with Quality Verifier if enabled (outer pass only). Nested tool-retry
            # ``handle()`` recursion replays a fresh synthetic stream; running the QV
            # wrapper again here can yield an empty iterator and break non-streaming recall.
            if retry_depth > 0:
                verified_stream = processed_stream
            else:
                verified_stream = await self._apply_quality_verifier_verification(
                    request,
                    processed_stream,
                    processing_context,
                    context,
                    quality_verifier_model_spec,
                    quality_verifier_frequency,
                    quality_verifier_max_history,
                    quality_verifier_config.max_consecutive_failures,
                    quality_verifier_config.cooldown_seconds,
                    quality_verifier_config.ttft_timeout_seconds,
                    quality_verifier_config.eligible_turn_count,
                    quality_verifier_config.skip_verification,
                )

        # Process stream with loop detection, tool-call retry, and empty-stream recovery
        async def monitored_stream() -> AsyncIterator[ProcessedResponse]:
            swallowed_detected = False

            async for raw_chunk in verified_stream:
                chunk = self._coerce_processed_chunk(raw_chunk)
                # Check for tool-call swallowed
                metadata = getattr(chunk, "metadata", {}) or {}
                if (
                    metadata.get("tool_call_swallowed")
                    and not metadata.get("tool_call_reactor_retry_failed")
                    and not swallowed_detected
                ):
                    swallowed_detected = True
                    # Get retry state from request using the helper method
                    retry_state = self._extract_retry_state(request)

                    # Skip retry if marker is present and limit not exceeded (prevents infinite loops)
                    if (
                        request.stream
                        and retry_state.reactor_retry_active
                        and retry_state.current_retry_count < 3
                    ):
                        # Retry marker present but below limit - skip retry to prevent loops
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                "Streaming: Skipping tool-call retry (marker present, count=%d) for session %s",
                                retry_state.current_retry_count,
                                processing_context.session_id,
                            )
                        # Continue with original chunk (no retry)
                        yield chunk
                        continue

                    # Check if limit exceeded before delegating
                    if (
                        request.stream
                        and retry_state.reactor_retry_active
                        and retry_state.current_retry_count >= 3
                    ):  # MAX_DANGEROUS_COMMAND_RETRIES
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Streaming: Tool call retry limit exceeded for session %s",
                                processing_context.session_id,
                                exc_info=True,
                            )
                        # Yield terminal error chunk
                        terminal_metadata: dict[str, JsonValue] = {
                            "dangerous_command_limit_exceeded": True,
                            "dangerous_command_retry_count": retry_state.current_retry_count
                            + 1,
                            "tool_call_reactor_retry_count": retry_state.current_retry_count
                            + 1,
                            "session_terminated": True,
                            "is_done": True,
                            "finish_reason": "security_limit",
                            "_steering_replacement": True,
                            "session_id": processing_context.session_id,
                        }
                        yield ProcessedResponse(
                            content="[Proxy Steering - Session Terminated]\n\n"
                            "This session has been terminated due to repeated attempts "
                            "to perform blocked tool calls.",
                            metadata=terminal_metadata,
                        )
                        return

                    # Delegate to coordinator (always delegate when tool_call_swallowed detected)
                    try:
                        from src.core.domain.backend_request_manager.context_models import (
                            ToolCallRetryState,
                        )

                        # Create a response envelope for coordinator
                        response_envelope = ResponseEnvelope(
                            content=chunk.content,
                            metadata=metadata,
                        )

                        tool_call_retry_state = ToolCallRetryState(
                            retry_count=retry_state.current_retry_count,
                            max_retries=3,
                            steering_message=None,
                            is_streaming=bool(request.stream),
                        )

                        retry_result: (
                            ResponseEnvelope | StreamingResponseEnvelope | None
                        )
                        if request.stream:
                            retry_result = await self._tool_call_retry_coordinator.handle_streaming(
                                request=request,
                                response=response_envelope,
                                context=context,
                                retry_state=tool_call_retry_state,
                            )
                        else:
                            retry_result = await self._tool_call_retry_coordinator.handle_non_streaming(
                                request=request,
                                response=response_envelope,
                                context=context,
                                retry_state=tool_call_retry_state,
                            )

                        if retry_result is not None:
                            if isinstance(retry_result, ResponseEnvelope):
                                retry_md = retry_result.metadata or {}
                                updated_extra_body = dict(
                                    getattr(request, "extra_body", None) or {}
                                )
                                tcr_val = retry_md.get(
                                    "tool_call_reactor_retry_count",
                                    tool_call_retry_state.retry_count,
                                )
                                updated_extra_body["_tool_call_reactor_retry_count"] = (
                                    int(tcr_val)
                                    if isinstance(tcr_val, int | float | str)
                                    else tool_call_retry_state.retry_count
                                )
                                dcc_val = retry_md.get(
                                    "dangerous_command_retry_count",
                                    tool_call_retry_state.retry_count,
                                )
                                updated_extra_body["_dangerous_command_retry_count"] = (
                                    int(dcc_val)
                                    if isinstance(dcc_val, int | float | str)
                                    else tool_call_retry_state.retry_count
                                )
                                updated_extra_body["_tool_call_reactor_retry"] = True
                                updated_request = request.model_copy(
                                    update={"extra_body": updated_extra_body}
                                )

                                nested_in = response_envelope_as_single_chunk_stream(
                                    retry_result
                                )
                                nested_out = await self.handle(
                                    stream=nested_in,
                                    request=updated_request,
                                    context=context,
                                    processing_context=processing_context,
                                    retry_depth=retry_depth + 1,
                                )
                                if nested_out.content is not None:
                                    async for retry_chunk in nested_out.content:
                                        retry_meta = dict(retry_chunk.metadata or {})
                                        retry_meta.setdefault(
                                            "_steering_replacement", True
                                        )
                                        yield ProcessedResponse(
                                            content=retry_chunk.content,
                                            metadata=retry_meta,
                                            usage=retry_chunk.usage,
                                        )
                                return
                            # Streaming retry envelope
                            if retry_result.content is not None:
                                async for retry_chunk in retry_result.content:
                                    retry_meta = dict(retry_chunk.metadata or {})
                                    retry_meta["_steering_replacement"] = True
                                    yield ProcessedResponse(
                                        content=retry_chunk.content,
                                        metadata=retry_meta,
                                        usage=retry_chunk.usage,
                                    )
                            return
                    except (TypeError, AttributeError, RuntimeError) as err:
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Tool-call retry coordination failed for session %s: %s",
                                processing_context.session_id,
                                err,
                                exc_info=True,
                            )

                # Run loop detection
                if loop_detector is not None:
                    try:
                        text_fragment = self._extract_text_from_chunk(chunk)
                        if text_fragment:
                            clean_fragment = text_fragment.strip()
                            if not clean_fragment.startswith(("data:", "event:")):
                                event = loop_detector.process_chunk(text_fragment)
                                if event is not None:
                                    # Cancel stream
                                    cancel_callback = stream.cancel_callback
                                    if cancel_callback is not None:
                                        try:
                                            if isinstance(cancel_callback, Callable):  # type: ignore[arg-type]
                                                if asyncio.iscoroutinefunction(
                                                    cancel_callback
                                                ):
                                                    await cancel_callback()
                                                else:
                                                    cancel_callback()
                                        except Exception as exc:
                                            if logger.isEnabledFor(logging.ERROR):
                                                logger.error(
                                                    "Failed to invoke cancel callback: %s",
                                                    exc,
                                                    exc_info=True,
                                                )

                                    # Emit cancellation chunk
                                    cancellation_payload = {
                                        "id": f"loop-detector-{int(event.timestamp)}",
                                        "object": "chat.completion.chunk",
                                        "created": int(event.timestamp),
                                        "model": "loop-detector",
                                        "choices": [
                                            {
                                                "index": 0,
                                                "delta": {"content": ""},
                                                "finish_reason": "cancelled",
                                            }
                                        ],
                                    }
                                    yield ProcessedResponse(
                                        content=cast(Any, cancellation_payload),
                                        metadata={
                                            "is_cancellation": True,
                                            "is_done": True,
                                            "loop_pattern": event.pattern,
                                            "loop_repetitions": event.repetition_count,
                                            "loop_detected": True,
                                            "session_id": processing_context.session_id,
                                        },
                                    )
                                    return
                    except (TypeError, AttributeError, RuntimeError, ValueError) as err:
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Loop detection failed for chunk in session %s: %s",
                                processing_context.session_id,
                                err,
                                exc_info=True,
                            )

                yield chunk

        # Attach metadata to chunks
        async def attach_metadata_stream() -> AsyncIterator[ProcessedResponse]:
            original_request_payload: JsonValue | None = None
            try:
                if hasattr(request, "model_dump"):
                    original_request_payload = cast(
                        JsonValue, request.model_dump(mode="json")
                    )
            except (AttributeError, TypeError, ValueError):
                original_request_payload = None

            (
                client_reasoning_counts_as_meaningful,
                client_reasoning_mode,
            ) = self._resolve_client_reasoning_policy(context, request)

            async for chunk in monitored_stream():
                # monitored_stream() returns AsyncIterator[ProcessedResponse], so chunk is always ProcessedResponse
                # NFR1.3: Preserve copy-on-write behavior - create new instance instead of mutating
                # Start with existing metadata or empty dict
                processed_metadata = dict(chunk.metadata) if chunk.metadata else {}

                # Backend-specific defaults must be applied outside of the
                # client-compatibility resolution block (which is best-effort and
                # may fail if config/DI is unavailable).
                try:
                    from src.core.common.backend_discovery_state import (
                        normalize_backend_name,
                    )

                    normalized_backend = normalize_backend_name(
                        processing_context.backend_name or ""
                    )
                except Exception:
                    normalized_backend = (processing_context.backend_name or "").lower()

                if normalized_backend in {"qwen-oauth", "zai-coding-plan"}:
                    processed_metadata.setdefault(
                        "_client_supports_reasoning_fields", True
                    )
                    processed_metadata.setdefault("reasoning_is_output", True)
                    # Strip reasoning aliases but keep `reasoning_content`.
                    processed_metadata.setdefault("_suppress_reasoning_fields", True)
                    processed_metadata.setdefault("_keep_reasoning_content", True)
                    processed_metadata.setdefault(
                        "_coerce_reasoning_into_content", False
                    )

                if original_request_payload is not None:
                    processed_metadata.setdefault(
                        "original_request", original_request_payload
                    )
                processed_metadata.setdefault(
                    "session_id", processing_context.session_id
                )
                if processing_context.client_os:
                    processed_metadata.setdefault(
                        "client_os", cast(JsonValue, processing_context.client_os)
                    )

                if client_reasoning_counts_as_meaningful:
                    processed_metadata.setdefault(
                        "_client_supports_reasoning_fields", True
                    )

                if client_reasoning_mode != "passthrough":
                    processed_metadata.setdefault("_suppress_reasoning_fields", True)
                    if client_reasoning_mode == "drop":
                        processed_metadata.setdefault(
                            "_coerce_reasoning_into_content", False
                        )
                # Create new ProcessedResponse instance with updated metadata (copy-on-write)
                yield ProcessedResponse(
                    content=chunk.content,
                    usage=chunk.usage,
                    metadata=processed_metadata,
                )

        count_reasoning_for_empty_stream = (
            self._resolve_count_reasoning_for_empty_stream(context)
        )

        # Gate empty stream
        async def gate_empty_stream() -> AsyncIterator[ProcessedResponse]:
            seen_meaningful = False

            pending_terminal: list[ProcessedResponse] = []

            async def _emit_terminal_stream_error(
                error: Exception,
            ) -> ProcessedResponse:
                provider = processing_context.backend_name or "unknown"
                model_name = processing_context.model_name or provider
                terminal_chunk = await handle_streaming_error(
                    error,
                    stream_id=processing_context.session_id,
                    provider=provider,
                )
                terminal_metadata = dict(getattr(terminal_chunk, "metadata", {}) or {})
                terminal_metadata.setdefault(
                    "session_id", processing_context.session_id
                )
                terminal_metadata.setdefault("model", model_name)
                terminal_metadata.setdefault("is_done", True)
                terminal_metadata.setdefault("finish_reason", "error")

                error_payload = terminal_metadata.get("error")
                if not isinstance(error_payload, dict):
                    error_payload = {
                        "type": type(error).__name__,
                        "message": str(error),
                        "code": "stream_failed",
                    }
                    terminal_metadata["error"] = error_payload

                terminal_content = getattr(terminal_chunk, "content", "")
                if not terminal_content:
                    import time

                    terminal_content = {
                        "id": f"chatcmpl-error-{int(time.time())}",
                        "object": "chat.completion.chunk",
                        "created": int(time.time()),
                        "model": model_name,
                        "choices": [
                            {"index": 0, "delta": {}, "finish_reason": "error"}
                        ],
                        "error": {
                            "type": error_payload.get("type") or type(error).__name__,
                            "message": error_payload.get("message") or str(error),
                            "code": error_payload.get("code") or "stream_failed",
                            "status_code": error_payload.get("status_code"),
                        },
                    }

                return ProcessedResponse(
                    content=normalize_to_processed_chunk_content(terminal_content),
                    metadata=terminal_metadata,
                    usage=getattr(terminal_chunk, "usage", None),
                )

            def _is_terminal_chunk(ch: ProcessedResponse) -> bool:
                md = getattr(ch, "metadata", {}) or {}
                if md.get("is_done") is True:
                    return True

                fr = md.get("finish_reason")
                if isinstance(fr, str) and fr in {
                    "stop",
                    "length",
                    "tool_calls",
                    "error",
                    "cancelled",
                    "security_limit",
                }:
                    return True

                c = getattr(ch, "content", None)
                if isinstance(c, str):
                    return "data: [DONE]" in c or c.strip() == "[DONE]"
                if isinstance(c, bytes | bytearray):
                    try:
                        decoded = c.decode("utf-8")
                    except UnicodeDecodeError:
                        decoded = c.decode("utf-8", errors="ignore")
                    return "data: [DONE]" in decoded or decoded.strip() == "[DONE]"

                return False

            def _is_terminal_error_chunk(ch: ProcessedResponse) -> bool:
                md = getattr(ch, "metadata", {}) or {}
                if md.get("error"):
                    return True
                if md.get("finish_reason") == "error":
                    return True
                c = getattr(ch, "content", None)
                if isinstance(c, dict) and c.get("error"):
                    return True

                raw_str: str | None = None
                if isinstance(c, str):
                    raw_str = c
                elif isinstance(c, bytes | bytearray):
                    try:
                        raw_str = c.decode("utf-8")
                    except UnicodeDecodeError:
                        raw_str = c.decode("utf-8", errors="ignore")

                if raw_str:
                    if (
                        '"finish_reason": "error"' in raw_str
                        or '"finish_reason":"error"' in raw_str
                    ):
                        return True
                    if '"error"' in raw_str:
                        payload_str = raw_str.strip()
                        if payload_str.startswith("data:"):
                            payload_str = payload_str[5:].strip()
                        if payload_str.startswith("{") and payload_str.endswith("}"):
                            try:
                                parsed = json.loads(payload_str)
                                if isinstance(parsed, dict) and parsed.get("error"):
                                    return True
                            except Exception:
                                pass
                return False

            try:
                async for chunk in attach_metadata_stream():
                    if _is_terminal_error_chunk(chunk):
                        seen_meaningful = True
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                "Terminal error chunk detected; suppressing empty-stream recovery for session %s",
                                processing_context.session_id,
                            )
                        yield chunk
                        continue

                    meaningful = self._chunk_has_meaningful_output(
                        chunk,
                        count_reasoning_for_empty_stream=count_reasoning_for_empty_stream,
                    )

                    if meaningful:
                        if not seen_meaningful:
                            mark_stream_meaningful_output(context)
                        seen_meaningful = True
                        yield chunk
                        continue

                    # To avoid long periods of silence (client timeouts) we stream
                    # non-meaningful chunks through, but we hold back terminal
                    # markers until we decide whether an empty-stream retry is needed.
                    if not seen_meaningful and _is_terminal_chunk(chunk):
                        pending_terminal.append(chunk)
                        continue

                    yield chunk
            except EmptyResponseRetryError:
                raise
            except Exception as stream_error:
                if not seen_meaningful:
                    if self._should_surface_pre_output_error(stream_error):
                        yield await _emit_terminal_stream_error(stream_error)
                        return
                    raise EmptyResponseRetryError(
                        recovery_prompt="",
                        session_id=processing_context.session_id,
                        retry_count=retry_depth + 1,
                        original_request=request,
                    ) from stream_error

                yield await _emit_terminal_stream_error(stream_error)
                return

            if not seen_meaningful:
                # Use retry_depth + 1 to match middleware's retry_count tracking
                # (retry_count starts at 1 for first retry)
                raise EmptyResponseRetryError(
                    recovery_prompt=self._empty_stream_recovery_prompt,
                    session_id=processing_context.session_id,
                    retry_count=retry_depth + 1,
                    original_request=request,
                )

            if pending_terminal:
                for terminal_chunk in pending_terminal:
                    yield terminal_chunk

        # Handle empty stream recovery
        async def stream_with_empty_recovery() -> AsyncIterator[ProcessedResponse]:
            try:
                async for chunk in gate_empty_stream():
                    yield chunk
            except EmptyResponseRetryError as exc:
                # Check retry_count from exception (starts at 1, so > means exceeded)
                if exc.retry_count > self._max_empty_stream_retries:
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            "Maximum empty stream recovery attempts reached for session %s",
                            processing_context.session_id,
                        )
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Empty stream recovery exhausted for session %s (retry=%s): %s",
                            processing_context.session_id,
                            exc.retry_count,
                            exc,
                        )
                    # Do not raise an exception from inside the streaming generator.
                    # Emit a terminal, OpenAI-compatible *error* chunk instead.
                    #
                    # IMPORTANT: Never surface proxy diagnostics as assistant content.
                    # Many clients treat assistant text as model output; error details
                    # must be carried via the structured `error` field.
                    provider = processing_context.backend_name or "unknown"
                    model_name = processing_context.model_name or "unknown"
                    terminal_error = BackendError(
                        message="Upstream model returned no user-visible content after retries.",
                        backend_name=provider,
                        status_code=502,
                        code="empty_stream_after_retries",
                        details={
                            "session_id": processing_context.session_id,
                            "provider": provider,
                            "model": model_name,
                            "reason": "empty_stream_after_retries",
                        },
                    )
                    terminal_chunk = await handle_streaming_error(
                        terminal_error,
                        stream_id=processing_context.session_id,
                        provider=provider,
                    )
                    terminal_metadata = dict(
                        getattr(terminal_chunk, "metadata", {}) or {}
                    )
                    terminal_metadata.setdefault(
                        "session_id", processing_context.session_id
                    )
                    terminal_metadata.setdefault("model", model_name)
                    terminal_metadata.setdefault("is_done", True)
                    terminal_metadata["proxy_warning"] = {
                        "message": "empty_stream_after_retries",
                        "type": "empty_stream_after_retries",
                    }

                    yield ProcessedResponse(
                        content=normalize_to_processed_chunk_content(
                            terminal_chunk.to_bytes()
                        ),
                        metadata=terminal_metadata,
                        usage=getattr(terminal_chunk, "usage", None),
                    )
                    return

                session_key = None
                if self._backend_work_guard is not None:
                    session_key = self._backend_work_guard.ensure_session_active(
                        context=context,
                        purpose="empty_stream_retry",
                        require_scope=False,
                    )
                elif self._cancellation_coordinator and context:
                    session_key = resolve_session_key_from_request_context(context)

                def _should_abort_for_cancellation() -> bool:
                    if self._backend_work_guard is not None:
                        return self._backend_work_guard.is_cancelled(session_key)
                    if self._cancellation_coordinator is None or session_key is None:
                        return False
                    try:
                        self._cancellation_coordinator.ensure_not_cancelled(session_key)
                        return False
                    except SessionCancelledError:
                        return True

                if _should_abort_for_cancellation():
                    return

                if logger.isEnabledFor(logging.INFO):
                    logger.info(
                        "Empty streaming response detected, retrying with recovery prompt for session %s",
                        processing_context.session_id,
                    )
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Empty streaming response retry triggered for session %s (retry=%s): %s",
                        processing_context.session_id,
                        exc.retry_count,
                        exc,
                    )

                if exc.recovery_prompt:
                    retry_request = await self._create_retry_request(
                        request, exc.recovery_prompt
                    )
                else:
                    retry_request = request

                try:
                    retry_response = (
                        await self._backend_processor.process_backend_request(
                            request=retry_request,
                            session_id=processing_context.session_id,
                            context=context,
                        )
                    )
                except SessionCancelledError:
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Retry backend request cancelled for session %s",
                            processing_context.session_id,
                        )
                    return

                if isinstance(retry_response, StreamingResponseEnvelope):
                    if _should_abort_for_cancellation():
                        return
                    # Recursively process retried stream with incremented retry_depth
                    retried = await self.handle(
                        stream=retry_response,
                        request=retry_request,
                        context=context,
                        processing_context=processing_context,
                        retry_depth=retry_depth + 1,
                    )
                    if retried.content is not None:
                        retry_stream = retried.content
                        if self._backend_work_guard is not None:
                            retry_stream = (
                                self._backend_work_guard.wrap_stream_with_cancellation(
                                    stream=retry_stream,
                                    session_key=session_key,
                                    purpose="empty_stream_retry",
                                )
                            )
                        if _should_abort_for_cancellation():
                            return
                        async for retry_chunk in retry_stream:
                            if _should_abort_for_cancellation():
                                return
                            yield retry_chunk
                    return

                # Non-streaming retry response (shouldn't happen, but handle gracefully)
                yield ProcessedResponse(
                    content=getattr(retry_response, "content", ""),
                    metadata=getattr(retry_response, "metadata", {}),
                )

        if synthetic_blocking or os.getenv(
            "LLM_PROXY_DISABLE_EMPTY_STREAM_RECOVERY", ""
        ).lower() in (
            "1",
            "true",
            "yes",
        ):

            async def stream_without_empty_recovery() -> (
                AsyncIterator[ProcessedResponse]
            ):
                async for chunk in attach_metadata_stream():
                    yield chunk

            content_stream = stream_without_empty_recovery()
        else:
            content_stream = stream_with_empty_recovery()

        prefetched_chunk: ProcessedResponse | None = None
        effective_status_code = stream.status_code
        effective_metadata = stream.metadata
        try:
            prefetched_chunk = await anext(content_stream)
        except StopAsyncIteration:
            prefetched_chunk = None

        if prefetched_chunk is not None:
            terminal_error_status = self._extract_terminal_error_status(
                prefetched_chunk
            )
            if terminal_error_status is not None:
                effective_status_code = terminal_error_status
                # Preserve the upstream error shape (code/type/message/param) on
                # the envelope metadata so the request processor can surface a
                # canonical, agent-recognizable error (e.g. OpenAI
                # ``context_length_exceeded``) instead of a generic
                # ``Backend returned N error`` body that breaks agent loops.
                terminal_error_payload = (
                    BackendStreamingResponseHandler._extract_terminal_error_payload(
                        prefetched_chunk
                    )
                )
                if isinstance(terminal_error_payload, dict):
                    base_metadata = dict(stream.metadata or {})
                    original_error_details = base_metadata.get("error_details")
                    if isinstance(original_error_details, dict):
                        merged_error_details = dict(original_error_details)
                        merged_error_details.update(terminal_error_payload)
                    else:
                        merged_error_details = dict(terminal_error_payload)
                    base_metadata["error_message"] = terminal_error_payload.get(
                        "message"
                    )
                    base_metadata["error_type"] = terminal_error_payload.get("type")
                    base_metadata["error_code"] = terminal_error_payload.get("code")
                    base_metadata["error_param"] = terminal_error_payload.get("param")
                    base_metadata["error_details"] = merged_error_details
                    effective_metadata = base_metadata

        async def _with_prefetched_chunk() -> AsyncIterator[ProcessedResponse]:
            if prefetched_chunk is not None:
                yield prefetched_chunk
            async for chunk in content_stream:
                yield chunk

        return StreamingResponseEnvelope(
            content=_with_prefetched_chunk(),
            media_type=stream.media_type,
            headers=stream.headers,
            status_code=effective_status_code,
            cancel_callback=stream.cancel_callback,
            metadata=effective_metadata,
        )
