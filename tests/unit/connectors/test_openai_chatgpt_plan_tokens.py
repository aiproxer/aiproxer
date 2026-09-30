"""Unit tests for ChatGPT-plan rotating refresh (task 2.4).

HTTP is mocked. These tests must not contact live OpenAI services.
Token fixtures must not use the sk- prefix (pre-commit secret scan).
"""

from __future__ import annotations

import ast
import asyncio
import importlib
import logging
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any
from unittest.mock import patch
from urllib.parse import parse_qs

import httpx
import pytest

ACCESS_TOKEN = "old-access-token-test-value"
REFRESH_TOKEN = "old-refresh-token-test-value"
ID_TOKEN = "old-id-token-test-value"
ROTATED_ACCESS_TOKEN = "rotated-access-token-test-value"
ROTATED_REFRESH_TOKEN = "rotated-refresh-token-test-value"
ROTATED_ID_TOKEN = "rotated-id-token-test-value"
ISSUED_CLIENT_ID = "oaiapp_test_issued_client"
PROFILE_ID = "primary"
OTHER_PROFILE_ID = "work"
SUBJECT = "oidc-subject-primary"
EMAIL = "chatgpt-plan-user@example.com"
DISPLAY_NAME = "ChatGPT Plan User"
ISSUER = "https://auth.openai.com"
SIWC_TOKEN_URL = "https://auth.openai.com/api/accounts/oauth/token"
SIWC_RESOURCE = "https://api.openai.com/v1"
PLAN_USE_SCOPE = "chatgpt.tokens.use.direct"
FULL_SCOPES = (
    "openid",
    "profile",
    "email",
    "offline_access",
    "resource.invoke",
    PLAN_USE_SCOPE,
)
SECRET_VALUES = (
    ACCESS_TOKEN,
    REFRESH_TOKEN,
    ID_TOKEN,
    ROTATED_ACCESS_TOKEN,
    ROTATED_REFRESH_TOKEN,
    ROTATED_ID_TOKEN,
)
PACKAGE_DIR = (
    Path(__file__).resolve().parents[3] / "src" / "connectors" / "openai_chatgpt_plan"
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
        "refresh_token": REFRESH_TOKEN,
        "id_token": ID_TOKEN,
        "granted_scopes": FULL_SCOPES,
        "resource": SIWC_RESOURCE,
        "access_token_expires_at": now - timedelta(seconds=30),
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


def _assert_no_secrets(text: str, extra: tuple[str, ...] = ()) -> None:
    for secret in SECRET_VALUES + extra:
        assert secret not in text


def _joined_log_text(caplog: pytest.LogCaptureFixture) -> str:
    return "\n".join(record.getMessage() for record in caplog.records)


def _rotated_payload() -> dict[str, Any]:
    return {
        "access_token": ROTATED_ACCESS_TOKEN,
        "refresh_token": ROTATED_REFRESH_TOKEN,
        "id_token": ROTATED_ID_TOKEN,
        "token_type": "Bearer",
        "expires_in": 3600,
        "scope": " ".join(FULL_SCOPES),
    }


class _RefreshCapture:
    def __init__(
        self,
        response: httpx.Response | None = None,
        *,
        responses: list[httpx.Response] | None = None,
    ) -> None:
        self.requests: list[httpx.Request] = []
        self._explicit = response
        self._queue = list(responses or [])

    def handler(self, request: httpx.Request) -> httpx.Response:
        url = str(request.url).split("?", 1)[0]
        if request.method == "POST" and url == SIWC_TOKEN_URL:
            self.requests.append(request)
            if self._queue:
                return self._queue.pop(0)
            if self._explicit is not None:
                return self._explicit
            return httpx.Response(200, json=_rotated_payload())
        return httpx.Response(404, json={"error": "not_found"})

    @property
    def form(self) -> dict[str, list[str]]:
        assert self.requests, "token endpoint was not called"
        return parse_qs(
            self.requests[0].content.decode("ascii"), keep_blank_values=True
        )


async def _manager(
    tmp_path: Path,
    capture: _RefreshCapture | None = None,
) -> tuple[Any, Any, _RefreshCapture, httpx.AsyncClient]:
    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore
    from src.connectors.openai_chatgpt_plan.tokens import ChatGPTPlanTokenManager

    capture = capture or _RefreshCapture()
    store = ChatGPTPlanProfileStore(tmp_path / "profiles")
    client = httpx.AsyncClient(transport=httpx.MockTransport(capture.handler))
    manager = ChatGPTPlanTokenManager(store, http_client=client)
    return manager, store, capture, client


class TestChatGPTPlanTokenRefreshSuccess:
    @pytest.mark.asyncio
    async def test_refresh_posts_issued_client_current_refresh_and_resource(
        self, tmp_path: Path
    ) -> None:
        manager, store, capture, client = await _manager(tmp_path)
        try:
            await store.save_atomic(_profile())
            refreshed = await manager.force_refresh(PROFILE_ID)
        finally:
            await client.aclose()

        form = capture.form
        assert str(capture.requests[0].url) == SIWC_TOKEN_URL
        assert form["grant_type"] == ["refresh_token"]
        assert form["client_id"] == [ISSUED_CLIENT_ID]
        assert form["client_id"] != ["dynamic_agent_client"]
        assert form["refresh_token"] == [REFRESH_TOKEN]
        assert form["resource"] == [SIWC_RESOURCE]
        assert "client_secret" not in form
        assert refreshed.access_token == ROTATED_ACCESS_TOKEN
        assert refreshed.refresh_token == ROTATED_REFRESH_TOKEN
        assert refreshed.id_token == ROTATED_ID_TOKEN

    @pytest.mark.asyncio
    async def test_refresh_atomically_persists_rotated_token_set(
        self, tmp_path: Path
    ) -> None:
        manager, store, capture, client = await _manager(tmp_path)
        try:
            existing = _profile()
            await store.save_atomic(existing)
            refreshed = await manager.force_refresh(PROFILE_ID)
            loaded = await store.load(PROFILE_ID)
        finally:
            await client.aclose()

        assert loaded is not None
        assert loaded.access_token == ROTATED_ACCESS_TOKEN
        assert loaded.refresh_token == ROTATED_REFRESH_TOKEN
        assert loaded.id_token == ROTATED_ID_TOKEN
        assert loaded.issued_client_id == ISSUED_CLIENT_ID
        assert loaded.subject == SUBJECT
        assert loaded.issuer == ISSUER
        assert loaded.status == "ready"
        assert loaded.created_at == existing.created_at
        assert loaded.access_token_expires_at is not None
        assert loaded.access_token_expires_at > _now()
        assert loaded.refresh_token != REFRESH_TOKEN
        assert loaded.access_token != ACCESS_TOKEN
        assert refreshed.access_token == loaded.access_token
        on_disk = (tmp_path / "profiles" / f"{PROFILE_ID}.json").read_text(
            encoding="utf-8"
        )
        assert ROTATED_ACCESS_TOKEN in on_disk
        assert ROTATED_REFRESH_TOKEN in on_disk
        assert ROTATED_ID_TOKEN in on_disk
        assert ACCESS_TOKEN not in on_disk
        assert REFRESH_TOKEN not in on_disk
        assert len(capture.requests) == 1

    @pytest.mark.asyncio
    async def test_get_access_token_returns_cached_token_when_not_near_expiry(
        self, tmp_path: Path
    ) -> None:
        manager, store, capture, client = await _manager(tmp_path)
        try:
            await store.save_atomic(
                _profile(access_token_expires_at=_now() + timedelta(hours=1))
            )
            token = await manager.get_access_token(PROFILE_ID)
        finally:
            await client.aclose()
        assert token == ACCESS_TOKEN
        assert capture.requests == []

    @pytest.mark.asyncio
    async def test_get_access_token_refreshes_when_access_token_near_expiry(
        self, tmp_path: Path
    ) -> None:
        manager, store, capture, client = await _manager(tmp_path)
        try:
            await store.save_atomic(
                _profile(access_token_expires_at=_now() + timedelta(seconds=15))
            )
            token = await manager.get_access_token(PROFILE_ID)
            loaded = await store.load(PROFILE_ID)
        finally:
            await client.aclose()
        assert token == ROTATED_ACCESS_TOKEN
        assert loaded is not None
        assert loaded.refresh_token == ROTATED_REFRESH_TOKEN
        assert len(capture.requests) == 1


class TestChatGPTPlanTokenRefreshConcurrency:
    @pytest.mark.asyncio
    async def test_concurrent_refresh_posts_token_endpoint_once(
        self, tmp_path: Path
    ) -> None:
        manager, store, capture, client = await _manager(tmp_path)
        try:
            await store.save_atomic(_profile())
            first, second = await asyncio.gather(
                manager.get_access_token(PROFILE_ID),
                manager.get_access_token(PROFILE_ID),
            )
            loaded = await store.load(PROFILE_ID)
        finally:
            await client.aclose()

        assert first == ROTATED_ACCESS_TOKEN
        assert second == ROTATED_ACCESS_TOKEN
        assert len(capture.requests) == 1
        assert loaded is not None
        assert loaded.refresh_token == ROTATED_REFRESH_TOKEN
        assert loaded.access_token == ROTATED_ACCESS_TOKEN

    @pytest.mark.asyncio
    async def test_distinct_profiles_refresh_independently(
        self, tmp_path: Path
    ) -> None:
        manager, store, capture, client = await _manager(tmp_path)
        try:
            await store.save_atomic(_profile())
            await store.save_atomic(
                _profile(
                    profile_id=OTHER_PROFILE_ID,
                    subject="oidc-subject-work",
                    refresh_token="work-refresh-token-test-value",
                )
            )
            await asyncio.gather(
                manager.get_access_token(PROFILE_ID),
                manager.get_access_token(OTHER_PROFILE_ID),
            )
        finally:
            await client.aclose()
        assert len(capture.requests) == 2


class TestChatGPTPlanTerminalRefresh:
    @pytest.mark.parametrize(
        "error_code",
        [
            "invalid_grant",
            "invalid_refresh_token",
            "token_expired",
            "refresh_token_expired",
            "refresh_token_invalidated",
            "refresh_token_reused",
            "disconnected",
        ],
    )
    @pytest.mark.asyncio
    async def test_terminal_refresh_marks_needs_reauth_without_retry_loop(
        self, tmp_path: Path, error_code: str
    ) -> None:
        capture = _RefreshCapture(
            httpx.Response(
                400,
                json={
                    "error": error_code,
                    "access_token": ROTATED_ACCESS_TOKEN,
                    "refresh_token": ROTATED_REFRESH_TOKEN,
                    "id_token": ROTATED_ID_TOKEN,
                },
            )
        )
        manager, store, capture, client = await _manager(tmp_path, capture)
        from src.connectors.openai_chatgpt_plan.tokens import ChatGPTPlanTokenError

        try:
            await store.save_atomic(_profile())
            with pytest.raises(ChatGPTPlanTokenError) as first:
                await manager.get_access_token(PROFILE_ID)
            loaded = await store.load(PROFILE_ID)
            assert loaded is not None
            assert loaded.status == "needs_reauth"
            assert loaded.issued_client_id == ISSUED_CLIENT_ID
            assert loaded.subject == SUBJECT
            assert loaded.issuer == ISSUER
            first_posts = len(capture.requests)
            assert first_posts == 1
            with pytest.raises(ChatGPTPlanTokenError) as second:
                await manager.get_access_token(PROFILE_ID)
            with pytest.raises(ChatGPTPlanTokenError):
                await manager.force_refresh(PROFILE_ID)
            assert len(capture.requests) == first_posts
            _assert_no_secrets(str(first.value))
            _assert_no_secrets(str(second.value))
            _assert_no_secrets(repr(first.value))
            if getattr(first.value, "details", None):
                _assert_no_secrets(str(first.value.details))
        finally:
            await client.aclose()

    @pytest.mark.asyncio
    async def test_transient_refresh_failure_leaves_tokens_and_ready_status(
        self, tmp_path: Path
    ) -> None:
        capture = _RefreshCapture(
            httpx.Response(503, json={"error": "temporarily_unavailable"})
        )
        manager, store, capture, client = await _manager(tmp_path, capture)
        from src.connectors.openai_chatgpt_plan.tokens import ChatGPTPlanTokenError

        try:
            existing = _profile()
            await store.save_atomic(existing)
            with pytest.raises(ChatGPTPlanTokenError):
                await manager.get_access_token(PROFILE_ID)
            loaded = await store.load(PROFILE_ID)
        finally:
            await client.aclose()
        assert loaded is not None
        assert loaded.status == "ready"
        assert loaded.access_token == ACCESS_TOKEN
        assert loaded.refresh_token == REFRESH_TOKEN
        assert loaded.id_token == ID_TOKEN

    @pytest.mark.asyncio
    async def test_mark_needs_reauth_prevents_refresh_post(
        self, tmp_path: Path
    ) -> None:
        manager, store, capture, client = await _manager(tmp_path)
        from src.connectors.openai_chatgpt_plan.tokens import ChatGPTPlanTokenError

        try:
            await store.save_atomic(_profile())
            await manager.mark_needs_reauth(PROFILE_ID, "operator requested reauth")
            loaded = await store.load(PROFILE_ID)
            assert loaded is not None
            assert loaded.status == "needs_reauth"
            with pytest.raises(ChatGPTPlanTokenError):
                await manager.get_access_token(PROFILE_ID)
            assert capture.requests == []
        finally:
            await client.aclose()


class TestChatGPTPlanTokenRedaction:
    @pytest.mark.asyncio
    async def test_tokens_never_appear_in_logs_or_exceptions(
        self, tmp_path: Path, caplog: pytest.LogCaptureFixture
    ) -> None:
        from src.connectors.openai_chatgpt_plan.tokens import ChatGPTPlanTokenError

        with caplog.at_level(logging.DEBUG):
            manager, store, capture, client = await _manager(tmp_path)
            try:
                await store.save_atomic(_profile())
                refreshed = await manager.force_refresh(PROFILE_ID)
                _assert_no_secrets(_joined_log_text(caplog))
                _assert_no_secrets(repr(refreshed))
                _assert_no_secrets(str(refreshed))

                error_capture = _RefreshCapture(
                    httpx.Response(
                        400,
                        json={
                            "error": "invalid_grant",
                            "access_token": ROTATED_ACCESS_TOKEN,
                            "refresh_token": ROTATED_REFRESH_TOKEN,
                            "id_token": ROTATED_ID_TOKEN,
                            "error_description": (
                                f"token {ROTATED_REFRESH_TOKEN} reused"
                            ),
                        },
                    )
                )
                err_manager, err_store, _, err_client = await _manager(
                    tmp_path, error_capture
                )
                try:
                    await err_store.save_atomic(
                        _profile(profile_id="other", subject="oidc-subject-other")
                    )
                    with pytest.raises(ChatGPTPlanTokenError) as exc_info:
                        await err_manager.force_refresh("other")
                    _assert_no_secrets(str(exc_info.value))
                    _assert_no_secrets(repr(exc_info.value))
                    if getattr(exc_info.value, "details", None):
                        _assert_no_secrets(str(exc_info.value.details))
                finally:
                    await err_client.aclose()
                _assert_no_secrets(_joined_log_text(caplog))
            finally:
                await client.aclose()


class TestChatGPTPlanTokenImportSafety:
    def test_importing_tokens_module_does_not_open_browser_or_network(self) -> None:
        module_name = "src.connectors.openai_chatgpt_plan.tokens"
        sys.modules.pop(module_name, None)
        side_effects: list[str] = []

        def _record(name: str) -> Any:
            def _inner(*args: Any, **kwargs: Any) -> Any:
                side_effects.append(name)
                raise AssertionError(f"unexpected import-time call: {name}")

            return _inner

        with (
            patch("webbrowser.open", side_effect=_record("webbrowser.open")),
            patch.object(
                httpx.Client, "request", side_effect=_record("httpx.Client.request")
            ),
            patch.object(
                httpx.AsyncClient,
                "request",
                side_effect=_record("httpx.AsyncClient.request"),
            ),
        ):
            importlib.import_module(module_name)
        assert side_effects == []

    def test_package_init_does_not_import_tokens_oauth_or_oidc(self) -> None:
        init_path = PACKAGE_DIR / "__init__.py"
        tree = ast.parse(init_path.read_text(encoding="utf-8"))
        imported: set[str] = set()
        for node in ast.walk(tree):
            if isinstance(node, ast.Import):
                imported.update(alias.name for alias in node.names)
            elif isinstance(node, ast.ImportFrom):
                module = node.module or ""
                imported.add(module)
                for alias in node.names:
                    imported.add(alias.name)
                    if module:
                        imported.add(f"{module}.{alias.name}")
        forbidden = {
            "tokens",
            "oauth",
            "oidc",
            ".tokens",
            ".oauth",
            ".oidc",
            "src.connectors.openai_chatgpt_plan.tokens",
            "src.connectors.openai_chatgpt_plan.oauth",
            "src.connectors.openai_chatgpt_plan.oidc",
        }
        assert imported.isdisjoint(forbidden)
