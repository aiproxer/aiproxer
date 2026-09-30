#!/usr/bin/env python3
"""Manage openai-chatgpt-plan SIWC profiles.

Operations:
    list / add / show / reauthorize / refresh / signout / remove / export / import

List and show never print access, refresh, or ID tokens, authorization codes,
or PKCE verifiers. Export is an explicit secret-bearing operation.

Examples:
    ./.venv/Scripts/python.exe scripts/manage_openai_chatgpt_plan_profiles.py list
    ./.venv/Scripts/python.exe scripts/manage_openai_chatgpt_plan_profiles.py add --profile-id primary
    ./.venv/Scripts/python.exe scripts/manage_openai_chatgpt_plan_profiles.py show primary
    ./.venv/Scripts/python.exe scripts/manage_openai_chatgpt_plan_profiles.py export primary --output profile.json
"""

from __future__ import annotations

import argparse
import asyncio
import contextlib
import json
import sys
from datetime import datetime
from pathlib import Path
from typing import Any

# Allow importing project modules when script is run directly.
sys.path.append(str(Path(__file__).parent.parent))

from src.connectors.openai_chatgpt_plan.config import (
    DEFAULT_CHATGPT_PLAN_HOST_STATE_PATH,
    DEFAULT_CHATGPT_PLAN_OAUTH_CALLBACK_PORT,
    DEFAULT_CHATGPT_PLAN_PROFILES_PATH,
)
from src.connectors.openai_chatgpt_plan.import_export import (
    export_chatgpt_plan_profile,
    import_chatgpt_plan_profile_from_path,
)
from src.connectors.openai_chatgpt_plan.models import (
    ChatGPTPlanProfile,
    ChatGPTPlanProfileSummary,
    is_valid_profile_id,
)
from src.connectors.openai_chatgpt_plan.oauth import (
    ChatGPTPlanOAuthService,
)
from src.connectors.openai_chatgpt_plan.storage import (
    ChatGPTPlanHostStore,
    ChatGPTPlanProfileStore,
)
from src.connectors.openai_chatgpt_plan.tokens import (
    ChatGPTPlanTokenManager,
)
from src.core.common.exceptions import LLMProxyError

_SECRET_KEYS = frozenset(
    {
        "access_token",
        "refresh_token",
        "id_token",
        "authorization",
        "code",
        "code_verifier",
        "pkce_verifier",
        "authorization_code",
    }
)


def _format_dt(value: datetime | None) -> str:
    if value is None:
        return "-"
    return value.isoformat()


def _actionable_status(status: str) -> str:
    mapping = {
        "ready": "ready for plan-funded inference",
        "missing_plan_scope": (
            "missing ChatGPT plan-use scope; reauthorize and grant "
            "chatgpt.tokens.use.direct"
        ),
        "needs_reauth": "needs reauthorization (refresh token invalid)",
        "signed_out": "signed out; reauthorize to restore tokens",
    }
    return mapping.get(status, status)


def _safe_profile_dict(profile: ChatGPTPlanProfile) -> dict[str, Any]:
    data = profile.model_dump(mode="json")
    for key in _SECRET_KEYS:
        data.pop(key, None)
    data["has_access_token"] = bool(profile.access_token)
    data["has_refresh_token"] = bool(profile.refresh_token)
    data["status_detail"] = _actionable_status(profile.status)
    return data


def _safe_summary_dict(summary: ChatGPTPlanProfileSummary) -> dict[str, Any]:
    data = summary.model_dump(mode="json")
    data["status_detail"] = _actionable_status(summary.status)
    return data


def _build_stores(
    args: argparse.Namespace,
) -> tuple[ChatGPTPlanHostStore, ChatGPTPlanProfileStore]:
    host = ChatGPTPlanHostStore(Path(args.host_state_path))
    profiles = ChatGPTPlanProfileStore(Path(args.profiles_path))
    return host, profiles


async def cmd_list(args: argparse.Namespace) -> int:
    _host, profiles = _build_stores(args)
    summaries = await profiles.list_profiles()
    if not summaries:
        print("No ChatGPT-plan SIWC profiles found.")
        print("Use 'add' to authorize a new profile.")
        return 0
    rows = [_safe_summary_dict(item) for item in summaries]
    if args.json:
        print(json.dumps(rows, indent=2))
        return 0
    header = f"{'Profile ID':<20} {'Email':<32} {'Status':<18} {'Updated':<28}"
    print(header)
    print("-" * len(header))
    for row in rows:
        print(
            f"{row['profile_id']:<20} {(row.get('email') or '-'):<32} "
            f"{row['status']:<18} {row.get('updated_at') or '-':<28}"
        )
        print(f"  -> {_actionable_status(str(row['status']))}")
    return 0


async def cmd_show(args: argparse.Namespace) -> int:
    _host, profiles = _build_stores(args)
    profile = await profiles.load(args.profile_id)
    if profile is None:
        print(f"Profile '{args.profile_id}' not found.")
        print("Use 'list' to see available profile IDs.")
        return 1
    safe = _safe_profile_dict(profile)
    if args.json:
        print(json.dumps(safe, indent=2))
        return 0
    print(f"ChatGPT-plan profile: {profile.profile_id}")
    print("-" * 42)
    print(f"Status:               {profile.status}")
    print(f"Status detail:        {_actionable_status(profile.status)}")
    print(f"Email:                {profile.email or '-'}")
    print(f"Display name:         {profile.display_name or '-'}")
    print(f"Issuer:               {profile.issuer}")
    print(f"Subject:              {profile.subject}")
    print(f"Issued client ID:     {profile.issued_client_id}")
    print(f"Granted scopes:       {', '.join(profile.granted_scopes) or '-'}")
    print(f"Access token present: {bool(profile.access_token)}")
    print(f"Refresh token present:{bool(profile.refresh_token)}")
    print(f"Access expires:       {_format_dt(profile.access_token_expires_at)}")
    print(f"Created:              {_format_dt(profile.created_at)}")
    print(f"Updated:              {_format_dt(profile.updated_at)}")
    return 0


async def cmd_add(args: argparse.Namespace) -> int:
    host_store, profile_store = _build_stores(args)
    profile_id = args.profile_id
    if not is_valid_profile_id(profile_id):
        print(
            f"Invalid profile id '{profile_id}'. "
            "Use a sanitized local alias (letters, digits, _-, max 64)."
        )
        return 1
    existing = await profile_store.load(profile_id)
    if existing is not None and not args.force:
        print(
            f"Profile '{profile_id}' already exists with status={existing.status}. "
            "Use reauthorize or pass --force to overwrite after removing it."
        )
        return 1
    host = await host_store.get_or_create()
    oauth = ChatGPTPlanOAuthService(profile_store=profile_store)
    profile = await oauth.authorize_new(
        profile_id=profile_id,
        host_id=host.ext_agent_host_id,
        callback_port=int(args.port),
        open_browser=not args.no_browser,
    )
    print(f"Authorized profile '{profile.profile_id}' with status={profile.status}.")
    print(f"Detail: {_actionable_status(profile.status)}")
    return 0


async def cmd_reauthorize(args: argparse.Namespace) -> int:
    host_store, profile_store = _build_stores(args)
    existing = await profile_store.load(args.profile_id)
    if existing is None:
        print(f"Profile '{args.profile_id}' not found.")
        return 1
    host = await host_store.get_or_create()
    oauth = ChatGPTPlanOAuthService(profile_store=profile_store)
    profile = await oauth.reauthorize(
        existing=existing,
        host_id=host.ext_agent_host_id,
        callback_port=int(args.port),
        open_browser=not args.no_browser,
    )
    print(f"Reauthorized profile '{profile.profile_id}' with status={profile.status}.")
    print(f"Detail: {_actionable_status(profile.status)}")
    return 0


async def cmd_refresh(args: argparse.Namespace) -> int:
    _host, profile_store = _build_stores(args)
    manager = ChatGPTPlanTokenManager(profile_store)
    profile = await manager.force_refresh(args.profile_id)
    print(f"Refreshed profile '{profile.profile_id}' with status={profile.status}.")
    print(f"Detail: {_actionable_status(profile.status)}")
    return 0


async def cmd_signout(args: argparse.Namespace) -> int:
    host_store, profile_store = _build_stores(args)
    oauth = ChatGPTPlanOAuthService(profile_store=profile_store)
    profile = await oauth.sign_out(args.profile_id)
    print(f"Signed out profile '{profile.profile_id}' (status={profile.status}).")
    print(f"Detail: {_actionable_status(profile.status)}")
    return 0


async def cmd_remove(args: argparse.Namespace) -> int:
    _host, profile_store = _build_stores(args)
    existing = await profile_store.load(args.profile_id)
    if existing is None:
        print(f"Profile '{args.profile_id}' not found.")
        return 1
    await profile_store.delete(args.profile_id)
    print(f"Removed profile '{args.profile_id}'.")
    return 0


async def cmd_export(args: argparse.Namespace) -> int:
    host_store, profile_store = _build_stores(args)
    output = Path(args.output)
    envelope = await export_chatgpt_plan_profile(
        profile_store,
        args.profile_id,
        host_store=host_store,
    )
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(envelope, indent=2), encoding="utf-8")
    with contextlib.suppress(OSError):
        output.chmod(0o600)
    print(
        f"Exported secret-bearing profile '{args.profile_id}' to {output}. "
        "Protect this file; it contains tokens."
    )
    return 0


async def cmd_import(args: argparse.Namespace) -> int:
    host_store, profile_store = _build_stores(args)
    if not is_valid_profile_id(args.profile_id):
        print(f"Invalid profile id '{args.profile_id}'.")
        return 1
    profile = await import_chatgpt_plan_profile_from_path(
        Path(args.input),
        dest_profile_id=args.profile_id,
        profile_store=profile_store,
        host_store=host_store,
    )
    print(
        f"Imported profile as '{profile.profile_id}' with status={profile.status}. "
        "Destination host identity was preserved."
    )
    print(f"Detail: {_actionable_status(profile.status)}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Manage openai-chatgpt-plan Sign in with ChatGPT profiles."
    )
    parser.add_argument(
        "--profiles-path",
        default=DEFAULT_CHATGPT_PLAN_PROFILES_PATH,
        help="Directory of per-profile JSON files.",
    )
    parser.add_argument(
        "--host-state-path",
        default=DEFAULT_CHATGPT_PLAN_HOST_STATE_PATH,
        help="Path to host.json with ext_agent_host_id.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p_list = sub.add_parser("list", help="List saved SIWC profiles (secrets redacted).")
    p_list.add_argument("--json", action="store_true")
    p_list.set_defaults(func=cmd_list)

    p_show = sub.add_parser("show", help="Show one profile without token values.")
    p_show.add_argument("profile_id")
    p_show.add_argument("--json", action="store_true")
    p_show.set_defaults(func=cmd_show)

    p_add = sub.add_parser("add", help="Authorize a new SIWC profile.")
    p_add.add_argument("--profile-id", required=True)
    p_add.add_argument(
        "--port", type=int, default=DEFAULT_CHATGPT_PLAN_OAUTH_CALLBACK_PORT
    )
    p_add.add_argument("--no-browser", action="store_true")
    p_add.add_argument("--force", action="store_true")
    p_add.set_defaults(func=cmd_add)

    p_reauth = sub.add_parser("reauthorize", help="Reauthorize an existing profile.")
    p_reauth.add_argument("profile_id")
    p_reauth.add_argument(
        "--port", type=int, default=DEFAULT_CHATGPT_PLAN_OAUTH_CALLBACK_PORT
    )
    p_reauth.add_argument("--no-browser", action="store_true")
    p_reauth.set_defaults(func=cmd_reauthorize)

    p_refresh = sub.add_parser("refresh", help="Force-refresh the selected profile.")
    p_refresh.add_argument("profile_id")
    p_refresh.set_defaults(func=cmd_refresh)

    p_signout = sub.add_parser("signout", help="Revoke and clear local tokens.")
    p_signout.add_argument("profile_id")
    p_signout.set_defaults(func=cmd_signout)

    p_remove = sub.add_parser("remove", help="Delete a local profile file.")
    p_remove.add_argument("profile_id")
    p_remove.set_defaults(func=cmd_remove)

    p_export = sub.add_parser(
        "export", help="Export a secret-bearing profile envelope to a file."
    )
    p_export.add_argument("profile_id")
    p_export.add_argument("--output", required=True)
    p_export.set_defaults(func=cmd_export)

    p_import = sub.add_parser(
        "import",
        help="Import a profile envelope without overwriting destination host ID.",
    )
    p_import.add_argument("input")
    p_import.add_argument("--profile-id", required=True)
    p_import.set_defaults(func=cmd_import)

    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        result = asyncio.run(args.func(args))
        return int(result)
    except LLMProxyError as exc:
        print(f"Error: {exc.message}")
        details = getattr(exc, "details", None) or {}
        if details:
            # details are already redacted by connector helpers
            print(json.dumps(details, indent=2, default=str))
        return 1
    except KeyboardInterrupt:
        print("Interrupted.")
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
