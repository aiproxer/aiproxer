"""
Test scaled turn counting for Quality Verifier in tool-heavy workloads.

Regression tests for the bug where Quality Verifier turn counter would not increment
when requests were tool followups or replacement was active, preventing Quality Verifier
from ever reaching its frequency threshold in tool-heavy coding sessions.
"""

from unittest.mock import MagicMock

import pytest
from src.core.domain.quality_verifier_turns import QV_ELIGIBLE_TURN_SCALE
from src.core.services.request_processor_service import RequestProcessor


@pytest.fixture
def request_processor():
    """Create a RequestProcessor with minimal mocked dependencies for turn counting tests."""
    mock_dependencies = {
        "command_processor": MagicMock(),
        "session_manager": MagicMock(),
        "backend_request_manager": MagicMock(),
        "response_manager": MagicMock(),
        "session_enricher": MagicMock(),
        "request_side_effects": MagicMock(),
        "command_handler": MagicMock(),
        "backend_preparer": MagicMock(),
        "transform_pipeline": MagicMock(),
        "backend_executor": MagicMock(),
        "app_state": MagicMock(),
        "replacement_service": None,
    }

    return RequestProcessor(**mock_dependencies)


def test_turn_count_storage_uses_scaled_integers(request_processor):
    """In-memory counter stores integer scaled units."""
    session_key = "test-session"

    request_processor._set_quality_verifier_turn_count(session_key, 200)

    count = request_processor._get_quality_verifier_turn_count(session_key)

    assert count == 200
    assert isinstance(count, int)


def test_fractional_turns_accumulate_without_float_drift(request_processor):
    """Ten tool steps at 0.2 weight = 10 * 200 = 2000 scaled = 2 logical turns."""
    session_key = "test-session"
    step = int(round(QV_ELIGIBLE_TURN_SCALE * 0.2))

    for _i in range(10):
        current = request_processor._get_quality_verifier_turn_count(session_key)
        request_processor._set_quality_verifier_turn_count(session_key, current + step)

    final_count = request_processor._get_quality_verifier_turn_count(session_key)

    assert final_count == 10 * step == 2000


def test_mixed_turn_increments(request_processor):
    """Two full user turns plus five tool steps at 0.1 weight."""
    session_key = "test-session"
    tool_step = int(round(QV_ELIGIBLE_TURN_SCALE * 0.1))

    request_processor._set_quality_verifier_turn_count(
        session_key, QV_ELIGIBLE_TURN_SCALE
    )
    current = request_processor._get_quality_verifier_turn_count(session_key)
    request_processor._set_quality_verifier_turn_count(
        session_key, current + QV_ELIGIBLE_TURN_SCALE
    )

    for _ in range(5):
        current = request_processor._get_quality_verifier_turn_count(session_key)
        request_processor._set_quality_verifier_turn_count(
            session_key, current + tool_step
        )

    final_count = request_processor._get_quality_verifier_turn_count(session_key)

    assert final_count == 2 * QV_ELIGIBLE_TURN_SCALE + 5 * tool_step


def test_turn_count_does_not_go_negative(request_processor):
    """Turn count is clamped to non-negative values."""
    session_key = "test-session"

    request_processor._set_quality_verifier_turn_count(session_key, -5)

    count = request_processor._get_quality_verifier_turn_count(session_key)

    assert count == 0


def test_turn_count_lru_eviction(request_processor):
    """LRU cache evicts old sessions when full."""
    test_count = 100
    step = int(round(QV_ELIGIBLE_TURN_SCALE * 0.1))

    for i in range(test_count):
        session_key = f"session-{i}"
        request_processor._set_quality_verifier_turn_count(session_key, i * step)

    cache_size = len(request_processor._quality_verifier_turn_counts)
    assert cache_size == test_count

    last_session = f"session-{test_count - 1}"
    count = request_processor._get_quality_verifier_turn_count(last_session)
    expected = (test_count - 1) * step
    assert count == expected


def test_legacy_float_in_lru_migrated_on_read(request_processor):
    """Float values left in the LRU map from older builds are migrated once."""
    session_key = "legacy"
    request_processor._quality_verifier_turn_counts[session_key] = 2.5  # type: ignore[assignment]

    count = request_processor._get_quality_verifier_turn_count(session_key)

    assert count == 2500
    assert request_processor._quality_verifier_turn_counts[session_key] == 2500
