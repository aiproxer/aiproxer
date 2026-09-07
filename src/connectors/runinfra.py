"""Runinfra OpenAI-compatible backend connector."""

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
    from src.connectors.contracts import ConnectorRequestContext
    from src.core.domain.chat import CanonicalChatRequest
    from src.core.services.translation_service import TranslationService

logger = logging.getLogger(__name__)

RUNINFRA_BACKEND_TYPE = "runinfra"
RUNINFRA_API_KEY_ENV = "RUNINFRA_API_KEY"
RUNINFRA_DEFAULT_BASE_URL = "https://api.runinfra.ai/v1"

_RUNINFRA_FALLBACK_MODELS: tuple[str, ...] = (
    "meta-llama/Llama-3.3-70B-Instruct",
    "meta-llama/Llama-3.1-8B-Instruct",
    "deepseek-ai/DeepSeek-V3",
    "deepseek-ai/DeepSeek-R1",
    "Qwen/Qwen2.5-Coder-32B-Instruct",
    "Qwen/Qwen2.5-72B-Instruct",
    "mistralai/Mistral-Small-24B-Instruct-2501",
)


def _normalize_runinfra_api_key(value: str) -> str:
    """Strip whitespace and a leading ``Bearer `` prefix."""
    s = value.strip()
    lower = s.lower()
    if lower.startswith("bearer "):
        s = s[7:].lstrip()
    return s


class RuninfraConnector(OpenAIConnector):
    """Connector for runinfra.ai's OpenAI Chat Completions-compatible API."""

    backend_type: str = RUNINFRA_BACKEND_TYPE

    # Vendor prefix for runinfra models in unified model routing
    VENDOR_PREFIX: str | None = RUNINFRA_BACKEND_TYPE

    def __init__(
        self,
        client: httpx.AsyncClient,
        config: AppConfig,
        translation_service: TranslationService | None = None,
    ) -> None:
        super().__init__(client, config, translation_service=translation_service)
        self.api_base_url = RUNINFRA_DEFAULT_BASE_URL

    async def initialize(self, **kwargs: Any) -> None:
        """Initialize connector, falling back to RUNINFRA_API_KEY env var if needed."""
        raw_key = kwargs.get("api_key")
        if isinstance(raw_key, str):
            norm = _normalize_runinfra_api_key(raw_key)
            kwargs["api_key"] = norm if norm else None

        if not kwargs.get("api_key"):
            env_key = os.getenv(RUNINFRA_API_KEY_ENV)
            if env_key:
                kwargs["api_key"] = _normalize_runinfra_api_key(env_key)

        kwargs.setdefault("api_base_url", self.api_base_url)
        await super().initialize(**kwargs)

    async def _prepare_payload(
        self,
        request_data: CanonicalChatRequest,
        processed_messages: list[Any],
        effective_model: str,
        context: ConnectorRequestContext | None = None,
    ) -> dict[str, Any]:
        """Build JSON body for Runinfra, stripping vendor prefix from model name."""
        payload = await super()._prepare_payload(
            request_data, processed_messages, effective_model, context
        )
        if "model" in payload:
            payload["model"] = strip_vendor_prefix(
                payload["model"], RUNINFRA_BACKEND_TYPE
            )
        return payload


class RuninfraConfiguredModelEnumerator:
    """Enumerate canonical runinfra.ai routes for configured backend instances."""

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        extra = config.extra or {}
        configured_models = config.models or extra.get("models")
        if isinstance(configured_models, list | tuple) and configured_models:
            models = [
                add_vendor_prefix(
                    strip_vendor_prefix(str(m).strip(), RUNINFRA_BACKEND_TYPE),
                    RUNINFRA_BACKEND_TYPE,
                )
                for m in configured_models
                if str(m).strip()
            ]
            if models:
                return BackendModelEnumeration.available(
                    instance_name=instance_name,
                    connector=RUNINFRA_BACKEND_TYPE,
                    models=models,
                    source="runinfra_configured",
                    instance_pinned=False,
                )

        suffix = instance_name.split(".", 1)[-1] if "." in instance_name else None
        raw_key = (
            config.api_key
            or extra.get("api_key")
            or (os.environ.get(f"{RUNINFRA_API_KEY_ENV}_{suffix}") if suffix else None)
            or os.environ.get(RUNINFRA_API_KEY_ENV)
        )

        api_key = _normalize_runinfra_api_key(str(raw_key)) if raw_key else None

        if not api_key:
            return BackendModelEnumeration.unavailable(
                instance_name=instance_name,
                connector=RUNINFRA_BACKEND_TYPE,
                source="runinfra",
                error_code="missing_api_key",
                instance_pinned=False,
            )

        shared_api_base_url = (
            config.api_url or extra.get("api_base_url") or RUNINFRA_DEFAULT_BASE_URL
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
                                        add_vendor_prefix(raw_id, RUNINFRA_BACKEND_TYPE)
                                    )
                            if live_models:
                                return BackendModelEnumeration.available(
                                    instance_name=instance_name,
                                    connector=RUNINFRA_BACKEND_TYPE,
                                    models=live_models,
                                    source="runinfra_upstream",
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
            add_vendor_prefix(m, RUNINFRA_BACKEND_TYPE)
            for m in _RUNINFRA_FALLBACK_MODELS
        ]
        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector=RUNINFRA_BACKEND_TYPE,
            models=curated_models,
            source="runinfra_curated",
            instance_pinned=False,
        )


backend_registry.register_backend(RUNINFRA_BACKEND_TYPE, RuninfraConnector)

__all__ = [
    "RUNINFRA_API_KEY_ENV",
    "RUNINFRA_BACKEND_TYPE",
    "RUNINFRA_DEFAULT_BASE_URL",
    "RuninfraConfiguredModelEnumerator",
    "RuninfraConnector",
]
