import os
from unittest.mock import patch

import pytest
from src.core.config.app_config import AppConfig, load_config


@pytest.fixture
def mock_env():
    with patch.dict(os.environ, {}, clear=True) as mock_environ:
        yield mock_environ


def test_hybrid_config_default_probability():
    config = AppConfig()
    assert config.backends.reasoning_injection_probability == 1.0


def test_hybrid_config_from_env(mock_env):
    mock_env["REASONING_INJECTION_PROBABILITY"] = "0.5"
    config = AppConfig.from_env()
    assert config.backends.reasoning_injection_probability == 0.5


def test_hybrid_config_from_file(tmp_path):
    config_file = tmp_path / "config.yaml"
    config_file.write_text("""
backends:
  reasoning_injection_probability: 0.25
""")
    config = load_config(str(config_file))
    assert config.backends.reasoning_injection_probability == 0.25


def test_hybrid_config_cli_overrides_all(tmp_path):
    config_file = tmp_path / "config.yaml"
    config_file.write_text("""
backends:
  reasoning_injection_probability: 0.25
""")
    with patch.dict(os.environ, {"REASONING_INJECTION_PROBABILITY": "0.5"}, clear=True):
        from src.core.cli import apply_cli_args, parse_cli_args

        args = parse_cli_args(
            ["--config", str(config_file), "--reasoning-injection-probability", "0.8"]
        )
        config = apply_cli_args(args)
        assert isinstance(config, AppConfig)
        assert config.backends.reasoning_injection_probability == 0.8


def test_hybrid_config_env_overrides_file(tmp_path):
    config_file = tmp_path / "config.yaml"
    config_file.write_text("""
backends:
  reasoning_injection_probability: 0.25
""")
    with patch.dict(os.environ, {"REASONING_INJECTION_PROBABILITY": "0.5"}, clear=True):
        from src.core.cli import apply_cli_args, parse_cli_args

        args = parse_cli_args(["--config", str(config_file)])
        config = apply_cli_args(args)
        assert isinstance(config, AppConfig)
        assert config.backends.reasoning_injection_probability == 0.5
