"""Catalog discovery service.

Fetches the raw catalog from the authenticated Codex backend (via
:class:`~src.connectors.openai_codex.catalog.endpoint_client.CodexCatalogEndpointClient`)
and parses it into a :class:`CodexModelCatalog`. Returns ``None`` on any failure
(missing credentials, timeout, transport failure, malformed output, parse error)
so the provider can fall back to the shipped snapshot.
"""

from __future__ import annotations

import logging
from pathlib import Path

from src.connectors.openai_codex.catalog.endpoint_client import (
    DEFAULT_CLIENT_VERSION,
    DEFAULT_TIMEOUT_SECONDS,
    CodexCatalogEndpointClient,
)
from src.connectors.openai_codex.catalog.interfaces import (
    ICodexCatalogEndpointClient,
    ICodexCatalogParser,
)
from src.connectors.openai_codex.catalog.parser import CodexCatalogParser
from src.connectors.openai_codex.catalog.types import CodexModelCatalog

logger = logging.getLogger(__name__)


class CodexCatalogDiscoveryService:
    """Discover the catalog at runtime from the authenticated Codex backend."""

    def __init__(
        self,
        *,
        endpoint_client: ICodexCatalogEndpointClient | None = None,
        client_version: str = DEFAULT_CLIENT_VERSION,
        auth_path: Path | None = None,
        timeout_seconds: float = DEFAULT_TIMEOUT_SECONDS,
        parser: ICodexCatalogParser | None = None,
    ) -> None:
        self._endpoint_client = (
            endpoint_client
            if endpoint_client is not None
            else CodexCatalogEndpointClient(
                client_version=client_version,
                auth_path=auth_path,
                timeout_seconds=timeout_seconds,
            )
        )
        self._parser = parser if parser is not None else CodexCatalogParser()

    async def discover(self) -> CodexModelCatalog | None:
        try:
            raw = await self._endpoint_client.fetch()
        except Exception:  # - fall back on any discovery failure
            logger.warning(
                "Codex catalog discovery raised; falling back to snapshot.",
                exc_info=True,
            )
            return None

        if raw is None:
            return None

        try:
            return self._parser.parse(raw)
        except Exception:  # - fall back on any parse failure
            logger.warning(
                "Codex catalog discovery parse failed; falling back to snapshot.",
                exc_info=True,
            )
            return None


__all__ = ["CodexCatalogDiscoveryService"]
