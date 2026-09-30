"""Unit tests for SettingsLoader service.

Tests cover configuration normalization, precedence order, and edge cases.
"""

from __future__ import annotations

import os
from dataclasses import asdict
from unittest.mock import patch

import pytest
from src.connectors._openai_codex_capabilities import CodexClientCapabilities
from src.connectors.openai_codex import (
    managed_oauth_constants as managed_oauth_constants_mod,
)
from src.connectors.openai_codex.catalog.config import (
    DEFAULT_CODEX_MODEL_CATALOG_CONFIG,
)
from src.connectors.openai_codex.interfaces import ISettingsLoader
from src.connectors.openai_codex.managed_oauth_constants import (
    DEFAULT_ALLOW_LEGACY_FALLBACK,
    DEFAULT_REFRESH_BUFFER_SECONDS,
    DEFAULT_SELECTION_STRATEGY,
)
from src.connectors.openai_codex.settings import SettingsLoader
from src.core.config.app_config import AppConfig, BackendConfig


def _with_openai_codex_backend(app: AppConfig, backend: BackendConfig) -> AppConfig:
    return app.model_copy(
        update={"backends": app.backends.model_copy(update={"openai_codex": backend})}
    )


class TestSettingsLoader:
    """Test SettingsLoader service implementation."""

    @pytest.fixture
    def loader(self):
        """Create a SettingsLoader instance for testing."""
        return SettingsLoader()

    @pytest.fixture
    def app_config(self):
        """Create a basic app config for testing."""
        return AppConfig()

    def test_loader_implements_interface(self, loader):
        """Verify loader implements ISettingsLoader interface."""
        assert isinstance(loader, ISettingsLoader)

    def test_load_defaults(self, loader, app_config):
        """Test loading settings with default values."""
        settings = loader.load(app_config)

        assert settings.default_capabilities == CodexClientCapabilities(
            tool_schema_mode="custom_only",
            bypass_tool_call_reactor=True,
            include_environment_context=False,
        )
        assert settings.agent_overrides == {}
        assert settings.renderer["default"] == "none"
        assert settings.renderer["fallback"] == "summary"
        assert settings.prompt["template"] is None
        assert settings.prompt["deduplicate"] is True
        assert settings.prompt["fallback_to_default"] is True
        assert settings.tool_schema["base_tools"] is None
        assert settings.tool_schema["custom_tools"] == []
        assert settings.streaming["max_retries"] == 2
        assert settings.streaming["retry_backoff_seconds"] == (0.5, 1.5, 3.0)
        assert settings.compatibility_layer["enabled"] is False
        assert settings.managed_oauth["enabled"] is True
        assert (
            settings.managed_oauth["storage_path"]
            == managed_oauth_constants_mod.DEFAULT_STORAGE_PATH
        )
        assert (
            settings.managed_oauth["selection_strategy"] == DEFAULT_SELECTION_STRATEGY
        )
        assert (
            settings.managed_oauth["refresh_buffer_seconds"]
            == DEFAULT_REFRESH_BUFFER_SECONDS
        )
        assert (
            settings.managed_oauth["allow_legacy_fallback"]
            == DEFAULT_ALLOW_LEGACY_FALLBACK
        )

    def test_model_catalog_defaults(self, loader, app_config):
        """Model catalog config defaults to discovery enabled, no override path."""
        settings = loader.load(app_config)
        assert settings.model_catalog == asdict(DEFAULT_CODEX_MODEL_CATALOG_CONFIG)
        assert settings.model_catalog["discovery_enabled"] is True
        assert settings.model_catalog["fallback_path"] is None
        assert settings.model_catalog["codex_binary_path"] is None
        assert settings.model_catalog["discovery_timeout_seconds"] == 10.0
        assert settings.model_catalog["client_version"] is None

    def test_early_session_verbosity_bump_defaults(self, loader, app_config):
        settings = loader.load(app_config)
        assert settings.early_session_verbosity_bump == {
            "enabled": True,
            "max_turns": 5,
        }

    def test_early_session_verbosity_bump_opt_out_from_yaml(self, loader, app_config):
        backend_config = BackendConfig(
            extra={
                "codex": {
                    "early_session_verbosity_bump": {
                        "enabled": False,
                        "max_turns": 2,
                    }
                }
            }
        )
        settings = loader.load(_with_openai_codex_backend(app_config, backend_config))
        assert settings.early_session_verbosity_bump == {
            "enabled": False,
            "max_turns": 2,
        }

    def test_model_catalog_from_yaml(self, loader, app_config):
        """Model catalog section is parsed from extra.codex.model_catalog."""
        backend_config = BackendConfig(
            extra={
                "codex": {
                    "model_catalog": {
                        "discovery_enabled": False,
                        "fallback_path": "/etc/codex/catalog.json",
                        "codex_binary_path": "/usr/local/bin/codex",
                        "discovery_timeout_seconds": 5.0,
                    }
                }
            }
        )
        app_config = _with_openai_codex_backend(app_config, backend_config)

        settings = loader.load(app_config)

        assert settings.model_catalog["discovery_enabled"] is False
        assert settings.model_catalog["fallback_path"] == "/etc/codex/catalog.json"
        assert settings.model_catalog["codex_binary_path"] == "/usr/local/bin/codex"
        assert settings.model_catalog["discovery_timeout_seconds"] == 5.0

    def test_model_catalog_env_overrides_yaml(self, loader, app_config):
        """ENV overrides take precedence over YAML for model catalog settings."""
        backend_config = BackendConfig(
            extra={
                "codex": {
                    "model_catalog": {
                        "discovery_enabled": True,
                        "fallback_path": "/yaml/path.json",
                        "discovery_timeout_seconds": 5.0,
                    }
                }
            }
        )
        app_config = _with_openai_codex_backend(app_config, backend_config)

        with patch.dict(
            os.environ,
            {
                "OPENAI_CODEX_MODEL_CATALOG_DISCOVERY_ENABLED": "false",
                "OPENAI_CODEX_MODEL_CATALOG_FALLBACK_PATH": "/env/path.json",
                "OPENAI_CODEX_MODEL_CATALOG_BINARY_PATH": "/env/codex",
                "OPENAI_CODEX_MODEL_CATALOG_DISCOVERY_TIMEOUT_SECONDS": "7",
            },
        ):
            settings = loader.load(app_config)

        assert settings.model_catalog["discovery_enabled"] is False
        assert settings.model_catalog["fallback_path"] == "/env/path.json"
        assert settings.model_catalog["codex_binary_path"] == "/env/codex"
        assert settings.model_catalog["discovery_timeout_seconds"] == 7.0

    def test_load_from_yaml_config(self, loader, app_config):
        """Test loading settings from YAML backend config."""
        # Create backend config with codex extra
        backend_config = BackendConfig(
            extra={
                "codex": {
                    "renderer": {"default": "custom_renderer"},
                    "prompt": {"template": "custom_template"},
                    "streaming": {"max_retries": 5},
                }
            }
        )
        app_config = _with_openai_codex_backend(app_config, backend_config)

        settings = loader.load(app_config)

        assert settings.renderer["default"] == "custom_renderer"
        assert settings.prompt["template"] == "custom_template"
        assert settings.streaming["max_retries"] == 5

    def test_load_from_env_vars(self, loader, app_config):
        """Test loading settings from environment variables."""
        with patch.dict(
            os.environ,
            {
                "OPENAI_CODEX_RENDERER_DEFAULT": "env_renderer",
                "OPENAI_CODEX_PROMPT_TEMPLATE": "env_template",
                "OPENAI_CODEX_STREAMING_MAX_RETRIES": "10",
            },
        ):
            settings = loader.load(app_config)

            assert settings.renderer["default"] == "env_renderer"
            assert settings.prompt["template"] == "env_template"
            assert settings.streaming["max_retries"] == 10

    def test_env_overrides_yaml(self, loader, app_config):
        """Test that environment variables override YAML config."""
        backend_config = BackendConfig(
            extra={
                "codex": {
                    "renderer": {"default": "yaml_renderer"},
                }
            }
        )
        app_config = _with_openai_codex_backend(app_config, backend_config)

        with patch.dict(os.environ, {"OPENAI_CODEX_RENDERER_DEFAULT": "env_renderer"}):
            settings = loader.load(app_config)

            assert settings.renderer["default"] == "env_renderer"

    def test_load_default_capabilities_from_json_env(self, loader, app_config):
        """Test loading default capabilities from JSON environment variable."""
        json_caps = '{"tool_text_format": "json_format", "protocol": "codex"}'
        with patch.dict(os.environ, {"OPENAI_CODEX_DEFAULT_CAPABILITIES": json_caps}):
            settings = loader.load(app_config)

            assert settings.default_capabilities.tool_text_format == "json_format"
            assert settings.default_capabilities.protocol == "codex"

    def test_load_agent_overrides(self, loader, app_config):
        """Test loading agent capability overrides."""
        backend_config = BackendConfig(
            extra={
                "codex": {
                    "agent_capabilities": {
                        "kilocode": {"tool_text_format": "kilo_format"},
                        "droid": {"protocol": "openai"},
                    }
                }
            }
        )
        app_config = _with_openai_codex_backend(app_config, backend_config)

        settings = loader.load(app_config)

        assert "kilocode" in settings.agent_overrides
        assert settings.agent_overrides["kilocode"]["tool_text_format"] == "kilo_format"
        assert "droid" in settings.agent_overrides
        assert settings.agent_overrides["droid"]["protocol"] == "openai"

    def test_load_tool_schema(self, loader, app_config):
        """Test loading tool schema configuration."""
        backend_config = BackendConfig(
            extra={
                "codex": {
                    "tool_schema": {
                        "base_tools": [{"name": "base_tool", "type": "function"}],
                        "custom_tools": [{"name": "custom_tool", "type": "function"}],
                    }
                }
            }
        )
        app_config = _with_openai_codex_backend(app_config, backend_config)

        settings = loader.load(app_config)

        assert len(settings.tool_schema["base_tools"]) == 1
        assert settings.tool_schema["base_tools"][0]["name"] == "base_tool"
        assert len(settings.tool_schema["custom_tools"]) == 1
        assert settings.tool_schema["custom_tools"][0]["name"] == "custom_tool"

    def test_load_compatibility_layer_settings(self, loader, app_config):
        """Test loading compatibility layer settings."""
        backend_config = BackendConfig(
            extra={
                "codex": {
                    "compatibility_layer": {
                        "enabled": True,
                        "detection": {
                            "cache_ttl_seconds": 7200,
                            "heuristic_threshold": 3,
                        },
                        "translation": {"max_tool_execution_timeout": 60},
                    }
                }
            }
        )
        app_config = _with_openai_codex_backend(app_config, backend_config)

        settings = loader.load(app_config)

        assert settings.compatibility_layer["enabled"] is True
        assert settings.compatibility_layer["detection"]["cache_ttl_seconds"] == 7200
        assert settings.compatibility_layer["detection"]["heuristic_threshold"] == 3
        assert (
            settings.compatibility_layer["translation"]["max_tool_execution_timeout"]
            == 60
        )

    def test_invalid_json_env_ignored(self, loader, app_config):
        """Test that invalid JSON in environment variables is ignored."""
        with patch.dict(
            os.environ, {"OPENAI_CODEX_DEFAULT_CAPABILITIES": "invalid json"}
        ):
            settings = loader.load(app_config)
            # Should fall back to defaults
            assert settings.default_capabilities == CodexClientCapabilities(
                tool_schema_mode="custom_only",
                bypass_tool_call_reactor=True,
                include_environment_context=False,
            )

    def test_invalid_tool_schema_filtered(self, loader, app_config):
        """Test that invalid tool schemas are filtered out."""
        backend_config = BackendConfig(
            extra={
                "codex": {
                    "tool_schema": {
                        "custom_tools": [
                            {"name": "valid_tool", "type": "function"},
                            {"type": "function"},  # Missing name
                            {"name": ""},  # Empty name
                            "not_a_dict",  # Not a dict
                        ]
                    }
                }
            }
        )
        app_config = _with_openai_codex_backend(app_config, backend_config)

        settings = loader.load(app_config)

        # Only valid tool should be included
        assert len(settings.tool_schema["custom_tools"]) == 1
        assert settings.tool_schema["custom_tools"][0]["name"] == "valid_tool"

    def test_prompt_deduplicate_env_override(self, loader, app_config):
        """Test prompt deduplicate setting from environment variable."""
        backend_config = BackendConfig(
            extra={"codex": {"prompt": {"deduplicate": False}}}
        )
        app_config = _with_openai_codex_backend(app_config, backend_config)

        with patch.dict(os.environ, {"OPENAI_CODEX_PROMPT_DEDUPLICATE": "true"}):
            settings = loader.load(app_config)
            assert settings.prompt["deduplicate"] is True

    def test_renderer_registry_configuration(self, loader, app_config):
        """Test that renderer registry is configured correctly."""
        backend_config = BackendConfig(
            extra={
                "codex": {
                    "renderer": {
                        "default": "custom",
                        "aliases": {"alias1": "target1"},
                        "modules": {"module1": "path1"},
                    }
                }
            }
        )
        app_config = _with_openai_codex_backend(app_config, backend_config)

        with patch(
            "src.connectors.openai_codex.settings.configure_renderer_registry"
        ) as mock_configure:
            loader.load(app_config)

            mock_configure.assert_called_once()
            call_kwargs = mock_configure.call_args[1]
            assert call_kwargs["default"] == "custom"
            assert call_kwargs["aliases"] == {"alias1": "target1"}
            assert call_kwargs["modules"] == {"module1": "path1"}

    def test_capabilities_merge_with_renderer_default(self, loader, app_config):
        """Test that capabilities are merged with renderer default."""
        backend_config = BackendConfig(
            extra={"codex": {"renderer": {"default": "custom_format"}}}
        )
        app_config = _with_openai_codex_backend(app_config, backend_config)

        settings = loader.load(app_config)

        # If default_capabilities.tool_text_format is None or "none",
        # it should be set to renderer default
        if settings.default_capabilities.tool_text_format in {None, "none"}:
            # This is handled in the loader logic
            assert settings.default_capabilities.tool_text_format == "custom_format"

    def test_managed_oauth_env_overrides_yaml(self, loader, app_config):
        """Managed OAuth settings should follow ENV > YAML precedence."""
        backend_config = BackendConfig(
            extra={
                "codex": {
                    "managed_oauth": {
                        "enabled": False,
                        "storage_path": "yaml/path",
                        "accounts": ["yaml_account"],
                        "selection_strategy": "random",
                        "refresh_buffer_seconds": 111,
                        "session_affinity_ttl_seconds": 222,
                        "session_affinity_max_entries": 333,
                        "allow_legacy_fallback": False,
                    }
                }
            }
        )
        app_config = _with_openai_codex_backend(app_config, backend_config)

        with patch.dict(
            os.environ,
            {
                "OPENAI_CODEX_MANAGED_OAUTH_ENABLED": "true",
                "OPENAI_CODEX_MANAGED_OAUTH_STORAGE_PATH": "env/path",
                "OPENAI_CODEX_MANAGED_OAUTH_ACCOUNTS": '["env_account_a","env_account_b"]',
                "OPENAI_CODEX_MANAGED_OAUTH_SELECTION_STRATEGY": "session-affinity",
                "OPENAI_CODEX_MANAGED_OAUTH_REFRESH_BUFFER_SECONDS": "444",
                "OPENAI_CODEX_MANAGED_OAUTH_SESSION_AFFINITY_TTL_SECONDS": "555",
                "OPENAI_CODEX_MANAGED_OAUTH_SESSION_AFFINITY_MAX_ENTRIES": "666",
                "OPENAI_CODEX_MANAGED_OAUTH_ALLOW_LEGACY_FALLBACK": "true",
            },
            clear=False,
        ):
            settings = loader.load(app_config)

        managed = settings.managed_oauth
        assert managed["enabled"] is True
        assert managed["storage_path"] == "env/path"
        assert managed["accounts"] == ["env_account_a", "env_account_b"]
        assert managed["selection_strategy"] == "session-affinity"
        assert managed["refresh_buffer_seconds"] == 444
        assert managed["session_affinity_ttl_seconds"] == 555
        assert managed["session_affinity_max_entries"] == 666
        assert managed["allow_legacy_fallback"] is True
