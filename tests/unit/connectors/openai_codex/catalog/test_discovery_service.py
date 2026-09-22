"""Tests for ``CodexCatalogDiscoveryService`` — authenticated endpoint discovery."""

from __future__ import annotations

from pathlib import Path
from typing import Any

import pytest
from src.connectors.openai_codex.catalog import discovery_service as discovery_module
from src.connectors.openai_codex.catalog.discovery_service import (
    CodexCatalogDiscoveryService,
)
from src.connectors.openai_codex.catalog.parser import CodexCatalogParser

from tests.unit.connectors.openai_codex.catalog.conftest import (
    FakeParser,
    sentinel_catalog,
)


class _FakeEndpointClient:
    """Endpoint-client fake recording fetch calls."""

    def __init__(
        self,
        *,
        result: dict[str, Any] | None = None,
        raises: BaseException | None = None,
    ) -> None:
        self._result = result
        self._raises = raises
        self.calls = 0

    async def fetch(self) -> dict[str, Any] | None:
        self.calls += 1
        if self._raises is not None:
            raise self._raises
        return self._result


@pytest.fixture()
def fake_parser() -> FakeParser:
    return FakeParser(result=sentinel_catalog())


class TestDiscoverySuccess:
    @pytest.mark.asyncio
    async def test_discover_success_parses_fetched_raw(
        self, fake_parser, raw_catalog
    ) -> None:
        endpoint = _FakeEndpointClient(result=raw_catalog)
        service = CodexCatalogDiscoveryService(
            endpoint_client=endpoint, parser=fake_parser
        )

        result = await service.discover()

        assert result is fake_parser._result
        assert fake_parser.calls == 1
        assert fake_parser.last_raw == raw_catalog
        assert endpoint.calls == 1

    @pytest.mark.asyncio
    async def test_discover_default_parser_parses_real_catalog(
        self, raw_catalog
    ) -> None:
        endpoint = _FakeEndpointClient(result=raw_catalog)
        service = CodexCatalogDiscoveryService(endpoint_client=endpoint)

        catalog = await service.discover()

        assert catalog is not None
        assert catalog.routable_slugs() == ("gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5")

    @pytest.mark.asyncio
    async def test_default_endpoint_client_receives_config(
        self, monkeypatch, fake_parser
    ) -> None:
        recorded: dict[str, Any] = {}

        class _RecordingEndpoint:
            def __init__(
                self,
                *,
                client_version: str,
                auth_path: Path | None,
                timeout_seconds: float,
            ) -> None:
                recorded["client_version"] = client_version
                recorded["auth_path"] = auth_path
                recorded["timeout_seconds"] = timeout_seconds

            async def fetch(self) -> dict[str, Any] | None:
                return None

        monkeypatch.setattr(
            discovery_module, "CodexCatalogEndpointClient", _RecordingEndpoint
        )

        service = CodexCatalogDiscoveryService(
            client_version="9.9.9",
            auth_path=Path("/x/auth.json"),
            timeout_seconds=3.5,
            parser=fake_parser,
        )
        await service.discover()

        assert recorded == {
            "client_version": "9.9.9",
            "auth_path": Path("/x/auth.json"),
            "timeout_seconds": 3.5,
        }


class TestDiscoveryFailuresReturnNone:
    @pytest.mark.asyncio
    async def test_endpoint_returns_none(self, fake_parser) -> None:
        endpoint = _FakeEndpointClient(result=None)
        service = CodexCatalogDiscoveryService(
            endpoint_client=endpoint, parser=fake_parser
        )

        assert await service.discover() is None
        assert fake_parser.calls == 0

    @pytest.mark.asyncio
    async def test_endpoint_raises_falls_back(self, fake_parser) -> None:
        endpoint = _FakeEndpointClient(raises=RuntimeError("boom"))
        service = CodexCatalogDiscoveryService(
            endpoint_client=endpoint, parser=fake_parser
        )

        assert await service.discover() is None
        assert fake_parser.calls == 0

    @pytest.mark.asyncio
    async def test_parser_raises_falls_back(self) -> None:
        class _BoomParser:
            def parse(self, raw: Any) -> Any:
                raise ValueError("bad catalog")

        endpoint = _FakeEndpointClient(result={"models": []})
        service = CodexCatalogDiscoveryService(
            endpoint_client=endpoint, parser=_BoomParser()
        )

        assert await service.discover() is None


def test_discovery_service_satisfies_protocol() -> None:
    from src.connectors.openai_codex.catalog.interfaces import (
        ICodexCatalogDiscoveryService,
    )

    assert isinstance(
        CodexCatalogDiscoveryService(parser=CodexCatalogParser()),
        ICodexCatalogDiscoveryService,
    )
