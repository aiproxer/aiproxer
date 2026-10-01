"""Session metrics initializer service implementation.

This service ensures session_metrics records exist early in the lifecycle
before backend work begins, with best-effort behavior and strict timeout
enforcement.
"""

from __future__ import annotations

import asyncio
import logging
import time
from datetime import datetime

from src.core.database.models.usage import SessionMetricsTable
from src.core.database.repositories.usage_repository import SessionMetricsRepository
from src.core.domain.session_key import SessionKey
from src.core.interfaces.session_metrics_initializer_interface import (
    ISessionMetricsInitializer,
)

logger = logging.getLogger(__name__)

# Default timeout for metrics initialization (2.0 seconds)
# This prevents blocking cancellation/EoS handling under DB slowness
DEFAULT_TIMEOUT_SECONDS = 2.0

# Cache TTL for recently initialized sessions (5 seconds)
# This avoids redundant database queries for concurrent initialization attempts
CACHE_TTL_SECONDS = 5.0


class SessionMetricsInitializer(ISessionMetricsInitializer):
    """Service for ensuring session metrics exist before EoS emission.

    This service performs best-effort upsert operations with strict timeout
    enforcement to ensure session_metrics records exist without blocking
    cancellation/EoS handling.
    """

    def __init__(
        self,
        session_repository: SessionMetricsRepository,
        timeout_seconds: float = DEFAULT_TIMEOUT_SECONDS,
        cache_ttl_seconds: float = CACHE_TTL_SECONDS,
    ) -> None:
        """Initialize the session metrics initializer.

        Args:
            session_repository: Repository for session metrics persistence
            timeout_seconds: Maximum time to wait for persistence operations
            cache_ttl_seconds: TTL for in-memory cache to avoid redundant queries
        """
        self._session_repository = session_repository
        self._timeout_seconds = timeout_seconds
        self._cache_ttl_seconds = cache_ttl_seconds
        # Cache: session_id -> (successful initialization timestamp, lock).
        # None means the lock exists but persistence has not succeeded yet.
        # Lock prevents concurrent initialization of the same session
        self._initialization_cache: dict[str, tuple[float | None, asyncio.Lock]] = {}
        self._cache_lock = asyncio.Lock()

    async def ensure_session_metrics(
        self, session_key: SessionKey, *, observed_at: datetime
    ) -> None:
        """Ensure session metrics record exists for the given session.

        This method performs a best-effort upsert operation with strict timeout
        enforcement. If persistence is unavailable or times out, the method logs
        the failure and returns without raising.

        Uses in-memory caching to avoid redundant database queries for recently
        initialized sessions.

        Args:
            session_key: Transport-agnostic session identity
            observed_at: Timestamp when the session was observed
        """
        session_id = session_key.primary_id

        # Check cache first to avoid redundant database queries
        async with self._cache_lock:
            if session_id in self._initialization_cache:
                cached_time, lock = self._initialization_cache[session_id]
                # Check if cache entry is still valid
                if (
                    cached_time is not None
                    and self._cache_ttl_seconds > 0
                    and time.time() - cached_time < self._cache_ttl_seconds
                ):
                    # Cache hit - skip database query
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Session metrics cache hit for session %s (skipping DB query)",
                            session_id,
                            extra={
                                "session_id": session_id,
                                "protocol": session_key.protocol,
                                "group_id": session_key.group_id,
                            },
                        )
                    return
                elif cached_time is not None:
                    # Cache expired - remove entry
                    del self._initialization_cache[session_id]

            # Create lock for this session to prevent concurrent initialization
            if session_id not in self._initialization_cache:
                self._initialization_cache[session_id] = (None, asyncio.Lock())
            _, session_lock = self._initialization_cache[session_id]

        # Acquire session-specific lock to prevent concurrent initialization
        async with session_lock:
            # Double-check cache after acquiring lock (another coroutine might have initialized)
            async with self._cache_lock:
                if session_id in self._initialization_cache:
                    cached_time, _ = self._initialization_cache[session_id]
                    if (
                        cached_time is not None
                        and self._cache_ttl_seconds > 0
                        and time.time() - cached_time < self._cache_ttl_seconds
                    ):
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                "Session metrics already initialized (cache hit after lock) for session %s",
                                session_id,
                            )
                        return

            # Create minimal session metrics record
            metrics = SessionMetricsTable(
                session_id=session_id,
                start_time=observed_at,
                last_activity=observed_at,
                turn_count=0,
                total_tokens=0,
                total_tool_calls=0,
                is_completed=False,
            )

            try:
                # Wrap upsert in timeout to prevent blocking
                await asyncio.wait_for(
                    self._session_repository.upsert(metrics),
                    timeout=self._timeout_seconds,
                )

                # Update cache on success
                async with self._cache_lock:
                    self._initialization_cache[session_id] = (time.time(), session_lock)

                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Session metrics initialized for session %s",
                        session_id,
                        extra={
                            "session_id": session_id,
                            "protocol": session_key.protocol,
                            "group_id": session_key.group_id,
                        },
                    )

            except asyncio.TimeoutError:
                # Timeout: log high-visibility error but don't raise
                logger.error(
                    "Session metrics initialization timeout (%.1fs) for session %s, "
                    "persistence unavailable - proceeding without metrics",
                    self._timeout_seconds,
                    session_id,
                    exc_info=True,
                    extra={
                        "session_id": session_id,
                        "protocol": session_key.protocol,
                        "group_id": session_key.group_id,
                        "timeout_seconds": self._timeout_seconds,
                        "error_code": "SESSION_METRICS_INIT_TIMEOUT",
                    },
                )

            except Exception as e:
                # Any other persistence error: log but don't raise
                logger.error(
                    "Session metrics initialization failed for session %s: %s, "
                    "persistence unavailable - proceeding without metrics",
                    session_id,
                    e,
                    exc_info=True,
                    extra={
                        "session_id": session_id,
                        "protocol": session_key.protocol,
                        "group_id": session_key.group_id,
                        "error_code": "SESSION_METRICS_INIT_FAILED",
                    },
                )
