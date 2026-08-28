"""
Unit tests for RedactionCacheEosSubscriber.
"""

from __future__ import annotations

import pytest
from src.core.domain.events.end_of_session_events import (
    EndOfSessionSignalType,
    RemoteBackendConnectionEndOfSessionEvent,
)
from src.core.services.event_bus import EventBus
from src.core.services.redaction_cache import RedactionCache
from src.core.services.redaction_cache_eos_subscriber import RedactionCacheEosSubscriber


class TestRedactionCacheEosSubscriber:
    """Tests for RedactionCacheEosSubscriber."""

    @pytest.fixture
    def event_bus(self) -> EventBus:
        return EventBus()

    @pytest.fixture
    def redaction_cache(self) -> RedactionCache:
        return RedactionCache()

    @pytest.fixture
    def subscriber(
        self, event_bus: EventBus, redaction_cache: RedactionCache
    ) -> RedactionCacheEosSubscriber:
        return RedactionCacheEosSubscriber(event_bus, redaction_cache)

    @pytest.mark.asyncio
    async def test_session_cleared_on_eos(
        self,
        subscriber: RedactionCacheEosSubscriber,
        event_bus: EventBus,
        redaction_cache: RedactionCache,
    ) -> None:
        """Test that the redaction cache for a session is cleared when an EoS event is received."""
        # Populate the cache
        session_id = "test-session-123"
        redaction_cache.mark_processed(session_id, "test message")
        assert redaction_cache.is_processed(session_id, "test message") is True

        # Start the subscriber
        await subscriber.start()

        try:
            # Publish EoS event
            event = RemoteBackendConnectionEndOfSessionEvent(
                session_id=session_id,
                backend="test-backend",
                signal_type=EndOfSessionSignalType.DONE_SENTINEL,
                reason="test-reason",
            )

            # Use event bus to publish and await completion
            await event_bus.publish(event)

            # The cache should be cleared for this session
            assert redaction_cache.is_processed(session_id, "test message") is False

            # Verify internal state is cleaned up
            assert session_id not in redaction_cache._states
        finally:
            await subscriber.stop()

    @pytest.mark.asyncio
    async def test_other_sessions_not_affected(
        self,
        subscriber: RedactionCacheEosSubscriber,
        event_bus: EventBus,
        redaction_cache: RedactionCache,
    ) -> None:
        """Test that clearing one session doesn't affect others."""
        session_id_1 = "test-session-1"
        session_id_2 = "test-session-2"

        redaction_cache.mark_processed(session_id_1, "msg1")
        redaction_cache.mark_processed(session_id_2, "msg2")

        await subscriber.start()

        try:
            # EoS for session 1
            event = RemoteBackendConnectionEndOfSessionEvent(
                session_id=session_id_1,
                backend="test-backend",
                signal_type=EndOfSessionSignalType.DONE_SENTINEL,
                reason="test-reason",
            )
            await event_bus.publish(event)

            # Session 1 cleared, Session 2 intact
            assert redaction_cache.is_processed(session_id_1, "msg1") is False
            assert redaction_cache.is_processed(session_id_2, "msg2") is True
        finally:
            await subscriber.stop()

    @pytest.mark.asyncio
    async def test_unsubscribed_after_stop(
        self,
        subscriber: RedactionCacheEosSubscriber,
        event_bus: EventBus,
        redaction_cache: RedactionCache,
    ) -> None:
        """Test that events are ignored after stopping."""
        session_id = "test-session-stop"
        redaction_cache.mark_processed(session_id, "test")

        await subscriber.start()
        await subscriber.stop()

        # Publish EoS event after stop
        event = RemoteBackendConnectionEndOfSessionEvent(
            session_id=session_id,
            backend="test-backend",
            signal_type=EndOfSessionSignalType.DONE_SENTINEL,
            reason="test-reason",
        )
        await event_bus.publish(event)

        # Cache should not be cleared
        assert redaction_cache.is_processed(session_id, "test") is True
