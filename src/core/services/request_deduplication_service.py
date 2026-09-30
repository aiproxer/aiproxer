"""Request deduplication service implementation.

This module provides a thread-safe request deduplication service that prevents
duplicate requests from being sent to backends within a configurable time window.

The service tracks request completion status to distinguish between:
- Zombie retries (after success or client disconnect) → BLOCKED
- Legitimate retries (after 429, 503, timeouts) → ALLOWED
- Parallel duplicates (while request is in-flight) → BLOCKED
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import logging
import time
from dataclasses import dataclass
from enum import Enum
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from src.core.domain.chat import ChatRequest

from src.core.interfaces.request_deduplication_interface import DeduplicationStats

logger = logging.getLogger(__name__)


class RequestStatus(str, Enum):
    """Status of a tracked request for deduplication."""

    IN_FLIGHT = "in_flight"  # Request currently processing
    SUCCESS = "success"  # Completed successfully (200, 201)
    RETRIABLE_ERROR = "retriable_error"  # Failed with retry-able error (429, 503, etc)
    CLIENT_DISCONNECT = "client_disconnect"  # Client disconnected before completion


@dataclass
class TrackedRequest:
    """Metadata for a tracked request."""

    timestamp: float
    status: RequestStatus
    status_code: int | None = None
    duplicate_count: int = 0
    is_streaming: bool = False
    expires_at: float = 0.0


class RequestDeduplicationService:
    """Thread-safe request deduplication with status-aware tracking.

    This service detects and blocks duplicate requests within a configurable
    time window while allowing legitimate retries after errors.

    Key behaviors:
    - Blocks zombie retries after successful completion
    - Blocks parallel duplicate requests (same request in-flight)
    - ALWAYS allows retries after 429/503/timeout (regardless of timing)
    - Blocks retries after client disconnect (zombie pattern)

    Attributes:
        _window_seconds: Time window for duplicate detection
        _enabled: Whether deduplication is enabled
        _max_cache_size: Maximum cache entries before forced cleanup
        _cache: Thread-safe cache mapping cache_key -> TrackedRequest
        _lock: Async lock for thread-safe cache access
    """

    _CLEANUP_INTERVAL_SECONDS = 30.0

    def __init__(
        self,
        window_seconds: float = 6.0,
        streaming_window_seconds: float | None = None,
        streaming_in_flight_window_seconds: float = 600.0,
        enabled: bool = True,
        max_cache_size: int = 10000,
    ) -> None:
        """Initialize the deduplication service.

        Args:
            window_seconds: Time window in seconds for duplicate detection.
                           Set to 0 or negative to disable.
            streaming_window_seconds: Optional explicit window for completed streaming requests.
            streaming_in_flight_window_seconds: Max window for in-flight streaming requests.
            enabled: Whether deduplication is enabled
            max_cache_size: Maximum cache entries before forced cleanup
        """
        self._window_seconds = window_seconds
        self._streaming_window_seconds = streaming_window_seconds
        self._streaming_in_flight_window_seconds = streaming_in_flight_window_seconds
        self._enabled = enabled
        self._max_cache_size = max_cache_size

        self._cache: dict[str, TrackedRequest] = {}
        self._lock = asyncio.Lock()

        self._duplicates_blocked = 0
        self._requests_processed = 0
        self._retries_after_error_allowed = 0
        self._last_cleanup_time = time.time()

    def _is_streaming_request(self, request: ChatRequest) -> bool:
        return bool(getattr(request, "stream", False))

    def _completed_ttl_seconds(
        self, *, is_streaming: bool, status_code: int | None
    ) -> float:
        ttl = self._window_seconds
        if is_streaming and self._streaming_window_seconds is not None:
            ttl = max(ttl, self._streaming_window_seconds)

        return max(0.0, ttl)

    def _in_flight_ttl_seconds(self, *, is_streaming: bool) -> float:
        ttl = self._window_seconds
        if is_streaming:
            ttl = max(ttl, self._streaming_in_flight_window_seconds)
        return max(0.0, ttl)

    def _compute_content_hash(self, request: ChatRequest, session_id: str) -> str:
        """Compute deterministic hash of request content.

        Args:
            request: The chat request
            session_id: The session identifier

        Returns:
            A 32-character hex hash of the request content
        """
        try:
            messages_data: list[dict[str, Any]] = []
            if request.messages:
                for msg in request.messages:
                    if hasattr(msg, "model_dump"):
                        messages_data.append(msg.model_dump())
                    elif isinstance(msg, dict):
                        messages_data.append(msg)
                    else:
                        messages_data.append({"content": str(msg)})

            content: dict[str, Any] = {
                "session_id": session_id,
                "model": request.model,
                "messages": messages_data,
            }

            if hasattr(request, "tools") and request.tools:
                content["tools"] = request.tools

            # Native /v1/responses turns stash the real wire body under
            # extra_body[responses_native_projected_payload] while messages stay
            # a placeholder ("."). Without hashing that payload, function_call
            # follow-ups collide with the prior turn and are swallowed.
            extra_body = getattr(request, "extra_body", None)
            if isinstance(extra_body, dict) and extra_body:
                native_payload = extra_body.get(
                    "responses_native_projected_payload"
                )
                if native_payload is not None:
                    content["responses_native_projected_payload"] = native_payload
                # Include a few other identity-affecting extra_body keys when present.
                for key in (
                    "acp_responses_standalone_mode",
                    "acp_responses_text_only_mode",
                ):
                    if key in extra_body:
                        content[key] = extra_body[key]

            serialized = json.dumps(content, sort_keys=True, default=str)
            return hashlib.sha256(serialized.encode()).hexdigest()[:32]
        except Exception as e:
            logger.warning("Failed to compute content hash: %s", e, exc_info=True)
            return hashlib.sha256(str(time.time()).encode()).hexdigest()[:32]

    async def check_and_register(
        self, request: ChatRequest, session_id: str
    ) -> tuple[bool, str, float | None]:
        """Check if request is a duplicate and register if not.

        This method implements status-aware deduplication:
        - Blocks duplicates while original is in-flight or succeeded
        - ALWAYS allows retries after retriable errors (429, 503, etc)
        - Blocks retries after client disconnect (zombie pattern)

        Args:
            request: The chat request to check
            session_id: The session identifier

        Returns:
            Tuple of (is_duplicate, content_hash, retry_after_seconds)
        """
        if not self._enabled or self._window_seconds <= 0:
            return (False, "", None)

        content_hash = self._compute_content_hash(request, session_id)
        cache_key = f"{session_id}:{content_hash}"
        current_time = time.time()
        is_streaming = self._is_streaming_request(request)

        async with self._lock:
            await self._maybe_cleanup_locked(current_time)

            self._requests_processed += 1

            if cache_key in self._cache:
                tracked = self._cache[cache_key]
                age = current_time - tracked.timestamp

                # Expired entries are treated as new requests.
                if tracked.expires_at and current_time >= tracked.expires_at:
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Request tracking expired (age=%.2fs); treating as new request: hash=%s session=%s",
                            age,
                            content_hash[:8],
                            session_id,
                        )
                else:
                    # CRITICAL: Always allow retries after retriable errors (429, 503, etc)
                    # regardless of how recent they are. This ensures legitimate retries
                    # after rate limits are never blocked.
                    if tracked.status == RequestStatus.RETRIABLE_ERROR:
                        self._retries_after_error_allowed += 1
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                "Allowing retry after %s (status_code=%s): hash=%s session=%s age=%.2fs",
                                tracked.status.value,
                                tracked.status_code,
                                content_hash[:8],
                                session_id,
                                age,
                            )
                        # Register as new in-flight request
                        ttl = self._in_flight_ttl_seconds(is_streaming=is_streaming)
                        self._cache[cache_key] = TrackedRequest(
                            timestamp=current_time,
                            status=RequestStatus.IN_FLIGHT,
                            status_code=None,
                            duplicate_count=0,
                            is_streaming=is_streaming,
                            expires_at=current_time + ttl,
                        )
                        return (False, content_hash, None)

                    # Still within per-entry TTL and status is blockable
                    if tracked.status in (
                        RequestStatus.IN_FLIGHT,
                        RequestStatus.SUCCESS,
                        RequestStatus.CLIENT_DISCONNECT,
                    ):
                        self._duplicates_blocked += 1
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                "Duplicate blocked (status=%s): hash=%s session=%s age=%.2fs",
                                tracked.status.value,
                                content_hash[:8],
                                session_id,
                                age,
                            )
                        tracked.duplicate_count += 1
                        effective_window = max(
                            0.0, (tracked.expires_at - tracked.timestamp)
                        )
                        retry_after_seconds = self._compute_retry_after_seconds(
                            age=age,
                            effective_window=effective_window,
                            duplicate_count=tracked.duplicate_count,
                        )
                        return (True, content_hash, retry_after_seconds)

            # New request or expired - register as in-flight
            ttl = self._in_flight_ttl_seconds(is_streaming=is_streaming)
            self._cache[cache_key] = TrackedRequest(
                timestamp=current_time,
                status=RequestStatus.IN_FLIGHT,
                status_code=None,
                duplicate_count=0,
                is_streaming=is_streaming,
                expires_at=current_time + ttl,
            )
            return (False, content_hash, None)

    def _compute_retry_after_seconds(
        self, *, age: float, effective_window: float, duplicate_count: int
    ) -> float | None:
        if effective_window <= 0:
            return None

        remaining = max(0.0, effective_window - age)
        capped_duplicates = min(duplicate_count, 6)
        backoff_seconds = 2.0**capped_duplicates
        backoff_seconds = max(1.0, min(effective_window, backoff_seconds))
        return max(remaining, backoff_seconds)

    async def mark_request_complete(
        self,
        content_hash: str,
        session_id: str,
        status_code: int | None = None,
        client_disconnected: bool = False,
    ) -> None:
        """Mark a request as complete with its final status.

        This updates the tracked request status to enable smart duplicate detection.

        Args:
            content_hash: The request content hash
            session_id: The session identifier
            status_code: HTTP status code (200, 429, 503, etc) or None
            client_disconnected: Whether client disconnected before completion
        """
        if not self._enabled:
            return

        cache_key = f"{session_id}:{content_hash}"

        async with self._lock:
            if cache_key not in self._cache:
                # Request not tracked (disabled or bypassed)
                return

            tracked = self._cache[cache_key]
            now = time.time()

            # Determine final status
            if client_disconnected:
                new_status = RequestStatus.CLIENT_DISCONNECT
            elif status_code is not None:
                if status_code in (200, 201, 202, 204):
                    new_status = RequestStatus.SUCCESS
                elif status_code in (429, 500, 502, 503, 504, 408):
                    # Retriable errors: rate limit, internal server error, service unavailable, timeouts
                    new_status = RequestStatus.RETRIABLE_ERROR
                else:
                    # Non-retriable errors (400, 401, 403, 404, 500, etc)
                    # Treat as success to block duplicates
                    new_status = RequestStatus.SUCCESS
            else:
                # No status code provided - assume success
                new_status = RequestStatus.SUCCESS

            # Update tracked request
            tracked.status = new_status
            tracked.status_code = status_code
            # Reset timestamp to completion time so dedup window measures from completion,
            # not from request start (important for long streaming responses).
            tracked.timestamp = now
            tracked.expires_at = now + self._completed_ttl_seconds(
                is_streaming=tracked.is_streaming, status_code=status_code
            )

            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Request completed: hash=%s session=%s status=%s code=%s",
                    content_hash[:8],
                    session_id,
                    new_status.value,
                    status_code,
                )

    async def _maybe_cleanup_locked(self, current_time: float) -> None:
        """Cleanup expired entries (called within lock).

        Args:
            current_time: Current monotonic time
        """
        # Optimize: Only cleanup if interval passed OR cache is significantly over limit (10% buffer)
        # This prevents O(N log N) sorting on every request when cache is full
        size_limit_with_buffer = self._max_cache_size * 1.1
        should_cleanup = (
            current_time - self._last_cleanup_time > self._CLEANUP_INTERVAL_SECONDS
            or len(self._cache) > size_limit_with_buffer
        )

        if not should_cleanup:
            return

        self._last_cleanup_time = current_time
        # First pass: remove expired entries (O(N))
        expired_keys = [
            key
            for key, tracked in self._cache.items()
            if tracked.expires_at and tracked.expires_at < current_time
        ]
        for key in expired_keys:
            del self._cache[key]

        # Second pass: if still over strict limit, remove oldest (O(N log N))
        # This runs rarely because we have a 10% buffer
        if len(self._cache) > self._max_cache_size:
            sorted_entries = sorted(self._cache.items(), key=lambda x: x[1].timestamp)
            to_remove = len(self._cache) - self._max_cache_size
            for key, _ in sorted_entries[:to_remove]:
                del self._cache[key]

        if expired_keys and logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Dedup cache cleanup: removed %d expired entries, cache_size=%d",
                len(expired_keys),
                len(self._cache),
            )

    async def cleanup(self) -> int:
        """Force cleanup of expired entries.

        Returns:
            Number of entries removed
        """
        async with self._lock:
            initial_size = len(self._cache)
            now = time.time()
            expired_keys = [
                key
                for key, tracked in self._cache.items()
                if tracked.expires_at and tracked.expires_at < now
            ]
            for key in expired_keys:
                del self._cache[key]

            return initial_size - len(self._cache)

    async def get_request_outcome(
        self, content_hash: str, session_id: str
    ) -> tuple[str, int | None] | None:
        """Return the tracked status and HTTP code for a request, if present."""
        if not self._enabled:
            return None
        cache_key = f"{session_id}:{content_hash}"
        async with self._lock:
            tracked = self._cache.get(cache_key)
            if tracked is None:
                return None
            return tracked.status.value, tracked.status_code

    def get_stats(self) -> DeduplicationStats:
        """Return deduplication statistics (non-blocking read).

        Returns:
            DeduplicationStats object with deduplication statistics
        """
        dedup_rate = (
            self._duplicates_blocked / self._requests_processed
            if self._requests_processed > 0
            else 0.0
        )

        # Extended stats
        extra_stats = {
            "retries_after_error_allowed": self._retries_after_error_allowed,
        }

        return DeduplicationStats(
            enabled=self._enabled,
            window_seconds=self._window_seconds,
            cache_size=len(self._cache),
            duplicates_blocked=self._duplicates_blocked,
            requests_processed=self._requests_processed,
            dedup_rate=dedup_rate,
            extra=extra_stats,
        )
