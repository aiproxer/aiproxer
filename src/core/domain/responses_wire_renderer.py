"""Render Responses semantic events into canonical SSE or WebSocket wire frames."""

from __future__ import annotations

import json
from collections.abc import AsyncGenerator, AsyncIterator
from typing import Any, Literal

from pydantic import ValidationError

from src.core.domain.responses_domain import ResponsesOutputItem
from src.core.domain.responses_resolved_session import ResponsesHistoryItem
from src.core.domain.responses_semantic_events import (
    ResponsesSemanticEvent,
    ResponsesSemanticEventType,
)
from src.core.interfaces.responses_session_store_interface import IResponsesSessionStore

_TERMINAL: frozenset[ResponsesSemanticEventType] = frozenset(
    {
        ResponsesSemanticEventType.RESPONSE_COMPLETED,
        ResponsesSemanticEventType.RESPONSE_FAILED,
        ResponsesSemanticEventType.RESPONSE_INCOMPLETE,
    }
)

_WIRE_TYPE: dict[ResponsesSemanticEventType, str] = {
    ResponsesSemanticEventType.RESPONSE_CREATED: "response.created",
    ResponsesSemanticEventType.RESPONSE_IN_PROGRESS: "response.in_progress",
    ResponsesSemanticEventType.OUTPUT_ITEM_ADDED: "response.output_item.added",
    ResponsesSemanticEventType.CONTENT_PART_ADDED: "response.content_part.added",
    ResponsesSemanticEventType.TEXT_DELTA: "response.output_text.delta",
    ResponsesSemanticEventType.TEXT_DONE: "response.output_text.done",
    ResponsesSemanticEventType.TOOL_CALL_ARGS_DELTA: "response.function_call_arguments.delta",
    ResponsesSemanticEventType.TOOL_CALL_ARGS_DONE: "response.function_call_arguments.done",
    ResponsesSemanticEventType.CONTENT_PART_DONE: "response.content_part.done",
    ResponsesSemanticEventType.OUTPUT_ITEM_DONE: "response.output_item.done",
    ResponsesSemanticEventType.RESPONSE_COMPLETED: "response.completed",
    ResponsesSemanticEventType.RESPONSE_FAILED: "response.failed",
    ResponsesSemanticEventType.RESPONSE_INCOMPLETE: "response.incomplete",
}


def _maybe_output_item(item: dict[str, Any]) -> ResponsesOutputItem | None:
    try:
        return ResponsesOutputItem.model_validate(item)
    except ValidationError:
        return None


def _terminal_response_id(event: ResponsesSemanticEvent, fallback: str) -> str:
    resp = event.response
    if isinstance(resp, dict):
        rid = resp.get("id")
        if isinstance(rid, str) and rid:
            return rid
    return fallback


def _wire_payload_for_semantic(
    event: ResponsesSemanticEvent,
    *,
    realtime_ws_terminal: bool,
) -> dict[str, Any]:
    if event.type == ResponsesSemanticEventType.PASSTHROUGH:
        base: dict[str, Any] = (
            dict(event.raw) if isinstance(event.raw, dict) else {"type": "passthrough"}
        )
        base["sequence_number"] = event.sequence_number
        return base

    wtype = _WIRE_TYPE.get(event.type)
    if wtype is None:
        return {
            "type": "passthrough",
            "sequence_number": event.sequence_number,
            "response_id": event.response_id,
        }

    if (
        event.type == ResponsesSemanticEventType.RESPONSE_COMPLETED
        and realtime_ws_terminal
    ):
        wtype = "response.done"

    out: dict[str, Any] = {
        "type": wtype,
        "sequence_number": event.sequence_number,
    }

    if event.output_index is not None:
        out["output_index"] = event.output_index
    if event.content_index is not None:
        out["content_index"] = event.content_index
    if event.item_id is not None:
        out["item_id"] = event.item_id

    if (
        event.type
        in {
            ResponsesSemanticEventType.TEXT_DELTA,
            ResponsesSemanticEventType.TOOL_CALL_ARGS_DELTA,
        }
        and event.delta is not None
    ):
        out["delta"] = event.delta

    if event.type == ResponsesSemanticEventType.TEXT_DONE and event.text is not None:
        out["text"] = event.text

    if (
        event.type == ResponsesSemanticEventType.TOOL_CALL_ARGS_DONE
        and event.text is not None
    ):
        out["arguments"] = event.text

    if (
        event.type
        in {
            ResponsesSemanticEventType.TOOL_CALL_ARGS_DELTA,
            ResponsesSemanticEventType.TOOL_CALL_ARGS_DONE,
        }
        and isinstance(event.name, str)
        and event.name.strip()
    ):
        out["name"] = event.name

    if event.type in {
        ResponsesSemanticEventType.OUTPUT_ITEM_ADDED,
        ResponsesSemanticEventType.OUTPUT_ITEM_DONE,
    } and isinstance(event.item, dict):
        out["item"] = dict(event.item)

    if event.type in {
        ResponsesSemanticEventType.CONTENT_PART_ADDED,
    } and isinstance(event.part, dict):
        out["part"] = dict(event.part)

    if event.type in {
        ResponsesSemanticEventType.RESPONSE_CREATED,
        ResponsesSemanticEventType.RESPONSE_IN_PROGRESS,
        ResponsesSemanticEventType.RESPONSE_COMPLETED,
        ResponsesSemanticEventType.RESPONSE_INCOMPLETE,
        ResponsesSemanticEventType.RESPONSE_FAILED,
    } and isinstance(event.response, dict):
        out["response"] = dict(event.response)

    if event.type == ResponsesSemanticEventType.RESPONSE_FAILED and isinstance(
        event.error, dict
    ):
        out["error"] = dict(event.error)

    return out


class ResponsesWireRenderer:
    def __init__(
        self,
        session_store: IResponsesSessionStore,
        *,
        transport: Literal["sse", "websocket"] = "sse",
        realtime_websocket_terminal: bool = False,
    ) -> None:
        self._session_store = session_store
        self._transport = transport
        self._realtime_ws_terminal = realtime_websocket_terminal

    def _sse_line(self, payload: dict[str, Any]) -> str:
        return f"data: {json.dumps(payload, ensure_ascii=False)}\n\n"

    async def render(
        self,
        events: (
            AsyncGenerator[ResponsesSemanticEvent, None]
            | AsyncIterator[ResponsesSemanticEvent]
        ),
        response_id: str,
        *,
        instructions: str | None = None,
        history_items: list[ResponsesHistoryItem] | None = None,
        ttl_seconds: int | None = None,
        emit_done_sentinel: bool = True,
    ) -> AsyncGenerator[str | dict[str, Any], None]:
        collected: list[ResponsesOutputItem] = []
        terminal: ResponsesSemanticEvent | None = None
        realtime_flag = self._realtime_ws_terminal and self._transport == "websocket"
        deferred_ws_terminal_payloads: list[dict[str, Any]] = []

        async for event in events:
            if (
                event.type == ResponsesSemanticEventType.OUTPUT_ITEM_DONE
                and isinstance(event.item, dict)
            ):
                oi = _maybe_output_item(event.item)
                if oi is not None:
                    collected.append(oi)

            if event.type in _TERMINAL:
                terminal = event

            payload = _wire_payload_for_semantic(
                event,
                realtime_ws_terminal=realtime_flag,
            )

            if self._transport == "sse":
                yield self._sse_line(payload)
            elif event.type in _TERMINAL:
                deferred_ws_terminal_payloads.append(payload)
            else:
                yield payload

        if terminal is None:
            if self._transport == "sse":
                if emit_done_sentinel:
                    yield "data: [DONE]\n\n"
            else:
                synthetic = ResponsesSemanticEvent(
                    type=ResponsesSemanticEventType.RESPONSE_INCOMPLETE,
                    response_id=response_id,
                    sequence_number=0,
                    response={"id": response_id},
                )
                yield _wire_payload_for_semantic(
                    synthetic,
                    realtime_ws_terminal=realtime_flag,
                )
            return

        tid = _terminal_response_id(terminal, response_id)
        if terminal.type != ResponsesSemanticEventType.RESPONSE_COMPLETED:
            if self._transport == "sse" and emit_done_sentinel:
                yield "data: [DONE]\n\n"
            elif self._transport == "websocket":
                for ws_payload in deferred_ws_terminal_payloads:
                    yield ws_payload
            return

        if terminal.type == ResponsesSemanticEventType.RESPONSE_COMPLETED:
            resp = terminal.response
            if isinstance(resp, dict):
                raw_out = resp.get("output")
                if isinstance(raw_out, list):
                    terminal_items: list[ResponsesOutputItem] = []
                    for el in raw_out:
                        if isinstance(el, dict):
                            oi = _maybe_output_item(el)
                            if oi is not None:
                                terminal_items.append(oi)
                    if terminal_items:
                        collected = terminal_items

        await self._session_store.store(
            tid,
            collected,
            ttl_seconds,
            instructions=instructions,
            history_items=[*(history_items or []), *collected],
        )

        if self._transport == "sse":
            if emit_done_sentinel:
                yield "data: [DONE]\n\n"
        else:
            for ws_payload in deferred_ws_terminal_payloads:
                yield ws_payload
