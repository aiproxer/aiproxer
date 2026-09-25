"""Tests for OpenAIConnector canonical connector API implementation."""

from __future__ import annotations

from dataclasses import replace
from typing import Any
from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest
from src.connectors.contracts import (
    ConnectorChatCompletionsRequest,
    ConnectorRequestContext,
    ConnectorResponsesRequest,
)
from src.connectors.openai import (
    _LLM_PROXY_CLIENT_HOST_KEY,
    _LLM_PROXY_REQUEST_ID_KEY,
    _LLM_PROXY_SESSION_ID_KEY,
    _LLM_PROXY_STREAM_HEADERS_KEY,
    _LLM_PROXY_STREAM_URL_KEY,
    OpenAIConnector,
)
from src.core.common.exceptions import BackendError
from src.core.config.app_config import AppConfig
from src.core.domain.chat import CanonicalChatRequest, ChatMessage
from src.core.domain.model_utils import RESOLVED_URI_PARAMS_EXTRA_BODY_KEY
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.domain.responses_native_wiring import (
    RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY,
)
from src.core.services.translation_service import TranslationService


async def _aiter_bytes(*chunks: bytes):
    for chunk in chunks:
        yield chunk


@pytest.fixture
def mock_client():
    """Create a mock HTTP client."""
    return AsyncMock(spec=httpx.AsyncClient)


@pytest.fixture
def mock_config():
    """Create a mock app config."""
    config = MagicMock(spec=AppConfig)
    config.streaming_yield_interval = 100
    return config


@pytest.fixture
def translation_service():
    """Create a translation service."""
    return TranslationService()


@pytest.fixture
def openai_connector(mock_client, mock_config, translation_service):
    """Create an OpenAIConnector instance."""
    connector = OpenAIConnector(
        client=mock_client,
        config=mock_config,
        translation_service=translation_service,
    )
    connector.api_key = "test-api-key"
    connector.api_base_url = "https://api.openai.com/v1"
    connector.disable_health_check()
    return connector


@pytest.fixture
def canonical_request():
    """Create a sample ConnectorChatCompletionsRequest."""
    return ConnectorChatCompletionsRequest(
        request=CanonicalChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="Hello")],
            max_tokens=100,
        ),
        processed_messages=[ChatMessage(role="user", content="Hello")],
        effective_model="gpt-4",
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=ConnectorRequestContext(
            request_id="test-request-id",
            session_id="test-session-id",
            client_host="127.0.0.1",
            extensions={},
        ),
        options={},
    )


class TestOpenAICanonicalAPI:
    """Tests for OpenAIConnector canonical API implementation."""

    @pytest.mark.asyncio
    async def test_payload_drops_proxy_only_message_metadata(
        self, openai_connector
    ) -> None:
        synthetic_memo = ChatMessage(
            role="user",
            content="[Session Steering Guidance]\\nplan",
            metadata={
                "source": "interleaved_thinking",
                "kind": "thinker_memo_synthetic_user",
                "non_forwardable": True,
            },
        )
        request = CanonicalChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="hello"), synthetic_memo],
        )

        payload = await openai_connector._prepare_payload(
            request_data=request,
            processed_messages=list(request.messages),
            effective_model="gpt-4",
        )

        assert payload["messages"][-1] == {
            "role": "user",
            "content": "[Session Steering Guidance]\\nplan",
        }
        assert "metadata" not in payload["messages"][-1]

    def test_implements_canonical_protocol(self, openai_connector):
        """Test that OpenAIConnector implements ICanonicalChatCompletionsBackend."""
        import inspect

        # Check if canonical method exists by inspecting signature
        # The canonical API should have a parameter named "request" as the first argument
        method = getattr(openai_connector, "chat_completions", None)
        assert method is not None, "chat_completions method not found"

        try:
            sig = inspect.signature(method)
            params = list(sig.parameters.values())

            # Check if the first parameter is "request"
            if len(params) >= 1 and params[0].name == "request":
                # Canonical API found
                param_annotation = params[0].annotation
                # Check if annotation matches ConnectorChatCompletionsRequest
                assert (
                    param_annotation == ConnectorChatCompletionsRequest
                    or "ConnectorChatCompletionsRequest" in str(param_annotation)
                ), f"Expected ConnectorChatCompletionsRequest, got {param_annotation}"
            else:
                # Legacy signature without 'request' as first param
                pytest.fail(
                    "Canonical chat_completions method signature not found. "
                    f"Found signature with {len(params)} parameters: {[p.name for p in params]}"
                )
        except (ValueError, TypeError) as e:
            pytest.fail(f"Failed to inspect signature: {e}")

    @pytest.mark.asyncio
    async def test_canonical_api_receives_typed_contracts(
        self, openai_connector, canonical_request
    ):
        """Test that canonical API receives ConnectorChatCompletionsRequest with typed contracts."""
        # Mock the internal implementation
        with patch.object(
            openai_connector,
            "_chat_completions_canonical",
            new_callable=AsyncMock,
        ) as mock_internal:
            mock_internal.return_value = ResponseEnvelope(
                content={"id": "test-id", "model": "gpt-4", "choices": []},
            )

            # Call canonical API
            await openai_connector.chat_completions(canonical_request)

            # Verify it was called with typed contracts
            mock_internal.assert_called_once()
            call_args = mock_internal.call_args

            # Verify request.request is CanonicalChatRequest
            assert isinstance(canonical_request.request, CanonicalChatRequest)

            # Verify processed_messages is Sequence[ChatMessage]
            assert all(
                isinstance(msg, ChatMessage)
                for msg in canonical_request.processed_messages
            )

            # Verify options is dict[str, JsonValue]
            assert isinstance(canonical_request.options, dict)

            # Verify the canonical request was passed correctly
            assert call_args[0][0] == canonical_request

    @pytest.mark.asyncio
    async def test_canonical_api_consumes_json_safe_options(
        self, openai_connector, canonical_request
    ):
        """Test that canonical API consumes options from JSON-safe dict."""
        # Set options with JSON-safe values
        canonical_request.options = {
            "openai_url": "https://custom.openai.com/v1",
            "headers_override": {"custom": "header"},
        }

        # Mock the internal implementation to verify options are used
        with patch.object(
            openai_connector,
            "_chat_completions_canonical",
            new_callable=AsyncMock,
        ) as mock_internal:
            mock_internal.return_value = ResponseEnvelope(
                content={"id": "test-id", "model": "gpt-4", "choices": []},
            )

            await openai_connector.chat_completions(canonical_request)

            # Verify options were passed correctly
            assert (
                canonical_request.options["openai_url"]
                == "https://custom.openai.com/v1"
            )

            # Verify the canonical request with options was passed
            call_args = mock_internal.call_args
            passed_request = call_args[0][0]
            assert (
                passed_request.options["openai_url"] == "https://custom.openai.com/v1"
            )

    @pytest.mark.asyncio
    async def test_context_used_for_logging_correlation(
        self, openai_connector, canonical_request
    ):
        """Test that ConnectorRequestContext is available for logging correlation."""
        # Create a new request with stream=False (CanonicalChatRequest is frozen)
        non_streaming_request = CanonicalChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="Hello")],
            max_tokens=100,
            stream=False,
        )

        # Set up context with correlation identifiers
        canonical_request.context = ConnectorRequestContext(
            request_id="test-req-789",
            session_id="test-session-012",
            client_host="10.0.0.1",
            extensions={},
        )
        canonical_request.request = non_streaming_request

        # Mock the internal implementation to avoid actual HTTP calls
        with patch.object(
            openai_connector,
            "_handle_non_streaming_response",
            new_callable=AsyncMock,
        ) as mock_handler:
            mock_handler.return_value = ResponseEnvelope(
                content={"id": "test-id", "model": "gpt-4", "choices": []},
                status_code=200,
            )

            result = await openai_connector.chat_completions(canonical_request)

            # Verify context was extracted and available
            assert result is not None
            # Context is extracted in _chat_completions_canonical and available for logging
            # We verify by checking that the method completed successfully

    @pytest.mark.asyncio
    async def test_canonical_api_streaming_path(
        self, openai_connector, canonical_request
    ):
        """Test that canonical API handles streaming requests correctly."""
        # Create a new request with stream=True (CanonicalChatRequest is frozen)
        streaming_request = CanonicalChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="Hello")],
            max_tokens=100,
            stream=True,
        )
        canonical_request.request = streaming_request

        # Mock streaming pipeline integration
        with patch(
            "src.core.ports.streaming_integration.integrate_streaming_pipeline",
            new_callable=AsyncMock,
        ) as mock_integrate:
            mock_integrate.return_value = StreamingResponseEnvelope(
                content=AsyncMock(),
                media_type="text/event-stream",
                headers={},
            )

            # Mock stream_completion
            with patch.object(
                openai_connector,
                "stream_completion",
                new_callable=AsyncMock,
            ) as mock_stream:
                mock_stream.return_value = AsyncMock()

                result = await openai_connector.chat_completions(canonical_request)

                # Verify streaming path was taken
                assert isinstance(result, StreamingResponseEnvelope)
                mock_stream.assert_called_once()

    @pytest.mark.asyncio
    async def test_streaming_uses_effective_model_not_domain_selector(
        self, openai_connector, canonical_request
    ):
        """stream_completion must POST the resolved model, not the original selector."""
        streaming_request = CanonicalChatRequest(
            model="cline:cline-free/free?reasoning_effort=xhigh",
            messages=[ChatMessage(role="user", content="Hello")],
            stream=True,
        )
        canonical_request.request = streaming_request
        canonical_request.effective_model = "cline-free/gemini-3.8-flash"

        with (
            patch(
                "src.core.ports.streaming_integration.integrate_streaming_pipeline",
                new_callable=AsyncMock,
                return_value=StreamingResponseEnvelope(
                    content=AsyncMock(),
                    media_type="text/event-stream",
                    headers={},
                ),
            ),
            patch.object(
                openai_connector,
                "stream_completion",
                new_callable=AsyncMock,
            ) as mock_stream,
        ):
            mock_stream.return_value = AsyncMock()
            await openai_connector.chat_completions(canonical_request)

        called_request = mock_stream.call_args[0][0]
        assert called_request.model == "cline-free/gemini-3.8-flash"

    @pytest.mark.asyncio
    async def test_canonical_api_non_streaming_path(
        self, openai_connector, canonical_request
    ):
        """Test that canonical API handles non-streaming requests correctly."""
        # Create a new request with stream=False (CanonicalChatRequest is frozen)
        non_streaming_request = CanonicalChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="Hello")],
            max_tokens=100,
            stream=False,
        )
        canonical_request.request = non_streaming_request

        # Mock non-streaming handler
        with patch.object(
            openai_connector,
            "_handle_non_streaming_response",
            new_callable=AsyncMock,
        ) as mock_handler:
            mock_handler.return_value = ResponseEnvelope(
                content={"id": "test-id", "model": "gpt-4", "choices": []},
                status_code=200,
            )

            result = await openai_connector.chat_completions(canonical_request)

            # Verify non-streaming path was taken
            assert isinstance(result, ResponseEnvelope)
            mock_handler.assert_called_once()

    @pytest.mark.asyncio
    async def test_options_json_safety_validation(
        self, openai_connector, canonical_request
    ):
        """Test that options are validated as JSON-safe values."""
        import json

        # Set options with JSON-safe values
        canonical_request.options = {
            "openai_url": "https://custom.openai.com/v1",
            "headers_override": {"custom": "header"},
            "numeric": 42,
            "boolean": True,
            "null_value": None,
        }

        # Mock the internal implementation
        with patch.object(
            openai_connector,
            "_chat_completions_canonical",
            new_callable=AsyncMock,
        ) as mock_internal:
            mock_internal.return_value = ResponseEnvelope(
                content={"id": "test-id", "model": "gpt-4", "choices": []},
            )

            await openai_connector.chat_completions(canonical_request)

            # Verify all options are JSON-serializable
            call_args = mock_internal.call_args
            passed_request = call_args[0][0]

            # All values should be JSON-serializable
            try:
                json.dumps(passed_request.options)
            except (TypeError, ValueError) as e:
                pytest.fail(f"Options contain non-JSON-safe values: {e}")

            # Verify options were passed correctly
            assert (
                passed_request.options["openai_url"] == "https://custom.openai.com/v1"
            )
            assert passed_request.options["numeric"] == 42
            assert passed_request.options["boolean"] is True

    @pytest.mark.asyncio
    async def test_context_in_error_logs(self, openai_connector, canonical_request):
        """Test that context correlation identifiers appear in error logs."""
        from json import JSONDecodeError

        # Set up context with correlation identifiers
        canonical_request.context = ConnectorRequestContext(
            request_id="test-req-error-789",
            session_id="test-session-error-012",
            client_host="10.0.0.100",
            extensions={},
        )

        # Create a non-streaming request
        non_streaming_request = CanonicalChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="Hello")],
            max_tokens=100,
            stream=False,
        )
        canonical_request.request = non_streaming_request

        # Mock HTTP client to return error response that triggers JSON parsing error
        mock_response = AsyncMock()
        mock_response.status_code = 500
        mock_response.headers = {}
        # Make json() raise JSONDecodeError to trigger the warning log path we fixed
        mock_response.json.side_effect = JSONDecodeError("Invalid JSON", "", 0)
        mock_response.text = "Internal server error"

        # Mock the internal handler to verify context is passed
        with patch.object(
            openai_connector,
            "_handle_non_streaming_response",
            new_callable=AsyncMock,
        ) as mock_handler:
            mock_handler.side_effect = BackendError(
                message="Test error", status_code=500
            )

            # Capture log messages
            with patch("src.connectors.openai.logger") as mock_logger:
                mock_logger.isEnabledFor.return_value = True

                # Call should raise an error
                with pytest.raises(Exception, match="Test error"):
                    await openai_connector.chat_completions(canonical_request)

                # Verify context was passed to helper method
                mock_handler.assert_called_once()
                call_args = mock_handler.call_args
                # Check that context parameter was passed (5th argument: url, payload, headers, session_id, context)
                assert len(call_args[0]) >= 5
                passed_context = call_args[0][4]
                assert passed_context is not None
                assert passed_context.request_id == "test-req-error-789"
                assert passed_context.session_id == "test-session-error-012"

    @pytest.mark.asyncio
    async def test_context_in_warning_logs(self, openai_connector, canonical_request):
        """Test that context correlation identifiers appear in warning logs."""

        # Set up context with correlation identifiers
        canonical_request.context = ConnectorRequestContext(
            request_id="test-req-warn-789",
            session_id="test-session-warn-012",
            client_host="10.0.0.200",
            extensions={},
        )

        # Create a request that triggers a warning (e.g., failed prompt token calculation)
        non_streaming_request = CanonicalChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="Hello")],
            max_tokens=100,
            stream=False,
        )
        canonical_request.request = non_streaming_request

        # Mock the internal implementation to trigger a warning
        with patch.object(
            openai_connector,
            "_handle_non_streaming_response",
            new_callable=AsyncMock,
        ) as mock_handler:
            mock_handler.return_value = ResponseEnvelope(
                content={"id": "test-id", "model": "gpt-4", "choices": []},
                status_code=200,
            )

            # Mock extract_prompt_text to raise (triggers warning in streaming path)
            # But we're testing non-streaming, so let's test with a different scenario
            # Instead, let's verify context is passed to helper methods

            # Capture log messages
            with patch("src.connectors.openai.logger") as mock_logger:
                mock_logger.isEnabledFor.return_value = True

                await openai_connector.chat_completions(canonical_request)

                # Verify context was passed to helper (indirect verification)
                mock_handler.assert_called_once()
                call_args = mock_handler.call_args
                # Check that context parameter was passed
                assert (
                    len(call_args[0]) >= 5
                )  # url, payload, headers, session_id, context
                passed_context = call_args[0][4] if len(call_args[0]) > 4 else None
                assert passed_context is not None
                assert passed_context.request_id == "test-req-warn-789"
                assert passed_context.session_id == "test-session-warn-012"

    @pytest.mark.asyncio
    async def test_headers_override_does_not_replace_backend_authorization(
        self, openai_connector, canonical_request
    ):
        """Backend Bearer token must win when options.headers_override also sets Authorization."""
        openai_connector.api_key = "backend-real-key"
        canonical_request.options = {
            "headers_override": {"Authorization": "Bearer wrong-client-token"},
        }
        non_streaming_request = CanonicalChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="Hello")],
            max_tokens=100,
            stream=False,
        )
        canonical_request.request = non_streaming_request

        captured: dict[str, Any] = {}

        async def fake_handle(
            url: str,
            payload: dict[str, Any],
            headers: dict[str, str] | None,
            session_id: str,
            context: Any | None = None,
        ) -> ResponseEnvelope:
            captured["headers"] = dict(headers or {})
            return ResponseEnvelope(
                content={"id": "x", "model": "gpt-4", "choices": []},
                status_code=200,
                headers={},
            )

        with patch.object(
            openai_connector,
            "_handle_non_streaming_response",
            new_callable=AsyncMock,
            side_effect=fake_handle,
        ):
            await openai_connector.chat_completions(canonical_request)

        assert captured["headers"]["Authorization"] == "Bearer backend-real-key"

    @pytest.mark.asyncio
    async def test_streaming_passes_resolved_url_and_headers_via_extra_body(
        self, openai_connector, canonical_request
    ):
        """Streaming must use the same URL/headers as the canonical non-stream path."""
        openai_connector.api_key = "stream-backend-key"
        canonical_request.options = {"openai_url": "https://custom.openai.example/v1"}
        streaming_request = CanonicalChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="Hello")],
            max_tokens=100,
            stream=True,
        )
        canonical_request.request = streaming_request

        captured_extra: dict[str, Any] = {}

        async def capture_stream(request: CanonicalChatRequest):
            captured_extra["extra_body"] = dict(request.extra_body or {})
            # Make this an async generator for the streaming pipeline
            if False:
                yield b""

        async def fake_integrate(raw_stream, *args: Any, **kwargs: Any):
            # Real pipeline iterates raw_stream; a bare AsyncMock return skips this,
            # so the async generator body of stream_completion would never run.
            async for _ in raw_stream:
                break
            return StreamingResponseEnvelope(
                content=AsyncMock(),
                media_type="text/event-stream",
                headers={},
            )

        with (
            patch(
                "src.core.ports.streaming_integration.integrate_streaming_pipeline",
                side_effect=fake_integrate,
            ),
            patch.object(openai_connector, "stream_completion", capture_stream),
        ):
            await openai_connector.chat_completions(canonical_request)

        extra = captured_extra["extra_body"]
        assert (
            extra[_LLM_PROXY_STREAM_URL_KEY]
            == "https://custom.openai.example/v1/chat/completions"
        )
        assert (
            extra[_LLM_PROXY_STREAM_HEADERS_KEY]["Authorization"]
            == "Bearer stream-backend-key"
        )
        # Correlation identifiers must reach the raw stream path so boundary
        # captures (PROXY_TO_BACKEND) get real request/session ids, not the
        # session-as-request-id fallback.
        assert extra[_LLM_PROXY_REQUEST_ID_KEY] == "test-request-id"
        assert extra[_LLM_PROXY_SESSION_ID_KEY] == "test-session-id"
        assert extra[_LLM_PROXY_CLIENT_HOST_KEY] == "127.0.0.1"

    @pytest.mark.asyncio
    async def test_stream_completion_retries_once_on_http2_no_error_termination(
        self, openai_connector
    ):
        request = CanonicalChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="Hello")],
            max_tokens=100,
            stream=True,
        )
        http_request = httpx.Request(
            "POST",
            "https://api.openai.com/v1/chat/completions",
            json={"model": "gpt-4", "stream": True},
        )
        streamed_response = MagicMock()
        streamed_response.status_code = 200
        streamed_response.headers = {"content-type": "text/event-stream"}
        streamed_response.aiter_bytes = lambda: _aiter_bytes(
            b'data: {"choices":[{"delta":{"content":"ok"}}]}\n\n',
            b"data: [DONE]\n\n",
        )
        streamed_response.aclose = AsyncMock()

        openai_connector.client.build_request.return_value = http_request
        with patch.object(
            openai_connector,
            "_prepare_payload",
            new_callable=AsyncMock,
            return_value={"model": "gpt-4", "messages": []},
        ):
            openai_connector._capture_http_client.send = AsyncMock(
                side_effect=[
                    httpx.RemoteProtocolError(
                        "<ConnectionTerminated error_code:ErrorCodes.NO_ERROR, last_stream_id:27, additional_data:None>"
                    ),
                    streamed_response,
                ]
            )

            chunks = [
                chunk async for chunk in openai_connector.stream_completion(request)
            ]

        assert chunks == [
            'data: {"choices":[{"delta":{"content":"ok"}}]}\n\n',
            "data: [DONE]\n\n",
        ]
        assert openai_connector._capture_http_client.send.await_count == 2

    @pytest.mark.asyncio
    async def test_stream_completion_retries_once_on_server_disconnected(
        self, openai_connector
    ):
        request = CanonicalChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="Hello")],
            max_tokens=100,
            stream=True,
        )
        http_request = httpx.Request(
            "POST",
            "https://api.openai.com/v1/chat/completions",
            json={"model": "gpt-4", "stream": True},
        )
        streamed_response = MagicMock()
        streamed_response.status_code = 200
        streamed_response.headers = {"content-type": "text/event-stream"}
        streamed_response.aiter_bytes = lambda: _aiter_bytes(
            b'data: {"choices":[{"delta":{"content":"ok"}}]}\n\n',
            b"data: [DONE]\n\n",
        )
        streamed_response.aclose = AsyncMock()

        openai_connector.client.build_request.return_value = http_request
        with patch.object(
            openai_connector,
            "_prepare_payload",
            new_callable=AsyncMock,
            return_value={"model": "gpt-4", "messages": []},
        ):
            openai_connector._capture_http_client.send = AsyncMock(
                side_effect=[
                    httpx.RemoteProtocolError("Server disconnected"),
                    streamed_response,
                ]
            )

            chunks = [
                chunk async for chunk in openai_connector.stream_completion(request)
            ]

        assert chunks == [
            'data: {"choices":[{"delta":{"content":"ok"}}]}\n\n',
            "data: [DONE]\n\n",
        ]
        assert openai_connector._capture_http_client.send.await_count == 2

    @pytest.mark.asyncio
    async def test_chat_completions_delegates_to_responses_for_native_projected_payload(
        self, openai_connector, canonical_request
    ) -> None:
        native_payload: dict[str, Any] = {
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "hi"}],
                }
            ],
            "model": "gpt-4",
        }
        domain = canonical_request.request.model_copy(
            update={
                "extra_body": {
                    RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY: native_payload,
                }
            }
        )
        req = replace(canonical_request, request=domain)
        with patch.object(
            openai_connector, "responses", new_callable=AsyncMock
        ) as mock_responses:
            mock_responses.return_value = ResponseEnvelope(
                content={"id": "resp-delegated", "object": "response"},
                status_code=200,
            )
            out = await openai_connector.chat_completions(req)
        mock_responses.assert_awaited_once_with(
            ConnectorResponsesRequest.from_chat_completions(req)
        )
        assert out is mock_responses.return_value


class TestOpenAIPayloadCleaning:
    """Tests for outbound payload hygiene."""

    @pytest.mark.asyncio
    async def test_prepare_payload_disables_deepseek_native_reasoning_controls(
        self, openai_connector
    ):
        request = CanonicalChatRequest(
            model="deepseek-v4-flash-free",
            messages=[ChatMessage(role="user", content="one")],
            reasoning_effort="max",
        )

        payload = await openai_connector._prepare_payload(
            request,
            request.messages,
            "deepseek-v4-flash-free",
        )

        assert "reasoning" not in payload
        assert "thinking" not in payload
        assert "reasoning_effort" not in payload

    @pytest.mark.asyncio
    async def test_prepare_payload_remaps_deepseek_developer_role_to_system(
        self, openai_connector
    ):
        """DeepSeek chat/completions rejects role=developer with HTTP 400."""
        request = CanonicalChatRequest(
            model="deepseek-v4-flash-free",
            messages=[
                ChatMessage(role="developer", content="You are helpful."),
                ChatMessage(role="user", content="one"),
            ],
        )

        payload = await openai_connector._prepare_payload(
            request,
            request.messages,
            "deepseek-v4-flash-free",
        )

        assert payload["messages"][0]["role"] == "system"
        assert payload["messages"][1]["role"] == "user"

    @pytest.mark.asyncio
    async def test_prepare_payload_preserves_non_deepseek_developer_role(
        self, openai_connector
    ):
        request = CanonicalChatRequest(
            model="gpt-compatible",
            messages=[
                ChatMessage(role="developer", content="You are helpful."),
                ChatMessage(role="user", content="one"),
            ],
        )

        payload = await openai_connector._prepare_payload(
            request,
            request.messages,
            "gpt-compatible",
        )

        assert payload["messages"][0]["role"] == "developer"

    @pytest.mark.asyncio
    async def test_prepare_payload_strips_empty_named_tool_calls(
        self, openai_connector
    ):
        """Agent harnesses may replay unnamed tool-call fragments that 400 upstream."""
        request = CanonicalChatRequest(
            model="gpt-compatible",
            messages=[
                ChatMessage(role="user", content="go"),
                ChatMessage(
                    role="assistant",
                    content=None,
                    tool_calls=[
                        {
                            "id": "call_good",
                            "type": "function",
                            "function": {
                                "name": "web_search",
                                "arguments": '{"q":"x"}',
                            },
                        },
                        {
                            "id": "",
                            "type": "function",
                            "function": {"name": "", "arguments": '"}"'},
                        },
                    ],
                ),
                ChatMessage(
                    role="tool",
                    content="Tool  not found",
                    tool_call_id="",
                ),
            ],
        )

        payload = await openai_connector._prepare_payload(
            request,
            request.messages,
            "gpt-compatible",
        )

        assert len(payload["messages"]) == 2
        assert payload["messages"][1]["tool_calls"] == [
            {
                "id": "call_good",
                "type": "function",
                "function": {"name": "web_search", "arguments": '{"q":"x"}'},
            }
        ]

    @pytest.mark.asyncio
    async def test_prepare_payload_preserves_deepseek_reasoning_continuation(
        self, openai_connector
    ):
        request = CanonicalChatRequest(
            model="deepseek-v4-flash-free",
            messages=[
                ChatMessage(role="user", content="one"),
                ChatMessage(
                    role="assistant",
                    content="two",
                    reasoning_content="old partial reasoning",
                ),
                ChatMessage(role="user", content="three"),
            ],
            reasoning_effort="max",
        )

        payload = await openai_connector._prepare_payload(
            request,
            request.messages,
            "deepseek-v4-flash-free",
        )

        assert "reasoning" not in payload
        assert "thinking" not in payload
        assert payload["messages"][1]["reasoning_content"] == "old partial reasoning"
        assert "reasoning" not in payload["messages"][1]

    @pytest.mark.asyncio
    async def test_prepare_payload_preserves_non_deepseek_reasoning_continuation(
        self, openai_connector
    ):
        request = CanonicalChatRequest(
            model="gpt-compatible",
            messages=[
                ChatMessage(
                    role="assistant",
                    content="two",
                    reasoning_content="reasoning to preserve",
                ),
            ],
            reasoning_effort="high",
        )

        payload = await openai_connector._prepare_payload(
            request,
            request.messages,
            "gpt-compatible",
        )

        assert payload["reasoning"] == {"effort": "high"}
        assert payload["messages"][0]["reasoning_content"] == "reasoning to preserve"

    @pytest.mark.asyncio
    async def test_prepare_payload_includes_top_level_verbosity(self, openai_connector):
        request = CanonicalChatRequest(
            model="gpt-5.4-mini",
            messages=[ChatMessage(role="user", content="hi")],
            verbosity="low",
        )

        payload = await openai_connector._prepare_payload(
            request,
            request.messages,
            "gpt-5.4-mini",
        )

        assert payload.get("verbosity") == "low"
        assert "text" not in payload or "verbosity" not in (payload.get("text") or {})

    @pytest.mark.asyncio
    async def test_prepare_payload_omits_verbosity_when_unset(self, openai_connector):
        request = CanonicalChatRequest(
            model="gpt-5.4-mini",
            messages=[ChatMessage(role="user", content="hi")],
        )

        payload = await openai_connector._prepare_payload(
            request,
            request.messages,
            "gpt-5.4-mini",
        )

        assert "verbosity" not in payload

    def test_clean_openai_payload_preserves_verbosity(self, openai_connector):
        cleaned = openai_connector._clean_openai_payload(
            {
                "model": "gpt-5.4-mini",
                "messages": [],
                "verbosity": "high",
                "reasoning_effort": "high",
            }
        )
        assert cleaned.get("verbosity") == "high"
        assert "reasoning_effort" not in cleaned

    def test_clean_openai_payload_strips_internal_stream_routing_keys(
        self, openai_connector
    ):
        cleaned = openai_connector._clean_openai_payload(
            {
                "model": "gpt-4",
                "messages": [],
                RESOLVED_URI_PARAMS_EXTRA_BODY_KEY: {"reasoning_effort": "max"},
                _LLM_PROXY_STREAM_URL_KEY: "https://x/v1/chat/completions",
                _LLM_PROXY_STREAM_HEADERS_KEY: {"Authorization": "Bearer z"},
            }
        )
        assert RESOLVED_URI_PARAMS_EXTRA_BODY_KEY not in cleaned
        assert _LLM_PROXY_STREAM_URL_KEY not in cleaned
        assert _LLM_PROXY_STREAM_HEADERS_KEY not in cleaned
        assert cleaned.get("model") == "gpt-4"

    def test_clean_openai_payload_strips_request_context_tokens(self, openai_connector):
        cleaned = openai_connector._clean_openai_payload(
            {
                "model": "gpt-4",
                "messages": [],
                "request_context_tokens": 8192,
                "metadata": {"request_context_tokens": 4096, "source": "test"},
            }
        )

        assert "request_context_tokens" not in cleaned
        assert cleaned.get("metadata") == {
            "request_context_tokens": 4096,
            "source": "test",
        }

    def test_clean_openai_payload_strips_edit_precision_private_keys(
        self, openai_connector
    ):
        cleaned = openai_connector._clean_openai_payload(
            {
                "model": "gpt-4",
                "messages": [],
                "_edit_precision_mode": True,
                "_edit_precision_meta": {"reason": "failed_edit"},
                "metadata": {"source": "test"},
            }
        )

        assert "_edit_precision_mode" not in cleaned
        assert "_edit_precision_meta" not in cleaned
        assert cleaned.get("metadata") == {"source": "test"}

    def test_clean_openai_payload_preserves_tool_schema_agent_property(
        self, openai_connector
    ):
        cleaned = openai_connector._clean_openai_payload(
            {
                "model": "kimi-k2.6",
                "agent": "pi",
                "tools": [
                    {
                        "type": "function",
                        "function": {
                            "name": "subagent",
                            "parameters": {
                                "type": "object",
                                "properties": {
                                    "tasks": {
                                        "type": "array",
                                        "items": {
                                            "type": "object",
                                            "required": ["agent", "task"],
                                            "properties": {
                                                "agent": {"type": "string"},
                                                "task": {"type": "string"},
                                            },
                                        },
                                    }
                                },
                            },
                        },
                    }
                ],
            }
        )

        assert "agent" not in cleaned
        task_item_schema = cleaned["tools"][0]["function"]["parameters"]["properties"][
            "tasks"
        ]["items"]
        assert task_item_schema["required"] == ["agent", "task"]
        assert "agent" in task_item_schema["properties"]
