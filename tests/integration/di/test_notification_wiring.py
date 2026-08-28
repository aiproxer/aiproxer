"""Integration test to verify DI wiring of the notification service."""

from __future__ import annotations

from src.core.config.app_config import AppConfig
from src.core.di.container import ServiceCollection
from src.core.di.registrations import register_all
from src.core.interfaces.notification_service_interface import INotificationService
from src.core.services.notification_service import NotificationService


def test_notification_service_is_registered_and_resolvable():
    """Verify that INotificationService can be resolved from the DI container."""
    services = ServiceCollection()

    # Use standard registration path
    register_all(services, AppConfig())

    provider = services.build_service_provider()

    # Resolve by interface
    notif_service = provider.get_service(INotificationService)
    assert notif_service is not None
    assert isinstance(notif_service, NotificationService)

    # Resolve by concrete type
    concrete_service = provider.get_service(NotificationService)
    assert concrete_service is not None
    assert concrete_service is notif_service


def test_notification_service_wired_with_default_provider():
    """Verify that the resolved service has a provider (DesktopNotifierProvider by default)."""
    services = ServiceCollection()
    register_all(services, AppConfig())
    provider = services.build_service_provider()

    notif_service = provider.get_required_service(NotificationService)

    # Check if provider is set (internal attribute)
    assert hasattr(notif_service, "_provider")
    assert notif_service._provider is not None
    from src.core.services.notifications.providers.desktop_notifier import (
        DesktopNotifierProvider,
    )

    assert isinstance(notif_service._provider, DesktopNotifierProvider)
