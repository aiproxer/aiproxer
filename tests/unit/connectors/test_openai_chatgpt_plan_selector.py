"""Unit tests for ChatGPT-plan explicit profile selection (task 2.5).

Covers multi-profile identity isolation (same email stays distinct), selector
precedence (request/backend-instance, extra.chatgpt_plan.profile_id, single or
designated default), unambiguous failure with available IDs, and no automatic
profile rotation on quota or authentication failure.
"""

from __future__ import annotations

from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from unittest.mock import patch

import pytest
from src.core.common.exceptions import LLMProxyError

ACCESS_TOKEN_WORK = "access-token-work-value"
ACCESS_TOKEN_PERSONAL = "access-token-personal-value"
SHARED_EMAIL = "same-user@example.com"
ISSUER = "https://auth.openai.com"


def _now() -> datetime:
    return datetime.now(timezone.utc)


def _profile(**overrides: Any):
    from src.connectors.openai_chatgpt_plan.models import ChatGPTPlanProfile

    now = _now()
    payload: dict[str, Any] = {
        "schema_version": 1,
        "profile_id": "alice-work",
        "issued_client_id": "oaiapp-work",
        "issuer": ISSUER,
        "subject": "subject-work",
        "email": SHARED_EMAIL,
        "display_name": "Same User",
        "access_token": ACCESS_TOKEN_WORK,
        "refresh_token": "refresh-token-work-value",
        "id_token": "id-token-work-value",
        "granted_scopes": (
            "openid",
            "profile",
            "email",
            "offline_access",
            "chatgpt.tokens.use.direct",
        ),
        "resource": "https://api.openai.com/v1",
        "status": "ready",
        "created_at": now,
        "updated_at": now,
    }
    payload.update(overrides)
    return ChatGPTPlanProfile(**payload)


def _profile_store(tmp_path: Path):
    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

    return ChatGPTPlanProfileStore(tmp_path / "profiles")


async def _seed_two_same_email_profiles(tmp_path: Path):
    store = _profile_store(tmp_path)
    work = _profile(
        profile_id="alice-work",
        subject="subject-work",
        issued_client_id="oaiapp-work",
        access_token=ACCESS_TOKEN_WORK,
    )
    personal = _profile(
        profile_id="alice-personal",
        subject="subject-personal",
        issued_client_id="oaiapp-personal",
        access_token=ACCESS_TOKEN_PERSONAL,
    )
    await store.save_atomic(work)
    await store.save_atomic(personal)
    return store


def _selector(store):
    from src.connectors.openai_chatgpt_plan.selector import ChatGPTPlanProfileSelector

    return ChatGPTPlanProfileSelector(store)


class TestSameEmailProfilesRemainDistinct:
    @pytest.mark.asyncio
    async def test_two_profiles_with_same_email_remain_distinct(
        self, tmp_path: Path
    ) -> None:
        store = await _seed_two_same_email_profiles(tmp_path)
        selector = _selector(store)

        work = await selector.resolve(request_profile_id="alice-work")
        personal = await selector.resolve(request_profile_id="alice-personal")

        assert work.email == personal.email == SHARED_EMAIL
        assert work.profile_id == "alice-work"
        assert personal.profile_id == "alice-personal"
        assert work.subject == "subject-work"
        assert personal.subject == "subject-personal"
        assert work.issued_client_id != personal.issued_client_id
        assert work.access_token == ACCESS_TOKEN_WORK
        assert personal.access_token == ACCESS_TOKEN_PERSONAL
        assert work.access_token != personal.access_token


class TestSelectorPrecedence:
    @pytest.mark.asyncio
    async def test_request_explicit_profile_id_wins_over_config(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.config import ChatGPTPlanConfig

        store = await _seed_two_same_email_profiles(tmp_path)
        selector = _selector(store)
        config = ChatGPTPlanConfig(profile_id="alice-work")

        selected = await selector.resolve(
            request_profile_id="alice-personal",
            config=config,
        )
        assert selected.profile_id == "alice-personal"
        assert selected.access_token == ACCESS_TOKEN_PERSONAL

    @pytest.mark.asyncio
    async def test_backend_instance_explicit_profile_id(self, tmp_path: Path) -> None:
        store = await _seed_two_same_email_profiles(tmp_path)
        selector = _selector(store)

        selected = await selector.resolve(backend_instance_profile_id="alice-work")
        assert selected.profile_id == "alice-work"
        assert selected.subject == "subject-work"

    @pytest.mark.asyncio
    async def test_request_explicit_overrides_backend_instance(
        self, tmp_path: Path
    ) -> None:
        store = await _seed_two_same_email_profiles(tmp_path)
        selector = _selector(store)

        selected = await selector.resolve(
            request_profile_id="alice-personal",
            backend_instance_profile_id="alice-work",
        )
        assert selected.profile_id == "alice-personal"

    @pytest.mark.asyncio
    async def test_backend_extra_chatgpt_plan_profile_id(self, tmp_path: Path) -> None:
        store = await _seed_two_same_email_profiles(tmp_path)
        selector = _selector(store)

        selected = await selector.resolve(
            backend_extra={"chatgpt_plan": {"profile_id": "alice-work"}},
        )
        assert selected.profile_id == "alice-work"

    @pytest.mark.asyncio
    async def test_config_profile_id_used_when_no_request_selector(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.config import ChatGPTPlanConfig

        store = await _seed_two_same_email_profiles(tmp_path)
        selector = _selector(store)
        config = ChatGPTPlanConfig(profile_id="alice-personal")

        selected = await selector.resolve(config=config)
        assert selected.profile_id == "alice-personal"
        assert selected.access_token == ACCESS_TOKEN_PERSONAL

    @pytest.mark.asyncio
    async def test_exactly_one_profile_is_used_as_operator_default(
        self, tmp_path: Path
    ) -> None:
        store = _profile_store(tmp_path)
        await store.save_atomic(_profile(profile_id="only-one", subject="only-sub"))
        selector = _selector(store)

        selected = await selector.resolve()
        assert selected.profile_id == "only-one"
        assert selected.subject == "only-sub"

    @pytest.mark.asyncio
    async def test_designated_default_used_when_multiple_profiles(
        self, tmp_path: Path
    ) -> None:
        store = await _seed_two_same_email_profiles(tmp_path)
        selector = _selector(store)

        selected = await selector.resolve(
            designated_default_profile_id="alice-work",
        )
        assert selected.profile_id == "alice-work"


class TestAmbiguousSelection:
    @pytest.mark.asyncio
    async def test_multiple_profiles_without_selection_raises_with_available_ids(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.selector import (
            ChatGPTPlanProfileSelectionError,
        )

        store = await _seed_two_same_email_profiles(tmp_path)
        selector = _selector(store)

        with pytest.raises(ChatGPTPlanProfileSelectionError) as exc_info:
            await selector.resolve()

        error = exc_info.value
        assert isinstance(error, LLMProxyError)
        text = str(error)
        assert "alice-work" in text
        assert "alice-personal" in text
        details = error.details or {}
        available = details.get("available_profile_ids") or details.get("available_ids")
        assert available is not None
        assert set(available) == {"alice-work", "alice-personal"}
        assert ACCESS_TOKEN_WORK not in text
        assert ACCESS_TOKEN_PERSONAL not in str(details)

    @pytest.mark.asyncio
    async def test_unknown_explicit_profile_raises_with_available_ids(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.selector import (
            ChatGPTPlanProfileSelectionError,
        )

        store = await _seed_two_same_email_profiles(tmp_path)
        selector = _selector(store)

        with pytest.raises(ChatGPTPlanProfileSelectionError) as exc_info:
            await selector.resolve(request_profile_id="does-not-exist")

        error = exc_info.value
        text = str(error)
        assert "alice-work" in text
        assert "alice-personal" in text
        assert "does-not-exist" in text
        assert ACCESS_TOKEN_WORK not in text

    @pytest.mark.asyncio
    async def test_no_profiles_raises_actionable_error(self, tmp_path: Path) -> None:
        from src.connectors.openai_chatgpt_plan.selector import (
            ChatGPTPlanProfileSelectionError,
        )

        store = _profile_store(tmp_path)
        selector = _selector(store)

        with pytest.raises(ChatGPTPlanProfileSelectionError) as exc_info:
            await selector.resolve()

        message = str(exc_info.value).lower()
        assert "profile" in message
        assert ACCESS_TOKEN_WORK not in str(exc_info.value)


class TestNoAutomaticProfileRotation:
    @pytest.mark.asyncio
    async def test_quota_failure_helper_does_not_switch_profiles(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.selector import (
            retain_profile_on_quota_or_auth_failure,
        )

        store = await _seed_two_same_email_profiles(tmp_path)
        selector = _selector(store)
        selected = await selector.resolve(request_profile_id="alice-work")

        kept = retain_profile_on_quota_or_auth_failure(
            selected.profile_id,
            available_profile_ids=["alice-work", "alice-personal"],
            reason="quota",
        )
        assert kept == "alice-work"

        also_kept = selector.retain_profile_on_quota_or_auth_failure(
            selected.profile_id,
            available_profile_ids=["alice-work", "alice-personal"],
            reason="subscription_sharing_usage_limit_exceeded",
        )
        assert also_kept == "alice-work"

    @pytest.mark.asyncio
    async def test_auth_failure_helper_does_not_switch_profiles(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.selector import (
            retain_profile_on_quota_or_auth_failure,
        )

        store = await _seed_two_same_email_profiles(tmp_path)
        selector = _selector(store)
        selected = await selector.resolve(request_profile_id="alice-work")

        kept = retain_profile_on_quota_or_auth_failure(
            selected.profile_id,
            available_profile_ids=["alice-work", "alice-personal"],
            reason="authentication",
        )
        assert kept == "alice-work"
        assert (
            selector.retain_profile_on_quota_or_auth_failure(
                selected.profile_id,
                available_profile_ids=["alice-personal", "alice-work"],
                reason="invalid_grant",
            )
            == "alice-work"
        )


class TestSelectorDoesNotReadLegacyCodexFiles:
    @pytest.mark.asyncio
    async def test_resolve_does_not_read_codex_auth_or_managed_accounts(
        self, tmp_path: Path
    ) -> None:
        decoy_codex = tmp_path / ".codex" / "auth.json"
        decoy_codex.parent.mkdir(parents=True)
        decoy_codex.write_text(
            '{"access_token": "access-token-work-value", "email": "same-user@example.com"}',
            encoding="utf-8",
        )
        decoy_managed = tmp_path / "openai_codex_oauth_accounts" / "acct.json"
        decoy_managed.parent.mkdir(parents=True)
        decoy_managed.write_text(
            '{"account_id": "legacy", "access_token": "access-token-work-value"}',
            encoding="utf-8",
        )

        store = await _seed_two_same_email_profiles(tmp_path)
        selector = _selector(store)
        opened: list[str] = []
        original_read_text = Path.read_text

        def _track_read(self: Path, *args: Any, **kwargs: Any) -> str:
            opened.append(self.as_posix())
            return original_read_text(self, *args, **kwargs)

        with patch.object(Path, "read_text", _track_read):
            selected = await selector.resolve(request_profile_id="alice-work")

        assert selected.profile_id == "alice-work"
        opened_joined = "\n".join(opened).replace("\\", "/")
        assert ".codex/auth.json" not in opened_joined
        assert "openai_codex_oauth_accounts" not in opened_joined
