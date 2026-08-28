"""Database abstraction layer using SQLModel and SQLAlchemy async.

This module provides:
- Unified async database engine and session management
- SQLModel-based ORM models for all persistent data
- Repository pattern implementations
- Alembic migration support

Usage:
    # Via DI container (recommended)
    engine = provider.get_required_service(DatabaseEngine)
    repo = provider.get_required_service(SQLModelMemoryRepository)

    # Direct usage (for tests)
    from src.core.database import DatabaseConfig, init_database
    config = DatabaseConfig(url="sqlite+aiosqlite:///:memory:")
    engine = await init_database(config)
"""

from src.core.database.config import DatabaseConfig
from src.core.database.engine import (
    DatabaseEngine,
    get_async_session,
    init_database,
)
from src.core.database.repositories.memory_repository import SQLModelMemoryRepository
from src.core.database.repositories.sso_repository import (
    SQLModelAuthorizationRepository,
    SQLModelRateLimitRepository,
    SQLModelTokenRepository,
)

__all__ = [
    # Configuration
    "DatabaseConfig",
    # Engine
    "DatabaseEngine",
    "get_async_session",
    "init_database",
    # Repositories
    "SQLModelMemoryRepository",
    "SQLModelTokenRepository",
    "SQLModelRateLimitRepository",
    "SQLModelAuthorizationRepository",
]
