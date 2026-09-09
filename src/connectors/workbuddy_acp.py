"""WorkBuddy AI backend connector using native Agent Client Protocol (ACP) over stdio."""

from __future__ import annotations

import asyncio
import glob
import logging
import os
import shutil
import subprocess
from collections.abc import Sequence
from pathlib import Path
from typing import Any

from src.connectors.acp_core.base_connector import BaseAcpConnector
from src.connectors.acp_core.types import ACPNotification, ACPProcessRuntime
from src.connectors.acp_core.workspace_policy import resolve_backend_init_acp_workspace
from src.connectors.base import add_vendor_prefix, strip_vendor_prefix
from src.connectors.contracts import ConnectorChatCompletionsRequest
from src.core.common.exceptions import BackendError, ConfigurationError
from src.core.common.model_catalog import BackendModelEnumeration
from src.core.config.app_config import BackendConfig

logger = logging.getLogger(__name__)

ACP_PROTOCOL_VERSION = 1
DEFAULT_WORKBUDDY_PROCESS_TIMEOUT_SECONDS = 300.0
DEFAULT_WORKBUDDY_IDLE_TIMEOUT_SECONDS = 30.0
DEFAULT_WORKBUDDY_MODEL = "primary-model"

# Known upstream model tiers supported by the WorkBuddy CLI engine
WORKBUDDY_NATIVE_MODELS: tuple[str, ...] = (
    "primary-model",
    "deep-model",
    "balanced-model",
    "fast-model",
)

# Canonical aliases mapping user queries or legacy model names to official tiers
WORKBUDDY_MODEL_ALIASES: dict[str, str] = {
    "hy4": "primary-model",
    "hy4-preview": "primary-model",
    "hy-preview": "primary-model",
    "hy3": "primary-model",
    "hy3-preview": "primary-model",
    "hunyuan": "primary-model",
    "hunyuan-4": "primary-model",
    "hunyuan4": "primary-model",
    "primary": "primary-model",
    "flagship": "primary-model",
    "auto": "primary-model",
    "default": "primary-model",
    "deep": "deep-model",
    "reasoning": "deep-model",
    "balanced": "balanced-model",
    "fast": "fast-model",
    "minimax": "fast-model",
}


def canonicalize_workbuddy_model_id(native_id: str) -> str:
    """Normalize model string to a valid WorkBuddy CLI model ID."""
    model = strip_vendor_prefix(native_id.strip(), "workbuddy").strip()
    if not model:
        return DEFAULT_WORKBUDDY_MODEL
    lowered = model.lower()
    return WORKBUDDY_MODEL_ALIASES.get(lowered, model)


def resolve_workbuddy_node_executable(configured: str | None = None) -> str | None:
    """Resolve the Node.js runtime bundled with WorkBuddy or available on host."""
    candidates: list[str] = []
    if configured and str(configured).strip():
        candidates.append(str(configured).strip())

    for env_var in (
        "WORKBUDDY_NODE_BIN",
        "WORKBUDDY_NODE_PATH",
        "NODE_BINARY",
        "NODE_EXECUTABLE",
    ):
        val = os.environ.get(env_var, "").strip()
        if val:
            candidates.append(val)

    # Search ~/.workbuddy-ai/binaries/node/versions/*/node.exe
    home = Path.home()
    node_pattern = str(
        home / ".workbuddy-ai" / "binaries" / "node" / "versions" / "*" / "node.exe"
    )
    for match in glob.glob(node_pattern):
        candidates.append(match)

    # POSIX equivalent
    node_pattern_posix = str(
        home / ".workbuddy-ai" / "binaries" / "node" / "versions" / "*" / "bin" / "node"
    )
    for match in glob.glob(node_pattern_posix):
        candidates.append(match)

    # Host PATH
    candidates.extend(["node", "node.exe"])

    for cand in candidates:
        if not cand:
            continue
        p = Path(cand)
        if p.is_file():
            return str(p.resolve())
        resolved = shutil.which(cand)
        if resolved:
            return resolved
    return None


def resolve_workbuddy_cli_path(configured: str | None = None) -> str | None:
    """Resolve the WorkBuddy codebuddy CLI script entrypoint."""
    candidates: list[str] = []
    if configured and str(configured).strip():
        candidates.append(str(configured).strip())

    for env_var in ("WORKBUDDY_CLI_PATH", "WORKBUDDY_CLI_BIN", "CODEBUDDY_CLI_PATH"):
        val = os.environ.get(env_var, "").strip()
        if val:
            candidates.append(val)

    # Check local appdata (default Windows installation path)
    local_app_data = os.environ.get("LOCALAPPDATA", "")
    if local_app_data:
        candidates.append(
            str(
                Path(local_app_data)
                / "Programs"
                / "WorkBuddyAI"
                / "resources"
                / "app.asar.unpacked"
                / "cli"
                / "bin"
                / "codebuddy"
            )
        )

    # Check Program Files
    prog_files = os.environ.get("PROGRAMFILES", "")
    if prog_files:
        candidates.append(
            str(
                Path(prog_files)
                / "WorkBuddyAI"
                / "resources"
                / "app.asar.unpacked"
                / "cli"
                / "bin"
                / "codebuddy"
            )
        )

    # Check PATH
    candidates.extend(["codebuddy", "codebuddy.cmd", "codebuddy.exe"])

    for cand in candidates:
        if not cand:
            continue
        p = Path(cand)
        if p.is_file():
            return str(p.resolve())
        resolved = shutil.which(cand)
        if resolved:
            return resolved
    return None


def build_workbuddy_acp_command(
    node_executable: str,
    cli_path: str,
    *,
    extra_args: Sequence[str] | None = None,
) -> list[str]:
    """Construct command line to spawn ``node codebuddy --acp --acp-transport stdio``."""
    cmd = [node_executable, cli_path, "--acp", "--acp-transport", "stdio"]
    if extra_args:
        cmd.extend(list(extra_args))
    return cmd


class WorkBuddyConfiguredModelEnumerator:
    """Enumerate model routes for configured WorkBuddy backend instances."""

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        extra = config.extra or {}
        configured_node = extra.get("node_executable") or extra.get("node_bin")
        configured_cli = (
            extra.get("cli_path")
            or extra.get("workbuddy_cli_path")
            or extra.get("executable")
        )

        node_exec = resolve_workbuddy_node_executable(
            str(configured_node) if configured_node else None
        )
        cli_path = resolve_workbuddy_cli_path(
            str(configured_cli) if configured_cli else None
        )

        if node_exec is None or cli_path is None:
            return BackendModelEnumeration.unavailable(
                instance_name=instance_name,
                connector="workbuddy-acp",
                source="workbuddy_configured",
                error_code="executable_not_found",
                instance_pinned=True,
            )

        configured_models = config.models or extra.get("models")
        if isinstance(configured_models, list) and configured_models:
            models = [
                add_vendor_prefix(
                    strip_vendor_prefix(str(m).strip(), "workbuddy"), "workbuddy"
                )
                for m in configured_models
                if str(m).strip()
            ]
            if models:
                return BackendModelEnumeration.available(
                    instance_name=instance_name,
                    connector="workbuddy-acp",
                    models=models,
                    source="workbuddy_configured",
                    instance_pinned=True,
                )

        default_routes = [
            "workbuddy/primary-model",
            "workbuddy/deep-model",
            "workbuddy/balanced-model",
            "workbuddy/fast-model",
            "workbuddy/hy4",
            "workbuddy/hy4-preview",
            "workbuddy/hunyuan",
            "workbuddy/auto",
        ]
        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector="workbuddy-acp",
            models=default_routes,
            source="workbuddy_configured",
            instance_pinned=True,
        )


class WorkBuddyAcpConnector(BaseAcpConnector[ACPProcessRuntime]):
    """WorkBuddy AI connector speaking native Agent Client Protocol (ACP) over stdio."""

    VENDOR_PREFIX: str = "workbuddy"
    backend_type: str = "workbuddy-acp"
    requires_explicit_workspace: bool = False

    def __init__(
        self,
        *args: Any,
        **kwargs: Any,
    ) -> None:
        client: Any = None
        config: Any = None
        translation_service: Any = None

        if len(args) == 1:
            config = args[0]
            translation_service = kwargs.get("translation_service")
        elif len(args) >= 2:
            client = args[0]
            config = args[1]
            translation_service = (
                args[2] if len(args) > 2 else kwargs.get("translation_service")
            )
        else:
            client = kwargs.get("client")
            config = kwargs.get("config")
            translation_service = kwargs.get("translation_service")

        super().__init__(config, translation_service=translation_service, **kwargs)
        self.client = client
        self.name = "workbuddy-acp"
        self._node_executable: str = ""
        self._cli_path: str = ""
        self._permission_mode: str = "bypassPermissions"
        self._thought_level: str | None = "enabled"
        self._configured_models: list[str] = []
        self._mcp_servers: list[Any] = []
        self._extra_args: list[str] = []
        self._custom_env: dict[str, str] = {}
        self._process_timeout = DEFAULT_WORKBUDDY_PROCESS_TIMEOUT_SECONDS
        self._idle_timeout = DEFAULT_WORKBUDDY_IDLE_TIMEOUT_SECONDS

    async def initialize(self, **kwargs: Any) -> None:
        """Initialize and validate the WorkBuddy backend configuration."""
        try:
            extra = getattr(self.config, "extra", {}) or {}
            if not isinstance(extra, dict):
                extra = {}

            workspace, cfg_err = resolve_backend_init_acp_workspace(
                project_dir=kwargs.get("project_dir") or extra.get("project_dir"),
                workspace_path=kwargs.get("workspace_path")
                or extra.get("workspace_path"),
                env_workspace=os.getenv("WORKBUDDY_WORKSPACE"),
                env_source_label="WORKBUDDY_WORKSPACE",
                is_usable=self._is_usable_directory,
            )
            if cfg_err:
                raise ConfigurationError(
                    message=cfg_err,
                    details={"error_code": "workbuddy_acp_workspace_invalid"},
                )
            self._default_project_dir = workspace

            configured_node = (
                kwargs.get("node_executable")
                or kwargs.get("node_bin")
                or kwargs.get("node")
                or extra.get("node_executable")
                or extra.get("node_bin")
            )
            resolved_node = resolve_workbuddy_node_executable(
                str(configured_node) if configured_node else None
            )
            if resolved_node is None:
                raise ConfigurationError(
                    message="Node.js executable for WorkBuddy could not be found.",
                    details={
                        "configured": configured_node,
                        "hint": "Set 'node_executable' in config or ensure Node is installed in ~/.workbuddy-ai/binaries/node/ or host PATH.",
                    },
                )
            self._node_executable = resolved_node

            configured_cli = (
                kwargs.get("cli_path")
                or kwargs.get("workbuddy_cli_path")
                or kwargs.get("executable")
                or extra.get("cli_path")
                or extra.get("workbuddy_cli_path")
                or extra.get("executable")
            )
            resolved_cli = resolve_workbuddy_cli_path(
                str(configured_cli) if configured_cli else None
            )
            if resolved_cli is None:
                raise ConfigurationError(
                    message="WorkBuddy CLI script (codebuddy) could not be found.",
                    details={
                        "configured": configured_cli,
                        "hint": "Set 'cli_path' in config or ensure WorkBuddy AI is installed in %LOCALAPPDATA%\\Programs\\WorkBuddyAI.",
                    },
                )
            self._cli_path = resolved_cli

            configured_model = str(
                kwargs.get("model") or extra.get("model") or self._model
            )
            self._model = canonicalize_workbuddy_model_id(configured_model)

            self._permission_mode = str(
                kwargs.get("permission_mode")
                or kwargs.get("mode")
                or extra.get("permission_mode")
                or extra.get("mode")
                or self._permission_mode
            )
            thought_level = kwargs.get("thought_level") or extra.get("thought_level")
            self._thought_level = (
                str(thought_level).strip() if thought_level is not None else "enabled"
            )

            configured_models = (
                kwargs.get("models")
                or getattr(self.config, "models", None)
                or extra.get("models")
            )
            if isinstance(configured_models, list):
                self._configured_models = [
                    strip_vendor_prefix(str(m).strip(), self.VENDOR_PREFIX)
                    for m in configured_models
                    if str(m).strip()
                ]

            self._process_timeout = float(
                kwargs.get(
                    "process_timeout",
                    extra.get(
                        "process_timeout", DEFAULT_WORKBUDDY_PROCESS_TIMEOUT_SECONDS
                    ),
                )
            )
            self._idle_timeout = float(
                kwargs.get(
                    "idle_timeout",
                    extra.get("idle_timeout", DEFAULT_WORKBUDDY_IDLE_TIMEOUT_SECONDS),
                )
            )

            mcp = kwargs.get("mcp_servers", extra.get("mcp_servers", []))
            self._mcp_servers = list(mcp) if isinstance(mcp, list) else []

            extra_args = (
                kwargs.get("extra_args")
                or kwargs.get("workbuddy_extra_args")
                or extra.get("extra_args")
            )
            if isinstance(extra_args, list):
                self._extra_args = [str(arg) for arg in extra_args]
            elif isinstance(extra_args, str) and extra_args.strip():
                self._extra_args = [extra_args.strip()]

            custom_env = kwargs.get("env") or extra.get("env")
            if isinstance(custom_env, dict):
                self._custom_env = {
                    str(k): str(v) for k, v in custom_env.items() if k is not None
                }

            if not self._check_available_sync():
                raise ConfigurationError(
                    message=f"WorkBuddy CLI is not callable with Node: {self._node_executable} {self._cli_path}",
                    details={"node": self._node_executable, "cli": self._cli_path},
                )

            self._validation_errors = []
            self._initialization_failed = False
            self.is_functional = True
        except Exception:
            self._initialization_failed = True
            self.is_functional = False
            self._validation_errors = ["workbuddy-acp initialization failed"]
            raise

    def _check_available_sync(self) -> bool:
        try:
            result = subprocess.run(
                [self._node_executable, self._cli_path, "--version"],
                capture_output=True,
                timeout=10,
                check=False,
                shell=False,
            )
            return result.returncode == 0
        except (subprocess.TimeoutExpired, FileNotFoundError, OSError):
            return False

    def _resolve_project_dir_for_request(
        self, request: ConnectorChatCompletionsRequest
    ) -> Path:
        try:
            return super()._resolve_project_dir_for_request(request)
        except (ConfigurationError, BackendError):
            if self._default_project_dir is not None:
                return self._default_project_dir
            return Path.cwd()

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

    async def _build_subprocess_command(self, runtime: ACPProcessRuntime) -> list[str]:
        return build_workbuddy_acp_command(
            self._node_executable,
            self._cli_path,
            extra_args=self._extra_args,
        )

    def _subprocess_env(self, runtime: ACPProcessRuntime) -> dict[str, str]:
        env = os.environ.copy()
        env["LLM_PROXY_CALLER_BACKEND"] = "workbuddy-acp"
        env["LLM_PROXY_ORIGIN_BACKEND"] = "workbuddy-acp"
        if self._custom_env:
            env.update(self._custom_env)
        return env

    async def _perform_handshake(self, runtime: ACPProcessRuntime) -> None:
        """Perform initialize -> session/new -> set_config_option handshake."""
        # 1. initialize
        init_id = await self._send_jsonrpc_message(
            runtime,
            "initialize",
            {
                "protocolVersion": ACP_PROTOCOL_VERSION,
                "clientCapabilities": {
                    "streaming": True,
                    "fs": {"readTextFile": False, "writeTextFile": False},
                    "terminal": False,
                },
                "clientInfo": {
                    "name": "llm-interactive-proxy",
                    "version": "1",
                },
            },
        )
        init_resp = await self._await_response(runtime, init_id)
        if init_resp.is_error and init_resp.error is not None:
            raise BackendError(
                message=f"WorkBuddy initialize failed: {init_resp.error.message}",
                details=init_resp.error.model_dump(),
            )

        # 2. session/new (mcpServers is required by codebuddy ACP)
        session_new_id = await self._send_jsonrpc_message(
            runtime,
            "session/new",
            {
                "cwd": str(runtime.project_dir),
                "mcpServers": list(self._mcp_servers),
            },
        )
        session_new_resp = await self._await_response(runtime, session_new_id)
        if session_new_resp.is_error and session_new_resp.error is not None:
            raise BackendError(
                message=f"WorkBuddy session/new failed: {session_new_resp.error.message}",
                details=session_new_resp.error.model_dump(),
            )

        session_result = session_new_resp.result or {}
        session_id = session_result.get("sessionId")
        if not isinstance(session_id, str) or not session_id.strip():
            raise BackendError(
                message="WorkBuddy session/new did not return a sessionId",
                details={"result": session_result},
            )
        runtime.session_id = session_id

        # 3. Configure model
        target_model = canonicalize_workbuddy_model_id(runtime.model)
        set_model_id = await self._send_jsonrpc_message(
            runtime,
            "session/set_config_option",
            {
                "sessionId": session_id,
                "configId": "model",
                "value": target_model,
            },
        )
        set_model_resp = await self._await_response(runtime, set_model_id)
        if set_model_resp.is_error and set_model_resp.error is not None:
            logger.warning(
                "WorkBuddy session/set_config_option for model=%s returned error: %s",
                target_model,
                set_model_resp.error.message,
            )

        # 4. Configure permission mode
        if self._permission_mode:
            set_mode_id = await self._send_jsonrpc_message(
                runtime,
                "session/set_config_option",
                {
                    "sessionId": session_id,
                    "configId": "mode",
                    "value": self._permission_mode,
                },
            )
            set_mode_resp = await self._await_response(runtime, set_mode_id)
            if set_mode_resp.is_error and set_mode_resp.error is not None:
                logger.warning(
                    "WorkBuddy session/set_config_option for mode=%s returned error: %s",
                    self._permission_mode,
                    set_mode_resp.error.message,
                )

        # 5. Configure thought level if requested
        if self._thought_level:
            set_thought_id = await self._send_jsonrpc_message(
                runtime,
                "session/set_config_option",
                {
                    "sessionId": session_id,
                    "configId": "thought_level",
                    "value": self._thought_level,
                },
            )
            await self._await_response(runtime, set_thought_id)

        runtime.initialized = True

    def _permission_option_id(self) -> str:
        return "allow-always"

    async def _handle_server_request(
        self, runtime: ACPProcessRuntime, msg: ACPNotification
    ) -> None:
        assert msg.id is not None
        rid = msg.id
        method = msg.method or ""

        if method == "session/request_permission":
            await self._send_jsonrpc_result(
                runtime,
                rid,
                {
                    "outcome": {
                        "outcome": "selected",
                        "optionId": self._permission_option_id(),
                    }
                },
            )
            return

        if logger.isEnabledFor(logging.WARNING):
            logger.warning(
                "Unhandled inbound WorkBuddy JSON-RPC request method=%s id=%s; replying error",
                method,
                rid,
            )
        await self._write_json_line(
            runtime,
            {
                "jsonrpc": "2.0",
                "id": rid,
                "error": {"code": -32601, "message": f"Method not handled: {method}"},
            },
        )

    def get_available_models(self) -> list[str]:
        if self._configured_models:
            return list(self._configured_models)
        return [
            "primary-model",
            "deep-model",
            "balanced-model",
            "fast-model",
            "hy4",
            "hy4-preview",
            "hunyuan",
            "auto",
        ]


from src.core.services.backend_registry import backend_registry

backend_registry.register_backend("workbuddy-acp", WorkBuddyAcpConnector)
