"""Quality Verifier service factory interface."""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from src.core.interfaces.notification_service_interface import INotificationService
from src.core.services.quality_verifier_service import QualityVerifierService


class IQualityVerifierServiceFactory(ABC):
    """Factory interface for creating QualityVerifierService instances."""

    @abstractmethod
    def create(
        self,
        model_spec: str,
        max_history: int | None = None,
        max_consecutive_failures: int = 5,
        cooldown_seconds: int = 300,
        notification_service: INotificationService | None = None,
    ) -> QualityVerifierService:
        """Create a QualityVerifierService for the provided model specification."""
        raise NotImplementedError
