"""Packaging contract tests for extracted OAuth connectors.

These tests pin requirements 1.2 and 1.4 from the oauth extraction spec.
"""

from __future__ import annotations

import re
from importlib import metadata
from pathlib import Path

import pytest
import tomli


def _project_root() -> Path:
    return Path(__file__).resolve().parents[4]


def _load_toml(path: Path) -> dict:
    with path.open("rb") as handle:
        return tomli.load(handle)


def _normalize_dependency_name(spec: str) -> str:
    return re.split(r"[<>=!\[\s]", spec)[0].strip().lower().replace("_", "-")


def test_core_distribution_exposes_oauth_extra_for_plugin_package() -> None:
    """Core package should provide oauth extra installing plugin distribution."""
    core_pyproject = _project_root() / "pyproject.toml"
    pyproject_data = _load_toml(core_pyproject)
    optional_deps = pyproject_data.get("project", {}).get("optional-dependencies", {})

    oauth_extra = optional_deps.get("oauth")
    assert isinstance(oauth_extra, list)
    assert "llm-interactive-proxy-oauth-connectors" in oauth_extra


def test_oauth_specific_dependency_is_not_required_by_core_distribution() -> None:
    """OAuth-only dependencies should not be mandatory for core package."""
    root = _project_root()
    core_pyproject = _load_toml(root / "pyproject.toml")

    core_dependency_names = {
        _normalize_dependency_name(spec)
        for spec in core_pyproject.get("project", {}).get("dependencies", [])
    }
    assert "google-auth-oauthlib" not in core_dependency_names

    try:
        plugin_requires = (
            metadata.requires("llm-interactive-proxy-oauth-connectors") or []
        )
    except metadata.PackageNotFoundError:
        pytest.skip("OAuth plugin package not installed in this environment")

    plugin_dependency_names = {
        _normalize_dependency_name(spec) for spec in plugin_requires
    }
    assert "google-auth-oauthlib" in plugin_dependency_names
