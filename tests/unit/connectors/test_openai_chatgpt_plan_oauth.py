"""Unit tests for ChatGPT-plan first-time SIWC registration (task 2.2).

HTTP is mocked. These tests must not contact live OpenAI services.
Token fixtures must not use the sk- prefix (pre-commit secret scan).
"""

from __future__ import annotations

import asyncio
import base64
import contextlib
import hashlib
import logging
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from unittest.mock import patch
from urllib.parse import parse_qs, urlparse

import httpx
import pytest

from tests.unit.connectors.test_openai_chatgpt_plan_oidc import (
    SIWC_DISCOVERY_URL,
    SIWC_JWKS_URL,
    _discovery_document,
    _encode_id_token,
    make_rsa_oidc,
)

ACCESS_TOKEN = "access-token-test-value"
REFRESH_TOKEN = "refresh-token-test-value"
ID_TOKEN = "id-token-test-value"
AUTH_CODE = "auth-code-test-value"
ISSUED_CLIENT_ID = "oaiapp_test_issued_client"
HOST_ID = "testhostid00000001"
PROFILE_ID = "primary"
SIWC_AUTHORIZE_PATH = "https://auth.openai.com/api/accounts/authorize"
SIWC_TOKEN_URL = "https://auth.openai.com/api/accounts/oauth/token"
SIWC_RESOURCE = "https://api.openai.com/v1"
REQUIRED_SCOPES = (
    "openid",
    "profile",
    "email",
    "offline_access",
    "resource.invoke",
    "chatgpt.tokens.use.direct",
)
SECRET_VALUES = (ACCESS_TOKEN, REFRESH_TOKEN, ID_TOKEN, AUTH_CODE)


def _service(tmp_path: Path, http_client: httpx.AsyncClient | None = None) -> Any:
    from src.connectors.openai_chatgpt_plan.oauth import ChatGPTPlanOAuthService
    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

    return ChatGPTPlanOAuthService(
        ChatGPTPlanProfileStore(tmp_path / "profiles"),
        http_client=http_client,
    )


def _parse_query(url: str) -> dict[str, list[str]]:
    return parse_qs(urlparse(url).query, keep_blank_values=True)


def _s256_challenge(verifier: str) -> str:
    digest = hashlib.sha256(verifier.encode("ascii")).digest()
    return base64.urlsafe_b64encode(digest).decode("ascii").rstrip("=")


def _token_payload() -> dict[str, Any]:
    return {
        "access_token": ACCESS_TOKEN,
        "refresh_token": REFRESH_TOKEN,
        "id_token": ID_TOKEN,
        "token_type": "Bearer",
        "expires_in": 3600,
        "scope": " ".join(REQUIRED_SCOPES),
    }


def _joined_log_text(caplog: pytest.LogCaptureFixture) -> str:
    return "\n".join(record.getMessage() for record in caplog.records)


def _assert_no_secrets(text: str) -> None:
    for secret in SECRET_VALUES:
        assert secret not in text


def _ready_profile(**overrides: Any) -> Any:
    from src.connectors.openai_chatgpt_plan.models import ChatGPTPlanProfile

    now = datetime.now(timezone.utc)
    payload: dict[str, Any] = {
        "profile_id": PROFILE_ID,
        "issued_client_id": ISSUED_CLIENT_ID,
        "issuer": "https://auth.openai.com",
        "subject": "oidc-subject-primary",
        "email": "chatgpt-plan-user@example.com",
        "display_name": "ChatGPT Plan User",
        "access_token": ACCESS_TOKEN,
        "refresh_token": REFRESH_TOKEN,
        "id_token": ID_TOKEN,
        "granted_scopes": REQUIRED_SCOPES,
        "resource": SIWC_RESOURCE,
        "access_token_expires_at": now,
        "status": "ready",
        "created_at": now,
        "updated_at": now,
    }
    payload.update(overrides)
    return ChatGPTPlanProfile(**payload)


class TestChatGPTPlanAuthorizationUrl:
    def test_two_attempts_use_fresh_state_nonce_and_pkce(self, tmp_path: Path) -> None:
        service = _service(tmp_path)
        first = service.create_new_registration_attempt(
            host_id=HOST_ID, callback_port=1455
        )
        second = service.create_new_registration_attempt(
            host_id=HOST_ID, callback_port=1455
        )

        assert first.authorize_url != second.authorize_url
        assert first.state != second.state
        assert first.nonce != second.nonce
        assert first.code_verifier != second.code_verifier
        assert first.code_challenge != second.code_challenge

        first_query = _parse_query(first.authorize_url)
        second_query = _parse_query(second.authorize_url)
        assert first_query["state"] != second_query["state"]
        assert first_query["nonce"] != second_query["nonce"]
        assert first_query["code_challenge"] != second_query["code_challenge"]

    def test_pkce_challenge_is_s256_of_verifier(self, tmp_path: Path) -> None:
        service = _service(tmp_path)
        attempt = service.create_new_registration_attempt(
            host_id=HOST_ID, callback_port=1455
        )
        expected = _s256_challenge(attempt.code_verifier)
        assert attempt.code_challenge == expected
        query = _parse_query(attempt.authorize_url)
        assert query["code_challenge"] == [expected]
        assert query["code_challenge_method"] == ["S256"]

    def test_redirect_uri_is_loopback_auth_callback_not_localhost(
        self, tmp_path: Path
    ) -> None:
        service = _service(tmp_path)
        attempt = service.create_new_registration_attempt(
            host_id=HOST_ID, callback_port=1455
        )
        expected = "http://127.0.0.1:1455/auth/callback"
        assert attempt.redirect_uri == expected
        query = _parse_query(attempt.authorize_url)
        assert query["redirect_uri"] == [expected]
        assert "localhost" not in attempt.redirect_uri
        assert "localhost" not in query["redirect_uri"][0]

    def test_authorize_query_includes_dynamic_registration_fields(
        self, tmp_path: Path
    ) -> None:
        service = _service(tmp_path)
        attempt = service.create_new_registration_attempt(
            host_id=HOST_ID, callback_port=1455
        )
        parsed = urlparse(attempt.authorize_url)
        assert f"{parsed.scheme}://{parsed.netloc}{parsed.path}" == SIWC_AUTHORIZE_PATH
        query = parse_qs(parsed.query)
        assert query["client_id"] == ["dynamic_agent_client"]
        assert query["agent_name_hint"] == ["AIProxer"]
        assert query["ext_agent_host_id"] == [HOST_ID]
        assert query["resource"] == [SIWC_RESOURCE]
        assert query["response_type"] == ["code"]
        assert query["scope"] == [" ".join(REQUIRED_SCOPES)]
        for scope in REQUIRED_SCOPES:
            assert scope in query["scope"][0].split()


_SHARED_RSA_OIDC = None


def _shared_rsa_oidc() -> Any:
    global _SHARED_RSA_OIDC
    if _SHARED_RSA_OIDC is None:
        _SHARED_RSA_OIDC = make_rsa_oidc()
    return _SHARED_RSA_OIDC


class _TokenExchangeCapture:
    def __init__(
        self,
        response: httpx.Response | None = None,
        *,
        rsa_oidc: Any | None = None,
    ) -> None:
        self.requests: list[httpx.Request] = []
        self._explicit_response = response
        self._rsa_oidc = rsa_oidc if rsa_oidc is not None else _shared_rsa_oidc()
        self.nonce: str | None = None

    def handler(self, request: httpx.Request) -> httpx.Response:
        url = str(request.url).split("?", 1)[0]
        if request.method == "GET" and url == SIWC_DISCOVERY_URL:
            return httpx.Response(200, json=_discovery_document())
        if request.method == "GET" and url == SIWC_JWKS_URL:
            return httpx.Response(200, json=self._rsa_oidc.jwks)
        self.requests.append(request)
        if self._explicit_response is not None:
            return self._explicit_response
        assert self.nonce is not None, "nonce must be captured before token POST"
        payload = _token_payload()
        payload["id_token"] = _encode_id_token(
            self._rsa_oidc,
            nonce=self.nonce,
            aud=ISSUED_CLIENT_ID,
        )
        return httpx.Response(200, json=payload)

    @property
    def form(self) -> dict[str, list[str]]:
        assert self.requests, "token endpoint was not called"
        return parse_qs(
            self.requests[0].content.decode("ascii"), keep_blank_values=True
        )


async def _wait_for(predicate: Any, timeout: float = 5.0) -> None:
    deadline = asyncio.get_running_loop().time() + timeout
    while asyncio.get_running_loop().time() < deadline:
        if predicate():
            return
        await asyncio.sleep(0.02)
    raise AssertionError("timed out waiting for authorization URL")


async def _drive_authorize_new(
    tmp_path: Path,
    *,
    callback_query: dict[str, str],
    token_response: httpx.Response | None = None,
    open_browser: bool = True,
    capture: _TokenExchangeCapture | None = None,
) -> Any:
    from src.connectors.openai_chatgpt_plan.oauth import ChatGPTPlanOAuthService
    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

    capture = capture or _TokenExchangeCapture(token_response)
    transport = httpx.MockTransport(capture.handler)
    http_client = httpx.AsyncClient(transport=transport)
    service = ChatGPTPlanOAuthService(
        ChatGPTPlanProfileStore(tmp_path / "profiles"),
        http_client=http_client,
        timeout_seconds=8,
    )
    authorize_urls: list[str] = []
    bind_hosts: list[str] = []

    import uvicorn

    original_config = uvicorn.Config

    class _SpyConfig(original_config):  # type: ignore[valid-type,misc]
        def __init__(self, *args: Any, **kwargs: Any) -> None:
            super().__init__(*args, **kwargs)
            bind_hosts.append(self.host)

    def _capture_open(url: str, *args: Any, **kwargs: Any) -> bool:
        authorize_urls.append(url)
        return True

    task: asyncio.Task[Any] | None = None
    try:
        with (
            patch("webbrowser.open", side_effect=_capture_open) as browser_open,
            patch(
                "src.connectors.openai_chatgpt_plan.oauth.uvicorn.Config",
                _SpyConfig,
            ),
        ):
            original_announce = ChatGPTPlanOAuthService._announce_authorize_url

            def _announce(self: Any, url: str) -> None:
                authorize_urls.append(url)
                original_announce(self, url)

            with patch.object(
                ChatGPTPlanOAuthService, "_announce_authorize_url", _announce
            ):
                task = asyncio.create_task(
                    service.authorize_new(
                        profile_id=PROFILE_ID,
                        host_id=HOST_ID,
                        callback_port=0,
                        open_browser=open_browser,
                    )
                )
                await _wait_for(lambda: bool(authorize_urls))
                query = _parse_query(authorize_urls[0])
                capture.nonce = query["nonce"][0]
                redirect_uri = query["redirect_uri"][0]
                state = query["state"][0]
                params = {"state": state, **callback_query}
                callback_url = httpx.URL(redirect_uri).copy_merge_params(params)
                async with httpx.AsyncClient() as browser:
                    await browser.get(str(callback_url))
                result = await asyncio.wait_for(task, timeout=8)
                return {
                    "profile": result,
                    "capture": capture,
                    "authorize_url": authorize_urls[0],
                    "bind_hosts": bind_hosts,
                    "browser_open": browser_open,
                    "redirect_uri": redirect_uri,
                    "service": service,
                }
    finally:
        if task is not None and not task.done():
            task.cancel()
            with contextlib.suppress(asyncio.CancelledError, Exception):
                await task
        await http_client.aclose()


class TestChatGPTPlanAuthorizeNewFlow:
    @pytest.mark.asyncio
    async def test_callback_missing_issued_client_id_fails_closed(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.oauth import ChatGPTPlanOAuthError
        from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

        with pytest.raises(ChatGPTPlanOAuthError) as exc_info:
            await _drive_authorize_new(
                tmp_path,
                callback_query={"code": AUTH_CODE},
            )
        text = str(exc_info.value)
        _assert_no_secrets(text)
        assert "client" in text.lower()
        store = ChatGPTPlanProfileStore(tmp_path / "profiles")
        assert await store.load(PROFILE_ID) is None

    @pytest.mark.asyncio
    async def test_token_post_uses_issued_client_pkce_redirect_and_resource(
        self, tmp_path: Path
    ) -> None:
        driven = await _drive_authorize_new(
            tmp_path,
            callback_query={"code": AUTH_CODE, "client_id": ISSUED_CLIENT_ID},
        )
        form = driven["capture"].form
        request = driven["capture"].requests[0]
        assert str(request.url) == SIWC_TOKEN_URL
        assert form["grant_type"] == ["authorization_code"]
        assert form["client_id"] == [ISSUED_CLIENT_ID]
        assert form["client_id"] != ["dynamic_agent_client"]
        assert form["code"] == [AUTH_CODE]
        assert form["redirect_uri"] == [driven["redirect_uri"]]
        assert form["redirect_uri"] == [
            f"http://127.0.0.1:{urlparse(driven['redirect_uri']).port}/auth/callback"
        ]
        assert "localhost" not in form["redirect_uri"][0]
        assert form["resource"] == [SIWC_RESOURCE]
        assert "client_secret" not in form
        assert b"client_secret" not in request.content
        verifier = form["code_verifier"][0]
        expected_challenge = _s256_challenge(verifier)
        authorize_query = _parse_query(driven["authorize_url"])
        assert authorize_query["code_challenge"] == [expected_challenge]

    @pytest.mark.asyncio
    async def test_persisted_issued_client_id_is_not_bootstrap_client(
        self, tmp_path: Path
    ) -> None:
        driven = await _drive_authorize_new(
            tmp_path,
            callback_query={"code": AUTH_CODE, "client_id": ISSUED_CLIENT_ID},
        )
        profile = driven["profile"]
        assert profile.issued_client_id == ISSUED_CLIENT_ID
        assert profile.issued_client_id != "dynamic_agent_client"
        assert profile.status == "ready"
        assert profile.issuer == "https://auth.openai.com"
        from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

        loaded = await ChatGPTPlanProfileStore(tmp_path / "profiles").load(PROFILE_ID)
        assert loaded is not None
        assert loaded.issued_client_id == ISSUED_CLIENT_ID
        assert loaded.issued_client_id != "dynamic_agent_client"
        assert loaded.status == "ready"
        assert loaded.issuer == "https://auth.openai.com"

    @pytest.mark.asyncio
    async def test_callback_binds_loopback_address(self, tmp_path: Path) -> None:
        driven = await _drive_authorize_new(
            tmp_path,
            callback_query={"code": AUTH_CODE, "client_id": ISSUED_CLIENT_ID},
        )
        assert driven["bind_hosts"]
        assert all(host == "127.0.0.1" for host in driven["bind_hosts"])
        assert "localhost" not in driven["redirect_uri"]
        assert urlparse(driven["redirect_uri"]).hostname == "127.0.0.1"

    @pytest.mark.asyncio
    async def test_open_browser_false_does_not_call_webbrowser(
        self, tmp_path: Path
    ) -> None:
        driven = await _drive_authorize_new(
            tmp_path,
            callback_query={"code": AUTH_CODE, "client_id": ISSUED_CLIENT_ID},
            open_browser=False,
        )
        driven["browser_open"].assert_not_called()
        assert driven["profile"].issued_client_id == ISSUED_CLIENT_ID

    @pytest.mark.asyncio
    async def test_tokens_do_not_appear_in_logs_or_exceptions(
        self, tmp_path: Path, caplog: pytest.LogCaptureFixture
    ) -> None:
        from src.connectors.openai_chatgpt_plan.oauth import ChatGPTPlanOAuthError

        with caplog.at_level(logging.DEBUG):
            driven = await _drive_authorize_new(
                tmp_path,
                callback_query={"code": AUTH_CODE, "client_id": ISSUED_CLIENT_ID},
            )
            _assert_no_secrets(_joined_log_text(caplog))
            _assert_no_secrets(repr(driven["profile"]))
            _assert_no_secrets(str(driven["profile"]))
            if driven["profile"].id_token:
                assert driven["profile"].id_token not in _joined_log_text(caplog)

            error_capture = _TokenExchangeCapture(
                httpx.Response(
                    400,
                    json={
                        "error": "invalid_grant",
                        "access_token": ACCESS_TOKEN,
                        "refresh_token": REFRESH_TOKEN,
                        "id_token": ID_TOKEN,
                    },
                )
            )
            with pytest.raises(ChatGPTPlanOAuthError) as exc_info:
                await _drive_authorize_new(
                    tmp_path,
                    callback_query={
                        "code": AUTH_CODE,
                        "client_id": ISSUED_CLIENT_ID,
                    },
                    capture=error_capture,
                )
            _assert_no_secrets(str(exc_info.value))
            _assert_no_secrets(repr(exc_info.value))
            if getattr(exc_info.value, "details", None):
                _assert_no_secrets(str(exc_info.value.details))
            _assert_no_secrets(_joined_log_text(caplog))


class TestChatGPTPlanReauthorizeIssuedIdentity:
    def test_reauthorize_attempt_reuses_issued_client_and_host_id(
        self, tmp_path: Path
    ) -> None:
        service = _service(tmp_path)
        attempt = service.create_reauthorization_attempt(
            issued_client_id=ISSUED_CLIENT_ID,
            host_id=HOST_ID,
            callback_port=1455,
        )
        query = _parse_query(attempt.authorize_url)
        assert query["client_id"] == [ISSUED_CLIENT_ID]
        assert query["client_id"] != ["dynamic_agent_client"]
        assert query["ext_agent_host_id"] == [HOST_ID]
        assert "agent_name_hint" not in query
        assert query["resource"] == [SIWC_RESOURCE]

    def test_reauthorize_attempt_rejects_dynamic_agent_client(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.oauth import ChatGPTPlanOAuthError

        service = _service(tmp_path)
        with pytest.raises(ChatGPTPlanOAuthError) as exc_info:
            service.create_reauthorization_attempt(
                issued_client_id="dynamic_agent_client",
                host_id=HOST_ID,
                callback_port=1455,
            )
        text = str(exc_info.value).lower()
        assert "issued" in text or "client" in text
        _assert_no_secrets(str(exc_info.value))


class TestChatGPTPlanSignOutAndRevoke:
    @pytest.mark.asyncio
    async def test_sign_out_revokes_then_clears_tokens_keeping_registration(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.oauth import ChatGPTPlanOAuthService
        from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

        revoke_url = "https://auth.openai.com/api/accounts/oauth/revoke"
        revoke_requests: list[httpx.Request] = []

        def handler(request: httpx.Request) -> httpx.Response:
            url = str(request.url).split("?", 1)[0]
            if request.method == "GET" and url == SIWC_DISCOVERY_URL:
                return httpx.Response(200, json=_discovery_document())
            if url == revoke_url:
                revoke_requests.append(request)
                return httpx.Response(200, json={})
            return httpx.Response(404, json={"error": "not_found"})

        store = ChatGPTPlanProfileStore(tmp_path / "profiles")
        await store.save_atomic(_ready_profile())
        client = httpx.AsyncClient(transport=httpx.MockTransport(handler))
        service = ChatGPTPlanOAuthService(store, http_client=client)
        try:
            signed_out = await service.sign_out(PROFILE_ID)
        finally:
            await client.aclose()

        assert len(revoke_requests) == 1
        form = parse_qs(
            revoke_requests[0].content.decode("ascii"), keep_blank_values=True
        )
        assert str(revoke_requests[0].url) == revoke_url
        assert form["token"] == [REFRESH_TOKEN]
        assert form["token_type_hint"] == ["refresh_token"]
        assert form["client_id"] == [ISSUED_CLIENT_ID]
        assert signed_out.status == "signed_out"
        assert signed_out.access_token is None
        assert signed_out.refresh_token is None
        assert signed_out.id_token is None
        assert signed_out.issued_client_id == ISSUED_CLIENT_ID
        assert signed_out.subject == "oidc-subject-primary"
        assert signed_out.issuer == "https://auth.openai.com"
        assert signed_out.email == "chatgpt-plan-user@example.com"
        loaded = await store.load(PROFILE_ID)
        assert loaded is not None
        assert loaded.status == "signed_out"
        assert loaded.access_token is None
        assert loaded.refresh_token is None
        assert loaded.id_token is None
        assert loaded.issued_client_id == ISSUED_CLIENT_ID

    @pytest.mark.asyncio
    async def test_sign_out_clears_tokens_when_revocation_fails(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.oauth import ChatGPTPlanOAuthService
        from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

        revoke_url = "https://auth.openai.com/api/accounts/oauth/revoke"

        def handler(request: httpx.Request) -> httpx.Response:
            url = str(request.url).split("?", 1)[0]
            if request.method == "GET" and url == SIWC_DISCOVERY_URL:
                return httpx.Response(200, json=_discovery_document())
            if url == revoke_url:
                return httpx.Response(
                    500,
                    json={
                        "error": "server_error",
                        "refresh_token": REFRESH_TOKEN,
                    },
                )
            return httpx.Response(404, json={"error": "not_found"})

        store = ChatGPTPlanProfileStore(tmp_path / "profiles")
        await store.save_atomic(_ready_profile())
        client = httpx.AsyncClient(transport=httpx.MockTransport(handler))
        service = ChatGPTPlanOAuthService(store, http_client=client)
        try:
            signed_out = await service.sign_out(PROFILE_ID)
        finally:
            await client.aclose()
        assert signed_out.status == "signed_out"
        assert signed_out.access_token is None
        assert signed_out.refresh_token is None
        assert signed_out.id_token is None
        assert signed_out.issued_client_id == ISSUED_CLIENT_ID

    @pytest.mark.asyncio
    async def test_sign_out_does_not_log_tokens(
        self, tmp_path: Path, caplog: pytest.LogCaptureFixture
    ) -> None:
        from src.connectors.openai_chatgpt_plan.oauth import ChatGPTPlanOAuthService
        from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

        revoke_url = "https://auth.openai.com/api/accounts/oauth/revoke"

        def handler(request: httpx.Request) -> httpx.Response:
            url = str(request.url).split("?", 1)[0]
            if request.method == "GET" and url == SIWC_DISCOVERY_URL:
                return httpx.Response(200, json=_discovery_document())
            if url == revoke_url:
                return httpx.Response(200, json={"refresh_token": REFRESH_TOKEN})
            return httpx.Response(404, json={"error": "not_found"})

        store = ChatGPTPlanProfileStore(tmp_path / "profiles")
        await store.save_atomic(_ready_profile())
        client = httpx.AsyncClient(transport=httpx.MockTransport(handler))
        service = ChatGPTPlanOAuthService(store, http_client=client)
        with caplog.at_level(logging.DEBUG):
            try:
                signed_out = await service.sign_out(PROFILE_ID)
            finally:
                await client.aclose()
        _assert_no_secrets(_joined_log_text(caplog))
        _assert_no_secrets(repr(signed_out))
        _assert_no_secrets(str(signed_out))


class TestChatGPTPlanOAuthImportSafety:
    def test_importing_oauth_module_does_not_open_browser_or_network(self) -> None:
        import importlib
        import sys

        module_name = "src.connectors.openai_chatgpt_plan.oauth"
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
