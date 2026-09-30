"""Capability-driven personal-OAuth classification for openai-chatgpt-plan.

Task 1.2: classify the new connector via BackendCapabilityDescriptor flags
without adding it to KNOWN_OAUTH_CONNECTORS or _PERSONAL_BACKEND_TYPES.
"""

from __future__ import annotations

import sys
from collections.abc import Iterator
from contextlib import contextmanager
from types import ModuleType, SimpleNamespace

from src.core.domain.request_context import RequestContext
from src.core.services.backend_registry import BackendRegistry

BACKEND_TYPE = "openai-chatgpt-plan"


@contextmanager
def _isolated_connector_modules() -> Iterator[None]:
    import src
    from src.core.services.backend_discovery import reset_backend_discovery_state

    original_modules: dict[str, ModuleType] = {
        key: module
        for key, module in sys.modules.items()
        if key.startswith("src.connectors")
    }
    original_connectors_package = getattr(src, "connectors", None)
    for key in list(original_modules):
        sys.modules.pop(key, None)
    reset_backend_discovery_state()
    try:
        yield
    finally:
        for key in list(sys.modules):
            if key.startswith("src.connectors"):
                sys.modules.pop(key, None)
        sys.modules.update(original_modules)
        if original_connectors_package is not None:
            src.connectors = original_connectors_package
        reset_backend_discovery_state()


def _discover_builtins(monkeypatch: object, access_mode: str) -> BackendRegistry:
    from src.core.services import backend_registry as registry_module

    monkeypatch.setenv("LLM_PROXY_ACCESS_MODE", access_mode)  # type: ignore[attr-defined]
    test_registry = BackendRegistry()
    original_registry = registry_module.backend_registry
    with _isolated_connector_modules():
        try:
            registry_module.backend_registry = test_registry
            import src.connectors as connectors

            connectors.ensure_builtin_connectors_discovered()
            return test_registry
        finally:
            registry_module.backend_registry = original_registry


class TestChatGPTPlanCapabilityClassification:
    def test_name_lists_do_not_contain_chatgpt_plan(self) -> None:
        from src.connectors.oauth_detector import KNOWN_OAUTH_CONNECTORS
        from src.core.services.resilience.scope import _PERSONAL_BACKEND_TYPES

        assert BACKEND_TYPE not in KNOWN_OAUTH_CONNECTORS
        assert "openai_chatgpt_plan" not in KNOWN_OAUTH_CONNECTORS
        assert BACKEND_TYPE not in _PERSONAL_BACKEND_TYPES

    def test_registration_declares_both_generic_flags(self) -> None:
        from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector

        descriptor = OpenAIChatGPTPlanConnector.capability_descriptor
        assert descriptor.is_oauth_based is True
        assert descriptor.requires_personal_auth is True


class TestChatGPTPlanMultiUserAvailability:
    def test_multi_user_discovery_skips_chatgpt_plan_via_capability(
        self, monkeypatch: object
    ) -> None:
        from src.core.common.backend_discovery_state import get_skipped_oauth_connectors

        registry = _discover_builtins(monkeypatch, "multi_user")
        registered = registry.get_registered_backends()
        skipped = get_skipped_oauth_connectors()

        assert BACKEND_TYPE not in registered
        assert "openai" in registered
        assert "openai-responses" in registered
        skipped_normalized = {name.replace("_", "-") for name in skipped}
        assert BACKEND_TYPE in skipped_normalized

    def test_single_user_discovery_still_registers_chatgpt_plan(
        self, monkeypatch: object
    ) -> None:
        registry = _discover_builtins(monkeypatch, "single_user")
        assert BACKEND_TYPE in registry.get_registered_backends()
        assert "openai" in registry.get_registered_backends()

    def test_multi_user_factory_lookup_reports_capability_unavailability(
        self, monkeypatch: object
    ) -> None:
        registry = _discover_builtins(monkeypatch, "multi_user")
        try:
            registry.get_backend_factory(BACKEND_TYPE)
            raised = False
            error_msg = ""
        except ValueError as exc:
            raised = True
            error_msg = str(exc)

        assert raised is True
        assert BACKEND_TYPE in error_msg
        assert "Multi User Mode" in error_msg
        assert "personal" in error_msg.lower() or "OAuth" in error_msg


class TestChatGPTPlanResilienceCapabilityScoping:
    def test_personal_scope_uses_capability_not_codex_or_oauth_substring(self) -> None:
        from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector
        from src.core.services.resilience.scope import (
            _PERSONAL_BACKEND_TYPES,
            build_resilience_instance_id,
            is_personal_backend_type,
        )

        assert OpenAIChatGPTPlanConnector.capability_descriptor.requires_personal_auth
        assert BACKEND_TYPE not in _PERSONAL_BACKEND_TYPES
        assert "oauth" not in BACKEND_TYPE
        assert "codex" not in BACKEND_TYPE
        assert is_personal_backend_type(BACKEND_TYPE) is True

        context = RequestContext(
            headers={},
            cookies={},
            state={},
            app_state=SimpleNamespace(),
            session_id="session-a",
        )
        assert (
            build_resilience_instance_id(BACKEND_TYPE, context)
            == f"{BACKEND_TYPE}:session-a"
        )

    def test_unrelated_backend_without_flags_keeps_previous_behavior(self) -> None:
        from src.core.services.resilience.scope import (
            build_resilience_instance_id,
            is_personal_backend_type,
        )

        context = RequestContext(
            headers={},
            cookies={},
            state={},
            app_state=SimpleNamespace(),
            session_id="session-a",
        )
        assert is_personal_backend_type("openai") is False
        assert build_resilience_instance_id("openai", context) == "openai"
        assert is_personal_backend_type("qwen-oauth") is True
        assert is_personal_backend_type("openai-codex") is True

    def test_config_capability_marks_backend_personal_without_name_list(self) -> None:
        from src.core.config.app_config import AppConfig
        from src.core.config.models.backends import BackendConfig, BackendSettings
        from src.core.services.resilience.scope import (
            _PERSONAL_BACKEND_TYPES,
            is_personal_backend_type,
        )

        backend_name = "custom-plan-backend"
        assert backend_name not in _PERSONAL_BACKEND_TYPES
        assert "oauth" not in backend_name
        assert "codex" not in backend_name

        settings = BackendSettings(
            **{
                backend_name: BackendConfig(
                    capability_descriptor={
                        "is_oauth_based": True,
                        "requires_personal_auth": True,
                    }
                )
            }
        )
        app_config = AppConfig(backends=settings)
        context = RequestContext(
            headers={},
            cookies={},
            state={},
            app_state=SimpleNamespace(app_config=app_config),
        )
        assert is_personal_backend_type(backend_name, context) is True
        assert is_personal_backend_type("openai", context) is False
