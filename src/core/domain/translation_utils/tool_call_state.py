from __future__ import annotations

import threading
from collections.abc import MutableMapping
from typing import Any

from cachetools import TTLCache  # type: ignore

codex_tool_call_index_base: MutableMapping[str, int] = TTLCache(maxsize=1000, ttl=600)
codex_tool_call_item_index: MutableMapping[str, dict[str, int]] = TTLCache(
    maxsize=1000, ttl=600
)
codex_function_name_cache: MutableMapping[str, str] = TTLCache(maxsize=1000, ttl=600)
# Fragments keyed only by tool call id. Upstream events often omit a stable response id
# per SSE message; call_ids from the API are unique enough for correlation within TTL.
codex_tool_call_arguments_by_call_id: MutableMapping[str, str] = TTLCache(
    maxsize=5000, ttl=600
)
_tool_state_lock = threading.Lock()


def reset_tool_call_state(response_id: str | None) -> None:
    if not response_id:
        return
    with _tool_state_lock:
        codex_tool_call_index_base.pop(response_id, None)
        codex_tool_call_item_index.pop(response_id, None)


def accumulate_tool_call_arguments(call_id: str, arguments_fragment: str) -> None:
    """Accumulate partial tool call arguments from streaming deltas.

    Args:
        call_id: The tool call ID
        arguments_fragment: The partial arguments string
    """
    if not call_id or not arguments_fragment:
        return
    with _tool_state_lock:
        prev = codex_tool_call_arguments_by_call_id.get(call_id, "")
        codex_tool_call_arguments_by_call_id[call_id] = prev + arguments_fragment


def get_accumulated_tool_call_arguments(call_id: str) -> str:
    """Get accumulated tool call arguments.

    Args:
        call_id: The tool call ID

    Returns:
        The accumulated arguments string, or "{}" if not found
    """
    if not call_id:
        return "{}"
    with _tool_state_lock:
        return codex_tool_call_arguments_by_call_id.get(call_id, "{}")


def clear_tool_call_arguments(call_id: str) -> None:
    """Clear accumulated arguments for a specific tool call.

    Args:
        call_id: The tool call ID
    """
    if not call_id:
        return
    with _tool_state_lock:
        codex_tool_call_arguments_by_call_id.pop(call_id, None)


def cache_function_name(call_id: str, name: str, *aliases: str) -> None:
    """Cache ``name`` under ``call_id`` and any alternate ids (e.g. item id).

    OpenAI Responses puts the function name on ``response.output_item.added``
    (``item.id`` / ``item.call_id``) while later ``function_call_arguments.*``
    events only carry ``item_id``. Caching under both keys lets later events
    resolve the name without OpenCode-specific branching.
    """
    if not name or not str(name).strip():
        return
    keys = [call_id, *aliases]
    with _tool_state_lock:
        for key in keys:
            if isinstance(key, str) and key.strip():
                codex_function_name_cache[key] = name


def get_cached_function_name(*call_ids: str) -> str:
    """Return the first cached function name for any of the given ids."""
    with _tool_state_lock:
        for call_id in call_ids:
            if isinstance(call_id, str) and call_id:
                cached = codex_function_name_cache.get(call_id, "")
                if cached:
                    return cached
        return ""


def assign_tool_call_index(
    response_id: str | None,
    output_index: Any,
    item_id: str | None,
) -> int:
    if not response_id:
        return 0

    if not isinstance(output_index, int):
        if item_id:
            with _tool_state_lock:
                item_index_dict = codex_tool_call_item_index.get(response_id, {})
                return item_index_dict.get(item_id, 0) if item_index_dict else 0
        return 0

    with _tool_state_lock:
        base = codex_tool_call_index_base.get(response_id)
        if base is None or output_index < base:
            codex_tool_call_index_base[response_id] = output_index
            base = output_index

        index = output_index - base
        if index < 0:
            index = 0

        if item_id:
            item_dict = codex_tool_call_item_index.setdefault(response_id, {})
            item_dict[item_id] = index

        return index
