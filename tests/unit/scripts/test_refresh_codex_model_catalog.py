"""Tests for the Codex model catalog snapshot refresh script."""

from __future__ import annotations

import importlib.util
import json
from pathlib import Path
from types import ModuleType
from typing import Any

import pytest

_REPO_ROOT = Path(__file__).resolve().parents[3]
_CATALOG_DIR = _REPO_ROOT / "src" / "connectors" / "openai_codex" / "catalog"
_DISCOVERY_MODULE = _CATALOG_DIR / "discovery_service.py"
_SCRIPT = _REPO_ROOT / "scripts" / "refresh_codex_model_catalog.py"

_RAW_CATALOG = {
    "models": [
        {
            "slug": "gpt-6-sol",
            "default_reasoning_level": "low",
            "supported_reasoning_levels": [{"effort": "xhigh", "description": "d"}],
            "visibility": "list",
            "supported_in_api": True,
        },
        {
            "slug": "gpt-6-luna",
            "visibility": "list",
            "supported_in_api": True,
        },
        {
            "slug": "codex-hidden",
            "visibility": "hide",
            "supported_in_api": True,
        },
        {
            "slug": "cli-only",
            "visibility": "list",
            "supported_in_api": False,
        },
    ]
}


def _load_refresh_script() -> ModuleType:
    spec = importlib.util.spec_from_file_location(
        "refresh_codex_model_catalog", _SCRIPT
    )
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


_refresh = _load_refresh_script()


class _FakeEndpoint:
    calls: list[dict[str, Any]] = []
    result: dict[str, Any] | None = None
    raises: BaseException | None = None

    def __init__(
        self,
        *,
        client_version: str,
        auth_path: Path | None,
        timeout_seconds: float,
    ) -> None:
        type(self).calls.append(
            {
                "client_version": client_version,
                "auth_path": auth_path,
                "timeout_seconds": timeout_seconds,
            }
        )

    async def fetch(self) -> dict[str, Any] | None:
        raises = type(self).raises
        if raises is not None:
            raise raises
        return type(self).result


@pytest.fixture()
def fake_endpoint(monkeypatch):
    _FakeEndpoint.calls = []
    _FakeEndpoint.result = _RAW_CATALOG
    _FakeEndpoint.raises = None
    monkeypatch.setattr(_refresh, "CodexCatalogEndpointClient", _FakeEndpoint)
    return _FakeEndpoint


class TestRefreshWritesSnapshot:
    def test_writes_normalized_json(self, tmp_path: Path, fake_endpoint) -> None:
        output = tmp_path / "catalog.json"
        rc = _refresh.main(["--output", str(output)])

        assert rc == 0
        assert output.read_text(encoding="utf-8") == (
            json.dumps(_RAW_CATALOG, indent=2, ensure_ascii=False) + "\n"
        )

    def test_reports_routable_slugs(
        self, tmp_path: Path, fake_endpoint, capsys
    ) -> None:
        output = tmp_path / "catalog.json"
        _refresh.main(["--output", str(output)])

        out = capsys.readouterr().out
        assert "gpt-6-sol" in out
        assert "gpt-6-luna" in out
        assert "codex-hidden" not in out
        assert "cli-only" not in out


class TestArgumentForwarding:
    def test_forwards_client_version_auth_path_timeout(
        self, tmp_path: Path, fake_endpoint
    ) -> None:
        output = tmp_path / "catalog.json"
        rc = _refresh.main(
            [
                "--output",
                str(output),
                "--client-version",
                "0.155.0",
                "--auth-path",
                "/custom/auth.json",
                "--timeout",
                "12.5",
            ]
        )

        assert rc == 0
        assert fake_endpoint.calls == [
            {
                "client_version": "0.155.0",
                "auth_path": Path("/custom/auth.json"),
                "timeout_seconds": 12.5,
            }
        ]

    def test_defaults(self, tmp_path: Path, fake_endpoint) -> None:
        output = tmp_path / "catalog.json"
        _refresh.main(["--output", str(output)])

        assert fake_endpoint.calls[0]["client_version"] == "0.156.0"
        assert fake_endpoint.calls[0]["auth_path"] is None
        assert fake_endpoint.calls[0]["timeout_seconds"] == 30.0


class TestRefreshFailures:
    def test_none_fetch_returns_nonzero_and_writes_nothing(
        self, tmp_path: Path, fake_endpoint
    ) -> None:
        fake_endpoint.result = None
        output = tmp_path / "catalog.json"

        assert _refresh.main(["--output", str(output)]) == 5
        assert not output.exists()

    def test_fetch_raises_returns_nonzero(self, tmp_path: Path, fake_endpoint) -> None:
        fake_endpoint.raises = RuntimeError("boom")
        output = tmp_path / "catalog.json"

        assert _refresh.main(["--output", str(output)]) == 5
        assert not output.exists()


class TestNoSubprocess:
    def test_script_does_not_reference_subprocess(self) -> None:
        source = _SCRIPT.read_text(encoding="utf-8")
        assert "subprocess" not in source
        assert not hasattr(_refresh, "subprocess")

    def test_discovery_module_does_not_import_subprocess(self) -> None:
        source = _DISCOVERY_MODULE.read_text(encoding="utf-8")
        assert "subprocess" not in source

    def test_catalog_package_does_not_resolve_executables(self) -> None:
        for path in _CATALOG_DIR.glob("*.py"):
            source = path.read_text(encoding="utf-8")
            assert "subprocess" not in source, path
            assert "candidate_codex_executables" not in source, path
