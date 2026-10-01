from __future__ import annotations

import json
import logging
import shlex
from contextvars import ContextVar
from typing import Any, cast

from src.core.app.constants.logging_constants import TRACE_LEVEL
from src.core.domain.chat import FunctionCall, ToolCall
from src.core.domain.tool_text_renderer import render_tool_call
from src.core.domain.translation_utils.content_utils import (
    ReasoningSummarySanitizerState,
    sanitize_reasoning_summary_stream_delta,
)
from src.core.domain.translation_utils.tool_call_state import (
    accumulate_tool_call_arguments,
    assign_tool_call_index,
    cache_function_name,
    clear_tool_call_arguments,
    get_accumulated_tool_call_arguments,
    get_cached_function_name,
    reset_tool_call_state,
)
from src.core.domain.translators.responses.streaming_parse import (
    parse_responses_stream_chunk,
)

logger = logging.getLogger(__name__)


def _resolve_function_call_name(
    chunk: dict[str, Any],
    *,
    item: dict[str, Any] | None = None,
) -> str:
    """Resolve a function-call name from the event, item, or prior cache.

    Responses streams commonly put ``name`` only on ``output_item.added`` while
    ``function_call_arguments.*`` carry ``item_id`` (item.id) which may differ
    from ``call_id``. Look up both ids so later events keep the name.
    """
    wire = chunk.get("name")
    if isinstance(wire, str) and wire.strip():
        return wire
    if item is not None:
        item_name = item.get("name")
        if isinstance(item_name, str) and item_name.strip():
            return item_name
    item_id = chunk.get("item_id")
    call_id = chunk.get("call_id")
    if item is not None:
        if not isinstance(item_id, str) or not item_id:
            raw_id = item.get("id")
            item_id = raw_id if isinstance(raw_id, str) else item_id
        if not isinstance(call_id, str) or not call_id:
            raw_call = item.get("call_id")
            call_id = raw_call if isinstance(raw_call, str) else call_id
    keys: list[str] = []
    for key in (item_id, call_id):
        if isinstance(key, str) and key and key not in keys:
            keys.append(key)
    return get_cached_function_name(*keys) if keys else ""


def _cache_function_call_name_aliases(
    name: str,
    *,
    item_id: str | None = None,
    call_id: str | None = None,
) -> None:
    if not isinstance(name, str) or not name.strip():
        return
    primary = call_id or item_id
    if not primary:
        return
    aliases: list[str] = []
    if item_id and item_id != primary:
        aliases.append(item_id)
    if call_id and call_id != primary and call_id not in aliases:
        aliases.append(call_id)
    cache_function_name(primary, name, *aliases)



def _local_shell_item_to_arguments_json(item: dict[str, Any]) -> str:
    """Serialize Codex ``local_shell_call`` output items to function ``arguments`` JSON.

    Upstream often sends shell fields on the item itself (``command``, ``description``)
    while ``action`` is missing or ``{}``. Mapping only ``action`` produced ``"{}"``,
    which surfaces in OpenAI-compatible clients as bash/tool calls with empty input.
    """
    action = item.get("action")
    if isinstance(action, str) and action.strip():
        try:
            json.loads(action)
            return action
        except json.JSONDecodeError:
            desc = item.get("description")
            return json.dumps(
                {
                    "command": action.strip(),
                    "description": desc if isinstance(desc, str) else "",
                },
                ensure_ascii=False,
            )
    if isinstance(action, dict) and action:
        return json.dumps(action, ensure_ascii=False)

    for key in ("arguments", "input"):
        raw = item.get(key)
        if isinstance(raw, str) and raw.strip():
            try:
                parsed = json.loads(raw)
                if isinstance(parsed, dict):
                    return json.dumps(parsed, ensure_ascii=False)
            except json.JSONDecodeError:
                desc = item.get("description")
                return json.dumps(
                    {
                        "command": raw.strip(),
                        "description": desc if isinstance(desc, str) else "",
                    },
                    ensure_ascii=False,
                )

    cmd = item.get("command")
    desc_val = item.get("description")
    if desc_val is None:
        desc_val = item.get("summary") or item.get("reason") or ""
    if cmd is not None and cmd != "":
        if isinstance(cmd, str):
            command_str = cmd
        elif isinstance(cmd, list | tuple):
            try:
                command_str = shlex.join(str(x) for x in cmd)
            except (TypeError, ValueError):
                command_str = " ".join(str(x) for x in cmd)
        else:
            command_str = str(cmd)
        payload: dict[str, Any] = {
            "command": command_str,
            "description": desc_val.strip() if isinstance(desc_val, str) else "",
        }
        timeout = item.get("timeout")
        if isinstance(timeout, int | float) and not isinstance(timeout, bool):
            payload["timeout"] = timeout
        for src_key, dest_key in (
            ("workdir", "workdir"),
            ("working_directory", "workdir"),
            ("cwd", "workdir"),
        ):
            v = item.get(src_key)
            if v is not None and v != "":
                payload[dest_key] = v
                break
        return json.dumps(payload, ensure_ascii=False)

    nested = item.get("shell") or item.get("local_shell")
    if isinstance(nested, dict):
        return _local_shell_item_to_arguments_json(nested)

    return "{}"


def _openai_client_shell_tool_name(tool_name: str) -> str:
    """Map Codex-native ``shell`` to ``bash`` for OpenAI-compatible clients (e.g. OpenCode)."""
    lname = (tool_name or "").strip().lower()
    if lname == "shell":
        return "bash"
    return tool_name


def _should_buffer_partial_tool_call(tool_name: str) -> bool:
    """Return True when early placeholder deltas should be suppressed.

    Some OpenAI-compatible coding clients validate tool arguments as soon as the
    first tool delta is seen. Emitting placeholder shell/function-call chunks
    with empty arguments causes immediate client-side validation failures before
    the final `response.output_item.done` event can supply complete arguments.
    """
    lname = (tool_name or "").strip().lower()
    return lname in {"shell", "bash", "local_shell_call", "apply_patch"}


def _normalize_shell_like_tool_arguments_json(
    tool_name: str, arguments_json: str
) -> str:
    """Coerce Codex shell payloads to OpenAI-client-friendly ``bash``-style JSON.

    Codex native tools use ``command`` as a string array. Clients such as OpenCode
    validate a ``bash`` tool with **string** ``command`` and **string**
    ``description``; array-shaped ``command`` or a missing ``description`` yields
    ``undefined`` field errors even when JSON parses successfully. Preserve
    OpenCode's optional ``timeout`` and ``workdir`` fields so timeout control is
    not silently replaced by the client's default.
    """
    lname = (tool_name or "").strip().lower()
    if lname not in ("shell", "bash"):
        return arguments_json
    try:
        obj = json.loads(arguments_json)
    except json.JSONDecodeError:
        return arguments_json
    if not isinstance(obj, dict):
        return arguments_json

    cmd = obj.get("command")
    if isinstance(cmd, list | tuple):
        try:
            cmd_str = shlex.join(str(x) for x in cmd)
        except (TypeError, ValueError):
            cmd_str = " ".join(str(x) for x in cmd)
    elif isinstance(cmd, str):
        cmd_str = cmd
    elif cmd is None:
        cmd_str = ""
    else:
        cmd_str = str(cmd)

    desc = obj.get("description")
    if desc is None:
        desc = obj.get("summary") or obj.get("reason") or ""
    if not isinstance(desc, str):
        desc = str(desc)

    normalized: dict[str, Any] = {"command": cmd_str, "description": desc}

    timeout = obj.get("timeout")
    if isinstance(timeout, int | float) and not isinstance(timeout, bool):
        normalized["timeout"] = timeout

    wd = obj.get("workdir") or obj.get("working_directory") or obj.get("cwd")
    if wd is not None and str(wd).strip():
        normalized["workdir"] = str(wd).strip()

    return json.dumps(normalized, ensure_ascii=False)


# Correlates Responses SSE events that omit `response.id` / top-level `id` with the
# active stream (set from `response.created` or any event that carries an explicit id).
_active_responses_stream_id: ContextVar[str | None] = ContextVar(
    "active_responses_stream_id", default=None
)
_reasoning_summary_sanitizer_state: ContextVar[ReasoningSummarySanitizerState] = (
    ContextVar(
        "reasoning_summary_sanitizer_state",
        default=ReasoningSummarySanitizerState(),
    )
)


def reset_active_responses_stream_context() -> None:
    """Clear active Responses stream id (tests, teardown, or error recovery)."""
    _active_responses_stream_id.set(None)
    _reasoning_summary_sanitizer_state.set(ReasoningSummarySanitizerState())


def responses_to_domain_stream_chunk(chunk: Any) -> dict[str, Any]:
    """Translate an OpenAI Responses streaming chunk to canonical format."""
    import time
    import uuid

    def _extract_text(value: Any) -> str:
        if isinstance(value, str):
            return value
        if isinstance(value, dict):
            if "text" in value:
                return _extract_text(value["text"])
            if "content" in value:
                return _extract_text(value["content"])
            if "value" in value:
                return _extract_text(value["value"])
        if isinstance(value, list):
            parts = [_extract_text(v) for v in value]
            return "".join(part for part in parts if part)
        if value is None:
            return ""
        return str(value)

    parsed = parse_responses_stream_chunk(chunk)
    if parsed.error is not None:
        return parsed.error
    if parsed.chunk is None:
        return {"error": "Invalid chunk format: expected a dictionary"}

    chunk = parsed.chunk
    event_type_from_sse = parsed.event_type_from_sse

    response_payload = chunk.get("response")
    if isinstance(response_payload, dict):
        response_nested_id = response_payload.get("id")
        created = response_payload.get("created")
        model = response_payload.get("model")
    else:
        response_nested_id = None
        created = None
        model = None

    event_type_hint = (chunk.get("type") or event_type_from_sse or "").strip()
    explicit_stream_id: str | None = None
    if isinstance(response_nested_id, str) and response_nested_id:
        explicit_stream_id = response_nested_id
    if explicit_stream_id is None:
        top_id = chunk.get("id")
        if (
            isinstance(top_id, str)
            and top_id
            and (event_type_hint or top_id.startswith("resp_"))
        ):
            # Prefer SSE/body `type` when present. Also accept OpenAI-style ids (`resp_…`);
            # synthetic heartbeats use `resp-` + hex and must not override stream context.
            explicit_stream_id = top_id

    if explicit_stream_id is not None:
        _active_responses_stream_id.set(explicit_stream_id)
        chunk_id = explicit_stream_id
    else:
        from_context = _active_responses_stream_id.get()
        chunk_id = from_context or f"resp-{uuid.uuid4().hex[:16]}"
    created = created or chunk.get("created") or int(time.time())
    model = model or chunk.get("model") or "unknown"
    object_type = chunk.get("object") or "response.chunk"
    index = chunk.get("index", 0)
    event_type = (
        (chunk.get("type") or event_type_from_sse or "").strip()
        if chunk.get("type") or event_type_from_sse
        else ""
    )

    if logger.isEnabledFor(TRACE_LEVEL):
        try:
            logger.log(
                TRACE_LEVEL,
                "Responses event type=%s payload=%s",
                event_type or "<none>",
                json.dumps(chunk)[:400],
            )
        except (TypeError, ValueError, UnicodeEncodeError):
            # JSON serialization errors - TypeError for non-serializable types,
            # ValueError for circular references, UnicodeEncodeError for encoding issues
            logger.log(
                TRACE_LEVEL,
                "Responses event type=%s payload=<non-serializable>",
                event_type or "<none>",
                exc_info=True,
            )

    def _build_chunk(
        delta: dict[str, Any] | None = None,
        finish_reason: str | None = None,
    ) -> dict[str, Any]:
        return {
            "id": chunk_id,
            "object": object_type,
            "created": created,
            "model": model,
            "choices": [
                {
                    "index": index,
                    "delta": delta or {},
                    "finish_reason": finish_reason,
                }
            ],
        }

    def _build_error_chunk(error_payload: Any) -> dict[str, Any]:
        if isinstance(error_payload, dict):
            error_dict = dict(error_payload)
        else:
            error_dict = {"message": str(error_payload), "type": "api_error"}
        error_dict.setdefault("message", "Responses stream reported failure")
        error_dict.setdefault("type", "api_error")
        result = _build_chunk({}, "error")
        result["error"] = error_dict
        return result

    if event_type == "response.output_text.delta":
        delta_payload = chunk.get("delta")
        text = _extract_text(delta_payload)
        if not text:
            return _build_chunk()
        delta_map: dict[str, Any] = {"content": text}
        if isinstance(delta_payload, dict):
            role = delta_payload.get("role")
            if role:
                delta_map["role"] = role
        delta_map.setdefault("role", "assistant")
        return _build_chunk(delta_map)

    if event_type == "response.reasoning_summary_text.delta":
        summary_text = _extract_text(chunk.get("delta"))
        summary_text, sanitizer_state = sanitize_reasoning_summary_stream_delta(
            summary_text,
            _reasoning_summary_sanitizer_state.get(),
            output_index=chunk.get("output_index"),
            summary_index=chunk.get("summary_index"),
        )
        _reasoning_summary_sanitizer_state.set(sanitizer_state)
        if not summary_text:
            return _build_chunk()
        return _build_chunk({"reasoning_summary": summary_text})

    if event_type == "response.reasoning_text.delta":
        reasoning_text = _extract_text(chunk.get("delta"))
        return _build_chunk({"reasoning_content": reasoning_text})

    if event_type == "response.function_call_arguments.delta":
        item_id = chunk.get("item_id")
        # Prefer SIWC/OpenAI call_id (call_...) over item_id (fc_...); OpenCode
        # links function_call_output by call_id.
        call_id = chunk.get("call_id") or item_id
        name = _resolve_function_call_name(chunk)
        delta_payload = chunk.get("delta") or {}
        if isinstance(delta_payload, str):
            arguments_fragment = delta_payload
        else:
            arguments_fragment = _extract_text(delta_payload)
            if not arguments_fragment:
                arguments_fragment = json.dumps(delta_payload)
        # Cache under item_id and call_id so later done events can resolve name.
        if name:
            _cache_function_call_name_aliases(
                name,
                item_id=item_id if isinstance(item_id, str) else None,
                call_id=(
                    chunk.get("call_id")
                    if isinstance(chunk.get("call_id"), str)
                    else (call_id if isinstance(call_id, str) else None)
                ),
            )
        # Accumulate under call_id and item_id — SIWC often keys deltas by
        # item_id (fc_...) while output_item.done prefers call_id (call_...).
        if arguments_fragment:
            accum_keys: list[str] = []
            for accum_key in (call_id, item_id):
                if (
                    isinstance(accum_key, str)
                    and accum_key
                    and accum_key not in accum_keys
                ):
                    accum_keys.append(accum_key)
            for accum_key in accum_keys:
                accumulate_tool_call_arguments(accum_key, arguments_fragment)
        # Never emit domain tool_calls from argument deltas.
        #
        # The Responses semantic pipeline reconstructs wire events via the
        # legacy chat mapper, which treats ANY non-empty tool_calls chunk as a
        # *completed* function_call cycle (added + args.done + item.done).
        # Emitting here AND again from response.output_item.done therefore
        # duplicates the same tool on the OpenCode wire (often once under
        # item_id fc_... and once under call_id call_...), which breaks
        # function_call_output linking and triggers client disconnect/retry
        # loops. Accumulate only; output_item.done is the single emit site
        # (mirrors response.function_call_arguments.done below).
        return _build_chunk()

    if event_type == "response.function_call_arguments.done":
        item_id = chunk.get("item_id")
        call_id = chunk.get("call_id") or item_id
        name = _resolve_function_call_name(chunk)
        arguments = chunk.get("arguments")
        # Re-cache under both ids so output_item.done / client mapping stay named.
        if name:
            _cache_function_call_name_aliases(
                name,
                item_id=item_id if isinstance(item_id, str) else None,
                call_id=(
                    chunk.get("call_id")
                    if isinstance(chunk.get("call_id"), str)
                    else (call_id if isinstance(call_id, str) else None)
                ),
            )
        # The complete tool call will be sent in response.output_item.done event.
        # Just return an empty chunk here to let the client know the event happened.
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "response.function_call_arguments.done: call_id=%s, name=%r, arguments=%r",
                call_id,
                name,
                (str(arguments)[:100] if arguments is not None else None),
            )
        return _build_chunk()

    if event_type == "response.output_item.done":
        item = chunk.get("item") or {}
        item_type = item.get("type")

        if logger.isEnabledFor(TRACE_LEVEL):
            try:
                logger.log(
                    TRACE_LEVEL,
                    "Responses output_item.done item=%s",
                    json.dumps(item)[:400],
                )
            except (TypeError, ValueError, UnicodeEncodeError):
                # JSON serialization errors - TypeError for non-serializable types,
                # ValueError for circular references, UnicodeEncodeError for encoding issues
                logger.log(
                    TRACE_LEVEL,
                    "Responses output_item.done item=<non-serializable>",
                    exc_info=True,
                )

        if item_type == "message":
            role = item.get("role")
            if role:
                return _build_chunk({"role": role})
            return _build_chunk()

        if item_type == "function_call":

            def _needs_accumulated_tool_arguments(val: Any) -> bool:
                return (
                    val is None
                    or val == ""
                    or val == "{}"
                    or (isinstance(val, dict) and len(val) == 0)
                )

            arguments = item.get("arguments", "{}")
            item_id_for_call = (
                str(item.get("id")) if item.get("id") is not None else None
            )
            call_id = (
                item.get("call_id") or item.get("id") or f"call_{uuid.uuid4().hex[:8]}"
            )
            # If arguments is None, empty, just "{}", or empty dict, try accumulated
            # arguments from deltas (API sometimes omits payload on output_item.done).
            lookup_keys = [
                k
                for k in (call_id, item_id_for_call)
                if isinstance(k, str) and k
            ]
            # Dedupe while preserving order
            seen_keys: set[str] = set()
            ordered_keys: list[str] = []
            for k in lookup_keys:
                if k not in seen_keys:
                    seen_keys.add(k)
                    ordered_keys.append(k)
            if _needs_accumulated_tool_arguments(arguments):
                accumulated = ""
                for key in ordered_keys:
                    accumulated = get_accumulated_tool_call_arguments(key)
                    if accumulated and accumulated != "{}":
                        break
                for key in ordered_keys:
                    clear_tool_call_arguments(key)
                if accumulated and accumulated != "{}":
                    arguments = accumulated
                else:
                    arguments = "{}"
            else:
                # We have arguments from the item, clear accumulated state
                for key in ordered_keys:
                    clear_tool_call_arguments(key)
            if not isinstance(arguments, str):
                arguments = json.dumps(arguments) if arguments else "{}"
            tool_name = _resolve_function_call_name(chunk, item=item)
            if not isinstance(tool_name, str) or not tool_name.strip():
                # Never emit unnamed tool calls to clients; harnesses (e.g. pi)
                # persist them and later replay empty-name history that 400s.
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Skipping function_call output_item.done with empty name "
                        "(call_id=%r)",
                        call_id,
                    )
                return _build_chunk()
            _cache_function_call_name_aliases(
                tool_name,
                item_id=str(item.get("id")) if item.get("id") is not None else None,
                call_id=str(call_id) if call_id else None,
            )
            arguments = _normalize_shell_like_tool_arguments_json(tool_name, arguments)
            emit_name = _openai_client_shell_tool_name(tool_name)
            tool_index = assign_tool_call_index(
                chunk_id, chunk.get("output_index"), call_id
            )
            tool_call_obj = ToolCall(
                id=call_id,
                type="function",
                function=FunctionCall(name=emit_name, arguments=arguments),
            )
            tool_text = render_tool_call(tool_call_obj)
            delta = {
                "tool_calls": [
                    {
                        "id": call_id,
                        "index": tool_index,
                        "type": "function",
                        "function": {
                            "name": emit_name,
                            "arguments": arguments,
                        },
                    }
                ]
            }
            if tool_text:
                delta["_tool_call_text"] = tool_text  # type: ignore[assignment]
            # Do not attach finish_reason here. Parallel tool calls may still
            # follow, and response.completed is the true Responses turn end.
            # Emitting finish_reason=tool_calls caused a3a0f80cf early-break
            # logic (and wire_emitter.is_finished) to close after the first tool.
            return _build_chunk(delta)

        if item_type == "custom_tool_call":
            input_payload = item.get("input", "")
            if not isinstance(input_payload, str):
                input_payload = json.dumps(input_payload)
            call_id = (
                item.get("call_id")
                or item.get("id")
                or f"custom_{uuid.uuid4().hex[:8]}"
            )
            custom_name = item.get("name", "")
            if not isinstance(custom_name, str) or not custom_name.strip():
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Skipping custom_tool_call output_item.done with empty name "
                        "(call_id=%r)",
                        call_id,
                    )
                return _build_chunk()
            tool_index = assign_tool_call_index(
                chunk_id, chunk.get("output_index"), call_id
            )
            tool_call_obj = ToolCall(
                id=call_id,
                type="function",
                function=FunctionCall(name=custom_name, arguments=input_payload),
            )
            tool_text = render_tool_call(tool_call_obj)
            delta = {
                "tool_calls": [
                    {
                        "id": call_id,
                        "index": tool_index,
                        "type": "function",
                        "function": {
                            "name": custom_name,
                            "arguments": input_payload or "{}",
                        },
                    }
                ]
            }
            if tool_text:
                delta["_tool_call_text"] = tool_text  # type: ignore[assignment]

            return _build_chunk(delta)

        if item_type == "local_shell_call":
            arguments = _local_shell_item_to_arguments_json(item)
            arguments = _normalize_shell_like_tool_arguments_json("shell", arguments)
            call_id = (
                item.get("call_id") or item.get("id") or f"shell_{uuid.uuid4().hex[:8]}"
            )
            tool_index = assign_tool_call_index(
                chunk_id, chunk.get("output_index"), call_id
            )
            tool_call_obj = ToolCall(
                id=call_id,
                type="function",
                function=FunctionCall(name="bash", arguments=arguments),
            )
            tool_text = render_tool_call(tool_call_obj)
            delta = {
                "tool_calls": [
                    {
                        "id": call_id,
                        "index": tool_index,
                        "type": "function",
                        "function": {"name": "bash", "arguments": arguments},
                    }
                ]
            }
            if tool_text:
                delta["_tool_call_text"] = tool_text  # type: ignore[assignment]

            return _build_chunk(delta)

        return _build_chunk()

    # Codex and some OpenAI streams terminate with ``response.done`` (same payload shape
    # as ``response.completed``). Treat both as terminal completion with usage.
    if event_type in {"response.completed", "response.done"}:
        response_info = chunk.get("response") or {}
        output_items = response_info.get("output")
        has_function_call = False
        has_message_text = False
        if isinstance(output_items, list):
            for out_item in output_items:
                if not isinstance(out_item, dict):
                    continue
                out_type = out_item.get("type")
                if out_type == "function_call":
                    has_function_call = True
                elif out_type == "message":
                    for part in out_item.get("content") or []:
                        if not isinstance(part, dict):
                            continue
                        if part.get("type") in {"output_text", "text"} and str(
                            part.get("text") or ""
                        ).strip():
                            has_message_text = True
                            break
        # Tool-only Responses turns should surface finish_reason=tool_calls so
        # chat-shaped consumers finalize the tool loop. Keep an explicit terminal
        # marker because connector early-break treats bare tool_calls as non-terminal
        # (parallel tools may still follow on intermediate chunks).
        completed_finish_reason = (
            "tool_calls" if has_function_call and not has_message_text else "stop"
        )
        result = _build_chunk({}, completed_finish_reason)
        result["_responses_terminal"] = True
        usage = response_info.get("usage")
        if usage:
            result["usage"] = usage
        response_id = response_info.get("id") or chunk_id
        if response_id:
            result["response_id"] = response_id
            reset_tool_call_state(response_id)
        _reasoning_summary_sanitizer_state.set(ReasoningSummarySanitizerState())
        _active_responses_stream_id.set(None)
        return result

    if event_type == "response.created":
        response_info = chunk.get("response") or {}
        response_id = response_info.get("id") or chunk_id
        if response_id:
            reset_tool_call_state(response_id)
        _reasoning_summary_sanitizer_state.set(ReasoningSummarySanitizerState())
        created_delta: dict[str, Any] = {}
        if response_id:
            created_delta["response_id"] = response_id
        created_delta["role"] = "assistant"
        return _build_chunk(created_delta or None)

    if event_type in ("error", "response.failed"):
        response_info = chunk.get("response") or {}
        error_payload = response_info.get("error") or chunk.get("error") or {}
        reset_tool_call_state(response_info.get("id") or chunk_id)
        _reasoning_summary_sanitizer_state.set(ReasoningSummarySanitizerState())
        _active_responses_stream_id.set(None)
        return _build_error_chunk(error_payload)

    if event_type == "response.incomplete":
        response_info = chunk.get("response") or {}
        incomplete_details = response_info.get("incomplete_details") or {}
        if not isinstance(incomplete_details, dict):
            incomplete_details = {"details": incomplete_details}
        reason = incomplete_details.get("reason")
        message = "Response incomplete"
        if isinstance(reason, str) and reason.strip():
            message = f"Response incomplete: {reason.strip()}"
        error_payload = {
            "code": "response_incomplete",
            "message": message,
            "type": "incomplete_error",
            "incomplete_details": incomplete_details,
        }
        reset_tool_call_state(response_info.get("id") or chunk_id)
        _reasoning_summary_sanitizer_state.set(ReasoningSummarySanitizerState())
        _active_responses_stream_id.set(None)
        return _build_error_chunk(error_payload)

    if event_type == "response.output_item.added":
        item = chunk.get("item") or {}
        item_type = item.get("type")

        if item_type == "function_call":
            item_id_raw = item.get("id")
            item_id = item_id_raw if isinstance(item_id_raw, str) else None
            call_id = (
                item.get("call_id") or item_id or f"call_{uuid.uuid4().hex[:8]}"
            )
            name = item.get("name", "")
            if not isinstance(name, str) or not name.strip():
                return _build_chunk()
            # Always cache under both item.id and call_id (they often differ).
            _cache_function_call_name_aliases(
                name, item_id=item_id, call_id=str(call_id) if call_id else None
            )
            # Never emit a tool-call domain chunk from output_item.added.
            # Arguments are still empty; OpenCode/@ai-sdk validate immediately and
            # disconnect on empty-arg completed function_call items synthesized by
            # the Responses semantic legacy path. Name caching above is enough;
            # the complete call is emitted on output_item.done.
            return _build_chunk()
        return _build_chunk()

    if event_type in {
        "response.output_text.done",
        "response.custom_tool_call_input.done",
        "response.custom_tool_call_input.delta",
        "response.function_call_arguments.delta",
        "response.in_progress",
        "response.content_part.done",
    }:
        return _build_chunk()

    if "choices" in chunk:
        choices = chunk.get("choices") or []
        if not isinstance(choices, list) or not choices:
            return _build_chunk()

        primary_choice = choices[0] or {}
        finish_reason = primary_choice.get("finish_reason")
        raw_delta = primary_choice.get("delta") or {}
        if isinstance(raw_delta, dict):
            delta = cast(dict[str, Any], dict(raw_delta))
        else:
            delta = {"content": cast(Any, str(raw_delta))}

        content_value = delta.get("content")
        if isinstance(content_value, list):
            text_parts: list[str] = []
            for part in content_value:
                if not isinstance(part, dict):
                    continue
                part_type = part.get("type")
                if part_type in {"output_text", "text", "input_text"}:
                    text_value = part.get("text") or part.get("value") or ""
                    if text_value:
                        text_parts.append(str(text_value))
            delta["content"] = cast(Any, "".join(text_parts))
        elif isinstance(content_value, dict):
            delta["content"] = cast(Any, json.dumps(dict(content_value)))
        elif content_value is None:
            delta.pop("content", None)
        else:
            delta["content"] = cast(Any, str(content_value))

        tool_calls = delta.get("tool_calls")
        if isinstance(tool_calls, list):
            normalized_tool_calls: list[dict[str, Any]] = []
            for tool_call in tool_calls:
                if isinstance(tool_call, dict):
                    call_data = dict(tool_call)
                else:
                    function = getattr(tool_call, "function", None)
                    call_data = {
                        "id": getattr(tool_call, "id", ""),
                        "type": getattr(tool_call, "type", "function"),
                        "function": {
                            "name": getattr(function, "name", ""),
                            "arguments": getattr(function, "arguments", "{}"),
                        },
                    }

                function_payload = cast(dict[str, Any], call_data.get("function") or {})
                if function_payload:
                    arguments = function_payload.get("arguments")
                    if isinstance(arguments, dict | list):
                        function_payload["arguments"] = json.dumps(arguments)
                    elif arguments is None:
                        function_payload["arguments"] = "{}"
                    else:
                        function_payload["arguments"] = str(arguments)

                normalized_tool_calls.append(call_data)

            if normalized_tool_calls:
                delta["tool_calls"] = normalized_tool_calls
            else:
                delta.pop("tool_calls", None)

        return _build_chunk(delta, finish_reason)

    return _build_chunk()


def from_domain_to_responses_stream_chunk(chunk: Any) -> dict[str, Any]:
    """Best-effort translation to Responses-style chunk schema."""
    from src.core.domain.translators.openai.streaming import (
        from_domain_to_openai_stream_chunk,
    )

    payload = from_domain_to_openai_stream_chunk(chunk)
    payload = dict(payload)
    payload["object"] = "response.chunk"
    return payload
