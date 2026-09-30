"""AppConfig ENV/YAML wiring for openai-chatgpt-plan (task 1.3).

Proves OPENAI_CHATGPT_PLAN_* overlays extra.chatgpt_plan via the core loader
(CLI > ENV > YAML > defaults at the AppConfig layer is ENV > YAML > defaults
here; connector-specific argparse flags are not added in this task).
"""

from __future__ import annotations

from pathlib import Path

import yaml
from src.core.config.app_config import AppConfig, load_config
from src.core.config.sources.backend_instances import BackendInstanceEnvSource

DEFAULT_PROFILES_PATH = "var/openai_chatgpt_plan/profiles"
DEFAULT_CALLBACK_PORT = 1455
DEFAULT_CATALOG_TTL_SECONDS = 300


def _typed_from_app(app: AppConfig, *, environ: dict[str, str] | None = None):
    from src.connectors.openai_chatgpt_plan.config import (
        chatgpt_plan_config_from_app_config,
    )

    return chatgpt_plan_config_from_app_config(app, environ=environ or {})


def _write_chatgpt_plan_yaml(
    path: Path, *, profile_id: str, callback_port: int
) -> Path:
    payload = {
        "backends": {
            "openai_chatgpt_plan": {
                "timeout": 120,
                "extra": {
                    "chatgpt_plan": {
                        "profile_id": profile_id,
                        "profiles_path": "var/yaml/chatgpt_plan/profiles",
                        "host_state_path": "var/yaml/chatgpt_plan/host.json",
                        "oauth": {"callback_port": callback_port},
                        "model_catalog": {"ttl_seconds": 90},
                    }
                },
            }
        }
    }
    path.write_text(yaml.safe_dump(payload, sort_keys=False), encoding="utf-8")
    return path


class TestLoadConfigYamlExtra:
    def test_yaml_extra_chatgpt_plan_loads_into_typed_config(
        self, tmp_path: Path
    ) -> None:
        config_path = _write_chatgpt_plan_yaml(
            tmp_path / "config.yaml",
            profile_id="yaml-file-profile",
            callback_port=2468,
        )
        app = load_config(config_path, environ={})
        backend = app.backends.lookup("openai_chatgpt_plan")
        assert backend is not None
        assert backend.extra["chatgpt_plan"]["profile_id"] == "yaml-file-profile"

        cfg = _typed_from_app(app)
        assert cfg.profile_id == "yaml-file-profile"
        assert cfg.profiles_path == "var/yaml/chatgpt_plan/profiles"
        assert cfg.host_state_path == "var/yaml/chatgpt_plan/host.json"
        assert cfg.oauth.callback_port == 2468
        assert cfg.model_catalog.ttl_seconds == 90

    def test_env_overrides_yaml_via_load_config(self, tmp_path: Path) -> None:
        config_path = _write_chatgpt_plan_yaml(
            tmp_path / "config.yaml",
            profile_id="yaml-file-profile",
            callback_port=2468,
        )
        app = load_config(
            config_path,
            environ={
                "OPENAI_CHATGPT_PLAN_PROFILE_ID": "env-file-profile",
                "OPENAI_CHATGPT_PLAN_OAUTH_CALLBACK_PORT": "1550",
                "OPENAI_CHATGPT_PLAN_MODEL_CATALOG_TTL_SECONDS": "12",
            },
        )
        cfg = _typed_from_app(app)
        assert cfg.profile_id == "env-file-profile"
        assert cfg.oauth.callback_port == 1550
        assert cfg.model_catalog.ttl_seconds == 12
        assert cfg.profiles_path == "var/yaml/chatgpt_plan/profiles"


class TestFromEnvChatgptPlan:
    def test_from_env_applies_openai_chatgpt_plan_namespace(self) -> None:
        app = AppConfig.from_env(
            environ={
                "OPENAI_CHATGPT_PLAN_PROFILE_ID": "env-only",
                "OPENAI_CHATGPT_PLAN_PROFILES_PATH": "var/env_only/profiles",
                "OPENAI_CHATGPT_PLAN_HOST_STATE_PATH": "var/env_only/host.json",
                "OPENAI_CHATGPT_PLAN_OAUTH_CALLBACK_PORT": "1700",
                "OPENAI_CHATGPT_PLAN_MODEL_CATALOG_TTL_SECONDS": "42",
            }
        )
        backend = app.backends.lookup("openai_chatgpt_plan")
        assert backend is not None
        plan = backend.extra.get("chatgpt_plan")
        assert isinstance(plan, dict)
        assert plan["profile_id"] == "env-only"
        assert plan["profiles_path"] == "var/env_only/profiles"
        assert plan["host_state_path"] == "var/env_only/host.json"
        assert plan["oauth"]["callback_port"] == 1700
        assert plan["model_catalog"]["ttl_seconds"] == 42

        cfg = _typed_from_app(app)
        assert cfg.profile_id == "env-only"
        assert cfg.oauth.callback_port == 1700
        assert cfg.model_catalog.ttl_seconds == 42

    def test_openai_codex_env_does_not_populate_chatgpt_plan_backend(self) -> None:
        app = AppConfig.from_env(
            environ={
                "OPENAI_CODEX_AUTH_PATH": "var/openai_codex/auth.json",
                "OPENAI_CODEX_API_KEY": "codex-test-key",
                "OPENAI_CODEX_PROFILE_ID": "codex-profile",
                "OPENAI_CODEX_OAUTH_CALLBACK_PORT": "9999",
            }
        )
        chatgpt = app.backends.lookup("openai_chatgpt_plan")
        extra = chatgpt.extra if chatgpt is not None else {}
        assert extra.get("chatgpt_plan") in (None, {})

        cfg = _typed_from_app(app)
        assert cfg.profile_id is None
        assert cfg.profiles_path == DEFAULT_PROFILES_PATH
        assert cfg.oauth.callback_port == DEFAULT_CALLBACK_PORT
        assert cfg.model_catalog.ttl_seconds == DEFAULT_CATALOG_TTL_SECONDS

        codex = app.backends.lookup("openai-codex")
        assert codex is not None
        assert codex.credentials_path == "var/openai_codex/auth.json"
        assert codex.api_key == "codex-test-key"


class TestBackendInstanceDiscovery:
    def test_openai_codex_numbered_keys_do_not_create_chatgpt_plan_instances(
        self,
    ) -> None:
        discovered = BackendInstanceEnvSource().load(
            {"OPENAI_CODEX_API_KEY_1": "codex-test-key-1"},
            existing_instance_names=set(),
            resolution=None,
        )
        backends = discovered.get("backends", {})
        assert "openai-codex.1" in backends
        assert all("chatgpt" not in name for name in backends)

    def test_no_chatgpt_plan_api_key_instance_alias(self) -> None:
        discovered = BackendInstanceEnvSource().load(
            {
                "OPENAI_CHATGPT_PLAN_API_KEY_1": "chatgpt-plan-key-should-not-create",
                "OPENAI_CHATGPT_PLAN_API_KEY": "chatgpt-plan-key-should-not-create-either",
            },
            existing_instance_names=set(),
            resolution=None,
        )
        backends = discovered.get("backends", {})
        assert "openai-chatgpt-plan.1" not in backends
        assert "openai_chatgpt_plan.1" not in backends
