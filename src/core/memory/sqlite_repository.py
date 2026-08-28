"""SQLite implementation of MemoryRepository for ProxyMem.

.. deprecated::
    This module is deprecated in favor of the SQLModel-based implementation
    at `src.core.database.repositories.memory_repository.SQLModelMemoryRepository`.

    The legacy implementation remains for backward compatibility during the
    transition period. New code should use the SQLModel implementation via
    the DI container by requesting `IMemoryRepository` or `SQLModelMemoryRepository`.
"""

from __future__ import annotations

import json
import logging
from datetime import datetime
from pathlib import Path
from typing import Any

import aiosqlite

from src.core.memory.config import MemoryConfiguration
from src.core.memory.models import (
    FileChange,
    GitOperation,
    SessionSummary,
    TaskItem,
    TestRun,
)

logger = logging.getLogger(__name__)


class MemoryRepository:
    """SQLite-backed repository for ProxyMem session summaries."""

    def __init__(self, config: MemoryConfiguration):
        logger.error(
            "DEPRECATED: MemoryRepository (aiosqlite) is deprecated. "
            "Use SQLModelMemoryRepository from src.core.database.repositories instead. "
            "This legacy implementation will be removed in a future release."
        )
        self._config = config
        self._db_path = Path(config.database_path)
        self._initialized = False
        self._db: aiosqlite.Connection | None = None

    async def _get_db(self) -> aiosqlite.Connection:
        """Get or create the database connection."""
        if self._db is None:
            self._db = await aiosqlite.connect(self._db_path)
            self._db.row_factory = aiosqlite.Row
        return self._db

    async def initialize_schema(self) -> None:
        """Create database schema if it doesn't exist."""
        if str(self._db_path) != ":memory:":
            self._db_path.parent.mkdir(parents=True, exist_ok=True)

        db = await self._get_db()
        await db.execute("""
            CREATE TABLE IF NOT EXISTS session_summaries (
                id TEXT PRIMARY KEY,
                user_id TEXT NOT NULL,
                tenant_id TEXT,
                project_id TEXT,
                project_root TEXT,
                session_id TEXT NOT NULL,
                session_start TIMESTAMP NOT NULL,
                client_agent TEXT,
                backend_model TEXT NOT NULL,
                title TEXT NOT NULL,
                scope TEXT,
                goals TEXT,
                modified_files TEXT,
                remaining_tasks TEXT,
                git_operations TEXT,
                operations_performed TEXT,
                open_questions TEXT,
                tests_run TEXT,
                errors TEXT,
                branch TEXT,
                head_sha TEXT,
                completion_status TEXT,
                key_decisions TEXT,
                risks_or_warnings TEXT,
                evidence TEXT,
                full_analysis TEXT,
                summary_version TEXT NOT NULL,
                created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
            )
        """)

        await db.execute("""
            CREATE INDEX IF NOT EXISTS idx_session_summaries_user_id
            ON session_summaries(user_id)
        """)

        await db.execute("""
            CREATE INDEX IF NOT EXISTS idx_session_summaries_session_start
            ON session_summaries(session_start DESC)
        """)

        await db.execute("""
            CREATE INDEX IF NOT EXISTS idx_session_summaries_user_session_start
            ON session_summaries(user_id, session_start DESC)
        """)

        await db.execute("""
            CREATE INDEX IF NOT EXISTS idx_session_summaries_user_tenant
            ON session_summaries(user_id, tenant_id, session_start DESC)
        """)

        await db.execute("""
            CREATE INDEX IF NOT EXISTS idx_session_summaries_user_project
            ON session_summaries(user_id, project_id, session_start DESC)
        """)

        await db.execute("""
            CREATE TABLE IF NOT EXISTS user_project_dirs (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                user_id TEXT NOT NULL,
                project_root TEXT NOT NULL,
                UNIQUE(user_id, project_root)
            )
        """)

        await db.commit()
        self._initialized = True
        logger.info("Memory repository schema initialized at %s", self._db_path)

    async def save_session_summary(self, summary: SessionSummary) -> None:
        """Persist a session summary to the database."""
        if not self._initialized:
            await self.initialize_schema()

        db = await self._get_db()
        await db.execute(
            """
            INSERT OR REPLACE INTO session_summaries (
                id, user_id, tenant_id, project_id, project_root,
                session_id, session_start, client_agent, backend_model,
                title, scope, goals, modified_files, remaining_tasks,
                git_operations, operations_performed, open_questions,
                tests_run, errors, branch, head_sha, completion_status,
                key_decisions, risks_or_warnings, evidence, full_analysis,
                summary_version, created_at
            ) VALUES (
                ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
                ?, ?, ?, ?, ?, ?, ?, ?, ?
            )
            """,
            (
                summary.id,
                summary.user_id,
                summary.tenant_id,
                summary.project_id,
                summary.project_root,
                summary.session_id,
                summary.session_start.isoformat(),
                summary.client_agent,
                summary.backend_model,
                summary.title,
                summary.scope,
                json.dumps(summary.goals),
                # PERFORMANCE: Avoid repeated model_dump() - check if already dict
                json.dumps(
                    [
                        f if isinstance(f, dict) else f.model_dump()
                        for f in summary.modified_files
                    ]
                ),
                json.dumps(
                    [
                        t if isinstance(t, dict) else t.model_dump()
                        for t in summary.remaining_tasks
                    ]
                ),
                json.dumps(
                    [
                        g if isinstance(g, dict) else g.model_dump()
                        for g in summary.git_operations
                    ]
                ),
                json.dumps(summary.operations_performed),
                json.dumps(summary.open_questions),
                json.dumps(
                    [
                        t if isinstance(t, dict) else t.model_dump()
                        for t in summary.tests_run
                    ]
                ),
                json.dumps(summary.errors),
                summary.branch,
                summary.head_sha,
                summary.completion_status,
                json.dumps(summary.key_decisions),
                json.dumps(summary.risks_or_warnings),
                json.dumps(summary.evidence),
                summary.full_analysis,
                summary.summary_version,
                summary.created_at.isoformat(),
            ),
        )
        await db.commit()
        logger.debug(
            "Saved session summary %s for user %s", summary.id, summary.user_id
        )

    async def get_recent_sessions(
        self,
        user_id: str,
        limit: int,
        tenant_id: str | None = None,
        project_id: str | None = None,
        project_root: str | None = None,
    ) -> list[SessionSummary]:
        """Retrieve recent session summaries for a user."""
        if not self._initialized:
            await self.initialize_schema()

        db = await self._get_db()
        query = "SELECT * FROM session_summaries WHERE user_id = ?"
        params: list[Any] = [user_id]

        if tenant_id is not None:
            query += " AND tenant_id = ?"
            params.append(tenant_id)

        if project_id is not None:
            query += " AND project_id = ?"
            params.append(project_id)
        elif project_root is not None:
            query += " AND project_root = ?"
            params.append(project_root)

        query += " ORDER BY session_start DESC LIMIT ?"
        params.append(limit)

        cursor = await db.execute(query, params)
        rows = await cursor.fetchall()

        return [self._row_to_summary(dict(row)) for row in rows]

    async def delete_old_sessions(self, before_date: datetime) -> int:
        """Delete sessions older than the specified date."""
        if not self._initialized:
            await self.initialize_schema()

        db = await self._get_db()
        cursor = await db.execute(
            "DELETE FROM session_summaries WHERE session_start < ?",
            (before_date.isoformat(),),
        )
        await db.commit()
        deleted = cursor.rowcount
        if deleted > 0:
            logger.info("Deleted %d old session summaries", deleted)
        return deleted

    async def get_or_create_project_id(self, user_id: str, project_root: str) -> str:
        """Get or create a stable project_id for a user+project_root pair."""
        if not self._initialized:
            await self.initialize_schema()

        db = await self._get_db()
        cursor = await db.execute(
            "SELECT id FROM user_project_dirs WHERE user_id = ? AND project_root = ?",
            (user_id, project_root),
        )
        row = await cursor.fetchone()

        if row:
            return f"proj-{row[0]}"

        cursor = await db.execute(
            "INSERT INTO user_project_dirs (user_id, project_root) VALUES (?, ?)",
            (user_id, project_root),
        )
        await db.commit()
        return f"proj-{cursor.lastrowid}"

    def _row_to_summary(self, row: dict[str, Any]) -> SessionSummary:
        """Convert a database row to a SessionSummary."""
        return SessionSummary(
            id=row["id"],
            user_id=row["user_id"],
            tenant_id=row.get("tenant_id"),
            project_id=row.get("project_id"),
            project_root=row.get("project_root"),
            session_id=row["session_id"],
            session_start=datetime.fromisoformat(row["session_start"]),
            client_agent=row.get("client_agent"),
            backend_model=row["backend_model"],
            title=row["title"],
            scope=row.get("scope", ""),
            goals=json.loads(row.get("goals") or "[]"),
            open_questions=json.loads(row.get("open_questions") or "[]"),
            remaining_tasks=[
                TaskItem(**t) for t in json.loads(row.get("remaining_tasks") or "[]")
            ],
            modified_files=[
                FileChange(**f) for f in json.loads(row.get("modified_files") or "[]")
            ],
            git_operations=[
                GitOperation(**g) for g in json.loads(row.get("git_operations") or "[]")
            ],
            completion_status=row.get("completion_status", "partial"),
            key_decisions=json.loads(row.get("key_decisions") or "[]"),
            operations_performed=json.loads(row.get("operations_performed") or "[]"),
            tests_run=[TestRun(**t) for t in json.loads(row.get("tests_run") or "[]")],
            errors=json.loads(row.get("errors") or "[]"),
            risks_or_warnings=json.loads(row.get("risks_or_warnings") or "[]"),
            evidence=json.loads(row.get("evidence") or "[]"),
            full_analysis=row.get("full_analysis", ""),
            branch=row.get("branch"),
            head_sha=row.get("head_sha"),
            summary_version=row["summary_version"],
            created_at=datetime.fromisoformat(row["created_at"]),
        )

    async def close(self) -> None:
        """Close the repository (for graceful shutdown)."""
        if self._db:
            await self._db.close()
            self._db = None
