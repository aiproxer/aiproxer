"""Unit tests for ChatGPT-plan host/profile persistence (task 2.1).

Covers stable host ID, per-profile files keyed by local profile ID (not email),
atomic replacement, owner-only permissions fallback, corruption diagnostics,
token redaction, and isolation from legacy Codex auth files.
"""

from __future__ import annotations

import json
import logging
import os
import stat
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from unittest.mock import patch

import pytest
from src.core.common.exceptions import LLMProxyError
from src.core.common.logging_utils import DEFAULT_REDACTED_FIELDS, redact_dict

ACCESS_TOKEN = "access-token-test-value"
REFRESH_TOKEN = "refresh-token-test-value"
ID_TOKEN = "id-token-test-value"
SHARED_EMAIL = "same-user@example.com"
ISSUED_CLIENT_ID = "oaiapp-test-client"
ISSUER = "https://auth.openai.com"


def _now() -> datetime:
    return datetime.now(timezone.utc)


def _profile(**overrides: Any):
    from src.connectors.openai_chatgpt_plan.models import ChatGPTPlanProfile

    now = _now()
    payload: dict[str, Any] = {
        "schema_version": 1,
        "profile_id": "primary",
        "issued_client_id": ISSUED_CLIENT_ID,
        "issuer": ISSUER,
        "subject": "subject-primary",
        "email": SHARED_EMAIL,
        "display_name": "Same User",
        "access_token": ACCESS_TOKEN,
        "refresh_token": REFRESH_TOKEN,
        "id_token": ID_TOKEN,
        "granted_scopes": (
            "openid",
            "profile",
            "email",
            "offline_access",
            "chatgpt.tokens.use.direct",
        ),
        "resource": "https://api.openai.com/v1",
        "access_token_expires_at": now,
        "refresh_token_expires_at": None,
        "status": "ready",
        "created_at": now,
        "updated_at": now,
    }
    payload.update(overrides)
    return ChatGPTPlanProfile(**payload)


def _host_store(tmp_path: Path):
    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanHostStore

    return ChatGPTPlanHostStore(tmp_path / "host.json")


def _profile_store(tmp_path: Path):
    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

    return ChatGPTPlanProfileStore(tmp_path / "profiles")


def _joined_log_text(caplog: pytest.LogCaptureFixture) -> str:
    return "\n".join(record.getMessage() for record in caplog.records)


class TestChatGPTPlanHostStore:
    @pytest.mark.asyncio
    async def test_get_or_create_mints_urn_uuid_stable_host_id(
        self, tmp_path: Path
    ) -> None:
        import re
        from uuid import UUID

        from src.connectors.openai_chatgpt_plan.models import (
            is_valid_ext_agent_host_id,
        )

        store = _host_store(tmp_path)
        first = await store.get_or_create()
        second = await store.get_or_create()

        assert first.schema_version == 1
        assert first.ext_agent_host_id == second.ext_agent_host_id
        assert first.created_at == second.created_at
        assert first.ext_agent_host_id.startswith("urn:uuid:")
        assert is_valid_ext_agent_host_id(first.ext_agent_host_id)
        uuid_part = first.ext_agent_host_id.removeprefix("urn:uuid:")
        assert re.fullmatch(
            r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-"
            r"[0-9a-fA-F]{4}-[0-9a-fA-F]{12}",
            uuid_part,
        )
        parsed = UUID(uuid_part)
        assert parsed.version == 4
        assert "@" not in first.ext_agent_host_id
        assert " " not in first.ext_agent_host_id

        reloaded = await _host_store(tmp_path).get_or_create()
        assert reloaded.ext_agent_host_id == first.ext_agent_host_id
        host_path = tmp_path / "host.json"
        assert host_path.is_file()
        raw = json.loads(host_path.read_text(encoding="utf-8"))
        assert raw["ext_agent_host_id"] == first.ext_agent_host_id
        assert raw["schema_version"] == 1

    @pytest.mark.asyncio
    async def test_bare_opaque_host_id_is_rejected_without_reminting(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.storage import (
            ChatGPTPlanHostStorageError,
        )

        host_path = tmp_path / "host.json"
        bare = "bt75cCs7A6xXS2uAq4rwA3MHgE3vlmQkdf8ynGGFcCI"
        host_path.write_text(
            json.dumps(
                {
                    "schema_version": 1,
                    "ext_agent_host_id": bare,
                    "created_at": "2026-09-30T13:29:36.263874Z",
                }
            ),
            encoding="utf-8",
        )
        store = _host_store(tmp_path)

        with pytest.raises(ChatGPTPlanHostStorageError) as exc_info:
            await store.get_or_create()

        message = str(exc_info.value).lower()
        assert "host" in message
        assert "corrupt" in message or "invalid" in message
        assert "delete" in message or "urn:uuid" in message
        # Must not remint over a present (but invalid) host.json
        raw = json.loads(host_path.read_text(encoding="utf-8"))
        assert raw["ext_agent_host_id"] == bare

    @pytest.mark.asyncio
    async def test_corrupt_host_file_raises_without_minting_new_id(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.storage import (
            ChatGPTPlanHostStorageError,
        )

        host_path = tmp_path / "host.json"
        host_path.write_text("{not-json", encoding="utf-8")
        store = _host_store(tmp_path)

        with pytest.raises(ChatGPTPlanHostStorageError) as exc_info:
            await store.get_or_create()

        assert isinstance(exc_info.value, LLMProxyError)
        message = str(exc_info.value).lower()
        assert "host" in message
        assert "corrupt" in message or "invalid" in message
        assert host_path.read_text(encoding="utf-8") == "{not-json"

    @pytest.mark.asyncio
    async def test_host_write_uses_os_replace(self, tmp_path: Path) -> None:
        store = _host_store(tmp_path)
        with patch("os.replace", wraps=os.replace) as spy:
            await store.get_or_create()
        assert spy.called


class TestChatGPTPlanProfileIdentityAndIsolation:
    @pytest.mark.asyncio
    async def test_save_and_load_by_profile_id_not_email(self, tmp_path: Path) -> None:
        store = _profile_store(tmp_path)
        profile = _profile(profile_id="alice-work", email=SHARED_EMAIL)
        await store.save_atomic(profile)

        loaded = await store.load("alice-work")
        assert loaded is not None
        assert loaded.profile_id == "alice-work"
        assert loaded.email == SHARED_EMAIL
        assert loaded.issued_client_id == ISSUED_CLIENT_ID
        assert loaded.issuer == ISSUER
        assert loaded.subject == "subject-primary"
        assert loaded.display_name == "Same User"
        assert loaded.granted_scopes == profile.granted_scopes
        assert loaded.access_token == ACCESS_TOKEN
        assert loaded.refresh_token == REFRESH_TOKEN
        assert loaded.id_token == ID_TOKEN
        assert loaded.status == "ready"
        assert loaded.schema_version == 1

        by_email = await store.load(SHARED_EMAIL)
        assert by_email is None

        profile_file = tmp_path / "profiles" / "alice-work.json"
        assert profile_file.is_file()
        raw = json.loads(profile_file.read_text(encoding="utf-8"))
        assert "ext_agent_host_id" not in raw
        assert raw["profile_id"] == "alice-work"
        assert "@" not in profile_file.name

    @pytest.mark.asyncio
    async def test_two_profiles_with_same_email_remain_distinct(
        self, tmp_path: Path
    ) -> None:
        store = _profile_store(tmp_path)
        work = _profile(
            profile_id="alice-work",
            subject="subject-work",
            issued_client_id="oaiapp-work",
        )
        personal = _profile(
            profile_id="alice-personal",
            subject="subject-personal",
            issued_client_id="oaiapp-personal",
            access_token="access-token-personal-value",
        )
        await store.save_atomic(work)
        await store.save_atomic(personal)

        loaded_work = await store.load("alice-work")
        loaded_personal = await store.load("alice-personal")
        assert loaded_work is not None
        assert loaded_personal is not None
        assert loaded_work.email == loaded_personal.email == SHARED_EMAIL
        assert loaded_work.subject == "subject-work"
        assert loaded_personal.subject == "subject-personal"
        assert loaded_work.issued_client_id != loaded_personal.issued_client_id
        assert loaded_work.access_token == ACCESS_TOKEN
        assert loaded_personal.access_token == "access-token-personal-value"

        summaries = await store.list_profiles()
        ids = {item.profile_id for item in summaries}
        assert ids == {"alice-work", "alice-personal"}
        for summary in summaries:
            dumped = summary.model_dump()
            assert "access_token" not in dumped
            assert "refresh_token" not in dumped
            assert "id_token" not in dumped

        assert (tmp_path / "profiles" / "alice-work.json").is_file()
        assert (tmp_path / "profiles" / "alice-personal.json").is_file()

    def test_email_is_rejected_as_profile_id(self) -> None:
        import pydantic

        with pytest.raises(pydantic.ValidationError):
            _profile(profile_id=SHARED_EMAIL)

    def test_path_separators_rejected_as_profile_id(self) -> None:
        import pydantic

        with pytest.raises(pydantic.ValidationError):
            _profile(profile_id="../evil")
        with pytest.raises(pydantic.ValidationError):
            _profile(profile_id="alice/work")

    @pytest.mark.asyncio
    async def test_load_missing_profile_returns_none(self, tmp_path: Path) -> None:
        store = _profile_store(tmp_path)
        assert await store.load("does-not-exist") is None

    @pytest.mark.asyncio
    async def test_filename_stem_mismatch_is_rejected_and_not_rewritten(
        self, tmp_path: Path, caplog: pytest.LogCaptureFixture
    ) -> None:
        from src.connectors.openai_chatgpt_plan.storage import (
            ChatGPTPlanProfileStorageError,
        )

        store = _profile_store(tmp_path)
        await store.save_atomic(_profile(profile_id="primary"))
        profiles_dir = tmp_path / "profiles"
        primary_path = profiles_dir / "primary.json"
        other_path = profiles_dir / "other.json"
        primary_path.rename(other_path)

        with pytest.raises(ChatGPTPlanProfileStorageError) as exc_info:
            await store.load("other")

        error = exc_info.value
        text = f"{error} {error.details}"
        assert ACCESS_TOKEN not in text
        assert REFRESH_TOKEN not in text
        assert ID_TOKEN not in text
        details = error.details or {}
        assert set(details) <= {"profile_id", "path", "expected_id"}
        assert details.get("expected_id") == "other"
        assert "primary" in str(details.get("profile_id", ""))
        assert "path" in details
        assert ACCESS_TOKEN not in str(details)
        message = str(error).lower()
        assert "profile_id" in message or "filename" in message or "mismatch" in message

        with caplog.at_level(logging.ERROR):
            summaries = await store.list_profiles()
        ids = {item.profile_id for item in summaries}
        assert "primary" not in ids
        assert "other" not in ids
        log_text = _joined_log_text(caplog)
        assert ACCESS_TOKEN not in log_text
        assert "other" in log_text.lower()

        with pytest.raises(ChatGPTPlanProfileStorageError):
            await store.clear_tokens("other", "signed_out")

        remaining = sorted(path.name for path in profiles_dir.glob("*.json"))
        assert remaining == ["other.json"]
        leftover = json.loads(other_path.read_text(encoding="utf-8"))
        assert leftover["profile_id"] == "primary"
        assert leftover["access_token"] == ACCESS_TOKEN

    @pytest.mark.asyncio
    async def test_clear_tokens_keeps_registration_metadata(
        self, tmp_path: Path
    ) -> None:
        store = _profile_store(tmp_path)
        await store.save_atomic(_profile())
        await store.clear_tokens("primary", "signed_out")
        loaded = await store.load("primary")
        assert loaded is not None
        assert loaded.access_token is None
        assert loaded.refresh_token is None
        assert loaded.id_token is None
        assert loaded.access_token_expires_at is None
        assert loaded.refresh_token_expires_at is None
        assert loaded.status == "signed_out"
        assert loaded.issued_client_id == ISSUED_CLIENT_ID
        assert loaded.subject == "subject-primary"
        assert loaded.issuer == ISSUER

    @pytest.mark.asyncio
    async def test_delete_removes_only_that_profile(self, tmp_path: Path) -> None:
        store = _profile_store(tmp_path)
        await store.save_atomic(_profile(profile_id="keep-me", subject="keep"))
        await store.save_atomic(_profile(profile_id="drop-me", subject="drop"))
        await store.delete("drop-me")
        assert await store.load("drop-me") is None
        kept = await store.load("keep-me")
        assert kept is not None
        assert kept.subject == "keep"


class TestAtomicReplaceAndPermissions:
    @pytest.mark.asyncio
    async def test_save_uses_os_replace_and_leaves_no_tmp_files(
        self, tmp_path: Path
    ) -> None:
        store = _profile_store(tmp_path)
        with patch("os.replace", wraps=os.replace) as spy:
            await store.save_atomic(_profile())
        assert spy.called
        leftover = list((tmp_path / "profiles").glob("*.tmp"))
        assert leftover == []
        assert (tmp_path / "profiles" / "primary.json").is_file()

    @pytest.mark.asyncio
    async def test_failed_replace_leaves_previous_profile_intact(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.storage import (
            ChatGPTPlanProfileStorageError,
        )

        store = _profile_store(tmp_path)
        original = _profile(access_token="access-token-original-value")
        await store.save_atomic(original)

        def _fail_replace(src: str, dst: str) -> None:
            raise OSError("simulated replace failure")

        with (
            patch("os.replace", _fail_replace),
            pytest.raises(ChatGPTPlanProfileStorageError),
        ):
            await store.save_atomic(_profile(access_token="access-token-updated-value"))

        loaded = await store.load("primary")
        assert loaded is not None
        assert loaded.access_token == "access-token-original-value"

    @pytest.mark.asyncio
    async def test_save_retries_transient_permission_error_on_replace(
        self, tmp_path: Path
    ) -> None:
        store = _profile_store(tmp_path)
        original_replace = os.replace
        calls = {"count": 0}

        def _flaky_replace(src: str, dst: str) -> None:
            calls["count"] += 1
            if calls["count"] == 1:
                raise PermissionError("[WinError 5] Access denied")
            original_replace(src, dst)

        with patch("os.replace", _flaky_replace):
            await store.save_atomic(_profile())

        loaded = await store.load("primary")
        assert loaded is not None
        assert loaded.profile_id == "primary"
        assert calls["count"] == 2

    @pytest.mark.asyncio
    async def test_owner_only_chmod_is_attempted(self, tmp_path: Path) -> None:
        store = _profile_store(tmp_path)
        chmod_calls: list[tuple[str, int]] = []
        real_chmod = os.chmod

        def _spy_chmod(path: Any, mode: int) -> None:
            chmod_calls.append((os.fsdecode(path), mode))
            real_chmod(path, mode)

        with patch("os.chmod", _spy_chmod):
            await store.save_atomic(_profile())

        assert any(mode == 0o600 for _path, mode in chmod_calls)

    @pytest.mark.skipif(
        os.name == "nt", reason="POSIX permission bits are not NTFS ACLs"
    )
    @pytest.mark.asyncio
    async def test_posix_saved_files_are_owner_rw_only(self, tmp_path: Path) -> None:
        host_store = _host_store(tmp_path)
        profile_store = _profile_store(tmp_path)
        await host_store.get_or_create()
        await profile_store.save_atomic(_profile())

        host_mode = stat.S_IMODE((tmp_path / "host.json").stat().st_mode)
        profile_mode = stat.S_IMODE(
            (tmp_path / "profiles" / "primary.json").stat().st_mode
        )
        assert host_mode == 0o600
        assert profile_mode == 0o600

    @pytest.mark.asyncio
    async def test_windows_acl_failure_does_not_block_save(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan import storage as storage_mod

        store = _profile_store(tmp_path)
        with (
            patch.object(storage_mod.os, "name", "nt"),
            patch.object(storage_mod, "_apply_windows_owner_acl", return_value=False),
        ):
            await store.save_atomic(_profile())

        loaded = await store.load("primary")
        assert loaded is not None
        assert loaded.access_token == ACCESS_TOKEN

    @pytest.mark.asyncio
    async def test_windows_acl_helper_is_invoked_when_os_name_is_nt(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan import storage as storage_mod

        store = _profile_store(tmp_path)
        with (
            patch.object(storage_mod.os, "name", "nt"),
            patch.object(
                storage_mod, "_apply_windows_owner_acl", return_value=True
            ) as acl_helper,
        ):
            await store.save_atomic(_profile())

        assert acl_helper.called


class TestCorruptionAndPermissionDiagnostics:
    @pytest.mark.asyncio
    async def test_corrupt_profile_blocks_only_that_profile(
        self, tmp_path: Path, caplog: pytest.LogCaptureFixture
    ) -> None:
        from src.connectors.openai_chatgpt_plan.storage import (
            ChatGPTPlanProfileStorageError,
        )

        store = _profile_store(tmp_path)
        await store.save_atomic(_profile(profile_id="healthy", subject="healthy-sub"))
        corrupt_path = tmp_path / "profiles" / "broken.json"
        corrupt_path.write_text(
            '{"access_token": "access-token-test-value", not-json',
            encoding="utf-8",
        )

        with caplog.at_level(logging.ERROR):
            summaries = await store.list_profiles()

        ids = {item.profile_id for item in summaries}
        assert ids == {"healthy"}
        healthy = await store.load("healthy")
        assert healthy is not None
        assert healthy.subject == "healthy-sub"

        with pytest.raises(ChatGPTPlanProfileStorageError) as exc_info:
            await store.load("broken")

        error = exc_info.value
        assert isinstance(error, LLMProxyError)
        text = f"{error} {error.details}"
        assert ACCESS_TOKEN not in text
        assert "broken" in str(error).lower()
        log_text = _joined_log_text(caplog)
        assert ACCESS_TOKEN not in log_text
        assert "broken" in log_text.lower()

    @pytest.mark.asyncio
    async def test_unsupported_schema_version_is_actionable(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.storage import (
            ChatGPTPlanProfileStorageError,
        )

        store = _profile_store(tmp_path)
        await store.save_atomic(_profile())
        path = tmp_path / "profiles" / "primary.json"
        payload = json.loads(path.read_text(encoding="utf-8"))
        payload["schema_version"] = 99
        path.write_text(json.dumps(payload), encoding="utf-8")

        with pytest.raises(ChatGPTPlanProfileStorageError) as exc_info:
            await store.load("primary")
        assert "schema" in str(exc_info.value).lower()
        assert ACCESS_TOKEN not in str(exc_info.value)
        assert ACCESS_TOKEN not in str(exc_info.value.details)

    @pytest.mark.asyncio
    async def test_persistent_permission_error_on_load_raises(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.storage import (
            ChatGPTPlanProfileStorageError,
        )

        store = _profile_store(tmp_path)
        await store.save_atomic(_profile())
        target = (tmp_path / "profiles" / "primary.json").resolve()
        original_read_text = Path.read_text

        def _deny_primary(self: Path, *args: Any, **kwargs: Any) -> str:
            if self.resolve() == target:
                raise PermissionError("[Errno 13] Permission denied")
            return original_read_text(self, *args, **kwargs)

        with (
            patch.object(Path, "read_text", _deny_primary),
            pytest.raises(ChatGPTPlanProfileStorageError) as exc_info,
        ):
            await store.load("primary")

        assert "permission" in str(exc_info.value).lower()
        assert ACCESS_TOKEN not in str(exc_info.value)


class TestSecretRedaction:
    def test_profile_str_and_repr_hide_tokens(self) -> None:
        profile = _profile()
        rendered = f"{profile!s} {profile!r}"
        assert ACCESS_TOKEN not in rendered
        assert REFRESH_TOKEN not in rendered
        assert ID_TOKEN not in rendered
        assert "primary" in rendered

    def test_exception_text_does_not_include_tokens(self) -> None:
        from src.connectors.openai_chatgpt_plan.storage import (
            ChatGPTPlanProfileStorageError,
        )

        error = ChatGPTPlanProfileStorageError(
            "profile primary is corrupt",
            details={"profile_id": "primary", "path": "primary.json"},
        )
        assert isinstance(error, LLMProxyError)
        dumped = str(error.to_dict())
        assert ACCESS_TOKEN not in dumped
        assert ACCESS_TOKEN not in str(error)

    def test_redact_helper_masks_siwc_token_fields(self) -> None:
        from src.connectors.openai_chatgpt_plan.models import (
            redact_chatgpt_plan_mapping,
        )

        payload = {
            "profile_id": "primary",
            "access_token": ACCESS_TOKEN,
            "refresh_token": REFRESH_TOKEN,
            "id_token": ID_TOKEN,
            "email": SHARED_EMAIL,
        }
        redacted = redact_chatgpt_plan_mapping(payload)
        rendered = json.dumps(redacted)
        assert ACCESS_TOKEN not in rendered
        assert REFRESH_TOKEN not in rendered
        assert ID_TOKEN not in rendered
        assert redacted["profile_id"] == "primary"

    def test_default_capture_redaction_covers_id_token(self) -> None:
        assert "access_token" in DEFAULT_REDACTED_FIELDS
        assert "refresh_token" in DEFAULT_REDACTED_FIELDS
        assert "id_token" in DEFAULT_REDACTED_FIELDS
        redacted = redact_dict(
            {
                "access_token": ACCESS_TOKEN,
                "refresh_token": REFRESH_TOKEN,
                "id_token": ID_TOKEN,
                "profile_id": "primary",
            }
        )
        rendered = json.dumps(redacted)
        assert ACCESS_TOKEN not in rendered
        assert REFRESH_TOKEN not in rendered
        assert ID_TOKEN not in rendered

    @pytest.mark.asyncio
    async def test_storage_logs_do_not_contain_tokens(
        self, tmp_path: Path, caplog: pytest.LogCaptureFixture
    ) -> None:
        store = _profile_store(tmp_path)
        with caplog.at_level(logging.DEBUG):
            await store.save_atomic(_profile())
            await store.load("primary")
            await store.list_profiles()
        log_text = _joined_log_text(caplog)
        assert ACCESS_TOKEN not in log_text
        assert REFRESH_TOKEN not in log_text
        assert ID_TOKEN not in log_text


class TestNoLegacyCodexImport:
    @pytest.mark.asyncio
    async def test_does_not_read_codex_auth_or_managed_accounts(
        self, tmp_path: Path
    ) -> None:
        decoy_codex = tmp_path / ".codex" / "auth.json"
        decoy_codex.parent.mkdir(parents=True)
        decoy_codex.write_text(
            json.dumps({"access_token": ACCESS_TOKEN, "email": SHARED_EMAIL}),
            encoding="utf-8",
        )
        decoy_managed = tmp_path / "openai_codex_oauth_accounts" / "acct.json"
        decoy_managed.parent.mkdir(parents=True)
        decoy_managed.write_text(
            json.dumps({"account_id": "legacy", "access_token": ACCESS_TOKEN}),
            encoding="utf-8",
        )

        opened: list[str] = []
        original_read_text = Path.read_text

        def _track_read(self: Path, *args: Any, **kwargs: Any) -> str:
            opened.append(self.as_posix())
            return original_read_text(self, *args, **kwargs)

        store = _profile_store(tmp_path)
        with patch.object(Path, "read_text", _track_read):
            summaries = await store.list_profiles()
            loaded = await store.load("primary")

        assert summaries == []
        assert loaded is None
        opened_joined = "\n".join(opened).replace("\\", "/")
        assert ".codex/auth.json" not in opened_joined
        assert "openai_codex_oauth_accounts" not in opened_joined

    def test_storage_module_does_not_import_legacy_codex(self) -> None:
        import ast
        from pathlib import Path as FilePath

        root = FilePath(__file__).resolve().parents[3]
        package = root / "src" / "connectors" / "openai_chatgpt_plan"
        forbidden = (
            "src.connectors.openai_codex",
            "src.connectors._openai_codex_connector",
            "src.connectors._openai_codex_v2_connector",
        )
        hits: list[str] = []
        for path in sorted(package.glob("*.py")):
            tree = ast.parse(path.read_text(encoding="utf-8"))
            for node in ast.walk(tree):
                names: list[str] = []
                if isinstance(node, ast.Import):
                    names.extend(alias.name for alias in node.names)
                elif isinstance(node, ast.ImportFrom) and node.module:
                    names.append(node.module)
                for name in names:
                    if any(
                        name == item or name.startswith(f"{item}.")
                        for item in forbidden
                    ):
                        hits.append(f"{path.name}: {name}")
        assert hits == []
