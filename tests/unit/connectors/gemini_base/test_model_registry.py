"""
Unit tests for GeminiModelRegistry.

Tests verify model discovery, caching, validation, and name mapping.
"""

from unittest.mock import AsyncMock, Mock

import httpx
import pytest
from src.connectors.gemini_base.config import DEFAULT_AVAILABLE_MODELS
from src.connectors.gemini_base.interfaces import (
    ICredentialCoordinator,
    IEndpointConfig,
    IModelDiscoveryStrategy,
)
from src.connectors.gemini_base.model_registry import GeminiModelRegistry
from src.connectors.gemini_base.models import GeminiOAuthCredentials
from src.core.common.exceptions import BackendError


@pytest.fixture(autouse=True)
def clear_global_cache():
    """Clear GeminiModelRegistry global cache before each test to ensure test isolation."""
    GeminiModelRegistry._global_loaded_models.clear()


@pytest.fixture
def mock_model_discovery():
    """Create a mock IModelDiscoveryStrategy."""
    discovery = Mock(spec=IModelDiscoveryStrategy)
    discovery.discover = AsyncMock(return_value=["gemini-2.5-pro", "gemini-2.5-flash"])
    discovery.get_fallback_models.return_value = DEFAULT_AVAILABLE_MODELS
    return discovery


@pytest.fixture
def mock_endpoint_config():
    """Create a mock IEndpointConfig."""
    config = Mock(spec=IEndpointConfig)
    config.get_base_url.return_value = "https://cloudcode-pa.googleapis.com"
    config.get_api_headers.return_value = {"Authorization": "Bearer test_token"}
    return config


@pytest.fixture
def mock_credential_coordinator():
    """Create a mock ICredentialCoordinator."""
    coordinator = Mock(spec=ICredentialCoordinator)
    coordinator.credentials = GeminiOAuthCredentials(
        access_token="test_token",
        refresh_token="refresh_token",
        expiry_date=9999999999999,
    )
    return coordinator


@pytest.fixture
def mock_http_client():
    """Create a mock httpx.AsyncClient."""
    return Mock(spec=httpx.AsyncClient)


@pytest.fixture
def registry(
    mock_model_discovery,
    mock_endpoint_config,
    mock_credential_coordinator,
    mock_http_client,
):
    """Create a GeminiModelRegistry instance."""
    return GeminiModelRegistry(
        model_discovery=mock_model_discovery,
        endpoint_config=mock_endpoint_config,
        credential_coordinator=mock_credential_coordinator,
        http_client=mock_http_client,
    )


class TestEnsureLoaded:
    """Test ensure_loaded method."""

    @pytest.mark.asyncio
    async def test_ensure_loaded_discovers_models_via_api(
        self, registry, mock_model_discovery, mock_endpoint_config, mock_http_client
    ):
        """Verify API discovery is used."""
        # Setup
        mock_model_discovery.discover.return_value = [
            "gemini-2.5-pro",
            "gemini-2.5-flash",
            "gemini-1.5-pro",
        ]

        # Execute
        await registry.ensure_loaded()

        # Verify
        mock_model_discovery.discover.assert_called_once()
        assert len(registry._available_models) > 0
        assert registry._models_from_api is True

    @pytest.mark.asyncio
    async def test_ensure_loaded_falls_back_to_hardcoded_list(
        self, registry, mock_model_discovery
    ):
        """Verify fallback behavior when API fails."""
        # Setup - API discovery fails
        mock_model_discovery.discover.return_value = []

        # Execute
        await registry.ensure_loaded()

        # Verify fallback models are used
        assert len(registry._available_models) > 0
        assert registry._models_from_api is False
        # Should contain fallback models
        assert any("gemini-2.5-pro" in m for m in registry._available_models)

    @pytest.mark.asyncio
    async def test_ensure_loaded_caches_results(self, registry, mock_model_discovery):
        """Verify caching (no duplicate API calls)."""
        # Execute twice
        await registry.ensure_loaded()
        await registry.ensure_loaded()

        # Verify discover was called only once (cached on second call)
        assert mock_model_discovery.discover.call_count == 1

    @pytest.mark.asyncio
    async def test_ensure_loaded_requires_valid_credentials(
        self, mock_model_discovery, mock_endpoint_config, mock_http_client
    ):
        """Verify credential dependency."""
        # Setup - no credentials
        mock_credential_coordinator = Mock(spec=ICredentialCoordinator)
        mock_credential_coordinator.credentials = None

        registry = GeminiModelRegistry(
            model_discovery=mock_model_discovery,
            endpoint_config=mock_endpoint_config,
            credential_coordinator=mock_credential_coordinator,
            http_client=mock_http_client,
        )

        # Execute - should fallback when no credentials
        await registry.ensure_loaded()

        # Verify fallback was used
        assert registry._models_from_api is False


class TestValidate:
    """Test validate method."""

    @pytest.mark.asyncio
    async def test_validate_raises_for_invalid_model(self, registry):
        """Verify validation raises BackendError."""
        # Setup - models loaded from API
        registry._available_models = ["gemini-2.5-pro", "gemini-2.5-flash"]
        registry._available_models_set = {"gemini-2.5-pro", "gemini-2.5-flash"}
        registry._models_from_api = True

        # Execute and verify exception
        with pytest.raises(BackendError) as exc_info:
            registry.validate("invalid-model")

        assert "not available" in exc_info.value.message.lower()
        assert exc_info.value.code == "model_not_found"
        assert exc_info.value.status_code == 400

    @pytest.mark.asyncio
    async def test_validate_skips_when_not_from_api(self, registry):
        """Verify validation skip for fallback."""
        # Setup - using fallback models
        registry._available_models = DEFAULT_AVAILABLE_MODELS
        registry._available_models_set = set(DEFAULT_AVAILABLE_MODELS)
        registry._models_from_api = False

        # Execute - should not raise
        registry.validate("some-model")  # Should not raise

    @pytest.mark.asyncio
    async def test_validate_passes_for_valid_model(self, registry):
        """Verify validation passes for valid model."""
        # Setup
        registry._available_models = ["gemini-2.5-pro", "gemini-2.5-flash"]
        registry._available_models_set = {"gemini-2.5-pro", "gemini-2.5-flash"}
        registry._models_from_api = True

        # Execute - should not raise
        registry.validate("gemini-2.5-pro")


class TestNameMapping:
    """Test name mapping methods."""

    def test_to_public_name_maps_internal_to_public(self, registry):
        """Verify public name mapping."""
        # Setup mapping
        registry._public_to_internal_map = {"gemini-3-pro": "gemini-3-pro-preview"}

        # Execute
        result = registry.to_public_name("gemini-3-pro-preview")

        # Verify
        assert result == "gemini-3-pro"

    def test_to_public_name_returns_original_when_no_mapping(self, registry):
        """Verify original name returned when no mapping exists."""
        registry._public_to_internal_map = {}

        result = registry.to_public_name("gemini-2.5-pro")

        assert result == "gemini-2.5-pro"

    def test_to_internal_name_maps_public_to_internal(self, registry):
        """Verify internal name mapping."""
        # Setup mapping
        registry._public_to_internal_map = {"gemini-3-pro": "gemini-3-pro-preview"}

        # Execute
        result = registry.to_internal_name("gemini-3-pro")

        # Verify
        assert result == "gemini-3-pro-preview"

    def test_to_internal_name_returns_original_when_no_mapping(self, registry):
        """Verify original name returned when no mapping exists."""
        registry._public_to_internal_map = {}

        result = registry.to_internal_name("gemini-2.5-pro")

        assert result == "gemini-2.5-pro"


class TestListPublicModels:
    """Test list_public_models method."""

    @pytest.mark.asyncio
    async def test_list_public_models_adds_vendor_prefix(self, registry):
        """Verify vendor prefix addition."""
        # Setup
        registry._available_models = ["gemini-2.5-pro", "gemini-2.5-flash"]
        registry._public_to_internal_map = {}
        registry._loaded = True  # Mark as loaded

        # Execute
        result = registry.list_public_models()

        # Verify
        assert len(result) == 2
        assert all(model.startswith("google/") for model in result)
        assert "google/gemini-2.5-pro" in result
        assert "google/gemini-2.5-flash" in result

    @pytest.mark.asyncio
    async def test_list_public_models_applies_public_mapping(self, registry):
        """Verify public name mapping is applied."""
        # Setup with mapping
        registry._available_models = ["gemini-3-pro-preview"]
        registry._public_to_internal_map = {"gemini-3-pro": "gemini-3-pro-preview"}
        registry._loaded = True  # Mark as loaded

        # Execute
        result = registry.list_public_models()

        # Verify - should map to public name and add prefix
        assert "google/gemini-3-pro" in result
        assert "google/gemini-3-pro-preview" not in result

    @pytest.mark.asyncio
    async def test_ensure_loaded_handles_api_discovery_exception(
        self, registry, mock_model_discovery, mock_endpoint_config, mock_http_client
    ):
        """Verify API discovery exceptions are handled gracefully with fallback.

        Requirement: 4.1 (unit testability), edge case coverage.
        """
        # Setup - API discovery raises exception
        mock_model_discovery.discover.side_effect = Exception("API discovery failed")

        # Execute - should not raise, should use fallback
        await registry.ensure_loaded()

        # Verify fallback models are used
        assert len(registry._available_models) > 0
        assert registry._models_from_api is False
        assert registry._loaded is True

    @pytest.mark.asyncio
    async def test_concurrent_ensure_loaded_calls(self, registry, mock_model_discovery):
        """Verify concurrent ensure_loaded calls are safe and don't cause duplicate API calls.

        Requirement: 4.1 (unit testability), edge case coverage.
        """
        import asyncio

        # Setup mock to return models
        mock_model_discovery.discover.return_value = [
            "gemini-2.5-pro",
            "gemini-2.5-flash",
        ]

        # Execute multiple concurrent calls
        await asyncio.gather(
            registry.ensure_loaded(),
            registry.ensure_loaded(),
            registry.ensure_loaded(),
        )

        # Should only call discover once (cached on subsequent calls)
        assert mock_model_discovery.discover.call_count == 1
        assert registry._loaded is True

    def test_concurrent_validate_calls(self, registry):
        """Verify concurrent validate calls are safe (validate is synchronous).

        Requirement: 4.1 (unit testability), edge case coverage.
        """
        import threading

        # Setup models
        registry._available_models = ["gemini-2.5-pro", "gemini-2.5-flash"]
        registry._available_models_set = {"gemini-2.5-pro", "gemini-2.5-flash"}
        registry._models_from_api = True

        # Execute multiple concurrent validations using threads
        results = []
        errors = []

        def validate_model():
            try:
                registry.validate("gemini-2.5-pro")
                results.append(True)
            except Exception as e:
                errors.append(e)

        threads = [threading.Thread(target=validate_model) for _ in range(10)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()

        # All should succeed (no exceptions)
        assert len(errors) == 0, f"Validation errors occurred: {errors}"
        assert len(results) == 10
