from __future__ import annotations

from typing import Any, cast

from src.core.domain.base_translator import BaseTranslator
from src.core.domain.chat import (
    CanonicalChatRequest,
    CanonicalChatResponse,
    CanonicalStreamChunk,
    ChatResponse,
    ToolCall,
)
from src.core.domain.translation_utils import (
    json_utils,
    media_utils,
    tool_utils,
    usage_utils,
)
from src.core.domain.translation_utils.content_utils import (
    safe_string as safe_string_value,
)
from src.core.domain.translation_utils.gemini_schema_utils import (
    sanitize_gemini_parameters as _sanitize_gemini_parameters_impl,
)
from src.core.domain.translation_utils.schema_validation import (
    basic_schema_validation,
    validate_json_against_schema,
)
from src.core.domain.translation_utils.structured_output import (
    attempt_json_repair,
    enhance_structured_output_response,
    extract_and_repair_json,
    iter_json_candidates,
)
from src.core.domain.translation_utils.tool_call_state import (
    assign_tool_call_index,
    cache_function_name,
    get_cached_function_name,
    reset_tool_call_state,
)
from src.core.domain.translation_utils.tool_call_state import (
    codex_function_name_cache as _tool_call_function_name_cache,
)
from src.core.domain.translation_utils.tool_call_state import (
    codex_tool_call_index_base as _tool_call_index_base,
)
from src.core.domain.translation_utils.tool_call_state import (
    codex_tool_call_item_index as _tool_call_item_index,
)
from src.core.domain.translators.defaults import (
    ensure_default_translator_factories_registered,
)
from src.core.domain.translators.registry import (
    TranslatorRegistry,
    get_global_translator_registry,
)

_MAX_SANITIZE_DEPTH = 100


class Translation(BaseTranslator):
    """Backward-compatible translation facade.

    The previous implementation bundled all translation logic in this class. This refactor
    keeps the public API stable while delegating format-specific work to dedicated
    translators registered in a TranslatorRegistry.
    """

    _codex_tool_call_index_base = _tool_call_index_base
    _codex_tool_call_item_index = _tool_call_item_index
    _codex_function_name_cache = _tool_call_function_name_cache

    @classmethod
    def _registry(cls) -> TranslatorRegistry:
        registry = get_global_translator_registry()
        ensure_default_translator_factories_registered(registry)
        return registry

    @classmethod
    def _translator(cls, format_name: str) -> Any:
        return cls._registry().get(format_name)

    @classmethod
    def _reset_tool_call_state(cls, response_id: str | None) -> None:
        reset_tool_call_state(response_id)

    @classmethod
    def _cache_function_name(cls, call_id: str, name: str) -> None:
        cache_function_name(call_id, name)

    @classmethod
    def _get_cached_function_name(cls, call_id: str) -> str:
        return get_cached_function_name(call_id)

    @classmethod
    def _assign_tool_call_index(
        cls,
        response_id: str | None,
        output_index: Any,
        item_id: str | None,
    ) -> int:
        return assign_tool_call_index(response_id, output_index, item_id)

    @staticmethod
    def validate_json_against_schema(
        json_data: dict[str, Any], schema: dict[str, Any]
    ) -> tuple[bool, str | None]:
        return validate_json_against_schema(json_data, schema)

    @staticmethod
    def _basic_schema_validation(
        json_data: dict[str, Any], schema: dict[str, Any]
    ) -> tuple[bool, str | None]:
        return basic_schema_validation(json_data, schema)

    @staticmethod
    def _normalize_responses_input_to_messages(
        input_payload: Any,
    ) -> list[dict[str, Any]]:
        return Translation.normalize_responses_input_to_messages(input_payload)

    @staticmethod
    def normalize_responses_input_to_messages(
        input_payload: Any,
    ) -> list[dict[str, Any]]:
        from src.core.domain.translators.responses.request import (
            normalize_responses_input_to_messages,
        )

        return normalize_responses_input_to_messages(input_payload)

    @staticmethod
    def _normalize_responses_content(content: Any) -> Any:
        return Translation.normalize_responses_content(content)

    @staticmethod
    def normalize_responses_content(content: Any) -> Any:
        from src.core.domain.translators.responses.request import (
            normalize_responses_content,
        )

        return normalize_responses_content(content)

    @staticmethod
    def _normalize_responses_content_part(
        content: dict[str, Any],
    ) -> list[dict[str, Any]]:
        return Translation.normalize_responses_content_part(content)

    @staticmethod
    def normalize_responses_content_part(
        content: dict[str, Any],
    ) -> list[dict[str, Any]]:
        from src.core.domain.translators.responses.request import (
            normalize_responses_content_part,
        )

        return normalize_responses_content_part(content)

    @staticmethod
    def _normalize_tool_arguments(args: Any) -> str:
        return Translation.normalize_tool_arguments(args)

    @staticmethod
    def normalize_tool_arguments(args: Any) -> str:
        return tool_utils.normalize_tool_arguments(args)

    @staticmethod
    def _is_json_serializable(
        value: Any,
        *,
        max_depth: int,
        _depth: int = 0,
        _seen: set[int] | None = None,
    ) -> bool:
        return Translation.is_json_serializable(value, max_depth=max_depth)

    @staticmethod
    def is_json_serializable(
        value: Any,
        *,
        max_depth: int,
        _depth: int = 0,
        _seen: set[int] | None = None,
    ) -> bool:
        return json_utils.is_json_serializable(
            value,
            max_depth=max_depth,
        )

    @staticmethod
    def _sanitize_dict_for_json(
        data: dict[str, Any],
        *,
        max_depth: int = _MAX_SANITIZE_DEPTH,
        _depth: int = 0,
        _seen: set[int] | None = None,
    ) -> dict[str, Any]:
        return Translation.sanitize_dict_for_json(data, max_depth=max_depth)

    @staticmethod
    def sanitize_dict_for_json(
        data: dict[str, Any],
        *,
        max_depth: int = _MAX_SANITIZE_DEPTH,
        _depth: int = 0,
        _seen: set[int] | None = None,
    ) -> dict[str, Any]:
        return json_utils.sanitize_dict_for_json(
            data,
            max_depth=max_depth,
        )

    @staticmethod
    def _sanitize_list_for_json(
        data: list[Any],
        *,
        max_depth: int = _MAX_SANITIZE_DEPTH,
        _depth: int = 0,
        _seen: set[int] | None = None,
    ) -> list[Any]:
        return Translation.sanitize_list_for_json(data, max_depth=max_depth)

    @staticmethod
    def sanitize_list_for_json(
        data: list[Any],
        *,
        max_depth: int = _MAX_SANITIZE_DEPTH,
        _depth: int = 0,
        _seen: set[int] | None = None,
    ) -> list[Any]:
        return json_utils.sanitize_list_for_json(
            data,
            max_depth=max_depth,
        )

    @staticmethod
    def _process_gemini_function_call(
        function_call: dict[str, Any], part: dict[str, Any] | None = None
    ) -> ToolCall:
        return Translation.process_gemini_function_call(function_call, part=part)

    @staticmethod
    def process_gemini_function_call(
        function_call: dict[str, Any], part: dict[str, Any] | None = None
    ) -> ToolCall:
        return tool_utils.process_gemini_function_call(function_call, part=part)

    @staticmethod
    def _safe_string(value: Any) -> str:
        return Translation.safe_string(value)

    @staticmethod
    def safe_string(value: Any) -> str:
        return safe_string_value(value)

    @staticmethod
    def _map_gemini_finish_reason(finish_reason: str | None) -> str | None:
        return Translation.map_gemini_finish_reason(finish_reason)

    @staticmethod
    def map_gemini_finish_reason(finish_reason: str | None) -> str | None:
        if finish_reason is None:
            return None

        normalized = str(finish_reason).lower()
        mapping = {
            "stop": "stop",
            "max_tokens": "length",
            "safety": "content_filter",
            "tool_calls": "tool_calls",
        }
        return mapping.get(normalized, "stop")

    @staticmethod
    def _normalize_stop_sequences(stop: Any) -> list[str] | None:
        return Translation.normalize_stop_sequences(stop)

    @staticmethod
    def normalize_stop_sequences(stop: Any) -> list[str] | None:
        if stop is None:
            return None
        if isinstance(stop, str):
            return [stop]
        if isinstance(stop, list):
            return [str(s) for s in stop]
        return [str(stop)]

    @staticmethod
    def gemini_to_domain_request(request: Any) -> CanonicalChatRequest:
        return cast(
            CanonicalChatRequest,
            Translation._translator("gemini").to_domain_request(request),
        )

    @staticmethod
    def anthropic_to_domain_request(request: Any) -> CanonicalChatRequest:
        return cast(
            CanonicalChatRequest,
            Translation._translator("anthropic").to_domain_request(request),
        )

    @staticmethod
    def anthropic_to_domain_response(response: Any) -> CanonicalChatResponse:
        return cast(
            CanonicalChatResponse,
            Translation._translator("anthropic").to_domain_response(response),
        )

    @staticmethod
    def gemini_to_domain_response(response: Any) -> CanonicalChatResponse:
        return cast(
            CanonicalChatResponse,
            Translation._translator("gemini").to_domain_response(response),
        )

    @staticmethod
    def gemini_to_domain_stream_chunk(
        chunk: Any,
    ) -> dict[str, Any] | CanonicalStreamChunk:
        translator = Translation._translator("gemini")
        return cast(
            dict[str, Any] | CanonicalStreamChunk,
            translator.to_domain_stream_chunk(chunk),
        )

    @staticmethod
    def openai_to_domain_request(request: Any) -> CanonicalChatRequest:
        return cast(
            CanonicalChatRequest,
            Translation._translator("openai").to_domain_request(request),
        )

    @staticmethod
    def openai_to_domain_response(response: Any) -> CanonicalChatResponse:
        return cast(
            CanonicalChatResponse,
            Translation._translator("openai").to_domain_response(response),
        )

    @staticmethod
    def responses_to_domain_request(request: Any) -> CanonicalChatRequest:
        return cast(
            CanonicalChatRequest,
            Translation._translator("responses").to_domain_request(request),
        )

    @staticmethod
    def responses_to_domain_response(response: Any) -> CanonicalChatResponse:
        return cast(
            CanonicalChatResponse,
            Translation._translator("responses").to_domain_response(response),
        )

    @staticmethod
    def openai_to_domain_stream_chunk(
        chunk: Any,
    ) -> dict[str, Any] | CanonicalStreamChunk:
        translator = Translation._translator("openai")
        return cast(
            dict[str, Any] | CanonicalStreamChunk,
            translator.to_domain_stream_chunk(chunk),
        )

    @staticmethod
    def responses_to_domain_stream_chunk(chunk: Any) -> dict[str, Any]:
        translator = Translation._translator("responses")
        return cast(dict[str, Any], translator.to_domain_stream_chunk(chunk))

    @staticmethod
    def openrouter_to_domain_request(request: Any) -> CanonicalChatRequest:
        return cast(
            CanonicalChatRequest,
            Translation._translator("openrouter").to_domain_request(request),
        )

    @staticmethod
    def _validate_request_parameters(request: CanonicalChatRequest) -> None:
        Translation.validate_request_parameters(request)

    @staticmethod
    def validate_request_parameters(request: CanonicalChatRequest) -> None:
        from src.core.domain.translators.gemini.request import (
            validate_request_parameters,
        )

        validate_request_parameters(request)

    @staticmethod
    def from_domain_to_gemini_request(request: CanonicalChatRequest) -> dict[str, Any]:
        return cast(
            dict[str, Any],
            Translation._translator("gemini").from_domain_request(request),
        )

    @staticmethod
    def _sanitize_gemini_parameters(schema: dict[str, Any]) -> dict[str, Any]:
        return Translation.sanitize_gemini_parameters(schema)

    @staticmethod
    def sanitize_gemini_parameters(schema: dict[str, Any]) -> dict[str, Any]:
        return _sanitize_gemini_parameters_impl(schema)

    @staticmethod
    def from_domain_to_openai_request(request: CanonicalChatRequest) -> dict[str, Any]:
        return cast(
            dict[str, Any],
            Translation._translator("openai").from_domain_request(request),
        )

    @staticmethod
    def anthropic_to_domain_stream_chunk(chunk: Any) -> dict[str, Any]:
        translator = Translation._translator("anthropic")
        return cast(dict[str, Any], translator.to_domain_stream_chunk(chunk))

    @staticmethod
    def from_domain_to_anthropic_request(
        request: CanonicalChatRequest,
    ) -> dict[str, Any]:
        return cast(
            dict[str, Any],
            Translation._translator("anthropic").from_domain_request(request),
        )

    @staticmethod
    def code_assist_to_domain_request(request: Any) -> CanonicalChatRequest:
        return cast(
            CanonicalChatRequest,
            Translation._translator("code_assist").to_domain_request(request),
        )

    @staticmethod
    def code_assist_to_domain_response(response: Any) -> CanonicalChatResponse:
        return cast(
            CanonicalChatResponse,
            Translation._translator("code_assist").to_domain_response(response),
        )

    @staticmethod
    def code_assist_to_domain_stream_chunk(chunk: Any) -> dict[str, Any]:
        translator = Translation._translator("code_assist")
        return cast(dict[str, Any], translator.to_domain_stream_chunk(chunk))

    @staticmethod
    def raw_text_to_domain_request(request: Any) -> CanonicalChatRequest:
        return cast(
            CanonicalChatRequest,
            Translation._translator("raw_text").to_domain_request(request),
        )

    @staticmethod
    def raw_text_to_domain_response(response: Any) -> CanonicalChatResponse:
        return cast(
            CanonicalChatResponse,
            Translation._translator("raw_text").to_domain_response(response),
        )

    @staticmethod
    def raw_text_to_domain_stream_chunk(
        chunk: Any,
    ) -> dict[str, Any] | CanonicalStreamChunk:
        translator = Translation._translator("raw_text")
        return cast(
            dict[str, Any] | CanonicalStreamChunk,
            translator.to_domain_stream_chunk(chunk),
        )

    @staticmethod
    def from_domain_to_responses_response(response: ChatResponse) -> dict[str, Any]:
        return cast(
            dict[str, Any],
            Translation._translator("responses").from_domain_response(response),
        )

    @staticmethod
    def from_domain_to_responses_request(
        request: CanonicalChatRequest,
    ) -> dict[str, Any]:
        return cast(
            dict[str, Any],
            Translation._translator("responses").from_domain_request(request),
        )

    @staticmethod
    def _filter_responses_extra_body(extra_body: dict[str, Any]) -> dict[str, Any]:
        return Translation.filter_responses_extra_body(extra_body)

    @staticmethod
    def filter_responses_extra_body(extra_body: dict[str, Any]) -> dict[str, Any]:
        from src.core.domain.translators.responses.request import (
            filter_responses_extra_body,
        )

        return filter_responses_extra_body(extra_body)

    @staticmethod
    def _normalize_usage_metadata(
        usage: dict[str, Any], source_format: str
    ) -> dict[str, Any]:
        return Translation.normalize_usage_metadata(usage, source_format)

    @staticmethod
    def normalize_usage_metadata(
        usage: dict[str, Any], source_format: str
    ) -> dict[str, Any]:
        return usage_utils.normalize_usage_metadata(usage, source_format)

    @staticmethod
    def _detect_image_mime_type(url: str) -> str:
        return Translation.detect_image_mime_type(url)

    @staticmethod
    def detect_image_mime_type(url: str) -> str:
        return media_utils.detect_image_mime_type(url)

    @staticmethod
    def _process_gemini_image_part(part: Any) -> dict[str, Any] | None:
        return Translation.process_gemini_image_part(part)

    @staticmethod
    def process_gemini_image_part(part: Any) -> dict[str, Any] | None:
        return media_utils.process_gemini_image_part(part)

    @staticmethod
    def enhance_structured_output_response(
        response: ChatResponse,
        original_request_extra_body: dict[str, Any] | None = None,
    ) -> ChatResponse:
        return enhance_structured_output_response(
            response, original_request_extra_body=original_request_extra_body
        )

    @staticmethod
    def _attempt_json_repair(
        json_data: dict[str, Any], schema: dict[str, Any], error_msg: str | None
    ) -> dict[str, Any] | None:
        return attempt_json_repair(json_data, schema, error_msg)

    @staticmethod
    def _iter_json_candidates(
        content: str,
        *,
        max_candidates: int = 20,
        max_object_size: int = 512 * 1024,
    ) -> list[str]:
        return iter_json_candidates(
            content,
            max_candidates=max_candidates,
            max_object_size=max_object_size,
        )

    @staticmethod
    def _extract_and_repair_json(content: str, schema: dict[str, Any]) -> str | None:
        return extract_and_repair_json(content, schema)


__all__ = ["Translation"]
