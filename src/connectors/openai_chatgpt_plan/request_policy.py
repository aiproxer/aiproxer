"""SIWC Responses instruction projection for the ChatGPT-plan connector.

Projects generic Responses payloads onto supported high-priority instruction
semantics: top-level ``instructions`` and ``developer`` items. Explicit
``role=system`` input/message items are never sent upstream. Duplicate
injection is prevented by request provenance tags, not by deleting repeated
text. This module does not load bundled prompt resources or client-family
adapters.
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

SIWC_INSTRUCTION_SOURCE_KEY = "siwc_instruction_source"
SOURCE_NATIVE_INSTRUCTIONS = "native_instructions"
SOURCE_CANONICAL_SYSTEM_PROMPT = "canonical_system_prompt"
SOURCE_RESIDUAL_SYSTEM_ITEM = "residual_system_item"

_ITEM_LIST_KEYS = ("input", "messages")
_KNOWN_INSTRUCTION_SOURCES = frozenset(
    {
        SOURCE_NATIVE_INSTRUCTIONS,
        SOURCE_CANONICAL_SYSTEM_PROMPT,
        SOURCE_RESIDUAL_SYSTEM_ITEM,
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


class ChatGPTPlanRequestPolicy:
    """Connector-local SIWC request policy for instruction projection."""

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

        stream_flag = getattr(request.request, "stream", False)
        return SIWCProjectedRequest(
            payload=payload,
            downstream_stream_requested=bool(stream_flag),
            explicit_profile_id=None,
        )
