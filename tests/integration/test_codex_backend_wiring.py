"""Integration tests for Codex connector backend wiring and configuration.

This test suite verifies:
- Backend registration through staged initialization
- Configuration defaults and precedence (CLI > ENV > YAML)
- DI wiring and component resolution
- Backend factory integration
"""

from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Any, cast
from unittest.mock import AsyncMock, patch

import httpx
import pytest
import pytest_asyncio

# Import connector to verify registration
import src.connectors  # noqa: F401 — populate backend registry for BackendSettings
from src.connectors.openai_codex import OpenAICodexConnector
from src.core.app.stages.backend import BackendStage
from src.core.config.app_config import AppConfig, BackendConfig, BackendSettings
from src.core.di.container import ServiceCollection
from src.core.domain.validation import ValidationResult
from src.core.services.backend_registry import backend_registry


@pytest_asyncio.fixture(name="auth_dir")  # type: ignore[reportUntypedFunctionDecorator]
async def auth_dir_tmp(tmp_path: Path) -> Path:
    """Create temporary auth directory with credentials."""
    data = {"tokens": {"access_token": "test_token"}}
    tmp_path.mkdir(parents=True, exist_ok=True)
    (tmp_path / "auth.json").write_text(json.dumps(data), encoding="utf-8")
    return tmp_path


@pytest.mark.integration
@pytest.mark.asyncio
async def test_backend_registration_in_staged_init(auth_dir: Path):
    """Test that Codex backend is registered during staged initialization."""
    # Create service collection
    services = ServiceCollection()
    config = AppConfig(
        backends=BackendSettings(default_backend="openai-codex"),
    )

    # Execute backend stage
    stage = BackendStage()
    await stage.execute(services, config)

    # Verify backend is registered
    registered_backends = backend_registry.get_registered_backends()
    assert "openai-codex" in registered_backends

    # Verify we can get the factory
    factory = backend_registry.get_backend_factory("openai-codex")
    assert factory is not None
    assert callable(factory)


@pytest.mark.integration
@pytest.mark.asyncio
async def test_codex_dependencies_registration(auth_dir: Path):
    """Test that Codex component dependencies are registered."""
    from src.connectors.openai_codex.interfaces import (
        ICredentialManager,
        ISettingsLoader,
        IToolExecutionService,
    )
    from src.core.di.registrations._backend.codex import register_codex_services

    services = ServiceCollection()

    # Register required dependencies
    import httpx

    services.add_singleton(
        httpx.AsyncClient, implementation_factory=lambda provider: httpx.AsyncClient()
    )

    # Register Codex services
    register_codex_services(services)

    # Verify services are registered
    provider = services.build_service_provider()

    settings_loader = provider.get_service(cast(type[Any], ISettingsLoader))
    assert settings_loader is not None

    credential_manager = provider.get_service(cast(type[Any], ICredentialManager))
    assert credential_manager is not None

    try:
        tool_execution_service = provider.get_service(
            cast(type[Any], IToolExecutionService)
        )
        assert tool_execution_service is not None
    finally:
        # cleanup credential manager
        await credential_manager.shutdown()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_configuration_precedence_env_overrides_yaml(auth_dir: Path):
    """Test that environment variables override YAML configuration."""
    from src.connectors.openai_codex.settings import SettingsLoader

    # Set environment variable
    os.environ["OPENAI_CODEX_STREAMING_MAX_RETRIES"] = "5"

    try:
        config = AppConfig()
        loader = SettingsLoader()
        settings = loader.load(config)

        # Verify environment override was applied
        assert settings.streaming["max_retries"] == 5
    finally:
        # Cleanup
        os.environ.pop("OPENAI_CODEX_STREAMING_MAX_RETRIES", None)


@pytest.mark.integration
@pytest.mark.asyncio
async def test_configuration_defaults_preserved(auth_dir: Path):
    """Test that configuration defaults are preserved when not overridden."""
    from src.connectors.openai_codex.settings import SettingsLoader

    # Clear any environment overrides
    env_vars_to_clear = [
        "OPENAI_CODEX_STREAMING_MAX_RETRIES",
        "OPENAI_CODEX_STREAMING_RETRY_BACKOFF",
        "OPENAI_CODEX_COMPATIBILITY_LAYER_ENABLED",
    ]
    original_values = {}
    for var in env_vars_to_clear:
        original_values[var] = os.environ.pop(var, None)

    try:
        config = AppConfig()
        loader = SettingsLoader()
        settings = loader.load(config)

        # Verify defaults are preserved
        assert settings.streaming["max_retries"] == 2  # Default from design
        assert settings.streaming["retry_backoff_seconds"] == (0.5, 1.5, 3.0)  # Default
        assert settings.compatibility_layer["enabled"] is False  # Default
    finally:
        # Restore original values
        for var, value in original_values.items():
            if value is not None:
                os.environ[var] = value


@pytest.mark.integration
@pytest.mark.asyncio
async def test_backend_factory_resolves_codex_connector(auth_dir: Path):
    """Test that backend factory can resolve Codex connector with dependencies."""
    from src.core.di.registrations._backend.codex import register_codex_services
    from src.core.di.registrations._backend.factory import register_backend_factory
    from src.core.services.backend_factory import BackendFactory
    from src.core.services.backend_registry import BackendRegistry, backend_registry
    from src.core.services.translation_service import TranslationService

    services = ServiceCollection()

    # Register BackendRegistry
    services.add_singleton(
        BackendRegistry, implementation_factory=lambda _: backend_registry
    )

    # Register required dependencies
    services.add_singleton(
        httpx.AsyncClient, implementation_factory=lambda provider: httpx.AsyncClient()
    )
    services.add_singleton(AppConfig, implementation_factory=lambda _: AppConfig())
    services.add_singleton(
        TranslationService, implementation_factory=lambda _: TranslationService()
    )

    # Register Codex services
    register_codex_services(services)

    # Register backend factory
    register_backend_factory(services, AppConfig())

    # Build provider
    provider = services.build_service_provider()

    # Get factory
    factory = provider.get_service(BackendFactory)
    assert factory is not None

    # Create Codex connector
    # Note: BackendFactory.create_backend only takes backend_type and optional config
    # The factory already has httpx_client from its constructor
    config = AppConfig()
    connector = factory.create_backend("openai-codex", config)

    assert connector is not None
    assert connector.backend_type == "openai-codex"


@pytest.mark.integration
@pytest.mark.asyncio
async def test_openai_codex_v2_backend_factory_and_settings_defaults(auth_dir: Path):
    """``openai-codex-v2`` is registered, constructible via BackendFactory, and loads WS v2 defaults."""
    from src.connectors.openai_codex_v2 import OpenAICodexV2Connector
    from src.connectors.openai_codex_v2.settings_loader import (
        OpenAICodexV2SettingsLoader,
    )
    from src.core.di.registrations._backend.codex import register_codex_services
    from src.core.di.registrations._backend.factory import register_backend_factory
    from src.core.services.backend_factory import BackendFactory
    from src.core.services.backend_registry import BackendRegistry, backend_registry
    from src.core.services.translation_service import TranslationService

    registered = backend_registry.get_registered_backends()
    assert "openai-codex-v2" in registered
    v2_factory = backend_registry.get_backend_factory("openai-codex-v2")
    assert v2_factory is not None
    assert callable(v2_factory)

    services = ServiceCollection()
    services.add_singleton(
        BackendRegistry, implementation_factory=lambda _: backend_registry
    )
    services.add_singleton(
        httpx.AsyncClient, implementation_factory=lambda _: httpx.AsyncClient()
    )
    services.add_singleton(AppConfig, implementation_factory=lambda _: AppConfig())
    services.add_singleton(
        TranslationService, implementation_factory=lambda _: TranslationService()
    )
    register_codex_services(services)
    register_backend_factory(services, AppConfig())

    provider = services.build_service_provider()
    factory = provider.get_service(BackendFactory)
    assert factory is not None

    base = AppConfig()
    cfg = base.model_copy(
        update={
            "backends": base.backends.model_copy(
                update={"openai_codex_v2": BackendConfig()}
            )
        }
    )

    connector = cast(
        OpenAICodexV2Connector, factory.create_backend("openai-codex-v2", cfg)
    )
    try:
        assert isinstance(connector, OpenAICodexV2Connector)
        assert connector.backend_type == "openai-codex-v2"

        loader = OpenAICodexV2SettingsLoader()
        settings = loader.load(cfg)
        assert settings.websocket["enabled"] is True
        assert settings.websocket.get("beta_mode") == "v2"
    finally:
        await connector.shutdown()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_connector_initialization_with_dependencies(auth_dir: Path):
    """Test that connector initializes correctly with injected dependencies."""
    from src.core.services.translation_service import TranslationService

    config = AppConfig()
    async with httpx.AsyncClient() as client:
        ts = TranslationService()

        # Create connector - components are initialized in __init__
        connector = OpenAICodexConnector(client, config, translation_service=ts)

        try:
            # Verify components are initialized (they should be after __init__)
            assert connector._settings_loader is not None
            assert connector._credential_manager is not None
            assert connector._payload_builder is not None
            assert connector._response_executor is not None

            # Initialize connector with mocked validation
            # Set _auth_credentials on credential manager before initialization
            connector._credential_manager._auth_credentials = {
                "tokens": {"access_token": "test_token"}
            }
            with (
                patch.object(
                    connector,
                    "_validate_credentials_file_exists",
                    return_value=ValidationResult.success(),
                ),
                patch.object(
                    connector,
                    "_validate_credentials_structure",
                    return_value=ValidationResult.success(),
                ),
                patch.object(connector, "_start_file_watching"),
            ):
                await connector.initialize(openai_codex_path=str(auth_dir))

                # Verify credential manager was initialized
                # Access via public interface - get_access_token is part of ICredentialManager interface
                # This is acceptable as we're testing initialization, not mutating private state
                from src.connectors.openai_codex.interfaces import ICredentialManager

                assert isinstance(connector._credential_manager, ICredentialManager)
                assert connector._credential_manager.get_access_token() is not None
        finally:
            await connector.shutdown()


@pytest.mark.integration
@pytest.mark.integration
@pytest.mark.asyncio
async def test_backend_functional_state_after_init(auth_dir: Path):
    """Test that is_backend_functional returns correct state after initialization."""
    from src.connectors.openai_codex import OpenAICodexConnector
    from src.core.services.translation_service import TranslationService

    async with httpx.AsyncClient() as client:
        cfg = AppConfig()
        ts = TranslationService()
        backend = OpenAICodexConnector(client, cfg, translation_service=ts)

        # Before initialization, backend should not be functional
        assert backend.is_backend_functional() is False

        try:
            # Set _auth_credentials on credential manager before initialization
            backend._credential_manager._auth_credentials = {
                "tokens": {"access_token": "test_token"}
            }
            with (
                patch.object(
                    backend,
                    "_validate_credentials_file_exists",
                    return_value=ValidationResult.success(),
                ),
                patch.object(
                    backend,
                    "_validate_credentials_structure",
                    return_value=ValidationResult.success(),
                ),
                patch.object(backend, "_start_file_watching"),
            ):
                await backend.initialize(openai_codex_path=str(auth_dir))

                # After initialization, backend should be functional
                assert backend.is_backend_functional() is True
        finally:
            await backend.shutdown()


@pytest.mark.integration
@pytest.mark.integration
@pytest.mark.asyncio
async def test_full_staged_initialization_pipeline(auth_dir: Path):
    """Test that connector works through full staged initialization pipeline (Req 3.4)."""
    from src.core.app.test_builder import build_test_app, create_test_config

    config = create_test_config()
    config = config.model_copy(
        update={
            "backends": config.backends.model_copy(
                update={"default_backend": "openai-codex"}
            )
        }
    )

    # Build app through full staged initialization
    app = build_test_app(config)
    service_provider = app.state.service_provider

    # Verify backend factory is available
    from src.core.services.backend_factory import BackendFactory

    backend_factory = service_provider.get_service(BackendFactory)
    assert backend_factory is not None

    # Verify backend can be created through factory after staged init
    # Note: build_test_app uses mock backends, so we verify the factory works
    # by checking that it can create a backend (even if it's a mock in test mode)
    backend_config = config.backends.lookup("openai-codex")
    if backend_config is None:
        backend_config = BackendConfig(credentials_path=str(auth_dir / "auth.json"))
    elif not backend_config.credentials_path:
        backend_config = backend_config.model_copy(
            update={"credentials_path": str(auth_dir / "auth.json")},
        )

    backend = await backend_factory.ensure_backend(
        backend_type="openai-codex",
        app_config=config,
        backend_config=backend_config,
    )
    try:
        assert backend is not None
        # In test mode, backend might be a mock, so we verify it was created
        # and that the backend type is registered
        assert "openai-codex" in backend_registry.get_registered_backends()
        # If backend has backend_type attribute, verify it
        if hasattr(backend, "backend_type"):
            assert backend.backend_type == "openai-codex"
    finally:
        if hasattr(backend, "shutdown"):
            await backend.shutdown()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_config_keys_honored_from_documentation(auth_dir: Path):
    """Test that documented configuration keys are honored (Req 9.1)."""
    from src.connectors.openai_codex.settings import SettingsLoader

    # Test key configuration keys that should be honored
    codex_backend = BackendConfig(
        extra={
            "codex": {
                "streaming": {
                    "max_retries": 3,
                    "retry_backoff_seconds": [1.0, 2.0, 4.0],
                },
                "compatibility_layer": {
                    "enabled": True,
                    "detection": {
                        "cache_ttl_seconds": 7200,
                        "heuristic_threshold": 3,
                    },
                },
            }
        }
    )
    config = AppConfig(
        backends=BackendSettings(openai_codex=codex_backend)  # type: ignore[call-arg]
    )

    loader = SettingsLoader()
    settings = loader.load(config)

    # Verify documented keys are honored
    assert settings.streaming["max_retries"] == 3
    assert settings.streaming["retry_backoff_seconds"] == (1.0, 2.0, 4.0)
    assert settings.compatibility_layer["enabled"] is True
    assert settings.compatibility_layer["detection"]["cache_ttl_seconds"] == 7200
    assert settings.compatibility_layer["detection"]["heuristic_threshold"] == 3


@pytest.mark.integration
@pytest.mark.asyncio
async def test_backend_type_identifier_stability(auth_dir: Path):
    """Test that backend type identifier remains stable (Task 4.2)."""
    # Verify backend_type class attribute is correct
    assert OpenAICodexConnector.backend_type == "openai-codex"

    # Verify instance also has correct backend_type
    async with httpx.AsyncClient() as client:
        from src.core.config.app_config import AppConfig
        from src.core.services.translation_service import TranslationService

        cfg = AppConfig()
        ts = TranslationService()
        connector = OpenAICodexConnector(client, cfg, translation_service=ts)
        assert connector.backend_type == "openai-codex"


@pytest.mark.integration
@pytest.mark.asyncio
async def test_backend_registry_resolves_codex_backend(auth_dir: Path):
    """Test that backend registry can resolve Codex backend type (Task 4.2)."""
    # Ensure backend is registered (import triggers registration)
    import src.connectors.openai_codex  # noqa: F401

    # Verify backend type is registered
    registered_backends = backend_registry.get_registered_backends()
    assert "openai-codex" in registered_backends

    # Verify factory can be retrieved
    factory = backend_registry.get_backend_factory("openai-codex")
    assert factory is not None
    assert callable(factory)

    # Verify factory returns correct connector class
    assert factory == OpenAICodexConnector


@pytest.mark.integration
@pytest.mark.asyncio
async def test_partial_dependency_bundle_behavior(auth_dir: Path):
    """Test that partial dependency bundle works correctly (Task 4.1).

    Verifies that connector-agnostic services come from DI while
    connector-bound components are created by the connector.
    """
    from src.connectors.openai_codex.contracts import CodexConnectorDependencies
    from src.connectors.openai_codex.interfaces import (
        ICredentialManager,
        ISettingsLoader,
        IToolExecutionService,
    )
    from src.core.di.registrations._backend.codex import register_codex_services
    from src.core.services.translation_service import TranslationService

    services = ServiceCollection()

    # Register required dependencies
    services.add_singleton(
        httpx.AsyncClient, implementation_factory=lambda provider: httpx.AsyncClient()
    )

    # Register Codex services (this registers connector-agnostic services)
    register_codex_services(services)

    # Build provider
    provider = services.build_service_provider()

    # Get CodexConnectorDependencies from DI (should have partial bundle)
    dependencies = provider.get_service(CodexConnectorDependencies)
    assert dependencies is not None

    # Verify connector-agnostic services are provided
    assert dependencies.settings_loader is not None
    assert isinstance(dependencies.settings_loader, ISettingsLoader)
    assert dependencies.credential_manager is not None
    assert isinstance(dependencies.credential_manager, ICredentialManager)
    assert dependencies.tool_execution_service is not None
    assert isinstance(dependencies.tool_execution_service, IToolExecutionService)

    # Verify connector-bound components are None (created by connector)
    assert dependencies.payload_builder is None
    assert dependencies.response_executor is None
    assert dependencies.compatibility_layer is None

    # Verify connector can be constructed with partial bundle
    config = AppConfig()
    ts = TranslationService()
    async with httpx.AsyncClient() as client:
        connector = OpenAICodexConnector(
            client, config, translation_service=ts, dependencies=dependencies
        )

        try:
            # Verify connector used DI-provided services
            assert connector._settings_loader is dependencies.settings_loader
            assert connector._credential_manager is dependencies.credential_manager
            assert (
                connector._tool_execution_service is dependencies.tool_execution_service
            )

            # Verify connector created its own connector-bound components
            assert connector._payload_builder is not None
            assert connector._response_executor is not None
            assert connector._compatibility_layer is not None
        finally:
            await connector.shutdown()
            await dependencies.credential_manager.shutdown()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_connector_construction_without_di_dependencies(auth_dir: Path):
    """Test that connector can be constructed without DI dependencies (Task 4.1).

    Verifies that connector creates defaults when dependencies are None.
    """
    from src.core.services.translation_service import TranslationService

    config = AppConfig()
    ts = TranslationService()
    async with httpx.AsyncClient() as client:
        # Create connector without dependencies (should create defaults)
        connector = OpenAICodexConnector(
            client, config, translation_service=ts, dependencies=None
        )

        try:
            # Verify connector created default components
            assert connector._settings_loader is not None
            assert connector._credential_manager is not None
            assert connector._tool_execution_service is not None
            assert connector._payload_builder is not None
            assert connector._response_executor is not None
            assert connector._compatibility_layer is not None

            # Verify components are functional (not None placeholders)
            assert hasattr(connector._settings_loader, "load")
            assert hasattr(connector._credential_manager, "get_access_token")
            assert hasattr(connector._tool_execution_service, "execute_proxy_tool")
        finally:
            await connector.shutdown()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_staged_initialization_constructs_connector_with_partial_bundle(
    auth_dir: Path,
):
    """Test that staged initialization can construct connector with partial bundle (Task 4.1)."""
    from src.core.app.stages.backend import BackendStage
    from src.core.di.registrations._backend.codex import register_codex_services
    from src.core.services.backend_factory import BackendFactory
    from src.core.services.translation_service import TranslationService

    services = ServiceCollection()
    config = AppConfig(
        backends=BackendSettings(default_backend="openai-codex"),
    )

    # Register required dependencies for staged initialization
    services.add_singleton(
        httpx.AsyncClient, implementation_factory=lambda provider: httpx.AsyncClient()
    )
    services.add_singleton(AppConfig, implementation_factory=lambda _: config)
    services.add_singleton(
        TranslationService, implementation_factory=lambda _: TranslationService()
    )

    # Register Codex services (partial bundle)
    register_codex_services(services)

    # Execute backend stage
    stage = BackendStage()
    await stage.execute(services, config)

    # Build provider
    provider = services.build_service_provider()

    # Get backend factory
    backend_factory = provider.get_service(BackendFactory)
    assert backend_factory is not None

    # Create connector through factory (should use partial bundle from DI)
    backend_config = getattr(config.backends, "openai_codex", None)
    if not backend_config:
        from src.core.config.app_config import BackendConfig

        backend_config = BackendConfig()

    with patch.object(
        OpenAICodexConnector,
        "initialize",
        new=AsyncMock(return_value=None),
    ) as initialize_mock:
        backend = await backend_factory.ensure_backend(
            backend_type="openai-codex",
            app_config=config,
            backend_config=backend_config,
        )

        try:
            assert backend is not None
            assert isinstance(backend, OpenAICodexConnector)
            assert backend.backend_type == "openai-codex"
            initialize_mock.assert_awaited_once()

            # Verify connector was constructed with components
            assert backend._settings_loader is not None
            assert backend._credential_manager is not None
            assert backend._payload_builder is not None
            assert backend._response_executor is not None
        finally:
            if hasattr(backend, "shutdown"):
                await backend.shutdown()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_response_envelope_wire_capture_compatibility(auth_dir: Path):
    """Test that ResponseEnvelope from executor is compatible with wire capture (Task 4.3, Req 8.2)."""
    from src.core.domain.responses import ResponseEnvelope
    from src.core.domain.usage_summary import UsageSummary
    from src.core.services.wire_capture_service import WireCapture
    from src.core.transport.fastapi.adapters.capture.wire_capture_coordinator import (
        WireCaptureCoordinator,
    )

    # Create wire capture service with config
    config = AppConfig()
    wire_capture = WireCapture(config)
    coordinator = WireCaptureCoordinator(wire_capture)

    # Create a ResponseEnvelope as returned by executor
    envelope = ResponseEnvelope(
        content={"choices": [{"message": {"role": "assistant", "content": "Test"}}]},
        status_code=200,
        headers={"Content-Type": "application/json"},
        usage=UsageSummary(prompt_tokens=10, completion_tokens=20, total_tokens=30),
        metadata={
            "backend": "openai-codex",
            "model": "gpt-5.1-codex",
            "session_id": "test-session-123",
        },
    )

    # Verify envelope has required fields for wire capture
    assert envelope.metadata is not None
    assert "backend" in envelope.metadata
    assert "model" in envelope.metadata
    assert "session_id" in envelope.metadata

    # Verify coordinator can extract fields (this is what wire capture uses)
    backend, model, key_name, session_id = coordinator._infer_capture_fields(
        envelope, None
    )
    assert backend == "openai-codex"
    assert model == "gpt-5.1-codex"
    assert session_id == "test-session-123"

    # Verify coordinator can schedule capture without errors
    # (This verifies envelope structure is compatible)
    try:
        coordinator.schedule_capture(envelope, envelope.content, None)
        # If no exception is raised, envelope is compatible
        compatibility_verified = True
    except Exception as e:
        compatibility_verified = False
        pytest.fail(f"ResponseEnvelope not compatible with wire capture: {e}")

    assert compatibility_verified


@pytest.mark.integration
@pytest.mark.asyncio
async def test_wire_capture_failure_does_not_affect_response(auth_dir: Path):
    """Test that wire capture failures don't affect response path (Task 4.3, Req 8.3)."""
    from unittest.mock import AsyncMock, MagicMock

    from src.core.domain.responses import ResponseEnvelope
    from src.core.domain.usage_summary import UsageSummary
    from src.core.interfaces.wire_capture_interface import IWireCapture
    from src.core.transport.fastapi.adapters.capture.wire_capture_coordinator import (
        WireCaptureCoordinator,
    )

    # Create a mock wire capture service that raises exceptions
    mock_wire_capture = MagicMock(spec=IWireCapture)
    mock_wire_capture.enabled.return_value = True
    mock_wire_capture.capture_outbound_response = AsyncMock(
        side_effect=RuntimeError("Wire capture failed")
    )

    coordinator = WireCaptureCoordinator(mock_wire_capture)

    # Create a ResponseEnvelope as returned by executor
    envelope = ResponseEnvelope(
        content={"choices": [{"message": {"role": "assistant", "content": "Test"}}]},
        status_code=200,
        headers={"Content-Type": "application/json"},
        usage=UsageSummary(prompt_tokens=10, completion_tokens=20, total_tokens=30),
        metadata={
            "backend": "openai-codex",
            "model": "gpt-5.1-codex",
            "session_id": "test-session-789",
        },
    )

    # Verify envelope is valid before capture attempt
    assert envelope.content is not None
    assert envelope.status_code == 200
    assert envelope.usage is not None

    # Schedule capture (should not raise exception even if capture fails)
    try:
        coordinator.schedule_capture(envelope, envelope.content, None)
        # Give background task a moment to fail
        import asyncio

        await asyncio.sleep(0.1)
    except Exception as e:
        pytest.fail(f"Wire capture failure should not propagate: {e}")

    # Verify envelope is still valid after capture attempt
    assert envelope.content is not None
    assert envelope.status_code == 200
    assert envelope.usage is not None
    assert envelope.metadata is not None

    # Verify capture was attempted (but failed silently)
    assert mock_wire_capture.capture_outbound_response.called


@pytest.mark.integration
@pytest.mark.asyncio
async def test_usage_accounting_can_extract_usage_from_envelope(auth_dir: Path):
    """Test that UsageAccountingOrchestrator can extract usage from ResponseEnvelope (Task 4.3, Req 8.1)."""
    from unittest.mock import AsyncMock, MagicMock

    from src.core.domain.responses import ResponseEnvelope
    from src.core.domain.usage_summary import UsageSummary
    from src.core.interfaces.planning_phase_manager_interface import (
        IPlanningPhaseManager,
    )
    from src.core.interfaces.resilience_interface import IResilienceCoordinator
    from src.core.interfaces.stream_session_id_resolver_interface import (
        IStreamSessionIdResolver,
    )
    from src.core.interfaces.usage_tracking_interface import IUsageTrackingService
    from src.core.interfaces.usage_tracking_wrapper_interface import (
        IUsageTrackingWrapper,
    )
    from src.core.services.backend_completion_flow.usage_accounting_orchestrator import (
        UsageAccountingOrchestrator,
    )

    # Create mock dependencies
    usage_tracking_service = MagicMock(spec=IUsageTrackingService)
    usage_tracking_service.record_response = AsyncMock()
    usage_tracking_wrapper = MagicMock(spec=IUsageTrackingWrapper)
    stream_session_id_resolver = MagicMock(spec=IStreamSessionIdResolver)
    planning_phase_manager = MagicMock(spec=IPlanningPhaseManager)
    resilience_coordinator = MagicMock(spec=IResilienceCoordinator)

    # Create orchestrator
    orchestrator = UsageAccountingOrchestrator(
        usage_tracking_service=usage_tracking_service,
        usage_tracking_wrapper=usage_tracking_wrapper,
        stream_session_id_resolver=stream_session_id_resolver,
        planning_phase_manager=planning_phase_manager,
        resilience_coordinator=resilience_coordinator,
    )

    # Create ResponseEnvelope with usage as returned by executor
    usage_summary = UsageSummary(
        prompt_tokens=10, completion_tokens=20, total_tokens=30
    )
    envelope = ResponseEnvelope(
        content={"choices": [{"message": {"role": "assistant", "content": "Test"}}]},
        status_code=200,
        headers={"Content-Type": "application/json"},
        usage=usage_summary,
        metadata={
            "backend": "openai-codex",
            "model": "gpt-5.1-codex",
            "session_id": "test-session-usage",
        },
    )

    # Verify usage is accessible via getattr pattern (as used by orchestrator)
    usage = getattr(envelope, "usage", None)
    assert usage is not None
    assert isinstance(usage, UsageSummary)
    assert usage.prompt_tokens == 10
    assert usage.completion_tokens == 20
    assert usage.total_tokens == 30

    # Verify usage can be converted to dict (as expected by orchestrator)
    usage_dict = usage.to_dict()
    assert isinstance(usage_dict, dict)
    assert usage_dict["prompt_tokens"] == 10
    assert usage_dict["completion_tokens"] == 20
    assert usage_dict["total_tokens"] == 30

    # Verify orchestrator can extract and process usage
    from tests.utils.fake_clock import FakeClockContext

    async with FakeClockContext() as clock:
        start_time = clock.time()
        wrapped = await orchestrator.wrap_response_for_usage(
            result=envelope,
            outbound_tokens=10,
            ctp_record_id="test-ctp-id",
            ptb_record_id="test-ptb-id",
            start_time=start_time,
            context=None,
            backend_type="openai-codex",
            effective_model="gpt-5.1-codex",
        )

    # Verify envelope is still valid
    assert isinstance(wrapped, ResponseEnvelope)
    assert wrapped is envelope
    assert wrapped.usage is not None

    # Verify usage tracking service was called with correct usage data
    assert usage_tracking_service.record_response.called
    call_args = usage_tracking_service.record_response.call_args_list

    # Should be called twice (once for ctp, once for ptb)
    assert len(call_args) == 2

    # Verify both calls have correct completion_tokens from usage
    for call in call_args:
        kwargs = call.kwargs
        assert kwargs["completion_tokens"] == 20
        assert kwargs["backend_reported_usage"] == usage_dict
        assert kwargs["http_status_code"] == 200


@pytest.mark.integration
@pytest.mark.asyncio
async def test_streaming_response_envelope_wire_capture_compatibility(auth_dir: Path):
    """Test that StreamingResponseEnvelope is compatible with wire capture (Task 4.3, Req 8.2)."""
    from src.core.domain.responses import StreamingResponseEnvelope
    from src.core.interfaces.response_processor_interface import ProcessedResponse
    from src.core.services.wire_capture_service import WireCapture
    from src.core.transport.fastapi.adapters.capture.wire_capture_coordinator import (
        WireCaptureCoordinator,
    )

    # Create wire capture service with config
    config = AppConfig()
    wire_capture = WireCapture(config)
    coordinator = WireCaptureCoordinator(wire_capture)

    # Create a streaming envelope as returned by executor
    async def mock_stream():
        yield ProcessedResponse(content=b"data: test\n\n", metadata={})

    envelope = StreamingResponseEnvelope(
        content=mock_stream(),
        media_type="text/event-stream",
        headers={"Content-Type": "text/event-stream"},
        status_code=200,
        metadata={
            "backend": "openai-codex",
            "model": "gpt-5.1-codex",
            "session_id": "test-session-456",
        },
    )

    # Verify envelope has required fields for wire capture
    assert envelope.metadata is not None
    assert "backend" in envelope.metadata
    assert "model" in envelope.metadata
    assert "session_id" in envelope.metadata

    # Verify coordinator can extract fields
    backend, model, key_name, session_id = coordinator._infer_capture_fields(
        envelope, None
    )
    assert backend == "openai-codex"
    assert model == "gpt-5.1-codex"
    assert session_id == "test-session-456"

    # Verify coordinator can wrap stream without errors
    # (This verifies envelope structure is compatible)
    try:
        wrapped = coordinator.wrap_stream(envelope, envelope.body_iterator)
        # If no exception is raised, envelope is compatible
        compatibility_verified = True
        # Consume stream to ensure it works
        async for _ in wrapped:
            break
    except Exception as e:
        compatibility_verified = False
        pytest.fail(f"StreamingResponseEnvelope not compatible with wire capture: {e}")

    assert compatibility_verified


@pytest.mark.integration
@pytest.mark.asyncio
async def test_wire_capture_redacts_secrets_in_content(auth_dir: Path):
    """Test that wire capture services redact secrets from captured content (Task 4.3, Req 8.5)."""
    import tempfile
    from pathlib import Path as PathLib

    from src.core.domain.responses import ResponseEnvelope
    from src.core.domain.usage_summary import UsageSummary
    from src.core.services.structured_wire_capture_service import StructuredWireCapture

    # Create a test API key that should be redacted
    # Using a clearly fake test value that doesn't match real API key patterns
    test_api_key = "test-api-key-for-redaction-verification-12345"

    # Create response content with secret
    response_content = {
        "choices": [
            {
                "message": {
                    "role": "assistant",
                    "content": f"Your API key is {test_api_key}",
                }
            }
        ],
        "usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
    }

    # Create envelope with content containing secret
    envelope = ResponseEnvelope(
        content=response_content,
        status_code=200,
        headers={"Content-Type": "application/json"},
        usage=UsageSummary(prompt_tokens=10, completion_tokens=20, total_tokens=30),
        metadata={
            "backend": "openai-codex",
            "model": "gpt-5.1-codex",
            "session_id": "test-session-redact",
        },
    )

    # Create temporary capture file
    with tempfile.NamedTemporaryFile(
        mode="w", delete=False, suffix=".jsonl"
    ) as tmp_file:
        capture_file_path = PathLib(tmp_file.name)

    try:
        # Create StructuredWireCapture service with capture file
        config = AppConfig()
        config.logging.capture_file = str(capture_file_path)
        # Set API key in config so redactor can discover it
        import os

        os.environ["OPENAI_API_KEY"] = test_api_key
        wire_capture = StructuredWireCapture(config)

        # Capture the response
        await wire_capture.capture_outbound_response(
            context=None,
            session_id="test-session-redact",
            backend="openai-codex",
            model="gpt-5.1-codex",
            key_name=None,
            response_content=envelope.content,
        )

        # Flush to ensure content is written
        await wire_capture.shutdown()

        # Read captured content
        with open(capture_file_path, encoding="utf-8") as f:
            captured_content = f.read()

        # Verify secret is redacted in captured content
        assert test_api_key not in captured_content
        # Verify redaction marker is present
        assert (
            "***" in captured_content
            or "(API_KEY_HAS_BEEN_REDACTED)" in captured_content
        )

        # Verify response envelope content is unchanged (redaction only affects capture)
        assert test_api_key in str(envelope.content)
    finally:
        # Cleanup
        if capture_file_path.exists():
            capture_file_path.unlink()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_usage_service_failure_does_not_affect_response(auth_dir: Path):
    """Test that usage tracking service failures don't affect response path (Task 4.3, Req 8.3)."""
    from unittest.mock import AsyncMock, MagicMock

    from src.core.domain.responses import ResponseEnvelope
    from src.core.domain.usage_summary import UsageSummary
    from src.core.interfaces.planning_phase_manager_interface import (
        IPlanningPhaseManager,
    )
    from src.core.interfaces.resilience_interface import IResilienceCoordinator
    from src.core.interfaces.stream_session_id_resolver_interface import (
        IStreamSessionIdResolver,
    )
    from src.core.interfaces.usage_tracking_interface import IUsageTrackingService
    from src.core.interfaces.usage_tracking_wrapper_interface import (
        IUsageTrackingWrapper,
    )
    from src.core.services.backend_completion_flow.usage_accounting_orchestrator import (
        UsageAccountingOrchestrator,
    )

    # Create mock dependencies with failing usage tracking service
    usage_tracking_service = MagicMock(spec=IUsageTrackingService)
    usage_tracking_service.record_response = AsyncMock(
        side_effect=RuntimeError("Usage tracking failed")
    )
    usage_tracking_wrapper = MagicMock(spec=IUsageTrackingWrapper)
    stream_session_id_resolver = MagicMock(spec=IStreamSessionIdResolver)
    planning_phase_manager = MagicMock(spec=IPlanningPhaseManager)
    resilience_coordinator = MagicMock(spec=IResilienceCoordinator)

    # Create orchestrator
    orchestrator = UsageAccountingOrchestrator(
        usage_tracking_service=usage_tracking_service,
        usage_tracking_wrapper=usage_tracking_wrapper,
        stream_session_id_resolver=stream_session_id_resolver,
        planning_phase_manager=planning_phase_manager,
        resilience_coordinator=resilience_coordinator,
    )

    # Create ResponseEnvelope with usage
    usage_summary = UsageSummary(
        prompt_tokens=10, completion_tokens=20, total_tokens=30
    )
    envelope = ResponseEnvelope(
        content={"choices": [{"message": {"role": "assistant", "content": "Test"}}]},
        status_code=200,
        headers={"Content-Type": "application/json"},
        usage=usage_summary,
        metadata={
            "backend": "openai-codex",
            "model": "gpt-5.1-codex",
            "session_id": "test-session-usage-fail",
        },
    )

    # Verify envelope is valid before usage recording attempt
    assert envelope.content is not None
    assert envelope.status_code == 200
    assert envelope.usage is not None

    # Wrap response for usage (should not raise exception even if usage tracking fails)
    from tests.utils.fake_clock import FakeClockContext

    async with FakeClockContext() as clock:
        start_time = clock.time()
        try:
            wrapped = await orchestrator.wrap_response_for_usage(
                result=envelope,
                outbound_tokens=10,
                ctp_record_id="test-ctp-id",
                ptb_record_id="test-ptb-id",
                start_time=start_time,
                context=None,
                backend_type="openai-codex",
                effective_model="gpt-5.1-codex",
            )
        except Exception as e:
            pytest.fail(f"Usage tracking failure should not propagate: {e}")

        # Verify envelope is still valid after usage recording attempt
        assert isinstance(wrapped, ResponseEnvelope)
        assert wrapped is envelope
        assert wrapped.content is not None
        assert wrapped.status_code == 200
        assert wrapped.usage is not None
        assert wrapped.metadata is not None

    # Verify usage tracking service was attempted (but failed silently)
    assert usage_tracking_service.record_response.called


@pytest.mark.integration
@pytest.mark.asyncio
async def test_gpt_6_astra_support_and_alias_resolution(auth_dir: Path):
    """Test that gpt-6-astra is supported by Codex connector and gpt-6.0-astra resolves via alias."""
    from typing import Any, cast

    from src.core.app.application_builder import ApplicationBuilder
    from src.core.config.app_config import load_config
    from src.core.di.provider_lifecycle import get_current_service_provider
    from src.core.domain.chat import ChatMessage, ChatRequest
    from src.core.interfaces.backend_model_resolver_interface import (
        IBackendModelResolver,
    )
    from src.core.services.backend_factory import BackendFactory

    config = load_config("config/config.yaml")
    builder = ApplicationBuilder()
    builder.add_default_stages()
    await builder.build(config)

    provider = get_current_service_provider()
    resolver = provider.get_required_service(cast(type[Any], IBackendModelResolver))

    # Test resolving openai-codex:gpt-6-astra
    target1 = await resolver.resolve_target(
        ChatRequest(
            model="openai-codex:gpt-6-astra",
            messages=[ChatMessage(role="user", content="hello")],
        )
    )
    assert target1.backend == "openai-codex"
    assert target1.model == "gpt-6-astra"

    # Test resolving openai-codex:gpt-6.0-astra alias
    target2 = await resolver.resolve_target(
        ChatRequest(
            model="openai-codex:gpt-6.0-astra",
            messages=[ChatMessage(role="user", content="hello")],
        )
    )
    assert target2.backend == "openai-codex"
    assert target2.model == "gpt-6-astra"

    # Verify connector recognizes gpt-6-astra
    backend_factory = provider.get_required_service(BackendFactory)
    connector = cast(
        OpenAICodexConnector, backend_factory.create_backend("openai-codex", config)
    )
    try:
        assert connector._is_codex_model("gpt-6-astra") is True
        assert "gpt-6-astra" in connector.SUPPORTED_CODEX_MODELS
    finally:
        await connector.shutdown()
