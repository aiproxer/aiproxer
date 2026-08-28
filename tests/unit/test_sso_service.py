"""Unit tests for SSO Service."""

from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest
from src.core.auth.sso.config import AuthorizationConfig, ProviderConfig, SSOConfig
from src.core.auth.sso.exceptions import AuthenticationError, ConfigurationError
from src.core.auth.sso.sso_service import SSOService


@pytest.fixture(autouse=True)
def mock_sso_discovery_api(respx_mock):
    """Global mock for OIDC discovery API calls."""
    metadata = {
        "authorization_endpoint": "https://accounts.google.com/o/oauth2/v2/auth",
        "token_endpoint": "https://oauth2.googleapis.com/token",
        "userinfo_endpoint": "https://openidconnect.googleapis.com/v1/userinfo",
        "jwks_uri": "https://www.googleapis.com/oauth2/v3/certs",
        "issuer": "https://accounts.google.com",
    }
    respx_mock.get("https://accounts.google.com/.well-known/openid-configuration").mock(
        return_value=httpx.Response(200, json=metadata)
    )
    # Also mock JWKS and Userinfo endpoints
    respx_mock.get("https://www.googleapis.com/oauth2/v3/certs").mock(
        return_value=httpx.Response(200, json={"keys": []})
    )
    respx_mock.get("https://openidconnect.googleapis.com/v1/userinfo").mock(
        return_value=httpx.Response(
            200, json={"sub": "user123", "email": "user@example.com"}
        )
    )
    return respx_mock


@pytest.fixture
def google_provider_config():
    """Google OAuth2 provider configuration."""
    return ProviderConfig(
        type="oauth2",
        client_id="test-client-id",
        client_secret="test-client-secret",
        discovery_url="https://accounts.google.com/.well-known/openid-configuration",
        scopes=["openid", "email", "profile"],
    )


@pytest.fixture
def github_provider_config():
    """GitHub OAuth2 provider configuration (manual)."""
    return ProviderConfig(
        type="oauth2",
        client_id="github-client-id",
        client_secret="github-client-secret",
        authorize_url="https://github.com/login/oauth/authorize",
        token_url="https://github.com/login/oauth/access_token",
        userinfo_url="https://api.github.com/user",
        scopes=["user:email"],
    )


@pytest.fixture
def sso_config(google_provider_config, github_provider_config):
    """SSO configuration with multiple providers."""
    return SSOConfig(
        enabled=True,
        session_lifetime_hours=24,
        providers={
            "google": google_provider_config,
            "github": github_provider_config,
        },
        authorization=AuthorizationConfig(mode="single_user"),
    )


@pytest.fixture
def sso_service(sso_config):
    """SSO service instance."""
    return SSOService(sso_config)


class TestSSOServiceBasics:
    """Test basic SSO service functionality."""

    def test_initialization(self, sso_service, sso_config):
        """Test SSO service initialization."""
        assert sso_service.config == sso_config
        assert sso_service._jwt is not None

    def test_get_supported_providers(self, sso_service):
        """Test getting list of supported providers."""
        providers = sso_service.get_supported_providers()
        assert "google" in providers
        assert "github" in providers
        assert len(providers) == 2

    def test_get_provider_config_success(self, sso_service, google_provider_config):
        """Test getting provider configuration."""
        config = sso_service._get_provider_config("google")
        assert config == google_provider_config

    def test_get_provider_config_not_found(self, sso_service):
        """Test getting non-existent provider configuration."""
        with pytest.raises(ConfigurationError, match="not configured"):
            sso_service._get_provider_config("nonexistent")


class TestOAuth2AuthorizationURL:
    """Test OAuth2 authorization URL generation."""

    @pytest.mark.asyncio
    async def test_create_authorization_url_with_discovery(self, sso_service):
        """Test creating authorization URL with OIDC discovery."""
        with patch(
            "src.core.auth.sso.sso_service.AsyncOAuth2Client"
        ) as mock_client_class:
            # Mock the OAuth2 client
            mock_client = AsyncMock()
            mock_client_class.return_value = mock_client

            # Mock URL creation
            mock_client.create_authorization_url = MagicMock(
                return_value=(
                    "https://accounts.google.com/o/oauth2/v2/auth?state=test123",
                    None,
                )
            )

            # Test
            url = await sso_service.create_authorization_url(
                provider="google",
                state="test123",
                redirect_uri="http://localhost:8080/auth/callback",
            )

            # Verify
            assert url.startswith("https://accounts.google.com/o/oauth2/v2/auth")
            mock_client.create_authorization_url.assert_called_once()

    @pytest.mark.asyncio
    async def test_create_authorization_url_manual(self, sso_service):
        """Test creating authorization URL with manual configuration."""
        with patch(
            "src.core.auth.sso.sso_service.AsyncOAuth2Client"
        ) as mock_client_class:
            # Mock the OAuth2 client
            mock_client = AsyncMock()
            mock_client_class.return_value = mock_client

            # Mock URL creation
            mock_client.create_authorization_url = MagicMock(
                return_value=(
                    "https://github.com/login/oauth/authorize?state=test456",
                    None,
                )
            )

            # Test
            url = await sso_service.create_authorization_url(
                provider="github",
                state="test456",
                redirect_uri="http://localhost:8080/auth/callback",
            )

            # Verify
            assert url.startswith("https://github.com/login/oauth/authorize")
            mock_client.create_authorization_url.assert_called_once()

    @pytest.mark.asyncio
    async def test_create_authorization_url_missing_endpoint(self, sso_config):
        """Test error when provider has no authorization endpoint."""
        # Create provider with no endpoints
        bad_config = ProviderConfig(
            type="oauth2",
            client_id="test",
            client_secret="test",
            scopes=["openid"],
        )
        sso_config.providers["bad"] = bad_config
        service = SSOService(sso_config)

        with pytest.raises(ConfigurationError, match="discovery_url.*authorize_url"):
            await service.create_authorization_url(
                provider="bad", state="test", redirect_uri="http://localhost/callback"
            )

    @pytest.mark.asyncio
    async def test_create_authorization_url_saml_not_implemented(self, sso_config):
        """Test SAML provider raises NotImplementedError."""
        saml_config = ProviderConfig(
            type="saml",
            client_id="test",
            client_secret="test",
            metadata_url="https://example.com/saml/metadata",
        )
        sso_config.providers["saml"] = saml_config
        service = SSOService(sso_config)

        # Mock httpx to fail immediately instead of waiting for timeout
        with patch("httpx.AsyncClient") as mock_client_class:
            mock_client = AsyncMock()
            mock_client_class.return_value.__aenter__.return_value = mock_client
            mock_client.get = AsyncMock(
                side_effect=httpx.ConnectError("Connection failed")
            )

            with pytest.raises(
                AuthenticationError, match="Failed to fetch SAML metadata"
            ):
                await service.create_authorization_url(
                    provider="saml",
                    state="test",
                    redirect_uri="http://localhost/callback",
                )


class TestOAuth2Callback:
    """Test OAuth2 callback handling."""

    @pytest.mark.asyncio
    async def test_handle_callback_with_id_token(self, sso_service):
        """Test handling callback with OIDC ID token."""
        with patch(
            "src.core.auth.sso.sso_service.AsyncOAuth2Client"
        ) as mock_client_class:
            # Mock the OAuth2 client
            mock_client = AsyncMock()
            mock_client_class.return_value = mock_client

            # Mock token exchange
            mock_client.fetch_token = AsyncMock(
                return_value={
                    "access_token": "test-access-token",
                    "id_token": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwiZW1haWwiOiJ0ZXN0QGV4YW1wbGUuY29tIn0.signature",
                }
            )

            # Mock JWT decode
            with patch.object(sso_service._jwt, "decode") as mock_decode:
                mock_decode.return_value = {
                    "sub": "1234567890",
                    "email": "test@example.com",
                }

                # Test
                result = await sso_service.handle_callback(
                    provider="google",
                    code="test-code",
                    state="test-state",
                    redirect_uri="http://localhost:8080/auth/callback",
                )

                # Verify
                assert result.success is True
                assert result.user_id == "1234567890"
                assert result.user_email == "test@example.com"
                assert result.provider == "google"
                mock_client.fetch_token.assert_called_once()

    @pytest.mark.asyncio
    async def test_handle_callback_with_userinfo(self, sso_service):
        """Test handling callback with userinfo endpoint."""
        with patch(
            "src.core.auth.sso.sso_service.AsyncOAuth2Client"
        ) as mock_client_class:
            # Mock the OAuth2 client
            mock_client = AsyncMock()
            mock_client_class.return_value = mock_client

            # Mock token exchange (no ID token)
            mock_client.fetch_token = AsyncMock(
                return_value={
                    "access_token": "test-access-token",
                }
            )

            # Mock userinfo request
            mock_client.get = AsyncMock(
                return_value=httpx.Response(
                    200,
                    json={"sub": "user123", "email": "user@example.com"},
                    request=httpx.Request(
                        "GET", "https://openidconnect.googleapis.com/v1/userinfo"
                    ),
                )
            )

            # Test
            result = await sso_service.handle_callback(
                provider="google",
                code="test-code",
                state="test-state",
                redirect_uri="http://localhost:8080/auth/callback",
            )

            # Verify
            assert result.success is True
            assert result.user_id == "user123"
            assert result.user_email == "user@example.com"
            mock_client.get.assert_called_once()

    @pytest.mark.asyncio
    async def test_handle_callback_github_specific(self, sso_service):
        """Test handling callback with GitHub-specific API."""
        with patch(
            "src.core.auth.sso.sso_service.AsyncOAuth2Client"
        ) as mock_client_class:
            # Mock the OAuth2 client
            mock_client = AsyncMock()
            mock_client_class.return_value = mock_client

            # Mock token exchange
            mock_client.fetch_token = AsyncMock(
                return_value={
                    "access_token": "github-token",
                }
            )

            # Mock GitHub user API
            mock_client.get = AsyncMock(
                return_value=httpx.Response(
                    200,
                    json={
                        "id": 12345,
                        "login": "testuser",
                        "email": "test@github.com",
                    },
                    request=httpx.Request("GET", "https://api.github.com/user"),
                )
            )

            # Test
            result = await sso_service.handle_callback(
                provider="github",
                code="test-code",
                state="test-state",
                redirect_uri="http://localhost:8080/auth/callback",
            )

            # Verify
            assert result.success is True
            assert result.user_id == "12345"
            assert result.user_email == "test@github.com"

    @pytest.mark.asyncio
    async def test_handle_callback_no_user_id(self, sso_service):
        """Test callback raises error when user ID cannot be determined."""
        with patch(
            "src.core.auth.sso.sso_service.AsyncOAuth2Client"
        ) as mock_client_class:
            # Mock the OAuth2 client
            mock_client = AsyncMock()
            mock_client_class.return_value = mock_client

            # Mock metadata
            mock_client.metadata = {
                "token_endpoint": "https://oauth2.googleapis.com/token",
            }
            mock_client.load_server_metadata = AsyncMock()

            # Mock token exchange (no ID token, no userinfo)
            mock_client.fetch_token = AsyncMock(
                return_value={
                    "access_token": "test-access-token",
                }
            )

            # Test - should raise AuthenticationError
            with pytest.raises(
                AuthenticationError, match="Could not determine user ID"
            ):
                await sso_service.handle_callback(
                    provider="google",
                    code="test-code",
                    state="test-state",
                    redirect_uri="http://localhost:8080/auth/callback",
                )

    @pytest.mark.asyncio
    async def test_handle_callback_missing_token_endpoint(self, sso_config):
        """Test callback fails when token endpoint is not configured."""
        # Create provider with no token endpoint
        bad_config = ProviderConfig(
            type="oauth2",
            client_id="test",
            client_secret="test",
            authorize_url="https://example.com/auth",
            scopes=["openid"],
        )
        sso_config.providers["bad"] = bad_config
        service = SSOService(sso_config)

        with pytest.raises(ConfigurationError, match="Token endpoint"):
            await service.handle_callback(
                provider="bad",
                code="test-code",
                state="test-state",
                redirect_uri="http://localhost/callback",
            )
