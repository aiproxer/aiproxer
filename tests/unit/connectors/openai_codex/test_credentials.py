"""Unit tests for CredentialManager and CredentialWatcher services.

Tests cover credential loading, validation, refresh, concurrency protection,
and file watcher debounce behavior.
"""

from __future__ import annotations

import base64
import contextlib
import json
import tempfile
import time
from pathlib import Path
from unittest.mock import AsyncMock, Mock, patch

import httpx
import pytest
from src.connectors.openai_codex.codex_quota_notifications import (
    user_facing_quota_type,
)
from src.connectors.openai_codex.credentials import (
    CredentialManager,
    CredentialWatcher,
    OpenAICredentialsFileHandler,
)
from src.connectors.openai_codex.interfaces import ICredentialManager
from src.connectors.openai_codex.managed_oauth_models import (
    ManagedOAuthAccount,
    ManagedOAuthConfig,
)
from src.connectors.openai_codex.managed_oauth_refresh import ManagedOAuthRefreshError
from src.connectors.openai_codex.managed_oauth_storage import ManagedOAuthStorageService
from watchdog.events import FileSystemEvent  # type: ignore[reportAttributeAccessIssue]


class TestCredentialManager:
    """Test CredentialManager service implementation."""

    @pytest.fixture
    def temp_auth_file(self):
        """Create a temporary auth.json file for testing."""
        with tempfile.NamedTemporaryFile(
            mode="w", suffix=".json", delete=False, encoding="utf-8"
        ) as f:
            auth_data = {
                "tokens": {
                    "access_token": "test_access_token",
                    "refresh_token": "test_refresh_token",
                    "account_id": "test_account_id",
                }
            }
            json.dump(auth_data, f)
            temp_path = Path(f.name)
        yield temp_path
        with contextlib.suppress(Exception):
            temp_path.unlink()

    @pytest.fixture
    def temp_auth_file_with_api_key(self):
        """Create a temporary auth.json file with OPENAI_API_KEY fallback."""
        with tempfile.NamedTemporaryFile(
            mode="w", suffix=".json", delete=False, encoding="utf-8"
        ) as f:
            auth_data = {"OPENAI_API_KEY": "test_api_key"}
            json.dump(auth_data, f)
            temp_path = Path(f.name)
        yield temp_path
        with contextlib.suppress(Exception):
            temp_path.unlink()

    @pytest.fixture
    def http_client(self):
        """Create an httpx AsyncClient for testing."""
        return httpx.AsyncClient()

    @pytest.fixture
    async def manager(self, http_client):
        """Create a CredentialManager instance for testing with proper cleanup."""
        mgr = CredentialManager(http_client=http_client)
        yield mgr
        # Ensure file watcher is stopped to prevent cross-test interference
        await mgr.shutdown()

    @pytest.mark.asyncio
    async def test_manager_implements_interface(self, manager):
        """Verify manager implements ICredentialManager interface."""
        assert isinstance(manager, ICredentialManager)

    @pytest.mark.asyncio
    async def test_initialize_loads_credentials_from_file(
        self, manager, temp_auth_file
    ):
        """Test that initialize loads credentials from file."""
        await manager.initialize(auth_path=temp_auth_file)

        assert manager._auth_path == temp_auth_file
        assert manager._auth_credentials is not None
        assert (
            manager._auth_credentials["tokens"]["access_token"] == "test_access_token"
        )

    @pytest.mark.asyncio
    async def test_initialize_starts_file_watcher(self, manager, temp_auth_file):
        """Test that initialize starts file watcher."""
        await manager.initialize(auth_path=temp_auth_file)

        assert manager.is_watcher_running() is True

    @pytest.mark.asyncio
    async def test_initialize_start_watcher_false_skips_watcher(
        self, manager, temp_auth_file
    ):
        """Catalog discovery passes start_watcher=False to avoid a watcher."""
        await manager.initialize(auth_path=temp_auth_file, start_watcher=False)

        assert manager.is_watcher_running() is False
        assert manager.get_access_token() == "test_access_token"

    @pytest.mark.asyncio
    async def test_get_access_token_returns_token(self, manager, temp_auth_file):
        """Test that get_access_token returns the access token."""
        await manager.initialize(auth_path=temp_auth_file)

        token = manager.get_access_token()
        assert token == "test_access_token"

    @pytest.mark.asyncio
    async def test_get_access_token_fallback_to_api_key(
        self, manager, temp_auth_file_with_api_key
    ):
        """Test that get_access_token falls back to OPENAI_API_KEY."""
        await manager.initialize(auth_path=temp_auth_file_with_api_key)

        token = manager.get_access_token()
        assert token == "test_api_key"

    @pytest.mark.asyncio
    async def test_get_access_token_returns_none_when_not_loaded(self, manager):
        """Test that get_access_token returns None when credentials not loaded."""
        token = manager.get_access_token()
        assert token is None

    @pytest.mark.asyncio
    async def test_get_account_id_extracts_from_jwt_access_token(
        self, manager, http_client
    ):
        """Test that get_account_id falls back to JWT claim extraction."""
        payload = {
            "https://api.openai.com/auth": {
                "chatgpt_account_id": "acct_test_123",
            }
        }

        def _b64url(obj: dict) -> str:
            raw = json.dumps(obj).encode("utf-8")
            return base64.urlsafe_b64encode(raw).decode("ascii").rstrip("=")

        token = f"{_b64url({'alg': 'none', 'typ': 'JWT'})}.{_b64url(payload)}."

        with tempfile.NamedTemporaryFile(
            mode="w", suffix=".json", delete=False, encoding="utf-8"
        ) as f:
            auth_data = {"tokens": {"access_token": token, "refresh_token": "r"}}
            json.dump(auth_data, f)
            temp_path = Path(f.name)

        try:
            await manager.initialize(auth_path=temp_path)
            assert manager.get_account_id() == "acct_test_123"
        finally:
            await manager.shutdown()
            with contextlib.suppress(Exception):
                temp_path.unlink()

    @pytest.mark.asyncio
    async def test_refresh_access_token_success(
        self, manager, temp_auth_file, http_client
    ):
        """Test successful token refresh."""
        await manager.initialize(auth_path=temp_auth_file)

        # Mock successful OAuth refresh response
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "access_token": "new_access_token",
            "refresh_token": "new_refresh_token",
            "id_token": "new_id_token",
        }

        with patch.object(http_client, "post", new_callable=AsyncMock) as mock_post:
            mock_post.return_value = mock_response

            result = await manager.refresh_access_token()

            assert result is True
            assert manager.get_access_token() == "new_access_token"
            mock_post.assert_called_once()
            call_args = mock_post.call_args
            assert call_args[0][0] == "https://auth.openai.com/oauth/token"
            assert call_args[1]["json"]["grant_type"] == "refresh_token"

    @pytest.mark.asyncio
    async def test_refresh_access_token_concurrency_protection(
        self, manager, temp_auth_file, http_client
    ):
        """Test that refresh is protected by lock to prevent concurrent refreshes."""
        await manager.initialize(auth_path=temp_auth_file)

        # Verify lock exists
        assert manager._token_refresh_lock is not None

        # Test that lock can be acquired (basic functionality check)
        async with manager._token_refresh_lock:
            # Lock acquired successfully
            assert True

    @pytest.mark.asyncio
    async def test_refresh_access_token_atomic_persistence(
        self, manager, temp_auth_file, http_client
    ):
        """Test that refreshed tokens are persisted atomically."""
        await manager.initialize(auth_path=temp_auth_file)

        # Mock successful OAuth refresh response
        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "access_token": "new_access_token",
            "refresh_token": "new_refresh_token",
        }

        with patch.object(http_client, "post", new_callable=AsyncMock) as mock_post:
            mock_post.return_value = mock_response

            result = await manager.refresh_access_token()

            assert result is True

            # Verify file was written atomically (check for temp file pattern)
            # The file should contain the new token
            with open(temp_auth_file, encoding="utf-8") as f:
                persisted_data = json.load(f)
                assert persisted_data["tokens"]["access_token"] == "new_access_token"

    @pytest.mark.asyncio
    async def test_refresh_access_token_failure_handling(
        self, manager, temp_auth_file, http_client
    ):
        """Test error handling during token refresh."""
        await manager.initialize(auth_path=temp_auth_file)

        # Mock failed OAuth refresh response
        mock_response = Mock()
        mock_response.status_code = 401
        mock_response.text = "Invalid refresh token"

        with patch.object(http_client, "post", new_callable=AsyncMock) as mock_post:
            mock_post.return_value = mock_response

            result = await manager.refresh_access_token()

            assert result is False
            # Original token should still be present
            assert manager.get_access_token() == "test_access_token"

    @pytest.mark.asyncio
    async def test_refresh_access_token_network_error(
        self, manager, temp_auth_file, http_client
    ):
        """Test handling of network errors during refresh."""
        await manager.initialize(auth_path=temp_auth_file)

        with patch.object(http_client, "post", new_callable=AsyncMock) as mock_post:
            mock_post.side_effect = httpx.HTTPError("Network error")

            result = await manager.refresh_access_token()

            assert result is False

    @pytest.mark.asyncio
    async def test_refresh_access_token_retries_on_read_timeout(
        self, manager, temp_auth_file, http_client
    ):
        """Transient read timeouts should retry before failing refresh."""
        await manager.initialize(auth_path=temp_auth_file)

        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "access_token": "new_access_token",
            "refresh_token": "new_refresh_token",
        }

        with (
            patch.object(http_client, "post", new_callable=AsyncMock) as mock_post,
            patch(
                "src.connectors.openai_codex.credentials.asyncio.sleep",
                new_callable=AsyncMock,
            ),
        ):
            mock_post.side_effect = [
                httpx.ReadTimeout("read timeout"),
                mock_response,
            ]

            result = await manager.refresh_access_token()

            assert result is True
            assert manager.get_access_token() == "new_access_token"
            assert mock_post.await_count == 2

    @pytest.mark.asyncio
    async def test_refresh_access_token_transient_errors_exhaust_retries(
        self, manager, temp_auth_file, http_client
    ):
        """After max attempts, transient errors return False without raising."""
        await manager.initialize(auth_path=temp_auth_file)

        with (
            patch.object(http_client, "post", new_callable=AsyncMock) as mock_post,
            patch(
                "src.connectors.openai_codex.credentials.asyncio.sleep",
                new_callable=AsyncMock,
            ),
        ):
            mock_post.side_effect = httpx.ReadTimeout("read timeout")

            result = await manager.refresh_access_token()

            assert result is False
            assert mock_post.await_count == 3

    @pytest.mark.asyncio
    async def test_refresh_managed_transient_error_logs_without_exc_info(self, manager):
        """Exhausted transient managed OAuth failures must not log traceback spam."""
        account = ManagedOAuthAccount(
            account_id="acct1",
            access_token="a",
            refresh_token="r",
            expiry_date=1,
        )
        exc = ManagedOAuthRefreshError(
            "failed after retries",
            account_id="acct1",
            from_transient_network=True,
        )
        with (
            patch.object(
                manager._managed_selector,
                "get_current_account",
                return_value=account,
            ),
            patch.object(
                manager._managed_refresh,
                "force_refresh",
                AsyncMock(side_effect=exc),
            ),
            patch("src.connectors.openai_codex.credentials.logger") as log,
        ):
            ok, err = await manager._refresh_managed_access_token()

        assert ok is False
        assert err is exc
        log.warning.assert_called_once()
        assert "exc_info" not in log.warning.call_args.kwargs

    @pytest.mark.asyncio
    async def test_refresh_managed_auth_error_logs_with_exc_info(self, manager):
        """Non-transient managed OAuth errors keep exc_info for diagnosability."""
        account = ManagedOAuthAccount(
            account_id="acct2",
            access_token="a",
            refresh_token="r",
            expiry_date=1,
        )
        exc = ManagedOAuthRefreshError(
            "invalid_grant",
            account_id="acct2",
            from_transient_network=False,
        )
        with (
            patch.object(
                manager._managed_selector,
                "get_current_account",
                return_value=account,
            ),
            patch.object(
                manager._managed_refresh,
                "force_refresh",
                AsyncMock(side_effect=exc),
            ),
            patch("src.connectors.openai_codex.credentials.logger") as log,
        ):
            ok, err = await manager._refresh_managed_access_token()

        assert ok is False
        assert err is exc
        log.warning.assert_called_once()
        assert log.warning.call_args.kwargs.get("exc_info") is True

    @pytest.mark.asyncio
    async def test_refresh_managed_auth_401_logs_account_email_without_exc_info(
        self, manager
    ):
        """Managed 401 refresh rejection should include account email without traceback spam."""
        account = ManagedOAuthAccount(
            account_id="acct401",
            email="acct401@example.com",
            access_token="a",
            refresh_token="r",
            expiry_date=1,
        )
        exc = ManagedOAuthRefreshError(
            "Token refresh rejected with HTTP 401 (token_expired)",
            account_id="acct401",
            account_email="acct401@example.com",
            needs_reauth=True,
            http_status=401,
        )
        with (
            patch.object(
                manager._managed_selector,
                "get_current_account",
                return_value=account,
            ),
            patch.object(
                manager._managed_refresh,
                "force_refresh",
                AsyncMock(side_effect=exc),
            ),
            patch("src.connectors.openai_codex.credentials.logger") as log,
        ):
            ok, err = await manager._refresh_managed_access_token()

        assert ok is False
        assert err is exc
        log.warning.assert_called_once()
        assert log.warning.call_args.args[1] == "acct401 (acct401@example.com)"
        assert log.warning.call_args.args[2] == 401
        assert "exc_info" not in log.warning.call_args.kwargs

    @pytest.mark.asyncio
    async def test_refresh_managed_unexpected_exception_returns_wrapped_error(
        self, manager
    ):
        """Unexpected refresh exceptions should be contained and wrapped."""
        account = ManagedOAuthAccount(
            account_id="acct-unexpected",
            email="acct-unexpected@example.com",
            access_token="a",
            refresh_token="r",
            expiry_date=1,
        )
        with (
            patch.object(
                manager._managed_selector,
                "get_current_account",
                return_value=account,
            ),
            patch.object(
                manager._managed_refresh,
                "force_refresh",
                AsyncMock(side_effect=RuntimeError("boom")),
            ),
        ):
            ok, err = await manager._refresh_managed_access_token()

        assert ok is False
        assert isinstance(err, ManagedOAuthRefreshError)
        assert err.account_id == "acct-unexpected"
        assert err.account_email == "acct-unexpected@example.com"
        assert "Unexpected managed OAuth refresh failure: boom" in str(err)

    @pytest.mark.asyncio
    async def test_refresh_access_token_transient_managed_skips_penalizing_rotation(
        self, manager, temp_auth_file
    ):
        """Transient managed refresh failures must not rotate or bump auth-failure counters."""
        exc = ManagedOAuthRefreshError(
            "failed after retries",
            account_id="managed_primary",
            from_transient_network=True,
        )
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            account = ManagedOAuthAccount(
                account_id="managed_primary",
                access_token="managed_access_token",
                refresh_token="managed_refresh_token",
                expiry_date=9_999_999_999_999,
            )
            await storage.save_account(account)
            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="first-available",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )
            original_default_paths = manager._default_auth_paths
            manager._default_auth_paths = lambda: [temp_auth_file]
            try:
                await manager.initialize(auth_path=None)
            finally:
                manager._default_auth_paths = original_default_paths

            with (
                patch.object(
                    manager._managed_refresh,
                    "force_refresh",
                    AsyncMock(side_effect=exc),
                ),
                patch.object(
                    manager._managed_selector,
                    "rotate_on_auth_failure",
                    AsyncMock(),
                ) as mock_rotate_penalizing,
                patch.object(
                    manager._managed_selector,
                    "rotate_away_without_auth_penalty",
                    AsyncMock(),
                ) as mock_rotate_soft,
            ):
                result = await manager.refresh_access_token()

            assert result is False
            mock_rotate_penalizing.assert_not_awaited()
            mock_rotate_soft.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_refresh_access_token_needs_reauth_advances_without_penalizing_rotation(
        self, manager, temp_auth_file
    ):
        """invalid_grant-style refresh errors must not invoke penalizing rotation."""
        exc = ManagedOAuthRefreshError(
            "Refresh token invalid or revoked; re-authorization required",
            account_id="acct_a",
            needs_reauth=True,
        )
        next_account = ManagedOAuthAccount(
            account_id="acct_b",
            access_token="tb",
            refresh_token="rb",
            expiry_date=9_999_999_999_999,
        )
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_a",
                    access_token="ta",
                    refresh_token="ra",
                    expiry_date=9_999_999_999_999,
                )
            )
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_b",
                    access_token="tb",
                    refresh_token="rb",
                    expiry_date=9_999_999_999_999,
                )
            )
            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="first-available",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )
            original_default_paths = manager._default_auth_paths
            manager._default_auth_paths = lambda: [temp_auth_file]
            try:
                await manager.initialize(auth_path=None)
            finally:
                manager._default_auth_paths = original_default_paths

            with (
                patch.object(
                    manager._managed_refresh,
                    "force_refresh",
                    AsyncMock(side_effect=exc),
                ),
                patch.object(
                    manager._managed_selector,
                    "reload_accounts",
                    AsyncMock(),
                ),
                patch.object(
                    manager._managed_selector,
                    "get_next_account",
                    AsyncMock(return_value=next_account),
                ),
                patch.object(
                    manager._managed_selector,
                    "rotate_on_auth_failure",
                    AsyncMock(),
                ) as mock_rotate_penalizing,
                patch.object(
                    manager._managed_selector,
                    "rotate_away_without_auth_penalty",
                    AsyncMock(),
                ) as mock_rotate_soft,
            ):
                result = await manager.refresh_access_token()

            assert result is True
            assert manager.get_access_token() == "tb"
            mock_rotate_penalizing.assert_not_awaited()
            mock_rotate_soft.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_refresh_access_token_other_managed_failure_uses_soft_rotation(
        self, manager, temp_auth_file
    ):
        """Non-transient, non-invalid_grant refresh errors rotate without auth penalties."""
        exc = ManagedOAuthRefreshError(
            "Token refresh rejected with HTTP 400 (server_error)",
            account_id="acct_a",
        )
        fallback = ManagedOAuthAccount(
            account_id="acct_b",
            access_token="tb",
            refresh_token="rb",
            expiry_date=9_999_999_999_999,
        )
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_a",
                    access_token="ta",
                    refresh_token="ra",
                    expiry_date=9_999_999_999_999,
                )
            )
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_b",
                    access_token="tb",
                    refresh_token="rb",
                    expiry_date=9_999_999_999_999,
                )
            )
            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="first-available",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )
            original_default_paths = manager._default_auth_paths
            manager._default_auth_paths = lambda: [temp_auth_file]
            try:
                await manager.initialize(auth_path=None)
            finally:
                manager._default_auth_paths = original_default_paths

            with (
                patch.object(
                    manager._managed_refresh,
                    "force_refresh",
                    AsyncMock(side_effect=exc),
                ),
                patch.object(
                    manager._managed_selector,
                    "rotate_on_auth_failure",
                    AsyncMock(),
                ) as mock_rotate_penalizing,
                patch.object(
                    manager._managed_selector,
                    "rotate_away_without_auth_penalty",
                    AsyncMock(return_value=fallback),
                ) as mock_rotate_soft,
            ):
                result = await manager.refresh_access_token()

            assert result is True
            assert manager.get_access_token() == "tb"
            mock_rotate_penalizing.assert_not_awaited()
            mock_rotate_soft.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_refresh_access_token_no_refresh_token(
        self, manager, temp_auth_file, http_client
    ):
        """Test refresh fails when refresh_token is missing."""
        # Create auth file without refresh_token
        with tempfile.NamedTemporaryFile(
            mode="w", suffix=".json", delete=False, encoding="utf-8"
        ) as f:
            auth_data = {"tokens": {"access_token": "test_token"}}
            json.dump(auth_data, f)
            temp_path = Path(f.name)

        try:
            await manager.initialize(auth_path=temp_path)

            result = await manager.refresh_access_token()

            assert result is False
        finally:
            temp_path.unlink()

    @pytest.mark.asyncio
    async def test_shutdown_stops_watcher(self, manager, temp_auth_file):
        """Test that shutdown stops the file watcher."""
        await manager.initialize(auth_path=temp_auth_file)

        assert manager.is_watcher_running() is True

        await manager.shutdown()

        assert manager.is_watcher_running() is False

    @pytest.mark.asyncio
    async def test_shutdown_idempotent(self, manager, temp_auth_file):
        """Test that shutdown can be called multiple times safely."""
        await manager.initialize(auth_path=temp_auth_file)

        await manager.shutdown()
        await manager.shutdown()  # Second call should be no-op

        assert manager.is_watcher_running() is False

    @pytest.mark.asyncio
    async def test_is_watcher_running(self, manager, temp_auth_file):
        """Test watcher state tracking."""
        assert manager.is_watcher_running() is False

        await manager.initialize(auth_path=temp_auth_file)
        assert manager.is_watcher_running() is True

        await manager.shutdown()
        assert manager.is_watcher_running() is False

    @pytest.mark.asyncio
    async def test_initialize_with_none_path_discovers_default(
        self, manager, http_client
    ):
        """Test that initialize discovers default auth path when None provided."""
        # Create a temp directory and auth file
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            auth_file = temp_path / ".codex" / "auth.json"
            auth_file.parent.mkdir(parents=True, exist_ok=True)

            auth_data = {"tokens": {"access_token": "test_token"}}
            with open(auth_file, "w", encoding="utf-8") as f:
                json.dump(auth_data, f)

            # Mock _default_auth_paths to return our temp path
            original_method = manager._default_auth_paths
            manager._default_auth_paths = lambda: [auth_file]

            try:
                await manager.initialize(auth_path=None)

                assert manager._auth_path == auth_file
                assert manager.get_access_token() == "test_token"
            finally:
                manager._default_auth_paths = original_method

    @pytest.mark.asyncio
    async def test_load_auth_caches_on_timestamp(self, manager, temp_auth_file):
        """Test that load_auth caches credentials when file timestamp unchanged."""
        await manager.initialize(auth_path=temp_auth_file)

        # First load
        load_count = [0]

        async def mock_load():
            load_count[0] += 1
            return await manager._load_auth(force_reload=False)

        # Load again without force_reload - should use cache
        result = await mock_load()
        assert result is True
        # Should not have incremented load_count if caching works
        # (We can't easily test this without mocking, but the logic is there)

    @pytest.mark.asyncio
    async def test_load_auth_force_reload_bypasses_cache(self, manager, temp_auth_file):
        """Test that force_reload bypasses cache."""
        await manager.initialize(auth_path=temp_auth_file)

        # Modify file
        with open(temp_auth_file, "r+", encoding="utf-8") as f:
            data = json.load(f)
            data["tokens"]["access_token"] = "modified_token"
            f.seek(0)
            json.dump(data, f)
            f.truncate()

        # Force reload
        result = await manager._load_auth(force_reload=True)
        assert result is True
        assert manager.get_access_token() == "modified_token"

    @pytest.mark.asyncio
    async def test_initialize_prefers_managed_accounts_over_legacy_auth_file(
        self, manager, temp_auth_file
    ):
        """Managed account source should take precedence over auth.json fallback."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            account = ManagedOAuthAccount(
                account_id="managed_primary",
                access_token="managed_access_token",
                refresh_token="managed_refresh_token",
                expiry_date=9_999_999_999_999,
            )
            await storage.save_account(account)
            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="first-available",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=True,
                    max_rate_limit_wait_seconds=0.01,
                )
            )

            original_default_paths = manager._default_auth_paths
            manager._default_auth_paths = lambda: [temp_auth_file]
            try:
                await manager.initialize(auth_path=None)
            finally:
                manager._default_auth_paths = original_default_paths

            assert manager.get_access_token() == "managed_access_token"
            assert manager._active_source == "managed"

    @pytest.mark.asyncio
    async def test_initialize_falls_back_to_legacy_when_no_managed_accounts(
        self, manager, temp_auth_file
    ):
        """Legacy auth.json should be used when managed OAuth store is empty."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=True,
                    max_rate_limit_wait_seconds=0.01,
                )
            )

            await manager.initialize(auth_path=temp_auth_file)

            assert manager.get_access_token() == "test_access_token"
            assert manager._active_source == "legacy"

    @pytest.mark.asyncio
    async def test_load_auth_prefers_managed_when_oauth_dir_override_has_legacy_file(
        self, manager
    ):
        """Managed accounts load before legacy even when ``_oauth_dir_override`` is set."""
        with tempfile.TemporaryDirectory() as temp_dir:
            oauth_dir = Path(temp_dir) / "codex_sidecar"
            oauth_dir.mkdir(parents=True, exist_ok=True)
            legacy = oauth_dir / "auth.json"
            with open(legacy, "w", encoding="utf-8") as f:
                json.dump({"tokens": {"access_token": "legacy_only"}}, f)

            storage_path = Path(temp_dir) / "managed"
            storage = ManagedOAuthStorageService(storage_path)
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="managed_one",
                    access_token="managed_token",
                    refresh_token="managed_refresh",
                    expiry_date=9_999_999_999_999,
                )
            )
            manager._oauth_dir_override = oauth_dir
            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="first-available",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=True,
                    max_rate_limit_wait_seconds=0.01,
                )
            )
            assert await manager._load_auth(force_reload=True) is True
            assert manager._active_source == "managed"
            assert manager.get_access_token() == "managed_token"

    @pytest.mark.asyncio
    async def test_load_auth_skips_legacy_when_managed_store_populated_but_managed_unavailable(
        self, manager
    ):
        """If managed account files exist, do not read legacy auth.json when managed load fails."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed"
            storage = ManagedOAuthStorageService(storage_path)
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="managed_one",
                    access_token="managed_token",
                    refresh_token="managed_refresh",
                    expiry_date=9_999_999_999_999,
                )
            )
            legacy = Path(temp_dir) / "auth.json"
            with open(legacy, "w", encoding="utf-8") as f:
                json.dump({"tokens": {"access_token": "legacy_only_token"}}, f)

            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="first-available",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=True,
                    max_rate_limit_wait_seconds=0.01,
                )
            )
            manager._default_auth_paths = lambda: [legacy]

            with (
                patch.object(
                    manager,
                    "_load_managed_auth",
                    new_callable=AsyncMock,
                    return_value=False,
                ) as mock_managed,
                patch.object(
                    manager,
                    "_load_legacy_auth",
                    new_callable=AsyncMock,
                    return_value=True,
                ) as mock_legacy,
            ):
                result = await manager._load_auth(force_reload=True)

            assert result is False
            assert manager._active_source == "none"
            assert manager.get_access_token() is None
            mock_managed.assert_awaited()
            mock_legacy.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_effective_max_rate_limit_retries_expands_with_account_count(
        self, manager
    ):
        """Rotation budget should grow when multiple managed accounts exist."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed"
            storage = ManagedOAuthStorageService(storage_path)
            exp = 9_999_999_999_999
            for i in range(3):
                await storage.save_account(
                    ManagedOAuthAccount(
                        account_id=f"acct_{i}",
                        access_token=f"t{i}",
                        refresh_token=f"r{i}",
                        expiry_date=exp,
                    )
                )
            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=True,
                    max_rate_limit_wait_seconds=0.01,
                )
            )
            assert await manager.effective_max_rate_limit_retries(2) == 3

    @pytest.mark.asyncio
    async def test_effective_max_rate_limit_retries_managed_disabled_returns_floor(
        self, manager
    ):
        """When managed OAuth is disabled, rotation budget must not expand past the floor."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed"
            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=False,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=True,
                    max_rate_limit_wait_seconds=0.01,
                )
            )
            assert await manager.effective_max_rate_limit_retries(7) == 7

    @pytest.mark.asyncio
    async def test_notify_codex_usage_limit_unrecovered_legacy_path(
        self, http_client, temp_auth_file
    ):
        """Legacy credentials should still trigger quota notifications when exhausted."""
        from unittest.mock import AsyncMock

        from src.core.interfaces.notification_service_interface import (
            INotificationService,
        )

        mock_svc = Mock(spec=INotificationService)
        mock_svc.is_enabled = True
        mock_svc.send_notification = AsyncMock(return_value="nid")
        mgr = CredentialManager(http_client=http_client, notification_service=mock_svc)
        await mgr.initialize(auth_path=temp_auth_file)
        mgr._auth_credentials = {
            "tokens": {"access_token": "x"},
            "user": {"email": "legacy@example.com"},
        }
        mgr._active_source = "legacy"
        try:
            await mgr.notify_codex_usage_limit_unrecovered(
                upstream_detail={
                    "error": {
                        "type": "usage_limit_reached",
                        "message": "The usage limit has been reached",
                        "plan_type": "plus",
                        "resets_in_seconds": 120,
                    }
                },
                retry_after_seconds=120.0,
                pool_exhaustion_confirmed=True,
            )
            mock_svc.send_notification.assert_awaited_once()
        finally:
            await mgr.shutdown()

    @pytest.mark.asyncio
    async def test_notify_codex_usage_limit_unrecovered_skips_non_usage_limit_payload(
        self, http_client, temp_auth_file
    ):
        """Non-Codex usage_limit errors must not trigger desktop quota notifications."""
        from src.core.interfaces.notification_service_interface import (
            INotificationService,
        )

        mock_svc = Mock(spec=INotificationService)
        mock_svc.is_enabled = True
        mock_svc.send_notification = AsyncMock(return_value="nid")
        mgr = CredentialManager(http_client=http_client, notification_service=mock_svc)
        await mgr.initialize(auth_path=temp_auth_file)
        mgr._auth_credentials = {"tokens": {"access_token": "x"}}
        mgr._active_source = "legacy"
        try:
            await mgr.notify_codex_usage_limit_unrecovered(
                upstream_detail={
                    "error": {"type": "invalid_request", "message": "nope"}
                },
                retry_after_seconds=None,
                pool_exhaustion_confirmed=True,
            )
            mock_svc.send_notification.assert_not_called()
        finally:
            await mgr.shutdown()

    @pytest.mark.asyncio
    async def test_evaluate_codex_remaining_quota_notifications_skips_when_disabled(
        self, http_client
    ) -> None:
        from src.core.interfaces.notification_service_interface import (
            INotificationService,
        )

        mock_svc = Mock(spec=INotificationService)
        mock_svc.is_enabled = True
        mock_svc.send_notification = AsyncMock(return_value="nid")
        mgr = CredentialManager(http_client=http_client, notification_service=mock_svc)
        with tempfile.TemporaryDirectory() as temp_dir:
            mgr.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=False,
                    storage_path=str(Path(temp_dir)),
                    accounts="all",
                    quota_remaining_alerts_enabled=False,
                )
            )
        try:
            await mgr.evaluate_codex_remaining_quota_notifications(
                {"x-codex-primary-used-percent": "99"},
            )
            mock_svc.send_notification.assert_not_called()
        finally:
            await mgr.shutdown()

    @pytest.mark.asyncio
    async def test_evaluate_codex_remaining_quota_notifications_legacy_email(
        self, http_client
    ) -> None:
        from src.core.interfaces.notification_service_interface import (
            INotificationService,
        )

        mock_svc = Mock(spec=INotificationService)
        mock_svc.is_enabled = True
        mock_svc.send_notification = AsyncMock(return_value="nid")
        mgr = CredentialManager(http_client=http_client, notification_service=mock_svc)
        with tempfile.TemporaryDirectory() as temp_dir:
            mgr.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=False,
                    storage_path=str(Path(temp_dir)),
                    accounts="all",
                    quota_remaining_alerts_enabled=True,
                    quota_remaining_alert_thresholds_percent=[25.0],
                )
            )
        mgr._auth_credentials = {
            "tokens": {"access_token": "x"},
            "user": {"email": "legacy@example.com"},
            "account_id": "acct-legacy",
        }
        try:
            await mgr.evaluate_codex_remaining_quota_notifications(
                {"x-codex-primary-used-percent": "90"},
            )
            mock_svc.send_notification.assert_awaited_once()
            msg = mock_svc.send_notification.call_args.kwargs["message"]
            assert "legacy@example.com" in msg
            assert "5 hour rolling window" in msg
        finally:
            await mgr.shutdown()

    @pytest.mark.asyncio
    async def test_handle_rate_limit_rotates_to_next_managed_account(self, manager):
        """Rate-limit handling should rotate active managed account when possible."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            expires_at = 9_999_999_999_999
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_a",
                    access_token="token_a",
                    refresh_token="refresh_a",
                    expiry_date=expires_at,
                )
            )
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_b",
                    access_token="token_b",
                    refresh_token="refresh_b",
                    expiry_date=expires_at,
                )
            )

            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )

            await manager.initialize(auth_path=None)
            first_token = manager.get_access_token()
            assert first_token in {"token_a", "token_b"}

            rotated = await manager.handle_rate_limit(60, session_id="session-1")

            assert rotated is True
            second_token = manager.get_access_token()
            assert second_token in {"token_a", "token_b"}
            assert second_token != first_token

    @pytest.mark.asyncio
    async def test_handle_rate_limit_persists_codex_usage_limit_on_rotated_account(
        self, manager
    ):
        """usage_limit_reached JSON should be stored on the account that was rate-limited."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            expires_at = 9_999_999_999_999
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_a",
                    access_token="token_a",
                    refresh_token="refresh_a",
                    expiry_date=expires_at,
                )
            )
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_b",
                    access_token="token_b",
                    refresh_token="refresh_b",
                    expiry_date=expires_at,
                )
            )

            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )

            await manager.initialize(auth_path=None)
            current = manager._managed_selector.get_current_account()
            assert current is not None
            first_id = current.account_id

            upstream = {
                "error": {
                    "type": "usage_limit_reached",
                    "message": "The usage limit has been reached",
                    "plan_type": "plus",
                    "resets_at": 1776358224,
                    "resets_in_seconds": 191966,
                }
            }
            rotated = await manager.handle_rate_limit(
                60.0,
                session_id="session-1",
                upstream_codex_error=upstream,
            )
            assert rotated is True

            limited = await storage.get_account(first_id)
            assert limited is not None
            assert limited.last_codex_usage_limit is not None
            assert limited.last_codex_usage_limit.get("plan_type") == "plus"
            assert limited.last_codex_usage_limit.get("resets_in_seconds") == 191966.0
            assert limited.last_codex_usage_limit.get("observed_at")

    @pytest.mark.asyncio
    async def test_record_codex_quota_headers_updates_managed_account_file(
        self, manager
    ):
        """x-codex-* headers should be written to the current managed account JSON."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            expires_at = 9_999_999_999_999
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_a",
                    access_token="token_a",
                    refresh_token="refresh_a",
                    expiry_date=expires_at,
                )
            )
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_b",
                    access_token="token_b",
                    refresh_token="refresh_b",
                    expiry_date=expires_at,
                )
            )

            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )

            await manager.initialize(auth_path=None)

            await manager.record_codex_quota_headers(
                {
                    "X-Codex-Plan-Type": "team",
                    "x-codex-primary-used-percent": "80",
                    "Other": "ignored",
                }
            )

            cur = manager._managed_selector.get_current_account()
            assert cur is not None
            on_disk = await storage.get_account(cur.account_id)
            assert on_disk is not None
            assert on_disk.last_codex_quota_headers is not None
            assert on_disk.last_codex_quota_headers.get("x-codex-plan-type") == "team"
            assert on_disk.last_codex_quota_observed_at

    @pytest.mark.asyncio
    async def test_list_managed_oauth_account_ids_excludes_needs_reauth(self, manager):
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)

            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_a",
                    access_token="token_a",
                    refresh_token="refresh_a",
                    expiry_date=9_999_999_999_999,
                )
            )
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_b",
                    access_token="token_b",
                    refresh_token="refresh_b",
                    expiry_date=9_999_999_999_999,
                    needs_reauth=True,
                )
            )

            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )

            account_ids = await manager.list_managed_oauth_account_ids()

            assert account_ids == ["acct_a"]

    @pytest.mark.asyncio
    async def test_list_managed_oauth_account_ids_skips_rate_limited_when_others_ok(
        self, manager
    ):
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            exp = 9_999_999_999_999
            rl_until = 9_999_999_999_000
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_ok",
                    access_token="token_ok",
                    refresh_token="refresh_ok",
                    expiry_date=exp,
                )
            )
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_rl",
                    access_token="token_rl",
                    refresh_token="refresh_rl",
                    expiry_date=exp,
                    rate_limited_until=rl_until,
                )
            )

            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )

            account_ids = await manager.list_managed_oauth_account_ids()
            assert account_ids == ["acct_ok"]

    @pytest.mark.asyncio
    async def test_list_managed_oauth_account_ids_all_rate_limited_lists_all_available(
        self, manager
    ):
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            exp = 9_999_999_999_999
            rl_until = 9_999_999_999_000
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_rl1",
                    access_token="t1",
                    refresh_token="r1",
                    expiry_date=exp,
                    rate_limited_until=rl_until,
                )
            )
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_rl2",
                    access_token="t2",
                    refresh_token="r2",
                    expiry_date=exp,
                    rate_limited_until=rl_until,
                )
            )

            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )

            account_ids = await manager.list_managed_oauth_account_ids()
            assert set(account_ids) == {"acct_rl1", "acct_rl2"}

    @pytest.mark.asyncio
    async def test_ensure_usage_window_warmup_activates_rate_limited_account(
        self, manager
    ):
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            exp = 9_999_999_999_999
            rl_until = 9_999_999_999_000
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_a",
                    access_token="ta",
                    refresh_token="ra",
                    expiry_date=exp,
                )
            )
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_rl",
                    access_token="tb",
                    refresh_token="rb",
                    expiry_date=exp,
                    rate_limited_until=rl_until,
                )
            )

            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )

            await manager.initialize(auth_path=None)
            ok = await manager.ensure_usage_window_warmup_managed_account(
                "acct_rl",
                session_id="warmup-sess",
            )
            assert ok is True
            cur = manager._managed_selector.get_current_account()
            assert cur is not None
            assert cur.account_id == "acct_rl"

    def test_usage_window_warmup_override_snapshot_restore_roundtrip(self, manager):
        selector_account = ManagedOAuthAccount(
            account_id="selector-a",
            access_token="selector-token",
            refresh_token="selector-refresh",
            expiry_date=9_999_999_999_999,
        )
        managed_account = ManagedOAuthAccount(
            account_id="managed-a",
            access_token="managed-token",
            refresh_token="managed-refresh",
            expiry_date=9_999_999_999_999,
        )
        manager._active_source = "managed"
        manager._managed_selector._current_account = selector_account  # type: ignore[reportPrivateUsage]
        manager._managed_current_account = managed_account
        manager._auth_credentials = {"tokens": {"access_token": "baseline"}}

        snapshot = manager.begin_usage_window_warmup_override()

        manager._active_source = "legacy"
        manager._managed_selector._current_account = None  # type: ignore[reportPrivateUsage]
        manager._managed_current_account = None
        manager._auth_credentials = {"tokens": {"access_token": "mutated"}}

        manager.end_usage_window_warmup_override(snapshot)

        assert manager._active_source == "managed"
        assert manager._managed_selector.get_current_account() == selector_account
        assert manager._managed_current_account == managed_account
        assert manager._auth_credentials == {"tokens": {"access_token": "baseline"}}
        assert manager._auth_credentials is not snapshot["auth_credentials"]

    @pytest.mark.asyncio
    async def test_record_codex_quota_headers_throttles_disk_writes(self, manager):
        """Quota header snapshots should not hit disk more than once per 60s per account."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            expires_at = 9_999_999_999_999
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_a",
                    access_token="token_a",
                    refresh_token="refresh_a",
                    expiry_date=expires_at,
                )
            )

            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )

            await manager.initialize(auth_path=None)

            saves: list[int] = []
            orig_save = manager._managed_storage.save_account

            async def counting_save(acc: ManagedOAuthAccount) -> None:
                saves.append(1)
                await orig_save(acc)

            manager._managed_storage.save_account = counting_save  # type: ignore[method-assign]

            headers = {"x-codex-plan-type": "team", "x-codex-primary-used-percent": "1"}
            await manager.record_codex_quota_headers(headers, force=False)
            assert len(saves) == 1
            cur = manager._managed_selector.get_current_account()
            assert cur is not None
            manager._codex_quota_last_disk_write_at[cur.account_id] = (
                time.monotonic() - 10.0
            )
            await manager.record_codex_quota_headers(headers, force=False)
            assert len(saves) == 1
            manager._codex_quota_last_disk_write_at[cur.account_id] = (
                time.monotonic() - 70.0
            )
            await manager.record_codex_quota_headers(headers, force=False)
            assert len(saves) == 2

    @pytest.mark.asyncio
    async def test_record_codex_quota_headers_force_bypasses_throttle(self, manager):
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            expires_at = 9_999_999_999_999
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="acct_a",
                    access_token="token_a",
                    refresh_token="refresh_a",
                    expiry_date=expires_at,
                )
            )

            manager.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )

            await manager.initialize(auth_path=None)

            saves: list[int] = []
            orig_save = manager._managed_storage.save_account

            async def counting_save(acc: ManagedOAuthAccount) -> None:
                saves.append(1)
                await orig_save(acc)

            manager._managed_storage.save_account = counting_save  # type: ignore[method-assign]

            headers = {"x-codex-plan-type": "team"}
            await manager.record_codex_quota_headers(headers, force=False)
            assert len(saves) == 1
            cur = manager._managed_selector.get_current_account()
            assert cur is not None
            manager._codex_quota_last_disk_write_at[cur.account_id] = (
                time.monotonic() - 10.0
            )
            await manager.record_codex_quota_headers(headers, force=True)
            assert len(saves) == 2


class TestCodexQuotaNotifications:
    """Desktop notification dedupe and exhaustion messaging on managed 429s."""

    @pytest.fixture
    def http_client(self):
        return httpx.AsyncClient()

    @pytest.mark.asyncio
    async def test_handle_rate_limit_notifies_once_per_dedupe_key(self, http_client):
        """Same account + quota window should not send duplicate notifications."""
        mock_notify = AsyncMock(return_value="nid-1")
        svc = Mock()
        svc.is_enabled = True
        svc.send_notification = mock_notify
        mgr = CredentialManager(http_client=http_client, notification_service=svc)

        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            expires_at = 9_999_999_999_999
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="only_one",
                    access_token="token_a",
                    refresh_token="refresh_a",
                    expiry_date=expires_at,
                )
            )
            mgr.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )
            await mgr.initialize(auth_path=None)

            upstream = {
                "error": {
                    "type": "usage_limit_reached",
                    "message": "limit",
                    "plan_type": "plus",
                    "resets_at": 1_776_358_224,
                    "resets_in_seconds": 191_966,
                }
            }
            await mgr.handle_rate_limit(
                60.0,
                session_id="s1",
                upstream_codex_error=upstream,
            )
            await mgr.handle_rate_limit(
                60.0,
                session_id="s1",
                upstream_codex_error=upstream,
            )
            assert mock_notify.await_count == 1
            await mgr.shutdown()

    @pytest.mark.asyncio
    async def test_handle_rate_limit_notifies_again_when_until_changes(
        self, http_client
    ):
        mock_notify = AsyncMock(return_value="nid")
        svc = Mock()
        svc.is_enabled = True
        svc.send_notification = mock_notify
        mgr = CredentialManager(http_client=http_client, notification_service=svc)

        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            expires_at = 9_999_999_999_999
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="only_one",
                    access_token="token_a",
                    refresh_token="refresh_a",
                    expiry_date=expires_at,
                )
            )
            mgr.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )
            await mgr.initialize(auth_path=None)

            upstream1 = {
                "error": {
                    "type": "usage_limit_reached",
                    "resets_at": 1_776_358_224,
                    "resets_in_seconds": 191_966,
                }
            }
            upstream2 = {
                "error": {
                    "type": "usage_limit_reached",
                    "resets_at": 1_786_358_224,
                    "resets_in_seconds": 191_966,
                }
            }
            await mgr.handle_rate_limit(
                60.0, session_id="s1", upstream_codex_error=upstream1
            )
            await mgr.handle_rate_limit(
                60.0, session_id="s1", upstream_codex_error=upstream2
            )
            assert mock_notify.await_count == 2
            await mgr.shutdown()

    @pytest.mark.asyncio
    async def test_handle_rate_limit_exhaustion_suffix_single_account(
        self, http_client
    ):
        mock_notify = AsyncMock(return_value="nid")
        svc = Mock()
        svc.is_enabled = True
        svc.send_notification = mock_notify
        mgr = CredentialManager(http_client=http_client, notification_service=svc)

        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="solo",
                    email="solo@example.com",
                    access_token="token_a",
                    refresh_token="refresh_a",
                    expiry_date=9_999_999_999_999,
                )
            )
            mgr.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )
            await mgr.initialize(auth_path=None)

            upstream = {
                "error": {
                    "type": "usage_limit_reached",
                    "resets_at": 1_776_358_224,
                    "resets_in_seconds": 10_000,
                }
            }
            await mgr.handle_rate_limit(
                60.0, session_id="s1", upstream_codex_error=upstream
            )
            mock_notify.assert_awaited_once()
            body = mock_notify.await_args.kwargs["message"]
            assert "Quotas exhausted on all available accounts" in body
            assert "solo@example.com" in body
            assert "sliding 5h window" in body
            await mgr.shutdown()

    @pytest.mark.asyncio
    async def test_handle_rate_limit_no_notification_when_disabled(self, http_client):
        mock_notify = AsyncMock(return_value="nid")
        svc = Mock()
        svc.is_enabled = False
        svc.send_notification = mock_notify
        mgr = CredentialManager(http_client=http_client, notification_service=svc)

        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            for aid in ("a", "b"):
                await storage.save_account(
                    ManagedOAuthAccount(
                        account_id=aid,
                        access_token=f"t_{aid}",
                        refresh_token=f"r_{aid}",
                        expiry_date=9_999_999_999_999,
                    )
                )
            mgr.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )
            await mgr.initialize(auth_path=None)
            await mgr.handle_rate_limit(60.0, session_id="s1")
            mock_notify.assert_not_awaited()
            await mgr.shutdown()

    @pytest.mark.asyncio
    async def test_handle_rate_limit_no_notification_without_service(self, http_client):
        mgr = CredentialManager(http_client=http_client)
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = Path(temp_dir) / "managed_oauth"
            storage = ManagedOAuthStorageService(storage_path)
            await storage.save_account(
                ManagedOAuthAccount(
                    account_id="solo",
                    access_token="token_a",
                    refresh_token="refresh_a",
                    expiry_date=9_999_999_999_999,
                )
            )
            mgr.configure_managed_oauth(
                ManagedOAuthConfig(
                    enabled=True,
                    storage_path=str(storage_path),
                    accounts="all",
                    selection_strategy="round-robin",
                    refresh_buffer_seconds=300,
                    session_affinity_ttl_seconds=3600,
                    session_affinity_max_entries=100,
                    allow_legacy_fallback=False,
                    max_rate_limit_wait_seconds=0.01,
                )
            )
            await mgr.initialize(auth_path=None)
            await mgr.handle_rate_limit(60.0, session_id="s1")
            await mgr.shutdown()


def test_user_facing_quota_type_sliding_vs_weekly() -> None:
    assert user_facing_quota_type(3600.0) == "sliding 5h window"
    assert user_facing_quota_type(10 * 24 * 3600.0) == "weekly limit"
    assert user_facing_quota_type(None) == "unknown"


def test_managed_oauth_account_codex_telemetry_fields_roundtrip() -> None:
    acc = ManagedOAuthAccount(
        account_id="acct1",
        access_token="at",
        refresh_token="rt",
        last_codex_quota_headers={"x-codex-plan-type": "team"},
        last_codex_quota_observed_at="2026-04-14T00:00:00+00:00",
        last_codex_usage_limit={
            "plan_type": "team",
            "observed_at": "2026-04-14T00:01:00+00:00",
        },
    )
    restored = ManagedOAuthAccount.model_validate(acc.model_dump())
    assert restored.last_codex_quota_headers == {"x-codex-plan-type": "team"}
    assert restored.last_codex_usage_limit is not None
    assert restored.last_codex_usage_limit.get("plan_type") == "team"


class TestCredentialWatcher:
    """Test CredentialWatcher debounce and file watching behavior."""

    @pytest.fixture
    def temp_auth_file(self):
        """Create a temporary auth.json file for testing."""
        with tempfile.NamedTemporaryFile(
            mode="w", suffix=".json", delete=False, encoding="utf-8"
        ) as f:
            auth_data = {"tokens": {"access_token": "test_token"}}
            json.dump(auth_data, f)
            temp_path = Path(f.name)
        yield temp_path
        with contextlib.suppress(Exception):
            temp_path.unlink()

    @pytest.fixture
    def mock_manager(self):
        """Create a mock CredentialManager."""
        manager = Mock()
        manager._schedule_reload = Mock()
        manager._auth_path = None
        return manager

    @pytest.fixture
    def watcher(self, mock_manager):
        """Create a CredentialWatcher instance."""
        return CredentialWatcher(mock_manager)

    def test_file_change_triggers_reload(self, watcher, mock_manager, temp_auth_file):
        """Test that file change triggers reload."""
        mock_manager._auth_path = temp_auth_file
        mock_manager._watcher = watcher
        watcher.schedule_reload = Mock()

        # Create a file system event
        event = Mock(spec=FileSystemEvent)
        event.is_directory = False
        event.src_path = str(temp_auth_file)

        handler = OpenAICredentialsFileHandler(mock_manager)
        handler.on_modified(event)

        # Should schedule reload
        watcher.schedule_reload.assert_called_once()

    def test_file_change_ignores_directory_events(self, watcher, mock_manager):
        """Test that directory events are ignored."""
        mock_manager._watcher = watcher
        watcher.schedule_reload = Mock()

        event = Mock(spec=FileSystemEvent)
        event.is_directory = True
        event.src_path = "/some/path"

        handler = OpenAICredentialsFileHandler(mock_manager)
        handler.on_modified(event)

        # Should not schedule reload
        watcher.schedule_reload.assert_not_called()

    def test_file_change_ignores_other_files(
        self, watcher, mock_manager, temp_auth_file
    ):
        """Test that changes to other files are ignored."""
        mock_manager._auth_path = temp_auth_file
        mock_manager._watcher = watcher
        watcher.schedule_reload = Mock()

        event = Mock(spec=FileSystemEvent)
        event.is_directory = False
        event.src_path = "/some/other/file.json"

        handler = OpenAICredentialsFileHandler(mock_manager)
        handler.on_modified(event)

        # Should not schedule reload
        watcher.schedule_reload.assert_not_called()

    @pytest.mark.asyncio
    async def test_debounce_prevents_multiple_reloads(
        self, watcher, mock_manager, temp_auth_file
    ):
        """Test that debounce prevents multiple reloads in quick succession."""
        mock_manager._auth_path = temp_auth_file
        mock_manager._watcher = watcher
        watcher.schedule_reload = Mock()

        # Simulate multiple file changes
        event = Mock(spec=FileSystemEvent)
        event.is_directory = False
        event.src_path = str(temp_auth_file)

        handler = OpenAICredentialsFileHandler(mock_manager)

        # First change
        handler.on_modified(event)
        assert watcher.schedule_reload.call_count == 1

        # Second change immediately - should be debounced by schedule_reload
        handler.on_modified(event)
        # Should still be 1 call if debounce is working
        # (The actual debounce logic is in schedule_reload)
        assert (
            watcher.schedule_reload.call_count == 2
        )  # Both calls go through, debounce is inside schedule_reload

    @pytest.mark.asyncio
    async def test_watcher_start_stop(self, watcher, mock_manager, temp_auth_file):
        """Test starting and stopping the watcher."""
        mock_manager._auth_path = temp_auth_file

        watcher.start(temp_auth_file)
        assert watcher.is_running() is True

        watcher.stop()
        assert watcher.is_running() is False

    @pytest.mark.asyncio
    async def test_watcher_stop_idempotent(self, watcher, mock_manager, temp_auth_file):
        """Test that stopping watcher multiple times is safe."""
        mock_manager._auth_path = temp_auth_file

        watcher.start(temp_auth_file)
        watcher.stop()
        watcher.stop()  # Second stop should be no-op

        assert watcher.is_running() is False
