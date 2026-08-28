"""Unit tests for SessionMetricsInitializer.

Tests cover:
- Success case: metrics created/updated
- Timeout case: returns without raising when DB is slow
- DB unavailable: logs error but doesn't raise
- Concurrent initialization: atomic upsert handles race conditions
"""

from __future__ import annotations

import asyncio
from datetime import datetime, timezone
from typing import Any, cast
from unittest.mock import AsyncMock, MagicMock

import pytest
from freezegun import freeze_time
from src.core.database.models.usage import SessionMetricsTable
from src.core.database.repositories.usage_repository import SessionMetricsRepository
from src.core.domain.session_key import SessionKey
from src.core.services.session_metrics_initializer import (
    DEFAULT_TIMEOUT_SECONDS,
    SessionMetricsInitializer,
)

from tests.utils.fake_clock import FakeClock, FakeClockContext


@pytest.fixture
def mock_session_repository() -> SessionMetricsRepository:
    """Create a mock session metrics repository."""
    mock = MagicMock(spec=SessionMetricsRepository)
    cast(Any, mock).upsert = AsyncMock()
    return mock


@pytest.fixture
def initializer(
    mock_session_repository: SessionMetricsRepository,
) -> SessionMetricsInitializer:
    """Create SessionMetricsInitializer instance for testing."""
    return SessionMetricsInitializer(
        session_repository=mock_session_repository,
        timeout_seconds=DEFAULT_TIMEOUT_SECONDS,
        cache_ttl_seconds=0.0,  # Disable cache in tests to allow testing actual DB calls
    )


@pytest.fixture
def sample_session_key() -> SessionKey:
    """Create a sample session key."""
    return SessionKey(
        protocol="http",
        primary_id="test-session-123",
        group_id="conversation-456",
    )


@pytest.fixture
def sample_observed_at() -> datetime:
    """Create a sample observation timestamp."""
    with freeze_time("2024-01-01 12:00:00"):
        return datetime.now(timezone.utc)


class TestSuccessCase:
    """Test successful metrics initialization."""

    @pytest.mark.asyncio
    async def test_metrics_created_successfully(
        self,
        initializer: SessionMetricsInitializer,
        mock_session_repository: SessionMetricsRepository,
        sample_session_key: SessionKey,
        sample_observed_at: datetime,
    ):
        """Test that metrics are created successfully."""
        # Setup: mock successful upsert
        mock_metrics = SessionMetricsTable(
            session_id="test-session-123",
            start_time=sample_observed_at,
            last_activity=sample_observed_at,
            turn_count=0,
            total_tokens=0,
            total_tool_calls=0,
            is_completed=False,
        )
        mock_repo = cast(Any, mock_session_repository)
        mock_repo.upsert = AsyncMock(return_value=mock_metrics)

        # Execute
        await initializer.ensure_session_metrics(
            sample_session_key, observed_at=sample_observed_at
        )

        # Verify: upsert was called with correct metrics
        mock_repo.upsert.assert_awaited_once()
        call_args = mock_repo.upsert.call_args[0][0]
        assert isinstance(call_args, SessionMetricsTable)
        assert call_args.session_id == "test-session-123"
        assert call_args.start_time == sample_observed_at
        assert call_args.last_activity == sample_observed_at
        assert call_args.turn_count == 0
        assert call_args.total_tokens == 0
        assert call_args.total_tool_calls == 0
        assert call_args.is_completed is False

    @pytest.mark.asyncio
    async def test_metrics_updated_successfully(
        self,
        initializer: SessionMetricsInitializer,
        mock_session_repository: SessionMetricsRepository,
        sample_session_key: SessionKey,
        sample_observed_at: datetime,
    ):
        """Test that existing metrics are updated successfully."""
        # Setup: mock successful upsert (update case)
        existing_metrics = SessionMetricsTable(
            session_id="test-session-123",
            start_time=sample_observed_at,
            last_activity=sample_observed_at,
            turn_count=5,
            total_tokens=1000,
            total_tool_calls=2,
            is_completed=False,
        )
        mock_repo = cast(Any, mock_session_repository)
        mock_repo.upsert = AsyncMock(return_value=existing_metrics)

        # Execute
        await initializer.ensure_session_metrics(
            sample_session_key, observed_at=sample_observed_at
        )

        # Verify: upsert was called (repository handles update logic)
        mock_repo.upsert.assert_awaited_once()


class TestTimeoutCase:
    """Test timeout behavior."""

    @pytest.mark.asyncio
    async def test_timeout_returns_without_raising(
        self,
        mock_session_repository: SessionMetricsRepository,
        sample_session_key: SessionKey,
        sample_observed_at: datetime,
    ):
        """Test that timeout returns without raising."""

        # Setup: mock slow upsert that exceeds timeout
        from tests.utils.fake_clock import FakeClockContext

        async def slow_upsert(metrics: SessionMetricsTable) -> SessionMetricsTable:
            # Use fake clock for deterministic time simulation
            await asyncio.sleep(DEFAULT_TIMEOUT_SECONDS + 0.5)
            return metrics

        mock_repo = cast(Any, mock_session_repository)
        mock_repo.upsert = AsyncMock(side_effect=slow_upsert)

        # Create initializer with short timeout for faster test
        initializer = SessionMetricsInitializer(
            session_repository=mock_session_repository,
            timeout_seconds=0.1,
            cache_ttl_seconds=0.0,  # Disable cache in tests
        )

        # Execute: should not raise, should return after timeout
        # Use fake clock to control time progression
        async with FakeClockContext() as clock:
            start_time = clock.now()
            # Start the async operation
            task = asyncio.create_task(
                initializer.ensure_session_metrics(
                    sample_session_key, observed_at=sample_observed_at
                )
            )
            # Advance clock to allow timeout to trigger
            clock.advance(0.1)
            # Wait for timeout to complete
            await task
            elapsed = clock.now() - start_time
            # Advance clock further to allow slow_upsert to complete (if it hadn't timed out)
            clock.advance(DEFAULT_TIMEOUT_SECONDS + 0.5)

        # Verify: returned quickly (within timeout + small buffer)
        assert elapsed < DEFAULT_TIMEOUT_SECONDS
        # Verify: upsert was called (but timed out)
        mock_repo.upsert.assert_awaited_once()


class TestDatabaseUnavailable:
    """Test behavior when database is unavailable."""

    @pytest.mark.asyncio
    async def test_database_error_logs_but_doesnt_raise(
        self,
        initializer: SessionMetricsInitializer,
        mock_session_repository: SessionMetricsRepository,
        sample_session_key: SessionKey,
        sample_observed_at: datetime,
    ):
        """Test that database errors are logged but don't raise."""
        # Setup: mock database error
        db_error = Exception("Database connection failed")
        mock_repo = cast(Any, mock_session_repository)
        mock_repo.upsert = AsyncMock(side_effect=db_error)

        # Execute: should not raise
        await initializer.ensure_session_metrics(
            sample_session_key, observed_at=sample_observed_at
        )

        # Verify: upsert was called
        mock_repo.upsert.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_database_timeout_logs_but_doesnt_raise(
        self,
        mock_session_repository: SessionMetricsRepository,
        sample_session_key: SessionKey,
        sample_observed_at: datetime,
    ):
        """Test that database timeout is logged but doesn't raise."""
        from tests.utils.fake_clock import FakeClockContext

        # Setup: mock slow upsert that exceeds timeout

        async def slow_upsert(metrics: SessionMetricsTable) -> SessionMetricsTable:
            # Use fake clock for deterministic time simulation
            async with FakeClockContext() as clock:
                sleep_task = asyncio.create_task(asyncio.sleep(0.2))
                clock.advance(0.2)
                await sleep_task
            return metrics

        mock_repo = cast(Any, mock_session_repository)
        mock_repo.upsert = AsyncMock(side_effect=slow_upsert)

        # Create initializer with very short timeout
        initializer = SessionMetricsInitializer(
            session_repository=mock_session_repository,
            timeout_seconds=0.05,
            cache_ttl_seconds=0.0,  # Disable cache in tests
        )

        # Execute: should not raise
        # Use fake clock to control time progression for timeout test
        async with FakeClockContext() as clock:
            # Start the async operation
            task = asyncio.create_task(
                initializer.ensure_session_metrics(
                    sample_session_key, observed_at=sample_observed_at
                )
            )
            # Advance clock to allow timeout to trigger
            clock.advance(0.05)
            # Wait for timeout to complete
            await task
            # Advance clock further to allow slow_upsert to complete (if it hadn't timed out)
            clock.advance(0.2)

        # Verify: upsert was called
        mock_repo.upsert.assert_awaited_once()


class TestConcurrentInitialization:
    """Test concurrent initialization behavior."""

    @pytest.mark.asyncio
    async def test_concurrent_initialization_handled_atomically(
        self,
        initializer: SessionMetricsInitializer,
        mock_session_repository: SessionMetricsRepository,
        sample_session_key: SessionKey,
        sample_observed_at: datetime,
    ):
        """Test that concurrent initialization is handled atomically."""
        # Setup: mock successful upsert
        mock_metrics = SessionMetricsTable(
            session_id="test-session-123",
            start_time=sample_observed_at,
            last_activity=sample_observed_at,
            turn_count=0,
            total_tokens=0,
            total_tool_calls=0,
            is_completed=False,
        )
        mock_repo = cast(Any, mock_session_repository)
        mock_repo.upsert = AsyncMock(return_value=mock_metrics)

        # Execute: multiple concurrent calls
        await asyncio.gather(
            *[
                initializer.ensure_session_metrics(
                    sample_session_key, observed_at=sample_observed_at
                )
                for _ in range(5)
            ]
        )

        # Verify: all calls completed
        # With cache disabled (cache_ttl_seconds=0), all 5 calls should reach the database
        # With cache enabled, only 1 call would reach the database (others hit cache)
        assert mock_repo.upsert.await_count == 5


class TestSessionKeyMapping:
    """Test session key to session_id mapping."""

    @pytest.mark.asyncio
    async def test_primary_id_maps_to_session_id(
        self,
        initializer: SessionMetricsInitializer,
        mock_session_repository: SessionMetricsRepository,
        sample_observed_at: datetime,
    ):
        """Test that SessionKey.primary_id maps to session_metrics.session_id."""
        # Setup: different session keys
        http_key = SessionKey(
            protocol="http",
            primary_id="trace-abc123",
            group_id="conversation-xyz",
        )
        ws_key = SessionKey(
            protocol="ws",
            primary_id="ws:conn-456",
            group_id=None,
        )

        mock_metrics = SessionMetricsTable(
            session_id="",
            start_time=sample_observed_at,
            last_activity=sample_observed_at,
            turn_count=0,
            total_tokens=0,
            total_tool_calls=0,
            is_completed=False,
        )
        mock_repo = cast(Any, mock_session_repository)
        mock_repo.upsert = AsyncMock(return_value=mock_metrics)

        # Execute: HTTP session
        await initializer.ensure_session_metrics(
            http_key, observed_at=sample_observed_at
        )
        http_call = mock_repo.upsert.call_args_list[0][0][0]
        assert http_call.session_id == "trace-abc123"

        # Execute: WebSocket session
        await initializer.ensure_session_metrics(ws_key, observed_at=sample_observed_at)
        ws_call = mock_repo.upsert.call_args_list[1][0][0]
        assert ws_call.session_id == "ws:conn-456"


class TestCachingBehavior:
    """Test caching behavior to reduce redundant database queries."""

    @pytest.mark.asyncio
    async def test_cache_populated_after_successful_initialization(
        self,
        initializer: SessionMetricsInitializer,
        mock_session_repository: SessionMetricsRepository,
        sample_session_key: SessionKey,
        sample_observed_at: datetime,
    ):
        """Test that cache is populated after successful initialization."""
        # Setup: mock successful upsert
        mock_metrics = SessionMetricsTable(
            session_id="test-session-123",
            start_time=sample_observed_at,
            last_activity=sample_observed_at,
            turn_count=0,
            total_tokens=0,
            total_tool_calls=0,
            is_completed=False,
        )
        mock_repo = cast(Any, mock_session_repository)
        mock_repo.upsert = AsyncMock(return_value=mock_metrics)

        # Create initializer with caching enabled (but disabled in fixture, so enable it)
        initializer_with_cache = SessionMetricsInitializer(
            session_repository=mock_session_repository,
            timeout_seconds=DEFAULT_TIMEOUT_SECONDS,
            cache_ttl_seconds=5.0,
        )

        # Verify cache is initially empty
        assert (
            sample_session_key.primary_id
            not in initializer_with_cache._initialization_cache
        )

        # First call: should populate cache
        await initializer_with_cache.ensure_session_metrics(
            sample_session_key, observed_at=sample_observed_at
        )

        # Verify cache was populated after successful call
        assert (
            sample_session_key.primary_id
            in initializer_with_cache._initialization_cache
        )
        cached_time, cached_lock = initializer_with_cache._initialization_cache[
            sample_session_key.primary_id
        ]
        assert isinstance(cached_time, float)
        assert isinstance(cached_lock, asyncio.Lock)

    @pytest.mark.asyncio
    async def test_cache_disabled_when_ttl_is_zero(
        self,
        mock_session_repository: SessionMetricsRepository,
        sample_session_key: SessionKey,
        sample_observed_at: datetime,
    ):
        """Test that cache is effectively disabled when TTL is 0."""
        # Setup: mock successful upsert
        mock_metrics = SessionMetricsTable(
            session_id="test-session-123",
            start_time=sample_observed_at,
            last_activity=sample_observed_at,
            turn_count=0,
            total_tokens=0,
            total_tool_calls=0,
            is_completed=False,
        )
        mock_repo = cast(Any, mock_session_repository)
        mock_repo.upsert = AsyncMock(return_value=mock_metrics)

        # Create initializer with cache disabled (TTL = 0)
        initializer = SessionMetricsInitializer(
            session_repository=mock_session_repository,
            timeout_seconds=DEFAULT_TIMEOUT_SECONDS,
            cache_ttl_seconds=0.0,
        )

        # Use FakeClockContext to control time safely
        # Create clock with initial time to avoid "set time backwards" error
        initial_clock = FakeClock(initial_time=1000.0)
        async with FakeClockContext(clock=initial_clock) as clock:
            # First call
            await initializer.ensure_session_metrics(
                sample_session_key, observed_at=sample_observed_at
            )

            assert mock_repo.upsert.await_count == 1

            # Advance time slightly
            clock.advance(0.1)

            # Second call immediately after: should hit database (cache disabled)
            await initializer.ensure_session_metrics(
                sample_session_key, observed_at=sample_observed_at
            )

            # Verify: 2 database calls (cache disabled)
            assert mock_repo.upsert.await_count == 2
