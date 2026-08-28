"""
Persistence registrar.

Registers database configuration, engines, repositories, and memory subsystem services.
"""

from __future__ import annotations

import logging
from typing import cast

from src.core.config.app_config import AppConfig
from src.core.di.container import ServiceCollection
from src.core.di.registrations._shared import (
    register_singleton_if_absent,
)
from src.core.interfaces.di_interface import IServiceProvider

logger = logging.getLogger(__name__)


def register(services: ServiceCollection, app_config: AppConfig | None) -> None:
    """Register persistence services.

    This registrar handles:
    - Database configuration and engine
    - Repositories (session, usage, SSO, etc.)
    - Memory subsystem

    Args:
        services: The service collection to register into
        app_config: Optional application configuration
    """
    # Register database configuration and engine
    _register_database_config(services, app_config)
    _register_database_engine(services)

    # Register repositories
    _register_repositories(services)

    # Register session metrics services
    _register_session_metrics_services(services)

    # Register memory subsystem (config-gated)
    _register_memory_subsystem(services, app_config)


def _register_database_config(
    services: ServiceCollection, app_config: AppConfig | None
) -> None:
    """Register DatabaseConfig from AppConfig."""
    # Local import to avoid import-time side effects
    from src.core.database.config import DatabaseConfig

    def database_config_factory(provider: IServiceProvider) -> DatabaseConfig:
        """Factory to create DatabaseConfig from AppConfig."""
        if app_config is not None:
            return app_config.database
        # Fallback to default config if app_config is None
        return DatabaseConfig()

    register_singleton_if_absent(
        services, DatabaseConfig, implementation_factory=database_config_factory
    )


def _register_database_engine(services: ServiceCollection) -> None:
    """Register DatabaseEngine with DatabaseConfig dependency."""
    # Local import to avoid import-time side effects
    from src.core.database.engine import DatabaseEngine

    def database_engine_factory(provider: IServiceProvider) -> DatabaseEngine:
        """Factory to create DatabaseEngine from DatabaseConfig."""
        from src.core.database.config import DatabaseConfig

        database_config = provider.get_required_service(DatabaseConfig)
        return DatabaseEngine(database_config)

    register_singleton_if_absent(
        services, DatabaseEngine, implementation_factory=database_engine_factory
    )


def _register_repositories(services: ServiceCollection) -> None:
    """Register all repository types with DatabaseEngine dependency."""
    # Local imports to avoid import-time side effects
    from src.core.database.engine import DatabaseEngine
    from src.core.database.repositories.backend_quota_repository import (
        BackendQuotaRepository,
    )
    from src.core.database.repositories.memory_repository import (
        SQLModelMemoryRepository,
    )
    from src.core.database.repositories.sso_repository import (
        SQLModelAuthorizationRepository,
        SQLModelRateLimitRepository,
        SQLModelTokenRepository,
    )
    from src.core.database.repositories.usage_repository import (
        SessionMetricsRepository,
        UsageRecordRepository,
    )

    def usage_record_repository_factory(
        provider: IServiceProvider,
    ) -> UsageRecordRepository:
        """Factory to create UsageRecordRepository."""
        engine = provider.get_required_service(DatabaseEngine)
        return UsageRecordRepository(engine)

    def backend_quota_repository_factory(
        provider: IServiceProvider,
    ) -> BackendQuotaRepository:
        """Factory to create BackendQuotaRepository."""
        engine = provider.get_required_service(DatabaseEngine)
        return BackendQuotaRepository(engine)

    def session_metrics_repository_factory(
        provider: IServiceProvider,
    ) -> SessionMetricsRepository:
        """Factory to create SessionMetricsRepository."""
        engine = provider.get_required_service(DatabaseEngine)
        return SessionMetricsRepository(engine)

    def memory_repository_factory(
        provider: IServiceProvider,
    ) -> SQLModelMemoryRepository:
        """Factory to create SQLModelMemoryRepository."""
        engine = provider.get_required_service(DatabaseEngine)
        return SQLModelMemoryRepository(engine)

    def token_repository_factory(
        provider: IServiceProvider,
    ) -> SQLModelTokenRepository:
        """Factory to create SQLModelTokenRepository."""
        engine = provider.get_required_service(DatabaseEngine)
        return SQLModelTokenRepository(engine)

    def authorization_repository_factory(
        provider: IServiceProvider,
    ) -> SQLModelAuthorizationRepository:
        """Factory to create SQLModelAuthorizationRepository."""
        engine = provider.get_required_service(DatabaseEngine)
        return SQLModelAuthorizationRepository(engine)

    def rate_limit_repository_factory(
        provider: IServiceProvider,
    ) -> SQLModelRateLimitRepository:
        """Factory to create SQLModelRateLimitRepository."""
        engine = provider.get_required_service(DatabaseEngine)
        return SQLModelRateLimitRepository(engine)

    # Register all repositories as singletons
    register_singleton_if_absent(
        services,
        UsageRecordRepository,
        implementation_factory=usage_record_repository_factory,
    )
    register_singleton_if_absent(
        services,
        SessionMetricsRepository,
        implementation_factory=session_metrics_repository_factory,
    )
    register_singleton_if_absent(
        services,
        BackendQuotaRepository,
        implementation_factory=backend_quota_repository_factory,
    )
    register_singleton_if_absent(
        services,
        SQLModelMemoryRepository,
        implementation_factory=memory_repository_factory,
    )
    register_singleton_if_absent(
        services,
        SQLModelTokenRepository,
        implementation_factory=token_repository_factory,
    )
    register_singleton_if_absent(
        services,
        SQLModelAuthorizationRepository,
        implementation_factory=authorization_repository_factory,
    )
    register_singleton_if_absent(
        services,
        SQLModelRateLimitRepository,
        implementation_factory=rate_limit_repository_factory,
    )


def _register_session_metrics_services(services: ServiceCollection) -> None:
    """Register session metrics initialization services."""
    # Local imports to avoid import-time side effects
    from typing import cast

    from src.core.database.repositories.usage_repository import (
        SessionMetricsRepository,
    )
    from src.core.interfaces.session_metrics_initializer_interface import (
        ISessionMetricsInitializer,
    )
    from src.core.services.session_metrics_initializer import (
        SessionMetricsInitializer,
    )

    def session_metrics_initializer_factory(
        provider: IServiceProvider,
    ) -> SessionMetricsInitializer:
        """Factory to create SessionMetricsInitializer."""
        session_repository = provider.get_required_service(SessionMetricsRepository)
        return SessionMetricsInitializer(session_repository=session_repository)

    # Register concrete implementation
    register_singleton_if_absent(
        services,
        SessionMetricsInitializer,
        implementation_factory=session_metrics_initializer_factory,
    )

    # Register interface bound to implementation
    register_singleton_if_absent(
        services,
        cast(type, ISessionMetricsInitializer),
        implementation_factory=lambda provider: provider.get_required_service(
            SessionMetricsInitializer
        ),  # type: ignore[type-abstract]
    )


def _register_memory_subsystem(
    services: ServiceCollection, app_config: AppConfig | None
) -> None:
    """Register memory subsystem services (config-gated).

    Only registers memory services if memory.available is True in config.
    """
    # Local imports to avoid import-time side effects
    from src.core.database.repositories.memory_repository import (
        SQLModelMemoryRepository,
    )
    from src.core.interfaces.memory_service_interface import IMemoryService
    from src.core.memory.capture_middleware import MemoryCaptureMiddleware
    from src.core.memory.config import MemoryConfiguration
    from src.core.memory.injection_middleware import ContextInjectionMiddleware
    from src.core.memory.repository import IMemoryRepository
    from src.core.memory.service import MemoryService

    # Check if memory is enabled
    if app_config is None or not app_config.memory.available:
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug("Memory subsystem is disabled, skipping registration")
        return

    # Register MemoryConfiguration
    def memory_config_factory(provider: IServiceProvider) -> MemoryConfiguration:
        """Factory to create MemoryConfiguration from AppConfig."""
        if app_config is not None:
            return app_config.memory
        return MemoryConfiguration()

    register_singleton_if_absent(
        services, MemoryConfiguration, implementation_factory=memory_config_factory
    )

    # Register IMemoryRepository (bound to SQLModelMemoryRepository)
    def memory_repository_interface_factory(
        provider: IServiceProvider,
    ) -> SQLModelMemoryRepository:
        """Factory to resolve IMemoryRepository interface."""
        return provider.get_required_service(SQLModelMemoryRepository)

    register_singleton_if_absent(
        services,
        cast(type, IMemoryRepository),
        implementation_factory=memory_repository_interface_factory,  # type: ignore[type-abstract]
    )

    # Register MemoryService
    def memory_service_factory(provider: IServiceProvider) -> MemoryService:
        """Factory to create MemoryService."""
        memory_config = provider.get_required_service(MemoryConfiguration)
        memory_repo: IMemoryRepository = provider.get_required_service(
            cast(type, IMemoryRepository)
        )
        return MemoryService(config=memory_config, repository=memory_repo)

    register_singleton_if_absent(
        services, MemoryService, implementation_factory=memory_service_factory
    )

    # Register IMemoryService interface
    register_singleton_if_absent(
        services,
        cast(type, IMemoryService),
        implementation_factory=lambda provider: provider.get_required_service(
            MemoryService
        ),  # type: ignore[type-abstract]
    )

    # Register memory middleware
    def memory_capture_middleware_factory(
        provider: IServiceProvider,
    ) -> MemoryCaptureMiddleware:
        """Factory to create MemoryCaptureMiddleware."""
        from src.core.interfaces.memory_service_interface import IMemoryService

        memory_service: IMemoryService = provider.get_required_service(
            cast(type, IMemoryService)
        )
        memory_config = provider.get_required_service(MemoryConfiguration)
        return MemoryCaptureMiddleware(
            memory_service=memory_service, config=memory_config
        )

    register_singleton_if_absent(
        services,
        MemoryCaptureMiddleware,
        implementation_factory=memory_capture_middleware_factory,
    )

    def context_injection_middleware_factory(
        provider: IServiceProvider,
    ) -> ContextInjectionMiddleware:
        """Factory to create ContextInjectionMiddleware."""
        from src.core.interfaces.memory_service_interface import IMemoryService
        from src.core.memory.context_injector import ContextInjector

        memory_service: IMemoryService = provider.get_required_service(
            cast(type, IMemoryService)
        )
        memory_config = provider.get_required_service(MemoryConfiguration)
        memory_repo: IMemoryRepository = provider.get_required_service(
            cast(type, IMemoryRepository)
        )
        context_injector = ContextInjector(config=memory_config, repository=memory_repo)
        return ContextInjectionMiddleware(
            memory_service=memory_service,
            context_injector=context_injector,
            config=memory_config,
        )

    register_singleton_if_absent(
        services,
        ContextInjectionMiddleware,
        implementation_factory=context_injection_middleware_factory,
    )

    # Register ProxyMemEosSubscriber
    from src.core.memory.eos_subscriber import ProxyMemEosSubscriber

    def proxymem_eos_subscriber_factory(
        provider: IServiceProvider,
    ) -> ProxyMemEosSubscriber:
        """Factory to create ProxyMemEosSubscriber."""
        from src.core.interfaces.event_bus_interface import IEventBus
        from src.core.interfaces.memory_service_interface import IMemoryService

        event_bus: IEventBus = provider.get_required_service(cast(type, IEventBus))
        memory_service: IMemoryService = provider.get_required_service(
            cast(type, IMemoryService)
        )
        return ProxyMemEosSubscriber(event_bus=event_bus, memory_service=memory_service)

    register_singleton_if_absent(
        services,
        ProxyMemEosSubscriber,
        implementation_factory=proxymem_eos_subscriber_factory,
    )
