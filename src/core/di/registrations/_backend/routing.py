"""
Backend routing and discovery registration helpers.

Handles registration of:
- Backend Routing Service
"""

from __future__ import annotations

import logging
from typing import cast

from src.core.config.app_config import AppConfig
from src.core.di.container import ServiceCollection
from src.core.di.registrations._shared import register_singleton_if_absent
from src.core.interfaces.di_interface import IServiceProvider

logger = logging.getLogger(__name__)


def register_backend_routing_service(services: ServiceCollection) -> None:
    """Register BackendRoutingService for discovery/routing decisions."""
    try:
        from src.core.services.backend_routing_service import BackendRoutingService

        def _routing_service_factory(
            provider: IServiceProvider,
        ) -> BackendRoutingService:
            from src.connectors.agy_cli_acp import (
                AgyCliConfiguredModelEnumerator,
            )
            from src.connectors.commandcode_anthropic import (
                CommandCodeAnthropicConfiguredModelEnumerator,
            )
            from src.connectors.commandcode_openai import (
                CommandCodeOpenAIConfiguredModelEnumerator,
            )
            from src.connectors.cursor_cli_acp import (
                CursorCliConfiguredModelEnumerator,
            )
            from src.connectors.eve_acp import (
                EveConfiguredModelEnumerator,
            )
            from src.connectors.freebuff_cli_acp import (
                FreebuffCliConfiguredModelEnumerator,
            )
            from src.connectors.nvidia import (
                NvidiaConfiguredModelEnumerator,
            )
            from src.connectors.openai_codex.catalog.provider import (
                CodexModelCatalogProvider,
            )
            from src.connectors.opencode_go import (
                OpencodeGoConfiguredModelEnumerator,
            )
            from src.connectors.runinfra import (
                RuninfraConfiguredModelEnumerator,
            )
            from src.connectors.workbuddy_acp import (
                WorkBuddyConfiguredModelEnumerator,
            )
            from src.core.config.models import RoutingConfig
            from src.core.interfaces.backend_config_provider_interface import (
                IBackendConfigProvider,
            )
            from src.core.interfaces.backend_lifecycle_manager_interface import (
                IBackendLifecycleManager,
            )
            from src.core.interfaces.resilience_interface import IResilienceCoordinator
            from src.core.services.configured_backend_model_enumerators import (
                CodexAppServerConfiguredModelEnumerator,
                ExplicitConfiguredModelEnumerator,
                OpenAICodexConfiguredModelEnumerator,
                OpencodeZenConfiguredModelEnumerator,
            )
            from src.core.services.model_capability_index import (
                BackendModelEnumeratorRegistry,
                ModelCapabilityDiscoverer,
            )

            config = provider.get_required_service(AppConfig)
            routing_cfg: RoutingConfig | None = getattr(config, "routing", None)
            backend_cfg_provider: IBackendConfigProvider = (
                provider.get_required_service(cast(type, IBackendConfigProvider))
            )
            lifecycle_manager = provider.get_service(
                cast(type, IBackendLifecycleManager)
            )
            resilience_coordinator = provider.get_service(
                cast(type, IResilienceCoordinator)
            )
            enumerators = BackendModelEnumeratorRegistry()
            enumerators.register(
                "cursor-cli-acp",
                CursorCliConfiguredModelEnumerator(),
                timeout_seconds=None,
            )
            enumerators.register(
                "gemini-cli-acp",
                ExplicitConfiguredModelEnumerator(connector="gemini-cli-acp"),
            )
            enumerators.register(
                "agy-cli-acp",
                AgyCliConfiguredModelEnumerator(),
                timeout_seconds=None,
            )
            enumerators.register(
                "eve-acp",
                EveConfiguredModelEnumerator(),
                timeout_seconds=None,
            )
            enumerators.register(
                "freebuff-cli-acp",
                FreebuffCliConfiguredModelEnumerator(),
                timeout_seconds=None,
            )
            wb_enumerator = WorkBuddyConfiguredModelEnumerator()
            enumerators.register(
                "workbuddy-acp",
                wb_enumerator,
                timeout_seconds=None,
            )
            enumerators.register(
                "workbuddy_acp",
                wb_enumerator,
                timeout_seconds=None,
            )
            enumerators.register(
                "opencode-go",
                OpencodeGoConfiguredModelEnumerator(),
                timeout_seconds=None,
            )
            enumerators.register(
                "opencode-zen",
                OpencodeZenConfiguredModelEnumerator(),
                timeout_seconds=None,
            )
            enumerators.register(
                "commandcode-openai",
                CommandCodeOpenAIConfiguredModelEnumerator(),
                timeout_seconds=None,
            )
            enumerators.register(
                "commandcode",
                CommandCodeOpenAIConfiguredModelEnumerator(),
                timeout_seconds=None,
            )
            enumerators.register(
                "commandcode-anthropic",
                CommandCodeAnthropicConfiguredModelEnumerator(),
                timeout_seconds=None,
            )
            enumerators.register(
                "nvidia",
                NvidiaConfiguredModelEnumerator(),
                timeout_seconds=None,
            )
            enumerators.register(
                "runinfra",
                RuninfraConfiguredModelEnumerator(),
                timeout_seconds=None,
            )
            codex_catalog_provider = provider.get_service(CodexModelCatalogProvider)
            if codex_catalog_provider is not None:
                catalog = codex_catalog_provider.get_catalog()
                catalog_source = codex_catalog_provider.get_catalog_source()
                enumerators.register(
                    "openai-codex-app-server",
                    CodexAppServerConfiguredModelEnumerator(
                        catalog=catalog,
                        catalog_source=catalog_source,
                    ),
                )
            else:
                catalog = None
                catalog_source = None

            codex_enumerator = OpenAICodexConfiguredModelEnumerator(
                catalog=catalog,
                catalog_source=catalog_source,
            )
            enumerators.register("openai-codex", codex_enumerator, timeout_seconds=None)
            enumerators.register("openai_codex", codex_enumerator, timeout_seconds=None)
            enumerators.register(
                "openai-codex-v2", codex_enumerator, timeout_seconds=None
            )
            enumerators.register(
                "openai_codex_v2", codex_enumerator, timeout_seconds=None
            )
            discoverer = ModelCapabilityDiscoverer(
                config_provider=backend_cfg_provider,
                backend_lifecycle_manager=lifecycle_manager,
                enumerator_registry=enumerators,
            )
            return BackendRoutingService(
                config_provider=backend_cfg_provider,
                routing_config=routing_cfg,
                capability_discoverer=discoverer,
                backend_lifecycle_manager=lifecycle_manager,
                resilience_coordinator=resilience_coordinator,
            )

        register_singleton_if_absent(
            services,
            BackendRoutingService,
            implementation_factory=_routing_service_factory,
        )
    except (ImportError, AttributeError, TypeError, RuntimeError):
        # Specific exceptions during service registration:
        # - ImportError: Module not found or import errors
        # - AttributeError: Missing attributes during config/provider resolution
        # - TypeError: Incorrect types during registration
        # - RuntimeError: Runtime errors during factory execution
        logger.exception("Failed to register BackendRoutingService")
        raise
