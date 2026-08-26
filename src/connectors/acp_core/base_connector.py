from __future__ import annotations

import asyncio
import contextlib
import hashlib
import json
import logging
import os
import re
import subprocess
import threading
import time
import uuid
from abc import ABC, abstractmethod
from collections.abc import AsyncGenerator, AsyncIterator, Sequence
from pathlib import Path
from typing import Any, Generic, TypeVar, cast

from src.connectors.acp_core.acp_subprocess_identity import (
    capture_acp_subprocess_identity,
    stale_kill_still_same_os_process,
)
from src.connectors.acp_core.tool_markdown import (
    acp_tool_payload_should_emit,
    coalesce_acp_tool_call_update_session_dict,
    extract_tool_correlation_key,
    extract_tool_input,
    extract_tool_name,
    extract_tool_output,
    format_acp_tool_completion_summary,
    format_acp_tool_heartbeat_line,
    format_acp_tool_started_summary,
    is_terminal_tool_status,
    iter_coalesced_acp_tool_session_dicts,
    payload_utf8_byte_length,
    utc_now_iso,
)
from src.connectors.acp_core.transcript import ACPTranscriptSerializer
from src.connectors.acp_core.types import (
    ACPError,
    ACPNotification,
    ACPProcessRuntime,
    ACPSessionUpdate,
    AcpStreamPiece,
    AcpToolStreamAccum,
    ACPUpdateContent,
    HistoryState,
)
from src.connectors.acp_core.workspace_policy import (
    ACP_MISSING_PROJECT_WORKSPACE_CODE,
    first_usable_workspace_dir,
    first_workspace_hint_str,
    is_usable_workspace_directory,
)
from src.connectors.base import LLMBackend, add_vendor_prefix, strip_vendor_prefix
from src.connectors.contracts import ConnectorChatCompletionsRequest
from src.connectors.mixins.usage_calculation_mixin import UsageCalculationMixin
from src.core.common.exceptions import (
    APIConnectionError,
    APITimeoutError,
    BackendError,
    ConfigurationError,
    ServiceUnavailableError,
)
from src.core.domain.chat import (
    CanonicalChatResponse,
    ChatCompletionChoice,
    ChatCompletionChoiceMessage,
    ChatMessage,
)
from src.core.domain.responses import (
    ProcessedResponse,
    ResponseEnvelope,
    StreamingResponseEnvelope,
)
from src.core.services.streaming.processed_stream_idle_keepalive import (
    wrap_processed_stream_with_idle_keepalive,
)

logger = logging.getLogger(__name__)

DEFAULT_PROCESS_TIMEOUT = 300.0
DEFAULT_IDLE_TIMEOUT = 30.0
DEFAULT_ACP_TOOL_HEARTBEAT_SECONDS = 30.0
MAX_RESPONSE_LINE_SIZE = 10 * 1024 * 1024
MAX_STDERR_TAIL_SIZE = 16 * 1024
ACP_UPDATE_METHOD = "session/update"
ACP_AGENT_MESSAGE_CHUNK = "agent_message_chunk"
ACP_AGENT_THOUGHT_CHUNK = "agent_thought_chunk"
ACP_CANCEL_METHODS = ("session/cancel", "session/stop", "session/end")
ACP_GRACEFUL_CANCEL_TIMEOUT_SECONDS = 12.0
# Default idle delay after a completed chat turn before terminating the pooled ACP child.
# Override with ``stale_acp_agent_kill_idle_seconds`` (config / env / CLI).
DEFAULT_STALE_ACP_AGENT_KILL_IDLE_SECONDS = 3600.0
ACP_RATE_LIMIT_BACKOFF_DELAYS: tuple[float, ...] = (5.0, 10.0, 30.0, 60.0)


def is_rate_limit_error(message: str | None) -> bool:
    """Detect rate limit or quota exhaustion errors across LLM gateways and ACP errors."""
    if not message:
        return False
    msg = message.lower()
    return any(
        pattern in msg
        for pattern in (
            "ratelimit",
            "rate_limit",
            "rate-limit",
            "rate limited",
            "rate limit",
            "gatewayratelimiterror",
            "429",
            "too many requests",
            "quota exceeded",
            "resource exhausted",
        )
    )


def _format_acp_error(error: ACPError) -> str:
    """Include JSON-RPC error data that generic ACP messages otherwise hide."""
    message = error.message.strip() or "Unknown ACP error"
    detail: str | None = None
    if isinstance(error.data, str):
        detail = error.data.strip()
    elif isinstance(error.data, dict):
        for key in ("error", "message", "details"):
            value = error.data.get(key)
            if isinstance(value, str) and value.strip():
                detail = value.strip()
                break

    if detail and detail.casefold() not in message.casefold():
        return f"{message}: {detail}"
    return message


_STALE_ACP_KILL_DELAY_MAX_SECONDS = 604800.0  # 7 days
# Increment when the canonicalization used for ACP history prefix hashes changes.
HISTORY_PREFIX_HASH_VERSION = 2

#: The runtime type a connector manages. Bound to :class:`ACPProcessRuntime` so
#: base-class code only touches common fields, while subclasses parameterize it
#: with a protocol-specific runtime (e.g. :class:`CodexAppServerRuntime`) and
#: get type-safe access to the extra fields inside their overrides.
RuntimeT = TypeVar("RuntimeT", bound=ACPProcessRuntime)


def _canonical_chat_message_for_history_hash(message: ChatMessage) -> dict[str, Any]:
    """Return stable identity fields for divergence detection.

    Uses :meth:`ChatMessage.to_dict` so fields such as ``metadata`` that are not
    part of the visible transcript do not spuriously invalidate the prefix hash.
    """

    return message.to_dict()


def _hash_chat_messages_prefix_stable(
    messages: Sequence[ChatMessage], end_exclusive: int
) -> str:
    """SHA-256 hex digest of the first ``end_exclusive`` messages (conversation prefix)."""

    if end_exclusive <= 0:
        return hashlib.sha256(b"").hexdigest()
    slice_msgs = messages[:end_exclusive]
    payload = [_canonical_chat_message_for_history_hash(m) for m in slice_msgs]
    canonical = json.dumps(
        {"m": payload, "v": HISTORY_PREFIX_HASH_VERSION},
        sort_keys=True,
        separators=(",", ":"),
    )
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


class _RuntimeCancellable(Generic[RuntimeT]):
    __slots__ = ("_connector", "_runtime", "_prompt_request_id")

    def __init__(
        self,
        connector: BaseAcpConnector[RuntimeT],
        runtime: RuntimeT,
        prompt_request_id: int,
    ) -> None:
        self._connector = connector
        self._runtime = runtime
        self._prompt_request_id = prompt_request_id

    def cancel(self) -> None:
        if self._runtime.cancellation_event is not None:
            self._runtime.cancellation_event.set()
        if self._runtime.cancellation_lock is None:
            return
        try:
            loop = asyncio.get_running_loop()
        except RuntimeError:
            return
        connector = cast(Any, self._connector)
        task = loop.create_task(
            connector._cancel_active_request(self._runtime, self._prompt_request_id)
        )
        task.add_done_callback(lambda t: t.exception() if not t.cancelled() else None)


class BaseAcpConnector(LLMBackend, UsageCalculationMixin, ABC, Generic[RuntimeT]):
    """Base class for ACP-based connectors."""

    VENDOR_PREFIX: str
    requires_explicit_workspace: bool = False

    def __init__(
        self, config: Any, translation_service: Any = None, **kwargs: Any
    ) -> None:
        super().__init__(config)
        self.translation_service = translation_service
        self.is_functional = False
        self._initialization_failed = False
        self._validation_errors: list[str] = []
        self._default_project_dir: Path | None = None
        self._model = "auto"
        self._process_timeout = DEFAULT_PROCESS_TIMEOUT
        self._idle_timeout = DEFAULT_IDLE_TIMEOUT
        self._acp_tool_heartbeat_seconds = DEFAULT_ACP_TOOL_HEARTBEAT_SECONDS
        self._rate_limit_backoff_delays: tuple[float, ...] = (
            ACP_RATE_LIMIT_BACKOFF_DELAYS
        )
        self._turn_pacing_delay_seconds: float = 0.0
        self._runtime_pool_lock = asyncio.Lock()
        self._runtimes: dict[tuple[str, str, str], RuntimeT] = {}

    @property
    def has_static_credentials(self) -> bool:
        return False

    def is_backend_functional(self) -> bool:
        return (
            self.is_functional
            and not self._initialization_failed
            and len(self._validation_errors) == 0
        )

    def _is_backend_functional_internal(self) -> bool:
        return self.is_backend_functional()

    def get_validation_errors(self) -> list[str]:
        return self._validation_errors.copy()

    def _resolve_stream_keepalive_interval(self) -> float:
        """Seconds between idle keepalive chunks while waiting for ACP subprocess output."""
        cfg = getattr(self, "config", None)
        failure_handling = getattr(cfg, "failure_handling", None)
        interval = getattr(failure_handling, "keepalive_interval", None)
        if isinstance(interval, int | float) and float(interval) > 0:
            return float(interval)
        return 12.0

    def _acp_transient_error_retry_delays(
        self,
        runtime: RuntimeT,
        response_model: str,
        error: ACPError,
    ) -> tuple[float, ...]:
        """Return connector-specific retry delays for transient ACP turn errors.

        ACP connectors may opt into bounded retries for provider failures that
        are safe to repeat before any assistant content has been emitted. The
        base connector deliberately returns no extra retries; this keeps
        provider-specific policy out of unrelated ACP backends.
        """

        del runtime, response_model, error
        return ()

    def _acp_transient_retry_label(self, runtime: RuntimeT, response_model: str) -> str:
        """Return the user-facing label for connector-specific transient retries."""

        del runtime, response_model
        return "ACP provider"

    def _stale_acp_kill_enabled(self) -> bool:
        return not bool(getattr(self.config, "disable_stale_acp_agent_kills", False))

    def _stale_acp_kill_delay_seconds(self) -> float:
        raw = getattr(
            self.config,
            "stale_acp_agent_kill_idle_seconds",
            DEFAULT_STALE_ACP_AGENT_KILL_IDLE_SECONDS,
        )
        if isinstance(raw, bool) or not isinstance(raw, int | float):
            return DEFAULT_STALE_ACP_AGENT_KILL_IDLE_SECONDS
        v = float(raw)
        if v != v:  # NaN
            return DEFAULT_STALE_ACP_AGENT_KILL_IDLE_SECONDS
        return max(1.0, min(v, _STALE_ACP_KILL_DELAY_MAX_SECONDS))

    async def _cancel_stale_kill_timer(self, runtime: RuntimeT) -> None:
        task = runtime.stale_kill_task
        if task is None:
            return
        current = asyncio.current_task()
        if task is current:
            runtime.stale_kill_task = None
            return
        if not task.done():
            task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await task
        runtime.stale_kill_task = None

    async def _schedule_stale_kill_after_turn(self, runtime: RuntimeT) -> None:
        await self._cancel_stale_kill_timer(runtime)
        if not self._stale_acp_kill_enabled():
            return
        delay = self._stale_acp_kill_delay_seconds()
        if logger.isEnabledFor(logging.INFO):
            logger.info(
                "Scheduling stale ACP agent kill in %.0fs (backend=%s project=%s "
                "model=%s client_session=%s)",
                delay,
                self.backend_type,
                runtime.project_dir,
                runtime.model,
                runtime.client_session_id,
            )
        connector = self
        runtime_ref = runtime

        async def _run() -> None:
            try:
                try:
                    await asyncio.sleep(delay)
                except asyncio.CancelledError:
                    return
                if not connector._stale_acp_kill_enabled():
                    return
                proc = runtime_ref.process
                if proc is None or proc.poll() is not None:
                    return
                ident = runtime_ref.acp_subprocess_identity
                if ident is not None and not stale_kill_still_same_os_process(
                    proc, ident
                ):
                    if logger.isEnabledFor(logging.INFO):
                        logger.info(
                            "Stale ACP kill skipped: OS process identity mismatch "
                            "(backend=%s pid=%s project=%s)",
                            connector.backend_type,
                            getattr(proc, "pid", None),
                            runtime_ref.project_dir,
                        )
                    return
                if ident is None and logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "ACP stale kill has no subprocess identity fingerprint "
                        "(backend=%s pid=%s); relying on subprocess handle state only",
                        connector.backend_type,
                        getattr(proc, "pid", None),
                    )
                req_lock = runtime_ref.request_lock
                if req_lock is not None and req_lock.locked():
                    if logger.isEnabledFor(logging.INFO):
                        logger.info(
                            "Stale ACP kill skipped: request in progress "
                            "(backend=%s pid=%s)",
                            connector.backend_type,
                            getattr(proc, "pid", None),
                        )
                    return
                if logger.isEnabledFor(logging.INFO):
                    logger.info(
                        "Stale ACP agent idle timeout: terminating process (backend=%s "
                        "pid=%s project=%s model=%s client_session=%s)",
                        connector.backend_type,
                        getattr(proc, "pid", None),
                        runtime_ref.project_dir,
                        runtime_ref.model,
                        runtime_ref.client_session_id,
                    )
                await connector._kill_runtime(runtime_ref)
            except asyncio.CancelledError:
                raise
            except Exception:
                logger.exception(
                    "Stale ACP agent kill task failed (backend=%s project=%s model=%s "
                    "client_session=%s)",
                    connector.backend_type,
                    runtime_ref.project_dir,
                    runtime_ref.model,
                    runtime_ref.client_session_id,
                )

        runtime.stale_kill_task = asyncio.create_task(_run())

    @abstractmethod
    async def _build_subprocess_command(self, runtime: RuntimeT) -> list[str]:
        """Build the command to spawn the backend subprocess."""

    @abstractmethod
    async def _perform_handshake(self, runtime: RuntimeT) -> None:
        """Perform the protocol handshake (initialize, authenticate, session/new)."""

    @abstractmethod
    async def _handle_server_request(
        self, runtime: RuntimeT, msg: ACPNotification
    ) -> None:
        """Handle server-initiated JSON-RPC requests (e.g., permissions)."""

    @abstractmethod
    def _create_runtime(
        self, project_dir: Path, model: str, client_session_id: str = "default"
    ) -> RuntimeT:
        """Construct a fresh runtime instance with its own locks."""

    def _reset_protocol_runtime_state(self, runtime: RuntimeT) -> None:
        """Reset protocol-specific runtime state before a (re)spawn/teardown.

        Called from :meth:`_spawn_process` and :meth:`_cleanup_runtime_state`
        instead of inline resets. The default clears the ACP ``session_id``;
        Codex overrides to clear ``thread_id`` / ``turn_id`` /
        ``pending_history_state`` (and must NOT touch ``session_id``). Common
        resets (``process``, ``initialized``, ``message_id``, ``last_activity``,
        ``history_state``, ``acp_subprocess_identity``) stay inline in the base.
        """

        runtime.session_id = None

    @staticmethod
    def _is_usable_directory(path: Path) -> bool:
        return is_usable_workspace_directory(path)

    def _build_runtime_key(
        self,
        project_dir: Path,
        model: str,
        client_session_id: str,
        *,
        responses_text_only: bool = False,
    ) -> tuple[str, str, str]:
        return (str(project_dir), model, client_session_id)

    def _is_responses_text_only_request(
        self, request: ConnectorChatCompletionsRequest
    ) -> bool:
        return False

    def _resolve_client_session_id(
        self, request: ConnectorChatCompletionsRequest
    ) -> str:
        """Resolve the logical client session used to key ACP subprocess pools.

        When neither ``ConnectorRequestContext.session_id`` nor
        ``request.request.session_id`` is set, callers share the pool key
        ``"default"`` (one ACP runtime per ``(project_dir, model)`` for all
        such traffic). Upstream layers should set a stable session id when
        isolation between clients or tabs is required.
        """

        sid: str | None = None
        if request.context is not None and request.context.session_id:
            sid = request.context.session_id
        if not sid:
            raw = getattr(request.request, "session_id", None)
            if isinstance(raw, str) and raw.strip():
                sid = raw
        if not sid and request.options:
            for key in ("session_id", "client_session_id"):
                raw_opt = request.options.get(key)
                if isinstance(raw_opt, str) and raw_opt.strip():
                    sid = raw_opt
                    break
        if not sid:
            extra_body = getattr(request.request, "extra_body", None)
            if isinstance(extra_body, dict):
                for key in ("session_id", "client_session_id"):
                    raw_eb = extra_body.get(key)
                    if isinstance(raw_eb, str) and raw_eb.strip():
                        sid = raw_eb
                        break
        if isinstance(sid, str) and sid.strip():
            normalized = sid.strip()
            # If this is a B2BUA B-leg attempt ID (e.g., llm-b2bua-b-<uuid>-<seq>),
            # strip the per-attempt sequence suffix so that all attempts and turns
            # for the same conversation share the same pooled ACP runtime.
            normalized = re.sub(r"^(llm-b2bua-b-[0-9a-fA-F-]+)-\d+$", r"\1", normalized)
            return normalized
        return "default"

    @staticmethod
    def _hash_messages_prefix(
        messages: Sequence[ChatMessage], end_exclusive: int
    ) -> str:
        return _hash_chat_messages_prefix_stable(messages, end_exclusive)

    async def _acquire_runtime(
        self, request: ConnectorChatCompletionsRequest
    ) -> RuntimeT:
        project_dir = self._resolve_project_dir_for_request(request)
        requested_model = strip_vendor_prefix(
            request.effective_model or self._model, self.VENDOR_PREFIX
        )
        client_session_id = self._resolve_client_session_id(request)
        runtime_key = self._build_runtime_key(
            project_dir,
            requested_model,
            client_session_id,
            responses_text_only=self._is_responses_text_only_request(request),
        )
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "ACP runtime lookup: key=%s project=%s model=%s client_session=%s",
                runtime_key,
                project_dir,
                requested_model,
                client_session_id,
            )

        async with self._runtime_pool_lock:
            runtime = self._runtimes.get(runtime_key)
            if runtime is None:
                runtime = self._create_runtime(
                    project_dir, requested_model, client_session_id
                )
                self._runtimes[runtime_key] = runtime

        return await self._reap_idle_runtime(runtime_key, runtime)

    def _resolve_project_dir_for_request(
        self, request: ConnectorChatCompletionsRequest
    ) -> Path:
        extra_body: object = getattr(request.request, "extra_body", None)
        extra_dict = cast(dict[str, Any] | None, extra_body)
        options = cast(dict[str, Any] | None, request.options)

        usable = first_usable_workspace_dir(
            extra_dict, options, is_usable=is_usable_workspace_directory
        )
        if usable is not None:
            return usable

        if self.requires_explicit_workspace:
            hint = first_workspace_hint_str(extra_dict, options)
            if hint is not None:
                raise BackendError(
                    message=f"Unusable ACP workspace directory: {hint}",
                    details={"code": ACP_MISSING_PROJECT_WORKSPACE_CODE, "hint": hint},
                )
            raise BackendError(
                message=(
                    "ACP backend requires an explicit workspace directory "
                    "(session project_dir or request project_dir/workspace_path/cwd/project)."
                ),
                details={"code": ACP_MISSING_PROJECT_WORKSPACE_CODE},
            )

        hint = first_workspace_hint_str(extra_dict, options)
        if hint is not None and logger.isEnabledFor(logging.DEBUG):
            logger.debug("Ignoring unusable ACP project_dir override: %s", hint)

        if self._default_project_dir is None:
            raise ConfigurationError(
                message=f"{self.backend_type} backend has no default project directory configured"
            )

        return self._default_project_dir

    async def _reap_idle_runtime(
        self, runtime_key: tuple[str, str, str], runtime: RuntimeT
    ) -> RuntimeT:
        """Drop idle subprocesses and swap in a fresh :class:`ACPProcessRuntime` slot.

        Replacing the pool entry (instead of only clearing ``runtime.process``)
        avoids unbounded growth of dead :class:`ACPProcessRuntime` objects while
        ensuring concurrent acquirers always resolve to the canonical instance
        currently registered for ``runtime_key``.
        """

        if self._idle_timeout <= 0:
            return runtime
        if runtime.request_lock is None or runtime.request_lock.locked():
            return runtime
        if runtime.process is None:
            # Another concurrent acquirer may have already idle-reaped this
            # key and swapped in a fresh runtime. Re-read the pool to return
            # the canonical instance; otherwise we would spawn a duplicate
            # child on a detached runtime (duplicate agents + divergent
            # history for one workspace/session/model tuple).
            async with self._runtime_pool_lock:
                canonical = self._runtimes.get(runtime_key)
            if canonical is not None and canonical is not runtime:
                return canonical
            return runtime
        if runtime.last_activity <= 0:
            return runtime
        if (time.monotonic() - runtime.last_activity) < self._idle_timeout:
            return runtime

        await self._kill_runtime(runtime)

        async with self._runtime_pool_lock:
            current = self._runtimes.get(runtime_key)
            if current is runtime:
                replacement = self._create_runtime(
                    runtime.project_dir, runtime.model, runtime.client_session_id
                )
                self._runtimes[runtime_key] = replacement
                return replacement
            if current is not None:
                return current
        return runtime

    def _subprocess_env(self, runtime: RuntimeT) -> dict[str, str]:
        """Environment for the ACP child process. Subclasses may specialize."""
        del runtime
        return os.environ.copy()

    async def _spawn_process(self, runtime: RuntimeT) -> None:
        assert runtime.process_lock is not None
        async with runtime.process_lock:
            process = runtime.process
            if process is not None and process.poll() is None:
                return

            cmd = await self._build_subprocess_command(runtime)

            new_process: subprocess.Popen[bytes] | None = None
            try:
                new_process = subprocess.Popen(
                    cmd,
                    stdin=subprocess.PIPE,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    cwd=str(runtime.project_dir),
                    shell=False,
                    env=self._subprocess_env(runtime),
                    creationflags=(
                        subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0
                    ),
                )
                runtime.process = new_process
                runtime.stderr_drain_stop_event.clear()
                # Clear diagnostics before the reader starts so bytes emitted
                # immediately during process startup cannot be erased by a
                # late reset.
                with runtime.stderr_tail_lock:
                    runtime.stderr_tail.clear()
                runtime.stderr_drain_thread = threading.Thread(
                    target=self._drain_stderr_thread,
                    args=(new_process, runtime),
                    name=f"acp-stderr-{new_process.pid}",
                    daemon=True,
                )
                runtime.stderr_drain_thread.start()
                await asyncio.sleep(0.1)
                if new_process.poll() is not None:
                    stderr = await self._read_stderr(new_process, runtime)
                    raise BackendError(
                        message="ACP process failed to start",
                        details={
                            "stderr": stderr,
                            "project_dir": str(runtime.project_dir),
                            "model": runtime.model,
                            "command": cmd,
                        },
                    )
                runtime.last_activity = time.monotonic()
                runtime.initialized = False
                self._reset_protocol_runtime_state(runtime)
                runtime.message_id = 0
                runtime.history_state = None
                runtime.process_cwd = runtime.project_dir
                runtime.acp_subprocess_identity = capture_acp_subprocess_identity(
                    new_process, cmd
                )
            except Exception as exc:
                if new_process is not None:
                    self._stop_stderr_drain(runtime)
                    self._cleanup_process(new_process)
                    self._join_stderr_drain_thread(runtime)
                runtime.process = None
                runtime.initialized = False
                self._reset_protocol_runtime_state(runtime)
                runtime.history_state = None
                runtime.process_cwd = None
                runtime.acp_subprocess_identity = None
                with runtime.stderr_tail_lock:
                    runtime.stderr_tail.clear()
                raise APIConnectionError(
                    message=f"Failed to start ACP process: {exc}",
                    details={
                        "project_dir": str(runtime.project_dir),
                        "model": runtime.model,
                    },
                ) from exc

    async def _kill_runtime(self, runtime: RuntimeT) -> None:
        await self._cancel_stale_kill_timer(runtime)
        assert runtime.process_lock is not None
        async with runtime.process_lock:
            process = runtime.process
            if process is None:
                # Logical runtime state must be reset even when the child has
                # already disappeared. Otherwise a failed turn can leave a
                # stale history/session snapshot attached to the pooled key.
                self._cleanup_runtime_state(runtime)
                return

            try:
                await self._terminate_process(process)
            finally:
                self._cleanup_runtime_state(runtime, process)

    async def _kill_all_runtimes(self) -> None:
        async with self._runtime_pool_lock:
            runtimes = list(self._runtimes.values())
            self._runtimes.clear()

        if runtimes:
            await asyncio.gather(
                *(self._kill_runtime(runtime) for runtime in runtimes),
                return_exceptions=True,
            )

    def _cleanup_runtime_state(
        self, runtime: RuntimeT, process: subprocess.Popen[bytes] | None = None
    ) -> None:
        process_cwd = runtime.process_cwd
        self._stop_stderr_drain(runtime)
        self._cleanup_process(process or runtime.process)
        self._join_stderr_drain_thread(runtime)
        runtime.process = None
        runtime.initialized = False
        self._reset_protocol_runtime_state(runtime)
        runtime.message_id = 0
        runtime.last_activity = 0.0
        runtime.history_state = None
        runtime.process_cwd = None
        runtime.last_prompt_params = None
        runtime.acp_subprocess_identity = None
        with runtime.stderr_tail_lock:
            runtime.stderr_tail.clear()
        try:
            self._cleanup_process_working_directory(runtime, process_cwd)
        except Exception:
            logger.exception(
                "Failed to clean up ACP subprocess working directory: %s",
                process_cwd,
            )

    def _cleanup_process_working_directory(
        self, runtime: RuntimeT, process_cwd: Path | None
    ) -> None:
        """Release connector-owned subprocess working-directory state.

        Most ACP connectors run directly in ``runtime.project_dir``. A
        connector that creates a disposable app root can override this hook;
        cleanup is deliberately best-effort so it cannot hide the original
        protocol/process failure.
        """

        del runtime, process_cwd

    def _cleanup_process(self, process: subprocess.Popen[bytes] | None = None) -> None:
        if process is None:
            return

        for stream_name in ("stdin", "stdout", "stderr"):
            stream = getattr(process, stream_name, None)
            if stream is None:
                continue
            with contextlib.suppress(OSError, ValueError):
                stream.close()

    def _stop_stderr_drain(self, runtime: RuntimeT) -> None:
        runtime.stderr_drain_stop_event.set()

    def _join_stderr_drain_thread(self, runtime: RuntimeT) -> None:
        """Detach the stderr reader without blocking the event loop.

        Runtime cleanup is invoked from async request/cancellation paths.  The
        reader is a daemon thread and its pipe is closed by
        :meth:`_cleanup_process` immediately before this helper is called, so
        it will exit promptly.  A zero-timeout join lets an already-finished
        reader be reclaimed while avoiding a synchronous wait on the event
        loop.  If the reader is still unwinding, it remains detached and will
        terminate once the closed pipe is observed.
        """
        thread = runtime.stderr_drain_thread
        if thread is None:
            return
        runtime.stderr_drain_thread = None
        if thread is not threading.current_thread():
            thread.join(timeout=0)

    def _drain_stderr_thread(
        self, process: subprocess.Popen[bytes], runtime: RuntimeT
    ) -> None:
        if process.stderr is None:
            return

        def _read_chunk() -> bytes:
            stream = cast(Any, process.stderr)
            return bytes(stream.read1(4096))

        try:
            while not runtime.stderr_drain_stop_event.is_set():
                chunk = _read_chunk()
                if not chunk:
                    return
                with runtime.stderr_tail_lock:
                    runtime.stderr_tail.extend(chunk)
                    if len(runtime.stderr_tail) > MAX_STDERR_TAIL_SIZE:
                        del runtime.stderr_tail[:-MAX_STDERR_TAIL_SIZE]
        except (OSError, ValueError):
            return

    async def _read_stderr(
        self, process: subprocess.Popen[bytes], runtime: RuntimeT | None = None
    ) -> str:
        if runtime is not None:
            thread = runtime.stderr_drain_thread
            deadline = time.monotonic() + 1.0
            while (
                thread is not None and thread.is_alive() and time.monotonic() < deadline
            ):
                await asyncio.sleep(0.01)
            with runtime.stderr_tail_lock:
                return bytes(runtime.stderr_tail).decode("utf-8", errors="replace")
        if process.stderr is None:
            return ""
        stderr_bytes: list[bytes] = []

        def _read_all() -> None:
            stream = cast(Any, process.stderr)
            with contextlib.suppress(OSError, ValueError):
                stderr_bytes.append(bytes(stream.read()))

        reader = threading.Thread(
            target=_read_all, name=f"acp-stderr-fallback-{process.pid}", daemon=True
        )
        reader.start()
        while reader.is_alive():
            await asyncio.sleep(0.01)
        reader.join()
        return (stderr_bytes[0] if stderr_bytes else b"").decode(
            "utf-8", errors="replace"
        )

    async def _terminate_process(self, process: subprocess.Popen[bytes]) -> None:
        if process.poll() is not None:
            return

        if os.name == "nt":
            try:
                result = await asyncio.to_thread(
                    subprocess.run,
                    ["taskkill", "/PID", str(process.pid), "/T", "/F"],
                    capture_output=True,
                    check=False,
                    shell=False,
                )
                if getattr(result, "returncode", 1) != 0 and process.poll() is None:
                    with contextlib.suppress(Exception):
                        process.terminate()
            finally:
                with contextlib.suppress(subprocess.TimeoutExpired):
                    await asyncio.to_thread(lambda: process.wait(timeout=5))
            return

        process.terminate()
        try:
            await asyncio.to_thread(lambda: process.wait(timeout=5))
        except subprocess.TimeoutExpired:
            process.kill()
            with contextlib.suppress(subprocess.TimeoutExpired):
                await asyncio.to_thread(lambda: process.wait(timeout=5))

    def _get_next_message_id(self, runtime: RuntimeT) -> int:
        runtime.message_id += 1
        return runtime.message_id

    async def _write_json_line(
        self, runtime: RuntimeT, payload: dict[str, Any]
    ) -> None:
        process = runtime.process
        if process is None or process.stdin is None:
            raise BackendError(message="ACP process not running")
        encoded = (json.dumps(payload, separators=(",", ":")) + "\n").encode("utf-8")

        def _write() -> None:
            assert process.stdin is not None
            process.stdin.write(encoded)
            process.stdin.flush()

        try:
            await asyncio.wait_for(
                asyncio.to_thread(_write), timeout=self._process_timeout
            )
        except asyncio.TimeoutError as exc:
            # A blocked pipe means the child is no longer making progress.  Tear
            # it down before the caller releases the request lock so a later
            # turn cannot inherit a half-written ACP conversation.
            await self._kill_runtime(runtime)
            raise APITimeoutError(
                message="Timeout writing to ACP process",
                details={"timeout": self._process_timeout, "model": runtime.model},
            ) from exc
        runtime.last_activity = time.monotonic()

    async def _send_jsonrpc_message(
        self, runtime: RuntimeT, method: str, params: dict[str, Any]
    ) -> int:
        message_id = self._get_next_message_id(runtime)
        payload = {
            "jsonrpc": "2.0",
            "method": method,
            "params": params,
            "id": message_id,
        }
        try:
            await self._write_json_line(runtime, payload)
            return message_id
        except APITimeoutError:
            raise
        except Exception as exc:
            raise APIConnectionError(
                message=f"Failed to communicate with ACP process: {exc}"
            ) from exc

    async def _send_jsonrpc_result(
        self, runtime: RuntimeT, request_id: int, result: dict[str, Any]
    ) -> None:
        await self._write_json_line(
            runtime, {"jsonrpc": "2.0", "id": request_id, "result": result}
        )

    async def _read_jsonrpc_message(self, runtime: RuntimeT) -> ACPNotification | None:
        process = runtime.process
        if process is None or process.stdout is None:
            raise BackendError(message="ACP process not running")

        def _read_limited() -> bytes:
            assert process.stdout is not None
            return bytes(process.stdout.readline(MAX_RESPONSE_LINE_SIZE + 1))

        try:
            line = await asyncio.to_thread(_read_limited)
            if not line:
                if process.poll() is not None:
                    stderr = await self._read_stderr(process, runtime)
                    self._cleanup_runtime_state(runtime, process)
                    raise BackendError(
                        message="ACP process exited unexpectedly",
                        details={
                            "stderr": stderr,
                            "project_dir": str(runtime.project_dir),
                            "model": runtime.model,
                        },
                    )
                return None
            if len(line) > MAX_RESPONSE_LINE_SIZE:
                raise BackendError(message="Response too large from ACP process")
            runtime.last_activity = time.monotonic()
            data = json.loads(line.decode("utf-8"))
            if not isinstance(data, dict):
                raise BackendError(message="Invalid non-object JSON response")
            return ACPNotification(**data)
        except json.JSONDecodeError as exc:
            raise BackendError(
                message="Invalid JSON response from ACP process",
                details={"error": str(exc)},
            ) from exc
        except BackendError:
            raise
        except Exception as exc:
            raise APIConnectionError(
                message=f"Failed to read from ACP process: {exc}"
            ) from exc

    async def _await_response(
        self, runtime: RuntimeT, request_id: int
    ) -> ACPNotification:
        deadline = time.monotonic() + self._process_timeout
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise APITimeoutError(
                    message="Timeout waiting for ACP response",
                    details={"timeout": self._process_timeout, "model": runtime.model},
                )

            response = await asyncio.wait_for(
                self._read_jsonrpc_message(runtime), timeout=remaining
            )
            if response is None:
                continue
            deadline = time.monotonic() + self._process_timeout
            if response.is_server_request:
                await self._handle_server_request(runtime, response)
                continue
            if response.id != request_id:
                continue
            return response

    async def _initialize_runtime(self, runtime: RuntimeT) -> None:
        if runtime.initialized and runtime.session_id:
            return
        await self._perform_handshake(runtime)

    def _extract_user_message_as_string(
        self, processed_messages: Sequence[ChatMessage | dict[str, Any] | str | Any]
    ) -> str:
        last_user_message = ""

        for message in processed_messages:
            role = ""
            content: Any = ""

            if isinstance(message, ChatMessage):
                role = message.role
                content = message.content
            elif isinstance(message, dict):
                role = str(message.get("role", ""))
                content = message.get("content")
                if content in (None, "") and "parts" in message:
                    content = message.get("parts")
            elif isinstance(message, str):
                role = "user"
                content = message
            else:
                role = str(getattr(message, "role", ""))
                content = getattr(message, "content", "")

            normalized = self._stringify_message_content(content)
            if role == "user" and normalized:
                last_user_message = normalized

        return last_user_message

    def _stringify_message_content(self, content: Any) -> str:
        if content is None:
            return ""
        if isinstance(content, str):
            return content
        if isinstance(content, Sequence) and not isinstance(
            content, str | bytes | bytearray
        ):
            parts: list[str] = []
            for item in content:
                if isinstance(item, str):
                    parts.append(item)
                    continue
                if isinstance(item, dict):
                    text = item.get("text")
                    nested = item.get("content")
                    if isinstance(text, str):
                        parts.append(text)
                    elif isinstance(nested, str):
                        parts.append(nested)
                else:
                    item_text = getattr(item, "text", None)
                    if isinstance(item_text, str):
                        parts.append(item_text)
            return " ".join(part for part in parts if part)
        return str(content)

    @staticmethod
    def _text_from_acp_content_block(content: Any) -> str | None:
        """Extract human-readable text from a session/update ``content`` block."""
        if isinstance(content, dict):
            td = content.get("textDelta")
            if isinstance(td, str) and td:
                return td
            raw_text = content.get("text")
            if content.get("type") == "text" and isinstance(raw_text, str):
                return raw_text
            try:
                normalized = ACPUpdateContent(**content)
            except Exception:
                return None
            if normalized.type == "text" and isinstance(normalized.text, str):
                return normalized.text
        return None

    @staticmethod
    def _open_thinking_block(text: str) -> str:
        """Open a visible thinking block for clients that cannot segment reasoning."""

        return f"Thinking:\n{text}"

    @staticmethod
    def _append_thinking_block(text: str) -> str:
        """Append incremental text inside an already-open thinking block."""

        return text

    @staticmethod
    def _close_thinking_block() -> str:
        """Close a visible thinking block before ordinary content resumes."""

        return "\n\n"

    def _thinking_content_piece(self, runtime: RuntimeT, text: str) -> AcpStreamPiece:
        if runtime.acp_thinking_block_open:
            return AcpStreamPiece(content=self._append_thinking_block(text))
        runtime.acp_thinking_block_open = True
        return AcpStreamPiece(content=self._open_thinking_block(text))

    def _prepend_thinking_close_if_needed(
        self, runtime: RuntimeT, pieces: list[AcpStreamPiece]
    ) -> list[AcpStreamPiece]:
        if not runtime.acp_thinking_block_open:
            return pieces
        for index, piece in enumerate(pieces):
            if piece.content is None:
                continue
            pieces[index] = AcpStreamPiece(
                content=f"{self._close_thinking_block()}{piece.content}",
                reasoning_content=piece.reasoning_content,
            )
            runtime.acp_thinking_block_open = False
            return pieces
        return pieces

    def _acp_progress_reasoning_line(
        self, session_update_kind: str, update: dict[str, Any]
    ) -> str | None:
        """Build a short progress line for plan/mode style updates (reasoning channel)."""
        if session_update_kind == "plan":
            title = update.get("title")
            if isinstance(title, str) and title.strip():
                return f"[plan] {title.strip()}\n"
            return "[plan]\n"
        if session_update_kind == "current_mode_update":
            mode = update.get("modeId") or update.get("mode")
            if isinstance(mode, str) and mode:
                return f"[mode] {mode}\n"
            return "[mode]\n"
        return None

    def _resolve_tool_stream_key(
        self, runtime: RuntimeT, tc: dict[str, Any], *, for_new_invocation: bool
    ) -> str:
        ck = extract_tool_correlation_key(tc)
        if ck:
            return ck
        if for_new_invocation:
            runtime.acp_anon_tool_seq += 1
            key = f"__anon__:{runtime.acp_anon_tool_seq}"
            runtime.acp_last_anon_stream_key = key
            return key
        if runtime.acp_last_anon_stream_key:
            return runtime.acp_last_anon_stream_key
        runtime.acp_anon_tool_seq += 1
        key = f"__anon__:{runtime.acp_anon_tool_seq}"
        runtime.acp_last_anon_stream_key = key
        return key

    @staticmethod
    def _acp_update_tool_sizes_from_merged(
        acc: AcpToolStreamAccum, merged: dict[str, Any]
    ) -> None:
        inp = extract_tool_input(merged)
        input_bytes = payload_utf8_byte_length(inp)
        if inp is not None and input_bytes >= acc.last_input_bytes:
            acc.last_input = inp
            acc.last_input_bytes = input_bytes
        out = extract_tool_output(merged)
        if out is not None:
            acc.last_output_bytes = max(
                acc.last_output_bytes, payload_utf8_byte_length(out)
            )

    def _ensure_acp_tool_accum(
        self, runtime: RuntimeT, key: str, tool_name: str
    ) -> AcpToolStreamAccum:
        acc = runtime.acp_tool_stream_accum.get(key)
        if acc is None:
            acc = AcpToolStreamAccum(tool_name=tool_name)
            acc.started_wall_iso = utc_now_iso()
            acc.started_perf = time.perf_counter()
            runtime.acp_tool_stream_accum[key] = acc
        elif tool_name and tool_name != "tool":
            acc.tool_name = tool_name
        return acc

    @staticmethod
    def _acp_mark_tool_ended_now(acc: AcpToolStreamAccum) -> None:
        if acc.ended_wall_iso is None:
            acc.ended_wall_iso = utc_now_iso()
        if acc.ended_perf is None:
            acc.ended_perf = time.perf_counter()

    def _acp_try_emit_tool_summary(
        self, acc: AcpToolStreamAccum
    ) -> list[AcpStreamPiece]:
        if acc.summary_emitted or not acc.started_wall_iso:
            return []
        self._acp_mark_tool_ended_now(acc)
        end_wall = acc.ended_wall_iso or utc_now_iso()
        end_perf = acc.ended_perf if acc.ended_perf is not None else time.perf_counter()
        started_perf = acc.started_perf if acc.started_perf > 0 else end_perf
        elapsed = max(0.0, end_perf - started_perf)
        text = format_acp_tool_completion_summary(
            acc.tool_name,
            input_payload=acc.last_input,
            input_bytes=acc.last_input_bytes,
            output_bytes=acc.last_output_bytes,
            started_iso=acc.started_wall_iso,
            ended_iso=end_wall,
            elapsed_s=elapsed,
        )
        acc.summary_emitted = True
        acc.pending_terminal_summary = False
        return [AcpStreamPiece(content=text)]

    def _acp_start_summary_pieces(
        self, acc: AcpToolStreamAccum
    ) -> list[AcpStreamPiece]:
        if acc.start_emitted or acc.summary_emitted or not acc.started_wall_iso:
            return []
        text = format_acp_tool_started_summary(
            acc.tool_name,
            input_payload=acc.last_input,
            input_bytes=acc.last_input_bytes,
            started_iso=acc.started_wall_iso,
        )
        acc.start_emitted = True
        acc.last_heartbeat_perf = (
            acc.started_perf if acc.started_perf > 0 else time.perf_counter()
        )
        return [AcpStreamPiece(content=text)]

    def _acp_in_progress_heartbeat_pieces(
        self, runtime: RuntimeT
    ) -> list[AcpStreamPiece]:
        interval = self._acp_tool_heartbeat_seconds
        if interval <= 0:
            return []
        now = time.perf_counter()
        out: list[AcpStreamPiece] = []
        for acc in runtime.acp_tool_stream_accum.values():
            if acc.summary_emitted or not acc.start_emitted:
                continue
            last = acc.last_heartbeat_perf or acc.started_perf
            if last > 0 and (now - last) < interval:
                continue
            started = acc.started_perf if acc.started_perf > 0 else now
            elapsed = max(0.0, now - started)
            out.append(
                AcpStreamPiece(
                    content=format_acp_tool_heartbeat_line(acc.tool_name, elapsed)
                )
            )
            acc.last_heartbeat_perf = now
        return out

    def _acp_has_in_progress_tools(self, runtime: RuntimeT) -> bool:
        return any(
            acc.start_emitted and not acc.summary_emitted
            for acc in runtime.acp_tool_stream_accum.values()
        )

    def _acp_terminal_summary_pieces(
        self,
        acc: AcpToolStreamAccum,
        merged: dict[str, Any],
        status_str: str | None,
        *,
        allow_defer: bool,
    ) -> list[AcpStreamPiece]:
        pieces: list[AcpStreamPiece] = []
        if (
            acc.pending_terminal_summary
            and not acc.summary_emitted
            and (
                extract_tool_output(merged) is not None
                or acc.last_output_bytes > 0
                or acc.last_input_bytes > 0
            )
        ):
            pieces.extend(self._acp_try_emit_tool_summary(acc))
        if not acc.summary_emitted and is_terminal_tool_status(status_str):
            no_output = (
                extract_tool_output(merged) is None and acc.last_output_bytes == 0
            )
            no_input = extract_tool_input(merged) is None and acc.last_input_bytes == 0
            correlated = extract_tool_correlation_key(merged) is not None
            if allow_defer and no_output and no_input and correlated:
                acc.pending_terminal_summary = True
            else:
                pieces.extend(self._acp_try_emit_tool_summary(acc))
        return pieces

    @staticmethod
    def _acp_tool_call_payload_is_multi_dict_list(upd: dict[str, Any]) -> bool:
        nested = (
            upd.get("toolCall") or upd.get("tool_call") or upd.get("toolInvocation")
        )
        if not isinstance(nested, list):
            return False
        return sum(1 for x in nested if isinstance(x, dict)) > 1

    def _acp_pieces_for_tool_call(
        self, runtime: RuntimeT, upd: dict[str, Any]
    ) -> list[AcpStreamPiece]:
        out: list[AcpStreamPiece] = []
        batch_multi = self._acp_tool_call_payload_is_multi_dict_list(upd)
        for merged in iter_coalesced_acp_tool_session_dicts(upd):
            if not merged or not acp_tool_payload_should_emit(merged):
                continue
            key = self._resolve_tool_stream_key(
                runtime, merged, for_new_invocation=True
            )
            name = extract_tool_name(merged)
            acc = self._ensure_acp_tool_accum(runtime, key, name)
            self._acp_update_tool_sizes_from_merged(acc, merged)
            status_raw = merged.get("status") or merged.get("state")
            status_str = status_raw.strip() if isinstance(status_raw, str) else None
            pieces = self._acp_terminal_summary_pieces(
                # Batched multi-tool payloads lack a stable follow-up edge per item, so
                # empty terminal entries emit immediately instead of waiting for an update.
                acc,
                merged,
                status_str,
                allow_defer=not batch_multi,
            )
            if not pieces and not is_terminal_tool_status(status_str):
                pieces = self._acp_start_summary_pieces(acc)
            out.extend(pieces)
        return out

    def _acp_pieces_for_tool_call_update(
        self, runtime: RuntimeT, upd: dict[str, Any]
    ) -> list[AcpStreamPiece]:
        merged = coalesce_acp_tool_call_update_session_dict(upd)
        if not merged:
            return []
        key = self._resolve_tool_stream_key(runtime, merged, for_new_invocation=False)
        if (
            key not in runtime.acp_tool_stream_accum
            and not acp_tool_payload_should_emit(merged)
        ):
            return []
        name = extract_tool_name(merged)
        acc = self._ensure_acp_tool_accum(runtime, key, name)
        self._acp_update_tool_sizes_from_merged(acc, merged)
        status_raw = merged.get("status") or merged.get("state")
        status_str = status_raw.strip() if isinstance(status_raw, str) else None
        pieces = self._acp_terminal_summary_pieces(
            acc, merged, status_str, allow_defer=True
        )
        if pieces or is_terminal_tool_status(status_str):
            return pieces
        return self._acp_start_summary_pieces(acc)

    def _flush_incomplete_acp_tool_streams(
        self, runtime: RuntimeT
    ) -> list[AcpStreamPiece]:
        """Emit summaries for tools still missing a final summary (incl. deferred terminal)."""
        out: list[AcpStreamPiece] = []
        for _key, acc in list(runtime.acp_tool_stream_accum.items()):
            if acc.summary_emitted or not acc.started_wall_iso:
                continue
            self._acp_mark_tool_ended_now(acc)
            out.extend(self._acp_try_emit_tool_summary(acc))
        return out

    def _session_update_to_stream_pieces(
        self, response: ACPNotification, runtime: RuntimeT
    ) -> list[AcpStreamPiece]:
        """Map a ``session/update`` JSON-RPC notification to zero or more stream pieces."""
        if response.method != ACP_UPDATE_METHOD or response.params is None:
            return []
        try:
            envelope = ACPSessionUpdate(**response.params)
        except Exception:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "ACP session/update params could not be parsed", exc_info=True
                )
            return []
        upd = envelope.update
        kind = upd.get("sessionUpdate")
        if not isinstance(kind, str):
            return []

        text = self._text_from_acp_content_block(upd.get("content"))

        if kind == ACP_AGENT_MESSAGE_CHUNK:
            if text:
                return self._prepend_thinking_close_if_needed(
                    runtime, [AcpStreamPiece(content=text)]
                )
            return []
        if kind == ACP_AGENT_THOUGHT_CHUNK:
            if text:
                return [self._thinking_content_piece(runtime, text)]
            return []
        if kind == "tool_call":
            return self._prepend_thinking_close_if_needed(
                runtime, self._acp_pieces_for_tool_call(runtime, upd)
            )
        if kind == "tool_call_update":
            return self._prepend_thinking_close_if_needed(
                runtime, self._acp_pieces_for_tool_call_update(runtime, upd)
            )

        progress = self._acp_progress_reasoning_line(kind, upd)
        if progress:
            return [self._thinking_content_piece(runtime, progress)]
        return []

    def _session_update_to_stream_piece(
        self, response: ACPNotification, runtime: RuntimeT
    ) -> AcpStreamPiece | None:
        """Back-compat helper merging multiple ``content`` / ``reasoning`` pieces."""
        pieces = self._session_update_to_stream_pieces(response, runtime)
        if not pieces:
            return None
        if len(pieces) == 1:
            return pieces[0]
        content_parts = [p.content for p in pieces if p.content is not None]
        reasoning_parts = [
            p.reasoning_content for p in pieces if p.reasoning_content is not None
        ]
        return AcpStreamPiece(
            content="".join(content_parts) if content_parts else None,
            reasoning_content="".join(reasoning_parts) if reasoning_parts else None,
        )

    async def _iter_acp_stream_pieces(
        self, runtime: RuntimeT, prompt_request_id: int, response_model: str
    ) -> AsyncGenerator[AcpStreamPiece, None]:
        runtime.acp_tool_stream_accum.clear()
        runtime.acp_anon_tool_seq = 0
        runtime.acp_last_anon_stream_key = None
        runtime.acp_thinking_block_open = False
        turn_start_time = time.monotonic()
        piece_count = 0
        deadline = time.monotonic() + self._process_timeout
        read_task: asyncio.Task[ACPNotification | None] | None = None
        rate_limit_attempt = 0
        transient_error_attempt = 0
        try:
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise asyncio.TimeoutError()
                interval = self._acp_tool_heartbeat_seconds
                slice_for_heartbeat = interval > 0 and self._acp_has_in_progress_tools(
                    runtime
                )
                wait_timeout = (
                    min(interval, remaining) if slice_for_heartbeat else remaining
                )

                if runtime.cancellation_event is not None:
                    if read_task is None:
                        read_task = asyncio.create_task(
                            self._read_jsonrpc_message(runtime)
                        )
                    cancel_task = asyncio.create_task(runtime.cancellation_event.wait())
                    try:
                        done, pending = await asyncio.wait(
                            {read_task, cancel_task},
                            return_when=asyncio.FIRST_COMPLETED,
                            timeout=wait_timeout,
                        )
                    except asyncio.CancelledError:
                        read_task.cancel()
                        cancel_task.cancel()
                        with contextlib.suppress(asyncio.CancelledError):
                            await read_task
                        with contextlib.suppress(asyncio.CancelledError):
                            await cancel_task
                        read_task = None
                        raise
                    if cancel_task not in done:
                        cancel_task.cancel()
                        with contextlib.suppress(asyncio.CancelledError):
                            await cancel_task
                    if not done:
                        for piece in self._acp_in_progress_heartbeat_pieces(runtime):
                            if piece.content or piece.reasoning_content:
                                yield piece
                        continue
                    for t in pending:
                        if t is read_task:
                            continue
                        t.cancel()
                        with contextlib.suppress(asyncio.CancelledError):
                            await t

                    if cancel_task in done:
                        if read_task is not None:
                            read_task.cancel()
                            with contextlib.suppress(asyncio.CancelledError):
                                await read_task
                            read_task = None
                        return
                    if read_task is None:
                        continue
                    response = read_task.result()
                    read_task = None
                else:
                    if read_task is None:
                        read_task = asyncio.create_task(
                            self._read_jsonrpc_message(runtime)
                        )
                    try:
                        response = await asyncio.wait_for(
                            asyncio.shield(read_task), timeout=wait_timeout
                        )
                        read_task = None
                    except asyncio.TimeoutError:
                        if time.monotonic() >= deadline:
                            pending_read = read_task
                            if pending_read is not None:
                                pending_read.cancel()
                                with contextlib.suppress(asyncio.CancelledError):
                                    await pending_read
                            read_task = None
                            raise
                        for piece in self._acp_in_progress_heartbeat_pieces(runtime):
                            if piece.content or piece.reasoning_content:
                                yield piece
                        continue

                if response is None:
                    continue

                # Refresh inactivity deadline on every received message from ACP
                deadline = time.monotonic() + self._process_timeout

                if response.is_server_request:
                    await self._handle_server_request(runtime, response)
                    continue

                if response.id == prompt_request_id:
                    if response.is_error and response.error is not None:
                        err_msg = _format_acp_error(response.error)
                        transient_delays = self._acp_transient_error_retry_delays(
                            runtime, response_model, response.error
                        )
                        if (
                            runtime.last_prompt_params is not None
                            and transient_delays
                            and transient_error_attempt < len(transient_delays)
                        ):
                            delay = transient_delays[transient_error_attempt]
                            transient_error_attempt += 1
                            if logger.isEnabledFor(logging.WARNING):
                                logger.warning(
                                    "ACP transient provider error (%s); retrying "
                                    "in %.1fs (attempt %d/%d)...",
                                    err_msg,
                                    delay,
                                    transient_error_attempt,
                                    len(transient_delays),
                                )
                            retry_label = self._acp_transient_retry_label(
                                runtime, response_model
                            )
                            yield AcpStreamPiece(
                                content=(
                                    f"\n\n[{retry_label} is temporarily unavailable "
                                    f"({err_msg}). Retrying in {int(delay)}s "
                                    f"(attempt {transient_error_attempt}/{len(transient_delays)})...]\n\n"
                                )
                            )
                            if runtime.cancellation_event is not None:
                                try:
                                    await asyncio.wait_for(
                                        runtime.cancellation_event.wait(), timeout=delay
                                    )
                                    return
                                except asyncio.TimeoutError:
                                    pass
                            else:
                                await asyncio.sleep(delay)

                            new_params = dict(runtime.last_prompt_params)
                            new_params["messageId"] = str(uuid.uuid4())
                            prompt_request_id = await self._send_jsonrpc_message(
                                runtime, "session/prompt", new_params
                            )
                            deadline = time.monotonic() + self._process_timeout
                            continue

                        delays = self._rate_limit_backoff_delays
                        if (
                            runtime.last_prompt_params is not None
                            and is_rate_limit_error(err_msg)
                            and rate_limit_attempt < len(delays)
                        ):
                            delay = delays[rate_limit_attempt]
                            rate_limit_attempt += 1
                            if logger.isEnabledFor(logging.WARNING):
                                logger.warning(
                                    "ACP process hit rate limit (%s); backing off for %.1fs (attempt %d/%d)...",
                                    err_msg,
                                    delay,
                                    rate_limit_attempt,
                                    len(delays),
                                )
                            yield AcpStreamPiece(
                                content=(
                                    f"\n\n[Rate limit encountered from model provider ({err_msg}). "
                                    f"Backing off for {int(delay)}s before retry (attempt {rate_limit_attempt}/{len(delays)})...]\n\n"
                                )
                            )
                            if runtime.cancellation_event is not None:
                                try:
                                    await asyncio.wait_for(
                                        runtime.cancellation_event.wait(), timeout=delay
                                    )
                                    return
                                except asyncio.TimeoutError:
                                    pass
                            else:
                                await asyncio.sleep(delay)

                            new_params = dict(runtime.last_prompt_params)
                            new_params["messageId"] = str(uuid.uuid4())
                            prompt_request_id = await self._send_jsonrpc_message(
                                runtime, "session/prompt", new_params
                            )
                            deadline = time.monotonic() + self._process_timeout
                            continue

                        is_turn_or_client_error = (
                            response.error.code in (-32600, -32602)
                            or "status error" in err_msg.lower()
                            or "invalid" in err_msg.lower()
                            or "tool" in err_msg.lower()
                            or "prompt" in err_msg.lower()
                            or "schedule" in err_msg.lower()
                            or "permission" in err_msg.lower()
                        )
                        if piece_count > 0:
                            if runtime.acp_thinking_block_open:
                                runtime.acp_thinking_block_open = False
                                yield AcpStreamPiece(
                                    content=self._close_thinking_block()
                                )
                            for flush_piece in self._flush_incomplete_acp_tool_streams(
                                runtime
                            ):
                                if flush_piece.content or flush_piece.reasoning_content:
                                    piece_count += 1
                                    yield flush_piece
                            notice = (
                                "\n\n[ACP backend turn failed after partial output: "
                                f"{err_msg}. The response above may be incomplete.]\n"
                            )
                            yield AcpStreamPiece(content=notice)
                            piece_count += 1
                            if logger.isEnabledFor(logging.WARNING):
                                logger.warning(
                                    "ACP turn failed after %d partial pieces; "
                                    "preserving output and completing with notice "
                                    "(model=%s session_id=%s error=%s)",
                                    piece_count - 1,
                                    runtime.model,
                                    runtime.session_id,
                                    err_msg,
                                )
                            if logger.isEnabledFor(logging.INFO):
                                logger.info(
                                    "ACP prompt turn completed with partial-output "
                                    "error notice: model=%s, session_id=%s, "
                                    "total_pieces=%d, duration=%.2fs",
                                    runtime.model,
                                    runtime.session_id,
                                    piece_count,
                                    time.monotonic() - turn_start_time,
                                )
                            break
                        raise BackendError(
                            message=f"ACP process error: {err_msg}",
                            details=response.error.model_dump(),
                            status_code=400 if is_turn_or_client_error else 500,
                        )
                    if runtime.acp_thinking_block_open:
                        runtime.acp_thinking_block_open = False
                        yield AcpStreamPiece(content=self._close_thinking_block())
                    for flush_piece in self._flush_incomplete_acp_tool_streams(runtime):
                        if flush_piece.content or flush_piece.reasoning_content:
                            piece_count += 1
                            yield flush_piece
                    if logger.isEnabledFor(logging.INFO):
                        logger.info(
                            "ACP prompt turn completed: model=%s, session_id=%s, total_pieces=%d, duration=%.2fs",
                            runtime.model,
                            runtime.session_id,
                            piece_count,
                            time.monotonic() - turn_start_time,
                        )
                    break

                for piece in self._session_update_to_stream_pieces(response, runtime):
                    if piece.content is not None or piece.reasoning_content is not None:
                        piece_count += 1
                        yield piece
        except asyncio.TimeoutError as exc:
            raise APITimeoutError(
                message="Timeout waiting for ACP response",
                details={"timeout": self._process_timeout, "model": response_model},
            ) from exc
        finally:
            if read_task is not None and not read_task.done():
                read_task.cancel()
                with contextlib.suppress(asyncio.CancelledError):
                    await read_task

    async def _iter_stream_pieces(
        self, runtime: RuntimeT, request_id: int, response_model: str
    ) -> AsyncGenerator[AcpStreamPiece, None]:
        """Dispatch to the protocol-specific stream-piece iterator.

        The default delegates to the ACP loop (:meth:`_iter_acp_stream_pieces`).
        Codex overrides to feed :meth:`_iter_codex_stream_pieces` so the shared
        streaming/non-streaming scaffolding (``_stream_response`` /
        ``_collect_non_streaming_response``) works for both protocols without
        re-implementing either.
        """

        async for piece in self._iter_acp_stream_pieces(
            runtime, request_id, response_model
        ):
            yield piece

    async def _collect_non_streaming_response(
        self,
        runtime: RuntimeT,
        requested_model: str,
        turn_request_id: int,
        request: ConnectorChatCompletionsRequest,
    ) -> ResponseEnvelope:
        """Accumulate a non-streaming turn into a :class:`ResponseEnvelope`.

        ACP default: iterate :meth:`_iter_stream_pieces`, join ``content`` /
        ``reasoning_content`` fragments, build a :class:`CanonicalChatResponse`
        with ``finish_reason="stop"``, and attach usage. Codex overrides to map
        the Codex turn ``finish_reason`` (raising on a failed turn).
        """

        fragments: list[str] = []
        reasoning_fragments: list[str] = []
        async for piece in self._iter_stream_pieces(
            runtime, turn_request_id, requested_model
        ):
            if piece.content:
                fragments.append(piece.content)
            if piece.reasoning_content:
                reasoning_fragments.append(piece.reasoning_content)
        full_response = "".join(fragments)
        full_reasoning = "".join(reasoning_fragments) if reasoning_fragments else None
        response = CanonicalChatResponse(
            id=str(uuid.uuid4()),
            object="chat.completion",
            created=int(time.time()),
            model=requested_model,
            choices=[
                ChatCompletionChoice(
                    index=0,
                    message=ChatCompletionChoiceMessage(
                        role="assistant",
                        content=full_response,
                        reasoning_content=full_reasoning,
                    ),
                    finish_reason="stop",
                )
            ],
        )
        envelope = ResponseEnvelope(
            content=response.model_dump(exclude_none=True), headers={}, status_code=200
        )
        return self.ensure_usage_in_response(
            envelope, list(request.processed_messages), requested_model
        )

    def _create_sse_chunk_from_piece(
        self, piece: AcpStreamPiece, model: str, chunk_id: str
    ) -> str | None:
        delta: dict[str, Any] = {}
        if piece.content:
            delta["content"] = piece.content
        if piece.reasoning_content:
            delta["reasoning_content"] = piece.reasoning_content
        if not delta:
            return None
        payload = {
            "id": chunk_id,
            "object": "chat.completion.chunk",
            "created": int(time.time()),
            "model": model,
            "choices": [{"index": 0, "delta": delta, "finish_reason": None}],
        }
        return f"data: {json.dumps(payload)}\n\n"

    @staticmethod
    def _create_sse_done_chunk() -> str:
        return "data: [DONE]\n\n"

    @staticmethod
    def _is_terminal_finish_reason(value: Any) -> bool:
        """Return whether a finish reason terminates an OpenAI stream."""

        return value in {"stop", "length", "content_filter", "tool_calls", "error"}

    @classmethod
    def _stream_chunk_is_terminal(cls, chunk: ProcessedResponse) -> bool:
        """Detect terminal markers before exposing a stream chunk downstream.

        Most providers send a separate ``data: [DONE]`` sentinel, but Codex
        emits its final OpenAI JSON chunk with ``finish_reason="stop"`` and
        downstream converters stop consuming at that chunk.  Detect both
        representations here so the lock-owning wrapper can finish teardown
        before yielding a terminal chunk to the client.
        """

        metadata = chunk.metadata
        if isinstance(metadata, dict):
            if metadata.get("is_done") is True:
                return True
            if cls._is_terminal_finish_reason(metadata.get("finish_reason")):
                return True

        content = chunk.content
        if content == cls._create_sse_done_chunk():
            return True

        payloads: list[Any] = []
        if isinstance(content, dict):
            payloads.append(content)
        else:
            if isinstance(content, bytes | bytearray):
                with contextlib.suppress(UnicodeDecodeError):
                    content = bytes(content).decode("utf-8")
            if isinstance(content, str):
                # Accept both SSE data lines and raw JSON payloads.  Ignore
                # comments/keepalives and malformed data lines; a later
                # well-formed terminal payload still has to release the lock.
                for line in content.splitlines():
                    line = line.strip()
                    if line.startswith("data:"):
                        line = line[5:].strip()
                        if line == "[DONE]":
                            return True
                    elif not line:
                        continue
                    else:
                        continue
                    if not line:
                        continue
                    with contextlib.suppress(json.JSONDecodeError):
                        payloads.append(json.loads(line))
                if not payloads:
                    with contextlib.suppress(json.JSONDecodeError):
                        payloads.append(json.loads(content))

        for payload in payloads:
            if not isinstance(payload, dict):
                continue
            if cls._is_terminal_finish_reason(payload.get("finish_reason")):
                return True
            choices = payload.get("choices")
            if isinstance(choices, list):
                for choice in choices:
                    if isinstance(choice, dict) and cls._is_terminal_finish_reason(
                        choice.get("finish_reason")
                    ):
                        return True
        return False

    async def _stream_response(
        self, runtime: RuntimeT, requested_model: str, prompt_request_id: int
    ) -> AsyncGenerator[ProcessedResponse, None]:
        chunk_id = str(uuid.uuid4())
        async for piece in self._iter_stream_pieces(
            runtime, prompt_request_id, requested_model
        ):
            sse = self._create_sse_chunk_from_piece(piece, requested_model, chunk_id)
            if sse is not None:
                yield ProcessedResponse(content=sse)

        # Emit the terminal chunk only after the protocol iterator has ended.
        # Keeping this out of ``finally`` is important: downstream adapters may
        # stop consuming as soon as they see [DONE].  The lock-owning wrapper
        # must be able to finish its own cleanup before exposing that chunk.
        yield ProcessedResponse(content=self._create_sse_done_chunk())

    async def _compute_history_and_user_message(
        self, runtime: RuntimeT, messages: Sequence[ChatMessage]
    ) -> tuple[str, HistoryState]:
        """Compute the user-message text and resulting history state for a turn.

        Shared divergence-detection body used by both ACP (``session/prompt``)
        and Codex (``turn/start``). On the first turn (``history_state is
        None``) the full Markdown transcript is sent. On detected prefix
        divergence (edit, branch switch, or truncated history) the agent
        subprocess is killed, respawned, and re-handshaked before resending the
        full transcript. On an idempotent retry (same message list as the last
        successful turn) only the last user line is sent. Otherwise an
        append-only tail is shipped. A failed post-respawn handshake kills the
        runtime so the next request respawns a fresh child instead of reusing a
        half-initialized stdio session.
        """

        state = runtime.history_state
        if state is None:
            user_message = ACPTranscriptSerializer.serialize(messages)
            new_history_state = HistoryState(
                message_count=len(messages),
                prefix_hash=self._hash_messages_prefix(messages, len(messages)),
            )
            return user_message, new_history_state

        n = state.message_count
        prefix_hash = state.prefix_hash
        diverged = (
            len(messages) < n or self._hash_messages_prefix(messages, n) != prefix_hash
        )

        # Prefix edit, branch switch, or truncated history vs. what the agent saw.
        if diverged:
            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    "History diverged or shrank; resetting agent process "
                    "(project=%s model=%s client_session=%s)",
                    runtime.project_dir,
                    runtime.model,
                    runtime.client_session_id,
                )
            await self._kill_runtime(runtime)
            await self._spawn_process(runtime)
            try:
                await self._initialize_runtime(runtime)
            except Exception:
                # A failed post-respawn handshake leaves a broken child; kill
                # it so the next request respawns a fresh process instead of
                # reusing the half-initialized stdio session.
                await self._kill_runtime(runtime)
                raise
            user_message = ACPTranscriptSerializer.serialize(messages)
            new_history_state = HistoryState(
                message_count=len(messages),
                prefix_hash=self._hash_messages_prefix(messages, len(messages)),
            )
            return user_message, new_history_state

        # Same message list as the last successful turn (e.g. client retry).
        if len(messages) == n:
            user_message = self._extract_user_message_as_string(messages)
            return user_message, state

        # Append-only: the agent already saw messages[:n]; ship incremental context.
        user_message = ACPTranscriptSerializer.serialize_tail(messages, n)
        if not user_message.strip():
            user_message = self._extract_user_message_as_string(messages)
        new_history_state = HistoryState(
            message_count=len(messages),
            prefix_hash=self._hash_messages_prefix(messages, len(messages)),
        )
        return user_message, new_history_state

    def _fit_prompt_to_transport_limit(
        self,
        runtime: RuntimeT,
        messages: Sequence[ChatMessage],
        user_message: str,
    ) -> str:
        """Allow a connector to bound protocol request text before dispatch."""

        del runtime, messages
        return user_message

    async def _prepare_turn_request_locked(
        self, runtime: RuntimeT, request: ConnectorChatCompletionsRequest
    ) -> tuple[int, str]:
        """Build ``session/prompt`` text and JSON-RPC id under ``runtime.request_lock``.

        ACP default: spawn + handshake, compute the user message and new history
        state via :meth:`_compute_history_and_user_message`, send
        ``session/prompt``, and commit ``history_state`` immediately. Codex
        overrides to send ``turn/start`` and stage ``pending_history_state``
        instead (committed only on a successful ``turn/completed``).
        """

        await self._cancel_stale_kill_timer(runtime)
        await self._spawn_process(runtime)
        await self._initialize_runtime(runtime)

        if self._turn_pacing_delay_seconds > 0 and runtime.last_activity > 0:
            elapsed = time.monotonic() - runtime.last_activity
            if elapsed < self._turn_pacing_delay_seconds:
                await asyncio.sleep(self._turn_pacing_delay_seconds - elapsed)

        messages = list(request.processed_messages)
        if not messages:
            raise BackendError(message="No messages found in request")

        user_message, new_history_state = await self._compute_history_and_user_message(
            runtime, messages
        )
        user_message = self._fit_prompt_to_transport_limit(
            runtime, messages, user_message
        )
        if not user_message:
            raise BackendError(message="No user message found in request")

        requested_model = request.effective_model or add_vendor_prefix(
            runtime.model, self.VENDOR_PREFIX
        )
        prompt_params: dict[str, Any] = {
            "sessionId": runtime.session_id,
            "prompt": [{"type": "text", "text": user_message}],
            "messageId": str(uuid.uuid4()),
        }
        runtime.last_prompt_params = prompt_params
        prompt_request_id = await self._send_jsonrpc_message(
            runtime, "session/prompt", prompt_params
        )
        if logger.isEnabledFor(logging.INFO):
            logger.info(
                "ACP turn prompt dispatched: model=%s, session_id=%s, msg_len=%d, request_id=%d",
                runtime.model,
                runtime.session_id,
                len(user_message),
                prompt_request_id,
            )
        runtime.history_state = new_history_state
        return prompt_request_id, requested_model

    async def _stream_response_with_lock(
        self,
        runtime: RuntimeT,
        requested_model: str,
        prompt_request_id: int,
        request_generation: int | None = None,
    ) -> AsyncGenerator[ProcessedResponse, None]:
        stream_completed = False
        natural_cleanup_done = False
        inner = self._stream_response(runtime, requested_model, prompt_request_id)
        try:
            # Closing the inner generator is essential when a downstream
            # adapter stops at the terminal [DONE] chunk.  Without this,
            # ``_stream_response`` remains suspended immediately after its
            # final yield and this wrapper never reaches teardown reliably.
            async with contextlib.aclosing(inner):
                async for chunk in inner:
                    if self._stream_chunk_is_terminal(chunk):
                        stream_completed = True
                        # A provider terminal JSON chunk (for example Codex's
                        # ``finish_reason="stop"``) is followed by the
                        # canonical ``[DONE]`` sentinel from
                        # ``_stream_response``.  Keep the inner iterator alive
                        # in that case so direct consumers still receive the
                        # sentinel; converter consumers may close the nested
                        # generators immediately after this terminal chunk.
                        if chunk.content == self._create_sse_done_chunk():
                            await inner.aclose()
                        if not natural_cleanup_done:
                            await self._invalidate_active_request_generation(
                                runtime, request_generation
                            )
                            cancellation_in_progress = (
                                runtime.cancellation_event is not None
                                and runtime.cancellation_event.is_set()
                            )
                            if not cancellation_in_progress:
                                if runtime.cancellation_event is not None:
                                    runtime.cancellation_event.clear()
                                await self._schedule_stale_kill_after_turn(runtime)
                                await self._release_runtime_request_lock(runtime)
                            natural_cleanup_done = True
                    yield chunk
                stream_completed = True
        finally:
            if not stream_completed:
                # A downstream consumer can close this generator without the
                # session cancellation coordinator seeing the disconnect. Own
                # teardown here as well; otherwise the per-runtime lock can
                # remain held behind a blocked stdout read forever.
                cancellation_in_progress = (
                    runtime.cancellation_event is not None
                    and runtime.cancellation_event.is_set()
                )
                if not cancellation_in_progress:
                    await self._cancel_active_request(
                        runtime,
                        prompt_request_id,
                        expected_generation=request_generation,
                    )
            elif not natural_cleanup_done:
                # Natural stream end: clear the event, schedule idle stale-kill,
                # and release the request lock so the next turn can acquire it.
                cancellation_in_progress = (
                    runtime.cancellation_event is not None
                    and runtime.cancellation_event.is_set()
                )
                if not cancellation_in_progress:
                    await self._invalidate_active_request_generation(
                        runtime, request_generation
                    )
                    if runtime.cancellation_event is not None:
                        runtime.cancellation_event.clear()
                    await self._schedule_stale_kill_after_turn(runtime)
                    await self._release_runtime_request_lock(runtime)

    async def _invalidate_active_request_generation(
        self, runtime: RuntimeT, request_generation: int | None
    ) -> None:
        """Make a completed stream's cancel callback a no-op before unlock.

        Streaming envelopes can report a client disconnect after the terminal
        ``[DONE]`` chunk has already been delivered.  The callback belongs to
        the completed turn, while the pooled runtime may already be serving a
        newer turn by the time it runs.  Serialize invalidation with active
        cancellation so the old callback cannot kill or unlock the newer turn.
        """

        if request_generation is None:
            return
        cancellation_lock = runtime.cancellation_lock
        if cancellation_lock is None:
            if runtime.active_request_generation == request_generation:
                runtime.active_request_generation = None
            return
        async with cancellation_lock:
            if runtime.active_request_generation == request_generation:
                runtime.active_request_generation = None

    async def _release_runtime_request_lock(self, runtime: RuntimeT) -> None:
        if runtime.request_lock is not None and runtime.request_lock.locked():
            runtime.request_lock.release()
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "ACP request lock released: project=%s model=%s client_session=%s",
                    runtime.project_dir,
                    runtime.model,
                    runtime.client_session_id,
                )

    async def _acquire_runtime_request_lock(self, runtime: RuntimeT) -> None:
        if runtime.request_lock is None:
            raise BackendError(message="ACP runtime is missing request lock")
        started = time.monotonic()
        await runtime.request_lock.acquire()
        waited = time.monotonic() - started
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "ACP request lock acquired: waited=%.3fs project=%s model=%s "
                "client_session=%s",
                waited,
                runtime.project_dir,
                runtime.model,
                runtime.client_session_id,
            )
        if waited >= 1.0:
            logger.warning(
                "ACP request lock waited %.3fs: project=%s model=%s client_session=%s",
                waited,
                runtime.project_dir,
                runtime.model,
                runtime.client_session_id,
            )

    async def _wait_for_process_exit(
        self, process: subprocess.Popen[bytes], timeout_s: float
    ) -> bool:
        if process.poll() is not None:
            return True
        try:
            await asyncio.wait_for(asyncio.to_thread(process.wait), timeout=timeout_s)
            return True
        except asyncio.TimeoutError:
            return False
        except Exception:
            return process.poll() is not None

    async def _attempt_graceful_cancel(
        self, runtime: RuntimeT, request_id: int, total_timeout_s: float
    ) -> bool:
        process = runtime.process
        if process is None or process.poll() is not None:
            return True

        deadline = time.monotonic() + total_timeout_s
        for method in ACP_CANCEL_METHODS:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return False
            try:
                await self._send_jsonrpc_message(
                    runtime,
                    method,
                    {
                        "sessionId": runtime.session_id,
                        "requestId": request_id,
                        "messageId": str(request_id),
                    },
                )
            except Exception:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug("ACP cancel method %s failed or not supported", method)
                continue

            exited = await self._wait_for_process_exit(
                process, timeout_s=min(remaining, 1.5)
            )
            if exited:
                return True

        if process.stdin is not None:
            with contextlib.suppress(OSError, ValueError):
                process.stdin.close()
            remaining = deadline - time.monotonic()
            if remaining > 0 and await self._wait_for_process_exit(
                process, timeout_s=min(remaining, 3.0)
            ):
                return True

        return process.poll() is not None

    def _cancellation_in_progress(self, runtime: RuntimeT) -> bool:
        return bool(
            runtime.cancellation_event is not None
            and runtime.cancellation_event.is_set()
        )

    async def _reset_runtime_after_turn_failure(self, runtime: RuntimeT) -> None:
        """Retire a runtime after a turn fails before a response completes.

        ACP sessions are stateful. Releasing the request lock while retaining a
        child that rejected, timed out, or partially processed the turn allows
        the next request to reuse an unknown protocol/session state. Always
        start the next turn from a clean child instead.
        """

        if self._cancellation_in_progress(runtime):
            return
        try:
            await self._kill_runtime(runtime)
        except Exception:
            # Preserve the original backend error while still clearing all
            # in-memory state if process termination itself is unreliable.
            logger.exception(
                "Failed to retire ACP runtime after turn failure; forcing logical reset "
                "(project=%s model=%s client_session=%s)",
                runtime.project_dir,
                runtime.model,
                runtime.client_session_id,
            )
            self._cleanup_runtime_state(runtime)

    async def _cancel_active_request(
        self,
        runtime: RuntimeT,
        prompt_request_id: int,
        expected_generation: int | None = None,
    ) -> bool:
        """Cancel the request iff it still owns the pooled runtime.

        ``expected_generation`` is supplied by streaming envelopes.  A stale
        callback from a completed stream must not tear down a newer request or
        release its request lock.  Calls without a generation retain the
        unconditional cancellation behavior used by non-streaming requests.
        """

        cancellation_lock = runtime.cancellation_lock
        if cancellation_lock is None:
            if (
                expected_generation is not None
                and runtime.active_request_generation != expected_generation
            ):
                return False
            if runtime.cancellation_event is not None:
                runtime.cancellation_event.set()
            try:
                await self._kill_runtime(runtime)
            finally:
                if (
                    expected_generation is None
                    or runtime.active_request_generation == expected_generation
                ):
                    runtime.active_request_generation = None
                if runtime.cancellation_event is not None:
                    runtime.cancellation_event.clear()
                await self._release_runtime_request_lock(runtime)
            return True

        async with cancellation_lock:
            if (
                expected_generation is not None
                and runtime.active_request_generation != expected_generation
            ):
                return False
            if runtime.cancellation_event is not None:
                runtime.cancellation_event.set()
            try:
                process = runtime.process
                if process is None or process.poll() is not None:
                    # Process already gone (or never attached): still stop the
                    # stderr drain and close any leftover pipe handles so a
                    # later respawn cannot leak FDs or mix diagnostics.
                    self._cleanup_runtime_state(runtime, process)
                    return True

                if logger.isEnabledFor(logging.INFO):
                    logger.info(
                        "Cancelling active ACP request (pid=%s), "
                        "attempting graceful cancellation then process kill",
                        process.pid,
                    )

                graceful_cancelled = await self._attempt_graceful_cancel(
                    runtime, prompt_request_id, ACP_GRACEFUL_CANCEL_TIMEOUT_SECONDS
                )

                if graceful_cancelled:
                    if logger.isEnabledFor(logging.INFO):
                        logger.info(
                            "ACP process (pid=%s) exited gracefully after cancellation",
                            process.pid,
                        )
                    self._cleanup_runtime_state(runtime, process)
                else:
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            "ACP process (pid=%s) did not exit gracefully, force-killing",
                            process.pid,
                        )
                    await self._kill_runtime(runtime)
            finally:
                # Invalidate the callback generation while cancellation_lock is
                # still held.  A stale callback then returns without touching a
                # newer request, and a new request cannot acquire request_lock
                # until the cancellation event has also been cleared.
                if (
                    expected_generation is None
                    or runtime.active_request_generation == expected_generation
                ):
                    runtime.active_request_generation = None
                if runtime.cancellation_event is not None:
                    runtime.cancellation_event.clear()
                # Always release the request lock after teardown (idempotent).
                # This owns the release in the cancellation path so a follow-up
                # request cannot acquire the lock against a half-torn-down
                # subprocess.
                await self._release_runtime_request_lock(runtime)
        return True

    async def chat_completions(  # type: ignore[override]
        self, request: ConnectorChatCompletionsRequest
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        if (
            request.cancellation_coordinator is not None
            and request.cancellation_token is not None
        ):
            request.cancellation_coordinator.ensure_not_cancelled(
                request.cancellation_token
            )
        if not self.is_backend_functional():
            raise ServiceUnavailableError(
                message=f"{self.backend_type} backend not initialized",
                details={"initialization_failed": self._initialization_failed},
            )

        runtime = await self._acquire_runtime(request)
        if runtime.request_lock is None:
            raise BackendError(message="ACP runtime is missing request lock")

        # Streaming holds the per-runtime lock for the full SSE response so idle reap
        # cannot swap the pool entry until the stream completes (see ``_reap_idle_runtime``).
        if bool(getattr(request.request, "stream", False)):
            await self._acquire_runtime_request_lock(runtime)
            try:
                (
                    prompt_request_id,
                    requested_model,
                ) = await self._prepare_turn_request_locked(runtime, request)
            except BaseException:
                await self._reset_runtime_after_turn_failure(runtime)
                if not self._cancellation_in_progress(runtime):
                    await self._release_runtime_request_lock(runtime)
                raise

            runtime.request_generation += 1
            request_generation = runtime.request_generation
            runtime.active_request_generation = request_generation

            async def _cancel_streaming_request() -> None:
                await self._cancel_active_request(
                    runtime, prompt_request_id, expected_generation=request_generation
                )

            stream_id: str | None = getattr(request.request, "session_id", None)
            if not stream_id and request.context is not None:
                stream_id = request.context.session_id

            async def _stream_with_keepalive() -> AsyncIterator[ProcessedResponse]:
                inner = self._stream_response_with_lock(
                    runtime, requested_model, prompt_request_id, request_generation
                )
                async for chunk in wrap_processed_stream_with_idle_keepalive(
                    inner,
                    keepalive_interval=self._resolve_stream_keepalive_interval(),
                    idle_timeout=None,
                    stream_id=stream_id,
                    model_name=requested_model,
                    on_idle_timeout=None,
                ):
                    yield chunk

            return StreamingResponseEnvelope(
                content=_stream_with_keepalive(),
                media_type="text/event-stream",
                headers={},
                cancel_callback=_cancel_streaming_request,
            )

        # Non-streaming: manual acquire + try/finally so the outer finally can
        # gate the lock release on cancellation NOT being in progress. When a
        # cancel callback fires mid-turn, ``_cancel_active_request`` owns
        # teardown and releases the lock in its ``finally`` after the subprocess
        # is fully torn down; releasing here too would let a follow-up request
        # acquire the lock against a half-torn-down child.
        await self._acquire_runtime_request_lock(runtime)
        turn_succeeded = False
        try:
            cancellable_registered = False
            try:
                (
                    prompt_request_id,
                    requested_model,
                ) = await self._prepare_turn_request_locked(runtime, request)
                if (
                    request.cancellation_coordinator is not None
                    and request.cancellation_token is not None
                ):
                    cancellable = _RuntimeCancellable(self, runtime, prompt_request_id)
                    request.cancellation_coordinator.register_cancellable(
                        request.cancellation_token, cancellable
                    )
                    cancellable_registered = True
                response = await self._collect_non_streaming_response(
                    runtime, requested_model, prompt_request_id, request
                )
                turn_succeeded = True
                return response
            except BaseException:
                await self._reset_runtime_after_turn_failure(runtime)
                raise
            finally:
                if (
                    cancellable_registered
                    and request.cancellation_coordinator is not None
                    and request.cancellation_token is not None
                ):
                    request.cancellation_coordinator.cleanup(request.cancellation_token)
        finally:
            cancellation_in_progress = self._cancellation_in_progress(runtime)
            if not cancellation_in_progress:
                # Idempotent: no-op if a cancel callback already released and
                # cleared the event before this finally observed it.
                if turn_succeeded:
                    await self._schedule_stale_kill_after_turn(runtime)
                await self._release_runtime_request_lock(runtime)
            # else: ``_cancel_active_request`` releases after teardown.

    async def shutdown(self) -> None:
        await self._kill_all_runtimes()

    def __del__(self) -> None:
        runtimes = getattr(self, "_runtimes", None)
        if not isinstance(runtimes, dict):
            return
        for runtime in runtimes.values():
            process = getattr(runtime, "process", None)
            if process is None:
                continue
            try:
                if process.poll() is None:
                    if os.name == "nt":
                        subprocess.run(
                            ["taskkill", "/PID", str(process.pid), "/T", "/F"],
                            capture_output=True,
                            check=False,
                            shell=False,
                        )
                    else:
                        process.terminate()
                    with contextlib.suppress(subprocess.TimeoutExpired):
                        process.wait(timeout=5)
            except Exception:
                pass
            finally:
                self._cleanup_process(process)

    async def __aenter__(self) -> BaseAcpConnector[RuntimeT]:
        return self

    async def __aexit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        _ = exc_type, exc_val, exc_tb
        await self.shutdown()
