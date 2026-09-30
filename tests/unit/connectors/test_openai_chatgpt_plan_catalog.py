"""Unit tests for ChatGPT-plan public /v1/models catalog (task 3.1).

HTTP is mocked. These tests must not contact live OpenAI services.
Token fixtures must not use the sk- prefix (pre-commit secret scan).
"""

from __future__ import annotations

import ast
import importlib
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any
from unittest.mock import patch

import httpx
import pytest
from src.connectors.base import add_vendor_prefix
from src.core.config.app_config import AppConfig
from src.core.services.translation_service import TranslationService

ACCESS_TOKEN = "catalog-access-token-test-value"
OTHER_ACCESS_TOKEN = "catalog-work-access-token-test-value"
ROTATED_ACCESS_TOKEN = "catalog-rotated-access-token-test-value"
ISSUED_CLIENT_ID = "oaiapp_test_issued_client"
OTHER_ISSUED_CLIENT_ID = "oaiapp_test_issued_client_work"
PROFILE_ID = "primary"
OTHER_PROFILE_ID = "work"
SUBJECT = "oidc-subject-primary"
OTHER_SUBJECT = "oidc-subject-work"
EMAIL = "chatgpt-plan-user@example.com"
DISPLAY_NAME = "ChatGPT Plan User"
ISSUER = "https://auth.openai.com"
SIWC_RESOURCE = "https://api.openai.com/v1"
OPENAI_MODELS_URL = "https://api.openai.com/v1/models"
CODEX_MODELS_URL = "https://chatgpt.com/backend-api/codex/models"
PLAN_USE_SCOPE = "chatgpt.tokens.use.direct"
FULL_SCOPES = (
    "openid",
    "profile",
    "email",
    "offline_access",
    "resource.invoke",
    PLAN_USE_SCOPE,
)
SECRET_VALUES = (ACCESS_TOKEN, OTHER_ACCESS_TOKEN, ROTATED_ACCESS_TOKEN)
PACKAGE_NAME = "src.connectors.openai_chatgpt_plan"
PACKAGE_DIR = (
    Path(__file__).resolve().parents[3] / "src" / "connectors" / "openai_chatgpt_plan"
)
CATALOG_PATH = PACKAGE_DIR / "catalog.py"
FORBIDDEN_MODULE_PREFIXES = (
    "src.connectors.openai_codex",
    "src.connectors.openai_codex_v2",
    "src.connectors._openai_codex_connector",
    "src.connectors._openai_codex_v2_connector",
    "src.connectors.openai_codex_app_server",
    "src.connectors.codex_event_mapper",
    "src.connectors.codex_helpers",
    "src.resources.codex",
)


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
        "refresh_token": "catalog-refresh-token-test-value",
        "id_token": "catalog-id-token-test-value",
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


def _models_payload(
    *model_ids: str,
    extra: list[dict[str, Any]] | None = None,
) -> dict[str, Any]:
    data: list[dict[str, Any]] = [
        {"id": model_id, "object": "model", "owned_by": "openai"}
        for model_id in model_ids
    ]
    if extra:
        data.extend(extra)
    return {"object": "list", "data": data}


class _FakeTokenManager:
    def __init__(self, tokens: dict[str, str] | None = None) -> None:
        self.tokens = tokens or {PROFILE_ID: ACCESS_TOKEN}
        self.calls: list[str] = []

    async def get_access_token(self, profile_id: str) -> str:
        self.calls.append(profile_id)
        return self.tokens[profile_id]

    async def force_refresh(self, profile_id: str) -> Any:
        raise AssertionError("model catalog must not call force_refresh")

    async def mark_needs_reauth(self, profile_id: str, reason: str) -> None:
        raise AssertionError("model catalog must not call mark_needs_reauth")


class _ModelsCapture:
    def __init__(
        self,
        payload: dict[str, Any] | None = None,
        *,
        status_code: int = 200,
        error: BaseException | None = None,
        payloads: list[dict[str, Any] | httpx.Response] | None = None,
    ) -> None:
        self.requests: list[httpx.Request] = []
        self._payload = payload if payload is not None else _models_payload("gpt-4o")
        self._status_code = status_code
        self._error = error
        self._queue = list(payloads or [])

    def handler(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        if self._error is not None:
            raise self._error
        if self._queue:
            item = self._queue.pop(0)
            if isinstance(item, httpx.Response):
                return item
            return httpx.Response(200, json=item)
        return httpx.Response(self._status_code, json=self._payload)

    @property
    def urls(self) -> list[str]:
        return [str(request.url) for request in self.requests]

    @property
    def models_gets(self) -> list[httpx.Request]:
        return [
            request
            for request in self.requests
            if request.method == "GET"
            and str(request.url).split("?", 1)[0] == OPENAI_MODELS_URL
        ]


async def _catalog(
    tmp_path: Path,
    capture: _ModelsCapture | None = None,
    *,
    tokens: dict[str, str] | None = None,
    ttl_seconds: int | None = None,
    config: Any = None,
) -> tuple[Any, Any, _FakeTokenManager, _ModelsCapture, httpx.AsyncClient]:
    from src.connectors.openai_chatgpt_plan.catalog import ChatGPTPlanModelCatalog
    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

    capture = capture or _ModelsCapture()
    store = ChatGPTPlanProfileStore(tmp_path / "profiles")
    token_manager = _FakeTokenManager(tokens)
    client = httpx.AsyncClient(transport=httpx.MockTransport(capture.handler))
    catalog = ChatGPTPlanModelCatalog(
        store,
        token_manager,
        http_client=client,
        ttl_seconds=ttl_seconds,
        config=config,
    )
    return catalog, store, token_manager, capture, client


def _imported_names_from_source(source: str) -> set[str]:
    tree = ast.parse(source)
    names: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            names.update(alias.name for alias in node.names)
        elif isinstance(node, ast.ImportFrom) and node.module:
            names.add(node.module)
    return names


def _is_forbidden_module_name(name: str) -> bool:
    normalized = name.strip()
    if not normalized:
        return False
    for prefix in FORBIDDEN_MODULE_PREFIXES:
        if normalized == prefix or normalized.startswith(f"{prefix}."):
            return True
    return False


def _unload_chatgpt_plan_modules() -> None:
    for key in list(sys.modules):
        if key == PACKAGE_NAME or key.startswith(f"{PACKAGE_NAME}."):
            sys.modules.pop(key, None)


class TestChatGPTPlanCatalogSourceBoundary:
    def test_catalog_module_exists_and_avoids_codex_imports_and_urls(self) -> None:
        assert CATALOG_PATH.is_file()
        source = CATALOG_PATH.read_text(encoding="utf-8")
        assert "sk-" not in source
        assert "chatgpt.com/backend-api/codex" not in source
        assert "src.resources.codex" not in source
        imported = _imported_names_from_source(source)
        forbidden = sorted(name for name in imported if _is_forbidden_module_name(name))
        assert forbidden == []

    def test_importing_catalog_module_does_not_perform_network(self) -> None:
        _unload_chatgpt_plan_modules()
        side_effects: list[str] = []

        def _record(name: str) -> Any:
            def _inner(*args: Any, **kwargs: Any) -> Any:
                side_effects.append(name)
                raise AssertionError(f"unexpected import-time call: {name}")

            return _inner

        with (
            patch.object(
                httpx.Client, "request", side_effect=_record("httpx.Client.request")
            ),
            patch.object(
                httpx.AsyncClient,
                "request",
                side_effect=_record("httpx.AsyncClient.request"),
            ),
        ):
            importlib.import_module(f"{PACKAGE_NAME}.catalog")

        assert side_effects == []


class TestProfileIdentityFingerprint:
    def test_fingerprint_uses_registration_identity_and_ignores_token_bytes(
        self,
    ) -> None:
        from src.connectors.openai_chatgpt_plan.catalog import (
            profile_identity_fingerprint,
        )

        left = _profile(access_token=ACCESS_TOKEN)
        right = _profile(access_token=ROTATED_ACCESS_TOKEN)
        fingerprint = profile_identity_fingerprint(left)
        assert fingerprint == profile_identity_fingerprint(right)
        assert ACCESS_TOKEN not in fingerprint
        assert ROTATED_ACCESS_TOKEN not in fingerprint
        assert ISSUED_CLIENT_ID not in fingerprint
        assert SUBJECT not in fingerprint
        other_subject = profile_identity_fingerprint(_profile(subject=OTHER_SUBJECT))
        other_client = profile_identity_fingerprint(
            _profile(issued_client_id=OTHER_ISSUED_CLIENT_ID)
        )
        assert fingerprint != other_subject
        assert fingerprint != other_client


class TestChatGPTPlanModelCatalogDiscovery:
    @pytest.mark.asyncio
    async def test_authenticates_ready_profile_and_gets_public_models(
        self, tmp_path: Path
    ) -> None:
        capture = _ModelsCapture(
            _models_payload("gpt-4o", "o3-mini", "gpt-4.1"),
        )
        catalog, store, token_manager, capture, client = await _catalog(
            tmp_path, capture
        )
        try:
            await store.save_atomic(_profile())
            models = await catalog.list_models(PROFILE_ID)
        finally:
            await client.aclose()

        assert models == ["gpt-4o", "o3-mini", "gpt-4.1"]
        assert token_manager.calls == [PROFILE_ID]
        assert len(capture.models_gets) == 1
        request = capture.models_gets[0]
        assert str(request.url) == OPENAI_MODELS_URL
        assert request.headers["authorization"] == f"Bearer {ACCESS_TOKEN}"
        assert CODEX_MODELS_URL not in capture.urls
        assert all("chatgpt.com" not in url for url in capture.urls)
        assert all("sk-" not in url for url in capture.urls)

    @pytest.mark.asyncio
    async def test_preserves_provider_slugs_order_and_list_visibility(
        self, tmp_path: Path
    ) -> None:
        payload = _models_payload(
            extra=[
                {
                    "id": "hidden-unlisted",
                    "object": "model",
                    "permission": [{"allow_view": False, "is_blocking": False}],
                },
                {"id": "gpt-4o", "object": "model", "owned_by": "openai"},
                {
                    "id": "blocked-model",
                    "object": "model",
                    "permission": [{"allow_view": True, "is_blocking": True}],
                },
                {"id": "o3-mini", "object": "model", "visible": False},
                {"id": "gpt-4.1", "object": "model", "listed": True},
                {"id": "not-a-model", "object": "fine-tune"},
                {"id": "   ", "object": "model"},
            ]
        )
        capture = _ModelsCapture(payload)
        catalog, store, token_manager, capture, client = await _catalog(
            tmp_path, capture
        )
        try:
            await store.save_atomic(_profile())
            models = await catalog.list_models(PROFILE_ID)
        finally:
            await client.aclose()

        assert models == ["gpt-4o", "gpt-4.1"]
        assert token_manager.calls == [PROFILE_ID]

    @pytest.mark.asyncio
    async def test_cache_hit_does_not_call_models_or_token_manager_again(
        self, tmp_path: Path
    ) -> None:
        capture = _ModelsCapture(_models_payload("gpt-4o", "o3-mini"))
        catalog, store, token_manager, capture, client = await _catalog(
            tmp_path, capture
        )
        try:
            await store.save_atomic(_profile())
            first = await catalog.list_models(PROFILE_ID)
            second = await catalog.list_models(PROFILE_ID)
        finally:
            await client.aclose()

        assert first == ["gpt-4o", "o3-mini"]
        assert second == first
        assert len(capture.models_gets) == 1
        assert token_manager.calls == [PROFILE_ID]
        entries = catalog._entries
        assert PROFILE_ID in entries
        assert ACCESS_TOKEN not in entries
        dumped = repr(entries)
        for secret in SECRET_VALUES:
            assert secret not in dumped

    @pytest.mark.asyncio
    async def test_ttl_zero_does_not_reuse_cached_catalog(self, tmp_path: Path) -> None:
        capture = _ModelsCapture(
            payloads=[
                _models_payload("gpt-4o"),
                _models_payload("o3-mini"),
            ]
        )
        catalog, store, token_manager, capture, client = await _catalog(
            tmp_path, capture, ttl_seconds=0
        )
        try:
            await store.save_atomic(_profile())
            first = await catalog.list_models(PROFILE_ID)
            second = await catalog.list_models(PROFILE_ID)
        finally:
            await client.aclose()

        assert first == ["gpt-4o"]
        assert second == ["o3-mini"]
        assert len(capture.models_gets) == 2
        assert token_manager.calls == [PROFILE_ID, PROFILE_ID]

    @pytest.mark.asyncio
    async def test_default_ttl_comes_from_chatgpt_plan_config(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.config import (
            DEFAULT_CHATGPT_PLAN_MODEL_CATALOG_TTL_SECONDS,
            ChatGPTPlanConfig,
        )

        catalog, store, _token_manager, _capture, client = await _catalog(tmp_path)
        try:
            await store.save_atomic(_profile())
            assert (
                catalog._ttl_seconds == DEFAULT_CHATGPT_PLAN_MODEL_CATALOG_TTL_SECONDS
            )
            configured = ChatGPTPlanConfig.model_validate(
                {"model_catalog": {"ttl_seconds": 60}}
            )
        finally:
            await client.aclose()

        catalog_with_config, store2, _tm, capture2, client2 = await _catalog(
            tmp_path / "cfg", config=configured
        )
        try:
            await store2.save_atomic(_profile())
            assert catalog_with_config._ttl_seconds == 60
            assert capture2.models_gets == []
        finally:
            await client2.aclose()

    @pytest.mark.asyncio
    async def test_force_refresh_invalidate_switch_reauth_and_delete_fetch_again(
        self, tmp_path: Path
    ) -> None:
        capture = _ModelsCapture(
            payloads=[
                _models_payload("gpt-4o"),
                _models_payload("gpt-4o", "o3-mini"),
                _models_payload("work-model"),
                _models_payload("gpt-4.1"),
                _models_payload("after-delete-should-not-happen"),
            ]
        )
        tokens = {PROFILE_ID: ACCESS_TOKEN, OTHER_PROFILE_ID: OTHER_ACCESS_TOKEN}
        catalog, store, token_manager, capture, client = await _catalog(
            tmp_path, capture, tokens=tokens
        )
        try:
            await store.save_atomic(_profile())
            await store.save_atomic(
                _profile(
                    profile_id=OTHER_PROFILE_ID,
                    issued_client_id=OTHER_ISSUED_CLIENT_ID,
                    subject=OTHER_SUBJECT,
                    access_token=OTHER_ACCESS_TOKEN,
                )
            )

            first = await catalog.list_models(PROFILE_ID)
            cached = await catalog.list_models(PROFILE_ID)
            assert first == ["gpt-4o"]
            assert cached == first
            assert len(capture.models_gets) == 1

            refreshed = await catalog.list_models(PROFILE_ID, force_refresh=True)
            assert refreshed == ["gpt-4o", "o3-mini"]
            assert len(capture.models_gets) == 2

            switched = await catalog.list_models(OTHER_PROFILE_ID)
            assert switched == ["work-model"]
            assert len(capture.models_gets) == 3
            assert (
                capture.models_gets[2].headers["authorization"]
                == f"Bearer {OTHER_ACCESS_TOKEN}"
            )
            still_primary = await catalog.list_models(PROFILE_ID)
            assert still_primary == ["gpt-4o", "o3-mini"]
            assert len(capture.models_gets) == 3

            await store.save_atomic(_profile(access_token=ROTATED_ACCESS_TOKEN))
            token_manager.tokens[PROFILE_ID] = ROTATED_ACCESS_TOKEN
            after_token_rotation = await catalog.list_models(PROFILE_ID)
            assert after_token_rotation == ["gpt-4o", "o3-mini"]
            assert len(capture.models_gets) == 3

            catalog.invalidate(PROFILE_ID)
            after_reauth = await catalog.list_models(PROFILE_ID)
            assert after_reauth == ["gpt-4.1"]
            assert len(capture.models_gets) == 4
            assert (
                capture.models_gets[3].headers["authorization"]
                == f"Bearer {ROTATED_ACCESS_TOKEN}"
            )

            await store.delete(PROFILE_ID)
            catalog.invalidate(PROFILE_ID)
            from src.connectors.openai_chatgpt_plan.catalog import (
                ChatGPTPlanCatalogError,
            )

            with pytest.raises(ChatGPTPlanCatalogError) as exc_info:
                await catalog.list_models(PROFILE_ID)
            assert exc_info.value.status_code == 404
            assert len(capture.models_gets) == 4
        finally:
            await client.aclose()

        assert CODEX_MODELS_URL not in capture.urls
        assert token_manager.calls == [
            PROFILE_ID,
            PROFILE_ID,
            OTHER_PROFILE_ID,
            PROFILE_ID,
        ]

    @pytest.mark.asyncio
    async def test_deleted_profile_does_not_return_stale_cache_without_invalidate(
        self, tmp_path: Path
    ) -> None:
        capture = _ModelsCapture(_models_payload("gpt-4o"))
        catalog, store, token_manager, capture, client = await _catalog(
            tmp_path, capture
        )
        try:
            await store.save_atomic(_profile())
            assert await catalog.list_models(PROFILE_ID) == ["gpt-4o"]
            await store.delete(PROFILE_ID)
            from src.connectors.openai_chatgpt_plan.catalog import (
                ChatGPTPlanCatalogError,
            )

            with pytest.raises(ChatGPTPlanCatalogError):
                await catalog.list_models(PROFILE_ID)
        finally:
            await client.aclose()

        assert len(capture.models_gets) == 1
        assert token_manager.calls == [PROFILE_ID]

    @pytest.mark.asyncio
    async def test_identity_change_without_invalidate_refetches(
        self, tmp_path: Path
    ) -> None:
        capture = _ModelsCapture(
            payloads=[_models_payload("gpt-4o"), _models_payload("o3-mini")]
        )
        catalog, store, token_manager, capture, client = await _catalog(
            tmp_path, capture
        )
        try:
            await store.save_atomic(_profile())
            assert await catalog.list_models(PROFILE_ID) == ["gpt-4o"]
            await store.save_atomic(_profile(subject=OTHER_SUBJECT))
            assert await catalog.list_models(PROFILE_ID) == ["o3-mini"]
        finally:
            await client.aclose()

        assert len(capture.models_gets) == 2
        assert token_manager.calls == [PROFILE_ID, PROFILE_ID]


class TestChatGPTPlanCatalogReadyProfileAndFailures:
    @pytest.mark.asyncio
    async def test_missing_plan_scope_refuses_without_calling_models(
        self, tmp_path: Path
    ) -> None:
        capture = _ModelsCapture(_models_payload("gpt-4o"))
        catalog, store, token_manager, capture, client = await _catalog(
            tmp_path, capture
        )
        try:
            await store.save_atomic(
                _profile(
                    status="missing_plan_scope",
                    granted_scopes=("openid", "profile", "email", "offline_access"),
                )
            )
            from src.connectors.openai_chatgpt_plan.catalog import (
                ChatGPTPlanCatalogError,
            )

            with pytest.raises(ChatGPTPlanCatalogError) as exc_info:
                await catalog.list_models(PROFILE_ID)
            assert exc_info.value.status_code in {401, 403}
            assert "missing_plan_scope" in str(exc_info.value.details)
        finally:
            await client.aclose()

        assert capture.requests == []
        assert token_manager.calls == []

    @pytest.mark.asyncio
    async def test_needs_reauth_refuses_without_calling_models(
        self, tmp_path: Path
    ) -> None:
        capture = _ModelsCapture(_models_payload("gpt-4o"))
        catalog, store, token_manager, capture, client = await _catalog(
            tmp_path, capture
        )
        try:
            await store.save_atomic(_profile(status="needs_reauth"))
            from src.connectors.openai_chatgpt_plan.catalog import (
                ChatGPTPlanCatalogError,
            )

            with pytest.raises(ChatGPTPlanCatalogError):
                await catalog.list_models(PROFILE_ID)
        finally:
            await client.aclose()

        assert capture.requests == []
        assert token_manager.calls == []

    @pytest.mark.asyncio
    async def test_transient_http_failure_is_unavailable_without_codex_fallback(
        self, tmp_path: Path
    ) -> None:
        capture = _ModelsCapture(
            status_code=503, payload={"error": {"message": "busy"}}
        )
        catalog, store, token_manager, capture, client = await _catalog(
            tmp_path, capture
        )
        try:
            await store.save_atomic(_profile())
            from src.connectors.openai_chatgpt_plan.catalog import (
                ChatGPTPlanCatalogError,
            )

            with pytest.raises(ChatGPTPlanCatalogError) as exc_info:
                await catalog.list_models(PROFILE_ID)
            assert exc_info.value.status_code == 503
        finally:
            await client.aclose()

        assert token_manager.calls == [PROFILE_ID]
        assert [str(request.url) for request in capture.models_gets] == [
            OPENAI_MODELS_URL
        ]
        assert CODEX_MODELS_URL not in capture.urls
        assert all("chatgpt.com" not in url for url in capture.urls)

    @pytest.mark.asyncio
    async def test_transient_network_failure_is_unavailable_without_codex_fallback(
        self, tmp_path: Path
    ) -> None:
        capture = _ModelsCapture(error=httpx.ConnectError("temporary dns failure"))
        catalog, store, token_manager, capture, client = await _catalog(
            tmp_path, capture
        )
        try:
            await store.save_atomic(_profile())
            from src.connectors.openai_chatgpt_plan.catalog import (
                ChatGPTPlanCatalogError,
            )

            with pytest.raises(ChatGPTPlanCatalogError) as exc_info:
                await catalog.list_models(PROFILE_ID)
            assert exc_info.value.status_code == 503
        finally:
            await client.aclose()

        assert token_manager.calls == [PROFILE_ID]
        assert CODEX_MODELS_URL not in capture.urls

    @pytest.mark.asyncio
    async def test_does_not_use_token_bytes_as_cache_key(self, tmp_path: Path) -> None:
        from src.connectors.openai_chatgpt_plan.catalog import (
            profile_identity_fingerprint,
        )

        capture = _ModelsCapture(_models_payload("gpt-4o"))
        catalog, store, token_manager, capture, client = await _catalog(
            tmp_path, capture
        )
        try:
            profile = _profile()
            await store.save_atomic(profile)
            await catalog.list_models(PROFILE_ID)
            entry = catalog._entries[PROFILE_ID]
            assert entry.profile_identity_fingerprint == profile_identity_fingerprint(
                profile
            )
            assert ACCESS_TOKEN not in entry.profile_identity_fingerprint
            assert ACCESS_TOKEN not in catalog._entries
        finally:
            await client.aclose()

        assert token_manager.calls == [PROFILE_ID]


class TestChatGPTPlanConnectorCatalogHook:
    @pytest.mark.asyncio
    async def test_get_available_models_async_uses_catalog_and_vendor_prefix(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector

        capture = _ModelsCapture(_models_payload("gpt-4o", "o3-mini"))
        catalog, store, _token_manager, capture, client = await _catalog(
            tmp_path, capture
        )
        connector = OpenAIChatGPTPlanConnector(
            client=client,
            config=AppConfig(),
            translation_service=TranslationService(),
        )
        connector.disable_health_check()
        try:
            await store.save_atomic(_profile())
            connector.bind_chatgpt_plan_model_catalog(catalog, PROFILE_ID)
            models = await connector.get_available_models_async()
            assert models == [
                add_vendor_prefix("gpt-4o", "openai"),
                add_vendor_prefix("o3-mini", "openai"),
            ]
            assert connector.get_available_models() == models
            assert len(capture.models_gets) == 1
            cached = await connector.get_available_models_async()
            assert cached == models
            assert len(capture.models_gets) == 1
        finally:
            await client.aclose()

        assert CODEX_MODELS_URL not in capture.urls
        assert "CodexModelCatalogStage" not in CATALOG_PATH.read_text(encoding="utf-8")
