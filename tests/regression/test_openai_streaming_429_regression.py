from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest
from src.connectors.openai import OpenAIConnector
from src.core.common.exceptions import RateLimitExceededError
from src.core.config.app_config import AppConfig
from src.core.domain.chat import CanonicalChatRequest


@pytest.mark.asyncio
async def test_openai_streaming_429_response_not_read_regression():
    """
    Validates that a 429 streaming response that fails to read does not crash
    with httpx.ResponseNotRead but correctly raises RateLimitExceededError.
    """

    class MockResponse:
        status_code = 429
        headers = httpx.Headers(
            {"retry-after": "10", "content-type": "application/json"}
        )

        async def aiter_bytes(self):
            raise httpx.ReadTimeout("Stream read timeout simulated")
            yield b""

        async def aclose(self):
            pass

        @property
        def text(self):
            raise httpx.ResponseNotRead()

    mock_response = MockResponse()

    mock_client = AsyncMock()
    mock_client.build_request.return_value = MagicMock()

    connector = OpenAIConnector(client=mock_client, config=AppConfig())
    connector._capture_http_client = AsyncMock()
    connector._capture_http_client.send.return_value = mock_response
    connector.api_key = "test-key"
    connector._prepare_payload = AsyncMock(
        return_value={"model": "test-model", "messages": []}
    )

    request = CanonicalChatRequest(
        model="test-model",
        messages=[{"role": "user", "content": "Hello"}],
    )

    with pytest.raises(RateLimitExceededError) as exc_info:
        async for _chunk in connector.stream_completion(request):
            pass

    assert exc_info.value.status_code == 429
    assert exc_info.value.reset_at == 10
