"""Atomic host and per-profile persistence for openai-chatgpt-plan.

Host identity lives in a single ``host.json`` file and is never written into
profile records. Profiles are one JSON file per sanitized local ``profile_id``.
Token values are stored on disk but must not appear in logs, captures, or
exception text.
"""

from __future__ import annotations

import asyncio
import contextlib
import json
import logging
import os
import stat
import subprocess
import tempfile
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from pydantic import ValidationError as PydanticValidationError

from src.connectors.openai_chatgpt_plan.models import (
    HOST_SCHEMA_VERSION,
    PROFILE_SCHEMA_VERSION,
    ChatGPTPlanHostState,
    ChatGPTPlanProfile,
    ChatGPTPlanProfileStatus,
    ChatGPTPlanProfileSummary,
    is_valid_profile_id,
)
from src.core.common.exceptions import LLMProxyError

logger = logging.getLogger(__name__)

_PERMISSION_ERROR_RETRIES = 3
_PERMISSION_ERROR_BASE_DELAY = 0.05
_OWNER_ONLY_MODE = stat.S_IRUSR | stat.S_IWUSR


class ChatGPTPlanStorageError(LLMProxyError):
    """Persistence failure for ChatGPT-plan host or profile files."""

    def __init__(
        self,
        message: str,
        details: dict[str, Any] | None = None,
        **kwargs: Any,
    ) -> None:
        status_code = kwargs.pop("status_code", 500)
        super().__init__(message, details=details, status_code=status_code, **kwargs)


class ChatGPTPlanHostStorageError(ChatGPTPlanStorageError):
    """Corrupt, unreadable, or unwritable ChatGPT-plan host state."""


class ChatGPTPlanProfileStorageError(ChatGPTPlanStorageError):
    """Corrupt, unreadable, or unwritable ChatGPT-plan profile file."""


def _utc_now() -> datetime:
    return datetime.now(timezone.utc)


def _new_host_id() -> str:
    """Mint a SIWC-supported host ID (``urn:uuid:`` + hyphenated UUIDv4)."""

    return f"urn:uuid:{uuid.uuid4()}"


def _apply_windows_owner_acl(path: Path) -> bool:
    """Restrict NTFS DACL to the current user (read/write). Best-effort.

    POSIX ``chmod`` cannot express owner-only NTFS ACLs. When ``icacls`` is
    unavailable or fails, callers still complete the atomic write and rely on
    the parent directory's existing ACL (documented Windows fallback).
    """

    user = os.environ.get("USERNAME") or os.environ.get("USER")
    if not user:
        logger.debug(
            "Windows owner-only ACL skipped for %s: USERNAME is unset", path.name
        )
        return False
    try:
        completed = subprocess.run(
            [
                "icacls",
                str(path),
                "/inheritance:r",
                "/grant:r",
                f"{user}:(R,W)",
            ],
            check=False,
            capture_output=True,
            text=True,
            timeout=15,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        logger.debug(
            "Windows owner-only ACL failed for %s: %s",
            path.name,
            exc,
            exc_info=True,
        )
        return False
    if completed.returncode != 0:
        logger.debug(
            "icacls owner-only failed for %s (exit %s)",
            path.name,
            completed.returncode,
        )
        return False
    return True


def _apply_owner_only_permissions(path: Path) -> bool:
    """Apply owner-only permissions where the OS supports them.

    On POSIX, ``chmod 0600`` is the security boundary. On Windows, ``chmod``
    is still attempted but is not sufficient; a best-effort DACL restriction
    is applied, and persistence continues if that helper fails.
    """

    chmod_ok = False
    try:
        os.chmod(path, _OWNER_ONLY_MODE)
        chmod_ok = True
    except OSError as exc:
        logger.debug(
            "chmod owner-only failed for %s: %s", path.name, exc, exc_info=True
        )

    if os.name == "nt":
        return _apply_windows_owner_acl(path)
    return chmod_ok


def _atomic_replace_with_retry(
    src: str,
    dst: str,
    *,
    retries: int = _PERMISSION_ERROR_RETRIES,
    base_delay: float = _PERMISSION_ERROR_BASE_DELAY,
) -> None:
    last_exc: PermissionError | None = None
    for attempt in range(retries):
        try:
            os.replace(src, dst)
            return
        except PermissionError as exc:
            last_exc = exc
            if attempt < retries - 1:
                time.sleep(base_delay * (attempt + 1))
    if last_exc is not None:
        raise last_exc
    raise PermissionError(f"Failed to replace {dst}")


def _write_atomic_json(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    content = json.dumps(payload, indent=2)
    fd, temp_path = tempfile.mkstemp(
        dir=str(path.parent),
        prefix=f"{path.stem}_",
        suffix=".tmp",
        text=True,
    )
    try:
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as handle:
                handle.write(content)
                handle.flush()
                os.fsync(handle.fileno())
        except Exception:
            with contextlib.suppress(OSError):
                os.close(fd)
            raise
        _apply_owner_only_permissions(Path(temp_path))
        _atomic_replace_with_retry(temp_path, str(path))
        _apply_owner_only_permissions(path)
    except Exception:
        with contextlib.suppress(OSError):
            os.unlink(temp_path)
        raise


def _read_text_with_retry(path: Path) -> str:
    last_exc: PermissionError | None = None
    for attempt in range(_PERMISSION_ERROR_RETRIES):
        try:
            return path.read_text(encoding="utf-8")
        except PermissionError as exc:
            last_exc = exc
            if attempt < _PERMISSION_ERROR_RETRIES - 1:
                time.sleep(_PERMISSION_ERROR_BASE_DELAY * (attempt + 1))
    if last_exc is not None:
        raise last_exc
    raise PermissionError(f"Failed to read {path.name}")


def _parse_json_object(raw: str) -> dict[str, Any]:
    try:
        parsed: object = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ValueError("file is not valid JSON") from exc
    if not isinstance(parsed, dict):
        raise ValueError("file must contain a JSON object")
    return parsed


class ChatGPTPlanHostStore:
    """Load or mint the installation-scoped ``ext_agent_host_id``."""

    def __init__(self, path: Path | str) -> None:
        self._path = Path(path)
        self._lock = asyncio.Lock()

    @property
    def path(self) -> Path:
        return self._path

    async def get_or_create(self) -> ChatGPTPlanHostState:
        async with self._lock:
            return await asyncio.to_thread(self._get_or_create_sync)

    def _get_or_create_sync(self) -> ChatGPTPlanHostState:
        if self._path.is_file():
            return self._load_sync()
        state = ChatGPTPlanHostState(
            schema_version=HOST_SCHEMA_VERSION,
            ext_agent_host_id=_new_host_id(),
            created_at=_utc_now(),
        )
        try:
            _write_atomic_json(self._path, state.model_dump(mode="json"))
        except OSError as exc:
            raise ChatGPTPlanHostStorageError(
                "Failed to write ChatGPT-plan host state. Check directory "
                "permissions for the configured host_state_path.",
                details={"path": str(self._path)},
            ) from exc
        logger.debug("Created ChatGPT-plan host state at %s", self._path.name)
        return state

    def _load_sync(self) -> ChatGPTPlanHostState:
        try:
            raw = _read_text_with_retry(self._path)
            payload = _parse_json_object(raw)
            version = payload.get("schema_version")
            if version != HOST_SCHEMA_VERSION:
                raise ChatGPTPlanHostStorageError(
                    "ChatGPT-plan host state has an unsupported schema_version. "
                    "Do not mint a replacement host ID; repair the host.json file.",
                    details={
                        "path": str(self._path),
                        "schema_version": version,
                    },
                )
            return ChatGPTPlanHostState.model_validate(payload)
        except ChatGPTPlanHostStorageError:
            raise
        except PermissionError as exc:
            raise ChatGPTPlanHostStorageError(
                "Permission denied reading ChatGPT-plan host state. "
                "Check file ownership and ACLs for host.json.",
                details={"path": str(self._path)},
            ) from exc
        except (ValueError, PydanticValidationError, OSError) as exc:
            raise ChatGPTPlanHostStorageError(
                "ChatGPT-plan host state is corrupt or invalid. "
                "Repair host.json; a new ext_agent_host_id will not be minted. "
                "If ext_agent_host_id is a bare opaque token (not urn:uuid:, "
                "urn:ietf:params:oauth:jwk-thumbprint:, or did:key:), delete "
                "host.json when no authorized profiles exist, then re-run "
                "profile add so a SIWC-compliant host ID is minted.",
                details={"path": str(self._path)},
            ) from exc


class ChatGPTPlanProfileStore:
    """Directory-backed store with one schema-versioned profile file per ID."""

    def __init__(self, profiles_path: Path | str) -> None:
        self._profiles_path = Path(profiles_path)

    @property
    def profiles_path(self) -> Path:
        return self._profiles_path

    def _profile_path(self, profile_id: str) -> Path:
        return self._profiles_path / f"{profile_id}.json"

    def _ensure_dir_sync(self) -> None:
        self._profiles_path.mkdir(parents=True, exist_ok=True)

    def _iter_profile_files_sync(self) -> list[Path]:
        if not self._profiles_path.exists():
            return []
        return sorted(
            path
            for path in self._profiles_path.iterdir()
            if path.is_file()
            and path.suffix == ".json"
            and is_valid_profile_id(path.stem)
        )

    def _load_from_path_sync(
        self, path: Path, *, profile_id: str
    ) -> ChatGPTPlanProfile:
        raw = _read_text_with_retry(path)
        payload = _parse_json_object(raw)
        version = payload.get("schema_version")
        if version != PROFILE_SCHEMA_VERSION:
            raise ChatGPTPlanProfileStorageError(
                f"ChatGPT-plan profile '{profile_id}' has an unsupported "
                "schema_version. Repair or delete that profile file; other "
                "profiles are unaffected.",
                details={
                    "profile_id": profile_id,
                    "path": str(path),
                    "schema_version": version,
                },
            )
        profile = ChatGPTPlanProfile.model_validate(payload)
        if profile.profile_id != profile_id:
            raise ChatGPTPlanProfileStorageError(
                f"ChatGPT-plan profile file '{path.name}' does not match stored "
                f"profile_id '{profile.profile_id}'. Rename or repair the file "
                "so the filename stem equals profile_id; other profiles are "
                "unaffected.",
                details={
                    "profile_id": profile.profile_id,
                    "path": str(path),
                    "expected_id": profile_id,
                },
            )
        return profile

    def _diagnose_load_failure(
        self, profile_id: str, path: Path, exc: BaseException
    ) -> ChatGPTPlanProfileStorageError:
        if isinstance(exc, ChatGPTPlanProfileStorageError):
            return exc
        if isinstance(exc, PermissionError):
            return ChatGPTPlanProfileStorageError(
                f"Permission denied reading ChatGPT-plan profile '{profile_id}'. "
                "Check file ownership and ACLs, then retry. Other profiles "
                "are unaffected.",
                details={"profile_id": profile_id, "path": str(path)},
            )
        return ChatGPTPlanProfileStorageError(
            f"ChatGPT-plan profile '{profile_id}' is corrupt or unreadable. "
            "Repair or delete that profile file; other profiles are unaffected.",
            details={"profile_id": profile_id, "path": str(path)},
        )

    async def list_profiles(self) -> list[ChatGPTPlanProfileSummary]:
        return await asyncio.to_thread(self._list_profiles_sync)

    def _list_profiles_sync(self) -> list[ChatGPTPlanProfileSummary]:
        self._ensure_dir_sync()
        summaries: list[ChatGPTPlanProfileSummary] = []
        for path in self._iter_profile_files_sync():
            profile_id = path.stem
            try:
                profile = self._load_from_path_sync(path, profile_id=profile_id)
            except (
                ChatGPTPlanProfileStorageError,
                PermissionError,
                ValueError,
                PydanticValidationError,
                OSError,
            ):
                logger.error(
                    "ChatGPT-plan profile '%s' is corrupt, unreadable, or has "
                    "an unsupported schema; skipping that profile. Repair or "
                    "delete %s. Other profiles are unaffected.",
                    profile_id,
                    path.name,
                )
                continue
            summaries.append(ChatGPTPlanProfileSummary.from_profile(profile))
        return summaries

    async def load(self, profile_id: str) -> ChatGPTPlanProfile | None:
        return await asyncio.to_thread(self._load_sync, profile_id)

    def _load_sync(self, profile_id: str) -> ChatGPTPlanProfile | None:
        if not is_valid_profile_id(profile_id):
            return None
        path = self._profile_path(profile_id)
        if not path.is_file():
            return None
        try:
            return self._load_from_path_sync(path, profile_id=profile_id)
        except (
            ChatGPTPlanProfileStorageError,
            PermissionError,
            ValueError,
            PydanticValidationError,
            OSError,
        ) as exc:
            raise self._diagnose_load_failure(profile_id, path, exc) from exc

    async def save_atomic(self, profile: ChatGPTPlanProfile) -> None:
        await asyncio.to_thread(self._save_atomic_sync, profile)

    def _save_atomic_sync(self, profile: ChatGPTPlanProfile) -> None:
        path = self._profile_path(profile.profile_id)
        payload = profile.model_dump(mode="json")
        try:
            _write_atomic_json(path, payload)
        except OSError as exc:
            raise ChatGPTPlanProfileStorageError(
                f"Failed to write ChatGPT-plan profile '{profile.profile_id}'. "
                "Check directory permissions for the configured profiles_path.",
                details={
                    "profile_id": profile.profile_id,
                    "path": str(path),
                },
            ) from exc
        logger.debug("Saved ChatGPT-plan profile %s", profile.profile_id)

    async def clear_tokens(
        self, profile_id: str, status: ChatGPTPlanProfileStatus
    ) -> None:
        profile = await self.load(profile_id)
        if profile is None:
            raise ChatGPTPlanProfileStorageError(
                f"ChatGPT-plan profile '{profile_id}' was not found.",
                details={"profile_id": profile_id},
                status_code=404,
            )
        updated = profile.model_copy(
            update={
                "access_token": None,
                "refresh_token": None,
                "id_token": None,
                "access_token_expires_at": None,
                "refresh_token_expires_at": None,
                "status": status,
                "updated_at": _utc_now(),
            }
        )
        await self.save_atomic(updated)

    async def delete(self, profile_id: str) -> None:
        await asyncio.to_thread(self._delete_sync, profile_id)

    def _delete_sync(self, profile_id: str) -> None:
        if not is_valid_profile_id(profile_id):
            return
        path = self._profile_path(profile_id)
        if not path.is_file():
            return
        try:
            path.unlink()
        except OSError as exc:
            raise ChatGPTPlanProfileStorageError(
                f"Failed to delete ChatGPT-plan profile '{profile_id}'.",
                details={"profile_id": profile_id, "path": str(path)},
            ) from exc
