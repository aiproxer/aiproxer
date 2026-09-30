"""Unit tests for ChatGPT-plan SIWC preview field/capability contract (task 4.3).

Covers forced upstream store=false and stream=true, HTTP previous_response_id
omission, soft-drop of unsupported SIWC scalar fields (explicit and incidental),
and the official preview field matrix fetched 2026-09-30. Does not implement
HTTP/headers.
"""

from __future__ import annotations

from typing import Any

import pytest
from src.connectors.contracts import ConnectorResponsesRequest
from src.core.common.exceptions import ResponsesProviderLimitationError
from src.core.domain.chat import CanonicalChatRequest, ChatMessage

from tests.unit.connectors.test_openai_chatgpt_plan_request_policy import (
    REQUEST_POLICY_PATH,
    _project,
    _responses_request,
)

PROVIDER = "openai-chatgpt-plan"

# Official SIWC preview unsupported fields (2026-09-30). This constant is the
# test-side source of truth; it must not be derived from
# ChatGPTPlanRequestPolicy._UNSUPPORTED_SIWC_FIELDS.
# https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations
OFFICIAL_UNSUPPORTED_SIWC_FIELDS = frozenset(
    {
        "background",
        "conversation",
        "max_output_tokens",
        "max_tool_calls",
        "metadata",
        "moderation",
        "multi_agent",
        "prompt",
        "prompt_cache_retention",
        "safety_identifier",
        "temperature",
        "top_logprobs",
        "top_p",
        "truncation",
        "user",
    }
)

EXPLICIT_FIELD_VALUES: dict[str, Any] = {
    "background": True,
    "conversation": "conv_preview_1",
    "max_output_tokens": 256,
    "max_tool_calls": 3,
    "metadata": {"trace": "client-owned"},
    "moderation": "auto",
    "multi_agent": True,
    "prompt": {"id": "pmpt_preview_1"},
    "prompt_cache_retention": "24h",
    "safety_identifier": "stable-client-id",
    "temperature": 0.7,
    "top_logprobs": 5,
    "top_p": 0.9,
    "truncation": "auto",
    "user": "alice",
}

FORBIDDEN_PRIVATE_ENDPOINT_MARKERS = (
    "chatgpt.com/backend-api",
    "backend-api/codex",
    "chatgpt.com/backend-api/codex",
)

USER_INPUT = [
    {
        "type": "message",
        "role": "user",
        "content": [{"type": "input_text", "text": "Hello."}],
    }
]


def _generic(**overrides: Any) -> dict[str, Any]:
    payload: dict[str, Any] = {
        "model": "gpt-4o",
        "input": list(USER_INPUT),
    }
    payload.update(overrides)
    return payload


def _request(**kwargs: Any) -> ConnectorResponsesRequest:
    canonical = CanonicalChatRequest(
        model="gpt-4o",
        messages=[ChatMessage(role="user", content="What is the status?")],
        **kwargs,
    )
    return ConnectorResponsesRequest(
        request=canonical,
        processed_messages=list(canonical.messages),
        effective_model="gpt-4o",
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=None,
    )


class TestForcedStoreAndStream:
    def test_project_sets_store_false_and_stream_true_when_omitted(self) -> None:
        projected = _project(_generic(), _responses_request(stream=False))
        assert projected.payload["store"] is False
        assert projected.payload["stream"] is True
        assert projected.downstream_stream_requested is False

    def test_downstream_stream_false_keeps_flag_false_and_upstream_stream_true(
        self,
    ) -> None:
        projected = _project(
            _generic(stream=False),
            _responses_request(stream=False),
        )
        assert projected.downstream_stream_requested is False
        assert projected.payload["stream"] is True
        assert projected.payload["store"] is False

    def test_explicit_store_true_on_canonical_request_raises_limitation(self) -> None:
        with pytest.raises(ResponsesProviderLimitationError) as exc_info:
            _project(_generic(), _request(store=True))
        error = exc_info.value
        assert error.feature == "store"
        assert error.provider == PROVIDER
        assert error.details["feature"] == "store"
        assert error.details["provider"] == PROVIDER
        assert "store" in str(error)

    def test_explicit_store_true_in_extra_body_raises_limitation(self) -> None:
        with pytest.raises(ResponsesProviderLimitationError) as exc_info:
            _project(_generic(), _request(extra_body={"store": True}))
        assert exc_info.value.feature == "store"
        assert exc_info.value.provider == PROVIDER

    def test_payload_store_true_without_explicit_client_request_is_forced_false(
        self,
    ) -> None:
        projected = _project(_generic(store=True), _request(store=None))
        assert projected.payload["store"] is False
        assert projected.payload["stream"] is True

    def test_canonical_store_false_is_not_rejected(self) -> None:
        projected = _project(_generic(), _request(store=False, stream=False))
        assert projected.payload["store"] is False
        assert projected.payload["stream"] is True
        assert projected.downstream_stream_requested is False


class TestPreviousResponseIdAlwaysOmitted:
    def test_previous_response_id_stripped_even_when_present_in_generic_payload(
        self,
    ) -> None:
        replayed_input = [
            {
                "type": "message",
                "role": "user",
                "content": [{"type": "input_text", "text": "Earlier turn."}],
            },
            {
                "type": "message",
                "role": "assistant",
                "content": [{"type": "output_text", "text": "Earlier reply."}],
            },
            {
                "type": "message",
                "role": "user",
                "content": [{"type": "input_text", "text": "Continue."}],
            },
        ]
        generic = _generic(
            previous_response_id="resp_replayed_history",
            input=replayed_input,
        )
        projected = _project(generic)
        assert "previous_response_id" not in projected.payload
        assert projected.payload["input"] == replayed_input
        assert projected.payload["store"] is False
        assert projected.payload["stream"] is True


class TestTemperatureSoftDrop:
    def test_explicit_temperature_on_canonical_request_is_soft_dropped(self) -> None:
        projected = _project(_generic(temperature=0.4), _request(temperature=0.4))
        assert "temperature" not in projected.payload
        assert projected.payload["store"] is False
        assert projected.payload["stream"] is True

    def test_incidental_payload_temperature_none_is_stripped(self) -> None:
        projected = _project(_generic(temperature=None), _request(temperature=None))
        assert "temperature" not in projected.payload
        assert projected.payload["store"] is False
        assert projected.payload["stream"] is True

    def test_payload_temperature_stripped_when_request_temperature_is_none(
        self,
    ) -> None:
        projected = _project(_generic(temperature=0.7), _request(temperature=None))
        assert "temperature" not in projected.payload


class TestMetadataSoftDrop:
    def test_explicit_request_metadata_is_soft_dropped(self) -> None:
        projected = _project(
            _generic(metadata={"trace": "client"}),
            _request(request_metadata={"trace": "client"}),
        )
        assert "metadata" not in projected.payload
        assert "request_metadata" not in projected.payload

    def test_explicit_extra_body_metadata_is_soft_dropped(self) -> None:
        projected = _project(
            _generic(metadata={"trace": "native"}),
            _request(extra_body={"metadata": {"trace": "native"}}),
        )
        assert "metadata" not in projected.payload

    def test_incidental_payload_metadata_none_is_stripped(self) -> None:
        projected = _project(_generic(metadata=None))
        assert "metadata" not in projected.payload

    def test_payload_metadata_stripped_when_not_requested_on_canonical_or_extra_body(
        self,
    ) -> None:
        projected = _project(
            _generic(metadata={"proxy": "default"}),
            _request(request_metadata=None, extra_body=None),
        )
        assert "metadata" not in projected.payload


class TestCanonicalAliasesSoftDropped:
    def test_max_tokens_succeeds_without_max_output_tokens_upstream(self) -> None:
        projected = _project(
            _generic(max_output_tokens=128, max_tokens=128),
            _request(max_tokens=128),
        )
        assert "max_output_tokens" not in projected.payload
        assert "max_tokens" not in projected.payload
        assert projected.payload["store"] is False
        assert projected.payload["stream"] is True

    def test_max_completion_tokens_succeeds_without_max_output_tokens_upstream(
        self,
    ) -> None:
        projected = _project(
            _generic(max_output_tokens=64, max_completion_tokens=64),
            _request(max_completion_tokens=64),
        )
        assert "max_output_tokens" not in projected.payload
        assert "max_completion_tokens" not in projected.payload

    def test_explicit_top_p_and_user_are_soft_dropped(self) -> None:
        projected_top_p = _project(_generic(top_p=0.2), _request(top_p=0.2))
        assert "top_p" not in projected_top_p.payload
        projected_user = _project(_generic(user="alice"), _request(user="alice"))
        assert "user" not in projected_user.payload


class TestOfficialUnsupportedFieldMatrixPinnedIndependently:
    def test_official_field_names_match_preview_docs_fetched_2026_09_30(self) -> None:
        assert {
            "background",
            "conversation",
            "max_output_tokens",
            "max_tool_calls",
            "metadata",
            "moderation",
            "multi_agent",
            "prompt",
            "prompt_cache_retention",
            "safety_identifier",
            "temperature",
            "top_logprobs",
            "top_p",
            "truncation",
            "user",
        } == OFFICIAL_UNSUPPORTED_SIWC_FIELDS
        assert "previous_response_id" not in OFFICIAL_UNSUPPORTED_SIWC_FIELDS
        assert "store" not in OFFICIAL_UNSUPPORTED_SIWC_FIELDS
        assert "stream" not in OFFICIAL_UNSUPPORTED_SIWC_FIELDS

    def test_production_unsupported_fields_match_official_set(self) -> None:
        from src.connectors.openai_chatgpt_plan.request_policy import (
            _UNSUPPORTED_SIWC_FIELDS,
        )

        extra = _UNSUPPORTED_SIWC_FIELDS - OFFICIAL_UNSUPPORTED_SIWC_FIELDS
        missing = OFFICIAL_UNSUPPORTED_SIWC_FIELDS - _UNSUPPORTED_SIWC_FIELDS
        assert extra == frozenset(), (
            "production soft-drops fields outside the official SIWC preview matrix "
            f"(wrong extras: {sorted(extra)})"
        )
        assert missing == frozenset(), (
            "production omits official SIWC preview unsupported fields "
            f"(missing: {sorted(missing)})"
        )

    @pytest.mark.parametrize("field", sorted(OFFICIAL_UNSUPPORTED_SIWC_FIELDS))
    def test_incidental_null_official_field_is_stripped_from_payload(
        self, field: str
    ) -> None:
        projected = _project(_generic(**{field: None}))
        assert field not in projected.payload
        assert projected.payload["store"] is False
        assert projected.payload["stream"] is True

    @pytest.mark.parametrize(
        "field,value",
        [
            (field, EXPLICIT_FIELD_VALUES[field])
            for field in sorted(EXPLICIT_FIELD_VALUES)
        ],
    )
    def test_payload_value_stripped_when_present(
        self, field: str, value: Any
    ) -> None:
        projected = _project(_generic(**{field: value}))
        assert field not in projected.payload

    @pytest.mark.parametrize(
        "field,value",
        [
            (field, EXPLICIT_FIELD_VALUES[field])
            for field in sorted(EXPLICIT_FIELD_VALUES)
        ],
    )
    def test_explicit_extra_body_official_field_is_soft_dropped(
        self, field: str, value: Any
    ) -> None:
        projected = _project(
            _generic(**{field: value}),
            _request(extra_body={field: value}),
        )
        assert field not in projected.payload
        assert projected.payload["store"] is False
        assert projected.payload["stream"] is True


class TestNoPrivateChatgptWorkaround:
    def test_request_policy_source_has_no_chatgpt_private_endpoint_urls(self) -> None:
        source = REQUEST_POLICY_PATH.read_text(encoding="utf-8")
        lowered = source.lower()
        for marker in FORBIDDEN_PRIVATE_ENDPOINT_MARKERS:
            assert marker not in lowered
        assert "sk-" not in source


class TestNestedAdditionalToolsHostedRejection:
    def test_hosted_tool_in_additional_tools_raises_limitation(self) -> None:
        generic = _generic(
            input=[
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Search files."}],
                    "additional_tools": [
                        {"type": "file_search", "vector_store_ids": ["vs_demo"]}
                    ],
                }
            ]
        )
        with pytest.raises(ResponsesProviderLimitationError) as exc_info:
            _project(generic)
        assert exc_info.value.feature == "file_search"
        assert exc_info.value.provider == PROVIDER
