"""
Database layer for SSO authentication.

This module provides SQLite database operations for token storage,
authorization tracking, and rate limiting with async support.
"""

import logging
import os
import secrets
import stat
from datetime import datetime, timedelta, timezone
from pathlib import Path

import aiosqlite

from src.core.auth.sso.exceptions import SSOException

logger = logging.getLogger(__name__)
from src.core.auth.sso.models import (
    TokenRecord,
)


class DatabaseManager:
    """Manages SQLite database schema and migrations."""

    SCHEMA_VERSION = 3

    # Schema definition
    SCHEMA_SQL = """
    -- Schema version tracking
    CREATE TABLE IF NOT EXISTS schema_version (
        version INTEGER PRIMARY KEY,
        applied_at TEXT NOT NULL
    );

    -- Agent tokens table
    CREATE TABLE IF NOT EXISTS agent_tokens (
        id TEXT PRIMARY KEY,
        token_hash TEXT NOT NULL UNIQUE,
        user_id TEXT NOT NULL,
        user_email TEXT NOT NULL,
        provider TEXT NOT NULL,
        is_authenticated INTEGER NOT NULL DEFAULT 0,
        is_active INTEGER NOT NULL DEFAULT 1,
        created_at TEXT NOT NULL,
        last_authenticated_at TEXT,
        auth_expires_at TEXT
    );

    CREATE INDEX IF NOT EXISTS idx_token_hash ON agent_tokens(token_hash);
    CREATE INDEX IF NOT EXISTS idx_user_id ON agent_tokens(user_id);
    CREATE INDEX IF NOT EXISTS idx_is_active ON agent_tokens(is_active);

    -- Pending authorizations (single-user mode)
    CREATE TABLE IF NOT EXISTS pending_authorizations (
        id TEXT PRIMARY KEY,
        sso_state TEXT NOT NULL UNIQUE,
        user_email TEXT NOT NULL,
        user_id TEXT NOT NULL,
        provider TEXT NOT NULL,
        confirmation_code_hash TEXT NOT NULL,
        attempts_remaining INTEGER NOT NULL DEFAULT 3,
        created_at TEXT NOT NULL,
        expires_at TEXT NOT NULL,
        client_ip TEXT NOT NULL
    );

    CREATE INDEX IF NOT EXISTS idx_sso_state ON pending_authorizations(sso_state);

    -- Rate limiting
    CREATE TABLE IF NOT EXISTS rate_limits (
        identifier TEXT PRIMARY KEY,
        failed_attempts INTEGER NOT NULL DEFAULT 0,
        last_attempt_at TEXT NOT NULL,
        blocked_until TEXT
    );

    -- SSO Login Tokens (One-off)
    CREATE TABLE IF NOT EXISTS sso_login_tokens (
        token TEXT PRIMARY KEY,
        created_at TEXT NOT NULL,
        expires_at TEXT NOT NULL,
        agent_token_id TEXT
    );
    
    CREATE INDEX IF NOT EXISTS idx_login_token_agent_token ON sso_login_tokens(agent_token_id);
    """

    def __init__(self, database_path: str):
        """
        Initialize database manager.

        Args:
            database_path: Path to SQLite database file
        """
        self.database_path = database_path

    async def initialize_schema(self) -> None:
        """
        Create or migrate database schema.

        Creates the database file with restrictive permissions and
        initializes all required tables.

        Raises:
            SSOException: If database initialization fails
        """
        try:
            # Ensure parent directory exists
            db_path = Path(self.database_path)
            db_path.parent.mkdir(parents=True, exist_ok=True)

            # Create database and apply schema
            async with aiosqlite.connect(self.database_path) as db:
                # Execute schema
                await db.executescript(self.SCHEMA_SQL)

                # Check current schema version
                cursor = await db.execute(
                    "SELECT version FROM schema_version ORDER BY version DESC LIMIT 1"
                )
                row = await cursor.fetchone()
                current_version = row[0] if row else 0

                # Apply migrations if needed
                if current_version < self.SCHEMA_VERSION:
                    await self._apply_migrations(db, current_version)

                await db.commit()

            # Set restrictive file permissions (owner read/write only)
            self._set_restrictive_permissions()

        except Exception as e:
            raise SSOException(
                "Failed to initialize database schema",
                details={"database_path": self.database_path, "error": str(e)},
                original_error=e,
            ) from e

    async def _apply_migrations(
        self, db: aiosqlite.Connection, current_version: int
    ) -> None:
        """
        Apply database migrations.

        Args:
            db: Database connection
            current_version: Current schema version
        """
        # Record schema version
        await db.execute(
            "INSERT INTO schema_version (version, applied_at) VALUES (?, ?)",
            (self.SCHEMA_VERSION, datetime.now(timezone.utc).isoformat()),
        )

        if current_version < 2:
            # Added sso_login_tokens table in version 2
            # Since SCHEMA_SQL creates it IF NOT EXISTS, we don't need explicit CREATE here for new installs
            # But for migration we might. However, executing SCHEMA_SQL at start covers it.
            # The migration tracking is mainly for data changes or complex alterations.
            pass

    def _set_restrictive_permissions(self) -> None:
        """
        Set restrictive file permissions on database file.

        Sets permissions to owner read/write only (0o600).
        """
        try:
            db_path = Path(self.database_path)
            if db_path.exists():
                # Set to owner read/write only (0o600)
                os.chmod(self.database_path, stat.S_IRUSR | stat.S_IWUSR)
        except OSError as e:
            # Permission setting is best-effort on Windows
            # Log for visibility but don't fail database initialization
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to set restrictive permissions on SSO database: %s",
                    e,
                    exc_info=True,
                )


class TokenRepository:
    """SQLite repository for agent token storage."""

    def __init__(self, database_path: str):
        """
        Initialize token repository.

        Args:
            database_path: Path to SQLite database file
        """
        self.database_path = database_path

    async def store_token(self, token_record: TokenRecord) -> None:
        """
        Store a new token record.

        Args:
            token_record: Token record to store

        Raises:
            SSOException: If storage fails
        """
        try:
            # Convert FakeDatetime objects to real datetime for SQLite compatibility
            def to_real_datetime(dt: datetime | None) -> datetime | None:
                if dt is None:
                    return None
                # Handle FakeDatetime from freezegun - check if it's a FakeDatetime instance
                if type(dt).__name__ == "FakeDatetime" or (
                    hasattr(dt, "year")
                    and hasattr(dt, "month")
                    and hasattr(dt, "day")
                    and not isinstance(dt, datetime)
                ):
                    return datetime(
                        dt.year,
                        dt.month,
                        dt.day,
                        dt.hour,
                        dt.minute,
                        dt.second,
                        getattr(dt, "microsecond", 0),
                        getattr(dt, "tzinfo", None),
                    )
                return dt

            async with aiosqlite.connect(self.database_path) as db:
                created_at = to_real_datetime(token_record.created_at)
                last_authenticated_at = to_real_datetime(
                    token_record.last_authenticated_at
                )
                auth_expires_at = to_real_datetime(token_record.auth_expires_at)

                # Ensure token_hash is a string (handle tuple case)
                token_hash_str = (
                    token_record.token_hash
                    if isinstance(token_record.token_hash, str)
                    else str(token_record.token_hash)
                )

                await db.execute(
                    """
                    INSERT INTO agent_tokens (
                        id, token_hash, user_id, user_email, provider,
                        is_authenticated, is_active, created_at,
                        last_authenticated_at, auth_expires_at
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        str(token_record.id),
                        token_hash_str,
                        str(token_record.user_id),
                        str(token_record.user_email),
                        str(token_record.provider),
                        1 if token_record.is_authenticated else 0,
                        1 if token_record.is_active else 0,
                        created_at.isoformat() if created_at else None,
                        (
                            last_authenticated_at.isoformat()
                            if last_authenticated_at
                            else None
                        ),
                        (auth_expires_at.isoformat() if auth_expires_at else None),
                    ),
                )
                await db.commit()
        except Exception as e:
            raise SSOException(
                "Failed to store token record",
                details={"token_id": token_record.id, "error": str(e)},
                original_error=e,
            ) from e

    async def get_by_id(self, token_id: str) -> TokenRecord | None:
        """
        Get token record by ID.

        Args:
            token_id: Token ID

        Returns:
            TokenRecord if found, None otherwise
        """
        try:
            async with aiosqlite.connect(self.database_path) as db:
                db.row_factory = aiosqlite.Row
                cursor = await db.execute(
                    """
                    SELECT id, token_hash, user_id, user_email, provider,
                           is_authenticated, is_active, created_at,
                           last_authenticated_at, auth_expires_at
                    FROM agent_tokens
                    WHERE id = ?
                    """,
                    (token_id,),
                )
                row = await cursor.fetchone()

                if row is None:
                    return None

                return self._row_to_token_record(row)
        except Exception as e:
            raise SSOException(
                "Failed to get token by ID",
                details={"token_id": token_id, "error": str(e)},
                original_error=e,
            ) from e

    async def find_by_user_id(self, user_id: str) -> TokenRecord | None:
        """
        Find an active token record by user ID.

        Args:
            user_id: User ID to search for

        Returns:
            TokenRecord if found, None otherwise

        Raises:
            SSOException: If database query fails
        """
        try:
            async with aiosqlite.connect(self.database_path) as db:
                db.row_factory = aiosqlite.Row
                cursor = await db.execute(
                    """
                    SELECT id, token_hash, user_id, user_email, provider,
                           is_authenticated, is_active, created_at,
                           last_authenticated_at, auth_expires_at
                    FROM agent_tokens
                    WHERE user_id = ? AND is_active = 1
                    ORDER BY created_at DESC
                    LIMIT 1
                    """,
                    (user_id,),
                )
                row = await cursor.fetchone()

                if row is None:
                    return None

                return self._row_to_token_record(row)

        except Exception as e:
            raise SSOException(
                "Failed to find token by user ID",
                details={"user_id": user_id, "error": str(e)},
                original_error=e,
            ) from e

    async def find_by_hash(self, token_hash: str) -> TokenRecord | None:
        """
        Find token record by hash using constant-time comparison.

        This method retrieves all active token hashes and performs
        constant-time comparison to prevent timing attacks.

        Args:
            token_hash: Token hash to search for

        Returns:
            TokenRecord if found, None otherwise

        Raises:
            SSOException: If database query fails
        """
        try:
            async with aiosqlite.connect(self.database_path) as db:
                db.row_factory = aiosqlite.Row
                cursor = await db.execute("""
                    SELECT id, token_hash, user_id, user_email, provider,
                           is_authenticated, is_active, created_at,
                           last_authenticated_at, auth_expires_at
                    FROM agent_tokens
                    WHERE is_active = 1
                    """)
                rows = await cursor.fetchall()

                # Perform constant-time comparison
                import hmac

                matching_row = None
                for row in rows:
                    # Use constant-time comparison
                    # hmac.compare_digest requires both strings to be the same type
                    try:
                        if hmac.compare_digest(str(row["token_hash"]), str(token_hash)):
                            matching_row = row
                            # Continue iterating to maintain constant time
                            # Don't break early
                    except (TypeError, ValueError):
                        # If comparison fails, continue to next row
                        continue

                if matching_row is None:
                    return None

                return self._row_to_token_record(matching_row)

        except aiosqlite.Error as e:
            raise SSOException(
                "Failed to find token by hash",
                details={"error": str(e)},
                original_error=e,
            ) from e

    async def update_auth_status(
        self,
        token_id: str,
        authenticated: bool,
        expiry: datetime | None,
    ) -> None:
        """
        Update authentication status for a token.

        Args:
            token_id: Token ID to update
            authenticated: New authentication status
            expiry: New expiry timestamp (None to clear)

        Raises:
            SSOException: If update fails
        """
        try:
            async with aiosqlite.connect(self.database_path) as db:
                await db.execute(
                    """
                    UPDATE agent_tokens
                    SET is_authenticated = ?,
                        last_authenticated_at = ?,
                        auth_expires_at = ?
                    WHERE id = ?
                    """,
                    (
                        1 if authenticated else 0,
                        datetime.now(timezone.utc).isoformat(),
                        expiry.isoformat() if expiry else None,
                        token_id,
                    ),
                )
                await db.commit()
        except Exception as e:
            raise SSOException(
                "Failed to update authentication status",
                details={"token_id": token_id, "error": str(e)},
                original_error=e,
            ) from e

    async def revoke_token(self, token_id: str) -> None:
        """
        Mark token as revoked (soft delete).

        Args:
            token_id: Token ID to revoke

        Raises:
            SSOException: If revocation fails
        """
        try:
            async with aiosqlite.connect(self.database_path) as db:
                await db.execute(
                    """
                    UPDATE agent_tokens
                    SET is_active = 0
                    WHERE id = ?
                    """,
                    (token_id,),
                )
                await db.commit()
        except Exception as e:
            raise SSOException(
                "Failed to revoke token",
                details={"token_id": token_id, "error": str(e)},
                original_error=e,
            ) from e

    async def get_all_token_hashes(self) -> list[str]:
        """
        Get all active token hashes for verification.

        Returns:
            List of active token hashes

        Raises:
            SSOException: If query fails
        """
        try:
            async with aiosqlite.connect(self.database_path) as db:
                cursor = await db.execute("""
                    SELECT token_hash
                    FROM agent_tokens
                    WHERE is_active = 1
                    """)
                rows = await cursor.fetchall()
                return [row[0] for row in rows]
        except Exception as e:
            raise SSOException(
                "Failed to get token hashes",
                details={"error": str(e)},
                original_error=e,
            ) from e

    def _row_to_token_record(self, row: aiosqlite.Row) -> TokenRecord:
        """
        Convert database row to TokenRecord.

        Args:
            row: Database row

        Returns:
            TokenRecord instance
        """
        return TokenRecord(
            id=row["id"],
            token_hash=row["token_hash"],
            user_id=row["user_id"],
            user_email=row["user_email"],
            provider=row["provider"],
            is_authenticated=bool(row["is_authenticated"]),
            is_active=bool(row["is_active"]),
            created_at=datetime.fromisoformat(row["created_at"]),
            last_authenticated_at=(
                datetime.fromisoformat(row["last_authenticated_at"])
                if row["last_authenticated_at"]
                else None
            ),
            auth_expires_at=(
                datetime.fromisoformat(row["auth_expires_at"])
                if row["auth_expires_at"]
                else None
            ),
        )

    async def create_login_token(
        self, ttl_minutes: int = 10, agent_token_id: str | None = None
    ) -> str:
        """
        Create a one-off login token for SSO link validation.

        Args:
            ttl_minutes: Token validity duration in minutes
            agent_token_id: Optional existing agent token ID for re-authentication

        Returns:
            Generated token string
        """
        token = secrets.token_urlsafe(32)
        now = datetime.now(timezone.utc)
        expires_at = now + timedelta(minutes=ttl_minutes)

        try:
            async with aiosqlite.connect(self.database_path) as db:
                await db.execute(
                    """
                    INSERT INTO sso_login_tokens (token, created_at, expires_at, agent_token_id)
                    VALUES (?, ?, ?, ?)
                    """,
                    (token, now.isoformat(), expires_at.isoformat(), agent_token_id),
                )
                await db.commit()
            return token
        except Exception as e:
            raise SSOException(
                "Failed to create login token",
                details={"error": str(e)},
                original_error=e,
            ) from e

    async def verify_and_consume_login_token(
        self, token: str
    ) -> tuple[bool, str | None]:
        """
        Verify and consume (delete) a login token.

        Args:
            token: The token to verify

        Returns:
            Tuple of (is_valid, agent_token_id)
            - is_valid: True if token was valid and consumed, False otherwise
            - agent_token_id: Associated agent token ID for re-auth, or None for new auth
        """
        if not token:
            return (False, None)

        try:
            async with aiosqlite.connect(self.database_path) as db:
                # Check if token exists and is not expired
                cursor = await db.execute(
                    """
                    SELECT expires_at, agent_token_id FROM sso_login_tokens
                    WHERE token = ?
                    """,
                    (token,),
                )
                row = await cursor.fetchone()

                if not row:
                    return (False, None)

                expires_at = datetime.fromisoformat(row[0])
                agent_token_id = row[1] if len(row) > 1 else None

                # Assume UTC if no timezone info
                if expires_at.tzinfo is None:
                    expires_at = expires_at.replace(tzinfo=timezone.utc)
                if datetime.now(timezone.utc) > expires_at:
                    # Delete expired token (cleanup)
                    await db.execute(
                        "DELETE FROM sso_login_tokens WHERE token = ?",
                        (token,),
                    )
                    await db.commit()
                    return (False, None)

                # Token is valid, consume it (delete it)
                await db.execute(
                    "DELETE FROM sso_login_tokens WHERE token = ?",
                    (token,),
                )
                await db.commit()
                return (True, agent_token_id)

        except (aiosqlite.Error, ValueError) as e:
            # Log the error for debugging, but still return invalid
            # to maintain original semantics (fail-safe behavior)
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Error verifying/consuming login token: %s",
                    e,
                    exc_info=True,
                )
            return (False, None)
