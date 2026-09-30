"""Responses Controller handling OpenAI Responses API endpoints."""

import asyncio
import contextlib
import dataclasses
import json
import logging
import re
import sre_parse
import time
from collections.abc import AsyncIterator, Awaitable
from datetime import datetime, timezone
from sre_constants import MAXREPEAT
from typing import Any, cast

from fastapi import HTTPException, Request, Response, WebSocket, WebSocketDisconnect
from fastapi.responses import StreamingResponse
from pydantic import ValidationError

from src.core.app.controllers.responses_stream_coercion import (
    coerce_stream_chunk_payload,
)
from src.core.common.exceptions import (
    InitializationError,
    LLMProxyError,
    ParsingError,
    ResponsesPreviousResponseNotFoundError,
    ResponsesProtocolError,
    ResponsesProviderLimitationError,
    ResponsesValidationError,
    RoutingError,
    TranslationError,
)
from src.core.domain.chat import CanonicalChatRequest, ChatMessage
from src.core.domain.client_termination import (
    ClientEndOfSessionSignal,
    ClientTerminationReason,
)
from src.core.domain.request_context import RequestContext
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.domain.responses_api import (
    ResponsesRequest,
    enforce_json_schema_limits,
)
from src.core.domain.responses_domain import (
    ResponsesDomainRequest,
    ResponsesInputItem,
    ResponsesOutputItem,
)
from src.core.domain.responses_event_normalizer import (
    ResponsesEventNormalizer,
    ResponsesStreamAbortedError,
    ResponsesStreamSource,
)
from src.core.domain.responses_native_wiring import (
    ACP_RESPONSES_STANDALONE_MODE_KEY,
    RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY,
)
from src.core.domain.responses_request_normalizer import ResponsesRequestNormalizer
from src.core.domain.responses_resolved_session import (
    ResponsesHistoryItem,
    effective_instructions_for_chained_turn,
)
from src.core.domain.responses_wire_renderer import ResponsesWireRenderer
from src.core.domain.translators.responses.wire_stream_emitter import (
    ResponsesWireStreamEmitter,
)
from src.core.interfaces.backend_config_provider_interface import (
    IBackendConfigProvider,
)
from src.core.interfaces.backend_model_resolver_interface import IBackendModelResolver
from src.core.interfaces.client_end_of_session_service_interface import (
    IClientEndOfSessionService,
)
from src.core.interfaces.di_interface import IServiceProvider
from src.core.interfaces.request_processor_interface import IRequestProcessor
from src.core.interfaces.responses_projector import IResponsesBackendProjector
from src.core.interfaces.responses_session_store_interface import IResponsesSessionStore
from src.core.interfaces.session_metrics_initializer_interface import (
    ISessionMetricsInitializer,
)
from src.core.interfaces.translation_service_interface import (
    ITranslationService,
)
from src.core.interfaces.wire_capture_interface import IWireCapture
from src.core.services.acp_responses_projector import project_responses_to_acp_chat
from src.core.services.anthropic_responses_projector import AnthropicResponsesProjector
from src.core.services.gemini_responses_projector import GeminiResponsesProjector
from src.core.services.json_repair_service import enforce_schema_size_limits
from src.core.services.openai_responses_projector import OpenAIResponsesProjector
from src.core.transport.fastapi.exception_adapters import (
    map_domain_exception_to_http_exception,
)
from src.core.transport.fastapi.request_adapters import (
    fastapi_to_domain_request_context,
)
from src.core.transport.fastapi.response_adapters import domain_response_to_fastapi
from src.core.transport.session_key_resolver import (
    resolve_session_key_from_request_context,
)

logger = logging.getLogger(__name__)


def _http_detail_with_request_id(
    detail: dict[str, Any], *, request_id: str
) -> dict[str, Any]:
    out = dict(detail)
    if request_id and "request_id" not in out:
        out["request_id"] = request_id
    return out


def _never_emit_stream_chunks() -> bool:
    """Always false; keeps empty-stream generators vulture-clean vs `if False`."""

    return False


async def _empty_responses_chunk_iterator() -> AsyncIterator[Any]:
    """Upstream chunk stream that emits no items (typed empty async iterator)."""

    if _never_emit_stream_chunks():
        yield b""  # pragma: no cover


def _coerce_tool_output_text(content: Any) -> str:
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    try:
        return json.dumps(content, ensure_ascii=False)
    except (TypeError, ValueError):
        return str(content)


def _chat_messages_to_responses_input(messages: list[Any]) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    for msg in messages:
        if isinstance(msg, dict):
            role = str(msg.get("role") or "user")
            content = msg.get("content")
            tool_calls = msg.get("tool_calls")
            tool_call_id = msg.get("tool_call_id")
        else:
            role = str(getattr(msg, "role", None) or "user")
            content = getattr(msg, "content", None)
            tool_calls = getattr(msg, "tool_calls", None)
            tool_call_id = getattr(msg, "tool_call_id", None)

        if tool_call_id is not None or role == "tool":
            tid = str(tool_call_id).strip() if tool_call_id is not None else ""
            if tid:
                out.append(
                    {
                        "type": "function_call_output",
                        "call_id": tid,
                        "output": _coerce_tool_output_text(content),
                    }
                )
                continue
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Chat-style messages conversion: tool message without usable "
                    "tool_call_id; skipping tool output item"
                )
            continue

        if isinstance(tool_calls, list) and tool_calls:
            text_parts: list[dict[str, Any]] = []
            if isinstance(content, str) and content:
                text_parts = [{"type": "input_text", "text": content}]
            elif isinstance(content, list):
                text_parts = [
                    (
                        dict(p)
                        if isinstance(p, dict)
                        else {"type": "input_text", "text": str(p)}
                    )
                    for p in content
                ]
            elif content is None:
                pass
            else:
                text_parts = [{"type": "input_text", "text": str(content)}]
            if text_parts:
                out.append({"type": "message", "role": role, "content": text_parts})

            for idx, tc in enumerate(tool_calls):
                if not isinstance(tc, dict):
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            "Chat-style messages conversion: skipping invalid tool_calls[%s] entry",
                            idx,
                        )
                    continue
                call_id = tc.get("id")
                call_id_s = str(call_id) if call_id is not None else f"call_{idx}"
                fn_raw = tc.get("function")
                fn_d: dict[str, Any] = fn_raw if isinstance(fn_raw, dict) else {}
                name_val = fn_d.get("name")
                name_s = str(name_val) if isinstance(name_val, str) else ""
                args_val = fn_d.get("arguments")
                if isinstance(args_val, str):
                    args_s = args_val
                elif args_val is None:
                    args_s = ""
                else:
                    try:
                        args_s = json.dumps(args_val, ensure_ascii=False)
                    except (TypeError, ValueError):
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Chat-style messages conversion: tool_calls[%s] arguments "
                                "could not be serialized to JSON; coercing with str()",
                                idx,
                            )
                        args_s = str(args_val)
                out.append(
                    {
                        "type": "function_call",
                        "call_id": call_id_s,
                        "name": name_s,
                        "arguments": args_s,
                    }
                )
            continue

        if isinstance(content, str):
            parts: list[dict[str, Any]] = [{"type": "input_text", "text": content}]
        elif isinstance(content, list):
            parts = [
                (
                    dict(p)
                    if isinstance(p, dict)
                    else {"type": "input_text", "text": str(p)}
                )
                for p in content
            ]
        elif content is None:
            parts = [{"type": "input_text", "text": ""}]
        else:
            parts = [{"type": "input_text", "text": str(content)}]
        out.append({"type": "message", "role": role, "content": parts})
    return out


def _responses_request_dump_for_normalize(req: ResponsesRequest) -> dict[str, Any]:
    raw = req.model_dump(mode="json", exclude_none=False)
    messages = raw.pop("messages", None)
    if messages is not None and raw.get("input") is None:
        raw["input"] = _chat_messages_to_responses_input(messages)
    return raw


class ResponsesController:
    """Controller for Responses API endpoints."""

    _MAX_REGEX_PATTERN_LENGTH = 512

    def __init__(
        self,
        request_processor: IRequestProcessor,
        translation_service: ITranslationService | None = None,
        wire_capture: IWireCapture | None = None,
        client_eos_service: IClientEndOfSessionService | None = None,
        metrics_initializer: ISessionMetricsInitializer | None = None,
        *,
        responses_session_store: IResponsesSessionStore | None = None,
        backend_model_resolver: IBackendModelResolver | None = None,
        openai_responses_projector: OpenAIResponsesProjector | None = None,
        anthropic_responses_projector: AnthropicResponsesProjector | None = None,
        gemini_responses_projector: GeminiResponsesProjector | None = None,
        backend_config_provider: IBackendConfigProvider | None = None,
    ) -> None:
        """Initialize the controller.

        Args:
            request_processor: The request processor service
            translation_service: Translation service for request conversion
            wire_capture: Optional wire capture service
            client_eos_service: Optional client end-of-session service for termination reporting
            metrics_initializer: Optional session metrics initializer for proactive metrics creation
        """
        self._processor = request_processor
        if translation_service is None:
            raise InitializationError(
                "Translation service must be provided by DI container"
            )

        self._translation_service = translation_service
        self._wire_capture = wire_capture
        self._client_eos_service = client_eos_service
        self._metrics_initializer = metrics_initializer
        if responses_session_store is None:
            raise InitializationError(
                "Responses session store must be provided by DI container"
            )
        if backend_model_resolver is None:
            raise InitializationError(
                "Backend model resolver must be provided by DI container"
            )
        if (
            openai_responses_projector is None
            or anthropic_responses_projector is None
            or gemini_responses_projector is None
        ):
            raise InitializationError(
                "Responses backend projectors must be provided by DI container"
            )
        self._responses_session_store = responses_session_store
        self._backend_model_resolver = backend_model_resolver
        self._openai_responses_projector = openai_responses_projector
        self._anthropic_responses_projector = anthropic_responses_projector
        self._gemini_responses_projector = gemini_responses_projector
        self._backend_config_provider = backend_config_provider
        self._responses_request_normalizer = ResponsesRequestNormalizer()

    async def _store_completed_responses_payload(
        self,
        payload: dict[str, Any],
        *,
        instructions: str | None,
        history_items: list[ResponsesHistoryItem] | None = None,
    ) -> None:
        rid = payload.get("id")
        if not isinstance(rid, str) or not rid:
            return
        items = self._responses_output_items_from_payload(payload, response_id=rid)
        stored_history = (
            cast(list[ResponsesHistoryItem], [*history_items, *items])
            if isinstance(history_items, list)
            else cast(list[ResponsesHistoryItem], list(items))
        )
        await self._responses_session_store.store(
            rid,
            items,
            instructions=instructions,
            history_items=stored_history,
        )

    async def _responses_history_for_turn(
        self,
        domain_request: ResponsesDomainRequest,
        output_items: list[ResponsesOutputItem],
    ) -> list[ResponsesHistoryItem]:
        """Build the complete visible transcript for a stored response.

        Cursor ACP runtimes are deliberately disposable.  Keeping the complete
        text transcript in the Responses session store lets a later
        ``previous_response_id`` turn start a fresh runtime without losing the
        original user prompt or earlier assistant turns.
        """

        history: list[ResponsesHistoryItem] = []
        previous_id = domain_request.previous_response_id
        if previous_id:
            resolved = await self._responses_session_store.resolve(previous_id)
            if resolved is not None:
                history.extend(resolved.history_items or resolved.output_items)
        raw_input: object = getattr(domain_request, "input", [])
        if isinstance(raw_input, str):
            history.append(
                ResponsesInputItem(type="message", role="user", content=raw_input)
            )
        elif isinstance(raw_input, list):
            for item in raw_input:
                if isinstance(item, ResponsesInputItem | ResponsesOutputItem):
                    history.append(item)
                else:
                    history.append(ResponsesInputItem.model_validate(item))
        history.extend(output_items)
        return history

    def _responses_output_items_from_payload(
        self, payload: dict[str, Any], *, response_id: str
    ) -> list[ResponsesOutputItem]:
        out_raw = payload.get("output")
        if isinstance(out_raw, list):
            return self._validate_responses_output_items(
                out_raw, response_id=response_id
            )

        choices_raw = payload.get("choices")
        if not isinstance(choices_raw, list):
            return []

        items: list[dict[str, Any]] = []
        for idx, choice in enumerate(choices_raw):
            if not isinstance(choice, dict):
                continue
            message = choice.get("message")
            if not isinstance(message, dict):
                continue

            content_parts: list[dict[str, Any]] = []
            content = message.get("content")
            if isinstance(content, str) and content:
                content_parts.append({"type": "output_text", "text": content})

            refusal = message.get("refusal")
            if isinstance(refusal, str) and refusal:
                content_parts.append({"type": "refusal", "refusal": refusal})

            items.append(
                {
                    "id": str(message.get("id") or f"{response_id}:choice:{idx}"),
                    "type": "message",
                    "role": str(message.get("role") or "assistant"),
                    "status": "completed",
                    "content": content_parts or None,
                }
            )

            tool_calls = message.get("tool_calls")
            if not isinstance(tool_calls, list):
                continue
            for tc_idx, tool_call in enumerate(tool_calls):
                if not isinstance(tool_call, dict):
                    continue
                function = tool_call.get("function")
                if not isinstance(function, dict):
                    continue
                arguments = function.get("arguments")
                if not isinstance(arguments, str):
                    try:
                        arguments = json.dumps(
                            arguments if arguments is not None else {}
                        )
                    except (TypeError, ValueError):
                        arguments = str(arguments or "")
                items.append(
                    {
                        "id": str(
                            tool_call.get("id")
                            or f"{response_id}:choice:{idx}:tool:{tc_idx}"
                        ),
                        "type": "function_call",
                        "status": "completed",
                        "call_id": str(tool_call.get("id") or ""),
                        "name": str(function.get("name") or ""),
                        "arguments": arguments,
                    }
                )

        return self._validate_responses_output_items(items, response_id=response_id)

    def _validate_responses_output_items(
        self, items_raw: list[Any], *, response_id: str
    ) -> list[ResponsesOutputItem]:
        items: list[ResponsesOutputItem] = []
        for idx, el in enumerate(items_raw):
            if not isinstance(el, dict):
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Responses session store: skipping non-object output[%s] "
                        "for response_id=%s",
                        idx,
                        response_id,
                    )
                continue
            try:
                items.append(ResponsesOutputItem.model_validate(el))
            except ValidationError as ve:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Responses session store: skipping invalid output[%s] "
                        "for response_id=%s: %s",
                        idx,
                        response_id,
                        ve,
                    )
                continue
        return items

    async def _prepare_responses_execution(
        self,
        *,
        responses_request: ResponsesRequest,
    ) -> tuple[
        ResponsesDomainRequest, CanonicalChatRequest, ResponsesStreamSource, str | None
    ]:
        normalized_raw = _responses_request_dump_for_normalize(responses_request)
        domain = self._responses_request_normalizer.normalize(normalized_raw)
        prior_items: list[ResponsesOutputItem] | None = None
        prior_history: list[ResponsesHistoryItem] | None = None
        prior_instructions: str | None = None
        if domain.previous_response_id:
            resolved = await self._responses_session_store.resolve(
                domain.previous_response_id
            )
            if resolved is None:
                raise ResponsesPreviousResponseNotFoundError(
                    domain.previous_response_id
                )
            prior_items = list(resolved.output_items)
            prior_history = list(resolved.history_items or resolved.output_items)
            prior_instructions = resolved.instructions
        instructions = effective_instructions_for_chained_turn(
            domain.instructions,
            prior_instructions,
        )
        domain = domain.model_copy(update={"instructions": instructions})
        route_model = str(domain.model).strip()
        route_probe = CanonicalChatRequest(
            model=route_model,
            messages=[ChatMessage(role="user", content=".")],
            stream=bool(domain.stream),
        )
        try:
            target = await self._backend_model_resolver.resolve_target(
                route_probe, None
            )
        except RoutingError as exc:
            details = exc.details or {}
            if (
                details.get("code") == "unknown_model"
                and ":" not in route_model
                and self._backend_config_provider is not None
            ):
                default_backend = (
                    self._backend_config_provider.get_default_backend() or ""
                ).strip()
                if default_backend:
                    prefixed = f"{default_backend}:{route_model}"
                    route_probe = CanonicalChatRequest(
                        model=prefixed,
                        messages=[ChatMessage(role="user", content=".")],
                        stream=bool(domain.stream),
                    )
                    target = await self._backend_model_resolver.resolve_target(
                        route_probe, None
                    )
                else:
                    raise
            else:
                raise
        backend = (target.backend or "").strip()
        backend_key = backend.casefold()
        backend_family = backend_key.split(".", 1)[0]
        explicit_model = f"{target.backend}:{target.model}"
        if backend_family == "cursor-cli-acp":
            # Cursor Responses turns are deliberately process-isolated.  The
            # generated key is an internal pool identity only; it is never
            # persisted as a Responses conversation/session identity.  Chained
            # turns replay the complete visible transcript from the session
            # store, so they do not depend on the previous ACP subprocess.
            import uuid

            domain = domain.model_copy(
                update={"session_id": f"acp-responses-{uuid.uuid4().hex}"}
            )
            canonical = project_responses_to_acp_chat(
                domain,
                prior_items,
                explicit_model=explicit_model,
                prior_history=prior_history,
            )
            canonical_extra_body = dict(canonical.extra_body or {})
            canonical_extra_body[ACP_RESPONSES_STANDALONE_MODE_KEY] = True
            canonical = canonical.model_copy(
                update={"extra_body": canonical_extra_body}
            )
            return (
                domain,
                canonical,
                ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS,
                instructions,
            )
        projector: IResponsesBackendProjector
        if backend_key in ("openai", "openai-responses") or backend_key.startswith(
            ("openai-codex", "openai-chatgpt-plan", "opencode")
        ):
            projector = self._openai_responses_projector
            stream_source = ResponsesStreamSource.OPENAI_RESPONSES
        elif backend_key == "anthropic":
            projector = self._anthropic_responses_projector
            stream_source = ResponsesStreamSource.ANTHROPIC
        elif backend_key.startswith("gemini") or backend_key == "google":
            projector = self._gemini_responses_projector
            stream_source = ResponsesStreamSource.GEMINI
        elif backend_key == "mock":
            # Test harness / in-process mock backends reuse the OpenAI-native wire path.
            projector = self._openai_responses_projector
            stream_source = ResponsesStreamSource.OPENAI_RESPONSES
        else:
            raise ResponsesProviderLimitationError(
                "responses_api.routing",
                backend or "unknown",
            )
        wire_payload, _flags = projector.project(domain, prior_items)
        canonical = CanonicalChatRequest(
            model=explicit_model,
            messages=[ChatMessage(role="user", content=".")],
            stream=bool(domain.stream),
            extra_body={RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY: wire_payload},
        )
        return domain, canonical, stream_source, instructions

    def _ensure_responses_schema_content(
        self,
        content: object,
        *,
        request_id: str,
        envelope: object,
        responses_model: str,
    ) -> object:

        try:
            from src.core.domain.chat import ChatResponse
            from src.core.domain.responses import ResponseEnvelope
            from src.core.interfaces.response_processor_interface import (
                ProcessedResponse,
            )

            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    f"Converting response to Responses API format - request_id={request_id}, content_type={type(content).__name__}"
                )

            # CRITICAL: If content is a string, try to parse it as JSON and check if it's already a Responses API response
            if isinstance(content, str) and content.strip():
                try:
                    parsed_content = json.loads(content)
                    if (
                        isinstance(parsed_content, dict)
                        and "response" in parsed_content
                    ):
                        # Content is a JSON string containing a Responses API response
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                f"Restored Responses API format from JSON string content - request_id={request_id}"
                            )
                        return parsed_content
                except (json.JSONDecodeError, ValueError):
                    # Not a JSON string, proceed with other checks
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Content is not a JSON string (cannot parse as JSON)",
                            exc_info=True,
                        )

            # Check if response has metadata with original Responses API response
            response_metadata = None
            if isinstance(envelope, ResponseEnvelope | ProcessedResponse):
                response_metadata = getattr(envelope, "metadata", None)
                if (
                    isinstance(response_metadata, dict)
                    and "original_responses_api_response" in response_metadata
                ):
                    original_response: dict[str, Any] = response_metadata["original_responses_api_response"]  # type: ignore[assignment]
                    if (
                        isinstance(original_response, dict)
                        and "response" in original_response
                    ):
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                f"Restored Responses API format from ResponseProcessor metadata - request_id={request_id}"
                            )
                        return original_response

            # If content is already in Responses API format (has 'response' key), return as-is
            if isinstance(content, dict) and "response" in content:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        f"Response already in Responses API format - request_id={request_id}"
                    )
                return content

            if (
                isinstance(content, dict)
                and content.get("object") == "response"
                and isinstance(content.get("output"), list)
            ):
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Response already native object=response with output - request_id=%s",
                        request_id,
                    )
                return content

            # Check if content has metadata with original Responses API response
            if isinstance(content, dict) and "metadata" in content:
                metadata: dict[str, Any] = content.get("metadata", {})  # type: ignore[assignment]
                if (
                    isinstance(metadata, dict)
                    and "original_responses_api_response" in metadata
                ):
                    original_response_from_content: dict[str, Any] = metadata["original_responses_api_response"]  # type: ignore[assignment]
                    if (
                        isinstance(original_response_from_content, dict)
                        and "response" in original_response_from_content
                    ):
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                f"Restored Responses API format from metadata - request_id={request_id}"
                            )
                        return original_response_from_content

            # Check if content is a Chat Completions response that was converted from Responses API
            # (content might be a JSON string containing Responses API response)
            if (
                isinstance(content, dict)
                and "choices" in content
                and "response" not in content
            ):
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        f"Checking Chat Completions format for embedded Responses API response - request_id={request_id}, content_keys={list(content.keys())}"
                    )
                # Check if message content contains a JSON string with Responses API format
                choices = content.get("choices", [])
                if (
                    choices
                    and isinstance(choices, list)
                    and len(choices) > 0
                    and isinstance(choices[0], dict)
                ):
                    message = choices[0].get("message", {})
                    if isinstance(message, dict):
                        # First, check if the message has a 'parsed' field with Responses API format
                        message_parsed = message.get("parsed")
                        if (
                            isinstance(message_parsed, dict)
                            and "response" in message_parsed
                        ):
                            # Extract the Responses API response from the parsed field
                            responses_response = dict(message_parsed)  # Make a copy
                            if "usage" not in responses_response and "usage" in content:
                                responses_response["usage"] = content["usage"]
                            # Preserve other top-level fields from outer response if missing
                            if "id" not in responses_response and "id" in content:
                                responses_response["id"] = content["id"]
                            if (
                                "created" not in responses_response
                                and "created" in content
                            ):
                                responses_response["created"] = content["created"]
                            if "model" not in responses_response and "model" in content:
                                responses_response["model"] = content["model"]
                            if "object" not in responses_response:
                                responses_response["object"] = "response"
                            if logger.isEnabledFor(logging.INFO):
                                logger.info(
                                    f"Successfully extracted Responses API format from message.parsed - request_id={request_id}"
                                )
                            return responses_response

                        # If no parsed field, try to parse the content field as JSON
                        message_content = message.get("content", "")
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                f"Found message content - request_id={request_id}, content_type={type(message_content).__name__}, content_preview={str(message_content)[:200] if isinstance(message_content, str) else 'N/A'}"
                            )
                        if isinstance(message_content, str) and message_content.strip():
                            try:
                                # Try to parse the message content as JSON
                                parsed_content = json.loads(message_content)
                                if logger.isEnabledFor(logging.DEBUG):
                                    logger.debug(
                                        f"Parsed message content - request_id={request_id}, parsed_keys={list(parsed_content.keys()) if isinstance(parsed_content, dict) else 'N/A'}"
                                    )
                                if (
                                    isinstance(parsed_content, dict)
                                    and "response" in parsed_content
                                ):
                                    # Extract the Responses API response from the JSON string
                                    # Preserve usage from the outer response if available
                                    responses_response = dict(
                                        parsed_content
                                    )  # Make a copy
                                    if (
                                        "usage" not in responses_response
                                        and "usage" in content
                                    ):
                                        responses_response["usage"] = content["usage"]
                                    # Preserve other top-level fields from outer response if missing
                                    if (
                                        "id" not in responses_response
                                        and "id" in content
                                    ):
                                        responses_response["id"] = content["id"]
                                    if (
                                        "created" not in responses_response
                                        and "created" in content
                                    ):
                                        responses_response["created"] = content[
                                            "created"
                                        ]
                                    if (
                                        "model" not in responses_response
                                        and "model" in content
                                    ):
                                        responses_response["model"] = content["model"]
                                    if "object" not in responses_response:
                                        responses_response["object"] = "response"
                                    if logger.isEnabledFor(logging.INFO):
                                        logger.info(
                                            f"Successfully extracted Responses API format from message content - request_id={request_id}"
                                        )
                                    return responses_response
                                else:
                                    if logger.isEnabledFor(logging.DEBUG):
                                        logger.debug(
                                            f"Parsed content does not have 'response' key - request_id={request_id}, has_response={isinstance(parsed_content, dict) and 'response' in parsed_content}"
                                        )
                            except (
                                json.JSONDecodeError,
                                ValueError,
                                TypeError,
                            ) as e:
                                if logger.isEnabledFor(logging.DEBUG):
                                    logger.debug(
                                        f"Failed to parse message content as JSON - request_id={request_id}, error={e}, content_preview={message_content[:200]}"
                                    )
                        else:
                            if logger.isEnabledFor(logging.DEBUG):
                                logger.debug(
                                    f"Message content is not a non-empty string - request_id={request_id}, type={type(message_content).__name__}, is_empty={not (isinstance(message_content, str) and message_content.strip())}"
                                )

            # If it's already a ChatResponse, use TranslationService to convert
            if isinstance(content, ChatResponse):
                converted_response = self._translation_service.from_domain_response(
                    content, target_format="responses"
                )
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        f"Response converted via TranslationService - request_id={request_id}"
                    )
                return converted_response

            # If it's a dict that looks like a ChatResponse, convert it first
            if isinstance(content, dict) and "choices" in content:
                try:
                    chat_response = ChatResponse(**content)
                    converted_response = self._translation_service.from_domain_response(
                        chat_response, target_format="responses"
                    )
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            f"Response converted from dict via TranslationService - request_id={request_id}"
                        )
                    return converted_response
                except ValidationError:
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            f"Failed to convert dict to ChatResponse (validation error) - request_id={request_id}",
                            exc_info=True,
                        )
                    # If conversion fails, fall back to manual conversion
                except (
                    TranslationError,
                    ParsingError,
                    TypeError,
                    AttributeError,
                    KeyError,
                ) as e:
                    # Catch domain exceptions and common data processing errors
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            f"Failed to convert dict to ChatResponse (domain/data error: {type(e).__name__}) - request_id={request_id}",
                            exc_info=True,
                        )
                    # If conversion fails, fall back to manual conversion
                except (ValueError, UnicodeError, OverflowError, RuntimeError):
                    # Catch additional specific exceptions for defensive guard:
                    # - ValueError: Value errors in data conversion
                    # - UnicodeError: String encoding/decoding errors
                    # - OverflowError: Numeric overflow
                    # - RuntimeError: General runtime errors
                    if logger.isEnabledFor(logging.ERROR):
                        logger.error(
                            f"Unexpected error converting dict to ChatResponse - request_id={request_id}",
                            exc_info=True,
                        )
                    # If conversion fails, fall back to manual conversion

            # Fallback: manual conversion for other formats
            import time as _time
            import uuid as _uuid

            # If already in expected schema, return as-is
            # Handle Anthropic-style message dict -> Responses API
            if (
                isinstance(content, dict)
                and content.get("type") == "message"
                and isinstance(content.get("content"), list)
            ):
                # Extract text blocks
                text_parts: list[str] = []
                for block in content.get("content", []):
                    if not isinstance(block, dict):
                        continue
                    btype = block.get("type")
                    if btype == "text":
                        part_text = block.get("text") or ""
                        if part_text:
                            text_parts.append(str(part_text))

                text = "\n\n".join(text_parts).strip()
                stop_reason = content.get("stop_reason") or "stop"
                if stop_reason == "end_turn":
                    finish_reason = "stop"
                elif stop_reason == "max_tokens":
                    finish_reason = "length"
                else:
                    finish_reason = str(stop_reason)

                # Try to parse the content as JSON for structured output
                parsed = None
                try:
                    if text.strip():
                        parsed = json.loads(text)
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                f"Successfully parsed structured output - request_id={request_id}"
                            )
                except (json.JSONDecodeError, ValueError) as e:
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            f"Content is not valid JSON, leaving unparsed - request_id={request_id}, error={e}"
                        )
                    # If parsing fails, leave parsed as None

                usage = content.get("usage") or {}
                responses_usage = {
                    "prompt_tokens": usage.get("input_tokens", 0),
                    "completion_tokens": usage.get("output_tokens", 0),
                    "total_tokens": (usage.get("input_tokens", 0) or 0)
                    + (usage.get("output_tokens", 0) or 0),
                }

                return {
                    "id": content.get("id", f"resp-{_uuid.uuid4().hex[:16]}"),
                    "object": "response",
                    "created": int(_time.time()),
                    "model": content.get("model", responses_model),
                    "choices": [
                        {
                            "index": 0,
                            "message": {
                                "role": "assistant",
                                "content": text,
                                "parsed": parsed,
                            },
                            "finish_reason": finish_reason,
                        }
                    ],
                    "usage": responses_usage,
                }

            # Normalize simple string into Responses API format
            if isinstance(content, str):
                text = content
            elif isinstance(content, bytes):
                text = content.decode("utf-8", errors="ignore")
            else:
                # Best-effort stringify for non-dict/list types
                try:
                    text = json.dumps(content)
                except TypeError:
                    # Content is not JSON serializable (e.g., contains custom objects)
                    logger.debug(
                        f"Failed to JSON serialize content, using str - request_id={request_id}",
                        exc_info=True,
                    )
                    text = str(content)

            # Try to parse the content as JSON for structured output
            parsed = None
            try:
                if text.strip():
                    parsed = json.loads(text)
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            f"Successfully parsed fallback structured output - request_id={request_id}"
                        )
            except (json.JSONDecodeError, ValueError) as e:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        f"Fallback content is not valid JSON, leaving unparsed - request_id={request_id}, error={e}"
                    )
                # If parsing fails, leave parsed as None

            return {
                "id": f"resp-{_uuid.uuid4().hex[:16]}",
                "object": "response",
                "created": int(_time.time()),
                "model": responses_model,
                "choices": [
                    {
                        "index": 0,
                        "message": {
                            "role": "assistant",
                            "content": text,
                            "parsed": parsed,
                        },
                        "finish_reason": "stop",
                    }
                ],
                "usage": {
                    "prompt_tokens": 0,
                    "completion_tokens": 0,
                    "total_tokens": 0,
                },
            }

        except (
            TypeError,
            ValueError,
            KeyError,
            AttributeError,
            UnicodeError,
            OverflowError,
        ):
            # Catch specific exceptions from response conversion:
            # - TypeError: Type mismatches in dict values or attribute access
            # - ValueError: Value errors in string formatting or conversions
            # - KeyError: Dictionary key access errors
            # - AttributeError: Attribute access (e.g., domain_request.model)
            # - UnicodeError: String encoding/decoding errors
            # - OverflowError: Numeric overflow (e.g., timestamp conversion)
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    f"Error in response conversion, returning original content - request_id={request_id}",
                    exc_info=True,
                )
            return content

    @staticmethod
    def _chat_completion_to_responses_object(
        content: object,
        *,
        responses_model: str,
        envelope: ResponseEnvelope | None = None,
    ) -> dict[str, Any]:
        if isinstance(content, bytes):
            try:
                content = content.decode("utf-8")
            except UnicodeDecodeError as exc:
                raise ResponsesProtocolError(
                    "ACP non-streaming response is not valid UTF-8",
                    code="invalid_response_shape",
                    param=None,
                    status_code=502,
                    error_type="api_error",
                ) from exc
        if isinstance(content, str):
            raw_text = content
            try:
                content = json.loads(content)
            except json.JSONDecodeError:
                content = raw_text
        if hasattr(content, "model_dump"):
            content = content.model_dump(exclude_none=True)  # type: ignore[union-attr]
        if isinstance(content, str):
            metadata = envelope.metadata if envelope and envelope.metadata else {}
            synthetic_content: dict[str, Any] = {
                "id": metadata.get("id"),
                "model": metadata.get("model") or responses_model,
                "choices": [
                    {
                        "index": 0,
                        "message": {"role": "assistant", "content": content},
                        "finish_reason": "stop",
                    }
                ],
            }
            content = synthetic_content
        if not isinstance(content, dict):
            raise ResponsesProtocolError(
                "ACP non-streaming response must be a Chat Completions object",
                code="invalid_response_shape",
                param=None,
                status_code=502,
                error_type="api_error",
            )

        response_id = str(content.get("id") or f"resp_{int(time.time())}")
        model = str(content.get("model") or responses_model)
        output: list[dict[str, Any]] = []
        choices = content.get("choices")
        if isinstance(choices, list):
            for choice_index, choice_raw in enumerate(choices):
                if not isinstance(choice_raw, dict):
                    continue
                message_raw = choice_raw.get("message")
                if not isinstance(message_raw, dict):
                    continue
                text = message_raw.get("content")
                if isinstance(text, str):
                    output.append(
                        {
                            "id": f"{response_id}:message:{choice_index}",
                            "type": "message",
                            "role": "assistant",
                            "status": "completed",
                            "content": [{"type": "output_text", "text": text}],
                        }
                    )
                tool_calls = message_raw.get("tool_calls")
                if isinstance(tool_calls, list):
                    for tool_index, tool_raw in enumerate(tool_calls):
                        if not isinstance(tool_raw, dict):
                            continue
                        function_raw = tool_raw.get("function")
                        if not isinstance(function_raw, dict):
                            continue
                        call_id = str(
                            tool_raw.get("id")
                            or f"{response_id}:call:{choice_index}:{tool_index}"
                        )
                        arguments_raw = function_raw.get("arguments")
                        arguments = (
                            arguments_raw
                            if isinstance(arguments_raw, str)
                            else json.dumps(arguments_raw or {}, ensure_ascii=False)
                        )
                        output.append(
                            {
                                "id": call_id,
                                "type": "function_call",
                                "status": "completed",
                                "call_id": call_id,
                                "name": str(function_raw.get("name") or ""),
                                "arguments": arguments,
                            }
                        )

        usage_raw = content.get("usage")
        usage_dict = usage_raw if isinstance(usage_raw, dict) else {}
        if not usage_dict and envelope is not None and envelope.usage is not None:
            usage_dict = envelope.usage.to_legacy_dict()

        def token_count(value: object) -> int:
            if isinstance(value, bool):
                return 0
            if isinstance(value, int):
                return value
            if isinstance(value, float | str):
                try:
                    return int(value)
                except ValueError:
                    return 0
            return 0

        input_tokens = token_count(
            usage_dict.get("input_tokens") or usage_dict.get("prompt_tokens")
        )
        output_tokens = token_count(
            usage_dict.get("output_tokens") or usage_dict.get("completion_tokens")
        )
        total_tokens = token_count(
            usage_dict.get("total_tokens") or input_tokens + output_tokens
        )
        normalized_usage = dict(usage_dict)
        normalized_usage["input_tokens"] = input_tokens
        normalized_usage["output_tokens"] = output_tokens
        normalized_usage["total_tokens"] = total_tokens
        normalized_usage.pop("prompt_tokens", None)
        normalized_usage.pop("completion_tokens", None)
        return {
            "id": response_id,
            "object": "response",
            "created_at": int(content.get("created") or time.time()),
            "status": "completed",
            "model": model,
            "output": output,
            "usage": normalized_usage,
        }

    async def handle_responses_request(
        self,
        request: Request,
        request_data: ResponsesRequest | dict[str, Any],
    ) -> Response:
        """Handle Responses API requests.

        Args:
            request: The HTTP request
            request_data: The parsed request data as a ResponsesRequest

        Returns:
            An HTTP response
        """
        request_id = self._resolve_request_id(request)
        responses_request = self._parse_responses_request(
            request_data, request_id=request_id
        )
        has_schema, schema_name = self._validate_schema_if_present(
            request_id=request_id,
            responses_request=responses_request,
        )

        if logger.isEnabledFor(logging.INFO):
            logger.info(
                "Responses API request received - request_id=%s, model=%s, has_schema=%s, schema_name=%s",
                request_id,
                responses_request.model,
                has_schema,
                schema_name,
            )

        try:
            self._log_schema_validation_attempt(
                request_id=request_id,
                responses_request=responses_request,
                has_schema=has_schema,
                schema_name=schema_name,
            )

            try:
                responses_domain, domain_request, stream_source, store_instructions = (
                    await self._prepare_responses_execution(
                        responses_request=responses_request,
                    )
                )
            except ResponsesValidationError:
                raise
            except ValidationError as exc:
                error_details = exc.errors()
                if logger.isEnabledFor(logging.INFO):
                    logger.info(
                        f"Validation error in Responses normalization - request_id={request_id}, errors={error_details}",
                        exc_info=True,
                    )
                raise self._map_validation_error(exc, request_id=request_id) from exc

            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    f"Request normalization successful - request_id={request_id}, "
                    f"domain_model={responses_domain.model}, processor_type={type(self._processor).__name__}"
                )

            ctx = self._build_request_context(
                request=request,
                domain_request=domain_request,
                responses_request=responses_request,
                request_id=request_id,
                schema_name=schema_name,
            )
            ctx.request_id = request_id
            ctx.extensions["responses_semantic_pipeline"] = True
            ctx.extensions["responses_stream_source"] = stream_source.value
            ctx.extensions["responses_store_instructions"] = store_instructions
            ctx.extensions["responses_store_history_items"] = cast(
                Any,
                await self._responses_history_for_turn(responses_domain, []),
            )
            request.state.responses_semantic_pipeline = True
            # Requirement 5.5: Proactive session metrics initialization
            # Initialize session metrics early in lifecycle before backend work begins
            # This ensures metrics exist for EoS emission even if client disconnects immediately
            # Design.md line 434-437: Two-phase approach - proactive (primary) + defensive fallback
            if self._metrics_initializer is not None and ctx.request_id:
                try:
                    session_key = resolve_session_key_from_request_context(ctx)
                    if session_key is not None:
                        await self._metrics_initializer.ensure_session_metrics(
                            session_key, observed_at=datetime.now(timezone.utc)
                        )
                except Exception as exc:
                    # Requirement 3.9: Fail-open behavior - log but don't raise
                    # Design.md line 413: Log with high-signal error code for visibility
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            "Failed to initialize session metrics proactively: %s",
                            exc,
                            exc_info=True,
                            extra={
                                "request_id": ctx.request_id,
                                "error_code": "SESSION_METRICS_INIT_FAILED",
                            },
                        )
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    f"Processing request through pipeline - request_id={request_id}"
                )
            response = await self._processor.process_request(ctx, domain_request)

            # Convert domain response to FastAPI response
            # Ensure we await the response if it's a coroutine
            if asyncio.iscoroutine(response):
                response = await response

            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    f"Request processing completed - request_id={request_id}, response_type={type(response).__name__}"
                )

            # Check if this is a streaming response
            if isinstance(response, StreamingResponseEnvelope):
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        f"Returning streaming response - request_id={request_id}"
                    )

                stream_generator = self._stream_response_envelope(
                    request=request,
                    domain_request=domain_request,
                    response=response,
                    request_id=request_id,
                    context=ctx,
                )

                # Build streaming headers, merging backend headers if available (Goal 1)
                streaming_headers = {
                    "cache-control": "no-cache",
                    "connection": "keep-alive",
                    "content-type": "text/event-stream",
                    "access-control-allow-origin": "*",
                    "access-control-allow-headers": "*",
                }
                if response.headers:
                    streaming_headers.update(response.headers)

                return StreamingResponse(
                    content=stream_generator,
                    status_code=200,
                    media_type="text/event-stream",
                    headers=streaming_headers,
                )

            converted_content: object
            if stream_source is ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS:
                converted_content = self._chat_completion_to_responses_object(
                    response.content,
                    responses_model=responses_domain.model,
                    envelope=response,
                )
            else:
                converted_content = self._ensure_responses_schema_content(
                    response.content,
                    request_id=request_id,
                    envelope=response,
                    responses_model=responses_domain.model,
                )

            final_response = domain_response_to_fastapi(
                dataclasses.replace(response, content=cast(Any, converted_content)),
                wire_capture=self._wire_capture,
                context=ctx,
            )

            if isinstance(converted_content, dict):
                payload_to_store = converted_content
                if isinstance(response.content, dict):
                    original_response_id = response.content.get("id")
                    if isinstance(original_response_id, str) and original_response_id:
                        payload_to_store = response.content
                await self._store_completed_responses_payload(
                    payload_to_store,
                    instructions=(
                        store_instructions
                        if isinstance(store_instructions, str)
                        else None
                    ),
                    history_items=cast(
                        list[ResponsesHistoryItem] | None,
                        ctx.extensions.get("responses_store_history_items"),
                    ),
                )

            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    f"Responses API request completed successfully - request_id={request_id}"
                )
            return final_response

        except LLMProxyError as e:
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    f"LLM Proxy error in Responses API - request_id={request_id}, error={e}",
                    exc_info=True,
                )
            # Map domain exceptions to HTTP exceptions
            raise map_domain_exception_to_http_exception(e, request=request) from e
        except HTTPException as e:
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    f"HTTP error in Responses API - request_id={request_id}, status={e.status_code}, detail={e.detail}",
                    exc_info=True,
                )
            # Re-raise HTTP exceptions
            raise
        except Exception as e:
            # Log and convert other exceptions to HTTP exceptions
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    f"Unexpected error handling Responses API request - request_id={request_id}, error={e}",
                    exc_info=True,
                )
            raise HTTPException(
                status_code=500,
                detail=_http_detail_with_request_id(
                    {
                        "error": {
                            "message": "An internal server error occurred while processing your request.",
                            "type": "internal_server_error",
                            "code": "internal_error",
                        }
                    },
                    request_id=request_id,
                ),
            ) from e

    @staticmethod
    def _resolve_request_id(request: Request) -> str:
        return getattr(request.state, "request_id", None) or f"req-{id(request)}"

    @staticmethod
    def _parse_responses_request(
        request_data: ResponsesRequest | dict[str, Any],
        *,
        request_id: str | None = None,
    ) -> ResponsesRequest:
        try:
            responses_request = (
                request_data
                if isinstance(request_data, ResponsesRequest)
                else ResponsesRequest.model_validate(request_data)
            )
            return responses_request
        except ValidationError as exc:
            raise ResponsesController._map_validation_error(
                exc, request_id=request_id
            ) from exc

    def _validate_schema_if_present(
        self, *, request_id: str, responses_request: ResponsesRequest
    ) -> tuple[bool, str | None]:
        response_format = responses_request.response_format
        has_schema = bool(response_format and response_format.json_schema)
        if not (has_schema and response_format and response_format.json_schema):
            return False, None

        json_schema = response_format.json_schema
        schema_name = getattr(json_schema, "name", "unnamed")
        try:
            self._validate_json_schema(json_schema.get_schema())
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "JSON schema validation passed - request_id=%s, schema_name=%s",
                    request_id,
                    schema_name,
                )
        except Exception as exc:
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "JSON schema validation failed - request_id=%s, schema_name=%s",
                    request_id,
                    schema_name,
                    exc_info=True,
                )
            raise HTTPException(
                status_code=400,
                detail=_http_detail_with_request_id(
                    {
                        "error": {
                            "message": f"Invalid JSON schema: {exc!s}",
                            "type": "invalid_schema",
                            "code": "invalid_schema",
                        }
                    },
                    request_id=request_id,
                ),
            )

        return True, schema_name

    def _log_schema_validation_attempt(
        self,
        *,
        request_id: str,
        responses_request: ResponsesRequest,
        has_schema: bool,
        schema_name: str | None,
    ) -> None:
        if not logger.isEnabledFor(logging.DEBUG):
            return
        if not has_schema:
            return
        response_format = responses_request.response_format
        json_schema = (
            getattr(response_format, "json_schema", None) if response_format else None
        )
        if json_schema is None:
            return
        logger.debug(
            "Schema validation requested - request_id=%s, schema_name=%s, strict=%s",
            request_id,
            schema_name,
            getattr(json_schema, "strict", True),
        )

    def _build_request_context(
        self,
        *,
        request: Request,
        domain_request: CanonicalChatRequest,
        responses_request: ResponsesRequest,
        request_id: str,
        schema_name: str | None,
    ) -> RequestContext:
        ctx = fastapi_to_domain_request_context(
            request,
            attach_original=True,
            domain_request=domain_request,
        )

        # Set request_id on context for SessionKey resolution (Requirement 1.6)
        ctx.request_id = request_id

        # Set protocol identifier for normalization (Requirement 1.10)
        ctx.extensions["protocol"] = "openai-responses"

        self._attach_schema_context(
            ctx=ctx,
            responses_request=responses_request,
            request_id=request_id,
            schema_name=schema_name,
        )
        return ctx

    def _attach_schema_context(
        self,
        *,
        ctx: RequestContext,
        responses_request: ResponsesRequest,
        request_id: str,
        schema_name: str | None,
    ) -> None:
        response_format = getattr(responses_request, "response_format", None)
        json_schema = (
            getattr(response_format, "json_schema", None) if response_format else None
        )
        if json_schema is None:
            return

        from src.core.domain.request_context import ProcessingContext

        if ctx.processing_context is None:
            ctx.processing_context = ProcessingContext(values={})

        schema_dict = json_schema.get_schema()
        if not isinstance(schema_dict, dict) or "type" not in schema_dict:
            raise HTTPException(
                status_code=400,
                detail=_http_detail_with_request_id(
                    {
                        "error": {
                            "message": "Invalid JSON schema: missing 'type' field",
                            "type": "invalid_request_error",
                            "code": "invalid_schema",
                        }
                    },
                    request_id=request_id,
                ),
            )

        ctx.processing_context.values.update(
            {
                "response_schema": schema_dict,
                "strict_schema_validation": getattr(json_schema, "strict", True),
                "schema_name": getattr(json_schema, "name", "unknown"),
                "request_id": request_id,
            }
        )

        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Schema context added to processing pipeline - request_id=%s, schema_name=%s, strict=%s",
                request_id,
                schema_name,
                getattr(json_schema, "strict", True),
            )

    @staticmethod
    def _map_validation_error(
        exc: ValidationError, *, request_id: str | None = None
    ) -> HTTPException:
        """Convert validation errors into HTTP exceptions with appropriate status codes."""

        errors = exc.errors()
        for error in errors:
            loc = error.get("loc", ())
            if any(part in {"schema", "schema_dict"} for part in loc):
                message = error.get("msg", "Invalid JSON schema")
                if message.lower().startswith("value error, "):
                    message = message[12:]
                detail = {
                    "error": {
                        "message": f"Invalid JSON schema: {message}",
                        "type": "invalid_request_error",
                        "code": "invalid_schema",
                    }
                }
                if request_id:
                    detail = _http_detail_with_request_id(detail, request_id=request_id)
                return HTTPException(
                    status_code=400,
                    detail=detail,
                )

        # For other validation errors, use OpenAI-style error format
        # Simplify error handling to avoid mypy issues with ValidationError loc field
        detail422 = {
            "error": {
                "message": "Invalid request format. Please check your request parameters and try again.",
                "type": "invalid_request_error",
                "code": "invalid_request",
            }
        }
        if request_id:
            detail422 = _http_detail_with_request_id(detail422, request_id=request_id)
        return HTTPException(
            status_code=422,
            detail=detail422,
        )

    def _stream_response_envelope(
        self,
        request: Request,
        domain_request: Any,
        response: StreamingResponseEnvelope,
        request_id: str,
        context: Any | None = None,
    ) -> AsyncIterator[str]:
        async def _generator() -> AsyncIterator[str]:
            import json
            import time
            from datetime import datetime, timezone

            response_id = f"resp_{int(time.time())}_{id(response)}"
            created_timestamp = int(time.time())
            stream_terminated = False
            wire_emitter: ResponsesWireStreamEmitter | None = None
            native_wire_passthrough = False
            last_domain_chunk: dict[str, Any] | None = None

            cancel_lock = asyncio.Lock()
            cancel_state = {"called": False}
            termination_reported = {"reported": False}

            async def report_client_termination(
                termination_reason: ClientTerminationReason,
            ) -> None:
                """Report client termination in shielded context.

                This function ensures termination reporting executes even if
                the request task is cancelled (Requirement 3.6, 3.8).
                """
                # Requirement 1.6: Only report if session context is available
                if context is None:
                    return

                session_key = resolve_session_key_from_request_context(context)
                if session_key is None:
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Cannot report client termination: session_key cannot be resolved",
                            extra={"request_id": request_id},
                        )
                    return

                # Deduplicate: only report once per stream
                async with cancel_lock:
                    if termination_reported["reported"]:
                        return
                    termination_reported["reported"] = True

                # Shield termination reporting to ensure it executes even if task is cancelled
                if self._client_eos_service is not None:
                    try:
                        signal = ClientEndOfSessionSignal(
                            session_key=session_key,
                            observed_at=datetime.now(timezone.utc),
                            reason=termination_reason,
                            details=f"HTTP streaming disconnect detected - request_id={request_id}",
                        )
                        # Use asyncio.shield to ensure this executes even if generator is cancelled
                        await asyncio.shield(
                            self._client_eos_service.report_client_termination(signal)
                        )
                    except Exception as exc:
                        # Fail-open: log but don't raise - termination reporting is best-effort
                        # Design.md line 445: Log with high-visibility error code
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Failed to report client termination for streaming disconnect: %s",
                                exc,
                                exc_info=True,
                                extra={
                                    "request_id": request_id,
                                    "session_key": {
                                        "protocol": session_key.protocol,
                                        "primary_id": session_key.primary_id,
                                    },
                                    "error_code": "CLIENT_TERMINATION_REPORT_FAILED",
                                },
                            )

            async def trigger_cancel(reason: str) -> None:
                # Set cancel_reason in RequestContext for normalization (Requirement 3.5, 3.6)
                if context is not None:
                    from src.core.domain.request_context import ProcessingContext

                    if context.processing_context is None:
                        context.processing_context = ProcessingContext(values={})
                    elif context.processing_context.values is None:
                        context.processing_context.values = {}
                    context.processing_context.values["cancel_reason"] = (
                        "client_disconnect"
                        if reason == "client_disconnect"
                        else "stream_cancelled"
                    )

                cancel_cb = response.cancel_callback
                if cancel_cb is None:
                    return

                async with cancel_lock:
                    if cancel_state["called"]:
                        return
                    cancel_state["called"] = True

                try:
                    await cancel_cb()
                    if logger.isEnabledFor(logging.INFO):
                        logger.info(
                            "Dispatched backend cancellation - request_id=%s, reason=%s",
                            request_id,
                            reason,
                        )
                except Exception as exc:
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            "Failed to propagate cancellation upstream - request_id=%s, reason=%s, error=%s",
                            request_id,
                            reason,
                            exc,
                            exc_info=True,
                        )

            async def is_disconnected() -> bool:
                checker = getattr(request, "is_disconnected", None)
                if checker is None or not callable(checker):
                    return False

                try:
                    result = checker()
                    if asyncio.iscoroutine(result):
                        return bool(await result)
                    return bool(result)
                except (RuntimeError, AttributeError, asyncio.CancelledError):
                    # RuntimeError: checker function or event loop issues
                    # AttributeError: defensive guard for unexpected attribute access
                    # CancelledError: async operation cancelled
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Failed checking client disconnect status - request_id=%s",
                            request_id,
                            exc_info=True,
                        )
                    return False

            chunk_iterator: AsyncIterator[Any] = (
                response.content
                if response.content is not None
                else _empty_responses_chunk_iterator()
            )

            extra_body_for_semantic = getattr(domain_request, "extra_body", None) or {}
            request_state = getattr(request, "state", None)
            state_semantic = bool(
                getattr(request_state, "responses_semantic_pipeline", False)
            )
            use_semantic = (
                bool(
                    context is not None
                    and isinstance(getattr(context, "extensions", None), dict)
                    and context.extensions.get("responses_semantic_pipeline")
                )
                or (
                    isinstance(extra_body_for_semantic, dict)
                    and RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY
                    in extra_body_for_semantic
                )
                or state_semantic
            )

            try:
                if use_semantic:
                    assert context is not None
                    ctx_extensions = context.extensions
                    src_raw = ctx_extensions.get("responses_stream_source")
                    if src_raw is None or (
                        isinstance(src_raw, str) and not src_raw.strip()
                    ):
                        raise ResponsesProtocolError(
                            "Missing or empty responses_stream_source for semantic "
                            "streaming",
                            code="missing_stream_source",
                            param="responses_stream_source",
                            status_code=500,
                            error_type="internal_error",
                        )
                    try:
                        stream_source = ResponsesStreamSource(str(src_raw))
                    except ValueError as exc:
                        raise ResponsesProtocolError(
                            f"Invalid responses_stream_source: {src_raw!r}",
                            code="invalid_stream_source",
                            param="responses_stream_source",
                            status_code=500,
                            error_type="internal_error",
                        ) from exc
                    normalizer = ResponsesEventNormalizer(
                        source=stream_source,
                        response_id=response_id,
                    )

                    async def _disconnect_aware_chunks() -> AsyncIterator[Any]:
                        nonlocal stream_terminated

                        async def _abort_if_disconnected() -> None:
                            nonlocal stream_terminated
                            if not await is_disconnected():
                                return
                            stream_terminated = True
                            await report_client_termination(
                                ClientTerminationReason.CLIENT_DISCONNECTED
                            )
                            await trigger_cancel("client_disconnect")
                            raise ResponsesStreamAbortedError(
                                "Client disconnected during Responses streaming"
                            )

                        await _abort_if_disconnected()
                        async for upstream_chunk in chunk_iterator:
                            await _abort_if_disconnected()
                            yield upstream_chunk
                        # Also inspect the disconnect state after upstream EOF.
                        # Otherwise an empty/short ACP stream can be mistaken for
                        # a successful completion after the client has gone away.
                        await _abort_if_disconnected()

                    renderer = ResponsesWireRenderer(
                        self._responses_session_store,
                        transport="sse",
                    )
                    store_instructions = ctx_extensions.get(
                        "responses_store_instructions"
                    )
                    if not isinstance(store_instructions, str):
                        store_instructions = None
                    raw_store_history_items = ctx_extensions.get(
                        "responses_store_history_items"
                    )
                    store_history_items = (
                        cast(list[ResponsesHistoryItem], raw_store_history_items)
                        if isinstance(raw_store_history_items, list)
                        else None
                    )
                    event_stream = normalizer.normalize(_disconnect_aware_chunks())
                    async for frame in renderer.render(
                        event_stream,
                        response_id,
                        instructions=store_instructions,
                        history_items=store_history_items,
                        emit_done_sentinel=(
                            stream_source
                            is not ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS
                        ),
                    ):
                        if isinstance(frame, str):
                            yield frame
                        else:
                            yield json.dumps(frame)
                    return

                async for chunk in chunk_iterator:
                    if await is_disconnected():
                        stream_terminated = True
                        # Report client termination before triggering cancellation
                        # (Requirement 1.1, 3.6: detect and report disconnect)
                        await report_client_termination(
                            ClientTerminationReason.CLIENT_DISCONNECTED
                        )
                        await trigger_cancel("client_disconnect")
                        break

                    try:
                        chunk_payload = coerce_stream_chunk_payload(
                            chunk, default_response_id=response_id
                        )

                        if not isinstance(chunk_payload, dict):
                            if logger.isEnabledFor(logging.DEBUG):
                                logger.debug(
                                    "Skipping non-object Responses stream chunk type=%s",
                                    type(chunk).__name__,
                                    extra={"request_id": request_id},
                                )
                            continue

                        last_domain_chunk = chunk_payload

                        ot_raw = chunk_payload.get("type")
                        if (
                            isinstance(ot_raw, str)
                            and ot_raw.startswith("response.")
                            and ot_raw != "response.chunk"
                        ):
                            native_wire_passthrough = True
                            yield f"data: {json.dumps(chunk_payload)}\n\n"
                            continue

                        if wire_emitter is None:
                            wire_emitter = ResponsesWireStreamEmitter(
                                model=str(
                                    getattr(domain_request, "model", None) or "unknown"
                                ),
                                created_at=float(created_timestamp),
                            )
                        for wire_evt in wire_emitter.feed(chunk_payload):
                            yield f"data: {json.dumps(wire_evt)}\n\n"

                    except Exception as exc:
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Error processing streaming chunk - request_id=%s, error=%s",
                                request_id,
                                exc,
                                exc_info=True,
                            )
                        continue

                if not stream_terminated:
                    if native_wire_passthrough:
                        pass
                    elif wire_emitter is not None and not wire_emitter.is_finished():
                        for wire_evt in wire_emitter.finalize(
                            tail_domain_chunk=last_domain_chunk
                        ):
                            yield f"data: {json.dumps(wire_evt)}\n\n"
                    elif wire_emitter is None and not native_wire_passthrough:
                        empty_wire = ResponsesWireStreamEmitter(
                            model=str(
                                getattr(domain_request, "model", None) or "unknown"
                            ),
                            created_at=float(created_timestamp),
                        )
                        for wire_evt in empty_wire.finalize(
                            tail_domain_chunk=last_domain_chunk
                        ):
                            yield f"data: {json.dumps(wire_evt)}\n\n"
                    yield "data: [DONE]\n\n"

            except GeneratorExit:
                # Client disconnected during streaming (Requirement 1.1, 3.6)
                stream_terminated = True
                # Report termination in shielded context (Requirement 3.8)
                try:
                    await asyncio.shield(
                        report_client_termination(
                            ClientTerminationReason.CLIENT_DISCONNECTED
                        )
                    )
                except Exception as exc:
                    # Fail-open: log but continue with cleanup
                    # Design.md line 445: Log with high-visibility error code
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            "Failed to report client termination in GeneratorExit handler: %s",
                            exc,
                            exc_info=True,
                            extra={
                                "request_id": request_id,
                                "error_code": "CLIENT_TERMINATION_REPORT_FAILED",
                            },
                        )
                await trigger_cancel("client_disconnect")
                raise
            except asyncio.CancelledError:
                stream_terminated = True
                # Report cancellation as client termination (Requirement 1.2, 3.8)
                try:
                    await asyncio.shield(
                        report_client_termination(
                            ClientTerminationReason.CLIENT_CANCELLED
                        )
                    )
                except Exception as exc:
                    # Fail-open: log but continue with cleanup
                    # Design.md line 445: Log with high-visibility error code
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            "Failed to report client termination in CancelledError handler: %s",
                            exc,
                            exc_info=True,
                            extra={
                                "request_id": request_id,
                                "error_code": "CLIENT_TERMINATION_REPORT_FAILED",
                            },
                        )
                await trigger_cancel("stream_cancelled")
                raise
            except ResponsesStreamAbortedError:
                # The client is gone.  Do not expose a synthetic terminal event or
                # persist partial output as a chainable response.
                stream_terminated = True
                return
            except ResponsesProtocolError:
                # Semantic / contract failures are not generic stream I/O errors; do not
                # run cancellation bookkeeping that assumes a full RequestContext.
                raise
            except Exception as e:
                if not stream_terminated:
                    await trigger_cancel("stream_error")
                if logger.isEnabledFor(logging.ERROR):
                    logger.error(
                        "Unexpected error in streaming response handler: %s",
                        e,
                        exc_info=True,
                        extra={"request_id": request_id},
                    )
                raise
            finally:
                # Ensure termination is reported even if stream ends abnormally
                # (defensive fallback for edge cases)
                if stream_terminated and not termination_reported["reported"]:
                    try:
                        await asyncio.shield(
                            report_client_termination(
                                ClientTerminationReason.CLIENT_DISCONNECTED
                            )
                        )
                    except Exception as exc:
                        # Fail-open: best-effort reporting
                        # Design.md line 445: Log with high-visibility error code
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Failed to report client termination in finally block: %s",
                                exc,
                                exc_info=True,
                                extra={
                                    "request_id": request_id,
                                    "error_code": "CLIENT_TERMINATION_REPORT_FAILED",
                                },
                            )

                if cancel_state["called"]:
                    close_method = getattr(response.content, "aclose", None)
                    if callable(close_method):
                        with contextlib.suppress(Exception):
                            close_result = close_method()
                            await cast(Awaitable[Any], close_result)

        return _generator()

    @staticmethod
    def _validate_json_schema(schema: dict[str, Any]) -> None:
        """
        Validate a JSON schema for correctness and completeness.

        Args:
            schema: The JSON schema to validate

        Raises:
            ValueError: If the schema is invalid
        """
        enforce_schema_size_limits(schema)

        # Check for required fields
        ResponsesController._ensure_safe_regex_patterns(schema)

        if "type" not in schema:
            raise ValueError("Schema must have a 'type' field")

        # Basic structure validation
        schema_type_raw = schema["type"]
        if isinstance(schema_type_raw, str):
            schema_types = [schema_type_raw]
        elif isinstance(schema_type_raw, list | tuple | set):
            schema_types = [
                str(t) for t in schema_type_raw if isinstance(t, str | bytes)
            ]
        else:
            schema_types = [str(schema_type_raw)]

        if "object" in schema_types:
            # Objects can describe their shape via properties, patternProperties,
            # or references. Require at least one structural keyword so callers
            # can use $ref-only schemas without triggering false positives.
            object_keywords = {
                "properties",
                "patternProperties",
                "additionalProperties",
                "$ref",
                "allOf",
                "anyOf",
                "oneOf",
            }
            if not any(key in schema for key in object_keywords):
                raise ValueError(
                    "Object schemas must declare properties, patternProperties, "
                    "additionalProperties, or use a composition/ref keyword"
                )

            properties = schema.get("properties")
            if properties is not None and not isinstance(properties, dict):
                raise ValueError("Properties must be a dictionary")

            if isinstance(properties, dict):
                # Validate each property
                for prop_name, prop_schema in properties.items():
                    if not isinstance(prop_schema, dict):
                        raise ValueError(
                            f"Property '{prop_name}' schema must be a dictionary"
                        )

                    allowed_structural_keywords = {
                        "type",
                        "$ref",
                        "anyOf",
                        "allOf",
                        "oneOf",
                        "enum",
                        "const",
                        "properties",
                        "patternProperties",
                        "items",
                        "contains",
                        "if",
                        "then",
                        "else",
                        "not",
                        "dependentSchemas",
                    }

                    if "type" not in prop_schema and not any(
                        key in prop_schema for key in allowed_structural_keywords
                    ):
                        raise ValueError(
                            f"Property '{prop_name}' must define a type or a "
                            "supported schema keyword"
                        )

        if "array" in schema_types:
            # Arrays should have items; allow both dict schemas and tuple-style lists
            if "items" not in schema:
                raise ValueError("Array schemas must have an 'items' field")

            items_schema = schema["items"]
            if not isinstance(items_schema, dict | list | tuple | bool):
                raise ValueError("Items schema must be a dictionary, list, or boolean")

        primitive_types = {"string", "number", "integer", "boolean", "null"}
        known_types = primitive_types | {"object", "array"}
        unknown_types = [t for t in schema_types if t not in known_types]
        for unknown in unknown_types:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning("Unusual schema type detected: %s", unknown)

        # Validate additional properties if present
        if "additionalProperties" in schema:
            additional_props = schema["additionalProperties"]
            if not isinstance(additional_props, bool | dict):
                raise ValueError("additionalProperties must be a boolean or schema")

        # Validate required fields if present
        if "required" in schema:
            required = schema["required"]
            if not isinstance(required, list):
                raise ValueError("Required field must be a list")

            # Check if required fields are defined (either directly or via composition)
            if "object" in schema_types:
                # Skip validation if schema uses composition keywords that may define fields
                composition_keywords = {"allOf", "anyOf", "oneOf", "$ref"}
                has_composition = any(key in schema for key in composition_keywords)

                if not has_composition and "properties" in schema:
                    properties = schema["properties"]
                    for req_field in required:
                        if req_field not in properties:
                            raise ValueError(
                                f"Required field '{req_field}' not found in properties"
                            )

        # Validate enum if present
        if "enum" in schema:
            enum_values = schema["enum"]
            if not isinstance(enum_values, list) or len(enum_values) == 0:
                raise ValueError("Enum must be a non-empty list")

        enforce_json_schema_limits(schema)

    @staticmethod
    def _ensure_safe_regex_patterns(schema: dict[str, Any]) -> None:
        """Validate regex patterns in a schema to avoid catastrophic backtracking."""

        stack: list[tuple[Any, str]] = [(schema, "$")]
        visited: set[int] = set()

        while stack:
            node, location = stack.pop()
            node_id = id(node)
            if node_id in visited:
                continue
            visited.add(node_id)

            if isinstance(node, dict):
                pattern = node.get("pattern")
                if isinstance(pattern, str):
                    ResponsesController._validate_single_regex(
                        pattern, f"{location}.pattern"
                    )

                pattern_properties = node.get("patternProperties")
                if isinstance(pattern_properties, dict):
                    for regex_key, sub_schema in pattern_properties.items():
                        if isinstance(regex_key, str):
                            ResponsesController._validate_single_regex(
                                regex_key,
                                f"{location}.patternProperties[{regex_key}]",
                            )
                        if isinstance(sub_schema, dict | list):
                            stack.append(
                                (
                                    sub_schema,
                                    f"{location}.patternProperties.{regex_key}",
                                )
                            )

                for key, value in node.items():
                    if key == "patternProperties":
                        continue
                    if isinstance(value, dict | list):
                        stack.append((value, f"{location}.{key}"))

            elif isinstance(node, list):
                for index, item in enumerate(node):
                    if isinstance(item, dict | list):
                        stack.append((item, f"{location}[{index}]"))

    @staticmethod
    def _validate_single_regex(pattern: str, location: str) -> None:
        """Validate an individual regex for potential ReDoS characteristics."""

        if len(pattern) > ResponsesController._MAX_REGEX_PATTERN_LENGTH:
            raise ValueError(
                "Regex pattern too long: "
                f"{location} has {len(pattern)} characters (limit is {ResponsesController._MAX_REGEX_PATTERN_LENGTH})"
            )

        try:
            parsed = sre_parse.parse(pattern)
        except re.error as exc:  # pragma: no cover - invalid regex handled elsewhere
            raise ValueError(
                f"Invalid regex pattern at {location}: {exc.args[0]}"
            ) from exc

        if ResponsesController._contains_nested_unbounded_repeat(parsed):
            raise ValueError(
                "Regex pattern contains nested unbounded quantifiers which "
                f"can lead to catastrophic backtracking: {location}"
            )

    @staticmethod
    def _contains_nested_unbounded_repeat(
        subpattern: sre_parse.SubPattern, inside_unbounded: bool = False
    ) -> bool:
        """Detect nested unbounded repeats within a parsed regex pattern."""

        # sre_parse.SubPattern is iterable but mypy can't understand this
        for token in cast(list[tuple[Any, Any]], subpattern):  # type: ignore[arg-type]
            operator, argument = token

            if operator in {sre_parse.MAX_REPEAT, sre_parse.MIN_REPEAT}:
                # unpack with type ignore due to mypy not understanding sre_parse tuple structure
                _, max_repeat, nested = cast(tuple, argument)  # type: ignore[misc]
                is_unbounded = max_repeat == MAXREPEAT

                if inside_unbounded and is_unbounded:
                    return True

                if ResponsesController._contains_nested_unbounded_repeat(
                    cast(sre_parse.SubPattern, nested),
                    inside_unbounded=is_unbounded or inside_unbounded,
                ):
                    return True

                continue

            if operator == sre_parse.SUBPATTERN:
                # argument is a tuple, nested pattern is the last element
                nested = cast(tuple, argument)[-1]  # type: ignore[index]
                if ResponsesController._contains_nested_unbounded_repeat(
                    cast(sre_parse.SubPattern, nested),
                    inside_unbounded=inside_unbounded,
                ):
                    return True
                continue

            if operator == sre_parse.BRANCH:
                # argument is a tuple, second element is list of branches
                _, branches = cast(tuple, argument)  # type: ignore[misc]
                for branch in cast(list[Any], branches):
                    if ResponsesController._contains_nested_unbounded_repeat(
                        cast(sre_parse.SubPattern, branch),
                        inside_unbounded=inside_unbounded,
                    ):
                        return True
                continue

            if operator in {sre_parse.ASSERT, sre_parse.ASSERT_NOT}:
                # argument is a tuple, second element is the nested pattern
                nested = cast(tuple, argument)[1]  # type: ignore[index]
                if ResponsesController._contains_nested_unbounded_repeat(
                    cast(sre_parse.SubPattern, nested),
                    inside_unbounded=inside_unbounded,
                ):
                    return True

        return False

    async def handle_websocket_connection(
        self, websocket: WebSocket, request_id: str | None = None
    ) -> None:
        """Handle WebSocket connections for Responses API.

        Args:
            websocket: FastAPI WebSocket connection
            request_id: Optional request ID for correlation
        """
        # Accept the connection
        await websocket.accept()

        if request_id is None:
            request_id = f"ws-{int(time.time())}-{id(websocket)}"

        if logger.isEnabledFor(logging.INFO):
            logger.info(
                "WebSocket connection established - request_id=%s",
                request_id,
            )

        connection_start_time = time.time()
        connection_timeout = 3600  # 60 minutes per OpenAI spec

        try:
            while True:
                # Check connection timeout
                elapsed = time.time() - connection_start_time
                if elapsed >= connection_timeout:
                    error_event = {
                        "type": "error",
                        "request_id": request_id,
                        "error": {
                            "type": "invalid_request_error",
                            "code": "websocket_connection_limit_reached",
                            "message": "Responses websocket connection limit reached (60 minutes). Create a new websocket connection to continue.",
                        },
                        "status": 400,
                    }
                    await websocket.send_json(error_event)
                    break

                # Receive message from client
                try:
                    message = await websocket.receive_text()
                except WebSocketDisconnect:
                    if logger.isEnabledFor(logging.INFO):
                        logger.info(
                            "WebSocket client disconnected - request_id=%s",
                            request_id,
                        )
                    break

                # Parse the message
                try:
                    event_data = json.loads(message)
                except json.JSONDecodeError as e:
                    error_event = {
                        "type": "error",
                        "request_id": request_id,
                        "error": {
                            "type": "invalid_request_error",
                            "code": "invalid_json",
                            "message": f"Invalid JSON: {e}",
                        },
                        "status": 400,
                    }
                    await websocket.send_json(error_event)
                    continue

                event_type = event_data.get("type")

                # Handle response.create events
                if event_type == "response.create":
                    await self._handle_websocket_response_create(
                        websocket=websocket,
                        event_data=event_data,
                        request_id=request_id,
                    )
                else:
                    # Unsupported event type
                    error_event = {
                        "type": "error",
                        "request_id": request_id,
                        "error": {
                            "type": "invalid_request_error",
                            "code": "unsupported_event_type",
                            "message": f"Unsupported event type: {event_type}",
                        },
                        "status": 400,
                    }
                    await websocket.send_json(error_event)

        except Exception as e:
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Error in WebSocket handler - request_id=%s: %s",
                    request_id,
                    e,
                    exc_info=True,
                )
            # Try to send error to client
            try:
                error_event = {
                    "type": "error",
                    "request_id": request_id,
                    "error": {
                        "type": "internal_server_error",
                        "code": "internal_error",
                        "message": "An internal error occurred",
                    },
                    "status": 500,
                }
                await websocket.send_json(error_event)
            except Exception:
                pass
        finally:
            # Clean up
            with contextlib.suppress(Exception):
                await websocket.close()

            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    "WebSocket connection closed - request_id=%s",
                    request_id,
                )

    async def _handle_websocket_response_create(
        self,
        websocket: WebSocket,
        event_data: dict[str, Any],
        request_id: str,
    ) -> None:
        """Handle response.create event from WebSocket client.

        Args:
            websocket: WebSocket connection
            event_data: Event data from client
            request_id: Request ID for correlation
        """
        # Capture inbound WebSocket event if wire capture is enabled
        if self._wire_capture and self._wire_capture.enabled():
            try:
                inbound_bytes = json.dumps(
                    event_data, separators=(",", ":"), ensure_ascii=False
                ).encode("utf-8")
                await self._wire_capture.capture_inbound_request(
                    context=None,
                    session_id=None,
                    request_payload=inbound_bytes,
                    capture_metadata={
                        "transport": "websocket",
                        "protocol_event": "frame",
                        "websocket_message_type": "text",
                        "event_type": "response.create",
                        "request_id": request_id,
                    },
                )
            except Exception as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Failed to capture WebSocket inbound event: %s",
                        e,
                        exc_info=True,
                    )

        try:
            responses_request = self._parse_responses_request(
                event_data, request_id=request_id
            )
            has_schema, schema_name = self._validate_schema_if_present(
                request_id=request_id,
                responses_request=responses_request,
            )
            self._log_schema_validation_attempt(
                request_id=request_id,
                responses_request=responses_request,
                has_schema=has_schema,
                schema_name=schema_name,
            )

            from src.core.domain.request_context import RequestContext

            ctx = RequestContext(
                request_id=request_id,
                headers={},
                cookies={},
                client_host=None,
                original_request=None,
                state={},
                app_state={},
            )
            ctx.extensions["protocol"] = "openai-responses-ws"

            (
                responses_domain,
                domain_request,
                stream_source,
                store_instructions,
            ) = await self._prepare_responses_execution(
                responses_request=responses_request,
            )
            ctx.extensions["responses_semantic_pipeline"] = True
            ctx.extensions["responses_stream_source"] = stream_source.value
            ctx.extensions["responses_store_instructions"] = store_instructions
            ctx.extensions["responses_store_history_items"] = cast(
                Any,
                await self._responses_history_for_turn(responses_domain, []),
            )

            response_format = responses_request.response_format
            if response_format and response_format.json_schema:
                self._attach_schema_context(
                    ctx=ctx,
                    responses_request=responses_request,
                    request_id=request_id,
                    schema_name=schema_name
                    or getattr(response_format.json_schema, "name", "unnamed"),
                )

            response = await self._processor.process_request(ctx, domain_request)
            if asyncio.iscoroutine(response):
                response = await response

            if isinstance(response, StreamingResponseEnvelope):
                if response.content is None:
                    raise ValueError("StreamingResponseEnvelope has no content")
                rid_ws = f"resp_ws_{int(time.time())}_{id(response)}"
                renderer = ResponsesWireRenderer(
                    self._responses_session_store,
                    transport="websocket",
                    realtime_websocket_terminal=True,
                )
                normalizer = ResponsesEventNormalizer(
                    source=stream_source,
                    response_id=rid_ws,
                )
                events = normalizer.normalize(response.content)
                async for frame in renderer.render(
                    events,
                    rid_ws,
                    instructions=(
                        store_instructions
                        if isinstance(store_instructions, str)
                        else None
                    ),
                    history_items=cast(
                        list[ResponsesHistoryItem] | None,
                        ctx.extensions.get("responses_store_history_items"),
                    ),
                ):
                    if isinstance(frame, dict):
                        await websocket.send_json(frame)
                    else:
                        await websocket.send_text(str(frame))

            else:
                converted_content: object
                if stream_source is ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS:
                    converted_content = self._chat_completion_to_responses_object(
                        response.content,
                        responses_model=responses_domain.model,
                        envelope=response,
                    )
                else:
                    converted_content = self._ensure_responses_schema_content(
                        response.content,
                        request_id=request_id,
                        envelope=response,
                        responses_model=responses_domain.model,
                    )
                content = converted_content
                payload_to_store: dict[str, Any] | None = None
                if isinstance(content, dict):
                    payload_to_store = content
                else:
                    from pydantic import BaseModel

                    if isinstance(content, BaseModel):
                        with contextlib.suppress(Exception):
                            payload_to_store = content.model_dump()

                if payload_to_store is None:
                    raise ResponsesProtocolError(
                        "WebSocket response.done requires a JSON object response body",
                        code="invalid_response_shape",
                        param=None,
                        status_code=502,
                        error_type="api_error",
                    )

                done_event = {
                    "type": "response.done",
                    "request_id": request_id,
                    "response": payload_to_store,
                }
                await self._store_completed_responses_payload(
                    payload_to_store,
                    instructions=(
                        store_instructions
                        if isinstance(store_instructions, str)
                        else None
                    ),
                    history_items=cast(
                        list[ResponsesHistoryItem] | None,
                        ctx.extensions.get("responses_store_history_items"),
                    ),
                )
                await websocket.send_json(done_event)

                if self._wire_capture and self._wire_capture.enabled():
                    try:
                        outbound_bytes = json.dumps(
                            payload_to_store,
                            separators=(",", ":"),
                            ensure_ascii=False,
                        ).encode("utf-8")
                        _cap_model = payload_to_store.get("model")
                        if not isinstance(_cap_model, str) or not _cap_model.strip():
                            _cap_model = getattr(responses_request, "model", None)
                        await self._wire_capture.capture_outbound_response(
                            context=ctx,
                            session_id=None,
                            backend=None,
                            model=_cap_model,
                            key_name=None,
                            response_content=outbound_bytes,
                            capture_metadata={
                                "transport": "websocket",
                                "protocol_event": "frame",
                                "websocket_message_type": "text",
                                "event_type": "response.done",
                                "request_id": request_id,
                            },
                        )
                    except Exception as e:
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Failed to capture WebSocket outbound event: %s",
                                e,
                                exc_info=True,
                            )

        except ValidationError as exc:
            error_event = {
                "type": "error",
                "request_id": request_id,
                "status": 422,
                "error": {
                    "code": "invalid_request",
                    "message": "Invalid request format",
                    "details": exc.errors(),
                },
            }
            await websocket.send_json(error_event)
        except HTTPException as exc:
            detail: dict[str, Any] = exc.detail if isinstance(exc.detail, dict) else {}
            raw_error_body = detail.get("error")
            error_body: dict[str, Any] = (
                raw_error_body if isinstance(raw_error_body, dict) else {}
            )
            error_code = error_body.get("code")
            error_message = error_body.get("message")
            error_type = error_body.get("type")
            param = error_body.get("param")
            ws_error_body: dict[str, Any] = {
                "code": str(error_code or "invalid_request"),
                "message": str(error_message or "Invalid request format"),
            }
            if isinstance(error_type, str) and error_type:
                ws_error_body["type"] = error_type
            if isinstance(param, str) and param:
                ws_error_body["param"] = param
            error_event = {
                "type": "error",
                "request_id": request_id,
                "status": int(exc.status_code),
                "error": ws_error_body,
            }
            await websocket.send_json(error_event)
        except ResponsesProtocolError as e:
            status_ws = int(getattr(e, "status_code", 400) or 400)
            err_type = str(getattr(e, "error_type", "invalid_request_error"))
            err_code = str(getattr(e, "code", "invalid_request_error"))
            error_event = {
                "type": "error",
                "request_id": request_id,
                "status": status_ws,
                "error": {
                    "type": err_type,
                    "code": err_code,
                    "message": str(getattr(e, "message", str(e))),
                },
            }
            param = getattr(e, "param", None)
            err_body = error_event["error"]
            if param is not None and isinstance(err_body, dict):
                err_body["param"] = param
            await websocket.send_json(error_event)
        except LLMProxyError as e:
            error_event = {
                "type": "error",
                "request_id": request_id,
                "status": 500,
                "error": {
                    "code": "proxy_error",
                    "message": str(e),
                },
            }
            await websocket.send_json(error_event)
        except Exception as e:
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Error processing WebSocket response.create - request_id=%s: %s",
                    request_id,
                    e,
                    exc_info=True,
                )
            error_event = {
                "type": "error",
                "request_id": request_id,
                "status": 500,
                "error": {
                    "code": "internal_error",
                    "message": "An internal error occurred",
                },
            }
            await websocket.send_json(error_event)


def _build_responses_controller_dependencies(
    service_provider: IServiceProvider,
) -> dict[str, Any]:
    from src.core.interfaces.wire_capture_interface import IWireCapture
    from src.core.services.request_processor_service import RequestProcessor
    from src.core.services.translation_service import TranslationService

    request_processor = service_provider.get_service(
        cast(type, IRequestProcessor)
    )  # type: ignore[type-abstract]
    if request_processor is None:
        request_processor = service_provider.get_service(RequestProcessor)
    if request_processor is None:
        raise InitializationError(
            "RequestProcessor is not registered in the service provider",
        )

    translation_service = service_provider.get_service(
        cast(type, ITranslationService)
    )  # type: ignore[type-abstract]
    if translation_service is None:
        translation_service = service_provider.get_service(TranslationService)
    if translation_service is None:
        raise InitializationError(
            "TranslationService is not registered in the service provider",
        )

    wire_capture = None
    try:
        wire_capture = service_provider.get_service(cast(type, IWireCapture))
    except (KeyError, AttributeError) as e:
        logger.debug("Wire capture service not available in DI: %s", e, exc_info=True)
    except Exception as e:
        logger.warning(
            "Unexpected error getting wire capture service from DI: %s",
            e,
            exc_info=True,
        )

    client_eos_service = None
    try:
        from src.core.interfaces.client_end_of_session_service_interface import (
            IClientEndOfSessionService,
        )

        client_eos_service = service_provider.get_service(
            cast(type, IClientEndOfSessionService)
        )
    except (KeyError, AttributeError) as e:
        logger.debug(
            "Client end-of-session service not available in DI: %s",
            e,
            exc_info=True,
        )
    except Exception as e:
        logger.warning(
            "Unexpected error getting client end-of-session service from DI: %s",
            e,
            exc_info=True,
        )

    from src.core.interfaces.responses_session_store_interface import (
        IResponsesSessionStore,
    )
    from src.core.services.in_memory_responses_session_store import (
        InMemoryResponsesSessionStore,
    )

    responses_session_store = service_provider.get_service(
        cast(type, IResponsesSessionStore)
    )
    if responses_session_store is None:
        responses_session_store = service_provider.get_service(
            InMemoryResponsesSessionStore
        )
    if responses_session_store is None:
        raise InitializationError(
            "IResponsesSessionStore is not registered in the service provider",
        )

    backend_model_resolver: IBackendModelResolver = (
        service_provider.get_required_service(cast(type, IBackendModelResolver))
    )
    openai_responses_projector = service_provider.get_required_service(
        OpenAIResponsesProjector
    )
    anthropic_responses_projector = service_provider.get_required_service(
        AnthropicResponsesProjector
    )
    gemini_responses_projector = service_provider.get_required_service(
        GeminiResponsesProjector
    )

    backend_config_provider: IBackendConfigProvider | None = None
    try:
        backend_config_provider = service_provider.get_service(
            cast(type, IBackendConfigProvider)
        )
    except (KeyError, AttributeError) as e:
        logger.debug(
            "Backend config provider not available in DI: %s", e, exc_info=True
        )
    except Exception as e:
        logger.warning(
            "Unexpected error getting backend config provider from DI: %s",
            e,
            exc_info=True,
        )

    metrics_initializer = None
    try:
        from src.core.interfaces.session_metrics_initializer_interface import (
            ISessionMetricsInitializer,
        )

        metrics_initializer = service_provider.get_service(
            cast(type, ISessionMetricsInitializer)
        )
    except (KeyError, AttributeError) as e:
        logger.debug(
            "Session metrics initializer not available in DI: %s",
            e,
            exc_info=True,
        )
    except Exception as e:
        logger.warning(
            "Unexpected error getting session metrics initializer from DI: %s",
            e,
            exc_info=True,
        )

    return {
        "request_processor": request_processor,
        "translation_service": translation_service,
        "wire_capture": wire_capture,
        "client_eos_service": client_eos_service,
        "metrics_initializer": metrics_initializer,
        "responses_session_store": responses_session_store,
        "backend_model_resolver": backend_model_resolver,
        "openai_responses_projector": openai_responses_projector,
        "anthropic_responses_projector": anthropic_responses_projector,
        "gemini_responses_projector": gemini_responses_projector,
        "backend_config_provider": backend_config_provider,
    }


def get_responses_controller(service_provider: IServiceProvider) -> ResponsesController:
    """Create a responses controller using the service provider.

    Args:
        service_provider: The service provider to use

    Returns:
        A configured responses controller

    Raises:
        InitializationError: If required dependencies are missing or construction fails
    """
    try:
        return ResponsesController(
            **_build_responses_controller_dependencies(service_provider)
        )
    except InitializationError:
        raise
    except Exception as e:
        raise InitializationError(f"Failed to create ResponsesController: {e}") from e
