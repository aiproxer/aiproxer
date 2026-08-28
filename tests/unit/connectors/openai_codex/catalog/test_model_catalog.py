"""Tests for ``CodexModelCatalog`` query and reasoning-effort clamp behavior.

These define the contract the connectors depend on. The catalog is constructed
directly from profiles (no parsing) so tests focus on query/clamp logic.
"""

from __future__ import annotations

import pytest

from tests.unit.connectors.openai_codex.catalog.conftest import build_test_catalog


@pytest.fixture()
def catalog():
    return build_test_catalog()


class TestCatalogRoutableSlugs:
    def test_routable_slugs_exclude_cli_only_and_hidden(self, catalog) -> None:
        """Only api-accepted, visible models are routable, in catalog order."""
        assert catalog.routable_slugs() == ("gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5")

    def test_is_supported_true_for_routable(self, catalog) -> None:
        assert catalog.is_supported("gpt-5.6-sol") is True
        assert catalog.is_supported("gpt-5.6-luna") is True
        assert catalog.is_supported("gpt-5.5") is True

    def test_is_supported_case_insensitive(self, catalog) -> None:
        assert catalog.is_supported("GPT-5.6-SOL") is True
        assert catalog.is_supported("Gpt-5.5") is True

    def test_is_supported_false_for_cli_only_and_hidden(self, catalog) -> None:
        assert catalog.is_supported("gpt-5.3-codex-spark") is False
        assert catalog.is_supported("codex-auto-review") is False

    def test_is_supported_false_for_unknown(self, catalog) -> None:
        assert catalog.is_supported("gpt-4-codex") is False
        assert catalog.is_supported("") is False


class TestCatalogProfiles:
    def test_get_profile_returns_profile(self, catalog) -> None:
        profile = catalog.get_profile("gpt-5.6-sol")
        assert profile is not None
        assert profile.slug == "gpt-5.6-sol"
        assert profile.default_reasoning_level == "low"
        assert profile.api_accepted is True

    def test_get_profile_case_insensitive(self, catalog) -> None:
        assert catalog.get_profile("GPT-5.6-SOL") is not None

    def test_get_profile_none_for_unknown(self, catalog) -> None:
        assert catalog.get_profile("gpt-4-codex") is None

    def test_get_profile_for_cli_only_returns_profile(self, catalog) -> None:
        """CLI-only/hidden profiles are still queryable (just not routable)."""
        profile = catalog.get_profile("gpt-5.3-codex-spark")
        assert profile is not None
        assert profile.api_accepted is False


class TestSupportedReasoningLevels:
    def test_known_model_returns_its_levels(self, catalog) -> None:
        assert catalog.supported_reasoning_levels("gpt-5.6-sol") == (
            "low",
            "medium",
            "high",
            "xhigh",
            "max",
            "ultra",
        )
        assert catalog.supported_reasoning_levels("gpt-5.5") == (
            "low",
            "medium",
            "high",
            "xhigh",
        )

    def test_unknown_model_falls_back_to_baseline(self, catalog) -> None:
        """Unknown models get low/medium/high (no extended tiers)."""
        assert catalog.supported_reasoning_levels("gpt-4-codex") == (
            "low",
            "medium",
            "high",
        )

    def test_default_reasoning_level_known_model(self, catalog) -> None:
        assert catalog.default_reasoning_level("gpt-5.6-sol") == "low"
        assert catalog.default_reasoning_level("gpt-5.5") == "medium"

    def test_default_reasoning_level_unknown_uses_global_default(self, catalog) -> None:
        assert catalog.default_reasoning_level("gpt-4-codex") == "medium"


class TestEffortValidation:
    def test_is_valid_effort_true_for_known(self, catalog) -> None:
        for effort in ("low", "medium", "high", "xhigh", "max", "ultra"):
            assert catalog.is_valid_effort(effort) is True

    def test_is_valid_effort_case_insensitive(self, catalog) -> None:
        assert catalog.is_valid_effort("ULTRA") is True

    def test_is_valid_effort_false_for_unknown(self, catalog) -> None:
        assert catalog.is_valid_effort("nope") is False
        assert catalog.is_valid_effort("") is False


@pytest.mark.parametrize(
    ("model", "effort", "expected"),
    [
        # Supported efforts pass through unchanged.
        ("gpt-5.6-sol", "ultra", "ultra"),
        ("gpt-5.6-sol", "low", "low"),
        ("gpt-5.6-luna", "max", "max"),
        ("gpt-5.5", "xhigh", "xhigh"),
        # ultra downgrades to max on luna (supports max, not ultra).
        ("gpt-5.6-luna", "ultra", "max"),
        # max/ultra downgrade to xhigh on xhigh-only models.
        ("gpt-5.5", "max", "xhigh"),
        ("gpt-5.5", "ultra", "xhigh"),
        # Unknown models: xhigh/max/ultra downgrade to high (baseline top).
        ("gpt-4-codex", "xhigh", "high"),
        ("gpt-4-codex", "max", "high"),
        ("gpt-4-codex", "ultra", "high"),
        ("gpt-4-codex", "high", "high"),
        ("gpt-4-codex", "low", "low"),
    ],
)
def test_clamp_reasoning_effort(
    catalog, model: str, effort: str, expected: str
) -> None:
    assert catalog.clamp_reasoning_effort(model, effort) == expected


class TestModelsSupporting:
    def test_ultra_supported_only_by_sol(self, catalog) -> None:
        assert catalog.models_supporting("ultra") == ("gpt-5.6-sol",)

    def test_max_supported_by_sol_and_luna(self, catalog) -> None:
        assert catalog.models_supporting("max") == ("gpt-5.6-sol", "gpt-5.6-luna")

    def test_xhigh_supported_by_all_routable(self, catalog) -> None:
        assert catalog.models_supporting("xhigh") == (
            "gpt-5.6-sol",
            "gpt-5.6-luna",
            "gpt-5.5",
        )


class TestCatalogDerivedFields:
    def test_reasoning_effort_order(self, catalog) -> None:
        assert catalog.reasoning_effort_order == (
            "low",
            "medium",
            "high",
            "xhigh",
            "max",
            "ultra",
        )

    def test_default_reasoning_effort(self, catalog) -> None:
        assert catalog.default_reasoning_effort == "medium"

    def test_reasoning_effort_descriptions(self, catalog) -> None:
        descriptions = catalog.reasoning_effort_descriptions
        assert (
            descriptions["ultra"] == "Maximum reasoning with automatic task delegation"
        )
        assert descriptions["low"] == "Fast responses with lighter reasoning"
        assert len(descriptions) == 6


class TestCatalogVerbosity:
    def test_supports_verbosity_true(self, catalog) -> None:
        assert catalog.supports_verbosity("gpt-5.6-sol") is True
        assert catalog.supports_verbosity("GPT-5.6-LUNA") is True

    def test_supports_verbosity_false(self, catalog) -> None:
        assert catalog.supports_verbosity("gpt-5.5") is False
        assert catalog.supports_verbosity("unknown-model") is False

    def test_default_verbosity_for(self, catalog) -> None:
        assert catalog.default_verbosity_for("gpt-5.6-sol") == "low"
        assert catalog.default_verbosity_for("gpt-5.6-luna") == "medium"
        assert catalog.default_verbosity_for("gpt-5.5") is None
        assert catalog.default_verbosity_for("missing") is None
