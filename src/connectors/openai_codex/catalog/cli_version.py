"""Discover the latest Codex CLI protocol version.

Replicates the Codex CLI update check in ``codex-rs/tui/src/updates.rs``:

``GET https://api.github.com/repos/openai/codex/releases/latest``

then ``extract_version_from_latest_tag``, which strips the ``rust-v`` prefix
from ``tag_name``. The proxy uses that version as the Codex ``client_version``
query parameter and outbound ``version`` / User-Agent so newly released models
are advertised and accepted without a connector code change.

A successful fetch is written to a small cache file so a later GitHub outage
still has a last-known version. There is no hardcoded CLI version in this
module.
"""

from __future__ import annotations

import contextlib
import json
import logging
import os
from pathlib import Path
from typing import Any

import httpx

logger = logging.getLogger(__name__)

CODEX_LATEST_RELEASE_URL = "https://api.github.com/repos/openai/codex/releases/latest"
DEFAULT_FETCH_TIMEOUT_SECONDS = 10.0
_CACHE_ENV = "OPENAI_CODEX_CLI_VERSION_CACHE_PATH"
_DEFAULT_CACHE_PATH = Path("var/cache/codex_cli_latest_version.json")
_GITHUB_ACCEPT = "application/vnd.github+json"
_GITHUB_USER_AGENT = "llm-interactive-proxy"


def extract_version_from_latest_tag(latest_tag_name: object) -> str | None:
    """Return the CLI version encoded in a GitHub ``tag_name``, or ``None``."""
    if not isinstance(latest_tag_name, str):
        return None
    text = latest_tag_name.strip()
    prefix = "rust-v"
    if not text.startswith(prefix):
        return None
    version = text[len(prefix) :].strip()
    return version or None


def default_cli_version_cache_path() -> Path | None:
    """Return the on-disk cache path, or ``None`` when persistence is disabled.

    Pytest runs skip the default repo path so unit tests do not write
    ``var/cache``. An explicit ``OPENAI_CODEX_CLI_VERSION_CACHE_PATH`` always
    wins.
    """
    override = os.getenv(_CACHE_ENV)
    if override is not None and override.strip():
        return Path(os.path.expandvars(os.path.expanduser(override.strip())))
    if os.getenv("PYTEST_CURRENT_TEST"):
        return None
    return _DEFAULT_CACHE_PATH


class CodexCliLatestVersionClient:
    """Fetch the latest Codex CLI version from GitHub Releases."""

    def __init__(
        self,
        *,
        http_client: httpx.AsyncClient | None = None,
        timeout_seconds: float = DEFAULT_FETCH_TIMEOUT_SECONDS,
        url: str = CODEX_LATEST_RELEASE_URL,
    ) -> None:
        self._http_client = http_client
        self._timeout_seconds = timeout_seconds
        self._url = url

    async def fetch(self) -> str | None:
        """Return the latest CLI version string, or ``None`` on any failure."""
        client = self._http_client
        owned_client = False
        if client is None:
            if os.getenv("PYTEST_CURRENT_TEST") and not os.getenv(
                "OPENAI_CODEX_CLI_VERSION_ALLOW_NETWORK"
            ):
                logger.debug("Skipping GitHub Codex latest-release fetch under pytest.")
                return None
            client = httpx.AsyncClient()
            owned_client = True
        try:
            try:
                response = await client.get(
                    self._url,
                    headers={
                        "Accept": _GITHUB_ACCEPT,
                        "User-Agent": _GITHUB_USER_AGENT,
                    },
                    timeout=self._timeout_seconds,
                )
            except httpx.TimeoutException:
                logger.warning(
                    "Codex CLI latest-release request timed out after %ss.",
                    self._timeout_seconds,
                )
                return None
            except httpx.TransportError as exc:
                logger.warning(
                    "Codex CLI latest-release transport failure (%s).",
                    exc.__class__.__name__,
                )
                return None

            if not 200 <= response.status_code < 300:
                logger.warning(
                    "Codex CLI latest-release request failed with HTTP %s.",
                    response.status_code,
                )
                return None

            try:
                raw = response.json()
            except ValueError:
                logger.warning("Codex CLI latest-release response was not valid JSON.")
                return None

            if not isinstance(raw, dict):
                logger.warning(
                    "Codex CLI latest-release response had an unexpected shape."
                )
                return None

            version = extract_version_from_latest_tag(raw.get("tag_name"))
            if version is None:
                logger.warning(
                    "Codex CLI latest-release tag_name was missing or unparsable."
                )
            return version
        finally:
            if owned_client:
                with contextlib.suppress(Exception):
                    await client.aclose()


class CodexCliLatestVersionResolver:
    """Resolve the latest CLI version via GitHub, falling back to a cache file."""

    def __init__(
        self,
        *,
        cache_path: Path | None = None,
        client: CodexCliLatestVersionClient | None = None,
    ) -> None:
        if cache_path is None:
            cache_path = default_cli_version_cache_path()
        self._cache_path = cache_path
        self._client = client if client is not None else CodexCliLatestVersionClient()

    async def resolve(self) -> str | None:
        """Return GitHub latest, else the cached last-known version, else ``None``."""
        fetched = await self._client.fetch()
        if fetched:
            self.write_cache(fetched)
            return fetched
        cached = self.read_cache()
        if cached:
            logger.info(
                "Codex CLI latest-release unavailable; using cached version %s.",
                cached,
            )
        return cached

    def read_cache(self) -> str | None:
        path = self._cache_path
        if path is None:
            return None
        try:
            raw_text = path.expanduser().read_text(encoding="utf-8")
        except OSError:
            return None
        try:
            payload: Any = json.loads(raw_text)
        except ValueError:
            return None
        if not isinstance(payload, dict):
            return None
        version = payload.get("latest_version")
        if isinstance(version, str) and version.strip():
            return version.strip()
        return None

    def write_cache(self, version: str) -> None:
        path = self._cache_path
        if path is None:
            return
        path = path.expanduser()
        try:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(
                json.dumps({"latest_version": version}, ensure_ascii=False) + "\n",
                encoding="utf-8",
            )
        except OSError:
            logger.warning(
                "Failed to write Codex CLI version cache to %s.",
                path,
                exc_info=True,
            )


__all__ = [
    "CODEX_LATEST_RELEASE_URL",
    "DEFAULT_FETCH_TIMEOUT_SECONDS",
    "CodexCliLatestVersionClient",
    "CodexCliLatestVersionResolver",
    "default_cli_version_cache_path",
    "extract_version_from_latest_tag",
]
