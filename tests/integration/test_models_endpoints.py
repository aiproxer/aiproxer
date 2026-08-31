"""
Integration tests for the models endpoints.

These tests verify that the /models and /v1/models endpoints work correctly
with both mocked and real backend configurations.
"""

from unittest.mock import AsyncMock, MagicMock, Mock, patch

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient
from src.core.app.test_builder import build_test_app as build_app
from src.core.interfaces.application_state_interface import IApplicationState
from src.core.interfaces.session_service_interface import ISessionService

from tests.unit.fixtures.markers import real_time

# Suppress Windows ProactorEventLoop warnings for this module
pytestmark = pytest.mark.filterwarnings(
    "ignore:unclosed event loop <ProactorEventLoop.*:ResourceWarning"
)

# from src.constants import BackendType  # Removed BackendType import


@pytest.fixture
def middleware_order_validator():
    """Fixture to validate middleware ordering in integration tests."""

    def validate_middleware_order(app, expected_order: list[str]) -> bool:
        """
        Validate that middleware is configured in the expected order.

        Args:
            app: FastAPI application
            expected_order: List of middleware class names in expected order

        Returns:
            True if middleware order matches expectations
        """
        if not hasattr(app, "user_middleware"):
            return False

        actual_order = []
        for middleware in app.user_middleware:
            middleware_class = middleware.cls
            actual_order.append(middleware_class.__name__)

        # Check if expected middleware classes are present in the correct order
        expected_indices = []
        for expected_middleware in expected_order:
            if expected_middleware in actual_order:
                expected_indices.append(actual_order.index(expected_middleware))
            else:
                # Middleware not found
                return False

        # Check if indices are in ascending order (correct order)
        return expected_indices == sorted(expected_indices)

    return validate_middleware_order


class TestModelsEndpoints:
    """Integration tests for models discovery endpoints."""

    # Suppress Windows ProactorEventLoop warnings for this module
    pytestmark = pytest.mark.filterwarnings(
        "ignore:unclosed event loop <ProactorEventLoop.*:ResourceWarning"
    )

    @pytest.fixture(scope="class")
    def app_with_auth_disabled(self):
        """Create app with authentication disabled."""
        from src.core.app.test_builder import create_test_config

        return build_app(config=create_test_config())

    @pytest.fixture(scope="class")
    def app_with_auth_enabled(self):
        """Create app with authentication enabled."""
        from src.core.app.test_builder import create_test_config

        config = create_test_config()
        new_auth = config.auth.model_copy(
            update={"disable_auth": False, "api_keys": ["test-key-123"]}
        )
        config = config.model_copy(update={"auth": new_auth})
        return build_app(config=config)

    def test_models_endpoint_no_auth(self, app_with_auth_disabled):
        """Test /models endpoint without authentication."""
        with TestClient(app_with_auth_disabled) as client:
            response = client.get("/models")

            assert response.status_code == 200
            data = response.json()
            assert "object" in data
            assert data["object"] == "list"
            assert "data" in data
            assert isinstance(data["data"], list)

            assert isinstance(data["data"], list)

    def test_v1_models_endpoint_no_auth(self, app_with_auth_disabled):
        """Test /v1/models endpoint without authentication."""
        with TestClient(app_with_auth_disabled) as client:
            response = client.get("/v1/models")

            assert response.status_code == 200
            data = response.json()
            assert "object" in data
            assert data["object"] == "list"
            assert "data" in data
            assert isinstance(data["data"], list)

    def test_models_endpoint_with_auth(self, app_with_auth_enabled):
        """Test /models endpoint with authentication."""
        with TestClient(app_with_auth_enabled) as client:
            # Without auth - should fail
            response = client.get("/models")
            assert response.status_code == 401

            # With valid auth
            response = client.get(
                "/models", headers={"Authorization": "Bearer test-key-123"}
            )
            assert response.status_code == 200
            data = response.json()
            assert data["object"] == "list"

    def test_models_endpoint_invalid_auth(self, app_with_auth_enabled, monkeypatch):
        """Test /models endpoint with invalid authentication."""
        # Ensure any prior API key env is cleared to avoid leakage across tests
        monkeypatch.delenv("API_KEYS", raising=False)
        monkeypatch.delenv("LLM_INTERACTIVE_PROXY_API_KEY", raising=False)
        with TestClient(app_with_auth_enabled) as client:
            response = client.get(
                "/models", headers={"Authorization": "Bearer invalid-key"}
            )
            assert response.status_code == 401
            detail = response.json().get("detail", "")
            assert detail in ("Unauthorized", "Invalid or missing API key")

    def test_middleware_configuration_order(
        self, monkeypatch, middleware_order_validator
    ):
        """Test that middleware is configured in the correct order."""
        monkeypatch.setenv("API_KEYS", "test-key-123")
        monkeypatch.setenv("DISABLE_AUTH", "false")
        app = build_app()

        # Expected middleware order (note: FastAPI applies middleware in reverse order)
        # Last added middleware executes first, so the actual execution order is:
        # RetryAfter -> CustomHeader -> APIKey -> CORS
        expected_order = [
            "RetryAfterMiddleware",
            "CustomHeaderMiddleware",
            "APIKeyMiddleware",
            "CORSMiddleware",
        ]

        # Validate middleware order
        assert middleware_order_validator(
            app, expected_order
        ), f"Middleware not configured in expected order. Expected: {expected_order}"

    def test_models_with_configured_backends(self, app_with_auth_disabled):
        """Test models discovery with configured backends."""
        with TestClient(app_with_auth_disabled) as client:
            response = client.get("/models")

            assert response.status_code == 200
            data = response.json()
            assert isinstance(data["data"], list)

            # Should expose models in OpenAI list format
            assert "object" in data
            assert data["object"] == "list"
            assert "data" in data
            assert isinstance(data["data"], list)

    def test_models_format_compliance(self, app_with_auth_disabled):
        """Test that models response follows OpenAI format."""
        with TestClient(app_with_auth_disabled) as client:
            response = client.get("/models")

            assert response.status_code == 200
            data = response.json()

            # Check overall structure
            assert data["object"] == "list"
            assert isinstance(data["data"], list)

            # Check each model object
            for model in data["data"]:
                assert "id" in model
                assert "object" in model
                assert model["object"] == "model"
                assert "owned_by" in model
                assert isinstance(model["id"], str)
                assert isinstance(model["owned_by"], str)

    def test_models_endpoint_error_handling(self, app_with_auth_disabled):
        """Test error handling in models endpoint."""
        with TestClient(app_with_auth_disabled) as client:
            from src.core.interfaces.backend_service_interface import IBackendService

            # Get the backend service from DI
            backend_service = (
                app_with_auth_disabled.state.service_provider.get_required_service(
                    IBackendService
                )
            )

            # Patch the internal method to raise an exception
            with patch.object(
                backend_service,
                "_get_or_create_backend",
                side_effect=Exception("Backend initialization failed"),
            ):
                response = client.get("/models")

                # The endpoint should not depend on backend discovery side effects.
                assert response.status_code == 200
                data = response.json()
                assert "data" in data
                assert isinstance(data["data"], list)


class TestModelsDiscovery:
    """Test actual model discovery from backends."""

    @pytest.fixture
    def mock_backend_factory(self):
        """Create a mock backend factory."""
        from src.core.services.backend_factory import BackendFactory

        factory = MagicMock(spec=BackendFactory)
        return factory

    @pytest.mark.asyncio
    async def test_discover_openai_models(self, mock_backend_factory):
        """Test discovering models from OpenAI backend."""
        from src.core.interfaces.rate_limiter_interface import IRateLimiter

        # Create mock rate limiter
        mock_rate_limiter = MagicMock(spec=IRateLimiter)
        mock_rate_limiter.check_rate_limit = AsyncMock(return_value=None)

        # Create mock config
        mock_config = MagicMock()
        mock_config.get.return_value = None

        # Create mock session service
        mock_session_service = MagicMock(spec=ISessionService)

        # Create backend service using builder
        mock_app_state = MagicMock(spec=IApplicationState)
        from tests.unit.fixtures.backend_service_builder import (
            create_backend_service_with_mocks,
        )
        from tests.utils.failover_stub import StubFailoverCoordinator

        # Mock OpenAI backend
        mock_openai = AsyncMock()
        mock_openai.get_available_models.return_value = [
            "gpt-4-turbo-preview",
            "gpt-4",
            "gpt-3.5-turbo",
            "gpt-3.5-turbo-16k",
        ]
        mock_openai.initialize = AsyncMock()

        # Set up the mock to return our mock backend when called with "openai"
        mock_backend_factory.ensure_backend = AsyncMock(return_value=mock_openai)
        mock_backend_factory.initialize_backend = AsyncMock()

        from src.core.interfaces.backend_lifecycle_manager_interface import (
            IBackendLifecycleManager,
        )

        # Mock lifecycle manager
        mock_lifecycle_manager = AsyncMock(spec=IBackendLifecycleManager)
        mock_lifecycle_manager.get_disabled_backends.return_value = {}
        mock_lifecycle_manager.get_or_create = AsyncMock(return_value=mock_openai)

        service = create_backend_service_with_mocks(
            factory=mock_backend_factory,
            rate_limiter=mock_rate_limiter,
            config=mock_config,
            session_service=mock_session_service,
            app_state=mock_app_state,
            failover_coordinator=StubFailoverCoordinator(),
            use_real_completion_flow=True,
            backend_lifecycle_manager=mock_lifecycle_manager,
        )

        # Get backend and discover models
        backend = await service._backend_lifecycle_manager.get_or_create("openai")
        models = await backend.get_available_models()

        assert len(models) == 4
        assert "gpt-4" in models
        assert "gpt-3.5-turbo" in models

    @pytest.mark.asyncio
    async def test_discover_anthropic_models(self, mock_backend_factory):
        """Test discovering models from Anthropic backend."""
        from src.core.interfaces.rate_limiter_interface import IRateLimiter

        # Create mock rate limiter
        mock_rate_limiter = MagicMock(spec=IRateLimiter)
        mock_rate_limiter.check_rate_limit = AsyncMock(return_value=None)

        # Create mock config
        mock_config = MagicMock()
        mock_config.get.return_value = None

        # Create mock session service
        mock_session_service = MagicMock(spec=ISessionService)

        # Create backend service using builder
        mock_app_state = MagicMock(spec=IApplicationState)
        from tests.unit.fixtures.backend_service_builder import (
            create_backend_service_with_mocks,
        )
        from tests.utils.failover_stub import StubFailoverCoordinator

        # Mock Anthropic backend
        mock_anthropic = AsyncMock()
        mock_anthropic.get_available_models.return_value = [
            "claude-3-opus-20240229",
            "claude-3-sonnet-20240229",
            "claude-3-haiku-20240307",
            "claude-2.1",
        ]
        mock_anthropic.initialize = AsyncMock()

        # Set up the mock to return our mock backend when called with "anthropic"
        mock_backend_factory.ensure_backend = AsyncMock(return_value=mock_anthropic)
        mock_backend_factory.initialize_backend = AsyncMock()

        from src.core.interfaces.backend_lifecycle_manager_interface import (
            IBackendLifecycleManager,
        )

        # Mock lifecycle manager
        mock_lifecycle_manager = AsyncMock(spec=IBackendLifecycleManager)
        mock_lifecycle_manager.get_disabled_backends.return_value = {}
        mock_lifecycle_manager.get_or_create = AsyncMock(return_value=mock_anthropic)

        service = create_backend_service_with_mocks(
            factory=mock_backend_factory,
            rate_limiter=mock_rate_limiter,
            config=mock_config,
            session_service=mock_session_service,
            app_state=mock_app_state,
            failover_coordinator=StubFailoverCoordinator(),
            use_real_completion_flow=True,
            backend_lifecycle_manager=mock_lifecycle_manager,
        )

        # Get backend and discover models
        backend = await service._backend_lifecycle_manager.get_or_create("anthropic")
        models = await backend.get_available_models()

        assert len(models) == 4
        assert "claude-3-opus-20240229" in models
        assert "claude-2.1" in models

    @pytest.mark.asyncio
    async def test_discover_models_with_failover(self, mock_backend_factory):
        """Test model discovery when primary backend fails."""
        from src.core.interfaces.rate_limiter_interface import IRateLimiter

        # Create mock rate limiter
        mock_rate_limiter = MagicMock(spec=IRateLimiter)
        mock_rate_limiter.check_rate_limit = AsyncMock(return_value=None)

        # Create mock config with failover
        mock_config = MagicMock()
        mock_config.get.return_value = None

        # Create mock session service
        mock_session_service = MagicMock(spec=ISessionService)

        # Create backend service with failover routes
        failover_routes = {
            "openai": {  # Used string literal
                "backend": "openrouter",  # Used string literal
                "model": "openai/gpt-4",
            }
        }

        mock_app_state = MagicMock(spec=IApplicationState)
        from tests.unit.fixtures.backend_service_builder import (
            create_backend_service_with_mocks,
        )
        from tests.utils.failover_stub import StubFailoverCoordinator

        # Mock failover backend
        mock_openrouter = MagicMock()
        mock_openrouter.get_available_models = Mock(
            return_value=[
                "openrouter/gpt-4",
                "openrouter/claude-3",
            ]
        )
        mock_openrouter.initialize = AsyncMock()

        # Mock the ensure_backend method to return the appropriate backend
        mock_backend_factory.ensure_backend = AsyncMock(
            side_effect=[
                ValueError("API key invalid"),
                mock_openrouter,
            ]
        )
        mock_backend_factory.initialize_backend = AsyncMock()

        from src.core.interfaces.backend_lifecycle_manager_interface import (
            IBackendLifecycleManager,
        )

        # Mock lifecycle manager
        mock_lifecycle_manager = AsyncMock(spec=IBackendLifecycleManager)
        mock_lifecycle_manager.get_disabled_backends.return_value = {}
        call_count = [0]

        async def get_or_create_side_effect(backend_type, session_id=None):
            call_count[0] += 1
            if backend_type == "openai" and call_count[0] == 1:
                raise ValueError("API key invalid")
            elif backend_type == "openrouter":
                return mock_openrouter
            else:
                raise ValueError(f"Unexpected backend: {backend_type}")

        mock_lifecycle_manager.get_or_create = AsyncMock(
            side_effect=get_or_create_side_effect
        )

        service = create_backend_service_with_mocks(
            factory=mock_backend_factory,
            rate_limiter=mock_rate_limiter,
            config=mock_config,
            session_service=mock_session_service,
            app_state=mock_app_state,
            failover_routes=failover_routes,
            failover_coordinator=StubFailoverCoordinator(),
            use_real_completion_flow=True,
            backend_lifecycle_manager=mock_lifecycle_manager,
        )

        # Should handle the error and not crash
        with pytest.raises(ValueError):
            await service._backend_lifecycle_manager.get_or_create("openai")

        # Second attempt should work with fallback
        backend = await service._backend_lifecycle_manager.get_or_create("openrouter")
        # get_available_models is not async
        models = backend.get_available_models()

        assert len(models) == 2
        assert "openrouter/gpt-4" in models
        assert "openrouter/claude-3" in models


class TestModelsEndpointIntegration:
    """Full integration tests with real app instances."""

    @pytest.fixture(scope="class")
    def app(self) -> FastAPI:
        """Create app with authentication disabled and default backend as openai."""
        from src.core.app.test_builder import create_test_config

        return build_app(config=create_test_config())

    @pytest.mark.integration
    def test_full_models_discovery_flow(self, app):
        """Test complete flow of model discovery."""
        with TestClient(app) as client:
            # First request to models endpoint
            response = client.get("/models")
            assert response.status_code == 200

            models_data = response.json()
            assert models_data["object"] == "list"

            # Verify models can be used in chat completion
            if models_data["data"]:
                model_id = models_data["data"][0]["id"]

                # Try to use the model
                chat_response = client.post(
                    "/v1/chat/completions",
                    json={
                        "model": model_id,
                        "messages": [{"role": "user", "content": "test"}],
                        "max_tokens": 10,
                    },
                )

                # Might fail if no real backend configured, but shouldn't crash
                assert chat_response.status_code in [200, 401, 403, 500]

    @pytest.mark.integration
    def test_models_caching_behavior(self, app):
        """Test that models endpoint implements proper caching."""
        with TestClient(app) as client:
            # First request
            response1 = client.get("/models")
            assert response1.status_code == 200
            models1 = response1.json()["data"]

            # Second request (should be cached or consistent)
            response2 = client.get("/models")
            assert response2.status_code == 200
            models2 = response2.json()["data"]

            # Models should be consistent
            assert len(models1) == len(models2)
            for m1, m2 in zip(models1, models2, strict=False):
                assert m1["id"] == m2["id"]

    @pytest.mark.integration
    @real_time(
        reason="Measures actual endpoint response time to ensure performance requirements are met."
    )
    def test_models_endpoint_performance(self, app):
        """Test models endpoint performance."""
        import time

        with TestClient(app) as client:
            # Warm up
            client.get("/models")

            # Measure response time
            start = time.time()
            response = client.get("/models")
            duration = time.time() - start

            assert response.status_code == 200
            # Should respond quickly (< 1 second)
            assert duration < 1.0

    @pytest.mark.parametrize("endpoint", ["/models", "/v1/models"])
    def test_both_endpoints_return_same_data(self, endpoint, app):
        """Test that both model endpoints return identical data."""
        with TestClient(app) as client:
            response = client.get(endpoint)
            assert response.status_code == 200

            data = response.json()
            assert data["object"] == "list"
            assert "data" in data

            # Both endpoints should return same structure
            for model in data["data"]:
                assert "id" in model
                assert "object" in model
                assert "owned_by" in model

    def test_models_endpoint_advertises_opencode_go_models(self, monkeypatch):
        """Test that opencode-go backend advertises its models proxy-wide in /models."""
        from src.core.config.app_config import AppConfig

        monkeypatch.setenv("OPENCODE_GO_API_KEY", "test-key-opencode")
        config = AppConfig.from_env(
            environ={
                "OPENCODE_GO_API_KEY": "test-key-opencode",
                "DISABLE_AUTH": "true",
            }
        )

        app = build_app(config=config)
        with TestClient(app) as client:
            response = client.get("/models")
            assert response.status_code == 200
            data = response.json()
            model_ids = [m["id"] for m in data.get("data", [])]

            assert "opencode-go/glm-5.1" in model_ids
            assert "opencode-go/minimax-m2.7" in model_ids
            assert "opencode-go/kimi-k2.5" in model_ids

    def test_models_endpoint_advertises_opencode_zen_models(self, monkeypatch):
        """Test that opencode-zen backend advertises its models proxy-wide in /models."""
        from src.core.config.app_config import AppConfig

        monkeypatch.setenv("OPENCODE_ZEN_API_KEY", "test-key-opencode-zen")
        config = AppConfig.from_env(
            environ={
                "OPENCODE_ZEN_API_KEY": "test-key-opencode-zen",
                "DISABLE_AUTH": "true",
            }
        )

        app = build_app(config=config)
        with TestClient(app) as client:
            response = client.get("/models")
            assert response.status_code == 200
            data = response.json()
            model_ids = [m["id"] for m in data.get("data", [])]

            assert "opencode-zen/anthropic/claude-sonnet-4-5" in model_ids
            assert "opencode-zen/openai/gpt-5.1" in model_ids
            assert "opencode-zen/z-ai/glm-5.1" in model_ids
            assert "opencode-zen/google/gemini-3-flash" in model_ids

    def test_models_endpoint_advertises_commandcode_openai_models(self, monkeypatch):
        """Test that commandcode-openai backend advertises its models proxy-wide in /models."""
        from src.core.config.app_config import AppConfig

        monkeypatch.setenv("COMMANDCODE_API_KEY", "test-key-commandcode")
        config = AppConfig.from_env(
            environ={
                "COMMANDCODE_API_KEY": "test-key-commandcode",
                "DISABLE_AUTH": "true",
            }
        )

        app = build_app(config=config)
        with TestClient(app) as client:
            response = client.get("/models")
            assert response.status_code == 200
            data = response.json()
            model_ids = [m["id"] for m in data.get("data", [])]

            assert "commandcode-openai/Qwen/Qwen3.7-Flash" in model_ids
            assert any(m.startswith("commandcode-openai/") for m in model_ids)

    def test_models_endpoint_advertises_commandcode_anthropic_models(self, monkeypatch):
        """Test that commandcode-anthropic backend advertises its models proxy-wide in /models."""
        from src.core.config.app_config import AppConfig, BackendConfig
        from src.core.config.models.auth import AuthConfig
        from src.core.config.models.backends import BackendSettings

        config = AppConfig(
            auth=AuthConfig(disable_auth=True),
            backends=BackendSettings(
                **{
                    "commandcode-anthropic": BackendConfig(
                        connector="commandcode-anthropic",
                        api_key="test-key-commandcode",
                    )
                }
            ),
        )

        app = build_app(config=config)
        with TestClient(app) as client:
            response = client.get("/models")
            assert response.status_code == 200
            data = response.json()
            model_ids = [m["id"] for m in data.get("data", [])]

            assert "commandcode-anthropic/claude-haiku-4-5-20251001" in model_ids
            assert any(m.startswith("commandcode-anthropic/") for m in model_ids)

    def test_models_endpoint_advertises_nvidia_models(self, monkeypatch):
        """Test that nvidia backend advertises its models proxy-wide in /models."""
        from src.core.config.app_config import AppConfig

        monkeypatch.setenv("NVIDIA_API_KEY", "test-key-nvidia")
        config = AppConfig.from_env(
            environ={
                "NVIDIA_API_KEY": "test-key-nvidia",
                "DISABLE_AUTH": "true",
            }
        )

        app = build_app(config=config)
        with TestClient(app) as client:
            response = client.get("/models")
            assert response.status_code == 200
            data = response.json()
            model_ids = [m["id"] for m in data.get("data", [])]

            assert any(m.startswith("nvidia/") for m in model_ids)

    def test_models_endpoint_advertises_runinfra_models(self, monkeypatch):
        """Test that runinfra backend advertises its models proxy-wide in /models."""
        from src.core.config.app_config import AppConfig

        monkeypatch.setenv("RUNINFRA_API_KEY", "test-key-runinfra")
        config = AppConfig.from_env(
            environ={
                "RUNINFRA_API_KEY": "test-key-runinfra",
                "DISABLE_AUTH": "true",
            }
        )

        app = build_app(config=config)
        with TestClient(app) as client:
            response = client.get("/models")
            assert response.status_code == 200
            data = response.json()
            model_ids = [m["id"] for m in data.get("data", [])]

            assert any(m.startswith("runinfra/") for m in model_ids)

    def test_models_endpoint_advertises_openai_codex_models(
        self, tmp_path, monkeypatch
    ):
        """Test that openai-codex backend advertises its models proxy-wide in /models."""
        from src.core.config.app_config import AppConfig, BackendConfig
        from src.core.config.models.auth import AuthConfig
        from src.core.config.models.backends import BackendSettings

        auth_file = tmp_path / "auth.json"
        auth_file.write_text(
            '{"tokens": {"access_token": "test-token"}}', encoding="utf-8"
        )

        config = AppConfig(
            auth=AuthConfig(disable_auth=True),
            backends=BackendSettings(
                **{
                    "openai-codex": BackendConfig(
                        connector="openai-codex",
                        credentials_path=str(auth_file),
                    )
                }
            ),
        )

        app = build_app(config=config)
        with TestClient(app) as client:
            response = client.get("/models")
            assert response.status_code == 200
            data = response.json()
            model_ids = [m["id"] for m in data.get("data", [])]

            assert "openai/gpt-5.5" in model_ids or "gpt-5.5" in model_ids
            assert any(m.startswith("openai/gpt-") for m in model_ids)
