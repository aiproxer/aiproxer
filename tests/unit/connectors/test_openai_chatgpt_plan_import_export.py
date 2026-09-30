"""Unit tests for ChatGPT-plan protected profile import/export (task 2.5).

Export may carry registration and token state. Source host identity is provenance
only: import must never replace the destination host's ext_agent_host_id.
`.codex/auth.json` and legacy openai-codex managed-account files are rejected
and never auto-imported as SIWC profiles.
"""

from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from unittest.mock import patch

import pytest
from src.core.common.exceptions import LLMProxyError

ACCESS_TOKEN = "access-token-export-value"
REFRESH_TOKEN = "refresh-token-export-value"
ID_TOKEN = "id-token-export-value"
SHARED_EMAIL = "same-user@example.com"
ISSUER = "https://auth.openai.com"
SOURCE_HOST_ID = "urn:uuid:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
DEST_HOST_ID = "urn:uuid:bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"


def _now() -> datetime:
    return datetime.now(timezone.utc)


def _profile(**overrides: Any):
    from src.connectors.openai_chatgpt_plan.models import ChatGPTPlanProfile

    now = _now()
    payload: dict[str, Any] = {
        "schema_version": 1,
        "profile_id": "primary",
        "issued_client_id": "oaiapp-export-client",
        "issuer": ISSUER,
        "subject": "subject-export",
        "email": SHARED_EMAIL,
        "display_name": "Export User",
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
        "status": "ready",
        "created_at": now,
        "updated_at": now,
    }
    payload.update(overrides)
    return ChatGPTPlanProfile(**payload)


def _host_store(tmp_path: Path, name: str = "host.json"):
    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanHostStore

    return ChatGPTPlanHostStore(tmp_path / name)


def _profile_store(tmp_path: Path, name: str = "profiles"):
    from src.connectors.openai_chatgpt_plan.storage import ChatGPTPlanProfileStore

    return ChatGPTPlanProfileStore(tmp_path / name)


async def _seed_dest_host(tmp_path: Path, host_id: str = DEST_HOST_ID):
    from src.connectors.openai_chatgpt_plan.models import ChatGPTPlanHostState

    host_path = tmp_path / "host.json"
    state = ChatGPTPlanHostState(
        schema_version=1,
        ext_agent_host_id=host_id,
        created_at=_now(),
    )
    host_path.write_text(
        json.dumps(state.model_dump(mode="json"), indent=2),
        encoding="utf-8",
    )
    return _host_store(tmp_path)


class TestExportEnvelope:
    @pytest.mark.asyncio
    async def test_export_contains_tokens_and_registration_not_host_overwrite(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.import_export import (
            export_chatgpt_plan_profile,
        )

        store = _profile_store(tmp_path)
        await store.save_atomic(_profile())
        source_host = await _seed_dest_host(tmp_path, SOURCE_HOST_ID)

        envelope = await export_chatgpt_plan_profile(
            store,
            "primary",
            host_store=source_host,
        )

        assert isinstance(envelope, dict)
        profile_payload = envelope.get("profile") or envelope
        assert profile_payload["access_token"] == ACCESS_TOKEN
        assert profile_payload["refresh_token"] == REFRESH_TOKEN
        assert profile_payload["id_token"] == ID_TOKEN
        assert profile_payload["issued_client_id"] == "oaiapp-export-client"
        assert profile_payload["issuer"] == ISSUER
        assert profile_payload["subject"] == "subject-export"
        assert profile_payload["email"] == SHARED_EMAIL
        assert profile_payload["profile_id"] == "primary"

        assert envelope.get("overwrite_destination_host") is not True
        assert envelope.get("overwrite_destination_host_id") is not True
        assert envelope.get("apply_source_host_id") is not True
        assert envelope.get("ext_agent_host_id") != SOURCE_HOST_ID
        assert "ext_agent_host_id" not in (envelope.get("profile") or {})

        provenance = envelope.get("source_host_provenance")
        if provenance is not None:
            assert provenance.get("overwrite_destination") is not True
            assert provenance.get("role") in {None, "provenance"}
            assert provenance.get("apply_to_destination_host") is not True

    @pytest.mark.asyncio
    async def test_export_unknown_profile_raises(self, tmp_path: Path) -> None:
        from src.connectors.openai_chatgpt_plan.import_export import (
            ChatGPTPlanImportExportError,
            export_chatgpt_plan_profile,
        )

        store = _profile_store(tmp_path)
        with pytest.raises(ChatGPTPlanImportExportError) as exc_info:
            await export_chatgpt_plan_profile(store, "missing")
        assert isinstance(exc_info.value, LLMProxyError)
        assert ACCESS_TOKEN not in str(exc_info.value)


class TestImportPreservesDestinationHost:
    @pytest.mark.asyncio
    async def test_import_keeps_destination_ext_agent_host_id(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.import_export import (
            export_chatgpt_plan_profile,
            import_chatgpt_plan_profile,
        )

        source_dir = tmp_path / "source"
        dest_dir = tmp_path / "dest"
        source_dir.mkdir()
        dest_dir.mkdir()

        source_store = _profile_store(source_dir)
        await source_store.save_atomic(_profile(profile_id="source-profile"))
        source_host = await _seed_dest_host(source_dir, SOURCE_HOST_ID)
        envelope = await export_chatgpt_plan_profile(
            source_store,
            "source-profile",
            host_store=source_host,
        )
        envelope["ext_agent_host_id"] = SOURCE_HOST_ID
        envelope["overwrite_destination_host"] = True
        envelope["apply_source_host_id"] = True

        dest_host = await _seed_dest_host(dest_dir, DEST_HOST_ID)
        dest_store = _profile_store(dest_dir)
        imported = await import_chatgpt_plan_profile(
            envelope,
            dest_profile_id="imported",
            profile_store=dest_store,
            host_store=dest_host,
        )

        assert imported.profile_id == "imported"
        assert imported.access_token == ACCESS_TOKEN
        assert imported.refresh_token == REFRESH_TOKEN
        assert imported.id_token == ID_TOKEN
        assert imported.issued_client_id == "oaiapp-export-client"
        assert imported.subject == "subject-export"
        assert imported.email == SHARED_EMAIL

        after = await dest_host.get_or_create()
        assert after.ext_agent_host_id == DEST_HOST_ID
        raw_host = json.loads((dest_dir / "host.json").read_text(encoding="utf-8"))
        assert raw_host["ext_agent_host_id"] == DEST_HOST_ID

        profile_path = dest_dir / "profiles" / "imported.json"
        assert profile_path.is_file()
        raw_profile = json.loads(profile_path.read_text(encoding="utf-8"))
        assert raw_profile["profile_id"] == "imported"
        assert Path(profile_path).stem == raw_profile["profile_id"]
        assert raw_profile.get("ext_agent_host_id") is None

    @pytest.mark.asyncio
    async def test_import_does_not_copy_source_host_id_when_dest_host_missing(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.import_export import (
            export_chatgpt_plan_profile,
            import_chatgpt_plan_profile,
        )

        source_dir = tmp_path / "source"
        dest_dir = tmp_path / "dest"
        source_dir.mkdir()
        dest_dir.mkdir()

        source_store = _profile_store(source_dir)
        await source_store.save_atomic(_profile())
        source_host = await _seed_dest_host(source_dir, SOURCE_HOST_ID)
        envelope = await export_chatgpt_plan_profile(
            source_store,
            "primary",
            host_store=source_host,
        )

        dest_host = _host_store(dest_dir)
        dest_store = _profile_store(dest_dir)
        await import_chatgpt_plan_profile(
            envelope,
            dest_profile_id="remote",
            profile_store=dest_store,
            host_store=dest_host,
        )

        minted = await dest_host.get_or_create()
        assert minted.ext_agent_host_id != SOURCE_HOST_ID
        raw_host = json.loads((dest_dir / "host.json").read_text(encoding="utf-8"))
        assert raw_host["ext_agent_host_id"] != SOURCE_HOST_ID

    @pytest.mark.asyncio
    async def test_two_imported_profiles_same_email_remain_distinct(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.import_export import (
            export_chatgpt_plan_profile,
            import_chatgpt_plan_profile,
        )

        source_store = _profile_store(tmp_path, "source-profiles")
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
        await source_store.save_atomic(work)
        await source_store.save_atomic(personal)
        dest_host = await _seed_dest_host(tmp_path)
        dest_store = _profile_store(tmp_path, "dest-profiles")

        for profile_id in ("alice-work", "alice-personal"):
            envelope = await export_chatgpt_plan_profile(source_store, profile_id)
            await import_chatgpt_plan_profile(
                envelope,
                dest_profile_id=profile_id,
                profile_store=dest_store,
                host_store=dest_host,
            )

        loaded_work = await dest_store.load("alice-work")
        loaded_personal = await dest_store.load("alice-personal")
        assert loaded_work is not None
        assert loaded_personal is not None
        assert loaded_work.email == loaded_personal.email == SHARED_EMAIL
        assert loaded_work.subject == "subject-work"
        assert loaded_personal.subject == "subject-personal"
        assert loaded_work.issued_client_id != loaded_personal.issued_client_id
        after = await dest_host.get_or_create()
        assert after.ext_agent_host_id == DEST_HOST_ID


class TestLegacyCodexImportRejected:
    @pytest.mark.asyncio
    async def test_loading_codex_auth_json_as_siwc_profile_is_rejected(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.import_export import (
            ChatGPTPlanImportExportError,
            import_chatgpt_plan_profile_from_path,
        )

        dest_host = await _seed_dest_host(tmp_path)
        dest_store = _profile_store(tmp_path)
        codex_auth = tmp_path / ".codex" / "auth.json"
        codex_auth.parent.mkdir(parents=True)
        codex_auth.write_text(
            json.dumps(
                {
                    "tokens": {
                        "access_token": ACCESS_TOKEN,
                        "refresh_token": REFRESH_TOKEN,
                        "id_token": ID_TOKEN,
                    },
                    "email": SHARED_EMAIL,
                }
            ),
            encoding="utf-8",
        )

        with pytest.raises(ChatGPTPlanImportExportError) as exc_info:
            await import_chatgpt_plan_profile_from_path(
                codex_auth,
                dest_profile_id="from-codex",
                profile_store=dest_store,
                host_store=dest_host,
            )

        error = exc_info.value
        assert isinstance(error, LLMProxyError)
        message = str(error).lower()
        assert "codex" in message or "siwc" in message or "legacy" in message
        assert ACCESS_TOKEN not in str(error)
        assert await dest_store.load("from-codex") is None
        after = await dest_host.get_or_create()
        assert after.ext_agent_host_id == DEST_HOST_ID

    @pytest.mark.asyncio
    async def test_legacy_managed_account_payload_is_rejected(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.import_export import (
            ChatGPTPlanImportExportError,
            import_chatgpt_plan_profile,
        )

        dest_host = await _seed_dest_host(tmp_path)
        dest_store = _profile_store(tmp_path)
        payload = {
            "account_id": "legacy-codex-account",
            "chatgpt_account_id": "acct-legacy",
            "access_token": ACCESS_TOKEN,
            "email": SHARED_EMAIL,
        }
        with pytest.raises(ChatGPTPlanImportExportError):
            await import_chatgpt_plan_profile(
                payload,
                dest_profile_id="legacy",
                profile_store=dest_store,
                host_store=dest_host,
            )
        assert await dest_store.load("legacy") is None

    @pytest.mark.asyncio
    async def test_import_does_not_auto_read_codex_or_managed_account_files(
        self, tmp_path: Path
    ) -> None:
        from src.connectors.openai_chatgpt_plan.import_export import (
            export_chatgpt_plan_profile,
            import_chatgpt_plan_profile,
        )

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

        source_store = _profile_store(tmp_path, "source-profiles")
        await source_store.save_atomic(_profile())
        dest_host = await _seed_dest_host(tmp_path)
        dest_store = _profile_store(tmp_path, "dest-profiles")
        envelope = await export_chatgpt_plan_profile(source_store, "primary")

        opened: list[str] = []
        original_read_text = Path.read_text

        def _track_read(self: Path, *args: Any, **kwargs: Any) -> str:
            opened.append(self.as_posix())
            return original_read_text(self, *args, **kwargs)

        with patch.object(Path, "read_text", _track_read):
            await import_chatgpt_plan_profile(
                envelope,
                dest_profile_id="imported",
                profile_store=dest_store,
                host_store=dest_host,
            )

        opened_joined = "\n".join(opened).replace("\\", "/")
        assert ".codex/auth.json" not in opened_joined
        assert "openai_codex_oauth_accounts" not in opened_joined
        loaded = await dest_store.load("imported")
        assert loaded is not None
        assert loaded.access_token == ACCESS_TOKEN
