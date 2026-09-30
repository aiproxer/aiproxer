"""Structural/contract tests proving openai_chatgpt_plan has no Codex dependency."""

from __future__ import annotations

import ast
from pathlib import Path
from unittest.mock import MagicMock

from src.connectors.openai_chatgpt_plan.connector import (
    PUBLIC_OPENAI_API_BASE,
    OpenAIChatGPTPlanConnector,
    _strip_codex_outbound_headers,
)
from src.core.config.app_config import AppConfig
from src.core.services.translation_service import TranslationService

PACKAGE_ROOT = (
    Path(__file__).resolve().parents[3] / "src" / "connectors" / "openai_chatgpt_plan"
)


def _iter_package_py_files() -> list[Path]:
    return sorted(p for p in PACKAGE_ROOT.rglob("*.py") if p.is_file())


def _imported_module_names(path: Path) -> list[str]:
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    names: list[str] = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            names.extend(alias.name for alias in node.names)
        elif isinstance(node, ast.ImportFrom) and node.module:
            names.append(node.module)
            names.extend(
                f"{node.module}.{alias.name}"
                for alias in node.names
                if alias.name != "*"
            )
    return names


def test_package_does_not_import_openai_codex_or_resources() -> None:
    hits: list[str] = []
    for path in _iter_package_py_files():
        for name in _imported_module_names(path):
            folded = name.casefold()
            if (
                "openai_codex" in folded
                or "resources.codex" in folded
                or folded.endswith(".codex")
                and "openai_chatgpt_plan" not in folded
            ):
                hits.append(f"{path}:{name}")
    assert hits == []


def test_package_does_not_call_private_codex_backend_api_url() -> None:
    forbidden = "chatgpt.com/backend-api/codex"
    hits = [
        str(p)
        for p in _iter_package_py_files()
        if forbidden in p.read_text(encoding="utf-8")
    ]
    assert hits == []


def test_public_api_base_is_openai_v1() -> None:
    assert PUBLIC_OPENAI_API_BASE == "https://api.openai.com/v1"


def test_strip_codex_outbound_headers_removes_private_headers() -> None:
    cleaned = _strip_codex_outbound_headers(
        {
            "Authorization": "Bearer token",
            "Content-Type": "application/json",
            "originator": "codex_cli_rs",
            "session_id": "abc",
            "codex-task-type": "something",
            "chatgpt-account-id": "acct",
            "User-Agent": "codex_cli_rs/1.0",
            "X-Custom": "keep-me",
        }
    )
    lowered = {k.lower(): v for k, v in cleaned.items()}
    assert lowered["authorization"] == "Bearer token"
    assert lowered["content-type"] == "application/json"
    assert lowered["x-custom"] == "keep-me"
    for forbidden in (
        "originator",
        "session_id",
        "codex-task-type",
        "chatgpt-account-id",
        "user-agent",
    ):
        assert forbidden not in lowered


def test_get_headers_does_not_emit_codex_private_headers() -> None:
    connector = OpenAIChatGPTPlanConnector(
        client=MagicMock(),
        config=AppConfig(),
        translation_service=TranslationService(),
    )
    headers = connector.get_headers(identity=None)
    lowered = {k.lower() for k in headers}
    for forbidden in (
        "originator",
        "session_id",
        "codex-task-type",
        "chatgpt-account-id",
        "conversation_id",
    ):
        assert forbidden not in lowered
    ua = headers.get("User-Agent") or headers.get("user-agent") or ""
    assert "codex_cli_rs" not in ua.casefold()
