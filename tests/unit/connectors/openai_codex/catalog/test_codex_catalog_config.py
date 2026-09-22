"""Tests for ``codex_model_catalog_config_from_mapping`` — config dict parsing."""

from __future__ import annotations

import logging

import pytest
from src.connectors.openai_codex.catalog.config import (
    DEFAULT_CODEX_MODEL_CATALOG_CONFIG,
    CodexModelCatalogConfig,
    codex_model_catalog_config_from_mapping,
)


class TestConfigDefaults:
    def test_none_mapping_returns_defaults(self) -> None:
        cfg = codex_model_catalog_config_from_mapping(None)
        assert cfg == DEFAULT_CODEX_MODEL_CATALOG_CONFIG
        assert cfg.discovery_enabled is True
        assert cfg.fallback_path is None
        assert cfg.codex_binary_path is None
        assert cfg.discovery_timeout_seconds == 10.0
        assert cfg.client_version == "0.156.0"
        assert cfg.auth_path is None

    def test_empty_mapping_returns_defaults(self) -> None:
        cfg = codex_model_catalog_config_from_mapping({})
        assert cfg == DEFAULT_CODEX_MODEL_CATALOG_CONFIG


class TestConfigOverrides:
    def test_explicit_overrides(self) -> None:
        cfg = codex_model_catalog_config_from_mapping(
            {
                "discovery_enabled": False,
                "fallback_path": "/etc/codex/catalog.json",
                "codex_binary_path": "/usr/local/bin/codex",
                "discovery_timeout_seconds": 5.0,
                "client_version": "0.154.0",
                "auth_path": "/home/u/.codex/auth.json",
            }
        )
        assert cfg == CodexModelCatalogConfig(
            discovery_enabled=False,
            fallback_path="/etc/codex/catalog.json",
            codex_binary_path="/usr/local/bin/codex",
            discovery_timeout_seconds=5.0,
            client_version="0.154.0",
            auth_path="/home/u/.codex/auth.json",
        )

    def test_partial_override_keeps_other_defaults(self) -> None:
        cfg = codex_model_catalog_config_from_mapping({"fallback_path": "/x"})
        assert cfg.fallback_path == "/x"
        assert cfg.discovery_enabled is True
        assert cfg.discovery_timeout_seconds == 10.0
        assert cfg.client_version == "0.156.0"
        assert cfg.auth_path is None

    def test_client_version_strips_whitespace(self) -> None:
        cfg = codex_model_catalog_config_from_mapping({"client_version": "  0.155.0  "})
        assert cfg.client_version == "0.155.0"

    def test_empty_client_version_falls_back_to_default(self) -> None:
        cfg = codex_model_catalog_config_from_mapping({"client_version": "   "})
        assert cfg.client_version == "0.156.0"

    def test_auth_path_strips_whitespace_and_empty_is_none(self) -> None:
        assert (
            codex_model_catalog_config_from_mapping({"auth_path": "  /a/b  "}).auth_path
            == "/a/b"
        )
        assert (
            codex_model_catalog_config_from_mapping({"auth_path": " "}).auth_path
            is None
        )

    def test_auth_path_expands_user(self, monkeypatch) -> None:
        """Documented ``~/.codex/auth.json`` examples must resolve literally."""
        monkeypatch.setenv("HOME", "/home/testuser")
        monkeypatch.setenv("USERPROFILE", "/home/testuser")
        cfg = codex_model_catalog_config_from_mapping(
            {"auth_path": "~/.codex/auth.json"}
        )
        assert cfg.auth_path is not None
        assert "~" not in cfg.auth_path
        assert cfg.auth_path.endswith(".codex/auth.json")

    def test_fallback_path_expands_user(self, monkeypatch) -> None:
        monkeypatch.setenv("HOME", "/home/testuser")
        monkeypatch.setenv("USERPROFILE", "/home/testuser")
        cfg = codex_model_catalog_config_from_mapping(
            {"fallback_path": "~/catalog.json"}
        )
        assert cfg.auth_path is None
        assert cfg.fallback_path is not None
        assert "~" not in cfg.fallback_path
        assert cfg.fallback_path.endswith("catalog.json")

    def test_deprecated_codex_binary_path_warns(
        self, caplog: pytest.LogCaptureFixture
    ) -> None:
        with caplog.at_level(logging.WARNING):
            cfg = codex_model_catalog_config_from_mapping(
                {"codex_binary_path": "/usr/local/bin/codex"}
            )
        assert cfg.codex_binary_path == "/usr/local/bin/codex"
        assert any(
            "codex_binary_path" in record.message and "deprecated" in record.message
            for record in caplog.records
        )

    def test_no_warning_when_codex_binary_path_absent(
        self, caplog: pytest.LogCaptureFixture
    ) -> None:
        with caplog.at_level(logging.WARNING):
            codex_model_catalog_config_from_mapping({"discovery_enabled": True})
        assert not any("deprecated" in record.message for record in caplog.records)


class TestConfigCoercion:
    @pytest.mark.parametrize(
        ("value", "expected"),
        [
            ("true", True),
            ("True", True),
            ("yes", True),
            ("on", True),
            ("1", True),
            ("false", False),
            ("0", False),
            ("no", False),
            (True, True),
            (False, False),
        ],
    )
    def test_discovery_enabled_string_coercion(
        self, value: object, expected: bool
    ) -> None:
        cfg = codex_model_catalog_config_from_mapping({"discovery_enabled": value})
        assert cfg.discovery_enabled is expected

    @pytest.mark.parametrize(
        ("value", "expected"),
        [
            ("5", 5.0),
            (5, 5.0),
            (5.0, 5.0),
            ("0.5", 0.5),
        ],
    )
    def test_timeout_coercion(self, value: object, expected: float) -> None:
        cfg = codex_model_catalog_config_from_mapping(
            {"discovery_timeout_seconds": value}
        )
        assert cfg.discovery_timeout_seconds == expected

    def test_fallback_path_strips_whitespace(self) -> None:
        cfg = codex_model_catalog_config_from_mapping({"fallback_path": "  /x  "})
        assert cfg.fallback_path == "/x"

    def test_empty_fallback_path_becomes_none(self) -> None:
        cfg = codex_model_catalog_config_from_mapping({"fallback_path": "   "})
        assert cfg.fallback_path is None

    def test_empty_codex_binary_path_becomes_none(self) -> None:
        cfg = codex_model_catalog_config_from_mapping({"codex_binary_path": ""})
        assert cfg.codex_binary_path is None

    def test_invalid_timeout_falls_back_to_default(self) -> None:
        cfg = codex_model_catalog_config_from_mapping(
            {"discovery_timeout_seconds": "not-a-number"}
        )
        assert cfg.discovery_timeout_seconds == 10.0

    def test_non_positive_timeout_falls_back_to_default(self) -> None:
        cfg = codex_model_catalog_config_from_mapping({"discovery_timeout_seconds": 0})
        assert cfg.discovery_timeout_seconds == 10.0
        cfg = codex_model_catalog_config_from_mapping({"discovery_timeout_seconds": -1})
        assert cfg.discovery_timeout_seconds == 10.0
