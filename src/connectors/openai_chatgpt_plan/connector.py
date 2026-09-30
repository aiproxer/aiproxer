"""Thin ChatGPT-plan facade over the public OpenAI Responses transport."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any, Protocol

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


class _ChatGPTPlanModelLister(Protocol):
    async def list_models(
        self, profile_id: str, *, force_refresh: bool = False
    ) -> list[str]: ...


class OpenAIChatGPTPlanConnector(OpenAIResponsesConnector):
    """Public Responses-based backend for official ChatGPT-plan usage.

    Thin registration and routing facade over ``OpenAIResponsesConnector``.
    Importing this module registers the backend and does not start
    authorization, token refresh, network I/O, or a credential prompt.
    """

    backend_type: str = "openai-chatgpt-plan"
    VENDOR_PREFIX: str | None = "openai"
    capability_descriptor = CHATGPT_PLAN_CAPABILITY_DESCRIPTOR
    _chatgpt_plan_model_catalog: _ChatGPTPlanModelLister | None = None
    _chatgpt_plan_profile_id: str | None = None

    @property
    def has_static_credentials(self) -> bool:
        return False

    def bind_chatgpt_plan_model_catalog(
        self, catalog: _ChatGPTPlanModelLister, profile_id: str
    ) -> None:
        """Attach a per-profile catalog used by ``get_available_models_async``.

        Catalog construction is lazy (initialize / enumerator). This hook does
        not register an application initialization stage.
        """

        self._chatgpt_plan_model_catalog = catalog
        self._chatgpt_plan_profile_id = profile_id

    def _bind_chatgpt_plan_catalog_from_init_kwargs(
        self, kwargs: Mapping[str, Any]
    ) -> None:
        from src.connectors.openai_chatgpt_plan.enumerator import (
            build_chatgpt_plan_model_catalog,
            chatgpt_plan_config_from_init_kwargs,
            profile_id_from_init_kwargs,
        )

        profile_id = profile_id_from_init_kwargs(kwargs)
        if profile_id is None:
            return
        plan_config = chatgpt_plan_config_from_init_kwargs(kwargs)
        catalog = build_chatgpt_plan_model_catalog(plan_config)
        self.bind_chatgpt_plan_model_catalog(catalog, profile_id)

    async def initialize(self, **kwargs: Any) -> None:
        await super().initialize(**kwargs)
        self._bind_chatgpt_plan_catalog_from_init_kwargs(kwargs)

    async def get_available_models_async(self) -> list[str]:
        catalog = self._chatgpt_plan_model_catalog
        profile_id = self._chatgpt_plan_profile_id
        if catalog is not None and profile_id:
            listed = await catalog.list_models(profile_id)
            self.available_models = [str(model) for model in listed]
        return self.get_available_models()


backend_registry.register_backend("openai-chatgpt-plan", OpenAIChatGPTPlanConnector)
