"""Verified installation of the latest stable go-freebuff-acp-wrapper release."""

from __future__ import annotations

import asyncio
import hashlib
import os
import platform
import re
import shutil
import stat
import tarfile
import tempfile
import zipfile
from pathlib import Path

import httpx

REPOSITORY = "matdev83/go-freebuff-acp-wrapper"
LATEST_RELEASE_URL = f"https://api.github.com/repos/{REPOSITORY}/releases/latest"
_MAX_DOWNLOAD_BYTES = 64 * 1024 * 1024
_INSTALL_LOCK = asyncio.Lock()


def default_cache_dir() -> Path:
    override = os.environ.get("FREEBUFF_ACP_WRAPPER_CACHE_DIR", "").strip()
    if override:
        return Path(override).expanduser()
    if os.name == "nt":
        root = Path(os.environ.get("LOCALAPPDATA", Path.home() / "AppData/Local"))
    elif platform.system() == "Darwin":
        root = Path.home() / "Library/Caches"
    else:
        root = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache"))
    return root / "llm-interactive-proxy" / "go-freebuff-acp-wrapper"


def platform_asset_suffix() -> str:
    systems = {"Windows": "windows", "Linux": "linux", "Darwin": "darwin"}
    machines = {
        "x86_64": "amd64",
        "amd64": "amd64",
        "aarch64": "arm64",
        "arm64": "arm64",
    }
    system = systems.get(platform.system())
    machine = machines.get(platform.machine().lower())
    if not system or not machine:
        raise RuntimeError(
            f"unsupported wrapper platform: {platform.system()}/{platform.machine()}"
        )
    extension = "zip" if system == "windows" else "tar.gz"
    return f"_{system}_{machine}.{extension}"


def _cached_executables(cache_dir: Path) -> list[Path]:
    name = (
        "go-freebuff-acp-wrapper.exe" if os.name == "nt" else "go-freebuff-acp-wrapper"
    )
    return sorted(
        cache_dir.glob(f"*/{name}"),
        key=lambda path: path.parent.stat().st_mtime,
        reverse=True,
    )


def _checksum_for(checksums: str, asset_name: str) -> str:
    for line in checksums.splitlines():
        fields = line.strip().split(maxsplit=1)
        if len(fields) == 2 and fields[1].lstrip("*") == asset_name:
            digest = fields[0].lower()
            if re.fullmatch(r"[0-9a-f]{64}", digest):
                return digest
    raise RuntimeError(f"checksum missing for release asset {asset_name}")


async def _download(client: httpx.AsyncClient, url: str) -> bytes:
    response = await client.get(url, follow_redirects=True, timeout=60)
    response.raise_for_status()
    content = response.content
    if len(content) > _MAX_DOWNLOAD_BYTES:
        raise RuntimeError(f"wrapper download exceeds {_MAX_DOWNLOAD_BYTES} bytes")
    return content


def _extract(archive: Path, destination: Path) -> Path:
    executable_name = (
        "go-freebuff-acp-wrapper.exe"
        if archive.suffix == ".zip"
        else "go-freebuff-acp-wrapper"
    )
    if archive.suffix == ".zip":
        with zipfile.ZipFile(archive) as package:
            member = next(
                (
                    name
                    for name in package.namelist()
                    if Path(name).name == executable_name
                ),
                None,
            )
            if member is None:
                raise RuntimeError("wrapper executable missing from release archive")
            with (
                package.open(member) as source,
                (destination / executable_name).open("wb") as target,
            ):
                shutil.copyfileobj(source, target)
    else:
        with tarfile.open(archive, "r:gz") as package:
            tar_member = next(
                (
                    item
                    for item in package.getmembers()
                    if Path(item.name).name == executable_name
                ),
                None,
            )
            if tar_member is None or not tar_member.isfile():
                raise RuntimeError("wrapper executable missing from release archive")
            tar_source = package.extractfile(tar_member)
            if tar_source is None:
                raise RuntimeError("cannot extract wrapper executable")
            with tar_source, (destination / executable_name).open("wb") as target:
                shutil.copyfileobj(tar_source, target)
    executable = destination / executable_name
    executable.chmod(
        executable.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH
    )
    return executable


async def install_latest_wrapper(
    client: httpx.AsyncClient,
    *,
    cache_dir: Path | None = None,
    release_url: str = LATEST_RELEASE_URL,
) -> str:
    """Install and return the latest stable wrapper, falling back to cache offline."""
    root = (cache_dir or default_cache_dir()).resolve()
    async with _INSTALL_LOCK:
        try:
            response = await client.get(release_url, follow_redirects=False, timeout=30)
            response.raise_for_status()
            release = response.json()
            if release.get("draft") or release.get("prerelease"):
                raise RuntimeError("GitHub latest release is not stable")
            tag = str(release.get("tag_name", "")).strip()
            if not re.fullmatch(r"v\d+\.\d+\.\d+", tag):
                raise RuntimeError(f"invalid wrapper release tag: {tag!r}")
            suffix = platform_asset_suffix()
            assets = {
                str(asset.get("name")): asset for asset in release.get("assets", [])
            }
            matches = [name for name in assets if name.endswith(suffix)]
            if len(matches) != 1 or "checksums.txt" not in assets:
                raise RuntimeError(f"wrapper release assets incomplete for {suffix}")
            asset_name = matches[0]
            installed = (
                root
                / tag
                / (
                    "go-freebuff-acp-wrapper.exe"
                    if suffix.endswith(".zip")
                    else "go-freebuff-acp-wrapper"
                )
            )
            if installed.is_file():
                return str(installed)

            checksums = (
                await _download(
                    client, str(assets["checksums.txt"]["browser_download_url"])
                )
            ).decode("utf-8")
            archive_bytes = await _download(
                client, str(assets[asset_name]["browser_download_url"])
            )
            expected = _checksum_for(checksums, asset_name)
            actual = hashlib.sha256(archive_bytes).hexdigest()
            if actual != expected:
                raise RuntimeError(
                    f"wrapper checksum mismatch for {asset_name}: expected {expected}, got {actual}"
                )

            root.mkdir(parents=True, exist_ok=True)
            with tempfile.TemporaryDirectory(
                prefix="freebuff-wrapper-", dir=root
            ) as temp:
                temp_dir = Path(temp)
                archive = temp_dir / asset_name
                archive.write_bytes(archive_bytes)
                executable = _extract(archive, temp_dir)
                target_dir = root / tag
                if not target_dir.exists():
                    temp_install = Path(
                        tempfile.mkdtemp(prefix=f".{tag}.installing-", dir=root)
                    )
                    try:
                        shutil.move(str(executable), temp_install / executable.name)
                        try:
                            temp_install.replace(target_dir)
                        except OSError:
                            if not installed.is_file():
                                raise
                    finally:
                        if temp_install.exists():
                            shutil.rmtree(temp_install)
            return str(installed)
        except (httpx.HTTPError, OSError, ValueError, RuntimeError):
            cached = _cached_executables(root) if root.exists() else []
            if cached:
                return str(cached[0])
            raise
