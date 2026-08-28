"""
Unit tests for CLI argument parsing.
"""

from src.core.config.cli_args import apply_cli_overrides, parse_cli_args


def test_parse_cli_args_empty():
    """Test parsing with no arguments."""
    result = parse_cli_args([])
    assert result == {}


def test_parse_cli_args_sso_enabled():
    """Test parsing --sso-enabled flag."""
    result = parse_cli_args(["--sso-enabled"])
    assert result == {"sso_enabled": True}


def test_parse_cli_args_sso_provider():
    """Test parsing --sso-provider flag."""
    result = parse_cli_args(["--sso-provider", "google"])
    assert result == {"sso_provider": "google"}


def test_parse_cli_args_sso_auth_mode():
    """Test parsing --sso-auth-mode flag."""
    result = parse_cli_args(["--sso-auth-mode", "enterprise"])
    assert result == {"sso_auth_mode": "enterprise"}


def test_parse_cli_args_multiple():
    """Test parsing multiple SSO flags."""
    result = parse_cli_args(
        [
            "--sso-enabled",
            "--sso-provider",
            "microsoft",
            "--sso-auth-mode",
            "single_user",
        ]
    )
    assert result == {
        "sso_enabled": True,
        "sso_provider": "microsoft",
        "sso_auth_mode": "single_user",
    }


def test_parse_cli_args_host_and_port():
    """Test parsing host and port flags."""
    result = parse_cli_args(["--host", "0.0.0.0", "--port", "9000"])
    assert result == {
        "host": "0.0.0.0",
        "port": 9000,
    }


def test_parse_cli_args_resilience_personal_backends():
    """Test parsing resilience personal backend overrides."""
    result = parse_cli_args(
        ["--resilience-personal-backends", "openai-codex,qwen-oauth"]
    )
    assert result == {
        "resilience_personal_backends": ["openai-codex", "qwen-oauth"],
    }


def test_parse_cli_args_resilience_shared_backends():
    """Test parsing resilience shared backend overrides."""
    result = parse_cli_args(["--resilience-shared-backends", "openai,openrouter"])
    assert result == {
        "resilience_shared_backends": ["openai", "openrouter"],
    }


def test_apply_cli_overrides_sso_enabled():
    """Test applying SSO enabled override."""
    env_dict = {}
    cli_args = {"sso_enabled": True}
    apply_cli_overrides(env_dict, cli_args)
    assert env_dict["SSO_ENABLED"] == "true"


def test_apply_cli_overrides_sso_provider():
    """Test applying SSO provider override."""
    env_dict = {}
    cli_args = {"sso_provider": "github"}
    apply_cli_overrides(env_dict, cli_args)
    assert env_dict["SSO_PROVIDER"] == "github"


def test_apply_cli_overrides_sso_auth_mode():
    """Test applying SSO auth mode override."""
    env_dict = {}
    cli_args = {"sso_auth_mode": "enterprise"}
    apply_cli_overrides(env_dict, cli_args)
    assert env_dict["SSO_AUTH_MODE"] == "enterprise"


def test_apply_cli_overrides_multiple():
    """Test applying multiple overrides."""
    env_dict = {"EXISTING_VAR": "value"}
    cli_args = {
        "sso_enabled": True,
        "sso_provider": "google",
        "sso_auth_mode": "single_user",
        "host": "127.0.0.1",
        "port": 8080,
    }
    apply_cli_overrides(env_dict, cli_args)

    assert env_dict["SSO_ENABLED"] == "true"
    assert env_dict["SSO_PROVIDER"] == "google"
    assert env_dict["SSO_AUTH_MODE"] == "single_user"
    assert env_dict["APP_HOST"] == "127.0.0.1"
    assert env_dict["APP_PORT"] == "8080"
    assert env_dict["EXISTING_VAR"] == "value"  # Existing vars preserved


def test_apply_cli_overrides_resilience_backends():
    """Test applying resilience backend overrides."""
    env_dict: dict[str, str] = {}
    cli_args = {
        "resilience_personal_backends": ["openai-codex", "qwen-oauth"],
        "resilience_shared_backends": ["openai", "openrouter"],
    }
    apply_cli_overrides(env_dict, cli_args)
    assert env_dict["RESILIENCE_PERSONAL_BACKEND_TYPES"] == "openai-codex,qwen-oauth"
    assert env_dict["RESILIENCE_SHARED_BACKEND_TYPES"] == "openai,openrouter"


def test_apply_cli_overrides_empty():
    """Test applying empty overrides doesn't modify env."""
    env_dict = {"EXISTING_VAR": "value"}
    cli_args = {}
    apply_cli_overrides(env_dict, cli_args)
    assert env_dict == {"EXISTING_VAR": "value"}
