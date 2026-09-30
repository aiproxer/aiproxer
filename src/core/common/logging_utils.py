"""
Logging utilities for the application.

This module provides utilities for logging, including:
- Performance guards for expensive log operations
- Redaction of sensitive information
- Consistent log level usage
- Enhanced context information
- Optional test/production ``env_tag`` on records (for filters and custom formats)
"""

from __future__ import annotations

# type: ignore[unreachable]
import contextlib
import json
import logging
import os
import re
import sys
import threading
from collections.abc import Callable
from functools import wraps
from typing import TYPE_CHECKING, Any, Literal, TypeVar, cast

if TYPE_CHECKING:
    from src.core.config.app_config import AppConfig

import structlog

from src.core.app.constants.logging_constants import TRACE_LEVEL

# Type variable for generic functions
T = TypeVar("T")

# Track logged security warnings to prevent spam
# Thread-safe: protected by _logged_warnings_lock for concurrent access
_logged_security_warnings: set[str] = set()
_logged_warnings_lock = threading.Lock()


# Environment detection
def _is_running_under_pytest() -> bool:
    """Detect whether the current process is running under pytest."""

    if os.getenv("PYTEST_CURRENT_TEST"):
        return True

    # Check if pytest is actually running (not just imported)
    if "pytest" in sys.modules or "_pytest" in sys.modules:
        # Additional checks to ensure we're actually running under pytest
        # and not just that pytest was imported by the application

        # Check for pytest-specific attributes that indicate a running session
        with contextlib.suppress(Exception):
            # Check if pytest has been configured (indicates running session)
            # This check is sometimes flaky with Pylance, so we'll rely on other indicators.
            # if hasattr(pytest, "config") and pytest.config is not None:
            #     return True
            pass

        # Check for other pytest runtime indicators
        pytest_indicators = [
            "_pytest.config.Config",
            "_pytest.runner.pytest_runtest_call",
            "_pytest.fixtures.fixture",
        ]

        for indicator in pytest_indicators:
            if indicator in sys.modules:
                return True

    return False


def _get_environment_tag() -> str:
    """Get the environment tag for logging.

    Returns:
        'test' if running under pytest, 'prod' otherwise
    """
    return "test" if _is_running_under_pytest() else "prod"


def get_environment_tag() -> str:
    """Public wrapper for environment tag lookup."""

    return _get_environment_tag()


def format_log_pid_short(process: int | None) -> str:
    """Format a process id for log lines: asterisk plus last four decimal digits.

    Used inside ``[pid=...]``, e.g. process ``440852`` -> ``*0852`` so the tag reads
    ``[pid=*0852]``.
    """
    if process is None:
        return "*----"
    try:
        pid_int = int(process)
    except (TypeError, ValueError):
        return "*----"
    return f"*{pid_int % 10000:04d}"


class EnvironmentTaggingFilter(logging.Filter):
    """Logging filter that adds environment tags to log records."""

    def __init__(self) -> None:
        super().__init__()
        self._env_tag = _get_environment_tag()

    def filter(self, record: logging.LogRecord) -> bool:  # type: ignore[override]
        """Add environment tag to log record.

        Args:
            record: The log record to filter

        Returns:
            True to include the record
        """
        record.env_tag = self._env_tag
        return True


class EnvironmentTaggingFormatter(logging.Formatter):
    """Logging formatter with shortened ``pid`` placeholder for log lines.

    Default format: ``YYYY-MM-DD HH:MM:SS,mmm [LEVEL] [pid=*XXXX] name:lineno message``
    where ``*XXXX`` is an asterisk plus the process id modulo 10000 (four digits).
    """

    def __init__(
        self,
        fmt: str | None = None,
        datefmt: str | None = None,
        style: Literal["%", "{", "$"] = "%",
    ) -> None:
        if fmt is None:
            fmt = "%(asctime)s [%(levelname)s] [pid=%(pid_short)s] %(name)s:%(lineno)d %(message)s"
        super().__init__(fmt, datefmt, style=style)

    def format(self, record: logging.LogRecord) -> str:
        """Set ``pid_short`` on the record for the format string."""
        record.pid_short = format_log_pid_short(getattr(record, "process", None))
        return super().format(record)


# Default set of fields to redact
DEFAULT_REDACTED_FIELDS = {
    "api_key",
    "access_token",
    "refresh_token",
    "id_token",
    "password",
    "secret",
    "authorization",
    "credentials",
}


# Regular expressions for redacting sensitive information
# Match common API key prefixes with more specific patterns to reduce false positives:
# - OpenAI keys: sk- followed by specific prefixes like proj-, test-, etc., then 20+ chars
# - Anthropic keys: ak- followed by specific prefixes like ant-, etc., then 20+ chars
# This avoids matching "ak_" sequences in common English words like "tracking", "monitoring"
API_KEY_PATTERN = re.compile(
    r"\b(?:sk[-_](?:proj|test|live|team|org|svc|[A-Za-z0-9])[A-Za-z0-9_-]{15,}|ak-(?:ant|sk|proj)[A-Za-z0-9_-]{17,})\b"
)
# ZAI-style keys: 32 hex chars, dot, 16+ mixed alphanum
ZAI_KEY_PATTERN = re.compile(r"\b[0-9a-f]{32}\.[A-Za-z0-9]{16,}\b")
BEARER_TOKEN_PATTERN = re.compile(r"Bearer\s+([a-zA-Z0-9._~+/-]+=*)")


class CompatibleBoundLogger:
    """Wrapper around structlog logger that adds stdlib logging API compatibility.

    This wrapper adds the `isEnabledFor` method to structlog's BoundLogger,
    providing compatibility with code that uses the standard library logging API.
    """

    def __init__(self, logger: Any):
        """Initialize the compatible logger wrapper.

        Args:
            logger: The underlying structlog logger
        """
        self._logger = logger

    def is_enabled_for(self, level: int) -> bool:
        """Check if logger is enabled for the given level (stdlib compatibility).

        Args:
            level: The logging level to check

        Returns:
            True if the logger is enabled for the given level
        """
        # Try structlog's is_enabled_for method first
        if hasattr(self._logger, "is_enabled_for"):
            return bool(self._logger.is_enabled_for(level))
        # Fall back to stdlib is_enabled_for if available
        if hasattr(self._logger, "is_enabled_for"):
            return bool(self._logger.is_enabled_for(level))
        # Default to True if we can't determine
        return True

    def isEnabledFor(self, level: int) -> bool:  # noqa: N802
        """Check if logger is enabled for the given level (stdlib logging API).

        This is an alias for is_enabled_for to maintain compatibility with
        standard library logging's camelCase method name.

        Args:
            level: The logging level to check

        Returns:
            True if the logger is enabled for the given level
        """
        return self.is_enabled_for(level)

    def __getattr__(self, name: str) -> Any:
        """Delegate all other attribute access to the underlying logger.

        Args:
            name: The attribute name

        Returns:
            The attribute from the underlying logger
        """
        return getattr(self._logger, name)


def get_logger(name: str | None = None) -> CompatibleBoundLogger:
    """Get a structured logger with stdlib compatibility.

    Args:
        name: Optional logger name

    Returns:
        A structured logger with isEnabledFor compatibility
    """
    return CompatibleBoundLogger(structlog.get_logger(name))


def is_log_level_enabled(logger: Any, level: int) -> bool:
    """
    Determine whether the given logger is enabled for the specified level.

    Supports both stdlib loggers (is_enabled_for) and structlog loggers
    (is_enabled_for) to avoid attribute errors during import-time checks.
    """
    check_stdlib = getattr(logger, "is_enabled_for", None)
    if callable(check_stdlib):
        return bool(check_stdlib(level))

    check_structlog = getattr(logger, "is_enabled_for", None)
    if callable(check_structlog):
        return bool(check_structlog(level))

    return False


def truncate_for_debug_log(text: str, *, max_chars: int = 512) -> str:
    """Shorten large strings so DEBUG logs do not dump full prompts or bodies."""

    if len(text) <= max_chars:
        return text
    return f"{text[:max_chars]}... [truncated, total_chars={len(text)}]"


def format_for_debug_log(obj: Any, *, max_chars: int = 512) -> str:
    """Serialize *obj* for DEBUG logs and truncate to *max_chars*."""

    if isinstance(obj, str):
        return truncate_for_debug_log(obj, max_chars=max_chars)
    dumped: Any
    if hasattr(obj, "model_dump") and callable(obj.model_dump):
        dumped = obj.model_dump()
    else:
        dumped = obj
    try:
        text = json.dumps(dumped, default=str, ensure_ascii=True)
    except (TypeError, ValueError):
        text = repr(dumped)
    return truncate_for_debug_log(text, max_chars=max_chars)


def redact(value: str, mask: str = "***") -> str:
    """Redact a sensitive value.

    Args:
        value: The value to redact
        mask: The mask to use

    Returns:
        The redacted value
    """
    if not value:
        return value

    # Keep first and last character
    if len(value) > 6:
        return f"{value[0:2]}{mask}{value[-2:]}"
    else:
        return mask


def redact_dict(
    data: dict[Any, Any], redacted_fields: set[str] | None = None, mask: str = "***"
) -> dict[Any, Any]:
    """Redact sensitive fields in a dictionary.

    Args:
        data: The dictionary to redact
        redacted_fields: The fields to redact
        mask: The mask to use

    Returns:
        The redacted dictionary
    """
    if redacted_fields is None:
        redacted_fields = DEFAULT_REDACTED_FIELDS

    normalized_fields = {field.lower() for field in redacted_fields}

    result: dict[Any, Any] = {}

    for key, value in data.items():
        key_lower = key.lower() if isinstance(key, str) else None

        if key_lower is not None and key_lower in normalized_fields:
            if isinstance(value, str):
                result[key] = redact(value, mask)
            else:
                result[key] = mask
        elif isinstance(value, dict):
            result[key] = redact_dict(value, redacted_fields, mask)
        elif isinstance(value, list):
            result[key] = [
                (
                    redact_dict(item, redacted_fields, mask)
                    if isinstance(item, dict)
                    else item
                )
                for item in value
            ]
        else:
            result[key] = value

    return result


def redact_text(text: str, mask: str = "***") -> str:
    """Redact sensitive information in text.

    Args:
        text: The text to redact
        mask: The mask to use

    Returns:
        The redacted text
    """
    if not text:
        return text

    redacted = text

    # Replace API keys and other credential formats using shared patterns.
    for pattern in (API_KEY_PATTERN, ZAI_KEY_PATTERN):
        redacted = pattern.sub(mask, redacted)

    # Bearer tokens use a captured group so we preserve the scheme prefix.
    redacted = BEARER_TOKEN_PATTERN.sub(f"Bearer {mask}", redacted)

    return redacted


def redact_sensitive_value(value: object | None, mask: str = "***") -> str | None:
    """Redact a sensitive value for safe logging.

    Args:
        value: Potentially sensitive value
        mask: The mask to use

    Returns:
        The redacted value or None
    """
    if value is None:
        return None

    if not isinstance(value, str):
        return redact_text(str(value), mask=mask)

    redacted = redact_text(value, mask=mask)
    if redacted != value:
        return redacted

    return redact(value, mask=mask)


class ApiKeyRedactionFilter(logging.Filter):
    """Logging filter that redacts known API keys from log records.

    This filter will sanitize `record.msg` and `record.args` (if they are
    strings or containers of strings) replacing any discovered API key
    occurrences with a mask.
    """

    def __init__(
        self, api_keys: list[str] | set[str] | None = None, mask: str = "***"
    ) -> None:
        super().__init__()
        self.mask = mask
        keys = set(api_keys or [])
        # Remove falsy/short values: API keys are long, and very short "keys"
        # (e.g., single characters) can cause catastrophic over-redaction in logs.
        keys = {k for k in keys if len(k) >= 8}
        # Build list of compiled patterns: explicit keys and default token patterns
        self.patterns: list[re.Pattern] = []
        if keys:
            # Escape keys for safe regex usage and sort by length desc to prefer longer matches
            escaped = sorted((re.escape(k) for k in keys), key=len, reverse=True)
            try:
                self.patterns.append(re.compile("|".join(escaped)))
            except re.error:
                # Fallback: compile each separately
                for e in escaped:
                    try:
                        self.patterns.append(re.compile(e))
                    except re.error:
                        continue

        # Always include some default generic patterns to cover common token forms
        with contextlib.suppress(Exception):
            self.patterns.append(API_KEY_PATTERN)
        with contextlib.suppress(Exception):
            self.patterns.append(BEARER_TOKEN_PATTERN)
        # Include ZAI key pattern for redaction
        with contextlib.suppress(Exception):
            self.patterns.append(ZAI_KEY_PATTERN)

    # Maximum recursion depth for sanitization to prevent stack overflow
    _MAX_SANITIZE_DEPTH: int = 50

    def _sanitize(
        self, obj: object, *, _depth: int = 0, _seen: set[int] | None = None
    ) -> object:
        """Recursively sanitize strings inside common containers.

        Args:
            obj: Object to sanitize
            _depth: Current recursion depth (internal use)
            _seen: Set of object ids already visited (internal use)

        Returns:
            Sanitized object with API keys redacted
        """
        if not self.patterns:
            return obj

        # Prevent stack overflow from deeply nested structures
        if _depth > self._MAX_SANITIZE_DEPTH:
            return obj

        # Track seen objects to prevent circular reference loops
        if _seen is None:
            _seen = set()

        # Only track mutable containers that could have circular refs
        obj_id = id(obj)
        if isinstance(obj, dict | list):
            if obj_id in _seen:
                return obj  # Circular reference, return as-is
            _seen.add(obj_id)

        if isinstance(obj, str):
            s = obj
            for pat in self.patterns:
                try:
                    # For bearer tokens, replace only the token portion if pattern captures it
                    if pat is BEARER_TOKEN_PATTERN:
                        s = pat.sub(f"Bearer {self.mask}", s)
                    else:
                        s = pat.sub(self.mask, s)
                except (TypeError, ValueError, AttributeError, re.error) as e:
                    if get_logger(__name__).isEnabledFor(logging.DEBUG):
                        get_logger(__name__).debug(
                            "Failed to apply sanitization pattern: %s", e, exc_info=True
                        )
                    continue
            return s
        if isinstance(obj, dict):
            return {
                k: self._sanitize(v, _depth=_depth + 1, _seen=_seen)
                for k, v in obj.items()
            }
        if isinstance(obj, list | tuple):
            sanitized = [self._sanitize(v, _depth=_depth + 1, _seen=_seen) for v in obj]
            return type(obj)(sanitized)
        return obj

    def filter(self, record: logging.LogRecord) -> bool:  # type: ignore[override]
        try:
            if not self.patterns:
                return True

            # Sanitize the message template
            if isinstance(record.msg, str):
                record.msg = self._sanitize(record.msg)  # type: ignore[assignment]

            # Sanitize any args
            if record.args:
                if isinstance(record.args, dict):
                    sanitized_args = self._sanitize(record.args)
                    record.args = cast(dict[str, object], sanitized_args)
                elif isinstance(record.args, tuple):
                    record.args = tuple(self._sanitize(a) for a in record.args)

            # Also attempt to sanitize other common record attributes
            for attr in ("message", "exc_text", "stack_info"):
                val = getattr(record, attr, None)
                if isinstance(val, str):
                    with contextlib.suppress(
                        TypeError, ValueError, AttributeError, re.error
                    ):
                        # Don't fail logging if sanitization of these attributes fails
                        setattr(record, attr, self._sanitize(val))
        except (TypeError, ValueError, AttributeError, re.error, RecursionError) as e:
            if get_logger(__name__).isEnabledFor(logging.WARNING):
                get_logger(__name__).warning(
                    "Logging filter encountered error during sanitization: %s",
                    e,
                    exc_info=True,
                )
            return True
        return True


def install_environment_tagging() -> None:
    """Install environment tagging filter on the root logger and its handlers."""
    try:
        root = logging.getLogger()
        filter_instance = EnvironmentTaggingFilter()

        # Add to root logger
        root.addFilter(filter_instance)

        # Add to existing handlers
        for handler in list(root.handlers):
            try:
                handler.addFilter(filter_instance)
                # Normalize formatter to EnvironmentTaggingFormatter (pid_short, etc.)
                if isinstance(handler.formatter, logging.Formatter):
                    new_formatter = EnvironmentTaggingFormatter(
                        fmt=handler.formatter._fmt, datefmt=handler.formatter.datefmt
                    )
                    handler.setFormatter(new_formatter)
            except Exception as e:
                get_logger(__name__).debug(
                    "Failed to update handler for environment tagging: %s",
                    e,
                    exc_info=True,
                )
                continue
    except Exception as e:
        get_logger(__name__).debug(
            "Failed to install environment tagging filter: %s", e, exc_info=True
        )


def configure_logging_with_environment_tagging(
    level: int = logging.INFO,
    log_format: str | None = None,
    log_file: str | None = None,
    use_colors: bool = False,
    console_stream: str = "stderr",
) -> None:
    """Configure root logging, structlog, env-tag filter, and standard formatters.

    Args:
        level: Logging level
        log_format: Optional log format string
        log_file: Optional log file path
        use_colors: Whether to enable colored output
    """
    if log_format is None:
        log_format = "%(asctime)s [%(levelname)s] [pid=%(pid_short)s] %(name)s:%(lineno)d %(message)s"

    formatter = EnvironmentTaggingFormatter(fmt=log_format)

    # Create handlers
    handlers: list[logging.Handler] = []

    # Console handler
    console_handler: logging.Handler
    stream_name = str(console_stream or "stderr").strip().lower()
    use_stdout = stream_name == "stdout"
    if use_colors:
        try:
            from rich.console import Console
            from rich.logging import RichHandler

            # Use RichHandler for colored output
            # Simplified format for Rich (time/level come from Rich); keep pid + location
            rich_fmt = "[pid=%(pid_short)s] %(name)s:%(lineno)d %(message)s"
            rich_formatter = EnvironmentTaggingFormatter(fmt=rich_fmt)

            console_handler = RichHandler(
                rich_tracebacks=True,
                markup=True,
                show_time=True,
                show_level=True,
                show_path=False,  # We include path in the message format
                log_time_format="[%Y-%m-%d %H:%M:%S]",
                console=Console(stderr=not use_stdout),
            )
            console_handler.setFormatter(rich_formatter)
        except ImportError:
            # Fallback to standard stream handler if rich is not installed
            console_handler = logging.StreamHandler(
                stream=(sys.stdout if use_stdout else sys.stderr)
            )
            console_handler.setFormatter(formatter)
    else:
        # Standard stream handler for plain text
        console_handler = logging.StreamHandler(
            stream=(sys.stdout if use_stdout else sys.stderr)
        )
        console_handler.setFormatter(formatter)

    handlers.append(console_handler)

    # File handler if specified
    if log_file:
        file_handler = logging.FileHandler(log_file, encoding="utf-8")
        file_handler.setFormatter(formatter)
        handlers.append(file_handler)

    # Configure structlog
    structlog_processors: list[Any] = [
        structlog.stdlib.PositionalArgumentsFormatter(),
        structlog.processors.StackInfoRenderer(),
        structlog.processors.format_exc_info,
        structlog.dev.ConsoleRenderer(colors=use_colors),
    ]

    structlog.configure(
        processors=structlog_processors,
        logger_factory=structlog.stdlib.LoggerFactory(),
        wrapper_class=structlog.stdlib.BoundLogger,
        cache_logger_on_first_use=True,
    )

    # Configure root logger
    _configure_root_logger(level, handlers)

    # Suppress noisy third-party loggers even when DEBUG is enabled globally
    # These loggers produce very verbose HTTP/2 and HPACK debugging output
    # that is not useful for normal operation
    logging.getLogger("httpcore.http2").setLevel(logging.WARNING)
    logging.getLogger("hpack.hpack").setLevel(logging.WARNING)
    # Parent covers hpack.table and other HPACK internals (not under hpack.hpack)
    logging.getLogger("hpack").setLevel(logging.WARNING)
    # Also suppress parent httpcore logger to catch any other httpcore sub-loggers
    logging.getLogger("httpcore").setLevel(logging.WARNING)
    # Verbose SSL / client setup lines at DEBUG
    logging.getLogger("httpx").setLevel(logging.WARNING)
    # `websockets` logs every inbound frame at DEBUG (extremely noisy for tool
    # call argument deltas). Enable wire-level logs only at application TRACE.
    _ws_logger = logging.getLogger("websockets")
    if level <= TRACE_LEVEL:
        _ws_logger.setLevel(logging.NOTSET)
    else:
        _ws_logger.setLevel(logging.WARNING)
    # Suppress aiosqlite debug logs - very verbose operation-level logging
    # (connection open/close, cursor operations, query execution details)
    logging.getLogger("aiosqlite").setLevel(logging.INFO)

    # Install environment tagging filter
    install_environment_tagging()


def install_api_key_redaction_filter(
    api_keys: list[str] | set[str] | None, mask: str = "***"
) -> None:
    """Install the API key redaction filter on the root logger and its handlers.

    This function is safe to call multiple times; it will add a filter instance
    which will redact any of the provided API keys from log records.
    """
    try:
        root = logging.getLogger()
        filter_instance = ApiKeyRedactionFilter(api_keys or [], mask=mask)

        # Add to root logger (catches most logging calls)
        root.addFilter(filter_instance)

        # Also add to existing handlers to be defensive
        for handler in list(root.handlers):
            try:
                handler.addFilter(filter_instance)
            except Exception as e:
                # Ignore handlers that cannot accept filters, but log for diagnostics
                get_logger(__name__).debug(
                    "Handler does not support filters: %s", e, exc_info=True
                )
                continue
    except Exception as e:
        # Never propagate logging configuration errors
        get_logger(__name__).debug(
            "Failed to enable API key redaction filter: %s", e, exc_info=True
        )
        return


def _discover_api_keys_from_config_auth(
    config: AppConfig | None, found: set[str]
) -> None:
    """Discover API keys from config.auth.api_keys for redaction purposes.

    SECURITY WARNING: This function is used by the redaction middleware to know
    which API keys to redact from requests and logs. However, API keys should
    NEVER be stored in config files - they should only be set via environment
    variables. This function reads from the in-memory AppConfig object, not
    from files on disk.
    """
    try:
        if config is not None and getattr(config, "auth", None):
            ak = getattr(config.auth, "api_keys", None)
            if ak:
                for k in ak if isinstance(ak, list | tuple) else [ak]:
                    if k:
                        found.add(str(k))
                        # SECURITY WARNING: Log when API keys are found in config
                        # Only log once per session to avoid log spam
                        warn_key = "auth.api_keys"
                        with _logged_warnings_lock:
                            if warn_key not in _logged_security_warnings:
                                logger = get_logger(__name__)
                                logger.warning(
                                    "SECURITY WARNING: API key found in config.auth.api_keys. "
                                    "API keys should only be set via environment variables, not config files."
                                )
                                _logged_security_warnings.add(warn_key)
    except Exception as e:
        # Suppress errors to ensure logging continues; add debug context
        get_logger(__name__).debug(
            "Error discovering API keys from config.auth: %s", e, exc_info=True
        )


def _discover_api_keys_from_config_backends(
    config: AppConfig | None, found: set[str]
) -> None:
    """Discover API keys from config.backends.<backend>.api_key for redaction purposes.

    SECURITY WARNING: This function is used by the redaction middleware to know
    which API keys to redact from requests and logs. However, API keys should
    NEVER be stored in config files - they should only be set via environment
    variables. This function reads from the in-memory AppConfig object, not
    from files on disk.
    """
    try:
        if config is not None and getattr(config, "backends", None):
            backends = config.backends
            # Attempt to get registry to discover backend names
            try:
                from src.core.services.backend_registry import backend_registry

                registered = backend_registry.get_registered_backends()
            except Exception as e:
                get_logger(__name__).debug(
                    "Backend registry discovery failed: %s", e, exc_info=True
                )
                registered = []

            # Iterate over registered backends and pull api_key fields
            for b in registered:
                try:
                    bcfg = getattr(backends, b, None)
                    if bcfg is None:
                        # Hyphenated backend names (e.g. qwen-oauth) live in
                        # __pydantic_extra__ and cannot be reached via getattr.
                        named = (
                            backends.get_named_backend_configs()
                            if hasattr(backends, "get_named_backend_configs")
                            else {}
                        )
                        bcfg = named.get(b)
                    if bcfg is None:
                        continue
                    ak = getattr(bcfg, "api_key", None)
                    if ak:
                        # Map backend names to environment variables (handle exceptions)
                        backend_env_map = {
                            "kimi-code": "KIMI_API_KEY",
                            "internlm": "INTERNAI_API_KEY",
                            "opencode-go": "OPENCODE_GO_API_KEY",
                            "zai-coding-plan": "ZAI_CODING_PLAN_API_KEY",
                        }
                        env_var = backend_env_map.get(
                            b, f"{b.upper().replace('-', '_')}_API_KEY"
                        )

                        from src.core.common.env_utils import (
                            get_env_value_with_windows_persistent_fallback,
                        )

                        env_val, _ = get_env_value_with_windows_persistent_fallback(
                            env_var
                        )
                        process_env_val = os.getenv(env_var)

                        if isinstance(ak, list | tuple):
                            for k in ak:
                                if k:
                                    found.add(str(k))
                                    if str(k) == env_val or str(k) == process_env_val:
                                        continue

                                    warn_key = f"backends.{b}.api_key"
                                    with _logged_warnings_lock:
                                        if warn_key not in _logged_security_warnings:
                                            logger = get_logger(__name__)
                                            logger.warning(
                                                f"SECURITY WARNING: API key found in config.backends.{b}.api_key. "
                                                "API keys should only be set via environment variables, not config files."
                                            )
                                            _logged_security_warnings.add(warn_key)
                        else:
                            found.add(str(ak))
                            if str(ak) == env_val or str(ak) == process_env_val:
                                continue

                            warn_key = f"backends.{b}.api_key"
                            with _logged_warnings_lock:
                                if warn_key not in _logged_security_warnings:
                                    logger = get_logger(__name__)
                                    logger.warning(
                                        f"SECURITY WARNING: API key found in config.backends.{b}.api_key. "
                                        "API keys should only be set via environment variables, not config files."
                                    )
                                    _logged_security_warnings.add(warn_key)
                except Exception as e:
                    # If backend attribute is missing or malformed, skip
                    get_logger(__name__).debug(
                        "Skipping malformed backend config: %s", e, exc_info=True
                    )
                    continue
    except Exception as e:
        # Suppress errors to ensure logging continues
        get_logger().warning(
            "Failed to discover API keys from config backends: %s", e, exc_info=True
        )


def _discover_api_keys_from_environment(found: set[str]) -> None:
    """Scan environment variables for API keys."""
    # More targeted and efficient environment scan
    api_key_vars = [
        "OPENROUTER_API_KEY",
        "GEMINI_API_KEY",
        "ANTHROPIC_API_KEY",
        "ZAI_API_KEY",
        "ZAI_CODING_PLAN_API_KEY",
        "MINIMAX_API_KEY",
        "OLLAMA_API_KEY",
        "KIMI_API_KEY",
        "OPENCODE_GO_API_KEY",
        "INTERNAI_API_KEY",
        "LLM_INTERACTIVE_PROXY_API_KEY",
        "OPENAI_API_KEY",
        "AUTH_TOKEN",
    ]
    for var in api_key_vars:
        if key := os.getenv(var):
            found.add(key)

    # Also scan for numbered API keys, e.g., GEMINI_API_KEY_1, INTERNAI_API_KEY_1
    for i in range(1, 21):
        if key := os.getenv(f"GEMINI_API_KEY_{i}"):
            found.add(key)
        if key := os.getenv(f"INTERNAI_API_KEY_{i}"):
            found.add(key)
        if key := os.getenv(f"OPENCODE_GO_API_KEY_{i}"):
            found.add(key)


def discover_api_keys_from_config_and_env(config: AppConfig | None = None) -> list[str]:
    """Discover API keys from both in-memory config and environment variables for redaction.

    SECURITY NOTICE: This function reads API keys from the in-memory AppConfig object
    (NOT from files on disk) for redaction purposes. API keys should NEVER be stored
    in config files - they should only be set via environment variables.

    The function scans:
    1. In-memory AppConfig object for redaction middleware
    2. Environment variables for all API keys

    Required environment variables:
    - OPENROUTER_API_KEY for OpenRouter
    - GEMINI_API_KEY for Gemini
    - ANTHROPIC_API_KEY for Anthropic
    - ZAI_API_KEY for ZAI
    - ZAI_CODING_PLAN_API_KEY for ZAI Coding Plan
    - MINIMAX_API_KEY for Minimax
    - LLM_INTERACTIVE_PROXY_API_KEY for proxy authentication

    SECURITY WARNING: If API keys are found in the config object, warnings are logged.
    """
    found: set[str] = set()

    # Read from in-memory config for redaction (with security warnings)
    _discover_api_keys_from_config_auth(config, found)
    _discover_api_keys_from_config_backends(config, found)

    # Always read from environment variables
    _discover_api_keys_from_environment(found)

    return list(found)


def _configure_root_logger(level: int, handlers: list[logging.Handler]) -> None:
    """Configure the root logger with the specified level and handlers."""
    # Get the root logger
    root_logger = logging.getLogger()

    # Set the logging level
    root_logger.setLevel(level)

    # Remove any existing handlers to prevent duplicate logs
    for handler in root_logger.handlers[:]:
        root_logger.removeHandler(handler)

    # Add the new handlers
    for handler in handlers:
        root_logger.addHandler(handler)


def log_call(
    level: int = logging.INFO,
) -> Callable[[Callable[..., T]], Callable[..., T]]:
    """Decorator to log function calls.

    Args:
        level: The log level to use

    Returns:
        A decorator function

    Notes:
        Intentional extension hook. This utility is provided for teams wanting
        lightweight call logging without introducing cross-cutting concerns in
        business code. It is safe to keep even if not used everywhere.
    """

    def decorator(func: Callable[..., T]) -> Callable[..., T]:
        logger = get_logger(func.__module__)

        @wraps(func)
        def wrapper(*args: Any, **kwargs: Any) -> T:
            if logger.is_enabled_for(level):
                logger.log(
                    level,
                    f"Calling {func.__name__}",
                    function=func.__name__,
                    module=func.__module__,
                )

            result = func(*args, **kwargs)

            if logger.is_enabled_for(level):
                logger.log(
                    level,
                    f"Finished {func.__name__}",
                    function=func.__name__,
                    module=func.__module__,
                )

            return result

        return cast(Callable[..., T], wrapper)

    return decorator


def log_async_call(
    level: int = logging.INFO,
) -> Callable[[Callable[..., T]], Callable[..., T]]:
    """Decorator to log async function calls.

    Args:
        level: The log level to use

    Returns:
        A decorator function

    Notes:
        Intentional extension hook. Async counterpart of log_call used where
        structured timing/trace logs are useful. Kept as a public helper.
    """

    def decorator(func: Callable[..., T]) -> Callable[..., T]:
        logger = get_logger(func.__module__)

        @wraps(func)
        async def wrapper(*args: Any, **kwargs: Any) -> T:
            if logger.is_enabled_for(level):
                logger.log(
                    level,
                    f"Calling {func.__name__}",
                    function=func.__name__,
                    module=func.__module__,
                )

            # Check if func is a coroutine function
            import asyncio

            if asyncio.iscoroutinefunction(func):
                result = await func(*args, **kwargs)
            else:
                result = func(*args, **kwargs)

            if logger.is_enabled_for(level):
                logger.log(
                    level,
                    f"Finished {func.__name__}",
                    function=func.__name__,
                    module=func.__module__,
                )

            return cast(T, result)

        return cast(Callable[..., T], wrapper)

    return decorator


class LogContext:
    """Context manager for adding context to logs."""

    def __init__(self, logger: CompatibleBoundLogger, **context: Any):
        """Initialize the context manager.

        Args:
            logger: The logger to use
            **context: The context to add
        """
        self.logger = logger
        self.context = context
        self.bound_logger: Any = None

    def __enter__(self) -> Any:
        """Enter the context.

        Returns:
            The bound logger
        """
        self.bound_logger = self.logger.bind(**self.context)
        return self.bound_logger

    def __exit__(self, *args: Any) -> None:
        """Exit the context."""
        # args contains (exc_type, exc_val, exc_tb) but they are not needed in this implementation
        self.bound_logger = None

    def get_logger(self) -> Any:
        """Get the bound logger.

        Returns:
            The bound logger
        """
        if self.bound_logger is None:
            raise RuntimeError(
                "Logger not bound. Use this context manager in a with statement."
            )
        return self.bound_logger
