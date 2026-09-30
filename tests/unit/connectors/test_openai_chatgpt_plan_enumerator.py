"""Unit tests for ChatGPT-plan configured model enumeration (task 3.2).

HTTP is mocked. Token fixtures must not use the sk- prefix.
"""

from __future__ import annotations

import sys
from collections.abc import Mapping
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any
from unittest.mock import AsyncMock

import httpx
import pytest
from src.connectors.base import add_vendor_prefix
from src.core.common.model_catalog import BackendModelEnumeration
from src.core.config.app_config import AppConfig, BackendConfig
from src.core.services.translation_service import TranslationService

ACCESS_TOKEN = "enumerator-access-token-test-value"
OTHER_ACCESS_TOKEN = "enumerator-work-access-token-test-value"
ISSUED_CLIENT_ID = "oaiapp_enumerator_issued_client"
OTHER_ISSUED_CLIENT_ID = "oaiapp_enumerator_issued_client_work"
PROFILE_ID = "primary"
OTHER_PROFILE_ID = "work"
SUBJECT = "oidc-subject-enumerator-primary"
OTHER_SUBJECT = "oidc-subject-enumerator-work"
EMAIL = "chatgpt-plan-enumerator@example.com"
DISPLAY_NAME = "ChatGPT Plan Enumerator User"
ISSUER = "https://auth.openai.com"
SIWC_RESOURCE = "https://api.openai.com/v1"
OPENAI_MODELS_URL = "https://api.openai.com/v1/models"
PLAN_USE_SCOPE = "chatgpt.tokens.use.direct"
FULL_SCOPES = (
    "openid",
    "profile",
    "email",
    "offline_access",
    "resource.invoke",
    PLAN_USE_SCOPE,
)
CONNECTOR = "openai-chatgpt-plan"
PACKAGE_NAME = "src.connectors.openai_chatgpt_plan"


def _now() -> datetime:
    return datetime.now(timezone.utc)


def _profile_kwargs(**overrides: Any) -> dict[str, Any]:
    now = _now()
    payload: dict[str, Any] = {
        "schema_version": 1,
        "profile_id": PROFILE_ID,
        "issued_client_id": ISSUED_CLIENT_ID,
        "issuer": ISSUER,
        "subject": SUBJECT,
        "email": EMAIL,
        "display_name": DISPLAY_NAME,
        "access_token": ACCESS_TOKEN,
        "refresh_token": "enumerator-refresh-token-test-value",
        "id_token": "enumerator-id-token-test-value",
        "granted_scopes": FULL_SCOPES,
        "resource": SIWC_RESOURCE,
        "access_token_expires_at": now + timedelta(hours=1),
        "refresh_token_expires_at": now + timedelta(days=30),
        "status": "ready",
        "created_at": now - timedelta(days=1),
        "updated_at": now - timedelta(hours=1),
    }
    payload.update(overrides)
    return payload


def _profile(**overrides: Any) -> Any:
    from src.connectors.openai_chatgpt_plan.models import ChatGPTPlanProfile

    return ChatGPTPlanProfile(**_profile_kwargs(**overrides))


def _models_payload(*model_ids: str) -> dict[str, Any]:
    return {
        "object": "list",
        "data": [
            {"id": model_id, "object": "model", "owned_by": "openai"}
            for model_id in model_ids
        ],
    }


def _backend_config(
    profile_id: str, *, profiles_path: str | None = None
) -> BackendConfig:
    chatgpt_plan: dict[str, Any] = {"profile_id": profile_id}
    if profiles_path is not None:
        chatgpt_plan["profiles_path"] = profiles_path
    return BackendConfig(
        connector=CONNECTOR,
        extra={"chatgpt_plan": chatgpt_plan},
    )


class _FakeTokenManager:
    def __init__(self, tokens: dict[str, str] | None = None) -> None:
        self.tokens = tokens or {PROFILE_ID: ACCESS_TOKEN}
        self.calls: list[str] = []

    async def get_access_token(self, profile_id: str) -> str:
        self.calls.append(profile_id)
        return self.tokens[profile_id]

    async def force_refresh(self, profile_id: str) -> Any:
        raise AssertionError("enumerator catalog must not call force_refresh")

    async def mark_needs_reauth(self, profile_id: str, reason: str) -> None:
        raise AssertionError("enumerator catalog must not call mark_needs_reauth")


class _ModelsCapture:
    def __init__(self) -> None:
        self.requests: list[httpx.Request] = []

    def handler(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        auth = request.headers.get("authorization", "")
        if OTHER_ACCESS_TOKEN in auth:
            payload = _models_payload("o3-mini", "gpt-4.1")
        else:
            payload = _models_payload("gpt-4o", "gpt-4.1")
        return httpx.Response(200, json=payload)


async def _real_catalog(
    tmp_path: Path,
) -> tuple[Any, Any, _FakeTokenManager, _ModelsCapture, httpx.AsyncClient]:
    from src.connectors.openai_chatgpt_plan.catalog import ChatGPTPlanModelCatalog
    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

    capture = _ModelsCapture()
    store = ChatGPTPlanProfileStore(tmp_path / "profiles")
    token_manager = _FakeTokenManager(
        {PROFILE_ID: ACCESS_TOKEN, OTHER_PROFILE_ID: OTHER_ACCESS_TOKEN}
    )
    client = httpx.AsyncClient(transport=httpx.MockTransport(capture.handler))
    catalog = ChatGPTPlanModelCatalog(
        store,
        token_manager,
        http_client=client,
        ttl_seconds=300,
    )
    await store.save_atomic(_profile())
    await store.save_atomic(
        _profile(
            profile_id=OTHER_PROFILE_ID,
            issued_client_id=OTHER_ISSUED_CLIENT_ID,
            subject=OTHER_SUBJECT,
            access_token=OTHER_ACCESS_TOKEN,
        )
    )
    return catalog, store, token_manager, capture, client


async def _persist_two_ready_profiles(tmp_path: Path) -> Path:
    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

    profiles_path = tmp_path / "profiles"
    store = ChatGPTPlanProfileStore(profiles_path)
    await store.save_atomic(_profile())
    await store.save_atomic(
        _profile(
            profile_id=OTHER_PROFILE_ID,
            issued_client_id=OTHER_ISSUED_CLIENT_ID,
            subject=OTHER_SUBJECT,
            access_token=OTHER_ACCESS_TOKEN,
        )
    )
    return profiles_path


def _install_owned_models_http(monkeypatch: pytest.MonkeyPatch) -> _ModelsCapture:
    from src.connectors.openai_chatgpt_plan.catalog import ChatGPTPlanModelCatalog

    capture = _ModelsCapture()

    async def _get(
        self: object, url: str, headers: Mapping[str, str]
    ) -> httpx.Response:
        del self
        request = httpx.Request("GET", url, headers=headers)
        return capture.handler(request)

    monkeypatch.setattr(ChatGPTPlanModelCatalog, "_get", _get)
    return capture


def _plan_config_for(profiles_path: Path, profile_id: str = PROFILE_ID) -> Any:
    from src.connectors.openai_chatgpt_plan.enumerator import (
        chatgpt_plan_config_from_backend_config,
    )

    return chatgpt_plan_config_from_backend_config(
        _backend_config(profile_id, profiles_path=str(profiles_path))
    )


def _make_connector() -> Any:
    from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector

    connector = OpenAIChatGPTPlanConnector(
        client=AsyncMock(spec=httpx.AsyncClient),
        config=AppConfig(),
        translation_service=TranslationService(),
    )
    connector.disable_health_check()
    return connector


class _FakeCatalog:
    def __init__(self, models_by_profile: dict[str, list[str]]) -> None:
        self.models_by_profile = models_by_profile
        self.calls: list[str] = []
        self.invalidations: list[str] = []

    async def list_models(
        self, profile_id: str, *, force_refresh: bool = False
    ) -> list[str]:
        del force_refresh
        self.calls.append(profile_id)
        return list(self.models_by_profile[profile_id])

    def invalidate(self, profile_id: str) -> None:
        self.invalidations.append(profile_id)
        self.models_by_profile.pop(profile_id, None)


class TestChatGPTPlanConfiguredModelEnumerator:
    @pytest.mark.asyncio
    async def test_enumerate_returns_selected_profile_models_only(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.enumerator import (
            ChatGPTPlanConfiguredModelEnumerator,
        )

        catalog, _store, token_manager, capture, client = await _real_catalog(tmp_path)
        enumerator = ChatGPTPlanConfiguredModelEnumerator(catalog=catalog)
        try:
            result = await enumerator.enumerate(
                "openai-chatgpt-plan.home",
                _backend_config(PROFILE_ID, profiles_path=str(tmp_path / "profiles")),
            )
        finally:
            await client.aclose()

        assert isinstance(result, BackendModelEnumeration)
        assert result.status == "available"
        assert result.connector == CONNECTOR
        assert result.instance_name == "openai-chatgpt-plan.home"
        assert result.models == (
            add_vendor_prefix("gpt-4o", "openai"),
            add_vendor_prefix("gpt-4.1", "openai"),
        )
        assert add_vendor_prefix("o3-mini", "openai") not in result.models
        assert token_manager.calls == [PROFILE_ID]
        assert len(capture.requests) == 1
        assert str(capture.requests[0].url).split("?", 1)[0] == OPENAI_MODELS_URL
        assert all(
            "chatgpt.com" not in str(request.url) for request in capture.requests
        )
        assert all("sk-" not in str(request.url) for request in capture.requests)

    @pytest.mark.asyncio
    async def test_two_instances_with_different_profile_ids_do_not_leak_models(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.enumerator import (
            ChatGPTPlanConfiguredModelEnumerator,
        )

        catalog, _store, token_manager, capture, client = await _real_catalog(tmp_path)
        enumerator = ChatGPTPlanConfiguredModelEnumerator(catalog=catalog)
        profiles_path = str(tmp_path / "profiles")
        try:
            home = await enumerator.enumerate(
                "openai-chatgpt-plan.home",
                _backend_config(PROFILE_ID, profiles_path=profiles_path),
            )
            work = await enumerator.enumerate(
                "openai-chatgpt-plan.work",
                _backend_config(OTHER_PROFILE_ID, profiles_path=profiles_path),
            )
        finally:
            await client.aclose()

        assert home.status == "available"
        assert work.status == "available"
        assert home.models == (
            add_vendor_prefix("gpt-4o", "openai"),
            add_vendor_prefix("gpt-4.1", "openai"),
        )
        assert work.models == (
            add_vendor_prefix("o3-mini", "openai"),
            add_vendor_prefix("gpt-4.1", "openai"),
        )
        assert add_vendor_prefix("gpt-4o", "openai") not in work.models
        assert add_vendor_prefix("o3-mini", "openai") not in home.models
        assert token_manager.calls == [PROFILE_ID, OTHER_PROFILE_ID]
        assert len(capture.requests) == 2

    @pytest.mark.asyncio
    async def test_stale_invalidated_catalog_refetches(self, tmp_path: Path) -> None:
        from src.connectors.openai_chatgpt_plan.enumerator import (
            ChatGPTPlanConfiguredModelEnumerator,
        )

        catalog, _store, token_manager, capture, client = await _real_catalog(tmp_path)
        enumerator = ChatGPTPlanConfiguredModelEnumerator(catalog=catalog)
        config = _backend_config(PROFILE_ID, profiles_path=str(tmp_path / "profiles"))
        try:
            first = await enumerator.enumerate("openai-chatgpt-plan.home", config)
            cached = await enumerator.enumerate("openai-chatgpt-plan.home", config)
            catalog.invalidate(PROFILE_ID)
            refreshed = await enumerator.enumerate("openai-chatgpt-plan.home", config)
        finally:
            await client.aclose()

        assert first.models == cached.models == refreshed.models
        assert first.status == "available"
        assert token_manager.calls == [PROFILE_ID, PROFILE_ID]
        assert len(capture.requests) == 2

    @pytest.mark.asyncio
    async def test_temporary_catalog_error_returns_unavailable(self) -> None:
        from src.connectors.openai_chatgpt_plan.catalog import ChatGPTPlanCatalogError
        from src.connectors.openai_chatgpt_plan.enumerator import (
            ChatGPTPlanConfiguredModelEnumerator,
        )

        class _UnavailableCatalog:
            async def list_models(
                self, profile_id: str, *, force_refresh: bool = False
            ) -> list[str]:
                del profile_id, force_refresh
                raise ChatGPTPlanCatalogError(
                    "ChatGPT-plan model catalog is temporarily unavailable.",
                    details={"reason": "transient_network"},
                    status_code=503,
                )

            def invalidate(self, profile_id: str) -> None:
                del profile_id

        enumerator = ChatGPTPlanConfiguredModelEnumerator(catalog=_UnavailableCatalog())
        result = await enumerator.enumerate(
            "openai-chatgpt-plan.home",
            _backend_config(PROFILE_ID),
        )
        assert result.status == "unavailable"
        assert result.models == ()
        assert result.connector == CONNECTOR
        assert result.error_code == "temporary_catalog_error"
        assert result.source == "chatgpt_plan_catalog"

    @pytest.mark.asyncio
    async def test_fake_catalog_is_scoped_to_requested_profile_only(self) -> None:
        from src.connectors.openai_chatgpt_plan.enumerator import (
            ChatGPTPlanConfiguredModelEnumerator,
        )

        catalog = _FakeCatalog(
            {
                PROFILE_ID: ["gpt-4o"],
                OTHER_PROFILE_ID: ["o3-mini"],
            }
        )
        enumerator = ChatGPTPlanConfiguredModelEnumerator(catalog=catalog)
        result = await enumerator.enumerate(
            "openai-chatgpt-plan.home",
            _backend_config(PROFILE_ID),
        )
        assert result.models == (add_vendor_prefix("gpt-4o", "openai"),)
        assert catalog.calls == [PROFILE_ID]


class TestChatGPTPlanOwnedCatalogGraph:
    """Production path: routing constructs enumerator() with no injected catalog."""

    @pytest.mark.asyncio
    async def test_owned_enumerator_isolates_profiles_and_is_instance_pinned(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        from src.connectors.openai_chatgpt_plan.enumerator import (
            ChatGPTPlanConfiguredModelEnumerator,
        )

        capture = _install_owned_models_http(monkeypatch)
        profiles_path = await _persist_two_ready_profiles(tmp_path)
        enumerator = ChatGPTPlanConfiguredModelEnumerator()

        home = await enumerator.enumerate(
            "openai-chatgpt-plan.home",
            _backend_config(PROFILE_ID, profiles_path=str(profiles_path)),
        )
        work = await enumerator.enumerate(
            "openai-chatgpt-plan.work",
            _backend_config(OTHER_PROFILE_ID, profiles_path=str(profiles_path)),
        )

        assert home.status == "available"
        assert work.status == "available"
        assert home.instance_pinned is True
        assert work.instance_pinned is True
        assert home.models == (
            add_vendor_prefix("gpt-4o", "openai"),
            add_vendor_prefix("gpt-4.1", "openai"),
        )
        assert work.models == (
            add_vendor_prefix("o3-mini", "openai"),
            add_vendor_prefix("gpt-4.1", "openai"),
        )
        assert add_vendor_prefix("gpt-4o", "openai") not in work.models
        assert add_vendor_prefix("o3-mini", "openai") not in home.models
        assert len(capture.requests) == 2

    @pytest.mark.asyncio
    async def test_owned_enumerator_invalidates_and_refetches(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        from src.connectors.openai_chatgpt_plan.enumerator import (
            ChatGPTPlanConfiguredModelEnumerator,
        )

        capture = _install_owned_models_http(monkeypatch)
        profiles_path = await _persist_two_ready_profiles(tmp_path)
        enumerator = ChatGPTPlanConfiguredModelEnumerator()
        config = _backend_config(PROFILE_ID, profiles_path=str(profiles_path))

        first = await enumerator.enumerate("openai-chatgpt-plan.home", config)
        cached = await enumerator.enumerate("openai-chatgpt-plan.home", config)
        owned = enumerator._catalog_for(_plan_config_for(profiles_path))
        owned.invalidate(PROFILE_ID)
        refreshed = await enumerator.enumerate("openai-chatgpt-plan.home", config)

        assert first.instance_pinned is True
        assert first.models == cached.models == refreshed.models
        assert len(capture.requests) == 2

    @pytest.mark.asyncio
    async def test_owned_enumerator_and_connector_share_catalog_object(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.catalog import ChatGPTPlanModelCatalog
        from src.connectors.openai_chatgpt_plan.enumerator import (
            ChatGPTPlanConfiguredModelEnumerator,
            build_chatgpt_plan_model_catalog,
        )
        from src.connectors.openai_chatgpt_plan.tokens import ChatGPTPlanTokenManager

        profiles_path = tmp_path / "profiles"
        plan_config = _plan_config_for(profiles_path)
        enumerator = ChatGPTPlanConfiguredModelEnumerator()
        owned = enumerator._catalog_for(plan_config)

        connector = _make_connector()
        await connector.initialize(
            chatgpt_plan={
                "profile_id": PROFILE_ID,
                "profiles_path": str(profiles_path),
            }
        )

        bound = connector._chatgpt_plan_model_catalog
        rebuilt = build_chatgpt_plan_model_catalog(plan_config)
        assert isinstance(owned, ChatGPTPlanModelCatalog)
        assert bound is owned
        assert rebuilt is owned
        assert id(bound) == id(owned)
        assert isinstance(owned._token_manager, ChatGPTPlanTokenManager)
        assert owned._token_manager is bound._token_manager

    @pytest.mark.asyncio
    async def test_connector_invalidation_refetches_owned_enumerator(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        from src.connectors.openai_chatgpt_plan.enumerator import (
            ChatGPTPlanConfiguredModelEnumerator,
        )

        capture = _install_owned_models_http(monkeypatch)
        profiles_path = await _persist_two_ready_profiles(tmp_path)
        enumerator = ChatGPTPlanConfiguredModelEnumerator()
        config = _backend_config(PROFILE_ID, profiles_path=str(profiles_path))

        first = await enumerator.enumerate("openai-chatgpt-plan.home", config)
        connector = _make_connector()
        await connector.initialize(
            chatgpt_plan={
                "profile_id": PROFILE_ID,
                "profiles_path": str(profiles_path),
            }
        )
        bound = connector._chatgpt_plan_model_catalog
        assert bound is enumerator._catalog_for(_plan_config_for(profiles_path))
        assert bound is not None
        bound.invalidate(PROFILE_ID)
        refreshed = await enumerator.enumerate("openai-chatgpt-plan.home", config)

        assert first.models == refreshed.models
        assert first.instance_pinned is True
        assert len(capture.requests) == 2

    def test_build_catalog_reuses_graph_for_same_resolved_profiles_path(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.enumerator import (
            build_chatgpt_plan_model_catalog,
        )

        path_a = tmp_path / "profiles-a"
        path_b = tmp_path / "profiles-b"
        cfg_a1 = _plan_config_for(path_a)
        cfg_a2 = _plan_config_for(path_a)
        cfg_b = _plan_config_for(path_b)

        first = build_chatgpt_plan_model_catalog(cfg_a1)
        second = build_chatgpt_plan_model_catalog(cfg_a2)
        other = build_chatgpt_plan_model_catalog(cfg_b)

        assert first is second
        assert first._token_manager is second._token_manager
        assert other is not first
        assert other._token_manager is not first._token_manager


class TestOpenAIChatGPTPlanConnectorCatalogBinding:
    @pytest.mark.asyncio
    async def test_initialize_lazy_imports_and_binds_selected_profile_catalog(
        self, tmp_path: Path
    ) -> None:
        catalog_module = f"{PACKAGE_NAME}.catalog"
        enumerator_module = f"{PACKAGE_NAME}.enumerator"
        sys.modules.pop(catalog_module, None)
        sys.modules.pop(enumerator_module, None)

        connector = _make_connector()
        assert catalog_module not in sys.modules

        await connector.initialize(
            chatgpt_plan={
                "profile_id": PROFILE_ID,
                "profiles_path": str(tmp_path / "profiles"),
            }
        )

        from src.connectors.openai_chatgpt_plan.catalog import ChatGPTPlanModelCatalog

        assert catalog_module in sys.modules
        assert connector._chatgpt_plan_profile_id == PROFILE_ID
        assert isinstance(
            connector._chatgpt_plan_model_catalog, ChatGPTPlanModelCatalog
        )
