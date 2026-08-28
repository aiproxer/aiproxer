from __future__ import annotations

from unittest.mock import patch

from src.core.config.app_config import AppConfig


def test_zai_api_key_applies_only_to_zai_backend() -> None:
    with patch(
        "src.core.config.env.from_env_part3.get_env_value_with_windows_persistent_fallback",
        side_effect=lambda key, **kwargs: (
            ("val-zai", "process") if key == "ZAI_API_KEY" else (None, "missing")
        ),
    ):
        config = AppConfig.from_env(environ={"ZAI_API_KEY": "val-zai"})

    zai_cfg = config.backends.lookup("zai")
    coding_plan_cfg = config.backends.lookup("zai-coding-plan")

    assert zai_cfg is not None
    assert coding_plan_cfg is not None
    assert zai_cfg.api_key == "val-zai"
    assert coding_plan_cfg.api_key is None


def test_zai_coding_plan_api_key_applies_only_to_coding_plan_backend() -> None:
    with patch(
        "src.core.config.env.from_env_part3.get_env_value_with_windows_persistent_fallback",
        side_effect=lambda key, **kwargs: (
            ("val-coding-plan", "process")
            if key == "ZAI_CODING_PLAN_API_KEY"
            else (None, "missing")
        ),
    ):
        config = AppConfig.from_env(
            environ={"ZAI_CODING_PLAN_API_KEY": "val-coding-plan"}
        )

    zai_cfg = config.backends.lookup("zai")
    coding_plan_cfg = config.backends.lookup("zai-coding-plan")

    assert zai_cfg is not None
    assert coding_plan_cfg is not None
    assert zai_cfg.api_key is None
    assert coding_plan_cfg.api_key == "val-coding-plan"


def test_zai_and_coding_plan_use_separate_keys() -> None:
    with patch(
        "src.core.config.env.from_env_part3.get_env_value_with_windows_persistent_fallback",
        side_effect=lambda key, **kwargs: (
            ("val-zai", "process")
            if key == "ZAI_API_KEY"
            else (
                ("val-coding-plan", "process")
                if key == "ZAI_CODING_PLAN_API_KEY"
                else (None, "missing")
            )
        ),
    ):
        config = AppConfig.from_env(
            environ={
                "ZAI_API_KEY": "val-zai",
                "ZAI_CODING_PLAN_API_KEY": "val-coding-plan",
            }
        )

    zai_cfg = config.backends.lookup("zai")
    coding_plan_cfg = config.backends.lookup("zai-coding-plan")

    assert zai_cfg is not None
    assert coding_plan_cfg is not None
    assert zai_cfg.api_key == "val-zai"
    assert coding_plan_cfg.api_key == "val-coding-plan"


def test_zai_env_config_prefers_windows_persistent_value_when_process_is_stale() -> (
    None
):
    with patch(
        "src.core.config.env.from_env_part3.get_env_value_with_windows_persistent_fallback",
        side_effect=lambda key, **kwargs: (
            ("fresh-zai-key", "windows-user")
            if key == "ZAI_API_KEY"
            else (None, "missing")
        ),
    ):
        config = AppConfig.from_env(environ={"ZAI_API_KEY": "stale-zai-key"})

    zai_cfg = config.backends.lookup("zai")

    assert zai_cfg is not None
    assert zai_cfg.api_key == "fresh-zai-key"


def test_zai_coding_plan_env_config_prefers_windows_persistent_value() -> None:
    with patch(
        "src.core.config.env.from_env_part3.get_env_value_with_windows_persistent_fallback",
        side_effect=lambda key, **kwargs: (
            ("fresh-coding-plan-key", "windows-user")
            if key == "ZAI_CODING_PLAN_API_KEY"
            else (None, "missing")
        ),
    ):
        config = AppConfig.from_env(
            environ={"ZAI_CODING_PLAN_API_KEY": "stale-coding-plan-key"}
        )

    coding_plan_cfg = config.backends.lookup("zai-coding-plan")

    assert coding_plan_cfg is not None
    assert coding_plan_cfg.api_key == "fresh-coding-plan-key"


def test_zai_env_config_preserves_base_url_and_timeout_overrides() -> None:
    with patch(
        "src.core.config.env.from_env_part3.get_env_value_with_windows_persistent_fallback",
        side_effect=lambda key, **kwargs: (
            ("val-zai", "process") if key == "ZAI_API_KEY" else (None, "missing")
        ),
    ):
        config = AppConfig.from_env(
            environ={
                "ZAI_API_KEY": "val-zai",
                "ZAI_API_BASE_URL": "https://example.invalid/zai/v4",
                "ZAI_TIMEOUT": "77",
            }
        )

    zai_cfg = config.backends.lookup("zai")

    assert zai_cfg is not None
    assert zai_cfg.api_url == "https://example.invalid/zai/v4"
    assert zai_cfg.timeout == 77


def test_zai_env_config_does_not_populate_keys_when_missing() -> None:
    with patch(
        "src.core.config.env.from_env_part3.get_env_value_with_windows_persistent_fallback",
        return_value=(None, "missing"),
    ):
        config = AppConfig.from_env(environ={})

    zai_cfg = config.backends.lookup("zai")
    coding_plan_cfg = config.backends.lookup("zai-coding-plan")

    assert zai_cfg is not None
    assert coding_plan_cfg is not None
    assert zai_cfg.api_key is None
    assert coding_plan_cfg.api_key is None
