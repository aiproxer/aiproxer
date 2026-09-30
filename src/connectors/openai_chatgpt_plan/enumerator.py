"""Configured-instance model enumeration for openai-chatgpt-plan.

Exposes the selected SIWC profile's public ``/v1/models`` list through the
generic backend enumerator registry. Discovery uses ChatGPTPlanModelCatalog
only; it does not use the Codex catalog, Codex enumerator, or a new
application initialization stage.
"""

from __future__ import annotations

import logging
from collections.abc import Mapping
from pathlib import Path
from threading import Lock
from typing import Any

from src.connectors.base import add_vendor_prefix
from src.connectors.openai_chatgpt_plan.catalog import (
    ChatGPTPlanCatalogError,
    ChatGPTPlanModelCatalog,
    IChatGPTPlanModelCatalog,
)
from src.connectors.openai_chatgpt_plan.config import (
    ChatGPTPlanConfig,
    load_chatgpt_plan_config,
)
from src.core.common.model_catalog import BackendModelEnumeration
from src.core.config.app_config import BackendConfig

logger = logging.getLogger(__name__)

CONNECTOR = "openai-chatgpt-plan"
VENDOR_PREFIX = "openai"
CATALOG_SOURCE = "chatgpt_plan_catalog"

_CATALOG_GRAPHS: dict[str, ChatGPTPlanModelCatalog] = {}
_CATALOG_GRAPHS_LOCK = Lock()


def _resolved_profiles_path(profiles_path: str) -> Path:
    return Path(profiles_path).expanduser().resolve()


def _normalize_profile_id(value: object) -> str | None:
    if not isinstance(value, str):
        return None
    stripped = value.strip()
    return stripped or None


def profile_id_from_backend_config(config: BackendConfig) -> str | None:
    """Prefer this instance's extra.chatgpt_plan.profile_id, then ChatGPTPlanConfig."""

    extra = config.extra or {}
    section = extra.get("chatgpt_plan")
    if isinstance(section, Mapping):
        explicit = _normalize_profile_id(section.get("profile_id"))
        if explicit is not None:
            return explicit
    return load_chatgpt_plan_config(extra).profile_id


def profile_id_from_init_kwargs(kwargs: Mapping[str, Any]) -> str | None:
    section = kwargs.get("chatgpt_plan")
    if isinstance(section, Mapping):
        explicit = _normalize_profile_id(section.get("profile_id"))
        if explicit is not None:
            return explicit
        return load_chatgpt_plan_config({"chatgpt_plan": dict(section)}).profile_id
    return load_chatgpt_plan_config(None).profile_id


def chatgpt_plan_config_from_backend_config(config: BackendConfig) -> ChatGPTPlanConfig:
    return load_chatgpt_plan_config(config.extra)


def chatgpt_plan_config_from_init_kwargs(
    kwargs: Mapping[str, Any],
) -> ChatGPTPlanConfig:
    section = kwargs.get("chatgpt_plan")
    if isinstance(section, Mapping):
        return load_chatgpt_plan_config({"chatgpt_plan": dict(section)})
    return load_chatgpt_plan_config(None)


def build_chatgpt_plan_model_catalog(
    plan_config: ChatGPTPlanConfig,
) -> ChatGPTPlanModelCatalog:
    """Return the shared catalog graph for one profiles store.

    Enumerator owned construction and connector initialize must reuse the same
    ``ChatGPTPlanModelCatalog`` / ``ChatGPTPlanTokenManager`` for a given
    resolved ``profiles_path``. Injected enumerator catalogs still win.
    """

    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore
    from src.connectors.openai_chatgpt_plan.tokens import ChatGPTPlanTokenManager

    profiles_path = _resolved_profiles_path(plan_config.profiles_path)
    key = str(profiles_path)
    with _CATALOG_GRAPHS_LOCK:
        existing = _CATALOG_GRAPHS.get(key)
        if existing is not None:
            return existing
        store = ChatGPTPlanProfileStore(profiles_path)
        token_manager = ChatGPTPlanTokenManager(store)
        catalog = ChatGPTPlanModelCatalog(store, token_manager, config=plan_config)
        _CATALOG_GRAPHS[key] = catalog
        return catalog


class ChatGPTPlanConfiguredModelEnumerator:
    """Enumerate public ChatGPT-plan models for one configured backend instance."""

    def __init__(
        self,
        catalog: IChatGPTPlanModelCatalog | None = None,
    ) -> None:
        self._catalog = catalog

    def _catalog_for(self, plan_config: ChatGPTPlanConfig) -> IChatGPTPlanModelCatalog:
        if self._catalog is not None:
            return self._catalog
        return build_chatgpt_plan_model_catalog(plan_config)

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        plan_config = chatgpt_plan_config_from_backend_config(config)
        profile_id = profile_id_from_backend_config(config)
        if profile_id is None:
            return BackendModelEnumeration.unavailable(
                instance_name=instance_name,
                connector=CONNECTOR,
                source=CATALOG_SOURCE,
                error_code="profile_not_configured",
                instance_pinned=True,
            )

        catalog = self._catalog_for(plan_config)
        try:
            models = await catalog.list_models(profile_id)
        except ChatGPTPlanCatalogError:
            logger.debug(
                "ChatGPT-plan model catalog is temporarily unavailable for %s",
                instance_name,
                exc_info=True,
            )
            return BackendModelEnumeration.unavailable(
                instance_name=instance_name,
                connector=CONNECTOR,
                source=CATALOG_SOURCE,
                error_code="temporary_catalog_error",
                instance_pinned=True,
            )

        prefixed = [
            add_vendor_prefix(str(model).strip(), VENDOR_PREFIX)
            for model in models
            if str(model).strip()
        ]
        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector=CONNECTOR,
            models=prefixed,
            source=CATALOG_SOURCE,
            instance_pinned=True,
        )


__all__ = [
    "CATALOG_SOURCE",
    "CONNECTOR",
    "ChatGPTPlanConfiguredModelEnumerator",
    "build_chatgpt_plan_model_catalog",
    "chatgpt_plan_config_from_backend_config",
    "chatgpt_plan_config_from_init_kwargs",
    "profile_id_from_backend_config",
    "profile_id_from_init_kwargs",
]
