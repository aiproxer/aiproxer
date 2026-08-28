"""Implementation of desktop notification service following SOLID principles."""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING

from src.core.interfaces.notification_service_interface import INotificationService
from src.core.services.notifications.providers.desktop_notifier import (
    DesktopNotifierProvider,
)

if TYPE_CHECKING:
    from src.core.config.models.notification import NotificationConfig
    from src.core.interfaces.notification_provider_interface import (
        INotificationProvider,
    )

logger = logging.getLogger(__name__)


class NotificationService(INotificationService):
    """Notification service that coordinates delivery via providers.

    Follows SOLID principles:
    - Single Responsibility: Manages notification logic (enabled/disabled check).
    - Open/Closed: Can support new delivery mechanisms by adding new providers.
    - Liskov Substitution: Works with any INotificationProvider.
    - Interface Segregation: Implements minimal INotificationService.
    - Dependency Inversion: Depends on INotificationProvider interface, not library.
    """

    def __init__(
        self,
        config: NotificationConfig,
        host: str,
        provider: INotificationProvider | None = None,
    ) -> None:
        """Initialize the notification service.

        Args:
            config: Notification configuration model.
            host: The host address the proxy is bound to (used for auto-detection).
            provider: Optional delivery provider. Defaults to DesktopNotifierProvider.
        """
        self._config = config
        self._host = host
        self._enabled = config.is_enabled(host)

        # Use provided provider or default to DesktopNotifierProvider
        self._provider = provider or DesktopNotifierProvider()

        if self._enabled:
            logger.debug(
                "NotificationService initialized (enabled=True, provider=%s)",
                self._provider.__class__.__name__,
            )
        else:
            logger.debug("NotificationService initialized (enabled=False)")

    async def send_notification(
        self,
        title: str,
        message: str,
        *,
        url: str | None = None,
        url_label: str = "Open link",
    ) -> str | None:
        """Send a notification if enabled.

        Delegates actual delivery to the injected provider.
        """
        if not self._enabled:
            return None

        try:
            return await self._provider.send(
                title=title,
                message=message,
                url=url,
                url_label=url_label,
            )
        except Exception as e:
            logger.debug("NotificationService provider failed: %s", e)
            return None

    @property
    def is_enabled(self) -> bool:
        """Whether notifications are currently enabled."""
        return self._enabled
