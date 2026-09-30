"""Typed ChatGPT-plan connector configuration (task 1.3).

Covers ChatGPTPlanConfig defaults, extra.chatgpt_plan YAML parsing,
OPENAI_CHATGPT_PLAN_* env overlays, and rejection of OPENAI_CODEX_* aliases.
"""

from __future__ import annotations

from pathlib import Path

from src.core.config.app_config import AppConfig, BackendConfig

REPO_ROOT = Path(__file__).resolve().parents[3]
PACKAGE_CONFIG = REPO_ROOT / "src" / "connectors" / "openai_chatgpt_plan" / "config.py"

DEFAULT_PROFILES_PATH = "var/openai_chatgpt_plan/profiles"
DEFAULT_HOST_STATE_PATH = "var/openai_chatgpt_plan/host.json"
DEFAULT_CALLBACK_PORT = 1455
DEFAULT_CATALOG_TTL_SECONDS = 300

YAML_CHATGPT_PLAN = {
    "profile_id": "yaml-profile",
    "profiles_path": "var/custom/chatgpt_plan/profiles",
    "host_state_path": "var/custom/chatgpt_plan/host.json",
    "oauth": {"callback_port": 2468},
    "model_catalog": {"ttl_seconds": 60},
}


def _load_from_extra(
    extra: dict[str, object] | None = None,
    *,
    environ: dict[str, str] | None = None,
):
    from src.connectors.openai_chatgpt_plan.config import load_chatgpt_plan_config

    return load_chatgpt_plan_config(extra, environ=environ or {})


class TestChatGPTPlanConfigDefaults:
    def test_typed_config_exists_with_expected_fields(self) -> None:
        from src.connectors.openai_chatgpt_plan.config import ChatGPTPlanConfig

        cfg = ChatGPTPlanConfig()
        assert hasattr(cfg, "profile_id")
        assert hasattr(cfg, "profiles_path")
        assert hasattr(cfg, "host_state_path")
        assert hasattr(cfg, "oauth")
        assert hasattr(cfg.oauth, "callback_port")
        assert hasattr(cfg, "model_catalog")
        assert hasattr(cfg.model_catalog, "ttl_seconds")

    def test_defaults_match_design(self) -> None:
        from src.connectors.openai_chatgpt_plan.config import ChatGPTPlanConfig

        cfg = ChatGPTPlanConfig()
        assert cfg.profile_id is None
        assert cfg.profiles_path == DEFAULT_PROFILES_PATH
        assert cfg.host_state_path == DEFAULT_HOST_STATE_PATH
        assert cfg.oauth.callback_port == DEFAULT_CALLBACK_PORT
        assert cfg.model_catalog.ttl_seconds == DEFAULT_CATALOG_TTL_SECONDS

    def test_load_without_extra_uses_defaults(self) -> None:
        cfg = _load_from_extra({}, environ={})
        assert cfg.profile_id is None
        assert cfg.profiles_path == DEFAULT_PROFILES_PATH
        assert cfg.host_state_path == DEFAULT_HOST_STATE_PATH
        assert cfg.oauth.callback_port == DEFAULT_CALLBACK_PORT
        assert cfg.model_catalog.ttl_seconds == DEFAULT_CATALOG_TTL_SECONDS


class TestYamlExtraChatgptPlan:
    def test_parses_nested_extra_chatgpt_plan(self) -> None:
        cfg = _load_from_extra({"chatgpt_plan": YAML_CHATGPT_PLAN}, environ={})
        assert cfg.profile_id == "yaml-profile"
        assert cfg.profiles_path == "var/custom/chatgpt_plan/profiles"
        assert cfg.host_state_path == "var/custom/chatgpt_plan/host.json"
        assert cfg.oauth.callback_port == 2468
        assert cfg.model_catalog.ttl_seconds == 60

    def test_partial_yaml_keeps_remaining_defaults(self) -> None:
        cfg = _load_from_extra(
            {"chatgpt_plan": {"profile_id": "only-profile"}},
            environ={},
        )
        assert cfg.profile_id == "only-profile"
        assert cfg.profiles_path == DEFAULT_PROFILES_PATH
        assert cfg.oauth.callback_port == DEFAULT_CALLBACK_PORT
        assert cfg.model_catalog.ttl_seconds == DEFAULT_CATALOG_TTL_SECONDS

    def test_from_app_config_reads_openai_chatgpt_plan_extra(self) -> None:
        from src.connectors.openai_chatgpt_plan.config import (
            chatgpt_plan_config_from_app_config,
        )

        app = AppConfig()
        app.mutate_backends(
            {
                "openai_chatgpt_plan": BackendConfig(
                    extra={"chatgpt_plan": YAML_CHATGPT_PLAN}
                )
            }
        )
        cfg = chatgpt_plan_config_from_app_config(app, environ={})
        assert cfg.profile_id == "yaml-profile"
        assert cfg.oauth.callback_port == 2468

    def test_from_app_config_accepts_hyphenated_backend_key(self) -> None:
        from src.connectors.openai_chatgpt_plan.config import (
            chatgpt_plan_config_from_app_config,
        )

        app = AppConfig()
        app.mutate_backends(
            {
                "openai-chatgpt-plan": BackendConfig(
                    extra={"chatgpt_plan": {"profile_id": "hyphen-key"}}
                )
            }
        )
        cfg = chatgpt_plan_config_from_app_config(app, environ={})
        assert cfg.profile_id == "hyphen-key"

    def test_ignores_extra_on_unrelated_backends(self) -> None:
        from src.connectors.openai_chatgpt_plan.config import (
            chatgpt_plan_config_from_app_config,
        )

        app = AppConfig()
        app.mutate_backends(
            {
                "openai_codex": BackendConfig(
                    extra={"chatgpt_plan": {"profile_id": "should-not-apply"}}
                ),
                "openai": BackendConfig(
                    extra={"chatgpt_plan": {"profile_id": "also-not-apply"}}
                ),
            }
        )
        cfg = chatgpt_plan_config_from_app_config(app, environ={})
        assert cfg.profile_id is None


class TestEnvOverridesYaml:
    def test_openai_chatgpt_plan_env_overrides_yaml(self) -> None:
        cfg = _load_from_extra(
            {"chatgpt_plan": YAML_CHATGPT_PLAN},
            environ={
                "OPENAI_CHATGPT_PLAN_PROFILE_ID": "env-profile",
                "OPENAI_CHATGPT_PLAN_PROFILES_PATH": "var/env/profiles",
                "OPENAI_CHATGPT_PLAN_HOST_STATE_PATH": "var/env/host.json",
                "OPENAI_CHATGPT_PLAN_OAUTH_CALLBACK_PORT": "1999",
                "OPENAI_CHATGPT_PLAN_MODEL_CATALOG_TTL_SECONDS": "15",
            },
        )
        assert cfg.profile_id == "env-profile"
        assert cfg.profiles_path == "var/env/profiles"
        assert cfg.host_state_path == "var/env/host.json"
        assert cfg.oauth.callback_port == 1999
        assert cfg.model_catalog.ttl_seconds == 15

    def test_partial_env_keeps_yaml_for_unset_keys(self) -> None:
        cfg = _load_from_extra(
            {"chatgpt_plan": YAML_CHATGPT_PLAN},
            environ={"OPENAI_CHATGPT_PLAN_PROFILE_ID": "env-only-profile"},
        )
        assert cfg.profile_id == "env-only-profile"
        assert cfg.profiles_path == "var/custom/chatgpt_plan/profiles"
        assert cfg.oauth.callback_port == 2468
        assert cfg.model_catalog.ttl_seconds == 60

    def test_env_overrides_defaults_without_yaml(self) -> None:
        cfg = _load_from_extra(
            {},
            environ={
                "OPENAI_CHATGPT_PLAN_PROFILE_ID": "env-default",
                "OPENAI_CHATGPT_PLAN_OAUTH_CALLBACK_PORT": "1800",
            },
        )
        assert cfg.profile_id == "env-default"
        assert cfg.oauth.callback_port == 1800
        assert cfg.profiles_path == DEFAULT_PROFILES_PATH
        assert cfg.model_catalog.ttl_seconds == DEFAULT_CATALOG_TTL_SECONDS


class TestOpenAICodexAliasesRejected:
    def test_openai_codex_env_does_not_populate_chatgpt_plan_config(self) -> None:
        cfg = _load_from_extra(
            {"chatgpt_plan": {"profile_id": "yaml-profile"}},
            environ={
                "OPENAI_CODEX_AUTH_PATH": "var/openai_codex/auth.json",
                "OPENAI_CODEX_PATH": "var/openai_codex",
                "OPENAI_CODEX_API_KEY": "codex-key-should-not-apply",
                "OPENAI_CODEX_MODEL_CATALOG_TTL_SECONDS": "9",
                "OPENAI_CODEX_OAUTH_CALLBACK_PORT": "9999",
                "OPENAI_CODEX_PROFILE_ID": "codex-profile",
                "OPENAI_CODEX_PROFILES_PATH": "var/openai_codex_oauth_accounts",
                "OPENAI_CODEX_HOST_STATE_PATH": "var/codex/host.json",
            },
        )
        assert cfg.profile_id == "yaml-profile"
        assert cfg.profiles_path == DEFAULT_PROFILES_PATH
        assert cfg.host_state_path == DEFAULT_HOST_STATE_PATH
        assert cfg.oauth.callback_port == DEFAULT_CALLBACK_PORT
        assert cfg.model_catalog.ttl_seconds == DEFAULT_CATALOG_TTL_SECONDS

    def test_extra_codex_does_not_populate_chatgpt_plan_config(self) -> None:
        cfg = _load_from_extra(
            {
                "codex": {
                    "profile_id": "codex-profile",
                    "managed_oauth": {
                        "storage_path": "var/openai_codex_oauth_accounts"
                    },
                    "model_catalog": {"ttl_seconds": 9},
                }
            },
            environ={},
        )
        assert cfg.profile_id is None
        assert cfg.profiles_path == DEFAULT_PROFILES_PATH
        assert cfg.oauth.callback_port == DEFAULT_CALLBACK_PORT

    def test_config_module_source_has_no_openai_codex_aliases(self) -> None:
        source = PACKAGE_CONFIG.read_text(encoding="utf-8")
        assert "OPENAI_CODEX_" not in source
        assert "OPENAI_CHATGPT_PLAN_" in source
