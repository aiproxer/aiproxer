"""
Core-level SSE serializer for streaming content.

This module contains the canonical SSE serialization logic used by core layers.
Transport modules re-export this serializer for backward compatibility.
"""

from __future__ import annotations

import base64
import json
import logging
import time
from typing import Any, cast

from src.core.common.sse_serializer_utils import get_first_delta
from src.core.domain.streaming.contracts import StreamingChunk
from src.core.domain.streaming.sentinels import SentinelManager
from src.core.domain.streaming.stop_chunk_with_usage import (
    StopChunkWithUsage,
    UsageChunkLeakError,
)
from src.core.domain.streaming.streaming_content import (
    _STREAMING_TYPED_STOP_WITH_USAGE_MARKER,
    StreamingContent,
)
from src.core.domain.translation_utils.openai_compat_ids import (
    coerce_openai_completion_id,
    normalize_tool_call_dict_id_inplace,
    sanitize_openai_chunk_delta_inplace,
    sanitize_openai_compatible_sse_payload_inplace,
)

logger = logging.getLogger(__name__)


def _tool_call_dicts_have_meaningful_arguments(
    tool_calls: list[dict[str, Any]],
) -> bool:
    """True if any tool call carries non-empty JSON/object arguments (not ``{}``)."""
    for tc in tool_calls:
        fn = tc.get("function")
        if not isinstance(fn, dict):
            continue
        args = fn.get("arguments")
        if args is None:
            continue
        if isinstance(args, str):
            s = args.strip()
            if not s or s == "{}":
                continue
            try:
                obj = json.loads(s)
                if obj in (None, {}, []):
                    continue
            except json.JSONDecodeError:
                return True
            return True
        if isinstance(args, dict | list) and args:
            return True
    return False


class SSESerializer:
    """Serializer for converting StreamingContent to SSE bytes."""

    _REASONING_DELTA_KEYS: tuple[str, ...] = (
        "reasoning_content",
        "reasoning",
        "thinking",
        "thought",
    )

    @classmethod
    def _coerce_and_strip_reasoning_fields_in_container(
        cls,
        container: dict[str, Any],
        *,
        keep_reasoning_content: bool,
        coerce_reasoning_into_content: bool,
    ) -> None:
        """Handle non-standard reasoning keys in OpenAI delta/message containers.

        Behavior depends on the caller:
        - When keep_reasoning_content=False: strip all reasoning keys after optionally
          copying the first available reasoning field into `content`.
        - When keep_reasoning_content=True: normalize to the canonical
          `reasoning_content` key, strip only aliases, and still mirror into `content`
          when `content` is empty.
        """
        content_val = container.get("content")
        has_content = content_val is not None and str(content_val) != ""

        reasoning_val = None
        canonical_reasoning = container.get("reasoning_content")
        if isinstance(canonical_reasoning, str) and canonical_reasoning:
            reasoning_val = canonical_reasoning
        else:
            for k in ("reasoning", "thinking", "thought"):
                v = container.get(k)
                if isinstance(v, str) and v:
                    reasoning_val = v
                    break

        if keep_reasoning_content:
            if isinstance(reasoning_val, str) and reasoning_val:
                container.setdefault("reasoning_content", reasoning_val)
            # Strip aliases only.
            for k in ("reasoning", "thinking", "thought"):
                container.pop(k, None)
        else:
            if (
                coerce_reasoning_into_content
                and not has_content
                and isinstance(reasoning_val, str)
                and reasoning_val
            ):
                # Coerce reasoning output into standard `content`.
                container["content"] = reasoning_val
            for k in cls._REASONING_DELTA_KEYS:
                container.pop(k, None)

    @classmethod
    def _sanitize_reasoning_fields_in_openai_payload(
        cls,
        payload: dict[str, Any],
        *,
        keep_reasoning_content: bool,
        coerce_reasoning_into_content: bool,
    ) -> None:
        choices = payload.get("choices")
        if not isinstance(choices, list):
            return
        for choice in choices:
            if not isinstance(choice, dict):
                continue
            for container_key in ("delta", "message"):
                container = choice.get(container_key)
                if isinstance(container, dict):
                    cls._coerce_and_strip_reasoning_fields_in_container(
                        container,
                        keep_reasoning_content=keep_reasoning_content,
                        coerce_reasoning_into_content=coerce_reasoning_into_content,
                    )

    @staticmethod
    def _ensure_openai_finish_reason_for_terminal_usage(
        content_copy: dict[str, Any], chunk: StreamingChunk
    ) -> None:
        """Ensure OpenAI streaming chunks carrying usage include a finish_reason.

        Some upstream providers send a terminal chunk with `usage` but omit
        `choices[].finish_reason` (leave it `null`). Several OpenAI-compatible
        clients use finish_reason to decide whether to dispatch tool calls or
        consider a turn complete. When finish_reason is missing, the client may
        continue looping even though the SSE stream is properly terminated.
        """
        # Only infer/force finish_reason when this chunk *actually* carries usage.
        # Some providers include `"usage": null` on every streaming chunk. Treat
        # that as "no usage" to avoid prematurely marking a non-terminal chunk as
        # finished (clients may stop reading after seeing finish_reason="stop").
        usage_from_payload = (
            content_copy.get("usage") if "usage" in content_copy else None
        )
        has_usage_payload = isinstance(usage_from_payload, dict)
        if not has_usage_payload and not chunk.metadata.usage:
            return

        choices = content_copy.get("choices")
        if not isinstance(choices, list) or not choices:
            return

        # Derive a reasonable default if upstream didn't provide one.
        inferred: str | None = chunk.metadata.finish_reason
        if inferred is None:
            inferred = "stop"
            first_choice = choices[0] if isinstance(choices[0], dict) else None
            if isinstance(first_choice, dict):
                delta = first_choice.get("delta")
                if isinstance(delta, dict):
                    tool_calls = delta.get("tool_calls")
                    if isinstance(tool_calls, list) and tool_calls:
                        inferred = "tool_calls"

        for choice in choices:
            if not isinstance(choice, dict):
                continue
            if choice.get("finish_reason") is None:
                choice["finish_reason"] = inferred

    def _serialize_stop_chunk_with_usage(self, content: StreamingContent) -> bytes:
        """Serialize StopChunkWithUsage to SSE bytes with usage at top level."""
        assert isinstance(content.content, StopChunkWithUsage)
        # Type hint for content.content as StopChunkWithUsage is already asserted
        plain_dict = dict(content.content)
        plain_dict.pop(_STREAMING_TYPED_STOP_WITH_USAGE_MARKER, None)
        try:
            chunk = content.to_typed_chunk()
            self._ensure_openai_finish_reason_for_terminal_usage(plain_dict, chunk)
        except Exception:
            # Best-effort: usage chunks should never fail serialization.
            pass
        canonical_tool_calls = self._serialize_canonical_tool_calls_terminal_payload(
            plain_dict
        )
        if canonical_tool_calls is not None:
            return canonical_tool_calls
        # StopChunkWithUsage is the explicit terminal usage carrier: always one SSE
        # frame with ``usage`` at the top level (OpenRouter-style). Legacy split
        # usage events apply only to plain dict payloads via
        # ``_serialize_openai_done_payload_with_optional_usage``.
        self._ensure_openai_compatible_top_level_id(plain_dict)
        return f"data: {json.dumps(plain_dict)}\n\ndata: [DONE]\n\n".encode()

    def _serialize_error_chunk(
        self, chunk: StreamingChunk, content: StreamingContent
    ) -> bytes | None:
        """Serialize error chunk to SSE bytes. Returns None if not an error chunk."""
        if chunk.metadata.finish_reason == "error" and (
            chunk.metadata.error is not None or "error" in content.metadata
        ):
            error_dict: Any = (
                chunk.metadata.error.model_dump(exclude_none=True)
                if chunk.metadata.error is not None
                else content.metadata.get("error", {})
            )
            # Pydantic error payloads are always dict-like here.
            created = (
                content.metadata.get("created")
                if isinstance(content.metadata.get("created"), int)
                else int(time.time())
            )
            model = (
                content.metadata.get("model")
                if isinstance(content.metadata.get("model"), str)
                else "unknown"
            )
            error_id = (
                content.metadata.get("id")
                if isinstance(content.metadata.get("id"), str)
                else f"chatcmpl-error-{created}"
            )

            error_data: dict[str, Any] = {
                "id": error_id,
                "object": "chat.completion.chunk",
                "created": created,
                "model": model,
                "choices": [
                    {
                        "index": 0,
                        "delta": {},
                        "finish_reason": "error",
                    }
                ],
                "error": error_dict,
            }
            return f"data: {json.dumps(error_data)}\n\ndata: [DONE]\n\n".encode()

        # Check for error in content if it's a dict
        if isinstance(content.content, dict) and content.content.get("error"):
            err_copy = dict(content.content)
            sanitize_openai_compatible_sse_payload_inplace(err_copy)
            return f"data: {json.dumps(err_copy)}\n\ndata: [DONE]\n\n".encode()
        return None

    def _serialize_cancellation_chunk(
        self, chunk: StreamingChunk, content: StreamingContent
    ) -> bytes | None:
        """Serialize cancellation chunk to SSE bytes. Returns None if not cancellation."""
        if chunk.is_cancellation and chunk.payload.kind != "empty":
            created = (
                content.metadata.get("created")
                if isinstance(content.metadata.get("created"), int)
                else int(time.time())
            )
            model = (
                content.metadata.get("model")
                if isinstance(content.metadata.get("model"), str)
                else "unknown"
            )
            chunk_id = (
                content.metadata.get("id")
                if isinstance(content.metadata.get("id"), str)
                else f"chatcmpl-cancel-{created}"
            )
            data: dict[str, Any] = {
                "id": chunk_id,
                "object": "chat.completion.chunk",
                "created": created,
                "model": model,
                "choices": [
                    {
                        "index": 0,
                        "delta": {"content": str(content.content)},
                        "finish_reason": "cancelled",
                    }
                ],
            }
            return f"data: {json.dumps(data)}\n\ndata: [DONE]\n\n".encode()
        return None

    def _serialize_done_chunk(
        self, chunk: StreamingChunk, content: StreamingContent
    ) -> bytes:
        """Serialize done chunk to SSE bytes (may include content or just [DONE])."""
        if self._is_terminal_error_like(chunk, content):
            # Defensive fallback: never collapse terminal errors into bare [DONE].
            error_dict = self._extract_error_payload(chunk, content)
            created = (
                content.metadata.get("created")
                if isinstance(content.metadata.get("created"), int)
                else int(time.time())
            )
            model = (
                content.metadata.get("model")
                if isinstance(content.metadata.get("model"), str)
                else "unknown"
            )
            error_id = (
                content.metadata.get("id")
                if isinstance(content.metadata.get("id"), str)
                else f"chatcmpl-error-{created}"
            )
            error_data: dict[str, Any] = {
                "id": error_id,
                "object": "chat.completion.chunk",
                "created": created,
                "model": model,
                "choices": [
                    {
                        "index": 0,
                        "delta": {},
                        "finish_reason": "error",
                    }
                ],
                "error": error_dict,
            }
            return f"data: {json.dumps(error_data)}\n\ndata: [DONE]\n\n".encode()

        if content._is_empty_completion_payload():  # type: ignore[attr-defined]
            return b"data: [DONE]\n\n"

        content_is_done_marker = (
            content.content == "[DONE]"
            or content.content == SentinelManager.DONE_MARKER
            or content.content == b"[DONE]"
        )
        if content.content and content.content != "" and not content_is_done_marker:
            if isinstance(content.content, dict) and "choices" in content.content:
                return self._serialize_openai_chunk_with_done(chunk, content)
            # For non-empty content that's not a dict with choices, serialize normally
            # but ensure [DONE] is added since chunk.is_done is True
            usage_payload = self._extract_usage_dict_for_legacy_openai(content)
            if usage_payload is not None:
                content_without_usage = StreamingContent(
                    content=content.content,
                    metadata=dict(content.metadata),
                    is_done=False,
                    is_empty=content.is_empty,
                    stream_id=content.stream_id,
                    is_cancellation=content.is_cancellation,
                    usage=None,
                )
                normal_bytes = self._serialize_normal_chunk(
                    content_without_usage.to_typed_chunk(), content_without_usage
                )
                usage_chunk = self._build_legacy_openai_usage_chunk(
                    content.metadata, usage_payload
                )
                return (
                    normal_bytes
                    + f"data: {json.dumps(usage_chunk)}\n\n".encode()
                    + b"data: [DONE]\n\n"
                )
            return self._serialize_normal_chunk(chunk, content)
        return b"data: [DONE]\n\n"

    @staticmethod
    def _extract_error_payload(
        chunk: StreamingChunk, content: StreamingContent
    ) -> dict[str, Any]:
        if chunk.metadata.error is not None:
            return chunk.metadata.error.model_dump(exclude_none=True)
        raw = content.metadata.get("error")
        if isinstance(raw, dict):
            return dict(raw)
        if raw is not None:
            return {"message": str(raw), "type": "api_error"}
        return {
            "message": "Upstream stream terminated with an error.",
            "type": "streaming_error",
        }

    @staticmethod
    def _is_terminal_error_like(
        chunk: StreamingChunk, content: StreamingContent
    ) -> bool:
        if chunk.metadata.finish_reason == "error":
            return True
        if chunk.metadata.error is not None:
            return True
        raw_error = content.metadata.get("error")
        if raw_error:
            return True
        return isinstance(content.content, dict) and bool(content.content.get("error"))

    def serialize(self, content: StreamingContent) -> bytes:
        """Serialize StreamingContent to SSE bytes.

        Args:
            content: The StreamingContent instance to serialize

        Returns:
            Bytes representation suitable for SSE streaming

        Raises:
            UsageChunkLeakError: If StopChunkWithUsage is incorrectly handled
        """
        if isinstance(content.content, StopChunkWithUsage):
            return self._serialize_stop_chunk_with_usage(content)
        chunk = content.to_typed_chunk()
        if chunk.is_done:
            error_bytes = self._serialize_error_chunk(chunk, content)
            if error_bytes is not None:
                return error_bytes
            cancellation_bytes = self._serialize_cancellation_chunk(chunk, content)
            if cancellation_bytes is not None:
                return cancellation_bytes
            return self._serialize_done_chunk(chunk, content)
        return self._serialize_normal_chunk(chunk, content)

    def _normalize_openai_chat_completion_to_stream_chunk(
        self, payload: dict[str, Any]
    ) -> dict[str, Any]:
        """Normalize `choices[].message` payloads into `choices[].delta` for SSE."""
        choices = payload.get("choices")
        if not isinstance(choices, list) or not choices:
            return payload

        normalized_choices: list[Any] = []
        converted_any = False

        for choice in choices:
            if not isinstance(choice, dict):
                normalized_choices.append(choice)
                continue

            if "delta" in choice:
                normalized_choices.append(choice)
                continue

            message = choice.get("message")
            if isinstance(message, dict):
                new_choice: dict[str, Any] = {
                    k: v for k, v in choice.items() if k != "message"
                }
                delta: dict[str, Any] = dict(message)
                if delta.get("content") is None:
                    delta["content"] = ""
                new_choice["delta"] = delta
                normalized_choices.append(new_choice)
                converted_any = True
            else:
                new_choice = dict(choice)
                new_choice.setdefault("delta", {})
                normalized_choices.append(new_choice)
                converted_any = True

        if not converted_any and payload.get("object") != "chat.completion":
            return payload

        normalized = dict(payload)
        normalized["choices"] = normalized_choices
        if payload.get("object") == "chat.completion":
            normalized["object"] = "chat.completion.chunk"
        return normalized

    @staticmethod
    def _openai_chunk_created_fallback(created_raw: Any) -> int | None:
        if isinstance(created_raw, int) and not isinstance(created_raw, bool):
            return created_raw
        if isinstance(created_raw, float) and created_raw.is_integer():
            return int(created_raw)
        return None

    def _ensure_openai_compatible_top_level_id(self, payload: dict[str, Any]) -> None:
        """Force string ``id`` on OpenAI-shaped stream payloads for strict clients."""

        payload["id"] = coerce_openai_completion_id(
            payload.get("id"),
            created_fallback=self._openai_chunk_created_fallback(
                payload.get("created")
            ),
        )

    def _serialize_canonical_tool_calls_terminal_payload(
        self, payload: dict[str, Any]
    ) -> bytes | None:
        """Serialize terminal tool-call chunks in strict OpenAI streaming shape.

        Tool-call deltas and the terminal ``finish_reason="tool_calls"`` marker
        must be separate frames. Usage is intentionally omitted from these frames
        because some coding agents treat usage-bearing tool-call finals as a
        completed assistant turn instead of dispatching the tool call.
        """
        choices = payload.get("choices")
        if not isinstance(choices, list) or not choices:
            return None

        terminal_choices: list[dict[str, Any]] = []
        delta_choices: list[dict[str, Any]] = []
        saw_tool_calls_terminal = False

        for idx, choice_any in enumerate(choices):
            if not isinstance(choice_any, dict):
                return None
            if choice_any.get("finish_reason") != "tool_calls":
                continue

            saw_tool_calls_terminal = True
            choice_index = choice_any.get("index", idx)
            terminal_choices.append(
                {
                    "index": choice_index,
                    "delta": {},
                    "finish_reason": "tool_calls",
                }
            )

            delta = choice_any.get("delta")
            if not isinstance(delta, dict):
                continue
            tool_calls = delta.get("tool_calls")
            if not isinstance(tool_calls, list) or not tool_calls:
                continue
            delta_choice = dict(choice_any)
            delta_choice["finish_reason"] = None
            delta_choice["delta"] = dict(delta)
            delta_choices.append(delta_choice)

        if not saw_tool_calls_terminal:
            return None

        terminal_payload = dict(payload)
        terminal_payload.pop("usage", None)
        terminal_payload["choices"] = terminal_choices
        self._ensure_openai_compatible_top_level_id(terminal_payload)

        if not delta_choices:
            return (
                f"data: {json.dumps(terminal_payload)}\n\n" "data: [DONE]\n\n"
            ).encode()

        delta_payload = dict(payload)
        delta_payload.pop("usage", None)
        delta_payload["choices"] = delta_choices
        self._ensure_openai_compatible_top_level_id(delta_payload)
        return (
            f"data: {json.dumps(delta_payload)}\n\n"
            f"data: {json.dumps(terminal_payload)}\n\n"
            "data: [DONE]\n\n"
        ).encode()

    def _serialize_openai_chunk_with_done(
        self, chunk: StreamingChunk, content: StreamingContent
    ) -> bytes:
        """Serialize an OpenAI-formatted chunk with done marker."""
        is_virtual = content.metadata.get("_virtual_tool_calls", False)
        content_copy = dict(
            self._normalize_openai_chat_completion_to_stream_chunk(
                cast(dict[str, Any], content.content)
            )
        )
        self._ensure_openai_compatible_top_level_id(content_copy)

        if content.metadata.get("_suppress_reasoning_fields"):
            self._sanitize_reasoning_fields_in_openai_payload(
                content_copy,
                keep_reasoning_content=bool(
                    content.metadata.get("_keep_reasoning_content")
                ),
                coerce_reasoning_into_content=bool(
                    content.metadata.get("_coerce_reasoning_into_content", True)
                ),
            )

        # Sanitize existing tool_calls in delta/message
        existing_choices = content_copy.get("choices", [])
        if isinstance(existing_choices, list) and existing_choices:
            new_choices = []
            choices_modified = False
            for choice_item in existing_choices:
                if not isinstance(choice_item, dict):
                    new_choices.append(choice_item)
                    continue
                new_choice: dict[str, Any] = dict(choice_item)
                choice_modified = False
                for container_key in ("delta", "message"):
                    container = new_choice.get(container_key)
                    if isinstance(container, dict):
                        tc_list = container.get("tool_calls")
                        if is_virtual:
                            if "tool_calls" in container:
                                new_container: dict[str, Any] = dict(container)
                                del new_container["tool_calls"]
                                new_choice[container_key] = new_container
                                choice_modified = True
                        elif isinstance(tc_list, list) and tc_list:
                            sanitized_calls: list[dict[str, Any]] = []
                            for idx, tc_any in enumerate(cast(list[Any], tc_list)):
                                if not isinstance(tc_any, dict):
                                    continue
                                sanitized_tc: dict[str, Any] = {}
                                for k, v in tc_any.items():
                                    if not isinstance(k, str):
                                        continue
                                    if k.startswith("_") or k == "extra_content":
                                        continue
                                    sanitized_tc[k] = v
                                # Ensure index is present (required by OpenAI streaming spec)
                                if "index" not in sanitized_tc:
                                    sanitized_tc["index"] = idx
                                normalize_tool_call_dict_id_inplace(sanitized_tc)
                                sanitized_calls.append(sanitized_tc)
                            new_container = dict(container)
                            new_container["tool_calls"] = sanitized_calls
                            new_choice[container_key] = new_container
                            choice_modified = True
                if choice_modified:
                    choices_modified = True
                new_choices.append(new_choice)
            if choices_modified:
                content_copy["choices"] = new_choices

        # If is_virtual was handled above, content_copy is ready
        if is_virtual:
            sanitize_openai_chunk_delta_inplace(content_copy)
            return f"data: {json.dumps(content_copy)}\n\ndata: [DONE]\n\n".encode()

        # Non-virtual: Inject tool_calls from typed metadata into the delta if present
        tool_calls = chunk.metadata.tool_calls
        if tool_calls:
            sanitized_calls = []
            for idx, tc in enumerate(tool_calls):
                if not hasattr(tc, "model_dump"):
                    continue
                tc_dict_any: Any = tc.model_dump(exclude_none=True)
                if not isinstance(tc_dict_any, dict):
                    continue
                sanitized_dict: dict[str, Any] = {}
                for k, v in tc_dict_any.items():
                    if not isinstance(k, str):
                        continue
                    if k.startswith("_") or k == "extra_content":
                        continue
                    sanitized_dict[k] = v
                # Ensure index is present (required by OpenAI streaming spec)
                if "index" not in sanitized_dict:
                    sanitized_dict["index"] = idx
                normalize_tool_call_dict_id_inplace(sanitized_dict)
                if sanitized_dict:
                    sanitized_calls.append(sanitized_dict)

            if sanitized_calls and _tool_call_dicts_have_meaningful_arguments(
                sanitized_calls
            ):
                delta = get_first_delta(content_copy)
                if delta:
                    delta["tool_calls"] = sanitized_calls
                    if content_copy.get("choices") and isinstance(
                        content_copy["choices"], list
                    ):
                        content_copy["choices"][0]["delta"] = delta
                return self._serialize_openai_done_payload_with_optional_usage(
                    content_copy, content
                )
        return self._serialize_openai_done_payload_with_optional_usage(
            content_copy, content
        )

    @staticmethod
    def _payload_has_meaningful_openai_choices(payload: dict[str, Any]) -> bool:
        choices = payload.get("choices")
        if not isinstance(choices, list) or not choices:
            return False
        for choice in choices:
            if not isinstance(choice, dict):
                continue
            if choice.get("finish_reason") is not None:
                return True
            delta = choice.get("delta")
            if isinstance(delta, dict) and delta:
                if any(
                    delta.get(key) not in (None, "", [], {})
                    for key in ("content", "tool_calls", "function_call", "refusal")
                ):
                    return True
                if delta.get("role") is not None and len(delta) > 1:
                    return True
            message = choice.get("message")
            if isinstance(message, dict) and any(
                message.get(key) not in (None, "", [], {})
                for key in ("content", "tool_calls", "function_call", "refusal")
            ):
                return True
        return False

    @staticmethod
    def _payload_has_streaming_assistant_body_for_usage_split(
        payload: dict[str, Any],
    ) -> bool:
        """True when the chunk carries assistant *body* that must stay usage-free.

        Legacy Chat Completions clients mis-handle ``usage`` on the same SSE frame
        as a normal assistant delta. Split usage only in that case. Pure terminal
        stop frames (empty ``delta`` + ``finish_reason``, or ``role``-only deltas)
        keep ``usage`` on the same JSON object (OpenRouter-style final chunk).
        """
        choices = payload.get("choices")
        if not isinstance(choices, list) or not choices:
            return False
        for choice in choices:
            if not isinstance(choice, dict):
                continue
            for container_key in ("delta", "message"):
                container = choice.get(container_key)
                if not isinstance(container, dict):
                    continue
                if any(
                    container.get(key) not in (None, "", [], {})
                    for key in ("content", "tool_calls", "function_call", "refusal")
                ):
                    return True
        return False

    def _extract_legacy_openai_usage_chunk_payload(
        self, payload: dict[str, Any]
    ) -> dict[str, Any] | None:
        raw_usage = payload.get("usage")
        if not isinstance(raw_usage, dict):
            return None
        if not self._payload_has_streaming_assistant_body_for_usage_split(payload):
            return None
        return self._build_legacy_openai_usage_chunk(payload, raw_usage)

    @staticmethod
    def _extract_usage_dict_for_legacy_openai(
        content: StreamingContent,
    ) -> dict[str, Any] | None:
        if content.usage is None:
            return None
        if isinstance(content.usage, dict):
            return dict(content.usage)
        return content.usage.to_legacy_dict()

    @staticmethod
    def _build_legacy_openai_usage_chunk(
        source: dict[str, Any], usage: dict[str, Any]
    ) -> dict[str, Any]:
        usage_chunk: dict[str, Any] = {
            "choices": [],
            "usage": usage,
        }
        for key in ("id", "object", "created", "model", "system_fingerprint"):
            value = source.get(key)
            if value is not None:
                usage_chunk[key] = value
        usage_chunk.setdefault("object", "chat.completion.chunk")
        return usage_chunk

    def _serialize_openai_done_payload_with_optional_usage(
        self, content_payload: dict[str, Any], content: StreamingContent
    ) -> bytes:
        canonical_tool_calls = self._serialize_canonical_tool_calls_terminal_payload(
            content_payload
        )
        if canonical_tool_calls is not None:
            return canonical_tool_calls

        if isinstance(
            content_payload.get("usage"), dict
        ) and not self._payload_has_meaningful_openai_choices(content_payload):
            self._ensure_openai_compatible_top_level_id(content_payload)
            return f"data: {json.dumps(content_payload)}\n\ndata: [DONE]\n\n".encode()

        usage_chunk = self._extract_legacy_openai_usage_chunk_payload(content_payload)
        if usage_chunk is not None:
            payload_without_usage = dict(content_payload)
            payload_without_usage.pop("usage", None)
            self._ensure_openai_compatible_top_level_id(payload_without_usage)
            self._ensure_openai_compatible_top_level_id(usage_chunk)
            return (
                f"data: {json.dumps(payload_without_usage)}\n\n"
                f"data: {json.dumps(usage_chunk)}\n\n"
                "data: [DONE]\n\n"
            ).encode()

        usage_from_content = self._extract_usage_dict_for_legacy_openai(content)
        if usage_from_content is None:
            return f"data: {json.dumps(content_payload)}\n\ndata: [DONE]\n\n".encode()

        # Usage carried only on StreamingContent.usage (not embedded in the dict):
        # merge into one terminal frame when this is not a legacy "assistant body +
        # usage" split case (avoids an extra OpenAI ``choices:[]`` chunk that breaks
        # Anthropic SSE conversion and OpenRouter-style single-frame finals).
        if not isinstance(content_payload.get("usage"), dict) and (
            not self._payload_has_streaming_assistant_body_for_usage_split(
                content_payload
            )
        ):
            merged = dict(content_payload)
            merged["usage"] = usage_from_content
            self._ensure_openai_compatible_top_level_id(merged)
            sanitize_openai_chunk_delta_inplace(merged)
            return f"data: {json.dumps(merged)}\n\ndata: [DONE]\n\n".encode()

        # Usage already embedded (e.g. streaming converter merged it); never append
        # a second ``choices:[]`` usage-only chunk.
        if isinstance(content_payload.get("usage"), dict):
            if usage_from_content is not None:
                content_payload["usage"] = usage_from_content
            self._ensure_openai_compatible_top_level_id(content_payload)
            sanitize_openai_chunk_delta_inplace(content_payload)
            return f"data: {json.dumps(content_payload)}\n\ndata: [DONE]\n\n".encode()

        usage_chunk = self._build_legacy_openai_usage_chunk(
            content_payload, usage_from_content
        )
        self._ensure_openai_compatible_top_level_id(content_payload)
        self._ensure_openai_compatible_top_level_id(usage_chunk)
        sanitize_openai_chunk_delta_inplace(content_payload)
        sanitize_openai_chunk_delta_inplace(usage_chunk)
        return (
            f"data: {json.dumps(content_payload)}\n\n"
            f"data: {json.dumps(usage_chunk)}\n\n"
            "data: [DONE]\n\n"
        ).encode()

    def _sanitize_tool_calls(
        self, tool_calls: list[dict[str, Any]]
    ) -> list[dict[str, Any]]:
        """Sanitize tool_calls by removing internal markers and ensuring index is present."""
        result = []
        for idx, tc in enumerate(tool_calls):
            sanitized = {
                k: v
                for k, v in tc.items()
                if not k.startswith("_") and k != "extra_content"
            }
            # Ensure index is present (required by OpenAI streaming spec)
            if "index" not in sanitized:
                sanitized["index"] = idx
            normalize_tool_call_dict_id_inplace(sanitized)
            result.append(sanitized)
        return result

    def _sanitize_chunk_tool_calls_in_place(self, content_copy: dict[str, Any]) -> None:
        """Sanitize tool_calls in choices[].delta/message in place."""
        choices = content_copy.get("choices", [])
        if not isinstance(choices, list):
            return
        for choice_item in choices:
            if not isinstance(choice_item, dict):
                continue
            for container_key in ("delta", "message"):
                container = choice_item.get(container_key)
                if isinstance(container, dict):
                    tc_list = container.get("tool_calls")
                    if isinstance(tc_list, list) and tc_list:
                        container["tool_calls"] = self._sanitize_tool_calls(tc_list)

    def _inject_reasoning_content(
        self, delta: dict[str, Any], reasoning: str | None
    ) -> None:
        """Inject reasoning content and aliases into delta if present."""
        if not reasoning:
            return

        # Non-standard field. Inject only the canonical name to reduce the
        # chance of breaking strict OpenAI-compatible clients.
        if "reasoning_content" not in delta:
            delta["reasoning_content"] = reasoning

    @staticmethod
    def _restore_empty_content_for_kept_reasoning(
        payload: dict[str, Any], content: StreamingContent
    ) -> None:
        """Preserve an explicit empty ``content`` when reasoning is kept.

        ``sanitize_openai_chunk_delta_inplace`` removes ``content: ""`` whenever
        reasoning is present. Clients that opt into ``_keep_reasoning_content``
        still expect a stable ``content`` field, so restore it after sanitizing.
        """
        if not (
            content.metadata.get("_suppress_reasoning_fields")
            and content.metadata.get("_keep_reasoning_content")
        ):
            return
        choices = payload.get("choices")
        if not isinstance(choices, list):
            return
        for choice in choices:
            if not isinstance(choice, dict):
                continue
            for container_key in ("delta", "message"):
                container = choice.get(container_key)
                if isinstance(container, dict) and container.get("reasoning_content"):
                    container.setdefault("content", "")

    def _inject_tool_calls(
        self, delta: dict[str, Any], tool_calls: list[Any] | None
    ) -> None:
        """Inject sanitized tool calls into delta if present."""
        if not tool_calls:
            return
        tool_calls_dicts: list[dict[str, Any]] = []
        for tc in tool_calls:
            if hasattr(tc, "model_dump"):
                tool_calls_dicts.append(tc.model_dump(exclude_none=True))
            elif isinstance(tc, dict):
                tool_calls_dicts.append(tc)
        if not tool_calls_dicts:
            return
        existing = delta.get("tool_calls")
        if (
            isinstance(existing, list)
            and existing
            and not _tool_call_dicts_have_meaningful_arguments(tool_calls_dicts)
        ):
            # Metadata often carries placeholder arguments from early deltas; do not
            # clobber delta.tool_calls that already hold merged upstream payloads.
            return
        delta["tool_calls"] = self._sanitize_tool_calls(tool_calls_dicts)

    def _serialize_openai_formatted_dict(
        self,
        working_content: dict[str, Any],
        chunk: StreamingChunk,
        content: StreamingContent,
    ) -> bytes:
        """Serialize an OpenAI-formatted dict chunk."""
        is_virtual_tc = content.metadata.get("_virtual_tool_calls", False)
        suppress_reasoning = bool(content.metadata.get("_suppress_reasoning_fields"))
        content_copy = dict(
            self._normalize_openai_chat_completion_to_stream_chunk(working_content)
        )
        self._ensure_openai_compatible_top_level_id(content_copy)

        if suppress_reasoning:
            self._sanitize_reasoning_fields_in_openai_payload(
                content_copy,
                keep_reasoning_content=bool(
                    content.metadata.get("_keep_reasoning_content")
                ),
                coerce_reasoning_into_content=bool(
                    content.metadata.get("_coerce_reasoning_into_content", True)
                ),
            )
        self._sanitize_chunk_tool_calls_in_place(content_copy)
        delta = get_first_delta(content_copy)

        if delta is not None:
            if is_virtual_tc:
                if "tool_calls" in delta:
                    # Filter out tool_calls for virtual mode
                    new_delta = {k: v for k, v in delta.items() if k != "tool_calls"}
                    delta.clear()
                    delta.update(new_delta)
            else:
                self._inject_tool_calls(delta, chunk.metadata.tool_calls)

            if not suppress_reasoning:
                self._inject_reasoning_content(delta, chunk.metadata.reasoning_content)

            # Ensure the modified delta is reflected in content_copy
            if content_copy.get("choices") and isinstance(
                content_copy["choices"], list
            ):
                content_copy["choices"][0]["delta"] = delta

        self._ensure_openai_finish_reason_for_terminal_usage(content_copy, chunk)
        sanitize_openai_chunk_delta_inplace(content_copy)

        parts = [f"data: {json.dumps(content_copy)}\n\n"]
        if chunk.is_done:
            parts.append("data: [DONE]\n\n")
        return "".join(parts).encode()

    def _build_delta_metadata(
        self, chunk: StreamingChunk, content: StreamingContent, delta: dict[str, Any]
    ) -> None:
        """Build delta metadata (role, tool_call_id, tool_calls, reasoning)."""
        if chunk.metadata.role:
            delta["role"] = chunk.metadata.role
        tool_call_id = content.metadata.get("tool_call_id")
        if isinstance(tool_call_id, str) and tool_call_id:
            delta["tool_call_id"] = tool_call_id
        is_virtual = content.metadata.get("_virtual_tool_calls", False)

        if not is_virtual:
            self._inject_tool_calls(delta, chunk.metadata.tool_calls)

        reasoning = chunk.metadata.reasoning_content
        suppress_reasoning = bool(content.metadata.get("_suppress_reasoning_fields"))
        if not suppress_reasoning:
            self._inject_reasoning_content(delta, reasoning)
        elif content.metadata.get("_keep_reasoning_content"):
            # Normalized provider streams carry reasoning in metadata rather than
            # inside an OpenAI-shaped payload. Preserve it for clients that opt
            # into the canonical reasoning_content field even when aliases are
            # suppressed.
            self._inject_reasoning_content(delta, reasoning)

    def _serialize_normal_chunk(
        self, chunk: StreamingChunk, content: StreamingContent
    ) -> bytes:
        """Serialize a normal (non-done) chunk."""
        # Fast path for simple text chunks
        if (
            chunk.payload.kind == "text"
            and chunk.payload.text is not None
            and not chunk.metadata.tool_calls
            and not chunk.metadata.reasoning_content
            and not chunk.metadata.finish_reason
            and not chunk.metadata.usage
            and not content.usage
            and not content.metadata.get("tool_call_id")
            and not content.metadata.get("_virtual_tool_calls")
        ):
            created = (
                content.metadata.get("created")
                if isinstance(content.metadata.get("created"), int)
                else int(time.time())
            )
            model = (
                content.metadata.get("model")
                if isinstance(content.metadata.get("model"), str)
                else "unknown"
            )
            chunk_id = (
                content.metadata.get("id")
                if isinstance(content.metadata.get("id"), str)
                else f"chatcmpl-{created}"
            )
            index = (
                content.metadata.get("index")
                if isinstance(content.metadata.get("index"), int)
                else 0
            )

            delta_fast: dict[str, Any] = {}
            if chunk.metadata.role:
                delta_fast["role"] = chunk.metadata.role
            delta_fast["content"] = chunk.payload.text

            choice_fast: dict[str, Any] = {
                "index": index,
                "delta": delta_fast,
                "finish_reason": None,
            }
            response_data_fast: dict[str, Any] = {
                "id": chunk_id,
                "object": "chat.completion.chunk",
                "created": created,
                "model": model,
                "choices": [choice_fast],
            }

            parts = [f"data: {json.dumps(response_data_fast)}\n\n"]
            if chunk.is_done:
                parts.append("data: [DONE]\n\n")
            return "".join(parts).encode()

        # Build delta object
        delta: dict[str, Any] = {}

        # Add metadata to delta
        self._build_delta_metadata(chunk, content, delta)

        # Add main content from typed payload
        if chunk.payload.kind == "text" and chunk.payload.text is not None:
            delta["content"] = chunk.payload.text
        elif (
            chunk.payload.kind == "opaque_json_dict" and chunk.payload.opaque_json_dict
        ):
            parsed_content = chunk.payload.opaque_json_dict
            # Check if OpenAI-formatted chunk
            if "choices" in parsed_content or "usage" in parsed_content:
                return self._serialize_openai_formatted_dict(
                    parsed_content, chunk, content
                )

            # Check for StopChunkWithUsage misuse
            if isinstance(content.content, StopChunkWithUsage):
                raise UsageChunkLeakError(chunk_id=parsed_content.get("id"))

            delta["content"] = json.dumps(parsed_content)
        elif chunk.payload.kind == "opaque_json" and chunk.payload.opaque_json:
            json_str = chunk.payload.opaque_json
            is_potential_openai = '"choices"' in json_str or '"usage"' in json_str
            is_leak_check_needed = isinstance(content.content, StopChunkWithUsage)

            if not is_potential_openai and not is_leak_check_needed:
                delta["content"] = json_str
            else:
                try:
                    parsed_json: Any = json.loads(json_str)
                    if isinstance(parsed_json, dict):
                        if "choices" in parsed_json or "usage" in parsed_json:
                            return self._serialize_openai_formatted_dict(
                                parsed_json, chunk, content
                            )
                        if is_leak_check_needed:
                            raise UsageChunkLeakError(chunk_id=parsed_json.get("id"))
                        delta["content"] = json.dumps(parsed_json)
                    else:
                        delta["content"] = json.dumps(parsed_json)
                except json.JSONDecodeError:
                    delta["content"] = json_str

        elif chunk.payload.kind == "binary" and chunk.payload.binary_b64:
            # Decode base64 binary content
            binary_data: bytes | None = None
            try:
                binary_data = base64.b64decode(chunk.payload.binary_b64)
                delta["content"] = binary_data.decode("utf-8")
            except (UnicodeDecodeError, ValueError):
                if binary_data is not None:
                    try:
                        delta["content"] = binary_data.decode("latin-1")
                    except UnicodeDecodeError:
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                "Failed to decode binary content, using string representation",
                                exc_info=True,
                            )
                        delta["content"] = str(binary_data)
                else:
                    delta["content"] = ""
        else:
            delta["content"] = ""

        reasoning = chunk.metadata.reasoning_content
        suppress_reasoning = bool(content.metadata.get("_suppress_reasoning_fields"))
        if (
            suppress_reasoning
            and not content.metadata.get("_keep_reasoning_content")
            and content.metadata.get("_coerce_reasoning_into_content", True)
            and reasoning
            and not delta.get("content")
        ):
            # The coerce policy must also work for metadata-only chunks. The
            # existing alias sanitizer handles dict payloads, but normalized
            # Anthropic/Alibaba thinking chunks have no content payload to edit.
            delta["content"] = reasoning

        created = (
            content.metadata.get("created")
            if isinstance(content.metadata.get("created"), int)
            else int(time.time())
        )
        model = (
            content.metadata.get("model")
            if isinstance(content.metadata.get("model"), str)
            else "unknown"
        )
        chunk_id = (
            content.metadata.get("id")
            if isinstance(content.metadata.get("id"), str)
            else f"chatcmpl-{created}"
        )
        index = (
            content.metadata.get("index")
            if isinstance(content.metadata.get("index"), int)
            else 0
        )

        # Build response data (OpenAI-compatible envelope)
        choice: dict[str, Any] = {
            "index": index,
            "delta": delta,
            "finish_reason": None,
        }
        response_data: dict[str, Any] = {
            "id": chunk_id,
            "object": "chat.completion.chunk",
            "created": created,
            "model": model,
            "choices": [choice],
        }

        if chunk.metadata.finish_reason:
            response_data["choices"][0]["finish_reason"] = chunk.metadata.finish_reason
        sanitize_openai_chunk_delta_inplace(response_data)
        self._restore_empty_content_for_kept_reasoning(response_data, content)
        parts = [f"data: {json.dumps(response_data)}\n\n"]
        if chunk.is_done:
            parts.append("data: [DONE]\n\n")
        return "".join(parts).encode()


__all__ = ["SSESerializer"]
