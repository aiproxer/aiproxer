"""Value models for ChatGPT-plan host state and SIWC profiles."""

from __future__ import annotations

import re
from collections.abc import Mapping
from datetime import datetime
from typing import Any, Literal, Protocol

from pydantic import BaseModel, ConfigDict, Field, field_validator

from src.core.common.logging_utils import DEFAULT_REDACTED_FIELDS, redact_dict

HOST_SCHEMA_VERSION = 1
PROFILE_SCHEMA_VERSION = 1
PROFILE_ID_PATTERN = r"^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$"
_PROFILE_ID_REGEX = re.compile(PROFILE_ID_PATTERN)

ChatGPTPlanProfileStatus = Literal[
    "ready", "missing_plan_scope", "needs_reauth", "signed_out"
]

CHATGPT_PLAN_SECRET_FIELDS = frozenset({"access_token", "refresh_token", "id_token"})


def redact_chatgpt_plan_mapping(
    data: Mapping[str, Any], mask: str = "***"
) -> dict[str, Any]:
    """Redact SIWC token fields using the shared logging redaction helper."""

    fields = set(DEFAULT_REDACTED_FIELDS) | set(CHATGPT_PLAN_SECRET_FIELDS)
    return redact_dict(dict(data), redacted_fields=fields, mask=mask)


def is_valid_profile_id(profile_id: str) -> bool:
    """Return True when ``profile_id`` is a sanitized local alias, not an email."""

    return bool(_PROFILE_ID_REGEX.fullmatch(profile_id))


class ChatGPTPlanHostState(BaseModel):
    """Schema-versioned stable host identity for one AIProxer installation."""

    model_config = ConfigDict(frozen=True, extra="ignore")

    schema_version: int = HOST_SCHEMA_VERSION
    ext_agent_host_id: str = Field(min_length=16)
    created_at: datetime

    @field_validator("ext_agent_host_id")
    @classmethod
    def _opaque_host_id(cls, value: str) -> str:
        stripped = value.strip()
        if not stripped or "@" in stripped or " " in stripped:
            raise ValueError("ext_agent_host_id must be an opaque non-email value")
        return stripped


class IChatGPTPlanHostStore(Protocol):
    async def get_or_create(self) -> ChatGPTPlanHostState: ...


class ChatGPTPlanProfile(BaseModel):
    """One locally stored Sign in with ChatGPT registration."""

    model_config = ConfigDict(frozen=True, extra="ignore")

    schema_version: int = PROFILE_SCHEMA_VERSION
    profile_id: str
    issued_client_id: str = Field(min_length=1)
    issuer: str = Field(min_length=1)
    subject: str = Field(min_length=1)
    email: str | None = None
    display_name: str | None = None
    access_token: str | None = Field(default=None, repr=False)
    refresh_token: str | None = Field(default=None, repr=False)
    id_token: str | None = Field(default=None, repr=False)
    granted_scopes: tuple[str, ...] = ()
    resource: str = "https://api.openai.com/v1"
    access_token_expires_at: datetime | None = None
    refresh_token_expires_at: datetime | None = None
    status: ChatGPTPlanProfileStatus
    created_at: datetime
    updated_at: datetime

    @field_validator("profile_id")
    @classmethod
    def _validate_profile_id(cls, value: str) -> str:
        if not is_valid_profile_id(value):
            raise ValueError(
                "profile_id must be a local alias matching "
                f"{PROFILE_ID_PATTERN}, not an email or filesystem path"
            )
        return value

    def __repr__(self) -> str:
        redacted = redact_chatgpt_plan_mapping(self.model_dump(mode="json"))
        return f"{self.__class__.__name__}({redacted!r})"

    def __str__(self) -> str:
        return self.__repr__()


class ChatGPTPlanProfileSummary(BaseModel):
    """Operator-safe profile listing row with no token material."""

    model_config = ConfigDict(frozen=True, extra="ignore")

    profile_id: str
    issuer: str
    subject: str
    email: str | None = None
    display_name: str | None = None
    status: ChatGPTPlanProfileStatus
    granted_scopes: tuple[str, ...] = ()
    updated_at: datetime

    @classmethod
    def from_profile(cls, profile: ChatGPTPlanProfile) -> ChatGPTPlanProfileSummary:
        return cls(
            profile_id=profile.profile_id,
            issuer=profile.issuer,
            subject=profile.subject,
            email=profile.email,
            display_name=profile.display_name,
            status=profile.status,
            granted_scopes=profile.granted_scopes,
            updated_at=profile.updated_at,
        )


class IChatGPTPlanProfileStore(Protocol):
    async def list_profiles(self) -> list[ChatGPTPlanProfileSummary]: ...

    async def load(self, profile_id: str) -> ChatGPTPlanProfile | None: ...

    async def save_atomic(self, profile: ChatGPTPlanProfile) -> None: ...

    async def clear_tokens(
        self, profile_id: str, status: ChatGPTPlanProfileStatus
    ) -> None: ...

    async def delete(self, profile_id: str) -> None: ...
