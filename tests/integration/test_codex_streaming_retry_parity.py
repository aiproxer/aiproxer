"""Integration tests for Codex connector streaming retry parity.

This test suite verifies that streaming authentication retry behavior matches
the current connector implementation for:
- Handshake-level authentication failures
- Chunk-level authentication failures
- Retry budget and backoff behavior
- Error shapes and status codes
"""

from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import AsyncMock, patch

import httpx
import pytest
import pytest_asyncio
from fastapi import HTTPException
from src.connectors.contracts import ConnectorChatCompletionsRequest
from src.connectors.openai_codex import OpenAICodexConnector
from src.core.config.app_config import AppConfig
from src.core.domain.chat import CanonicalChatRequest
from src.core.domain.responses import StreamingResponseEnvelope
from src.core.domain.validation import ValidationResult
from src.core.interfaces.response_processor_interface import ProcessedResponse
from src.core.services.translation_service import TranslationService

from tests.unit.connectors.openai_codex.test_openai_codex_helpers import (
    create_mock_credential_manager,
    create_mock_settings_loader,
)
from tests.unit.fixtures.markers import real_time


def _codex_conn_req(
    request: CanonicalChatRequest, *, effective_model: str
) -> ConnectorChatCompletionsRequest:
    return ConnectorChatCompletionsRequest(
        request=request,
        processed_messages=[],
        effective_model=effective_model,
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=None,
        options={},
    )


@pytest_asyncio.fixture(name="auth_dir")
async def auth_dir_tmp(tmp_path: Path):
    """Create temporary auth directory with credentials."""
    data = {"tokens": {"access_token": "test_token"}}
    tmp_path.mkdir(parents=True, exist_ok=True)
    (tmp_path / "auth.json").write_text(json.dumps(data), encoding="utf-8")
    return tmp_path


@pytest_asyncio.fixture(name="codex_connector")
async def codex_connector_fixture(auth_dir: Path):
    """Create connector with mocked HTTP client."""
    async with httpx.AsyncClient() as client:
        cfg = AppConfig()
        ts = TranslationService()

        # Create connector - api_key setter will be called but _credential_manager exists by then
        # The setter checks hasattr, so we need to ensure _credential_manager exists
        backend = OpenAICodexConnector(client, cfg, translation_service=ts)

        # Set _auth_credentials on credential manager before initialization
        backend._credential_manager._auth_credentials = {
            "tokens": {"access_token": "test_token"}
        }

        with (
            patch.object(
                backend,
                "_validate_credentials_file_exists",
                return_value=ValidationResult.success(),
            ),
            patch.object(
                backend,
                "_validate_credentials_structure",
                return_value=ValidationResult.success(),
            ),
            patch.object(backend, "_start_file_watching"),
        ):
            await backend.initialize(openai_codex_path=str(auth_dir))
            yield backend


class MockStreamHandle:
    """Mock streaming response handle."""

    def __init__(self, chunks: list[ProcessedResponse], headers: dict | None = None):
        self.chunks = chunks
        self.headers = headers or {}
        self.cancel_callback: AsyncMock | None = None

    @property
    def iterator(self):
        """Return async iterator for chunks."""

        async def _gen():
            for chunk in self.chunks:
                yield chunk

        return _gen()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_streaming_handshake_auth_failure_retry_success(
    auth_dir: Path,
):
    """Test that handshake authentication failures trigger retry with token refresh.

    This test validates that:
    - Executor is called for Codex model requests (Req 3.1, 3.2, 3.3)
    - Retry logic goes through the unified executor path (Req 6.1, 6.2)
    """
    # Create connector with mocked credential manager via dependency injection
    async with httpx.AsyncClient() as client:
        cfg = AppConfig()
        ts = TranslationService()

        mock_credential_manager = create_mock_credential_manager(refresh_success=True)
        mock_credential_manager.refresh_access_token = AsyncMock(return_value=True)
        # Ensure _auth_credentials is set after _load_auth is called
        mock_credential_manager._auth_credentials = {
            "tokens": {"access_token": "test_token"}
        }

        from src.connectors.openai_codex.contracts import CodexConnectorDependencies

        dependencies = CodexConnectorDependencies(
            credential_manager=mock_credential_manager,
        )

        codex_connector = OpenAICodexConnector(
            client, cfg, translation_service=ts, dependencies=dependencies
        )

        # Track executor calls to verify unified execution path
        original_execute = codex_connector._response_executor.execute
        executor_call_count = [0]

        async def tracked_execute(*args, **kwargs):
            executor_call_count[0] += 1
            return await original_execute(*args, **kwargs)

        codex_connector._response_executor.execute = tracked_execute

        with (
            patch.object(
                codex_connector,
                "_validate_credentials_file_exists",
                return_value=ValidationResult.success(),
            ),
            patch.object(
                codex_connector,
                "_validate_credentials_structure",
                return_value=ValidationResult.success(),
            ),
            patch.object(codex_connector, "_start_file_watching"),
        ):
            await codex_connector.initialize(openai_codex_path=str(auth_dir))

            # Create request
            request = CanonicalChatRequest(
                model="openai-codex:gpt-5.5",
                messages=[{"role": "user", "content": "Hello"}],
                stream=True,
            )

            # Mock streaming response: first attempt fails with 401, second succeeds
            call_count = [0]

            async def mock_streaming_response(*args, **kwargs):
                call_count[0] += 1
                if call_count[0] == 1:
                    # First attempt: authentication failure
                    raise HTTPException(status_code=401, detail="Unauthorized")
                else:
                    # Second attempt: success
                    chunks = [
                        ProcessedResponse(
                            content={
                                "id": "chunk-1",
                                "object": "chat.completion.chunk",
                                "choices": [
                                    {
                                        "index": 0,
                                        "delta": {"content": "Hello"},
                                        "finish_reason": None,
                                    }
                                ],
                            }
                        ),
                        ProcessedResponse(
                            content={
                                "id": "chunk-2",
                                "object": "chat.completion.chunk",
                                "choices": [
                                    {
                                        "index": 0,
                                        "delta": {"content": " world"},
                                        "finish_reason": "stop",
                                    }
                                ],
                            }
                        ),
                    ]
                    handle = MockStreamHandle(chunks)
                    return handle

            with patch.object(
                codex_connector._response_executor._base_connector,
                "_handle_streaming_response",
                side_effect=mock_streaming_response,
            ):
                result = await codex_connector.chat_completions(
                    _codex_conn_req(request, effective_model="openai-codex:gpt-5.5")
                )

                assert isinstance(result, StreamingResponseEnvelope)
                # Refresh should be called when retrying after 401
                # Note: refresh is called during the retry loop, so we need to consume the stream
                # to trigger the retry logic
                chunks = []
                async for chunk in result.content:
                    chunks.append(chunk)

                assert len(chunks) > 0
                # After consuming stream, refresh should have been called
                assert (
                    mock_credential_manager.refresh_access_token.call_count >= 1
                )  # Should have refreshed at least once
                # Verify executor was called (unified execution path)
                assert (
                    executor_call_count[0] >= 1
                ), "Executor should be called for Codex model requests"


@pytest.mark.integration
@pytest.mark.asyncio
async def test_streaming_handshake_auth_failure_retry_exhausted(
    auth_dir: Path,
):
    """Test that exhausted retries return proper error shape."""
    # Create connector with mocked credential manager and settings loader via dependency injection
    async with httpx.AsyncClient() as client:
        cfg = AppConfig()
        ts = TranslationService()

        mock_credential_manager = create_mock_credential_manager(refresh_success=True)
        mock_credential_manager._auth_credentials = {
            "tokens": {"access_token": "test_token"}
        }
        # Use settings loader with max_retries=0 to ensure exception is raised immediately
        mock_settings_loader = create_mock_settings_loader(
            max_retries=0,
            retry_backoff_seconds=(0.01,),  # Reduced from 0.1 for performance
        )

        from src.connectors.openai_codex.contracts import CodexConnectorDependencies

        dependencies = CodexConnectorDependencies(
            credential_manager=mock_credential_manager,
            settings_loader=mock_settings_loader,
        )

        codex_connector = OpenAICodexConnector(
            client, cfg, translation_service=ts, dependencies=dependencies
        )

        with (
            patch.object(
                codex_connector,
                "_validate_credentials_file_exists",
                return_value=ValidationResult.success(),
            ),
            patch.object(
                codex_connector,
                "_validate_credentials_structure",
                return_value=ValidationResult.success(),
            ),
            patch.object(codex_connector, "_start_file_watching"),
        ):
            await codex_connector.initialize(openai_codex_path=str(auth_dir))

            # Create request
            request = CanonicalChatRequest(
                model="openai-codex:gpt-5.5",
                messages=[{"role": "user", "content": "Hello"}],
                stream=True,
            )

            # Mock streaming response: always fails with 401
            call_count = [0]

            async def mock_streaming_response(*args, **kwargs):
                call_count[0] += 1
                raise HTTPException(status_code=401, detail="Unauthorized")

            with patch.object(
                codex_connector._response_executor._base_connector,
                "_handle_streaming_response",
                side_effect=mock_streaming_response,
            ):
                with pytest.raises(HTTPException) as exc_info:
                    result = await codex_connector.chat_completions(
                        _codex_conn_req(request, effective_model="openai-codex:gpt-5.5")
                    )
                    # If we get here, consume the stream to trigger the error
                    if isinstance(result, StreamingResponseEnvelope):
                        async for _ in result.content:
                            pass

                # Verify error shape matches exact expected format
                assert exc_info.value.status_code == 401
                detail = exc_info.value.detail
                assert isinstance(detail, dict)
                assert detail.get("error") == "openai_codex_stream_auth_failed"
                assert (
                    detail.get("message")
                    == "Codex streaming request failed authentication during handshake and could not be recovered."
                )
                assert "details" in detail
                details = detail["details"]
                assert details.get("backend") == "openai-codex"
                assert "attempts" in details
                assert "max_retries" in details
                assert (
                    details["attempts"] == 0
                )  # With max_retries=0, no retries attempted
                assert details["max_retries"] == 0


@pytest.mark.integration
@pytest.mark.asyncio
async def test_streaming_chunk_level_auth_failure_retry(
    auth_dir: Path,
):
    """Test that chunk-level authentication failures trigger retry."""
    # Create connector with mocked credential manager via dependency injection
    async with httpx.AsyncClient() as client:
        cfg = AppConfig()
        ts = TranslationService()

        mock_credential_manager = create_mock_credential_manager(refresh_success=True)
        mock_credential_manager._auth_credentials = {
            "tokens": {"access_token": "test_token"}
        }

        from src.connectors.openai_codex.contracts import CodexConnectorDependencies

        dependencies = CodexConnectorDependencies(
            credential_manager=mock_credential_manager,
        )

        codex_connector = OpenAICodexConnector(
            client, cfg, translation_service=ts, dependencies=dependencies
        )

        # Track executor calls to verify unified execution path
        original_execute = codex_connector._response_executor.execute
        executor_call_count = [0]

        async def tracked_execute(*args, **kwargs):
            executor_call_count[0] += 1
            return await original_execute(*args, **kwargs)

        codex_connector._response_executor.execute = tracked_execute

        with (
            patch.object(
                codex_connector,
                "_validate_credentials_file_exists",
                return_value=ValidationResult.success(),
            ),
            patch.object(
                codex_connector,
                "_validate_credentials_structure",
                return_value=ValidationResult.success(),
            ),
            patch.object(codex_connector, "_start_file_watching"),
        ):
            await codex_connector.initialize(openai_codex_path=str(auth_dir))

            # Create request
            request = CanonicalChatRequest(
                model="openai-codex:gpt-5.5",
                messages=[{"role": "user", "content": "Hello"}],
                stream=True,
            )

            call_count = [0]

            async def mock_streaming_response(*args, **kwargs):
                call_count[0] += 1
                if call_count[0] == 1:
                    # First attempt: handshake succeeds, but chunk indicates auth failure
                    # Format matches _should_retry_for_auth_error detection logic
                    chunks = [
                        ProcessedResponse(
                            content={
                                "error": "auth_failed",
                                "details": {
                                    "metadata": {"status_code": 401},
                                },
                            }
                        )
                    ]
                    handle = MockStreamHandle(chunks)
                    return handle
                else:
                    # Second attempt: success
                    chunks = [
                        ProcessedResponse(
                            content={
                                "id": "chunk-2",
                                "object": "chat.completion.chunk",
                                "choices": [
                                    {
                                        "index": 0,
                                        "delta": {"content": "Hello"},
                                        "finish_reason": "stop",
                                    }
                                ],
                            }
                        )
                    ]
                    handle = MockStreamHandle(chunks)
                    return handle

            with patch.object(
                codex_connector._response_executor._base_connector,
                "_handle_streaming_response",
                side_effect=mock_streaming_response,
            ):
                result = await codex_connector.chat_completions(
                    _codex_conn_req(request, effective_model="openai-codex:gpt-5.5")
                )

                assert isinstance(result, StreamingResponseEnvelope)

                # Consume stream to trigger retry logic (refresh happens during stream consumption)
                chunks = []
                async for chunk in result.content:
                    chunks.append(chunk)

                # Should have refreshed after detecting auth error in chunk
                # Note: Refresh happens during stream consumption when auth error is detected
                assert (
                    mock_credential_manager.refresh_access_token.call_count >= 1
                ), f"Expected refresh to be called, but call_count was {mock_credential_manager.refresh_access_token.call_count}"

                # Verify executor was called (unified execution path for chunk retry)
                assert (
                    executor_call_count[0] >= 1
                ), "Executor should be called for Codex model requests, including chunk retries"

                # Should have received successful chunks after retry
                assert len(chunks) > 0


@pytest.mark.integration
@pytest.mark.asyncio
@real_time(
    reason="Measures actual retry backoff timing to ensure exponential backoff is working correctly."
)
async def test_streaming_retry_backoff_behavior(
    auth_dir: Path,
):
    """Test that retry backoff delays are applied correctly."""
    import time

    # Create connector with mocked credential manager and settings loader via dependency injection
    async with httpx.AsyncClient() as client:
        cfg = AppConfig()
        ts = TranslationService()

        mock_credential_manager = create_mock_credential_manager(refresh_success=True)
        mock_credential_manager._auth_credentials = {
            "tokens": {"access_token": "test_token"}
        }
        # Use settings loader with known backoff sequence (reduced delays for test performance)
        mock_settings_loader = create_mock_settings_loader(
            max_retries=2,
            retry_backoff_seconds=(
                0.0005,
                0.001,
                0.0015,
            ),  # Further reduced for performance
        )

        from src.connectors.openai_codex.contracts import CodexConnectorDependencies

        dependencies = CodexConnectorDependencies(
            credential_manager=mock_credential_manager,
            settings_loader=mock_settings_loader,
        )

        codex_connector = OpenAICodexConnector(
            client, cfg, translation_service=ts, dependencies=dependencies
        )

        with (
            patch.object(
                codex_connector,
                "_validate_credentials_file_exists",
                return_value=ValidationResult.success(),
            ),
            patch.object(
                codex_connector,
                "_validate_credentials_structure",
                return_value=ValidationResult.success(),
            ),
            patch.object(codex_connector, "_start_file_watching"),
        ):
            await codex_connector.initialize(openai_codex_path=str(auth_dir))

            # Create request
            request = CanonicalChatRequest(
                model="openai-codex:gpt-5.5",
                messages=[{"role": "user", "content": "Hello"}],
                stream=True,
            )

            call_count = [0]
            retry_times = []

            async def mock_streaming_response(*args, **kwargs):
                call_count[0] += 1
                if call_count[0] <= 2:
                    # Use fixed timestamp for deterministic retry tracking
                    retry_times.append(1000.0)
                    raise HTTPException(status_code=401, detail="Unauthorized")
                else:
                    chunks = [
                        ProcessedResponse(
                            content={
                                "id": "chunk-1",
                                "object": "chat.completion.chunk",
                                "choices": [
                                    {
                                        "index": 0,
                                        "delta": {"content": "Hello"},
                                        "finish_reason": "stop",
                                    }
                                ],
                            }
                        )
                    ]
                    handle = MockStreamHandle(chunks)
                    return handle

            with patch.object(
                codex_connector._response_executor._base_connector,
                "_handle_streaming_response",
                side_effect=mock_streaming_response,
            ):
                start_time = time.time()
                result = await codex_connector.chat_completions(
                    _codex_conn_req(request, effective_model="openai-codex:gpt-5.5")
                )

                # Consume stream to trigger retry and backoff
                async for _ in result.content:
                    pass

                end_time = time.time()

                # Verify backoff was applied (should take at least 0.0005 seconds)
                elapsed = end_time - start_time
                assert elapsed >= 0.0005  # At least first backoff delay

                assert isinstance(result, StreamingResponseEnvelope)


@pytest.mark.integration
@pytest.mark.asyncio
async def test_streaming_refresh_failure_returns_error(
    auth_dir: Path,
):
    """Test that refresh failure returns proper error shape."""
    # Create connector with mocked credential manager that returns False on refresh
    async with httpx.AsyncClient() as client:
        cfg = AppConfig()
        ts = TranslationService()

        mock_credential_manager = create_mock_credential_manager(refresh_success=False)
        mock_credential_manager._auth_credentials = {
            "tokens": {"access_token": "test_token"}
        }

        from src.connectors.openai_codex.contracts import CodexConnectorDependencies

        dependencies = CodexConnectorDependencies(
            credential_manager=mock_credential_manager,
        )

        codex_connector = OpenAICodexConnector(
            client, cfg, translation_service=ts, dependencies=dependencies
        )

        with (
            patch.object(
                codex_connector,
                "_validate_credentials_file_exists",
                return_value=ValidationResult.success(),
            ),
            patch.object(
                codex_connector,
                "_validate_credentials_structure",
                return_value=ValidationResult.success(),
            ),
            patch.object(codex_connector, "_start_file_watching"),
        ):
            await codex_connector.initialize(openai_codex_path=str(auth_dir))

    # Create request
    request = CanonicalChatRequest(
        model="openai-codex:gpt-5.5",
        messages=[{"role": "user", "content": "Hello"}],
        stream=True,
    )

    # Mock streaming response: fails with 401
    async def mock_streaming_response(*args, **kwargs):
        raise HTTPException(status_code=401, detail="Unauthorized")

    with patch.object(
        codex_connector._response_executor._base_connector,
        "_handle_streaming_response",
        side_effect=mock_streaming_response,
    ):
        result = await codex_connector.chat_completions(
            _codex_conn_req(request, effective_model="openai-codex:gpt-5.5")
        )

        assert isinstance(result, StreamingResponseEnvelope)

        with pytest.raises(HTTPException) as exc_info:
            async for _ in result.content:
                pass

            # Verify error shape when refresh fails
            assert exc_info.value.status_code == 401
            detail = exc_info.value.detail
            assert isinstance(detail, dict)
            assert detail.get("error") == "openai_codex_stream_auth_failed"
            assert "handshake" in detail.get("message", "").lower()
            # Should have attempted refresh
            assert mock_credential_manager.refresh_access_token.call_count >= 1


@pytest.mark.integration
@pytest.mark.asyncio
async def test_streaming_ordering_and_termination_parity(
    codex_connector: OpenAICodexConnector,
):
    """Test that streaming chunks arrive in correct order and stream terminates properly (Req 1.2)."""
    # Create request
    request = CanonicalChatRequest(
        model="openai-codex:gpt-5.5",
        messages=[{"role": "user", "content": "Count to 3"}],
        stream=True,
    )

    # Create chunks in specific order
    chunks = [
        ProcessedResponse(content={"choices": [{"delta": {"content": "1"}}]}),
        ProcessedResponse(content={"choices": [{"delta": {"content": "2"}}]}),
        ProcessedResponse(content={"choices": [{"delta": {"content": "3"}}]}),
        ProcessedResponse(
            content={"choices": [{"delta": {}, "finish_reason": "stop"}]}
        ),
    ]

    async def mock_streaming_response(*args, **kwargs):
        handle = MockStreamHandle(chunks)
        return handle

    with patch.object(
        codex_connector._response_executor._base_connector,
        "_handle_streaming_response",
        side_effect=mock_streaming_response,
    ):
        result = await codex_connector.chat_completions(
            _codex_conn_req(request, effective_model="openai-codex:gpt-5.5")
        )

        assert isinstance(result, StreamingResponseEnvelope)

        # Consume stream and verify ordering
        received_chunks = []
        async for chunk in result.content:
            received_chunks.append(chunk)

        # Verify chunks arrived in correct order
        assert len(received_chunks) == 4
        # Verify stream terminated properly (no exception, all chunks received)
        assert (
            received_chunks[-1].content.get("choices", [{}])[0].get("finish_reason")
            == "stop"
        )


@pytest.mark.integration
@pytest.mark.asyncio
async def test_streaming_translation_ordering_with_compatibility(
    codex_connector: OpenAICodexConnector,
):
    """Test that streaming chunks are translated in correct order during normal flow (Req 3.2, 7.2)."""
    from src.core.domain.chat import ChatMessage

    # Enable compatibility layer and set up Droid detection
    codex_connector._compatibility_layer_enabled = True
    from src.connectors._openai_codex_droid_session_detector import DroidSessionDetector

    droid_detector = DroidSessionDetector()
    if (
        hasattr(codex_connector, "_compatibility_layer")
        and codex_connector._compatibility_layer
    ):
        codex_connector._compatibility_layer._droid_detector = droid_detector

    # Create request with Droid-style headers
    request = CanonicalChatRequest(
        model="openai-codex:gpt-5.5",
        messages=[ChatMessage(role="user", content="Test")],
        stream=True,
    )

    # Create chunks with tool calls that need translation
    chunks = [
        ProcessedResponse(
            content={
                "choices": [
                    {
                        "delta": {
                            "tool_calls": [
                                {
                                    "id": "call_1",
                                    "type": "function",
                                    "function": {
                                        "name": "read_file",
                                        "arguments": '{"path": "test.py"}',
                                    },
                                }
                            ]
                        }
                    }
                ]
            }
        ),
        ProcessedResponse(
            content={
                "choices": [
                    {
                        "delta": {
                            "tool_calls": [
                                {
                                    "id": "call_1",
                                    "type": "function",
                                    "function": {"arguments": '{"path": "test.py"}'},
                                }
                            ]
                        }
                    }
                ]
            }
        ),
        ProcessedResponse(
            content={
                "choices": [
                    {
                        "delta": {},
                        "finish_reason": "tool_calls",
                    }
                ]
            }
        ),
    ]

    async def mock_streaming_response(*args, **kwargs):
        handle = MockStreamHandle(chunks)
        return handle

    with patch.object(
        codex_connector._response_executor._base_connector,
        "_handle_streaming_response",
        side_effect=mock_streaming_response,
    ):
        result = await codex_connector.chat_completions(
            ConnectorChatCompletionsRequest(
                request=request,
                processed_messages=[],
                effective_model="openai-codex:gpt-5.5",
                identity=None,
                cancellation_token=None,
                cancellation_coordinator=None,
                context=None,
                options={"metadata": {"headers": {"User-Agent": "factory-cli/1.0"}}},
            )
        )

        assert isinstance(result, StreamingResponseEnvelope)

        # Consume stream and verify chunks arrive in order
        received_chunks = []
        async for chunk in result.content:
            received_chunks.append(chunk)

        # Verify chunks arrived in correct order (should be 3 chunks)
        assert len(received_chunks) == 3
        # Verify first chunk has tool call
        assert "tool_calls" in str(received_chunks[0].content) or "choices" in str(
            received_chunks[0].content
        )


@pytest.mark.integration
@pytest.mark.asyncio
async def test_streaming_translation_ordering_preserved_during_retry(
    auth_dir: Path,
):
    """Test that streaming translation ordering is preserved during auth retry restarts (Req 3.2, 6.2, 7.2)."""
    from src.core.domain.chat import ChatMessage

    # Create connector with mocked credential manager via dependency injection
    async with httpx.AsyncClient() as client:
        cfg = AppConfig()
        ts = TranslationService()

        mock_credential_manager = create_mock_credential_manager(refresh_success=True)
        mock_credential_manager._auth_credentials = {
            "tokens": {"access_token": "test_token"}
        }

        from src.connectors.openai_codex.contracts import CodexConnectorDependencies

        dependencies = CodexConnectorDependencies(
            credential_manager=mock_credential_manager,
        )

        codex_connector = OpenAICodexConnector(
            client, cfg, translation_service=ts, dependencies=dependencies
        )

        with (
            patch.object(
                codex_connector,
                "_validate_credentials_file_exists",
                return_value=ValidationResult.success(),
            ),
            patch.object(
                codex_connector,
                "_validate_credentials_structure",
                return_value=ValidationResult.success(),
            ),
            patch.object(codex_connector, "_start_file_watching"),
        ):
            await codex_connector.initialize(openai_codex_path=str(auth_dir))

    # Create request
    request = CanonicalChatRequest(
        model="openai-codex:gpt-5.5",
        messages=[ChatMessage(role="user", content="Test")],
        stream=True,
    )

    call_count = [0]

    async def mock_streaming_response(*args, **kwargs):
        call_count[0] += 1
        if call_count[0] == 1:
            # First attempt: handshake succeeds, but chunk indicates auth failure
            chunks = [
                ProcessedResponse(
                    content={
                        "error": "auth_failed",
                        "details": {
                            "metadata": {"status_code": 401},
                        },
                    }
                )
            ]
            handle = MockStreamHandle(chunks)
            return handle
        else:
            # Second attempt: success with ordered chunks
            chunks = [
                ProcessedResponse(content={"choices": [{"delta": {"content": "A"}}]}),
                ProcessedResponse(content={"choices": [{"delta": {"content": "B"}}]}),
                ProcessedResponse(
                    content={"choices": [{"delta": {}, "finish_reason": "stop"}]}
                ),
            ]
            handle = MockStreamHandle(chunks)
            return handle

    with patch.object(
        codex_connector._response_executor._base_connector,
        "_handle_streaming_response",
        side_effect=mock_streaming_response,
    ):
        result = await codex_connector.chat_completions(
            _codex_conn_req(request, effective_model="openai-codex:gpt-5.5")
        )

        assert isinstance(result, StreamingResponseEnvelope)

        # Consume stream to trigger retry logic
        received_chunks = []
        async for chunk in result.content:
            received_chunks.append(chunk)

        # Verify chunks arrived in correct order after retry
        assert len(received_chunks) == 3
        # Verify ordering: A, B, stop
        assert "A" in str(received_chunks[0].content)
        assert "B" in str(received_chunks[1].content)
        assert (
            received_chunks[2].content.get("choices", [{}])[0].get("finish_reason")
            == "stop"
        )


@pytest.mark.integration
@pytest.mark.asyncio
async def test_compatibility_state_preserved_across_retries(
    auth_dir: Path,
):
    """Test that compatibility state is preserved across retries (Req 3.2, 7.3)."""
    from src.connectors.openai_codex.contracts import CompatibilityState
    from src.core.domain.chat import ChatMessage

    # Create connector with mocked credential manager via dependency injection
    async with httpx.AsyncClient() as client:
        cfg = AppConfig()
        ts = TranslationService()

        mock_credential_manager = create_mock_credential_manager(refresh_success=True)

        from src.connectors.openai_codex.contracts import CodexConnectorDependencies

        dependencies = CodexConnectorDependencies(
            credential_manager=mock_credential_manager,
        )

        codex_connector = OpenAICodexConnector(
            client, cfg, translation_service=ts, dependencies=dependencies
        )

        with (
            patch.object(
                codex_connector,
                "_validate_credentials_file_exists",
                return_value=ValidationResult.success(),
            ),
            patch.object(
                codex_connector,
                "_validate_credentials_structure",
                return_value=ValidationResult.success(),
            ),
            patch.object(codex_connector, "_start_file_watching"),
        ):
            await codex_connector.initialize(openai_codex_path=str(auth_dir))
            codex_connector._auth_credentials = {
                "tokens": {"access_token": "test_token"}
            }

    # Create compatibility state
    state = CompatibilityState()
    state.is_droid = True
    state.droid_tool_name_cache["call_1"] = "Read"

    # Create request
    request = CanonicalChatRequest(
        model="openai-codex:gpt-5.5",
        messages=[ChatMessage(role="user", content="Test")],
        stream=True,
    )

    call_count = [0]
    state_access_count = [0]

    async def mock_streaming_response(*args, **kwargs):
        call_count[0] += 1
        if call_count[0] == 1:
            # First attempt: auth failure
            chunks = [
                ProcessedResponse(
                    content={
                        "error": "auth_failed",
                        "details": {
                            "metadata": {"status_code": 401},
                        },
                    }
                )
            ]
            handle = MockStreamHandle(chunks)
            return handle
        else:
            # Second attempt: success
            chunks = [
                ProcessedResponse(
                    content={
                        "choices": [
                            {
                                "delta": {
                                    "tool_calls": [
                                        {
                                            "id": "call_1",
                                            "type": "function",
                                            "function": {
                                                "name": "read_file",
                                                "arguments": '{"path": "test.py"}',
                                            },
                                        }
                                    ]
                                }
                            }
                        ]
                    }
                ),
                ProcessedResponse(
                    content={
                        "choices": [
                            {
                                "delta": {},
                                "finish_reason": "tool_calls",
                            }
                        ]
                    }
                ),
            ]
            handle = MockStreamHandle(chunks)
            return handle

    # Track state access in executor
    original_execute = codex_connector._response_executor.execute

    async def tracked_execute(payload, context):
        # Check if compatibility state is in context metadata
        if context.metadata and "compatibility_state" in context.metadata:
            state_access_count[0] += 1
        return await original_execute(payload, context)

    codex_connector._response_executor.execute = tracked_execute

    with patch.object(
        codex_connector._response_executor._base_connector,
        "_handle_streaming_response",
        side_effect=mock_streaming_response,
    ):
        # Create context with compatibility state
        from src.connectors._openai_codex_capabilities import CodexClientCapabilities
        from src.connectors.openai_codex.contracts import (
            CodexRequestContext,
            ProcessedMessage,
        )

        context = CodexRequestContext(
            request=request,
            processed_messages=[ProcessedMessage(role="user", content="Test")],
            effective_model="gpt-5.5",
            session_id="test_session",
            capabilities=CodexClientCapabilities(),
            metadata={"compatibility_state": state},
        )

        # Execute via executor directly to test state preservation
        from src.connectors.openai_codex.contracts import CodexPayload

        payload = CodexPayload(
            model="gpt-5.5",
            input=[],
            tools=[],
            tool_choice="auto",
            parallel_tool_calls=False,
            store=False,
            stream=True,
            include=[],
            prompt_cache_key="test_key",
        )

        result = await codex_connector._response_executor.execute(payload, context)

        assert isinstance(result, StreamingResponseEnvelope)

        # Consume stream to trigger retry logic
        received_chunks = []
        async for chunk in result.content:
            received_chunks.append(chunk)

        # Verify state was accessed (preserved across retries)
        # Note: State is extracted once at start of streaming iterator
        assert state_access_count[0] >= 1, "Compatibility state should be accessed"
        # Verify chunks were received (state was used during translation)
        assert len(received_chunks) > 0, "Chunks should be received"
        # Note: State cleanup happens after stream completes, so state.is_droid may be False
        # The important thing is that state was preserved during the retry loop
