"""
Tests for backend auto-discovery mechanism.

These tests verify that backends are automatically discovered and registered
without requiring hardcoded imports.
"""

import sys
from collections.abc import Iterator
from contextlib import contextmanager
from importlib.util import find_spec
from pathlib import Path
from types import ModuleType

import pytest
from src.core.services.backend_registry import BackendRegistry


@contextmanager
def _isolated_connector_modules() -> Iterator[None]:
    """Temporarily drop ``src.connectors*`` from ``sys.modules``, then restore.

    Other tests may already hold references to connector classes/protocols.
    Leaving freshly re-imported replacements in ``sys.modules`` breaks later
    identity-based equality and DI lookups (e.g. Codex catalog stage tests).
    """
    import src

    original_modules: dict[str, ModuleType] = {
        key: module
        for key, module in sys.modules.items()
        if key.startswith("src.connectors")
    }
    original_connectors_package = getattr(src, "connectors", None)

    for key in list(original_modules):
        sys.modules.pop(key, None)

    from src.core.services.backend_discovery import reset_backend_discovery_state

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


class TestBackendAutoDiscovery:
    """Test suite for backend auto-discovery functionality."""

    def test_all_backends_auto_registered(self):
        """Test that all backend modules are automatically discovered and registered."""
        # Create a fresh registry for testing
        test_registry = BackendRegistry()

        # Store the original global registry
        from src.core.services import backend_registry as registry_module

        original_registry = registry_module.backend_registry

        with _isolated_connector_modules():
            try:
                # Replace with test registry
                registry_module.backend_registry = test_registry

                # Import connectors and trigger the lazy discovery hook explicitly.
                import src.connectors as connectors

                connectors.ensure_builtin_connectors_discovered()

                # Get all registered backends
                registered = test_registry.get_registered_backends()

                # Verify we have backends registered
                assert len(registered) > 0, "No backends were auto-discovered"

                # Core backends expected to be available without optional OAuth plugins.
                expected_backends = [
                    "anthropic",
                    "gemini",
                    "gemini-cli-cloud-project",
                    "hybrid",
                    "minimax",
                    "openai",
                    "openai-codex",
                    "openai-responses",
                    "openrouter",
                    "zai",
                    "zai-coding-plan",
                    "zenmux",
                ]
                if find_spec("src.connectors.opencode_go") is not None:
                    expected_backends.append("opencode-go")

                # Check that all expected backends are registered
                for backend_name in expected_backends:
                    assert backend_name in registered, (
                        f"Backend '{backend_name}' was not auto-discovered. "
                        f"Registered backends: {registered}"
                    )

                # Verify no duplicates
                assert len(registered) == len(
                    set(registered)
                ), "Duplicate backends detected in registry"

                # Extracted OAuth connectors must not be loaded from core auto-discovery.
                extracted_oauth_backends = {
                    "antigravity-oauth",
                    "gemini-oauth-auto",
                    "gemini-oauth-free",
                    "gemini-oauth-plan",
                    "qwen-oauth",
                }
                leaked_oauth = extracted_oauth_backends.intersection(set(registered))
                assert not leaked_oauth, (
                    "Extracted OAuth backends leaked into core discovery: "
                    f"{sorted(leaked_oauth)}"
                )
            finally:
                registry_module.backend_registry = original_registry

    def test_backend_modules_discovered_without_hardcoded_imports(self):
        """Test that backend modules are discovered dynamically, not from hardcoded list."""
        # Read the connectors __init__.py file
        connectors_init = Path("src/connectors/__init__.py")
        content = connectors_init.read_text()

        # Verify it uses pkgutil.iter_modules for discovery
        assert (
            "pkgutil.iter_modules" in content
        ), "Backend discovery should use pkgutil.iter_modules for auto-discovery"

        # Verify it doesn't have hardcoded backend imports (except base)
        # Check that we're not importing specific backends by name
        forbidden_patterns = [
            "from .anthropic import",
            "from .openai import",
            "from .gemini_oauth_plan import",
            "from .openrouter import",
        ]

        for pattern in forbidden_patterns:
            assert pattern not in content, (
                f"Found hardcoded import '{pattern}' in connectors __init__.py. "
                "Backends should be auto-discovered, not hardcoded."
            )

    def test_core_discovery_does_not_mutate_sys_path_for_optional_plugins(self):
        """Core discovery must not inject optional plugin sources into sys.path."""
        connectors_init = Path("src/connectors/__init__.py")
        content = connectors_init.read_text(encoding="utf-8")

        assert "llm-interactive-proxy-oauth-connectors" not in content
        assert "sys.path.insert(" not in content

    def test_new_backend_would_be_auto_discovered(self, tmp_path):
        """Test that a new backend file would be automatically discovered."""
        # This test verifies the auto-discovery mechanism would work for new backends
        # by simulating the discovery process

        # Get the project root directory
        project_root = Path(__file__).parent.parent.parent
        connectors_path = project_root / "src" / "connectors"
        # Get all Python files in connectors directory (excluding utilities)
        skip_files = (
            "__init__",
            "base",
            "streaming_utils",
            "gemini_request_counter",
            "gemini_oauth_base",
            "gemini_cli_acp_types",  # Shared ACP typing helpers, not a backend
            "cursor_cli_auth",  # Cursor CLI dual-auth helpers, not a backend
            "oauth_detector",
            "openai_codex_config",  # Configuration file, not a backend
            "openai_websocket_client",  # WebSocket client utility, not a backend
            "codex_event_mapper",  # Codex notification->piece mapper, not a backend
            "codex_helpers",  # Codex pure helpers, not a backend
            # Extracted OAuth connectors are loaded through plugin entry points.
            "antigravity_oauth",
            "gemini_oauth_auto",
            "gemini_oauth_free",
            "gemini_oauth_plan",
            "qwen_oauth",
            "agy_acp_wrapper_installer",
            "freebuff_acp_wrapper_installer",
        )
        backend_files = [
            f.stem
            for f in connectors_path.glob("*.py")
            if f.stem not in skip_files and not f.stem.startswith("_")
        ]

        # Verify we found backend files
        assert len(backend_files) > 0, "No backend files found in src/connectors/"

        # Verify each backend file has register_backend call
        for backend_file in backend_files:
            backend_path = connectors_path / f"{backend_file}.py"
            content = backend_path.read_text()

            # Each backend should register itself
            assert "backend_registry.register_backend" in content, (
                f"Backend file '{backend_file}.py' doesn't call "
                "backend_registry.register_backend(). All backends must "
                "register themselves for auto-discovery to work."
            )

    def test_base_module_not_auto_imported(self):
        """Test that base.py is not auto-imported (it's imported explicitly)."""
        from src.core.services import backend_registry as registry_module

        test_registry = BackendRegistry()
        original_registry = registry_module.backend_registry

        with _isolated_connector_modules():
            try:
                registry_module.backend_registry = test_registry

                # Import connectors
                import src.connectors  # noqa: F401  # pyright: ignore[reportUnusedImport]

                # base.py should not register any backend
                registered = test_registry.get_registered_backends()
                assert "base" not in registered, "base.py should not register a backend"
            finally:
                registry_module.backend_registry = original_registry

    def test_failed_backend_import_doesnt_break_others(self):
        """Test that if one backend fails to import, others still load."""
        # This is more of a documentation test showing the resilient behavior
        # The actual implementation logs warnings but continues

        connectors_init = Path("src/connectors/__init__.py")
        content = connectors_init.read_text()

        # Verify we have exception handling
        assert (
            "except Exception" in content
        ), "Auto-discovery should handle import failures gracefully"
        assert "logger.warning" in content, "Failed imports should be logged"

    def test_backend_registry_singleton_pattern(self):
        """Test that backend_registry is a singleton."""
        from src.core.services.backend_registry import backend_registry as reg1
        from src.core.services.backend_registry import backend_registry as reg2

        # Should be the same instance
        assert reg1 is reg2, "backend_registry should be a singleton"


class TestBackendRegistryInterface:
    """Test the BackendRegistry class interface."""

    def test_register_backend_basic(self):
        """Test basic backend registration."""
        registry = BackendRegistry()

        from typing import Any

        from src.connectors.base import LLMBackend
        from src.core.domain.base import DomainModel
        from src.core.domain.responses import (
            ResponseEnvelope,
            StreamingResponseEnvelope,
        )
        from src.core.interfaces.configuration import IAppIdentityConfig
        from src.core.interfaces.model_bases import InternalDTO

        class MockBackend(LLMBackend):
            async def initialize(self, **kwargs: Any) -> None:
                pass

            def get_available_models(self) -> list[str]:
                return ["mock-model"]

            async def chat_completions(  # type: ignore[override]
                self,
                request_data: DomainModel | InternalDTO | dict[str, Any],
                processed_messages: list[dict[str, Any]],
                effective_model: str,
                identity: IAppIdentityConfig | None = None,
                cancellation_token: Any | None = None,
                cancellation_coordinator: Any | None = None,
                **kwargs: Any,
            ) -> ResponseEnvelope | StreamingResponseEnvelope:
                return ResponseEnvelope(content={}, usage=None)

            async def stream_chat_completions(
                self,
                request_data: DomainModel | InternalDTO | dict[str, Any],
                processed_messages: list[dict[str, Any]],
                effective_model: str,
                identity: IAppIdentityConfig | None = None,
                **kwargs: Any,
            ):
                yield {}

        def mock_factory() -> MockBackend:
            from src.core.config.app_config import AppConfig

            return MockBackend(config=AppConfig())

        registry.register_backend("test-backend", mock_factory)

        assert "test-backend" in registry.get_registered_backends()
        assert registry.get_backend_factory("test-backend") == mock_factory

    def test_register_backend_duplicate_same_factory_is_idempotent(self, caplog):
        """Re-registering with the same factory object is a no-op (DEBUG only)."""
        registry = BackendRegistry()

        from typing import Any

        from src.connectors.base import LLMBackend
        from src.core.domain.base import DomainModel
        from src.core.domain.responses import (
            ResponseEnvelope,
            StreamingResponseEnvelope,
        )
        from src.core.interfaces.configuration import IAppIdentityConfig
        from src.core.interfaces.model_bases import InternalDTO

        class MockBackend(LLMBackend):
            async def initialize(self, **kwargs: Any) -> None:
                pass

            def get_available_models(self) -> list[str]:
                return ["mock-model"]

            async def chat_completions(  # type: ignore[override]
                self,
                request_data: DomainModel | InternalDTO | dict[str, Any],
                processed_messages: list[dict[str, Any]],
                effective_model: str,
                identity: IAppIdentityConfig | None = None,
                cancellation_token: Any | None = None,
                cancellation_coordinator: Any | None = None,
                **kwargs: Any,
            ) -> ResponseEnvelope | StreamingResponseEnvelope:
                return ResponseEnvelope(content={}, usage=None)

            async def stream_chat_completions(
                self,
                request_data: DomainModel | InternalDTO | dict[str, Any],
                processed_messages: list[dict[str, Any]],
                effective_model: str,
                identity: IAppIdentityConfig | None = None,
                **kwargs: Any,
            ):
                yield {}

        def mock_factory() -> MockBackend:
            from src.core.config.app_config import AppConfig

            return MockBackend(config=AppConfig())

        assert registry.register_backend("test-backend", mock_factory) is True
        with caplog.at_level("DEBUG"):
            assert registry.register_backend("test-backend", mock_factory) is False

        assert "idempotent re-registration" in caplog.text
        assert len(registry.get_registered_backends()) == 1

    def test_register_backend_duplicate_different_factory_logs_warning(self, caplog):
        """Conflicting factory for the same name logs WARNING and keeps the first."""
        registry = BackendRegistry()

        from typing import Any

        from src.connectors.base import LLMBackend
        from src.core.domain.base import DomainModel
        from src.core.domain.responses import (
            ResponseEnvelope,
            StreamingResponseEnvelope,
        )
        from src.core.interfaces.configuration import IAppIdentityConfig
        from src.core.interfaces.model_bases import InternalDTO

        class MockBackend(LLMBackend):
            async def initialize(self, **kwargs: Any) -> None:
                pass

            def get_available_models(self) -> list[str]:
                return ["mock-model"]

            async def chat_completions(  # type: ignore[override]
                self,
                request_data: DomainModel | InternalDTO | dict[str, Any],
                processed_messages: list[dict[str, Any]],
                effective_model: str,
                identity: IAppIdentityConfig | None = None,
                cancellation_token: Any | None = None,
                cancellation_coordinator: Any | None = None,
                **kwargs: Any,
            ) -> ResponseEnvelope | StreamingResponseEnvelope:
                return ResponseEnvelope(content={}, usage=None)

            async def stream_chat_completions(
                self,
                request_data: DomainModel | InternalDTO | dict[str, Any],
                processed_messages: list[dict[str, Any]],
                effective_model: str,
                identity: IAppIdentityConfig | None = None,
                **kwargs: Any,
            ):
                yield {}

        def mock_factory_a() -> MockBackend:
            from src.core.config.app_config import AppConfig

            return MockBackend(config=AppConfig())

        def mock_factory_b() -> MockBackend:
            from src.core.config.app_config import AppConfig

            return MockBackend(config=AppConfig())

        assert registry.register_backend("test-backend", mock_factory_a) is True
        with caplog.at_level("WARNING"):
            assert registry.register_backend("test-backend", mock_factory_b) is False

        assert "different factory" in caplog.text
        assert registry.get_backend_factory("test-backend") is mock_factory_a

    def test_get_nonexistent_backend_raises_error(self):
        """Test that getting non-existent backend raises error."""
        registry = BackendRegistry()

        with pytest.raises(ValueError, match="not registered"):
            registry.get_backend_factory("nonexistent-backend")

    def test_register_backend_invalid_name_raises_error(self):
        """Test that invalid backend name raises error."""
        registry = BackendRegistry()

        from typing import Any

        from src.connectors.base import LLMBackend
        from src.core.domain.base import DomainModel
        from src.core.domain.responses import (
            ResponseEnvelope,
            StreamingResponseEnvelope,
        )
        from src.core.interfaces.configuration import IAppIdentityConfig
        from src.core.interfaces.model_bases import InternalDTO

        class MockBackend(LLMBackend):
            async def initialize(self, **kwargs: Any) -> None:
                pass

            def get_available_models(self) -> list[str]:
                return ["mock-model"]

            async def chat_completions(  # type: ignore[override]
                self,
                request_data: DomainModel | InternalDTO | dict[str, Any],
                processed_messages: list[dict[str, Any]],
                effective_model: str,
                identity: IAppIdentityConfig | None = None,
                cancellation_token: Any | None = None,
                cancellation_coordinator: Any | None = None,
                **kwargs: Any,
            ) -> ResponseEnvelope | StreamingResponseEnvelope:
                return ResponseEnvelope(content={}, usage=None)

            async def stream_chat_completions(
                self,
                request_data: DomainModel | InternalDTO | dict[str, Any],
                processed_messages: list[dict[str, Any]],
                effective_model: str,
                identity: IAppIdentityConfig | None = None,
                **kwargs: Any,
            ):
                yield {}

        def mock_factory() -> MockBackend:
            from src.core.config.app_config import AppConfig

            return MockBackend(config=AppConfig())

        with pytest.raises(ValueError, match="non-empty string"):
            registry.register_backend("", mock_factory)

        with pytest.raises(ValueError, match="non-empty string"):
            registry.register_backend(None, mock_factory)  # type: ignore

    def test_register_backend_invalid_factory_raises_error(self):
        """Test that invalid factory raises error."""
        registry = BackendRegistry()

        with pytest.raises(TypeError, match="must be a callable"):
            registry.register_backend("test-backend", "not-a-callable")  # type: ignore
