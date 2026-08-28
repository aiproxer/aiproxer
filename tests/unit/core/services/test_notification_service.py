"""Unit tests for the NotificationService and providers."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from src.core.config.models.notification import NotificationConfig
from src.core.services.notification_service import NotificationService
from src.core.services.notifications.providers.desktop_notifier import (
    DesktopNotifierProvider,
)


@pytest.fixture
def mock_config():
    return NotificationConfig(enabled=True)


@pytest.fixture
def disabled_config():
    return NotificationConfig(enabled=False)


@pytest.mark.asyncio
async def test_notification_service_delegates_to_provider(mock_config):
    """Verify that NotificationService delegates to the provider."""
    mock_provider = MagicMock()
    mock_provider.send = AsyncMock(return_value="sent-id")

    service = NotificationService(
        config=mock_config, host="127.0.0.1", provider=mock_provider
    )
    assert service.is_enabled is True

    result = await service.send_notification("Title", "Message")

    assert result == "sent-id"
    mock_provider.send.assert_called_once_with(
        title="Title", message="Message", url=None, url_label="Open link"
    )


@pytest.mark.asyncio
async def test_notification_service_skips_when_disabled(disabled_config):
    """Verify that NotificationService skips when disabled."""
    mock_provider = MagicMock()
    service = NotificationService(
        config=disabled_config, host="127.0.0.1", provider=mock_provider
    )

    result = await service.send_notification("Title", "Message")

    assert result is None
    mock_provider.send.assert_not_called()


@pytest.mark.asyncio
async def test_desktop_notifier_provider_sends():
    """Verify that DesktopNotifierProvider interacts with the library."""
    with patch(
        "src.core.services.notifications.providers.desktop_notifier.DesktopNotifier"
    ) as mock_notifier_cls:
        mock_notifier = MagicMock()
        mock_notifier.send = AsyncMock(return_value="notif-123")
        mock_notifier_cls.return_value = mock_notifier

        provider = DesktopNotifierProvider()
        result = await provider.send("Title", "Message")

        assert result == "notif-123"
        mock_notifier.send.assert_called_once_with(
            title="Title", message="Message", buttons=(), on_clicked=None
        )


@pytest.mark.asyncio
async def test_notification_service_uses_default_provider(mock_config):
    """Verify that NotificationService uses DesktopNotifierProvider by default."""
    with patch(
        "src.core.services.notification_service.DesktopNotifierProvider"
    ) as mock_provider_cls:
        mock_provider = MagicMock()
        mock_provider_cls.return_value = mock_provider

        service = NotificationService(config=mock_config, host="127.0.0.1")
        assert isinstance(service._provider, MagicMock)
        mock_provider_cls.assert_called_once()
