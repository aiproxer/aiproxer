"""Repository for backend quota records."""

from __future__ import annotations

import json
import logging
from datetime import datetime, timezone
from typing import TYPE_CHECKING

from sqlalchemy import select

from src.core.database.models.usage import BackendQuotaTable
from src.core.database.repositories.base import AsyncRepository

if TYPE_CHECKING:
    from src.core.database.engine import DatabaseEngine

logger = logging.getLogger(__name__)


class BackendQuotaRepository(AsyncRepository[BackendQuotaTable]):
    """Repository for backend quota CRUD operations."""

    def __init__(self, engine: DatabaseEngine) -> None:
        """Initialize the repository.

        Args:
            engine: Database engine for session creation
        """
        super().__init__(engine)

    @property
    def model_class(self) -> type[BackendQuotaTable]:
        """Return the model class."""
        return BackendQuotaTable

    async def upsert_quota(
        self, backend_type: str, quota_headers: dict[str, str]
    ) -> BackendQuotaTable:
        """Insert or update backend quota.

        Args:
            backend_type: The type of backend
            quota_headers: The quota headers to store

        Returns:
            The upserted backend quota record
        """
        async with self._engine.session() as session:
            existing = await session.get(BackendQuotaTable, backend_type)

            quota_json = json.dumps(quota_headers)
            now = datetime.now(timezone.utc)

            if existing:
                # Update existing record
                existing.quota_headers_json = quota_json
                existing.last_updated = now
                session.add(existing)
                await session.flush()
                return existing
            else:
                # Create new record
                new_record = BackendQuotaTable(
                    backend_type=backend_type,
                    quota_headers_json=quota_json,
                    last_updated=now,
                )
                session.add(new_record)
                await session.flush()
                return new_record

    async def get_all_quotas(self) -> dict[str, dict[str, str]]:
        """Get all stored quotas.

        Returns:
            Dictionary mapping backend_type to quota headers
        """
        async with self._engine.session() as session:
            statement = select(BackendQuotaTable)
            result = await session.execute(statement)
            records = result.scalars().all()

            return {
                record.backend_type: json.loads(record.quota_headers_json)
                for record in records
            }
