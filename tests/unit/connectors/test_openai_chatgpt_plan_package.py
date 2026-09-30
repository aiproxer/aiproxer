"""Structural and registration contracts for the ChatGPT-plan connector package.

Task 1.1: package existence, thin Responses facade, backend registration,
vendor-prefix routing, import side-effect safety, and isolation from the
legacy Codex connector family.
"""

from __future__ import annotations

import ast
import importlib
import sys
from collections.abc import Iterator
from pathlib import Path
from types import ModuleType
from typing import Any
from unittest.mock import AsyncMock, patch

import httpx
from src.connectors.base import add_vendor_prefix
from src.core.config.app_config import AppConfig
from src.core.services.backend_registry import BackendRegistry
from src.core.services.translation_service import TranslationService

PACKAGE_NAME = "src.connectors.openai_chatgpt_plan"
CONNECTOR_CLASS_NAME = "OpenAIChatGPTPlanConnector"
BACKEND_TYPE = "openai-chatgpt-plan"
PACKAGE_DIR = (
    Path(__file__).resolve().parents[3] / "src" / "connectors" / "openai_chatgpt_plan"
)

FORBIDDEN_MODULE_PREFIXES = (
    "src.connectors.openai_codex",
    "src.connectors.openai_codex_v2",
    "src.connectors._openai_codex_connector",
    "src.connectors._openai_codex_v2_connector",
    "src.connectors.openai_codex_app_server",
    "src.connectors.codex_event_mapper",
    "src.connectors.codex_helpers",
    "src.resources.codex",
)

FORBIDDEN_MODULE_TAILS = (
    "openai_codex",
    "_openai_codex_connector",
    "_openai_codex_v2_connector",
    "openai_codex_app_server",
    "codex_event_mapper",
    "codex_helpers",
)


def _is_forbidden_module_name(name: str) -> bool:
    normalized = name.strip()
    if not normalized:
        return False
    for prefix in FORBIDDEN_MODULE_PREFIXES:
        if normalized == prefix or normalized.startswith(f"{prefix}."):
            return True
    tail = normalized.rsplit(".", 1)[-1]
    return tail in FORBIDDEN_MODULE_TAILS


def _iter_package_python_files() -> list[Path]:
    assert (
        PACKAGE_DIR.is_dir()
    ), f"Expected ChatGPT-plan connector package at {PACKAGE_DIR}"
    return sorted(path for path in PACKAGE_DIR.rglob("*.py") if path.is_file())


def _imported_names_from_source(source: str) -> set[str]:
    tree = ast.parse(source)
    names: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            names.update(alias.name for alias in node.names)
        elif isinstance(node, ast.ImportFrom) and node.module:
            names.add(node.module)
    return names


def _unload_chatgpt_plan_modules() -> None:
    for key in list(sys.modules):
        if key == PACKAGE_NAME or key.startswith(f"{PACKAGE_NAME}."):
            sys.modules.pop(key, None)


def _iter_reachable_src_modules(root: ModuleType) -> Iterator[str]:
    seen: set[int] = set()
    stack: list[ModuleType] = [root]
    while stack:
        module = stack.pop()
        module_id = id(module)
        if module_id in seen:
            continue
        seen.add(module_id)
        name = getattr(module, "__name__", "") or ""
        if name.startswith("src."):
            yield name
        for value in vars(module).values():
            if isinstance(value, ModuleType):
                value_name = getattr(value, "__name__", "") or ""
                if value_name.startswith("src."):
                    stack.append(value)


def _make_connector() -> Any:
    from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector

    connector = OpenAIChatGPTPlanConnector(
        client=AsyncMock(spec=httpx.AsyncClient),
        config=AppConfig(),
        translation_service=TranslationService(),
    )
    connector.disable_health_check()
    return connector


class TestOpenAIChatGPTPlanPackageStructure:
    def test_package_exports_connector_class(self) -> None:
        module = importlib.import_module(PACKAGE_NAME)
        connector_cls = getattr(module, CONNECTOR_CLASS_NAME, None)
        assert connector_cls is not None
        assert isinstance(connector_cls, type)
        assert connector_cls.__name__ == CONNECTOR_CLASS_NAME

    def test_connector_uses_public_responses_transport_not_codex(self) -> None:
        from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector
        from src.connectors.openai_responses import OpenAIResponsesConnector

        assert issubclass(OpenAIChatGPTPlanConnector, OpenAIResponsesConnector)
        assert OpenAIChatGPTPlanConnector.__mro__[1] is OpenAIResponsesConnector

        mro_modules = {cls.__module__ for cls in OpenAIChatGPTPlanConnector.__mro__}
        assert not any(_is_forbidden_module_name(name) for name in mro_modules)
        assert "src.connectors._openai_codex_connector" not in mro_modules
        assert "src.connectors._openai_codex_v2_connector" not in mro_modules

    def test_backend_type_is_openai_chatgpt_plan(self) -> None:
        from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector

        assert OpenAIChatGPTPlanConnector.backend_type == BACKEND_TYPE
        connector = _make_connector()
        assert connector.backend_type == BACKEND_TYPE

    def test_has_static_credentials_is_false(self) -> None:
        from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector

        connector = _make_connector()
        assert connector.has_static_credentials is False
        class_value = OpenAIChatGPTPlanConnector.__dict__.get("has_static_credentials")
        if class_value is not None and not isinstance(class_value, property):
            assert class_value is False

    def test_uses_public_openai_api_base(self) -> None:
        connector = _make_connector()
        assert connector.api_base_url == "https://api.openai.com/v1"

    def test_vendor_prefix_matches_openai_responses_family(self) -> None:
        from src.connectors.openai import OpenAIConnector
        from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector
        from src.connectors.openai_responses import OpenAIResponsesConnector

        assert OpenAIChatGPTPlanConnector.VENDOR_PREFIX == "openai"
        assert (
            OpenAIChatGPTPlanConnector.VENDOR_PREFIX
            == OpenAIResponsesConnector.VENDOR_PREFIX
        )
        assert OpenAIChatGPTPlanConnector.VENDOR_PREFIX == OpenAIConnector.VENDOR_PREFIX

        connector = _make_connector()
        connector.available_models = ["gpt-4o", "openai/gpt-4.1"]
        assert connector.get_available_models() == [
            add_vendor_prefix("gpt-4o", "openai"),
            add_vendor_prefix("openai/gpt-4.1", "openai"),
        ]

    def test_not_added_to_known_oauth_connector_name_list(self) -> None:
        from src.connectors.oauth_detector import KNOWN_OAUTH_CONNECTORS

        assert BACKEND_TYPE not in KNOWN_OAUTH_CONNECTORS
        assert "openai_chatgpt_plan" not in KNOWN_OAUTH_CONNECTORS


class TestOpenAIChatGPTPlanRegistration:
    def test_package_import_registers_backend_type(self) -> None:
        from src.core.services import backend_registry as registry_module

        original_registry = registry_module.backend_registry
        test_registry = BackendRegistry()
        _unload_chatgpt_plan_modules()
        try:
            registry_module.backend_registry = test_registry
            module = importlib.import_module(PACKAGE_NAME)
            registered = test_registry.get_registered_backends()
            assert BACKEND_TYPE in registered
            factory = test_registry.get_backend_factory(BACKEND_TYPE)
            assert factory is getattr(module, CONNECTOR_CLASS_NAME)
        finally:
            registry_module.backend_registry = original_registry
            _unload_chatgpt_plan_modules()

    def test_builtin_discovery_registers_backend_alongside_legacy_codex(self) -> None:
        from src.core.services import backend_registry as registry_module
        from src.core.services.backend_discovery import reset_backend_discovery_state

        original_registry = registry_module.backend_registry
        test_registry = BackendRegistry()
        original_modules = {
            key: module
            for key, module in sys.modules.items()
            if key.startswith("src.connectors")
        }
        for key in list(original_modules):
            sys.modules.pop(key, None)
        reset_backend_discovery_state()
        try:
            registry_module.backend_registry = test_registry
            import src.connectors as connectors

            connectors.ensure_builtin_connectors_discovered()
            registered = test_registry.get_registered_backends()
            assert BACKEND_TYPE in registered
            assert "openai-codex" in registered
            assert "openai-responses" in registered
            assert "openai" in registered
        finally:
            registry_module.backend_registry = original_registry
            for key in list(sys.modules):
                if key.startswith("src.connectors"):
                    sys.modules.pop(key, None)
            sys.modules.update(original_modules)
            reset_backend_discovery_state()


class TestOpenAIChatGPTPlanImportIsolation:
    def test_package_source_ast_does_not_import_legacy_codex(self) -> None:
        forbidden_hits: list[str] = []
        for path in _iter_package_python_files():
            imported = _imported_names_from_source(path.read_text(encoding="utf-8"))
            for name in sorted(imported):
                if _is_forbidden_module_name(name):
                    forbidden_hits.append(f"{path.as_posix()}: {name}")
        assert forbidden_hits == []

    def test_import_graph_does_not_load_legacy_codex_or_app_server(self) -> None:
        _unload_chatgpt_plan_modules()
        before = {name for name in sys.modules if _is_forbidden_module_name(name)}
        module = importlib.import_module(PACKAGE_NAME)
        after = set(sys.modules)
        newly_forbidden = sorted(
            name for name in (after - before) if _is_forbidden_module_name(name)
        )
        reachable = sorted(
            name
            for name in _iter_reachable_src_modules(module)
            if _is_forbidden_module_name(name)
        )
        assert newly_forbidden == [], newly_forbidden
        assert reachable == [], reachable
        still_only_preexisting = {
            name for name in after if _is_forbidden_module_name(name)
        }
        assert still_only_preexisting <= before

    def test_import_does_not_open_browser_refresh_tokens_or_prompt(self) -> None:
        _unload_chatgpt_plan_modules()
        side_effects: list[str] = []

        def _record(name: str) -> Any:
            def _inner(*args: Any, **kwargs: Any) -> Any:
                side_effects.append(name)
                raise AssertionError(f"unexpected import-time call: {name}")

            return _inner

        with (
            patch("webbrowser.open", side_effect=_record("webbrowser.open")),
            patch("builtins.input", side_effect=_record("input")),
            patch.object(
                httpx.Client, "request", side_effect=_record("httpx.Client.request")
            ),
            patch.object(
                httpx.Client, "send", side_effect=_record("httpx.Client.send")
            ),
            patch.object(
                httpx.AsyncClient,
                "request",
                side_effect=_record("httpx.AsyncClient.request"),
            ),
            patch.object(
                httpx.AsyncClient, "send", side_effect=_record("httpx.AsyncClient.send")
            ),
        ):
            importlib.import_module(PACKAGE_NAME)

        assert side_effects == []
