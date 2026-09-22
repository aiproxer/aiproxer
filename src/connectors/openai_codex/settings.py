"""Settings loader for OpenAI Codex connector.

This module normalizes connector configuration with precedence:
CLI > ENV > YAML (app config), while preserving defaults.
"""

from __future__ import annotations

import json
import logging
import os
from collections.abc import Mapping
from dataclasses import asdict
from typing import Any

from src.connectors._openai_codex_capabilities import CodexClientCapabilities
from src.connectors.openai_codex.catalog.config import (
    apply_model_catalog_env_overrides,
    codex_model_catalog_config_from_mapping,
)
from src.connectors.openai_codex.contracts import CodexConnectorSettings
from src.connectors.openai_codex.early_session_verbosity_bump import (
    normalize_early_session_verbosity_bump,
)
from src.connectors.openai_codex.interfaces import ISettingsLoader
from src.connectors.openai_codex.managed_oauth_constants import (
    DEFAULT_ALLOW_LEGACY_FALLBACK,
    DEFAULT_REFRESH_BUFFER_SECONDS,
    DEFAULT_SELECTION_STRATEGY,
    DEFAULT_SESSION_AFFINITY_MAX_ENTRIES,
    DEFAULT_SESSION_AFFINITY_TTL_SECONDS,
    DEFAULT_STORAGE_PATH,
)
from src.connectors.openai_codex.utils import (
    coerce_float_sequence,
    coerce_positive_int,
    load_json_env,
    to_mapping,
    to_string_list,
)
from src.core.app.constants.logging_constants import TRACE_LEVEL
from src.core.config.app_config import AppConfig
from src.core.services.tool_text_renderer import configure_renderer_registry

logger = logging.getLogger(__name__)


class SettingsLoader(ISettingsLoader):
    """Service for loading and normalizing Codex connector settings."""

    def __init__(
        self,
        *,
        backend_yaml_attr: str = "openai_codex",
        backend_registry_lookup: str = "openai-codex",
        default_websocket_enabled: bool = False,
        default_websocket_beta_mode: str = "v1",
    ) -> None:
        """Configure which ``AppConfig.backends`` entry backs this loader.

        ``openai-codex-v2`` uses a parallel YAML key / registry name with different
        websocket defaults while reusing the same ``extra.codex`` parsing logic.
        """
        self._backend_yaml_attr = backend_yaml_attr
        self._backend_registry_lookup = backend_registry_lookup
        self._default_websocket_enabled = default_websocket_enabled
        self._default_websocket_beta_mode = default_websocket_beta_mode

    def load(self, app_config: AppConfig) -> CodexConnectorSettings:  # noqa: C901
        settings: dict[str, Any] = {
            # Default to client-supplied tools only.
            #
            # This backend is typically used by external agents (not Codex CLI),
            # and exposing Codex CLI-style built-in tools (e.g. apply_patch/shell)
            # by default can cause clients to receive tool calls they cannot execute.
            "default_capabilities": CodexClientCapabilities(
                tool_schema_mode="custom_only",
                bypass_tool_call_reactor=True,
                include_environment_context=False,
            ),
            "agent_overrides": {},
            "renderer": {
                "default": "none",
                "fallback": "summary",
                "aliases": {},
                "modules": {},
            },
            "prompt": {
                "template": None,
                "prepend": [],
                "append": [],
                "deduplicate": True,
                "fallback_to_default": True,
            },
            "tool_schema": {
                "base_tools": None,
                "custom_tools": [],
            },
            "streaming": {
                "max_retries": 2,
                "retry_backoff_seconds": (0.5, 1.5, 3.0),
            },
            "websocket": {
                "enabled": self._default_websocket_enabled,
                "beta_mode": self._default_websocket_beta_mode,
            },
            "managed_oauth": {
                "enabled": True,
                "storage_path": DEFAULT_STORAGE_PATH,
                "accounts": "all",
                "selection_strategy": DEFAULT_SELECTION_STRATEGY,
                "refresh_buffer_seconds": DEFAULT_REFRESH_BUFFER_SECONDS,
                "session_affinity_ttl_seconds": DEFAULT_SESSION_AFFINITY_TTL_SECONDS,
                "session_affinity_max_entries": DEFAULT_SESSION_AFFINITY_MAX_ENTRIES,
                "allow_legacy_fallback": DEFAULT_ALLOW_LEGACY_FALLBACK,
                "quota_remaining_alerts_enabled": True,
                "quota_remaining_alert_thresholds_percent": [25.0, 10.0],
            },
            "compatibility_layer": {
                "enabled": False,
                "detection": {
                    "cache_ttl_seconds": 3600,
                    "heuristic_threshold": 2,
                },
                "translation": {
                    "max_tool_execution_timeout": 30,
                    "result_format": "kilo_standard",
                },
                "telemetry": {
                    "log_translations": True,
                    "log_detection": True,
                    "emit_metrics": True,
                },
            },
        }

        backend_config = getattr(app_config.backends, self._backend_yaml_attr, None)
        if backend_config is None and hasattr(app_config.backends, "lookup"):
            backend_config = app_config.backends.lookup(self._backend_registry_lookup)
        backend_extra: dict[str, Any] = {}
        if backend_config and hasattr(backend_config, "extra"):
            try:
                extra_candidate = backend_config.extra
                if isinstance(extra_candidate, Mapping):
                    backend_extra = dict(extra_candidate)
            except (TypeError, ValueError) as e:
                if logger.isEnabledFor(TRACE_LEVEL):
                    logger.log(
                        TRACE_LEVEL,
                        "Failed to extract backend extra config: %s (type=%s)",
                        str(e),
                        type(e).__name__,
                        exc_info=True,
                    )
                backend_extra = {}

        codex_cfg = to_mapping(backend_extra.get("codex")) or {}

        # Default capabilities
        for override_source in (
            codex_cfg.get("default_capabilities"),
            load_json_env("OPENAI_CODEX_DEFAULT_CAPABILITIES"),
        ):
            mapping = to_mapping(override_source)
            if mapping:
                settings["default_capabilities"] = settings[
                    "default_capabilities"
                ].merge(mapping)

        # Agent overrides
        combined_agent_overrides: dict[str, dict[str, Any]] = {}
        for source in (
            codex_cfg.get("agent_capabilities"),
            load_json_env("OPENAI_CODEX_AGENT_CAPABILITIES"),
        ):
            mapping = to_mapping(source)
            if not mapping:
                continue
            for raw_agent, caps in mapping.items():
                if not isinstance(raw_agent, str):  # type: ignore[unreachable]
                    continue
                agent_key = raw_agent.strip().lower()
                if not agent_key:
                    continue
                cap_mapping = to_mapping(caps)
                if not cap_mapping:
                    continue
                combined_agent_overrides.setdefault(agent_key, {}).update(cap_mapping)
        settings["agent_overrides"] = combined_agent_overrides

        # Renderer configuration
        renderer_cfg = to_mapping(codex_cfg.get("renderer")) or {}
        renderer_aliases = to_mapping(renderer_cfg.get("aliases")) or {}
        renderer_modules = to_mapping(renderer_cfg.get("modules")) or {}
        env_renderer_aliases = (
            to_mapping(load_json_env("OPENAI_CODEX_RENDERER_ALIASES") or {}) or {}
        )
        env_renderer_modules = (
            to_mapping(load_json_env("OPENAI_CODEX_RENDERER_MODULES") or {}) or {}
        )

        env_renderer_default = os.getenv("OPENAI_CODEX_RENDERER_DEFAULT")
        env_renderer_fallback = os.getenv("OPENAI_CODEX_RENDERER_FALLBACK")
        renderer_default = (
            env_renderer_default
            if env_renderer_default is not None
            else renderer_cfg.get("default")
        )
        renderer_fallback = (
            env_renderer_fallback
            if env_renderer_fallback is not None
            else renderer_cfg.get("fallback")
        )
        renderer_default = (renderer_default or "none").strip() or "none"
        renderer_fallback = (renderer_fallback or "summary").strip() or "summary"

        # Prompt configuration
        prompt_cfg = to_mapping(codex_cfg.get("prompt")) or {}
        prompt_template = prompt_cfg.get("template") or os.getenv(
            "OPENAI_CODEX_PROMPT_TEMPLATE"
        )
        prepend_sections = to_string_list(prompt_cfg.get("prepend")) + to_string_list(
            load_json_env("OPENAI_CODEX_PROMPT_PREPEND")
        )
        append_sections = to_string_list(prompt_cfg.get("append")) + to_string_list(
            load_json_env("OPENAI_CODEX_PROMPT_APPEND")
        )
        prompt_deduplicate_env = os.getenv("OPENAI_CODEX_PROMPT_DEDUPLICATE")
        if prompt_deduplicate_env is not None:
            prompt_deduplicate = prompt_deduplicate_env.strip().lower() in {
                "1",
                "true",
                "yes",
                "on",
            }
        else:
            prompt_deduplicate = bool(prompt_cfg.get("deduplicate", True))
        fallback_to_default_env = os.getenv("OPENAI_CODEX_PROMPT_FALLBACK_DEFAULT")
        if fallback_to_default_env is not None:
            fallback_to_default = fallback_to_default_env.strip().lower() in {
                "1",
                "true",
                "yes",
                "on",
            }
        else:
            fallback_to_default = bool(prompt_cfg.get("fallback_to_default", True))

        settings["prompt"].update(
            {
                "template": (
                    prompt_template.strip()
                    if isinstance(prompt_template, str)
                    else None
                ),
                "prepend": prepend_sections,
                "append": append_sections,
                "deduplicate": prompt_deduplicate,
                "fallback_to_default": fallback_to_default,
            }
        )

        # Tool schema configuration
        tool_schema_cfg = to_mapping(codex_cfg.get("tool_schema")) or {}
        # base_tools should be stored as raw dicts (Codex format), not ToolDefinition objects
        base_tools_raw = tool_schema_cfg.get("base_tools") or load_json_env(
            "OPENAI_CODEX_TOOL_SCHEMA_BASE"
        )
        base_tools: list[dict[str, Any]] | None = None
        if base_tools_raw:
            # Convert to list of dicts, preserving Codex format
            if isinstance(base_tools_raw, list):
                base_tools = []
                for tool in base_tools_raw:
                    if isinstance(tool, dict | Mapping):
                        base_tools.append(dict(tool))
                    elif hasattr(tool, "model_dump") and callable(tool.model_dump):
                        base_tools.append(tool.model_dump())  # type: ignore[attr-defined]
                    else:
                        base_tools.append(
                            dict(tool) if isinstance(tool, Mapping) else {}
                        )
            elif isinstance(base_tools_raw, dict | Mapping):
                base_tools = [dict(base_tools_raw)]

        # custom_tools should also be stored as raw dicts (Codex format)
        custom_tools_raw = tool_schema_cfg.get("custom_tools") or load_json_env(
            "OPENAI_CODEX_TOOL_SCHEMA_CUSTOM"
        )
        custom_tools_dicts: list[dict[str, Any]] = []
        if custom_tools_raw:
            # Convert to list of dicts, preserving Codex format
            if isinstance(custom_tools_raw, list):
                for tool in custom_tools_raw:
                    if isinstance(tool, dict | Mapping):
                        tool_dict = dict(tool)
                        # Validate tool schema: must have a non-empty "name" field
                        tool_name = tool_dict.get("name")
                        if isinstance(tool_name, str) and tool_name.strip():
                            custom_tools_dicts.append(tool_dict)
                        # Skip tools without name or with empty name
                    elif hasattr(tool, "model_dump"):
                        tool_dict = tool.model_dump()
                        tool_name = tool_dict.get("name")
                        if isinstance(tool_name, str) and tool_name.strip():
                            custom_tools_dicts.append(tool_dict)
            elif isinstance(custom_tools_raw, dict | Mapping):
                tool_dict = dict(custom_tools_raw)
                tool_name = tool_dict.get("name")
                if isinstance(tool_name, str) and tool_name.strip():
                    custom_tools_dicts = [tool_dict]

        settings["tool_schema"].update(
            {
                "base_tools": base_tools,
                "custom_tools": custom_tools_dicts,
            }
        )

        # Configure renderer registry after aliases/modules are ready
        combined_aliases_raw: dict[Any, Any] = {}
        combined_aliases_raw.update(renderer_aliases)
        combined_aliases_raw.update(env_renderer_aliases)
        combined_modules_raw: dict[Any, Any] = {}
        combined_modules_raw.update(renderer_modules)
        combined_modules_raw.update(env_renderer_modules)

        sanitized_aliases: dict[str, str] = {}
        for alias, target in combined_aliases_raw.items():
            if not isinstance(alias, str) or not isinstance(target, str):
                continue
            alias_key = alias.strip()
            target_key = target.strip()
            if alias_key and target_key:
                sanitized_aliases[alias_key] = target_key

        sanitized_modules: dict[str, str] = {}
        for name, dotted_path in combined_modules_raw.items():
            if not isinstance(name, str) or not isinstance(dotted_path, str):
                continue
            renderer_name = name.strip()
            path_value = dotted_path.strip()
            if renderer_name and path_value:
                sanitized_modules[renderer_name] = path_value

        try:
            configure_renderer_registry(
                aliases=sanitized_aliases or None,
                modules=sanitized_modules or None,
                default=renderer_default,
                fallback=renderer_fallback,
            )
        except Exception as exc:
            logger.warning(
                "Failed to configure tool text renderer registry: %s",
                exc,
                exc_info=True,
            )

        settings["renderer"].update(
            {
                "default": renderer_default,
                "fallback": renderer_fallback,
                "aliases": sanitized_aliases,
                "modules": sanitized_modules,
            }
        )

        if settings["default_capabilities"].tool_text_format in {
            None,
            "none",
        } and renderer_default not in {None, "", "none"}:
            settings["default_capabilities"] = settings["default_capabilities"].merge(
                {"tool_text_format": renderer_default}
            )

        # Streaming settings
        streaming_cfg = to_mapping(codex_cfg.get("streaming")) or {}
        max_retries = coerce_positive_int(streaming_cfg.get("max_retries"))
        env_max_retries = os.getenv("OPENAI_CODEX_STREAMING_MAX_RETRIES")
        if env_max_retries is not None:
            max_retries_env = coerce_positive_int(env_max_retries)
            if max_retries_env is not None:
                max_retries = max_retries_env
        if max_retries is None:
            max_retries = settings["streaming"]["max_retries"]

        backoff_seq = (
            coerce_float_sequence(streaming_cfg.get("retry_backoff_seconds"))
            or settings["streaming"]["retry_backoff_seconds"]
        )
        env_backoff = os.getenv("OPENAI_CODEX_STREAMING_RETRY_BACKOFF")
        if env_backoff:
            maybe_env_backoff = coerce_float_sequence(env_backoff)
            if maybe_env_backoff:
                backoff_seq = maybe_env_backoff

        if not backoff_seq:
            backoff_seq = (0.5, 1.5, 3.0)

        settings["streaming"] = {
            "max_retries": max_retries,
            "retry_backoff_seconds": tuple(backoff_seq),
        }

        # WebSocket settings
        websocket_cfg = to_mapping(codex_cfg.get("websocket")) or {}
        ws_enabled = websocket_cfg.get("enabled")
        env_ws_enabled = os.getenv("OPENAI_CODEX_WEBSOCKET_ENABLED")
        if env_ws_enabled is not None:
            ws_enabled = env_ws_enabled.strip().lower() in {
                "1",
                "true",
                "yes",
                "on",
            }
        elif ws_enabled is None:
            ws_enabled = settings["websocket"]["enabled"]

        ws_beta_mode = websocket_cfg.get("beta_mode")
        if ws_beta_mode is not None and isinstance(ws_beta_mode, str):
            ws_beta_mode = ws_beta_mode.strip().lower()
        if ws_beta_mode not in ("v1", "v2"):
            ws_beta_mode = settings["websocket"].get("beta_mode", "v1")

        settings["websocket"] = {
            "enabled": bool(ws_enabled),
            "beta_mode": ws_beta_mode,
        }

        # Managed OAuth settings
        managed_cfg = to_mapping(codex_cfg.get("managed_oauth")) or {}
        truthy = {"1", "true", "yes", "on"}
        selection_strategies = {
            "round-robin",
            "random",
            "first-available",
            "session-affinity",
        }

        managed_enabled = managed_cfg.get("enabled")
        env_managed_enabled = os.getenv("OPENAI_CODEX_MANAGED_OAUTH_ENABLED")
        if env_managed_enabled is not None:
            managed_enabled = env_managed_enabled.strip().lower() in truthy
        elif managed_enabled is None:
            managed_enabled = settings["managed_oauth"]["enabled"]

        storage_path = (
            os.getenv("OPENAI_CODEX_MANAGED_OAUTH_STORAGE_PATH")
            or managed_cfg.get("storage_path")
            or settings["managed_oauth"]["storage_path"]
        )
        storage_path = (
            storage_path.strip()
            if isinstance(storage_path, str) and storage_path.strip()
            else settings["managed_oauth"]["storage_path"]
        )

        raw_accounts_source: Any = managed_cfg.get("accounts")
        env_accounts_json = load_json_env("OPENAI_CODEX_MANAGED_OAUTH_ACCOUNTS")
        env_accounts_raw = os.getenv("OPENAI_CODEX_MANAGED_OAUTH_ACCOUNTS")
        if env_accounts_json is not None:
            raw_accounts_source = env_accounts_json
        elif env_accounts_raw is not None:
            raw_accounts_source = env_accounts_raw

        accounts: list[str] | str = "all"
        if isinstance(raw_accounts_source, str):
            normalized = raw_accounts_source.strip()
            if normalized and normalized.lower() != "all":
                if normalized.startswith("["):
                    parsed = load_json_env("OPENAI_CODEX_MANAGED_OAUTH_ACCOUNTS")
                    if isinstance(parsed, list):
                        accounts = to_string_list(parsed)
                    else:
                        accounts = [
                            part.strip()
                            for part in normalized.split(",")
                            if part.strip()
                        ]
                else:
                    accounts = [
                        part.strip() for part in normalized.split(",") if part.strip()
                    ]
        elif isinstance(raw_accounts_source, list):
            accounts = to_string_list(raw_accounts_source)
        elif raw_accounts_source == "all":
            accounts = "all"

        selection_strategy_raw = (
            os.getenv("OPENAI_CODEX_MANAGED_OAUTH_SELECTION_STRATEGY")
            or managed_cfg.get("selection_strategy")
            or settings["managed_oauth"]["selection_strategy"]
        )
        selection_strategy = (
            selection_strategy_raw.strip().lower()
            if isinstance(selection_strategy_raw, str)
            else settings["managed_oauth"]["selection_strategy"]
        )
        if selection_strategy not in selection_strategies:
            selection_strategy = settings["managed_oauth"]["selection_strategy"]

        refresh_buffer = coerce_positive_int(
            os.getenv("OPENAI_CODEX_MANAGED_OAUTH_REFRESH_BUFFER_SECONDS")
        )
        if refresh_buffer is None:
            refresh_buffer = coerce_positive_int(
                managed_cfg.get("refresh_buffer_seconds")
            )
        if refresh_buffer is None:
            refresh_buffer = settings["managed_oauth"]["refresh_buffer_seconds"]

        affinity_ttl = coerce_positive_int(
            os.getenv("OPENAI_CODEX_MANAGED_OAUTH_SESSION_AFFINITY_TTL_SECONDS")
        )
        if affinity_ttl is None:
            affinity_ttl = coerce_positive_int(
                managed_cfg.get("session_affinity_ttl_seconds")
            )
        if affinity_ttl is None:
            affinity_ttl = settings["managed_oauth"]["session_affinity_ttl_seconds"]

        affinity_max = coerce_positive_int(
            os.getenv("OPENAI_CODEX_MANAGED_OAUTH_SESSION_AFFINITY_MAX_ENTRIES")
        )
        if affinity_max is None:
            affinity_max = coerce_positive_int(
                managed_cfg.get("session_affinity_max_entries")
            )
        if affinity_max is None:
            affinity_max = settings["managed_oauth"]["session_affinity_max_entries"]

        allow_legacy_fallback = managed_cfg.get("allow_legacy_fallback")
        env_allow_fallback = os.getenv(
            "OPENAI_CODEX_MANAGED_OAUTH_ALLOW_LEGACY_FALLBACK"
        )
        if env_allow_fallback is not None:
            allow_legacy_fallback = env_allow_fallback.strip().lower() in truthy
        elif allow_legacy_fallback is None:
            allow_legacy_fallback = settings["managed_oauth"]["allow_legacy_fallback"]

        quota_remaining_alerts_enabled = managed_cfg.get(
            "quota_remaining_alerts_enabled"
        )
        env_quota_alerts = os.getenv("OPENAI_CODEX_QUOTA_REMAINING_ALERTS_ENABLED")
        if env_quota_alerts is not None:
            quota_remaining_alerts_enabled = env_quota_alerts.strip().lower() in truthy
        elif quota_remaining_alerts_enabled is None:
            quota_remaining_alerts_enabled = settings["managed_oauth"][
                "quota_remaining_alerts_enabled"
            ]

        quota_thresholds_raw: Any = managed_cfg.get(
            "quota_remaining_alert_thresholds_percent"
        )
        env_thresholds_parsed = load_json_env("OPENAI_CODEX_QUOTA_REMAINING_THRESHOLDS")
        if isinstance(env_thresholds_parsed, list):
            quota_thresholds_raw = env_thresholds_parsed
        else:
            env_thresholds_plain = os.getenv("OPENAI_CODEX_QUOTA_REMAINING_THRESHOLDS")
            if isinstance(env_thresholds_plain, str) and env_thresholds_plain.strip():
                quota_thresholds_raw = env_thresholds_plain.strip()

        default_thresholds: list[float] = list(
            settings["managed_oauth"]["quota_remaining_alert_thresholds_percent"]
        )
        quota_thresholds: list[float] = list(default_thresholds)
        if isinstance(quota_thresholds_raw, list):
            parsed_list: list[float] = []
            for x in quota_thresholds_raw:
                if isinstance(x, int | float):
                    try:
                        parsed_list.append(float(x))
                    except (TypeError, ValueError):
                        continue
            if parsed_list:
                quota_thresholds = parsed_list
        elif isinstance(quota_thresholds_raw, str):
            stripped = quota_thresholds_raw.strip()
            if stripped.startswith("["):
                try:
                    loaded = json.loads(stripped)
                except json.JSONDecodeError:
                    loaded = None
                if isinstance(loaded, list):
                    parsed_bracket: list[float] = []
                    for x in loaded:
                        if isinstance(x, int | float):
                            try:
                                parsed_bracket.append(float(x))
                            except (TypeError, ValueError):
                                continue
                    if parsed_bracket:
                        quota_thresholds = parsed_bracket
            elif stripped:
                parts = [p.strip() for p in stripped.split(",") if p.strip()]
                parsed_csv: list[float] = []
                for p in parts:
                    try:
                        parsed_csv.append(float(p))
                    except ValueError:
                        continue
                if parsed_csv:
                    quota_thresholds = parsed_csv

        settings["managed_oauth"] = {
            "enabled": bool(managed_enabled),
            "storage_path": storage_path,
            "accounts": accounts if accounts else "all",
            "selection_strategy": selection_strategy,
            "refresh_buffer_seconds": refresh_buffer,
            "session_affinity_ttl_seconds": affinity_ttl,
            "session_affinity_max_entries": affinity_max,
            "allow_legacy_fallback": bool(allow_legacy_fallback),
            "quota_remaining_alerts_enabled": bool(quota_remaining_alerts_enabled),
            "quota_remaining_alert_thresholds_percent": quota_thresholds,
        }

        # Compatibility layer settings
        compat_cfg = to_mapping(codex_cfg.get("compatibility_layer")) or {}

        compat_enabled = compat_cfg.get("enabled")
        env_compat_enabled = os.getenv("OPENAI_CODEX_COMPATIBILITY_LAYER_ENABLED")
        if env_compat_enabled is not None:
            compat_enabled = env_compat_enabled.strip().lower() in {
                "1",
                "true",
                "yes",
                "on",
            }
        elif compat_enabled is None:
            compat_enabled = settings["compatibility_layer"]["enabled"]

        detection_cfg = to_mapping(compat_cfg.get("detection")) or {}
        cache_ttl = coerce_positive_int(detection_cfg.get("cache_ttl_seconds"))
        if cache_ttl is None:
            cache_ttl = settings["compatibility_layer"]["detection"][
                "cache_ttl_seconds"
            ]

        heuristic_threshold = coerce_positive_int(
            detection_cfg.get("heuristic_threshold")
        )
        if heuristic_threshold is None:
            heuristic_threshold = settings["compatibility_layer"]["detection"][
                "heuristic_threshold"
            ]

        translation_cfg = to_mapping(compat_cfg.get("translation")) or {}
        max_timeout = coerce_positive_int(
            translation_cfg.get("max_tool_execution_timeout")
        )
        if max_timeout is None:
            max_timeout = settings["compatibility_layer"]["translation"][
                "max_tool_execution_timeout"
            ]

        result_format = (
            translation_cfg.get("result_format")
            or settings["compatibility_layer"]["translation"]["result_format"]
        )

        telemetry_cfg = to_mapping(compat_cfg.get("telemetry")) or {}
        log_translations = telemetry_cfg.get("log_translations")
        if log_translations is None:
            log_translations = settings["compatibility_layer"]["telemetry"][
                "log_translations"
            ]

        log_detection = telemetry_cfg.get("log_detection")
        if log_detection is None:
            log_detection = settings["compatibility_layer"]["telemetry"][
                "log_detection"
            ]

        emit_metrics = telemetry_cfg.get("emit_metrics")
        if emit_metrics is None:
            emit_metrics = settings["compatibility_layer"]["telemetry"]["emit_metrics"]

        settings["compatibility_layer"] = {
            "enabled": bool(compat_enabled),
            "detection": {
                "cache_ttl_seconds": cache_ttl,
                "heuristic_threshold": heuristic_threshold,
            },
            "translation": {
                "max_tool_execution_timeout": max_timeout,
                "result_format": str(result_format),
            },
            "telemetry": {
                "log_translations": bool(log_translations),
                "log_detection": bool(log_detection),
                "emit_metrics": bool(emit_metrics),
            },
        }

        gpt55_base: dict[str, Any] = {
            "enabled": True,
            "proactive_enabled": True,
            "reactive_enabled": True,
            "source_model": "gpt-5.5",
            "target_model": "gpt-5.4",
            "free_plan_types": ["free"],
        }
        gpt55_yaml = (
            to_mapping(codex_cfg.get("gpt55_unsupported_free_plan_downgrade")) or {}
        )
        settings["gpt55_unsupported_free_plan_downgrade"] = {**gpt55_base, **gpt55_yaml}

        settings["early_session_verbosity_bump"] = (
            normalize_early_session_verbosity_bump(
                to_mapping(codex_cfg.get("early_session_verbosity_bump"))
            )
        )

        # Model catalog auto-discovery + fallback settings
        # (extra.codex.model_catalog; ENV overrides per-key, shared with the
        # startup discovery stage).
        model_catalog_yaml = to_mapping(codex_cfg.get("model_catalog")) or {}
        model_catalog_merged = apply_model_catalog_env_overrides(model_catalog_yaml)
        model_catalog_config = codex_model_catalog_config_from_mapping(
            model_catalog_merged
        )
        settings["model_catalog"] = asdict(model_catalog_config)

        if logger.isEnabledFor(TRACE_LEVEL):
            logger.log(
                TRACE_LEVEL,
                "Codex connector settings loaded: default_capabilities=%s, renderer_default=%s, renderer_fallback=%s",
                settings["default_capabilities"].to_dict(),
                renderer_default,
                renderer_fallback,
            )

        return CodexConnectorSettings(**settings)
