"""Explicit SIWC profile export/import that never overwrites destination host identity.

Export may include registration and token material plus optional source-host
provenance. Import treats source-host fields as provenance only and never writes
them into the destination ``host.json`` ``ext_agent_host_id``.

``.codex/auth.json`` and legacy ``openai-codex`` managed-account files are not
valid SIWC profiles and are never auto-imported.
"""

from __future__ import annotations

import json
from collections.abc import Mapping
from pathlib import Path
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict
from pydantic import ValidationError as PydanticValidationError

from src.connectors.openai_chatgpt_plan.models import (
    ChatGPTPlanProfile,
    IChatGPTPlanHostStore,
    IChatGPTPlanProfileStore,
    is_valid_profile_id,
    redact_chatgpt_plan_mapping,
)
from src.core.common.exceptions import LLMProxyError

PROFILE_EXPORT_KIND: Literal["openai-chatgpt-plan-siwc-profile"] = (
    "openai-chatgpt-plan-siwc-profile"
)
PROFILE_EXPORT_SCHEMA_VERSION = 1
SOURCE_HOST_PROVENANCE_ROLE: Literal["provenance"] = "provenance"
_LEGACY_MANAGED_ACCOUNT_DIR = "openai_codex_oauth_accounts"
_CODEX_DIR_NAME = ".codex"
_CODEX_AUTH_FILENAME = "auth.json"


def _redact_details(details: Mapping[str, Any] | None) -> dict[str, Any]:
    if not details:
        return {}
    return redact_chatgpt_plan_mapping(details)


class ChatGPTPlanImportExportError(LLMProxyError):
    """Invalid, unsupported, or unsafe ChatGPT-plan profile import/export."""

    def __init__(
        self,
        message: str,
        details: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        status_code = kwargs.pop("status_code", 400)
        super().__init__(
            message,
            details=_redact_details(details),
            status_code=status_code,
            **kwargs,
        )


class ChatGPTPlanSourceHostProvenance(BaseModel):
    """Source host identity retained only as provenance, never applied to dest."""

    model_config = ConfigDict(frozen=True, extra="ignore")

    ext_agent_host_id: str | None = None
    role: Literal["provenance"] = SOURCE_HOST_PROVENANCE_ROLE
    overwrite_destination: Literal[False] = False
    apply_to_destination_host: Literal[False] = False


class ChatGPTPlanProfileExportEnvelope(BaseModel):
    """Secret-bearing SIWC profile envelope for explicit operator import/export."""

    model_config = ConfigDict(frozen=True, extra="ignore")

    schema_version: int = PROFILE_EXPORT_SCHEMA_VERSION
    kind: Literal["openai-chatgpt-plan-siwc-profile"] = PROFILE_EXPORT_KIND
    profile: ChatGPTPlanProfile
    source_host_provenance: ChatGPTPlanSourceHostProvenance | None = None


def is_legacy_codex_credential_path(path: Path | str) -> bool:
    """Return True for ``.codex/auth.json`` or openai-codex managed-account paths."""

    resolved = Path(path)
    parts = resolved.parts
    if _LEGACY_MANAGED_ACCOUNT_DIR in parts:
        return True
    if _CODEX_DIR_NAME in parts and resolved.name == _CODEX_AUTH_FILENAME:
        return True
    posix = resolved.as_posix().replace("\\", "/")
    return posix.endswith(f"{_CODEX_DIR_NAME}/{_CODEX_AUTH_FILENAME}")


def _looks_like_legacy_codex_credentials(payload: Mapping[str, Any]) -> bool:
    kind = payload.get("kind")
    if kind == PROFILE_EXPORT_KIND:
        return False
    if "account_id" in payload or "chatgpt_account_id" in payload:
        return True
    tokens = payload.get("tokens")
    if isinstance(tokens, Mapping) and (
        "access_token" in tokens or "refresh_token" in tokens or "id_token" in tokens
    ):
        return True
    return isinstance(kind, str) and "codex" in kind.lower()


def _legacy_import_rejected_error(
    *, path_name: str | None = None
) -> ChatGPTPlanImportExportError:
    details: dict[str, Any] = {}
    if path_name:
        details["path"] = path_name
    return ChatGPTPlanImportExportError(
        "Refusing to import .codex/auth.json or openai-codex managed-account "
        "files as SIWC profiles. Authorize with Sign in with ChatGPT or import "
        "an explicit openai-chatgpt-plan export envelope.",
        details=details or None,
    )


def build_chatgpt_plan_profile_export(
    profile: ChatGPTPlanProfile,
    *,
    source_host_id: str | None = None,
) -> dict[str, Any]:
    """Build a secret-bearing envelope that does not instruct host overwrite."""

    envelope: dict[str, Any] = {
        "schema_version": PROFILE_EXPORT_SCHEMA_VERSION,
        "kind": PROFILE_EXPORT_KIND,
        "profile": profile.model_dump(mode="json"),
    }
    if source_host_id:
        envelope["source_host_provenance"] = ChatGPTPlanSourceHostProvenance(
            ext_agent_host_id=source_host_id,
            role=SOURCE_HOST_PROVENANCE_ROLE,
            overwrite_destination=False,
            apply_to_destination_host=False,
        ).model_dump(mode="json")
    return envelope


async def export_chatgpt_plan_profile(
    profile_store: IChatGPTPlanProfileStore,
    profile_id: str,
    *,
    host_store: IChatGPTPlanHostStore | None = None,
) -> dict[str, Any]:
    """Export one saved SIWC profile, optionally attaching source-host provenance."""

    profile = await profile_store.load(profile_id)
    if profile is None:
        raise ChatGPTPlanImportExportError(
            f"ChatGPT-plan profile '{profile_id}' was not found.",
            details={"profile_id": profile_id},
            status_code=404,
        )
    source_host_id: str | None = None
    if host_store is not None:
        host = await host_store.get_or_create()
        source_host_id = host.ext_agent_host_id
    return build_chatgpt_plan_profile_export(profile, source_host_id=source_host_id)


def _extract_profile_payload(
    payload: Mapping[str, Any], *, dest_profile_id: str
) -> ChatGPTPlanProfile:
    if _looks_like_legacy_codex_credentials(payload):
        raise _legacy_import_rejected_error()

    kind = payload.get("kind")
    profile_obj: Mapping[str, Any]
    if kind == PROFILE_EXPORT_KIND:
        nested = payload.get("profile")
        if not isinstance(nested, Mapping):
            raise ChatGPTPlanImportExportError(
                "ChatGPT-plan profile export envelope is missing a profile object.",
                details={"dest_profile_id": dest_profile_id},
            )
        profile_obj = nested
    elif "issued_client_id" in payload and "issuer" in payload and "subject" in payload:
        profile_obj = payload
    else:
        raise ChatGPTPlanImportExportError(
            "Payload is not an explicit openai-chatgpt-plan SIWC profile export. "
            "Legacy .codex/auth.json and openai-codex managed-account files cannot "
            "be imported as SIWC profiles.",
            details={"dest_profile_id": dest_profile_id},
        )

    merged = dict(profile_obj)
    merged["profile_id"] = dest_profile_id
    try:
        return ChatGPTPlanProfile.model_validate(merged)
    except PydanticValidationError as exc:
        raise ChatGPTPlanImportExportError(
            "ChatGPT-plan profile import payload is not a valid SIWC profile.",
            details={"dest_profile_id": dest_profile_id},
        ) from exc


async def import_chatgpt_plan_profile(
    payload: Mapping[str, Any],
    *,
    dest_profile_id: str,
    profile_store: IChatGPTPlanProfileStore,
    host_store: IChatGPTPlanHostStore,
) -> ChatGPTPlanProfile:
    """Import a SIWC profile into dest storage without replacing dest host ID.

    Source ``ext_agent_host_id`` and any overwrite flags on the envelope are
    ignored. Destination host identity is loaded or minted only via
    ``host_store.get_or_create``.
    """

    if not is_valid_profile_id(dest_profile_id):
        raise ChatGPTPlanImportExportError(
            "Destination profile_id must be a sanitized local alias, not an "
            "email or filesystem path.",
            details={"dest_profile_id": dest_profile_id},
        )
    profile = _extract_profile_payload(payload, dest_profile_id=dest_profile_id)
    await host_store.get_or_create()
    await profile_store.save_atomic(profile)
    return profile


def _parse_json_object(raw: str) -> dict[str, Any]:
    try:
        parsed: object = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ChatGPTPlanImportExportError(
            "ChatGPT-plan profile import file is not valid JSON.",
        ) from exc
    if not isinstance(parsed, dict):
        raise ChatGPTPlanImportExportError(
            "ChatGPT-plan profile import file must contain a JSON object.",
        )
    return parsed


async def import_chatgpt_plan_profile_from_path(
    path: Path | str,
    *,
    dest_profile_id: str,
    profile_store: IChatGPTPlanProfileStore,
    host_store: IChatGPTPlanHostStore,
) -> ChatGPTPlanProfile:
    """Import from an explicit operator-selected file; never auto-discover Codex."""

    source = Path(path)
    if is_legacy_codex_credential_path(source):
        raise _legacy_import_rejected_error(path_name=source.name)
    try:
        raw = source.read_text(encoding="utf-8")
    except OSError as exc:
        raise ChatGPTPlanImportExportError(
            "Failed to read ChatGPT-plan profile import file.",
            details={"path": source.name},
        ) from exc
    payload = _parse_json_object(raw)
    return await import_chatgpt_plan_profile(
        payload,
        dest_profile_id=dest_profile_id,
        profile_store=profile_store,
        host_store=host_store,
    )


__all__ = [
    "PROFILE_EXPORT_KIND",
    "PROFILE_EXPORT_SCHEMA_VERSION",
    "SOURCE_HOST_PROVENANCE_ROLE",
    "ChatGPTPlanImportExportError",
    "ChatGPTPlanProfileExportEnvelope",
    "ChatGPTPlanSourceHostProvenance",
    "build_chatgpt_plan_profile_export",
    "export_chatgpt_plan_profile",
    "import_chatgpt_plan_profile",
    "import_chatgpt_plan_profile_from_path",
    "is_legacy_codex_credential_path",
]
