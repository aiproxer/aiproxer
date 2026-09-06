"""
Backend services initialization stage.

This stage registers backend-related services via DI registrar and delegates validation.
"""

from __future__ import annotations

import logging
from typing import cast

from src.core.config.app_config import AppConfig
from src.core.di.container import ServiceCollection
from src.core.interfaces.backend_validator_interface import IBackendValidator

from .base import InitializationStage

logger = logging.getLogger(__name__)


class BackendStage(InitializationStage):
    """
    Stage for registering backend-related services via DI registrar and delegating validation.
    """

    @property
    def name(self) -> str:
        return "backends"

    def get_dependencies(self) -> list[str]:
        return ["infrastructure", "core_services"]

    def get_description(self) -> str:
        return "Register backend services (registry, factory, service)"

    async def execute(self, services: ServiceCollection, config: AppConfig) -> None:
        """Register backend services."""
        if logger.isEnabledFor(logging.INFO):
            logger.info("Initializing backend services...")

        # Backend registrations are now handled by backend registrar
        # Register backend services via registrar
        from src.core.di.registrations import backend

        backend.register(services, config)

        if logger.isEnabledFor(logging.INFO):
            logger.info("Backend services initialized successfully")

    async def validate(self, services: ServiceCollection, config: AppConfig) -> bool:
        """Validate that backend services can be registered and backends are functional."""
        from src.core.di.provider_lifecycle import get_current_service_provider

        # Resolve IBackendValidator from the validation provider installed by ApplicationBuilder
        provider = get_current_service_provider()
        validator: IBackendValidator = provider.get_required_service(
            cast(type, IBackendValidator)
        )

        # Delegate validation to the backend validation service
        is_valid = bool(await validator.validate_all(config))
        return is_valid
