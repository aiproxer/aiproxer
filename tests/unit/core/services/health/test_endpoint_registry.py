"""Tests for the EndpointRegistry class."""

from __future__ import annotations

from src.core.services.health.endpoint_registry import EndpointRegistry


class TestEndpointRegistry:
    """Tests for EndpointRegistry."""

    def test_register_backend_creates_health_state(self) -> None:
        """Test that registering a backend creates a health state."""
        registry = EndpointRegistry()

        state = registry.register_backend("openai.1", "https://api.openai.com/v1")

        assert state is not None
        assert state.api_url == "https://api.openai.com/v1"
        assert state.is_healthy is True  # Optimistic default

    def test_register_multiple_backends_same_url(self) -> None:
        """Test that multiple backends can share the same URL."""
        registry = EndpointRegistry()

        state1 = registry.register_backend("openai.1", "https://api.openai.com/v1")
        state2 = registry.register_backend("openai.2", "https://api.openai.com/v1")

        # Should return the same health state object
        assert state1 is state2

        # Both backends should be registered
        backends = registry.get_backends_for_url("https://api.openai.com/v1")
        assert "openai.1" in backends
        assert "openai.2" in backends
        assert len(backends) == 2

    def test_register_different_urls(self) -> None:
        """Test registering backends with different URLs."""
        registry = EndpointRegistry()

        registry.register_backend("openai.1", "https://api.openai.com/v1")
        registry.register_backend("anthropic.1", "https://api.anthropic.com")

        urls = registry.get_all_urls()
        assert len(urls) == 2

    def test_unregister_backend(self) -> None:
        """Test unregistering a backend."""
        registry = EndpointRegistry()

        registry.register_backend("openai.1", "https://api.openai.com/v1")
        registry.register_backend("openai.2", "https://api.openai.com/v1")
        registry.unregister_backend("openai.1")

        backends = registry.get_backends_for_url("https://api.openai.com/v1")
        assert "openai.1" not in backends
        assert "openai.2" in backends

        # Verify the URL state wasn't deleted since another backend uses it
        assert "https://api.openai.com/v1" in registry._health_states

        # Unregister the second backend
        registry.unregister_backend("openai.2")

        # Verify the URL state is deleted when no backends use it
        assert "https://api.openai.com/v1" not in registry._health_states

    def test_get_url_for_backend(self) -> None:
        """Test getting URL for a backend."""
        registry = EndpointRegistry()

        registry.register_backend("openai.1", "https://api.openai.com/v1")

        url = registry.get_url_for_backend("openai.1")
        assert url == "https://api.openai.com/v1"

        # Non-existent backend
        assert registry.get_url_for_backend("unknown") is None

    def test_normalize_url(self) -> None:
        """Test URL normalization."""
        # Test trailing slash removal
        assert (
            EndpointRegistry._normalize_url("https://api.openai.com/v1/")
            == "https://api.openai.com/v1"
        )

        # Test lowercase scheme and host (path is case-sensitive per RFC)
        assert (
            EndpointRegistry._normalize_url("HTTPS://API.OPENAI.COM/v1")
            == "https://api.openai.com/v1"
        )

        # Test port removal for default ports
        assert (
            EndpointRegistry._normalize_url("https://api.openai.com:443/v1")
            == "https://api.openai.com/v1"
        )

        # Test non-default port preservation
        assert (
            EndpointRegistry._normalize_url("https://api.openai.com:8080/v1")
            == "https://api.openai.com:8080/v1"
        )

    def test_extract_hostname(self) -> None:
        """Test hostname extraction."""
        assert (
            EndpointRegistry.extract_hostname("https://api.openai.com/v1")
            == "api.openai.com"
        )
        assert (
            EndpointRegistry.extract_hostname("https://api.openai.com:8080/v1")
            == "api.openai.com"
        )

    def test_is_url_healthy(self) -> None:
        """Test URL health status check."""
        registry = EndpointRegistry()

        registry.register_backend("openai.1", "https://api.openai.com/v1")

        # Initially healthy (optimistic)
        assert registry.is_url_healthy("https://api.openai.com/v1") is True

        # Unknown URL returns True (assume healthy)
        assert registry.is_url_healthy("https://unknown.com") is True

    def test_is_backend_healthy(self) -> None:
        """Test backend health status check."""
        registry = EndpointRegistry()

        registry.register_backend("openai.1", "https://api.openai.com/v1")

        assert registry.is_backend_healthy("openai.1") is True
        # Unknown backend returns True
        assert registry.is_backend_healthy("unknown") is True

    def test_clear(self) -> None:
        """Test clearing the registry."""
        registry = EndpointRegistry()

        registry.register_backend("openai.1", "https://api.openai.com/v1")
        registry.clear()

        assert len(registry) == 0
        assert registry.get_all_urls() == []

    def test_len(self) -> None:
        """Test length of registry."""
        registry = EndpointRegistry()

        assert len(registry) == 0

        registry.register_backend("openai.1", "https://api.openai.com/v1")
        assert len(registry) == 1

        registry.register_backend("openai.2", "https://api.openai.com/v1")
        assert len(registry) == 1  # Same URL

        registry.register_backend("anthropic.1", "https://api.anthropic.com")
        assert len(registry) == 2

    def test_backend_changes_url(self) -> None:
        """Test backend changing its URL."""
        registry = EndpointRegistry()

        registry.register_backend("openai.1", "https://api.openai.com/v1")
        registry.register_backend("openai.1", "https://new-api.openai.com/v1")

        # Should no longer be associated with old URL
        backends = registry.get_backends_for_url("https://api.openai.com/v1")
        assert "openai.1" not in backends

        # Should be associated with new URL
        backends = registry.get_backends_for_url("https://new-api.openai.com/v1")
        assert "openai.1" in backends
