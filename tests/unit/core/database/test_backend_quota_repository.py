import pytest
from src.core.database.config import DatabaseConfig
from src.core.database.engine import DatabaseEngine
from src.core.database.repositories.backend_quota_repository import (
    BackendQuotaRepository,
)


@pytest.fixture
async def engine():
    config = DatabaseConfig(url="sqlite+aiosqlite:///:memory:")
    engine = DatabaseEngine(config)
    await engine.initialize()
    yield engine
    await engine.close()


@pytest.mark.asyncio
async def test_upsert_and_get_all_quotas(engine):
    repo = BackendQuotaRepository(engine)

    # Test insert
    headers = {"x-codex-primary-used-percent": "50.0"}
    await repo.upsert_quota("openai", headers)

    quotas = await repo.get_all_quotas()
    assert "openai" in quotas
    assert quotas["openai"]["x-codex-primary-used-percent"] == "50.0"

    # Test update
    updated_headers = {"x-codex-primary-used-percent": "60.0", "new-header": "val"}
    await repo.upsert_quota("openai", updated_headers)

    quotas = await repo.get_all_quotas()
    assert quotas["openai"]["x-codex-primary-used-percent"] == "60.0"
    assert quotas["openai"]["new-header"] == "val"
