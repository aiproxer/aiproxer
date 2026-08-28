from unittest.mock import AsyncMock, MagicMock

import pytest
from fastapi import Request
from src.core.app.controllers.responses_controller import ResponsesController
from src.core.domain.responses import StreamingResponseEnvelope
from src.core.interfaces.request_processor_interface import IRequestProcessor
from src.core.interfaces.translation_service_interface import ITranslationService

from tests.utils.responses_controller_test_deps import (
    build_responses_controller_backend_kwargs,
)


@pytest.mark.asyncio
async def test_handle_responses_request_propagates_streaming_headers():
    # Mock dependencies
    request_processor = MagicMock(spec=IRequestProcessor)
    translation_service = MagicMock(spec=ITranslationService)

    # Mock domain request
    domain_request = MagicMock()
    domain_request.model = "gpt-4"
    domain_request.stream = True
    translation_service.to_domain_request.return_value = domain_request

    # Mock streaming response from processor
    async def mock_iter():
        yield MagicMock()

    streaming_envelope = StreamingResponseEnvelope(
        content=mock_iter(), headers={"x-codex-primary-used-percent": "80.0"}
    )
    request_processor.process_request = AsyncMock(return_value=streaming_envelope)

    controller = ResponsesController(
        request_processor,
        translation_service=translation_service,
        **build_responses_controller_backend_kwargs(),
    )

    # Mock FastAPI request
    request = MagicMock(spec=Request)
    request.state = MagicMock()
    request.state.request_id = "test-req"

    # Execute
    response = await controller.handle_responses_request(
        request, {"model": "gpt-4", "stream": True}
    )

    # Verify headers
    assert response.headers["x-codex-primary-used-percent"] == "80.0"
    assert response.headers["content-type"] == "text/event-stream"
