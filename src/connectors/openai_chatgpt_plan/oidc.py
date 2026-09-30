"""OIDC discovery/JWKS validation for ChatGPT-plan ID tokens.

Uses Authlib ``CodeIDToken`` so signature, issuer, audience, expiration, and
nonce are verified against OpenAI's published metadata. Unverified JWT claim
extraction is not used.
"""

from __future__ import annotations

import asyncio
import logging
import time
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any

import httpx
from authlib.jose import JsonWebKey, JsonWebToken  # type: ignore[import-untyped]
from authlib.jose.errors import (  # type: ignore[import-untyped]
    BadSignatureError,
    DecodeError,
    ExpiredTokenError,
    InvalidClaimError,
    JoseError,
    MissingClaimError,
)
from authlib.oidc.core import CodeIDToken  # type: ignore[import-untyped]

from src.core.common.exceptions import LLMProxyError
from src.core.common.logging_utils import DEFAULT_REDACTED_FIELDS, redact_dict
from src.core.url_safety import assert_url_safe_for_egress

logger = logging.getLogger(__name__)

SIWC_OIDC_ISSUER = "https://auth.openai.com"
SIWC_OIDC_DISCOVERY_URL = "https://auth.openai.com/.well-known/openid-configuration"
PLAN_USE_SCOPE = "chatgpt.tokens.use.direct"
DEFAULT_OIDC_METADATA_TTL_SECONDS = 3600.0
DEFAULT_OIDC_HTTP_TIMEOUT_SECONDS = 15.0
DEFAULT_OIDC_LEEWAY_SECONDS = 60

_OIDC_REDACT_FIELDS = set(DEFAULT_REDACTED_FIELDS) | {
    "access_token",
    "refresh_token",
    "id_token",
}

_CLAIM_ERROR_LABELS = {
    "iss": "issuer",
    "aud": "audience",
    "nonce": "nonce",
    "exp": "expiration",
    "sub": "subject",
}


def _redact_oidc_mapping(data: Mapping[str, Any] | None) -> dict[str, Any]:
    if not data:
        return {}
    return redact_dict(dict(data), redacted_fields=_OIDC_REDACT_FIELDS)


class ChatGPTPlanOidcError(LLMProxyError):
    """Fail-closed ChatGPT-plan OIDC/JWKS validation error."""

    def __init__(
        self,
        message: str,
        details: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        status_code = kwargs.pop("status_code", 401)
        super().__init__(
            message,
            details=_redact_oidc_mapping(details),
            status_code=status_code,
            **kwargs,
        )


@dataclass(frozen=True)
class ValidatedOidcIdentity:
    """Cryptographically verified SIWC ID-token identity."""

    issuer: str
    subject: str
    audience: str
    email: str | None = None
    display_name: str | None = None


def has_chatgpt_plan_use_scope(scopes: tuple[str, ...] | list[str]) -> bool:
    """Return True when granted scopes include ChatGPT plan-funded inference."""

    return PLAN_USE_SCOPE in scopes


class ChatGPTPlanOidcValidator:
    """Fetch OpenAI OIDC metadata/JWKS and validate ID tokens with Authlib."""

    def __init__(
        self,
        *,
        http_client: httpx.AsyncClient | None = None,
        metadata_ttl_seconds: float = DEFAULT_OIDC_METADATA_TTL_SECONDS,
        discovery_url: str = SIWC_OIDC_DISCOVERY_URL,
        expected_issuer: str = SIWC_OIDC_ISSUER,
        leeway_seconds: int = DEFAULT_OIDC_LEEWAY_SECONDS,
    ) -> None:
        self._http_client = http_client
        self._metadata_ttl_seconds = metadata_ttl_seconds
        self._discovery_url = discovery_url
        self._expected_issuer = expected_issuer
        self._leeway_seconds = leeway_seconds
        self._lock = asyncio.Lock()
        self._metadata: dict[str, Any] | None = None
        self._metadata_expires_at = 0.0
        self._jwks: dict[str, Any] | None = None
        self._jwks_uri: str | None = None
        self._jwks_expires_at = 0.0

    async def validate_id_token(
        self,
        *,
        id_token: str,
        expected_audience: str,
        expected_nonce: str,
        access_token: str | None = None,
    ) -> ValidatedOidcIdentity:
        if not isinstance(id_token, str) or not id_token.strip():
            raise ChatGPTPlanOidcError(
                "ChatGPT-plan token response is missing an ID token."
            )
        audience = expected_audience.strip()
        if not audience:
            raise ChatGPTPlanOidcError(
                "ChatGPT-plan ID token audience (issued client ID) is required."
            )
        nonce = expected_nonce.strip()
        if not nonce:
            raise ChatGPTPlanOidcError(
                "ChatGPT-plan ID token nonce is missing from the "
                "authorization attempt."
            )

        metadata = await self._load_discovery_metadata()
        jwks = await self._load_jwks(metadata)
        algorithms = self._signing_algorithms(metadata)
        codec = JsonWebToken(algorithms)
        claims_params: dict[str, Any] = {
            "nonce": nonce,
            "client_id": audience,
        }
        if isinstance(access_token, str) and access_token:
            claims_params["access_token"] = access_token

        try:
            keys = JsonWebKey.import_key_set(jwks)
            claims = codec.decode(
                id_token,
                keys,
                claims_cls=CodeIDToken,
                claims_options={
                    "iss": {
                        "essential": True,
                        "values": [self._expected_issuer],
                    },
                    "aud": {"essential": True, "values": [audience]},
                    "sub": {"essential": True},
                    "exp": {"essential": True},
                    "nonce": {"essential": True},
                },
                claims_params=claims_params,
            )
            claims.validate(leeway=self._leeway_seconds)
        except ChatGPTPlanOidcError:
            raise
        except Exception as exc:
            raise self._map_jose_error(exc) from exc

        subject = claims.get("sub")
        if not isinstance(subject, str) or not subject.strip():
            raise ChatGPTPlanOidcError(
                "ChatGPT-plan ID token subject validation failed."
            )
        email = claims.get("email")
        display_name = claims.get("name")
        return ValidatedOidcIdentity(
            issuer=self._expected_issuer,
            subject=subject.strip(),
            audience=audience,
            email=email.strip() if isinstance(email, str) and email.strip() else None,
            display_name=(
                display_name.strip()
                if isinstance(display_name, str) and display_name.strip()
                else None
            ),
        )

    def _signing_algorithms(self, metadata: Mapping[str, Any]) -> list[str]:
        raw = metadata.get("id_token_signing_alg_values_supported")
        algorithms: list[str] = []
        if isinstance(raw, list):
            for item in raw:
                if isinstance(item, str) and item and item.lower() != "none":
                    algorithms.append(item)
        return algorithms or ["RS256"]

    async def _load_discovery_metadata(self) -> dict[str, Any]:
        async with self._lock:
            now = time.monotonic()
            if self._metadata is not None and now < self._metadata_expires_at:
                return self._metadata
        metadata = await self._fetch_json_object(self._discovery_url)
        issuer = metadata.get("issuer")
        if issuer != self._expected_issuer:
            raise ChatGPTPlanOidcError(
                "ChatGPT-plan OIDC discovery issuer does not match OpenAI."
            )
        jwks_uri = metadata.get("jwks_uri")
        if not isinstance(jwks_uri, str) or not jwks_uri.strip():
            raise ChatGPTPlanOidcError(
                "ChatGPT-plan OIDC discovery is missing jwks_uri."
            )
        async with self._lock:
            self._metadata = metadata
            self._metadata_expires_at = time.monotonic() + self._metadata_ttl_seconds
            if self._jwks_uri != jwks_uri:
                self._jwks = None
                self._jwks_expires_at = 0.0
                self._jwks_uri = jwks_uri
            return metadata

    async def _load_jwks(self, metadata: Mapping[str, Any]) -> dict[str, Any]:
        jwks_uri = metadata.get("jwks_uri")
        if not isinstance(jwks_uri, str) or not jwks_uri.strip():
            raise ChatGPTPlanOidcError(
                "ChatGPT-plan OIDC discovery is missing jwks_uri."
            )
        async with self._lock:
            now = time.monotonic()
            if (
                self._jwks is not None
                and self._jwks_uri == jwks_uri
                and now < self._jwks_expires_at
            ):
                return self._jwks
        jwks = await self._fetch_json_object(jwks_uri)
        keys = jwks.get("keys")
        if not isinstance(keys, list) or not keys:
            raise ChatGPTPlanOidcError(
                "ChatGPT-plan OIDC JWKS document does not contain keys."
            )
        async with self._lock:
            self._jwks = jwks
            self._jwks_uri = jwks_uri
            self._jwks_expires_at = time.monotonic() + self._metadata_ttl_seconds
            return jwks

    async def _fetch_json_object(self, url: str) -> dict[str, Any]:
        assert_url_safe_for_egress(url)
        try:
            if self._http_client is None:
                async with httpx.AsyncClient() as client:
                    response = await client.get(
                        url, timeout=DEFAULT_OIDC_HTTP_TIMEOUT_SECONDS
                    )
            else:
                response = await self._http_client.get(
                    url, timeout=DEFAULT_OIDC_HTTP_TIMEOUT_SECONDS
                )
        except httpx.HTTPError as exc:
            logger.debug("ChatGPT-plan OIDC HTTP error for %s", url, exc_info=True)
            raise ChatGPTPlanOidcError(
                "ChatGPT-plan OIDC metadata request failed."
            ) from exc
        if response.status_code >= 400:
            raise ChatGPTPlanOidcError(
                "ChatGPT-plan OIDC metadata request failed.",
                details={"status_code": response.status_code},
            )
        try:
            payload = response.json()
        except ValueError as exc:
            raise ChatGPTPlanOidcError(
                "ChatGPT-plan OIDC metadata response is not valid JSON."
            ) from exc
        if not isinstance(payload, dict):
            raise ChatGPTPlanOidcError(
                "ChatGPT-plan OIDC metadata response is not a JSON object."
            )
        return payload

    def _map_jose_error(self, exc: BaseException) -> ChatGPTPlanOidcError:
        if isinstance(exc, ChatGPTPlanOidcError):
            return exc
        if isinstance(exc, BadSignatureError):
            return ChatGPTPlanOidcError(
                "ChatGPT-plan ID token signature verification failed."
            )
        if isinstance(exc, ExpiredTokenError):
            return ChatGPTPlanOidcError("ChatGPT-plan ID token is expired.")
        if isinstance(exc, InvalidClaimError):
            claim = getattr(exc, "claim_name", "") or ""
            label = _CLAIM_ERROR_LABELS.get(claim, claim or "claim")
            return ChatGPTPlanOidcError(
                f"ChatGPT-plan ID token {label} validation failed."
            )
        if isinstance(exc, MissingClaimError):
            text = str(exc)
            for claim, label in _CLAIM_ERROR_LABELS.items():
                if claim in text:
                    return ChatGPTPlanOidcError(
                        f"ChatGPT-plan ID token {label} validation failed."
                    )
            return ChatGPTPlanOidcError(
                "ChatGPT-plan ID token is missing a required claim."
            )
        if isinstance(exc, DecodeError | JoseError):
            logger.debug("ChatGPT-plan ID token JOSE error", exc_info=True)
            return ChatGPTPlanOidcError(
                "ChatGPT-plan ID token signature verification failed."
            )
        logger.debug("ChatGPT-plan ID token validation failed", exc_info=True)
        return ChatGPTPlanOidcError("ChatGPT-plan ID token validation failed.")


__all__ = [
    "DEFAULT_OIDC_LEEWAY_SECONDS",
    "DEFAULT_OIDC_METADATA_TTL_SECONDS",
    "PLAN_USE_SCOPE",
    "SIWC_OIDC_DISCOVERY_URL",
    "SIWC_OIDC_ISSUER",
    "ChatGPTPlanOidcError",
    "ChatGPTPlanOidcValidator",
    "ValidatedOidcIdentity",
    "has_chatgpt_plan_use_scope",
]
