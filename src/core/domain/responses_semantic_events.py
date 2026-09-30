"""Semantic streaming events for Responses API normalization (provider-neutral)."""

from __future__ import annotations

from enum import Enum
from typing import Any

from pydantic import ConfigDict, Field

from src.core.interfaces.model_bases import DomainModel


class ResponsesSemanticEventType(str, Enum):
    RESPONSE_CREATED = "response_created"
    RESPONSE_IN_PROGRESS = "response_in_progress"
    OUTPUT_ITEM_ADDED = "output_item_added"
    CONTENT_PART_ADDED = "content_part_added"
    TEXT_DELTA = "text_delta"
    TEXT_DONE = "text_done"
    TOOL_CALL_ARGS_DELTA = "tool_call_args_delta"
    TOOL_CALL_ARGS_DONE = "tool_call_args_done"
    CONTENT_PART_DONE = "content_part_done"
    OUTPUT_ITEM_DONE = "output_item_done"
    RESPONSE_COMPLETED = "response_completed"
    RESPONSE_FAILED = "response_failed"
    RESPONSE_INCOMPLETE = "response_incomplete"
    PASSTHROUGH = "passthrough"


class ResponsesSemanticEvent(DomainModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    type: ResponsesSemanticEventType
    response_id: str
    sequence_number: int = Field(ge=0)
    output_index: int | None = None
    content_index: int | None = None
    item_id: str | None = None
    name: str | None = None
    delta: str | None = None
    text: str | None = None
    item: dict[str, Any] | None = None
    part: dict[str, Any] | None = None
    response: dict[str, Any] | None = None
    error: dict[str, Any] | None = None
    raw: dict[str, Any] | None = None
