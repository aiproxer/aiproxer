"""Codex model catalog subsystem.

Direct authenticated backend catalog GET + shipped fallback snapshot, shared by
the ``openai-codex``, ``openai-codex-v2`` and ``openai-codex-app-server``
connectors. The catalog is fetched at startup from
``GET https://chatgpt.com/backend-api/codex/models`` using the legacy/current
Codex OAuth account, and the shipped snapshot is used on any failure.
"""

from __future__ import annotations

from src.connectors.openai_codex.catalog.config import (
    DEFAULT_CLIENT_VERSION,
    DEFAULT_CODEX_MODEL_CATALOG_CONFIG,
    CodexModelCatalogConfig,
    apply_model_catalog_env_overrides,
    codex_model_catalog_config_from_mapping,
    get_model_catalog_env_overrides,
)
from src.connectors.openai_codex.catalog.discovery_service import (
    CodexCatalogDiscoveryService,
)
from src.connectors.openai_codex.catalog.endpoint_client import (
    CODEX_CATALOG_URL,
    CodexCatalogEndpointClient,
)
from src.connectors.openai_codex.catalog.fallback_loader import (
    CodexCatalogFallbackLoader,
)
from src.connectors.openai_codex.catalog.interfaces import (
    ICodexCatalogCredentialProvider,
    ICodexCatalogDiscoveryService,
    ICodexCatalogEndpointClient,
    ICodexCatalogFallbackLoader,
    ICodexCatalogParser,
    ICodexModelCatalog,
    ICodexModelCatalogProvider,
)
from src.connectors.openai_codex.catalog.parser import CodexCatalogParser
from src.connectors.openai_codex.catalog.provider import CodexModelCatalogProvider
from src.connectors.openai_codex.catalog.types import (
    CodexModelCatalog,
    CodexModelReasoningProfile,
    filter_catalog_by_client_version,
    parse_client_version,
)

__all__ = [
    "CODEX_CATALOG_URL",
    "DEFAULT_CLIENT_VERSION",
    "DEFAULT_CODEX_MODEL_CATALOG_CONFIG",
    "CodexCatalogDiscoveryService",
    "CodexCatalogEndpointClient",
    "CodexCatalogFallbackLoader",
    "CodexCatalogParser",
    "CodexModelCatalog",
    "CodexModelCatalogConfig",
    "CodexModelCatalogProvider",
    "CodexModelReasoningProfile",
    "ICodexCatalogCredentialProvider",
    "ICodexCatalogDiscoveryService",
    "ICodexCatalogEndpointClient",
    "ICodexCatalogFallbackLoader",
    "ICodexCatalogParser",
    "ICodexModelCatalog",
    "ICodexModelCatalogProvider",
    "apply_model_catalog_env_overrides",
    "codex_model_catalog_config_from_mapping",
    "filter_catalog_by_client_version",
    "get_model_catalog_env_overrides",
    "parse_client_version",
]
