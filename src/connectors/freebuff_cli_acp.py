"""Freebuff CLI backend via ACP wrapper."""

from __future__ import annotations

import asyncio
import logging
import os
import re
import shutil
import subprocess
from collections.abc import Sequence
from pathlib import Path
from typing import Any

import httpx

from src.connectors.acp_core.base_connector import BaseAcpConnector
from src.connectors.acp_core.types import ACPNotification, ACPProcessRuntime
from src.connectors.acp_core.workspace_policy import resolve_backend_init_acp_workspace
from src.connectors.base import strip_vendor_prefix
from src.connectors.freebuff_acp_wrapper_installer import install_latest_wrapper
from src.core.common.exceptions import BackendError, ConfigurationError
from src.core.common.model_catalog import BackendModelEnumeration
from src.core.config.app_config import AppConfig, BackendConfig
from src.core.services.translation_service import TranslationService

logger = logging.getLogger(__name__)

ACP_PROTOCOL_VERSION = 1
DEFAULT_FREEBUFF_PROCESS_TIMEOUT_SECONDS = 300.0
DEFAULT_FREEBUFF_MODEL = "mimo/mimo-v2.5"
CANONICAL_MODEL_ID_PATTERN = re.compile(
    r"^[a-z0-9][a-z0-9._-]*/[a-zA-Z0-9][a-zA-Z0-9._-]*$"
)


def parse_freebuff_wrapper_model_catalog(stdout: str) -> list[str]:
    """Parse canonical model IDs emitted by go-freebuff-acp-wrapper --list-models."""
    models: list[str] = []
    seen: set[str] = set()
    for line in stdout.splitlines():
        model = line.strip()
        if not CANONICAL_MODEL_ID_PATTERN.fullmatch(model) or model in seen:
            continue
        seen.add(model)
        models.append(model)
    return models


def resolve_freebuff_acp_wrapper_executable(configured: str | None) -> str | None:
    """Resolve go-freebuff-acp-wrapper binary suitable for subprocess.Popen."""
    for candidate in (
        (configured or "").strip(),
        os.environ.get("FREEBUFF_ACP_WRAPPER_BIN", "").strip(),
    ):
        if not candidate:
            continue
        p = Path(candidate)
        if p.is_file():
            return str(p.resolve())
        resolved = shutil.which(candidate)
        if resolved:
            return resolved
    for name in ("go-freebuff-acp-wrapper", "go-freebuff-acp-wrapper.exe"):
        resolved = shutil.which(name)
        if resolved:
            return resolved
    for dev_path in (
        Path(
            "C:/Users/Mateusz/source/repos/go-freebuff-acp-wrapper/go-freebuff-acp-wrapper.exe"
        ),
        Path.home()
        / "source/repos/go-freebuff-acp-wrapper/go-freebuff-acp-wrapper.exe",
    ):
        if dev_path.is_file():
            return str(dev_path.resolve())
    return None


def build_freebuff_acp_wrapper_command(
    executable: str,
    *,
    model: str | None = None,
    termctrl_binary: str | None = None,
    freebuff_binary: str | None = None,
    lock_dir: str | None = None,
    timeout_seconds: int | None = None,
    pace_ms: int | None = None,
    extra_args: Sequence[str] | None = None,
) -> list[str]:
    cmd = [executable]
    if model and model != "auto":
        cmd.extend(["--default-model", model])
    if termctrl_binary:
        cmd.extend(["--termctrl-binary", termctrl_binary])
    if freebuff_binary:
        cmd.extend(["--freebuff-binary", freebuff_binary])
    if lock_dir:
        cmd.extend(["--lock-dir", lock_dir])
    if timeout_seconds is not None and timeout_seconds > 0:
        cmd.extend(["--timeout", str(timeout_seconds)])
    if pace_ms is not None and pace_ms >= 0:
        cmd.extend(["--pace-ms", str(pace_ms)])
    if extra_args:
        cmd.extend(list(extra_args))
    return cmd


def build_freebuff_model_catalog_command(executable: str) -> list[str]:
    """Build the wrapper's non-interactive canonical catalog command."""
    return [executable, "--list-models"]


async def run_freebuff_wrapper_probe(
    command: list[str], *, timeout: float
) -> tuple[int, bytes, bytes]:
    """Run a wrapper probe without requiring asyncio subprocess support."""
    process = subprocess.Popen(
        command,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        shell=False,
    )
    communicate_task = asyncio.create_task(asyncio.to_thread(process.communicate))
    try:
        stdout, stderr = await asyncio.wait_for(
            asyncio.shield(communicate_task), timeout
        )
    except BaseException:
        if process.poll() is None:
            process.kill()
        await communicate_task
        raise
    return process.returncode or 0, stdout, stderr


class FreebuffCliConfiguredModelEnumerator:
    """Enumerate canonical Freebuff routes through the configured wrapper."""

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        extra = config.extra
        configured = extra.get("wrapper_executable") or extra.get(
            "freebuff_acp_wrapper_executable"
        )
        executable = resolve_freebuff_acp_wrapper_executable(
            str(configured) if configured else None
        )
        auto_download = bool(extra.get("wrapper_auto_download", True))
        if os.environ.get("FREEBUFF_ACP_WRAPPER_AUTO_DOWNLOAD", "").strip().lower() in {
            "0",
            "false",
            "no",
            "off",
        }:
            auto_download = False
        if executable is None and auto_download:
            cache_dir = extra.get("wrapper_cache_dir")
            async with httpx.AsyncClient() as client:
                try:
                    executable = await install_latest_wrapper(
                        client,
                        cache_dir=(
                            Path(str(cache_dir)).expanduser() if cache_dir else None
                        ),
                    )
                except Exception:
                    logger.debug(
                        "auto-download of freebuff wrapper failed", exc_info=True
                    )
        if executable is None:
            return BackendModelEnumeration.unavailable(
                instance_name=instance_name,
                connector="freebuff-cli-acp",
                source="freebuff_wrapper",
                error_code="wrapper_not_found",
                instance_pinned=True,
            )

        command = build_freebuff_model_catalog_command(executable)
        timeout = float(extra.get("model_discovery_timeout_seconds", 15.0))
        try:
            returncode, stdout, _ = await run_freebuff_wrapper_probe(
                command, timeout=timeout
            )
        except (TimeoutError, FileNotFoundError, OSError):
            returncode = 1
            stdout = b""

        models = (
            parse_freebuff_wrapper_model_catalog(
                stdout.decode("utf-8", errors="replace")
            )
            if returncode == 0
            else []
        )

        if not models:
            models = [DEFAULT_FREEBUFF_MODEL]
            configured_models = extra.get("models")
            if isinstance(configured_models, list):
                for m in configured_models:
                    m_str = str(m).strip()
                    if m_str and m_str not in models:
                        models.append(m_str)

        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector="freebuff-cli-acp",
            models=models,
            source="freebuff_wrapper",
            instance_pinned=True,
        )


class FreebuffCliAcpConnector(BaseAcpConnector[ACPProcessRuntime]):
    """Freebuff AI coding assistant backend through go-freebuff-acp-wrapper."""

    backend_type: str = "freebuff-cli-acp"
    VENDOR_PREFIX: str = "freebuff"
    requires_explicit_workspace: bool = True

    def __init__(
        self,
        client: httpx.AsyncClient,
        config: AppConfig,
        translation_service: TranslationService,
        **_: Any,
    ) -> None:
        super().__init__(config, translation_service=translation_service)
        self.client = client
        self.name = "freebuff-cli-acp"
        self._wrapper_executable = "go-freebuff-acp-wrapper"
        self._termctrl_binary: str | None = None
        self._freebuff_binary: str | None = None
        self._lock_dir: str | None = None
        self._model = DEFAULT_FREEBUFF_MODEL
        self._configured_models: list[str] = []
        self._mcp_servers: list[Any] = []
        self._extra_wrapper_args: list[str] = []
        self._process_timeout = DEFAULT_FREEBUFF_PROCESS_TIMEOUT_SECONDS
        self._pace_ms: int | None = None

    async def initialize(self, **kwargs: Any) -> None:
        try:
            workspace, cfg_err = resolve_backend_init_acp_workspace(
                project_dir=kwargs.get("project_dir"),
                workspace_path=kwargs.get("workspace_path"),
                env_workspace=os.getenv("FREEBUFF_CLI_WORKSPACE"),
                env_source_label="FREEBUFF_CLI_WORKSPACE",
                is_usable=self._is_usable_directory,
            )
            if cfg_err:
                raise ConfigurationError(
                    message=cfg_err,
                    details={"error_code": "freebuff_cli_acp_workspace_invalid"},
                )
            self._default_project_dir = workspace

            wrapper = kwargs.get("wrapper_executable") or kwargs.get(
                "freebuff_acp_wrapper_executable"
            )
            self._wrapper_executable = str(wrapper or self._wrapper_executable)

            termctrl_bin = kwargs.get("termctrl_binary") or os.environ.get(
                "TERMCTRL_BINARY"
            )
            self._termctrl_binary = str(termctrl_bin).strip() if termctrl_bin else None

            freebuff_bin = kwargs.get("freebuff_binary") or os.environ.get(
                "FREEBUFF_BINARY"
            )
            self._freebuff_binary = str(freebuff_bin).strip() if freebuff_bin else None

            lock_dir = kwargs.get("lock_dir") or os.environ.get("FREEBUFF_LOCK_DIR")
            self._lock_dir = str(lock_dir).strip() if lock_dir else None

            configured_model = str(kwargs.get("model") or self._model)
            self._model = strip_vendor_prefix(configured_model, self.VENDOR_PREFIX)

            configured_models = kwargs.get("models")
            self._configured_models = (
                [
                    str(model).strip()
                    for model in configured_models
                    if str(model).strip()
                ]
                if isinstance(configured_models, list)
                else []
            )

            self._process_timeout = float(
                kwargs.get("process_timeout", self._process_timeout)
            )
            self._idle_timeout = float(kwargs.get("idle_timeout", self._idle_timeout))
            pace_ms = kwargs.get("pace_ms")
            self._pace_ms = int(pace_ms) if pace_ms is not None else None

            mcp = kwargs.get("mcp_servers", [])
            self._mcp_servers = mcp if isinstance(mcp, list) else []

            extra = kwargs.get("wrapper_extra_args") or kwargs.get(
                "freebuff_acp_wrapper_extra_args"
            )
            if isinstance(extra, list):
                self._extra_wrapper_args = [str(x) for x in extra]
            elif isinstance(extra, str) and extra.strip():
                self._extra_wrapper_args = [extra.strip()]
            else:
                self._extra_wrapper_args = []

            resolved = resolve_freebuff_acp_wrapper_executable(self._wrapper_executable)
            auto_download = bool(kwargs.get("wrapper_auto_download", True))
            if os.environ.get(
                "FREEBUFF_ACP_WRAPPER_AUTO_DOWNLOAD", ""
            ).strip().lower() in {
                "0",
                "false",
                "no",
                "off",
            }:
                auto_download = False
            if resolved is None and auto_download:
                cache_dir = kwargs.get("wrapper_cache_dir")
                try:
                    resolved = await install_latest_wrapper(
                        self.client,
                        cache_dir=(
                            Path(str(cache_dir)).expanduser() if cache_dir else None
                        ),
                    )
                except Exception:
                    logger.debug(
                        "auto-download of freebuff wrapper failed", exc_info=True
                    )
            if resolved is None:
                raise ConfigurationError(
                    message="go-freebuff-acp-wrapper executable not found",
                    details={
                        "configured": self._wrapper_executable,
                        "hint": "Enable wrapper_auto_download, put the wrapper on PATH, "
                        "or set FREEBUFF_ACP_WRAPPER_BIN / wrapper_executable.",
                    },
                )
            self._wrapper_executable = resolved

            if not await self._check_wrapper_available():
                raise ConfigurationError(
                    message=f"go-freebuff-acp-wrapper not callable: {self._wrapper_executable}",
                    details={"executable": self._wrapper_executable},
                )

            if not self._configured_models:
                self._configured_models = await self._discover_models()

            self._validation_errors = []
            self._initialization_failed = False
            self.is_functional = True
        except Exception:
            self._initialization_failed = True
            self.is_functional = False
            self._validation_errors = ["freebuff-cli-acp initialization failed"]
            raise

    async def _run_probe(
        self, command: list[str], *, timeout: float
    ) -> tuple[int, bytes, bytes]:
        return await run_freebuff_wrapper_probe(command, timeout=timeout)

    async def _discover_models(self) -> list[str]:
        command = build_freebuff_model_catalog_command(self._wrapper_executable)
        try:
            returncode, stdout, stderr = await self._run_probe(command, timeout=15)
        except (TimeoutError, FileNotFoundError, OSError):
            logger.warning("freebuff model discovery failed", exc_info=True)
            return [DEFAULT_FREEBUFF_MODEL]
        if returncode != 0:
            logger.warning(
                "freebuff wrapper model discovery exited with code %s: %s",
                returncode,
                stderr.decode("utf-8", errors="replace").strip(),
            )
            return [DEFAULT_FREEBUFF_MODEL]
        models = parse_freebuff_wrapper_model_catalog(
            stdout.decode("utf-8", errors="replace")
        )
        return models if models else [DEFAULT_FREEBUFF_MODEL]

    async def _check_wrapper_available(self) -> bool:
        try:
            returncode, _, _ = await self._run_probe(
                [self._wrapper_executable, "--version"], timeout=10
            )
            return returncode == 0
        except (TimeoutError, FileNotFoundError, OSError):
            return False

    async def _build_subprocess_command(self, runtime: ACPProcessRuntime) -> list[str]:
        timeout = int(self._process_timeout) if self._process_timeout > 0 else None
        return build_freebuff_acp_wrapper_command(
            self._wrapper_executable,
            model=runtime.model,
            termctrl_binary=self._termctrl_binary,
            freebuff_binary=self._freebuff_binary,
            lock_dir=self._lock_dir,
            timeout_seconds=timeout,
            pace_ms=self._pace_ms,
            extra_args=self._extra_wrapper_args,
        )

    def _create_runtime(
        self, project_dir: Path, model: str, client_session_id: str = "default"
    ) -> ACPProcessRuntime:
        return ACPProcessRuntime(
            project_dir=project_dir,
            model=model,
            client_session_id=client_session_id,
            process_lock=asyncio.Lock(),
            request_lock=asyncio.Lock(),
            cancellation_lock=asyncio.Lock(),
            cancellation_event=asyncio.Event(),
        )

    async def _perform_handshake(self, runtime: ACPProcessRuntime) -> None:
        initialize_id = await self._send_jsonrpc_message(
            runtime,
            "initialize",
            {
                "protocolVersion": ACP_PROTOCOL_VERSION,
                "clientCapabilities": {
                    "fs": {"readTextFile": False, "writeTextFile": False},
                    "terminal": False,
                },
                "clientInfo": {
                    "name": "llm-interactive-proxy",
                    "version": "1",
                },
            },
        )
        initialize_response = await self._await_response(runtime, initialize_id)
        if initialize_response.is_error and initialize_response.error is not None:
            raise BackendError(
                message=f"freebuff ACP initialize failed: {initialize_response.error.message}",
                details=initialize_response.error.model_dump(),
            )

        auth_id = await self._send_jsonrpc_message(
            runtime,
            "authenticate",
            {"methodId": "freebuff"},
        )
        auth_response = await self._await_response(runtime, auth_id)
        if auth_response.is_error and auth_response.error is not None:
            raise BackendError(
                message=f"freebuff ACP authenticate failed: {auth_response.error.message}",
                details=auth_response.error.model_dump(),
            )

        session_new_id = await self._send_jsonrpc_message(
            runtime,
            "session/new",
            {
                "cwd": str(runtime.project_dir),
                "mcpServers": list(self._mcp_servers),
            },
        )
        session_new_response = await self._await_response(runtime, session_new_id)
        if session_new_response.is_error and session_new_response.error is not None:
            raise BackendError(
                message=f"freebuff ACP session/new failed: {session_new_response.error.message}",
                details=session_new_response.error.model_dump(),
            )

        session_result = session_new_response.result or {}
        session_id = session_result.get("sessionId")
        if not isinstance(session_id, str) or not session_id.strip():
            raise BackendError(
                message="freebuff ACP session/new did not return a sessionId",
                details={"result": session_result},
            )

        runtime.session_id = session_id
        runtime.initialized = True

    async def _handle_server_request(
        self, runtime: ACPProcessRuntime, msg: ACPNotification
    ) -> None:
        assert msg.id is not None
        method = msg.method or ""
        await self._write_json_line(
            runtime,
            {
                "jsonrpc": "2.0",
                "id": msg.id,
                "error": {"code": -32601, "message": f"Method not handled: {method}"},
            },
        )

    def get_available_models(self) -> list[str]:
        return list(self._configured_models)


from src.core.services.backend_registry import backend_registry

backend_registry.register_backend("freebuff-cli-acp", FreebuffCliAcpConnector)
