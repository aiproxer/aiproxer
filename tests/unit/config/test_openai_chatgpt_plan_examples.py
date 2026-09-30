"""Example/schema files for the openai-chatgpt-plan backend (task 1.3)."""

from __future__ import annotations

from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parents[3]
EXAMPLE_BACKEND = (
    REPO_ROOT / "config" / "backends" / "openai_chatgpt_plan" / "backend.example.yaml"
)
SCHEMA_BACKEND = (
    REPO_ROOT / "config" / "schemas" / "openai_chatgpt_plan_backend.schema.yaml"
)
CONFIG_EXAMPLE = REPO_ROOT / "config" / "config.example.yaml"
SAMPLE_ENV = REPO_ROOT / "config" / "sample.env"
APP_SCHEMA = REPO_ROOT / "config" / "schemas" / "app_config.schema.yaml"


def test_chatgpt_plan_backend_example_exists_and_uses_new_backend_type() -> None:
    assert EXAMPLE_BACKEND.is_file()
    text = EXAMPLE_BACKEND.read_text(encoding="utf-8")
    assert "openai-chatgpt-plan" in text
    assert 'backend_type: "openai-codex"' not in text
    assert "compatibility_layer" not in text
    assert "chatgpt_plan" in text
    assert "profile_id" in text
    parsed = yaml.safe_load(text)
    assert parsed["backend_type"] == "openai-chatgpt-plan"
    from src.core.config.yaml_validation import validate_yaml_against_schema

    validate_yaml_against_schema(EXAMPLE_BACKEND, SCHEMA_BACKEND)


def test_chatgpt_plan_schema_exists_and_uses_new_backend_type() -> None:
    assert SCHEMA_BACKEND.is_file()
    text = SCHEMA_BACKEND.read_text(encoding="utf-8")
    assert "openai-chatgpt-plan" in text
    assert 'const: "openai-codex"' not in text
    assert "compatibility_layer" not in text
    schema = yaml.safe_load(text)
    assert schema["properties"]["backend_type"]["const"] == "openai-chatgpt-plan"


def test_config_example_describes_chatgpt_plan_distinct_from_api_key_openai() -> None:
    text = CONFIG_EXAMPLE.read_text(encoding="utf-8")
    assert "openai_chatgpt_plan:" in text
    assert "openai-chatgpt-plan" in text
    assert "OPENAI_CHATGPT_PLAN_" in text
    assert "API-key" in text or "API key" in text
    assert "openai-responses" in text
    data = yaml.safe_load(text)
    openai_backend = data["backends"]["openai"]
    plan_backend = data["backends"]["openai_chatgpt_plan"]
    assert "chatgpt_plan" not in openai_backend.get("extra", {})
    assert "chatgpt_plan" in plan_backend["extra"]
    plan = plan_backend["extra"]["chatgpt_plan"]
    assert plan["oauth"]["callback_port"] == 1455
    assert plan["model_catalog"]["ttl_seconds"] == 300
    assert plan["profiles_path"] == "var/openai_chatgpt_plan/profiles"
    assert plan["host_state_path"] == "var/openai_chatgpt_plan/host.json"


def test_sample_env_documents_openai_chatgpt_plan_namespace_only() -> None:
    text = SAMPLE_ENV.read_text(encoding="utf-8")
    assert "OPENAI_CHATGPT_PLAN_PROFILE_ID" in text
    assert "OPENAI_CHATGPT_PLAN_PROFILES_PATH" in text
    assert "OPENAI_CHATGPT_PLAN_HOST_STATE_PATH" in text
    assert "OPENAI_CHATGPT_PLAN_OAUTH_CALLBACK_PORT" in text
    assert "OPENAI_CHATGPT_PLAN_MODEL_CATALOG_TTL_SECONDS" in text
    for line in text.splitlines():
        stripped = line.lstrip("# ").strip()
        if stripped.startswith("OPENAI_CHATGPT_PLAN_"):
            assert "OPENAI_CODEX_" not in stripped


def test_app_config_schema_includes_chatgpt_plan_settings() -> None:
    text = APP_SCHEMA.read_text(encoding="utf-8")
    assert "openai_chatgpt_plan:" in text
    assert "chatgpt_plan:" in text
    schema = yaml.safe_load(text)
    backends = schema["properties"]["backends"]["properties"]
    assert "openai_chatgpt_plan" in backends
    extra_props = backends["openai_chatgpt_plan"]["properties"]["extra"]["properties"]
    assert "chatgpt_plan" in extra_props
