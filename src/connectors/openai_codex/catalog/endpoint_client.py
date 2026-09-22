"""Direct Codex backend model-catalog client.

Fetches the model catalog from the authenticated Codex backend:

``GET https://chatgpt.com/backend-api/codex/models?client_version=<version>``

This replaces shelling out to ``codex debug models``. Credentials are loaded
through :class:`~src.connectors.openai_codex.credentials.CredentialManager`
(legacy/current account only; managed-account selection is disabled for this
minimal version). The client never logs tokens, authorization headers,
credential JSON, or response bodies.
"""

from __future__ import annotations

import contextlib
import logging
from collections.abc import Mapping
from pathlib import Path
from typing import Any

import httpx

from src.connectors.openai_codex.catalog.config import (
    DEFAULT_CLIENT_VERSION,
    DEFAULT_DISCOVERY_TIMEOUT_SECONDS,
)
from src.connectors.openai_codex.catalog.interfaces import (
    ICodexCatalogCredentialProvider,
)

logger = logging.getLogger(__name__)

CODEX_CATALOG_URL = "https://chatgpt.com/backend-api/codex/models"
# Re-exported from ``config`` (the single source of truth) for backward
# compatibility with existing import sites.
DEFAULT_TIMEOUT_SECONDS = DEFAULT_DISCOVERY_TIMEOUT_SECONDS


class _UnauthorizedError(Exception):
    """Raised internally when the backend returns HTTP 401."""


class CodexCatalogEndpointClient:
    """Fetch the raw Codex model catalog over authenticated HTTP."""

    def __init__(
        self,
        *,
        http_client: httpx.AsyncClient | None = None,
        credential_manager: ICodexCatalogCredentialProvider | None = None,
        client_version: str = DEFAULT_CLIENT_VERSION,
        auth_path: Path | None = None,
        timeout_seconds: float = DEFAULT_TIMEOUT_SECONDS,
        base_url: str = CODEX_CATALOG_URL,
    ) -> None:
        self._http_client = http_client
        self._credential_manager = credential_manager
        self._client_version = client_version
        # ``~`` is never expanded by YAML or native Windows shells, so expand
        # here (config parsing and the credential manager do the same).
        self._auth_path = auth_path.expanduser() if auth_path is not None else None
        self._timeout_seconds = timeout_seconds
        self._base_url = base_url

    async def fetch(self) -> Mapping[str, Any] | None:
        """Return the raw catalog mapping, or ``None`` on any failure.

        The client only closes HTTP clients and credential managers it created
        itself; injected collaborators remain owned by the caller.
        """

        client = self._http_client
        owned_client = client is None
        if client is None:
            client = httpx.AsyncClient()

        manager = self._credential_manager
        owned_manager = manager is None
        if manager is None:
            # Imported lazily to avoid a package import cycle
            # (``openai_codex.credentials`` <- ``openai_codex`` package ->
            # ``catalog`` -> this module).
            from src.connectors.openai_codex.credentials import CredentialManager
            from src.connectors.openai_codex.managed_oauth_constants import (
                DEFAULT_STORAGE_PATH,
            )
            from src.connectors.openai_codex.managed_oauth_models import (
                ManagedOAuthConfig,
            )

            created_manager = CredentialManager(client)
            created_manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=False,
                    storage_path=DEFAULT_STORAGE_PATH,
                )
            )
            manager = created_manager

        try:
            try:
                await manager.initialize(self._auth_path, start_watcher=False)
            except Exception:
                logger.warning(
                    "Codex catalog discovery: credential initialization failed; "
                    "falling back.",
                    exc_info=True,
                )
                return None

            token = manager.get_access_token()
            if not token:
                logger.info(
                    "Codex catalog discovery: no Codex credentials available; "
                    "falling back to snapshot."
                )
                return None

            try:
                result = await self._request(client, manager, token)
            except _UnauthorizedError:
                if not await manager.refresh_access_token():
                    logger.info(
                        "Codex catalog discovery: token refresh failed; falling back."
                    )
                    return None
                refreshed_token = manager.get_access_token()
                if not refreshed_token:
                    logger.info(
                        "Codex catalog discovery: no token after refresh; falling back."
                    )
                    return None
                try:
                    result = await self._request(client, manager, refreshed_token)
                except _UnauthorizedError:
                    logger.info(
                        "Codex catalog discovery: authentication rejected after "
                        "refresh; falling back."
                    )
                    return None
            return result
        finally:
            if owned_manager:
                with contextlib.suppress(Exception):
                    await manager.shutdown()
            if owned_client:
                with contextlib.suppress(Exception):
                    await client.aclose()

    async def _request(
        self,
        client: httpx.AsyncClient,
        manager: ICodexCatalogCredentialProvider,
        token: str,
    ) -> Mapping[str, Any] | None:
        headers = {
            "Authorization": f"Bearer {token}",
            "Accept": "application/json",
            "User-Agent": f"codex_cli_rs/{self._client_version}",
        }
        account_id = manager.get_account_id()
        if isinstance(account_id, str) and account_id:
            headers["chatgpt-account-id"] = account_id

        try:
            response = await client.get(
                self._base_url,
                params={"client_version": self._client_version},
                headers=headers,
                timeout=self._timeout_seconds,
            )
        except httpx.TimeoutException:
            logger.warning(
                "Codex catalog discovery request timed out after %ss; falling back.",
                self._timeout_seconds,
            )
            return None
        except httpx.TransportError as exc:
            logger.warning(
                "Codex catalog discovery transport failure (%s); falling back.",
                exc.__class__.__name__,
            )
            return None

        if response.status_code == 401:
            raise _UnauthorizedError

        if not 200 <= response.status_code < 300:
            logger.warning(
                "Codex catalog discovery request failed with HTTP %s; falling back.",
                response.status_code,
            )
            return None

        try:
            raw = response.json()
        except ValueError:
            logger.warning(
                "Codex catalog discovery response was not valid JSON; falling back."
            )
            return None

        if not isinstance(raw, Mapping) or not isinstance(raw.get("models"), list):
            logger.warning(
                "Codex catalog discovery response had an unexpected shape; "
                "falling back."
            )
            return None
        return raw


__all__ = [
    "CODEX_CATALOG_URL",
    "DEFAULT_CLIENT_VERSION",
    "DEFAULT_TIMEOUT_SECONDS",
    "CodexCatalogEndpointClient",
]
