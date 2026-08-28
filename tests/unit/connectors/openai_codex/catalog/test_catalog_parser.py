"""Tests for ``CodexCatalogParser`` — raw ``codex debug models`` JSON -> catalog."""

from __future__ import annotations

import pytest
from src.connectors.openai_codex.catalog.parser import CodexCatalogParser

from tests.unit.connectors.openai_codex.catalog.conftest import make_raw_catalog


@pytest.fixture()
def parser() -> CodexCatalogParser:
    return CodexCatalogParser()


class TestParserHappyPath:
    def test_parse_returns_catalog_with_routable_slugs(
        self, parser, raw_catalog
    ) -> None:
        catalog = parser.parse(raw_catalog)
        assert catalog.routable_slugs() == ("gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5")

    def test_parse_derives_effort_order_from_widest_model(
        self, parser, raw_catalog
    ) -> None:
        catalog = parser.parse(raw_catalog)
        # gpt-5.6-sol has the widest supported_reasoning_levels (6 tiers).
        assert catalog.reasoning_effort_order == (
            "low",
            "medium",
            "high",
            "xhigh",
            "max",
            "ultra",
        )

    def test_parse_aggregates_effort_descriptions(self, parser, raw_catalog) -> None:
        catalog = parser.parse(raw_catalog)
        descriptions = catalog.reasoning_effort_descriptions
        assert (
            descriptions["ultra"] == "Maximum reasoning with automatic task delegation"
        )
        assert descriptions["max"] == "Maximum reasoning depth for the hardest problems"
        assert len(descriptions) == 6

    def test_parse_default_reasoning_effort_is_medium(
        self, parser, raw_catalog
    ) -> None:
        assert parser.parse(raw_catalog).default_reasoning_effort == "medium"

    def test_parse_preserves_per_model_levels(self, parser, raw_catalog) -> None:
        catalog = parser.parse(raw_catalog)
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

    def test_parse_preserves_per_model_default(self, parser, raw_catalog) -> None:
        catalog = parser.parse(raw_catalog)
        assert catalog.default_reasoning_level("gpt-5.6-sol") == "low"
        assert catalog.default_reasoning_level("gpt-5.5") == "medium"
        assert catalog.default_reasoning_level("gpt-5.3-codex-spark") == "high"

    def test_parse_excludes_cli_only_and_hidden_from_routable(
        self, parser, raw_catalog
    ) -> None:
        catalog = parser.parse(raw_catalog)
        assert (
            catalog.is_supported("gpt-5.3-codex-spark") is False
        )  # supported_in_api=False
        assert catalog.is_supported("codex-auto-review") is False  # visibility=hide
        # ...but the profiles are still present (queryable).
        assert catalog.get_profile("gpt-5.3-codex-spark") is not None
        assert catalog.get_profile("codex-auto-review") is not None
        assert catalog.get_profile("gpt-5.3-codex-spark").api_accepted is False
        assert catalog.get_profile("codex-auto-review").api_accepted is False

    def test_parse_preserves_context_windows(self, parser, raw_catalog) -> None:
        catalog = parser.parse(raw_catalog)
        profile = catalog.get_profile("gpt-5.6-sol")
        assert profile is not None
        assert profile.context_window == 372000
        assert profile.max_context_window == 372000

    def test_parse_preserves_verbosity_fields(self, parser, raw_catalog) -> None:
        catalog = parser.parse(raw_catalog)
        sol = catalog.get_profile("gpt-5.6-sol")
        assert sol is not None
        assert sol.support_verbosity is True
        assert sol.default_verbosity == "low"
        # First-class fields must not remain only in extra
        assert "support_verbosity" not in sol.extra
        assert "default_verbosity" not in sol.extra

        unsupported = catalog.get_profile("gpt-5.5")
        assert unsupported is not None
        assert unsupported.support_verbosity is False
        assert unsupported.default_verbosity is None


class TestParserEdgeCases:
    def test_parse_empty_models(self, parser) -> None:
        catalog = parser.parse({"models": []})
        assert catalog.routable_slugs() == ()
        assert catalog.reasoning_effort_order == ()
        assert catalog.reasoning_effort_descriptions == {}

    def test_parse_missing_models_key_treated_as_empty(self, parser) -> None:
        catalog = parser.parse({})
        assert catalog.routable_slugs() == ()

    def test_parse_skips_malformed_entries(self, parser) -> None:
        raw = {
            "models": [
                {
                    "slug": "gpt-5.5",
                    "default_reasoning_level": "medium",
                },  # missing levels
                "not-a-dict",  # not a mapping
                {"default_reasoning_level": "medium"},  # missing slug
                make_raw_catalog()["models"][0],  # valid sol
            ]
        }
        catalog = parser.parse(raw)
        assert catalog.routable_slugs() == ("gpt-5.6-sol",)

    def test_parse_skips_levels_without_effort(self, parser) -> None:
        raw = {
            "models": [
                {
                    "slug": "gpt-5.5",
                    "default_reasoning_level": "medium",
                    "supported_reasoning_levels": [
                        {"effort": "low", "description": "d"},
                        {"description": "no effort key"},
                        {"effort": "high", "description": "d"},
                    ],
                    "visibility": "list",
                    "supported_in_api": True,
                }
            ]
        }
        catalog = parser.parse(raw)
        assert catalog.supported_reasoning_levels("gpt-5.5") == ("low", "high")

    def test_parse_defaults_visibility_and_supported_in_api(self, parser) -> None:
        raw = {
            "models": [
                {
                    "slug": "gpt-5.5",
                    "default_reasoning_level": "medium",
                    "supported_reasoning_levels": [
                        {"effort": "low", "description": "d"}
                    ],
                }
            ]
        }
        catalog = parser.parse(raw)
        profile = catalog.get_profile("gpt-5.5")
        assert profile is not None
        assert profile.visibility == "list"
        assert profile.supported_in_api is True
        assert catalog.is_supported("gpt-5.5") is True

    def test_parse_defaults_missing_default_reasoning_level(self, parser) -> None:
        raw = {
            "models": [
                {
                    "slug": "gpt-5.5",
                    "supported_reasoning_levels": [
                        {"effort": "low", "description": "d"}
                    ],
                    "supported_in_api": True,
                }
            ]
        }
        catalog = parser.parse(raw)
        assert catalog.default_reasoning_level("gpt-5.5") == "medium"

    def test_parse_widest_model_not_first_defines_order(self, parser) -> None:
        raw = {
            "models": [
                {
                    "slug": "gpt-5.5",
                    "default_reasoning_level": "medium",
                    "supported_reasoning_levels": [
                        {"effort": "low", "description": "d"},
                        {"effort": "high", "description": "d"},
                    ],
                    "supported_in_api": True,
                },
                {
                    "slug": "gpt-5.6-sol",
                    "default_reasoning_level": "low",
                    "supported_reasoning_levels": [
                        {"effort": "low", "description": "d"},
                        {"effort": "medium", "description": "d"},
                        {"effort": "high", "description": "d"},
                        {"effort": "ultra", "description": "d"},
                    ],
                    "supported_in_api": True,
                },
            ]
        }
        catalog = parser.parse(raw)
        # Widest model (gpt-5.6-sol, 4 levels) defines the order.
        assert catalog.reasoning_effort_order == ("low", "medium", "high", "ultra")

    def test_parse_clamp_uses_parsed_profiles(self, parser, raw_catalog) -> None:
        catalog = parser.parse(raw_catalog)
        assert catalog.clamp_reasoning_effort("gpt-5.6-luna", "ultra") == "max"
        assert catalog.clamp_reasoning_effort("gpt-5.5", "max") == "xhigh"
