"""Shared fixtures and fakes for the Codex catalog subsystem tests."""

from __future__ import annotations

import json
from collections.abc import Mapping
from pathlib import Path
from typing import Any

import pytest
from src.connectors.openai_codex.catalog.interfaces import (
    ICodexCatalogDiscoveryService,
    ICodexCatalogFallbackLoader,
    ICodexCatalogParser,
)
from src.connectors.openai_codex.catalog.types import (
    CodexModelCatalog,
    CodexModelReasoningProfile,
)

# Reasoning effort descriptions verbatim from `codex debug models`.
_EFFORT_DESCRIPTIONS = {
    "low": "Fast responses with lighter reasoning",
    "medium": "Balances speed and reasoning depth for everyday tasks",
    "high": "Greater reasoning depth for complex problems",
    "xhigh": "Extra high reasoning depth for complex problems",
    "max": "Maximum reasoning depth for the hardest problems",
    "ultra": "Maximum reasoning with automatic task delegation",
}


def _levels(*efforts: str) -> list[dict[str, str]]:
    return [{"effort": e, "description": _EFFORT_DESCRIPTIONS[e]} for e in efforts]


def make_raw_catalog() -> dict[str, Any]:
    """Return a sample raw ``codex debug models`` payload covering all edge cases.

    Includes:
    - gpt-5.6-sol: full ultra tier, api-accepted (routable).
    - gpt-5.6-luna: max tier (no ultra), api-accepted (routable).
    - gpt-5.5: xhigh-only, api-accepted (routable).
    - gpt-5.3-codex-spark: CLI-only (supported_in_api=False) -> NOT routable.
    - codex-auto-review: hidden (visibility=hide) -> NOT routable.
    """
    return {
        "models": [
            {
                "slug": "gpt-5.6-sol",
                "display_name": "GPT-5.6-Sol",
                "default_reasoning_level": "low",
                "supported_reasoning_levels": _levels(
                    "low", "medium", "high", "xhigh", "max", "ultra"
                ),
                "visibility": "list",
                "supported_in_api": True,
                "support_verbosity": True,
                "default_verbosity": "low",
                "context_window": 372000,
                "max_context_window": 372000,
            },
            {
                "slug": "gpt-5.6-luna",
                "display_name": "GPT-5.6-Luna",
                "default_reasoning_level": "medium",
                "supported_reasoning_levels": _levels(
                    "low", "medium", "high", "xhigh", "max"
                ),
                "visibility": "list",
                "supported_in_api": True,
                "support_verbosity": True,
                "default_verbosity": "medium",
                "context_window": 372000,
                "max_context_window": 372000,
            },
            {
                "slug": "gpt-5.5",
                "display_name": "GPT-5.5",
                "default_reasoning_level": "medium",
                "supported_reasoning_levels": _levels("low", "medium", "high", "xhigh"),
                "visibility": "list",
                "supported_in_api": True,
                "support_verbosity": False,
                "context_window": 272000,
                "max_context_window": 272000,
            },
            {
                "slug": "gpt-5.3-codex-spark",
                "display_name": "GPT-5.3-Codex-Spark",
                "default_reasoning_level": "high",
                "supported_reasoning_levels": _levels("low", "medium", "high", "xhigh"),
                "visibility": "list",
                "supported_in_api": False,
                "context_window": 128000,
                "max_context_window": 128000,
            },
            {
                "slug": "codex-auto-review",
                "display_name": "Codex Auto Review",
                "default_reasoning_level": "medium",
                "supported_reasoning_levels": _levels("low", "medium", "high", "xhigh"),
                "visibility": "hide",
                "supported_in_api": True,
                "context_window": 272000,
                "max_context_window": 1000000,
            },
        ]
    }


def write_raw_catalog(path: Path, raw: dict[str, Any] | None = None) -> Path:
    """Write a raw catalog JSON to ``path`` and return it."""
    path.write_text(
        json.dumps(raw if raw is not None else make_raw_catalog()), encoding="utf-8"
    )
    return path


def build_test_catalog() -> CodexModelCatalog:
    """Build a real ``CodexModelCatalog`` from the sample profiles + derived order."""
    raw = make_raw_catalog()
    profiles: dict[str, CodexModelReasoningProfile] = {}
    for entry in raw["models"]:
        slug = entry["slug"]
        levels = tuple(lv["effort"] for lv in entry["supported_reasoning_levels"])
        profiles[slug.lower()] = CodexModelReasoningProfile(
            slug=slug,
            default_reasoning_level=entry["default_reasoning_level"],
            supported_reasoning_levels=levels,
            visibility=entry.get("visibility", "list"),
            supported_in_api=entry.get("supported_in_api", True),
            context_window=entry.get("context_window"),
            max_context_window=entry.get("max_context_window"),
            support_verbosity=bool(entry.get("support_verbosity", False)),
            default_verbosity=(
                entry["default_verbosity"]
                if isinstance(entry.get("default_verbosity"), str)
                else None
            ),
        )
    order = ("low", "medium", "high", "xhigh", "max", "ultra")
    descriptions = dict(_EFFORT_DESCRIPTIONS)
    return CodexModelCatalog(
        profiles=profiles,
        reasoning_effort_order=order,
        default_reasoning_effort="medium",
        reasoning_effort_descriptions=descriptions,
    )


def sentinel_catalog() -> CodexModelCatalog:
    """An empty catalog instance usable as an identity sentinel in tests."""
    return CodexModelCatalog(profiles={})


class FakeParser:
    """Parser fake: records the raw payload and returns a configured catalog."""

    def __init__(self, *, result: CodexModelCatalog | None = None) -> None:
        self._result = result if result is not None else sentinel_catalog()
        self.last_raw: Mapping[str, Any] | None = None
        self.calls = 0

    def parse(self, raw: Mapping[str, Any]) -> CodexModelCatalog:
        self.calls += 1
        self.last_raw = raw
        return self._result


class FakeFallback:
    """Fallback loader fake."""

    def __init__(self, *, result: CodexModelCatalog | None = None) -> None:
        self._result = result if result is not None else sentinel_catalog()
        self.calls = 0

    def load(self) -> CodexModelCatalog:
        self.calls += 1
        return self._result


class FakeDiscovery:
    """Discovery service fake."""

    def __init__(
        self,
        *,
        result: CodexModelCatalog | None = None,
        raises: BaseException | None = None,
    ) -> None:
        self._result = result
        self._raises = raises
        self.calls = 0

    async def discover(self) -> CodexModelCatalog | None:
        self.calls += 1
        if self._raises is not None:
            raise self._raises
        return self._result


# Make the fakes structurally satisfy the protocols (for isinstance/typing).
assert isinstance(FakeParser(), ICodexCatalogParser)  # type: ignore[abstract]
assert isinstance(FakeFallback(), ICodexCatalogFallbackLoader)  # type: ignore[abstract]
assert isinstance(FakeDiscovery(), ICodexCatalogDiscoveryService)  # type: ignore[abstract]


@pytest.fixture()
def raw_catalog() -> dict[str, Any]:
    return make_raw_catalog()


@pytest.fixture()
def test_catalog() -> CodexModelCatalog:
    return build_test_catalog()
