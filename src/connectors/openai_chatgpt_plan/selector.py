"""Explicit ChatGPT-plan profile selection without automatic rotation.

Precedence:
1. request or backend-instance explicit profile id;
2. backend config ``extra.chatgpt_plan.profile_id``;
3. operator-designated default, or the only saved profile;
4. otherwise a typed error that lists available profile IDs.

Quota exhaustion and authentication failure never select a different profile.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from typing import Any, Protocol

from src.connectors.openai_chatgpt_plan.config import ChatGPTPlanConfig
from src.connectors.openai_chatgpt_plan.models import (
    ChatGPTPlanProfile,
    IChatGPTPlanProfileStore,
    redact_chatgpt_plan_mapping,
)
from src.core.common.exceptions import LLMProxyError

_CHATGPT_PLAN_EXTRA_KEY = "chatgpt_plan"


def _redact_details(details: Mapping[str, Any] | None) -> dict[str, Any]:
    if not details:
        return {}
    return redact_chatgpt_plan_mapping(details)


class ChatGPTPlanProfileSelectionError(LLMProxyError):
    """Ambiguous, missing, or unknown ChatGPT-plan profile selection."""

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


class IChatGPTPlanProfileSelector(Protocol):
    async def resolve(
        self,
        *,
        request_profile_id: str | None = None,
        backend_instance_profile_id: str | None = None,
        backend_extra: Mapping[str, Any] | None = None,
        config: ChatGPTPlanConfig | None = None,
        designated_default_profile_id: str | None = None,
    ) -> ChatGPTPlanProfile: ...

    def retain_profile_on_quota_or_auth_failure(
        self,
        selected_profile_id: str,
        *,
        available_profile_ids: Sequence[str] | None = None,
        reason: str | None = None,
    ) -> str: ...


def _normalize_profile_id(value: str | None) -> str | None:
    if value is None:
        return None
    stripped = value.strip()
    return stripped or None


def _profile_id_from_backend_extra(extra: Mapping[str, Any] | None) -> str | None:
    if extra is None:
        return None
    section: object = extra.get(_CHATGPT_PLAN_EXTRA_KEY, extra)
    if not isinstance(section, Mapping):
        return None
    raw = section.get("profile_id")
    if isinstance(raw, str):
        return _normalize_profile_id(raw)
    return None


def retain_profile_on_quota_or_auth_failure(
    selected_profile_id: str,
    *,
    available_profile_ids: Sequence[str] | None = None,
    reason: str | None = None,
) -> str:
    """Keep the selected profile after quota or auth failure.

    ``available_profile_ids`` and ``reason`` are accepted so callers can pass
    pool/error context; they must not change the returned profile id.
    """

    del available_profile_ids, reason
    return selected_profile_id


class ChatGPTPlanProfileSelector:
    """Resolve one stored SIWC profile using explicit selection semantics."""

    def __init__(self, profile_store: IChatGPTPlanProfileStore) -> None:
        self._profile_store = profile_store

    def retain_profile_on_quota_or_auth_failure(
        self,
        selected_profile_id: str,
        *,
        available_profile_ids: Sequence[str] | None = None,
        reason: str | None = None,
    ) -> str:
        return retain_profile_on_quota_or_auth_failure(
            selected_profile_id,
            available_profile_ids=available_profile_ids,
            reason=reason,
        )

    async def resolve(
        self,
        *,
        request_profile_id: str | None = None,
        backend_instance_profile_id: str | None = None,
        backend_extra: Mapping[str, Any] | None = None,
        config: ChatGPTPlanConfig | None = None,
        designated_default_profile_id: str | None = None,
    ) -> ChatGPTPlanProfile:
        summaries = await self._profile_store.list_profiles()
        available_ids = [item.profile_id for item in summaries]
        selected_id = self._select_profile_id(
            available_ids=available_ids,
            request_profile_id=request_profile_id,
            backend_instance_profile_id=backend_instance_profile_id,
            backend_extra=backend_extra,
            config=config,
            designated_default_profile_id=designated_default_profile_id,
        )
        profile = await self._profile_store.load(selected_id)
        if profile is None:
            raise self._unknown_profile_error(selected_id, available_ids)
        return profile

    def _select_profile_id(
        self,
        *,
        available_ids: Sequence[str],
        request_profile_id: str | None,
        backend_instance_profile_id: str | None,
        backend_extra: Mapping[str, Any] | None,
        config: ChatGPTPlanConfig | None,
        designated_default_profile_id: str | None,
    ) -> str:
        explicit = _normalize_profile_id(request_profile_id) or _normalize_profile_id(
            backend_instance_profile_id
        )
        if explicit is not None:
            return explicit

        config_profile_id = None
        if config is not None:
            config_profile_id = _normalize_profile_id(config.profile_id)
        if config_profile_id is None:
            config_profile_id = _profile_id_from_backend_extra(backend_extra)
        if config_profile_id is not None:
            return config_profile_id

        designated = _normalize_profile_id(designated_default_profile_id)
        if designated is not None:
            return designated
        if len(available_ids) == 1:
            return available_ids[0]
        raise self._ambiguous_or_empty_error(available_ids)

    def _unknown_profile_error(
        self, profile_id: str, available_ids: Sequence[str]
    ) -> ChatGPTPlanProfileSelectionError:
        listed = _format_available_ids(available_ids)
        return ChatGPTPlanProfileSelectionError(
            f"ChatGPT-plan profile '{profile_id}' was not found. "
            f"Available profile IDs: {listed}.",
            details={
                "requested_profile_id": profile_id,
                "available_profile_ids": list(available_ids),
            },
            status_code=404,
        )

    def _ambiguous_or_empty_error(
        self, available_ids: Sequence[str]
    ) -> ChatGPTPlanProfileSelectionError:
        if not available_ids:
            return ChatGPTPlanProfileSelectionError(
                "No ChatGPT-plan profiles are saved. Add a profile with Sign in "
                "with ChatGPT, then select it explicitly.",
                details={"available_profile_ids": []},
            )
        listed = _format_available_ids(available_ids)
        return ChatGPTPlanProfileSelectionError(
            "ChatGPT-plan profile selection is ambiguous because multiple "
            f"profiles are saved and none was selected. Available profile IDs: "
            f"{listed}. Set extra.chatgpt_plan.profile_id or pass an explicit "
            "request/backend-instance profile.",
            details={"available_profile_ids": list(available_ids)},
        )


def _format_available_ids(available_ids: Sequence[str]) -> str:
    if not available_ids:
        return "(none)"
    return ", ".join(available_ids)


__all__ = [
    "ChatGPTPlanProfileSelectionError",
    "ChatGPTPlanProfileSelector",
    "IChatGPTPlanProfileSelector",
    "retain_profile_on_quota_or_auth_failure",
]
