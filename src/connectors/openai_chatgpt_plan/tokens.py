"""Serialized rotating refresh for ChatGPT-plan SIWC profiles.

Per-profile asyncio locks plus a post-lock reload prevent duplicate refresh
POSTs when several requests race. Terminal refresh failures mark the profile
``needs_reauth`` and never retry a dead refresh token.
"""

from __future__ import annotations

import asyncio
import contextlib
from contextlib import AbstractAsyncContextManager
import logging
import re
from collections.abc import Mapping
from datetime import datetime, timedelta, timezone
from collections.abc import AsyncIterator
from typing import Any, Protocol

import httpx

from src.connectors.openai_chatgpt_plan.models import ChatGPTPlanProfile
from src.connectors.openai_chatgpt_plan.oauth import SIWC_RESOURCE, SIWC_TOKEN_URL
from src.connectors.openai_chatgpt_plan.oidc import has_chatgpt_plan_use_scope
from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore
from src.core.common.exceptions import LLMProxyError
from src.core.common.logging_utils import DEFAULT_REDACTED_FIELDS, redact_dict
from src.core.url_safety import assert_url_safe_for_egress

logger = logging.getLogger(__name__)

DEFAULT_REFRESH_SKEW_SECONDS = 60.0
REFRESH_TIMEOUT_SECONDS = 30.0
DEFAULT_REFRESH_TOKEN_LIFETIME = timedelta(days=30)
_SAFE_ERROR_CODE = re.compile(r"^[a-z0-9._-]{1,64}$")

TERMINAL_REFRESH_ERROR_CODES = frozenset(
    {
        "invalid_grant",
        "invalid_refresh_token",
        "token_expired",
        "refresh_token_expired",
        "refresh_token_invalidated",
        "refresh_token_reused",
        "disconnected",
        "invalid_client",
    }
)

_TOKEN_REDACT_FIELDS = set(DEFAULT_REDACTED_FIELDS) | {
    "access_token",
    "refresh_token",
    "id_token",
    "error_description",
}


def _utc_now() -> datetime:
    return datetime.now(timezone.utc)


def _redact_token_mapping(data: Mapping[str, Any] | None) -> dict[str, Any]:
    if not data:
        return {}
    return redact_dict(dict(data), redacted_fields=_TOKEN_REDACT_FIELDS)


class ChatGPTPlanTokenError(LLMProxyError):
    """Refresh or bearer-acquisition failure for a ChatGPT-plan profile."""

    def __init__(
        self,
        message: str,
        details: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        status_code = kwargs.pop("status_code", 401)
        super().__init__(
            message,
            details=_redact_token_mapping(details),
            status_code=status_code,
            **kwargs,
        )


class IChatGPTPlanTokenManager(Protocol):
    async def get_access_token(self, profile_id: str) -> str: ...

    async def force_refresh(self, profile_id: str) -> ChatGPTPlanProfile: ...

    async def mark_needs_reauth(self, profile_id: str, reason: str) -> None: ...

    def profile_request_slot(
        self, profile_id: str
    ) -> AbstractAsyncContextManager[None]: ...


class ChatGPTPlanTokenManager:
    """Acquire a valid access token with per-profile refresh serialization."""

    def __init__(
        self,
        profile_store: ChatGPTPlanProfileStore,
        *,
        http_client: httpx.AsyncClient | None = None,
        refresh_skew_seconds: float = DEFAULT_REFRESH_SKEW_SECONDS,
        timeout_seconds: float = REFRESH_TIMEOUT_SECONDS,
    ) -> None:
        self._profile_store = profile_store
        self._http_client = http_client
        self._refresh_skew_seconds = refresh_skew_seconds
        self._timeout_seconds = timeout_seconds
        self._locks: dict[str, asyncio.Lock] = {}
        self._locks_guard = asyncio.Lock()
        # Serialize refresh against in-flight upstream requests on the same
        # profile so concurrent HTTP/2 writers are not disrupted mid-write.
        self._inflight: dict[str, int] = {}
        self._inflight_guard = asyncio.Lock()
        self._inflight_zero: dict[str, asyncio.Event] = {}
        self._refresh_gate: dict[str, asyncio.Event] = {}

    async def get_access_token(self, profile_id: str) -> str:
        profile = await self._require_profile(profile_id)
        self._raise_if_unusable(profile)
        if self._is_access_token_fresh(profile):
            return self._require_access_token(profile)
        lock = await self._lock_for(profile_id)
        async with lock:
            profile = await self._require_profile(profile_id)
            self._raise_if_unusable(profile)
            if self._is_access_token_fresh(profile):
                return self._require_access_token(profile)
            refreshed = await self._refresh_locked(profile)
            return self._require_access_token(refreshed)

    async def force_refresh(self, profile_id: str) -> ChatGPTPlanProfile:
        profile = await self._require_profile(profile_id)
        self._raise_if_unusable(profile)
        seen_refresh = profile.refresh_token
        lock = await self._lock_for(profile_id)
        async with lock:
            profile = await self._require_profile(profile_id)
            self._raise_if_unusable(profile)
            if (
                seen_refresh
                and profile.refresh_token != seen_refresh
                and self._is_access_token_fresh(profile)
            ):
                return profile
            return await self._refresh_locked(profile)

    async def mark_needs_reauth(self, profile_id: str, reason: str) -> None:
        """Mark the profile as requiring a new browser authorization.

        ``reason`` is accepted for callers and must never be logged; it may
        contain token material from an upstream error payload.
        """

        profile = await self._require_profile(profile_id)
        if profile.status == "needs_reauth":
            return
        logger.warning(
            "ChatGPT-plan profile %s requires reauthorization",
            profile_id,
        )
        logger.debug("ChatGPT-plan profile %s marked needs_reauth", profile_id)
        _ = reason
        updated = profile.model_copy(
            update={"status": "needs_reauth", "updated_at": _utc_now()}
        )
        await self._profile_store.save_atomic(updated)

    async def _lock_for(self, profile_id: str) -> asyncio.Lock:
        async with self._locks_guard:
            lock = self._locks.get(profile_id)
            if lock is None:
                lock = asyncio.Lock()
                self._locks[profile_id] = lock
            return lock

    async def _ensure_inflight_events(self, profile_id: str) -> tuple[asyncio.Event, asyncio.Event]:
        async with self._inflight_guard:
            zero = self._inflight_zero.get(profile_id)
            if zero is None:
                zero = asyncio.Event()
                zero.set()
                self._inflight_zero[profile_id] = zero
            gate = self._refresh_gate.get(profile_id)
            if gate is None:
                gate = asyncio.Event()
                gate.set()
                self._refresh_gate[profile_id] = gate
            return zero, gate

    @contextlib.asynccontextmanager
    async def profile_request_slot(
        self, profile_id: str
    ) -> AsyncIterator[None]:
        """Hold a per-profile in-flight slot for the duration of an upstream call.

        New slots wait while a refresh is in progress so additional concurrent
        HTTP/2 writers are not opened under token rotation. In-flight writers keep
        their slots; WriteError is retried once at the HTTP client layer.
        """
        _zero, gate = await self._ensure_inflight_events(profile_id)
        await gate.wait()
        async with self._inflight_guard:
            self._inflight[profile_id] = self._inflight.get(profile_id, 0) + 1
            zero = self._inflight_zero[profile_id]
            zero.clear()
        try:
            yield
        finally:
            async with self._inflight_guard:
                remaining = max(0, self._inflight.get(profile_id, 1) - 1)
                self._inflight[profile_id] = remaining
                if remaining == 0:
                    self._inflight_zero[profile_id].set()

    @contextlib.asynccontextmanager
    async def _refresh_exclusion(
        self, profile_id: str
    ) -> AsyncIterator[None]:
        """Block new profile slots for the duration of a token refresh.

        In-flight HTTP/2 writers keep their slots; new requests wait on the gate
        so they do not open concurrent writes on a connection mid-refresh.
        WriteError from writers already in flight is retried once at the HTTP
        client layer.
        """
        _zero, gate = await self._ensure_inflight_events(profile_id)
        gate.clear()
        try:
            yield
        finally:
            gate.set()

    async def _require_profile(self, profile_id: str) -> ChatGPTPlanProfile:
        profile = await self._profile_store.load(profile_id)
        if profile is None:
            raise ChatGPTPlanTokenError(
                "ChatGPT-plan profile was not found.",
                details={"profile_id": profile_id},
                status_code=404,
            )
        return profile

    def _raise_if_unusable(self, profile: ChatGPTPlanProfile) -> None:
        if profile.status in ("needs_reauth", "signed_out"):
            raise ChatGPTPlanTokenError(
                "ChatGPT-plan profile requires reauthorization.",
                details={
                    "profile_id": profile.profile_id,
                    "status": profile.status,
                },
            )

    def _is_access_token_fresh(self, profile: ChatGPTPlanProfile) -> bool:
        if not profile.access_token:
            return False
        expires = profile.access_token_expires_at
        if expires is None:
            return False
        if expires.tzinfo is None:
            expires = expires.replace(tzinfo=timezone.utc)
        skew = timedelta(seconds=self._refresh_skew_seconds)
        return expires > _utc_now() + skew

    def _require_access_token(self, profile: ChatGPTPlanProfile) -> str:
        token = profile.access_token
        if not isinstance(token, str) or not token:
            raise ChatGPTPlanTokenError(
                "ChatGPT-plan profile is missing an access token.",
                details={"profile_id": profile.profile_id},
            )
        return token

    async def _refresh_locked(self, profile: ChatGPTPlanProfile) -> ChatGPTPlanProfile:
        async with self._refresh_exclusion(profile.profile_id):
            return await self._refresh_locked_inner(profile)

    async def _refresh_locked_inner(
        self, profile: ChatGPTPlanProfile
    ) -> ChatGPTPlanProfile:
        refresh_token = (profile.refresh_token or "").strip()
        if not refresh_token:
            await self.mark_needs_reauth(profile.profile_id, "missing_refresh_token")
            raise ChatGPTPlanTokenError(
                "ChatGPT-plan profile requires reauthorization.",
                details={"profile_id": profile.profile_id, "status": "needs_reauth"},
            )
        logger.debug("Refreshing ChatGPT-plan profile %s", profile.profile_id)
        payload, status_code = await self._post_refresh(profile, refresh_token)
        if status_code >= 400:
            error_code = _extract_oauth_error_code(payload)
            if _is_terminal_refresh_error(status_code, error_code):
                await self.mark_needs_reauth(profile.profile_id, error_code)
                raise ChatGPTPlanTokenError(
                    "ChatGPT-plan refresh token is no longer valid. "
                    "Reauthorization is required.",
                    details=_safe_refresh_error_details(profile.profile_id, error_code),
                )
            raise ChatGPTPlanTokenError(
                "ChatGPT-plan token refresh failed.",
                details={"profile_id": profile.profile_id, "status_code": status_code},
                status_code=503 if status_code >= 500 else 401,
            )
        return await self._persist_rotated_tokens(profile, payload)

    async def _post_refresh(
        self, profile: ChatGPTPlanProfile, refresh_token: str
    ) -> tuple[dict[str, Any], int]:
        assert_url_safe_for_egress(SIWC_TOKEN_URL)
        form = {
            "grant_type": "refresh_token",
            "refresh_token": refresh_token,
            "client_id": profile.issued_client_id,
            "resource": profile.resource or SIWC_RESOURCE,
        }
        headers = {"Content-Type": "application/x-www-form-urlencoded"}
        try:
            if self._http_client is None:
                async with httpx.AsyncClient() as client:
                    response = await client.post(
                        SIWC_TOKEN_URL,
                        data=form,
                        headers=headers,
                        timeout=self._timeout_seconds,
                    )
            else:
                response = await self._http_client.post(
                    SIWC_TOKEN_URL,
                    data=form,
                    headers=headers,
                    timeout=self._timeout_seconds,
                )
        except httpx.HTTPError as exc:
            logger.debug(
                "ChatGPT-plan token refresh HTTP error for profile %s",
                profile.profile_id,
                exc_info=True,
            )
            raise ChatGPTPlanTokenError(
                "ChatGPT-plan token refresh failed.",
                details={"profile_id": profile.profile_id},
                status_code=503,
            ) from exc
        payload = _response_json_object(response)
        return payload, response.status_code

    async def _persist_rotated_tokens(
        self,
        profile: ChatGPTPlanProfile,
        tokens: Mapping[str, Any],
    ) -> ChatGPTPlanProfile:
        access_token = tokens.get("access_token")
        if not isinstance(access_token, str) or not access_token:
            raise ChatGPTPlanTokenError(
                "ChatGPT-plan token refresh response is missing credentials.",
                details={"profile_id": profile.profile_id},
            )
        refresh_raw = tokens.get("refresh_token")
        refresh_token = (
            refresh_raw
            if isinstance(refresh_raw, str) and refresh_raw
            else profile.refresh_token
        )
        id_raw = tokens.get("id_token")
        id_token = id_raw if isinstance(id_raw, str) and id_raw else profile.id_token
        now = _utc_now()
        access_expires = _expires_at_from_seconds(now, tokens.get("expires_in"))
        refresh_expires = _expires_at_from_seconds(
            now, tokens.get("refresh_expires_in")
        )
        if refresh_expires is None:
            refresh_expires = now + DEFAULT_REFRESH_TOKEN_LIFETIME
        granted_scopes = profile.granted_scopes
        status = profile.status
        if "scope" in tokens:
            granted_scopes = _parse_granted_scopes(tokens.get("scope"))
            status = (
                "ready"
                if has_chatgpt_plan_use_scope(granted_scopes)
                else "missing_plan_scope"
            )
        updated = profile.model_copy(
            update={
                "access_token": access_token,
                "refresh_token": refresh_token,
                "id_token": id_token,
                "granted_scopes": granted_scopes,
                "access_token_expires_at": access_expires,
                "refresh_token_expires_at": refresh_expires,
                "status": status,
                "updated_at": now,
            }
        )
        await self._profile_store.save_atomic(updated)
        logger.debug("Rotated ChatGPT-plan tokens for profile %s", profile.profile_id)
        return updated


def _parse_granted_scopes(scope_raw: Any) -> tuple[str, ...]:
    if isinstance(scope_raw, str) and scope_raw.strip():
        return tuple(scope_raw.split())
    if isinstance(scope_raw, list | tuple):
        return tuple(str(item) for item in scope_raw if str(item).strip())
    return ()


def _expires_at_from_seconds(now: datetime, raw: Any) -> datetime | None:
    if isinstance(raw, int | float):
        return now + timedelta(seconds=int(raw))
    return None


def _response_json_object(response: httpx.Response) -> dict[str, Any]:
    try:
        payload = response.json()
    except ValueError:
        return {}
    if isinstance(payload, dict):
        return payload
    return {}


def _extract_oauth_error_code(payload: Mapping[str, Any]) -> str:
    error = payload.get("error")
    if isinstance(error, str):
        return error.strip()
    if isinstance(error, dict):
        nested = error.get("code") or error.get("error")
        if isinstance(nested, str):
            return nested.strip()
    for key in ("error_code", "code"):
        value = payload.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip()
    return ""


def _is_terminal_refresh_error(status_code: int, error_code: str) -> bool:
    normalized = error_code.strip().lower()
    if normalized in TERMINAL_REFRESH_ERROR_CODES:
        return True
    if "disconnect" in normalized or "reused" in normalized:
        return True
    return status_code in (400, 401, 403)


def _safe_refresh_error_details(profile_id: str, error_code: str) -> dict[str, Any]:
    details: dict[str, Any] = {
        "profile_id": profile_id,
        "status": "needs_reauth",
    }
    normalized = error_code.strip().lower()
    if _SAFE_ERROR_CODE.fullmatch(normalized):
        details["error"] = normalized
    return details


__all__ = [
    "DEFAULT_REFRESH_SKEW_SECONDS",
    "TERMINAL_REFRESH_ERROR_CODES",
    "ChatGPTPlanTokenError",
    "ChatGPTPlanTokenManager",
    "IChatGPTPlanTokenManager",
]
