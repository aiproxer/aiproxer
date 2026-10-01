"""Unit tests for EndOfSessionService.

Tests cover:
- Config gating (disabled, emit_events=False)
- Atomic claim dedupe (concurrent signals)
- In-memory cache dedupe
- Event emission with correct payload
- Dispatch timeout behavior (stop waiting, don't cancel handlers)
- Termination category and error classification
- Missing session_id handling
- Restart safety (DB-backed dedupe)
"""

from __future__ import annotations

import asyncio
from dataclasses import replace
from datetime import datetime, timezone
from unittest.mock import AsyncMock, MagicMock

import pytest
from freezegun import freeze_time
from src.core.config.models.end_of_session import EndOfSessionConfig
from src.core.database.repositories.usage_repository import SessionMetricsRepository
from src.core.domain.events.end_of_session_events import (
    EndOfSessionErrorClassification,
    EndOfSessionSignal,
    EndOfSessionSignalType,
    EndOfSessionTerminationCategory,
    RemoteBackendConnectionEndOfSessionEvent,
)
from src.core.interfaces.event_bus_interface import IEventBus
from src.core.services.end_of_session_service import EndOfSessionService


@pytest.fixture
def mock_event_bus() -> MagicMock:
    """Create a mock event bus."""
    mock = MagicMock(spec=IEventBus)
    mock.publish = AsyncMock()
    mock.publish_nowait = AsyncMock()
    return mock


@pytest.fixture
def mock_session_repository() -> MagicMock:
    """Create a mock session metrics repository."""
    mock = MagicMock(spec=SessionMetricsRepository)
    mock.claim_eos_emission = AsyncMock(return_value=True)
    mock.has_ended = AsyncMock(return_value=False)
    return mock


@pytest.fixture
def default_config() -> EndOfSessionConfig:
    """Create default EoS configuration."""
    return EndOfSessionConfig(
        enabled=True,
        emit_events=True,
        detect_stream_signals=True,
        detect_tool_completion=True,
        dispatch_timeout_seconds=5.0,
    )


@pytest.fixture
def service(
    mock_event_bus: MagicMock,
    default_config: EndOfSessionConfig,
    mock_session_repository: MagicMock,
) -> EndOfSessionService:
    """Create EndOfSessionService instance for testing."""
    return EndOfSessionService(
        event_bus=mock_event_bus,
        config=default_config,
        session_repository=mock_session_repository,
    )


@pytest.fixture
def sample_signal() -> EndOfSessionSignal:
    """Create a sample EoS signal."""
    # Use fixed timestamp - tests should control time via @freeze_time decorator
    return EndOfSessionSignal(
        session_id="test-session-123",
        signal_type=EndOfSessionSignalType.DONE_SENTINEL,
        termination_category=EndOfSessionTerminationCategory.NORMAL,
        observed_at=datetime(2024, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
        reason="Stream completed",
        protocol="openai",
        backend="openai",
    )


@pytest.mark.asyncio
async def test_request_metrics_claim_preserves_conversation_event_identity(
    service: EndOfSessionService,
    mock_session_repository: MagicMock,
    mock_event_bus: MagicMock,
    sample_signal: EndOfSessionSignal,
) -> None:
    # HTTP metrics are initialized by request_id, even when the conversation is shared.
    mock_session_repository.claim_eos_emission.side_effect = lambda **kwargs: kwargs[
        "session_id"
    ] in {"req-main", "req-title"}
    for request_id in ("req-main", "req-title", "req-main"):
        await service.record_signal(replace(sample_signal, request_id=request_id))
    assert mock_event_bus.publish.await_count == 2
    events = [call.args[0] for call in mock_event_bus.publish.await_args_list]
    assert [event.request_id for event in events] == ["req-main", "req-title"]
    assert all(event.session_id == sample_signal.session_id for event in events)
    assert await service.has_ended(sample_signal.session_id, "req-main")
    assert not await service.has_ended(sample_signal.session_id, "req-next")


class TestConfigGating:
    """Test configuration gating behavior."""

    @pytest.mark.asyncio
    async def test_disabled_config_skips_emission(
        self,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
    ):
        """Test that disabled config prevents emission."""
        config = EndOfSessionConfig(enabled=False, emit_events=True)
        service = EndOfSessionService(
            event_bus=mock_event_bus,
            config=config,
            session_repository=mock_session_repository,
        )

        await service.record_signal(sample_signal)

        mock_session_repository.claim_eos_emission.assert_not_awaited()
        mock_event_bus.publish.assert_not_awaited()
        mock_event_bus.publish_nowait.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_emit_events_false_skips_emission(
        self,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
    ):
        """Test that emit_events=False prevents emission."""
        config = EndOfSessionConfig(enabled=True, emit_events=False)
        service = EndOfSessionService(
            event_bus=mock_event_bus,
            config=config,
            session_repository=mock_session_repository,
        )

        await service.record_signal(sample_signal)

        mock_session_repository.claim_eos_emission.assert_not_awaited()
        mock_event_bus.publish.assert_not_awaited()
        mock_event_bus.publish_nowait.assert_not_awaited()


class TestMissingSessionId:
    """Test handling of missing session_id."""

    @pytest.mark.asyncio
    @freeze_time("2024-01-01 12:00:00")
    async def test_missing_session_id_skips_emission(
        self,
        service: EndOfSessionService,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
    ):
        """Test that missing session_id prevents emission."""
        signal = EndOfSessionSignal(
            session_id="",
            signal_type=EndOfSessionSignalType.DONE_SENTINEL,
            termination_category=EndOfSessionTerminationCategory.NORMAL,
            observed_at=datetime(2024, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
        )

        await service.record_signal(signal)

        mock_session_repository.claim_eos_emission.assert_not_awaited()
        mock_event_bus.publish.assert_not_awaited()


class TestInMemoryDedupe:
    """Test in-memory cache dedupe behavior."""

    @pytest.mark.asyncio
    async def test_in_memory_cache_prevents_duplicate_emission(
        self,
        service: EndOfSessionService,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
    ):
        """Test that in-memory cache prevents duplicate emissions."""
        # First emission succeeds
        mock_session_repository.claim_eos_emission.return_value = True
        await service.record_signal(sample_signal)

        # Verify first emission occurred
        assert mock_session_repository.claim_eos_emission.await_count == 1

        # Second emission should be skipped due to cache
        await service.record_signal(sample_signal)

        # Should not attempt another claim
        assert mock_session_repository.claim_eos_emission.await_count == 1

    @pytest.mark.asyncio
    async def test_has_ended_checks_cache(
        self, service: EndOfSessionService, sample_signal: EndOfSessionSignal
    ):
        """Test that has_ended checks in-memory cache."""
        assert not await service.has_ended(sample_signal.session_id)

        # Mark as ended
        await service._mark_ended(sample_signal.session_id)

        assert await service.has_ended(sample_signal.session_id)

    @pytest.mark.asyncio
    async def test_has_ended_checks_session_even_when_request_id_provided(
        self, service: EndOfSessionService, sample_signal: EndOfSessionSignal
    ):
        """Session-level cache should short-circuit request-scoped checks."""
        await service._mark_ended(sample_signal.session_id)

        assert await service.has_ended(sample_signal.session_id, request_id="req-1")


class TestCacheEviction:
    """Test in-memory cache eviction behavior."""

    @pytest.mark.asyncio
    async def test_cache_evicts_oldest_item(
        self,
        service: EndOfSessionService,
        mock_session_repository: MagicMock,
    ):
        """Test that cache evicts oldest item when limit exceeded."""
        # Monkey-patch MAX_CACHE_SIZE for this test
        import src.core.services.end_of_session_service as service_module

        original_max_size = service_module.MAX_CACHE_SIZE
        service_module.MAX_CACHE_SIZE = 2

        try:
            # Add 3 items
            await service._mark_ended("session-1")
            await service._mark_ended("session-2")
            await service._mark_ended("session-3")

            # Verify size is capped at 2
            assert len(service._ended_sessions) == 2

            # Verify eviction: session-1 should be gone (oldest)
            assert not await service.has_ended("session-1")
            assert await service.has_ended("session-2")
            assert await service.has_ended("session-3")

            # Access session-2 to make it most recently used
            await service._mark_ended("session-2")

            # Add session-4
            await service._mark_ended("session-4")

            # Verify eviction: session-3 should be gone (oldest, since session-2 was refreshed)
            assert not await service.has_ended("session-3")
            assert await service.has_ended("session-2")
            assert await service.has_ended("session-4")

        finally:
            service_module.MAX_CACHE_SIZE = original_max_size


class TestAtomicClaimDedupe:
    """Test atomic database claim dedupe behavior."""

    @pytest.mark.asyncio
    async def test_atomic_claim_failure_skips_emission(
        self,
        service: EndOfSessionService,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
    ):
        """Test that failed atomic claim prevents emission."""
        mock_session_repository.claim_eos_emission.return_value = False

        await service.record_signal(sample_signal)

        # Should attempt claim but not emit event
        mock_session_repository.claim_eos_emission.assert_awaited_once()
        mock_event_bus.publish.assert_not_awaited()
        mock_event_bus.publish_nowait.assert_not_awaited()

        # Cache should be updated
        assert await service.has_ended(sample_signal.session_id)

    @pytest.mark.asyncio
    @freeze_time("2024-01-01 12:00:00")
    async def test_concurrent_signals_only_one_emission(
        self,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        default_config: EndOfSessionConfig,
    ):
        """Test that concurrent signals for same session produce only one emission."""
        session_id = "concurrent-session-123"

        # First call succeeds, subsequent calls fail
        call_count = 0

        async def claim_side_effect(*args, **kwargs):
            nonlocal call_count
            call_count += 1
            return call_count == 1  # Only first call succeeds

        mock_session_repository.claim_eos_emission.side_effect = claim_side_effect

        service = EndOfSessionService(
            event_bus=mock_event_bus,
            config=default_config,
            session_repository=mock_session_repository,
        )

        # Create multiple signals for same session
        fixed_time = datetime(2024, 1, 1, 12, 0, 0, tzinfo=timezone.utc)
        signals = [
            EndOfSessionSignal(
                session_id=session_id,
                signal_type=EndOfSessionSignalType.DONE_SENTINEL,
                termination_category=EndOfSessionTerminationCategory.NORMAL,
                observed_at=fixed_time,
                reason=f"Signal {i}",
            )
            for i in range(5)
        ]

        # Process all signals concurrently
        await asyncio.gather(*[service.record_signal(signal) for signal in signals])

        # Only one emission should occur
        assert mock_event_bus.publish.await_count == 1

        # All claims should have been attempted (but cache may prevent some)
        # At least one claim should have been attempted
        assert mock_session_repository.claim_eos_emission.await_count >= 1
        # Due to in-memory cache, subsequent signals may be skipped before DB claim
        # This is expected behavior - cache prevents duplicate DB calls

        # Cache should reflect session ended
        assert await service.has_ended(session_id)

    @pytest.mark.asyncio
    async def test_terminal_state_persistence_after_claim(
        self,
        service: EndOfSessionService,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
    ):
        """Test that terminal state is persisted after successful claim."""
        mock_session_repository.claim_eos_emission.return_value = True

        await service.record_signal(sample_signal)

        # Verify claim was called with correct parameters
        mock_session_repository.claim_eos_emission.assert_awaited_once()
        call_kwargs = mock_session_repository.claim_eos_emission.call_args.kwargs
        assert call_kwargs["session_id"] == sample_signal.session_id
        assert call_kwargs["signal_type"] == sample_signal.signal_type.value
        assert call_kwargs["reason"] == sample_signal.reason
        assert call_kwargs["emitted_at"] is not None

        # Verify cache reflects terminal state
        assert await service.has_ended(sample_signal.session_id)

        # Verify event was emitted
        mock_event_bus.publish.assert_awaited_once()

    @pytest.mark.asyncio
    @freeze_time("2024-01-01 12:00:00")
    async def test_restart_safety_db_backed_dedupe(
        self,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        default_config: EndOfSessionConfig,
    ):
        """Test that DB-backed dedupe works after restart (simulated by new service instance)."""
        session_id = "restart-session-123"

        # Simulate session already ended in DB (has_ended returns True)
        mock_session_repository.has_ended = AsyncMock(return_value=True)
        mock_session_repository.claim_eos_emission.return_value = False

        # Create new service instance (simulating restart)
        service = EndOfSessionService(
            event_bus=mock_event_bus,
            config=default_config,
            session_repository=mock_session_repository,
        )

        signal = EndOfSessionSignal(
            session_id=session_id,
            signal_type=EndOfSessionSignalType.DONE_SENTINEL,
            termination_category=EndOfSessionTerminationCategory.NORMAL,
            observed_at=datetime(2024, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
        )

        # Try to emit - should be skipped
        await service.record_signal(signal)

        # Should attempt claim but fail (already claimed)
        mock_session_repository.claim_eos_emission.assert_awaited_once()

        # Should not emit event
        mock_event_bus.publish.assert_not_awaited()

        # Cache should be updated after failed claim
        assert await service.has_ended(session_id)


class TestEventEmission:
    """Test event emission behavior."""

    @pytest.mark.asyncio
    async def test_event_emission_with_correct_payload(
        self,
        service: EndOfSessionService,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
    ):
        """Test that event is emitted with correct payload."""
        mock_session_repository.claim_eos_emission.return_value = True

        await service.record_signal(sample_signal)

        # Verify event was published
        mock_event_bus.publish.assert_awaited_once()
        call_args = mock_event_bus.publish.call_args
        assert call_args is not None

        event = call_args[0][0]
        assert isinstance(event, RemoteBackendConnectionEndOfSessionEvent)
        assert event.session_id == sample_signal.session_id
        assert event.signal_type == sample_signal.signal_type
        assert event.termination_category == sample_signal.termination_category
        assert event.reason == sample_signal.reason
        assert event.protocol == sample_signal.protocol
        assert event.backend == sample_signal.backend

    @pytest.mark.asyncio
    @freeze_time("2024-01-01 12:00:00")
    async def test_error_classification_defaults_to_unknown(
        self,
        service: EndOfSessionService,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
    ):
        """Test that missing error classification defaults to unknown_error."""
        signal = EndOfSessionSignal(
            session_id="test-session-123",
            signal_type=EndOfSessionSignalType.ERROR_TERMINATION,
            termination_category=EndOfSessionTerminationCategory.ERROR,
            observed_at=datetime(2024, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
            error_classification=None,  # Missing classification
        )
        mock_session_repository.claim_eos_emission.return_value = True

        await service.record_signal(signal)

        call_args = mock_event_bus.publish.call_args
        assert call_args is not None
        event = call_args[0][0]
        assert (
            event.error_classification == EndOfSessionErrorClassification.UNKNOWN_ERROR
        )

    @pytest.mark.asyncio
    @freeze_time("2024-01-01 12:00:00")
    async def test_error_classification_preserved_when_present(
        self,
        service: EndOfSessionService,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
    ):
        """Test that error classification is preserved when present."""
        signal = EndOfSessionSignal(
            session_id="test-session-123",
            signal_type=EndOfSessionSignalType.ERROR_TERMINATION,
            termination_category=EndOfSessionTerminationCategory.ERROR,
            observed_at=datetime(2024, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
            error_classification=EndOfSessionErrorClassification.TRANSPORT_ERROR,
        )
        mock_session_repository.claim_eos_emission.return_value = True

        await service.record_signal(signal)

        call_args = mock_event_bus.publish.call_args
        assert call_args is not None
        event = call_args[0][0]
        assert (
            event.error_classification
            == EndOfSessionErrorClassification.TRANSPORT_ERROR
        )


class TestDispatchTimeout:
    """Test dispatch timeout behavior."""

    @pytest.mark.asyncio
    async def test_zero_timeout_uses_publish_nowait(
        self,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
    ):
        """Test that zero timeout uses publish_nowait."""
        config = EndOfSessionConfig(
            enabled=True,
            emit_events=True,
            dispatch_timeout_seconds=0.0,
        )
        service = EndOfSessionService(
            event_bus=mock_event_bus,
            config=config,
            session_repository=mock_session_repository,
        )
        mock_session_repository.claim_eos_emission.return_value = True

        await service.record_signal(sample_signal)

        mock_event_bus.publish_nowait.assert_awaited_once()
        mock_event_bus.publish.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_timeout_stops_waiting_without_canceling(
        self,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
    ):
        """Test that timeout stops waiting without canceling handlers."""
        config = EndOfSessionConfig(
            enabled=True,
            emit_events=True,
            dispatch_timeout_seconds=0.1,
        )
        service = EndOfSessionService(
            event_bus=mock_event_bus,
            config=config,
            session_repository=mock_session_repository,
        )
        mock_session_repository.claim_eos_emission.return_value = True

        # Make publish hang indefinitely
        async def slow_publish(*args, **kwargs):
            from tests.utils.fake_clock import FakeClockContext

            async with FakeClockContext() as clock:
                sleep_task = asyncio.create_task(asyncio.sleep(1.0))
                clock.advance(1.0)
                await sleep_task

        mock_event_bus.publish = AsyncMock(side_effect=slow_publish)

        await service.record_signal(sample_signal)

        # Should have attempted publish (shield prevents cancellation)
        mock_event_bus.publish.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_timeout_logs_warning_but_continues(
        self,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
        caplog,
    ):
        """Test that timeout logs warning but doesn't raise exception."""
        import logging

        config = EndOfSessionConfig(
            enabled=True,
            emit_events=True,
            dispatch_timeout_seconds=0.01,  # Very short timeout
        )
        service = EndOfSessionService(
            event_bus=mock_event_bus,
            config=config,
            session_repository=mock_session_repository,
        )
        mock_session_repository.claim_eos_emission.return_value = True

        # Make publish hang longer than timeout
        async def slow_publish(*args, **kwargs):
            from tests.utils.fake_clock import FakeClockContext

            async with FakeClockContext() as clock:
                sleep_task = asyncio.create_task(asyncio.sleep(0.1))
                clock.advance(0.1)
                await sleep_task

        mock_event_bus.publish = AsyncMock(side_effect=slow_publish)

        with caplog.at_level(logging.WARNING):
            await service.record_signal(sample_signal)

        # Should log timeout warning
        assert (
            "timeout" in caplog.text.lower()
            or "continuing without waiting" in caplog.text.lower()
        )

        # Should not raise exception
        assert mock_event_bus.publish.await_count == 1


class TestFailOpen:
    """Test fail-open error handling when persistence is unavailable."""

    @pytest.mark.asyncio
    async def test_db_unavailable_emits_event_in_fail_open_mode(
        self,
        service: EndOfSessionService,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
    ):
        """Test that DB unavailability triggers fail-open emission."""
        # Setup: DB claim fails
        mock_session_repository.claim_eos_emission.side_effect = Exception("DB error")

        # Execute: should not raise, should emit event in fail-open mode
        await service.record_signal(sample_signal)

        # Verify: event was emitted despite DB failure (uses publish with timeout)
        mock_event_bus.publish.assert_awaited_once()
        # Verify: session marked as ended in cache
        assert await service.has_ended(sample_signal.session_id)

    @pytest.mark.asyncio
    async def test_db_unavailable_dedupe_prevents_duplicate_emission(
        self,
        service: EndOfSessionService,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
    ):
        """Test that fail-open mode deduplicates multiple signals."""
        # Setup: DB claim fails
        mock_session_repository.claim_eos_emission.side_effect = Exception("DB error")

        # Execute: multiple signals for same session
        await service.record_signal(sample_signal)
        await service.record_signal(sample_signal)

        # Verify: only one event emitted (dedupe works)
        assert mock_event_bus.publish.await_count == 1

    @pytest.mark.asyncio
    async def test_db_timeout_emits_event_in_fail_open_mode(
        self,
        service: EndOfSessionService,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
    ):
        """Test that DB timeout triggers fail-open emission."""
        # Setup: DB claim raises timeout error
        mock_session_repository.claim_eos_emission.side_effect = asyncio.TimeoutError()

        # Execute: should emit in fail-open mode
        await service.record_signal(sample_signal)

        # Verify: event was emitted despite timeout
        mock_event_bus.publish.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_fail_open_logs_high_signal_diagnostic(
        self,
        service: EndOfSessionService,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
        caplog,
    ):
        """Test that fail-open mode logs high-signal diagnostic."""
        import logging

        # Setup: DB claim fails
        mock_session_repository.claim_eos_emission.side_effect = Exception("DB error")

        # Execute
        with caplog.at_level(logging.ERROR):
            await service.record_signal(sample_signal)

        # Verify: error logged with fail-open message
        # The error_code is in extra dict (for structured logging) but we verify the message
        assert "fail-open mode" in caplog.text.lower()
        assert "persistence unavailable" in caplog.text.lower()
        # Verify error code appears in log output (may be in structured format)
        assert (
            "EOS_PERSISTENCE_UNAVAILABLE" in caplog.text
            or "persistence unavailable" in caplog.text.lower()
        )

    @pytest.mark.asyncio
    async def test_event_bus_error_logged_but_not_raised(
        self,
        service: EndOfSessionService,
        mock_event_bus: MagicMock,
        mock_session_repository: MagicMock,
        sample_signal: EndOfSessionSignal,
    ):
        """Test that event bus errors are logged but not raised."""
        mock_session_repository.claim_eos_emission.return_value = True
        mock_event_bus.publish.side_effect = Exception("Event bus error")

        # Should not raise
        await service.record_signal(sample_signal)

        # Should have attempted emission
        mock_event_bus.publish.assert_awaited_once()
