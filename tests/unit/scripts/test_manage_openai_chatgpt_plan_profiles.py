"""CLI tests for manage_openai_chatgpt_plan_profiles (task 6.1)."""

from __future__ import annotations

import importlib.util
import json
import sys
from datetime import datetime, timezone
from pathlib import Path
from types import ModuleType
from typing import Any

import pytest

ACCESS = "siwc-access-token-cli-placeholder"
REFRESH = "siwc-refresh-token-cli-placeholder"
IDTOK = "siwc-id-token-cli-placeholder"
REPO_ROOT = Path(__file__).resolve().parents[3]
SCRIPT_PATH = REPO_ROOT / "scripts" / "manage_openai_chatgpt_plan_profiles.py"


def _load_cli() -> ModuleType:
    if str(REPO_ROOT) not in sys.path:
        sys.path.insert(0, str(REPO_ROOT))
    spec = importlib.util.spec_from_file_location(
        "manage_openai_chatgpt_plan_profiles", SCRIPT_PATH
    )
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _now() -> datetime:
    return datetime.now(timezone.utc)


def _write_profile(profiles_dir: Path, **overrides: Any) -> Path:
    from src.connectors.openai_chatgpt_plan.models import ChatGPTPlanProfile

    now = _now()
    payload: dict[str, Any] = {
        "schema_version": 1,
        "profile_id": "primary",
        "issued_client_id": "oaiapp-cli-client",
        "issuer": "https://auth.openai.com",
        "subject": "sub-cli",
        "email": "cli-user@example.com",
        "display_name": "CLI User",
        "access_token": ACCESS,
        "refresh_token": REFRESH,
        "id_token": IDTOK,
        "granted_scopes": ("openid", "chatgpt.tokens.use.direct"),
        "resource": "https://api.openai.com/v1",
        "access_token_expires_at": now.isoformat(),
        "status": "ready",
        "created_at": now.isoformat(),
        "updated_at": now.isoformat(),
    }
    payload.update(overrides)
    profile = ChatGPTPlanProfile.model_validate(payload)
    profiles_dir.mkdir(parents=True, exist_ok=True)
    path = profiles_dir / f"{profile.profile_id}.json"
    path.write_text(profile.model_dump_json(indent=2), encoding="utf-8")
    return path


def test_list_and_show_never_print_secrets(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    cli = _load_cli()
    profiles = tmp_path / "profiles"
    host = tmp_path / "host.json"
    _write_profile(profiles, status="missing_plan_scope")

    code = cli.main(
        [
            "--profiles-path",
            str(profiles),
            "--host-state-path",
            str(host),
            "list",
            "--json",
        ]
    )
    assert code == 0
    out = capsys.readouterr().out
    for secret in (ACCESS, REFRESH, IDTOK):
        assert secret not in out
    assert "missing_plan_scope" in out

    code = cli.main(
        [
            "--profiles-path",
            str(profiles),
            "--host-state-path",
            str(host),
            "show",
            "primary",
            "--json",
        ]
    )
    assert code == 0
    out = capsys.readouterr().out
    payload = json.loads(out)
    for secret in (ACCESS, REFRESH, IDTOK):
        assert secret not in out
        assert secret not in json.dumps(payload)
    assert "access_token" not in payload
    assert "refresh_token" not in payload
    assert "id_token" not in payload
    assert payload["status"] == "missing_plan_scope"
    assert "plan-use" in payload["status_detail"]


def test_show_needs_reauth_actionable_message(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    cli = _load_cli()
    profiles = tmp_path / "profiles"
    host = tmp_path / "host.json"
    _write_profile(
        profiles, status="needs_reauth", access_token=None, refresh_token=None
    )

    code = cli.main(
        [
            "--profiles-path",
            str(profiles),
            "--host-state-path",
            str(host),
            "show",
            "primary",
        ]
    )
    assert code == 0
    out = capsys.readouterr().out
    assert "needs_reauth" in out
    assert "reauthorization" in out.casefold()
    assert ACCESS not in out
