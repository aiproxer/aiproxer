"""SIWC Responses request projection for the ChatGPT-plan connector.

Projects generic Responses payloads onto supported high-priority instruction
semantics: top-level ``instructions`` and ``developer`` items. Explicit
``role=system`` input/message items are never sent upstream. Duplicate
injection is prevented by request provenance tags, not by deleting repeated
text. Client-supplied function/custom tools, ``tool_choice``, call/output
linkage, and text/image/file content parts are preserved. Explicit hosted
tools from the SIWC preview matrix are rejected; web search is preserved.

Chat Completions-shaped translator output that still carries top-level
``messages`` is normalized onto Responses ``input`` before upstream send so
SIWC preview never receives the unsupported ``messages`` parameter.

Chat Completions tool schemas nested under ``function`` (missing top-level
``name``) are flattened to the Responses flat-function shape SIWC accepts
(``tools[i].name`` / ``description`` / ``parameters``). Nested
``additional_tools`` entries are normalized the same way. Hosted tools remain
rejected; no client-family adapters are introduced.

Product decision (harness compatibility): unsupported SIWC preview scalar
fields are soft-dropped (stripped / never sent upstream) rather than hard-
failing with ``ResponsesProviderLimitationError``. Harness clients commonly
send ``max_tokens`` / ``max_output_tokens``, ``temperature``, ``top_p``, etc.;
official SIWC docs require omitting them upstream
(https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations).
Aliases ``max_tokens`` / ``max_completion_tokens`` -> ``max_output_tokens`` and
``request_metadata`` -> ``metadata`` are likewise stripped. Hard reject remains
for unsupported hosted tools and for explicit ``store=true``. Upstream HTTP
inference always uses ``store=false`` and ``stream=true`` and never sends
``previous_response_id``.
This module does not load bundled prompt resources or client-family adapters.
"""

from __future__ import annotations

import copy
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any, Protocol

from src.connectors.contracts import (
    ConnectorChatCompletionsRequest,
    ConnectorResponsesRequest,
)
from src.core.common.exceptions import ResponsesProviderLimitationError

SIWC_INSTRUCTION_SOURCE_KEY = "siwc_instruction_source"
SOURCE_NATIVE_INSTRUCTIONS = "native_instructions"
SOURCE_CANONICAL_SYSTEM_PROMPT = "canonical_system_prompt"
SOURCE_RESIDUAL_SYSTEM_ITEM = "residual_system_item"
SIWC_PROVIDER = "openai-chatgpt-plan"

_ITEM_LIST_KEYS = ("input", "messages")
_KNOWN_INSTRUCTION_SOURCES = frozenset(
    {
        SOURCE_NATIVE_INSTRUCTIONS,
        SOURCE_CANONICAL_SYSTEM_PROMPT,
        SOURCE_RESIDUAL_SYSTEM_ITEM,
    }
)
# Official SIWC preview (2026-09-30) + design matrix. Web search is supported.
# https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations
_UNSUPPORTED_HOSTED_TOOL_TYPES = frozenset(
    {
        "image_generation",
        "image_generation_call",
        "file_search",
        "file_search_call",
        "code_interpreter",
        "code_interpreter_call",
        "computer",
        "computer_call",
        "computer_use",
        "computer_use_preview",
        "mcp",
        "mcp_call",
        "mcp_list_tools",
        "connectors",
        "tool_search",
        "tool_search_call",
        "programmatic_tool_calling",
    }
)
# Official SIWC preview unsupported fields (2026-09-30). Soft-dropped from the
# upstream payload (never hard-fail) for harness compatibility.
# https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations
_UNSUPPORTED_SIWC_FIELDS = frozenset(
    {
        "background",
        "conversation",
        "max_output_tokens",
        "max_tool_calls",
        "metadata",
        "moderation",
        "multi_agent",
        "prompt",
        "prompt_cache_retention",
        "safety_identifier",
        "temperature",
        "top_logprobs",
        "top_p",
        "truncation",
        "user",
    }
)
# Client/canonical aliases that map onto unsupported SIWC field names. Strip
# these from the projected payload so they are never sent upstream.
_PAYLOAD_FIELD_ALIASES_TO_STRIP = frozenset(
    {
        "max_tokens",
        "max_completion_tokens",
        "request_metadata",
    }
)

@dataclass(frozen=True)
class SIWCProjectedRequest:
    payload: dict[str, Any]
    downstream_stream_requested: bool
    explicit_profile_id: str | None


class IChatGPTPlanRequestPolicy(Protocol):
    def project(
        self,
        *,
        request: ConnectorResponsesRequest | ConnectorChatCompletionsRequest,
        generic_payload: Mapping[str, Any],
    ) -> SIWCProjectedRequest: ...


def _non_empty_str(value: Any) -> str | None:
    if isinstance(value, str) and value:
        return value
    return None


def _pop_source_tag(item: dict[str, Any]) -> str | None:
    raw = item.pop(SIWC_INSTRUCTION_SOURCE_KEY, None)
    metadata = item.get("metadata")
    if raw is None and isinstance(metadata, dict):
        raw = metadata.pop(SIWC_INSTRUCTION_SOURCE_KEY, None)
        if not metadata:
            item.pop("metadata", None)
    if isinstance(raw, str) and raw in _KNOWN_INSTRUCTION_SOURCES:
        return raw
    return None


def _project_item_list(items: list[Any], *, represented_sources: set[str]) -> list[Any]:
    projected: list[Any] = []
    for item in items:
        if not isinstance(item, dict):
            projected.append(item)
            continue
        cleaned = dict(item)
        source = _pop_source_tag(cleaned)
        role = cleaned.get("role")
        if isinstance(role, str) and role.lower() == "system":
            if source in represented_sources:
                continue
            cleaned["role"] = "developer"
        projected.append(cleaned)
    return projected


def _normalize_messages_to_input(payload: dict[str, Any]) -> None:
    """Move Chat Completions ``messages`` onto Responses ``input``; never send ``messages``.

    Translator output from Chat Completions frontends commonly still carries
    top-level ``messages``. SIWC preview Responses rejects that parameter, so
    after item-list projection we always pop ``messages``. When it is a list,
    merge into ``input`` (extend an existing list, otherwise replace/set).
    """
    messages = payload.pop("messages", None)
    if not isinstance(messages, list):
        return
    existing_input = payload.get("input")
    if isinstance(existing_input, list):
        existing_input.extend(messages)
    else:
        payload["input"] = list(messages)


# Fields lifted from Chat Completions nested ``function`` onto Responses tools.
_FLAT_FUNCTION_FIELDS = ("name", "description", "parameters", "strict", "format")


def _callable_tool_type(value: Any) -> str | None:
    """Return normalized function/custom type, or None if not a callable tool type."""
    if value is None:
        return "function"
    if not isinstance(value, str):
        return None
    normalized = value.strip().casefold()
    if not normalized:
        return "function"
    if normalized in {"function", "custom"}:
        return normalized
    return None


def _flatten_chat_shaped_tool_dict(item: dict[str, Any]) -> dict[str, Any]:
    """Lift Chat nested ``function`` fields to Responses flat tool shape when needed.

    Chat Completions emits ``{"type":"function","function":{"name":...}}``.
    Responses / SIWC require top-level ``name`` (and related fields). Already-flat
    tools and non-callable hosted/search tools are left unchanged.
    """
    out = dict(item)

    additional = out.get("additional_tools")
    if isinstance(additional, list):
        flattened_additional: list[Any] = []
        for tool in additional:
            if isinstance(tool, dict):
                flattened_additional.append(_flatten_chat_shaped_tool_dict(tool))
            elif isinstance(tool, Mapping):
                flattened_additional.append(_flatten_chat_shaped_tool_dict(dict(tool)))
            else:
                flattened_additional.append(tool)
        out["additional_tools"] = flattened_additional

    nested = out.get("function")
    if not isinstance(nested, Mapping):
        return out

    resolved_type = _callable_tool_type(out.get("type"))
    if resolved_type is None:
        return out

    if _non_empty_str(out.get("name")) is not None:
        return out

    nested_name = _non_empty_str(nested.get("name"))
    if nested_name is None:
        return out

    if not isinstance(out.get("type"), str) or not str(out.get("type")).strip():
        out["type"] = resolved_type

    for field in _FLAT_FUNCTION_FIELDS:
        if field in out and out[field] is not None:
            continue
        if field not in nested:
            continue
        value = nested[field]
        if value is None:
            continue
        out[field] = value

    out.pop("function", None)
    return out


def _normalize_chat_shaped_tools(payload: dict[str, Any]) -> None:
    """Flatten Chat Completions tool schemas onto Responses flat-function shape."""
    tools = payload.get("tools")
    if isinstance(tools, list):
        payload["tools"] = [
            _flatten_chat_shaped_tool_dict(dict(tool))
            if isinstance(tool, Mapping)
            else tool
            for tool in tools
        ]

    tool_choice = payload.get("tool_choice")
    if isinstance(tool_choice, Mapping):
        nested = tool_choice.get("function")
        resolved_type = _callable_tool_type(tool_choice.get("type"))
        if (
            resolved_type is not None
            and isinstance(nested, Mapping)
            and _non_empty_str(tool_choice.get("name")) is None
            and _non_empty_str(nested.get("name")) is not None
        ):
            choice = dict(tool_choice)
            if not isinstance(choice.get("type"), str) or not str(
                choice.get("type")
            ).strip():
                choice["type"] = resolved_type
            choice["name"] = nested["name"]
            choice.pop("function", None)
            payload["tool_choice"] = choice


def _hosted_tool_type(value: Any) -> str | None:
    if not isinstance(value, str) or not value:
        return None
    normalized = value.strip().casefold()
    if normalized in _UNSUPPORTED_HOSTED_TOOL_TYPES:
        return normalized
    return None


def _reject_hosted_tool_dict(item: Mapping[str, Any]) -> None:
    hosted = _hosted_tool_type(item.get("type"))
    if hosted is not None:
        raise ResponsesProviderLimitationError(hosted, SIWC_PROVIDER)
    additional = item.get("additional_tools")
    if not isinstance(additional, list):
        return
    for tool in additional:
        if not isinstance(tool, Mapping):
            continue
        nested = _hosted_tool_type(tool.get("type"))
        if nested is not None:
            raise ResponsesProviderLimitationError(nested, SIWC_PROVIDER)


def _reject_unsupported_hosted_tools(payload: Mapping[str, Any]) -> None:
    for key in ("tools", *_ITEM_LIST_KEYS):
        items = payload.get(key)
        if not isinstance(items, list):
            continue
        for item in items:
            if isinstance(item, Mapping):
                _reject_hosted_tool_dict(item)
    tool_choice = payload.get("tool_choice")
    if isinstance(tool_choice, Mapping):
        hosted = _hosted_tool_type(tool_choice.get("type"))
        if hosted is not None:
            raise ResponsesProviderLimitationError(hosted, SIWC_PROVIDER)


def _extra_body_mapping(canonical: Any) -> Mapping[str, Any]:
    raw = getattr(canonical, "extra_body", None)
    if isinstance(raw, Mapping):
        return raw
    return {}


def _reject_explicit_store_true(
    request: ConnectorResponsesRequest | ConnectorChatCompletionsRequest,
) -> None:
    """Hard-reject explicit store=true; SIWC preview requires store=false."""
    canonical = request.request
    extra_body = _extra_body_mapping(canonical)
    if getattr(canonical, "store", None) is True or extra_body.get("store") is True:
        raise ResponsesProviderLimitationError("store", SIWC_PROVIDER)


def _strip_unsupported_siwc_fields(payload: dict[str, Any]) -> None:
    """Soft-drop unsupported SIWC scalar fields and known aliases from payload."""
    for field in _UNSUPPORTED_SIWC_FIELDS:
        payload.pop(field, None)
    for alias in _PAYLOAD_FIELD_ALIASES_TO_STRIP:
        payload.pop(alias, None)


def _apply_required_siwc_transport_flags(payload: dict[str, Any]) -> None:
    payload["store"] = False
    payload["stream"] = True
    payload.pop("previous_response_id", None)


class ChatGPTPlanRequestPolicy:
    """Connector-local SIWC request policy for instruction and field projection."""

    def project(
        self,
        *,
        request: ConnectorResponsesRequest | ConnectorChatCompletionsRequest,
        generic_payload: Mapping[str, Any],
    ) -> SIWCProjectedRequest:
        payload = copy.deepcopy(dict(generic_payload))
        represented_sources: set[str] = set()

        native_instructions = _non_empty_str(payload.get("instructions"))
        canonical_prompt = _non_empty_str(
            getattr(request.request, "system_prompt", None)
        )

        if native_instructions is not None:
            represented_sources.add(SOURCE_NATIVE_INSTRUCTIONS)
            if canonical_prompt is not None and canonical_prompt == native_instructions:
                represented_sources.add(SOURCE_CANONICAL_SYSTEM_PROMPT)
        elif canonical_prompt is not None:
            payload["instructions"] = canonical_prompt
            represented_sources.add(SOURCE_CANONICAL_SYSTEM_PROMPT)

        for key in _ITEM_LIST_KEYS:
            items = payload.get(key)
            if isinstance(items, list):
                payload[key] = _project_item_list(
                    items, represented_sources=represented_sources
                )

        _normalize_messages_to_input(payload)
        _normalize_chat_shaped_tools(payload)
        _reject_unsupported_hosted_tools(payload)
        _reject_explicit_store_true(request)
        _strip_unsupported_siwc_fields(payload)
        _apply_required_siwc_transport_flags(payload)

        stream_flag = getattr(request.request, "stream", False)
        return SIWCProjectedRequest(
            payload=payload,
            downstream_stream_requested=bool(stream_flag),
            explicit_profile_id=None,
        )
