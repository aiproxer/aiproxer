"""CommandCode OpenAI-compatible backend connector."""

from __future__ import annotations

import logging
import os
from typing import TYPE_CHECKING, Any

import httpx

from src.connectors.base import add_vendor_prefix, strip_vendor_prefix
from src.connectors.openai import OpenAIConnector
from src.core.common.model_catalog import BackendModelEnumeration
from src.core.config.app_config import AppConfig, BackendConfig
from src.core.services.backend_registry import backend_registry

if TYPE_CHECKING:
    from src.core.services.translation_service import TranslationService

logger = logging.getLogger(__name__)

COMMANDCODE_OPENAI_BACKEND_TYPE = "commandcode-openai"
COMMANDCODE_API_KEY_ENV = "COMMANDCODE_API_KEY"
COMMANDCODE_OPENAI_DEFAULT_BASE_URL = "https://api.commandcode.ai/provider/v1"

_COMMANDCODE_OPENAI_FALLBACK_MODELS: tuple[str, ...] = (
    "Qwen/Qwen3.7-Flash",
    "Qwen/Qwen2.5-Coder-32B-Instruct",
    "deepseek-ai/DeepSeek-V3",
    "deepseek-ai/DeepSeek-R1",
    "meta-llama/Llama-3.3-70B-Instruct",
    "mistralai/Mistral-Small-24B-Instruct-2501",
    "claude-3-5-sonnet-20241022",
    "claude-3-5-haiku-20241022",
    "claude-3-opus-20240229",
    "claude-haiku-4-5-20251001",
    "claude-sonnet-4-5-20250929",
)


class CommandCodeOpenAIConnector(OpenAIConnector):
    """Connector for CommandCode's OpenAI Chat Completions-compatible API."""

    backend_type: str = COMMANDCODE_OPENAI_BACKEND_TYPE

    # Multi-vendor gateway: upstream model IDs are already vendor-qualified
    VENDOR_PREFIX: str | None = None

    def __init__(
        self,
        client: httpx.AsyncClient,
        config: AppConfig,
        translation_service: TranslationService | None = None,
    ) -> None:
        super().__init__(client, config, translation_service=translation_service)
        self.api_base_url = COMMANDCODE_OPENAI_DEFAULT_BASE_URL

    async def initialize(self, **kwargs: Any) -> None:
        """Initialize connector, falling back to COMMANDCODE_API_KEY env var if needed."""
        if not kwargs.get("api_key"):
            env_key = os.getenv(COMMANDCODE_API_KEY_ENV)
            if env_key:
                kwargs["api_key"] = env_key.strip()

        kwargs.setdefault("api_base_url", self.api_base_url)
        await super().initialize(**kwargs)


class CommandCodeOpenAIConfiguredModelEnumerator:
    """Enumerate canonical CommandCode OpenAI routes for configured backend instances."""

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        extra = config.extra or {}
        configured_models = config.models or extra.get("models")
        if isinstance(configured_models, list | tuple) and configured_models:
            models = [
                add_vendor_prefix(
                    strip_vendor_prefix(
                        str(m).strip(), COMMANDCODE_OPENAI_BACKEND_TYPE
                    ),
                    COMMANDCODE_OPENAI_BACKEND_TYPE,
                )
                for m in configured_models
                if str(m).strip()
            ]
            if models:
                return BackendModelEnumeration.available(
                    instance_name=instance_name,
                    connector=COMMANDCODE_OPENAI_BACKEND_TYPE,
                    models=models,
                    source="commandcode_openai_configured",
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
                connector=COMMANDCODE_OPENAI_BACKEND_TYPE,
                source="commandcode_openai",
                error_code="missing_api_key",
                instance_pinned=False,
            )

        shared_api_base_url = (
            config.api_url
            or extra.get("api_base_url")
            or COMMANDCODE_OPENAI_DEFAULT_BASE_URL
        )
        api_base_url = str(shared_api_base_url).rstrip("/")
        timeout_seconds = float(extra.get("model_discovery_timeout_seconds", 5.0))

        clean_key = str(api_key).strip()
        if clean_key:
            try:
                headers = {
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
                                            raw_id, COMMANDCODE_OPENAI_BACKEND_TYPE
                                        )
                                    )
                            if live_models:
                                return BackendModelEnumeration.available(
                                    instance_name=instance_name,
                                    connector=COMMANDCODE_OPENAI_BACKEND_TYPE,
                                    models=live_models,
                                    source="commandcode_openai_upstream",
                                    instance_pinned=False,
                                )
            except (httpx.TransportError, TimeoutError, ConnectionError) as exc:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Live model discovery failed for %s, falling back to curated list: %s",
                        instance_name,
                        exc,
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
            add_vendor_prefix(m, COMMANDCODE_OPENAI_BACKEND_TYPE)
            for m in _COMMANDCODE_OPENAI_FALLBACK_MODELS
        ]
        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector=COMMANDCODE_OPENAI_BACKEND_TYPE,
            models=curated_models,
            source="commandcode_openai_curated",
            instance_pinned=False,
        )


backend_registry.register_backend("commandcode-openai", CommandCodeOpenAIConnector)
backend_registry.register_backend("commandcode_openai", CommandCodeOpenAIConnector)
backend_registry.register_backend("commandcode", CommandCodeOpenAIConnector)

__all__ = [
    "COMMANDCODE_API_KEY_ENV",
    "COMMANDCODE_OPENAI_BACKEND_TYPE",
    "COMMANDCODE_OPENAI_DEFAULT_BASE_URL",
    "CommandCodeOpenAIConfiguredModelEnumerator",
    "CommandCodeOpenAIConnector",
]
