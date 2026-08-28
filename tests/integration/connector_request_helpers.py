"""Helpers for integration tests calling connector ``chat_completions``."""

from __future__ import annotations

from src.connectors.contracts import ConnectorChatCompletionsRequest
from src.core.domain.chat import CanonicalChatRequest, ChatRequest


def make_connector_chat_request(
    chat: ChatRequest,
    *,
    effective_model: str | None = None,
) -> ConnectorChatCompletionsRequest:
    """Build the canonical connector request used by backends in tests."""
    canonical = CanonicalChatRequest.model_validate(chat.model_dump())
    return ConnectorChatCompletionsRequest(
        request=canonical,
        processed_messages=list(canonical.messages),
        effective_model=(
            effective_model if effective_model is not None else canonical.model
        ),
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=None,
        options={},
    )
