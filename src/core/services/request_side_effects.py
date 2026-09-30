"""
Request side effects implementation.

Handles best-effort side effects for request processing including:
- Streaming tool registry updates
- Memory context injection
- Memory capture

All operations are fail-open (log and continue on errors).
"""

from __future__ import annotations

import logging

from src.core.domain.chat import ChatRequest
from src.core.domain.request_context import RequestContext
from src.core.interfaces.request_processor_internal import IRequestSideEffects
from src.core.memory.capture_middleware import MemoryCaptureMiddleware
from src.core.memory.injection_middleware import ContextInjectionMiddleware

logger = logging.getLogger(__name__)


class RequestSideEffects(IRequestSideEffects):
    """
    Handles best-effort side effects for request processing.

    This component is responsible for applying side effects that should not
    block request processing if they fail:
    - Tool name registration in streaming context registry
    - Memory context injection
    - Memory request capture
    """

    def __init__(
        self,
        context_injector: ContextInjectionMiddleware | None = None,
        memory_capture: MemoryCaptureMiddleware | None = None,
    ) -> None:
        """
        Initialize request side effects handler.

        Args:
            context_injector: Context injection middleware (optional)
            memory_capture: Memory capture middleware (optional)
        """
        self._context_injector = context_injector
        self._memory_capture = memory_capture

    async def apply(
        self, context: RequestContext, session_id: str, request: ChatRequest
    ) -> ChatRequest:
        """
        Apply best-effort side effects and return updated request.

        Args:
            context: Request context
            session_id: Session ID
            request: Chat request

        Returns:
            Updated request (possibly modified by context injection)

        This method handles:
        - Streaming tool registry updates
        - Memory context injection
        - Memory capture

        All operations are fail-open (log and continue on errors).
        """
        # Populate allowed tools in streaming registry for dynamic tool detection
        try:
            allowed_tools: list[str] = []
            tools = getattr(request, "tools", None)
            # Native /v1/responses turns historically stashed tools only inside
            # extra_body[responses_native_projected_payload]; fall back there so
            # OpenCode tools still register when canonical.tools was omitted.
            if not tools:
                extra_body = getattr(request, "extra_body", None)
                if isinstance(extra_body, dict):
                    native = extra_body.get("responses_native_projected_payload")
                    if isinstance(native, dict):
                        native_tools = native.get("tools")
                        if isinstance(native_tools, list) and native_tools:
                            tools = native_tools
            if tools:
                for tool in tools:
                    if isinstance(tool, dict):
                        func = tool.get("function")
                        if isinstance(func, dict):
                            name = func.get("name")
                            if name:
                                allowed_tools.append(name)
                                continue
                        # Responses API flat tools: {"type":"function","name":"..."}
                        flat_name = tool.get("name")
                        if isinstance(flat_name, str) and flat_name.strip():
                            allowed_tools.append(flat_name)
                    elif hasattr(tool, "function"):
                        # Pydantic model (chat-style nested function)
                        func = getattr(tool, "function", None)
                        name = getattr(func, "name", None)
                        if name:
                            allowed_tools.append(name)
                        else:
                            flat_name = getattr(tool, "name", None)
                            if isinstance(flat_name, str) and flat_name.strip():
                                allowed_tools.append(flat_name)
                    else:
                        flat_name = getattr(tool, "name", None)
                        if isinstance(flat_name, str) and flat_name.strip():
                            allowed_tools.append(flat_name)

            from src.core.services.streaming.stream_context_registry import (
                get_global_streaming_context_registry,
            )

            registry = get_global_streaming_context_registry()
            buffer = registry.get_tool_call_buffer(session_id)
            buffer.allowed_tools = allowed_tools if allowed_tools else None
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    f"Registered allowed tools for session {session_id}: {allowed_tools}"
                )
        except (AttributeError, TypeError, KeyError, ValueError, RuntimeError) as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(f"Failed to register allowed tools: {e}", exc_info=True)

        # Inject memory context if enabled (after project detection, before capture)
        if self._context_injector:
            try:
                request = await self._context_injector.maybe_inject_context(
                    session_id, request
                )
            except (AttributeError, TypeError, ValueError, RuntimeError, KeyError) as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Context injection failed for session %s: %s",
                        session_id,
                        e,
                        exc_info=True,
                    )
            except Exception as e:
                # Fallback for any other unexpected exceptions (preserve fail-open behavior)
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Unexpected error during context injection for session %s: %s",
                        session_id,
                        e,
                        exc_info=True,
                    )

        # Capture user request interactions (before processing)
        if self._memory_capture:
            try:
                # We capture without awaiting to avoid latency impact
                # This depends on capture_request being safe to run as background task
                # or just being fast. Since it's async, we should await it or spawn task.
                # Given strict sequentiality requirements for memory (context depends on previous),
                # awaiting is safer, but capture_request just buffers.
                await self._memory_capture.capture_request(session_id, request)
            except (AttributeError, TypeError, ValueError, RuntimeError, KeyError) as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Memory capture failed for session %s: %s",
                        session_id,
                        e,
                        exc_info=True,
                    )
            except Exception as e:
                # Fallback for any other unexpected exceptions (preserve fail-open behavior)
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Unexpected error during memory capture for session %s: %s",
                        session_id,
                        e,
                        exc_info=True,
                    )

        return request
