"""Thin ChatGPT-plan facade over the public OpenAI Responses transport."""

from __future__ import annotations

from src.connectors.openai_responses import OpenAIResponsesConnector
from src.core.domain.backend_capability_descriptor import BackendCapabilityDescriptor
from src.core.services.backend_registry import backend_registry

CHATGPT_PLAN_CAPABILITY_DESCRIPTOR = BackendCapabilityDescriptor(
    protocol_family="openai",
    supports_streaming=True,
    supports_tool_calls=True,
    is_oauth_based=True,
    requires_personal_auth=True,
)


class OpenAIChatGPTPlanConnector(OpenAIResponsesConnector):
    """Public Responses-based backend for official ChatGPT-plan usage.

    Thin registration and routing facade over ``OpenAIResponsesConnector``.
    Importing this module registers the backend and does not start
    authorization, token refresh, network I/O, or a credential prompt.
    """

    backend_type: str = "openai-chatgpt-plan"
    VENDOR_PREFIX: str | None = "openai"
    capability_descriptor = CHATGPT_PLAN_CAPABILITY_DESCRIPTOR

    @property
    def has_static_credentials(self) -> bool:
        return False


backend_registry.register_backend("openai-chatgpt-plan", OpenAIChatGPTPlanConnector)
