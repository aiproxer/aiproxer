from __future__ import annotations

import logging
from typing import Any

from src.core.domain.chat import (
    CanonicalStreamChunk,
    StreamingChatCompletionChoice,
    StreamingChatCompletionChoiceDelta,
)
from src.core.domain.translation_utils.content_utils import coerce_reasoning_text
from src.core.domain.translation_utils.openai_compat_ids import (
    coerce_openai_completion_id,
    sanitize_openai_chunk_tool_call_ids_inplace,
)

logger = logging.getLogger(__name__)


def _int_created_fallback(created_raw: Any) -> int | None:
    if isinstance(created_raw, int) and not isinstance(created_raw, bool):
        return created_raw
    if isinstance(created_raw, float) and created_raw.is_integer():
        return int(created_raw)
    return None


def openai_to_domain_stream_chunk(chunk: Any) -> CanonicalStreamChunk | dict[str, Any]:
    """Translate an OpenAI streaming chunk to a canonical CanonicalStreamChunk object."""
    import json
    import time
    import uuid

    if isinstance(chunk, bytes | bytearray):
        try:
            chunk = chunk.decode("utf-8")
        except UnicodeDecodeError:
            logger.warning(
                "Failed to decode bytes chunk in openai_to_domain_stream_chunk",
                exc_info=True,
            )
            return {"error": "Invalid chunk format: unable to decode bytes"}

    if isinstance(chunk, str):
        stripped_chunk = chunk.strip()

        if not stripped_chunk:
            delta = StreamingChatCompletionChoiceDelta()
            choice = StreamingChatCompletionChoice(
                index=0, delta=delta, finish_reason=None
            )
            return CanonicalStreamChunk(
                id=f"chatcmpl-{uuid.uuid4().hex[:16]}",
                object="chat.completion.chunk",
                created=int(time.time()),
                model="unknown",
                choices=[choice],
            )

        if stripped_chunk.startswith(":"):
            delta = StreamingChatCompletionChoiceDelta()
            choice = StreamingChatCompletionChoice(
                index=0, delta=delta, finish_reason=None
            )
            return CanonicalStreamChunk(
                id=f"chatcmpl-{uuid.uuid4().hex[:16]}",
                object="chat.completion.chunk",
                created=int(time.time()),
                model="unknown",
                choices=[choice],
            )

        if stripped_chunk.startswith("data:"):
            stripped_chunk = stripped_chunk[5:].strip()

        if stripped_chunk == "[DONE]":
            delta = StreamingChatCompletionChoiceDelta()
            choice = StreamingChatCompletionChoice(
                index=0, delta=delta, finish_reason="stop"
            )
            return CanonicalStreamChunk(
                id=f"chatcmpl-{uuid.uuid4().hex[:16]}",
                object="chat.completion.chunk",
                created=int(time.time()),
                model="unknown",
                choices=[choice],
            )

        try:
            chunk = json.loads(stripped_chunk)
        except json.JSONDecodeError as exc:
            logger.warning(
                "Responses stream chunk JSON decode failed: %s",
                stripped_chunk[:300],
                exc_info=True,
            )
            return {
                "error": "Invalid chunk format: expected JSON after 'data:' prefix",
                "details": {"message": str(exc)},
            }

    if not isinstance(chunk, dict):
        return {"error": "Invalid chunk format: expected a dictionary"}

    if "choices" not in chunk or not isinstance(chunk.get("choices"), list):
        return {"error": "Invalid chunk: missing 'choices'"}

    sanitize_openai_chunk_tool_call_ids_inplace(chunk)

    choices = chunk.get("choices")
    if isinstance(choices, list):
        for choice in choices:
            if not isinstance(choice, dict):
                continue
            delta = choice.get("delta")
            if not isinstance(delta, dict):
                continue

            normalized_reasoning = None
            if "reasoning_content" in delta:
                normalized_reasoning = coerce_reasoning_text(
                    delta.get("reasoning_content")
                )
            if not normalized_reasoning and "reasoning" in delta:
                normalized_reasoning = coerce_reasoning_text(delta.get("reasoning"))
            if not normalized_reasoning and "thinking" in delta:
                normalized_reasoning = coerce_reasoning_text(delta.get("thinking"))
            if not normalized_reasoning and "thought" in delta:
                normalized_reasoning = coerce_reasoning_text(delta.get("thought"))
            if not normalized_reasoning and "reasoning_summary" in delta:
                normalized_reasoning = coerce_reasoning_text(
                    delta.get("reasoning_summary")
                )
            if not normalized_reasoning and isinstance(delta.get("metadata"), dict):
                normalized_reasoning = coerce_reasoning_text(
                    delta["metadata"].get("reasoning")
                )

            if normalized_reasoning:
                delta["reasoning_content"] = normalized_reasoning
                delta.setdefault("reasoning", normalized_reasoning)

    try:
        canonical_choices = []
        for choice_dict in chunk.get("choices", []):
            if isinstance(choice_dict, dict):
                delta_dict = choice_dict.get("delta", {})
                if not isinstance(delta_dict, dict):
                    delta_dict = {}

                delta_obj = StreamingChatCompletionChoiceDelta(**delta_dict)
                choice_obj = StreamingChatCompletionChoice(
                    index=choice_dict.get("index", 0),
                    delta=delta_obj,
                    finish_reason=choice_dict.get("finish_reason"),
                    logprobs=choice_dict.get("logprobs"),
                )
                canonical_choices.append(choice_obj)

        coerced_id = coerce_openai_completion_id(
            chunk.get("id"),
            created_fallback=_int_created_fallback(chunk.get("created")),
        )
        return CanonicalStreamChunk(
            id=coerced_id,
            object=chunk.get("object", "chat.completion.chunk"),
            created=chunk.get("created"),
            model=chunk.get("model"),
            choices=canonical_choices,
            usage=chunk.get("usage"),
            system_fingerprint=chunk.get("system_fingerprint"),
        )
    except Exception as exc:
        logger.warning(
            "Failed to convert OpenAI chunk to CanonicalStreamChunk: %s",
            exc,
            exc_info=True,
        )
        return {"error": f"Failed to convert chunk: {exc}"}


def from_domain_to_openai_stream_chunk(chunk: Any) -> dict[str, Any]:
    """Translate a domain stream chunk to the canonical OpenAI SSE format."""
    if isinstance(chunk, dict):
        chunk_dict = chunk
    else:
        dumped = getattr(chunk, "model_dump", lambda: None)()
        if isinstance(dumped, dict):
            chunk_dict = dumped
        else:
            chunk_dict = {
                "id": getattr(chunk, "id", "chatcmpl-stream"),
                "object": getattr(chunk, "object", "chat.completion.chunk"),
                "created": getattr(chunk, "created", int(__import__("time").time())),
                "model": getattr(chunk, "model", "unknown"),
                "choices": [
                    {
                        "index": 0,
                        "delta": getattr(chunk, "delta", {}) or {},
                        "finish_reason": getattr(chunk, "finish_reason", None),
                    }
                ],
            }

    raw_choices = chunk_dict.get("choices")
    choices: list[dict[str, Any]] = []
    if isinstance(raw_choices, list):
        choices = [c for c in raw_choices if isinstance(c, dict)]

    if not choices:
        choices = [
            {
                "index": 0,
                "delta": getattr(chunk, "delta", {}) or {},
                "finish_reason": getattr(chunk, "finish_reason", None),
            }
        ]

    first_choice = choices[0]
    delta = first_choice.get("delta") or {}
    tool_call_text = None
    if isinstance(delta, dict) and "_tool_call_text" in delta:
        tool_call_text = delta.get("_tool_call_text")
        delta = dict(delta)
        delta.pop("_tool_call_text", None)

    tool_calls = delta.get("tool_calls") if isinstance(delta, dict) else None
    if tool_calls:
        delta = dict(delta)
        delta["tool_calls"] = tool_calls
        if tool_call_text is not None:
            delta["content"] = tool_call_text
        else:
            delta.pop("content", None)
    else:
        content = delta.get("content")
        if content is None:
            content = getattr(chunk, "content", None)
        if content is not None:
            delta = dict(delta)
            delta["content"] = content
        if tool_call_text is not None:
            delta["content"] = tool_call_text

    # Ensure reasoning aliases are present for compatibility
    if delta.get("reasoning_content"):
        reasoning = delta["reasoning_content"]
        if not delta.get("reasoning"):
            delta["reasoning"] = reasoning
        if not delta.get("thinking"):
            delta["thinking"] = reasoning
        if not delta.get("thought"):
            delta["thought"] = reasoning

    normalized_choice = {
        "index": first_choice.get("index", 0),
        "delta": delta,
        "finish_reason": first_choice.get(
            "finish_reason", getattr(chunk, "finish_reason", None)
        ),
    }

    raw_top_id = chunk_dict.get("id")
    if raw_top_id is None and not isinstance(chunk, dict):
        raw_top_id = getattr(chunk, "id", None)
    outbound_id = coerce_openai_completion_id(
        raw_top_id,
        created_fallback=_int_created_fallback(chunk_dict.get("created")),
    )

    return {
        "id": outbound_id,
        "object": chunk_dict.get(
            "object", getattr(chunk, "object", "chat.completion.chunk")
        ),
        "created": chunk_dict.get(
            "created",
            getattr(chunk, "created", int(__import__("time").time())),
        ),
        "model": chunk_dict.get("model", getattr(chunk, "model", "unknown")),
        "choices": [normalized_choice],
    }
