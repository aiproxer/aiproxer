"""Token refresh service for OpenAI Codex managed OAuth accounts."""

from __future__ import annotations

import asyncio
import logging
import time
from collections.abc import Mapping
from typing import Any

import httpx

from src.connectors.openai_codex.managed_oauth_constants import (
    OPENAI_OAUTH_CLIENT_ID,
    OPENAI_OAUTH_TOKEN_URL,
)
from src.connectors.openai_codex.managed_oauth_jwt import (
    extract_chatgpt_account_id_from_token,
    extract_email_from_token,
    extract_expiry_ms_from_token,
)
from src.connectors.openai_codex.managed_oauth_models import ManagedOAuthAccount
from src.connectors.openai_codex.managed_oauth_storage import ManagedOAuthStorageService

logger = logging.getLogger(__name__)

# Match legacy credential refresh: slow token endpoint + transient errors are common.
_MANAGED_OAUTH_TOKEN_TIMEOUT_S = 30.0


def _is_transient_network_error(exc: BaseException) -> bool:
    """True for transport-level failures that are worth retrying (like legacy OAuth POST)."""
    return isinstance(
        exc,
        httpx.TimeoutException | httpx.ConnectError | httpx.ReadError,
    )


class ManagedOAuthRefreshError(RuntimeError):
    """Raised when token refresh fails for a managed account."""

    def __init__(
        self,
        message: str,
        *,
        account_id: str,
        account_email: str | None = None,
        needs_reauth: bool = False,
        from_transient_network: bool = False,
        http_status: int | None = None,
    ) -> None:
        super().__init__(message)
        self.account_id = account_id
        self.account_email = account_email
        self.needs_reauth = needs_reauth
        self.from_transient_network = from_transient_network
        self.http_status = http_status


def _format_account_label(account: ManagedOAuthAccount) -> str:
    """Human-readable account label for refresh logs."""
    if isinstance(account.email, str) and account.email.strip():
        return f"{account.account_id} ({account.email.strip()})"
    return account.account_id


def _oauth_error_from_payload(
    payload: Mapping[str, Any],
) -> tuple[str | None, str | None]:
    """Extract best-effort OAuth error code/message from token endpoint JSON."""
    code: str | None = None
    message: str | None = None

    error_field = payload.get("error")
    if isinstance(error_field, Mapping):
        raw_code = error_field.get("code")
        if not isinstance(raw_code, str):
            raw_code = error_field.get("error")
        raw_msg = error_field.get("message")
        if isinstance(raw_code, str) and raw_code.strip():
            code = raw_code.strip()
        if isinstance(raw_msg, str) and raw_msg.strip():
            message = raw_msg.strip()
    elif isinstance(error_field, str) and error_field.strip():
        code = error_field.strip()

    if message is None:
        raw_desc = payload.get("error_description")
        if isinstance(raw_desc, str) and raw_desc.strip():
            message = raw_desc.strip()

    return code, message


def _format_status_failure_message(
    status_code: int,
    *,
    error_code: str | None,
    error_message: str | None,
) -> str:
    """Build a compact refresh-failure message from HTTP status + optional OAuth fields."""
    message = f"Token refresh rejected with HTTP {status_code}"
    if error_code:
        message += f" ({error_code})"
    if error_message:
        message += f": {error_message}"
    return message


class ManagedOAuthRefreshService:
    """Refreshes managed OAuth tokens with retry and per-account locking."""

    def __init__(
        self,
        storage: ManagedOAuthStorageService,
        *,
        http_client: httpx.AsyncClient | None = None,
        max_retries: int = 3,
        base_delay_seconds: float = 1.0,
    ) -> None:
        self._storage = storage
        self._http_client = http_client
        self._max_retries = max(1, int(max_retries))
        self._base_delay_seconds = max(0.1, float(base_delay_seconds))
        self._refresh_locks: dict[str, asyncio.Lock] = {}

    def _get_lock(self, account_id: str) -> asyncio.Lock:
        lock = self._refresh_locks.get(account_id)
        if lock is None:
            lock = asyncio.Lock()
            self._refresh_locks[account_id] = lock
        return lock

    async def refresh_if_needed(
        self,
        account: ManagedOAuthAccount,
        *,
        buffer_ms: int = 300_000,
    ) -> ManagedOAuthAccount:
        """Refresh token if account is expired (or near expiry)."""
        if not account.is_expired(buffer_ms=buffer_ms):
            return account
        return await self._refresh_with_retry(account)

    async def force_refresh(self, account: ManagedOAuthAccount) -> ManagedOAuthAccount:
        """Force token refresh irrespective of current expiry timestamp."""
        return await self._refresh_with_retry(account)

    async def _refresh_with_retry(
        self,
        account: ManagedOAuthAccount,
    ) -> ManagedOAuthAccount:
        last_error: Exception | None = None
        for attempt in range(self._max_retries):
            try:
                return await self._refresh_once(account)
            except ManagedOAuthRefreshError:
                raise
            except Exception as exc:
                last_error = exc
                if attempt + 1 >= self._max_retries:
                    break
                delay = self._base_delay_seconds * (2**attempt)
                if _is_transient_network_error(exc):
                    logger.info(
                        "Transient error during managed OAuth token refresh (%s, attempt %d/%d) "
                        "for account %s; retrying in %.1fs",
                        exc.__class__.__name__,
                        attempt + 1,
                        self._max_retries,
                        _format_account_label(account),
                        delay,
                    )
                elif logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Managed OAuth refresh attempt %d/%d failed for %s; retrying in %.1fs",
                        attempt + 1,
                        self._max_retries,
                        _format_account_label(account),
                        delay,
                        exc_info=True,
                    )
                await asyncio.sleep(delay)

        assert last_error is not None
        raise ManagedOAuthRefreshError(
            f"Managed OAuth refresh failed after {self._max_retries} attempts: {last_error}",
            account_id=account.account_id,
            account_email=account.email,
            from_transient_network=_is_transient_network_error(last_error),
        )

    async def _refresh_once(self, account: ManagedOAuthAccount) -> ManagedOAuthAccount:
        async with self._get_lock(account.account_id):
            # Another coroutine may have refreshed this account already.
            stored = await self._storage.get_account(account.account_id)
            if stored is not None and not stored.is_expired(buffer_ms=60_000):
                return stored

            if self._http_client is None:
                async with httpx.AsyncClient() as client:
                    return await self._execute_refresh(account, client)
            return await self._execute_refresh(account, self._http_client)

    async def _execute_refresh(
        self,
        account: ManagedOAuthAccount,
        client: httpx.AsyncClient,
    ) -> ManagedOAuthAccount:
        # Refresh POST uses OPENAI_OAUTH_TOKEN_URL (hardcoded). httpx defaults
        # ``follow_redirects`` to False on the client unless overridden; no SSRF
        # preflight is required while the URL remains a constant (not from config).
        payload = {
            "client_id": OPENAI_OAUTH_CLIENT_ID,
            "grant_type": "refresh_token",
            "refresh_token": account.refresh_token,
            "scope": account.scope or "openid profile email",
        }

        response = await client.post(
            OPENAI_OAUTH_TOKEN_URL,
            json=payload,
            headers={"Content-Type": "application/json"},
            timeout=_MANAGED_OAUTH_TOKEN_TIMEOUT_S,
        )

        if response.status_code >= 400:
            error_code: str | None = None
            error_message: str | None = None
            try:
                body = response.json()
                if isinstance(body, Mapping):
                    error_code, error_message = _oauth_error_from_payload(body)
            except Exception:
                # Keep the HTTP status context even when body is not JSON.
                pass

            if response.status_code == 400 and error_code == "invalid_grant":
                marked = account.mark_needs_reauth()
                await self._storage.save_account(marked)
                raise ManagedOAuthRefreshError(
                    "Refresh token invalid or revoked; re-authorization required",
                    account_id=account.account_id,
                    account_email=account.email,
                    needs_reauth=True,
                    http_status=400,
                )

            needs_reauth = response.status_code in (401, 403)
            if needs_reauth:
                marked = account.mark_needs_reauth()
                await self._storage.save_account(marked)

            raise ManagedOAuthRefreshError(
                _format_status_failure_message(
                    response.status_code,
                    error_code=error_code,
                    error_message=error_message,
                ),
                account_id=account.account_id,
                account_email=account.email,
                needs_reauth=needs_reauth,
                http_status=response.status_code,
            )

        try:
            data = response.json()
        except Exception as exc:
            raise ManagedOAuthRefreshError(
                f"Unable to parse refresh response JSON: {exc}",
                account_id=account.account_id,
                account_email=account.email,
            ) from exc

        access_token = data.get("access_token")
        if not isinstance(access_token, str) or not access_token:
            raise ManagedOAuthRefreshError(
                "Refresh response missing access_token",
                account_id=account.account_id,
                account_email=account.email,
            )

        refresh_token_raw = data.get("refresh_token")
        refresh_token = (
            refresh_token_raw
            if isinstance(refresh_token_raw, str) and refresh_token_raw
            else account.refresh_token
        )

        token_type_raw = data.get("token_type")
        token_type = (
            token_type_raw
            if isinstance(token_type_raw, str) and token_type_raw
            else account.token_type
        )

        scope_raw = data.get("scope")
        scope = scope_raw if isinstance(scope_raw, str) and scope_raw else account.scope

        now_ms = int(time.time() * 1000)
        expires_in_raw = data.get("expires_in")
        expiry_ms: int | None = None
        if isinstance(expires_in_raw, int | float):
            expires_in_seconds = max(int(float(expires_in_raw)), 0)
            if expires_in_seconds > 0:
                expiry_ms = now_ms + (expires_in_seconds * 1000)
        if expiry_ms is None:
            expiry_ms = extract_expiry_ms_from_token(access_token)

        updated = account.with_updated_tokens(
            access_token=access_token,
            refresh_token=refresh_token,
            expiry_date=expiry_ms,
            email=extract_email_from_token(access_token),
            chatgpt_account_id=extract_chatgpt_account_id_from_token(access_token),
            scope=scope,
            token_type=token_type,
        )
        await self._storage.save_account(updated)
        return updated
