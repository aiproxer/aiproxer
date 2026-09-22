"""Interfaces for the Codex model catalog subsystem.

Protocol-based contracts for the catalog components, used for dependency
inversion and test substitution. All connectors depend on
:class:`ICodexModelCatalog` (the query API) rather than the concrete catalog.
"""

from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import Any, Protocol, runtime_checkable

from src.connectors.openai_codex.catalog.types import (
    CodexModelCatalog,
    CodexModelReasoningProfile,
)


@runtime_checkable
class ICodexModelCatalog(Protocol):
    """Read-only query API over the Codex model catalog.

    Implemented by :class:`CodexModelCatalog` and injectable into the three
    Codex connector variants via DI.
    """

    reasoning_effort_order: tuple[str, ...]
    default_reasoning_effort: str
    reasoning_effort_descriptions: Mapping[str, str]

    def routable_slugs(self) -> tuple[str, ...]: ...

    def is_supported(self, slug: str) -> bool: ...

    def get_profile(self, slug: str) -> CodexModelReasoningProfile | None: ...

    def supported_reasoning_levels(self, slug: str) -> tuple[str, ...]: ...

    def default_reasoning_level(self, slug: str) -> str: ...

    def is_valid_effort(self, effort: str) -> bool: ...

    def clamp_reasoning_effort(self, slug: str, effort: str) -> str: ...

    def models_supporting(self, effort: str) -> tuple[str, ...]: ...

    def supports_verbosity(self, slug: str) -> bool: ...

    def default_verbosity_for(self, slug: str) -> str | None: ...


@runtime_checkable
class ICodexCatalogParser(Protocol):
    """Parses raw Codex backend catalog JSON into a ``CodexModelCatalog``."""

    def parse(self, raw: Mapping[str, Any]) -> CodexModelCatalog: ...


@runtime_checkable
class ICodexCatalogFallbackLoader(Protocol):
    """Loads the shipped fallback catalog snapshot (or an override path)."""

    def load(self) -> CodexModelCatalog: ...


@runtime_checkable
class ICodexCatalogEndpointClient(Protocol):
    """Fetches the raw Codex model catalog from the authenticated backend.

    Returns the raw JSON mapping (with a ``models`` list) on success, or
    ``None`` on any failure (missing credentials, timeout, transport failure,
    non-2xx response, malformed JSON, invalid shape, exhausted auth retry) so
    the caller can fall back to the shipped snapshot.
    """

    async def fetch(self) -> Mapping[str, Any] | None: ...


@runtime_checkable
class ICodexCatalogCredentialProvider(Protocol):
    """Minimal credential surface used by the catalog endpoint client.

    Structurally satisfied by
    :class:`src.connectors.openai_codex.credentials.CredentialManager`; allows
    tests to substitute a lightweight fake without implementing the full
    ``ICredentialManager`` ABC.
    """

    async def initialize(
        self, auth_path: Path | None, *, start_watcher: bool = True
    ) -> None: ...

    async def refresh_access_token(self) -> bool: ...

    def get_access_token(self) -> str | None: ...

    def get_account_id(self) -> str | None: ...

    async def shutdown(self) -> None: ...


@runtime_checkable
class ICodexCatalogDiscoveryService(Protocol):
    """Discovers the catalog at runtime via the authenticated Codex backend.

    Returns ``None`` on any failure (missing credentials, timeout, transport
    failure, malformed output) so the caller can fall back to the shipped
    snapshot.
    """

    async def discover(self) -> CodexModelCatalog | None: ...


@runtime_checkable
class ICodexModelCatalogProvider(Protocol):
    """Orchestrates discovery -> fallback and exposes the resolved catalog."""

    async def load(self) -> None:
        """Eagerly resolve the catalog (discovery, else fallback) and cache it."""
        ...

    def get_catalog(self) -> CodexModelCatalog:
        """Return the resolved catalog (call after :meth:`load`)."""
        ...

    def load_fallback_only(self) -> CodexModelCatalog:
        """Load and return the fallback snapshot synchronously (no discovery).

        Used when no DI catalog is available (e.g. tests, or when the startup
        discovery stage did not run).
        """
        ...


__all__ = [
    "ICodexCatalogCredentialProvider",
    "ICodexCatalogDiscoveryService",
    "ICodexCatalogEndpointClient",
    "ICodexCatalogFallbackLoader",
    "ICodexCatalogParser",
    "ICodexModelCatalog",
    "ICodexModelCatalogProvider",
]
