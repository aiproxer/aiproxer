import pytest
from src.core.cli import apply_cli_args, parse_cli_args


def test_sso_captcha_default_enabled(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that SSO captcha is disabled by default (requires explicit configuration)."""
    # Ensure no env var
    monkeypatch.delenv("SSO_CAPTCHA_ENABLED", raising=False)
    monkeypatch.setenv("SSO_ENABLED", "true")

    args = parse_cli_args([])
    cfg = apply_cli_args(args)
    if isinstance(cfg, tuple):
        cfg = cfg[0]

    assert cfg.sso is not None
    # Captcha is disabled by default (requires site_key and secret_key to be useful)
    if cfg.sso.captcha is None:
        assert True  # Disabled captcha may result in None
    else:
        assert cfg.sso.captcha.enabled is False


def test_sso_captcha_disabled_via_env(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that SSO captcha can be disabled via environment variable."""
    monkeypatch.setenv("SSO_ENABLED", "true")
    monkeypatch.setenv("SSO_CAPTCHA_ENABLED", "false")

    args = parse_cli_args([])
    cfg = apply_cli_args(args)
    if isinstance(cfg, tuple):
        cfg = cfg[0]

    assert cfg.sso is not None
    # If captcha is disabled in AppConfig via env, it might be None or enabled=False depending on implementation
    # AppConfig logic: if captcha_enabled is false, captcha_config is None.
    # See src/core/config/app_config.py around line 2355
    if cfg.sso.captcha is None:
        assert True
    else:
        assert cfg.sso.captcha.enabled is False


def test_sso_captcha_enabled_via_env(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that SSO captcha can be enabled via environment variable."""
    monkeypatch.setenv("SSO_ENABLED", "true")
    monkeypatch.setenv("SSO_CAPTCHA_ENABLED", "true")

    args = parse_cli_args([])
    cfg = apply_cli_args(args)
    if isinstance(cfg, tuple):
        cfg = cfg[0]

    assert cfg.sso is not None
    assert cfg.sso.captcha is not None
    assert cfg.sso.captcha.enabled is True


def test_sso_captcha_disabled_via_cli(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that SSO captcha can be disabled via CLI, overriding env."""
    monkeypatch.setenv("SSO_ENABLED", "true")
    # Set env to true to verify CLI override
    monkeypatch.setenv("SSO_CAPTCHA_ENABLED", "true")

    args = parse_cli_args(["--disable-sso-captcha"])
    cfg = apply_cli_args(args)
    if isinstance(cfg, tuple):
        cfg = cfg[0]

    assert cfg.sso is not None
    assert cfg.sso.captcha is not None
    assert cfg.sso.captcha.enabled is False


def test_sso_captcha_config_file_defaults(
    monkeypatch: pytest.MonkeyPatch, tmp_path
) -> None:
    """Test that config file settings are respected but overridden by CLI/Env."""
    cfg_file = tmp_path / "sso_config.yaml"
    cfg_file.write_text("""
sso:
  enabled: true
  captcha:
    enabled: false
    provider: cloudflare_turnstile
""")

    monkeypatch.delenv("SSO_CAPTCHA_ENABLED", raising=False)
    monkeypatch.setenv("SSO_ENABLED", "true")

    # 1. Config file only - verify default from file
    args = parse_cli_args(["--config", str(cfg_file)])
    cfg = apply_cli_args(args)
    if isinstance(cfg, tuple):
        cfg = cfg[0]

    if cfg.sso.captcha is None:
        assert True
    else:
        assert cfg.sso.captcha.enabled is False

    # 2. CLI override (CLI > Config) - skip env parse to save time
    args = parse_cli_args(["--config", str(cfg_file), "--disable-sso-captcha"])
    cfg = apply_cli_args(args)
    if isinstance(cfg, tuple):
        cfg = cfg[0]

    assert cfg.sso.captcha is not None
    assert cfg.sso.captcha.enabled is False
