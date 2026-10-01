from __future__ import annotations

from src.core.domain.translation import Translation
from src.core.domain.translators.registry import TranslatorRegistry
from src.core.domain.translators.responses_translator import ResponsesTranslator
from src.core.services.translation_service import TranslationService


def test_reasoning_item_does_not_become_an_empty_assistant_choice() -> None:
    service = TranslationService()
    response = service.to_domain_response(
        {
            "id": "resp_reasoning_then_answer",
            "model": "any-model",
            "status": "completed",
            "output": [
                {"id": "rs_1", "type": "reasoning", "summary": []},
                {
                    "id": "msg_1",
                    "type": "message",
                    "role": "assistant",
                    "status": "completed",
                    "content": [{"type": "output_text", "text": "PROOF"}],
                },
            ],
            "usage": {"input_tokens": 17, "output_tokens": 5, "total_tokens": 22},
        },
        "responses",
    )
    assert len(response.choices) == 1
    assert response.choices[0].message.content == "PROOF"
    assert response.usage is not None
    assert response.usage.prompt_tokens == 17


def test_responses_translator_format_names() -> None:
    translator = ResponsesTranslator()
    assert "responses" in set(translator.format_names)
    assert "openai-responses" in set(translator.format_names)


def test_responses_translator_registry_alias_routes_openai_responses() -> None:
    registry = TranslatorRegistry()
    translator = ResponsesTranslator()
    registry.register(translator)

    assert registry.get("responses") is translator
    assert registry.get("openai-responses") is translator


def test_responses_translator_to_domain_request_matches_translation_facade() -> None:
    payload = {
        "model": "gpt-4o-mini",
        "instructions": "You are helpful.",
        "input": "Hello",
        "response_format": {
            "type": "json_schema",
            "json_schema": {
                "name": "greeting",
                "schema": {
                    "type": "object",
                    "properties": {"answer": {"type": "string"}},
                    "required": ["answer"],
                },
                "strict": True,
            },
        },
        "max_output_tokens": 64,
        "temperature": 0.2,
        "top_p": 0.9,
        "store": True,
        "include": ["output_text"],
        "reasoning": {"effort": "high"},
        "text": {"verbosity": "low"},
        "stream_options": {"include_obfuscation": False},
    }

    translator = ResponsesTranslator()
    expected = Translation.responses_to_domain_request(payload).model_dump()
    actual = translator.to_domain_request(payload).model_dump()
    assert actual == expected


def test_responses_translator_to_domain_response_matches_translation_facade() -> None:
    payload = {
        "id": "resp_123",
        "object": "response",
        "created": 1700000000,
        "model": "gpt-4o-mini",
        "output": [
            {
                "type": "message",
                "role": "assistant",
                "status": "completed",
                "content": [
                    {"type": "output_text", "text": "Hello."},
                    {
                        "type": "tool_call",
                        "id": "call_1",
                        "function": {"name": "lookup", "arguments": {"q": "x"}},
                    },
                    {"type": "reasoning", "text": "Think."},
                ],
            }
        ],
        "usage": {"prompt_tokens": 8, "completion_tokens": 5, "total_tokens": 13},
        "system_fingerprint": "fp_abc",
    }

    translator = ResponsesTranslator()
    expected = Translation.responses_to_domain_response(payload).model_dump()
    actual = translator.to_domain_response(payload).model_dump()
    assert actual == expected


def test_responses_translator_to_domain_stream_chunk_matches_translation_facade() -> (
    None
):
    sse_chunk = (
        "event: response.output_text.delta\n"
        'data: {"type":"response.output_text.delta","delta":"hi","response":{"id":"resp_123","created":1700000000,"model":"gpt-4o-mini"}}\n\n'
    )

    translator = ResponsesTranslator()
    expected = Translation.responses_to_domain_stream_chunk(sse_chunk)
    actual = translator.to_domain_stream_chunk(sse_chunk)
    assert actual == expected


def test_responses_translator_from_domain_request_matches_translation_service() -> None:
    payload = {
        "model": "gpt-4o-mini",
        "messages": [{"role": "user", "content": "Hello"}],
        "extra_body": {
            "response_format": {
                "type": "json_schema",
                "json_schema": {
                    "name": "greeting",
                    "schema": {
                        "type": "object",
                        "properties": {"x": {"type": "string"}},
                    },
                    "strict": True,
                },
            },
            "store": True,
            "include": ["output_text"],
        },
    }
    canonical = Translation.openai_to_domain_request(payload)

    translator = ResponsesTranslator()
    service = TranslationService()
    expected = service.from_domain_to_responses_request(canonical)
    actual = translator.from_domain_request(canonical)
    assert actual == expected


def test_responses_translator_from_domain_response_matches_translation_service() -> (
    None
):
    payload = {
        "id": "resp_123",
        "object": "response",
        "created": 1700000000,
        "model": "gpt-4o-mini",
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": '{"answer":"hi"}'},
                "finish_reason": "stop",
            }
        ],
        "usage": {"prompt_tokens": 8, "completion_tokens": 5, "total_tokens": 13},
    }
    canonical = Translation.responses_to_domain_response(payload)

    translator = ResponsesTranslator()
    service = TranslationService()
    expected = service.from_domain_to_responses_response(canonical)
    actual = translator.from_domain_response(canonical)
    assert actual == expected
