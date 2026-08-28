"""Tests for ``CodexCatalogDiscoveryService`` — subprocess ``codex debug models``."""

from __future__ import annotations

import json
import subprocess
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


def _completed(
    *, returncode: int = 0, stdout: str = "", stderr: str = ""
) -> subprocess.CompletedProcess:
    return subprocess.CompletedProcess(
        args=["codex", "debug", "models"],
        returncode=returncode,
        stdout=stdout,
        stderr=stderr,
    )


class _RecordingRun:
    """Fake ``subprocess.run`` that records kwargs and returns/raises configured."""

    def __init__(
        self,
        *,
        result: subprocess.CompletedProcess | None = None,
        raises: BaseException | None = None,
    ) -> None:
        self.result = result or _completed(returncode=0, stdout="{}", stderr="")
        self.raises = raises
        self.last_kwargs: dict[str, Any] | None = None
        self.last_cmd: list[str] | None = None
        self.calls = 0

    def __call__(
        self, cmd: list[str], *args: Any, **kwargs: Any
    ) -> subprocess.CompletedProcess:
        self.calls += 1
        self.last_cmd = list(cmd)
        self.last_kwargs = dict(kwargs)
        if self.raises is not None:
            raise self.raises
        return self.result


@pytest.fixture()
def fake_parser() -> FakeParser:
    return FakeParser(result=sentinel_catalog())


@pytest.fixture()
def patch_bin(monkeypatch):
    """Patch ``candidate_codex_executables`` to return a controlled candidate list."""

    def _patch(candidates: list[str]):
        recorded: dict[str, Any] = {}

        def fake(configured: str | None) -> list[str]:
            recorded["configured"] = configured
            return candidates

        monkeypatch.setattr(discovery_module, "candidate_codex_executables", fake)
        return recorded

    return _patch


class TestDiscoverySuccess:
    @pytest.mark.asyncio
    async def test_discover_success_parses_stdout(
        self, monkeypatch, patch_bin, fake_parser, raw_catalog
    ) -> None:
        patch_bin(["/fake/codex"])
        run = _RecordingRun(
            result=_completed(returncode=0, stdout=json.dumps(raw_catalog))
        )
        monkeypatch.setattr(subprocess, "run", run)

        service = CodexCatalogDiscoveryService(
            codex_binary_path=None, timeout_seconds=10.0, parser=fake_parser
        )

        result = await service.discover()

        assert result is fake_parser._result
        assert fake_parser.calls == 1
        assert fake_parser.last_raw == raw_catalog
        assert run.last_cmd == ["/fake/codex", "debug", "models"]

    @pytest.mark.asyncio
    async def test_discover_passes_timeout_to_subprocess(
        self, monkeypatch, patch_bin, fake_parser, raw_catalog
    ) -> None:
        patch_bin(["/fake/codex"])
        run = _RecordingRun(
            result=_completed(returncode=0, stdout=json.dumps(raw_catalog))
        )
        monkeypatch.setattr(subprocess, "run", run)

        service = CodexCatalogDiscoveryService(timeout_seconds=5.0, parser=fake_parser)
        await service.discover()

        assert run.last_kwargs is not None
        assert run.last_kwargs.get("timeout") == 5.0

    @pytest.mark.asyncio
    async def test_discover_uses_configured_binary_path(
        self, monkeypatch, patch_bin, fake_parser, raw_catalog
    ) -> None:
        recorded = patch_bin(["/fake/codex"])
        monkeypatch.setattr(
            subprocess,
            "run",
            _RecordingRun(result=_completed(stdout=json.dumps(raw_catalog))),
        )

        service = CodexCatalogDiscoveryService(
            codex_binary_path="/custom/codex", parser=fake_parser
        )
        await service.discover()

        assert recorded["configured"] == "/custom/codex"

    @pytest.mark.asyncio
    async def test_discover_default_parser_parses_real_catalog(
        self, monkeypatch, patch_bin, raw_catalog
    ) -> None:
        """Integration: default parser yields a queryable catalog."""
        patch_bin(["/fake/codex"])
        monkeypatch.setattr(
            subprocess,
            "run",
            _RecordingRun(result=_completed(stdout=json.dumps(raw_catalog))),
        )

        service = CodexCatalogDiscoveryService()
        catalog = await service.discover()

        assert catalog is not None
        assert catalog.routable_slugs() == ("gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5")


class TestDiscoveryFailuresReturnNone:
    @pytest.mark.asyncio
    async def test_binary_missing_returns_none(self, patch_bin, fake_parser) -> None:
        patch_bin([])

        service = CodexCatalogDiscoveryService(parser=fake_parser)
        result = await service.discover()

        assert result is None
        assert fake_parser.calls == 0

    @pytest.mark.asyncio
    async def test_timeout_returns_none(
        self, monkeypatch, patch_bin, fake_parser
    ) -> None:
        patch_bin(["/fake/codex"])
        monkeypatch.setattr(
            subprocess,
            "run",
            _RecordingRun(raises=subprocess.TimeoutExpired(cmd=["codex"], timeout=10)),
        )

        service = CodexCatalogDiscoveryService(parser=fake_parser)
        assert await service.discover() is None
        assert fake_parser.calls == 0

    @pytest.mark.asyncio
    async def test_nonzero_exit_returns_none(
        self, monkeypatch, patch_bin, fake_parser
    ) -> None:
        patch_bin(["/fake/codex"])
        monkeypatch.setattr(
            subprocess,
            "run",
            _RecordingRun(result=_completed(returncode=1, stderr="boom")),
        )

        service = CodexCatalogDiscoveryService(parser=fake_parser)
        assert await service.discover() is None
        assert fake_parser.calls == 0

    @pytest.mark.asyncio
    async def test_malformed_stdout_returns_none(
        self, monkeypatch, patch_bin, fake_parser
    ) -> None:
        patch_bin(["/fake/codex"])
        monkeypatch.setattr(
            subprocess, "run", _RecordingRun(result=_completed(stdout="not json"))
        )

        service = CodexCatalogDiscoveryService(parser=fake_parser)
        assert await service.discover() is None
        assert fake_parser.calls == 0

    @pytest.mark.asyncio
    async def test_empty_stdout_returns_none(
        self, monkeypatch, patch_bin, fake_parser
    ) -> None:
        patch_bin(["/fake/codex"])
        monkeypatch.setattr(
            subprocess, "run", _RecordingRun(result=_completed(stdout=""))
        )

        service = CodexCatalogDiscoveryService(parser=fake_parser)
        assert await service.discover() is None

    @pytest.mark.asyncio
    async def test_oserror_returns_none(
        self, monkeypatch, patch_bin, fake_parser
    ) -> None:
        patch_bin(["/fake/codex"])
        monkeypatch.setattr(
            subprocess, "run", _RecordingRun(raises=OSError("spawn failed"))
        )

        service = CodexCatalogDiscoveryService(parser=fake_parser)
        assert await service.discover() is None


def test_discovery_service_satisfies_protocol() -> None:
    from src.connectors.openai_codex.catalog.interfaces import (
        ICodexCatalogDiscoveryService,
    )

    assert isinstance(
        CodexCatalogDiscoveryService(parser=CodexCatalogParser()),
        ICodexCatalogDiscoveryService,
    )
