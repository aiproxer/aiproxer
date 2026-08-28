"""
Regression tests for Request ID and SessionKey resolution.

This test verifies that every request handled by the proxy is assigned a unique
request_id, which is essential for SessionKey resolution and session-scoped features.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest
from httpx import ASGITransport, AsyncClient
from src.core.app.application_builder import ApplicationBuilder
from src.core.config.app_config import AppConfig


@pytest.mark.asyncio
async def test_request_id_is_populated_in_request_context():
    """
    Test that a request through the /v1/chat/completions endpoint
    results in a RequestContext with a non-empty request_id.

    This is a regression test for the issue where request_id was missing,
    causing "Cannot resolve SessionKey" log entries.
    """
    config = AppConfig.model_validate({"auth": {"disable_auth": True}})

    # Build a real app but we'll inspect the response headers
    builder = ApplicationBuilder().add_default_stages()
    app = await builder.build(config)

    async with AsyncClient(
        transport=ASGITransport(app=app), base_url="http://test"
    ) as client:
        # We'll call a valid endpoint
        response = await client.post(
            "/v1/chat/completions",
            json={"model": "gpt-4", "messages": [{"role": "user", "content": "hi"}]},
        )

        # 1. Verify the X-Request-ID header is present in the response
        assert "X-Request-ID" in response.headers
        request_id = response.headers["X-Request-ID"]
        assert request_id.startswith("req-")

        # 2. Verify we didn't get a 500 error (middleware should be safe)
        # Note: it might return 503 if no backends are configured, which is fine
        assert response.status_code != 500


@pytest.mark.asyncio
async def test_request_id_preserves_upstream_header():
    """
    Test that if an upstream X-Request-ID is provided, it is preserved
    and used in the RequestContext.
    """
    config = AppConfig.model_validate({"auth": {"disable_auth": True}})

    builder = ApplicationBuilder().add_default_stages()
    app = await builder.build(config)

    upstream_id = "upstream-trace-123"

    async with AsyncClient(
        transport=ASGITransport(app=app), base_url="http://test"
    ) as client:
        response = await client.post(
            "/v1/chat/completions",
            headers={"X-Request-ID": upstream_id},
            json={"model": "gpt-4", "messages": [{"role": "user", "content": "hi"}]},
        )

        assert response.headers.get("X-Request-ID") == upstream_id


@pytest.mark.asyncio
async def test_session_key_resolution_success():
    """
    Verify that SessionKey resolution succeeds for a standard request context
    created during a request.
    """
    from fastapi import Request
    from src.core.transport.fastapi.request_adapters import (
        fastapi_to_domain_request_context,
    )
    from src.core.transport.session_key_resolver import (
        resolve_session_key_from_request_context,
    )

    # Mock a FastAPI request
    mock_request = MagicMock(spec=Request)
    mock_request.headers = {"x-request-id": "test-req-id"}
    mock_request.cookies = {}
    mock_request.state = MagicMock()
    mock_request.state.request_state = {}
    mock_request.app = MagicMock()
    mock_request.client = MagicMock()
    mock_request.client.host = "127.0.0.1"

    # Convert to domain context
    context = fastapi_to_domain_request_context(mock_request)

    # Resolve session key
    session_key = resolve_session_key_from_request_context(context)

    assert session_key is not None
    assert session_key.primary_id == "test-req-id"
    assert session_key.protocol == "http"
