"""Tests for ``CodexCatalogFallbackLoader`` — shipped snapshot / override path."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from src.connectors.openai_codex.catalog.fallback_loader import (
    CodexCatalogFallbackLoader,
)
from src.connectors.openai_codex.catalog.parser import CodexCatalogParser

from tests.unit.connectors.openai_codex.catalog.conftest import (
    FakeParser,
    sentinel_catalog,
    write_raw_catalog,
)


class TestFallbackLoaderOverridePath:
    def test_load_override_path_reads_file_and_delegates_to_parser(
        self, tmp_path: Path, raw_catalog
    ) -> None:
        catalog_file = write_raw_catalog(tmp_path / "catalog.json")
        fake_parser = FakeParser(result=sentinel_catalog())

        loader = CodexCatalogFallbackLoader(
            fallback_path=str(catalog_file), parser=fake_parser
        )

        result = loader.load()

        assert result is fake_parser._result
        assert fake_parser.calls == 1
        # The loader must read the file as JSON and pass the dict to the parser.
        assert fake_parser.last_raw == raw_catalog

    def test_load_override_path_missing_raises(self, tmp_path: Path) -> None:
        loader = CodexCatalogFallbackLoader(
            fallback_path=str(tmp_path / "does-not-exist.json"),
            parser=FakeParser(),
        )
        with pytest.raises(FileNotFoundError):
            loader.load()

    def test_load_override_path_invalid_json_raises(self, tmp_path: Path) -> None:
        bad = tmp_path / "bad.json"
        bad.write_text("not json {", encoding="utf-8")
        loader = CodexCatalogFallbackLoader(fallback_path=str(bad), parser=FakeParser())
        with pytest.raises(json.JSONDecodeError):
            loader.load()

    def test_load_override_path_with_default_parser_parses_catalog(
        self, tmp_path: Path
    ) -> None:
        """Integration: default parser produces a queryable catalog from the file."""
        catalog_file = write_raw_catalog(tmp_path / "catalog.json")
        loader = CodexCatalogFallbackLoader(fallback_path=str(catalog_file))

        catalog = loader.load()

        assert catalog.routable_slugs() == ("gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5")
        assert catalog.is_supported("gpt-5.6-sol") is True


class TestFallbackLoaderShippedResource:
    def test_load_shipped_resource_without_override(self) -> None:
        """When no override path is set, load the shipped snapshot resource."""
        loader = CodexCatalogFallbackLoader()
        catalog = loader.load()
        # The shipped snapshot must parse to a non-empty routable catalog.
        assert len(catalog.routable_slugs()) > 0
        assert "gpt-5.6-sol" in catalog.routable_slugs()
        assert catalog.reasoning_effort_order  # derived, non-empty
        assert catalog.default_reasoning_effort == "medium"


def test_fallback_loader_satisfies_protocol() -> None:
    from src.connectors.openai_codex.catalog.interfaces import (
        ICodexCatalogFallbackLoader,
    )

    assert isinstance(
        CodexCatalogFallbackLoader(fallback_path=None, parser=CodexCatalogParser()),
        ICodexCatalogFallbackLoader,
    )
