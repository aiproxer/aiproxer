"""Unit tests for ChatGPT-plan OIDC/JWKS identity validation (task 2.3).

HTTP is mocked. These tests must not contact live OpenAI services.
Token fixtures must not use the sk- prefix (pre-commit secret scan).
"""

from __future__ import annotations

import asyncio
import contextlib
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from unittest.mock import patch
from urllib.parse import parse_qs, urlparse

import httpx
import pytest
from authlib.jose import JsonWebKey, JsonWebToken  # type: ignore[import-untyped]
from cryptography.hazmat.primitives.asymmetric import rsa

ACCESS_TOKEN = "access-token-test-value"
REFRESH_TOKEN = "refresh-token-test-value"
AUTH_CODE = "auth-code-test-value"
ISSUED_CLIENT_ID = "oaiapp_test_issued_client"
OTHER_CLIENT_ID = "oaiapp_other_issued_client"
HOST_ID = "testhostid00000001"
PROFILE_ID = "primary"
SUBJECT = "oidc-subject-primary"
OTHER_SUBJECT = "oidc-subject-other"
EMAIL = "chatgpt-plan-user@example.com"
DISPLAY_NAME = "ChatGPT Plan User"
DEFAULT_NONCE = "test-oidc-nonce-abcdefghijklmnopqrstuvwxyz"
SIWC_ISSUER = "https://auth.openai.com"
SIWC_DISCOVERY_URL = "https://auth.openai.com/.well-known/openid-configuration"
SIWC_JWKS_URL = "https://auth.openai.com/.well-known/jwks.json"
SIWC_TOKEN_URL = "https://auth.openai.com/api/accounts/oauth/token"
SIWC_RESOURCE = "https://api.openai.com/v1"
PLAN_USE_SCOPE = "chatgpt.tokens.use.direct"
IDENTITY_SCOPES = (
    "openid",
    "profile",
    "email",
    "offline_access",
    "resource.invoke",
)
FULL_SCOPES = (*IDENTITY_SCOPES, PLAN_USE_SCOPE)
SECRET_VALUES = (ACCESS_TOKEN, REFRESH_TOKEN, AUTH_CODE)


@dataclass(frozen=True)
class _RsaOidc:
    private_key: Any
    jwks: dict[str, Any]
    kid: str


def make_rsa_oidc(*, kid: str = "test-chatgpt-plan-oidc-key") -> _RsaOidc:
    private_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    jwk = JsonWebKey.import_key(private_key, {"kty": "RSA"})
    public = jwk.as_dict(is_private=False)
    public["kid"] = kid
    public["use"] = "sig"
    public["alg"] = "RS256"
    return _RsaOidc(private_key=private_key, jwks={"keys": [public]}, kid=kid)


@pytest.fixture(scope="module")
def rsa_oidc() -> _RsaOidc:
    return make_rsa_oidc()


def _now_ts() -> int:
    return int(time.time())


def _encode_id_token(
    rsa_oidc: _RsaOidc,
    *,
    private_key: Any | None = None,
    **claim_overrides: Any,
) -> str:
    now = _now_ts()
    payload: dict[str, Any] = {
        "iss": SIWC_ISSUER,
        "sub": SUBJECT,
        "aud": ISSUED_CLIENT_ID,
        "exp": now + 3600,
        "iat": now,
        "nonce": DEFAULT_NONCE,
        "email": EMAIL,
        "name": DISPLAY_NAME,
    }
    payload.update(claim_overrides)
    codec = JsonWebToken(["RS256"])
    token = codec.encode(
        {"alg": "RS256", "kid": rsa_oidc.kid},
        payload,
        private_key if private_key is not None else rsa_oidc.private_key,
    )
    return token.decode("ascii") if isinstance(token, bytes) else str(token)


def _discovery_document() -> dict[str, Any]:
    return {
        "issuer": SIWC_ISSUER,
        "jwks_uri": SIWC_JWKS_URL,
        "id_token_signing_alg_values_supported": ["RS256"],
        "revocation_endpoint": "https://auth.openai.com/api/accounts/oauth/revoke",
    }


def _oidc_client(
    rsa_oidc: _RsaOidc, *, jwks: dict[str, Any] | None = None
) -> httpx.AsyncClient:
    document = _discovery_document()
    keys = jwks if jwks is not None else rsa_oidc.jwks

    def handler(request: httpx.Request) -> httpx.Response:
        url = str(request.url).split("?", 1)[0]
        if url == SIWC_DISCOVERY_URL:
            return httpx.Response(200, json=document)
        if url == SIWC_JWKS_URL:
            return httpx.Response(200, json=keys)
        return httpx.Response(404, json={"error": "not_found"})

    return httpx.AsyncClient(transport=httpx.MockTransport(handler))


def _parse_query(url: str) -> dict[str, list[str]]:
    return parse_qs(urlparse(url).query, keep_blank_values=True)


def _assert_no_secrets(text: str, extra: tuple[str, ...] = ()) -> None:
    for secret in SECRET_VALUES + extra:
        assert secret not in text


def _profile_kwargs(**overrides: Any) -> dict[str, Any]:
    now = datetime.now(timezone.utc)
    payload: dict[str, Any] = {
        "schema_version": 1,
        "profile_id": PROFILE_ID,
        "issued_client_id": ISSUED_CLIENT_ID,
        "issuer": SIWC_ISSUER,
        "subject": SUBJECT,
        "email": EMAIL,
        "display_name": DISPLAY_NAME,
        "access_token": "existing-access-token-test-value",
        "refresh_token": "existing-refresh-token-test-value",
        "id_token": "existing-id-token-test-value",
        "granted_scopes": FULL_SCOPES,
        "resource": SIWC_RESOURCE,
        "access_token_expires_at": now,
        "refresh_token_expires_at": None,
        "status": "ready",
        "created_at": now,
        "updated_at": now,
    }
    payload.update(overrides)
    return payload


class _AuthorizeOidcCapture:
    def __init__(
        self,
        rsa_oidc: _RsaOidc,
        *,
        scope: str,
        subject: str = SUBJECT,
        claim_overrides: dict[str, Any] | None = None,
        id_token: str | None = None,
        omit_id_token: bool = False,
        signing_key: Any | None = None,
    ) -> None:
        self.rsa_oidc = rsa_oidc
        self.scope = scope
        self.subject = subject
        self.claim_overrides = claim_overrides or {}
        self.forced_id_token = id_token
        self.omit_id_token = omit_id_token
        self.signing_key = signing_key
        self.nonce: str | None = None
        self.requests: list[httpx.Request] = []

    def set_nonce(self, nonce: str) -> None:
        self.nonce = nonce

    def handler(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        url = str(request.url).split("?", 1)[0]
        if url == SIWC_DISCOVERY_URL:
            return httpx.Response(200, json=_discovery_document())
        if url == SIWC_JWKS_URL:
            return httpx.Response(200, json=self.rsa_oidc.jwks)
        if url == SIWC_TOKEN_URL:
            payload: dict[str, Any] = {
                "access_token": ACCESS_TOKEN,
                "refresh_token": REFRESH_TOKEN,
                "token_type": "Bearer",
                "expires_in": 3600,
                "scope": self.scope,
            }
            if not self.omit_id_token:
                if self.forced_id_token is not None:
                    payload["id_token"] = self.forced_id_token
                else:
                    assert (
                        self.nonce is not None
                    ), "nonce must be captured before token POST"
                    payload["id_token"] = _encode_id_token(
                        self.rsa_oidc,
                        private_key=self.signing_key,
                        nonce=self.nonce,
                        sub=self.subject,
                        **self.claim_overrides,
                    )
            return httpx.Response(200, json=payload)
        return httpx.Response(404, json={"error": "not_found"})


async def _wait_for(predicate: Any, timeout: float = 5.0) -> None:
    deadline = asyncio.get_running_loop().time() + timeout
    while asyncio.get_running_loop().time() < deadline:
        if predicate():
            return
        await asyncio.sleep(0.02)
    raise AssertionError("timed out waiting for authorization URL")


async def _drive_oauth(
    tmp_path: Path,
    rsa_oidc: _RsaOidc,
    *,
    callback_query: dict[str, str],
    capture: _AuthorizeOidcCapture,
    open_browser: bool = False,
    reauthorize_existing: Any | None = None,
    host_id: str = HOST_ID,
) -> dict[str, Any]:
    from src.connectors.openai_chatgpt_plan.oauth import ChatGPTPlanOAuthService
    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

    transport = httpx.MockTransport(capture.handler)
    http_client = httpx.AsyncClient(transport=transport)
    store = ChatGPTPlanProfileStore(tmp_path / "profiles")
    service = ChatGPTPlanOAuthService(
        store,
        http_client=http_client,
        timeout_seconds=8,
    )
    authorize_urls: list[str] = []

    import uvicorn

    original_config = uvicorn.Config

    class _SpyConfig(original_config):  # type: ignore[valid-type,misc]
        def __init__(self, *args: Any, **kwargs: Any) -> None:
            super().__init__(*args, **kwargs)

    def _announce(self: Any, url: str) -> None:
        authorize_urls.append(url)

    task: asyncio.Task[Any] | None = None
    try:
        with (
            patch(
                "src.connectors.openai_chatgpt_plan.oauth.uvicorn.Config", _SpyConfig
            ),
            patch.object(ChatGPTPlanOAuthService, "_announce_authorize_url", _announce),
        ):
            if reauthorize_existing is None:
                coro = service.authorize_new(
                    profile_id=PROFILE_ID,
                    host_id=host_id,
                    callback_port=0,
                    open_browser=open_browser,
                )
            else:
                coro = service.reauthorize(
                    existing=reauthorize_existing,
                    host_id=host_id,
                    callback_port=0,
                    open_browser=open_browser,
                )
            task = asyncio.create_task(coro)
            await _wait_for(lambda: bool(authorize_urls))
            query = _parse_query(authorize_urls[0])
            capture.set_nonce(query["nonce"][0])
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
                "redirect_uri": redirect_uri,
                "store": store,
                "service": service,
            }
    finally:
        if task is not None and not task.done():
            task.cancel()
            with contextlib.suppress(asyncio.CancelledError, Exception):
                await task
        await http_client.aclose()


class TestChatGPTPlanOidcIndependentFailures:
    @pytest.mark.asyncio
    async def test_rejects_bad_signature(self, rsa_oidc: _RsaOidc) -> None:
        from src.connectors.openai_chatgpt_plan.oidc import (
            ChatGPTPlanOidcError,
            ChatGPTPlanOidcValidator,
        )

        other_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        token = _encode_id_token(rsa_oidc, private_key=other_key)
        client = _oidc_client(rsa_oidc)
        validator = ChatGPTPlanOidcValidator(http_client=client)
        try:
            with pytest.raises(ChatGPTPlanOidcError) as exc_info:
                await validator.validate_id_token(
                    id_token=token,
                    expected_audience=ISSUED_CLIENT_ID,
                    expected_nonce=DEFAULT_NONCE,
                )
        finally:
            await client.aclose()
        text = str(exc_info.value).lower()
        assert "signature" in text
        _assert_no_secrets(str(exc_info.value), extra=(token,))

    @pytest.mark.asyncio
    async def test_rejects_wrong_issuer(self, rsa_oidc: _RsaOidc) -> None:
        from src.connectors.openai_chatgpt_plan.oidc import (
            ChatGPTPlanOidcError,
            ChatGPTPlanOidcValidator,
        )

        token = _encode_id_token(rsa_oidc, iss="https://evil.example")
        client = _oidc_client(rsa_oidc)
        validator = ChatGPTPlanOidcValidator(http_client=client)
        try:
            with pytest.raises(ChatGPTPlanOidcError) as exc_info:
                await validator.validate_id_token(
                    id_token=token,
                    expected_audience=ISSUED_CLIENT_ID,
                    expected_nonce=DEFAULT_NONCE,
                )
        finally:
            await client.aclose()
        text = str(exc_info.value).lower()
        assert "issuer" in text
        _assert_no_secrets(str(exc_info.value), extra=(token,))

    @pytest.mark.asyncio
    async def test_rejects_wrong_audience(self, rsa_oidc: _RsaOidc) -> None:
        from src.connectors.openai_chatgpt_plan.oidc import (
            ChatGPTPlanOidcError,
            ChatGPTPlanOidcValidator,
        )

        token = _encode_id_token(rsa_oidc, aud="other-audience-client")
        client = _oidc_client(rsa_oidc)
        validator = ChatGPTPlanOidcValidator(http_client=client)
        try:
            with pytest.raises(ChatGPTPlanOidcError) as exc_info:
                await validator.validate_id_token(
                    id_token=token,
                    expected_audience=ISSUED_CLIENT_ID,
                    expected_nonce=DEFAULT_NONCE,
                )
        finally:
            await client.aclose()
        text = str(exc_info.value).lower()
        assert "audience" in text
        _assert_no_secrets(str(exc_info.value), extra=(token,))

    @pytest.mark.asyncio
    async def test_rejects_expired(self, rsa_oidc: _RsaOidc) -> None:
        from src.connectors.openai_chatgpt_plan.oidc import (
            ChatGPTPlanOidcError,
            ChatGPTPlanOidcValidator,
        )

        now = _now_ts()
        token = _encode_id_token(rsa_oidc, iat=now - 3600, exp=now - 120)
        client = _oidc_client(rsa_oidc)
        validator = ChatGPTPlanOidcValidator(http_client=client)
        try:
            with pytest.raises(ChatGPTPlanOidcError) as exc_info:
                await validator.validate_id_token(
                    id_token=token,
                    expected_audience=ISSUED_CLIENT_ID,
                    expected_nonce=DEFAULT_NONCE,
                )
        finally:
            await client.aclose()
        text = str(exc_info.value).lower()
        assert "expir" in text
        _assert_no_secrets(str(exc_info.value), extra=(token,))

    @pytest.mark.asyncio
    async def test_rejects_wrong_nonce(self, rsa_oidc: _RsaOidc) -> None:
        from src.connectors.openai_chatgpt_plan.oidc import (
            ChatGPTPlanOidcError,
            ChatGPTPlanOidcValidator,
        )

        token = _encode_id_token(rsa_oidc, nonce="unrelated-nonce-value")
        client = _oidc_client(rsa_oidc)
        validator = ChatGPTPlanOidcValidator(http_client=client)
        try:
            with pytest.raises(ChatGPTPlanOidcError) as exc_info:
                await validator.validate_id_token(
                    id_token=token,
                    expected_audience=ISSUED_CLIENT_ID,
                    expected_nonce=DEFAULT_NONCE,
                )
        finally:
            await client.aclose()
        text = str(exc_info.value).lower()
        assert "nonce" in text
        _assert_no_secrets(str(exc_info.value), extra=(token,))


class TestChatGPTPlanOidcHappyPathAndScope:
    @pytest.mark.asyncio
    async def test_valid_token_returns_verified_identity(
        self, rsa_oidc: _RsaOidc
    ) -> None:
        from src.connectors.openai_chatgpt_plan.oidc import ChatGPTPlanOidcValidator

        token = _encode_id_token(rsa_oidc)
        client = _oidc_client(rsa_oidc)
        validator = ChatGPTPlanOidcValidator(http_client=client)
        try:
            identity = await validator.validate_id_token(
                id_token=token,
                expected_audience=ISSUED_CLIENT_ID,
                expected_nonce=DEFAULT_NONCE,
            )
        finally:
            await client.aclose()
        assert identity.issuer == SIWC_ISSUER
        assert identity.subject == SUBJECT
        assert identity.audience == ISSUED_CLIENT_ID
        assert identity.email == EMAIL
        assert identity.display_name == DISPLAY_NAME

    @pytest.mark.asyncio
    async def test_missing_plan_scope_retains_identity_but_not_ready(
        self, tmp_path: Path, rsa_oidc: _RsaOidc
    ) -> None:
        capture = _AuthorizeOidcCapture(
            rsa_oidc,
            scope=" ".join(IDENTITY_SCOPES),
        )
        driven = await _drive_oauth(
            tmp_path,
            rsa_oidc,
            callback_query={"code": AUTH_CODE, "client_id": ISSUED_CLIENT_ID},
            capture=capture,
        )
        profile = driven["profile"]
        assert profile.status == "missing_plan_scope"
        assert profile.status != "ready"
        assert profile.issuer == SIWC_ISSUER
        assert profile.subject == SUBJECT
        assert profile.issuer != "pending-oidc-validation"
        assert profile.subject != "pending-oidc-validation"
        assert profile.issued_client_id == ISSUED_CLIENT_ID
        assert profile.email == EMAIL
        assert profile.display_name == DISPLAY_NAME
        assert PLAN_USE_SCOPE not in profile.granted_scopes
        assert profile.access_token == ACCESS_TOKEN
        assert profile.refresh_token == REFRESH_TOKEN
        loaded = await driven["store"].load(PROFILE_ID)
        assert loaded is not None
        assert loaded.status == "missing_plan_scope"
        assert loaded.issuer == SIWC_ISSUER
        assert loaded.subject == SUBJECT
        assert loaded.issued_client_id == ISSUED_CLIENT_ID

    @pytest.mark.asyncio
    async def test_plan_scope_activates_ready_profile(
        self, tmp_path: Path, rsa_oidc: _RsaOidc
    ) -> None:
        capture = _AuthorizeOidcCapture(
            rsa_oidc,
            scope=" ".join(FULL_SCOPES),
        )
        driven = await _drive_oauth(
            tmp_path,
            rsa_oidc,
            callback_query={"code": AUTH_CODE, "client_id": ISSUED_CLIENT_ID},
            capture=capture,
        )
        profile = driven["profile"]
        assert profile.status == "ready"
        assert profile.issuer == SIWC_ISSUER
        assert profile.subject == SUBJECT
        assert PLAN_USE_SCOPE in profile.granted_scopes
        loaded = await driven["store"].load(PROFILE_ID)
        assert loaded is not None
        assert loaded.status == "ready"
        assert loaded.profile_id == PROFILE_ID
        assert (tmp_path / "profiles" / f"{PROFILE_ID}.json").is_file()

    @pytest.mark.asyncio
    async def test_bad_signature_does_not_persist_profile(
        self, tmp_path: Path, rsa_oidc: _RsaOidc
    ) -> None:
        from src.connectors.openai_chatgpt_plan.oauth import ChatGPTPlanOAuthError

        other_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        capture = _AuthorizeOidcCapture(
            rsa_oidc,
            scope=" ".join(FULL_SCOPES),
            signing_key=other_key,
        )
        with pytest.raises(ChatGPTPlanOAuthError) as exc_info:
            await _drive_oauth(
                tmp_path,
                rsa_oidc,
                callback_query={"code": AUTH_CODE, "client_id": ISSUED_CLIENT_ID},
                capture=capture,
            )
        _assert_no_secrets(str(exc_info.value))
        loaded = await _load_saved_profile(tmp_path)
        assert loaded is None


async def _load_saved_profile(tmp_path: Path) -> Any:
    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

    return await ChatGPTPlanProfileStore(tmp_path / "profiles").load(PROFILE_ID)


class TestChatGPTPlanReauthorizeIdentity:
    def test_reauthorize_url_reuses_issued_client_and_host_id(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.oauth import ChatGPTPlanOAuthService
        from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

        service = ChatGPTPlanOAuthService(
            ChatGPTPlanProfileStore(tmp_path / "profiles")
        )
        attempt = service.create_reauthorization_attempt(
            issued_client_id=ISSUED_CLIENT_ID,
            host_id=HOST_ID,
            callback_port=1455,
            id_token_hint="id-token-hint-test-value",
            login_hint=EMAIL,
        )
        query = _parse_query(attempt.authorize_url)
        assert query["client_id"] == [ISSUED_CLIENT_ID]
        assert query["client_id"] != ["dynamic_agent_client"]
        assert "agent_name_hint" not in query
        assert query["ext_agent_host_id"] == [HOST_ID]
        assert query["id_token_hint"] == ["id-token-hint-test-value"]
        assert query["login_hint"] == [EMAIL]
        assert query["resource"] == [SIWC_RESOURCE]
        assert PLAN_USE_SCOPE in query["scope"][0].split()
        assert attempt.redirect_uri == "http://127.0.0.1:1455/auth/callback"

    @pytest.mark.asyncio
    async def test_conflicting_subject_does_not_overwrite(
        self, tmp_path: Path, rsa_oidc: _RsaOidc
    ) -> None:
        from src.connectors.openai_chatgpt_plan.models import ChatGPTPlanProfile
        from src.connectors.openai_chatgpt_plan.oauth import ChatGPTPlanOAuthError
        from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

        store = ChatGPTPlanProfileStore(tmp_path / "profiles")
        existing = ChatGPTPlanProfile(**_profile_kwargs())
        await store.save_atomic(existing)
        original = (tmp_path / "profiles" / f"{PROFILE_ID}.json").read_text(
            encoding="utf-8"
        )

        capture = _AuthorizeOidcCapture(
            rsa_oidc,
            scope=" ".join(FULL_SCOPES),
            subject=OTHER_SUBJECT,
        )
        with pytest.raises(ChatGPTPlanOAuthError) as exc_info:
            await _drive_oauth(
                tmp_path,
                rsa_oidc,
                callback_query={"code": AUTH_CODE},
                capture=capture,
                reauthorize_existing=existing,
            )
        text = str(exc_info.value).lower()
        assert "identit" in text or "subject" in text or "conflict" in text
        _assert_no_secrets(str(exc_info.value))
        loaded = await store.load(PROFILE_ID)
        assert loaded is not None
        assert loaded.subject == SUBJECT
        assert loaded.issued_client_id == ISSUED_CLIENT_ID
        assert loaded.access_token == "existing-access-token-test-value"
        assert loaded.refresh_token == "existing-refresh-token-test-value"
        assert (tmp_path / "profiles" / f"{PROFILE_ID}.json").read_text(
            encoding="utf-8"
        ) == original

    @pytest.mark.asyncio
    async def test_conflicting_callback_client_id_does_not_overwrite(
        self, tmp_path: Path, rsa_oidc: _RsaOidc
    ) -> None:
        from src.connectors.openai_chatgpt_plan.models import ChatGPTPlanProfile
        from src.connectors.openai_chatgpt_plan.oauth import ChatGPTPlanOAuthError
        from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

        store = ChatGPTPlanProfileStore(tmp_path / "profiles")
        existing = ChatGPTPlanProfile(**_profile_kwargs())
        await store.save_atomic(existing)
        original = (tmp_path / "profiles" / f"{PROFILE_ID}.json").read_text(
            encoding="utf-8"
        )

        capture = _AuthorizeOidcCapture(
            rsa_oidc,
            scope=" ".join(FULL_SCOPES),
        )
        with pytest.raises(ChatGPTPlanOAuthError) as exc_info:
            await _drive_oauth(
                tmp_path,
                rsa_oidc,
                callback_query={"code": AUTH_CODE, "client_id": OTHER_CLIENT_ID},
                capture=capture,
                reauthorize_existing=existing,
            )
        text = str(exc_info.value).lower()
        assert "client" in text
        _assert_no_secrets(str(exc_info.value))
        loaded = await store.load(PROFILE_ID)
        assert loaded is not None
        assert loaded.issued_client_id == ISSUED_CLIENT_ID
        assert loaded.subject == SUBJECT
        assert loaded.access_token == "existing-access-token-test-value"
        assert (tmp_path / "profiles" / f"{PROFILE_ID}.json").read_text(
            encoding="utf-8"
        ) == original

    @pytest.mark.asyncio
    async def test_matching_identity_reuses_issued_client_and_updates_tokens(
        self, tmp_path: Path, rsa_oidc: _RsaOidc
    ) -> None:
        from src.connectors.openai_chatgpt_plan.models import ChatGPTPlanProfile
        from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

        store = ChatGPTPlanProfileStore(tmp_path / "profiles")
        existing = ChatGPTPlanProfile(**_profile_kwargs())
        await store.save_atomic(existing)

        capture = _AuthorizeOidcCapture(
            rsa_oidc,
            scope=" ".join(FULL_SCOPES),
            subject=SUBJECT,
        )
        driven = await _drive_oauth(
            tmp_path,
            rsa_oidc,
            callback_query={"code": AUTH_CODE},
            capture=capture,
            reauthorize_existing=existing,
        )
        profile = driven["profile"]
        query = _parse_query(driven["authorize_url"])
        assert query["client_id"] == [ISSUED_CLIENT_ID]
        assert query["client_id"] != ["dynamic_agent_client"]
        assert query["ext_agent_host_id"] == [HOST_ID]
        assert "agent_name_hint" not in query
        assert profile.subject == SUBJECT
        assert profile.issuer == SIWC_ISSUER
        assert profile.issued_client_id == ISSUED_CLIENT_ID
        assert profile.access_token == ACCESS_TOKEN
        assert profile.refresh_token == REFRESH_TOKEN
        assert profile.status == "ready"
        assert profile.created_at == existing.created_at
        loaded = await store.load(PROFILE_ID)
        assert loaded is not None
        assert loaded.access_token == ACCESS_TOKEN
        assert loaded.subject == SUBJECT


class TestChatGPTPlanOidcRevocationEndpoint:
    @pytest.mark.asyncio
    async def test_get_revocation_endpoint_from_discovery(
        self, rsa_oidc: _RsaOidc
    ) -> None:
        from src.connectors.openai_chatgpt_plan.oidc import ChatGPTPlanOidcValidator

        client = _oidc_client(rsa_oidc)
        validator = ChatGPTPlanOidcValidator(http_client=client)
        try:
            endpoint = await validator.get_revocation_endpoint()
        finally:
            await client.aclose()
        assert endpoint == "https://auth.openai.com/api/accounts/oauth/revoke"
