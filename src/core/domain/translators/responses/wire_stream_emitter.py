"""Emit OpenAI Responses wire-format SSE events from canonical stream chunks."""

from __future__ import annotations

import json
import time
import uuid
from dataclasses import dataclass
from typing import Any, cast


def _coerce_str(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    return str(value)


def _coerce_json_str(value: Any, *, default: str = "") -> str:
    if value is None:
        return default
    if isinstance(value, str):
        return value
    if isinstance(value, dict | list):
        return json.dumps(value)
    return str(value)


def _extract_delta_map(
    domain_chunk: dict[str, Any],
) -> tuple[dict[str, Any], str | None]:
    """Return ``(delta_dict, finish_reason)`` from a canonical stream chunk."""
    choices = domain_chunk.get("choices")
    if not isinstance(choices, list) or not choices:
        return {}, None

    primary = choices[0]
    if not isinstance(primary, dict):
        return {}, None

    finish_raw = primary.get("finish_reason")
    finish_reason = finish_raw if isinstance(finish_raw, str) and finish_raw else None

    delta = primary.get("delta")
    if isinstance(delta, dict):
        return cast(dict[str, Any], dict(delta)), finish_reason
    if delta is None:
        return {}, finish_reason
    return {"content": _coerce_str(delta)}, finish_reason


def _minimal_response_snapshot(
    *,
    response_id: str,
    model: str,
    created_at: float,
    status: str,
    output: list[dict[str, Any]],
    usage: dict[str, Any] | None = None,
    incomplete_reason: str | None = None,
) -> dict[str, Any]:
    payload: dict[str, Any] = {
        "id": response_id,
        "object": "response",
        "created_at": created_at,
        "model": model,
        "status": status,
        "output": output,
        "parallel_tool_calls": False,
        "tools": [],
        "tool_choice": "auto",
    }
    if usage:
        payload["usage"] = usage
    if incomplete_reason:
        payload["incomplete_details"] = {"reason": incomplete_reason}
    return payload


@dataclass
class _ToolCallState:
    item_id: str
    call_id: str
    name: str
    output_index: int
    arguments: str = ""
    done: bool = False


class ResponsesWireStreamEmitter:
    """Convert internal ``choices``/``delta`` chunks to official Responses events."""

    def __init__(self, *, model: str, created_at: float | None = None) -> None:
        self._model = model or "unknown"
        self._created_at = float(created_at or time.time())
        self._response_id: str | None = None
        self._seq = 0
        self._primed = False
        self._finished = False

        self._used_output_indexes: set[int] = set()
        self._next_output_index = 0

        self._message_item_id: str | None = None
        self._message_output_index: int | None = None
        self._message_done = False
        self._text_buffer: list[str] = []

        self._tool_calls: dict[str, _ToolCallState] = {}
        self._completed_output_items: dict[int, dict[str, Any]] = {}

    def _next_seq(self) -> int:
        current = self._seq
        self._seq += 1
        return current

    def _ensure_response_id(self, domain_chunk: dict[str, Any]) -> str:
        if self._response_id:
            return self._response_id
        rid = domain_chunk.get("id")
        if isinstance(rid, str) and rid.strip():
            self._response_id = rid.strip()
        else:
            self._response_id = f"resp_{uuid.uuid4().hex[:40]}"
        return self._response_id

    def _reserve_output_index(self, preferred: int | None = None) -> int:
        if (
            isinstance(preferred, int)
            and preferred >= 0
            and preferred not in self._used_output_indexes
        ):
            self._used_output_indexes.add(preferred)
            if preferred >= self._next_output_index:
                self._next_output_index = preferred + 1
            return preferred

        while self._next_output_index in self._used_output_indexes:
            self._next_output_index += 1
        assigned = self._next_output_index
        self._used_output_indexes.add(assigned)
        self._next_output_index += 1
        return assigned

    def _prelude(self, response_id: str) -> list[dict[str, Any]]:
        empty = _minimal_response_snapshot(
            response_id=response_id,
            model=self._model,
            created_at=self._created_at,
            status="in_progress",
            output=[],
        )
        return [
            {
                "type": "response.created",
                "sequence_number": self._next_seq(),
                "response": empty,
            },
            {
                "type": "response.in_progress",
                "sequence_number": self._next_seq(),
                "response": empty,
            },
        ]

    def _ensure_message_started(self, out: list[dict[str, Any]]) -> None:
        if self._message_item_id is not None:
            return

        self._message_item_id = f"msg_{uuid.uuid4().hex[:24]}"
        self._message_output_index = self._reserve_output_index()

        out.append(
            {
                "type": "response.output_item.added",
                "sequence_number": self._next_seq(),
                "output_index": self._message_output_index,
                "item": {
                    "id": self._message_item_id,
                    "type": "message",
                    "role": "assistant",
                    "status": "in_progress",
                    "content": [],
                },
            }
        )
        out.append(
            {
                "type": "response.content_part.added",
                "sequence_number": self._next_seq(),
                "item_id": self._message_item_id,
                "output_index": self._message_output_index,
                "content_index": 0,
                "part": {
                    "type": "output_text",
                    "text": "",
                    "annotations": [],
                },
            }
        )

    def _normalize_tool_deltas(
        self, delta_map: dict[str, Any]
    ) -> list[tuple[str, str, str, int | None]]:
        raw_tool_calls = delta_map.get("tool_calls")
        if not isinstance(raw_tool_calls, list):
            return []

        normalized: list[tuple[str, str, str, int | None]] = []
        for raw in raw_tool_calls:
            if hasattr(raw, "model_dump"):
                item = raw.model_dump(exclude_none=True)
            elif isinstance(raw, dict):
                item = dict(raw)
            else:
                function = getattr(raw, "function", None)
                item = {
                    "id": getattr(raw, "id", None),
                    "index": getattr(raw, "index", None),
                    "function": {
                        "name": getattr(function, "name", ""),
                        "arguments": getattr(function, "arguments", ""),
                    },
                }

            fn = item.get("function")
            fn_dict = fn if isinstance(fn, dict) else {}

            call_id_raw = item.get("id") or item.get("call_id")
            call_id = (
                _coerce_str(call_id_raw).strip()
                if call_id_raw is not None
                else f"call_{uuid.uuid4().hex[:12]}"
            )
            if not call_id:
                call_id = f"call_{uuid.uuid4().hex[:12]}"

            name = _coerce_str(fn_dict.get("name") or item.get("name")).strip()
            if not name:
                # Skip unnamed tool-call fragments; replaying them causes HTTP 400s.
                continue
            arguments_fragment = _coerce_json_str(
                fn_dict.get("arguments", item.get("arguments")), default=""
            )
            idx_raw = item.get("index")
            preferred_index = (
                idx_raw if isinstance(idx_raw, int) and idx_raw >= 0 else None
            )
            normalized.append((call_id, name, arguments_fragment, preferred_index))

        return normalized

    def _ensure_tool_state(
        self,
        out: list[dict[str, Any]],
        *,
        call_id: str,
        name: str,
        preferred_index: int | None,
    ) -> _ToolCallState:
        existing = self._tool_calls.get(call_id)
        if existing is not None:
            if name and not existing.name:
                existing.name = name
            return existing

        output_index = self._reserve_output_index(preferred_index)
        item_id = call_id
        state = _ToolCallState(
            item_id=item_id,
            call_id=call_id,
            name=name,
            output_index=output_index,
        )
        self._tool_calls[call_id] = state

        out.append(
            {
                "type": "response.output_item.added",
                "sequence_number": self._next_seq(),
                "output_index": output_index,
                "item": {
                    "id": item_id,
                    "type": "function_call",
                    "status": "in_progress",
                    "call_id": call_id,
                    "name": name,
                    "arguments": "",
                },
            }
        )
        return state

    @staticmethod
    def _merge_arguments_buffer(current: str, fragment: str) -> str:
        if not fragment:
            return current
        if not current:
            return fragment
        if fragment.startswith(current):
            return fragment
        if current.endswith(fragment):
            return current
        return current + fragment

    def _close_tool_call(
        self, out: list[dict[str, Any]], state: _ToolCallState
    ) -> None:
        if state.done:
            return
        arguments = state.arguments.strip() or "{}"

        done_args_evt: dict[str, Any] = {
            "type": "response.function_call_arguments.done",
            "sequence_number": self._next_seq(),
            "item_id": state.item_id,
            "output_index": state.output_index,
            "arguments": arguments,
        }
        if state.name:
            done_args_evt["name"] = state.name
        out.append(done_args_evt)

        done_item = {
            "id": state.item_id,
            "type": "function_call",
            "status": "completed",
            "call_id": state.call_id,
            "name": state.name,
            "arguments": arguments,
        }
        out.append(
            {
                "type": "response.output_item.done",
                "sequence_number": self._next_seq(),
                "output_index": state.output_index,
                "item": done_item,
            }
        )
        self._completed_output_items[state.output_index] = done_item
        state.done = True

    def _close_message(self, out: list[dict[str, Any]]) -> None:
        if self._message_item_id is None or self._message_done:
            return
        if self._message_output_index is None:
            return

        full_text = "".join(self._text_buffer)
        done_part = {"type": "output_text", "text": full_text, "annotations": []}

        out.append(
            {
                "type": "response.output_text.done",
                "sequence_number": self._next_seq(),
                "item_id": self._message_item_id,
                "output_index": self._message_output_index,
                "content_index": 0,
                "text": full_text,
                "logprobs": [],
            }
        )
        out.append(
            {
                "type": "response.content_part.done",
                "sequence_number": self._next_seq(),
                "item_id": self._message_item_id,
                "output_index": self._message_output_index,
                "content_index": 0,
                "part": done_part,
            }
        )

        message_item = {
            "id": self._message_item_id,
            "type": "message",
            "role": "assistant",
            "status": "completed",
            "content": [done_part],
        }
        out.append(
            {
                "type": "response.output_item.done",
                "sequence_number": self._next_seq(),
                "output_index": self._message_output_index,
                "item": message_item,
            }
        )
        self._completed_output_items[self._message_output_index] = message_item
        self._message_done = True

    def _ordered_output_items(self) -> list[dict[str, Any]]:
        return [
            self._completed_output_items[idx]
            for idx in sorted(self._completed_output_items.keys())
        ]

    def feed(self, domain_chunk: dict[str, Any]) -> list[dict[str, Any]]:
        """Translate one canonical stream chunk to wire-format events."""
        out: list[dict[str, Any]] = []
        if self._finished:
            return out

        if domain_chunk.get("error"):
            err = domain_chunk.get("error")
            out.append(
                {
                    "type": "error",
                    "sequence_number": self._next_seq(),
                    "error": (
                        err if isinstance(err, dict) else {"message": _coerce_str(err)}
                    ),
                }
            )
            self._finished = True
            return out

        rid = self._ensure_response_id(domain_chunk)
        if not self._primed:
            out.extend(self._prelude(rid))
            self._primed = True

        model = domain_chunk.get("model")
        if isinstance(model, str) and model.strip():
            self._model = model.strip()

        delta, finish_reason = _extract_delta_map(domain_chunk)

        text_piece = delta.get("content")
        text = _coerce_str(text_piece) if text_piece is not None else ""
        if text:
            self._ensure_message_started(out)
            if (
                self._message_item_id is not None
                and self._message_output_index is not None
            ):
                self._text_buffer.append(text)
                out.append(
                    {
                        "type": "response.output_text.delta",
                        "sequence_number": self._next_seq(),
                        "item_id": self._message_item_id,
                        "output_index": self._message_output_index,
                        "content_index": 0,
                        "delta": text,
                        "logprobs": [],
                    }
                )

        for (
            call_id,
            name,
            arguments_fragment,
            preferred_index,
        ) in self._normalize_tool_deltas(delta):
            state = self._ensure_tool_state(
                out,
                call_id=call_id,
                name=name,
                preferred_index=preferred_index,
            )
            if arguments_fragment:
                state.arguments = self._merge_arguments_buffer(
                    state.arguments, arguments_fragment
                )
                delta_evt: dict[str, Any] = {
                    "type": "response.function_call_arguments.delta",
                    "sequence_number": self._next_seq(),
                    "item_id": state.item_id,
                    "output_index": state.output_index,
                    "delta": arguments_fragment,
                }
                if state.name:
                    delta_evt["name"] = state.name
                out.append(delta_evt)

        if finish_reason:
            out.extend(self._terminal_events(domain_chunk, finish_reason))

        return out

    def is_finished(self) -> bool:
        return self._finished

    def _terminal_events(
        self, domain_chunk: dict[str, Any], finish_reason: str
    ) -> list[dict[str, Any]]:
        if self._finished:
            return []

        out: list[dict[str, Any]] = []
        rid = self._ensure_response_id(domain_chunk)
        if not self._primed:
            out.extend(self._prelude(rid))
            self._primed = True

        self._close_message(out)
        for state in sorted(
            self._tool_calls.values(), key=lambda item: item.output_index
        ):
            self._close_tool_call(out, state)

        # Always emit a final text.done event (even if empty) when a message was started.
        # This ensures strict clients see a complete message lifecycle even in pure-tool-call
        # responses.
        if self._message_item_id is not None and not self._message_done:
            self._close_message(out)

        usage = domain_chunk.get("usage")
        usage_dict = usage if isinstance(usage, dict) else None

        incomplete_reason: str | None = None
        if finish_reason in {"length", "max_output_tokens"}:
            incomplete_reason = "max_output_tokens"
        elif finish_reason == "content_filter":
            incomplete_reason = "content_filter"

        response = _minimal_response_snapshot(
            response_id=rid,
            model=self._model,
            created_at=self._created_at,
            status="incomplete" if incomplete_reason else "completed",
            output=self._ordered_output_items(),
            usage=usage_dict,
            incomplete_reason=incomplete_reason,
        )

        out.append(
            {
                "type": (
                    "response.incomplete" if incomplete_reason else "response.completed"
                ),
                "sequence_number": self._next_seq(),
                "response": response,
            }
        )
        self._finished = True
        return out

    def finalize(
        self, *, tail_domain_chunk: dict[str, Any] | None = None
    ) -> list[dict[str, Any]]:
        """Emit closing events if stream ended without terminal finish reason."""
        if self._finished:
            return []

        events: list[dict[str, Any]] = []
        if not self._primed:
            rid = self._ensure_response_id(
                tail_domain_chunk if isinstance(tail_domain_chunk, dict) else {}
            )
            events.extend(self._prelude(rid))
            self._primed = True

        if tail_domain_chunk and isinstance(tail_domain_chunk, dict):
            _, finish_reason = _extract_delta_map(tail_domain_chunk)
            if finish_reason:
                return events + self._terminal_events(tail_domain_chunk, finish_reason)

        synthetic: dict[str, Any] = {
            "id": self._response_id or f"resp_{uuid.uuid4().hex[:40]}"
        }
        if isinstance(tail_domain_chunk, dict) and isinstance(
            tail_domain_chunk.get("usage"), dict
        ):
            synthetic["usage"] = tail_domain_chunk["usage"]
        return events + self._terminal_events(synthetic, "stop")
