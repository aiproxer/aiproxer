from unittest.mock import AsyncMock, MagicMock

import pytest
from src.core.common.exceptions import BackendError
from src.core.domain.backend_request_manager.context_models import (
    ResponseProcessingContext,
)
from src.core.domain.chat import ChatRequest
from src.core.domain.request_context import RequestContext
from src.core.domain.responses import StreamingResponseEnvelope
from src.core.interfaces.loop_detector_interface import ILoopDetector
from src.core.interfaces.response_processor_interface import ProcessedResponse
from src.core.services.backend_request_manager.streaming_response_handler import (
    BackendStreamingResponseHandler,
)


async def async_chunk_iterator(chunks):
    for chunk in chunks:
        yield chunk


@pytest.mark.asyncio
async def test_regression_400_bypasses_empty_stream_retry():
    """
    Validates that a 400 BackendError raised before meaningful output
    bypasses the empty stream retry logic and surfaces immediately as a 400 error chunk.
    """
    # 1. Setup handler dependencies
    mock_response_processor = AsyncMock()
    mock_backend_processor = AsyncMock()
    mock_loop_detector_factory = MagicMock()
    mock_loop_detector = MagicMock(spec=ILoopDetector)
    mock_loop_detector.process_chunk.return_value = None
    mock_loop_detector_factory.create.return_value = mock_loop_detector
    mock_quality_verifier = AsyncMock()

    async def passthrough_stream(request, stream, context, **kwargs):
        async for chunk in stream:
            yield chunk

    mock_quality_verifier.verify_or_passthrough = passthrough_stream

    handler = BackendStreamingResponseHandler(
        response_processor=mock_response_processor,
        backend_processor=mock_backend_processor,
        loop_detector_factory=mock_loop_detector_factory,
        quality_verifier_stream_verifier=mock_quality_verifier,
        tool_call_retry_coordinator=AsyncMock(),
        cancellation_coordinator=AsyncMock(),
    )

    # 2. Create contexts
    base_request = ChatRequest(
        messages=[{"role": "user", "content": "test"}], model="test"
    )
    request_context = RequestContext(
        headers={},
        cookies={},
        session_id="test-session-123",
        state=None,
        app_state=None,
    )
    processing_context = ResponseProcessingContext(
        session_id="test-session-123", backend_name="openai", model_name="gpt-4"
    )

    # 3. Create a failing stream that raises a 400 BackendError
    async def failing_stream():
        raise BackendError(
            message="tool_choice is invalid", backend_name="openai", status_code=400
        )
        yield ProcessedResponse(content="", metadata={})

    envelope = StreamingResponseEnvelope(content=failing_stream())

    # 4. Handle the stream
    result = await handler.handle(
        stream=envelope,
        request=base_request,
        context=request_context,
        processing_context=processing_context,
    )

    # 5. Consume the stream
    streamed_chunks = []
    async for chunk in result.content:
        streamed_chunks.append(chunk)

    # 6. Verify expectations
    # It should NOT have called process_backend_request (no retry!)
    mock_backend_processor.process_backend_request.assert_not_called()

    # The effective status code should be 400
    assert result.status_code == 400

    # The chunk should be an error chunk with 400
    assert len(streamed_chunks) == 1
    assert "tool_choice is invalid" in str(streamed_chunks[0].content)
    assert streamed_chunks[0].metadata["error"]["status_code"] == 400
