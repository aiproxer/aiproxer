from __future__ import annotations

import logging
from typing import Any

from src.core.domain.chat import (
    CanonicalChatResponse,
    ChatCompletionChoice,
    ChatCompletionChoiceMessage,
    ChatResponse,
    FunctionCall,
    ToolCall,
)

logger = logging.getLogger(__name__)
from src.core.domain.translation_utils.content_utils import coerce_reasoning_text
from src.core.domain.translation_utils.tool_utils import normalize_tool_arguments
from src.core.domain.translation_utils.usage_utils import normalize_usage_metadata
from src.core.domain.translators.openai.response import openai_to_domain_response
from src.core.domain.usage_summary import UsageSummary


def responses_to_domain_response(response: Any) -> CanonicalChatResponse:
    """Translate an OpenAI Responses API response to a canonical response."""
    import time

    if not isinstance(response, dict):
        return openai_to_domain_response(response)

    if response.get("choices") and not response.get("output"):
        return openai_to_domain_response(response)

    output_items = response.get("output") or []
    choices: list[ChatCompletionChoice] = []

    for idx, item in enumerate(output_items):
        if not isinstance(item, dict):
            continue
        # Reasoning is a separate Responses output item, not an assistant choice.
        # An empty choice here hides the subsequent message in chat consumers.
        if item.get("type") == "reasoning":
            continue

        role = item.get("role", "assistant")
        content_parts = item.get("content")
        if not isinstance(content_parts, list):
            content_parts = []

        text_segments: list[str] = []
        tool_calls: list[ToolCall] = []
        reasoning_segments: list[str] = []

        for part in content_parts:
            if not isinstance(part, dict):
                continue

            part_type = part.get("type")
            if part_type in {"output_text", "text", "input_text"}:
                text_value = part.get("text") or part.get("value") or ""
                if text_value:
                    text_segments.append(str(text_value))
            elif part_type in {"reasoning", "thinking", "assistant_reasoning"}:
                reasoning_value = part.get("text") or part.get("value")
                normalized_reasoning = coerce_reasoning_text(reasoning_value)
                if normalized_reasoning:
                    reasoning_segments.append(normalized_reasoning)
            elif part_type == "tool_call":
                function_payload = (
                    part.get("function") or part.get("function_call") or {}
                )
                normalized_args = normalize_tool_arguments(
                    function_payload.get("arguments")
                    or function_payload.get("args")
                    or function_payload.get("arguments_json")
                )
                tool_calls.append(
                    ToolCall(
                        id=part.get("id") or f"tool_call_{idx}_{len(tool_calls)}",
                        function=FunctionCall(
                            name=function_payload.get("name", ""),
                            arguments=normalized_args,
                        ),
                    )
                )
            else:
                metadata = part.get("metadata") or {}
                for key in ("thought", "thinking", "reasoning"):
                    if metadata.get(key):
                        normalized_reasoning = coerce_reasoning_text(metadata[key])
                        if normalized_reasoning:
                            reasoning_segments.append(normalized_reasoning)
                            break

        content_text = "\n".join(
            segment for segment in text_segments if segment
        ).strip()
        reasoning_text = "\n".join(
            segment for segment in reasoning_segments if segment
        ).strip()

        finish_reason = item.get("finish_reason") or item.get("status")
        if finish_reason == "completed":
            finish_reason = "stop"
        elif finish_reason == "incomplete":
            finish_reason = "length"
        elif finish_reason in {"in_progress", "generating"}:
            finish_reason = None
        elif finish_reason is None and (content_text or tool_calls):
            finish_reason = "stop"

        message = ChatCompletionChoiceMessage(
            role=role,
            content=content_text or None,
            reasoning_content=reasoning_text or None,
            tool_calls=tool_calls or None,
        )

        choices.append(
            ChatCompletionChoice(
                index=len(choices),
                message=message,
                finish_reason=finish_reason,
            )
        )

    if not choices:
        output_text = response.get("output_text")
        fallback_text_segments: list[str] = []
        if isinstance(output_text, list):
            fallback_text_segments = [
                str(segment) for segment in output_text if segment
            ]
        elif isinstance(output_text, str) and output_text:
            fallback_text_segments = [output_text]

        if fallback_text_segments:
            aggregated_text = "".join(fallback_text_segments)
            status = response.get("status")
            fallback_finish_reason: str | None
            if status == "completed":
                fallback_finish_reason = "stop"
            elif status == "incomplete":
                fallback_finish_reason = "length"
            elif status in {"in_progress", "generating"}:
                fallback_finish_reason = None
            else:
                fallback_finish_reason = "stop" if aggregated_text else None

            message = ChatCompletionChoiceMessage(
                role="assistant",
                content=aggregated_text,
                tool_calls=None,
            )

            choices.append(
                ChatCompletionChoice(
                    index=0,
                    message=message,
                    finish_reason=fallback_finish_reason,
                )
            )

    if not choices:
        return openai_to_domain_response(response)

    usage = response.get("usage") or {}
    normalized_usage = normalize_usage_metadata(usage, "openai-responses")

    return CanonicalChatResponse(
        id=response.get("id", f"resp-{int(time.time())}"),
        object=response.get("object", "response"),
        created=response.get("created", int(time.time())),
        model=response.get("model", "unknown"),
        choices=choices,
        usage=UsageSummary.from_dict(normalized_usage) if normalized_usage else None,
        system_fingerprint=response.get("system_fingerprint"),
    )


def from_domain_to_responses_response(response: ChatResponse) -> dict[str, Any]:
    """
    Translate a domain ChatResponse to a Responses API response format.

    This method converts the internal domain response to the OpenAI Responses API format,
    including parsing structured outputs and handling JSON schema validation results.
    """
    import json
    import time

    choices = []
    output_items: list[dict[str, Any]] = []
    aggregated_output_text: list[str | None] = []

    def _map_finish_reason_to_status(finish_reason: str | None) -> str:
        if finish_reason in (None, "", "stop"):
            return "completed"
        if finish_reason == "length":
            return "incomplete"
        if finish_reason in {"tool_calls", "function_call"}:
            return "requires_action"
        if finish_reason == "content_filter":
            return "blocked"
        return "completed"

    for choice in response.choices:
        if choice.message:
            parsed_content = None
            raw_content = choice.message.content or ""

            cleaned_content = raw_content.strip()

            if cleaned_content.startswith("```json") and cleaned_content.endswith(
                "```"
            ):
                cleaned_content = cleaned_content[7:-3].strip()
            elif cleaned_content.startswith("```") and cleaned_content.endswith("```"):
                cleaned_content = cleaned_content[3:-3].strip()

            if cleaned_content:
                try:
                    parsed_content = json.loads(cleaned_content)
                    raw_content = cleaned_content
                except json.JSONDecodeError:
                    try:
                        import re

                        json_pattern = r"\{.*\}"
                        json_match = re.search(json_pattern, cleaned_content, re.DOTALL)
                        if json_match:
                            potential_json = json_match.group(0)
                            parsed_content = json.loads(potential_json)
                            raw_content = potential_json
                    except (json.JSONDecodeError, AttributeError) as e:
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                "Failed to parse JSON from content in Responses API translation: %s",
                                e,
                                exc_info=True,
                            )

            message_payload: dict[str, Any] = {
                "role": choice.message.role,
                "content": raw_content or None,
                "parsed": parsed_content,
            }

            tool_calls_payload: list[dict[str, Any]] = []
            if choice.message.tool_calls:
                for tool_call in choice.message.tool_calls:
                    if hasattr(tool_call, "model_dump"):
                        tool_data = tool_call.model_dump()
                    elif isinstance(tool_call, dict):
                        tool_data = dict(tool_call)
                    else:
                        function = getattr(tool_call, "function", None)
                        tool_data = {
                            "id": getattr(tool_call, "id", ""),
                            "type": getattr(tool_call, "type", "function"),
                            "function": {
                                "name": getattr(function, "name", ""),
                                "arguments": getattr(function, "arguments", "{}"),
                            },
                        }

                    function_payload = tool_data.get("function")
                    if isinstance(function_payload, dict):
                        arguments = function_payload.get("arguments")
                        if isinstance(arguments, dict | list):
                            function_payload["arguments"] = json.dumps(arguments)
                        elif arguments is None:
                            function_payload["arguments"] = "{}"

                    tool_calls_payload.append(tool_data)

            if tool_calls_payload:
                message_payload["tool_calls"] = tool_calls_payload

            response_choice = {
                "index": choice.index,
                "message": message_payload,
                "finish_reason": choice.finish_reason or "stop",
            }
            choices.append(response_choice)

            text_value = (
                message_payload.get("content")
                if isinstance(message_payload.get("content"), str)
                else None
            )
            if text_value:
                aggregated_output_text.append(text_value)
            else:
                aggregated_output_text.append(None)

            output_content_parts: list[dict[str, Any]] = []

            if text_value:
                output_content_parts.append({"type": "output_text", "text": text_value})

            if tool_calls_payload:
                for tool_call_payload in tool_calls_payload:
                    tool_call_dict: dict[str, Any] = (
                        tool_call_payload if isinstance(tool_call_payload, dict) else {}
                    )
                    output_content_parts.append(
                        {
                            "type": "tool_call",
                            "id": tool_call_dict.get("id", ""),
                            "function": tool_call_dict.get("function", {}),
                        }
                    )

            output_item = {
                "id": f"msg-{response.id}-{choice.index}",
                "type": "message",
                "role": choice.message.role,
                "status": _map_finish_reason_to_status(choice.finish_reason),
                "content": output_content_parts,
            }

            if choice.finish_reason:
                output_item["finish_reason"] = choice.finish_reason

            output_items.append(output_item)

    responses_response: dict[str, Any] = {
        "id": response.id,
        "object": "response",
        "created": response.created or int(time.time()),
        "model": response.model,
        "choices": choices,
    }

    if output_items:
        responses_response["output"] = output_items

        text_values = [text for text in aggregated_output_text if text is not None]
        if text_values:
            responses_response["output_text"] = [
                text if text is not None else "" for text in aggregated_output_text
            ]

    if response.usage:
        responses_response["usage"] = response.usage

    if hasattr(response, "system_fingerprint") and response.system_fingerprint:
        responses_response["system_fingerprint"] = response.system_fingerprint

    if hasattr(response, "service_tier") and response.service_tier:
        responses_response["service_tier"] = response.service_tier

    return responses_response
