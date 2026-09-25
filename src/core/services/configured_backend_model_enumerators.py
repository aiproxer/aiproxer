"""Startup-safe model sources for configured local-agent backend instances."""

from __future__ import annotations

import json
import logging
import os
import sys
from pathlib import Path
from typing import Any

import httpx

from src.connectors.base import add_vendor_prefix, strip_vendor_prefix
from src.core.common.model_catalog import BackendModelEnumeration
from src.core.config.app_config import BackendConfig

logger = logging.getLogger(__name__)

_OPENCODE_ZEN_VENDOR_PREFIX = "opencode-zen"
_OPENCODE_ZEN_DEFAULT_ENDPOINT = "https://opencode.ai/zen/v1"

_OPENCODE_ZEN_FALLBACK_MODELS: tuple[str, ...] = (
    "claude-fable-5",
    "claude-opus-4-8",
    "claude-opus-4-7",
    "claude-opus-4-6",
    "claude-opus-4-5",
    "claude-opus-4-1",
    "claude-sonnet-4-6",
    "claude-sonnet-4-5",
    "claude-sonnet-4",
    "claude-haiku-4-5",
    "gemini-3.5-flash",
    "gemini-3.1-pro",
    "gemini-3-flash",
    "gpt-5.5",
    "gpt-5.5-pro",
    "gpt-5.4",
    "gpt-5.4-pro",
    "gpt-5.4-mini",
    "gpt-5.4-nano",
    "gpt-5.3-codex-spark",
    "gpt-5.3-codex",
    "gpt-5.2",
    "gpt-5.2-codex",
    "gpt-5.1",
    "gpt-5.1-codex-max",
    "gpt-5.1-codex",
    "gpt-5.1-codex-mini",
    "gpt-5",
    "gpt-5-codex",
    "gpt-5-nano",
    "grok-build-0.1",
    "deepseek-v4-pro",
    "deepseek-v4-flash",
    "glm-5.1",
    "glm-5",
    "minimax-m2.7",
    "minimax-m2.5",
    "kimi-k2.6",
    "kimi-k2.5",
    "qwen3.6-plus",
    "qwen3.5-plus",
    "big-pickle",
    "deepseek-v4-flash-free",
    "mimo-v2.5-free",
    "qwen3.6-plus-free",
    "minimax-m3-free",
    "nemotron-3-ultra-free",
    "north-mini-code-free",
)


def _normalize_opencode_zen_model_name(model_name: str) -> str:
    """Map raw model names to vendor/model-name format."""
    exact_mappings = {
        "glm-4.6": "z-ai/glm-4.6",
        "qwen3-coder": "qwen/qwen3-coder",
        "kimi-k2": "moonshotai/kimi-k2-0905",
        "kimi-k2-thinking": "moonshotai/kimi-k2-thinking",
        "grok-code": "x-ai/grok-code-fast-1",
        "big-pickle": "stealth/big-pickle",
        "alpha-gd4": "stealth/alpha-gd4",
    }
    if model_name in exact_mappings:
        return exact_mappings[model_name]
    if "/" in model_name:
        return model_name
    if model_name.startswith("claude"):
        return f"anthropic/{model_name}"
    if model_name.startswith(("gpt", "o1-")):
        return f"openai/{model_name}"
    if model_name.startswith("gemini"):
        return f"google/{model_name}"
    if model_name.startswith("grok"):
        return f"x-ai/{model_name}"
    if model_name.startswith("deepseek"):
        return f"deepseek/{model_name}"
    if model_name.startswith("minimax"):
        return f"minimax/{model_name}"
    if model_name.startswith("mimo"):
        return f"mimo/{model_name}"
    if model_name.startswith("glm"):
        return f"z-ai/{model_name}"
    if model_name.startswith("kimi"):
        return f"moonshotai/{model_name}"
    if model_name.startswith("qwen"):
        return f"qwen/{model_name}"
    if model_name.startswith("nemotron"):
        return f"nvidia/{model_name}"
    if model_name.startswith("north"):
        return f"north/{model_name}"
    return model_name


class ExplicitConfiguredModelEnumerator:
    """Treat an instance's explicit ``models`` list as its complete catalog."""

    def __init__(self, *, connector: str, source: str = "configured") -> None:
        self._connector = connector
        self._source = source

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        models = [str(model).strip() for model in config.models if str(model).strip()]
        if not models:
            return BackendModelEnumeration.unavailable(
                instance_name=instance_name,
                connector=self._connector,
                source=self._source,
                error_code="models_not_configured",
                instance_pinned=True,
            )
        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector=self._connector,
            models=models,
            source=self._source,
            instance_pinned=True,
        )


from src.connectors._openai_codex_connector import (
    OpenAICodexConfiguredModelEnumerator as OpenAICodexConfiguredModelEnumerator,
)


class CodexAppServerConfiguredModelEnumerator:
    """Project the shared startup Codex catalog onto an App Server instance."""

    def __init__(self, *, catalog: Any, catalog_source: str) -> None:
        self._catalog = catalog
        self._catalog_source = catalog_source

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        del config
        models = ["openai/auto"]
        if self._catalog_source == "discovery":
            models.extend(
                add_vendor_prefix(str(slug), "openai")
                for slug in self._catalog.routable_slugs()
            )
        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector="openai-codex-app-server",
            models=models,
            source=f"codex_{self._catalog_source}",
            instance_pinned=True,
        )


class OpencodeZenConfiguredModelEnumerator:
    """Enumerate canonical OpenCode Zen routes for configured backend instances."""

    def _get_credentials(
        self, instance_name: str, config: BackendConfig
    ) -> tuple[str | None, bool]:
        extra = config.extra or {}
        suffix = instance_name.split(".", 1)[-1] if "." in instance_name else None
        api_key = (
            config.api_key
            or extra.get("api_key")
            or (os.environ.get(f"OPENCODE_ZEN_API_KEY_{suffix}") if suffix else None)
            or os.environ.get("OPENCODE_ZEN_API_KEY")
        )
        if api_key:
            return str(api_key).strip(), True

        custom_path = (
            getattr(config, "credentials_path", None)
            or extra.get("credentials_path")
            or os.environ.get("OPENCODE_AUTH_PATH")
        )
        candidates: list[Path] = []
        if custom_path:
            candidates.append(Path(custom_path).expanduser())
        else:
            if sys.platform == "win32" or os.name == "nt":
                localappdata = os.environ.get("LOCALAPPDATA")
                if localappdata:
                    candidates.append(Path(localappdata) / "opencode" / "auth.json")
                candidates.append(
                    Path.home() / ".local" / "share" / "opencode" / "auth.json"
                )
                candidates.append(
                    Path.home() / "AppData" / "Local" / "opencode" / "auth.json"
                )
            else:
                xdg_data_home = os.environ.get("XDG_DATA_HOME")
                if xdg_data_home:
                    candidates.append(Path(xdg_data_home) / "opencode" / "auth.json")
                candidates.append(
                    Path.home() / ".local" / "share" / "opencode" / "auth.json"
                )

        for path in candidates:
            if path.exists() and path.is_file():
                try:
                    with open(path, encoding="utf-8") as f:
                        data = json.load(f)
                    provider_creds = (
                        data.get("opencode") if isinstance(data, dict) else None
                    )
                    if isinstance(provider_creds, dict):
                        token = (
                            provider_creds.get("access")
                            or provider_creds.get("accessToken")
                            or provider_creds.get("access_token")
                            or provider_creds.get("token")
                            or provider_creds.get("key")
                        )
                        if token:
                            return str(token).strip(), True
                        return None, True
                except Exception as exc:
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Failed to read OpenCode auth file %s for %s: %s",
                            path,
                            instance_name,
                            exc,
                            exc_info=True,
                        )

        return None, False

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        extra = config.extra or {}
        configured_models = config.models or extra.get("models")
        if isinstance(configured_models, list | tuple) and configured_models:
            models = [
                add_vendor_prefix(
                    strip_vendor_prefix(str(m).strip(), _OPENCODE_ZEN_VENDOR_PREFIX),
                    _OPENCODE_ZEN_VENDOR_PREFIX,
                )
                for m in configured_models
                if str(m).strip()
            ]
            if models:
                return BackendModelEnumeration.available(
                    instance_name=instance_name,
                    connector=_OPENCODE_ZEN_VENDOR_PREFIX,
                    models=models,
                    source="opencode_zen_configured",
                    instance_pinned=False,
                )

        token, has_creds = self._get_credentials(instance_name, config)
        if not has_creds:
            return BackendModelEnumeration.unavailable(
                instance_name=instance_name,
                connector=_OPENCODE_ZEN_VENDOR_PREFIX,
                source="opencode_zen",
                error_code="missing_credentials",
                instance_pinned=False,
            )

        shared_api_base_url = (
            config.api_url
            or extra.get("api_base_url")
            or _OPENCODE_ZEN_DEFAULT_ENDPOINT
        )
        api_base_url = str(shared_api_base_url).rstrip("/")
        timeout_seconds = float(extra.get("model_discovery_timeout_seconds", 5.0))

        async def _try_fetch(headers: dict[str, str]) -> list[str] | None:
            try:
                async with httpx.AsyncClient(timeout=timeout_seconds) as client:
                    resp = await client.get(f"{api_base_url}/models", headers=headers)
                    if resp.status_code == 200:
                        data = resp.json()
                        rows = data.get("data") if isinstance(data, dict) else None
                        if isinstance(rows, list) and rows:
                            result: list[str] = []
                            seen: set[str] = set()
                            for r in rows:
                                raw_id = ""
                                if isinstance(r, dict):
                                    raw_id = str(r.get("id") or "")
                                elif isinstance(r, str):
                                    raw_id = r
                                if raw_id and raw_id not in seen:
                                    seen.add(raw_id)
                                    norm_id = _normalize_opencode_zen_model_name(raw_id)
                                    result.append(
                                        add_vendor_prefix(
                                            norm_id, _OPENCODE_ZEN_VENDOR_PREFIX
                                        )
                                    )
                            if result:
                                return result
            except (httpx.TransportError, TimeoutError, ConnectionError) as exc:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Live model discovery attempt failed for %s: %s",
                        instance_name,
                        exc,
                    )
            except Exception as exc:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Live model discovery attempt failed for %s: %s",
                        instance_name,
                        exc,
                        exc_info=True,
                    )
            return None

        public_headers = {
            "Accept": "application/json",
            "Content-Type": "application/json",
            "HTTP-Referer": "https://opencode.ai/",
            "X-Title": "opencode",
        }
        live_models = await _try_fetch(public_headers)

        if not live_models and token:
            auth_headers = {
                **public_headers,
                "Authorization": f"Bearer {token}",
            }
            live_models = await _try_fetch(auth_headers)

        if live_models:
            return BackendModelEnumeration.available(
                instance_name=instance_name,
                connector=_OPENCODE_ZEN_VENDOR_PREFIX,
                models=live_models,
                source="opencode_zen_upstream",
                instance_pinned=False,
            )

        curated_models = [
            add_vendor_prefix(
                _normalize_opencode_zen_model_name(m), _OPENCODE_ZEN_VENDOR_PREFIX
            )
            for m in _OPENCODE_ZEN_FALLBACK_MODELS
        ]
        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector=_OPENCODE_ZEN_VENDOR_PREFIX,
            models=curated_models,
            source="opencode_zen_curated",
            instance_pinned=False,
        )


_CLINE_DEFAULT_API_HOST = "https://api.cline.bot"
_CLINE_FREE_VIRTUAL_MODEL = "cline-free/free"
_CLINE_FREE_FALLBACK_MODELS: tuple[str, ...] = (
    "cline-free/gemini-3.8-flash",
    "cline-free/deepseek-v4.1-flash",
    "cline-free/muse-spark-1.3-contributor",
    "cline-free/mimo-v2.6-flash",
    "stealth/space-bunny-alpha",
)


class ClineConfiguredModelEnumerator:
    """Startup-safe catalog for the optional Cline OAuth connector.

    Advertises the synthetic ``cline-free/free`` waterfall route plus curated
    free models so model-only selectors resolve before the connector is
    activated. Without this, the proxy returns HTTP 404 ``unknown_model`` for
    ``cline-free/free`` because it is not an explicit ``backend:model`` selector.
    """

    def _resolve_api_host(self, config: BackendConfig) -> str:
        extra = config.extra or {}
        explicit = (
            config.api_url
            or extra.get("api_base_url")
            or extra.get("cline_api_base_url")
            or extra.get("api_host")
        )
        if isinstance(explicit, str) and explicit.strip():
            host = explicit.strip().rstrip("/")
            if host.endswith("/api/v1"):
                return host[: -len("/api/v1")]
            return host
        return _CLINE_DEFAULT_API_HOST

    def _base_models(self, config: BackendConfig) -> list[str]:
        models: list[str] = [_CLINE_FREE_VIRTUAL_MODEL]
        configured = config.models or (config.extra or {}).get("models")
        if isinstance(configured, list | tuple):
            for raw in configured:
                name = str(raw).strip()
                if name and name not in models:
                    models.append(name)
        for fallback in _CLINE_FREE_FALLBACK_MODELS:
            if fallback not in models:
                models.append(fallback)
        return models

    async def _fetch_live_free_models(self, api_host: str) -> list[str]:
        url = f"{api_host.rstrip('/')}/api/v1/ai/cline/recommended-models"
        try:
            async with httpx.AsyncClient(timeout=5.0) as client:
                response = await client.get(
                    url,
                    headers={
                        "Accept": "application/json",
                        "HTTP-Referer": "https://cline.bot",
                        "X-Title": "Cline",
                    },
                )
            if response.status_code != 200:
                return []
            payload = response.json()
        except (httpx.TransportError, TimeoutError, ConnectionError, ValueError):
            return []
        except Exception:
            logger.debug(
                "Cline recommended-models discovery failed",
                exc_info=True,
            )
            return []

        free_entries = payload.get("free") if isinstance(payload, dict) else None
        if not isinstance(free_entries, list):
            return []
        models: list[str] = []
        seen: set[str] = set()
        for item in free_entries:
            raw_id = ""
            if isinstance(item, dict):
                raw_id = str(item.get("id") or "").strip()
            elif isinstance(item, str):
                raw_id = item.strip()
            if raw_id and raw_id not in seen:
                seen.add(raw_id)
                models.append(raw_id)
        return models

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        models = self._base_models(config)
        live_models = await self._fetch_live_free_models(self._resolve_api_host(config))
        source = "cline_curated"
        if live_models:
            source = "cline_recommended"
            for live_model in live_models:
                if live_model not in models:
                    models.append(live_model)
        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector="cline",
            models=models,
            source=source,
            instance_pinned=False,
        )


__all__ = [
    "ClineConfiguredModelEnumerator",
    "CodexAppServerConfiguredModelEnumerator",
    "ExplicitConfiguredModelEnumerator",
    "OpencodeZenConfiguredModelEnumerator",
    "OpenAICodexConfiguredModelEnumerator",
]
