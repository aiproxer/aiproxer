"""Tests for ``CodexModelCatalogStage`` — startup discovery + DI registration."""

from __future__ import annotations

import json
from dataclasses import asdict
from pathlib import Path
from typing import Any, cast

import pytest
from src.connectors.openai_codex.catalog.config import (
    DEFAULT_CODEX_MODEL_CATALOG_CONFIG,
    CodexModelCatalogConfig,
)
from src.core.app.stages.codex_model_catalog import (
    CodexModelCatalogStage,
    resolve_codex_model_catalog_config,
)
from src.core.config.app_config import AppConfig, BackendConfig
from src.core.di.container import ServiceCollection

_MIN_RAW_CATALOG = {
    "models": [
        {
            "slug": "gpt-5.6-sol",
            "default_reasoning_level": "low",
            "supported_reasoning_levels": [
                {"effort": "low", "description": "d"},
                {"effort": "medium", "description": "d"},
                {"effort": "high", "description": "d"},
                {"effort": "xhigh", "description": "d"},
                {"effort": "max", "description": "d"},
                {"effort": "ultra", "description": "d"},
            ],
            "visibility": "list",
            "supported_in_api": True,
        },
        {
            "slug": "gpt-5.5",
            "default_reasoning_level": "medium",
            "supported_reasoning_levels": [
                {"effort": "low", "description": "d"},
                {"effort": "medium", "description": "d"},
                {"effort": "high", "description": "d"},
                {"effort": "xhigh", "description": "d"},
            ],
            "visibility": "list",
            "supported_in_api": True,
        },
    ]
}


def _write_catalog(path: Path) -> Path:
    path.write_text(json.dumps(_MIN_RAW_CATALOG), encoding="utf-8")
    return path


def _config_with_model_catalog(model_catalog: dict) -> AppConfig:
    backend = BackendConfig(extra={"codex": {"model_catalog": model_catalog}})
    base = AppConfig()
    return base.model_copy(
        update={"backends": base.backends.model_copy(update={"openai_codex": backend})}
    )


def _assert_catalog_config_equal(
    actual: CodexModelCatalogConfig,
    expected: CodexModelCatalogConfig,
) -> None:
    """Compare catalog configs by fields, not class identity.

    Autodiscovery tests may reload ``src.connectors*`` mid-suite; comparing
    dataclass instances from different class objects fails even when values
    match. Field comparison stays stable across reloads.
    """
    assert asdict(actual) == asdict(expected)


class TestStageMetadata:
    def test_name(self) -> None:
        assert CodexModelCatalogStage().name == "codex_model_catalog"

    def test_dependencies(self) -> None:
        assert CodexModelCatalogStage().get_dependencies() == ["core_services"]


class TestResolveModelCatalogConfig:
    def test_defaults_when_no_model_catalog_section(self) -> None:
        config = AppConfig()
        cfg = resolve_codex_model_catalog_config(config)
        _assert_catalog_config_equal(cfg, DEFAULT_CODEX_MODEL_CATALOG_CONFIG)

    def test_reads_from_openai_codex_backend(self) -> None:
        config = _config_with_model_catalog(
            {
                "discovery_enabled": False,
                "fallback_path": "/x/catalog.json",
                "discovery_timeout_seconds": 7.0,
            }
        )
        cfg = resolve_codex_model_catalog_config(config)
        _assert_catalog_config_equal(
            cfg,
            CodexModelCatalogConfig(
                discovery_enabled=False,
                fallback_path="/x/catalog.json",
                discovery_timeout_seconds=7.0,
            ),
        )

    def test_reads_from_v2_when_v1_absent(self) -> None:
        base = AppConfig()
        v2_backend = BackendConfig(
            extra={"codex": {"model_catalog": {"discovery_enabled": False}}}
        )
        config = base.model_copy(
            update={
                "backends": base.backends.model_copy(
                    update={
                        "openai_codex": None,
                        "openai_codex_v2": v2_backend,
                    }
                )
            }
        )
        cfg = resolve_codex_model_catalog_config(config)
        assert cfg.discovery_enabled is False


class TestResolveAuthPathPrecedence:
    def _config_with_backend(self, backend: BackendConfig) -> AppConfig:
        base = AppConfig()
        return base.model_copy(
            update={
                "backends": base.backends.model_copy(update={"openai_codex": backend})
            }
        )

    def test_model_catalog_auth_path_wins(self) -> None:
        backend = BackendConfig(
            credentials_path="/backend/creds.json",
            extra={
                "openai_codex_path": "/legacy/dir",
                "codex": {
                    "model_catalog": {"auth_path": "/explicit/model-catalog-auth.json"}
                },
            },
        )
        cfg = resolve_codex_model_catalog_config(self._config_with_backend(backend))
        assert cfg.auth_path == "/explicit/model-catalog-auth.json"

    def test_backend_credentials_path_when_no_explicit_auth_path(self) -> None:
        backend = BackendConfig(
            credentials_path="/backend/creds.json",
            extra={
                "openai_codex_path": "/legacy/dir",
                "codex": {"model_catalog": {}},
            },
        )
        cfg = resolve_codex_model_catalog_config(self._config_with_backend(backend))
        assert cfg.auth_path == "/backend/creds.json"

    def test_legacy_openai_codex_path_when_no_credentials_path(self) -> None:
        backend = BackendConfig(
            extra={
                "openai_codex_path": "/legacy/dir",
                "codex": {"model_catalog": {}},
            }
        )
        cfg = resolve_codex_model_catalog_config(self._config_with_backend(backend))
        assert cfg.auth_path == "/legacy/dir"

    def test_none_when_no_backend_paths(self) -> None:
        backend = BackendConfig(extra={"codex": {"model_catalog": {}}})
        cfg = resolve_codex_model_catalog_config(self._config_with_backend(backend))
        assert cfg.auth_path is None

    def test_no_model_catalog_section_still_honors_backend_auth_path(self) -> None:
        # Discovery runs with defaults even without a section, so the
        # backend's credentials paths must not be silently dropped.
        backend = BackendConfig(credentials_path="/backend/creds.json")
        cfg = resolve_codex_model_catalog_config(self._config_with_backend(backend))
        assert cfg.auth_path == "/backend/creds.json"
        assert cfg.discovery_enabled is True

    def test_defaults_when_no_backend_at_all(self) -> None:
        config = AppConfig()
        cfg = resolve_codex_model_catalog_config(config)
        _assert_catalog_config_equal(cfg, DEFAULT_CODEX_MODEL_CATALOG_CONFIG)


class TestStageExecute:
    @pytest.mark.asyncio
    async def test_registers_catalog_from_fallback_file(self, tmp_path: Path) -> None:
        catalog_file = _write_catalog(tmp_path / "catalog.json")
        config = _config_with_model_catalog(
            {
                "discovery_enabled": False,
                "fallback_path": str(catalog_file),
            }
        )

        services = ServiceCollection()
        stage = CodexModelCatalogStage()
        await stage.execute(services, config)

        # Import the interface after execute so DI key matches the stage's type
        # object even if connector modules were reloaded earlier in the suite.
        from src.connectors.openai_codex.catalog.interfaces import ICodexModelCatalog

        provider = services.build_service_provider()
        catalog: Any = provider.get_required_service(cast(type, ICodexModelCatalog))
        assert catalog.routable_slugs() == ("gpt-5.6-sol", "gpt-5.5")
        assert catalog.is_supported("gpt-5.6-sol") is True

    @pytest.mark.asyncio
    async def test_registers_catalog_even_when_no_model_catalog_section(
        self, tmp_path: Path, monkeypatch
    ) -> None:
        """With defaults (discovery enabled) but discovery unavailable and a
        fallback override, the stage still registers a catalog."""
        catalog_file = _write_catalog(tmp_path / "catalog.json")
        # Force discovery to fail so it falls back to the override path.
        from src.connectors.openai_codex.catalog.endpoint_client import (
            CodexCatalogEndpointClient,
        )

        async def _no_fetch(self) -> None:
            return None

        monkeypatch.setattr(CodexCatalogEndpointClient, "fetch", _no_fetch)
        config = _config_with_model_catalog(
            {"discovery_enabled": True, "fallback_path": str(catalog_file)}
        )

        services = ServiceCollection()
        stage = CodexModelCatalogStage()
        await stage.execute(services, config)

        from src.connectors.openai_codex.catalog.interfaces import ICodexModelCatalog

        provider = services.build_service_provider()
        catalog: Any = provider.get_required_service(cast(type, ICodexModelCatalog))
        assert catalog.routable_slugs() == ("gpt-5.6-sol", "gpt-5.5")
