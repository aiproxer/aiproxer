"""Unit tests for BackendConfigProvider."""

import pytest

# Suppress Windows ProactorEventLoop warnings for this module
pytestmark = pytest.mark.filterwarnings(
    "ignore:unclosed event loop <ProactorEventLoop.*:ResourceWarning"
)

from src.core.config.app_config import AppConfig, BackendConfig, BackendSettings
from src.core.interfaces.backend_config_provider_interface import IBackendConfigProvider
from src.core.services.backend_config_provider import BackendConfigProvider


class TestBackendConfigProvider:
    """Test suite for BackendConfigProvider."""

    def test_get_backend_config_with_attribute_access(self) -> None:
        """Test getting a backend config using attribute access."""
        # Arrange
        app_config = AppConfig(
            backends=BackendSettings(test_backend=BackendConfig(api_key="test-key"))
        )
        provider = BackendConfigProvider(app_config)

        # Act
        config = provider.get_backend_config("test_backend")

        # Assert
        assert config is not None
        assert isinstance(config, BackendConfig)
        assert config.api_key == "test-key"

    def test_get_backend_config_with_dict_access(self) -> None:
        """Test getting a backend config using dictionary access."""
        # Arrange
        app_config = AppConfig(
            backends=BackendSettings(openai=BackendConfig(api_key="test-key"))
        )
        provider = BackendConfigProvider(app_config)

        # Act
        config = provider.get_backend_config("openai")

        # Assert
        assert config is not None
        assert isinstance(config, BackendConfig)
        assert config.api_key == "test-key"

    def test_get_backend_config_with_nonexistent_backend(self) -> None:
        """Test getting a config for a backend that doesn't exist."""
        # Arrange
        app_config = AppConfig()
        provider = BackendConfigProvider(app_config)

        # Act
        config = provider.get_backend_config("nonexistent")

        # Assert
        assert config is not None
        assert isinstance(config, BackendConfig)
        assert config.api_key is None

    def test_get_backend_config_with_empty_backend(self) -> None:
        """Test getting a config for a backend with empty config."""
        # Arrange
        app_config = AppConfig(backends=BackendSettings(openai=BackendConfig()))
        provider = BackendConfigProvider(app_config)

        # Act
        config = provider.get_backend_config("openai")

        # Assert
        assert config is not None
        assert isinstance(config, BackendConfig)
        assert config.api_key is None

    def test_openai_responses_falls_back_to_openai_api_key(self) -> None:
        app_config = AppConfig(
            backends={
                "openai": {"api_key": "test-key"},
                "openai-responses": {},
            }
        )
        provider = BackendConfigProvider(app_config)

        config = provider.get_backend_config("openai-responses")
        assert config is not None
        assert config.api_key == "test-key"

    def test_openai_responses_instance_falls_back_to_openai_api_key(self) -> None:
        app_config = AppConfig(
            backends={
                "openai": {"api_key": "test-key"},
            }
        )
        provider = BackendConfigProvider(app_config)

        config = provider.get_backend_config("openai-responses.1")
        assert config is not None
        assert config.api_key == "test-key"

    def test_iter_backend_names(self) -> None:
        """Test iterating over backend names."""
        # Arrange
        app_config = AppConfig(
            backends=BackendSettings(
                test_backend1=BackendConfig(api_key="test-key"),
                test_backend2=BackendConfig(api_key="test-key-2"),
            )
        )
        provider = BackendConfigProvider(app_config)

        # Act
        backend_names = list(provider.iter_backend_names())

        # Assert
        assert "test_backend1" in backend_names
        assert "test_backend2" in backend_names

    def test_iter_backend_names_includes_dict_backends(self) -> None:
        """Configured dictionary backends should be included in iteration."""
        # Arrange
        app_config = AppConfig(
            backends=BackendSettings(
                default_backend="openai",
                custom_backend=BackendConfig(api_key="test-key"),
            )
        )
        provider = BackendConfigProvider(app_config)

        # Act
        backend_names = provider.iter_backend_names()

        # Assert
        assert "custom_backend" in backend_names

    def test_iter_configured_backend_names_excludes_empty_registered_defaults(
        self,
    ) -> None:
        app_config = AppConfig(
            backends=BackendSettings(
                cursor_project=BackendConfig(connector="cursor-cli-acp"),
                empty_backend=BackendConfig(),
            )
        )
        provider = BackendConfigProvider(app_config)

        assert list(provider.iter_configured_backend_names()) == ["cursor_project"]

    def test_get_default_backend(self) -> None:
        """Test getting the default backend."""
        # Arrange
        app_config = AppConfig(backends=BackendSettings(default_backend="gemini"))
        provider = BackendConfigProvider(app_config)

        # Act
        default_backend = provider.get_default_backend()

        # Assert
        assert default_backend == "gemini"

    def test_get_default_backend_fallback(self) -> None:
        """Test getting the default backend when not set."""
        # Arrange
        app_config = AppConfig(backends=BackendSettings(default_backend=""))
        provider = BackendConfigProvider(app_config)

        # Act
        default_backend = provider.get_default_backend()

        # Assert
        assert default_backend == "openai"  # Default fallback

    def test_functional_backends(self) -> None:
        """Test getting functional backends."""
        # Arrange
        app_config = AppConfig(
            backends=BackendSettings(
                test_backend1=BackendConfig(api_key="test-key"),
                test_backend2=BackendConfig(),
            )
        )
        provider = BackendConfigProvider(app_config)

        # Act
        functional_backends = provider.get_functional_backends()

        # Assert
        assert "test_backend1" in functional_backends
        assert "test_backend2" not in functional_backends

    def test_implements_interface(self) -> None:
        """Test that BackendConfigProvider implements IBackendConfigProvider."""
        # Arrange
        app_config = AppConfig()
        provider = BackendConfigProvider(app_config)

        # Act/Assert
        assert isinstance(provider, IBackendConfigProvider)

    def test_empty_hyphen_discovery_prefers_filled_underscore_yaml(self) -> None:
        """Empty discovery default under hyphen must not beat filled YAML under underscore."""
        app_config = AppConfig(
            backends={
                "openai-chatgpt-plan": {},
                "openai_chatgpt_plan": {
                    "extra": {"chatgpt_plan": {"profile_id": "primary"}},
                    "api_url": "http://example.com",
                },
            }
        )
        provider = BackendConfigProvider(app_config)

        for lookup in ("openai-chatgpt-plan", "openai_chatgpt_plan"):
            config = provider.get_backend_config(lookup)
            assert config is not None
            assert config.extra == {"chatgpt_plan": {"profile_id": "primary"}}
            assert config.api_url == "http://example.com"

    def test_both_filled_aliases_prefer_richer_config(self) -> None:
        """When both hyphen and underscore entries are filled, prefer the richer one."""
        app_config = AppConfig(
            backends={
                "openai-chatgpt-plan": {
                    "api_url": "http://hyphen.example",
                    "models": ["m1"],
                },
                "openai_chatgpt_plan": {
                    "api_url": "http://underscore.example",
                    "models": ["m1", "m2"],
                    "extra": {"chatgpt_plan": {"profile_id": "primary"}},
                    "timeout": 60,
                },
            }
        )
        provider = BackendConfigProvider(app_config)

        config = provider.get_backend_config("openai-chatgpt-plan")
        assert config is not None
        assert config.extra == {"chatgpt_plan": {"profile_id": "primary"}}
        assert config.api_url == "http://underscore.example"
        assert config.models == ["m1", "m2"]
        assert config.timeout == 60

    def test_api_key_preference_still_wins_over_richer_without_key(self) -> None:
        """Existing credential preference: a config with api_key beats a richer one without."""
        app_config = AppConfig(
            backends={
                "my-backend": {
                    "api_key": "secret-key",
                },
                "my_backend": {
                    "extra": {"nested": {"value": 1}},
                    "api_url": "http://rich.example",
                    "models": ["a", "b"],
                    "timeout": 30,
                },
            }
        )
        provider = BackendConfigProvider(app_config)

        config = provider.get_backend_config("my-backend")
        assert config is not None
        assert config.api_key == "secret-key"

    def test_openai_responses_fallback_still_works_with_empty_hyphen_and_underscore(
        self,
    ) -> None:
        """openai-responses credential fallback to openai must remain intact."""
        app_config = AppConfig(
            backends={
                "openai": {"api_key": "openai-key"},
                "openai-responses": {},
                "openai_responses": {},
            }
        )
        provider = BackendConfigProvider(app_config)

        for lookup in ("openai-responses", "openai_responses"):
            config = provider.get_backend_config(lookup)
            assert config is not None
            assert config.api_key == "openai-key"
