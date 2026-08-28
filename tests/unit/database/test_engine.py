"""Unit tests for database engine."""

import pytest
from sqlalchemy.ext.asyncio import AsyncEngine, AsyncSession, async_sessionmaker
from src.core.database.config import DatabaseConfig
from src.core.database.engine import DatabaseEngine, get_async_session, init_database


class TestDatabaseEngine:
    """Tests for DatabaseEngine class."""

    @pytest.fixture
    def in_memory_config(self) -> DatabaseConfig:
        """Create in-memory SQLite config for testing."""
        return DatabaseConfig(url="sqlite+aiosqlite:///:memory:")

    def test_init_with_config(self, in_memory_config: DatabaseConfig) -> None:
        """Test engine initialization with config."""
        engine = DatabaseEngine(in_memory_config)

        assert engine.config == in_memory_config
        assert engine._engine is None
        assert engine._session_factory is None
        assert engine._initialized is False

    def test_engine_property_creates_engine(
        self, in_memory_config: DatabaseConfig
    ) -> None:
        """Test that engine property lazily creates AsyncEngine."""
        db_engine = DatabaseEngine(in_memory_config)

        engine = db_engine.engine

        assert engine is not None
        assert isinstance(engine, AsyncEngine)
        # Calling again returns same instance
        assert db_engine.engine is engine

    def test_session_factory_property(self, in_memory_config: DatabaseConfig) -> None:
        """Test that session_factory property creates async_sessionmaker."""
        db_engine = DatabaseEngine(in_memory_config)

        factory = db_engine.session_factory

        assert factory is not None
        assert isinstance(factory, async_sessionmaker)
        # Calling again returns same instance
        assert db_engine.session_factory is factory

    async def test_initialize_creates_tables(
        self, in_memory_config: DatabaseConfig
    ) -> None:
        """Test that initialize() creates all tables."""
        db_engine = DatabaseEngine(in_memory_config)

        await db_engine.initialize()

        assert db_engine._initialized is True

        # Verify tables exist by querying metadata
        async with db_engine.engine.begin() as conn:
            from sqlalchemy import inspect

            def get_table_names(connection: object) -> list[str]:
                inspector = inspect(connection)
                return inspector.get_table_names()

            table_names = await conn.run_sync(get_table_names)

        # Check expected tables exist
        assert "session_summaries" in table_names
        assert "user_project_dirs" in table_names
        assert "agent_tokens" in table_names
        assert "pending_authorizations" in table_names
        assert "rate_limits" in table_names
        assert "sso_login_tokens" in table_names
        assert "schema_version" in table_names

        await db_engine.close()

    async def test_session_context_manager(
        self, in_memory_config: DatabaseConfig
    ) -> None:
        """Test session context manager."""
        db_engine = DatabaseEngine(in_memory_config)
        await db_engine.initialize()

        async with db_engine.session() as session:
            assert isinstance(session, AsyncSession)
            # Session should be usable
            result = await session.execute(__import__("sqlalchemy").text("SELECT 1"))
            assert result.scalar() == 1

        await db_engine.close()

    async def test_session_commits_on_success(
        self, in_memory_config: DatabaseConfig
    ) -> None:
        """Test that session auto-commits on successful exit."""
        db_engine = DatabaseEngine(in_memory_config)
        await db_engine.initialize()

        from src.core.database.models.sso import RateLimitTable

        # Insert a record
        async with db_engine.session() as session:
            record = RateLimitTable(identifier="test-ip")
            session.add(record)

        # Verify it was committed (in a new session)
        async with db_engine.session() as session:
            result = await session.get(RateLimitTable, "test-ip")
            assert result is not None
            assert result.identifier == "test-ip"

        await db_engine.close()

    async def test_session_rollbacks_on_error(
        self, in_memory_config: DatabaseConfig
    ) -> None:
        """Test that session rolls back on exception."""
        db_engine = DatabaseEngine(in_memory_config)
        await db_engine.initialize()

        from src.core.database.models.sso import RateLimitTable

        # Try to insert but raise exception
        with pytest.raises(ValueError):
            async with db_engine.session() as session:
                record = RateLimitTable(identifier="test-rollback")
                session.add(record)
                raise ValueError("Test exception")

        # Verify it was not committed
        async with db_engine.session() as session:
            result = await session.get(RateLimitTable, "test-rollback")
            assert result is None

        await db_engine.close()

    async def test_close_disposes_engine(
        self, in_memory_config: DatabaseConfig
    ) -> None:
        """Test that close() disposes engine."""
        db_engine = DatabaseEngine(in_memory_config)
        await db_engine.initialize()

        await db_engine.close()

        assert db_engine._engine is None
        assert db_engine._session_factory is None
        assert db_engine._initialized is False

    async def test_dispose_calls_close(self, in_memory_config: DatabaseConfig) -> None:
        """Test that dispose() properly closes the engine.

        This ensures that the DI container can call dispose() during shutdown
        to prevent connection termination errors.
        """
        db_engine = DatabaseEngine(in_memory_config)
        await db_engine.initialize()

        # Call dispose (what the DI container does during shutdown)
        await db_engine.dispose()

        # Verify engine is properly closed
        assert db_engine._engine is None
        assert db_engine._session_factory is None
        assert db_engine._initialized is False


class TestModuleFunctions:
    """Tests for module-level convenience functions."""

    @pytest.fixture
    def in_memory_config(self) -> DatabaseConfig:
        """Create in-memory SQLite config for testing."""
        return DatabaseConfig(url="sqlite+aiosqlite:///:memory:")

    async def test_init_database(self, in_memory_config: DatabaseConfig) -> None:
        """Test init_database function."""
        engine = await init_database(in_memory_config)

        assert engine is not None
        assert engine._initialized is True

        await engine.close()

    async def test_get_async_session(self, in_memory_config: DatabaseConfig) -> None:
        """Test get_async_session function."""
        engine = await init_database(in_memory_config)

        async with get_async_session(engine) as session:
            assert isinstance(session, AsyncSession)

        await engine.close()
