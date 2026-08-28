"""
Anthropic Controller

Handles Anthropic API endpoints.
"""

import asyncio
import json
import logging
from collections.abc import AsyncGenerator, AsyncIterable, AsyncIterator
from typing import Any, cast

from fastapi import HTTPException, Request, Response
from pydantic import BaseModel, Field

from src.anthropic_converters import (
    _map_finish_reason,  # pyright: ignore[reportPrivateUsage]
    anthropic_to_openai_request,
    openai_stream_to_anthropic_stream,
    openai_to_anthropic_response,
)
from src.anthropic_models import AnthropicMessagesRequest
from src.core.app.constants.logging_constants import TRACE_LEVEL
from src.core.app.controllers.request_processor_resolver import (
    resolve_request_processor,
)
from src.core.app.controllers.session_resolution import resolve_session_before_capture
from src.core.common.contract_serialization import serialize_for_logging
from src.core.common.exceptions import (
    InitializationError,
    LLMProxyError,
    ServiceResolutionError,
)
from src.core.domain.request_context import RequestContext
from src.core.interfaces.di_interface import IServiceProvider
from src.core.interfaces.request_processor_interface import IRequestProcessor
from src.core.interfaces.wire_capture_interface import IWireCapture

# FastAPI Response is imported as Response in line 12
from src.core.transport.fastapi.exception_adapters import (
    map_domain_exception_to_http_exception,
)
from src.core.transport.fastapi.request_adapters import (
    fastapi_to_domain_request_context,
)
from src.core.transport.fastapi.response_adapters import domain_response_to_fastapi

logger = logging.getLogger(__name__)


class UsageFromHeaders(BaseModel):
    """Usage information extracted from response headers."""

    prompt_tokens: int = Field(default=0, description="Number of prompt tokens")
    completion_tokens: int = Field(default=0, description="Number of completion tokens")
    total_tokens: int = Field(default=0, description="Total number of tokens")


class AnthropicController:
    """Controller for Anthropic-related endpoints."""

    def __init__(
        self,
        request_processor: IRequestProcessor,
        wire_capture: IWireCapture | None = None,
    ) -> None:
        """Initialize the controller.

        Args:
            request_processor: The request processor service
        """
        self._processor = request_processor
        self._wire_capture = wire_capture

    def _extract_usage_from_headers(self, response: Any) -> UsageFromHeaders | None:
        """Extract usage information from response headers.

        FastAPI responses may have usage info in x-usage-* headers.
        """
        if not hasattr(response, "headers"):
            return None

        headers = getattr(response, "headers", {})
        if not headers:
            return None

        try:
            prompt_tokens = int(headers.get("x-usage-prompt-tokens", 0))
            completion_tokens = int(headers.get("x-usage-completion-tokens", 0))
            total_tokens = int(headers.get("x-usage-total-tokens", 0))

            if prompt_tokens or completion_tokens or total_tokens:
                return UsageFromHeaders(
                    prompt_tokens=prompt_tokens,
                    completion_tokens=completion_tokens,
                    total_tokens=total_tokens,
                )
        except (ValueError, TypeError) as e:
            # Optional metadata extraction - log for debugging but return None gracefully
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Failed to extract usage from Anthropic response headers: %s",
                    type(e).__name__,
                    exc_info=True,
                )

        return None

    async def _capture_and_return_response(
        self,
        response_data: Any,
        status_code: int,
        headers: dict[str, Any],
        ctx: RequestContext,
        anthropic_request: AnthropicMessagesRequest,
    ) -> Response:
        """Capture the outbound response and return a FastAPIResponse."""
        # Convert Pydantic models to dict for capturing and JSON serialization
        if hasattr(response_data, "model_dump"):
            response_dict = response_data.model_dump(exclude_none=True)
        else:
            response_dict = response_data
        response_bytes = json.dumps(response_dict).encode("utf-8")

        # Drop upstream content-type (e.g. SSE from canonical coordinator) so the
        # explicit JSON media_type below wins for non-streaming Anthropic payloads.
        headers = {k: v for k, v in headers.items() if str(k).lower() != "content-type"}

        if self._wire_capture and self._wire_capture.enabled():
            session_id = ctx.session_id or ""
            await self._wire_capture.capture_outbound_response(
                context=ctx,
                session_id=session_id,
                backend=None,  # Client-facing response (not backend)
                model=anthropic_request.model,
                key_name=None,
                response_content=response_bytes,
                capture_metadata={
                    "transport": "http",
                    "protocol_event": "response",
                    "http_status_code": int(status_code),
                },
            )
        from fastapi import Response as FastAPIResponse

        return FastAPIResponse(
            content=response_bytes,
            media_type="application/json",
            status_code=status_code,
            headers=headers,
        )

    async def handle_anthropic_messages(  # noqa: C901
        self, request: Request, request_data: AnthropicMessagesRequest | dict[str, Any]
    ) -> Response:
        """Handle Anthropic messages requests.

        Args:
            request: The HTTP request
            request_data: The parsed request data

        Returns:
            An HTTP response
        """
        try:
            # Convert Anthropic request to OpenAI format
            # Ensure we operate on AnthropicMessagesRequest for converters
            # Normalize request_data into a concrete AnthropicMessagesRequest
            import dataclasses

            if isinstance(request_data, AnthropicMessagesRequest):
                anthropic_request: AnthropicMessagesRequest = request_data
            else:
                # Convert various shapes (dict, pydantic, dataclass) to a dict
                payload: dict[str, Any]
                if isinstance(
                    request_data, dict
                ):  # pyright: ignore[reportUnnecessaryIsInstance]
                    payload = request_data
                elif hasattr(request_data, "model_dump"):
                    payload = request_data.model_dump()
                elif hasattr(request_data, "dict"):
                    payload = request_data.dict()
                elif dataclasses.is_dataclass(request_data):
                    payload = dataclasses.asdict(request_data)
                else:
                    # Fallback: try to coerce to dict via vars() for objects with __dict__
                    try:
                        payload = vars(request_data)  # type: ignore[arg-type]
                    except (TypeError, AttributeError) as e:
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                f"Unable to convert request_data to dict via vars(): {e}",
                                exc_info=True,
                            )
                        # Last resort: empty payload
                        payload = {}

                anthropic_request = AnthropicMessagesRequest(**(payload or {}))

            # Capture anthropic-beta header for prompt caching support
            beta_header = request.headers.get("anthropic-beta")
            if beta_header and not anthropic_request.anthropic_beta:
                anthropic_request.anthropic_beta = beta_header

            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    f"Handling Anthropic messages request: model={anthropic_request.model}, processor_type={type(self._processor).__name__}, processor_id={id(self._processor)}"
                )

            # Convert Anthropic request to canonical OpenAI request
            chat_request = anthropic_to_openai_request(anthropic_request)

            # Read raw body bytes for capture and context
            try:
                raw_body_bytes = await request.body()
            except (asyncio.TimeoutError, RuntimeError, HTTPException) as e:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        f"Failed to read request body: {e}",
                        exc_info=True,
                    )
                raw_body_bytes = b""

            # Log request body preview for debugging
            if raw_body_bytes:
                preview = raw_body_bytes[:1024]
                try:
                    rendered_preview = preview.decode("utf-8", errors="replace")
                except (UnicodeDecodeError, AttributeError) as e:
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            f"Failed to decode request body preview: {e}",
                            exc_info=True,
                        )
                    rendered_preview = repr(preview)
                if logger.isEnabledFor(TRACE_LEVEL):
                    logger.log(
                        TRACE_LEVEL,
                        "Incoming /v1/messages raw request (len=%d): %s%s",
                        len(raw_body_bytes),
                        rendered_preview,
                        "..." if len(raw_body_bytes) > len(preview) else "",
                    )

            # Convert FastAPI Request to RequestContext with typed fields populated
            ctx = fastapi_to_domain_request_context(
                request,
                attach_original=True,
                domain_request=chat_request,
                raw_body=raw_body_bytes if raw_body_bytes else None,
            )

            # Set protocol identifier for normalization (Requirement 1.11)
            if ctx.extensions is None:  # pyright: ignore[reportUnnecessaryComparison]
                ctx.extensions = {}
            ctx.extensions["protocol"] = "anthropic"

            # Ensure session_id is available in context if provided in request
            if hasattr(chat_request, "session_id") and chat_request.session_id:
                ctx.session_id = chat_request.session_id

            provider = cast(
                IServiceProvider | None,
                getattr(getattr(request.app, "state", None), "service_provider", None),
            )
            await resolve_session_before_capture(
                service_provider=provider,
                context=ctx,
            )

            if self._wire_capture and self._wire_capture.enabled():
                # Wire capture is optional - log unexpected errors but don't fail request
                try:
                    await self._wire_capture.capture_inbound_request(
                        context=ctx,
                        session_id=getattr(ctx, "session_id", None),
                        request_payload=chat_request,
                        raw_body=raw_body_bytes,
                        capture_metadata={
                            "transport": "http",
                            "protocol_event": "request",
                            "http_method": request.method,
                            "url": str(request.url),
                        },
                    )
                except (AttributeError, RuntimeError):
                    # Wire capture service not available or disabled (benign)
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Wire capture inbound request skipped (service unavailable or disabled)",
                            exc_info=True,
                        )
                except Exception as e:
                    # Unexpected error - log for debugging
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            "Unexpected error capturing inbound request, continuing without wire capture: %s",
                            e,
                            exc_info=True,
                        )

            # Process the request using the request processor
            response = await self._processor.process_request(ctx, chat_request)

            # Check if response is a coroutine and await it if needed
            if asyncio.iscoroutine(response):
                response = await response

            # Convert domain response to FastAPI response
            adapted_response: Response = domain_response_to_fastapi(
                response, wire_capture=self._wire_capture, context=ctx
            )

            # Convert the OpenAI response back to Anthropic format
            # Check if the response is a streaming response
            from fastapi.responses import StreamingResponse

            if isinstance(adapted_response, StreamingResponse):
                # For streaming responses, we'll handle them separately
                openai_response_data: dict[str, Any] = {}
                anthropic_response_data: dict[str, Any] = {}
            else:
                # For regular responses, extract the body content
                body_content: bytes | memoryview = adapted_response.body
                if isinstance(body_content, memoryview):
                    body_content = body_content.tobytes()

                # Try to parse as JSON, but handle plain strings gracefully
                decoded_content = ""
                try:
                    decoded_content = body_content.decode()
                    openai_response_data = json.loads(decoded_content)

                    # If JSON parsing returned a string (e.g., response was just quoted text),
                    # convert it to a proper OpenAI-style response structure
                    if isinstance(openai_response_data, str):
                        usage = self._extract_usage_from_headers(adapted_response)
                        openai_response_data = {
                            "choices": [
                                {
                                    "message": {"content": openai_response_data},
                                    "finish_reason": "stop",
                                }
                            ],
                            # Extract usage from response headers if available
                            "usage": usage.model_dump() if usage else None,
                        }
                except json.JSONDecodeError:
                    # If it's not valid JSON, treat it as a plain text response
                    usage = self._extract_usage_from_headers(adapted_response)
                    openai_response_data = {
                        "choices": [
                            {
                                "message": {"content": decoded_content},
                                "finish_reason": "stop",
                            }
                        ],
                        # Extract usage from response headers if available
                        "usage": usage.model_dump() if usage else None,
                    }

                # Preferred path: if we still have access to the domain ChatResponse,
                # format Anthropic directly from it to preserve content reliably.
                try:
                    from src.core.domain.chat import ChatResponse as _ChatResponse

                    if hasattr(response, "content") and isinstance(
                        response.content, _ChatResponse
                    ):
                        cr: _ChatResponse = response.content
                        first = cr.choices[0] if cr.choices else None
                        text = first.message.content if first and first.message else ""
                        reasoning_text = (
                            first.message.reasoning_content
                            if first and first.message
                            else None
                        )
                        usage_summary = cr.usage
                        stop_reason = (
                            _map_finish_reason(first.finish_reason)
                            if first and first.finish_reason is not None
                            else None
                        )
                        content_blocks: list[dict[str, Any]] = []
                        if reasoning_text:
                            content_blocks.append(
                                {
                                    "type": "thinking",
                                    "thinking": reasoning_text,
                                }
                            )
                        content_blocks.append(
                            {"type": "text", "text": text or ""}  # type: ignore[arg-type]
                        )

                        anthropic_response_data = {
                            "id": cr.id,
                            "type": "message",
                            "role": "assistant",
                            "content": content_blocks,
                            "model": cr.model,
                            "stop_reason": stop_reason,
                            "stop_sequence": None,
                            "usage": {
                                "input_tokens": (
                                    usage_summary.prompt_tokens or 0
                                    if usage_summary
                                    else 0
                                ),
                                "output_tokens": (
                                    usage_summary.completion_tokens or 0
                                    if usage_summary
                                    else 0
                                ),
                            },
                        }
                    else:
                        # Fallback: convert from OpenAI-shaped dict defensively
                        if "choices" in openai_response_data and (
                            isinstance(openai_response_data.get("choices"), list)
                        ):
                            anthropic_resp_obj = openai_to_anthropic_response(
                                openai_response_data
                            )
                            if isinstance(anthropic_resp_obj, dict):
                                anthropic_response_data = anthropic_resp_obj
                            elif hasattr(anthropic_resp_obj, "model_dump"):
                                anthropic_response_data = anthropic_resp_obj.model_dump(
                                    exclude_unset=True
                                )
                            else:
                                anthropic_response_data = cast(
                                    dict[str, Any], anthropic_resp_obj
                                )

                        else:
                            # Ensure openai_response_data is a dictionary before using dict()
                            if isinstance(
                                openai_response_data, dict
                            ):  # pyright: ignore[reportUnnecessaryIsInstance]
                                anthropic_response_data = openai_response_data
                            else:
                                # Convert to a safe fallback structure
                                anthropic_response_data = {
                                    "choices": [
                                        {
                                            "message": {
                                                "content": str(openai_response_data)
                                            },
                                            "finish_reason": "stop",
                                        }
                                    ]
                                }
                except Exception as e:
                    # On any error, log it and try to preserve original response
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            f"Error building Anthropic response from domain: {e}",
                            exc_info=True,
                        )
                    # If openai_response_data is valid, use it as-is (will be converted later)
                    if (
                        isinstance(
                            openai_response_data, dict
                        )  # pyright: ignore[reportUnnecessaryIsInstance]
                        and openai_response_data
                    ):
                        anthropic_response_data = openai_response_data
                    else:
                        # Create a safe fallback structure only as last resort
                        anthropic_response_data = {
                            "choices": [
                                {
                                    "message": {"content": str(openai_response_data)},
                                    "finish_reason": "stop",
                                }
                            ]
                        }

            # Check if streaming was requested
            is_streaming = anthropic_request.stream
            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    f"Streaming requested: {is_streaming}, adapted_response type: {type(adapted_response)}"
                )

            # Return as FastAPI Response with appropriate format
            from fastapi.responses import StreamingResponse

            if is_streaming:
                # For streaming, we need to return the adapted response directly
                # since domain_response_to_fastapi should handle streaming properly
                if logger.isEnabledFor(logging.INFO):
                    # Log response summary without exposing full content (NFR4.2)
                    response_summary = (
                        serialize_for_logging(adapted_response, redact=True)
                        if isinstance(adapted_response, dict | list)
                        or hasattr(adapted_response, "model_dump")
                        else str(type(adapted_response).__name__)
                    )
                    logger.info(f"Returning streaming response: {response_summary}")
                if isinstance(adapted_response, StreamingResponse):
                    # Ensure Anthropic streaming endpoints advertise proper SSE headers
                    sse_content_type = "text/event-stream; charset=utf-8"

                    # The body_iterator from the StreamingResponse is what we need to convert
                    source_iterator = adapted_response.body_iterator

                    async def _byte_wrapper(
                        iterator: AsyncIterable[str | bytes | memoryview],
                    ) -> AsyncGenerator[bytes, None]:
                        async for chunk in iterator:
                            if isinstance(chunk, memoryview):
                                yield chunk.tobytes()
                            elif isinstance(chunk, str):
                                yield chunk.encode("utf-8")
                            else:
                                yield chunk

                    openai_stream_bytes = _byte_wrapper(source_iterator)

                    # This session ID may be None, but the converter has a fallback
                    session_id = ctx.session_id or ""

                    # The new stateful async generator that will handle the conversion
                    anthropic_stream_str = openai_stream_to_anthropic_stream(
                        openai_stream_bytes,
                        anthropic_request,
                        anthropic_request.model,
                        session_id,
                    )

                    async def _anthropic_stream() -> AsyncIterator[bytes]:
                        """Yields bytes from the converted Anthropic-formatted stream."""
                        async for chunk_str in anthropic_stream_str:
                            yield chunk_str.encode("utf-8")

                    # The final stream to be sent to the client
                    final_stream = _anthropic_stream()

                    # Wrap the final stream with wire capture if enabled
                    if self._wire_capture and self._wire_capture.enabled():
                        # Wire capture is optional - log unexpected errors but don't fail request
                        try:
                            final_stream = self._wire_capture.wrap_outbound_stream(
                                context=ctx,
                                session_id=session_id,
                                backend=None,  # Client-facing stream (not backend)
                                model=anthropic_request.model,
                                key_name=None,
                                stream=final_stream,
                            )
                        except (AttributeError, RuntimeError):
                            # Wire capture service not available or disabled
                            if logger.isEnabledFor(logging.DEBUG):
                                logger.debug(
                                    "Wire capture outbound stream wrapping skipped (service unavailable or disabled)",
                                    exc_info=True,
                                )
                        except Exception as e:
                            # Unexpected error - log for debugging
                            if logger.isEnabledFor(logging.WARNING):
                                logger.warning(
                                    "Unexpected error wrapping outbound stream with wire capture, continuing without wire capture: %s",
                                    e,
                                    exc_info=True,
                                )

                    headers = dict(adapted_response.headers)
                    headers["content-type"] = sse_content_type
                    headers.setdefault("cache-control", "no-cache")
                    headers.setdefault("connection", "keep-alive")

                    return StreamingResponse(
                        final_stream,
                        media_type=sse_content_type,
                        status_code=getattr(adapted_response, "status_code", 200),
                        headers=headers,
                        background=adapted_response.background,
                    )
                else:
                    # If somehow we got a non-streaming response but streaming was requested,
                    # convert it to a simple streaming response
                    async def simple_stream() -> AsyncIterator[bytes]:
                        if hasattr(adapted_response, "body"):
                            body_content = adapted_response.body
                            if isinstance(body_content, memoryview):
                                yield body_content.tobytes()
                            elif isinstance(
                                body_content, bytes
                            ):  # pyright: ignore[reportUnnecessaryIsInstance]
                                yield body_content
                            else:
                                yield str(body_content).encode("utf-8")
                        else:
                            yield b'data: {"error": "Unable to stream response"}\n\n'

                    return StreamingResponse(
                        simple_stream(),  # type: ignore[arg-type]
                        media_type="text/event-stream",
                        headers=getattr(adapted_response, "headers", {}),
                    )
            else:
                # For non-streaming, return Anthropic-formatted JSON response
                if logger.isEnabledFor(logging.INFO):
                    # Log response with redaction to prevent secret leakage (NFR4.2)
                    if isinstance(
                        anthropic_response_data, dict
                    ):  # pyright: ignore[reportUnnecessaryIsInstance]
                        response_summary = serialize_for_logging(
                            anthropic_response_data, redact=True
                        )
                    else:
                        response_summary = serialize_for_logging(
                            anthropic_response_data, redact=True
                        )
                    logger.info(f"Returning JSON response: {response_summary}")

                status_code = getattr(adapted_response, "status_code", 200)

                # If we're using the OpenAI format (choices), convert it to Anthropic format
                if (
                    isinstance(
                        anthropic_response_data, dict
                    )  # pyright: ignore[reportUnnecessaryIsInstance]
                    and "choices" in anthropic_response_data
                ):
                    # Convert OpenAI format to Anthropic format using shared converter
                    anthropic_formatted = openai_to_anthropic_response(
                        anthropic_response_data
                    )
                    # Sanitize headers to remove compression hints that can confuse clients
                    raw_headers = getattr(adapted_response, "headers", {})
                    safe_headers = {
                        k: v
                        for k, v in raw_headers.items()
                        if k.lower()
                        not in (
                            "content-encoding",
                            "transfer-encoding",
                            "content-length",
                        )
                    }
                    return await self._capture_and_return_response(
                        anthropic_formatted,
                        status_code,
                        safe_headers,
                        ctx,
                        anthropic_request,
                    )
                else:
                    # Already in Anthropic format or custom format
                    raw_headers = getattr(adapted_response, "headers", {})
                    safe_headers = {
                        k: v
                        for k, v in raw_headers.items()
                        if k.lower()
                        not in (
                            "content-encoding",
                            "transfer-encoding",
                            "content-length",
                        )
                    }
                    return await self._capture_and_return_response(
                        anthropic_response_data,
                        status_code,
                        safe_headers,
                        ctx,
                        anthropic_request,
                    )
        except LLMProxyError as e:
            # Map domain exceptions to HTTP exceptions
            raise map_domain_exception_to_http_exception(e, request=request)
        except HTTPException as e:
            # Re-raise HTTP exceptions
            raise e
        except Exception as e:
            # Log and convert other exceptions to HTTP exceptions
            if logger.isEnabledFor(logging.ERROR):
                logger.error(f"Error handling Anthropic messages: {e}", exc_info=True)
            raise HTTPException(
                status_code=500,
                detail={"error": {"message": str(e), "type": "server_error"}},
            ) from e


def get_anthropic_controller(service_provider: IServiceProvider) -> AnthropicController:
    """Create an Anthropic controller using the service provider.

    Args:
        service_provider: The service provider to use

    Returns:
        A configured Anthropic controller

    Raises:
        Exception: If the request processor could not be found or created
    """
    try:
        # Try to get the existing request processor from the service provider

        try:
            request_processor: IRequestProcessor = resolve_request_processor(
                service_provider
            )
        except InitializationError as exc:
            raise InitializationError(
                f"Failed to create AnthropicController: {exc}"
            ) from exc

        wire_capture = None
        from typing import cast

        # Wire capture is optional - get it if available
        try:
            wire_capture = service_provider.get_service(cast(type, IWireCapture))
        except (ServiceResolutionError, AttributeError):
            # Service not registered or provider doesn't have get_service method
            # This is expected when wire capture is disabled
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Wire capture service not available in DI container (service not registered or disabled)",
                    exc_info=True,
                )
        except Exception as e:
            # Unexpected error during service resolution - log for debugging
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Unexpected error getting wire capture service, continuing without wire capture: %s",
                    e,
                    exc_info=True,
                )

        return AnthropicController(request_processor, wire_capture=wire_capture)
    except Exception as e:
        raise InitializationError(f"Failed to create AnthropicController: {e}") from e
