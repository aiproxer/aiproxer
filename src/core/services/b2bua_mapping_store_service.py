"""B2BUA continuity mapping stores (in-memory and persistent)."""

from __future__ import annotations

import asyncio
import logging
import sqlite3
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Final

from src.core.interfaces.b2bua_mapping_store_interface import (
    B2buaAttemptRecord,
    B2buaContinuityResolution,
    IB2buaMappingStore,
)
from src.core.interfaces.configuration_interface import IConfig

logger = logging.getLogger(__name__)

_DEFAULT_MAX_MAPPINGS: Final[int] = 50_000
_DEFAULT_CONTINUITY_TTL_SECONDS: Final[int] = 3600
_DEFAULT_PERSISTENT_DB_PATH: Final[Path] = Path("var/state/b2bua_continuity.sqlite3")
_SQLITE_BUSY_TIMEOUT_SECONDS: Final[float] = 30.0

# Must stay aligned with ``b2bua_session_resolver_service`` anonymous bootstrap prefix.
_ANON_AUTH_SCOPE_PREFIX: Final[str] = "__b2bua-anon-auth__"
_LOCALHOST_AUTH_SCOPE: Final[str] = "localhost"


def _echo_continuity_auth_allows(
    *,
    stored_auth_scope_id: str,
    requesting_auth_scope_id: str | None,
) -> bool:
    """Whether an echoed A-leg id may be reused under the current auth scope."""
    if requesting_auth_scope_id == stored_auth_scope_id:
        return True
    if stored_auth_scope_id.startswith(_ANON_AUTH_SCOPE_PREFIX):
        if requesting_auth_scope_id is None:
            return True
        if requesting_auth_scope_id == _LOCALHOST_AUTH_SCOPE:
            return True
    return False


def _read_config_ttl_seconds(config: IConfig | None) -> int:
    if config is None:
        return _DEFAULT_CONTINUITY_TTL_SECONDS

    try:
        session_cfg = getattr(config, "session", None)
        b2bua_cfg = getattr(session_cfg, "b2bua", None)
        configured = getattr(b2bua_cfg, "continuity_max_age_seconds", None)
        if isinstance(configured, int) and configured >= 1:
            return configured
    except (AttributeError, TypeError):
        return _DEFAULT_CONTINUITY_TTL_SECONDS

    return _DEFAULT_CONTINUITY_TTL_SECONDS


def _read_config_sliding_expiration(config: IConfig | None) -> bool:
    if config is None:
        return True

    try:
        session_cfg = getattr(config, "session", None)
        b2bua_cfg = getattr(session_cfg, "b2bua", None)
        configured = getattr(b2bua_cfg, "continuity_sliding_expiration", None)
        if isinstance(configured, bool):
            return configured
    except (AttributeError, TypeError):
        return True

    return True


def _normalize_required_identifier(value: str, field_name: str) -> str:
    normalized = value.strip()
    if not normalized:
        raise ValueError(f"{field_name} cannot be empty")
    return normalized


@dataclass
class _ContinuityEntry:
    a_session_id: str
    expires_at: float
    last_accessed_at: float
    last_b_seq: int = 0


class InMemoryB2buaMappingStore(IB2buaMappingStore):
    """In-memory continuity mapping store with TTL and bounded growth."""

    def __init__(
        self,
        config: IConfig | None = None,
        *,
        continuity_ttl_seconds: int | None = None,
        sliding_expiration: bool | None = None,
        max_entries: int = _DEFAULT_MAX_MAPPINGS,
        time_provider: Callable[[], float] | None = None,
    ) -> None:
        if max_entries < 1:
            raise ValueError("max_entries must be >= 1")

        self._time_provider = time_provider if time_provider is not None else time.time
        self._lock = asyncio.Lock()
        self._entries: dict[tuple[str, str], _ContinuityEntry] = {}
        self._a_session_to_key: dict[str, tuple[str, str]] = {}
        self._attempt_records: dict[str, list[B2buaAttemptRecord]] = {}
        self._max_entries = max_entries

        config_ttl = _read_config_ttl_seconds(config)
        config_sliding = _read_config_sliding_expiration(config)
        self._continuity_ttl_seconds = (
            continuity_ttl_seconds if continuity_ttl_seconds is not None else config_ttl
        )
        self._sliding_expiration = (
            sliding_expiration if sliding_expiration is not None else config_sliding
        )

    async def resolve_or_create_a_session_id(
        self,
        *,
        auth_scope_id: str,
        client_session_id: str,
        create_a_session_id: Callable[[], str],
    ) -> B2buaContinuityResolution:
        """Resolve existing active mapping or create a new one.

        Fails open by returning a fresh A-leg id when an internal store error occurs.
        """
        try:
            return await self._resolve_or_create_core(
                auth_scope_id,
                client_session_id,
                create_a_session_id,
            )
        except Exception as exc:  # - must fail open on store faults
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "B2BUA continuity mapping store failure; failing open with new A-leg id",
                    exc_info=exc,
                )
            return B2buaContinuityResolution(
                a_session_id=create_a_session_id(),
                reused_existing=False,
                had_store_error=True,
            )

    async def try_resolve_echoed_a_session_id(
        self,
        *,
        a_session_id: str,
        requesting_auth_scope_id: str | None,
    ) -> B2buaContinuityResolution | None:
        try:
            normalized_a = _normalize_required_identifier(
                a_session_id,
                "a_session_id",
            )
        except ValueError:
            return None

        now = self._time_provider()
        async with self._lock:
            self._cleanup_expired_entries(now)
            key = self._a_session_to_key.get(normalized_a)
            if key is None:
                return None
            entry = self._entries.get(key)
            if entry is None or entry.expires_at <= now:
                return None
            stored_auth, _stored_client = key
            if not _echo_continuity_auth_allows(
                stored_auth_scope_id=stored_auth,
                requesting_auth_scope_id=requesting_auth_scope_id,
            ):
                return None
            entry.last_accessed_at = now
            if self._sliding_expiration:
                entry.expires_at = now + self._continuity_ttl_seconds
            return B2buaContinuityResolution(
                a_session_id=entry.a_session_id,
                reused_existing=True,
                had_store_error=False,
            )

    async def allocate_next_b_seq(self, a_session_id: str) -> int:
        """Allocate the next per-A-leg B-leg sequence number."""
        normalized_a_session_id = _normalize_required_identifier(
            a_session_id,
            "a_session_id",
        )
        now = self._time_provider()

        async with self._lock:
            self._cleanup_expired_entries(now)
            key = self._a_session_to_key.get(normalized_a_session_id)
            if key is None:
                raise KeyError(
                    f"A-leg session mapping not found for a_session_id={normalized_a_session_id}"
                )

            entry = self._entries.get(key)
            if entry is None:
                self._a_session_to_key.pop(normalized_a_session_id, None)
                raise KeyError(
                    f"A-leg session mapping not found for a_session_id={normalized_a_session_id}"
                )

            entry.last_b_seq += 1
            entry.last_accessed_at = now
            if self._sliding_expiration:
                entry.expires_at = now + self._continuity_ttl_seconds

            return entry.last_b_seq

    async def record_attempt(
        self,
        *,
        a_session_id: str,
        b_session_id: str,
        seq: int,
        backend_type: str | None,
        effective_model: str | None,
        reason: str | None,
    ) -> None:
        """Record attempt metadata and retain until mapping expiration."""
        normalized_a_session_id = _normalize_required_identifier(
            a_session_id,
            "a_session_id",
        )
        normalized_b_session_id = _normalize_required_identifier(
            b_session_id,
            "b_session_id",
        )
        if seq < 1:
            raise ValueError("seq must be >= 1")

        now = self._time_provider()
        async with self._lock:
            self._cleanup_expired_entries(now)
            if normalized_a_session_id not in self._a_session_to_key:
                raise KeyError(
                    "A-leg session mapping not found for "
                    f"a_session_id={normalized_a_session_id}"
                )

            record = B2buaAttemptRecord(
                b_session_id=normalized_b_session_id,
                a_session_id=normalized_a_session_id,
                seq=seq,
                backend_type=backend_type,
                effective_model=effective_model,
                reason=reason,
            )
            records = self._attempt_records.setdefault(normalized_a_session_id, [])
            records.append(record)

    async def get_attempt_records(self, a_session_id: str) -> list[B2buaAttemptRecord]:
        """Return attempt records retained for one A-leg session."""
        normalized_a_session_id = _normalize_required_identifier(
            a_session_id,
            "a_session_id",
        )
        now = self._time_provider()
        async with self._lock:
            self._cleanup_expired_entries(now)
            return list(self._attempt_records.get(normalized_a_session_id, []))

    async def _resolve_or_create_core(
        self,
        auth_scope_id: str,
        client_session_id: str,
        create_a_session_id: Callable[[], str],
    ) -> B2buaContinuityResolution:
        key = (
            _normalize_required_identifier(auth_scope_id, "auth_scope_id"),
            _normalize_required_identifier(client_session_id, "client_session_id"),
        )
        now = self._time_provider()

        async with self._lock:
            self._cleanup_expired_entries(now)

            existing = self._entries.get(key)
            if existing is not None:
                existing.last_accessed_at = now
                if self._sliding_expiration:
                    existing.expires_at = now + self._continuity_ttl_seconds
                return B2buaContinuityResolution(
                    a_session_id=existing.a_session_id,
                    reused_existing=True,
                    had_store_error=False,
                )

            a_session_id = create_a_session_id()
            entry = _ContinuityEntry(
                a_session_id=a_session_id,
                expires_at=now + self._continuity_ttl_seconds,
                last_accessed_at=now,
                last_b_seq=0,
            )
            self._entries[key] = entry
            self._a_session_to_key[a_session_id] = key
            self._evict_if_needed()

            return B2buaContinuityResolution(
                a_session_id=a_session_id,
                reused_existing=False,
                had_store_error=False,
            )

    def _cleanup_expired_entries(self, now: float) -> None:
        expired_keys = [
            key for key, entry in self._entries.items() if entry.expires_at <= now
        ]
        for key in expired_keys:
            entry = self._entries.pop(key, None)
            if entry is not None:
                self._a_session_to_key.pop(entry.a_session_id, None)
                self._attempt_records.pop(entry.a_session_id, None)

    def _evict_if_needed(self) -> None:
        while len(self._entries) > self._max_entries:
            oldest_key = min(
                self._entries.items(),
                key=lambda item: item[1].last_accessed_at,
            )[0]
            entry = self._entries.pop(oldest_key, None)
            if entry is not None:
                self._a_session_to_key.pop(entry.a_session_id, None)
                self._attempt_records.pop(entry.a_session_id, None)


class PersistentB2buaMappingStore(IB2buaMappingStore):
    """SQLite-backed continuity mapping store for restart and worker safety."""

    def __init__(
        self,
        *,
        database_path: str | Path | None = None,
        config: IConfig | None = None,
        continuity_ttl_seconds: int | None = None,
        sliding_expiration: bool | None = None,
        max_entries: int = _DEFAULT_MAX_MAPPINGS,
        time_provider: Callable[[], float] | None = None,
    ) -> None:
        if max_entries < 1:
            raise ValueError("max_entries must be >= 1")

        config_ttl = _read_config_ttl_seconds(config)
        config_sliding = _read_config_sliding_expiration(config)

        self._database_path = (
            Path(database_path)
            if database_path is not None
            else _DEFAULT_PERSISTENT_DB_PATH
        )
        self._database_path.parent.mkdir(parents=True, exist_ok=True)
        self._continuity_ttl_seconds = (
            continuity_ttl_seconds if continuity_ttl_seconds is not None else config_ttl
        )
        self._sliding_expiration = (
            sliding_expiration if sliding_expiration is not None else config_sliding
        )
        self._max_entries = max_entries
        self._time_provider = time_provider if time_provider is not None else time.time
        self._lock = asyncio.Lock()

        self._initialize_database()

    async def resolve_or_create_a_session_id(
        self,
        *,
        auth_scope_id: str,
        client_session_id: str,
        create_a_session_id: Callable[[], str],
    ) -> B2buaContinuityResolution:
        """Resolve existing active mapping or create a new one.

        Fails open by returning a fresh A-leg id when an internal store error occurs.
        """
        try:
            return await self._resolve_or_create_core(
                auth_scope_id,
                client_session_id,
                create_a_session_id,
            )
        except Exception as exc:  # - must fail open on store faults
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Persistent B2BUA continuity store failure; failing open with new A-leg id",
                    exc_info=exc,
                )
            return B2buaContinuityResolution(
                a_session_id=create_a_session_id(),
                reused_existing=False,
                had_store_error=True,
            )

    async def try_resolve_echoed_a_session_id(
        self,
        *,
        a_session_id: str,
        requesting_auth_scope_id: str | None,
    ) -> B2buaContinuityResolution | None:
        try:
            normalized_a = _normalize_required_identifier(
                a_session_id,
                "a_session_id",
            )
        except ValueError:
            return None

        now = self._time_provider()
        try:
            async with self._lock:
                with self._connect() as conn:
                    conn.execute("BEGIN IMMEDIATE")
                    self._cleanup_expired_entries(conn, now)
                    row = conn.execute(
                        """
                        SELECT auth_scope_id, expires_at
                        FROM b2bua_mappings
                        WHERE a_session_id = ?
                        """,
                        (normalized_a,),
                    ).fetchone()
                    if row is None:
                        conn.commit()
                        return None
                    expires_at = float(row["expires_at"])
                    if expires_at <= now:
                        conn.commit()
                        return None
                    stored_auth = str(row["auth_scope_id"])
                    if not _echo_continuity_auth_allows(
                        stored_auth_scope_id=stored_auth,
                        requesting_auth_scope_id=requesting_auth_scope_id,
                    ):
                        conn.commit()
                        return None
                    if self._sliding_expiration:
                        conn.execute(
                            """
                            UPDATE b2bua_mappings
                            SET last_accessed_at = ?, expires_at = ?
                            WHERE a_session_id = ?
                            """,
                            (
                                now,
                                now + self._continuity_ttl_seconds,
                                normalized_a,
                            ),
                        )
                    else:
                        conn.execute(
                            """
                            UPDATE b2bua_mappings
                            SET last_accessed_at = ?
                            WHERE a_session_id = ?
                            """,
                            (now, normalized_a),
                        )
                    conn.commit()
                    return B2buaContinuityResolution(
                        a_session_id=normalized_a,
                        reused_existing=True,
                        had_store_error=False,
                    )
        except Exception as exc:  # - must fail open on store faults
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Persistent B2BUA echo continuity lookup failure",
                    exc_info=exc,
                )
            return None

    async def allocate_next_b_seq(self, a_session_id: str) -> int:
        """Atomically allocate next B-leg sequence for an active A-leg mapping."""
        normalized_a_session_id = _normalize_required_identifier(
            a_session_id,
            "a_session_id",
        )
        now = self._time_provider()

        async with self._lock:
            with self._connect() as conn:
                conn.execute("BEGIN IMMEDIATE")
                self._cleanup_expired_entries(conn, now)
                row = conn.execute(
                    """
                    SELECT last_b_seq
                    FROM b2bua_mappings
                    WHERE a_session_id = ?
                    """,
                    (normalized_a_session_id,),
                ).fetchone()
                if row is None:
                    raise KeyError(
                        "A-leg session mapping not found for "
                        f"a_session_id={normalized_a_session_id}"
                    )

                next_seq = int(row["last_b_seq"]) + 1
                if self._sliding_expiration:
                    conn.execute(
                        """
                        UPDATE b2bua_mappings
                        SET last_b_seq = ?, last_accessed_at = ?, expires_at = ?
                        WHERE a_session_id = ?
                        """,
                        (
                            next_seq,
                            now,
                            now + self._continuity_ttl_seconds,
                            normalized_a_session_id,
                        ),
                    )
                else:
                    conn.execute(
                        """
                        UPDATE b2bua_mappings
                        SET last_b_seq = ?, last_accessed_at = ?
                        WHERE a_session_id = ?
                        """,
                        (
                            next_seq,
                            now,
                            normalized_a_session_id,
                        ),
                    )
                conn.commit()
                return next_seq

    async def record_attempt(
        self,
        *,
        a_session_id: str,
        b_session_id: str,
        seq: int,
        backend_type: str | None,
        effective_model: str | None,
        reason: str | None,
    ) -> None:
        """Persist backend attempt metadata for diagnostics."""
        normalized_a_session_id = _normalize_required_identifier(
            a_session_id,
            "a_session_id",
        )
        normalized_b_session_id = _normalize_required_identifier(
            b_session_id,
            "b_session_id",
        )
        if seq < 1:
            raise ValueError("seq must be >= 1")

        now = self._time_provider()
        async with self._lock:
            with self._connect() as conn:
                conn.execute("BEGIN IMMEDIATE")
                self._cleanup_expired_entries(conn, now)
                mapping_row = conn.execute(
                    """
                    SELECT 1
                    FROM b2bua_mappings
                    WHERE a_session_id = ?
                    """,
                    (normalized_a_session_id,),
                ).fetchone()
                if mapping_row is None:
                    raise KeyError(
                        "A-leg session mapping not found for "
                        f"a_session_id={normalized_a_session_id}"
                    )

                conn.execute(
                    """
                    INSERT INTO b2bua_attempts (
                        b_session_id,
                        a_session_id,
                        seq,
                        backend_type,
                        effective_model,
                        reason,
                        created_at
                    ) VALUES (?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        normalized_b_session_id,
                        normalized_a_session_id,
                        seq,
                        backend_type,
                        effective_model,
                        reason,
                        now,
                    ),
                )

                if self._sliding_expiration:
                    conn.execute(
                        """
                        UPDATE b2bua_mappings
                        SET last_accessed_at = ?, expires_at = ?
                        WHERE a_session_id = ?
                        """,
                        (
                            now,
                            now + self._continuity_ttl_seconds,
                            normalized_a_session_id,
                        ),
                    )
                else:
                    conn.execute(
                        """
                        UPDATE b2bua_mappings
                        SET last_accessed_at = ?
                        WHERE a_session_id = ?
                        """,
                        (
                            now,
                            normalized_a_session_id,
                        ),
                    )
                conn.commit()

    async def get_attempt_records(self, a_session_id: str) -> list[B2buaAttemptRecord]:
        """Load attempt records for one A-leg ordered by sequence."""
        normalized_a_session_id = _normalize_required_identifier(
            a_session_id,
            "a_session_id",
        )
        now = self._time_provider()
        async with self._lock:
            with self._connect() as conn:
                conn.execute("BEGIN IMMEDIATE")
                self._cleanup_expired_entries(conn, now)
                rows = conn.execute(
                    """
                    SELECT b_session_id, a_session_id, seq, backend_type, effective_model, reason
                    FROM b2bua_attempts
                    WHERE a_session_id = ?
                    ORDER BY seq ASC, created_at ASC
                    """,
                    (normalized_a_session_id,),
                ).fetchall()
                conn.commit()
                return [
                    B2buaAttemptRecord(
                        b_session_id=str(row["b_session_id"]),
                        a_session_id=str(row["a_session_id"]),
                        seq=int(row["seq"]),
                        backend_type=row["backend_type"],
                        effective_model=row["effective_model"],
                        reason=row["reason"],
                    )
                    for row in rows
                ]

    async def _resolve_or_create_core(
        self,
        auth_scope_id: str,
        client_session_id: str,
        create_a_session_id: Callable[[], str],
    ) -> B2buaContinuityResolution:
        normalized_auth_scope = _normalize_required_identifier(
            auth_scope_id,
            "auth_scope_id",
        )
        normalized_client_session_id = _normalize_required_identifier(
            client_session_id,
            "client_session_id",
        )
        now = self._time_provider()

        async with self._lock:
            with self._connect() as conn:
                conn.execute("BEGIN IMMEDIATE")
                self._cleanup_expired_entries(conn, now)
                existing_row = conn.execute(
                    """
                    SELECT a_session_id
                    FROM b2bua_mappings
                    WHERE auth_scope_id = ? AND client_session_id = ?
                    """,
                    (normalized_auth_scope, normalized_client_session_id),
                ).fetchone()
                if existing_row is not None:
                    a_session_id = str(existing_row["a_session_id"])
                    if self._sliding_expiration:
                        conn.execute(
                            """
                            UPDATE b2bua_mappings
                            SET last_accessed_at = ?, expires_at = ?
                            WHERE auth_scope_id = ? AND client_session_id = ?
                            """,
                            (
                                now,
                                now + self._continuity_ttl_seconds,
                                normalized_auth_scope,
                                normalized_client_session_id,
                            ),
                        )
                    else:
                        conn.execute(
                            """
                            UPDATE b2bua_mappings
                            SET last_accessed_at = ?
                            WHERE auth_scope_id = ? AND client_session_id = ?
                            """,
                            (
                                now,
                                normalized_auth_scope,
                                normalized_client_session_id,
                            ),
                        )
                    conn.commit()
                    return B2buaContinuityResolution(
                        a_session_id=a_session_id,
                        reused_existing=True,
                        had_store_error=False,
                    )

                a_session_id = create_a_session_id()
                conn.execute(
                    """
                    INSERT INTO b2bua_mappings (
                        auth_scope_id,
                        client_session_id,
                        a_session_id,
                        last_b_seq,
                        created_at,
                        last_accessed_at,
                        expires_at
                    ) VALUES (?, ?, ?, 0, ?, ?, ?)
                    """,
                    (
                        normalized_auth_scope,
                        normalized_client_session_id,
                        a_session_id,
                        now,
                        now,
                        now + self._continuity_ttl_seconds,
                    ),
                )
                self._evict_if_needed(conn)
                conn.commit()
                return B2buaContinuityResolution(
                    a_session_id=a_session_id,
                    reused_existing=False,
                    had_store_error=False,
                )

    def _initialize_database(self) -> None:
        with self._connect() as conn:
            conn.execute("PRAGMA journal_mode=WAL")
            conn.execute("PRAGMA foreign_keys=ON")
            conn.execute("""
                CREATE TABLE IF NOT EXISTS b2bua_mappings (
                    auth_scope_id TEXT NOT NULL,
                    client_session_id TEXT NOT NULL,
                    a_session_id TEXT NOT NULL UNIQUE,
                    last_b_seq INTEGER NOT NULL DEFAULT 0,
                    created_at REAL NOT NULL,
                    last_accessed_at REAL NOT NULL,
                    expires_at REAL NOT NULL,
                    PRIMARY KEY (auth_scope_id, client_session_id)
                )
                """)
            conn.execute("""
                CREATE TABLE IF NOT EXISTS b2bua_attempts (
                    b_session_id TEXT PRIMARY KEY,
                    a_session_id TEXT NOT NULL,
                    seq INTEGER NOT NULL,
                    backend_type TEXT,
                    effective_model TEXT,
                    reason TEXT,
                    created_at REAL NOT NULL,
                    FOREIGN KEY (a_session_id)
                        REFERENCES b2bua_mappings(a_session_id)
                        ON DELETE CASCADE
                )
                """)
            conn.execute("""
                CREATE INDEX IF NOT EXISTS idx_b2bua_mappings_expires_at
                ON b2bua_mappings(expires_at)
                """)
            conn.execute("""
                CREATE INDEX IF NOT EXISTS idx_b2bua_mappings_last_accessed
                ON b2bua_mappings(last_accessed_at)
                """)
            conn.execute("""
                CREATE INDEX IF NOT EXISTS idx_b2bua_attempts_a_session
                ON b2bua_attempts(a_session_id)
                """)
            conn.commit()

    def _connect(self) -> sqlite3.Connection:
        connection = sqlite3.connect(
            str(self._database_path),
            timeout=_SQLITE_BUSY_TIMEOUT_SECONDS,
            isolation_level=None,
            check_same_thread=False,
        )
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA foreign_keys=ON")
        return connection

    def _cleanup_expired_entries(self, conn: sqlite3.Connection, now: float) -> None:
        conn.execute(
            """
            DELETE FROM b2bua_mappings
            WHERE expires_at <= ?
            """,
            (now,),
        )

    def _evict_if_needed(self, conn: sqlite3.Connection) -> None:
        count_row = conn.execute(
            "SELECT COUNT(*) AS total FROM b2bua_mappings"
        ).fetchone()
        total_entries = int(count_row["total"]) if count_row is not None else 0
        overflow = total_entries - self._max_entries
        if overflow <= 0:
            return

        rows_to_evict = conn.execute(
            """
            SELECT auth_scope_id, client_session_id
            FROM b2bua_mappings
            ORDER BY last_accessed_at ASC
            LIMIT ?
            """,
            (overflow,),
        ).fetchall()

        for row in rows_to_evict:
            conn.execute(
                """
                DELETE FROM b2bua_mappings
                WHERE auth_scope_id = ? AND client_session_id = ?
                """,
                (
                    str(row["auth_scope_id"]),
                    str(row["client_session_id"]),
                ),
            )


__all__ = [
    "B2buaAttemptRecord",
    "B2buaContinuityResolution",
    "InMemoryB2buaMappingStore",
    "PersistentB2buaMappingStore",
]
