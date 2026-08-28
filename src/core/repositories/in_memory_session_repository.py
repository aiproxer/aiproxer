from __future__ import annotations

import logging
import time
from contextlib import suppress
from datetime import datetime, timezone

from src.core.domain.session import Session
from src.core.interfaces.repositories_interface import ISessionRepository
from src.core.services.conversation_fingerprint_service import (
    ConversationFingerprintBundle,
)

logger = logging.getLogger(__name__)

# Maximum number of sessions to keep in memory to prevent unbounded growth
# 50,000 sessions provides a large window for active sessions without unbounded growth
_MAX_SESSIONS = 50_000

# Default TTL for sessions: remove if not accessed for 24 hours
# This prevents accumulation of stale sessions when cleanup_expired is never called
_DEFAULT_SESSION_TTL_SECONDS = 24 * 3600

# Maximum number of session IDs to track per user to prevent unbounded growth
# This prevents memory leaks when a single user creates many sessions
_MAX_SESSIONS_PER_USER = 1000

# Maximum number of session IDs to track per client to prevent unbounded growth
# This prevents memory leaks when a single client creates many sessions
_MAX_SESSIONS_PER_CLIENT = 1000


class InMemorySessionRepository(ISessionRepository):
    """In-memory implementation of session repository.

    This repository keeps sessions in memory and does not persist them.
    It is suitable for development and testing.

    Automatically cleans up stale sessions to prevent unbounded memory growth.
    """

    def __init__(
        self,
        max_sessions: int = _MAX_SESSIONS,
        default_ttl_seconds: int = _DEFAULT_SESSION_TTL_SECONDS,
    ) -> None:
        """Initialize the in-memory session repository.

        Args:
            max_sessions: Maximum number of sessions to keep in memory
            default_ttl_seconds: Default TTL in seconds for stale sessions
        """
        self._sessions: dict[str, Session] = {}
        self._user_sessions: dict[str, list[str]] = {}
        self._last_accessed: dict[str, float] = {}
        # Session continuity tracking
        self._fingerprints: dict[str, str] = {}  # session_id -> fingerprint
        self._client_sessions: dict[str, list[str]] = {}  # client_key -> session_ids
        self._fingerprint_bundles: dict[str, ConversationFingerprintBundle] = {}

        # Reverse mappings for efficient deletion (prevents O(N) scans)
        self._session_to_user: dict[str, str] = {}  # session_id -> user_id
        self._session_to_client: dict[str, str] = {}  # session_id -> client_key

        self._max_sessions = max_sessions
        self._default_ttl_seconds = default_ttl_seconds
        self._max_sessions_per_user = _MAX_SESSIONS_PER_USER
        self._max_sessions_per_client = _MAX_SESSIONS_PER_CLIENT

    async def _maybe_cleanup_stale_sessions(self) -> None:
        """Clean up stale sessions based on TTL.

        This prevents unbounded growth when cleanup_expired is never called.
        """
        if len(self._sessions) < self._max_sessions:
            return

        now = time.time()
        expired_sessions = []

        for session_id, last_access in self._last_accessed.items():
            if now - last_access > self._default_ttl_seconds:
                expired_sessions.append(session_id)

        for session_id in expired_sessions:
            await self.delete(session_id)
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Removed stale session: %s (last access: %.1fs ago)",
                    session_id,
                    now - self._last_accessed.get(session_id, 0),
                )

    async def _evict_oldest_session(self) -> None:
        """Evict the oldest session when max limit is reached (LRU eviction).

        This prevents unbounded growth by removing least recently used sessions.
        """
        if not self._last_accessed:
            return

        # Find oldest session by last access time
        oldest_session_id = min(self._last_accessed.items(), key=lambda x: x[1])[0]
        await self.delete(oldest_session_id)

        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Evicted oldest session: %s (max_sessions=%d reached)",
                oldest_session_id,
                self._max_sessions,
            )

    async def get_by_id(self, id: str) -> Session | None:
        """Get a session by its ID."""
        session = self._sessions.get(id)
        if session:
            self._last_accessed[id] = time.time()
        return session

    async def get_all(self) -> list[Session]:
        """Get all sessions."""
        return list(self._sessions.values())

    async def add(self, entity: Session) -> Session:
        """Add a new session."""
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                f"InMemorySessionRepository.add: session_id={entity.id}, history_size={len(entity.history)}"
            )

        # Check if we need to evict old sessions before adding new one
        if entity.id not in self._sessions:
            await self._maybe_cleanup_stale_sessions()
            # Enforce max limit by evicting oldest sessions if needed
            while len(self._sessions) >= self._max_sessions:
                await self._evict_oldest_session()

        self._sessions[entity.id] = entity
        self._last_accessed[entity.id] = time.time()

        # Track by user if available
        if hasattr(entity, "user_id") and entity.user_id:
            user_id = entity.user_id
            self._session_to_user[entity.id] = user_id

            if user_id not in self._user_sessions:
                self._user_sessions[user_id] = []
            user_session_list = self._user_sessions[user_id]
            # Add new session ID
            if entity.id not in user_session_list:
                user_session_list.append(entity.id)
            # Enforce per-user limit to prevent unbounded growth
            if len(user_session_list) > self._max_sessions_per_user:
                # Remove oldest session IDs (FIFO eviction)
                excess_count = len(user_session_list) - self._max_sessions_per_user
                evicted_ids = user_session_list[:excess_count]
                self._user_sessions[user_id] = user_session_list[excess_count:]
                # Note: We don't fully delete these sessions from self._sessions here
                # to maintain global limit logic, but we remove the user mapping.
                for eid in evicted_ids:
                    self._session_to_user.pop(eid, None)

                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Evicted %d oldest session IDs for user %s (max_sessions_per_user=%d reached)",
                        excess_count,
                        user_id,
                        self._max_sessions_per_user,
                    )

        return entity

    async def update(self, entity: Session) -> Session:
        """Update an existing session."""
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                f"InMemorySessionRepository.update: session_id={entity.id}, history_size={len(entity.history)}"
            )
        existing_session = self._sessions.get(entity.id)
        if existing_session is None:
            return await self.add(entity)

        previous_user_id = self._session_to_user.get(entity.id)
        new_user_id = getattr(entity, "user_id", None)

        self._sessions[entity.id] = entity
        self._last_accessed[entity.id] = time.time()

        if previous_user_id and previous_user_id != new_user_id:
            tracked_sessions = self._user_sessions.get(previous_user_id)
            if tracked_sessions and entity.id in tracked_sessions:
                tracked_sessions.remove(entity.id)
            if tracked_sessions is not None and not tracked_sessions:
                del self._user_sessions[previous_user_id]
            self._session_to_user.pop(entity.id, None)

        if new_user_id:
            self._session_to_user[entity.id] = new_user_id
            tracked_sessions = self._user_sessions.setdefault(new_user_id, [])
            if entity.id not in tracked_sessions:
                tracked_sessions.append(entity.id)
            # Enforce per-user limit to prevent unbounded growth
            if len(tracked_sessions) > self._max_sessions_per_user:
                # Remove oldest session IDs (FIFO eviction)
                excess_count = len(tracked_sessions) - self._max_sessions_per_user
                evicted_ids = tracked_sessions[:excess_count]
                self._user_sessions[new_user_id] = tracked_sessions[excess_count:]
                for eid in evicted_ids:
                    self._session_to_user.pop(eid, None)

                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Evicted %d oldest session IDs for user %s (max_sessions_per_user=%d reached)",
                        excess_count,
                        new_user_id,
                        self._max_sessions_per_user,
                    )

        return entity

    async def delete(self, id: str) -> bool:
        """Delete a session by its ID.

        This method is now optimized to use reverse mappings instead of full scans.
        It also ensures all related state is cleaned up even if the session object
        is not in the main _sessions dictionary (orphans).
        """
        deleted = False

        # Remove from main collections
        if id in self._sessions:
            del self._sessions[id]
            deleted = True

        if id in self._last_accessed:
            del self._last_accessed[id]
            deleted = True

        # Efficiently remove from user tracking
        user_id = self._session_to_user.pop(id, None)
        if user_id and user_id in self._user_sessions:
            session_ids = self._user_sessions[user_id]
            with suppress(ValueError):
                session_ids.remove(id)
            if not session_ids:
                del self._user_sessions[user_id]
            deleted = True

        # Efficiently remove from client session tracking
        client_key = self._session_to_client.pop(id, None)
        if client_key and client_key in self._client_sessions:
            session_ids = self._client_sessions[client_key]
            with suppress(ValueError):
                session_ids.remove(id)
            if not session_ids:
                del self._client_sessions[client_key]
            deleted = True

        # Remove from fingerprint tracking
        if id in self._fingerprints:
            del self._fingerprints[id]
            deleted = True
        if id in self._fingerprint_bundles:
            del self._fingerprint_bundles[id]
            deleted = True

        return deleted

    async def get_by_user_id(self, user_id: str) -> list[Session]:
        """Get all sessions for a specific user."""
        session_ids = self._user_sessions.get(user_id, [])
        return [self._sessions[id] for id in session_ids if id in self._sessions]

    async def cleanup_expired(self, max_age_seconds: int) -> int:
        """Clean up expired sessions.

        Args:
            max_age_seconds: Maximum age of sessions to keep in seconds

        Returns:
            The number of sessions deleted
        """
        now = datetime.now(timezone.utc)
        now_timestamp = time.time()
        expired_ids = []

        for session_id, session in self._sessions.items():
            # Use session's last_active_at if available, otherwise fall back to _last_accessed
            if hasattr(session, "last_active_at") and session.last_active_at:
                last_active = session.last_active_at

                if isinstance(last_active, datetime):
                    if (
                        last_active.tzinfo is None
                        or last_active.tzinfo.utcoffset(last_active) is None
                    ):
                        last_active = last_active.replace(tzinfo=timezone.utc)
                    else:
                        last_active = last_active.astimezone(timezone.utc)

                    age = (now - last_active).total_seconds()
                else:
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Session %s has non-datetime last_active_at (%s); falling back to access timestamp",
                            session_id,
                            type(last_active).__name__,
                        )
                    last_access_timestamp = self._last_accessed.get(
                        session_id, now_timestamp
                    )
                    age = now_timestamp - last_access_timestamp
            else:
                # Fall back to internal tracking
                last_access_timestamp = self._last_accessed.get(session_id, 0.0)
                age = now_timestamp - last_access_timestamp

            if age > max_age_seconds:
                expired_ids.append(session_id)

        count = 0
        for session_id in expired_ids:
            if await self.delete(session_id):
                count += 1

        if count > 0 and logger.isEnabledFor(logging.INFO):
            logger.info("Cleaned up %d expired sessions", count)

        return count

    async def update_fingerprint(self, session_id: str, fingerprint: str) -> None:
        """Update the conversation fingerprint for a session.

        Args:
            session_id: Session ID to update
            fingerprint: New fingerprint value
        """
        self._fingerprints[session_id] = fingerprint
        self._last_accessed[session_id] = time.time()

        # Self-cleanup: if we have too many fingerprints compared to sessions,
        # we might have orphans. Limit fingerprints to 2x max sessions.
        if len(self._fingerprints) > self._max_sessions * 2:
            # Evict oldest fingerprints by last access
            orphans = [sid for sid in self._fingerprints if sid not in self._sessions]
            if orphans:
                # Simple heuristic: remove up to 10% of max sessions worth of orphans
                for sid in orphans[: self._max_sessions // 10]:
                    await self.delete(sid)

    async def update_client_session(self, session_id: str, client_key: str) -> None:
        """Track a session as belonging to a specific client.

        Args:
            session_id: Session ID
            client_key: Client identifier (e.g., IP + user-agent hash)
        """
        self._session_to_client[session_id] = client_key

        if client_key not in self._client_sessions:
            self._client_sessions[client_key] = []
        client_session_list = self._client_sessions[client_key]
        if session_id not in client_session_list:
            client_session_list.append(session_id)
        # Enforce per-client limit to prevent unbounded growth
        if len(client_session_list) > self._max_sessions_per_client:
            # Remove oldest session IDs (FIFO eviction)
            excess_count = len(client_session_list) - self._max_sessions_per_client
            evicted_ids = client_session_list[:excess_count]
            self._client_sessions[client_key] = client_session_list[excess_count:]
            for eid in evicted_ids:
                self._session_to_client.pop(eid, None)

            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Evicted %d oldest session IDs for client %s (max_sessions_per_client=%d reached)",
                    excess_count,
                    client_key,
                    self._max_sessions_per_client,
                )

    async def find_by_client_and_fingerprint(
        self, client_key: str, fingerprint: str
    ) -> Session | None:
        """Find a session by client key and conversation fingerprint.

        Args:
            client_key: Client identifier
            fingerprint: Conversation fingerprint to match

        Returns:
            Session if found, None otherwise
        """
        # Get all sessions for this client
        session_ids = self._client_sessions.get(client_key, [])

        # Check each session for matching fingerprint

        for session_id in session_ids:
            if self._fingerprints.get(session_id) == fingerprint:
                session = self._sessions.get(session_id)
                if session:
                    self._last_accessed[session_id] = time.time()
                    return session

        return None

    async def find_recent_sessions_by_client(
        self, client_key: str, max_age_seconds: int
    ) -> list[Session]:
        """Find recent sessions for a client.

        Args:
            client_key: Client identifier
            max_age_seconds: Maximum age in seconds

        Returns:
            List of recent sessions, ordered by most recent first
        """
        session_ids = self._client_sessions.get(client_key, [])
        now = time.time()

        recent_sessions = []
        for session_id in session_ids:
            last_access = self._last_accessed.get(session_id, 0)
            age = now - last_access

            if age <= max_age_seconds:
                session = self._sessions.get(session_id)
                if session:
                    recent_sessions.append((last_access, session))

        # Sort by last access time (most recent first)
        recent_sessions.sort(key=lambda x: x[0], reverse=True)

        return [session for _, session in recent_sessions]

    async def get_session_fingerprint(self, session_id: str) -> str | None:
        """Get the fingerprint for a session.

        Args:
            session_id: Session ID

        Returns:
            Fingerprint if found, None otherwise
        """
        return self._fingerprints.get(session_id)

    async def update_fingerprint_bundle(
        self, session_id: str, bundle: ConversationFingerprintBundle
    ) -> None:
        """Store extended fingerprint metadata."""
        self._fingerprint_bundles[session_id] = bundle
        self._last_accessed[session_id] = time.time()

        # Self-cleanup: if we have too many bundles compared to sessions,
        # we might have orphans. Limit bundles to 2x max sessions.
        if len(self._fingerprint_bundles) > self._max_sessions * 2:
            orphans = [
                sid for sid in self._fingerprint_bundles if sid not in self._sessions
            ]
            if orphans:
                for sid in orphans[: self._max_sessions // 10]:
                    await self.delete(sid)

    async def get_fingerprint_bundle(
        self, session_id: str
    ) -> ConversationFingerprintBundle | None:
        """Retrieve stored fingerprint metadata."""
        return self._fingerprint_bundles.get(session_id)

    async def get_session_last_access(self, session_id: str) -> float | None:
        """Return the last access timestamp for the session."""
        return self._last_accessed.get(session_id)
