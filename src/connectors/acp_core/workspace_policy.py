"""Shared workspace path rules for ACP backends (session / request hints)."""

from __future__ import annotations

import os
from collections.abc import Callable, Mapping
from pathlib import Path
from typing import Any

ACP_BACKEND_TYPES: frozenset[str] = frozenset(
    {
        "gemini-cli-acp",
        "cursor-cli-acp",
        "agy-cli-acp",
        "eve-acp",
        "workbuddy-acp",
        # Local-agent workspace-required backend even though it speaks the Codex
        # app-server protocol (not ACP); shares the workspace resolution policy.
        "openai-codex-app-server",
    }
)

ACP_MISSING_PROJECT_WORKSPACE_CODE = "acp_missing_project_workspace"

ACP_WORKSPACE_OPTION_KEYS: tuple[str, ...] = (
    "project_dir",
    "workspace_path",
    "cwd",
    "project",
)


def _is_trivial_workspace_token(stripped: str) -> bool:
    """True for tokens that are never usable project roots (proxy CWD, etc.)."""

    return stripped in (".", "..")


def extract_workspace_override_from_mapping(m: Mapping[str, Any]) -> str | None:
    """Return the first non-empty workspace hint string from a single mapping."""

    for key in ACP_WORKSPACE_OPTION_KEYS:
        value = m.get(key)
        if isinstance(value, str):
            s = value.strip()
            if s and not _is_trivial_workspace_token(s):
                return s
    return None


def first_workspace_hint_str(*candidates: Mapping[str, Any] | None) -> str | None:
    """First non-empty workspace hint across mappings (extra_body, options, …)."""

    for mapping in candidates:
        if mapping is None:
            continue
        found = extract_workspace_override_from_mapping(mapping)
        if found is not None:
            return found
    return None


def is_usable_workspace_directory(path: Path) -> bool:
    """True when ``path`` is an existing readable directory."""

    return path.exists() and path.is_dir() and os.access(path, os.R_OK)


def resolve_backend_init_acp_workspace(
    *,
    project_dir: Any = None,
    workspace_path: Any = None,
    env_workspace: str | None = None,
    env_source_label: str,
    is_usable: Callable[[Path], bool] | None = None,
) -> tuple[Path | None, str | None]:
    """Resolve optional static workspace from init kwargs / env.

    Picks the first non-blank value in order ``project_dir``, ``workspace_path``,
    then ``env_workspace`` (skipping ``.``, ``..``, and empty strings). If that
    value is an **absolute** path and a readable directory, returns
    ``(resolved, None)``. If it is absolute but invalid or not readable, returns
    ``(None, error_message)`` for :class:`ConfigurationError`.

    Relative paths and absent/placeholder values yield ``(None, None)`` so
    backends can start without YAML workspace settings and rely on per-request
    ``project_dir`` from session enrichment.
    """

    checker = is_usable or is_usable_workspace_directory
    chosen: tuple[str, str] | None = None
    for label, raw in (
        ("project_dir", project_dir),
        ("workspace_path", workspace_path),
        (env_source_label, env_workspace),
    ):
        if raw is None:
            continue
        s = str(raw).strip()
        if not s or _is_trivial_workspace_token(s):
            continue
        chosen = (label, s)
        break

    if chosen is None:
        return None, None

    label, s = chosen
    try:
        p = Path(s).expanduser()
    except (OSError, RuntimeError) as e:
        return None, f"{label} is not a valid path ({s!r}): {e}"
    if not p.is_absolute():
        return None, None
    try:
        resolved = p.resolve()
    except (OSError, RuntimeError) as e:
        return None, f"{label} is not a valid path ({s!r}): {e}"
    if not checker(resolved):
        return None, f"{label} is not a readable directory: {s!r}"
    return resolved, None


def first_usable_workspace_dir(
    *candidates: Mapping[str, Any] | None,
    is_usable: Callable[[Path], bool] | None = None,
    require_absolute_hint: bool = False,
) -> Path | None:
    """Resolve the first usable directory from workspace hints across mappings."""

    checker = is_usable or is_usable_workspace_directory
    for mapping in candidates:
        if mapping is None:
            continue
        raw = extract_workspace_override_from_mapping(mapping)
        if raw is None:
            continue
        try:
            expanded = Path(raw).expanduser()
            if require_absolute_hint and not expanded.is_absolute():
                continue
            candidate = expanded.resolve()
        except (OSError, RuntimeError):
            continue
        if checker(candidate):
            return candidate
    return None
