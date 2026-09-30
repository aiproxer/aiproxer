"""Per-profile public GET /v1/models discovery and cache.

Authenticates with a ready SIWC profile and lists models from
``https://api.openai.com/v1/models`` only. Cache keys are derived from
non-secret registration identity, never bearer token bytes. Transient
discovery failures surface as catalog unavailability; this module does not
load Codex catalogs, bundled snapshots, or client-version fallbacks.
"""

from __future__ import annotations

import asyncio
import hashlib
import logging
import time
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any, Protocol

import httpx

from src.connectors.openai_chatgpt_plan.config import (
    DEFAULT_CHATGPT_PLAN_MODEL_CATALOG_TTL_SECONDS,
    ChatGPTPlanConfig,
)
from src.connectors.openai_chatgpt_plan.models import (
    ChatGPTPlanProfile,
    IChatGPTPlanProfileStore,
    redact_chatgpt_plan_mapping,
)
from src.connectors.openai_chatgpt_plan.oidc import has_chatgpt_plan_use_scope
from src.connectors.openai_chatgpt_plan.tokens import IChatGPTPlanTokenManager
from src.core.common.exceptions import LLMProxyError
from src.core.url_safety import assert_url_safe_for_egress

logger = logging.getLogger(__name__)

OPENAI_MODELS_URL = "https://api.openai.com/v1/models"
DEFAULT_CATALOG_TIMEOUT_SECONDS = 30.0


class ChatGPTPlanCatalogError(LLMProxyError):
    """Public model-catalog discovery failed or was refused for a profile."""

    def __init__(
        self,
        message: str,
        details: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        status_code = kwargs.pop("status_code", 503)
        super().__init__(
            message,
            details=redact_chatgpt_plan_mapping(details or {}),
            status_code=status_code,
            **kwargs,
        )


class IChatGPTPlanModelCatalog(Protocol):
    async def list_models(
        self,
        profile_id: str,
        *,
        force_refresh: bool = False,
    ) -> list[str]: ...

    def invalidate(self, profile_id: str) -> None: ...


@dataclass(frozen=True)
class ProfileCatalogEntry:
    profile_identity_fingerprint: str
    fetched_at: float
    models: tuple[str, ...]


def profile_identity_fingerprint(profile: ChatGPTPlanProfile) -> str:
    """Hash non-secret registration identity; never include bearer token bytes."""

    material = "\n".join(
        (
            profile.issued_client_id.strip(),
            profile.issuer.strip(),
            profile.subject.strip(),
        )
    )
    return hashlib.sha256(material.encode("utf-8")).hexdigest()


def _permission_allows_listing(permission: Mapping[str, Any]) -> bool:
    if permission.get("is_blocking") is True:
        return False
    return permission.get("allow_view") is not False


def _listable_model_id(item: object) -> str | None:
    if not isinstance(item, Mapping):
        return None
    raw_id = item.get("id")
    # Live SIWC /v1/models entries identify models by `slug`, not OpenAI `id`.
    if not isinstance(raw_id, str) or not raw_id.strip():
        raw_id = item.get("slug")
    if not isinstance(raw_id, str):
        return None
    model_id = raw_id.strip()
    if not model_id:
        return None
    obj = item.get("object")
    if obj is not None and obj != "model":
        return None
    for flag_name in ("visible", "listed", "listable"):
        if item.get(flag_name) is False:
            return None
    visibility = item.get("visibility")
    if isinstance(visibility, str) and visibility.strip().lower() == "hide":
        return None
    permissions = item.get("permission")
    if isinstance(permissions, list) and permissions:
        allowed = [
            _permission_allows_listing(permission)
            for permission in permissions
            if isinstance(permission, Mapping)
        ]
        if allowed and not any(allowed):
            return None
    return model_id


def _parse_listable_model_ids(payload: object) -> list[str]:
    if not isinstance(payload, Mapping):
        raise ChatGPTPlanCatalogError(
            "ChatGPT-plan model catalog response is invalid.",
            details={"reason": "invalid_payload"},
            status_code=503,
        )
    # Live SIWC GET /v1/models uses `models`; API-key style OpenAI uses `data`.
    if "models" in payload:
        data = payload.get("models")
    else:
        data = payload.get("data")
    if data is None:
        return []
    if not isinstance(data, list):
        raise ChatGPTPlanCatalogError(
            "ChatGPT-plan model catalog response is invalid.",
            details={"reason": "invalid_payload"},
            status_code=503,
        )
    models: list[str] = []
    for item in data:
        model_id = _listable_model_id(item)
        if model_id is not None:
            models.append(model_id)
    return models


class ChatGPTPlanModelCatalog:
    """Fetch and cache public /v1/models results for one SIWC profile at a time."""

    def __init__(
        self,
        profile_store: IChatGPTPlanProfileStore,
        token_manager: IChatGPTPlanTokenManager,
        *,
        http_client: httpx.AsyncClient | None = None,
        ttl_seconds: int | None = None,
        config: ChatGPTPlanConfig | None = None,
        timeout_seconds: float = DEFAULT_CATALOG_TIMEOUT_SECONDS,
    ) -> None:
        self._profile_store = profile_store
        self._token_manager = token_manager
        self._http_client = http_client
        if ttl_seconds is None:
            if config is None:
                ttl_seconds = DEFAULT_CHATGPT_PLAN_MODEL_CATALOG_TTL_SECONDS
            else:
                ttl_seconds = config.model_catalog.ttl_seconds
        self._ttl_seconds = ttl_seconds
        self._timeout_seconds = timeout_seconds
        self._entries: dict[str, ProfileCatalogEntry] = {}
        self._locks: dict[str, asyncio.Lock] = {}
        self._locks_guard = asyncio.Lock()

    async def list_models(
        self,
        profile_id: str,
        *,
        force_refresh: bool = False,
    ) -> list[str]:
        profile = await self._require_ready_profile(profile_id)
        fingerprint = profile_identity_fingerprint(profile)
        if not force_refresh:
            cached = self._fresh_models(profile_id, fingerprint)
            if cached is not None:
                return cached
        lock = await self._lock_for(profile_id)
        async with lock:
            profile = await self._require_ready_profile(profile_id)
            fingerprint = profile_identity_fingerprint(profile)
            if not force_refresh:
                cached = self._fresh_models(profile_id, fingerprint)
                if cached is not None:
                    return cached
            access_token = await self._token_manager.get_access_token(profile_id)
            models = await self._fetch_models(access_token)
            self._entries[profile_id] = ProfileCatalogEntry(
                profile_identity_fingerprint=fingerprint,
                fetched_at=time.monotonic(),
                models=tuple(models),
            )
            logger.debug(
                "Cached ChatGPT-plan model catalog for profile %s (%s models)",
                profile_id,
                len(models),
            )
            return list(models)

    def invalidate(self, profile_id: str) -> None:
        self._entries.pop(profile_id, None)

    async def _lock_for(self, profile_id: str) -> asyncio.Lock:
        async with self._locks_guard:
            lock = self._locks.get(profile_id)
            if lock is None:
                lock = asyncio.Lock()
                self._locks[profile_id] = lock
            return lock

    async def _require_ready_profile(self, profile_id: str) -> ChatGPTPlanProfile:
        profile = await self._profile_store.load(profile_id)
        if profile is None:
            self.invalidate(profile_id)
            raise ChatGPTPlanCatalogError(
                "ChatGPT-plan profile was not found.",
                details={"profile_id": profile_id},
                status_code=404,
            )
        if profile.status != "ready" or not has_chatgpt_plan_use_scope(
            profile.granted_scopes
        ):
            status_code = 403 if profile.status == "missing_plan_scope" else 401
            raise ChatGPTPlanCatalogError(
                "ChatGPT-plan profile is not ready for model discovery.",
                details={
                    "profile_id": profile.profile_id,
                    "status": profile.status,
                },
                status_code=status_code,
            )
        return profile

    def _fresh_models(self, profile_id: str, fingerprint: str) -> list[str] | None:
        entry = self._entries.get(profile_id)
        if entry is None:
            return None
        if entry.profile_identity_fingerprint != fingerprint:
            return None
        age = time.monotonic() - entry.fetched_at
        if age >= self._ttl_seconds:
            return None
        return list(entry.models)

    async def _fetch_models(self, access_token: str) -> list[str]:
        assert_url_safe_for_egress(OPENAI_MODELS_URL)
        headers = {
            "Authorization": f"Bearer {access_token}",
            "Accept": "application/json",
        }
        try:
            response = await self._get(OPENAI_MODELS_URL, headers)
        except httpx.RequestError as exc:
            logger.debug(
                "ChatGPT-plan model catalog request failed",
                exc_info=True,
            )
            raise ChatGPTPlanCatalogError(
                "ChatGPT-plan model catalog is temporarily unavailable.",
                details={"reason": "transient_network"},
                status_code=503,
            ) from exc
        if response.status_code >= 400:
            raise ChatGPTPlanCatalogError(
                "ChatGPT-plan model catalog is temporarily unavailable.",
                details={
                    "reason": "upstream_error",
                    "status_code": response.status_code,
                },
                status_code=503,
            )
        try:
            payload = response.json()
        except ValueError as exc:
            raise ChatGPTPlanCatalogError(
                "ChatGPT-plan model catalog is temporarily unavailable.",
                details={"reason": "invalid_payload"},
                status_code=503,
            ) from exc
        return _parse_listable_model_ids(payload)

    async def _get(self, url: str, headers: Mapping[str, str]) -> httpx.Response:
        if self._http_client is None:
            async with httpx.AsyncClient() as client:
                return await client.get(
                    url, headers=headers, timeout=self._timeout_seconds
                )
        return await self._http_client.get(
            url, headers=headers, timeout=self._timeout_seconds
        )


__all__ = [
    "DEFAULT_CATALOG_TIMEOUT_SECONDS",
    "OPENAI_MODELS_URL",
    "ChatGPTPlanCatalogError",
    "ChatGPTPlanModelCatalog",
    "IChatGPTPlanModelCatalog",
    "ProfileCatalogEntry",
    "profile_identity_fingerprint",
]
