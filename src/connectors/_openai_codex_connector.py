r"""
OpenAI Codex connector using managed OAuth accounts with legacy fallback.

Primary mode uses managed OAuth account files maintained by the proxy
(`var/openai_codex_oauth_accounts` by default), including account selection,
refresh, and rotation. If no managed accounts are configured, the connector can
fall back to Codex CLI `auth.json` tokens.

Default legacy credential file locations (first that exists is used):
- Windows: %USERPROFILE%\.codex\auth.json
- Cross-platform: ~/.codex/auth.json

Configuration:
- `openai_codex_path`: optional directory that contains legacy `auth.json` for
  discovery; managed OAuth accounts in ``managed_oauth.storage_path`` still load
  first when present.
- `openai_api_base_url`: optional base URL override (default: https://api.openai.com/v1)
"""

from __future__ import annotations

import asyncio
import contextlib
import inspect
import json
import logging
import os
import sys
import threading
import time
import uuid
from collections.abc import AsyncIterator, Awaitable, Callable, Mapping, Sequence
from copy import deepcopy
from pathlib import Path
from typing import TYPE_CHECKING, Any, cast

import httpx
from fastapi import HTTPException
from pydantic import ValidationError
from watchdog.observers import Observer

if TYPE_CHECKING:
    from watchdog.observers.api import BaseObserver

from src.connectors._openai_codex_capabilities import (
    CodexCapabilityResolver,
    CodexClientCapabilities,
)
from src.connectors._openai_codex_kilo_tool_translator import KiloToolTranslator
from src.connectors._openai_codex_request_translator import CodexRequestTranslator
from src.connectors._openai_codex_session_detector import SessionDetector
from src.connectors.base import add_vendor_prefix, strip_vendor_prefix
from src.connectors.contracts import (
    ConnectorChatCompletionsRequest,
    ConnectorRequestContext,
)
from src.connectors.openai import OpenAIConnector
from src.connectors.openai_codex.catalog.fallback_loader import (
    CodexCatalogFallbackLoader,
)
from src.connectors.openai_codex.catalog.interfaces import ICodexModelCatalog
from src.connectors.openai_codex.compat import CompatibilityLayer
from src.connectors.openai_codex.continuation import (
    InMemoryCodexContinuationCoordinator,
)
from src.connectors.openai_codex.contracts import (
    CodexConnectorDependencies,
    CodexConnectorSettings,
    CodexPayload,
    CodexRequestContext,
    ProcessedMessage,
    ToolExecutionResult,
)
from src.connectors.openai_codex.credentials import (
    CredentialManager,
    OpenAICredentialsFileHandler,
)
from src.connectors.openai_codex.executor import ResponseExecutor
from src.connectors.openai_codex.gpt55_account_compatibility import (
    gpt55_config_from_mapping,
)
from src.connectors.openai_codex.managed_oauth_constants import DEFAULT_STORAGE_PATH
from src.connectors.openai_codex.managed_oauth_models import ManagedOAuthConfig
from src.connectors.openai_codex.payload import PayloadBuilder
from src.connectors.openai_codex.prompt import PromptResolver
from src.connectors.openai_codex.request_translator import RequestTranslator
from src.connectors.openai_codex.settings import SettingsLoader
from src.connectors.openai_codex.tool_schema import ToolSchemaResolver
from src.connectors.openai_codex.tool_schemas import get_codex_tool_schema
from src.connectors.openai_codex.tools import ToolExecutionService
from src.connectors.openai_codex.utils import build_codex_user_agent, message_to_text
from src.core.app.constants.logging_constants import TRACE_LEVEL
from src.core.common.exceptions import (
    AuthenticationError,
    BackendError,
    InvalidRequestError,
    ServiceResolutionError,
)
from src.core.common.model_catalog import BackendModelEnumeration
from src.core.config.app_config import AppConfig, BackendConfig
from src.core.domain.model_utils import (
    RESOLVED_URI_PARAMS_EXTRA_BODY_KEY,
    parse_model_with_params,
)
from src.core.domain.responses import (
    ResponseEnvelope,
    StreamingResponseEnvelope,
)
from src.core.domain.validation import ValidationResult
from src.core.services.backend_registry import backend_registry
from src.core.services.translation_service import TranslationService

logger = logging.getLogger(__name__)

# Vendor prefix for OpenAI models in unified model naming convention
OPENAI_VENDOR_PREFIX = "openai"


class OpenAICodexConnector(OpenAIConnector):
    backend_type: str = "openai-codex"

    @property
    def has_static_credentials(self) -> bool:
        return False

    # The model catalog is resolved at instance construction (from DI, else the
    # shipped fallback snapshot) — see ``self._catalog``. The read-only
    # properties below expose the legacy class-attribute surface so existing
    # call sites (and ``payload.py``) keep working without hardcoded slugs.
    @property
    def SUPPORTED_CODEX_MODELS(self) -> tuple[str, ...]:  # noqa: N802
        return self._catalog.routable_slugs()

    @property
    def REASONING_EFFORT_LEVELS(self) -> tuple[str, ...]:  # noqa: N802
        return self._catalog.reasoning_effort_order

    @property
    def DEFAULT_REASONING_EFFORT(self) -> str:  # noqa: N802
        return self._catalog.default_reasoning_effort

    CODEX_PROMPT_RESOURCE_PACKAGE = "src.resources.codex"
    CODEX_PROMPT_RESOURCE_NAME = "gpt_5_codex_prompt.md"
    CODEX_ORIGINATOR = "codex_cli_rs"
    CODEX_VERSION_HEADER = "0.0.0"

    def __init__(
        self,
        client: httpx.AsyncClient,
        config: AppConfig,
        response_processor: Any | None = None,
        translation_service: TranslationService | None = None,
        dependencies: CodexConnectorDependencies | None = None,
    ) -> None:
        # Detect swapped positional args (backend factory passes translation_service)
        if translation_service is None and isinstance(
            response_processor, TranslationService
        ):
            translation_service = response_processor
            response_processor = None

        super().__init__(
            client=client,
            config=config,
            translation_service=translation_service,
            response_processor=response_processor,
        )
        self.disable_health_check()
        self.name = "openai-codex"

        self._working_directory: str | None = None

        # Credentials and validation state
        self.is_functional = False
        self._credential_validation_errors: list[str] = []
        self._initialization_failed = False
        self._last_validation_time = 0.0

        # File watcher state
        self._file_observer_ref: BaseObserver | None = None
        self._event_loop: asyncio.AbstractEventLoop | None = None
        self._pending_reload_task: asyncio.Future[None] | None = None
        self._reload_scheduling_event = threading.Event()
        self._reload_task_lock = threading.Lock()
        self._shutdown_requested = threading.Event()
        self._usage_window_warmup_credentials_lock = asyncio.Lock()

        # Dependency resolution
        self._dependencies = dependencies or self._resolve_dependencies()

        # Validate dependency overrides before use
        if self._dependencies:
            self._validate_dependencies(self._dependencies)

        # Resolve the model catalog (DI discovery result, else shipped fallback).
        self._catalog: ICodexModelCatalog = self._resolve_model_catalog()

        self._settings_loader = (
            self._dependencies.settings_loader
            if self._dependencies and self._dependencies.settings_loader is not None
            else SettingsLoader()
        )
        self._connector_settings_model = self._settings_loader.load(config)
        self._connector_settings = self._connector_settings_model.model_dump()

        self._default_capabilities = self._connector_settings_model.default_capabilities
        self._renderer_default = self._connector_settings["renderer"]["default"]
        self._renderer_fallback = self._connector_settings["renderer"]["fallback"]
        self._prompt_settings = self._connector_settings["prompt"]
        self._default_tool_schema_override = self._connector_settings["tool_schema"][
            "base_tools"
        ]
        self._custom_tool_schema_default = self._connector_settings["tool_schema"][
            "custom_tools"
        ]

        self._capability_resolver = CodexCapabilityResolver(
            default_capabilities=self._default_capabilities,
            agent_overrides=self._connector_settings_model.agent_overrides,
        )

        # Credential manager
        self._credential_manager = (
            self._dependencies.credential_manager
            if self._dependencies and self._dependencies.credential_manager is not None
            else CredentialManager(self.client)
        )
        self._configure_managed_oauth_credential_manager()
        self._codex_quota_persist_tasks: set[asyncio.Task[Any]] = set()

        # Compatibility and tool execution
        # Avoid bool(MagicMock)==True if tests monkeypatch settings/DI.
        compat_cfg = getattr(
            self._connector_settings_model, "compatibility_layer", None
        )
        enabled_val = getattr(compat_cfg, "enabled", False)
        self._compatibility_layer_enabled = (
            enabled_val if isinstance(enabled_val, bool) else False
        )

        self._session_detector: SessionDetector | None = None
        if self._compatibility_layer_enabled:
            detection_cfg = getattr(compat_cfg, "detection", None)
            cache_ttl_val = getattr(detection_cfg, "cache_ttl_seconds", 3600)
            threshold_val = getattr(detection_cfg, "heuristic_threshold", 2)
            cache_ttl_seconds = (
                cache_ttl_val if isinstance(cache_ttl_val, int) else 3600
            )
            heuristic_threshold = threshold_val if isinstance(threshold_val, int) else 2
            self._session_detector = SessionDetector(
                cache_ttl_seconds=cache_ttl_seconds,
                heuristic_threshold=heuristic_threshold,
            )

        self._kilo_tool_translator: KiloToolTranslator | None = None
        if self._compatibility_layer_enabled:
            self._kilo_tool_translator = KiloToolTranslator(self, None)

        self._tool_execution_service = (
            self._dependencies.tool_execution_service
            if self._dependencies
            and self._dependencies.tool_execution_service is not None
            else ToolExecutionService()
        )
        self._continuation_coordinator = InMemoryCodexContinuationCoordinator()

        self._compatibility_layer = (
            self._dependencies.compatibility_layer
            if self._dependencies and self._dependencies.compatibility_layer is not None
            else CompatibilityLayer(
                session_detector=cast(Any, self._session_detector),
                kilo_translator=cast(Any, self._kilo_tool_translator),
                tool_execution_service=self._tool_execution_service,
            )
        )

        # Request/payload components
        self._request_translator = CodexRequestTranslator(self)
        self._request_translator_adapter = RequestTranslator(self._request_translator)
        self._prompt_resolver = PromptResolver()
        self._tool_schema_resolver = ToolSchemaResolver(
            settings=self._connector_settings_model,
            tool_execution_service=self._tool_execution_service,
        )

        self._payload_builder = (
            self._dependencies.payload_builder
            if self._dependencies and self._dependencies.payload_builder is not None
            else PayloadBuilder(
                connector=self,
                request_translator=self._request_translator_adapter,
                prompt_resolver=self._prompt_resolver,
                tool_schema_resolver=self._tool_schema_resolver,
                settings=self._connector_settings_model,
                message_to_text_converter=self._message_to_text,
            )
        )

        # Get retry config from normalized settings
        streaming_cfg = self._connector_settings.get("streaming", {})
        max_retries = int(streaming_cfg.get("max_retries", 2))
        backoff_seq = streaming_cfg.get("retry_backoff_seconds") or ()
        retry_backoff_seconds = tuple(backoff_seq) if backoff_seq else (0.5, 1.5, 3.0)

        self._response_executor = (
            self._dependencies.response_executor
            if self._dependencies and self._dependencies.response_executor is not None
            else self._create_default_response_executor(
                max_retries=max_retries,
                retry_backoff_seconds=retry_backoff_seconds,
            )
        )

    def _create_default_response_executor(
        self,
        *,
        max_retries: int,
        retry_backoff_seconds: tuple[float, ...],
    ) -> ResponseExecutor:
        websocket_cfg = self._connector_settings.get("websocket", {})
        use_websocket = bool(websocket_cfg.get("enabled", False))
        ws_beta = str(websocket_cfg.get("beta_mode") or "v1").strip().lower()
        if ws_beta not in ("v1", "v2"):
            ws_beta = "v1"
        gpt55_cfg = gpt55_config_from_mapping(
            self._connector_settings.get("gpt55_unsupported_free_plan_downgrade")
        )
        return ResponseExecutor(
            base_connector=self,
            credential_manager=self._credential_manager,
            max_retries=max_retries,
            retry_backoff_seconds=retry_backoff_seconds,
            compatibility_layer=self._compatibility_layer,
            continuation_coordinator=self._continuation_coordinator,
            use_websocket=use_websocket,
            websocket_beta_mode=ws_beta,
            connector_transport_backend=self.backend_type,
            continuation_backend_label=self.backend_type,
            gpt55_free_plan_downgrade=gpt55_cfg,
        )

    def _validate_dependencies(self, dependencies: CodexConnectorDependencies) -> None:
        """Validate that dependency overrides implement expected interfaces.

        Raises ServiceResolutionError with clear message if any override is invalid.

        Args:
            dependencies: Dependency overrides to validate

        Raises:
            ServiceResolutionError: If any override does not implement expected interface
        """
        from src.connectors.openai_codex.interfaces import (
            ICompatibilityLayer,
            ICredentialManager,
            IPayloadBuilder,
            IResponseExecutor,
            ISettingsLoader,
            IToolExecutionService,
        )

        errors: list[str] = []

        if dependencies.settings_loader is not None and not isinstance(
            dependencies.settings_loader, ISettingsLoader
        ):
            # Check if it has the required method
            loader_type = type(dependencies.settings_loader)
            if "MagicMock" in loader_type.__name__:
                # MagicMock creates attributes dynamically, check instance
                if not hasattr(dependencies.settings_loader, "load") or not callable(
                    getattr(dependencies.settings_loader, "load", None)
                ):
                    errors.append(
                        f"settings_loader override must implement ISettingsLoader interface "
                        f"(got {loader_type.__name__}, missing or non-callable 'load' method)"
                    )
            elif not hasattr(loader_type, "load"):
                errors.append(
                    f"settings_loader override must implement ISettingsLoader interface "
                    f"(got {loader_type.__name__}, missing 'load' method)"
                )

        if dependencies.credential_manager is not None and not isinstance(
            dependencies.credential_manager, ICredentialManager
        ):
            # Check if it has the required methods
            manager_type = type(dependencies.credential_manager)
            required_methods = [
                "initialize",
                "refresh_access_token",
                "get_access_token",
                "shutdown",
            ]
            if "MagicMock" in manager_type.__name__:
                # MagicMock creates attributes dynamically, check instance
                missing_methods = [
                    method
                    for method in required_methods
                    if not hasattr(dependencies.credential_manager, method)
                    or not callable(
                        getattr(dependencies.credential_manager, method, None)
                    )
                ]
            else:
                missing_methods = [
                    method
                    for method in required_methods
                    if not hasattr(manager_type, method)
                ]
            if missing_methods:
                errors.append(
                    f"credential_manager override must implement ICredentialManager interface "
                    f"(got {manager_type.__name__}, missing methods: {', '.join(missing_methods)})"
                )

        if dependencies.payload_builder is not None and not isinstance(
            dependencies.payload_builder, IPayloadBuilder
        ):
            builder_type = type(dependencies.payload_builder)
            if "MagicMock" in builder_type.__name__:
                if not hasattr(
                    dependencies.payload_builder, "build_payload"
                ) or not callable(
                    getattr(dependencies.payload_builder, "build_payload", None)
                ):
                    errors.append(
                        f"payload_builder override must implement IPayloadBuilder interface "
                        f"(got {builder_type.__name__}, missing or non-callable 'build_payload' method)"
                    )
            elif not hasattr(builder_type, "build_payload"):
                errors.append(
                    f"payload_builder override must implement IPayloadBuilder interface "
                    f"(got {builder_type.__name__}, missing 'build_payload' method)"
                )

        if dependencies.response_executor is not None and not isinstance(
            dependencies.response_executor, IResponseExecutor
        ):
            executor_type = type(dependencies.response_executor)
            if "MagicMock" in executor_type.__name__:
                if not hasattr(
                    dependencies.response_executor, "execute"
                ) or not callable(
                    getattr(dependencies.response_executor, "execute", None)
                ):
                    errors.append(
                        f"response_executor override must implement IResponseExecutor interface "
                        f"(got {executor_type.__name__}, missing or non-callable 'execute' method)"
                    )
            elif not hasattr(executor_type, "execute"):
                errors.append(
                    f"response_executor override must implement IResponseExecutor interface "
                    f"(got {executor_type.__name__}, missing 'execute' method)"
                )

        if dependencies.compatibility_layer is not None and not isinstance(
            dependencies.compatibility_layer, ICompatibilityLayer
        ):
            compat_type = type(dependencies.compatibility_layer)
            required_methods = [
                "apply",
                "translate_stream_chunk",
                "cleanup_state",
                "create_state",
            ]
            if "MagicMock" in compat_type.__name__:
                missing_methods = [
                    method
                    for method in required_methods
                    if not hasattr(dependencies.compatibility_layer, method)
                    or not callable(
                        getattr(dependencies.compatibility_layer, method, None)
                    )
                ]
            else:
                missing_methods = [
                    method
                    for method in required_methods
                    if not hasattr(compat_type, method)
                ]
            if missing_methods:
                errors.append(
                    f"compatibility_layer override must implement ICompatibilityLayer interface "
                    f"(got {compat_type.__name__}, missing methods: {', '.join(missing_methods)})"
                )

        if dependencies.tool_execution_service is not None and not isinstance(
            dependencies.tool_execution_service, IToolExecutionService
        ):
            tool_service_type = type(dependencies.tool_execution_service)
            required_methods = [
                "execute_proxy_tool",
                "get_available_tool_schemas",
            ]
            if "MagicMock" in tool_service_type.__name__:
                missing_methods = [
                    method
                    for method in required_methods
                    if not hasattr(dependencies.tool_execution_service, method)
                    or not callable(
                        getattr(dependencies.tool_execution_service, method, None)
                    )
                ]
            else:
                missing_methods = [
                    method
                    for method in required_methods
                    if not hasattr(tool_service_type, method)
                ]
            if missing_methods:
                errors.append(
                    f"tool_execution_service override must implement IToolExecutionService interface "
                    f"(got {tool_service_type.__name__}, missing methods: {', '.join(missing_methods)})"
                )

        if errors:
            error_message = "Invalid dependency overrides:\n" + "\n".join(
                f"  - {error}" for error in errors
            )
            raise ServiceResolutionError(error_message)

    def _resolve_dependencies(self) -> CodexConnectorDependencies | None:
        try:
            from src.core.di.services import get_or_build_service_provider

            provider = get_or_build_service_provider()
            resolved = provider.get_service(CodexConnectorDependencies)
            # Some tests monkeypatch the DI provider with MagicMock. Treat mocked
            # dependency bundles as absent so we don't leak MagicMock settings.
            if resolved is None:
                return None
            if "mock" in type(resolved).__name__.lower():
                return None
            return resolved
        except (ImportError, AttributeError, ServiceResolutionError) as err:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Failed to resolve CodexConnectorDependencies: %s",
                    err,
                    exc_info=True,
                )
            return None

    def _resolve_model_catalog(self) -> ICodexModelCatalog:
        """Resolve the Codex model catalog.

        Priority:
        1. Injected via ``CodexConnectorDependencies.model_catalog`` (tests / DI).
        2. Resolved from the DI provider (registered by ``CodexModelCatalogStage``
           at startup with the discovered catalog).
        3. Shipped fallback snapshot (``src/resources/codex/...``).
        """
        if self._dependencies and self._dependencies.model_catalog is not None:
            catalog = self._dependencies.model_catalog
            if "mock" not in type(catalog).__name__.lower():
                return cast(ICodexModelCatalog, catalog)
        try:
            from src.core.di.services import get_or_build_service_provider

            provider = get_or_build_service_provider()
            catalog = provider.get_service(cast(type[Any], ICodexModelCatalog))
            if catalog is not None and "mock" not in type(catalog).__name__.lower():
                return cast(ICodexModelCatalog, catalog)
        except (ImportError, AttributeError, ServiceResolutionError) as err:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Codex model catalog not in DI; loading shipped fallback: %s",
                    err,
                )
        return CodexCatalogFallbackLoader().load()

    def _refresh_settings_from_overrides(self) -> None:
        try:
            self._connector_settings_model = CodexConnectorSettings(
                **self._connector_settings
            )
        except (TypeError, ValidationError) as err:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Failed to create CodexConnectorSettings from overrides, using loader: %s",
                    err,
                    exc_info=True,
                )
            self._connector_settings_model = self._settings_loader.load(self.config)
            self._connector_settings = self._connector_settings_model.model_dump()

        self._default_capabilities = self._connector_settings_model.default_capabilities
        self._renderer_default = self._connector_settings["renderer"]["default"]
        self._renderer_fallback = self._connector_settings["renderer"]["fallback"]
        self._prompt_settings = self._connector_settings["prompt"]
        self._default_tool_schema_override = self._connector_settings["tool_schema"][
            "base_tools"
        ]
        self._custom_tool_schema_default = self._connector_settings["tool_schema"][
            "custom_tools"
        ]

        self._capability_resolver = CodexCapabilityResolver(
            default_capabilities=self._default_capabilities,
            agent_overrides=self._connector_settings_model.agent_overrides,
        )
        self._configure_managed_oauth_credential_manager()

        compat_cfg = getattr(
            self._connector_settings_model, "compatibility_layer", None
        )
        enabled_val = getattr(compat_cfg, "enabled", False)
        self._compatibility_layer_enabled = (
            enabled_val if isinstance(enabled_val, bool) else False
        )
        if self._compatibility_layer_enabled and self._session_detector is None:
            detection_cfg = getattr(compat_cfg, "detection", None)
            cache_ttl_val = getattr(detection_cfg, "cache_ttl_seconds", 3600)
            threshold_val = getattr(detection_cfg, "heuristic_threshold", 2)
            cache_ttl_seconds = (
                cache_ttl_val if isinstance(cache_ttl_val, int) else 3600
            )
            heuristic_threshold = threshold_val if isinstance(threshold_val, int) else 2
            self._session_detector = SessionDetector(
                cache_ttl_seconds=cache_ttl_seconds,
                heuristic_threshold=heuristic_threshold,
            )
        if not self._compatibility_layer_enabled:
            self._session_detector = None

        if self._compatibility_layer_enabled and self._kilo_tool_translator is None:
            session_service = getattr(self, "_session_service", None)
            self._kilo_tool_translator = KiloToolTranslator(self, session_service)

        # Reinitialize tool schema resolver if needed
        self._tool_schema_resolver = ToolSchemaResolver(
            settings=self._connector_settings_model,
            tool_execution_service=self._tool_execution_service,
        )

        if isinstance(self._payload_builder, PayloadBuilder):
            self._payload_builder = PayloadBuilder(
                connector=self,
                request_translator=self._request_translator_adapter,
                prompt_resolver=self._prompt_resolver,
                tool_schema_resolver=self._tool_schema_resolver,
                settings=self._connector_settings_model,
                message_to_text_converter=self._message_to_text,
            )

        # Executor configuration is set via constructor, no private field mutation needed

    def _build_managed_oauth_config(self) -> ManagedOAuthConfig:
        raw = self._connector_settings.get("managed_oauth", {})
        payload = raw if isinstance(raw, dict) else {}
        try:
            return ManagedOAuthConfig.from_mapping(
                payload,
                default_storage_path=DEFAULT_STORAGE_PATH,
            )
        except Exception as exc:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Invalid OpenAI Codex managed OAuth settings; using defaults: %s",
                    exc,
                    exc_info=True,
                )
            return ManagedOAuthConfig(
                enabled=True,
                storage_path=DEFAULT_STORAGE_PATH,
                accounts="all",
            )

    def _configure_managed_oauth_credential_manager(self) -> None:
        configure_method = getattr(
            self._credential_manager, "configure_managed_oauth", None
        )
        if not callable(configure_method):
            return
        config = self._build_managed_oauth_config()
        try:
            configure_method(config)
        except Exception as exc:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to apply managed OAuth settings to credential manager: %s",
                    exc,
                    exc_info=True,
                )

    async def list_managed_oauth_account_ids(self) -> list[str]:
        """Return managed OAuth account IDs for usage-window warm-up fan-out.

        Skips locally rate-limited accounts while any non-cooldown account exists;
        when all are on cooldown, all usable ids are still listed for probes.
        Prefer the credential manager implementation when present.
        """
        cm_list = getattr(
            self._credential_manager, "list_managed_oauth_account_ids", None
        )
        if callable(cm_list):
            try:
                raw = cm_list()
                result = await raw if inspect.isawaitable(raw) else raw
            except Exception as exc:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Credential manager warm-up account enumeration failed: %s",
                        exc,
                        exc_info=True,
                    )
            else:
                if isinstance(result, list):
                    return [str(x) for x in result if isinstance(x, str) and x.strip()]

        selector = getattr(self._credential_manager, "_managed_selector", None)
        if selector is None:
            return []

        try:
            reload_accounts = getattr(selector, "reload_accounts", None)
            if callable(reload_accounts):
                result = reload_accounts()
                if inspect.isawaitable(result):
                    await result

            now_ms = int(time.time() * 1000)
            available_accounts_fn = getattr(selector, "_available_accounts", None)
            if callable(available_accounts_fn):
                maybe_accounts = available_accounts_fn(now_ms)
                if (
                    not isinstance(maybe_accounts, tuple)
                    or len(maybe_accounts) != 2
                    or not isinstance(maybe_accounts[0], Sequence)
                    or not isinstance(maybe_accounts[1], Sequence)
                ):
                    return []
                available, eligible = maybe_accounts
                if available and logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "OpenAI Codex warm-up account fan-out candidates: %d available, %d eligible",
                        len(available),
                        len(eligible),
                    )
                use = eligible if eligible else available
                return [
                    account.account_id
                    for account in use
                    if isinstance(account.account_id, str) and account.account_id
                ]
        except Exception as exc:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to enumerate managed OAuth accounts for warm-up fan-out: %s",
                    exc,
                    exc_info=True,
                )
        return []

    @staticmethod
    def _usage_window_warmup_managed_account_hint(
        request: ConnectorChatCompletionsRequest,
    ) -> tuple[bool, str | None]:
        """Return ``(is_usage_window_warmup, managed_account_id_or_none)``."""
        ctx = request.context
        ext: dict[str, Any] = {}
        if ctx is not None and isinstance(ctx.extensions, dict):
            ext = ctx.extensions
        if ext.get("usage_window_warmup") is not True:
            return False, None
        for key in ("warmup_target_account_id", "openai_codex_managed_account_id"):
            raw = ext.get(key)
            if isinstance(raw, str) and raw.strip():
                return True, raw.strip()
        domain = request.request
        extra_body = getattr(domain, "extra_body", None)
        if isinstance(extra_body, dict):
            mid = extra_body.get("openai_codex_managed_account_id")
            if isinstance(mid, str) and mid.strip():
                return True, mid.strip()
        return True, None

    async def _prepare_usage_window_warmup_credentials(
        self, request: ConnectorChatCompletionsRequest
    ) -> Callable[[], Awaitable[None]] | None:
        is_warmup, managed_id = self._usage_window_warmup_managed_account_hint(request)
        if not is_warmup:
            return None

        snapshot: Mapping[str, Any] | None = None
        begin_fn = getattr(
            self._credential_manager, "begin_usage_window_warmup_override", None
        )
        end_fn = getattr(
            self._credential_manager, "end_usage_window_warmup_override", None
        )
        if callable(begin_fn) and callable(end_fn):
            raw_snapshot = begin_fn()
            maybe_snapshot = (
                await raw_snapshot
                if inspect.isawaitable(raw_snapshot)
                else raw_snapshot
            )
            if isinstance(maybe_snapshot, Mapping):
                snapshot = cast(Mapping[str, Any], maybe_snapshot)

        async def restore() -> None:
            if snapshot is not None and callable(end_fn):
                raw_restore = end_fn(snapshot)
                if inspect.isawaitable(raw_restore):
                    await raw_restore
            token_after_restore = self._credential_manager.get_access_token()
            if isinstance(token_after_restore, str) and token_after_restore.strip():
                self.api_key = token_after_restore

        fn = getattr(
            self._credential_manager,
            "ensure_usage_window_warmup_managed_account",
            None,
        )
        if not callable(fn):
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Usage window warm-up: credential manager missing "
                    "ensure_usage_window_warmup_managed_account; skipping account override."
                )
            return restore if snapshot is not None else None
        domain = request.request
        sid_raw = getattr(domain, "session_id", None)
        session_id = sid_raw if isinstance(sid_raw, str) and sid_raw.strip() else None
        raw = fn(managed_id, session_id=session_id)
        ok = await raw if inspect.isawaitable(raw) else bool(raw)
        if not ok and managed_id is not None:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Usage window warm-up: could not bind requested managed account %s; failing targeted probe.",
                    managed_id,
                )
            raise BackendError(
                message=(
                    "Usage window warm-up could not bind targeted managed account"
                ),
                details={
                    "warmup_target_account_id": managed_id,
                    "retryable": False,
                    "status_code": 409,
                },
            )
        token = self._credential_manager.get_access_token()
        if isinstance(token, str) and token.strip():
            self.api_key = token
        return restore if snapshot is not None else None

    @property
    def _auth_credentials(self) -> dict[str, Any] | None:
        return getattr(self._credential_manager, "_auth_credentials", None)

    @_auth_credentials.setter
    def _auth_credentials(self, value: dict[str, Any] | None) -> None:
        self._credential_manager._auth_credentials = value  # type: ignore[reportPrivateUsage]
        token = self._credential_manager.get_access_token()
        if token:
            self.api_key = token

    @property
    def _auth_path(self) -> Path | None:
        return getattr(self._credential_manager, "_auth_path", None)

    @_auth_path.setter
    def _auth_path(self, value: Path | None) -> None:
        self._credential_manager._auth_path = value  # type: ignore[reportPrivateUsage]

    @property
    def _oauth_dir_override(self) -> Path | None:
        return getattr(self._credential_manager, "_oauth_dir_override", None)

    @_oauth_dir_override.setter
    def _oauth_dir_override(self, value: Path | None) -> None:
        self._credential_manager._oauth_dir_override = value  # type: ignore[reportPrivateUsage]

    @property
    def _last_modified(self) -> float:
        return float(getattr(self._credential_manager, "_last_modified", 0.0))

    @_last_modified.setter
    def _last_modified(self, value: float) -> None:
        self._credential_manager._last_modified = float(value)  # type: ignore[reportPrivateUsage]

    @property
    def _file_observer(self) -> BaseObserver | None:
        return self._file_observer_ref

    @_file_observer.setter
    def _file_observer(self, value: BaseObserver | None) -> None:
        self._file_observer_ref = value

    def _is_codex_model(self, model_name: str) -> bool:
        """Return True when the model is a supported Codex model."""
        clean_model = strip_vendor_prefix(model_name.lower(), OPENAI_VENDOR_PREFIX)
        return self._catalog.is_supported(clean_model)

    @classmethod
    def _codex_system_prompt(cls) -> str:
        """Load the Codex system prompt from bundled resources or vendor sources."""
        return PromptResolver._codex_system_prompt()  # type: ignore[reportPrivateUsage]

    def _codex_user_agent(self) -> str:
        """Build a Codex CLI compatible User-Agent string."""
        return build_codex_user_agent(self.CODEX_ORIGINATOR, self.CODEX_VERSION_HEADER)

    def _codex_account_id(self) -> str | None:
        """Return the ChatGPT account_id from cached credentials when available."""
        if hasattr(self._credential_manager, "get_account_id"):
            account_id = self._credential_manager.get_account_id()
            return account_id if isinstance(account_id, str) else None
        return None

    @staticmethod
    def _message_to_text(message: Any) -> str:
        return message_to_text(message)

    def _build_environment_context_block(
        self, request_data: Any, effective_model: str
    ) -> str:
        """Compose the <environment_context> block with best-effort metadata."""
        extra_body = getattr(request_data, "extra_body", {}) or {}
        override = extra_body.get("codex_environment_context")
        if isinstance(override, str) and override.strip():
            return override

        cwd = extra_body.get("project_dir") or extra_body.get("cwd")
        if not cwd:
            cwd = os.getcwd()

        sandbox_mode = extra_body.get("sandbox_mode") or "read-only"
        approval_policy = extra_body.get("approval_policy") or "never"
        network_access = extra_body.get("network_access") or "restricted"
        shell_value = extra_body.get("shell") or os.environ.get("SHELL") or "bash"
        if isinstance(shell_value, str) and "/" in shell_value:
            shell_value = shell_value.rsplit("/", 1)[-1] or shell_value
        shell = shell_value or "bash"

        lines = [
            "<environment_context>",
            f"  <cwd>{cwd}</cwd>",
            f"  <approval_policy>{approval_policy}</approval_policy>",
            f"  <sandbox_mode>{sandbox_mode}</sandbox_mode>",
            f"  <network_access>{network_access}</network_access>",
            f"  <shell>{shell}</shell>",
            "</environment_context>",
        ]
        return "\n".join(lines)

    def _extract_custom_instruction_sections(self, request_data: Any) -> list[str]:
        """Extract custom instruction sections from request."""
        sections: list[str] = []

        request_prompt = getattr(request_data, "system_prompt", None)
        if isinstance(request_prompt, str) and request_prompt.strip():
            sections.append(request_prompt.strip())

        messages = getattr(request_data, "messages", [])
        for message in messages or []:
            role = getattr(message, "role", None)
            if role is None and isinstance(message, dict):
                role = message.get("role")
            if (role or "").lower() != "system":
                continue
            text = self._message_to_text(message)
            if text.strip():
                sections.append(text.strip())

        extra_body = getattr(request_data, "extra_body", {}) or {}
        extra_prompt = extra_body.get("codex_system_prompt")
        if isinstance(extra_prompt, str) and extra_prompt.strip():
            sections.append(extra_prompt.strip())
        elif isinstance(extra_prompt, list | tuple):
            for part in extra_prompt:
                if isinstance(part, str) and part.strip():
                    sections.append(part.strip())

        deduplicated: list[str] = []
        seen: set[str] = set()
        for section in sections:
            normalized = section.strip()
            if not normalized or normalized in seen:
                continue
            seen.add(normalized)
            deduplicated.append(normalized)
        return deduplicated

    def _render_user_instruction_block(
        self, sections: Sequence[str | None]
    ) -> dict[str, Any] | None:
        """Render custom instruction sections into a Codex <user_instructions> block."""
        sanitized_sections: list[str] = []
        for section in sections:
            if section is None:
                continue
            normalized = section.strip()
            if not normalized:
                continue
            sanitized_sections.append(self._sanitize_codex_instructions(normalized))

        if not sanitized_sections:
            return None

        combined = "\n\n".join(sanitized_sections)
        payload_text = (
            "<user_instructions>\n\n" f"{combined}" "\n\n</user_instructions>"
        )
        return {
            "type": "message",
            "role": "user",
            "content": [
                {
                    "type": "input_text",
                    "text": payload_text,
                }
            ],
        }

    def _resolve_system_prompt(
        self,
        request_data: Any,
        capabilities: CodexClientCapabilities,
        custom_instruction_sections: Sequence[str] | None = None,
    ) -> str:
        """Determine the system prompt based on capability settings and request data."""
        prompt_mode = (capabilities.prompt_mode or "codex_default").lower()
        custom_sections = (
            list(custom_instruction_sections)
            if custom_instruction_sections is not None
            else self._extract_custom_instruction_sections(request_data)
        )
        custom_clean = [piece for piece in custom_sections if piece]

        default_prompt_template = self._prompt_settings.get("template")
        default_prompt = (
            default_prompt_template
            if isinstance(default_prompt_template, str)
            and default_prompt_template.strip()
            else self._codex_system_prompt()
        )
        prepend_sections = list(self._prompt_settings.get("prepend", []))
        append_sections = list(self._prompt_settings.get("append", []))
        deduplicate = bool(self._prompt_settings.get("deduplicate", True))
        fallback_to_default = bool(
            self._prompt_settings.get("fallback_to_default", True)
        )

        if prompt_mode == "codex_default":
            combined = [*prepend_sections, default_prompt, *append_sections]
            result = self._combine_prompt_sections(combined, deduplicate)
            return result if result is not None else ""

        if prompt_mode == "merge_custom":
            combined = [
                *prepend_sections,
                default_prompt,
                *custom_clean,
                *append_sections,
            ]
            result = self._combine_prompt_sections(combined, deduplicate)
            return result if result is not None else ""

        if prompt_mode == "custom_only":
            combined = prepend_sections + custom_clean + append_sections
            merged = self._combine_prompt_sections(combined, deduplicate)
            if merged:
                return merged
            if not fallback_to_default:
                return ""
            fallback_combined = [*prepend_sections, default_prompt, *append_sections]
            result = self._combine_prompt_sections(fallback_combined, deduplicate)
            return result if result is not None else ""

        fallback_combined = [*prepend_sections, default_prompt, *append_sections]
        result = self._combine_prompt_sections(fallback_combined, deduplicate)
        return result if result is not None else ""

    @staticmethod
    def _combine_prompt_sections(
        sections: Sequence[str], deduplicate: bool
    ) -> str | None:
        return PromptResolver._combine_prompt_sections(sections, deduplicate)  # type: ignore[reportPrivateUsage]

    @staticmethod
    def _sanitize_codex_instructions(text: str) -> str:
        return PromptResolver._sanitize_codex_instructions(text)  # type: ignore[reportPrivateUsage]

    def _default_codex_tools(self) -> list[dict[str, Any]]:
        """Return the tool definitions expected by the Codex Responses API."""
        if self._default_tool_schema_override is not None:
            return deepcopy(self._default_tool_schema_override)

        tools = self._tool_schema_resolver._get_default_tools()  # type: ignore[reportPrivateUsage]
        return [
            (
                tool.model_dump(exclude_none=True)
                if hasattr(tool, "model_dump")
                else dict(tool)
            )
            for tool in tools
        ]

    def _resolve_capabilities(
        self, request_data: Any, metadata: dict[str, Any] | None = None
    ) -> CodexClientCapabilities:
        """Resolve client capabilities for downstream translation."""
        return self._capability_resolver.resolve(request_data, metadata)

    def _build_codex_input_items(
        self,
        request_data: Any,
        processed_messages: list[Any],
        effective_model: str,
        capabilities: CodexClientCapabilities | None = None,
        custom_instruction_sections: Sequence[str] | None = None,
    ) -> list[dict[str, Any]]:
        resolved_capabilities = capabilities or self._resolve_capabilities(request_data)
        return self._request_translator.build_input_items(
            request_data,
            processed_messages,
            effective_model,
            resolved_capabilities,
            custom_instruction_sections=custom_instruction_sections,
        )

    def _is_native_responses_payload(self, request_data: Any) -> bool:
        """Detect if a request payload is in the native Codex/Responses format."""
        if hasattr(request_data, "model_dump"):
            data = request_data.model_dump()
        elif isinstance(request_data, dict):
            data = request_data
        else:
            return False

        if (
            "messages" in data
            and isinstance(data.get("messages"), list)
            and not ("prompt_cache_key" in data or "instructions" in data)
        ):
            return False

        if "input" in data:
            input_val = data.get("input")
            if not isinstance(input_val, list):
                return False
            if input_val:
                first_item = input_val[0]
                if isinstance(first_item, dict):
                    has_responses_structure = "type" in first_item or (
                        "role" in first_item and "content" in first_item
                    )
                    if has_responses_structure:
                        return True

        responses_specific_fields = {"prompt_cache_key", "include", "store"}
        return any(field in data for field in responses_specific_fields)

    def _normalize_processed_messages(
        self, processed_messages: list[Any]
    ) -> list[ProcessedMessage]:
        normalized: list[ProcessedMessage] = []
        for message in processed_messages or []:
            if isinstance(message, ProcessedMessage):
                normalized.append(message)
                continue
            if hasattr(message, "model_dump") and callable(message.model_dump):
                dumped = message.model_dump(exclude_none=True)
                if isinstance(dumped, dict):
                    if dumped.get("content") is None:
                        dumped = {**dumped, "content": ""}
                    normalized.append(ProcessedMessage.model_validate(dumped))
                    continue
            if isinstance(message, dict):
                payload = dict(message)
                if payload.get("content") is None:
                    payload["content"] = ""
                normalized.append(ProcessedMessage(**payload))
                continue
            role = getattr(message, "role", None)
            content = getattr(message, "content", None)
            if role is not None and content is not None:
                normalized.append(ProcessedMessage(role=role, content=content))
        return normalized

    def _build_codex_payload(
        self,
        request_data: Any,
        processed_messages: list[Any],
        effective_model: str,
        capabilities: CodexClientCapabilities | None = None,
    ) -> tuple[CodexPayload, str]:
        """Build Codex payload and return payload object with conversation ID."""
        resolved_capabilities = capabilities or self._resolve_capabilities(request_data)
        processed = self._normalize_processed_messages(processed_messages)
        session_id = getattr(request_data, "session_id", None) or str(uuid.uuid4())

        try:
            context = CodexRequestContext(
                request=request_data,
                processed_messages=processed,
                effective_model=effective_model,
                capabilities=resolved_capabilities,
                session_id=session_id,
                metadata=getattr(request_data, "metadata", None),
            )
        except (TypeError, ValidationError) as err:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Failed to validate CodexRequestContext, using model_construct fallback: %s",
                    err,
                    exc_info=True,
                )
            context = CodexRequestContext.model_construct(
                request=request_data,
                processed_messages=processed,
                effective_model=effective_model,
                capabilities=resolved_capabilities,
                session_id=session_id,
                metadata=getattr(request_data, "metadata", None),
            )

        payload = self._payload_builder.build_payload(context)
        return payload, payload.prompt_cache_key

    def _coerce_payload_for_executor(
        self, payload: CodexPayload | dict[str, Any], context: CodexRequestContext
    ) -> CodexPayload:
        # Handle CodexPayload instance
        if isinstance(payload, CodexPayload):
            # Ensure stream attribute exists (handles model_construct edge cases)
            if not hasattr(payload, "stream"):
                stream_val = getattr(context.request, "stream", False)
                # Rebuild payload with stream field
                pydantic_dict = payload.model_dump()
                pydantic_dict["stream"] = stream_val
                return CodexPayload.model_validate(pydantic_dict)
            return payload

        # Convert dict or mock to dict
        payload_dict: dict[str, Any]
        if isinstance(payload, dict):  # type: ignore[reportUnnecessaryIsInstance]
            payload_dict = dict(payload)
        elif hasattr(payload, "model_dump"):
            payload_dict = payload.model_dump()
        elif hasattr(payload, "__dict__"):
            payload_dict = dict(payload.__dict__)
        else:
            # Try to convert mock or other object to dict
            payload_dict = {}
            for key in [
                "model",
                "input",
                "tools",
                "tool_choice",
                "parallel_tool_calls",
                "store",
                "stream",
                "include",
                "prompt_cache_key",
                "reasoning",
                "instructions",
                "extras",
            ]:
                if hasattr(payload, key):
                    payload_dict[key] = getattr(payload, key)

        # Ensure required fields are present with defaults
        if "stream" not in payload_dict:
            payload_dict["stream"] = getattr(context.request, "stream", False)
        if "model" not in payload_dict:
            payload_dict["model"] = context.effective_model
        if "input" not in payload_dict:
            payload_dict["input"] = []
        if "tools" not in payload_dict:
            payload_dict["tools"] = []
        if "tool_choice" not in payload_dict:
            payload_dict["tool_choice"] = "auto"
        if "parallel_tool_calls" not in payload_dict:
            payload_dict["parallel_tool_calls"] = False
        if "store" not in payload_dict:
            payload_dict["store"] = False
        if "include" not in payload_dict:
            payload_dict["include"] = []
        if "prompt_cache_key" not in payload_dict:
            payload_dict["prompt_cache_key"] = getattr(
                payload, "prompt_cache_key", None
            ) or str(uuid.uuid4())

        # Use public interface to convert dict to payload
        if self._payload_builder is not None:
            try:
                converted_payload: CodexPayload = (
                    self._payload_builder.convert_dict_to_payload(payload_dict, context)
                )
                return converted_payload
            except (TypeError, ValidationError) as err:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Failed to convert payload via convert_dict_to_payload, using model_validate: %s",
                        err,
                        exc_info=True,
                    )
                return CodexPayload.model_validate(payload_dict)
        try:
            return CodexPayload.model_validate(payload_dict)
        except (TypeError, ValidationError) as err:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Failed to validate payload, using model_construct with defaults: %s",
                    err,
                    exc_info=True,
                )
            return CodexPayload.model_construct(**payload_dict)

    def _select_renderer_key(self, capabilities: CodexClientCapabilities) -> str:
        """Map capability preference to a registered renderer key."""
        renderer_default = (
            self._renderer_default
            if isinstance(self._renderer_default, str)
            else "none"
        )
        preferred_value = capabilities.tool_text_format or renderer_default
        preferred = preferred_value.strip() if isinstance(preferred_value, str) else ""  # type: ignore[reportUnnecessaryIsInstance]
        if not preferred:
            return renderer_default
        if preferred.lower() in {"default", "inherit"}:
            return renderer_default
        return preferred

    def _build_codex_headers(self, conversation_id: str) -> dict[str, str]:
        """Construct Codex-specific HTTP headers."""
        headers = self.get_headers() or {}
        headers["OpenAI-Beta"] = "responses=experimental"
        headers["Accept"] = "text/event-stream"
        headers["version"] = self.CODEX_VERSION_HEADER
        headers["originator"] = self.CODEX_ORIGINATOR
        headers["User-Agent"] = self._codex_user_agent()
        headers["conversation_id"] = conversation_id
        headers["session_id"] = conversation_id
        headers["Codex-Task-Type"] = "standard"

        account_id = self._codex_account_id()
        if account_id:
            headers["chatgpt-account-id"] = account_id

        return headers

    def _refresh_codex_headers_auth(
        self, headers: dict[str, str], conversation_id: str
    ) -> None:
        """Update Codex headers in place with latest auth token and session markers."""
        fresh_headers = self.get_headers() or {}
        for key, value in fresh_headers.items():
            headers[key] = value
        headers["conversation_id"] = conversation_id
        headers["session_id"] = conversation_id

    # Legacy methods removed: _should_retry_stream_for_auth_error and _extract_status_code
    # Retry logic is now handled by ResponseExecutor in executor.py

    @staticmethod
    def _format_tool_results_text(tool_results: list[Any]) -> str:
        texts: list[str] = []
        for result in tool_results:
            text: str | None
            if isinstance(result, ToolExecutionResult):
                text = result.result
            elif isinstance(result, dict):
                value = result.get("result")
                text = value if isinstance(value, str) else None
            else:
                text = None
            if text:
                texts.append(text)
        return "\n\n".join(texts)

    def _format_kilo_response(
        self, response: dict[str, Any], tool_results: list[Any]
    ) -> dict[str, Any]:
        if not tool_results:
            return response

        content = ""
        if response.get("choices"):
            choice = response["choices"][0]
            if "message" in choice:
                content = choice["message"].get("content", "")
            elif "delta" in choice:
                content = choice["delta"].get("content", "")

        tool_results_text = self._format_tool_results_text(tool_results)
        if tool_results_text:
            merged_content = (
                f"{tool_results_text}\n\n{content}" if content else tool_results_text
            )
            if response.get("choices"):
                choice = response["choices"][0]
                if "message" in choice:
                    choice["message"]["content"] = merged_content
                elif "delta" in choice:
                    choice["delta"]["content"] = merged_content

        return response

    async def _format_kilo_stream_response(
        self, stream: AsyncIterator[Any], tool_results: list[Any]
    ) -> AsyncIterator[Any]:
        if tool_results:
            tool_results_text = self._format_tool_results_text(tool_results)
            if tool_results_text:
                yield {
                    "choices": [
                        {
                            "delta": {
                                "content": tool_results_text + "\n\n",
                                "role": "assistant",
                            },
                            "index": 0,
                            "finish_reason": None,
                        }
                    ],
                    "created": int(time.time()),
                    "model": "gpt-4",
                    "object": "chat.completion.chunk",
                }

        async for chunk in stream:
            yield chunk

    def _update_processing_context(
        self, domain_request: Any, updates: dict[str, Any]
    ) -> None:
        if not hasattr(domain_request, "processing_context"):
            return
        if domain_request.processing_context is None:
            domain_request.processing_context = {}

        context = domain_request.processing_context
        if (
            hasattr(context, "update")
            and callable(context.update)
            or isinstance(context, dict)
        ):
            context.update(updates)

    def _resolve_reasoning_effort(
        self, model: str, uri_params: dict[str, Any], request_data: Any
    ) -> str:
        effort = uri_params.get("reasoning_effort")
        if isinstance(effort, list):
            effort = effort[0] if effort else None
        if isinstance(effort, str):
            effort = effort.lower().strip()

        if not effort:
            effort = getattr(request_data, "reasoning_effort", None)
            if isinstance(effort, str):
                effort = effort.lower().strip()

        if not effort:
            effort = self.DEFAULT_REASONING_EFFORT

        if not self._catalog.is_valid_effort(effort):
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Invalid reasoning_effort '%s', falling back to '%s'. Supported levels: %s",
                    effort,
                    self._catalog.default_reasoning_effort,
                    ", ".join(self._catalog.reasoning_effort_order),
                )
            effort = self._catalog.default_reasoning_effort

        # Clamp the requested effort to a level the model actually supports.
        # Extended tiers (xhigh/max/ultra) are only available on newer models;
        # unsupported tiers are downgraded to the highest supported level at or
        # below the requested depth (e.g. ultra -> max -> xhigh -> high).
        supported_levels = self._catalog.supported_reasoning_levels(model)
        if effort not in supported_levels:
            downgraded = self._catalog.clamp_reasoning_effort(model, effort)
            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    "Model '%s' does not support '%s' reasoning effort. "
                    "Downgrading to '%s'. Supported levels for this model: %s.",
                    model,
                    effort,
                    downgraded,
                    ", ".join(supported_levels),
                )
            effort = downgraded

        return effort

    def _resolve_verbosity(
        self, model: str, uri_params: dict[str, Any], request_data: Any
    ) -> str | None:
        """Resolve output verbosity for Codex Responses ``text.verbosity``.

        Returns ``None`` when unset, invalid, or the model does not support
        verbosity (omit from outbound payload).

        The canonical request field is authoritative after centralized
        parameter resolution. Raw URI and body values remain compatibility
        fallbacks for callers that bypass that preparation step. An invalid
        value is skipped so a valid lower-precedence source can still apply.
        """
        allowed = {"low", "medium", "high"}

        def _normalize(raw: Any) -> str | None:
            if isinstance(raw, list):
                raw = raw[0] if raw else None
            if not isinstance(raw, str):
                return None
            value = raw.lower().strip()
            return value or None

        def _accept(raw: Any) -> str | None:
            value = _normalize(raw)
            if value is None:
                return None
            if value in allowed:
                return value
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Invalid verbosity '%s' for Codex; omitting. Supported: %s",
                    value,
                    ", ".join(sorted(allowed)),
                )
            return None

        verbosity = _accept(getattr(request_data, "verbosity", None))

        if verbosity is None:
            verbosity = _accept(uri_params.get("verbosity"))

        if verbosity is None:
            extra_body = getattr(request_data, "extra_body", None)
            if isinstance(extra_body, dict):
                verbosity = _accept(extra_body.get("verbosity"))
                if verbosity is None:
                    text_cfg = extra_body.get("text")
                    if isinstance(text_cfg, dict):
                        verbosity = _accept(text_cfg.get("verbosity"))

        if verbosity is None:
            return None

        if not self._catalog.supports_verbosity(model):
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Model '%s' does not support verbosity; omitting text.verbosity.",
                    model,
                )
            return None

        return verbosity

    async def _call_codex_responses_api(
        self,
        request_data: Any,
        processed_messages: list[Any],
        effective_model: str,
        domain_request: Any,
        *,
        options_metadata: Mapping[str, Any] | None = None,
        request_context: ConnectorRequestContext | None = None,
    ) -> Any:
        """Call the Codex-specific Responses API endpoint."""
        capabilities = self._resolve_capabilities(request_data)

        self._update_processing_context(
            domain_request,
            {
                "codex_capabilities": capabilities.to_dict(),
                "bypass_tool_call_reactor": capabilities.bypass_tool_call_reactor,
                "tool_text_format": capabilities.tool_text_format,
            },
        )

        session_id = getattr(domain_request, "session_id", None) or str(uuid.uuid4())
        metadata = None
        if hasattr(domain_request, "metadata"):
            metadata = domain_request.metadata
        elif hasattr(request_data, "metadata"):
            metadata = request_data.metadata

        if isinstance(options_metadata, Mapping) and options_metadata:
            merged: dict[str, Any] = (
                dict(metadata) if isinstance(metadata, dict) else {}
            )
            for k, v in options_metadata.items():
                merged[k] = v
            metadata = merged or None

        is_kilocode = False
        tool_results: list[Any] = []
        translated_tools: dict[str, list[Any]] = {
            "codex_tools": [],
            "proxy_tools": [],
            "mcp_tools": [],
        }
        compatibility_state = None
        normalized_messages = self._normalize_processed_messages(processed_messages)
        payload_messages = normalized_messages

        if self._compatibility_layer_enabled and self._session_detector:
            detection_result = await self._session_detector.detect(
                request_data=request_data,
                metadata=metadata,
                session_id=session_id,
                backend=self.backend_type,
            )
            is_kilocode = detection_result.is_kilocode
            self._update_processing_context(
                domain_request,
                {
                    "is_kilocode_client": is_kilocode,
                    "kilocode_detection_method": detection_result.detection_method,
                },
            )

        if self._compatibility_layer_enabled and self._compatibility_layer:
            try:
                context = CodexRequestContext.model_construct(
                    request=request_data,
                    processed_messages=normalized_messages,
                    effective_model=effective_model,
                    capabilities=capabilities,
                    session_id=session_id,
                    metadata=metadata,
                )
                compat_result = await self._compatibility_layer.apply(context)
                compatibility_state = compat_result.state
                is_kilocode = is_kilocode or compat_result.state.is_kilocode
                translated_tools = {
                    "codex_tools": compat_result.codex_tools,
                    "proxy_tools": compat_result.proxy_tools,
                    "mcp_tools": compat_result.mcp_tools,
                }
                tool_results = compat_result.tool_results
                payload_messages = context.processed_messages
            except Exception as exc:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Compatibility layer apply failed: %s", exc, exc_info=True
                    )

        payload, _conversation_id = self._build_codex_payload(
            request_data,
            payload_messages,
            effective_model,
            capabilities=capabilities,
        )

        if is_kilocode and translated_tools["codex_tools"]:
            payload_tools = payload.tools
            if not isinstance(payload_tools, list):  # type: ignore[reportUnnecessaryIsInstance]
                payload_tools = []
                payload.tools = payload_tools

            existing_names: set[str] = set()
            for entry in payload_tools:
                name_value = getattr(entry, "name", None)
                if name_value is None and isinstance(entry, dict):
                    name_value = entry.get("name")
                if isinstance(name_value, str):
                    existing_names.add(name_value)

            for tool in translated_tools["codex_tools"]:
                schema = get_codex_tool_schema(tool.name)
                if not schema:
                    continue
                schema_name = schema.name
                if not isinstance(schema_name, str):  # type: ignore[reportUnnecessaryIsInstance]
                    continue
                if schema_name in existing_names:
                    continue
                payload_tools.append(schema)
                existing_names.add(schema_name)
                if logger.isEnabledFor(TRACE_LEVEL):
                    logger.log(
                        TRACE_LEVEL, "Added Codex-side tool %s to payload", schema_name
                    )

        stream_val = bool(getattr(request_data, "stream", False))

        executor_metadata: dict[str, object] | None = None
        if isinstance(metadata, dict):
            executor_metadata = dict(metadata)
        if compatibility_state is not None:
            if executor_metadata is None:
                executor_metadata = {}
            executor_metadata["compatibility_state"] = compatibility_state
        if request_context is not None:
            if executor_metadata is None:
                executor_metadata = {}
            executor_metadata["connector_request_context"] = request_context
            executor_metadata["capture_key_name"] = self.backend_type

        executor_context = CodexRequestContext.model_construct(
            request=request_data,
            processed_messages=payload_messages,
            effective_model=effective_model,
            capabilities=capabilities,
            session_id=session_id,
            metadata=executor_metadata,
        )

        # Always use ResponseExecutor for Codex requests - retry logic is handled by executor
        if not self._response_executor:
            raise ServiceResolutionError(
                "ResponseExecutor dependency not initialized. "
                "This indicates a configuration or dependency injection issue. "
                "Ensure CodexConnectorDependencies provides a valid IResponseExecutor override, "
                "or that the default ResponseExecutor construction succeeded during connector initialization.",
                details={
                    "backend": self.backend_type,
                    "dependency": "IResponseExecutor",
                    "suggestion": "Check connector initialization logs for dependency resolution errors.",
                },
            )

        payload_obj = self._coerce_payload_for_executor(payload, executor_context)
        response = await self._response_executor.execute(payload_obj, executor_context)

        # Codex backend requires streaming SSE; if the client requested non-streaming,
        # accumulate the stream into a single ResponseEnvelope.
        if not stream_val and isinstance(response, StreamingResponseEnvelope):
            from src.connectors.gemini_base.response_accumulator import (
                StreamingResponseAccumulator,
            )

            accumulator = StreamingResponseAccumulator(backend_type=self.backend_type)
            response = await accumulator.accumulate(response)

        # Apply KiloCode formatting if needed (preserves existing behavior)
        if is_kilocode and tool_results:
            if stream_val:
                from collections.abc import AsyncIterator

                if hasattr(response, "content") and isinstance(
                    response.content, AsyncIterator
                ):
                    formatted_stream = self._format_kilo_stream_response(
                        response.content, tool_results  # type: ignore[arg-type]
                    )
                    return StreamingResponseEnvelope(
                        content=formatted_stream,
                        media_type=getattr(response, "media_type", "text/event-stream"),
                        headers=getattr(response, "headers", {}),
                        cancel_callback=getattr(response, "cancel_callback", None),
                    )
            else:
                if isinstance(response, ResponseEnvelope):
                    if isinstance(response.content, dict):
                        response.content = self._format_kilo_response(
                            response.content, tool_results
                        )
                elif isinstance(response, dict):
                    response = self._format_kilo_response(response, tool_results)

        return response

    def update_quota_headers(self, headers: Mapping[str, Any]) -> None:
        """Mirror base quota tracking and persist ``x-codex-*`` on the active managed account."""
        super().update_quota_headers(headers)
        record_raw = getattr(
            self._credential_manager, "record_codex_quota_headers", None
        )
        eval_raw = getattr(
            self._credential_manager,
            "evaluate_codex_remaining_quota_notifications",
            None,
        )
        has_codex_quota = any(str(k).lower().startswith("x-codex-") for k in headers)
        try:
            loop = asyncio.get_running_loop()
        except RuntimeError:
            return

        if has_codex_quota and callable(eval_raw):
            evaluate = cast(Callable[..., Awaitable[None]], eval_raw)

            async def _remaining_alerts() -> None:
                try:
                    await evaluate(headers, managed_oauth_account_id=None)
                except Exception as exc:
                    logger.warning(
                        "OpenAI Codex evaluate_codex_remaining_quota_notifications failed: %s",
                        exc,
                        exc_info=True,
                    )

            alert_task = loop.create_task(_remaining_alerts())
            self._codex_quota_persist_tasks.add(alert_task)
            alert_task.add_done_callback(self._codex_quota_persist_tasks.discard)

        if not callable(record_raw):
            return
        record = cast(Callable[..., Awaitable[None]], record_raw)

        async def _persist() -> None:
            try:
                await record(headers, force=False)
            except OSError as exc:
                logger.warning(
                    "OpenAI Codex record_codex_quota_headers failed: %s",
                    exc,
                )
            except Exception as exc:
                logger.exception(
                    "OpenAI Codex record_codex_quota_headers failed: %s",
                    exc,
                )

        task = loop.create_task(_persist())
        self._codex_quota_persist_tasks.add(task)
        task.add_done_callback(self._codex_quota_persist_tasks.discard)

    async def _refresh_access_token(self) -> bool:
        refreshed = await self._credential_manager.refresh_access_token()
        if refreshed:
            token = self._credential_manager.get_access_token()
            if token:
                self.api_key = token
            return True
        return False

    async def _handle_rate_limit_rotation(
        self,
        retry_after_seconds: float | None,
        *,
        session_id: str | None = None,
        upstream_codex_error: Mapping[str, Any] | None = None,
        response_headers: Mapping[str, Any] | None = None,
        managed_oauth_account_id: str | None = None,
    ) -> bool:
        """Rotate managed OAuth accounts on 429 responses when available."""
        rotate_method = getattr(self._credential_manager, "handle_rate_limit", None)
        if not callable(rotate_method):
            return False

        try:
            result = rotate_method(
                retry_after_seconds,
                session_id=session_id,
                upstream_codex_error=upstream_codex_error,
                response_headers=response_headers,
                managed_oauth_account_id=managed_oauth_account_id,
            )
            rotated = await result if inspect.isawaitable(result) else bool(result)
        except Exception as exc:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to rotate managed OAuth account after rate limit: %s",
                    exc,
                    exc_info=True,
                )
            return False

        if rotated:
            token = self._credential_manager.get_access_token()
            if token:
                self.api_key = token
            return True
        return False

    async def _handle_auth_failure_rotation(
        self,
        *,
        session_id: str | None = None,
    ) -> bool:
        """Rotate managed OAuth accounts on repeated auth denials."""
        rotate_method = getattr(self._credential_manager, "handle_auth_failure", None)
        if not callable(rotate_method):
            return False

        try:
            result = rotate_method(session_id=session_id)
            rotated = await result if inspect.isawaitable(result) else bool(result)
        except Exception as exc:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to rotate managed OAuth account after auth failure: %s",
                    exc,
                    exc_info=True,
                )
            return False

        if rotated:
            token = self._credential_manager.get_access_token()
            if token:
                self.api_key = token
            return True
        return False

    async def _handle_forbidden_rotation(
        self,
        *,
        session_id: str | None = None,
    ) -> bool:
        """Rotate managed OAuth accounts after HTTP 403 without auth-failure penalties."""
        rotate_method = getattr(
            self._credential_manager,
            "handle_forbidden_rotation",
            None,
        )
        if not callable(rotate_method):
            return False

        try:
            result = rotate_method(session_id=session_id)
            rotated = await result if inspect.isawaitable(result) else bool(result)
        except Exception as exc:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to rotate managed OAuth account after HTTP 403: %s",
                    exc,
                    exc_info=True,
                )
            return False

        if rotated:
            token = self._credential_manager.get_access_token()
            if token:
                self.api_key = token
            return True
        return False

    # -----------------------------
    # Health Tracking API (stale token handling pattern)
    # -----------------------------
    def is_backend_functional(self) -> bool:
        return self.is_functional and not self._initialization_failed

    def get_validation_errors(self) -> list[str]:
        return self._credential_validation_errors.copy()

    def _fail_init(self, errors: list[str]) -> None:
        self._initialization_failed = True
        self.is_functional = False
        self._credential_validation_errors = errors
        if logger.isEnabledFor(logging.ERROR):
            logger.error("OpenAI Codex initialization failed: %s", "; ".join(errors))

    def _degrade(self, errors: list[str]) -> None:
        self.is_functional = False
        self._credential_validation_errors = errors
        if logger.isEnabledFor(logging.WARNING):
            logger.warning("OpenAI Codex backend degraded: %s", "; ".join(errors))

    def _recover(self) -> None:
        self.is_functional = True
        self._credential_validation_errors = []
        self._last_validation_time = time.time()
        if logger.isEnabledFor(logging.INFO):
            logger.info("OpenAI Codex backend recovered")

    # -----------------------------
    # Validation methods (stale token handling pattern)
    # -----------------------------
    def _validate_credentials_file_exists(self) -> ValidationResult:
        if hasattr(self._credential_manager, "_validate_credentials_file_exists"):
            result = self._credential_manager._validate_credentials_file_exists()  # type: ignore[reportPrivateUsage]
            return cast(ValidationResult, result)
        return ValidationResult.failure("Credential manager not available")

    def _validate_credentials_structure(
        self, credentials: dict[str, Any]
    ) -> ValidationResult:
        if hasattr(self._credential_manager, "_validate_credentials_structure"):
            result = self._credential_manager._validate_credentials_structure(  # type: ignore[reportPrivateUsage]
                credentials
            )
            return cast(ValidationResult, result)
        return ValidationResult.failure("Credential manager not available")

    async def _validate_runtime_credentials(self) -> bool:
        current_time = time.time()
        if current_time - self._last_validation_time < 30:
            return True

        res = self._validate_credentials_file_exists()
        if not res:
            self._credential_validation_errors = res.errors
            return False

        errors = list(res.errors)

        if self._auth_credentials is not None:
            res_struct = self._validate_credentials_structure(self._auth_credentials)
            if not res_struct:
                errors.extend(res_struct.errors)
                self._credential_validation_errors = errors
                return False
        else:
            errors.append("OAuth credentials not loaded in memory")
            self._credential_validation_errors = errors
            return False

        self._credential_validation_errors = []
        self._last_validation_time = current_time
        return True

    # -----------------------------
    # File watching methods (stale token handling pattern)
    # -----------------------------
    def _start_file_watching(self) -> None:
        if os.environ.get("PYTEST_CURRENT_TEST") and os.environ.get(
            "ENABLE_CODEX_FILE_WATCH", ""
        ).lower() not in {"1", "true", "yes"}:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug("Skipping OpenAI Codex credentials watcher under pytest.")
            return
        if self._auth_path is None or self._file_observer is not None:
            return

        try:
            self._file_observer = Observer()
            self._file_observer.daemon = True
            handler = OpenAICredentialsFileHandler(self)
            watch_dir = self._auth_path.parent
            self._file_observer.schedule(handler, str(watch_dir), recursive=False)
            self._file_observer.start()
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Started watching OpenAI Codex credentials directory: %s", watch_dir
                )
        except Exception as exc:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to start file watching for OpenAI Codex credentials: %s",
                    exc,
                    exc_info=True,
                )

    def _stop_file_watching(self) -> None:
        observer = self._file_observer
        if observer is None:
            return

        self._file_observer = None

        try:
            observer.stop()
            # 5.0s timeout to allow clean thread termination
            observer.join(timeout=5.0)

            # Verify thread stopped (safe check for BaseObserver which extends Thread)
            if (
                hasattr(observer, "is_alive")
                and observer.is_alive()  # type: ignore
                and logger.isEnabledFor(logging.WARNING)
            ):
                logger.warning(
                    "OpenAI Codex file watcher thread did not stop within timeout. "
                    "This may cause issues in parallel test execution."
                )
        except Exception as exc:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Error stopping OpenAI Codex file watcher: %s", exc, exc_info=True
                )

    def _schedule_credentials_reload(self) -> None:
        if self._shutdown_requested.is_set():
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug("Skipping reload - shutdown requested")
            return

        if self._reload_scheduling_event.is_set():
            return

        with self._reload_task_lock:
            if (
                self._pending_reload_task is not None
                and not self._pending_reload_task.done()
            ):
                return
            self._reload_scheduling_event.set()

        async def reload_task() -> None:
            try:
                logger.debug("Reloading OpenAI Codex credentials due to file change")
                try:
                    loaded = await self._load_auth(force_reload=True)
                except TypeError:
                    loaded = await self._load_auth()
                if loaded:
                    if self._auth_credentials is not None:
                        res = self._validate_credentials_structure(
                            self._auth_credentials
                        )
                        if res:
                            self._recover()
                        else:
                            self._degrade(res.errors)
                    else:
                        self._degrade(
                            ["Failed to load credentials despite successful file read"]
                        )
                else:
                    self._degrade(["Failed to reload credentials from file"])
            except Exception as exc:
                logger.error(
                    "Error during OpenAI Codex credentials reload: %s",
                    exc,
                    exc_info=True,
                )
                self._degrade([f"Credentials reload failed: {exc}"])

        loop = self._event_loop
        if loop is None:
            try:
                loop = asyncio.get_running_loop()
            except RuntimeError:
                logger.warning(
                    "Cannot schedule credentials reload: no running event loop available.",
                    exc_info=True,
                )
                self._reload_scheduling_event.clear()
                return
            self._event_loop = loop

        if loop.is_closed():
            logger.warning("Cannot schedule credentials reload: event loop is closed.")
            self._reload_scheduling_event.clear()
            return

        def _clear(_: asyncio.Future[Any]) -> None:
            with self._reload_task_lock:
                self._pending_reload_task = None
            self._reload_scheduling_event.clear()

        def _assign_task(task: asyncio.Future[None]) -> None:
            task.add_done_callback(_clear)
            with self._reload_task_lock:
                self._pending_reload_task = task

        try:
            try:
                running_loop = asyncio.get_running_loop()
            except RuntimeError:
                running_loop = None

            if running_loop is loop:
                task = loop.create_task(reload_task())
                _assign_task(task)
                return

            def schedule_task() -> None:
                try:
                    task = loop.create_task(reload_task())
                    _assign_task(task)
                except Exception as exc:
                    logger.warning(
                        "Failed to schedule OpenAI Codex credentials reload: %s",
                        exc,
                        exc_info=True,
                    )
                    self._reload_scheduling_event.clear()

            loop.call_soon_threadsafe(schedule_task)
        except RuntimeError as exc:
            logger.warning(
                "Failed to schedule OpenAI Codex credentials reload: %s",
                exc,
                exc_info=True,
            )
            self._reload_scheduling_event.clear()

    async def _load_auth(self, force_reload: bool = False) -> bool:
        loaded = await self._credential_manager._load_auth(force_reload=force_reload)  # type: ignore[reportPrivateUsage]
        loaded_bool = bool(loaded)
        if loaded_bool:
            token = self._credential_manager.get_access_token()
            if isinstance(token, str) and token:
                self.api_key = token
        return loaded_bool

    async def initialize(self, **kwargs: Any) -> None:  # type: ignore[override]
        """Initialize backend with enhanced validation using stale token handling pattern."""
        logger.info("Initializing OpenAI Codex backend with enhanced validation.")

        self._refresh_settings_from_overrides()

        try:
            self._event_loop = asyncio.get_running_loop()
        except RuntimeError:
            self._event_loop = None

        base = kwargs.get("openai_api_base_url") or kwargs.get("api_base_url")
        if isinstance(base, str) and base:
            self.api_base_url = base

        dir_override = kwargs.get("openai_codex_path")
        if isinstance(dir_override, str) and dir_override:
            self._oauth_dir_override = Path(dir_override)

        res_file = self._validate_credentials_file_exists()
        if not res_file:
            self._fail_init(res_file.errors)
            return

        if not await self._load_auth():
            self._fail_init(["Failed to load credentials despite validation passing"])
            return

        if self._auth_credentials is not None:
            res_struct = self._validate_credentials_structure(self._auth_credentials)
            if not res_struct:
                self._fail_init(res_struct.errors)
                return
        else:
            self._fail_init(["OAuth credentials are None after loading"])
            return

        self._start_file_watching()
        self.is_functional = True
        self._last_validation_time = time.time()
        logger.info("Credentials file validation passed for %s.", self.name)

    async def _prepare_payload(
        self,
        request_data: Any,
        processed_messages: list[Any],
        effective_model: str,
        context: Any = None,
    ) -> dict[str, Any]:
        payload = await super()._prepare_payload(
            request_data, processed_messages, effective_model, context
        )
        if "model" in payload and isinstance(payload["model"], str):
            payload["model"] = strip_vendor_prefix(
                payload["model"], OPENAI_VENDOR_PREFIX
            )
        return payload

    async def chat_completions(
        self,
        request: ConnectorChatCompletionsRequest,
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        # Structural enforcement: check cancellation immediately if coordinator and token provided
        if (
            request.cancellation_coordinator is not None
            and request.cancellation_token is not None
        ):
            request.cancellation_coordinator.ensure_not_cancelled(
                request.cancellation_token
            )

        is_warmup_request, _ = self._usage_window_warmup_managed_account_hint(request)
        warmup_lock_acquired = False
        restore_warmup_credentials: Callable[[], Awaitable[None]] | None = None
        try:
            if is_warmup_request:
                await self._usage_window_warmup_credentials_lock.acquire()
                warmup_lock_acquired = True
            restore_warmup_credentials = (
                await self._prepare_usage_window_warmup_credentials(request)
            )

            request_data = request.request
            processed_messages = list(request.processed_messages)
            effective_model = request.effective_model
            kwargs = dict(request.options) if request.options else {}

            uri_params: dict[str, Any] = {}
            model_for_parsing = effective_model
            if ":" in model_for_parsing and not model_for_parsing.startswith("openai/"):
                model_for_parsing = model_for_parsing.split(":", 1)[1]

            extra_body_early = getattr(request_data, "extra_body", None) or {}
            pre_resolved_uri = extra_body_early.get(RESOLVED_URI_PARAMS_EXTRA_BODY_KEY)
            if isinstance(pre_resolved_uri, dict) and pre_resolved_uri:
                uri_params = dict(pre_resolved_uri)

            if "?" not in model_for_parsing:
                effective_model = strip_vendor_prefix(
                    model_for_parsing, OPENAI_VENDOR_PREFIX
                )
            else:
                try:
                    parsed = parse_model_with_params(model_for_parsing)
                    _, parsed_model, parsed_uri = (
                        parsed.backend_type,
                        parsed.model_name,
                        parsed.uri_params,
                    )
                    if not uri_params:
                        uri_params = parsed_uri
                    effective_model = strip_vendor_prefix(
                        parsed_model, OPENAI_VENDOR_PREFIX
                    )
                except Exception as exc:
                    logger.debug(
                        "Failed to parse model URI params: %s", exc, exc_info=True
                    )
                    if ":" in effective_model:
                        effective_model = effective_model.split(":", 1)[1]
                    effective_model = strip_vendor_prefix(
                        effective_model, OPENAI_VENDOR_PREFIX
                    )
                    if "?" in effective_model:
                        effective_model = effective_model.split("?", 1)[0]

            resolved_reasoning_effort = self._resolve_reasoning_effort(
                effective_model, uri_params, request_data
            )
            resolved_verbosity = self._resolve_verbosity(
                effective_model, uri_params, request_data
            )
            if isinstance(request_data, dict):
                request_data["_codex_resolved_reasoning_effort"] = (
                    resolved_reasoning_effort
                )
                request_data["_codex_resolved_verbosity"] = resolved_verbosity
            else:
                # CanonicalChatRequest / ChatRequest are frozen Pydantic models; normal setattr raises.
                object.__setattr__(
                    request_data,
                    "_codex_resolved_reasoning_effort",
                    resolved_reasoning_effort,
                )
                object.__setattr__(
                    request_data,
                    "_codex_resolved_verbosity",
                    resolved_verbosity,
                )

            ok = await self._validate_runtime_credentials()
            errors = self.get_validation_errors()
            if not ok:
                self._degrade(errors)
                raise HTTPException(
                    status_code=502,
                    detail={
                        "error": "openai_codex_credentials_invalid",
                        "message": "OpenAI Codex credentials validation failed: "
                        f"{'; '.join(errors)}",
                        "details": {
                            "backend": self.name,
                            "validation_errors": errors,
                            "suggestion": "Please check your OAuth credentials file and ensure it contains valid tokens.access_token or OPENAI_API_KEY",
                        },
                    },
                )

            if not self.api_key:
                self._degrade(["OAuth credentials not initialized"])
                raise HTTPException(
                    status_code=502,
                    detail={
                        "error": "openai_codex_credentials_unavailable",
                        "message": "OpenAI Codex credentials not initialized. Backend may have failed to start.",
                        "details": {
                            "backend": self.name,
                            "validation_errors": self.get_validation_errors(),
                            "suggestion": "Check backend initialization logs. Ensure auth.json exists and contains valid tokens.",
                        },
                    },
                )

            if self._is_codex_model(effective_model):
                try:
                    result = cast(
                        ResponseEnvelope | StreamingResponseEnvelope,
                        await self._call_codex_responses_api(
                            request_data=request_data,
                            processed_messages=processed_messages,
                            effective_model=effective_model,
                            domain_request=request_data,
                            options_metadata=(
                                cast(Mapping[str, Any], md_raw)
                                if isinstance(
                                    (md_raw := kwargs.get("metadata")), Mapping
                                )
                                else None
                            ),
                            request_context=request.context,
                        ),
                    )
                    if not self.is_functional:
                        self._recover()
                    return result
                except Exception as exc:
                    status = getattr(exc, "status_code", None)
                    if status in (401, 403) and isinstance(
                        exc, AuthenticationError | HTTPException | InvalidRequestError
                    ):
                        self._degrade([f"Authentication failed: {exc!s}"])
                    raise

            requested_model_raw: str | None = None
            if isinstance(request_data, dict):
                request_model_value = request_data.get("model")
                if isinstance(request_model_value, str):
                    requested_model_raw = request_model_value
            else:
                model_attr = getattr(request_data, "model", None)
                if isinstance(model_attr, str):
                    requested_model_raw = model_attr

            explicit_codex_selection = False
            if isinstance(requested_model_raw, str):
                try:
                    parsed_requested_model = parse_model_with_params(
                        requested_model_raw
                    )
                    explicit_codex_selection = (
                        parsed_requested_model.backend_type.lower()
                        == self.backend_type.lower()
                    )
                except Exception:
                    explicit_codex_selection = requested_model_raw.lower().startswith(
                        f"{self.backend_type.lower()}:"
                    )

            if explicit_codex_selection:
                supported_models = ", ".join(self.SUPPORTED_CODEX_MODELS)
                raise HTTPException(
                    status_code=400,
                    detail={
                        "error": "openai_codex_model_not_supported",
                        "message": (
                            "Model is not supported by the OpenAI Codex backend and was not "
                            "forwarded to metered OpenAI API endpoints."
                        ),
                        "details": {
                            "backend": self.name,
                            "requested_model": effective_model,
                            "supported_models": list(self.SUPPORTED_CODEX_MODELS),
                            "suggestion": (
                                "Use one of the supported Codex model slugs for this backend: "
                                f"{supported_models}"
                            ),
                        },
                    },
                )

            fallback_request = ConnectorChatCompletionsRequest(
                request=request_data,
                processed_messages=processed_messages,
                effective_model=effective_model,
                identity=request.identity,
                cancellation_token=request.cancellation_token,
                cancellation_coordinator=request.cancellation_coordinator,
                context=request.context,
                options=kwargs,
            )
            return await super().chat_completions(fallback_request)
        finally:
            if restore_warmup_credentials is not None:
                await restore_warmup_credentials()
            if warmup_lock_acquired:
                self._usage_window_warmup_credentials_lock.release()

    async def _handle_non_streaming_response(
        self,
        url: str,
        payload: dict[str, Any],
        headers: dict[str, str] | None,
        session_id: str,
        context: Any | None = None,
    ) -> ResponseEnvelope:
        """Override to ensure compatibility state cleanup for non-streaming responses.

        Note: This method is only called for non-Codex fallback paths (when effective_model
        is not a Codex model). For Codex models, cleanup is handled by ResponseExecutor in
        its finally blocks. This override serves as a safety net for edge cases where
        compatibility state might exist in non-Codex paths, though typically compatibility
        state is only created for Codex requests via _call_codex_responses_api.
        """
        compatibility_state = None

        # Extract compatibility state from payload's executor metadata
        # Note: compatibility_state is typically only created for Codex requests,
        # which use ResponseExecutor directly. This method handles non-Codex models
        # that fall back to parent OpenAI connector.
        if isinstance(payload, dict):  # type: ignore[reportUnnecessaryIsInstance]
            metadata = payload.get("metadata", {})
            if isinstance(metadata, dict):  # type: ignore[reportUnnecessaryIsInstance]
                compatibility_state = metadata.get("compatibility_state")

        try:
            # Call parent implementation
            result = await super()._handle_non_streaming_response(
                url, payload, headers, session_id, context
            )

            # Clean up compatibility state to prevent memory leaks
            if self._compatibility_layer and compatibility_state:
                try:
                    await self._compatibility_layer.cleanup_state(compatibility_state)
                except Exception as exc:
                    logger.debug(
                        "Failed to cleanup compatibility state for non-streaming response: %s",
                        exc,
                        exc_info=True,
                    )

            return result

        except asyncio.CancelledError:
            # Ensure cleanup even if parent is cancelled
            if self._compatibility_layer and compatibility_state:
                try:
                    await self._compatibility_layer.cleanup_state(compatibility_state)
                except Exception as exc:
                    logger.debug(
                        "Failed to cleanup compatibility state during cancellation: %s",
                        exc,
                        exc_info=True,
                    )
            raise
        except Exception as e:
            # Ensure cleanup even if parent fails
            # Log the outer exception for debugging before ensuring cleanup
            logger.debug(
                "Exception in _handle_non_streaming_response, ensuring cleanup: %s",
                e,
                exc_info=True,
            )
            if self._compatibility_layer and compatibility_state:
                try:
                    await self._compatibility_layer.cleanup_state(compatibility_state)
                except Exception as exc:
                    logger.debug(
                        "Failed to cleanup compatibility state during error handling: %s",
                        exc,
                        exc_info=True,
                    )
            raise

    def get_available_models(self) -> list[str]:
        return [
            add_vendor_prefix(m, OPENAI_VENDOR_PREFIX)
            for m in self.SUPPORTED_CODEX_MODELS
        ]

    def __del__(self) -> None:
        self._stop_file_watching()

    async def shutdown(self) -> None:
        """Stop background file watchers to avoid thread leaks."""
        # 1. Signal shutdown to prevent new tasks
        self._shutdown_requested.set()

        # 2. Cancel local pending reload tasks
        pending_task: asyncio.Future[Any] | None = None
        with self._reload_task_lock:
            pending_task = self._pending_reload_task
            self._pending_reload_task = None
        if pending_task is not None and not pending_task.done():
            pending_task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await pending_task
        self._reload_scheduling_event.clear()

        # 3. Stop local file watcher synchronously
        self._stop_file_watching()

        # 4. Clean up WebSocket connections in response executor
        if self._response_executor and hasattr(self._response_executor, "_transport"):
            transport = getattr(self._response_executor, "_transport", None)
            if transport and hasattr(transport, "cleanup"):
                try:
                    await transport.cleanup()
                except Exception as e:
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            "Error during WebSocket transport cleanup: %s",
                            e,
                            exc_info=True,
                        )

        # 5. Stop delegated credential manager
        if hasattr(self._credential_manager, "shutdown"):
            await self._credential_manager.shutdown()


backend_registry.register_backend("openai-codex", OpenAICodexConnector)


_OPENAI_CODEX_FALLBACK_MODELS: tuple[str, ...] = (
    "gpt-5.5",
    "gpt-5.4",
    "gpt-5.3-codex",
    "gpt-5.2-codex",
    "gpt-5.1-codex",
    "gpt-5-codex",
    "gpt-5.1-codex-mini",
    "gpt-5.1-codex-max",
    "gpt-5.3-codex-spark",
    "gpt-5",
    "gpt-5-nano",
)


def _normalize_codex_model_name(name: str) -> str:
    cleaned = name.strip()
    for prefix in ("openai-codex-v2", "openai-codex", "openai"):
        if cleaned.startswith(f"{prefix}/"):
            cleaned = cleaned[len(prefix) + 1 :]
            break
    return add_vendor_prefix(cleaned, "openai")


class OpenAICodexConfiguredModelEnumerator:
    """Enumerate canonical OpenAI Codex routes for configured backend instances."""

    def __init__(
        self,
        *,
        catalog: Any = None,
        catalog_source: str | None = None,
    ) -> None:
        self._catalog = catalog
        self._catalog_source = catalog_source

    def _has_credentials(self, instance_name: str, config: BackendConfig) -> bool:
        extra = config.extra or {}
        # 1. Direct API key
        if config.api_key or extra.get("api_key"):
            return True
        if os.environ.get("OPENAI_API_KEY") or os.environ.get("OPENAI_CODEX_API_KEY"):
            return True
        suffix = instance_name.split(".", 1)[-1] if "." in instance_name else None
        if suffix and (
            os.environ.get(f"OPENAI_API_KEY_{suffix}")
            or os.environ.get(f"OPENAI_CODEX_API_KEY_{suffix}")
        ):
            return True

        # 2. Managed OAuth accounts storage
        managed_cfg = (
            extra.get("managed_oauth")
            if isinstance(extra.get("managed_oauth"), dict)
            else {}
        )
        storage_path_val = (
            managed_cfg.get("storage_path") if isinstance(managed_cfg, dict) else None
        )
        storage_path_str = (
            storage_path_val
            or extra.get("storage_path")
            or "var/openai_codex_oauth_accounts"
        )
        storage_path = Path(storage_path_str)
        try:
            if (
                storage_path.exists()
                and storage_path.is_dir()
                and any(
                    p.is_file() and p.suffix == ".json" for p in storage_path.iterdir()
                )
            ):
                return True
        except Exception:
            pass

        # 3. Legacy auth.json files
        custom_path = (
            getattr(config, "credentials_path", None)
            or extra.get("openai_codex_path")
            or extra.get("auth_path")
            or os.environ.get("OPENAI_CODEX_AUTH_PATH")
            or os.environ.get("OPENAI_AUTH_PATH")
        )
        candidates: list[Path] = []
        if custom_path:
            p = Path(custom_path).expanduser()
            if p.is_dir():
                candidates.append(p / "auth.json")
            else:
                candidates.append(p)
        else:
            if sys.platform == "win32" or os.name == "nt":
                userprofile = os.environ.get("USERPROFILE")
                if userprofile:
                    candidates.append(Path(userprofile) / ".codex" / "auth.json")
                localappdata = os.environ.get("LOCALAPPDATA")
                if localappdata:
                    candidates.append(Path(localappdata) / "codex" / "auth.json")
            candidates.append(Path.home() / ".codex" / "auth.json")
            candidates.append(Path.home() / ".local" / "share" / "codex" / "auth.json")

        for path in candidates:
            try:
                if path.exists() and path.is_file():
                    with open(path, encoding="utf-8") as f:
                        data = json.load(f)
                    if isinstance(data, dict) and (
                        data.get("tokens")
                        or data.get("access_token")
                        or data.get("token")
                        or data.get("refresh_token")
                        or data.get("user")
                        or data.get("OPENAI_API_KEY")
                    ):
                        return True
            except Exception:
                pass

        return False

    def _resolve_catalog(self) -> tuple[Any, str]:
        if self._catalog is not None:
            source = self._catalog_source or "catalog"
            return self._catalog, source
        try:
            from typing import cast

            from src.connectors.openai_codex.catalog.interfaces import (
                ICodexModelCatalog,
            )
            from src.connectors.openai_codex.catalog.provider import (
                CodexModelCatalogProvider,
            )
            from src.core.di.services import get_or_build_service_provider

            provider = get_or_build_service_provider()
            catalog_provider = provider.get_service(CodexModelCatalogProvider)
            if catalog_provider is not None:
                return (
                    catalog_provider.get_catalog(),
                    catalog_provider.get_catalog_source(),
                )
            catalog = provider.get_service(cast(type[Any], ICodexModelCatalog))
            if catalog is not None and "mock" not in type(catalog).__name__.lower():
                return catalog, "discovery"
        except Exception:
            pass
        try:
            from src.connectors.openai_codex.catalog.fallback_loader import (
                CodexCatalogFallbackLoader,
            )

            return CodexCatalogFallbackLoader().load(), "fallback"
        except Exception:
            return None, "fallback"

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        extra = config.extra or {}
        connector = (
            str(getattr(config, "connector", "") or "").strip()
            or instance_name.split(".", 1)[0]
        )
        configured_models = config.models or extra.get("models")
        if isinstance(configured_models, list | tuple) and configured_models:
            models = [
                _normalize_codex_model_name(str(m))
                for m in configured_models
                if str(m).strip()
            ]
            return BackendModelEnumeration.available(
                instance_name=instance_name,
                connector=connector,
                models=models,
                source=f"{connector}_configured",
                instance_pinned=False,
            )

        if not self._has_credentials(instance_name, config):
            return BackendModelEnumeration.unavailable(
                instance_name=instance_name,
                connector=connector,
                source=f"{connector}_discovery",
                error_code="missing_credentials",
                instance_pinned=False,
            )

        catalog, source = self._resolve_catalog()
        if catalog is not None:
            routable = catalog.routable_slugs()
            if routable:
                models = [add_vendor_prefix(str(slug), "openai") for slug in routable]
                return BackendModelEnumeration.available(
                    instance_name=instance_name,
                    connector=connector,
                    models=models,
                    source=f"codex_{source}",
                    instance_pinned=False,
                )

        models = [
            add_vendor_prefix(slug, "openai") for slug in _OPENAI_CODEX_FALLBACK_MODELS
        ]
        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector=connector,
            models=models,
            source=f"{connector}_curated",
            instance_pinned=False,
        )
