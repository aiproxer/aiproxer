"""Contract tests for resilience circuit breaker configuration."""

from __future__ import annotations

import pytest
from pydantic import ValidationError
from src.core.config.models.misc import ResilienceConfig


def test_resilience_config_includes_circuit_breaker_defaults() -> None:
    """Resilience defaults include a safe, enabled circuit breaker config."""
    config = ResilienceConfig()

    assert config.circuit_breaker.enabled is True
    assert config.circuit_breaker.failure_threshold >= 1
    assert config.circuit_breaker.open_cooldown_seconds > 0


def test_resilience_config_parses_disabled_circuit_breaker() -> None:
    """Circuit breaker settings parse cleanly from partial input."""
    config = ResilienceConfig.model_validate({"circuit_breaker": {"enabled": False}})

    assert config.circuit_breaker.enabled is False


@pytest.mark.parametrize(
    "payload",
    [
        {"circuit_breaker": {"failure_threshold": 0}},
        {"circuit_breaker": {"open_cooldown_seconds": 0}},
        {"circuit_breaker": {"open_cooldown_seconds": -1}},
    ],
)
def test_resilience_config_rejects_invalid_circuit_breaker_values(
    payload: dict[str, dict[str, float | int]],
) -> None:
    """Validation rejects non-sensical circuit breaker thresholds."""
    with pytest.raises(ValidationError):
        ResilienceConfig.model_validate(payload)
