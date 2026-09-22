"""Configuration for Codex model catalog auto-discovery.

Lives under ``backends.openai_codex.extra.codex.model_catalog`` (and the v2 /
app-server equivalents). ``client_version`` declares the Codex protocol
compatibility level sent to the internal backend catalog endpoint; it must not
be automatically derived from npm's latest version.
"""

from __future__ import annotations

import logging
import os
from collections.abc import Mapping
from dataclasses import dataclass

logger = logging.getLogger(__name__)

# Canonical Codex protocol compatibility level sent as the ``client_version``
# query parameter. Single source of truth — the endpoint client, discovery
# service, and scripts import this instead of defining their own copy.
DEFAULT_CLIENT_VERSION = "0.156.0"
DEFAULT_DISCOVERY_TIMEOUT_SECONDS = 10.0


@dataclass(frozen=True, slots=True)
class CodexModelCatalogConfig:
    """Operator-tunable knobs for catalog discovery and fallback.

    Attributes:
        discovery_enabled: When True (default), fetch the catalog from the
            authenticated Codex backend at startup and use the parsed catalog;
            on any failure fall back to the shipped snapshot. When False, skip
            discovery and use the snapshot.
        fallback_path: Optional override path to a fallback catalog JSON file
            (same format as the backend catalog response). When None, the
            shipped snapshot under ``src/resources/codex/`` is used.
        codex_binary_path: Deprecated no-op, kept parse-compatible for one
            release. The CLI executable is no longer required and is never
            resolved or executed.
        discovery_timeout_seconds: HTTP request timeout for the catalog GET
            before falling back.
        client_version: Codex protocol compatibility level sent as the
            ``client_version`` query parameter (default ``0.156.0``).
        auth_path: Optional explicit path to a legacy/current ``auth.json``.
            When None, the credential manager discovers the default location.
    """

    discovery_enabled: bool = True
    fallback_path: str | None = None
    codex_binary_path: str | None = None
    discovery_timeout_seconds: float = DEFAULT_DISCOVERY_TIMEOUT_SECONDS
    client_version: str = DEFAULT_CLIENT_VERSION
    auth_path: str | None = None


DEFAULT_CODEX_MODEL_CATALOG_CONFIG = CodexModelCatalogConfig()

_TRUE_STRINGS = frozenset({"1", "true", "yes", "on"})


def _coerce_bool(value: object, default: bool) -> bool:
    if value is None:
        return default
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        return value.strip().lower() in _TRUE_STRINGS
    return bool(value)


def _coerce_str_or_none(value: object) -> str | None:
    if isinstance(value, str):
        stripped = value.strip()
        return stripped or None
    return None


def _expand_path(value: str | None) -> str | None:
    """Expand a leading ``~`` (and env vars) in an operator-provided path.

    YAML and native Windows shells never expand ``~`` for us, so documented
    examples like ``auth_path: ~/.codex/auth.json`` would otherwise be checked
    literally and silently miss. Expansion here covers config-file values; the
    endpoint client, provider, fallback loader, and credential manager apply
    the same expansion defensively at their own boundaries.
    """
    if value is None:
        return None
    expanded = os.path.expandvars(os.path.expanduser(value))
    return expanded or None


def _coerce_timeout(value: object, default: float) -> float:
    if value is None:
        return default
    if isinstance(value, int | float):
        seconds = float(value)
    elif isinstance(value, str):
        try:
            seconds = float(value.strip())
        except ValueError:
            return default
    else:
        return default
    if seconds <= 0:
        return default
    return seconds


def get_model_catalog_env_overrides() -> dict[str, object]:
    """Return per-key model-catalog overrides from the environment.

    Single source of truth for ``OPENAI_CODEX_MODEL_CATALOG_*`` handling so
    startup discovery (stage) and outbound requests (connector settings) apply
    the same effective configuration.
    """
    overrides: dict[str, object] = {}
    env_discovery_enabled = os.getenv("OPENAI_CODEX_MODEL_CATALOG_DISCOVERY_ENABLED")
    if env_discovery_enabled is not None:
        overrides["discovery_enabled"] = env_discovery_enabled
    env_fallback_path = os.getenv("OPENAI_CODEX_MODEL_CATALOG_FALLBACK_PATH")
    if env_fallback_path is not None and env_fallback_path.strip():
        overrides["fallback_path"] = env_fallback_path.strip()
    env_binary_path = os.getenv("OPENAI_CODEX_MODEL_CATALOG_BINARY_PATH")
    if env_binary_path is not None and env_binary_path.strip():
        overrides["codex_binary_path"] = env_binary_path.strip()
    env_timeout = os.getenv("OPENAI_CODEX_MODEL_CATALOG_DISCOVERY_TIMEOUT_SECONDS")
    if env_timeout is not None and env_timeout.strip():
        overrides["discovery_timeout_seconds"] = env_timeout.strip()
    env_client_version = os.getenv("OPENAI_CODEX_MODEL_CATALOG_CLIENT_VERSION")
    if env_client_version is not None and env_client_version.strip():
        overrides["client_version"] = env_client_version.strip()
    env_auth_path = os.getenv("OPENAI_CODEX_MODEL_CATALOG_AUTH_PATH")
    if env_auth_path is not None and env_auth_path.strip():
        overrides["auth_path"] = env_auth_path.strip()
    return overrides


def apply_model_catalog_env_overrides(
    base: Mapping[str, object] | None,
) -> dict[str, object]:
    """Merge environment overrides over a YAML/settings mapping."""
    merged: dict[str, object] = dict(base) if base else {}
    merged.update(get_model_catalog_env_overrides())
    return merged


def codex_model_catalog_config_from_mapping(
    raw: Mapping[str, object] | None,
) -> CodexModelCatalogConfig:
    """Build a :class:`CodexModelCatalogConfig` from a YAML/settings mapping."""
    if not raw:
        return DEFAULT_CODEX_MODEL_CATALOG_CONFIG
    codex_binary_path = _coerce_str_or_none(raw.get("codex_binary_path"))
    if codex_binary_path:
        logger.warning(
            "Codex model catalog: `codex_binary_path` is deprecated and ignored; "
            "discovery uses the authenticated backend catalog endpoint."
        )
    return CodexModelCatalogConfig(
        discovery_enabled=_coerce_bool(raw.get("discovery_enabled"), True),
        fallback_path=_expand_path(_coerce_str_or_none(raw.get("fallback_path"))),
        codex_binary_path=codex_binary_path,
        discovery_timeout_seconds=_coerce_timeout(
            raw.get("discovery_timeout_seconds"), DEFAULT_DISCOVERY_TIMEOUT_SECONDS
        ),
        client_version=(
            _coerce_str_or_none(raw.get("client_version")) or DEFAULT_CLIENT_VERSION
        ),
        auth_path=_expand_path(_coerce_str_or_none(raw.get("auth_path"))),
    )


__all__ = [
    "DEFAULT_CLIENT_VERSION",
    "DEFAULT_CODEX_MODEL_CATALOG_CONFIG",
    "DEFAULT_DISCOVERY_TIMEOUT_SECONDS",
    "CodexModelCatalogConfig",
    "apply_model_catalog_env_overrides",
    "codex_model_catalog_config_from_mapping",
    "get_model_catalog_env_overrides",
]
