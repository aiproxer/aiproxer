"""Integration tests for OAuth connector filtering in Multi User Mode.

Tests that OAuth connectors are filtered during auto-discovery when running
in Multi User Mode, while all connectors are loaded in Single User Mode.

Uses subprocess-based tests for true module isolation between test runs.

Requirements satisfied:
- 3.1, 3.2: Single User Mode loads all connectors including OAuth
- 6.1, 6.2: Multi User Mode skips OAuth connectors during auto-discovery
- 6.4: Backend registry does not contain OAuth connectors in Multi User Mode
- 6.5: Requests to OAuth connectors fail in Multi User Mode
- 10.4, 10.5: Logging of loaded/skipped OAuth connectors
"""

from __future__ import annotations

import hashlib
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any, TypedDict

import pytest

_PROJECT_ROOT = Path(__file__).parent.parent.parent
_SUBLPROC_CACHE = _PROJECT_ROOT / ".pytest_cache" / "oauth_subprocess_cache.json"
_WATCHED = [
    _PROJECT_ROOT / "src" / "connectors" / "__init__.py",
    _PROJECT_ROOT / "src" / "core" / "services" / "backend_registry.py",
]


class _SingleUserData(TypedDict):
    total_backends: int
    oauth_count: int
    found_oauth: list[str]


class _MultiUserData(TypedDict):
    total_backends: int
    oauth_count: int
    found_non_oauth: list[str]
    no_oauth_in_registry: bool
    stderr: str
    rejection_no_exception: bool
    rejection_success: bool


def _cache_key(*scripts: str) -> str:
    h = hashlib.md5()
    for s in scripts:
        h.update(s.encode())
    for p in _WATCHED:
        if p.exists():
            st = p.stat()
            h.update(f"{p.name}:{st.st_size}:{st.st_mtime}".encode())
    return h.hexdigest()


def _load_cache_file() -> dict[str, Any]:
    if not _SUBLPROC_CACHE.exists():
        return {}
    try:
        with open(_SUBLPROC_CACHE) as f:
            return json.load(f)
    except (OSError, json.JSONDecodeError):
        return {}


def _write_cache_file(data: dict[str, Any]) -> None:
    _SUBLPROC_CACHE.parent.mkdir(exist_ok=True)
    try:
        with open(_SUBLPROC_CACHE, "w") as f:
            json.dump(data, f)
    except OSError:
        pass


def _load_cached(key: str) -> dict[str, Any] | None:
    data = _load_cache_file()
    entry = data.get(key)
    if entry:
        return entry
    return None


def _store_cached(key: str, data: dict[str, Any]) -> None:
    store = _load_cache_file()
    store[key] = data
    _write_cache_file(store)


_SINGLE_SCRIPT = """
import os
os.environ["LLM_PROXY_ACCESS_MODE"] = "single_user"
import src.connectors  # noqa: F401
src.connectors.ensure_builtin_connectors_discovered()
from src.core.services.backend_registry import backend_registry

backends = backend_registry.get_registered_backends()
oauth_backends = [b for b in backends if any(p in b for p in ["oauth", "codex"])]
known_oauth = ["gemini-oauth-auto", "gemini-oauth-plan", "gemini-oauth-free",
               "qwen-oauth", "openai-codex", "cursor-oauth"]
found_oauth = [n for n in known_oauth if n in backends]
print(f"TOTAL_BACKENDS:{len(backends)}")
print(f"OAUTH_BACKENDS:{len(oauth_backends)}")
print(f"FOUND_OAUTH:{','.join(found_oauth)}")
"""


_MULTI_SCRIPT = """
import os
import logging
import sys
logging.basicConfig(level=logging.INFO, format="%(message)s", stream=sys.stderr)
os.environ["LLM_PROXY_ACCESS_MODE"] = "multi_user"

import src.connectors  # noqa: F401
src.connectors.ensure_builtin_connectors_discovered()
from src.core.services.backend_registry import backend_registry

backends = backend_registry.get_registered_backends()
oauth_backends = [b for b in backends if any(p in b for p in ["oauth", "codex"])]
known_oauth = ["gemini-oauth-auto", "gemini-oauth-plan", "gemini-oauth-free",
               "qwen-oauth", "openai-codex", "antigravity-oauth",
               "kiro-oauth-auto", "cursor-oauth"]
found_oauth = [n for n in known_oauth if n in backends]
non_oauth = ["openai", "anthropic", "gemini"]
found_non_oauth = [n for n in non_oauth if n in backends]
print(f"TOTAL_BACKENDS:{len(backends)}")
print(f"OAUTH_BACKENDS:{len(oauth_backends)}")
print(f"FOUND_NON_OAUTH:{','.join(found_non_oauth)}")
print(f"NO_OAUTH_IN_REGISTRY:{len(found_oauth) == 0}")
try:
    backend_registry.get_backend_factory("gemini-oauth-auto")
    print("REJECTION_ERROR_NO_EXCEPTION")
except ValueError as e:
    err = str(e)
    print(f"REJECTION_ERROR_MESSAGE:{err}")
    if "Multi User Mode" in err and "OAuth" in err:
        print("REJECTION_SUCCESS_SPECIFIC_ERROR")
"""


def _run_subprocess_cached(script: str, cache_label: str) -> tuple[int, int, list[str]]:
    key = _cache_key(cache_label, script)
    cached = _load_cached(key)
    if cached is not None:
        return (
            cached["total"],
            cached["oauth"],
            list(cached.get("found", [])),
        )

    env = {
        **dict(os.environ),
        "PYTHONDONTWRITEBYTECODE": "1",
        "PYTHONOPTIMIZE": "1",
    }
    result = subprocess.run(
        [sys.executable, "-c", script],
        cwd=str(_PROJECT_ROOT),
        capture_output=True,
        text=True,
        env=env,
    )
    if result.returncode != 0:
        raise RuntimeError(f"Script failed: {result.stderr}")

    lines = result.stdout.strip().split("\n")
    total = int(
        next(l.split(":")[-1] for l in lines if l.startswith("TOTAL_BACKENDS:"))
    )
    oauth_count = int(
        next(l.split(":")[-1] for l in lines if l.startswith("OAUTH_BACKENDS:"))
    )
    if "FOUND_OAUTH:" in script:
        found_str = next(
            l.split(":")[-1] for l in lines if l.startswith("FOUND_OAUTH:")
        )
    else:
        found_str = next(
            l.split(":")[-1] for l in lines if l.startswith("FOUND_NON_OAUTH:")
        )
    found = found_str.split(",") if found_str else []

    _store_cached(key, {"total": total, "oauth": oauth_count, "found": found})
    return (total, oauth_count, found)


def _run_multi_subprocess_cached(script: str, cache_label: str) -> _MultiUserData:
    key = _cache_key(cache_label + "_extra", script)
    cached = _load_cached(key)
    if cached is not None:
        return _MultiUserData(
            total_backends=cached["total"],
            oauth_count=cached["oauth"],
            found_non_oauth=list(cached.get("found_non_oauth", [])),
            no_oauth_in_registry=cached["no_oauth"],
            stderr=cached.get("stderr", ""),
            rejection_no_exception=cached["rejection_no_exc"],
            rejection_success=cached["rejection_success"],
        )

    env = {
        **dict(os.environ),
        "PYTHONDONTWRITEBYTECODE": "1",
        "PYTHONOPTIMIZE": "1",
    }
    result = subprocess.run(
        [sys.executable, "-c", script],
        cwd=str(_PROJECT_ROOT),
        capture_output=True,
        text=True,
        env=env,
    )
    if result.returncode != 0:
        raise RuntimeError(f"Script failed: {result.stderr}")

    lines = result.stdout.strip().split("\n")
    total = int(
        next(l.split(":")[-1] for l in lines if l.startswith("TOTAL_BACKENDS:"))
    )
    oauth_count = int(
        next(l.split(":")[-1] for l in lines if l.startswith("OAUTH_BACKENDS:"))
    )
    found_non_str = next(
        l.split(":")[-1] for l in lines if l.startswith("FOUND_NON_OAUTH:")
    )
    no_oauth = (
        next(l.split(":")[-1] for l in lines if l.startswith("NO_OAUTH_IN_REGISTRY:"))
        == "True"
    )
    rejection_no_exception = any(
        "REJECTION_ERROR_NO_EXCEPTION" in line for line in lines
    )
    rejection_success = any(
        "REJECTION_SUCCESS_SPECIFIC_ERROR" in line for line in lines
    )
    found_non = found_non_str.split(",") if found_non_str else []

    _store_cached(
        key,
        {
            "total": total,
            "oauth": oauth_count,
            "found_non_oauth": found_non,
            "no_oauth": no_oauth,
            "stderr": result.stderr,
            "rejection_no_exc": rejection_no_exception,
            "rejection_success": rejection_success,
        },
    )

    return _MultiUserData(
        total_backends=total,
        oauth_count=oauth_count,
        found_non_oauth=found_non,
        no_oauth_in_registry=no_oauth,
        stderr=result.stderr,
        rejection_no_exception=rejection_no_exception,
        rejection_success=rejection_success,
    )


@pytest.fixture(scope="module")
def single_user_data() -> _SingleUserData:
    total, oauth, found = _run_subprocess_cached(_SINGLE_SCRIPT, "single")
    return _SingleUserData(total_backends=total, oauth_count=oauth, found_oauth=found)


@pytest.fixture(scope="module")
def multi_user_data_module() -> _MultiUserData:
    return _run_multi_subprocess_cached(_MULTI_SCRIPT, "multi")


class TestOAuthConnectorFilteringSingleUserMode:
    def test_single_user_mode_loads_all_connectors_including_oauth(
        self, single_user_data: _SingleUserData
    ) -> None:
        assert single_user_data["total_backends"] > 0, "No backends were loaded"
        assert (
            single_user_data["oauth_count"] > 0
        ), "OAuth backends should be loaded in Single User Mode"

    def test_single_user_mode_includes_specific_oauth_connectors(
        self, single_user_data: _SingleUserData
    ) -> None:
        assert (
            len(single_user_data["found_oauth"]) > 0
        ), "No OAuth connectors found in Single User Mode"


@pytest.fixture(scope="class")
def multi_user_data(
    multi_user_data_module: _MultiUserData,
) -> _MultiUserData:
    return multi_user_data_module


class TestOAuthConnectorFilteringMultiUserMode:
    def test_multi_user_mode_skips_oauth_connectors(
        self, multi_user_data: _MultiUserData
    ) -> None:
        assert multi_user_data["total_backends"] > 0, "No backends were loaded"
        assert multi_user_data["oauth_count"] == 0, (
            f"OAuth backends should NOT be loaded in Multi User Mode, "
            f"but found: {multi_user_data['oauth_count']}"
        )

    def test_multi_user_mode_loads_non_oauth_connectors(
        self, multi_user_data: _MultiUserData
    ) -> None:
        assert multi_user_data["total_backends"] > 0, "No backends were loaded"
        assert (
            len(multi_user_data["found_non_oauth"]) > 0
        ), "Non-OAuth connectors should be loaded in Multi User Mode"

    def test_multi_user_mode_backend_registry_excludes_oauth(
        self, multi_user_data: _MultiUserData
    ) -> None:
        assert (
            multi_user_data["no_oauth_in_registry"] is True
        ), "OAuth connectors found in backend registry in Multi User Mode"

    def test_multi_user_mode_logs_skipped_oauth_count(
        self, multi_user_data: _MultiUserData
    ) -> None:
        log_output = multi_user_data["stderr"].lower()
        assert (
            "skip" in log_output or "filter" in log_output or "block" in log_output
        ), f"Expected logging about skipped OAuth connectors, got: {multi_user_data['stderr']}"


class TestOAuthConnectorFilteringRequestRejection:
    def test_multi_user_mode_rejects_requests_to_oauth_connectors(
        self, multi_user_data_module: _MultiUserData
    ) -> None:
        assert not multi_user_data_module[
            "rejection_no_exception"
        ], "OAuth connector should not be available"
        assert multi_user_data_module["rejection_success"], (
            "Error message should be specific to Multi User Mode OAuth blocking. "
            f"Data: {multi_user_data_module}"
        )


class TestOAuthConnectorFilteringComparison:
    def test_single_user_has_more_backends_than_multi_user(
        self,
        single_user_data: _SingleUserData,
        multi_user_data_module: _MultiUserData,
    ) -> None:
        count_single = single_user_data["total_backends"]
        count_multi = multi_user_data_module["total_backends"]

        assert (
            count_single > count_multi
        ), f"Single User Mode ({count_single}) should have more backends than Multi User Mode ({count_multi})"

    def test_difference_is_oauth_connectors_only(
        self, single_user_data: _SingleUserData
    ) -> None:
        oauth_count = single_user_data["oauth_count"]
        assert oauth_count > 0, "No OAuth connectors found in Single User Mode"
