from __future__ import annotations

import json
import logging
from collections.abc import Mapping
from dataclasses import asdict
from typing import Any

from src.core.common.env_utils import get_env_value_with_windows_persistent_fallback
from src.core.config.env.util import (
    env_to_bool as _env_to_bool,
)
from src.core.config.env.util import (
    env_to_float as _env_to_float,
)
from src.core.config.env.util import (
    env_to_int as _env_to_int,
)
from src.core.config.env.util import (
    get_env_value as _get_env_value,
)
from src.core.config.env.util import (
    to_int as _to_int,
)
from src.core.config.parameter_resolution import ParameterResolution, ParameterSource
from src.core.domain.configuration.replacement_rule import ReplacementRule

logger = logging.getLogger(__name__)


def _has_numbered_env_variants(env: Mapping[str, str], base_name: str) -> bool:
    for i in range(1, 21):
        raw = env.get(f"{base_name}_{i}")
        if isinstance(raw, str) and raw.strip():
            return True
    return False


def _load_replacement_rules_from_env(
    env: Mapping[str, str],
    resolution: ParameterResolution | None,
) -> list[ReplacementRule]:
    """Load replacement rules from REPLACEMENT_RULES environment variable.

    The environment variable should contain a JSON array of rule objects:
    [{"from_pattern": "*", "to_backend": "qwen-oauth", "to_model": "qwen3-coder-plus"}]

    Returns:
        List of ReplacementRule objects, or empty list if not set
    """
    replacement_rules_env = env.get("REPLACEMENT_RULES")
    if not replacement_rules_env:
        return []

    try:
        rules_data = json.loads(replacement_rules_env)
        if not isinstance(rules_data, list):
            logger.warning(
                "REPLACEMENT_RULES environment variable must be a JSON array. "
                f"Got: {type(rules_data).__name__}"
            )
            return []

        rules = []
        for i, rule_data in enumerate(rules_data):
            if not isinstance(rule_data, dict):
                logger.warning(
                    f"Replacement rule at index {i} must be an object. "
                    f"Skipping invalid rule."
                )
                continue

            try:
                rule = ReplacementRule(
                    from_pattern=rule_data.get("from_pattern", ""),
                    to_backend=rule_data.get("to_backend", ""),
                    to_model=rule_data.get("to_model", ""),
                )
                rules.append(rule)
            except (TypeError, ValueError) as e:
                logger.warning(
                    f"Invalid replacement rule at index {i}: {e}. Skipping rule."
                )
                continue

        # Record the whole list at once to avoid set_by_path creating a dict
        if resolution and rules:
            resolution.record(
                "replacement.replacement_rules",
                rules,
                ParameterSource.ENVIRONMENT,
                origin="REPLACEMENT_RULES",
            )

        return rules
    except json.JSONDecodeError as e:
        logger.warning(
            f"Failed to parse REPLACEMENT_RULES environment variable as JSON: {e}. "
            f"Ignoring replacement rules from environment."
        )
        return []


def _apply_gemini_backend(
    config_backends: dict[str, Any],
    env: Mapping[str, str],
    gemini_key: str,
    resolution: ParameterResolution | None,
) -> None:
    if logger.isEnabledFor(logging.INFO):
        _, gemini_key_source = get_env_value_with_windows_persistent_fallback(
            "GEMINI_API_KEY", environ=env
        )
        logger.info(
            "Gemini key diagnostics [from_env_part3]: env_type=%s source=%s",
            type(env).__name__,
            gemini_key_source,
        )

    config_backends["gemini"] = config_backends.get("gemini", {})
    config_backends["gemini"]["api_key"] = gemini_key
    config_backends["gemini"]["api_url"] = _get_env_value(
        env,
        "GEMINI_API_BASE_URL",
        "https://generativelanguage.googleapis.com",
        path="backends.gemini.api_url",
        resolution=resolution,
    )
    gemini_timeout = _get_env_value(
        env,
        "GEMINI_TIMEOUT",
        None,
        path="backends.gemini.timeout",
        resolution=resolution,
        transform=lambda value: _to_int(value, 0),
    )
    if gemini_timeout:
        config_backends["gemini"]["timeout"] = gemini_timeout
    if resolution is not None:
        resolution.record(
            "backends.gemini.api_key",
            config_backends["gemini"]["api_key"],
            ParameterSource.ENVIRONMENT,
            origin="GEMINI_API_KEY",
        )


def apply_config_part3(
    config: dict[str, Any],
    env: Mapping[str, str],
    resolution: ParameterResolution | None,
) -> None:
    config_backends: dict[str, Any] = config["backends"]
    assert isinstance(config_backends, dict)

    if env.get("OPENROUTER_API_KEY"):
        config_backends["openrouter"] = config_backends.get("openrouter", {})
        config_backends["openrouter"]["api_key"] = env["OPENROUTER_API_KEY"]
        config_backends["openrouter"]["api_url"] = _get_env_value(
            env,
            "OPENROUTER_API_BASE_URL",
            "https://openrouter.ai/api/v1",
            path="backends.openrouter.api_url",
            resolution=resolution,
        )
        timeout_value = _get_env_value(
            env,
            "OPENROUTER_TIMEOUT",
            None,
            path="backends.openrouter.timeout",
            resolution=resolution,
            transform=lambda value: _to_int(value, 0),
        )
        if timeout_value:
            config_backends["openrouter"]["timeout"] = timeout_value
        if resolution is not None:
            resolution.record(
                "backends.openrouter.api_key",
                config_backends["openrouter"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="OPENROUTER_API_KEY",
            )

    gemini_key, _gemini_key_source = get_env_value_with_windows_persistent_fallback(
        "GEMINI_API_KEY", environ=env
    )
    if gemini_key and not _has_numbered_env_variants(env, "GEMINI_API_KEY"):
        _apply_gemini_backend(config_backends, env, gemini_key, resolution)

    if env.get("ANTHROPIC_API_KEY"):
        config_backends["anthropic"] = config_backends.get("anthropic", {})
        config_backends["anthropic"]["api_key"] = env["ANTHROPIC_API_KEY"]
        config_backends["anthropic"]["api_url"] = _get_env_value(
            env,
            "ANTHROPIC_API_BASE_URL",
            "https://api.anthropic.com/v1",
            path="backends.anthropic.api_url",
            resolution=resolution,
        )
        anthropic_timeout = _get_env_value(
            env,
            "ANTHROPIC_TIMEOUT",
            None,
            path="backends.anthropic.timeout",
            resolution=resolution,
            transform=lambda value: _to_int(value, 0),
        )
        if anthropic_timeout:
            config_backends["anthropic"]["timeout"] = anthropic_timeout
        if resolution is not None:
            resolution.record(
                "backends.anthropic.api_key",
                config_backends["anthropic"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="ANTHROPIC_API_KEY",
            )

    zai_key, zai_key_source = get_env_value_with_windows_persistent_fallback(
        "ZAI_API_KEY", environ=env
    )
    if zai_key:

        # Diagnostics: log source metadata only (never log key material).
        if logger.isEnabledFor(logging.INFO):
            logger.info(
                "ZAI key diagnostics [from_env_part3]: env_type=%s source=%s",
                type(env).__name__,
                zai_key_source,
            )

        config_backends["zai"] = config_backends.get("zai", {})
        config_backends["zai"]["api_key"] = zai_key
        config_backends["zai"]["api_url"] = _get_env_value(
            env,
            "ZAI_API_BASE_URL",
            None,
            path="backends.zai.api_url",
            resolution=resolution,
        )
        zai_timeout = _get_env_value(
            env,
            "ZAI_TIMEOUT",
            None,
            path="backends.zai.timeout",
            resolution=resolution,
            transform=lambda value: _to_int(value, 0),
        )
        if zai_timeout:
            config_backends["zai"]["timeout"] = zai_timeout
        if resolution is not None:
            resolution.record(
                "backends.zai.api_key",
                config_backends["zai"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="ZAI_API_KEY",
            )

    # zai-coding-plan uses its own dedicated API key
    coding_plan_key, coding_plan_key_source = (
        get_env_value_with_windows_persistent_fallback(
            "ZAI_CODING_PLAN_API_KEY", environ=env
        )
    )
    if coding_plan_key:
        config_backends["zai-coding-plan"] = config_backends.get("zai-coding-plan", {})
        config_backends["zai-coding-plan"]["api_key"] = coding_plan_key
        if logger.isEnabledFor(logging.INFO):
            logger.info(
                "ZAI Coding Plan key diagnostics [from_env_part3]: env_type=%s source=%s",
                type(env).__name__,
                coding_plan_key_source,
            )
        if resolution is not None:
            resolution.record(
                "backends.zai-coding-plan.api_key",
                config_backends["zai-coding-plan"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="ZAI_CODING_PLAN_API_KEY",
            )

    if env.get("ZENMUX_API_KEY"):
        config_backends["zenmux"] = config_backends.get("zenmux", {})
        config_backends["zenmux"]["api_key"] = env["ZENMUX_API_KEY"]
        config_backends["zenmux"]["api_url"] = _get_env_value(
            env,
            "ZENMUX_API_BASE_URL",
            "https://zenmux.ai/api/v1",
            path="backends.zenmux.api_url",
            resolution=resolution,
        )
        zenmux_timeout = _get_env_value(
            env,
            "ZENMUX_TIMEOUT",
            None,
            path="backends.zenmux.timeout",
            resolution=resolution,
            transform=lambda value: _to_int(value, 0),
        )
        if zenmux_timeout:
            config_backends["zenmux"]["timeout"] = zenmux_timeout
        if resolution is not None:
            resolution.record(
                "backends.zenmux.api_key",
                config_backends["zenmux"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="ZENMUX_API_KEY",
            )

    if env.get("OPENAI_API_KEY"):
        config_backends["openai"] = config_backends.get("openai", {})
        config_backends["openai"]["api_key"] = env["OPENAI_API_KEY"]
        config_backends["openai"]["api_url"] = _get_env_value(
            env,
            "OPENAI_API_BASE_URL",
            "https://api.openai.com/v1",
            path="backends.openai.api_url",
            resolution=resolution,
        )
        openai_timeout = _get_env_value(
            env,
            "OPENAI_TIMEOUT",
            None,
            path="backends.openai.timeout",
            resolution=resolution,
            transform=lambda value: _to_int(value, 0),
        )
        if openai_timeout:
            config_backends["openai"]["timeout"] = openai_timeout
        if resolution is not None:
            resolution.record(
                "backends.openai.api_key",
                config_backends["openai"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="OPENAI_API_KEY",
            )

    if env.get("KIMI_API_KEY"):
        config_backends["kimi-code"] = config_backends.get("kimi-code", {})
        config_backends["kimi-code"]["api_key"] = env["KIMI_API_KEY"]
        config_backends["kimi-code"]["api_url"] = _get_env_value(
            env,
            "KIMI_API_BASE_URL",
            "https://api.kimi.com/coding/v1",
            path="backends.kimi-code.api_url",
            resolution=resolution,
        )
        kimi_timeout = _get_env_value(
            env,
            "KIMI_TIMEOUT",
            None,
            path="backends.kimi-code.timeout",
            resolution=resolution,
            transform=lambda value: _to_int(value, 0),
        )
        if kimi_timeout:
            config_backends["kimi-code"]["timeout"] = kimi_timeout
        if resolution is not None:
            resolution.record(
                "backends.kimi-code.api_key",
                config_backends["kimi-code"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="KIMI_API_KEY",
            )

    _apply_extended_provider_backends(config_backends, env, resolution)

    ollama_api_base_url = _get_env_value(
        env,
        "OLLAMA_API_BASE_URL",
        None,
        path="backends.ollama.api_url",
        resolution=resolution,
    )
    if ollama_api_base_url:
        config_backends["ollama"] = config_backends.get("ollama", {})
        config_backends["ollama"]["api_url"] = ollama_api_base_url
        ollama_timeout = _get_env_value(
            env,
            "OLLAMA_TIMEOUT",
            None,
            path="backends.ollama.timeout",
            resolution=resolution,
            transform=lambda value: _to_int(value, 0),
        )
        if ollama_timeout:
            config_backends["ollama"]["timeout"] = ollama_timeout
        if resolution is not None:
            resolution.record(
                "backends.ollama.api_url",
                config_backends["ollama"]["api_url"],
                ParameterSource.ENVIRONMENT,
                origin="OLLAMA_API_BASE_URL",
            )
    if env.get("OLLAMA_API_KEY"):
        config_backends["ollama"] = config_backends.get("ollama", {})
        config_backends["ollama"]["api_key"] = env["OLLAMA_API_KEY"]
        if resolution is not None:
            resolution.record(
                "backends.ollama.api_key",
                config_backends["ollama"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="OLLAMA_API_KEY",
            )

    # Collect INTERNAI_API_KEY and all numbered variants (INTERNAI_API_KEY_1, _2, etc.)
    internai_api_keys: list[str] = []
    if env.get("INTERNAI_API_KEY"):
        internai_api_keys.append(env["INTERNAI_API_KEY"])

    # Collect numbered variants
    i = 1
    while True:
        key_name = f"INTERNAI_API_KEY_{i}"
        if key_name in env:
            key_value = env[key_name]
            if key_value and key_value not in internai_api_keys:
                internai_api_keys.append(key_value)
            i += 1
        else:
            break

    if internai_api_keys:
        config_backends["internlm"] = config_backends.get("internlm", {})
        # Set primary api_key for backward compatibility
        config_backends["internlm"]["api_key"] = internai_api_keys[0]
        # Set list of all keys for rotation in extra dict
        if "extra" not in config_backends["internlm"]:
            config_backends["internlm"]["extra"] = {}
        config_backends["internlm"]["extra"]["api_keys"] = internai_api_keys
        config_backends["internlm"]["api_url"] = _get_env_value(
            env,
            "INTERNAI_API_BASE_URL",
            "https://chat.intern-ai.org.cn/api/v1",
            path="backends.internlm.api_url",
            resolution=resolution,
        )
        internai_timeout = _get_env_value(
            env,
            "INTERNAI_TIMEOUT",
            None,
            path="backends.internlm.timeout",
            resolution=resolution,
            transform=lambda value: _to_int(value, 0),
        )
        if internai_timeout:
            config_backends["internlm"]["timeout"] = internai_timeout
        if resolution is not None:
            resolution.record(
                "backends.internlm.api_key",
                config_backends["internlm"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="INTERNAI_API_KEY",
            )
            resolution.record(
                "backends.internlm.extra.api_keys",
                config_backends["internlm"]["extra"]["api_keys"],
                ParameterSource.ENVIRONMENT,
                origin="INTERNAI_API_KEY + numbered variants",
            )

    default_backend_type: str = str(config["backends"].get("default_backend", "openai"))
    if default_backend_type not in config_backends:
        config_backends[default_backend_type] = config_backends.get(
            default_backend_type, {}
        )
        if env.get("PYTEST_CURRENT_TEST") and (
            not config_backends[default_backend_type]
            or not config_backends[default_backend_type].get("api_key")
        ):
            config_backends[default_backend_type]["api_key"] = [
                f"test-key-{default_backend_type}"
            ]
            logger.info(
                "Added test API key for default backend %s", default_backend_type
            )

    # Load replacement rules and convert to dict format for config merging
    replacement_rules_objects = _load_replacement_rules_from_env(
        env, resolution=resolution
    )
    replacement_rules_dicts = [asdict(rule) for rule in replacement_rules_objects]

    config["replacement"] = {
        "enabled": _env_to_bool(
            "REPLACEMENT_ENABLED",
            False,
            env,
            path="replacement.enabled",
            resolution=resolution,
        ),
        "probability": _env_to_float(
            "REPLACEMENT_PROBABILITY",
            0.0,
            env,
            path="replacement.probability",
            resolution=resolution,
        ),
        # Handle new replacement_rules format (JSON array)
        "replacement_rules": replacement_rules_dicts,
        # Legacy backend_model for backward compatibility
        "backend_model": _get_env_value(
            env,
            "REPLACEMENT_BACKEND_MODEL",
            "",
            path="replacement.backend_model",
            resolution=resolution,
        ),
        "turn_count": _env_to_int(
            "REPLACEMENT_TURN_COUNT",
            1,
            env,
            path="replacement.turn_count",
            resolution=resolution,
        ),
        "allow_oauth_auto_replacement": _env_to_bool(
            "ALLOW_OAUTH_AUTO_REPLACEMENT",
            False,
            env,
            path="replacement.allow_oauth_auto_replacement",
            resolution=resolution,
        ),
    }

    # Notification settings
    if "LLM_PROXY_ENABLE_NOTIFICATIONS" in env:
        notifications_value = env["LLM_PROXY_ENABLE_NOTIFICATIONS"].strip().lower()
        config["notifications"] = {
            "enabled": notifications_value in {"1", "true", "yes", "on"}
        }
        if resolution is not None:
            resolution.record(
                "notifications.enabled",
                config["notifications"]["enabled"],
                ParameterSource.ENVIRONMENT,
                origin="LLM_PROXY_ENABLE_NOTIFICATIONS",
            )

    sso_enabled = _env_to_bool(
        "SSO_ENABLED",
        False,
        env,
        path="sso.enabled",
        resolution=resolution,
    )

    if sso_enabled:
        from src.core.auth.sso.config import (
            AuthorizationConfig,
            CaptchaConfig,
            SSOConfig,
        )

        captcha_enabled = _env_to_bool(
            "SSO_CAPTCHA_ENABLED",
            True,
            env,
            path="sso.captcha.enabled",
            resolution=resolution,
        )

        captcha_config = CaptchaConfig(
            enabled=captcha_enabled,
            provider=_get_env_value(
                env,
                "SSO_CAPTCHA_PROVIDER",
                "cloudflare_turnstile",
                path="sso.captcha.provider",
                resolution=resolution,
            ),
            site_key=_get_env_value(
                env,
                "SSO_CAPTCHA_SITE_KEY",
                None,
                path="sso.captcha.site_key",
                resolution=resolution,
            ),
            secret_key=_get_env_value(
                env,
                "SSO_CAPTCHA_SECRET_KEY",
                None,
                path="sso.captcha.secret_key",
                resolution=resolution,
            ),
            verify_url=_get_env_value(
                env,
                "SSO_CAPTCHA_VERIFY_URL",
                "https://challenges.cloudflare.com/turnstile/v0/siteverify",
                path="sso.captcha.verify_url",
                resolution=resolution,
            ),
            widget_mode=_get_env_value(
                env,
                "SSO_CAPTCHA_WIDGET_MODE",
                "invisible",
                path="sso.captcha.widget_mode",
                resolution=resolution,
            ),
            timeout_seconds=_env_to_float(
                "SSO_CAPTCHA_TIMEOUT_SECONDS",
                5.0,
                env,
                path="sso.captcha.timeout_seconds",
                resolution=resolution,
            ),
        )

        config["sso"] = SSOConfig(
            enabled=True,
            session_lifetime_hours=_env_to_int(
                "SSO_SESSION_LIFETIME_HOURS",
                24,
                env,
                path="sso.session_lifetime_hours",
                resolution=resolution,
            ),
            database_path=_get_env_value(
                env,
                "SSO_DATABASE_PATH",
                "./var/sso_auth.db",
                path="sso.database_path",
                resolution=resolution,
            ),
            authorization=AuthorizationConfig(
                mode=_get_env_value(
                    env,
                    "SSO_AUTH_MODE",
                    "single_user",
                    path="sso.authorization.mode",
                    resolution=resolution,
                ),
                api_url=_get_env_value(
                    env,
                    "SSO_AUTH_API_URL",
                    None,
                    path="sso.authorization.api_url",
                    resolution=resolution,
                ),
                api_timeout=_env_to_int(
                    "SSO_AUTH_API_TIMEOUT",
                    30,
                    env,
                    path="sso.authorization.api_timeout",
                    resolution=resolution,
                ),
                confirmation_code_expiry_minutes=_env_to_int(
                    "SSO_CONFIRMATION_CODE_EXPIRY_MINUTES",
                    10,
                    env,
                    path="sso.authorization.confirmation_code_expiry_minutes",
                    resolution=resolution,
                ),
                max_confirmation_attempts=_env_to_int(
                    "SSO_MAX_CONFIRMATION_ATTEMPTS",
                    3,
                    env,
                    path="sso.authorization.max_confirmation_attempts",
                    resolution=resolution,
                ),
            ),
            captcha=captcha_config,
            providers={},
        )
    else:
        config["sso"] = None


def _apply_extended_provider_backends(
    config_backends: dict[str, Any],
    env: Mapping[str, str],
    resolution: ParameterResolution | None,
) -> None:
    alibaba_token_plan_key, _ = get_env_value_with_windows_persistent_fallback(
        "ALIBABA_TOKEN_PLAN_API_KEY", environ=env
    )
    if alibaba_token_plan_key:
        config_backends["alibaba-token-plan-intl"] = config_backends.get(
            "alibaba-token-plan-intl", {}
        )
        # Presence registers the backend; the connector always reads the live env value.
        config_backends["alibaba-token-plan-intl"]["api_key"] = alibaba_token_plan_key
        if resolution is not None:
            resolution.record(
                "backends.alibaba-token-plan-intl.api_key",
                config_backends["alibaba-token-plan-intl"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="ALIBABA_TOKEN_PLAN_API_KEY",
            )

    if env.get("OPENCODE_GO_API_KEY") and not _has_numbered_env_variants(
        env, "OPENCODE_GO_API_KEY"
    ):
        config_backends["opencode-go"] = config_backends.get("opencode-go", {})
        config_backends["opencode-go"]["api_key"] = env["OPENCODE_GO_API_KEY"]
        config_backends["opencode-go"]["api_url"] = _get_env_value(
            env,
            "OPENCODE_GO_API_BASE_URL",
            "https://opencode.ai/zen/go/v1",
            path="backends.opencode-go.api_url",
            resolution=resolution,
        )
        opencode_go_timeout = _get_env_value(
            env,
            "OPENCODE_GO_TIMEOUT",
            None,
            path="backends.opencode-go.timeout",
            resolution=resolution,
            transform=lambda value: _to_int(value, 0),
        )
        if opencode_go_timeout:
            config_backends["opencode-go"]["timeout"] = opencode_go_timeout
        if resolution is not None:
            resolution.record(
                "backends.opencode-go.api_key",
                config_backends["opencode-go"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="OPENCODE_GO_API_KEY",
            )

    if env.get("OPENCODE_ZEN_API_KEY") and not _has_numbered_env_variants(
        env, "OPENCODE_ZEN_API_KEY"
    ):
        config_backends["opencode-zen"] = config_backends.get("opencode-zen", {})
        config_backends["opencode-zen"]["api_key"] = env["OPENCODE_ZEN_API_KEY"]
        config_backends["opencode-zen"]["api_url"] = _get_env_value(
            env,
            "OPENCODE_ZEN_API_BASE_URL",
            "https://opencode.ai/zen/v1",
            path="backends.opencode-zen.api_url",
            resolution=resolution,
        )
        if resolution is not None:
            resolution.record(
                "backends.opencode-zen.api_key",
                config_backends["opencode-zen"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="OPENCODE_ZEN_API_KEY",
            )

    if env.get("COMMANDCODE_API_KEY") and not _has_numbered_env_variants(
        env, "COMMANDCODE_API_KEY"
    ):
        config_backends["commandcode-openai"] = config_backends.get(
            "commandcode-openai", {}
        )
        config_backends["commandcode-openai"]["api_key"] = env["COMMANDCODE_API_KEY"]
        config_backends["commandcode-openai"]["api_url"] = _get_env_value(
            env,
            "COMMANDCODE_API_BASE_URL",
            "https://api.commandcode.ai/provider/v1",
            path="backends.commandcode-openai.api_url",
            resolution=resolution,
        )
        if resolution is not None:
            resolution.record(
                "backends.commandcode-openai.api_key",
                config_backends["commandcode-openai"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="COMMANDCODE_API_KEY",
            )

    if env.get("MINIMAX_API_KEY"):
        config_backends["minimax"] = config_backends.get("minimax", {})
        config_backends["minimax"]["api_key"] = env["MINIMAX_API_KEY"]
        config_backends["minimax"]["api_url"] = _get_env_value(
            env,
            "MINIMAX_API_BASE_URL",
            "https://api.minimax.io/v1",
            path="backends.minimax.api_url",
            resolution=resolution,
        )
        minimax_timeout = _get_env_value(
            env,
            "MINIMAX_TIMEOUT",
            None,
            path="backends.minimax.timeout",
            resolution=resolution,
            transform=lambda value: _to_int(value, 0),
        )
        if minimax_timeout:
            config_backends["minimax"]["timeout"] = minimax_timeout
        if resolution is not None:
            resolution.record(
                "backends.minimax.api_key",
                config_backends["minimax"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="MINIMAX_API_KEY",
            )

    if env.get("NVIDIA_API_KEY") and not _has_numbered_env_variants(
        env, "NVIDIA_API_KEY"
    ):
        config_backends["nvidia"] = config_backends.get("nvidia", {})
        config_backends["nvidia"]["api_key"] = env["NVIDIA_API_KEY"]
        config_backends["nvidia"]["api_url"] = _get_env_value(
            env,
            "NVIDIA_API_BASE_URL",
            "https://integrate.api.nvidia.com/v1",
            path="backends.nvidia.api_url",
            resolution=resolution,
        )
        nvidia_timeout = _get_env_value(
            env,
            "NVIDIA_TIMEOUT",
            None,
            path="backends.nvidia.timeout",
            resolution=resolution,
            transform=lambda value: _to_int(value, 0),
        )
        if nvidia_timeout:
            config_backends["nvidia"]["timeout"] = nvidia_timeout
        if resolution is not None:
            resolution.record(
                "backends.nvidia.api_key",
                config_backends["nvidia"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="NVIDIA_API_KEY",
            )

    if env.get("RUNINFRA_API_KEY") and not _has_numbered_env_variants(
        env, "RUNINFRA_API_KEY"
    ):
        config_backends["runinfra"] = config_backends.get("runinfra", {})
        config_backends["runinfra"]["api_key"] = env["RUNINFRA_API_KEY"]
        config_backends["runinfra"]["api_url"] = _get_env_value(
            env,
            "RUNINFRA_API_BASE_URL",
            "https://api.runinfra.ai/v1",
            path="backends.runinfra.api_url",
            resolution=resolution,
        )
        runinfra_timeout = _get_env_value(
            env,
            "RUNINFRA_TIMEOUT",
            None,
            path="backends.runinfra.timeout",
            resolution=resolution,
            transform=lambda value: _to_int(value, 0),
        )
        if runinfra_timeout:
            config_backends["runinfra"]["timeout"] = runinfra_timeout
        if resolution is not None:
            resolution.record(
                "backends.runinfra.api_key",
                config_backends["runinfra"]["api_key"],
                ParameterSource.ENVIRONMENT,
                origin="RUNINFRA_API_KEY",
            )

    if (
        env.get("OPENAI_CODEX_AUTH_PATH")
        or env.get("OPENAI_CODEX_PATH")
        or (
            env.get("OPENAI_CODEX_API_KEY")
            and not _has_numbered_env_variants(env, "OPENAI_CODEX_API_KEY")
        )
    ):
        config_backends["openai-codex"] = config_backends.get("openai-codex", {})
        codex_auth_path = env.get("OPENAI_CODEX_AUTH_PATH") or env.get(
            "OPENAI_CODEX_PATH"
        )
        if codex_auth_path:
            config_backends["openai-codex"]["credentials_path"] = codex_auth_path
            if resolution is not None:
                resolution.record(
                    "backends.openai-codex.credentials_path",
                    codex_auth_path,
                    ParameterSource.ENVIRONMENT,
                    origin=(
                        "OPENAI_CODEX_AUTH_PATH"
                        if env.get("OPENAI_CODEX_AUTH_PATH")
                        else "OPENAI_CODEX_PATH"
                    ),
                )
        if env.get("OPENAI_CODEX_API_KEY"):
            config_backends["openai-codex"]["api_key"] = env["OPENAI_CODEX_API_KEY"]
            if resolution is not None:
                resolution.record(
                    "backends.openai-codex.api_key",
                    config_backends["openai-codex"]["api_key"],
                    ParameterSource.ENVIRONMENT,
                    origin="OPENAI_CODEX_API_KEY",
                )
