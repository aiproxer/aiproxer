"""Tests for httpx.ReadError handling in OpenAI connector.

httpx.ReadError occurs when the backend disconnects during streaming.
It should map to BackendError (502), not ServiceUnavailableError (503).
"""

from __future__ import annotations

import httpx
import pytest
from src.connectors.openai import (
    _is_retryable_http2_stream_termination,
    _raise_for_httpx_request_error,
)
from src.core.common.exceptions import BackendError, ServiceUnavailableError


@pytest.mark.asyncio
class TestHttpErrorMapping:
    """Test error mapping for httpx transport errors."""

    def test_read_error_maps_to_backend_error_502(self) -> None:
        """httpx.ReadError should map to BackendError(502), not ServiceUnavailableError(503).

        This is the bug fix: previously ReadError fell to the catch-all handler
        which logged at ERROR level with full traceback and raised
        ServiceUnavailableError with "Could not connect to backend" message,
        which is misleading since we WERE connected and streaming.
        """
        exc = httpx.ReadError(
            "connection reset by peer",
            request=httpx.Request("POST", "https://example.com/v1/chat/completions"),
        )

        with pytest.raises(BackendError) as ctx:
            _raise_for_httpx_request_error(
                exc,
                url="https://example.com/v1/chat/completions",
                log_extra=None,
            )

        assert ctx.value.status_code == 502
        assert ctx.value.details.get("reason") == "read_error"
        assert "read error" in ctx.value.message.lower()
        assert "Could not connect to backend" not in ctx.value.message

    def test_read_timeout_maps_to_backend_error_504(self) -> None:
        """httpx.ReadTimeout should map to BackendError(504)."""
        exc = httpx.ReadTimeout(
            "timed out",
            request=httpx.Request("POST", "https://example.com/v1/chat/completions"),
        )

        with pytest.raises(BackendError) as ctx:
            _raise_for_httpx_request_error(
                exc,
                url="https://example.com/v1/chat/completions",
                log_extra=None,
            )

        assert ctx.value.status_code == 504
        assert ctx.value.details.get("reason") == "read_timeout"

    def test_connect_timeout_maps_to_service_unavailable_503(self) -> None:
        """httpx.ConnectTimeout should map to ServiceUnavailableError(503)."""
        exc = httpx.ConnectTimeout(
            "timed out",
            request=httpx.Request("POST", "https://example.com/v1/chat/completions"),
        )

        with pytest.raises(ServiceUnavailableError) as ctx:
            _raise_for_httpx_request_error(
                exc,
                url="https://example.com/v1/chat/completions",
                log_extra=None,
            )

        assert ctx.value.status_code == 503
        assert ctx.value.details.get("reason") == "connect_timeout"
        assert "connect" in ctx.value.message.lower()

    def test_write_timeout_maps_to_backend_error_504(self) -> None:
        """httpx.WriteTimeout should map to BackendError(504)."""
        exc = httpx.WriteTimeout(
            "timed out",
            request=httpx.Request("POST", "https://example.com/v1/chat/completions"),
        )

        with pytest.raises(BackendError) as ctx:
            _raise_for_httpx_request_error(
                exc,
                url="https://example.com/v1/chat/completions",
                log_extra=None,
            )

        assert ctx.value.status_code == 504
        assert ctx.value.details.get("reason") == "write_timeout"

    def test_unknown_error_maps_to_service_unavailable_503(self) -> None:
        """Other httpx.RequestError should map to ServiceUnavailableError(503)."""
        exc = httpx.RequestError(
            "generic error",
            request=httpx.Request("POST", "https://example.com/v1/chat/completions"),
        )

        with pytest.raises(ServiceUnavailableError) as ctx:
            _raise_for_httpx_request_error(
                exc,
                url="https://example.com/v1/chat/completions",
                log_extra=None,
            )

        assert ctx.value.status_code == 503
        assert "Could not connect to backend" in ctx.value.message

    def test_remote_protocol_error_maps_to_backend_error_502(self) -> None:
        """httpx.RemoteProtocolError should map to BackendError(502)."""
        exc = httpx.RemoteProtocolError(
            "Server disconnected",
            request=httpx.Request("POST", "https://example.com/v1/chat/completions"),
        )

        with pytest.raises(BackendError) as ctx:
            _raise_for_httpx_request_error(
                exc,
                url="https://example.com/v1/chat/completions",
                log_extra=None,
            )

        assert ctx.value.status_code == 502
        assert ctx.value.details.get("reason") == "remote_protocol_error"
        assert "remote server disconnected" in ctx.value.message.lower()


class TestRetryableHttp2StreamTermination:
    """Test HTTP/2 stream termination retry detection."""

    def test_read_error_is_not_retryable(self) -> None:
        """httpx.ReadError should NOT trigger retry logic.

        ReadError indicates a connection-level failure mid-stream,
        which cannot be safely retried since partial data was already sent/received.
        """
        exc = httpx.ReadError(
            "connection reset by peer",
            request=httpx.Request("POST", "https://example.com/v1/chat/completions"),
        )

        assert not _is_retryable_http2_stream_termination(exc)

    def test_remote_protocol_error_with_server_disconnected_is_retryable(self) -> None:
        """httpx.RemoteProtocolError with server disconnected message is retryable."""
        exc = httpx.RemoteProtocolError(
            "Server disconnected",
            request=httpx.Request("POST", "https://example.com/v1/chat/completions"),
        )

        assert _is_retryable_http2_stream_termination(exc)

    def test_remote_protocol_error_with_graceful_termination_is_retryable(self) -> None:
        """httpx.RemoteProtocolError with graceful HTTP/2 termination is retryable."""
        # Simulate the actual exception message from httpx/httpcore
        exc = httpx.RemoteProtocolError(
            "Server disconnected without sending a response. ConnectionTerminated",
            request=httpx.Request("POST", "https://example.com/v1/chat/completions"),
        )
        assert _is_retryable_http2_stream_termination(exc)


    def test_write_error_maps_to_backend_error_502(self) -> None:
        """httpx.WriteError should map to BackendError(502), not opaque 503."""
        exc = httpx.WriteError(
            "write failed",
            request=httpx.Request("POST", "https://example.com/v1/responses"),
        )

        with pytest.raises(BackendError) as ctx:
            _raise_for_httpx_request_error(
                exc,
                url="https://example.com/v1/responses",
                log_extra=None,
            )

        assert ctx.value.status_code == 502
        assert ctx.value.details.get("reason") == "write_error"
        assert ctx.value.details.get("retryable") is True
        assert "Could not connect to backend" not in ctx.value.message

    def test_write_error_is_retryable(self) -> None:
        """httpx.WriteError is retryable once (HTTP/2 concurrent write disruption)."""
        exc = httpx.WriteError(
            "write failed",
            request=httpx.Request("POST", "https://example.com/v1/responses"),
        )
        assert _is_retryable_http2_stream_termination(exc)
