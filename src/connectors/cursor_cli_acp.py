"""Cursor CLI backend using Agent Client Protocol (ACP) over stdio JSON-RPC."""

from __future__ import annotations

import asyncio
import logging
import os
import re
import shutil
import subprocess
import time
from collections.abc import Iterable, Sequence
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Any, cast

import httpx

from src.connectors.acp_core.base_connector import BaseAcpConnector
from src.connectors.acp_core.transcript import ACPTranscriptSerializer
from src.connectors.acp_core.types import (
    ACPNotification,
    ACPProcessRuntime,
    HistoryState,
)
from src.connectors.acp_core.workspace_policy import (
    first_usable_workspace_dir,
    first_workspace_hint_str,
    resolve_backend_init_acp_workspace,
)
from src.connectors.base import add_vendor_prefix, strip_vendor_prefix
from src.connectors.contracts import ConnectorChatCompletionsRequest
from src.connectors.cursor_cli_auth import (
    CursorAuthMode,
    CursorAuthPolicy,
    build_cursor_cli_env,
    discovery_modes_to_try,
    is_cursor_api_key_invalid_error,
    is_cursor_auth_required_error,
    read_cursor_login_store_api_key,
    resolve_cursor_auth_policy,
)
from src.core.common.exceptions import BackendError, ConfigurationError
from src.core.common.model_catalog import BackendModelEnumeration
from src.core.config.app_config import AppConfig, BackendConfig
from src.core.domain.chat import ChatMessage
from src.core.domain.responses_native_wiring import (
    ACP_RESPONSES_STANDALONE_MODE_KEY,
    ACP_RESPONSES_TEXT_ONLY_MODE_KEY,
)
from src.core.services.translation_service import TranslationService

logger = logging.getLogger(__name__)

_ANSI_ESCAPE_RE = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")

ACP_PROTOCOL_VERSION = 1


def _strip_ansi(text: str) -> str:
    return _ANSI_ESCAPE_RE.sub("", text)


def parse_agent_models_listing(stdout: str) -> list[str]:
    """Parse ``agent --list-models`` output into exact Cursor CLI model ids."""
    models: list[str] = []
    seen: set[str] = set()
    for raw_line in stdout.splitlines():
        line = _strip_ansi(raw_line).strip()
        if not line or line.lower().startswith("loading"):
            continue
        if " - " not in line:
            continue
        left, _right = line.split(" - ", 1)
        model_id = left.strip()
        if not model_id or model_id in seen:
            continue
        if " " in model_id:
            continue
        seen.add(model_id)
        models.append(model_id)
    return models


def _cursor_cli_model_route(model_id: str) -> str:
    """Expose a stable provider route while retaining Cursor's exact CLI id."""

    route_id = model_id.removeprefix("cursor-")
    return add_vendor_prefix(route_id, "cursor")


def resolve_cursor_cli_model_id(
    advertised: str,
    *,
    cli_ids: dict[str, str],
    requested_model: str,
) -> str:
    """Map an advertised proxy route to a Cursor CLI ``--model`` id."""

    mapped = cli_ids.get(advertised)
    if mapped:
        return mapped
    stripped = strip_vendor_prefix(requested_model or advertised, "cursor")
    if not stripped:
        return requested_model or advertised
    # Empty-catalog pass-through: Cursor often expects the ``cursor-`` prefix.
    return f"cursor-{stripped}"


def _cursor_model_fast_tier(
    model: str, *, available_cli_model_ids: Iterable[str] = ()
) -> bool | None:
    """Return the explicit Cursor speed tier encoded by a model id.

    Cursor's parameterized picker uses ``-fast`` for the fast route. Standard
    routes are paired with an explicit ``<model>-fast`` catalog entry. Known
    Cursor Grok effort routes and Composer models also require confirmation
    when discovery is unavailable. Unknown models leave normal selection alone
    until the ACP response exposes ``fast``.
    """

    normalized = model.strip().casefold()
    if normalized.endswith("-fast"):
        return True
    available = {item.strip().casefold() for item in available_cli_model_ids}
    if f"{normalized}-fast" in available:
        return False
    for effort_suffix in ("-low", "-medium", "-high", "-xhigh", "-max"):
        if normalized.endswith(effort_suffix):
            base_model = normalized[: -len(effort_suffix)]
            if f"{base_model}-fast" in available:
                return False
            if normalized.startswith("cursor-grok-"):
                return False
            break
    if normalized.startswith("composer-"):
        return False
    return None


def _cursor_config_option(
    config_options: object, config_id: str
) -> dict[str, Any] | None:
    """Find an ACP config option in either list or mapping response shapes."""

    if isinstance(config_options, list):
        candidates = config_options
    elif isinstance(config_options, dict):
        direct = config_options.get(config_id)
        if isinstance(direct, dict):
            return direct
        candidates = list(config_options.values())
    else:
        return None

    for candidate in candidates:
        if isinstance(candidate, dict) and candidate.get("id") == config_id:
            return candidate
    return None


def _cursor_config_options(result: dict[str, Any]) -> object:
    """Read ACP config options while tolerating absent optional metadata."""

    return result.get("configOptions")


def _cursor_config_option_current_value(option: dict[str, Any]) -> str | None:
    value = option.get("currentValue")
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, str):
        return value.casefold()
    return None


@dataclass(frozen=True)
class CursorCliModelCatalog:
    routes: tuple[str, ...]
    cli_ids: dict[str, str]
    discovery_mode: CursorAuthMode | None = None
    auth_policy: CursorAuthPolicy | None = None


def normalize_cursor_cli_extra_args(value: object) -> list[str]:
    """Normalize configured Cursor CLI arguments without shell re-parsing."""
    if isinstance(value, list):
        return [str(item) for item in value]
    if isinstance(value, str) and value.strip():
        return [value.strip()]
    return []


def _parse_list_models_catalog(stdout: str) -> CursorCliModelCatalog:
    routes: list[str] = []
    cli_ids: dict[str, str] = {}
    for cli_model_id in parse_agent_models_listing(stdout):
        route = _cursor_cli_model_route(cli_model_id)
        if route in cli_ids:
            continue
        cli_ids[route] = cli_model_id
        routes.append(route)
    return CursorCliModelCatalog(routes=tuple(routes), cli_ids=cli_ids)


async def _run_list_models(
    *,
    executable: str,
    cursor_api_endpoint: str | None,
    extra_args: Sequence[str] | None,
    workspace: Path | None,
    timeout_seconds: float,
    env: dict[str, str],
) -> subprocess.CompletedProcess[str]:
    command = [executable]
    if cursor_api_endpoint:
        command.extend(["-e", cursor_api_endpoint])
    if extra_args:
        command.extend(list(extra_args))
    command.append("--list-models")
    return await asyncio.to_thread(
        subprocess.run,
        command,
        capture_output=True,
        text=True,
        timeout=timeout_seconds,
        check=False,
        shell=False,
        cwd=str(workspace) if isinstance(workspace, Path) else None,
        env=env,
    )


async def discover_cursor_cli_model_catalog(
    *,
    executable: str,
    cursor_api_endpoint: str | None,
    extra_args: Sequence[str] | None = None,
    workspace: Path | None,
    timeout_seconds: float,
    auth_timeout_seconds: float = 30.0,
) -> CursorCliModelCatalog:
    """Run Cursor's noninteractive catalog command without starting ACP.

    Auth policy is cookie-first for session identity. Discovery retries with
    ``CURSOR_API_KEY`` when cookie-only ``--list-models`` fails auth.
    """

    env_key_invalid = False
    # Read login-store apiKey once up front. Cursor CLI may unlink auth.json
    # during later status/list-models probes; re-reading mid-loop can lose it.
    discovery_api_key = await asyncio.to_thread(read_cursor_login_store_api_key)
    policy = await asyncio.to_thread(
        resolve_cursor_auth_policy,
        executable,
        timeout_seconds=auth_timeout_seconds,
        env_key_invalid=env_key_invalid,
    )
    # Prefer the key captured before probes if the policy re-read races.
    if discovery_api_key and not policy.login_store_key_present:
        policy = CursorAuthPolicy(
            cookie_usable=policy.cookie_usable,
            env_key_present=policy.env_key_present,
            env_key_invalid=policy.env_key_invalid,
            discovery_mode=policy.discovery_mode,
            acp_mode=policy.acp_mode,
            login_store_key_present=True,
        )
    last_stderr = ""
    for mode in discovery_modes_to_try(policy):
        env = build_cursor_cli_env(mode, discovery_api_key=discovery_api_key)
        try:
            result = await _run_list_models(
                executable=executable,
                cursor_api_endpoint=cursor_api_endpoint,
                extra_args=extra_args,
                workspace=workspace,
                timeout_seconds=timeout_seconds,
                env=env,
            )
        except (subprocess.TimeoutExpired, FileNotFoundError, OSError):
            logger.warning(
                "Cursor model discovery command failed (mode=%s)",
                mode,
                exc_info=True,
            )
            continue

        stderr = (result.stderr or "").strip()
        last_stderr = stderr
        if result.returncode == 0:
            catalog = _parse_list_models_catalog(result.stdout or "")
            # Refresh ACP mode: cookie preferred when usable; otherwise key.
            if mode == "with_env_key" and policy.cookie_usable:
                acp_mode: CursorAuthMode = "cookie_only"
            else:
                acp_mode = mode if mode == "with_env_key" else policy.acp_mode
                if policy.cookie_usable:
                    acp_mode = "cookie_only"
            resolved_policy = CursorAuthPolicy(
                cookie_usable=policy.cookie_usable,
                env_key_present=policy.env_key_present,
                env_key_invalid=env_key_invalid,
                discovery_mode=mode,
                acp_mode=acp_mode,
                login_store_key_present=policy.login_store_key_present,
            )
            return CursorCliModelCatalog(
                routes=catalog.routes,
                cli_ids=catalog.cli_ids,
                discovery_mode=mode,
                auth_policy=resolved_policy,
            )

        if is_cursor_api_key_invalid_error(stderr):
            env_key_invalid = True
            logger.warning(
                "Cursor model discovery rejected CURSOR_API_KEY (mode=%s): %s",
                mode,
                stderr,
            )
            # Rebuild policy so later ACP uses cookie when available.
            policy = CursorAuthPolicy(
                cookie_usable=policy.cookie_usable,
                env_key_present=policy.env_key_present,
                env_key_invalid=True,
                discovery_mode=policy.discovery_mode,
                acp_mode=("cookie_only" if policy.cookie_usable else policy.acp_mode),
                login_store_key_present=policy.login_store_key_present,
            )
            continue

        if is_cursor_auth_required_error(stderr):
            # Cookie-only --list-models often fails on CLI builds that require
            # an API key for discovery, even when login cookies / env key still
            # work for status or ACP. Warn only when no usable fallback remains.
            if policy.cookie_usable or policy.discovery_key_available:
                logger.debug(
                    "Cursor --list-models auth rejected (mode=%s); "
                    "auth remains usable via cookie=%s discovery_key=%s: %s",
                    mode,
                    policy.cookie_usable,
                    policy.discovery_key_available,
                    stderr,
                )
            else:
                logger.warning(
                    "Cursor model discovery auth failed (mode=%s): %s",
                    mode,
                    stderr,
                )
            continue

        logger.warning(
            "Cursor model discovery command failed with exit code %s (mode=%s): %s",
            result.returncode,
            mode,
            stderr,
        )

    if policy.cookie_usable or policy.discovery_key_available:
        logger.info(
            "Cursor model discovery returned an empty catalog while auth remains "
            "usable (cookie=%s discovery_key=%s); requests may still proceed. "
            "Last discovery error: %s",
            policy.cookie_usable,
            policy.discovery_key_available and not env_key_invalid,
            last_stderr or "(none)",
        )
    else:
        logger.warning(
            "Cursor model discovery exhausted auth modes; last error: %s",
            last_stderr or "(none)",
        )
    final_policy = CursorAuthPolicy(
        cookie_usable=policy.cookie_usable,
        env_key_present=policy.env_key_present,
        env_key_invalid=env_key_invalid,
        discovery_mode=policy.discovery_mode,
        acp_mode=(
            "cookie_only"
            if policy.cookie_usable
            else (
                "with_env_key"
                if (policy.env_key_present or policy.login_store_key_present)
                and not env_key_invalid
                else "cookie_only"
            )
        ),
        login_store_key_present=policy.login_store_key_present,
    )
    return CursorCliModelCatalog(
        routes=(),
        cli_ids={},
        discovery_mode=None,
        auth_policy=final_policy,
    )


class CursorCliConfiguredModelEnumerator:
    """Enumerate a configured Cursor instance without creating an ACP session."""

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        extra = config.extra
        configured_executable = extra.get("cursor_cli_executable") or extra.get(
            "agent_executable"
        )
        executable = resolve_cursor_agent_executable(
            str(configured_executable) if configured_executable else None
        )
        if executable is None:
            return BackendModelEnumeration.unavailable(
                instance_name=instance_name,
                connector="cursor-cli-acp",
                source="cursor_cli",
                error_code="executable_not_found",
                instance_pinned=True,
            )
        endpoint = extra.get("cursor_api_endpoint") or extra.get("cursor_api_base_url")
        extra_args = normalize_cursor_cli_extra_args(
            extra.get("cursor_cli_extra_args") or extra.get("cursor_extra_cli_args")
        )
        timeout = float(extra.get("cursor_model_discovery_timeout_seconds", 60.0))
        catalog = await discover_cursor_cli_model_catalog(
            executable=executable,
            cursor_api_endpoint=str(endpoint) if endpoint else None,
            extra_args=extra_args,
            workspace=None,
            timeout_seconds=timeout,
        )
        if not catalog.routes:
            return BackendModelEnumeration.unavailable(
                instance_name=instance_name,
                connector="cursor-cli-acp",
                source="cursor_cli",
                error_code="catalog_unavailable",
                instance_pinned=True,
            )
        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector="cursor-cli-acp",
            models=catalog.routes,
            source="cursor_cli",
            instance_pinned=True,
        )


def resolve_cursor_agent_executable(configured: str | None) -> str | None:
    """Resolve path to Cursor CLI launcher suitable for ``subprocess.Popen`` (no shell)."""
    for candidate in (
        (configured or "").strip(),
        os.environ.get("CURSOR_AGENT_BIN", "").strip(),
    ):
        if not candidate:
            continue
        p = Path(candidate)
        if p.is_file():
            return str(p.resolve())
        resolved = shutil.which(candidate)
        if resolved:
            return resolved
    resolved = shutil.which("agent")
    if resolved:
        return resolved
    if os.name == "nt":
        resolved = shutil.which("agent.cmd")
        if resolved:
            return resolved
    return None


def build_cursor_agent_acp_command(
    executable: str,
    *,
    model: str | None,
    trust_workspace: bool,
    extra_args: Sequence[str] | None,
    cursor_api_endpoint: str | None,
    mode: str | None = None,
) -> list[str]:
    cmd: list[str] = [executable]
    if cursor_api_endpoint:
        cmd.extend(["-e", cursor_api_endpoint])
    if model:
        cmd.extend(["--model", model])
    if mode:
        cmd.extend(["--mode", mode])
    if trust_workspace:
        cmd.append("--trust")
    if extra_args:
        cmd.extend(list(extra_args))
    cmd.append("acp")
    return cmd


class CursorCliAcpConnector(BaseAcpConnector[ACPProcessRuntime]):
    """Cursor CLI backend using Agent Client Protocol over stdio."""

    backend_type: str = "cursor-cli-acp"
    VENDOR_PREFIX: str = "cursor"
    # Cursor's CLI process is authenticated once and then reused across proxy
    # request/session identifiers.  Those identifiers are B2BUA attempt ids,
    # not Cursor conversation identities.
    requires_explicit_workspace: bool = False

    def __init__(
        self,
        client: httpx.AsyncClient,
        config: AppConfig,
        translation_service: TranslationService,
        **_: Any,
    ) -> None:
        super().__init__(config, translation_service=translation_service)
        self.client = client
        self.name = "cursor-cli-acp"
        self._cursor_cli_executable: str = "agent"
        self._model = "composer-2"
        self._auto_accept = True
        self._trust_workspace = True
        self._mcp_servers: list[Any] = []
        self._cursor_api_endpoint: str | None = None
        self._extra_cli_args: list[str] = []
        self._cached_models: list[str] = []
        self._model_cli_ids: dict[str, str] = {}
        self._models_cache_fetched_at: float = 0.0
        self._models_cache_ttl_seconds: float = 3600.0
        self._model_discovery_timeout_seconds: float = 60.0
        self._extension_response_log: set[str] = set()
        self._auth_policy: CursorAuthPolicy | None = None
        self._acp_auth_mode: CursorAuthMode = "cookie_only"

    def _ensure_mutable_state(self) -> None:
        if not hasattr(self, "_mcp_servers"):
            self._mcp_servers = []

    async def initialize(self, **kwargs: Any) -> None:
        self._ensure_mutable_state()
        try:
            env_workspace = os.getenv("CURSOR_CLI_WORKSPACE")
            workspace, cfg_err = resolve_backend_init_acp_workspace(
                project_dir=kwargs.get("project_dir"),
                workspace_path=kwargs.get("workspace_path"),
                env_workspace=env_workspace,
                env_source_label="CURSOR_CLI_WORKSPACE",
                is_usable=self._is_usable_directory,
            )
            if cfg_err:
                raise ConfigurationError(
                    message=cfg_err,
                    details={"error_code": "cursor_cli_acp_workspace_invalid"},
                )
            configured_workspace = next(
                (
                    str(raw).strip()
                    for raw in (
                        kwargs.get("project_dir"),
                        kwargs.get("workspace_path"),
                        env_workspace,
                    )
                    if raw is not None and str(raw).strip()
                ),
                None,
            )
            if workspace is None and configured_workspace is not None:
                raise ConfigurationError(
                    message=(
                        "cursor-cli-acp configured workspace must be an explicit "
                        f"absolute readable directory: {configured_workspace!r}"
                    ),
                    details={"error_code": "cursor_cli_acp_workspace_invalid"},
                )
            # Optional instance default only. Unconfigured backends stay unbound
            # so each request/session workspace can spawn its own ACP child.
            self._default_project_dir = workspace
            exe_kw = kwargs.get("cursor_cli_executable") or kwargs.get(
                "agent_executable"
            )
            self._cursor_cli_executable = str(exe_kw or self._cursor_cli_executable)
            configured_model = str(kwargs.get("model") or self._model)
            self._model = strip_vendor_prefix(configured_model, self.VENDOR_PREFIX)
            self._auto_accept = bool(kwargs.get("auto_accept", self._auto_accept))
            self._trust_workspace = bool(
                kwargs.get("trust_workspace", self._trust_workspace)
            )
            self._process_timeout = float(
                kwargs.get("process_timeout", self._process_timeout)
            )
            self._idle_timeout = float(kwargs.get("idle_timeout", self._idle_timeout))
            self._models_cache_ttl_seconds = max(
                0.0,
                float(
                    kwargs.get(
                        "cursor_models_cache_ttl_seconds",
                        self._models_cache_ttl_seconds,
                    )
                ),
            )
            self._model_discovery_timeout_seconds = max(
                0.1,
                float(kwargs.get("cursor_model_discovery_timeout_seconds", 60.0)),
            )
            mcp = kwargs.get("mcp_servers", [])
            if isinstance(mcp, list):
                self._mcp_servers = mcp
            else:
                self._mcp_servers = []
            endpoint = kwargs.get("cursor_api_endpoint") or kwargs.get(
                "cursor_api_base_url"
            )
            self._cursor_api_endpoint = (
                str(endpoint).strip()
                if isinstance(endpoint, str) and endpoint
                else None
            )
            extra = kwargs.get("cursor_cli_extra_args") or kwargs.get(
                "cursor_extra_cli_args"
            )
            self._extra_cli_args = normalize_cursor_cli_extra_args(extra)

            resolved = resolve_cursor_agent_executable(self._cursor_cli_executable)
            if resolved is None:
                raise ConfigurationError(
                    message="Cursor CLI (agent) executable not found",
                    details={
                        "configured": self._cursor_cli_executable,
                        "hint": "Install Cursor CLI and ensure `agent` is on PATH, "
                        "or set CURSOR_AGENT_BIN to the full path to agent.cmd / agent.",
                    },
                )
            self._cursor_cli_executable = resolved

            if not await self._check_agent_available():
                raise ConfigurationError(
                    message=f"Cursor CLI not callable: {self._cursor_cli_executable}",
                    details={"executable": self._cursor_cli_executable},
                )

            await self._ensure_models_discovered(force=True, workspace=workspace)
            self._validation_errors = []
            self._initialization_failed = False
            self.is_functional = True
        except Exception:
            self._initialization_failed = True
            self.is_functional = False
            self._validation_errors = ["cursor-cli-acp initialization failed"]
            raise

    async def _check_agent_available(self) -> bool:
        try:
            result = await asyncio.to_thread(
                subprocess.run,
                [self._cursor_cli_executable, "--version"],
                capture_output=True,
                timeout=15,
                check=False,
                shell=False,
            )
            return result.returncode == 0
        except (subprocess.TimeoutExpired, FileNotFoundError, OSError):
            return False

    def _apply_auth_policy(self, policy: CursorAuthPolicy | None) -> None:
        if policy is None:
            return
        self._auth_policy = policy
        self._acp_auth_mode = policy.acp_mode

    def _subprocess_env(self, runtime: ACPProcessRuntime) -> dict[str, str]:
        del runtime
        return build_cursor_cli_env(self._acp_auth_mode)

    async def _discover_models(self, *, workspace: Path | None = None) -> list[str]:
        """Discover selectable models from Cursor's supported CLI catalog command."""

        workspace = workspace or getattr(self, "_default_project_dir", None)

        catalog = await discover_cursor_cli_model_catalog(
            executable=self._cursor_cli_executable,
            cursor_api_endpoint=self._cursor_api_endpoint,
            extra_args=self._extra_cli_args,
            workspace=workspace,
            timeout_seconds=self._model_discovery_timeout_seconds,
        )
        self._model_cli_ids = dict(catalog.cli_ids)
        self._apply_auth_policy(catalog.auth_policy)
        return list(catalog.routes)

    async def _ensure_models_discovered(
        self,
        *,
        force: bool = False,
        workspace: Path | None = None,
    ) -> None:
        """Populate ``_cached_models`` via a live ACP capability probe.

        Skips subprocess when cache is fresh (within TTL) unless ``force`` is True.
        TTL 0 means refresh on every call.
        """
        exe = getattr(self, "_cursor_cli_executable", None)
        if not isinstance(exe, str) or not exe.strip():
            return
        now = time.monotonic()
        # ``_models_cache_fetched_at`` is the discovery-attempt sentinel.  It
        # is updated even when the CLI returns no models or fails, so an empty
        # result is cached for the same TTL as a successful result.  Checking
        # ``_cached_models`` here would retry a failing discovery on every
        # request and can block callers for the full subprocess timeout.
        if (
            self._models_cache_fetched_at > 0
            and not force
            and self._models_cache_ttl_seconds > 0
            and (now - self._models_cache_fetched_at) < self._models_cache_ttl_seconds
        ):
            return

        try:
            if workspace is None:
                self._cached_models = await self._discover_models()
            else:
                self._cached_models = await self._discover_models(workspace=workspace)
        except Exception:
            # A failed capability probe is a negative discovery result.  Cache
            # it for the configured TTL so every request does not launch a
            # subprocess that can block for the full discovery timeout.
            logger.warning(
                "Cursor ACP model discovery failed; caching an empty result",
                exc_info=True,
            )
            self._cached_models = []
        self._models_cache_fetched_at = time.monotonic()

    async def get_available_models_async(self) -> list[str]:
        """Return ACP-accepted model ids, refreshing the capability cache."""
        if self.is_backend_functional():
            await self._ensure_models_discovered(force=False)
        return self.get_available_models()

    def get_available_models(self) -> list[str]:
        return list(self._cached_models)

    async def _acquire_runtime(
        self, request: ConnectorChatCompletionsRequest
    ) -> ACPProcessRuntime:
        project_dir = self._resolve_project_dir_for_request(request)
        await self._ensure_models_discovered(force=False, workspace=project_dir)
        requested_model = strip_vendor_prefix(
            request.effective_model or self._model,
            self.VENDOR_PREFIX,
        )
        advertised = add_vendor_prefix(requested_model, self.VENDOR_PREFIX)
        if self._cached_models and advertised not in self._cached_models:
            raise BackendError(
                message=f"Cursor ACP model is not currently available: {advertised}",
                details={
                    "code": "cursor_model_unavailable",
                    "requested_model": advertised,
                },
            )
        if not self._cached_models:
            policy = self._auth_policy
            if policy is None or not policy.any_usable:
                raise BackendError(
                    message=(
                        "Cursor CLI authentication is unavailable. "
                        "Run 'agent login' for local cookie auth, and/or set a valid "
                        "CURSOR_API_KEY (discovery may require the key on some CLI "
                        "versions)."
                    ),
                    details={
                        "code": "cursor_cli_auth_unavailable",
                        "requested_model": advertised,
                    },
                )
            logger.info(
                "Cursor model catalog empty but auth is usable; "
                "allowing requested model %s via deterministic CLI id mapping",
                advertised,
            )
        cli_model_id = resolve_cursor_cli_model_id(
            advertised,
            cli_ids=self._model_cli_ids,
            requested_model=requested_model,
        )
        return await super()._acquire_runtime(
            replace(request, effective_model=cli_model_id)
        )

    def _resolve_project_dir_for_request(
        self, request: ConnectorChatCompletionsRequest
    ) -> Path:
        extra_body: object = getattr(request.request, "extra_body", None)
        extra_dict = cast(dict[str, Any] | None, extra_body)
        options = cast(dict[str, Any] | None, request.options)
        hint = first_workspace_hint_str(extra_dict, options)

        request_workspace = first_usable_workspace_dir(
            extra_dict,
            options,
            is_usable=self._is_usable_directory,
            require_absolute_hint=True,
        )
        if request_workspace is not None:
            return request_workspace
        if hint is not None:
            raise BackendError(
                message=(
                    "cursor-cli-acp request workspace must be an existing readable "
                    f"absolute directory: {hint!r}"
                ),
                details={
                    "code": "cursor_cli_acp_workspace_invalid",
                    "workspace": hint,
                },
            )
        return self._resolve_fallback_workspace()

    def _resolve_process_cwd_workspace(self) -> Path | None:
        """Last-resort spawn directory when the request/session named none."""

        try:
            cwd = Path.cwd().expanduser().resolve()
        except (OSError, RuntimeError):
            return None
        if self._is_usable_directory(cwd):
            return cwd
        return None

    def _resolve_fallback_workspace(self) -> Path:
        if self._default_project_dir is not None:
            return self._default_project_dir
        cwd_workspace = self._resolve_process_cwd_workspace()
        if cwd_workspace is not None:
            return cwd_workspace
        raise ConfigurationError(
            message=(
                "cursor-cli-acp has no workspace to spawn in. Send an absolute "
                "session project_dir or request project_dir/workspace_path/cwd/"
                "project, or set extra.workspace_path / CURSOR_CLI_WORKSPACE."
            ),
            details={"error_code": "cursor_cli_acp_workspace_required"},
        )

    def _build_runtime_key(
        self,
        project_dir: Path,
        model: str,
        client_session_id: str,
        *,
        responses_text_only: bool = False,
    ) -> tuple[str, str, str]:
        """Reuse one Cursor child per workspace/model and execution mode."""
        mode_key = (
            f"responses-text-only:{client_session_id}"
            if responses_text_only
            else "default"
        )
        return super()._build_runtime_key(project_dir, model, mode_key)

    def _is_responses_text_only_request(
        self, request: ConnectorChatCompletionsRequest
    ) -> bool:
        extra_body = getattr(request.request, "extra_body", None)
        return bool(
            isinstance(extra_body, dict)
            and extra_body.get(ACP_RESPONSES_TEXT_ONLY_MODE_KEY) is True
        )

    def _resolve_client_session_id(
        self, request: ConnectorChatCompletionsRequest
    ) -> str:
        extra_body = getattr(request.request, "extra_body", None)
        if (
            isinstance(extra_body, dict)
            and extra_body.get(ACP_RESPONSES_TEXT_ONLY_MODE_KEY) is True
        ):
            raw = getattr(request.request, "session_id", None)
            if isinstance(raw, str) and raw.strip():
                return raw.strip()
        return super()._resolve_client_session_id(request)

    async def _compute_history_and_user_message(
        self,
        runtime: ACPProcessRuntime,
        messages: Sequence[Any],
    ) -> tuple[str, HistoryState]:
        """Keep the pre-session-isolation Cursor transcript behavior.

        Cursor's ACP child is shared across proxy request/session identifiers;
        send the full transcript only when that child is first started, then
        send the latest user message on subsequent turns.  The generic ACP
        divergence reset would otherwise restart Cursor and re-authenticate it
        whenever a new B2BUA attempt has a different message prefix.
        """
        if runtime.history_state is None:
            return (
                ACPTranscriptSerializer.serialize(messages),
                HistoryState(message_count=len(messages), prefix_hash="cursor-shared"),
            )

        latest_user_message = self._extract_user_message_as_string(messages)
        instruction_lines: list[str] = []
        for message in messages:
            if isinstance(message, ChatMessage):
                role = message.role
                content = message.content
            elif isinstance(message, dict):
                role = str(message.get("role", ""))
                content = message.get("content")
            else:
                role = str(getattr(message, "role", ""))
                content = getattr(message, "content", "")

            if role.casefold() not in {"system", "developer"}:
                continue
            instruction = self._stringify_message_content(content).strip()
            if instruction:
                instruction_lines.append(
                    f"**{role.capitalize()} instruction:** {instruction}"
                )

        if instruction_lines:
            # Cursor's ACP continuation API accepts one prompt string and does
            # not receive the canonical ``system_prompt`` field. Re-send the
            # current instruction prefix with each incremental turn so a new
            # Responses ``instructions`` value is honored by the existing
            # subprocess conversation.
            prompt = (
                "[System Note: Apply these current instructions for this turn. "
                "They override any earlier instructions.]\n\n"
                + "\n".join(instruction_lines)
                + "\n\n[Current Request]\n"
                + latest_user_message
            )
        else:
            prompt = latest_user_message

        return (
            prompt,
            HistoryState(message_count=len(messages), prefix_hash="cursor-shared"),
        )

    def _stale_acp_kill_enabled(self) -> bool:
        """Keep the authenticated Cursor ACP child alive across idle periods."""
        return False

    async def _build_subprocess_command(self, runtime: ACPProcessRuntime) -> list[str]:
        return build_cursor_agent_acp_command(
            self._cursor_cli_executable,
            model=runtime.model,
            trust_workspace=self._trust_workspace,
            extra_args=self._extra_cli_args,
            cursor_api_endpoint=self._cursor_api_endpoint,
            mode="ask" if runtime.responses_text_only_mode else None,
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
                    "_meta": {"parameterizedModelPicker": True},
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
                message=f"Cursor CLI initialize failed: {initialize_response.error.message}",
                details=initialize_response.error.model_dump(),
            )

        # Cursor resolves credentials from its existing `agent login` state or
        # CURSOR_API_KEY. Calling ACP `authenticate(cursor_login)` here forces an
        # interactive browser flow even when the child is already authenticated.
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
                message=f"Cursor CLI session/new failed: {session_new_response.error.message}",
                details=session_new_response.error.model_dump(),
            )

        session_result = session_new_response.result or {}
        session_id = session_result.get("sessionId")
        if not isinstance(session_id, str) or not session_id.strip():
            raise BackendError(
                message="Cursor CLI session/new did not return a sessionId",
                details={"result": session_result},
            )

        runtime.session_id = session_id

        requested_fast = _cursor_model_fast_tier(
            runtime.model, available_cli_model_ids=self._model_cli_ids.values()
        )
        fast_option = _cursor_config_option(
            _cursor_config_options(session_result), "fast"
        )
        if requested_fast is None and fast_option is not None:
            # The parameterized picker exposes the speed switch even when the
            # catalog did not include a distinct standard route. Keep the
            # default request on standard speed unless the caller selected an
            # explicit ``-fast`` model.
            requested_fast = False

        if requested_fast is not None:
            if fast_option is None:
                raise BackendError(
                    message=(
                        "Cursor ACP did not expose the parameterized fast option "
                        f"for model {runtime.model!r}"
                    ),
                    details={
                        "code": "cursor_speed_tier_unavailable",
                        "model": runtime.model,
                        "requested_fast": requested_fast,
                    },
                )

            requested_value = "true" if requested_fast else "false"
            set_config_id = await self._send_jsonrpc_message(
                runtime,
                "session/set_config_option",
                {
                    "sessionId": session_id,
                    "configId": "fast",
                    "value": requested_value,
                },
            )
            set_config_response = await self._await_response(runtime, set_config_id)
            if set_config_response.is_error:
                error_message = (
                    set_config_response.error.message
                    if set_config_response.error is not None
                    else "unknown ACP error"
                )
                raise BackendError(
                    message=(
                        "Cursor ACP rejected the requested fast tier "
                        f"{requested_value!r}: {error_message}"
                    ),
                    details={
                        "code": "cursor_speed_tier_rejected",
                        "model": runtime.model,
                        "requested_fast": requested_fast,
                        "error": (
                            set_config_response.error.model_dump()
                            if set_config_response.error is not None
                            else None
                        ),
                    },
                )

            set_config_result = set_config_response.result or {}
            confirmed_option = _cursor_config_option(
                _cursor_config_options(set_config_result), "fast"
            )
            effective_value = (
                _cursor_config_option_current_value(confirmed_option)
                if confirmed_option is not None
                else None
            )
            if effective_value != requested_value:
                raise BackendError(
                    message=(
                        "Cursor ACP did not confirm the requested fast tier: "
                        f"requested {requested_value!r}, effective {effective_value!r}"
                    ),
                    details={
                        "code": "cursor_speed_tier_mismatch",
                        "model": runtime.model,
                        "requested_fast": requested_fast,
                        "requested_value": requested_value,
                        "effective_value": effective_value,
                    },
                )

        runtime.initialized = True

    def _permission_option_id(self) -> str:
        if self._auto_accept:
            return "allow-always"
        return "reject-once"

    async def _prepare_turn_request_locked(
        self,
        runtime: ACPProcessRuntime,
        request: ConnectorChatCompletionsRequest,
    ) -> tuple[int, str]:
        extra_body = getattr(request.request, "extra_body", None)
        runtime.responses_text_only_mode = bool(
            isinstance(extra_body, dict)
            and extra_body.get(ACP_RESPONSES_TEXT_ONLY_MODE_KEY) is True
        )
        runtime.responses_standalone_mode = bool(
            runtime.responses_text_only_mode
            and isinstance(extra_body, dict)
            and extra_body.get(ACP_RESPONSES_STANDALONE_MODE_KEY) is True
        )
        try:
            return await super()._prepare_turn_request_locked(runtime, request)
        except BaseException:
            # Startup, handshake, and session/prompt preparation all happen
            # before BaseAcpConnector gets a chance to schedule its normal
            # post-turn retirement.  Responses turns use one-shot runtimes,
            # so leaving a failed runtime in the pool would orphan its child
            # process and make every failed request consume another slot.
            if runtime.responses_standalone_mode:
                await self._retire_standalone_runtime(runtime)
            raise

    async def _retire_standalone_runtime(self, runtime: ACPProcessRuntime) -> None:
        """Kill and remove an ephemeral Responses runtime after preparation fails."""

        try:
            await self._kill_runtime(runtime)
        except asyncio.CancelledError:
            raise
        except Exception:
            # Preserve the preparation error for the caller while still
            # retiring the unusable pool entry below.
            logger.warning(
                "Failed to terminate standalone Cursor ACP runtime after turn preparation failure",
                exc_info=True,
            )
        finally:
            await self._remove_runtime_from_pool(runtime)

    async def _remove_runtime_from_pool(self, runtime: ACPProcessRuntime) -> None:
        """Remove a retired runtime slot without disturbing other conversations."""

        async with self._runtime_pool_lock:
            retired_keys = [
                key for key, candidate in self._runtimes.items() if candidate is runtime
            ]
            for key in retired_keys:
                self._runtimes.pop(key, None)

    async def _schedule_stale_kill_after_turn(self, runtime: ACPProcessRuntime) -> None:
        """Retire one-shot Responses runtimes immediately after their turn.

        Cursor ACP keeps stale-kill scheduling disabled for authenticated pooled
        sessions.  Responses requests use a unique internal conversation key and
        replay stored transcript history, so every Responses runtime is one-shot.
        Explicitly kill and remove those marked ephemeral after each turn.
        """

        if runtime.responses_standalone_mode:
            try:
                await self._kill_runtime(runtime)
            finally:
                # Pool retirement must not depend on process termination being
                # fully successful.  A failed kill still leaves the runtime
                # unusable for a one-shot Responses turn and retaining its pool
                # entry would make every standalone request accumulate state.
                await self._remove_runtime_from_pool(runtime)
            return
        await super()._schedule_stale_kill_after_turn(runtime)

    async def _cancel_active_request(
        self,
        runtime: ACPProcessRuntime,
        prompt_request_id: int,
        expected_generation: int | None = None,
    ) -> bool:
        owned_at_entry = (
            expected_generation is None
            or runtime.active_request_generation == expected_generation
        )
        try:
            cancelled = await super()._cancel_active_request(
                runtime,
                prompt_request_id,
                expected_generation=expected_generation,
            )
        except BaseException:
            if owned_at_entry and runtime.responses_standalone_mode:
                await self._remove_runtime_from_pool(runtime)
            raise
        if cancelled and runtime.responses_standalone_mode:
            await self._remove_runtime_from_pool(runtime)
        return cancelled

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
                        "optionId": (
                            "reject-once"
                            if runtime.responses_text_only_mode
                            else self._permission_option_id()
                        ),
                    }
                },
            )
            return

        if method == "cursor/ask_question":
            if method not in self._extension_response_log:
                self._extension_response_log.add(method)
                if logger.isEnabledFor(logging.INFO):
                    logger.info(
                        "ACP extension %s: auto-skipping (headless proxy)", method
                    )
            await self._send_jsonrpc_result(
                runtime,
                rid,
                {"outcome": {"outcome": "skipped", "reason": "proxy_auto_skip"}},
            )
            return

        if method == "cursor/create_plan":
            if method not in self._extension_response_log:
                self._extension_response_log.add(method)
                if logger.isEnabledFor(logging.INFO):
                    logger.info(
                        "ACP extension %s: auto-rejecting (headless proxy)", method
                    )
            await self._send_jsonrpc_result(
                runtime,
                rid,
                {"outcome": {"outcome": "rejected", "reason": "proxy_auto_reject"}},
            )
            return

        if method.startswith("cursor/"):
            if method not in self._extension_response_log:
                self._extension_response_log.add(method)
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Unhandled Cursor ACP extension %s; replying empty result",
                        method,
                    )
            await self._send_jsonrpc_result(runtime, rid, {})
            return

        if logger.isEnabledFor(logging.WARNING):
            logger.warning(
                "Unhandled inbound JSON-RPC request method=%s id=%s; replying error",
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


from src.core.services.backend_registry import backend_registry

backend_registry.register_backend("cursor-cli-acp", CursorCliAcpConnector)
