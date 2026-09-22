"""Startup stage: discover the Codex model catalog and register it in DI.

Fetches the Codex model catalog from the authenticated backend at startup (via
:class:`src.connectors.openai_codex.catalog.provider.CodexModelCatalogProvider`)
and registers the resolved :class:`ICodexModelCatalog` as a DI singleton shared
by the ``openai-codex``, ``openai-codex-v2`` and ``openai-codex-app-server``
connectors. On any discovery failure the provider falls back to the shipped
snapshot; if that also fails, the stage logs and leaves registration absent
(connectors then fall back to their own shipped-file load).
"""

from __future__ import annotations

import logging
import os
from collections.abc import Mapping
from dataclasses import replace
from typing import TYPE_CHECKING, Any, cast

from src.core.app.stages.base import InitializationStage
from src.core.config.app_config import AppConfig
from src.core.di.container import ServiceCollection

if TYPE_CHECKING:
    from src.connectors.openai_codex.catalog.config import CodexModelCatalogConfig

logger = logging.getLogger(__name__)

# Backend config attributes that may carry an ``extra.codex.model_catalog``
# section. The first one found wins; the app-server variant consumes the shared
# catalog and does not contribute discovery config.
_CODEX_CONFIG_BACKEND_ATTRS = ("openai_codex", "openai_codex_v2")


def resolve_codex_model_catalog_config(
    config: AppConfig,
) -> CodexModelCatalogConfig:
    """Resolve the catalog config from the first codex backend that defines it.

    Applies the ``OPENAI_CODEX_MODEL_CATALOG_*`` environment overrides first
    (shared with the connector settings loader), then the ``auth_path``
    precedence: the model-catalog ``auth_path`` wins, then the backend
    ``credentials_path``, then the legacy backend ``extra.openai_codex_path``;
    when none is set the credential manager's own environment/default
    discovery is used.

    Returns the environment-overridden defaults when no codex backend is
    configured; when a backend exists but defines no ``model_catalog`` section,
    the defaults apply with ``auth_path`` still filled from the backend's
    ``credentials_path`` / ``extra.openai_codex_path`` (discovery runs with
    defaults in that case, so dropping the backend paths would be surprising).
    """
    from src.connectors.openai_codex.catalog.config import (
        DEFAULT_CODEX_MODEL_CATALOG_CONFIG,
        apply_model_catalog_env_overrides,
        codex_model_catalog_config_from_mapping,
    )
    from src.connectors.openai_codex.utils import to_mapping

    backends = getattr(config, "backends", None)
    first_backend: Any = None
    for attr in _CODEX_CONFIG_BACKEND_ATTRS:
        backend = _get_backend(backends, attr)
        if backend is None:
            continue
        if first_backend is None:
            first_backend = backend
        extra = getattr(backend, "extra", None)
        if not isinstance(extra, Mapping):
            continue
        codex = extra.get("codex")
        if not isinstance(codex, Mapping):
            continue
        model_catalog = codex.get("model_catalog")
        if model_catalog is None:
            continue
        merged = apply_model_catalog_env_overrides(to_mapping(model_catalog))
        cfg = codex_model_catalog_config_from_mapping(merged)
        return _with_backend_auth_path(cfg, backend)
    if first_backend is not None:
        # No ``model_catalog`` section anywhere: discovery still runs with
        # defaults, so honor the backend's credentials paths instead of
        # silently dropping them.
        cfg = codex_model_catalog_config_from_mapping(
            apply_model_catalog_env_overrides(None)
        )
        # Preserve the shared default instance when no overrides are present so
        # existing identity/equality expectations keep holding.
        if cfg == DEFAULT_CODEX_MODEL_CATALOG_CONFIG:
            cfg = DEFAULT_CODEX_MODEL_CATALOG_CONFIG
        return _with_backend_auth_path(cfg, first_backend)
    return codex_model_catalog_config_from_mapping(
        apply_model_catalog_env_overrides(None)
    )


def _with_backend_auth_path(
    cfg: CodexModelCatalogConfig, backend: Any
) -> CodexModelCatalogConfig:
    """Fill ``cfg.auth_path`` from backend credentials config when unset."""
    if cfg.auth_path:
        return cfg

    credentials_path = getattr(backend, "credentials_path", None)
    if isinstance(credentials_path, str) and credentials_path.strip():
        expanded = os.path.expandvars(os.path.expanduser(credentials_path.strip()))
        return replace(cfg, auth_path=expanded)
    extra = getattr(backend, "extra", None)
    if isinstance(extra, Mapping):
        legacy = extra.get("openai_codex_path")
        if isinstance(legacy, str) and legacy.strip():
            expanded = os.path.expandvars(os.path.expanduser(legacy.strip()))
            return replace(cfg, auth_path=expanded)
    return cfg


def _get_backend(backends: Any, attr: str) -> Any:
    if backends is None:
        return None
    backend = getattr(backends, attr, None)
    if backend is not None:
        return backend
    lookup = getattr(backends, "lookup", None)
    if callable(lookup):
        try:
            return lookup(attr.replace("_", "-"))
        except Exception as exc:  # - lookup is best-effort
            logger.debug("backends.lookup(%s) failed: %s", attr, exc)
            return None
    return None


class CodexModelCatalogStage(InitializationStage):
    """Discover and register the Codex model catalog at startup."""

    @property
    def name(self) -> str:
        return "codex_model_catalog"

    def get_dependencies(self) -> list[str]:
        return ["core_services"]

    async def execute(self, services: ServiceCollection, config: AppConfig) -> None:
        from src.connectors.openai_codex.catalog.interfaces import ICodexModelCatalog
        from src.connectors.openai_codex.catalog.provider import (
            CodexModelCatalogProvider,
        )

        catalog_config = resolve_codex_model_catalog_config(config)
        provider = CodexModelCatalogProvider(config=catalog_config)
        try:
            await provider.load()
        except Exception as exc:  # - never fail startup over catalog
            logger.error(
                "Codex model catalog load failed; connectors will lazy-load "
                "the shipped fallback. Error: %s",
                exc,
                exc_info=True,
            )
            return

        try:
            catalog = provider.get_catalog()
        except RuntimeError:
            return

        services.add_instance(cast(type[Any], ICodexModelCatalog), catalog)
        services.add_instance(CodexModelCatalogProvider, provider)
        logger.info(
            "Codex model catalog registered (%d routable models).",
            len(catalog.routable_slugs()),
        )
