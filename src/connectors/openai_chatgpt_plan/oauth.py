"""Sign in with ChatGPT dynamic registration, loopback OAuth, and OIDC identity."""

from __future__ import annotations

import asyncio
import contextlib
import logging
import secrets
import webbrowser
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from typing import Any, Protocol
from urllib.parse import quote, urlencode

import httpx
import uvicorn
from authlib.common.security import generate_token  # type: ignore[import-untyped]
from authlib.oauth2.rfc7636 import (  # type: ignore[import-untyped]
    create_s256_code_challenge,
)
from fastapi import FastAPI, Request
from fastapi.responses import HTMLResponse

from src.connectors.openai_chatgpt_plan.models import (
    ChatGPTPlanProfile,
    ChatGPTPlanProfileStatus,
    is_valid_profile_id,
    redact_chatgpt_plan_mapping,
)
from src.connectors.openai_chatgpt_plan.oidc import (
    PLAN_USE_SCOPE,
    ChatGPTPlanOidcError,
    ChatGPTPlanOidcValidator,
    has_chatgpt_plan_use_scope,
)
from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore
from src.core.common.exceptions import LLMProxyError
from src.core.common.logging_utils import DEFAULT_REDACTED_FIELDS, redact_dict
from src.core.url_safety import assert_url_safe_for_egress

logger = logging.getLogger(__name__)

SIWC_AUTHORIZE_URL = "https://auth.openai.com/api/accounts/authorize"
SIWC_TOKEN_URL = "https://auth.openai.com/api/accounts/oauth/token"
SIWC_RESOURCE = "https://api.openai.com/v1"
DYNAMIC_CLIENT_ID = "dynamic_agent_client"
AGENT_NAME_HINT = "AIProxer"
LOOPBACK_HOST = "127.0.0.1"
CALLBACK_PATH = "/auth/callback"
REQUIRED_SCOPES: tuple[str, ...] = (
    "openid",
    "profile",
    "email",
    "offline_access",
    "resource.invoke",
    PLAN_USE_SCOPE,
)
DEFAULT_AUTHORIZE_TIMEOUT_SECONDS = 180.0
TOKEN_EXCHANGE_TIMEOUT_SECONDS = 30.0
REVOCATION_TIMEOUT_SECONDS = 15.0
PKCE_VERIFIER_LENGTH = 64
STATE_NONCE_LENGTH = 48

_OAUTH_REDACT_FIELDS = set(DEFAULT_REDACTED_FIELDS) | {
    "access_token",
    "refresh_token",
    "id_token",
    "code",
    "code_verifier",
    "client_secret",
}

_CALLBACK_OK_HTML = (
    "<h2>Authorization complete.</h2>"
    "<p>You can close this window and return to the terminal.</p>"
)
_CALLBACK_FAIL_HTML = (
    "<h2>Authorization failed.</h2>"
    "<p>You can close this window and check the terminal output.</p>"
)


def _utc_now() -> datetime:
    return datetime.now(timezone.utc)


def _redact_oauth_mapping(data: Mapping[str, Any] | None) -> dict[str, Any]:
    if not data:
        return {}
    return redact_dict(dict(data), redacted_fields=_OAUTH_REDACT_FIELDS)


class ChatGPTPlanOAuthError(LLMProxyError):
    """Fail-closed SIWC authorization, callback, or token-exchange error."""

    def __init__(
        self,
        message: str,
        details: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        status_code = kwargs.pop("status_code", 401)
        super().__init__(
            message,
            details=_redact_oauth_mapping(details),
            status_code=status_code,
            **kwargs,
        )


@dataclass(frozen=True)
class ChatGPTPlanAuthorizationRequest:
    """One-shot SIWC authorization parameters. PKCE verifier is not repr'd."""

    state: str
    nonce: str
    code_verifier: str
    code_challenge: str
    redirect_uri: str
    authorize_url: str

    def __repr__(self) -> str:
        return (
            "ChatGPTPlanAuthorizationRequest("
            f"state={self.state!r}, nonce={self.nonce!r}, "
            "code_verifier='***', "
            f"code_challenge={self.code_challenge!r}, "
            f"redirect_uri={self.redirect_uri!r})"
        )


class IChatGPTPlanOAuthService(Protocol):
    async def authorize_new(
        self,
        *,
        profile_id: str,
        host_id: str,
        callback_port: int,
        open_browser: bool,
    ) -> ChatGPTPlanProfile: ...

    async def reauthorize(
        self,
        *,
        existing: ChatGPTPlanProfile,
        host_id: str,
        callback_port: int,
        open_browser: bool,
    ) -> ChatGPTPlanProfile: ...

    async def revoke(self, profile: ChatGPTPlanProfile) -> None: ...

    async def sign_out(self, profile_id: str) -> ChatGPTPlanProfile: ...


def build_redirect_uri(callback_port: int) -> str:
    """Advertise the exact SIWC loopback callback URI for this attempt."""

    return f"http://{LOOPBACK_HOST}:{int(callback_port)}{CALLBACK_PATH}"


class ChatGPTPlanOAuthService:
    """Dynamic registration, loopback callback, OIDC validation, and revocation."""

    def __init__(
        self,
        profile_store: ChatGPTPlanProfileStore,
        *,
        http_client: httpx.AsyncClient | None = None,
        timeout_seconds: float = DEFAULT_AUTHORIZE_TIMEOUT_SECONDS,
        agent_name_hint: str = AGENT_NAME_HINT,
        oidc_validator: ChatGPTPlanOidcValidator | None = None,
    ) -> None:
        self._profile_store = profile_store
        self._http_client = http_client
        self._timeout_seconds = timeout_seconds
        self._agent_name_hint = agent_name_hint
        self._open_browser = False
        self._oidc = oidc_validator or ChatGPTPlanOidcValidator(http_client=http_client)

    def create_new_registration_attempt(
        self,
        *,
        host_id: str,
        callback_port: int,
    ) -> ChatGPTPlanAuthorizationRequest:
        """Build a fresh dynamic-registration authorize URL (no network)."""

        redirect_uri = build_redirect_uri(callback_port)
        state = generate_token(STATE_NONCE_LENGTH)
        nonce = generate_token(STATE_NONCE_LENGTH)
        code_verifier = generate_token(PKCE_VERIFIER_LENGTH)
        code_challenge = create_s256_code_challenge(code_verifier)
        params = {
            "client_id": DYNAMIC_CLIENT_ID,
            "agent_name_hint": self._agent_name_hint,
            "ext_agent_host_id": host_id,
            "response_type": "code",
            "redirect_uri": redirect_uri,
            "scope": " ".join(REQUIRED_SCOPES),
            "resource": SIWC_RESOURCE,
            "state": state,
            "nonce": nonce,
            "code_challenge": code_challenge,
            "code_challenge_method": "S256",
        }
        authorize_url = f"{SIWC_AUTHORIZE_URL}?{urlencode(params, quote_via=quote)}"
        return ChatGPTPlanAuthorizationRequest(
            state=state,
            nonce=nonce,
            code_verifier=code_verifier,
            code_challenge=code_challenge,
            redirect_uri=redirect_uri,
            authorize_url=authorize_url,
        )

    def create_reauthorization_attempt(
        self,
        *,
        issued_client_id: str,
        host_id: str,
        callback_port: int,
        id_token_hint: str | None = None,
        login_hint: str | None = None,
    ) -> ChatGPTPlanAuthorizationRequest:
        """Build a returning-sign-in authorize URL for an issued client ID."""

        client_id = issued_client_id.strip()
        if not client_id or client_id == DYNAMIC_CLIENT_ID:
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan reauthorization requires the profile's issued "
                "client ID, not dynamic_agent_client.",
                status_code=400,
            )
        redirect_uri = build_redirect_uri(callback_port)
        state = generate_token(STATE_NONCE_LENGTH)
        nonce = generate_token(STATE_NONCE_LENGTH)
        code_verifier = generate_token(PKCE_VERIFIER_LENGTH)
        code_challenge = create_s256_code_challenge(code_verifier)
        params = {
            "client_id": client_id,
            "ext_agent_host_id": host_id,
            "response_type": "code",
            "redirect_uri": redirect_uri,
            "scope": " ".join(REQUIRED_SCOPES),
            "resource": SIWC_RESOURCE,
            "state": state,
            "nonce": nonce,
            "code_challenge": code_challenge,
            "code_challenge_method": "S256",
        }
        hint = (id_token_hint or "").strip()
        if hint:
            params["id_token_hint"] = hint
        email_hint = (login_hint or "").strip()
        if email_hint:
            params["login_hint"] = email_hint
        authorize_url = f"{SIWC_AUTHORIZE_URL}?{urlencode(params, quote_via=quote)}"
        return ChatGPTPlanAuthorizationRequest(
            state=state,
            nonce=nonce,
            code_verifier=code_verifier,
            code_challenge=code_challenge,
            redirect_uri=redirect_uri,
            authorize_url=authorize_url,
        )

    def _announce_authorize_url(self, url: str) -> None:
        if self._open_browser:
            try:
                webbrowser.open(url)
            except (OSError, webbrowser.Error):
                logger.info(
                    "Browser open failed; open this ChatGPT plan authorization URL: %s",
                    url,
                )
                logger.debug(
                    "webbrowser.open failed during ChatGPT-plan authorization",
                    exc_info=True,
                )
            return
        logger.info(
            "Open this URL to authorize the ChatGPT plan profile "
            "(callback is listening on 127.0.0.1): %s",
            url,
        )

    async def authorize_new(
        self,
        *,
        profile_id: str,
        host_id: str,
        callback_port: int,
        open_browser: bool,
    ) -> ChatGPTPlanProfile:
        if not is_valid_profile_id(profile_id):
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan profile_id is not a valid local alias.",
                details={"profile_id": profile_id},
                status_code=400,
            )
        host = host_id.strip()
        if not host:
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan host_id is required for dynamic registration.",
                status_code=400,
            )
        self._require_callback_port(callback_port)

        attempt, code, issued_client_id = await self._run_loopback_authorization(
            callback_port=callback_port,
            open_browser=open_browser,
            attempt_factory=lambda port: self.create_new_registration_attempt(
                host_id=host,
                callback_port=port,
            ),
            expected_issued_client_id=None,
            log_profile_id=profile_id,
        )
        tokens = await self._exchange_authorization_code(
            code=code,
            issued_client_id=issued_client_id,
            redirect_uri=attempt.redirect_uri,
            code_verifier=attempt.code_verifier,
        )
        profile = await self._profile_from_validated_tokens(
            profile_id=profile_id,
            issued_client_id=issued_client_id,
            tokens=tokens,
            nonce=attempt.nonce,
        )
        await self._profile_store.save_atomic(profile)
        return profile

    async def reauthorize(
        self,
        *,
        existing: ChatGPTPlanProfile,
        host_id: str,
        callback_port: int,
        open_browser: bool,
    ) -> ChatGPTPlanProfile:
        if not is_valid_profile_id(existing.profile_id):
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan profile_id is not a valid local alias.",
                details={"profile_id": existing.profile_id},
                status_code=400,
            )
        host = host_id.strip()
        if not host:
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan host_id is required for reauthorization.",
                status_code=400,
            )
        self._require_callback_port(callback_port)
        issued = existing.issued_client_id.strip()
        if not issued or issued == DYNAMIC_CLIENT_ID:
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan reauthorization requires the profile's issued "
                "client ID, not dynamic_agent_client.",
                status_code=400,
            )

        attempt, code, issued_client_id = await self._run_loopback_authorization(
            callback_port=callback_port,
            open_browser=open_browser,
            attempt_factory=lambda port: self.create_reauthorization_attempt(
                issued_client_id=issued,
                host_id=host,
                callback_port=port,
                id_token_hint=existing.id_token,
                login_hint=existing.email,
            ),
            expected_issued_client_id=issued,
            log_profile_id=existing.profile_id,
        )
        tokens = await self._exchange_authorization_code(
            code=code,
            issued_client_id=issued_client_id,
            redirect_uri=attempt.redirect_uri,
            code_verifier=attempt.code_verifier,
        )
        profile = await self._profile_from_validated_tokens(
            profile_id=existing.profile_id,
            issued_client_id=issued_client_id,
            tokens=tokens,
            nonce=attempt.nonce,
            existing=existing,
        )
        await self._profile_store.save_atomic(profile)
        return profile

    async def revoke(self, profile: ChatGPTPlanProfile) -> None:
        """Attempt RFC 7009 refresh-token revocation. Failures are non-fatal."""

        refresh = (profile.refresh_token or "").strip()
        if not refresh:
            return
        try:
            endpoint = await self._oidc.get_revocation_endpoint()
        except ChatGPTPlanOidcError:
            logger.debug(
                "ChatGPT-plan revocation discovery failed for profile %s",
                profile.profile_id,
                exc_info=True,
            )
            return
        if not endpoint:
            logger.debug("ChatGPT-plan OIDC discovery has no revocation endpoint")
            return
        try:
            assert_url_safe_for_egress(endpoint)
        except ValueError:
            logger.debug(
                "ChatGPT-plan revocation endpoint failed URL safety checks",
                exc_info=True,
            )
            return
        form = {
            "token": refresh,
            "token_type_hint": "refresh_token",
            "client_id": profile.issued_client_id,
        }
        headers = {"Content-Type": "application/x-www-form-urlencoded"}
        try:
            if self._http_client is None:
                async with httpx.AsyncClient() as client:
                    response = await client.post(
                        endpoint,
                        data=form,
                        headers=headers,
                        timeout=REVOCATION_TIMEOUT_SECONDS,
                    )
            else:
                response = await self._http_client.post(
                    endpoint,
                    data=form,
                    headers=headers,
                    timeout=REVOCATION_TIMEOUT_SECONDS,
                )
        except httpx.HTTPError:
            logger.debug(
                "ChatGPT-plan token revocation HTTP error for profile %s",
                profile.profile_id,
                exc_info=True,
            )
            return
        if response.status_code >= 400:
            logger.debug(
                "ChatGPT-plan token revocation returned HTTP %s for profile %s",
                response.status_code,
                profile.profile_id,
            )

    async def sign_out(self, profile_id: str) -> ChatGPTPlanProfile:
        """Revoke remote tokens if possible, then clear local bearer material."""

        profile = await self._profile_store.load(profile_id)
        if profile is None:
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan profile was not found.",
                details={"profile_id": profile_id},
                status_code=404,
            )
        try:
            await self.revoke(profile)
        except (
            ChatGPTPlanOidcError,
            ChatGPTPlanOAuthError,
            httpx.HTTPError,
            ValueError,
        ):
            logger.debug(
                "ChatGPT-plan token revocation attempt failed for profile %s",
                profile_id,
                exc_info=True,
            )
        await self._profile_store.clear_tokens(profile_id, "signed_out")
        cleared = await self._profile_store.load(profile_id)
        if cleared is None:
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan profile was not found after sign-out.",
                details={"profile_id": profile_id},
                status_code=404,
            )
        return cleared

    def _require_callback_port(self, callback_port: int) -> None:
        if callback_port < 0 or callback_port > 65535:
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan OAuth callback_port is out of range.",
                details={"callback_port": callback_port},
                status_code=400,
            )

    async def _run_loopback_authorization(
        self,
        *,
        callback_port: int,
        open_browser: bool,
        attempt_factory: Callable[[int], ChatGPTPlanAuthorizationRequest],
        expected_issued_client_id: str | None,
        log_profile_id: str,
    ) -> tuple[ChatGPTPlanAuthorizationRequest, str, str]:
        self._open_browser = open_browser
        loop = asyncio.get_running_loop()
        callback_future: asyncio.Future[tuple[str, str]] = loop.create_future()
        attempt_holder: list[ChatGPTPlanAuthorizationRequest] = []
        app = FastAPI()

        async def oauth_callback(request: Request) -> HTMLResponse:
            if not attempt_holder:
                return HTMLResponse(_CALLBACK_FAIL_HTML, status_code=400)
            return await self._handle_oauth_callback(
                request=request,
                expected_state=attempt_holder[0].state,
                callback_future=callback_future,
                expected_issued_client_id=expected_issued_client_id,
            )

        app.add_api_route(CALLBACK_PATH, oauth_callback, methods=["GET"])

        config = uvicorn.Config(
            app,
            host=LOOPBACK_HOST,
            port=int(callback_port),
            log_level="error",
            access_log=False,
            lifespan="off",
        )
        server = uvicorn.Server(config)
        server_task = asyncio.create_task(server.serve())
        try:
            await self._wait_for_callback_server(server, server_task)
            actual_port = self._bound_port(server, fallback_port=int(callback_port))
            attempt = attempt_factory(actual_port)
            attempt_holder.append(attempt)
            logger.debug(
                "Starting ChatGPT-plan authorization for profile %s",
                log_profile_id,
            )
            self._announce_authorize_url(attempt.authorize_url)
            try:
                code, issued_client_id = await asyncio.wait_for(
                    callback_future, timeout=self._timeout_seconds
                )
            except asyncio.TimeoutError as exc:
                raise ChatGPTPlanOAuthError(
                    "ChatGPT-plan authorization timed out waiting for "
                    "the loopback callback."
                ) from exc
            return attempt, code, issued_client_id
        finally:
            server.should_exit = True
            if not server_task.done():
                with contextlib.suppress(asyncio.TimeoutError, Exception):
                    await asyncio.wait_for(server_task, timeout=5)
            if not server_task.done():
                server_task.cancel()
            with contextlib.suppress(asyncio.CancelledError, Exception):
                await server_task

    async def _wait_for_callback_server(
        self,
        server: uvicorn.Server,
        server_task: asyncio.Task[None],
    ) -> None:
        while not server.started:
            if server_task.done():
                with contextlib.suppress(Exception):
                    await server_task
                raise ChatGPTPlanOAuthError(
                    "ChatGPT-plan OAuth callback server failed to start on 127.0.0.1. "
                    "Try a different callback port."
                )
            await asyncio.sleep(0.01)

    def _bound_port(self, server: uvicorn.Server, *, fallback_port: int) -> int:
        servers = getattr(server, "servers", None)
        if not servers:
            return fallback_port
        sockets = getattr(servers[0], "sockets", None)
        if not sockets:
            return fallback_port
        return int(sockets[0].getsockname()[1])

    async def _handle_oauth_callback(
        self,
        *,
        request: Request,
        expected_state: str,
        callback_future: asyncio.Future[tuple[str, str]],
        expected_issued_client_id: str | None,
    ) -> HTMLResponse:
        error = request.query_params.get("error")
        received_state = request.query_params.get("state")
        code = request.query_params.get("code")
        callback_client_id = (request.query_params.get("client_id") or "").strip()

        if error:
            self._fail_callback(
                callback_future,
                ChatGPTPlanOAuthError(
                    "ChatGPT-plan authorization was denied or returned an OAuth error."
                ),
            )
            return HTMLResponse(_CALLBACK_FAIL_HTML, status_code=400)

        if not received_state or not secrets.compare_digest(
            received_state, expected_state
        ):
            self._fail_callback(
                callback_future,
                ChatGPTPlanOAuthError(
                    "ChatGPT-plan OAuth state mismatch; aborting authorization."
                ),
            )
            return HTMLResponse(_CALLBACK_FAIL_HTML, status_code=400)

        if not code:
            self._fail_callback(
                callback_future,
                ChatGPTPlanOAuthError(
                    "ChatGPT-plan callback did not include an authorization code."
                ),
            )
            return HTMLResponse(_CALLBACK_FAIL_HTML, status_code=400)

        try:
            issued_client_id = self._resolve_callback_issued_client_id(
                callback_client_id=callback_client_id,
                expected_issued_client_id=expected_issued_client_id,
            )
        except ChatGPTPlanOAuthError as exc:
            self._fail_callback(callback_future, exc)
            return HTMLResponse(_CALLBACK_FAIL_HTML, status_code=400)

        if not callback_future.done():
            callback_future.set_result((code, issued_client_id))
        return HTMLResponse(_CALLBACK_OK_HTML, status_code=200)

    def _resolve_callback_issued_client_id(
        self,
        *,
        callback_client_id: str,
        expected_issued_client_id: str | None,
    ) -> str:
        if expected_issued_client_id is None:
            if not callback_client_id or callback_client_id == DYNAMIC_CLIENT_ID:
                raise ChatGPTPlanOAuthError(
                    "ChatGPT-plan new-registration callback is missing the issued "
                    "client ID. Registration is incomplete."
                )
            return callback_client_id

        if callback_client_id == DYNAMIC_CLIENT_ID:
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan reauthorization returned a conflicting client ID."
            )
        if callback_client_id and callback_client_id != expected_issued_client_id:
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan reauthorization returned a conflicting client ID."
            )
        return expected_issued_client_id

    def _fail_callback(
        self,
        callback_future: asyncio.Future[tuple[str, str]],
        error: ChatGPTPlanOAuthError,
    ) -> None:
        if not callback_future.done():
            callback_future.set_exception(error)

    async def _exchange_authorization_code(
        self,
        *,
        code: str,
        issued_client_id: str,
        redirect_uri: str,
        code_verifier: str,
    ) -> dict[str, Any]:
        assert_url_safe_for_egress(SIWC_TOKEN_URL)
        form = {
            "grant_type": "authorization_code",
            "code": code,
            "redirect_uri": redirect_uri,
            "client_id": issued_client_id,
            "code_verifier": code_verifier,
            "resource": SIWC_RESOURCE,
        }
        headers = {"Content-Type": "application/x-www-form-urlencoded"}
        try:
            if self._http_client is None:
                async with httpx.AsyncClient() as client:
                    response = await client.post(
                        SIWC_TOKEN_URL,
                        data=form,
                        headers=headers,
                        timeout=TOKEN_EXCHANGE_TIMEOUT_SECONDS,
                    )
            else:
                response = await self._http_client.post(
                    SIWC_TOKEN_URL,
                    data=form,
                    headers=headers,
                    timeout=TOKEN_EXCHANGE_TIMEOUT_SECONDS,
                )
        except httpx.HTTPError as exc:
            logger.debug(
                "ChatGPT-plan token exchange HTTP error",
                exc_info=True,
            )
            raise ChatGPTPlanOAuthError("ChatGPT-plan token exchange failed.") from exc

        if response.status_code >= 400:
            logger.debug(
                "ChatGPT-plan token exchange returned HTTP %s",
                response.status_code,
            )
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan token exchange failed.",
                details={"status_code": response.status_code},
            )

        try:
            payload = response.json()
        except ValueError as exc:
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan token exchange response is not valid JSON."
            ) from exc
        if not isinstance(payload, dict):
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan token exchange response is not a JSON object."
            )
        return payload

    async def _profile_from_validated_tokens(
        self,
        *,
        profile_id: str,
        issued_client_id: str,
        tokens: Mapping[str, Any],
        nonce: str,
        existing: ChatGPTPlanProfile | None = None,
    ) -> ChatGPTPlanProfile:
        redacted = redact_chatgpt_plan_mapping(tokens)
        access_token = tokens.get("access_token")
        if not isinstance(access_token, str) or not access_token:
            logger.debug(
                "ChatGPT-plan token payload keys after redaction: %s",
                sorted(redacted.keys()),
            )
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan token response is missing credentials."
            )

        refresh_token = tokens.get("refresh_token")
        refresh_value = refresh_token if isinstance(refresh_token, str) else None
        id_token = tokens.get("id_token")
        if not isinstance(id_token, str) or not id_token.strip():
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan token response is missing an ID token."
            )

        granted_scopes = _parse_granted_scopes(tokens.get("scope"))
        try:
            identity = await self._oidc.validate_id_token(
                id_token=id_token,
                expected_audience=issued_client_id,
                expected_nonce=nonce,
                access_token=access_token,
            )
        except ChatGPTPlanOidcError as exc:
            raise ChatGPTPlanOAuthError(
                exc.message,
                details=exc.details,
                status_code=exc.status_code,
            ) from exc

        if existing is not None and (
            identity.subject != existing.subject
            or identity.issuer != existing.issuer
            or issued_client_id != existing.issued_client_id
        ):
            raise ChatGPTPlanOAuthError(
                "ChatGPT-plan reauthorization identity conflicts with the "
                "selected profile. The callback or ID token resolved to a "
                "different subject or registration."
            )

        now = _utc_now()
        access_expires: datetime | None = None
        expires_in = tokens.get("expires_in")
        if isinstance(expires_in, int | float):
            access_expires = now + timedelta(seconds=int(expires_in))

        status: ChatGPTPlanProfileStatus = (
            "ready"
            if has_chatgpt_plan_use_scope(granted_scopes)
            else "missing_plan_scope"
        )
        created_at = existing.created_at if existing is not None else now
        return ChatGPTPlanProfile(
            profile_id=profile_id,
            issued_client_id=issued_client_id,
            issuer=identity.issuer,
            subject=identity.subject,
            email=identity.email,
            display_name=identity.display_name,
            access_token=access_token,
            refresh_token=refresh_value,
            id_token=id_token,
            granted_scopes=granted_scopes,
            resource=SIWC_RESOURCE,
            access_token_expires_at=access_expires,
            status=status,
            created_at=created_at,
            updated_at=now,
        )


def _parse_granted_scopes(scope_raw: Any) -> tuple[str, ...]:
    if isinstance(scope_raw, str) and scope_raw.strip():
        return tuple(scope_raw.split())
    if isinstance(scope_raw, list | tuple):
        return tuple(str(item) for item in scope_raw if str(item).strip())
    return ()


__all__ = [
    "AGENT_NAME_HINT",
    "CALLBACK_PATH",
    "DYNAMIC_CLIENT_ID",
    "LOOPBACK_HOST",
    "PLAN_USE_SCOPE",
    "REQUIRED_SCOPES",
    "SIWC_AUTHORIZE_URL",
    "SIWC_RESOURCE",
    "SIWC_TOKEN_URL",
    "ChatGPTPlanAuthorizationRequest",
    "ChatGPTPlanOAuthError",
    "ChatGPTPlanOAuthService",
    "IChatGPTPlanOAuthService",
    "build_redirect_uri",
]
