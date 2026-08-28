"""CommandCode Anthropic Messages-compatible backend connector."""

from __future__ import annotations

import logging
import os
from typing import TYPE_CHECKING, Any

import httpx

from src.connectors.anthropic import AnthropicBackend
from src.connectors.base import add_vendor_prefix, strip_vendor_prefix
from src.core.common.exceptions import ConfigurationError
from src.core.common.model_catalog import BackendModelEnumeration
from src.core.config.app_config import AppConfig, BackendConfig
from src.core.services.backend_registry import backend_registry

if TYPE_CHECKING:
    from src.core.services.translation_service import TranslationService

logger = logging.getLogger(__name__)

COMMANDCODE_ANTHROPIC_BACKEND_TYPE = "commandcode-anthropic"
COMMANDCODE_API_KEY_ENV = "COMMANDCODE_API_KEY"
COMMANDCODE_ANTHROPIC_DEFAULT_BASE_URL = "https://api.commandcode.ai/provider/v1"

_COMMANDCODE_ANTHROPIC_FALLBACK_MODELS: tuple[str, ...] = (
    "claude-3-5-sonnet-20241022",
    "claude-3-5-haiku-20241022",
    "claude-3-opus-20240229",
    "claude-haiku-4-5-20251001",
    "claude-sonnet-4-5-20250929",
    "Qwen/Qwen3.7-Flash",
    "deepseek-ai/DeepSeek-V3",
    "deepseek-ai/DeepSeek-R1",
)


class CommandCodeAnthropicConnector(AnthropicBackend):
    """Connector for CommandCode's Anthropic Messages-compatible API."""

    backend_type: str = COMMANDCODE_ANTHROPIC_BACKEND_TYPE

    # Multi-vendor gateway: upstream model IDs are already vendor-qualified
    VENDOR_PREFIX: str | None = None

    def __init__(
        self,
        client: httpx.AsyncClient,
        config: AppConfig,
        translation_service: TranslationService,
    ) -> None:
        super().__init__(client, config, translation_service)
        self.available_models: list[str] = []

    async def initialize(self, **kwargs: Any) -> None:
        """Initialize connector, falling back to COMMANDCODE_API_KEY env var if needed."""
        api_key = kwargs.get("api_key")
        if not api_key:
            env_key = os.getenv(COMMANDCODE_API_KEY_ENV)
            if env_key:
                api_key = env_key.strip()

        if not api_key:
            raise ConfigurationError(
                message=f"{COMMANDCODE_API_KEY_ENV} is required for {COMMANDCODE_ANTHROPIC_BACKEND_TYPE}",
                code="missing_config",
            )

        base_url = str(
            kwargs.get("anthropic_api_base_url")
            or kwargs.get("api_base_url")
            or COMMANDCODE_ANTHROPIC_DEFAULT_BASE_URL
        ).rstrip("/")

        await super().initialize(
            anthropic_api_base_url=base_url,
            key_name=COMMANDCODE_ANTHROPIC_BACKEND_TYPE,
            api_key=api_key,
            auth_header_name="x-api-key",
        )

        try:
            await self.list_models(
                base_url=base_url, key_name=self.key_name, api_key=self.api_key
            )
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to fetch CommandCode Anthropic models at startup: %s", e
                )

    def get_available_models(self) -> list[str]:
        """Return cached CommandCode model IDs without adding synthetic anthropic/ prefix."""
        return list(self.available_models)

    async def get_available_models_async(self) -> list[str]:
        """Return CommandCode model IDs without adding synthetic anthropic/ prefix."""
        await self._ensure_models_loaded()
        return list(self.available_models)


class CommandCodeAnthropicConfiguredModelEnumerator:
    """Enumerate canonical CommandCode Anthropic routes for configured backend instances."""

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        extra = config.extra or {}
        configured_models = config.models or extra.get("models")
        if isinstance(configured_models, list | tuple) and configured_models:
            models = [
                add_vendor_prefix(
                    strip_vendor_prefix(
                        str(m).strip(), COMMANDCODE_ANTHROPIC_BACKEND_TYPE
                    ),
                    COMMANDCODE_ANTHROPIC_BACKEND_TYPE,
                )
                for m in configured_models
                if str(m).strip()
            ]
            if models:
                return BackendModelEnumeration.available(
                    instance_name=instance_name,
                    connector=COMMANDCODE_ANTHROPIC_BACKEND_TYPE,
                    models=models,
                    source="commandcode_anthropic_configured",
                    instance_pinned=False,
                )

        suffix = instance_name.split(".", 1)[-1] if "." in instance_name else None
        api_key = (
            config.api_key
            or extra.get("api_key")
            or (
                os.environ.get(f"{COMMANDCODE_API_KEY_ENV}_{suffix}")
                if suffix
                else None
            )
            or os.environ.get(COMMANDCODE_API_KEY_ENV)
        )

        if not api_key:
            return BackendModelEnumeration.unavailable(
                instance_name=instance_name,
                connector=COMMANDCODE_ANTHROPIC_BACKEND_TYPE,
                source="commandcode_anthropic",
                error_code="missing_api_key",
                instance_pinned=False,
            )

        shared_api_base_url = (
            config.api_url
            or extra.get("anthropic_api_base_url")
            or extra.get("api_base_url")
            or COMMANDCODE_ANTHROPIC_DEFAULT_BASE_URL
        )
        api_base_url = str(shared_api_base_url).rstrip("/")
        timeout_seconds = float(extra.get("model_discovery_timeout_seconds", 5.0))

        clean_key = str(api_key).strip()
        if clean_key:
            try:
                headers = {
                    "x-api-key": clean_key,
                    "Authorization": f"Bearer {clean_key}",
                    "Accept": "application/json",
                }
                async with httpx.AsyncClient(timeout=timeout_seconds) as client:
                    response = await client.get(
                        f"{api_base_url}/models", headers=headers
                    )
                    if response.status_code == 200:
                        data = response.json()
                        rows = data.get("data") if isinstance(data, dict) else None
                        if isinstance(rows, list) and rows:
                            live_models: list[str] = []
                            seen: set[str] = set()
                            for r in rows:
                                raw_id = ""
                                if isinstance(r, dict):
                                    raw_id = str(r.get("id") or "")
                                elif isinstance(r, str):
                                    raw_id = r
                                raw_id = raw_id.strip()
                                if raw_id and raw_id not in seen:
                                    seen.add(raw_id)
                                    live_models.append(
                                        add_vendor_prefix(
                                            raw_id,
                                            COMMANDCODE_ANTHROPIC_BACKEND_TYPE,
                                        )
                                    )
                            if live_models:
                                return BackendModelEnumeration.available(
                                    instance_name=instance_name,
                                    connector=COMMANDCODE_ANTHROPIC_BACKEND_TYPE,
                                    models=live_models,
                                    source="commandcode_anthropic_upstream",
                                    instance_pinned=False,
                                )
            except Exception as exc:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Live model discovery failed for %s, falling back to curated list: %s",
                        instance_name,
                        exc,
                        exc_info=True,
                    )

        curated_models = [
            add_vendor_prefix(m, COMMANDCODE_ANTHROPIC_BACKEND_TYPE)
            for m in _COMMANDCODE_ANTHROPIC_FALLBACK_MODELS
        ]
        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector=COMMANDCODE_ANTHROPIC_BACKEND_TYPE,
            models=curated_models,
            source="commandcode_anthropic_curated",
            instance_pinned=False,
        )


backend_registry.register_backend(
    "commandcode-anthropic", CommandCodeAnthropicConnector
)
backend_registry.register_backend(
    "commandcode_anthropic", CommandCodeAnthropicConnector
)

__all__ = [
    "COMMANDCODE_ANTHROPIC_BACKEND_TYPE",
    "COMMANDCODE_ANTHROPIC_DEFAULT_BASE_URL",
    "COMMANDCODE_API_KEY_ENV",
    "CommandCodeAnthropicConfiguredModelEnumerator",
    "CommandCodeAnthropicConnector",
]
