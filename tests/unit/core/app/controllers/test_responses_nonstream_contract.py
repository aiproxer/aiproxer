"""Shared HTTP Responses contract, independent of connector or model names."""

import json
from typing import Any
from unittest.mock import AsyncMock

import pytest
from src.core.app.controllers.responses_controller import ResponsesController
from src.core.domain.backend_target import BackendTarget
from src.core.domain.responses import ResponseEnvelope
from src.core.domain.responses_api import ResponsesRequest
from src.core.services.translation_service import TranslationService

from tests.unit.core.app.controllers.test_responses_controller_routing_regression import (
    _make_request,
)
from tests.utils.responses_controller_test_deps import (
    build_responses_controller_backend_kwargs,
)


@pytest.mark.asyncio
@pytest.mark.parametrize("backend", ["mock", "opencode-go", "openai-chatgpt-plan"])
@pytest.mark.parametrize("shape", ["choices", "output"])
async def test_http_nonstream_response_has_native_output_and_required_metadata(
    backend: str, shape: str
) -> None:
    kwargs = build_responses_controller_backend_kwargs()
    kwargs["backend_model_resolver"].resolve_target = AsyncMock(
        return_value=BackendTarget(backend=backend, model="test-model", uri_params={})
    )
    content = {
        "id": "resp_contract",
        "object": "response",
        "created": 123,
        "model": "test-model",
        "usage": {"input_tokens": 17, "output_tokens": 5, "total_tokens": 22},
    }
    if shape == "choices":
        content["choices"] = [
            {
                "index": 0,
                "message": {"role": "assistant", "content": "PROOF"},
                "finish_reason": "stop",
            }
        ]
    else:
        content["output"] = [
            {
                "id": "msg_contract",
                "type": "message",
                "role": "assistant",
                "status": "completed",
                "content": [{"type": "output_text", "text": "PROOF"}],
            }
        ]
    processor = AsyncMock()
    processor.process_request.return_value = ResponseEnvelope(content=content)
    controller = ResponsesController(
        processor, translation_service=TranslationService(), **kwargs
    )
    response = await controller.handle_responses_request(
        _make_request(),
        ResponsesRequest.model_validate(
            {"model": f"{backend}:test-model", "input": "probe", "stream": False}
        ),
    )
    payload = json.loads(bytes(response.body))
    assert payload["object"] == "response"
    assert payload["created_at"] == 123
    assert payload["status"] == "completed"
    assert payload["output"][0]["content"] == [
        {"type": "output_text", "text": "PROOF", "annotations": []}
    ]
    assert payload["usage"]["input_tokens"] == 17
    assert payload["usage"]["output_tokens"] == 5


def test_native_output_status_annotations_and_function_calls_are_preserved() -> None:
    payload: dict[str, Any] = {
        "id": "resp_native",
        "object": "response",
        "created_at": 456,
        "status": "incomplete",
        "incomplete_details": {"reason": "max_output_tokens"},
        "model": "any-model",
        "usage": {"input_tokens": 7, "output_tokens": 3, "total_tokens": 10},
        "output": [
            {
                "id": "msg_native",
                "type": "message",
                "content": [
                    {
                        "type": "output_text",
                        "text": "Partial",
                        "annotations": [
                            {
                                "type": "url_citation",
                                "url": "https://example.com",
                                "title": "Example",
                                "start_index": 0,
                                "end_index": 7,
                            }
                        ],
                    }
                ],
            },
            {
                "id": "fc_native",
                "type": "function_call",
                "call_id": "call_native",
                "name": "lookup",
                "arguments": "{}",
                "status": "completed",
            },
        ],
    }
    result = ResponsesController._normalize_nonstream_responses_object(
        payload, responses_model="any-model", envelope=ResponseEnvelope(content=payload)
    )
    assert result == payload
    assert result is not payload
    assert result["output"][0] is not payload["output"][0]
