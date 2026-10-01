"""Normalize provider stream chunks into Responses semantic streaming events."""

from __future__ import annotations

import json
from collections.abc import AsyncIterator
from enum import Enum
from typing import Any

from src.core.domain.responses_semantic_events import (
    ResponsesSemanticEvent,
    ResponsesSemanticEventType,
)
from src.core.interfaces.response_processor_interface import ProcessedResponse

_TERMINAL_TYPES: frozenset[ResponsesSemanticEventType] = frozenset(
    {
        ResponsesSemanticEventType.RESPONSE_COMPLETED,
        ResponsesSemanticEventType.RESPONSE_FAILED,
        ResponsesSemanticEventType.RESPONSE_INCOMPLETE,
    }
)


class ResponsesStreamAbortedError(Exception):
    """Signal that a stream ended before a Responses terminal event was emitted."""


class ResponsesStreamSource(str, Enum):
    OPENAI_RESPONSES = "openai_responses"
    OPENAI_CHAT_COMPLETIONS = "openai_chat_completions"
    ANTHROPIC = "anthropic"
    GEMINI = "gemini"


def _coerce_int(value: Any) -> int | None:
    if value is None:
        return None
    if isinstance(value, bool):
        return None
    if isinstance(value, int):
        return value
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def _extract_text_delta(delta_val: Any) -> str:
    if isinstance(delta_val, str):
        return delta_val
    if isinstance(delta_val, dict):
        for key in ("text", "value", "content"):
            inner = delta_val.get(key)
            if isinstance(inner, str) and inner:
                return inner
    return ""


def _parse_stream_dicts_from_text(text: str) -> list[dict[str, Any]]:
    if not text:
        return []
    normalized = text.replace("\n", "\n")
    payloads: list[dict[str, Any]] = []
    if "data:" in normalized:
        for block in normalized.split("\n\n"):
            for line in block.splitlines():
                line_s = line.strip()
                if not line_s.startswith("data:"):
                    continue
                data_part = line_s[5:].lstrip()
                if not data_part or data_part == "[DONE]":
                    continue
                try:
                    obj = json.loads(data_part)
                except json.JSONDecodeError:
                    continue
                if isinstance(obj, dict):
                    payloads.append(obj)
        if payloads:
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

    for line in normalized.splitlines():
        line_s = line.strip()
        if not line_s:
            continue
        if not line_s.startswith("{"):
            continue
        try:
            obj = json.loads(line_s)
        except json.JSONDecodeError:
            continue
        if isinstance(obj, dict):
            payloads.append(obj)
    return payloads


class ResponsesEventNormalizer:
    def __init__(self, *, source: ResponsesStreamSource, response_id: str) -> None:
        self._source = source
        self._default_response_id = response_id
        self._active_response_id = response_id
        self._seq = 0
        self._openai_legacy_stream_started = False
        self._openai_legacy_text_started = False
        self._openai_legacy_text_finalized = False
        self._openai_legacy_response_id: str | None = None
        self._openai_legacy_model: str | None = None
        self._openai_legacy_text_fragments: list[str] = []
        self._gemini_lifecycle_started = False
        self._anthropic_message_id: str | None = None
        self._anthropic_items: dict[int, dict[str, Any]] = {}
        self._anthropic_text_buffers: dict[int, list[str]] = {}
        self._anthropic_tool_arg_buffers: dict[int, list[str]] = {}
        self._gemini_text_fragments: list[str] = []
        # item.id / call_id -> function name from output_item.added
        self._function_names_by_item_id: dict[str, str] = {}
        # Completed function_call items from legacy chat reconstruction; included
        # in RESPONSE_COMPLETED.output so OpenCode can link function_call_output.
        self._legacy_function_call_items: list[dict[str, Any]] = []

    @staticmethod
    def _build_output_text_part(text: str) -> dict[str, Any]:
        return {"type": "output_text", "text": text}

    @staticmethod
    def _build_function_call_item(
        item_id: str,
        arguments: str,
        *,
        name: str = "",
        call_id: str | None = None,
    ) -> dict[str, Any]:
        resolved_call_id = call_id or item_id
        item: dict[str, Any] = {
            "id": item_id,
            "type": "function_call",
            "status": "completed",
            "call_id": resolved_call_id,
            "arguments": arguments,
        }
        if isinstance(name, str) and name.strip():
            item["name"] = name
        return item

    @staticmethod
    def _build_message_item(item_id: str, text: str) -> dict[str, Any]:
        return {
            "id": item_id,
            "type": "message",
            "role": "assistant",
            "status": "completed",
            "content": [ResponsesEventNormalizer._build_output_text_part(text)],
        }

    def _next(
        self,
        *,
        type: ResponsesSemanticEventType,
        response_id: str,
        output_index: int | None = None,
        content_index: int | None = None,
        item_id: str | None = None,
        name: str | None = None,
        delta: str | None = None,
        text: str | None = None,
        item: dict[str, Any] | None = None,
        part: dict[str, Any] | None = None,
        response: dict[str, Any] | None = None,
        error: dict[str, Any] | None = None,
        raw: dict[str, Any] | None = None,
    ) -> ResponsesSemanticEvent:
        seq = self._seq
        self._seq += 1
        return ResponsesSemanticEvent(
            type=type,
            response_id=response_id,
            sequence_number=seq,
            output_index=output_index,
            content_index=content_index,
            item_id=item_id,
            name=name,
            delta=delta,
            text=text,
            item=item,
            part=part,
            response=response,
            error=error,
            raw=raw,
        )

    def _remember_function_name(self, *, item_id: str | None, call_id: str | None, name: str) -> None:
        if not isinstance(name, str) or not name.strip():
            return
        for key in (item_id, call_id):
            if isinstance(key, str) and key.strip():
                self._function_names_by_item_id[key] = name

    def _lookup_function_name(self, *keys: str | None) -> str | None:
        for key in keys:
            if isinstance(key, str) and key:
                cached = self._function_names_by_item_id.get(key)
                if cached:
                    return cached
        return None

    def _consume_openai_response_id(self, payload: dict[str, Any]) -> str:
        response_obj = payload.get("response")
        if isinstance(response_obj, dict):
            rid = response_obj.get("id")
            if isinstance(rid, str) and rid:
                self._active_response_id = rid
                return rid
        top_id = payload.get("id")
        if isinstance(top_id, str) and top_id:
            self._active_response_id = top_id
            return top_id
        return self._active_response_id

    def _unwrap_to_dicts(self, chunk: Any) -> list[dict[str, Any]]:
        if isinstance(chunk, ProcessedResponse):
            md = chunk.metadata
            inner = self._unwrap_payload_value(chunk.content)
            if isinstance(md.get("tool_calls"), list) and md["tool_calls"]:
                synthetic: dict[str, Any] = {
                    "choices": [{"delta": {"tool_calls": md["tool_calls"]}}]
                }
                if inner:
                    return [*inner, synthetic]
                return [synthetic]
            return inner
        return self._unwrap_payload_value(chunk)

    def _unwrap_payload_value(self, chunk: Any) -> list[dict[str, Any]]:
        if isinstance(chunk, bytes | bytearray):
            chunk = chunk.decode("utf-8", errors="ignore")
        if isinstance(chunk, str):
            return _parse_stream_dicts_from_text(chunk)
        if isinstance(chunk, dict):
            return [dict(chunk)]
        return []

    async def normalize(
        self, chunks: AsyncIterator[Any]
    ) -> AsyncIterator[ResponsesSemanticEvent]:
        terminal_emitted = False
        stream_aborted = False
        try:
            async for raw in chunks:
                stop_after_raw = False
                for payload in self._unwrap_to_dicts(raw):
                    for event in self._map_payload(payload):
                        if event.type in _TERMINAL_TYPES:
                            if terminal_emitted:
                                continue
                            terminal_emitted = True
                            stop_after_raw = True
                        yield event
                # A terminal event means the Responses turn is finished. Stop
                # consuming upstream immediately so SSE clients receive
                # `response.completed` + `[DONE]` without waiting for the
                # provider HTTP body to EOF (chatgpt-plan can linger).
                if stop_after_raw:
                    break
        except ResponsesStreamAbortedError:
            # Client disconnect/cancellation must not be turned into a synthetic
            # response.completed event.  Re-raise so the renderer can skip both
            # wire output and chainable session persistence.
            stream_aborted = True
            raise
        except Exception as exc:
            if not terminal_emitted and not stream_aborted:
                if (
                    self._source is ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS
                    and not self._openai_legacy_stream_started
                ):
                    for opening_event in self._start_openai_legacy_stream(
                        self._default_response_id, None
                    ):
                        yield opening_event
                yield self._next(
                    type=ResponsesSemanticEventType.RESPONSE_FAILED,
                    response_id=self._default_response_id,
                    error={
                        "message": str(exc),
                        "type": type(exc).__name__,
                    },
                )
                terminal_emitted = True
            return
        finally:
            if not terminal_emitted and not stream_aborted:
                if (
                    self._source is ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS
                    and not self._openai_legacy_stream_started
                ):
                    for opening_event in self._start_openai_legacy_stream(
                        self._default_response_id, None
                    ):
                        yield opening_event
                terminal_response_id = self._default_response_id
                terminal_response: dict[str, Any] = {"id": self._default_response_id}
                if self._openai_legacy_stream_started:
                    terminal_response_id = (
                        self._openai_legacy_response_id or self._default_response_id
                    )
                    for closing_event in self._finalize_openai_legacy_text(
                        terminal_response_id
                    ):
                        yield closing_event
                    terminal_response = {
                        "id": terminal_response_id,
                        "object": "response",
                        "status": "completed",
                        "model": self._openai_legacy_model,
                        "output": (
                            [
                                self._build_message_item(
                                    f"item_{terminal_response_id}_0",
                                    "".join(self._openai_legacy_text_fragments),
                                )
                            ]
                            if self._openai_legacy_text_started
                            else []
                        ),
                    }
                yield self._next(
                    type=ResponsesSemanticEventType.RESPONSE_COMPLETED,
                    response_id=terminal_response_id,
                    response=terminal_response,
                )

    def _map_payload(self, payload: dict[str, Any]) -> list[ResponsesSemanticEvent]:
        if self._source in {
            ResponsesStreamSource.OPENAI_RESPONSES,
            ResponsesStreamSource.OPENAI_CHAT_COMPLETIONS,
        }:
            return self._map_openai(payload)
        if self._source == ResponsesStreamSource.ANTHROPIC:
            return self._map_anthropic(payload)
        return self._map_gemini(payload)

    def _try_openai_legacy_chat(
        self, d: dict[str, Any], rid: str
    ) -> list[ResponsesSemanticEvent] | None:
        choices = d.get("choices")
        if not isinstance(choices, list) or not choices:
            return None
        choice0 = choices[0]
        if not isinstance(choice0, dict):
            return None
        delta_raw = choice0.get("delta")
        delta: dict[str, Any] = delta_raw if isinstance(delta_raw, dict) else {}
        finish_reason = choice0.get("finish_reason")
        content_raw = delta.get("content")
        content: str = content_raw if isinstance(content_raw, str) else ""
        tool_calls_raw = delta.get("tool_calls")
        tool_calls_list: list[Any] = (
            tool_calls_raw if isinstance(tool_calls_raw, list) else []
        )
        has_text = bool(content)
        has_tools = bool(tool_calls_list)
        has_finish = bool(finish_reason)
        if not has_text and not has_tools and not has_finish:
            # Role-only chunks still establish a valid legacy stream lifecycle.
            # They are otherwise non-informative and must not leak as passthrough
            # events on the Responses wire.
            return self._start_openai_legacy_stream(rid, d.get("model"))

        events = self._start_openai_legacy_stream(rid, d.get("model"))
        if self._openai_legacy_response_id is not None:
            rid = self._openai_legacy_response_id

        item_id_text = f"item_{rid}_0"
        if has_text:
            if not self._openai_legacy_text_started:
                events.extend(
                    [
                        self._next(
                            type=ResponsesSemanticEventType.OUTPUT_ITEM_ADDED,
                            response_id=rid,
                            output_index=0,
                            item_id=item_id_text,
                            item={
                                "id": item_id_text,
                                "type": "message",
                                "role": "assistant",
                                "status": "in_progress",
                                "content": [],
                            },
                        ),
                        self._next(
                            type=ResponsesSemanticEventType.CONTENT_PART_ADDED,
                            response_id=rid,
                            output_index=0,
                            content_index=0,
                            item_id=item_id_text,
                            part={"type": "output_text", "text": ""},
                        ),
                    ]
                )
                self._openai_legacy_text_started = True
            self._openai_legacy_text_fragments.append(content)
            events.append(
                self._next(
                    type=ResponsesSemanticEventType.TEXT_DELTA,
                    response_id=rid,
                    output_index=0,
                    content_index=0,
                    item_id=item_id_text,
                    delta=content,
                )
            )

        if has_tools:
            for idx, tc_el in enumerate(tool_calls_list):
                if not isinstance(tc_el, dict):
                    continue
                call_id = tc_el.get("id") or tc_el.get("call_id")
                call_id_s = str(call_id) if call_id is not None else f"call_{idx}"
                fn = tc_el.get("function")
                fn_d = fn if isinstance(fn, dict) else {}
                name_val = fn_d.get("name")
                name_s = str(name_val) if isinstance(name_val, str) else ""
                args_val = fn_d.get("arguments")
                args_s = args_val if isinstance(args_val, str) else ""
                # OpenCode rejects completed function_call items with empty args.
                # Wait for a domain chunk that carries the final arguments payload.
                if not args_s.strip():
                    continue
                if not name_s.strip():
                    # Never emit unnamed tool calls on the Responses wire.
                    continue
                # Dedupe: a prior args.delta may already have finalized this
                # call_id (or its fc_/call_ alias). OpenCode disconnects or
                # double-executes when the same tool appears twice.
                already = {
                    str(it.get("call_id") or it.get("id") or "")
                    for it in self._legacy_function_call_items
                    if isinstance(it, dict)
                }
                if call_id_s in already:
                    continue
                if name_s:
                    self._remember_function_name(
                        item_id=call_id_s, call_id=call_id_s, name=name_s
                    )
                events.append(
                    self._next(
                        type=ResponsesSemanticEventType.OUTPUT_ITEM_ADDED,
                        response_id=rid,
                        output_index=idx,
                        content_index=0,
                        item_id=call_id_s,
                        name=name_s or None,
                        item={
                            "id": call_id_s,
                            "type": "function_call",
                            "status": "in_progress",
                            "name": name_s,
                            "call_id": call_id_s,
                            "arguments": "",
                        },
                    )
                )
                events.append(
                    self._next(
                        type=ResponsesSemanticEventType.TOOL_CALL_ARGS_DELTA,
                        response_id=rid,
                        output_index=idx,
                        content_index=0,
                        item_id=call_id_s,
                        name=name_s or None,
                        delta=args_s,
                    )
                )
                events.append(
                    self._next(
                        type=ResponsesSemanticEventType.TOOL_CALL_ARGS_DONE,
                        response_id=rid,
                        output_index=idx,
                        content_index=0,
                        item_id=call_id_s,
                        name=name_s or None,
                        text=args_s,
                    )
                )
                done_item = self._build_function_call_item(
                    call_id_s,
                    args_s,
                    name=name_s,
                    call_id=call_id_s,
                )
                self._legacy_function_call_items.append(dict(done_item))
                events.append(
                    self._next(
                        type=ResponsesSemanticEventType.OUTPUT_ITEM_DONE,
                        response_id=rid,
                        output_index=idx,
                        content_index=0,
                        item_id=call_id_s,
                        name=name_s or None,
                        item=done_item,
                    )
                )

        if has_finish:
            error_raw = d.get("error")
            if isinstance(error_raw, dict) or str(finish_reason) == "error":
                error = (
                    dict(error_raw)
                    if isinstance(error_raw, dict)
                    else {
                        "message": "ACP backend stream failed",
                        "type": "backend_error",
                    }
                )
                events.append(
                    self._next(
                        type=ResponsesSemanticEventType.RESPONSE_FAILED,
                        response_id=rid,
                        error=error,
                        response={"id": rid, "object": "response", "error": error},
                    )
                )
                return events
            text = "".join(self._openai_legacy_text_fragments)
            output: list[dict[str, Any]] = []
            if self._openai_legacy_text_started:
                completed_item = self._build_message_item(item_id_text, text)
                events.extend(self._finalize_openai_legacy_text(rid))
                output.append(completed_item)
            # Include function_call items so clients linking via
            # response.completed.output (and OpenCode tool loops) see call_id.
            output.extend(dict(item) for item in self._legacy_function_call_items)
            events.append(
                self._next(
                    type=ResponsesSemanticEventType.RESPONSE_COMPLETED,
                    response_id=rid,
                    response={
                        "id": rid,
                        "object": "response",
                        "status": "completed",
                        "model": d.get("model"),
                        "output": output,
                    },
                )
            )
        return events

    def _start_openai_legacy_stream(
        self, rid: str, model: Any
    ) -> list[ResponsesSemanticEvent]:
        if self._openai_legacy_stream_started:
            return []

        self._openai_legacy_response_id = rid
        self._openai_legacy_model = model if isinstance(model, str) else None
        self._openai_legacy_stream_started = True
        return [
            self._next(
                type=ResponsesSemanticEventType.RESPONSE_CREATED,
                response_id=rid,
                response={
                    "id": rid,
                    "model": model,
                    "object": "response",
                },
            ),
            self._next(
                type=ResponsesSemanticEventType.RESPONSE_IN_PROGRESS,
                response_id=rid,
            ),
        ]

    def _finalize_openai_legacy_text(self, rid: str) -> list[ResponsesSemanticEvent]:
        if self._openai_legacy_text_finalized or not self._openai_legacy_text_started:
            return []
        self._openai_legacy_text_finalized = True
        text = "".join(self._openai_legacy_text_fragments)
        item_id = f"item_{rid}_0"
        completed_item = self._build_message_item(item_id, text)
        return [
            self._next(
                type=ResponsesSemanticEventType.TEXT_DONE,
                response_id=rid,
                output_index=0,
                content_index=0,
                item_id=item_id,
                text=text,
            ),
            self._next(
                type=ResponsesSemanticEventType.CONTENT_PART_DONE,
                response_id=rid,
                output_index=0,
                content_index=0,
                item_id=item_id,
                part=self._build_output_text_part(text),
            ),
            self._next(
                type=ResponsesSemanticEventType.OUTPUT_ITEM_DONE,
                response_id=rid,
                output_index=0,
                item_id=item_id,
                item=completed_item,
            ),
        ]

    def _map_openai(self, d: dict[str, Any]) -> list[ResponsesSemanticEvent]:
        rid = self._consume_openai_response_id(d)
        et = str(d.get("type") or "")

        legacy = self._try_openai_legacy_chat(d, rid)
        if legacy is not None:
            return legacy

        if et == "response.created":
            return [
                self._next(
                    type=ResponsesSemanticEventType.RESPONSE_CREATED,
                    response_id=rid,
                    response=(
                        d.get("response") if isinstance(d.get("response"), dict) else {}
                    ),
                )
            ]
        if et == "response.in_progress":
            return [
                self._next(
                    type=ResponsesSemanticEventType.RESPONSE_IN_PROGRESS,
                    response_id=rid,
                    response=(
                        d.get("response") if isinstance(d.get("response"), dict) else {}
                    ),
                )
            ]
        if et == "response.output_item.added":
            item_raw = d.get("item")
            item: dict[str, Any] = item_raw if isinstance(item_raw, dict) else {}
            item_id = (
                item.get("id") if isinstance(item.get("id"), str) else d.get("item_id")
            )
            item_id_s = str(item_id) if item_id is not None else None
            call_id_raw = item.get("call_id")
            call_id_s = (
                str(call_id_raw) if isinstance(call_id_raw, str) and call_id_raw else None
            )
            name_raw = item.get("name")
            if isinstance(name_raw, str) and name_raw.strip():
                self._remember_function_name(
                    item_id=item_id_s, call_id=call_id_s, name=name_raw
                )
            return [
                self._next(
                    type=ResponsesSemanticEventType.OUTPUT_ITEM_ADDED,
                    response_id=rid,
                    output_index=_coerce_int(d.get("output_index")),
                    content_index=_coerce_int(d.get("content_index")),
                    item_id=item_id_s,
                    name=name_raw if isinstance(name_raw, str) and name_raw.strip() else None,
                    item=dict(item),
                )
            ]
        if et == "response.output_item.done":
            item_raw = d.get("item")
            item_done: dict[str, Any] = item_raw if isinstance(item_raw, dict) else {}
            item_id = (
                item_done.get("id")
                if isinstance(item_done.get("id"), str)
                else d.get("item_id")
            )
            item_id_s = str(item_id) if item_id is not None else None
            call_id_raw = item_done.get("call_id")
            call_id_s = (
                str(call_id_raw) if isinstance(call_id_raw, str) and call_id_raw else None
            )
            name_raw = item_done.get("name")
            if isinstance(name_raw, str) and name_raw.strip():
                self._remember_function_name(
                    item_id=item_id_s, call_id=call_id_s, name=name_raw
                )
            elif item_id_s or call_id_s:
                # Carry name from earlier output_item.added when done omits it.
                resolved = self._lookup_function_name(item_id_s, call_id_s)
                if resolved:
                    item_done = dict(item_done)
                    item_done["name"] = resolved
                    name_raw = resolved
            # Ensure function_call items always expose call_id for OpenCode
            # function_call_output linking (item.id may be fc_..., call_id call_...).
            if str(item_done.get("type") or "") == "function_call":
                item_done = dict(item_done)
                if not (
                    isinstance(item_done.get("call_id"), str)
                    and str(item_done.get("call_id")).strip()
                ):
                    item_done["call_id"] = call_id_s or item_id_s
            return [
                self._next(
                    type=ResponsesSemanticEventType.OUTPUT_ITEM_DONE,
                    response_id=rid,
                    output_index=_coerce_int(d.get("output_index")),
                    content_index=_coerce_int(d.get("content_index")),
                    item_id=item_id_s,
                    name=(
                        name_raw
                        if isinstance(name_raw, str) and name_raw.strip()
                        else None
                    ),
                    item=dict(item_done),
                )
            ]
        if et == "response.content_part.added":
            part_raw = d.get("part")
            part: dict[str, Any] = part_raw if isinstance(part_raw, dict) else {}
            item_id = d.get("item_id")
            item_id_s = str(item_id) if item_id is not None else None
            return [
                self._next(
                    type=ResponsesSemanticEventType.CONTENT_PART_ADDED,
                    response_id=rid,
                    output_index=_coerce_int(d.get("output_index")),
                    content_index=_coerce_int(d.get("content_index")),
                    item_id=item_id_s,
                    part=dict(part),
                )
            ]
        if et == "response.content_part.done":
            item_id = d.get("item_id")
            item_id_s = str(item_id) if item_id is not None else None
            return [
                self._next(
                    type=ResponsesSemanticEventType.CONTENT_PART_DONE,
                    response_id=rid,
                    output_index=_coerce_int(d.get("output_index")),
                    content_index=_coerce_int(d.get("content_index")),
                    item_id=item_id_s,
                )
            ]
        if et == "response.output_text.delta":
            item_id = d.get("item_id")
            item_id_s = str(item_id) if item_id is not None else None
            return [
                self._next(
                    type=ResponsesSemanticEventType.TEXT_DELTA,
                    response_id=rid,
                    output_index=_coerce_int(d.get("output_index")),
                    content_index=_coerce_int(d.get("content_index")),
                    item_id=item_id_s,
                    delta=_extract_text_delta(d.get("delta")),
                )
            ]
        if et == "response.output_text.done":
            item_id = d.get("item_id")
            item_id_s = str(item_id) if item_id is not None else None
            text_val = d.get("text")
            text_s = text_val if isinstance(text_val, str) else ""
            return [
                self._next(
                    type=ResponsesSemanticEventType.TEXT_DONE,
                    response_id=rid,
                    output_index=_coerce_int(d.get("output_index")),
                    content_index=_coerce_int(d.get("content_index")),
                    item_id=item_id_s,
                    text=text_s,
                )
            ]
        if et == "response.function_call_arguments.delta":
            item_id = d.get("item_id")
            item_id_s = str(item_id) if item_id is not None else None
            call_id_raw = d.get("call_id")
            call_id_s = (
                str(call_id_raw) if isinstance(call_id_raw, str) and call_id_raw else None
            )
            wire_name = d.get("name")
            resolved_name = (
                wire_name
                if isinstance(wire_name, str) and wire_name.strip()
                else self._lookup_function_name(item_id_s, call_id_s)
            )
            if isinstance(resolved_name, str) and resolved_name.strip():
                self._remember_function_name(
                    item_id=item_id_s, call_id=call_id_s, name=resolved_name
                )
            delta_raw = d.get("delta")
            if isinstance(delta_raw, str):
                delta_s = delta_raw
            else:
                delta_s = _extract_text_delta(delta_raw)
                if not delta_s and delta_raw is not None:
                    try:
                        delta_s = json.dumps(delta_raw, ensure_ascii=False)
                    except (TypeError, ValueError):
                        delta_s = str(delta_raw)
            return [
                self._next(
                    type=ResponsesSemanticEventType.TOOL_CALL_ARGS_DELTA,
                    response_id=rid,
                    output_index=_coerce_int(d.get("output_index")),
                    content_index=_coerce_int(d.get("content_index")),
                    item_id=item_id_s,
                    name=resolved_name if isinstance(resolved_name, str) else None,
                    delta=delta_s,
                )
            ]
        if et == "response.function_call_arguments.done":
            item_id = d.get("item_id")
            item_id_s = str(item_id) if item_id is not None else None
            call_id_raw = d.get("call_id")
            call_id_s = (
                str(call_id_raw) if isinstance(call_id_raw, str) and call_id_raw else None
            )
            wire_name = d.get("name")
            resolved_name = (
                wire_name
                if isinstance(wire_name, str) and wire_name.strip()
                else self._lookup_function_name(item_id_s, call_id_s)
            )
            if isinstance(resolved_name, str) and resolved_name.strip():
                self._remember_function_name(
                    item_id=item_id_s, call_id=call_id_s, name=resolved_name
                )
            args_val = d.get("arguments")
            args_s = args_val if isinstance(args_val, str) else ""
            return [
                self._next(
                    type=ResponsesSemanticEventType.TOOL_CALL_ARGS_DONE,
                    response_id=rid,
                    output_index=_coerce_int(d.get("output_index")),
                    content_index=_coerce_int(d.get("content_index")),
                    item_id=item_id_s,
                    name=resolved_name if isinstance(resolved_name, str) else None,
                    text=args_s,
                )
            ]
        if et in {"response.completed", "response.done"}:
            prefix: list[ResponsesSemanticEvent] = []
            raw_completed_response = d.get("response")
            completed_response: dict[str, Any] = (
                dict(raw_completed_response)
                if isinstance(raw_completed_response, dict)
                else {}
            )
            if self._openai_legacy_stream_started:
                rid = self._openai_legacy_response_id or rid
                prefix = self._finalize_openai_legacy_text(rid)
                completed_response["id"] = rid
                if self._openai_legacy_text_started:
                    completed_response["output"] = [
                        self._build_message_item(
                            f"item_{rid}_0",
                            "".join(self._openai_legacy_text_fragments),
                        )
                    ]
            return [
                *prefix,
                self._next(
                    type=ResponsesSemanticEventType.RESPONSE_COMPLETED,
                    response_id=rid,
                    response=completed_response,
                ),
            ]
        if et == "response.failed":
            response_obj = (
                d.get("response") if isinstance(d.get("response"), dict) else {}
            )
            err = response_obj.get("error") if isinstance(response_obj, dict) else None
            if not isinstance(err, dict):
                err = d.get("error") if isinstance(d.get("error"), dict) else {}
            return [
                self._next(
                    type=ResponsesSemanticEventType.RESPONSE_FAILED,
                    response_id=rid,
                    error=dict(err) if isinstance(err, dict) else {"details": err},
                    response=response_obj if isinstance(response_obj, dict) else {},
                )
            ]
        if et == "response.incomplete":
            return [
                self._next(
                    type=ResponsesSemanticEventType.RESPONSE_INCOMPLETE,
                    response_id=rid,
                    response=(
                        d.get("response") if isinstance(d.get("response"), dict) else {}
                    ),
                )
            ]

        if (
            not et
            and isinstance(d.get("id"), str)
            and isinstance(d.get("output"), list)
        ):
            keys = set(d.keys())
            if (
                d.get("object") == "response"
                or str(d.get("status") or "") in {"completed", "complete"}
                or keys <= {"id", "output"}
            ):
                rid = self._consume_openai_response_id(d)
                return [
                    self._next(
                        type=ResponsesSemanticEventType.RESPONSE_COMPLETED,
                        response_id=rid,
                        response=dict(d),
                    )
                ]

        if et.startswith("response."):
            return [
                self._next(
                    type=ResponsesSemanticEventType.PASSTHROUGH,
                    response_id=rid,
                    output_index=_coerce_int(d.get("output_index")),
                    content_index=_coerce_int(d.get("content_index")),
                    item_id=str(d["item_id"]) if d.get("item_id") is not None else None,
                    raw=dict(d),
                )
            ]

        return [
            self._next(
                type=ResponsesSemanticEventType.PASSTHROUGH,
                response_id=rid,
                raw=dict(d),
            )
        ]

    def _map_anthropic(self, d: dict[str, Any]) -> list[ResponsesSemanticEvent]:
        rid = self._default_response_id
        et = str(d.get("type") or "")

        if et == "message_start":
            msg_raw = d.get("message")
            msg: dict[str, Any] = msg_raw if isinstance(msg_raw, dict) else {}
            msg_id = msg.get("id")
            if isinstance(msg_id, str) and msg_id:
                self._anthropic_message_id = msg_id
            return [
                self._next(
                    type=ResponsesSemanticEventType.RESPONSE_CREATED,
                    response_id=rid,
                    response={"id": rid, "message": dict(msg)},
                ),
                self._next(
                    type=ResponsesSemanticEventType.RESPONSE_IN_PROGRESS,
                    response_id=rid,
                ),
            ]

        if et == "content_block_start":
            index = _coerce_int(d.get("index")) or 0
            block_raw = d.get("content_block")
            block: dict[str, Any] = block_raw if isinstance(block_raw, dict) else {}
            btype = str(block.get("type") or "")
            tool_id = block.get("id")
            item_id = (
                tool_id
                if btype == "tool_use" and isinstance(tool_id, str) and tool_id
                else f"anthropic_block_{index}"
            )
            item_type = "tool_use" if btype == "tool_use" else "message"
            item_payload: dict[str, Any] = {
                "id": item_id,
                "type": item_type,
                "role": "assistant",
            }
            if isinstance(block.get("name"), str) and block.get("name"):
                item_payload["name"] = block["name"]
            self._anthropic_items[index] = dict(item_payload)
            self._anthropic_text_buffers[index] = []
            self._anthropic_tool_arg_buffers[index] = []
            return [
                self._next(
                    type=ResponsesSemanticEventType.OUTPUT_ITEM_ADDED,
                    response_id=rid,
                    output_index=index,
                    content_index=index,
                    item_id=item_id,
                    item=item_payload,
                ),
                self._next(
                    type=ResponsesSemanticEventType.CONTENT_PART_ADDED,
                    response_id=rid,
                    output_index=index,
                    content_index=index,
                    item_id=item_id,
                    part=dict(block),
                ),
            ]

        if et == "content_block_delta":
            index = _coerce_int(d.get("index")) or 0
            item_id = f"anthropic_block_{index}"
            delta_raw = d.get("delta")
            delta: dict[str, Any] = delta_raw if isinstance(delta_raw, dict) else {}
            dtype = str(delta.get("type") or "")
            if dtype == "text_delta":
                text_val = delta.get("text")
                text_s = text_val if isinstance(text_val, str) else ""
                if text_s:
                    self._anthropic_text_buffers.setdefault(index, []).append(text_s)
                return [
                    self._next(
                        type=ResponsesSemanticEventType.TEXT_DELTA,
                        response_id=rid,
                        output_index=index,
                        content_index=index,
                        item_id=item_id,
                        delta=text_s,
                    )
                ]
            if dtype == "input_json_delta":
                pj = delta.get("partial_json")
                frag = pj if isinstance(pj, str) else ""
                if frag:
                    self._anthropic_tool_arg_buffers.setdefault(index, []).append(frag)
                return [
                    self._next(
                        type=ResponsesSemanticEventType.TOOL_CALL_ARGS_DELTA,
                        response_id=rid,
                        output_index=index,
                        content_index=index,
                        item_id=item_id,
                        delta=frag,
                    )
                ]
            return [
                self._next(
                    type=ResponsesSemanticEventType.PASSTHROUGH,
                    response_id=rid,
                    raw=dict(d),
                )
            ]

        if et == "content_block_stop":
            index = _coerce_int(d.get("index")) or 0
            item_id = f"anthropic_block_{index}"
            item_payload = dict(
                self._anthropic_items.get(index) or {"id": item_id, "type": "message"}
            )
            tool_args = "".join(self._anthropic_tool_arg_buffers.get(index, []))
            text = "".join(self._anthropic_text_buffers.get(index, []))
            if tool_args:
                item_payload = self._build_function_call_item(item_id, tool_args)
            elif text:
                item_payload = self._build_message_item(item_id, text)
            item_payload.setdefault("status", "completed")
            return [
                self._next(
                    type=ResponsesSemanticEventType.CONTENT_PART_DONE,
                    response_id=rid,
                    output_index=index,
                    content_index=index,
                    item_id=item_id,
                ),
                self._next(
                    type=ResponsesSemanticEventType.OUTPUT_ITEM_DONE,
                    response_id=rid,
                    output_index=index,
                    content_index=index,
                    item_id=item_id,
                    item=item_payload,
                ),
            ]

        if et == "message_stop":
            output_items: list[dict[str, Any]] = []
            sorted_indices = sorted(self._anthropic_items)
            text_only_indices = [
                idx
                for idx in sorted_indices
                if "".join(self._anthropic_text_buffers.get(idx, []))
                and not "".join(self._anthropic_tool_arg_buffers.get(idx, []))
            ]
            single_text_only = len(text_only_indices) == 1

            for index in sorted_indices:
                item_payload = self._anthropic_items[index]
                item_id = str(item_payload.get("id") or f"anthropic_block_{index}")
                tool_args = "".join(self._anthropic_tool_arg_buffers.get(index, []))
                text = "".join(self._anthropic_text_buffers.get(index, []))
                if tool_args:
                    fn_item = self._build_function_call_item(item_id, tool_args)
                    name = item_payload.get("name")
                    if isinstance(name, str) and name:
                        fn_item["name"] = name
                    output_items.append(fn_item)
                elif text:
                    msg_id = (
                        (self._anthropic_message_id or item_id)
                        if single_text_only
                        else item_id
                    )
                    output_items.append(self._build_message_item(msg_id, text))
            return [
                self._next(
                    type=ResponsesSemanticEventType.RESPONSE_COMPLETED,
                    response_id=rid,
                    response={"id": rid, "output": output_items},
                )
            ]

        return [
            self._next(
                type=ResponsesSemanticEventType.PASSTHROUGH,
                response_id=rid,
                raw=dict(d),
            )
        ]

    def _map_gemini(self, d: dict[str, Any]) -> list[ResponsesSemanticEvent]:
        rid = self._default_response_id
        out: list[ResponsesSemanticEvent] = []
        cands = d.get("candidates")
        if not isinstance(cands, list) or not cands:
            return [
                self._next(
                    type=ResponsesSemanticEventType.PASSTHROUGH,
                    response_id=rid,
                    raw=dict(d),
                )
            ]

        c0 = cands[0] if isinstance(cands[0], dict) else {}
        content_raw = c0.get("content")
        content: dict[str, Any] = content_raw if isinstance(content_raw, dict) else {}
        parts = content.get("parts")
        has_text = False
        if isinstance(parts, list):
            for part in parts:
                if isinstance(part, dict) and isinstance(part.get("text"), str):
                    has_text = True
                    break

        finish = c0.get("finishReason")
        finish_s = finish if isinstance(finish, str) else ""
        will_complete = bool(finish_s)

        def _ensure_gemini_lifecycle() -> None:
            nonlocal out
            if self._gemini_lifecycle_started:
                return
            out.extend(
                [
                    self._next(
                        type=ResponsesSemanticEventType.RESPONSE_CREATED,
                        response_id=rid,
                        response={"id": rid},
                    ),
                    self._next(
                        type=ResponsesSemanticEventType.RESPONSE_IN_PROGRESS,
                        response_id=rid,
                    ),
                ]
            )
            self._gemini_lifecycle_started = True

        if isinstance(parts, list):
            for idx, part in enumerate(parts):
                if not isinstance(part, dict):
                    continue
                if isinstance(part.get("text"), str):
                    _ensure_gemini_lifecycle()
                    self._gemini_text_fragments.append(part["text"])
                    out.append(
                        self._next(
                            type=ResponsesSemanticEventType.TEXT_DELTA,
                            response_id=rid,
                            output_index=0,
                            content_index=idx,
                            item_id="gemini_message_0",
                            delta=part["text"],
                        )
                    )

        if will_complete and not has_text:
            _ensure_gemini_lifecycle()

        if will_complete:
            output_items: list[dict[str, Any]] = []
            text = "".join(self._gemini_text_fragments)
            if text:
                output_items.append(self._build_message_item("gemini_message_0", text))
            out.append(
                self._next(
                    type=ResponsesSemanticEventType.RESPONSE_COMPLETED,
                    response_id=rid,
                    response={
                        "id": rid,
                        "finishReason": finish_s,
                        "output": output_items,
                    },
                )
            )

        return out
